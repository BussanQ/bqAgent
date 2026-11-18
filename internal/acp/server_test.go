package acp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeBackend func(context.Context, string, string) (Conversation, error)

func (f fakeBackend) Open(ctx context.Context, cwd, id string) (Conversation, error) {
	return f(ctx, cwd, id)
}

type fakeConversation struct {
	id       string
	mu       sync.Mutex
	model    string
	closed   atomic.Int32
	prompt   func(context.Context, []ContentBlock, Emit) (string, error)
	replay   func(context.Context, Emit) error
	setModel func(context.Context, string) error
}

func (c *fakeConversation) ID() string { return c.id }
func (c *fakeConversation) Models() ModelState {
	c.mu.Lock()
	defer c.mu.Unlock()
	current := c.model
	if current == "" {
		current = "one"
	}
	return ModelState{CurrentModelID: current, AvailableModels: []Model{
		{ModelID: "one", Name: "One", Description: "First model"},
		{ModelID: "two", Name: "Two"},
	}}
}
func (c *fakeConversation) Replay(ctx context.Context, emit Emit) error {
	if c.replay != nil {
		return c.replay(ctx, emit)
	}
	return nil
}
func (c *fakeConversation) Prompt(ctx context.Context, blocks []ContentBlock, emit Emit) (string, error) {
	if c.prompt != nil {
		return c.prompt(ctx, blocks, emit)
	}
	return "end_turn", nil
}
func (c *fakeConversation) SetModel(ctx context.Context, id string) error {
	if c.setModel != nil {
		return c.setModel(ctx, id)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.model = id
	return nil
}
func (c *fakeConversation) Close() { c.closed.Add(1) }

type frame struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  map[string]any  `json:"params"`
	Result  map[string]any  `json:"result"`
	Error   *rpcError       `json:"error"`
}

type frameWriter struct{ frames chan frame }

func (w frameWriter) Write(p []byte) (int, error) {
	if len(p) == 0 || p[len(p)-1] != '\n' {
		return 0, errors.New("missing JSONL newline")
	}
	var f frame
	if err := json.Unmarshal(p, &f); err != nil {
		return 0, err
	}
	if f.JSONRPC != "2.0" {
		return 0, errors.New("missing JSON-RPC version")
	}
	w.frames <- f
	return len(p), nil
}

type harness struct {
	t      *testing.T
	input  *io.PipeWriter
	frames chan frame
	done   chan error
	cancel context.CancelFunc
}

func newHarness(t *testing.T, backend Backend) *harness {
	t.Helper()
	reader, input := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	h := &harness{t: t, input: input, frames: make(chan frame, 128), done: make(chan error, 1), cancel: cancel}
	go func() { h.done <- Serve(ctx, reader, frameWriter{h.frames}, backend) }()
	t.Cleanup(func() {
		cancel()
		_ = input.Close()
		_ = reader.Close()
		select {
		case <-h.done:
		case <-time.After(2 * time.Second):
			t.Error("Serve did not stop")
		}
	})
	return h
}
func (h *harness) raw(raw string) {
	h.t.Helper()
	if _, err := io.WriteString(h.input, raw+"\n"); err != nil {
		h.t.Fatal(err)
	}
}
func (h *harness) send(id any, method string, params any) {
	h.t.Helper()
	msg := map[string]any{"jsonrpc": "2.0", "method": method, "params": params}
	if id != nil {
		msg["id"] = id
	}
	data, err := json.Marshal(msg)
	if err != nil {
		h.t.Fatal(err)
	}
	h.raw(string(data))
}
func (h *harness) next() frame {
	h.t.Helper()
	select {
	case f := <-h.frames:
		return f
	case <-time.After(2 * time.Second):
		h.t.Fatal("timed out waiting for ACP frame")
	}
	return frame{}
}
func (h *harness) response(id string) frame {
	h.t.Helper()
	f := h.next()
	want, _ := json.Marshal(id)
	if string(f.ID) != string(want) {
		h.t.Fatalf("response ID = %s, want %s; frame=%+v", f.ID, want, f)
	}
	return f
}
func (h *harness) ok(id string) frame {
	h.t.Helper()
	f := h.response(id)
	if f.Error != nil {
		h.t.Fatalf("%s failed: %+v", id, f.Error)
	}
	return f
}
func (h *harness) modelOK(id, sessionID, current string) frame {
	h.t.Helper()
	f := h.next()
	if f.Method != "session/update" || f.Params["sessionId"] != sessionID {
		h.t.Fatalf("model notification = %+v", f)
	}
	update, ok := f.Params["update"].(map[string]any)
	if !ok || update["sessionUpdate"] != "config_option_update" {
		h.t.Fatalf("model update = %#v", f.Params)
	}
	assertConfig(h.t, frame{Result: update}, current)
	return h.ok(id)
}

