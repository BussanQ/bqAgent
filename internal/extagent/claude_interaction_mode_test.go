package extagent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

const claudeModeUserLine = `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"review changes"},{"type":"image","source":{"data":"image bytes"}}]}}`

type modeUserReader struct {
	reader io.Reader
	once   sync.Once
	read   chan struct{}
}

func (u *modeUserReader) Read(p []byte) (int, error) {
	u.once.Do(func() { close(u.read) })
	return u.reader.Read(p)
}

type modeTestWire struct {
	setup    *claudeModeSetup
	control  *claudeUsageControl
	input    *os.File
	decoder  *json.Decoder
	output   *io.PipeWriter
	user     *modeUserReader
	stopped  chan struct{}
	readDone chan error
}

type modeWireFrame struct {
	Type     string
	ID       string `json:"request_id"`
	Request  struct{ Subtype, Mode string }
	Response struct {
		ID       string `json:"request_id"`
		Response struct{ Behavior string }
	}
}

func newModeTestWire(t *testing.T, ctx context.Context, pick, launch string, host *claudePromptHost) *modeTestWire {
	t.Helper()
	w := &modeTestWire{stopped: make(chan struct{}), readDone: make(chan error, 1)}
	w.setup = newClaudeModeSetup(ctx, pick, func() { close(w.stopped) })
	w.user = &modeUserReader{reader: strings.NewReader(claudeModeUserLine + "\n"), read: make(chan struct{})}
	if host != nil {
		host.mode.launch = launch
	}
	in, c, err := openClaudeTurnControl(w.user, nil, nil, host, w.setup, launch)
	if err != nil {
		t.Fatal(err)
	}
	w.input, w.control, w.decoder = in, c, json.NewDecoder(in)
	reader, out := io.Pipe()
	w.output = out
	go func() {
		_, _, err := readClaudeContextStream(reader, func(Activity) {}, "root", nil, c)
		w.readDone <- err
	}()
	t.Cleanup(func() { out.Close(); in.Close(); c.finish(); reader.Close(); <-w.setup.done })
	return w
}

func (w *modeTestWire) send(t *testing.T, frame any) {
	t.Helper()
	if err := json.NewEncoder(w.output).Encode(frame); err != nil {
		t.Fatal(err)
	}
}
func (w *modeTestWire) request(t *testing.T, subtype string) modeWireFrame {
	t.Helper()
	_ = w.input.SetReadDeadline(time.Now().Add(2 * time.Second))
	var f modeWireFrame
	if err := w.decoder.Decode(&f); err != nil || f.Type != "control_request" || f.ID == "" || f.Request.Subtype != subtype {
		t.Fatalf("wanted %s, got %+v err=%v", subtype, f, err)
	}
	select {
	case <-w.user.read:
		t.Fatal("read user input before mode confirmation")
	default:
	}
	return f
}
func (w *modeTestWire) reply(t *testing.T, id, subtype string, data any) {
	t.Helper()
	w.send(t, map[string]any{"type": "control_response", "response": map[string]any{"request_id": id, "subtype": subtype, "response": data}})
}
func (w *modeTestWire) finish(t *testing.T) {
	t.Helper()
	w.send(t, map[string]string{"type": "result", "session_id": "root", "result": "done"})
	w.output.Close()
	select {
	case err := <-w.readDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reader did not retire")
	}
}

