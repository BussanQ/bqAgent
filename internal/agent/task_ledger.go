package agent

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"
)

// execEntry is one real tool execution in the current turn. Only the agent's
// own runRegularToolCall creates entries, so a model-written verification
// string can never fabricate one.
type execEntry struct {
	callID  string
	tool    string
	command string
	start   uint64
	end     uint64 // 0 while the call is still running
	success bool
	// mutating entries may have changed the workspace: they invalidate every
	// verification that was not entirely started after they finished.
	mutating bool
	// verifier marks a successful test/build/lint style bash command.
	verifier bool
	// reliable means the command's exit status reflects every step (no ||, ;,
	// background job or un-pipefailed pipeline) and it is not merely a read.
	reliable bool
	// write marks a definite change: a file edit tool or a shell command with an
	// explicit write signal. Unlike mutating it is not a conservative guess, so
	// a read-only command the classifier cannot parse never counts as a change.
	write bool
	// barrier entries are synthetic (new user input, goal change). They
	// invalidate older evidence but are not workspace mutations.
	barrier bool
}

// execLedger records executions with a logical clock. All methods are safe for
// concurrent use by parallel tool goroutines.
type execLedger struct {
	mu      sync.Mutex
	clock   uint64
	entries []*execEntry
}

var mutatingFileTools = map[string]bool{"write_file": true, "edit_file": true}

// begin registers a tool call about to run and returns nil for tools that
// neither verify nor mutate (reads, searches, bookkeeping).
func (l *execLedger) begin(callID, tool string, args map[string]any) *execEntry {
	if l == nil {
		return nil
	}
	entry := &execEntry{callID: callID, tool: tool}
	switch {
	case tool == "bash":
		command, _ := args["command"].(string)
		entry.command = strings.TrimSpace(command)
		kind := classifyBash(entry.command)
		entry.mutating = kind == bashMutating
		entry.write = entry.mutating && explicitShellWrite(entry.command)
		entry.verifier = kind == bashVerify
		entry.reliable = kind != bashReadOnly && reliableExitStatus(entry.command)
	case mutatingFileTools[tool], tool == "consult_group_agent":
		// Assume mutation while running; a failed file tool is demoted in finish.
		entry.mutating = true
		entry.write = mutatingFileTools[tool]
	default:
		return nil
	}
	l.mu.Lock()
	l.clock++
	entry.start = l.clock
	l.entries = append(l.entries, entry)
	l.mu.Unlock()
	return entry
}

func (l *execLedger) finish(entry *execEntry, success bool) {
	if l == nil || entry == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.clock++
	entry.end = l.clock
	entry.success = success
	if !success {
		entry.verifier = false
		// A failed write or shell command may still have partially applied, so
		// a failure never demotes a mutating entry.
	}
}

// barrier invalidates all earlier evidence without counting as a mutation.
// It returns the new clock, which
// callers keep as the baseline for "work since the latest user input".
func (l *execLedger) barrier() uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.clock++
	l.entries = append(l.entries, &execEntry{start: l.clock, end: l.clock, mutating: true, barrier: true})
	return l.clock
}

// verified reports whether a passed verification is backed by a real,
// successful bash call in this turn with the same command (and the same
// call ID when supplied) and no mutation that overlapped or followed it.
func (l *execLedger) verified(callID, command string) bool {
	return l.verificationIssue(callID, command) == ""
}

// verificationIssue returns "" when the verification is backed by evidence,
// otherwise the concrete reason it is not, so the model can fix the actual
// cause (an output-truncating pipe, a paraphrased command, a later edit)
// instead of blindly retrying the same record.
func (l *execLedger) verificationIssue(callID, command string) string {
	return l.verificationIssueFor(callID, command, false)
}

