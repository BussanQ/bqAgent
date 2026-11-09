package extagent

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	"bqagent/internal/media"
)

// A legacy Pi command can use JSON mode if its initial read-only RPC probe
// fails. Once a real prompt has been sent it is never replayed.
var errPiRPCUnavailable = errors.New("Pi RPC mode unavailable")

type piFrame struct {
	event piEvent
	err   error
}

type piRPC struct {
	usage     *piContextReader
	write     func(map[string]any) error
	frames    <-chan piFrame
	dialogs   *piDialogs
	sessionID string
}

type piModel struct {
	ID, Provider  string
	ContextWindow int64
}

type piState struct {
	SessionID                 string
	Model                     piModel
	ThinkingLevel             string
	IsStreaming, IsCompacting bool
	PendingMessageCount       int
}

func (s piState) idle() bool {
	return !s.IsStreaming && !s.IsCompacting && s.PendingMessageCount == 0
}

func (m piModel) optionID() string {
	if m.Provider == "unknown" {
		return ""
	}
	return piCatalogModelID(m.Provider, m.ID)
}

// Older idle replies may contain only the session ID. Keep settings that
// were confirmed earlier, but never carry one model's effort to another.
func (s piState) applyOptions(state *SessionState) {
	if s.Model.Provider != "" || s.Model.ID != "" {
		id := s.Model.optionID()
		if id == "" {
			return
		}
		if id != state.Model {
			state.Model, state.Effort = id, ""
		}
	}
	if validateModel(s.ThinkingLevel) == nil {
		state.Effort = s.ThinkingLevel
	}
}

// Pin a read-only probe or a resumed turn to its intended session. Wrapper
// options precede the native program and must stay untouched.
func withoutPiSessionFlags(args []string) []string {
	args = withoutCLIOptions(args, "--session", "--session-id", "--fork")
	start := 0
	if sep := commandWrapperSeparator(args); sep >= 0 {
		start = sep + 2
	}
	tail := slices.DeleteFunc(args[start:], func(arg string) bool {
		flag, _, _ := strings.Cut(arg, "=")
		switch flag {
		case "-c", "--continue", "-r", "--resume", "--no-session":
			return true
		}
		return false
	})
	return args[:start+len(tail)]
}

func piRPCArgs(args []string, sessionID string) []string {
	if sessionID != "" {
		args = withoutPiSessionFlags(args)
	}
	args = append(withoutCLIOptions(args, "--mode"), "--mode", "rpc")
	if sessionID != "" {
		args = append(args, "--session", sessionID)
	}
	return args
}

func runPiRPC(ctx context.Context, spec CommandSpec, state SessionState, request TurnRequest, ask piDialogHandler) (TurnResponse, error) {
	// Allow Pi to flush its native session in response to abort before
	// terminating the process group if it does not settle.
	child, cancel := context.WithCancel(context.WithoutCancel(ctx))
	defer cancel()
	stopWatch := context.AfterFunc(ctx, func() {
		timer := time.NewTimer(1500 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-child.Done():
		case <-timer.C:
			cancel()
		}
	})
	defer stopWatch()
	cmd := exec.CommandContext(child, spec.Command, piRPCArgs(spec.Args, state.ExternalSessionID)...)
	cmd.Dir = request.CWD
	configureExternalProcess(cmd)
	cmd.Stderr = io.Discard
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return TurnResponse{}, err
	}
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return TurnResponse{}, err
	}
	if err = cmd.Start(); err != nil {
		stdout.Close()
		return TurnResponse{}, err
	}
	frames := make(chan piFrame, 1)
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		defer close(frames)
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 65536), maxACPProtocolMessageBytes)
		for scanner.Scan() {
			var event piEvent
			err := json.Unmarshal(scanner.Bytes(), &event)
			select {
			case frames <- piFrame{event, err}:
			case <-child.Done():
				return
			}
			if err != nil {
				return
			}
		}
		if err := scanner.Err(); err != nil {
			select {
			case frames <- piFrame{err: err}:
			case <-child.Done():
			}
		}
	}()
	encoder := json.NewEncoder(stdin)
	var writeMu sync.Mutex
	rpc := piRPC{frames: frames, write: func(frame map[string]any) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return encoder.Encode(frame)
	}}
	dialogCtx, closeDialogs := context.WithCancel(ctx)
	rpc.dialogs = &piDialogs{ctx: dialogCtx, ask: ask, failed: make(chan error, 1)}
	defer func() {
		closeDialogs()
		cancel()
		stdin.Close()
		_ = cmd.Wait()
		<-readDone
		rpc.dialogs.wait.Wait()
	}()
	var out TurnResponse
	if request.Compact != nil {
		out, err = rpc.compact(ctx, state, request.Compact)
	} else {
		out, err = rpc.prompt(ctx, state, request.Prompt, request.Images, request.Steering)
	}
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	return out, err
}