func (h *harness) wantError(id string, code int) {
	h.t.Helper()
	f := h.response(id)
	if f.Error == nil || f.Error.Code != code {
		h.t.Fatalf("error = %+v, want %d", f.Error, code)
	}
}
func (h *harness) initialize() frame {
	h.send("init", "initialize", map[string]any{"protocolVersion": 1})
	return h.ok("init")
}
func (h *harness) open(id string) frame {
	h.send(id, "session/new", map[string]any{"cwd": "/workspace", "mcpServers": []any{}})
	return h.ok(id)
}
func promptParams(id string) map[string]any {
	return map[string]any{"sessionId": id, "prompt": []ContentBlock{{Type: "text", Text: "hello"}}}
}
func requireSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for backend")
	}
}
func assertConfig(t *testing.T, f frame, current string) {
	t.Helper()
	configs, ok := f.Result["configOptions"].([]any)
	if !ok || len(configs) != 1 {
		t.Fatalf("configOptions = %#v", f.Result)
	}
	config := configs[0].(map[string]any)
	if config["id"] != "model" || config["category"] != "model" || config["type"] != "select" || config["currentValue"] != current {
		t.Fatalf("model config = %#v", config)
	}
	options := config["options"].([]any)
	if len(options) != 2 || options[0].(map[string]any)["value"] != "one" || options[1].(map[string]any)["name"] != "Two" {
		t.Fatalf("model options = %#v", options)
	}
}

func TestHandshakeNewPromptStream(t *testing.T) {
	retained := make(chan Emit, 1)
	seenBlocks := make(chan []ContentBlock, 1)
	var openCtx context.Context
	c := &fakeConversation{id: "s1", prompt: func(ctx context.Context, blocks []ContentBlock, emit Emit) (string, error) {
		seenBlocks <- blocks
		retained <- emit
		if err := emit(Update{"sessionUpdate": "agent_message_chunk", "content": ContentBlock{Type: "text", Text: "hello"}}); err != nil {
			return "", err
		}
		if err := emit(Update{"sessionUpdate": "agent_message_chunk", "content": ContentBlock{Type: "text", Text: " world"}}); err != nil {
			return "", err
		}
		return "end_turn", nil
	}}
	h := newHarness(t, fakeBackend(func(ctx context.Context, cwd, id string) (Conversation, error) {
		openCtx = ctx
		if cwd != "/workspace" || id != "" {
			return nil, errors.New("wrong open args")
		}
		return c, nil
	}))
	init := h.initialize()
	if init.Result["protocolVersion"] != float64(1) || init.Result["agentInfo"].(map[string]any)["name"] != "bqagent" {
		t.Fatalf("initialize = %#v", init.Result)
	}
	caps := init.Result["agentCapabilities"].(map[string]any)
	if caps["loadSession"] != true {
		t.Fatal("loadSession missing")
	}
	for _, key := range []string{"image", "audio", "embeddedContext"} {
		if caps["promptCapabilities"].(map[string]any)[key] != false {
			t.Fatalf("capability %s is not disabled", key)
		}
	}
	for _, key := range []string{"http", "sse"} {
		if caps["mcpCapabilities"].(map[string]any)[key] != false {
			t.Fatalf("MCP %s is not disabled", key)
		}
	}
	if len(init.Result["authMethods"].([]any)) != 0 || init.Result["_meta"].(map[string]any)["bqagent/manualPermissions"] != false {
		t.Fatal("unexpected authentication/permissions")
	}
	created := h.open("new")
	if created.Result["sessionId"] != "s1" {
		t.Fatalf("new = %#v", created.Result)
	}
	assertConfig(t, created, "one")
	if openCtx.Err() != nil {
		t.Fatal("Open lifetime ended with new response")
	}
	text := strings.Repeat("x", 100<<10)
	h.send("prompt", "session/prompt", map[string]any{"sessionId": "s1", "prompt": []ContentBlock{{Type: "text", Text: text}}})
	for range 2 {
		f := h.next()
		if f.Method != "session/update" || f.Params["sessionId"] != "s1" {
			t.Fatalf("unexpected update: %+v", f)
		}
	}
	if got := h.ok("prompt").Result["stopReason"]; got != "end_turn" {
		t.Fatalf("stopReason = %v", got)
	}
	if got := <-seenBlocks; len(got) != 1 || got[0].Text != text {
		t.Fatal("large prompt was changed")
	}
	if err := (<-retained)(Update{"sessionUpdate": "late"}); err == nil {
		t.Fatal("late update accepted")
	}
	h.initialize() // repeated initialization preserves the session
	h.send("again", "session/set_model", map[string]any{"sessionId": "s1", "modelId": "two"})
	h.modelOK("again", "s1", "two")
}