// verificationIssueFor with evidenceOnly (analysis tasks) accepts any exactly
// matching successful bash call, including read-only ones such as
// go list or grep, without the test/build/lint and staleness requirements;
// the caller has already ruled out definite writes this turn.
func (l *execLedger) verificationIssueFor(callID, command string, evidenceOnly bool) string {
	command = strings.TrimSpace(command)
	callID = strings.TrimSpace(callID)
	if command == "" {
		return "the verification command is empty"
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	exact, near := "", ""
	for i := len(l.entries) - 1; i >= 0; i-- {
		e := l.entries[i]
		if e.tool != "bash" || e.end == 0 || (callID != "" && e.callID != callID) {
			continue
		}
		if e.command != command {
			if near == "" && (callID != "" || strings.Contains(e.command, command)) {
				near = fmt.Sprintf("the closest bash call ran %q, which is not the recorded command; record the exact command string", abbreviateCommand(e.command))
				if !evidenceOnly && e.success && !e.reliable {
					near += ", and " + unreliableReason(e.command)
				}
			}
			continue
		}
		var issue string
		switch {
		case !e.success:
			issue = "that bash call failed"
		case evidenceOnly:
			issue = ""
		case !e.reliable:
			issue = unreliableReason(e.command)
		default:
			issue = l.staleReason(e)
		}
		if issue == "" {
			return ""
		}
		if exact == "" {
			exact = issue
		}
	}
	switch {
	case exact != "":
		return exact
	case near != "":
		return near
	case callID != "":
		return fmt.Sprintf("no finished bash call with tool_call_id %q exists in this turn", callID)
	}
	return "no bash call in this turn ran exactly this command"
}

// unreliableReason explains why a successful command does not prove a check passed.
func unreliableReason(command string) string {
	if classifyBash(command) == bashReadOnly {
		return "it is a read-only command, not a test/build/lint check"
	}
	return "its exit status does not prove success: ||, ;, background jobs or a pipeline without set -o pipefail (e.g. `| tail`, `| head`, `| grep`) hide a failing check; rerun the check on its own, or prefix it with `set -o pipefail && `"
}

// staleReason names the mutation that overlapped or followed the evidence
// entry, or returns "" when the evidence is still valid. Callers hold l.mu.
func (l *execLedger) staleReason(e *execEntry) string {
	for _, m := range l.entries {
		if m == e || !m.mutating || (m.end != 0 && m.end <= e.start) {
			continue
		}
		switch {
		case m.barrier:
			return "new user input or a goal change came after that bash call; rerun the check"
		case m.tool == "bash":
			return fmt.Sprintf("the possibly side-effecting command %q ran during or after that bash call; rerun the check after the last change", m.command)
		}
		return fmt.Sprintf("a %s call changed the workspace during or after that bash call; rerun the check after the last change", m.tool)
	}
	return ""
}

// hasMutation reports a workspace mutation that started after the baseline.
func (l *execLedger) hasMutation(since uint64) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range l.entries {
		if e.mutating && !e.barrier && e.start > since {
			return true
		}
	}
	return false
}

// hasDefiniteWrite reports a file edit or explicit shell write in this turn,
// whether or not it succeeded (a failed write may be partially applied).
func (l *execLedger) hasDefiniteWrite() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range l.entries {
		if e.write && !e.barrier {
			return true
		}
	}
	return false
}

var writingWords = map[string]bool{"rm": true, "mv": true, "cp": true, "mkdir": true, "touch": true, "tee": true, "patch": true, "chmod": true, "truncate": true, "ln": true, "install": true}
var writingGit = map[string]bool{"commit": true, "apply": true, "checkout": true, "reset": true, "restore": true, "merge": true, "rebase": true, "add": true, "stash": true, "push": true, "clean": true, "am": true, "cherry-pick": true}

// explicitShellWrite recognises clear file-writing shell commands only.
func explicitShellWrite(command string) bool {
	c, valid := shellControlText(strings.TrimSpace(command))
	if !valid {
		return false // cannot parse: not a definite write
	}
	for _, harmless := range []string{"2>&1", "&>/dev/null", ">/dev/null", "2>/dev/null", "> /dev/null", "2> /dev/null"} {
		c = strings.ReplaceAll(c, harmless, " ")
	}
	if strings.Contains(c, ">") {
		return true
	}
	c = strings.NewReplacer("&&", "\n", "||", "\n", ";", "\n", "|", "\n").Replace(c)
	for _, segment := range strings.Split(c, "\n") {
		words := strings.Fields(segment)
		for len(words) > 0 && strings.Contains(words[0], "=") {
			words = words[1:]
		}
		if len(words) == 0 {
			continue
		}
		name := words[0]
		if writingWords[name] {
			return true
		}
		if name == "git" && len(words) > 1 && writingGit[words[1]] {
			return true
		}
		if name == "sed" || name == "perl" {
			for _, w := range words[1:] {
				if strings.HasPrefix(w, "-i") {
					return true
				}
			}
		}
	}
	return false
}

// now returns the current logical time, used to detect changes after a
// completion record was saved.
func (l *execLedger) now() uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.clock
}

// mutatedSince reports whether a workspace mutation started after the given
// logical time or is still running.
func (l *execLedger) mutatedSince(clock uint64) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range l.entries {
		if e.mutating && !e.barrier && (e.end == 0 || e.end > clock) {
			return true
		}
	}
	return false
}