func TestClaudeInteractionModeHoldsTextAndImagesForNativeConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name, pick, launch, root, configured, want string
		legacyAck                                  bool
	}{
		{"plan using native workspace settings", "plan", "", "", "dontAsk", "plan", false},
		{"build preserves workspace policy", "default", "", "", "acceptEdits", "acceptEdits", false},
		{"default when workspace sets no policy", "default", "", "", "", "default", true},
		{"strict launch policy", "default", "dontAsk", "", "", "dontAsk", false},
		{"native mode supersedes launch", "default", "bypassPermissions", "default", "", "default", false},
		{"confirmed full access", "default", "", "bypassPermissions", "", "bypassPermissions", false},
		{"unconfirmed full access flag", "default", "bypassPermissions", "", "", "default", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newModeTestWire(t, t.Context(), tc.pick, tc.launch, nil)
			init := w.request(t, "initialize")
			if tc.root != "" {
				w.send(t, map[string]string{"type": "system", "subtype": "init", "permissionMode": tc.root, "session_id": "root"})
			}
			w.reply(t, init.ID, "success", map[string]any{})
			if tc.root == "" && (tc.launch == "" || tc.launch == "plan") {
				settings := w.request(t, "get_settings")
				w.reply(t, settings.ID, "success", map[string]any{"effective": map[string]any{"permissions": map[string]string{"defaultMode": tc.configured}}})
			}
			set := w.request(t, "set_permission_mode")
			if set.Request.Mode != tc.want || set.ID == init.ID {
				t.Fatal(set)
			}
			data := map[string]string{}
			if !tc.legacyAck {
				data["mode"] = tc.want
			}
			w.reply(t, set.ID, "success", data)
			var raw json.RawMessage
			if err := w.decoder.Decode(&raw); err != nil || string(raw) != claudeModeUserLine {
				t.Fatal(string(raw), err)
			}
			w.finish(t)
			if err := w.decoder.Decode(&raw); err != io.EOF {
				t.Fatal("extra input after prompt", string(raw), err)
			}
			if w.setup.err != nil || w.control.mode.interaction() != tc.pick {
				t.Fatal(w.setup.err, w.control.mode.interaction())
			}
		})
	}
}

func TestClaudeModeSetupIgnoresForeignReplayedAndNestedReceipts(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		data         any
	}{
		{"rejected", "error", nil},
		{"reported execution", "success", map[string]string{"mode": "default"}},
		{"null mode", "success", map[string]any{"mode": nil}},
		{"malformed", "success", "plan"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newModeTestWire(t, t.Context(), "plan", "dontAsk", nil)
			init := w.request(t, "initialize")
			w.reply(t, init.ID, "success", nil)
			set := w.request(t, "set_permission_mode")
			ok := map[string]any{"subtype": "success", "request_id": set.ID, "response": map[string]string{"mode": "plan"}}
			w.send(t, map[string]any{"type": "control_response", "response": ok, "session_id": "other"})
			w.send(t, map[string]any{"type": "control_response", "response": ok, "parent_tool_use_id": "parent"})
			w.send(t, map[string]any{"type": "control_response", "response": ok, "isReplay": true})
			w.reply(t, "another", "success", map[string]string{"mode": "plan"})
			w.reply(t, set.ID, tc.status, tc.data)
			<-w.setup.done
			select {
			case <-w.stopped:
			default:
				t.Fatal("child not stopped")
			}
			var raw json.RawMessage
			if err := w.decoder.Decode(&raw); err != io.EOF || w.setup.err == nil {
				t.Fatal(err, w.setup.err, string(raw))
			}
			select {
			case <-w.user.read:
				t.Fatal("released rejected prompt")
			default:
			}
		})
	}
}

func TestClaudeUnconfirmedModeEndsOnCancelDisconnectOrInvalidSettings(t *testing.T) {
	for _, why := range []string{"cancel", "disconnect", "settings"} {
		t.Run(why, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			w := newModeTestWire(t, ctx, "default", "", nil)
			init := w.request(t, "initialize")
			w.reply(t, init.ID, "success", nil)
			settings := w.request(t, "get_settings")
			switch why {
			case "cancel":
				cancel()
			case "disconnect":
				w.output.Close()
			case "settings":
				w.reply(t, settings.ID, "success", map[string]any{"sources": []string{}})
			}
			select {
			case <-w.setup.done:
			case <-time.After(2 * time.Second):
				t.Fatal("setup still waiting")
			}
			if w.setup.err == nil || why == "cancel" && !errors.Is(w.setup.err, context.Canceled) {
				t.Fatal(w.setup.err)
			}
			if err := w.decoder.Decode(&json.RawMessage{}); err != io.EOF {
				t.Fatal(err)
			}
			select {
			case <-w.user.read:
				t.Fatal("read unconfirmed prompt")
			default:
			}
		})
	}
}

