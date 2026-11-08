package extagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"bqagent/internal/media"
)

type CLIAdapter struct{}

func (adapter CLIAdapter) SendTurn(ctx context.Context, spec CommandSpec, state SessionState, cwd, prompt string, images ...media.Image) (TurnResponse, error) {
	return adapter.sendTurn(ctx, spec, state, TurnRequest{CWD: cwd, Prompt: prompt, Images: images}, nil)
}

func (adapter CLIAdapter) sendTurn(ctx context.Context, spec CommandSpec, state SessionState, request TurnRequest, ask piDialogHandler) (TurnResponse, error) {
	cwd, prompt, images := request.CWD, request.Prompt, request.Images
	if state.Model != "" {
		// Apply the override to a copy. Other sessions share the detected spec.
		spec.Args = cliModelArgs(spec.Args, state.Agent, state.Model)
	}
	if state.Effort != "" {
		spec.Args = cliEffortArgs(spec.Args, state.Agent, state.Effort)
	}
	switch state.Agent {
	case AgentClaude:
		return adapter.runClaude(ctx, spec, state, request, ask)
	case AgentCodex:
		return adapter.runCodex(ctx, spec, state, cwd, prompt, images)
	case AgentPi:
		if request.Steering != nil {
			response, err := runPiRPC(ctx, spec, state, request, ask)
			if !errors.Is(err, errPiRPCUnavailable) || ctx.Err() != nil {
				return response, err
			}
		}
		return adapter.runPi(ctx, spec, state, cwd, prompt, images)
	default:
		return TurnResponse{}, fmt.Errorf("cli is not supported for %s", state.Agent)
	}
}

func cliEffortArgs(args []string, agent AgentName, effort string) []string {
	switch agent {
	case AgentPi:
		return append(withoutCLIOptions(args, "--thinking"), "--thinking", effort)
	case AgentClaude:
		return append(withoutCLIOptions(args, "--effort"), "--effort", effort)
	case AgentCodex:
		// Codex uses TOML config values and later -c flags take precedence.
		return append(append([]string(nil), args...), "-c", "model_reasoning_effort="+strconv.Quote(effort))
	default:
		return args
	}
}

func cliModelArgs(args []string, agent AgentName, model string) []string {
	args = withoutCLIOptions(args, "--model", "-m")
	if agent == AgentPi {
		if provider, id, ok := strings.Cut(model, "/"); ok {
			return append(withoutCLIOptions(args, "--provider"), "--provider", provider, "--model", id)
		}
	}
	return append(args, "--model", model)
}