// workSummary counts successful file edits and successful verifier commands
// since the given clock.
func (l *execLedger) workSummary(since uint64) (edits, verifiers int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range l.entries {
		if e.start <= since {
			continue
		}
		switch {
		case mutatingFileTools[e.tool] && e.success:
			edits++
		case e.verifier && e.success:
			verifiers++
		}
	}
	return edits, verifiers
}

type bashKind int

const (
	bashMutating bashKind = iota
	bashReadOnly
	bashVerify
)

var readOnlyWords = map[string]bool{
	"ls": true, "cat": true, "head": true, "tail": true, "grep": true, "egrep": true, "rg": true,
	"wc": true, "pwd": true, "echo": true, "printf": true, "true": true, "date": true, "which": true,
	"stat": true, "file": true, "diff": true, "cmp": true, "sort": true, "uniq": true, "tree": true,
	"cd": true, "test": true, "[": true,
}

// Flags that make otherwise read-only checkers rewrite files (snapshots, fixes).
var writingFlags = map[string]bool{"-update": true, "--update": true, "-u": true, "--fix": true, "-fix": true, "--write": true, "-w": true, "--update-snapshots": true, "--output": true, "-o": true}

var gitReadOnly = map[string]bool{"status": true, "diff": true, "log": true, "show": true, "rev-parse": true, "ls-files": true, "blame": true, "describe": true}

// classifyBash is deliberately conservative: anything it cannot positively
// recognise (redirects, subshells, background jobs, unknown programs) is
// treated as potentially mutating, which only makes older evidence stale.
// A compound command is verification/read-only only if every segment is.
func classifyBash(command string) bashKind {
	c, valid := shellControlText(strings.TrimSpace(command))
	if !valid || c == "" {
		return bashMutating
	}
	for _, harmless := range []string{"2>&1", "&>/dev/null", ">/dev/null", "2>/dev/null", "> /dev/null", "2> /dev/null"} {
		c = strings.ReplaceAll(c, harmless, " ")
	}
	if strings.ContainsAny(c, ">`") || strings.Contains(c, "$(") || strings.Contains(c, "<(") {
		return bashMutating
	}
	c = strings.NewReplacer("&&", "\n", "||", "\n", ";", "\n", "|", "\n").Replace(c)
	if strings.Contains(c, "&") {
		return bashMutating
	}
	kind := bashReadOnly
	for _, segment := range strings.Split(c, "\n") {
		segment = strings.TrimSpace(segment)
		if segment == "" {
			continue
		}
		switch classifySegment(strings.Fields(segment)) {
		case bashMutating:
			return bashMutating
		case bashVerify:
			kind = bashVerify
		}
	}
	return kind
}

func classifySegment(words []string) bashKind {
	for len(words) > 0 && strings.Contains(words[0], "=") && !strings.HasPrefix(words[0], "-") {
		words = words[1:] // leading VAR=value assignments
	}
	if len(words) > 0 && words[0] == "time" {
		words = words[1:]
	}
	if len(words) == 0 {
		return bashReadOnly
	}
	name, rest := words[0], words[1:]
	has := func(flags ...string) bool {
		for _, word := range rest {
			for _, flag := range flags {
				if word == flag || strings.HasPrefix(word, flag+"=") {
					return true
				}
			}
		}
		return false
	}
	sub := ""
	if len(rest) > 0 {
		sub = rest[0]
	}
	for _, word := range rest {
		if readOnlyWords[name] && word != "-o" && !strings.HasPrefix(word, "--output") {
			continue // e.g. grep -w, sort -u are harmless; sort -o writes
		}
		if writingFlags[word] || strings.HasPrefix(word, "--output=") || strings.HasPrefix(word, "--fix=") || strings.HasPrefix(word, "--update") {
			return bashMutating
		}
	}
	switch name {
	case "git":
		if sub == "diff" && has("--check") {
			return bashVerify
		}
		if gitReadOnly[sub] {
			return bashReadOnly
		}
	case "go":
		switch sub {
		case "test", "vet":
			return bashVerify
		case "build":
			return bashVerify
		case "list", "version", "env", "doc":
			return bashReadOnly
		}
	case "gofmt":
		if has("-l") && !has("-w") {
			return bashReadOnly
		}
	case "cargo":
		switch sub {
		case "test", "check", "build", "clippy", "doc":
			return bashVerify
		}
	case "npm", "pnpm", "yarn", "bun":
		switch sub {
		case "test", "t":
			return bashVerify
		case "run", "run-script":
			if len(rest) > 1 {
				switch rest[1] {
				case "test", "lint", "build", "check", "typecheck":
					return bashVerify
				}
			}
		}
	case "make":
		for _, target := range rest {
			switch target {
			case "test", "check", "lint", "vet", "verify", "build":
			default:
				return bashMutating
			}
		}
		if len(rest) > 0 {
			return bashVerify
		}
	case "pytest", "py.test", "rspec", "jest", "vitest", "ctest":
		return bashVerify
	case "python", "python3":
		if len(rest) >= 2 && rest[0] == "-m" && (rest[1] == "pytest" || rest[1] == "unittest") {
			return bashVerify
		}
	case "mvn", "gradle", "./gradlew", "dotnet":
		if sub == "test" || sub == "verify" || sub == "check" {
			return bashVerify
		}
	case "tsc":
		if has("--noEmit") {
			return bashVerify
		}
	default:
		if readOnlyWords[name] {
			return bashReadOnly
		}
	}
	return bashMutating
}