func TestClaudeInteractionModeCatalogAndQueueHonorSessionTransport(t *testing.T) {
	root := t.TempDir()
	store := NewStateStore(root)
	cli := &AgentTransport{Agent: AgentClaude, Kind: TransportCLI}
	acp := &AgentTransport{Agent: AgentClaude, Kind: TransportACP}
	b := NewBroker(store, map[AgentName]DetectionResult{AgentClaude: {Preferred: cli, CLI: cli, ACP: acp}}, nil)
	defer b.Close()
	for _, tc := range []struct {
		chat      string
		kind      TransportKind
		supported bool
	}{{"cli", TransportCLI, true}, {"acp", TransportACP, false}} {
		state := SessionState{Agent: AgentClaude, Transport: tc.kind, BQSessionID: tc.chat, ExternalSessionID: "native", Model: "sonnet", InteractionMode: "plan"}
		if err := store.Save(state); err != nil {
			t.Fatal(err)
		}
		menu, err := b.ModelCapabilities(t.Context(), AgentClaude, tc.chat, root)
		if err != nil || menu.SupportsInteractionMode != tc.supported || tc.supported && (menu.InteractionMode != "plan" || len(menu.InteractionModes) != 2) {
			t.Fatal(menu, err)
		}
		queued, err := b.QueuedMessageOptions(t.Context(), AgentClaude, tc.chat, PromptOptions{InteractionMode: "default"}, false)
		if (err == nil) != tc.supported || tc.supported && (queued.InteractionMode != "default" || queued.Model != "sonnet" || queued.ExternalSessionID != "native") {
			t.Fatal(queued, err)
		}
		if _, err := b.optionsForTurn(t.Context(), TurnRequest{Agent: AgentClaude, InteractionMode: "bypassPermissions"}, state, tc.kind); err == nil {
			t.Fatal("interaction menu accepted arbitrary permission policy")
		}
	}
}

func TestClaudePlanSelectionRetainsToolApprovalsAndConfirmsPolicyAfterPlanExit(t *testing.T) {
	host := newClaudePromptHost(t.Context(), func(context.Context, acpPermissionParams) acpPermissionOutcome {
		return acpPermissionOutcome{Outcome: "selected", OptionID: "allow"}
	})
	w := newModeTestWire(t, t.Context(), "plan", "", host)
	init := w.request(t, "initialize")
	// A startup approval must still be writable while the setup waits.
	tool := func(name, id string) {
		w.send(t, map[string]any{"type": "control_request", "request_id": id, "request": map[string]any{"subtype": "can_use_tool", "tool_name": name, "tool_use_id": "tool-" + id, "input": map[string]string{"plan": "review"}}})
	}
	tool("Read", "early")
	var approval modeWireFrame
	if err := w.decoder.Decode(&approval); err != nil || approval.Type != "control_response" || approval.Response.ID != "early" || approval.Response.Response.Behavior != "allow" {
		t.Fatal(approval, err)
	}
	w.reply(t, init.ID, "success", nil)
	settings := w.request(t, "get_settings")
	w.reply(t, settings.ID, "success", map[string]any{"effective": map[string]any{"permissions": map[string]string{"defaultMode": "dontAsk"}}})
	set := w.request(t, "set_permission_mode")
	w.reply(t, set.ID, "success", map[string]string{"mode": "plan"})
	var user json.RawMessage
	if err := w.decoder.Decode(&user); err != nil || string(user) != claudeModeUserLine {
		t.Fatal(string(user), err)
	}
	tool("ExitPlanMode", "exit")
	var change modeWireFrame
	if err := w.decoder.Decode(&change); err != nil || change.Request.Subtype != "set_permission_mode" || change.Request.Mode != "dontAsk" {
		t.Fatal(change, err)
	}
	if err := w.decoder.Decode(&approval); err != nil || approval.Response.ID != "exit" || approval.Response.Response.Behavior != "allow" {
		t.Fatal(approval, err)
	}
	w.reply(t, change.ID, "success", map[string]string{"mode": "dontAsk"})
	w.finish(t)
	if got := w.control.mode.interaction(); got != "default" {
		t.Fatal(got)
	}
}

func TestClaudeDelayedPlanExitAckCannotReplaceNewerRootStatus(t *testing.T) {
	m := new(claudePlanMode)
	m.observe("init", "acceptEdits")
	m.observe("status", "plan")
	request := m.exitRequest(claudeToolRequest{Name: "ExitPlanMode"}, map[string]any{"behavior": "allow"}).(map[string]any)
	m.observe("status", "plan")
	m.reply(claudeControlReply{ID: request["request_id"].(string), Subtype: "success", Response: json.RawMessage(`{"mode":"acceptEdits"}`)})
	if got := m.interaction(); got != "plan" {
		t.Fatal(got)
	}
}