func (rpc piRPC) call(ctx context.Context, command string, params map[string]any) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if params == nil {
		params = make(map[string]any)
	}
	params["id"], params["type"] = "rpc-"+rand.Text(), command
	if err := rpc.write(params); err != nil {
		return nil, err
	}
	for {
		select {
		case err := <-rpc.dialogErrors():
			return nil, err
		case <-ctx.Done():
			return nil, ctx.Err()
		case frame, ok := <-rpc.frames:
			if !ok {
				return nil, io.EOF
			}
			if frame.err != nil {
				return nil, frame.err
			}
			e := frame.event
			if rpc.usage != nil && rpc.usage.id != "" {
				rpc.usage.observe(e)
			}
			if e.Type == "extension_ui_request" {
				if err := rpc.dialog(e); err != nil {
					return nil, err
				}
			}
			if e.ID.text == params["id"] && e.Type == "response" && e.Command == command {
				if !e.Success {
					return nil, piRPCError(e)
				}
				return e.Data, nil
			}
		}
	}
}

type piResponseError struct{ command, message string }

func (e *piResponseError) Error() string { return fmt.Sprintf("Pi %s：%s", e.command, e.message) }

func piRPCError(event piEvent) error {
	return &piResponseError{event.Command, firstNonEmpty(activityOutput(event.Error), "请求被拒绝")}
}

func (rpc piRPC) compact(ctx context.Context, state SessionState, command *CompactCommand) (TurnResponse, error) {
	if sink := contextUsageSink(ctx); sink != nil {
		rpc.usage = &piContextReader{report: sink}
	}
	out := TurnResponse{State: state, Command: "compact"}
	rpc.sessionID = state.ExternalSessionID
	// Check identity and availability without applying any pending model choice.
	if _, err := rpc.setup(ctx, SessionState{ExternalSessionID: state.ExternalSessionID}); err != nil {
		return out, err
	}
	if command.Started != nil {
		command.Started()
	}
	params := map[string]any{}
	if command.Instructions != "" {
		params["customInstructions"] = command.Instructions
	}
	result, err := rpc.call(ctx, "compact", params)
	if ctx.Err() != nil {
		abortCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		_, _ = rpc.call(abortCtx, "abort", nil)
		return out, ctx.Err()
	}
	var rejected *piResponseError
	if errors.As(err, &rejected) && rejected.command == "compact" {
		switch rejected.message {
		case "Nothing to compact (session too small)":
			out.Reply = "Pi 会话内容较少，无需压缩。"
			return out, nil
		case "Already compacted":
			out.Reply = "Pi 会话已经压缩，无需再次处理。"
			return out, nil
		}
	}
	if err != nil {
		return out, err
	}
	if rpc.usage != nil {
		rpc.usage.compacted(result)
		rpc.readContextStats(ctx)
	}
	out.Reply = "Pi 会话上下文已压缩。"
	return out, nil
}