func TestLoadReplayBeforeResponse(t *testing.T) {
	retained := make(chan Emit, 1)
	c := &fakeConversation{id: "saved", replay: func(ctx context.Context, emit Emit) error {
		retained <- emit
		for _, text := range []string{"old user", "old answer"} {
			if err := emit(Update{"sessionUpdate": "agent_message_chunk", "content": ContentBlock{Type: "text", Text: text}}); err != nil {
				return err
			}
		}
		return nil
	}}
	h := newHarness(t, fakeBackend(func(ctx context.Context, cwd, id string) (Conversation, error) {
		if id != "saved" {
			return nil, errors.New("missing load ID")
		}
		return c, nil
	}))
	h.initialize()
	h.send("load", "session/load", map[string]any{"sessionId": "saved", "cwd": "/workspace"})
	for _, text := range []string{"old user", "old answer"} {
		f := h.next()
		if f.Method != "session/update" || f.Params["sessionId"] != "saved" {
			t.Fatalf("unexpected replay: %+v", f)
		}
		update := f.Params["update"].(map[string]any)
		if update["content"].(map[string]any)["text"] != text {
			t.Fatalf("replay order = %+v", update)
		}
	}
	assertConfig(t, h.ok("load"), "one")
	if err := (<-retained)(Update{}); err == nil {
		t.Fatal("late replay update accepted")
	}
	h.send("prompt", "session/prompt", promptParams("saved"))
	h.ok("prompt")
}

func TestCancelBusyAndParallelSessions(t *testing.T) {
	started := make(chan struct{})
	var calls atomic.Int32
	var opens atomic.Int32
	first := &fakeConversation{id: "first", prompt: func(ctx context.Context, blocks []ContentBlock, emit Emit) (string, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-ctx.Done()
			return "end_turn", ctx.Err()
		}
		return "end_turn", nil
	}}
	second := &fakeConversation{id: "second"}
	h := newHarness(t, fakeBackend(func(context.Context, string, string) (Conversation, error) {
		if opens.Add(1) == 1 {
			return first, nil
		}
		return second, nil
	}))
	h.initialize()
	h.open("new1")
	h.open("new2")
	h.send("active", "session/prompt", promptParams("first"))
	requireSignal(t, started)
	h.send("overlap", "session/prompt", promptParams("first"))
	h.wantError("overlap", codeBusy)
	h.send("model", "session/set_model", map[string]any{"sessionId": "first", "modelId": "two"})
	h.wantError("model", codeBusy)
	h.send("config", "session/set_config_option", map[string]any{"sessionId": "first", "configId": "model", "value": "two"})
	h.wantError("config", codeBusy)
	h.send("load", "session/load", map[string]any{"sessionId": "first", "cwd": "/workspace"})
	h.wantError("load", codeBusy)
	h.send("parallel", "session/prompt", promptParams("second"))
	h.ok("parallel")
	h.send(nil, "session/cancel", map[string]any{"sessionId": "first"})
	if got := h.ok("active").Result["stopReason"]; got != "cancelled" {
		t.Fatalf("stopReason = %v", got)
	}
	h.send("continue", "session/prompt", promptParams("first"))
	h.ok("continue")
	if opens.Load() != 2 || first.closed.Load() != 0 {
		t.Fatal("busy load changed the active resource")
	}
}