func (adapter CLIAdapter) runClaude(ctx context.Context, spec CommandSpec, state SessionState, request TurnRequest, ask piDialogHandler) (TurnResponse, error) {
	cwd, prompt, images := request.CWD, request.Prompt, request.Images
	compact := request.Compact
	args := append([]string{}, spec.Args...)
	var mode string
	if compact == nil {
		mode = firstNonEmpty(request.InteractionMode, state.InteractionMode)
		if err := validateInteractionMode(mode, AgentClaude, TransportCLI); err != nil {
			return TurnResponse{}, err
		}
	}
	launchMode := claudeLaunchMode(args)
	var host *claudePromptHost
	if ask != nil && request.PermissionSink != nil && compact == nil {
		if interactiveArgs, ok := claudeApprovalArgs(args); ok {
			args = interactiveArgs
			host = newClaudePromptHost(ctx, ask)
			host.mode.launch = launchMode
		}
	}
	stream := ActivitySinkFromContext(ctx) != nil && (hasArg(args, "json") || hasArg(args, "stream-json") || hasArg(args, "--output-format=json") || hasArg(args, "--output-format=stream-json"))
	if compact != nil {
		args = claudeCompactArgs(args)
		prompt = "/compact"
		if compact.Instructions != "" {
			prompt += " " + compact.Instructions
		}
		stream = true
	}
	structuredInput := len(images) > 0 || host != nil || mode != "" || compact != nil ||
		request.Steering != nil && contextUsageSink(ctx) != nil && !hasArg(args, "--no-session-persistence")
	if structuredInput {
		stream = true
	}
	if stream {
		args = append(withoutCLIOptions(args, "--output-format"), "--output-format", "stream-json")
		for _, flag := range []string{"--verbose", "--include-partial-messages"} {
			if !hasArg(args, flag) {
				args = append(args, flag)
			}
		}
	}
	args, err := claudeResumeArgs(ctx, args, state, cwd)
	if err != nil {
		return TurnResponse{}, err
	}
	var stdin io.Reader
	if structuredInput {
		args = append(withoutCLIOptions(args, "--input-format"), "--input-format", "stream-json")
		if !hasArg(args, "-p") && !hasArg(args, "--print") {
			args = append(args, "-p")
		}
		stdin, err = claudeStreamInput(prompt, images)
		if err != nil {
			return TurnResponse{}, err
		}
	} else {
		args = append(args, prompt)
	}
	childCtx := ctx
	var setup *claudeModeSetup
	if mode != "" {
		var stop context.CancelFunc
		childCtx, stop = context.WithCancel(ctx)
		defer stop()
		setup = newClaudeModeSetup(childCtx, mode, stop)
		defer setup.cancel()
	}
	command := exec.CommandContext(childCtx, spec.Command, args...)
	command.Stdin = stdin
	command.Dir = cwd
	configureExternalProcess(command)
	var control *claudeUsageControl
	if report := contextUsageSink(ctx); (report != nil || host != nil || setup != nil) && stdin != nil && stream {
		// The optional probe runs after the root result. Limit its deadline
		// to the CLI process itself, preserving any detached tools it launched.
		if in, c, openErr := openClaudeTurnControl(stdin, report, func() error { return command.Process.Kill() }, host, setup, launchMode); openErr == nil {
			control = c
			command.Stdin = in
			defer in.Close()
		} else if host != nil || setup != nil {
			return TurnResponse{}, openErr
		}
	}
	if stream {
		var reply, sessionID string
		err := runCLIStream(ctx, command, func(r io.Reader) (err error) {
			if compact != nil {
				if compact.Started != nil {
					compact.Started()
				}
				reply, err = readClaudeCompactUsage(r, state.ExternalSessionID, contextUsageSink(ctx), control)
				return err
			}
			sink := ActivitySinkFromContext(ctx)
			if sink == nil {
				sink = func(Activity) {}
			}
			reply, sessionID, err = readClaudeContextStream(r, sink, firstNonEmpty(state.ExternalSessionID, state.ClaudeNewID), contextUsageSink(ctx), control)
			return err
		})
		if control != nil && control.finish() && ctx.Err() == nil && reply != "" {
			err = nil
		}
		if setup != nil {
			<-setup.done
			if setup.err != nil {
				if ctx.Err() != nil {
					return TurnResponse{}, ctx.Err()
				}
				if err != nil && (command.Process == nil || errors.Is(setup.err, context.Canceled)) {
					return TurnResponse{}, err
				}
				return TurnResponse{}, fmt.Errorf("Claude 无法切换交互模式：%w", setup.err)
			}
			state.InteractionMode = control.mode.interaction()
		}
		if compact != nil {
			return TurnResponse{Command: "compact", State: state, Reply: reply}, err
		}
		if err != nil {
			if setup != nil && state.ExternalSessionID != "" && state.ExternalSessionID == sessionID {
				return TurnResponse{State: state}, err
			}
			return TurnResponse{}, err
		}
		if err := acceptClaudeSession(&state, sessionID); err != nil {
			return TurnResponse{}, err
		}
		return TurnResponse{Reply: reply, State: state}, nil
	}
	output, err := command.Output()
	if err != nil {
		return TurnResponse{}, formatCLIError(err)
	}
	reply, sessionID := parseClaudeJSON(output)
	if strings.TrimSpace(reply) == "" {
		reply = strings.TrimSpace(string(output))
	}
	if err := acceptClaudeSession(&state, sessionID); err != nil {
		return TurnResponse{}, err
	}
	return TurnResponse{Reply: reply, State: state}, nil
}

func (adapter CLIAdapter) runCodex(ctx context.Context, spec CommandSpec, state SessionState, cwd, prompt string, images []media.Image) (TurnResponse, error) {
	outputFile, err := os.CreateTemp("", "bqagent-codex-*.txt")
	if err != nil {
		return TurnResponse{}, err
	}
	outputPath := outputFile.Name()
	_ = outputFile.Close()
	defer os.Remove(outputPath)

	args := codexArgs(spec, state.ExternalSessionID)
	paths, cleanup, err := cliImageFiles(images)
	if err != nil {
		return TurnResponse{}, err
	}
	defer cleanup()
	for _, path := range paths {
		args = append(args, "--image", path)
	}
	args = append(args, "--output-last-message", outputPath)
	if len(paths) > 0 {
		args = append(args, "--")
		if prompt == "" {
			prompt = "请查看所附图片。"
		}
	}
	args = append(args, prompt)

	command := exec.CommandContext(ctx, spec.Command, args...)
	command.Dir = cwd
	configureExternalProcess(command)
	var fallback, sessionID string
	var output []byte
	structured := hasArg(args, "--json")
	if sink := ActivitySinkFromContext(ctx); sink != nil && structured {
		err = runCLIStream(ctx, command, func(r io.Reader) (readErr error) {
			fallback, sessionID, readErr = readCodexCLI(r, sink)
			return readErr
		})
	} else {
		output, err = command.Output()
		sessionID = extractSessionIDFromJSONLines(output)
		if !structured {
			fallback = strings.TrimSpace(string(output))
		}
	}
	if err != nil {
		return TurnResponse{}, formatCLIError(err)
	}
	replyContent, _ := os.ReadFile(outputPath)
	reply := strings.TrimSpace(string(replyContent))
	state.ExternalSessionID = firstNonEmpty(sessionID, state.ExternalSessionID)
	if reply == "" && structured && len(output) > 0 {
		// An unobserved JSON run produces the same public answer as a live run.
		// Its stdout also contains tools and summaries, each with its own role.
		fallback, _, err = readCodexCLI(bytes.NewReader(output), nil)
		if err != nil {
			return TurnResponse{State: state}, err
		}
	}
	if reply == "" {
		reply = fallback
	}
	if structured && reply == "" {
		return TurnResponse{State: state}, fmt.Errorf("codex returned an empty final response")
	}
	return TurnResponse{Reply: reply, State: state}, nil
}

