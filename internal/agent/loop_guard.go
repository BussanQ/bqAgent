package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

const (
	taskUpdateRecoveryThreshold   = 3
	taskUpdateCheckpointThreshold = 5
	taskUpdateStallRecoveryPrompt = "Repeated task_update calls are not task progress. Continue this same turn now with a substantive tool such as read_file, glob, grep, edit_file or execute_bash that does the actual work or verification. Call task_update again only when a real stage, acceptance result, blocker or scope change must be recorded."
)

type loopGuard struct {
	config           StageConfig
	callCounts       map[string]int
	failureCounts    map[string]int
	argumentFailures map[string]int
	pathFailures     map[string]int
	taskUpdateStall  int
	lastEmptySearch  string
	emptySearchCount int
	progress         *progressTracker
	seenEdits        map[string]bool
}

type loopGuardAction struct {
	recoveryPrompt   string
	checkpointReason string
	stallCheckpoint  bool
}

func (action loopGuardAction) merge(next loopGuardAction) loopGuardAction {
	if action.checkpointReason == "" || (action.stallCheckpoint && next.checkpointReason != "" && !next.stallCheckpoint) {
		action.checkpointReason = next.checkpointReason
		action.stallCheckpoint = next.stallCheckpoint
	}
	if action.recoveryPrompt == "" {
		action.recoveryPrompt = next.recoveryPrompt
	}
	return action
}

func newLoopGuard(config StageConfig) *loopGuard {
	return &loopGuard{config: config, callCounts: map[string]int{}, failureCounts: map[string]int{}, argumentFailures: map[string]int{}, pathFailures: map[string]int{}, progress: newProgressTracker(), seenEdits: map[string]bool{}}
}

// Repeated successful operations within a recovered batch are intentional parts
// of one model response. Count them once for loop detection, but still count
// every failure and repeated requests in later batches.
func (guard *loopGuard) observeRecoveredTool(call ToolCall, result string, successes map[string]bool) loopGuardAction {
	if call.Function.Name == "execute_bash" && !strings.HasPrefix(strings.TrimSpace(result), "Error:") {
		signature := call.Function.Name + "\x00" + strings.TrimSpace(call.Function.Arguments)
		if successes[signature] {
			return loopGuardAction{}
		}
		successes[signature] = true
	}
	return guard.observeTool(call, result)
}

func (guard *loopGuard) observeTool(call ToolCall, result string) loopGuardAction {
	if guard == nil {
		return loopGuardAction{}
	}
	failed := strings.HasPrefix(strings.TrimSpace(result), "Error:")
	// Invalid arguments cannot make progress. Bound retries even in CLI mode,
	// where the configurable stage loop protection may be disabled. Call IDs
	// are intentionally excluded because providers generate a new ID on retry.
	if strings.HasPrefix(result, "Error: Invalid JSON arguments for tool ") || strings.HasPrefix(result, "Error: Tool arguments for ") {
		signature := call.Function.Name + "\x00" + strings.TrimSpace(call.Function.Arguments)
		guard.argumentFailures[signature]++
		if guard.argumentFailures[signature] >= 3 {
			return loopGuardAction{checkpointReason: fmt.Sprintf("loop protection: repeated invalid arguments for tool %s", call.Function.Name)}
		}
	}
	fresh := guard.progress.observe(call, result)
	if guard.progress.edited {
		// A real edit lets previously counted calls (e.g. the same test) run
		// again. This also clears unrelated failure counts; acceptable since
		// edits must themselves be novel to trigger it.
		guard.callCounts = map[string]int{}
		guard.failureCounts = map[string]int{}
		guard.pathFailures = map[string]int{}
	}
	if call.Function.Name == "task_update" {
		// task_update is bookkeeping: rewriting the plan (even with different
		// states) without substantive work in between is not progress.
		guard.taskUpdateStall++
		if guard.taskUpdateStall == taskUpdateRecoveryThreshold {
			return loopGuardAction{recoveryPrompt: taskUpdateStallRecoveryPrompt}
		}
		if guard.taskUpdateStall >= taskUpdateCheckpointThreshold {
			return loopGuardAction{checkpointReason: "loop protection: repeated task_update without substantive work after recovery", stallCheckpoint: true}
		}
	} else if emptySearch := emptySearchSignature(call, result); emptySearch != "" {
		if guard.lastEmptySearch == emptySearch {
			guard.emptySearchCount++
			if guard.emptySearchCount == 2 {
				return loopGuardAction{recoveryPrompt: emptySearchRecoveryPrompt}
			}
			if guard.emptySearchCount >= 3 {
				return loopGuardAction{checkpointReason: fmt.Sprintf("loop protection: repeated empty search in %s after recovery", call.Function.Name)}
			}
		} else {
			guard.lastEmptySearch = emptySearch
			guard.emptySearchCount = 1
		}
	} else if fresh {
		guard.taskUpdateStall = 0
		guard.lastEmptySearch = ""
		guard.emptySearchCount = 0
	}
	if !guard.config.LoopProtection {
		return loopGuardAction{}
	}
	signature := call.Function.Name + "\x00" + canonicalArguments(call.Function.Arguments)
	guard.callCounts[signature]++
	if failed {
		guard.failureCounts[signature]++
		if guard.failureCounts[signature] >= guard.config.RepeatedFailureLimit {
			return loopGuardAction{checkpointReason: fmt.Sprintf("loop protection: repeated failing tool call %s", call.Function.Name)}
		}
		if pathSignature := failedPathSignature(call); pathSignature != "" {
			guard.pathFailures[pathSignature]++
			if guard.pathFailures[pathSignature] >= guard.config.RepeatedFailureLimit {
				return loopGuardAction{checkpointReason: fmt.Sprintf("loop protection: repeated failing path in %s", call.Function.Name)}
			}
		}
	}
	if guard.callCounts[signature] >= guard.config.DuplicateCallLimit {
		return loopGuardAction{checkpointReason: fmt.Sprintf("loop protection: repeated tool call %s", call.Function.Name)}
	}
	return loopGuardAction{}
}