func TestInvalidRequestsAndModelSelection(t *testing.T) {
	var opens atomic.Int32
	c := &fakeConversation{id: "s1"}
	h := newHarness(t, fakeBackend(func(context.Context, string, string) (Conversation, error) { opens.Add(1); return c, nil }))
	h.send("early", "session/new", map[string]any{"cwd": "/workspace"})
	h.wantError("early", codeNotInitialized)
	h.raw("{")
	if f := h.next(); f.Error == nil || f.Error.Code != -32700 || string(f.ID) != "null" {
		t.Fatalf("parse error = %+v", f)
	}
	for _, raw := range []string{`[]`, `null`, `{"jsonrpc":"1.0","method":"initialize","id":true}`, `{"jsonrpc":"2.0","method":42,"id":1}`} {
		h.raw(raw)
		if f := h.next(); f.Error == nil || f.Error.Code != -32600 {
			t.Fatalf("invalid request = %+v", f)
		}
	}
	h.send("badinit", "initialize", map[string]any{"protocolVersion": "1"})
	h.wantError("badinit", -32602)
	h.initialize()
	h.send("missing", "does/not/exist", map[string]any{})
	h.wantError("missing", -32601)
	h.send("badparams", "session/new", []any{})
	h.wantError("badparams", -32602)
	for _, method := range []string{"session/new", "session/load"} {
		params := map[string]any{"cwd": "/workspace", "mcpServers": []any{map[string]any{"name": "unsupported", "command": "example"}}}
		if method == "session/load" {
			params["sessionId"] = "s1"
		}
		h.send(method, method, params)
		h.wantError(method, -32602)
	}
	if opens.Load() != 0 {
		t.Fatal("invalid request reached backend")
	}
	h.open("new")
	for _, kind := range []string{"image", "resource", "resource_link", "audio", "unknown", ""} {
		h.send("block", "session/prompt", map[string]any{"sessionId": "s1", "prompt": []any{map[string]any{"type": kind}}})
		h.wantError("block", -32602)
	}
	h.send("unknown", "session/prompt", promptParams("absent"))
	h.wantError("unknown", -32602)
	h.send("model", "session/set_model", map[string]any{"sessionId": "s1", "modelId": "two"})
	h.modelOK("model", "s1", "two")
	if c.Models().CurrentModelID != "two" {
		t.Fatal("set_model did not update backend")
	}
	h.send("config", "session/set_config_option", map[string]any{"sessionId": "s1", "configId": "model", "value": "one"})
	assertConfig(t, h.ok("config"), "one")
	h.send("badmodel", "session/set_model", map[string]any{"sessionId": "s1", "modelId": "absent"})
	h.wantError("badmodel", -32602)
	h.send("badconfig", "session/set_config_option", map[string]any{"sessionId": "s1", "configId": "mode", "value": "one"})
	h.wantError("badconfig", -32602)
	h.send("cancel-request", "session/cancel", map[string]any{"sessionId": "s1"})
	h.wantError("cancel-request", -32600)
	h.send(nil, "unknown/notification", map[string]any{})
	h.send(nil, "session/cancel", map[string]any{"sessionId": "absent"})
	h.initialize() // also proves notifications did not produce responses
}

func TestModelChangesSynchronizeConfigOptions(t *testing.T) {
	c := &fakeConversation{id: "models"}
	h := newHarness(t, fakeBackend(func(context.Context, string, string) (Conversation, error) { return c, nil }))
	h.initialize()
	h.open("new")

	// The legacy method acknowledges with {}, so its notification must first
	// carry the complete configuration and actual backend selection.
	h.send("legacy", "session/set_model", map[string]any{"sessionId": "models", "modelId": "two"})
	response := h.modelOK("legacy", "models", "two")
	if len(response.Result) != 0 {
		t.Fatalf("legacy response = %#v", response.Result)
	}

	// The config method communicates the same state in its response. No extra
	// notification may replace that response or remain queued after it.
	h.send("config", "session/set_config_option", map[string]any{"sessionId": "models", "configId": "model", "value": "one"})
	assertConfig(t, h.ok("config"), "one")
	h.send("invalid", "session/set_model", map[string]any{"sessionId": "models", "modelId": "absent"})
	h.wantError("invalid", -32602)
	h.send("prompt", "session/prompt", promptParams("models"))
	h.ok("prompt")
	if c.Models().CurrentModelID != "one" {
		t.Fatal("failed model change modified selection")
	}
}