// Select on the opened session. Resuming some Pi versions restores their
// saved model over the launch arguments.
func (rpc piRPC) selectModel(ctx context.Context, current piState, wanted string) (piState, error) {
	provider, model, qualified := strings.Cut(wanted, "/")
	if !qualified {
		provider, model = current.Model.Provider, wanted
	}
	if (piModel{Provider: provider, ID: model}).optionID() == "" {
		return piState{}, fmt.Errorf("Pi 无法确定模型 %q 的供应商或模型 ID", wanted)
	}
	if provider == current.Model.Provider && model == current.Model.ID {
		return current, nil
	}
	data, err := rpc.call(ctx, "set_model", map[string]any{"provider": provider, "modelId": model})
	if err != nil {
		return piState{}, err
	}
	selected := piState{SessionID: current.SessionID}
	if len(data) != 0 && json.Unmarshal(data, &selected.Model) != nil {
		return piState{}, fmt.Errorf("Pi 返回了无效的模型选择结果")
	}
	if selected.Model.ID == "" && selected.Model.Provider == "" {
		// A bare setter acknowledgement needs a readback on the same child.
		data, err = rpc.call(ctx, "get_state", nil)
		if err != nil {
			return piState{}, err
		}
		selected = piState{}
		if json.Unmarshal(data, &selected) != nil || selected.SessionID != current.SessionID || !selected.idle() {
			return piState{}, fmt.Errorf("Pi 未确认原会话的模型选择结果")
		}
	}
	if selected.Model.ID != model || selected.Model.Provider != provider {
		return piState{}, fmt.Errorf("Pi 未确认所选模型 %q", wanted)
	}
	return selected, nil
}

func (rpc piRPC) setup(ctx context.Context, state SessionState) (SessionState, error) {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	data, err := rpc.call(ctx, "get_state", nil)
	var current piState
	if err != nil || json.Unmarshal(data, &current) != nil || current.SessionID == "" {
		return SessionState{}, fmt.Errorf("%w: get_state did not return a native session", errPiRPCUnavailable)
	}
	if state.ExternalSessionID != "" && state.ExternalSessionID != current.SessionID {
		return SessionState{}, fmt.Errorf("Pi 打开了不同的会话，预期 %q，实际 %q", state.ExternalSessionID, current.SessionID)
	}
	if !current.idle() {
		return SessionState{}, fmt.Errorf("Pi 会话仍有任务在运行")
	}
	effort := state.Effort
	if state.Model != "" {
		current, err = rpc.selectModel(ctx, current, state.Model)
		if err != nil {
			return SessionState{}, err
		}
	}
	state.ExternalSessionID = current.SessionID
	state.Model, state.Effort = "", ""
	current.applyOptions(&state)
	if effort != "" && effort != current.ThinkingLevel {
		if _, err := rpc.call(ctx, "set_thinking_level", map[string]any{"level": effort}); err != nil {
			return state, err
		}
		state.Effort = effort
	}
	if rpc.usage != nil {
		rpc.usage.id, rpc.usage.model = current.SessionID, current.Model
	}
	return state, nil
}

func (rpc piRPC) prompt(ctx context.Context, requested SessionState, text string, images []media.Image, hooks *TurnSteering) (TurnResponse, error) {
	if sink := contextUsageSink(ctx); sink != nil {
		rpc.usage = &piContextReader{report: sink}
	}
	rpc.sessionID = requested.ExternalSessionID
	state, err := rpc.setup(ctx, requested)
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		if state.ExternalSessionID != "" && state.ExternalSessionID == requested.ExternalSessionID {
			// A model setter may already have succeeded before the effort failed.
			return TurnResponse{State: state, settingsAcknowledged: state.Model != ""}, err
		}
		return TurnResponse{}, err
	}
	rpc.sessionID = state.ExternalSessionID
	if hooks != nil && hooks.Target != nil {
		hooks.Target(state)
	}
	out := TurnResponse{State: state, settingsAcknowledged: state.Model != "" || state.Effort != ""}
	frame := map[string]any{"id": "initial", "type": "prompt", "message": text}
	addPiImages(frame, images)
	if err := rpc.write(frame); err != nil {
		return out, err
	}
	run := piRPCTurn{rpc: rpc, hooks: hooks, output: piOutput{sessionID: state.ExternalSessionID}, sink: ActivitySinkFromContext(ctx)}
	out.Reply, err = run.run(ctx)
	if run.settledState != nil {
		run.settledState.applyOptions(&out.State)
		out.settingsAcknowledged = out.State.Model != "" || out.State.Effort != ""
		if rpc.usage != nil {
			rpc.usage.settledModel(run.settledState.Model)
		}
	}
	if err == nil {
		rpc.readContextStats(ctx)
	}
	return out, err
}

func addPiImages(frame map[string]any, images []media.Image) {
	if len(images) > 0 {
		blocks := make([]map[string]string, 0, len(images))
		for _, image := range images {
			blocks = append(blocks, map[string]string{"type": "image", "mimeType": image.ContentType(), "data": image.Base64()})
		}
		frame["images"] = blocks
	}
}