// shellControlText masks quoted/escaped metacharacters so test regexes and
// literal arguments are not mistaken for shell pipelines or command separators.
// Expansions that can execute nested commands are deliberately unsupported.
func shellControlText(command string) (string, bool) {
	var text strings.Builder
	var quote rune
	escaped := false
	chars := []rune(command)
	for i, ch := range chars {
		if escaped {
			if ch == '\n' {
				text.WriteByte(' ')
			} else if strings.ContainsRune("|;&`<>$", ch) {
				text.WriteByte('x')
			} else {
				text.WriteRune(ch)
			}
			escaped = false
			continue
		}
		if ch == '\\' && quote != '\'' {
			escaped = true
			continue
		}
		if quote != 0 {
			if ch == quote {
				quote = 0
			} else if quote == '"' && (ch == '`' || ch == '$' && i+1 < len(chars) && shellExpansionStart(chars[i+1])) {
				return "", false
			} else if strings.ContainsRune("|;&`<>$\n", ch) {
				text.WriteByte('x')
			} else {
				text.WriteRune(ch)
			}
			continue
		}
		switch ch {
		case '\'', '"':
			quote = ch
		case '$', '`':
			return "", false
		default:
			text.WriteRune(ch)
		}
	}
	return text.String(), quote == 0 && !escaped
}

func shellExpansionStart(ch rune) bool {
	return ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || strings.ContainsRune("({_@*#?-$!", ch)
}

var pipefailSetter = regexp.MustCompile(`(^|[;&\n])\s*set\s+(-[A-Za-z]+\s+)*-o\s+pipefail\s*(;|&&|\n)`)

// reliableExitStatus reports whether a command's exit status reflects all of
// its steps. Under sh -c, `go test || true`, `go test; echo`, `go test | tail`
// and background jobs all hide a failing check, so they cannot be evidence.
// Only plain commands and && chains (or pipelines after set -o pipefail) count.
func reliableExitStatus(command string) bool {
	c, valid := shellControlText(strings.TrimSpace(command))
	if !valid || c == "" {
		return false
	}
	for _, harmless := range []string{"2>&1", "&>/dev/null", ">/dev/null", "2>/dev/null", "> /dev/null", "2> /dev/null", ">&2"} {
		c = strings.ReplaceAll(c, harmless, " ")
	}
	pipefail := pipefailSetter.MatchString(c)
	c = pipefailSetter.ReplaceAllString(c, " ")
	if strings.Contains(c, "||") {
		return false
	}
	c = strings.ReplaceAll(c, "&&", " ")
	if strings.ContainsAny(c, ";&\n") {
		return false
	}
	return !strings.Contains(c, "|") || pipefail
}

// normalizeSpace collapses whitespace for quote comparison.
func normalizeSpace(text string) string { return strings.Join(strings.Fields(text), " ") }

// quoteGrounded checks that quote is a verbatim (whitespace-normalised) piece of
// the latest user message accepted this turn and long enough to carry meaning. This
// grounds a goal replacement in the user's own words; it is a speed bump
// against invented authorization, NOT proof the user authorized the change.
func quoteGrounded(quote, userText string) bool {
	quote, text := normalizeSpace(quote), normalizeSpace(userText)
	if quote == "" || text == "" {
		return false
	}
	if utf8.RuneCountInString(text) < minQuoteRunes {
		return quote == text
	}
	return utf8.RuneCountInString(quote) >= minQuoteRunes && strings.Contains(text, quote)
}

const minQuoteRunes = 6