func TestOversizedLineRecovers(t *testing.T) {
	h := newHarness(t, fakeBackend(func(context.Context, string, string) (Conversation, error) { return nil, errors.New("unused") }))
	h.raw(strings.Repeat("x", maxLineBytes+1))
	if f := h.next(); f.Error == nil || f.Error.Code != -32600 {
		t.Fatalf("line limit error = %+v", f)
	}
	h.initialize()
}

func TestSessionIDCollisionPreservesConversation(t *testing.T) {
	var opens atomic.Int32
	started := make(chan struct{})
	original := &fakeConversation{id: "same", prompt: func(ctx context.Context, _ []ContentBlock, _ Emit) (string, error) {
		close(started)
		<-ctx.Done()
		return "", ctx.Err()
	}}
	collision := &fakeConversation{id: "same"}
	h := newHarness(t, fakeBackend(func(context.Context, string, string) (Conversation, error) {
		if opens.Add(1) == 1 {
			return original, nil
		}
		return collision, nil
	}))
	h.initialize()
	h.open("new")
	h.send("active", "session/prompt", promptParams("same"))
	requireSignal(t, started)
	h.send("collision", "session/new", map[string]any{"cwd": "/workspace"})
	h.wantError("collision", codeSessionConflict)
	if original.closed.Load() != 0 {
		t.Fatal("collision closed the active conversation")
	}
	h.send(nil, "session/cancel", map[string]any{"sessionId": "same"})
	if h.ok("active").Result["stopReason"] != "cancelled" {
		t.Fatal("original prompt was lost")
	}
	h.send("model", "session/set_model", map[string]any{"sessionId": "same", "modelId": "two"})
	h.modelOK("model", "same", "two")
	if original.Models().CurrentModelID != "two" || original.closed.Load() != 0 {
		t.Fatal("collision replaced original conversation")
	}
}

func TestEOFCancelsAndClosesUncooperativePrompt(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	seenCtx := make(chan context.Context, 1)
	retained := make(chan Emit, 1)
	c := &fakeConversation{id: "s1", prompt: func(ctx context.Context, _ []ContentBlock, emit Emit) (string, error) {
		seenCtx <- ctx
		retained <- emit
		close(started)
		<-release // deliberately ignores cancellation
		return "end_turn", nil
	}}
	h := newHarness(t, fakeBackend(func(context.Context, string, string) (Conversation, error) { return c, nil }))
	h.initialize()
	h.open("new")
	h.send("prompt", "session/prompt", promptParams("s1"))
	requireSignal(t, started)
	_ = h.input.Close()
	select {
	case err := <-h.done:
		if err != nil {
			t.Fatal(err)
		}
		h.done <- err // leave the completion available to cleanup
	case <-time.After(2 * time.Second):
		t.Fatal("EOF waited for uncooperative prompt")
	}
	if (<-seenCtx).Err() != context.Canceled || c.closed.Load() != 1 {
		t.Fatal("EOF did not cancel and close")
	}
	if err := (<-retained)(Update{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("late update after EOF: %v", err)
	}
}

type blockedReader struct{ release <-chan struct{} }

func (r blockedReader) Read([]byte) (int, error) { <-r.release; return 0, io.EOF }

func TestContextCancellationWithGenericReader(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, blockedReader{release}, io.Discard, fakeBackend(func(context.Context, string, string) (Conversation, error) { return nil, errors.New("unused") }))
	}()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Serve returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation waited for generic Reader")
	}
}

type failWriter struct {
	frameWriter
	fail  atomic.Bool
	err   error
	short bool
}

func (w *failWriter) Write(p []byte) (int, error) {
	if w.fail.Load() {
		if w.short {
			return len(p) - 1, nil
		}
		return 0, w.err
	}
	return w.frameWriter.Write(p)
}