// progressTracker dedups "progress" by fingerprint. A successful call counts
// as new progress only the first time its (tool, arguments, sha256(result))
// is seen since the last reset, so re-reading an unchanged file cannot reset
// stall counters forever. Bookkeeping tools (task_scope,
// task_update) never count. Limit: it is a conservative dedup, not proof of a
// real workspace change.
type progressTracker struct {
	seen   map[string]bool
	edits  map[string]bool
	edited bool // last observe saw a novel successful edit/write
}

func newProgressTracker() *progressTracker {
	return &progressTracker{seen: map[string]bool{}, edits: map[string]bool{}}
}

func (t *progressTracker) reset() { t.seen = map[string]bool{} }

// observe reports whether the call is new substantive progress.
func (t *progressTracker) observe(call ToolCall, result string) bool {
	t.edited = false
	switch call.Function.Name {
	case "task_scope", "task_update":
		return false
	}
	if !toolResultMadeProgress(call, result) {
		return false
	}
	// edit_file fails when old_string is missing or equals new_string, so a
	// success is a real change. write_file output cannot distinguish a no-op,
	// so only canonical (path, content) never seen before counts. Repeating an
	// identical edit/write (A/B/A/B flip-flop) never resets anything.
	if name := call.Function.Name; name == "edit_file" || name == "write_file" {
		key := name + "\x00" + canonicalArguments(call.Function.Arguments)
		if t.edits[key] {
			return false
		}
		t.edits[key] = true
		t.seen = map[string]bool{}
		t.edited = true
		return true
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(result)))
	key := call.Function.Name + "\x00" + canonicalArguments(call.Function.Arguments) + "\x00" + hex.EncodeToString(sum[:])
	if t.seen[key] {
		return false
	}
	t.seen[key] = true
	return true
}

// canonicalArguments parses JSON arguments and re-marshals them (sorted keys,
// no insignificant whitespace) so formatting cannot dodge duplicate detection.
func canonicalArguments(raw string) string {
	trimmed := strings.TrimSpace(raw)
	parsed, err := parseArguments(trimmed)
	if err != nil {
		return trimmed
	}
	encoded, err := json.Marshal(parsed)
	if err != nil {
		return trimmed
	}
	return string(encoded)
}

func toolResultMadeProgress(call ToolCall, result string) bool {
	trimmed := strings.TrimSpace(result)
	// Tool errors are encoded with Error: by runRegularToolCall. Successful
	// commands (especially file edits) need not produce stdout.
	if strings.HasPrefix(trimmed, "Error:") {
		return false
	}
	switch call.Function.Name {
	case "glob":
		return trimmed != "No files matched."
	case "grep":
		return trimmed != "No matches found."
	default:
		return true
	}
}

func emptySearchSignature(call ToolCall, result string) string {
	trimmed := strings.TrimSpace(result)
	switch call.Function.Name {
	case "glob":
		if trimmed == "No files matched." {
			return call.Function.Name + "\x00" + strings.TrimSpace(call.Function.Arguments)
		}
	case "grep":
		if trimmed == "No matches found." {
			return call.Function.Name + "\x00" + strings.TrimSpace(call.Function.Arguments)
		}
	}
	return ""
}

func failedPathSignature(call ToolCall) string {
	parsed, err := parseArguments(call.Function.Arguments)
	if err != nil {
		return ""
	}
	arguments, ok := parsed.(map[string]any)
	if !ok {
		return ""
	}
	path, _ := arguments["path"].(string)
	path = strings.ToLower(filepath.ToSlash(strings.TrimSpace(path)))
	if path == "" {
		return ""
	}
	return call.Function.Name + "\x00" + path
}