func codexArgs(spec CommandSpec, resumeID string) []string {
	if strings.TrimSpace(resumeID) == "" {
		return append([]string{}, spec.Args...)
	}
	var args []string
	flags := spec.Args
	if separator := commandWrapperSeparator(flags); separator >= 0 {
		args = append(args, flags[:separator+2]...)
		flags = flags[separator+2:]
	}
	// Keep global options before exec and keep model overrides on the resumed
	// invocation, including when the configured command wraps the Codex binary.
	for i, flag := range flags {
		if flag == "exec" {
			args = append(args, flags[:i]...)
			flags = flags[i+1:]
			break
		}
	}
	args = append(args, "exec", "resume", strings.TrimSpace(resumeID))
	if len(flags) > 0 && flags[0] == "resume" {
		flags = flags[1:]
	}
	args = append(args, flags...)
	return ensureCodexJSONFlags(args)
}

func commandWrapperSeparator(args []string) int {
	for index, arg := range args {
		if arg == "--" && index+1 < len(args) {
			return index
		}
	}
	return -1
}

func ensureCodexJSONFlags(args []string) []string {
	if !hasArg(args, "--json") {
		args = append(args, "--json")
	}
	if !hasArg(args, "--skip-git-repo-check") {
		args = append(args, "--skip-git-repo-check")
	}
	return args
}

func hasArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func parseClaudeJSON(payload []byte) (reply string, sessionID string) {
	var result struct {
		Result    string `json:"result"`
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(payload, &result); err == nil {
		return strings.TrimSpace(result.Result), strings.TrimSpace(result.SessionID)
	}
	return "", ""
}

func extractSessionIDFromJSONLines(payload []byte) string {
	for _, line := range strings.Split(string(payload), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var decoded any
		if err := json.Unmarshal([]byte(line), &decoded); err != nil {
			continue
		}
		if id := findStringField(decoded, "thread_id", "session_id", "sessionId"); id != "" && looksLikeSessionID(id) {
			return id
		}
	}
	return ""
}

func findStringField(value any, keys ...string) string {
	switch typed := value.(type) {
	case map[string]any:
		for _, key := range keys {
			if text, ok := typed[key].(string); ok && strings.TrimSpace(text) != "" {
				return strings.TrimSpace(text)
			}
		}
		for _, item := range typed {
			if text := findStringField(item, keys...); text != "" {
				return text
			}
		}
	case []any:
		for _, item := range typed {
			if text := findStringField(item, keys...); text != "" {
				return text
			}
		}
	}
	return ""
}

var sessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{6,}$`)

func looksLikeSessionID(value string) bool {
	return sessionIDPattern.MatchString(strings.TrimSpace(value))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func formatCLIError(err error) error {
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return err
	}
	stderr := strings.TrimSpace(string(exitErr.Stderr))
	if stderr == "" {
		return err
	}
	return fmt.Errorf("%w: %s", err, stderr)
}

func logPath(root, name string) string {
	return filepath.Join(root, name)
}

// Configured resume/fork flags must not select a pre-clear session. The native
// adapter appends the new context's own resume ID once it has one.
func scopedCLIArgs(name AgentName, args []string) []string {
	switch name {
	case AgentPi:
		next := withoutPiSessionFlags(args)
		if hasArg(args, "--no-session") {
			next = append(next, "--no-session")
		}
		return next
	case AgentClaude:
		next := claudeCompactArgs(args)
		if hasArg(args, "--no-session-persistence") {
			next = append(next, "--no-session-persistence")
		}
		return next
	case AgentCodex:
		start := 0
		if sep := commandWrapperSeparator(args); sep >= 0 {
			start = sep + 2
		}
		native := args[start:]
		if !slices.Contains(native, "resume") {
			return args
		}
		result := slices.Clone(args[:start])
		var execSeen, resume bool
		for i := 0; i < len(native); i++ {
			arg := native[i]
			switch arg {
			case "-c", "--config", "--profile", "-p", "--enable", "--disable", "--model", "-m", "-C", "--cd", "--add-dir", "-s", "--sandbox", "--color", "--image", "-i", "--output-schema", "--output-last-message", "-o":
				result = append(result, arg)
				if i+1 < len(native) {
					i++
					result = append(result, native[i])
				}
				continue
			case "exec", "e":
				execSeen = true
			case "resume":
				if !execSeen {
					result = append(result, "exec")
					execSeen = true
				}
				resume = true
				continue
			case "--last", "--all":
				if resume {
					continue
				}
			}
			if resume && !strings.HasPrefix(arg, "-") {
				continue
			} // resume ID or old prompt
			result = append(result, arg)
		}
		return result
	}
	return args
}