func TestOutputFailureCancelsAndCloses(t *testing.T) {
	for _, short := range []bool{false, true} {
		name := "error"
		if short {
			name = "short-write"
		}
		t.Run(name, func(t *testing.T) {
			reader, input := io.Pipe()
			defer reader.Close()
			defer input.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			frames := make(chan frame, 32)
			writeErr := errors.New("broken output")
			writer := &failWriter{frameWriter: frameWriter{frames}, err: writeErr, short: short}
			started := make(chan struct{})
			emitNow := make(chan struct{})
			cancelled := make(chan struct{})
			c := &fakeConversation{id: "s1", prompt: func(ctx context.Context, _ []ContentBlock, emit Emit) (string, error) {
				close(started)
				<-emitNow
				_ = emit(Update{"sessionUpdate": "agent_message_chunk"})
				<-ctx.Done()
				close(cancelled)
				return "", ctx.Err()
			}}
			done := make(chan error, 1)
			go func() {
				done <- Serve(ctx, reader, writer, fakeBackend(func(context.Context, string, string) (Conversation, error) { return c, nil }))
			}()
			h := &harness{t: t, input: input, frames: frames, done: done, cancel: cancel}
			h.initialize()
			h.open("new")
			h.send("prompt", "session/prompt", promptParams("s1"))
			requireSignal(t, started)
			writer.fail.Store(true)
			close(emitNow)
			select {
			case err := <-done:
				want := writeErr
				if short {
					want = io.ErrShortWrite
				}
				if !errors.Is(err, want) {
					t.Fatalf("Serve error = %v, want %v", err, want)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("Serve ignored output failure")
			}
			requireSignal(t, cancelled)
			if c.closed.Load() != 1 {
				t.Fatal("output failure did not close conversation")
			}
		})
	}
}

func TestLoadFailurePreservesSessionAndCanRetry(t *testing.T) {
	original := &fakeConversation{id: "saved"}
	broken := &fakeConversation{id: "saved", replay: func(context.Context, Emit) error { return errors.New("replay failed") }}
	reloaded := &fakeConversation{id: "saved"}
	var calls atomic.Int32
	h := newHarness(t, fakeBackend(func(context.Context, string, string) (Conversation, error) {
		switch calls.Add(1) {
		case 1:
			return original, nil
		case 2:
			return broken, nil
		default:
			return reloaded, nil
		}
	}))
	h.initialize()
	h.open("new")
	h.send("broken", "session/load", map[string]any{"cwd": "/workspace", "sessionId": "saved"})
	h.wantError("broken", -32603)
	h.send("original", "session/set_model", map[string]any{"sessionId": "saved", "modelId": "two"})
	h.modelOK("original", "saved", "two")
	if original.Models().CurrentModelID != "two" || original.closed.Load() != 0 {
		t.Fatal("failed load lost the original session")
	}
	h.send("retry", "session/load", map[string]any{"cwd": "/workspace", "sessionId": "saved"})
	assertConfig(t, h.ok("retry"), "one")
	h.send("reloaded", "session/set_model", map[string]any{"sessionId": "saved", "modelId": "two"})
	h.modelOK("reloaded", "saved", "two")
	if reloaded.Models().CurrentModelID != "two" {
		t.Fatal("load did not replace the session")
	}
	_ = h.input.Close()
	select {
	case err := <-h.done:
		if err != nil {
			t.Fatal(err)
		}
		h.done <- err
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not stop")
	}
	for _, c := range []*fakeConversation{original, broken, reloaded} {
		if c.closed.Load() != 1 {
			t.Fatalf("conversation closed %d times", c.closed.Load())
		}
	}
}

func TestBackendErrorsReleaseSession(t *testing.T) {
	var opens, prompts, models atomic.Int32
	c := &fakeConversation{id: "saved"}
	c.prompt = func(context.Context, []ContentBlock, Emit) (string, error) {
		if prompts.Add(1) == 1 {
			return "", errors.New("prompt failed")
		}
		return "end_turn", nil
	}
	c.setModel = func(context.Context, string) error {
		if models.Add(1) == 1 {
			return errors.New("model failed")
		}
		return nil
	}
	h := newHarness(t, fakeBackend(func(context.Context, string, string) (Conversation, error) {
		if opens.Add(1) == 1 {
			return nil, errors.New("open failed")
		}
		return c, nil
	}))
	h.initialize()
	h.send("openfail", "session/load", map[string]any{"cwd": "/workspace", "sessionId": "saved"})
	h.wantError("openfail", -32603)
	h.send("retry", "session/load", map[string]any{"cwd": "/workspace", "sessionId": "saved"})
	h.ok("retry")
	h.send("promptfail", "session/prompt", promptParams("saved"))
	h.wantError("promptfail", -32603)
	h.send("promptretry", "session/prompt", promptParams("saved"))
	h.ok("promptretry")
	h.send("modelfail", "session/set_model", map[string]any{"sessionId": "saved", "modelId": "two"})
	h.wantError("modelfail", -32603)
	h.send("modelretry", "session/set_model", map[string]any{"sessionId": "saved", "modelId": "two"})
	h.modelOK("modelretry", "saved", "one")
}

func TestConcurrentUpdatesPrecedeTerminalResponse(t *testing.T) {
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	prompt := func(ctx context.Context, _ []ContentBlock, emit Emit) (string, error) {
		started <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		var wg sync.WaitGroup
		for range 20 {
			wg.Add(1)
			go func() { defer wg.Done(); _ = emit(Update{"sessionUpdate": "agent_message_chunk"}) }()
		}
		wg.Wait()
		return "end_turn", nil
	}
	var opens atomic.Int32
	h := newHarness(t, fakeBackend(func(context.Context, string, string) (Conversation, error) {
		id := "first"
		if opens.Add(1) == 2 {
			id = "second"
		}
		return &fakeConversation{id: id, prompt: prompt}, nil
	}))
	h.initialize()
	h.open("first")
	h.open("second")
	h.send("first", "session/prompt", promptParams("first"))
	h.send("second", "session/prompt", promptParams("second"))
	requireSignal(t, started)
	requireSignal(t, started)
	close(release)
	counts := map[string]int{}
	completed := map[string]bool{}
	for range 42 {
		f := h.next()
		if f.Method == "session/update" {
			id := f.Params["sessionId"].(string)
			if completed[id] {
				t.Fatal("update followed terminal response")
			}
			counts[id]++
		} else {
			var id string
			if err := json.Unmarshal(f.ID, &id); err != nil {
				t.Fatal(err)
			}
			if f.Error != nil || counts[id] != 20 || completed[id] {
				t.Fatalf("invalid terminal frame: %+v, counts=%v", f, counts)
			}
			completed[id] = true
		}
	}
	if len(completed) != 2 {
		t.Fatal("missing prompt responses")
	}
}

func TestOpenCompletingAfterShutdownClosesResource(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	closed := make(chan struct{})
	c := &closeSignalConversation{fakeConversation: fakeConversation{id: "late"}, closed: closed}
	h := newHarness(t, fakeBackend(func(context.Context, string, string) (Conversation, error) {
		close(started)
		<-release
		return c, nil
	}))
	h.initialize()
	h.send("new", "session/new", map[string]any{"cwd": "/workspace"})
	requireSignal(t, started)
	_ = h.input.Close()
	select {
	case err := <-h.done:
		if err != nil {
			t.Fatal(err)
		}
		h.done <- err
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown waited for Open")
	}
	close(release)
	requireSignal(t, closed)
	if c.fakeConversation.closed.Load() != 1 {
		t.Fatal("late resource not closed exactly once")
	}
}

type closeSignalConversation struct {
	fakeConversation
	closed chan struct{}
}

func (c *closeSignalConversation) Close() { c.fakeConversation.Close(); close(c.closed) }

func TestSlowControlRequestsDoNotBlockOtherSessions(t *testing.T) {
	for _, operation := range []string{"open", "replay", "model"} {
		t.Run(operation, func(t *testing.T) {
			started := make(chan struct{})
			release := make(chan struct{})
			defer close(release)
			c := &fakeConversation{id: "slow"}
			if operation == "replay" {
				c.replay = func(context.Context, Emit) error { close(started); <-release; return nil }
			}
			if operation == "model" {
				c.setModel = func(context.Context, string) error { close(started); <-release; return nil }
			}
			var calls atomic.Int32
			h := newHarness(t, fakeBackend(func(ctx context.Context, cwd, id string) (Conversation, error) {
				if calls.Add(1) == 1 {
					return &fakeConversation{id: "fast"}, nil
				}
				if operation == "open" {
					close(started)
					<-release
				}
				return c, nil
			}))
			h.initialize()
			h.open("fast")
			switch operation {
			case "open":
				h.send("slow", "session/new", map[string]any{"cwd": "/workspace"})
			case "replay":
				h.send("slow", "session/load", map[string]any{"cwd": "/workspace", "sessionId": "slow"})
			case "model":
				h.open("newslow")
				h.send("slow", "session/set_model", map[string]any{"sessionId": "slow", "modelId": "two"})
			}
			requireSignal(t, started)
			h.send("fastprompt", "session/prompt", promptParams("fast"))
			h.ok("fastprompt")
		})
	}
}
