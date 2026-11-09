package extagent

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

type compactOutcome struct {
	reply string
	err   error
}

// In-memory, independently ordered requests and notifications allow a native
// item to precede the acknowledgement or arrive while Stop is pending.
type compactWire struct {
	c        *codexAppServerClient
	requests chan rpcEnvelope
}

func newCompactWire() *compactWire {
	w := &compactWire{c: &codexAppServerClient{responses: map[string]chan rpcEnvelope{}, done: make(chan struct{})}, requests: make(chan rpcEnvelope, 8)}
	w.c.stdin = modelWire(func(msg rpcEnvelope) { w.requests <- msg })
	return w
}
func (w *compactWire) next(t *testing.T, method string) rpcEnvelope {
	t.Helper()
	select {
	case msg := <-w.requests:
		if msg.Method != method {
			t.Fatalf("wanted %s, got %s", method, msg.Method)
		}
		return msg
	case <-time.After(2 * time.Second):
		t.Fatalf("missing %s", method)
		return rpcEnvelope{}
	}
}
func (w *compactWire) respond(msg rpcEnvelope, result string) {
	w.c.mu.Lock()
	defer w.c.mu.Unlock()
	if ch := w.c.responses[string(msg.ID)]; ch != nil {
		ch <- rpcEnvelope{Result: json.RawMessage(result)}
	}
}
func (w *compactWire) notify(method, raw string) {
	w.c.notification(rpcEnvelope{Method: method, Params: json.RawMessage(raw)})
}
func (w *compactWire) start(ctx context.Context, started func()) <-chan compactOutcome {
	out := make(chan compactOutcome, 1)
	go func() {
		r, e := w.c.compactThread(ctx, "native", "repo", &CompactCommand{Started: started})
		out <- compactOutcome{r, e}
	}()
	return out
}
func (w *compactWire) dispatch(t *testing.T) rpcEnvelope {
	t.Helper()
	resume := w.next(t, "thread/resume")
	var p struct{ ThreadID, Cwd string }
	if json.Unmarshal(resume.Params, &p) != nil || p.ThreadID != "native" || p.Cwd != "repo" {
		t.Fatal(string(resume.Params))
	}
	w.respond(resume, `{"thread":{"id":"native","status":{"type":"idle"}}}`)
	req := w.next(t, "thread/compact/start")
	var target map[string]string
	if json.Unmarshal(req.Params, &target) != nil || len(target) != 1 || target["threadId"] != "native" {
		t.Fatal(string(req.Params))
	}
	return req
}

func TestCodexCompactRequiresNativeCompletionOnMatchingThread(t *testing.T) {
	for _, tc := range []struct {
		name                         string
		early                        bool
		marker, status, detail, want string
	}{
		{"item", false, "item/completed", "completed", "", "已压缩"},
		{"legacy before ack", true, "thread/compacted", "completed", "", "已压缩"},
		{"final items", false, "snapshot", "completed", "", "已压缩"},
		{"failed after retries", false, "", "failed", "capacity", "capacity"},
		{"completed without marker", false, "", "completed", "", "未确认"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			w := newCompactWire()
			started := false
			out := w.start(ctx, func() { started = true })
			req := w.dispatch(t)
			if !started {
				t.Fatal("missing command status")
			}
			if !tc.early {
				w.respond(req, `{}`)
			}
			w.notify("turn/started", `{"threadId":"other","turn":{"id":"compaction"}}`)
			w.notify("thread/compacted", `{"threadId":"other","turnId":"compaction"}`)
			w.notify("turn/completed", `{"threadId":"native","turn":{"id":"old","status":"completed"}}`)
			w.notify("turn/started", `{"threadId":"native","turn":{"id":"compaction"}}`)
			w.notify("item/completed", `{"threadId":"native","turnId":"stale","item":{"type":"contextCompaction","id":"previous"}}`)
			w.notify("item/started", `{"threadId":"native","turnId":"compaction","item":{"type":"contextCompaction","id":"i"}}`)
			w.notify("error", `{"threadId":"native","turnId":"compaction","willRetry":true,"error":{"message":"retry"}}`)
			w.notify("thread/compacted", `{"threadId":"native","turnId":"wrong"}`)
			w.notify("turn/completed", `{"threadId":"native","turn":{"id":"wrong","status":"completed"}}`)
			switch tc.marker {
			case "item/completed":
				w.notify(tc.marker, `{"threadId":"native","turnId":"compaction","item":{"type":"contextCompaction","id":"i"}}`)
			case "thread/compacted":
				w.notify(tc.marker, `{"threadId":"native"}`)
			}
			if tc.detail != "" {
				w.notify("error", `{"threadId":"native","turnId":"compaction","willRetry":false,"error":{"message":"capacity"}}`)
			}
			select {
			case got := <-out:
				t.Fatalf("ended before native completion: %+v", got)
			default:
			}
			items := "[]"
			if tc.marker == "snapshot" {
				items = `[{"id":"i","type":"contextCompaction"}]`
			}
			w.notify("turn/completed", `{"threadId":"native","turn":{"id":"compaction","status":"`+tc.status+`","items":`+items+`}}`)
			if tc.early {
				select {
				case <-out:
					t.Fatal("ended before ack")
				default:
				}
				w.respond(req, `{}`)
			}
			got := <-out
			if tc.marker != "" {
				if got.err != nil || !strings.Contains(got.reply, tc.want) {
					t.Fatal(got)
				}
			} else if got.err == nil || !strings.Contains(got.err.Error(), tc.want) {
				t.Fatal(got)
			}
			if len(w.requests) != 0 {
				t.Fatal("unexpected second operation")
			}
		})
	}
}

func TestCodexCompactStopWaitsForTheIdentifiedTurn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := newCompactWire()
	out := w.start(ctx, nil)
	w.dispatch(t) // stop before the start acknowledgement
	cancel()
	w.notify("turn/started", `{"threadId":"native","turn":{"id":"foreign"}}`)
	w.notify("turn/completed", `{"threadId":"native","turn":{"id":"foreign","status":"completed","items":[{"type":"agentMessage","id":"f"}]}}`)
	w.notify("item/started", `{"threadId":"elsewhere","turnId":"wrong","item":{"type":"contextCompaction"}}`)
	w.notify("item/started", `{"threadId":"native","turnId":"ours","item":{"type":"contextCompaction"}}`)
	req := w.next(t, "turn/interrupt")
	var target struct{ ThreadID, TurnID string }
	if json.Unmarshal(req.Params, &target) != nil || target.ThreadID != "native" || target.TurnID != "ours" {
		t.Fatal(string(req.Params))
	}
	w.respond(req, `{}`)
	w.notify("turn/completed", `{"threadId":"native","turn":{"id":"ours","status":"interrupted"}}`)
	if got := <-out; !errors.Is(got.err, context.Canceled) {
		t.Fatal(got)
	}
}

func TestCodexCompactRejectsBusyOrUnexpectedResumedThread(t *testing.T) {
	for _, thread := range []string{`{"id":"other"}`, `{"id":"native","status":{"type":"active"}}`} {
		w := newCompactWire()
		out := w.start(context.Background(), func() { t.Error("dispatched despite wrong or busy thread") })
		w.respond(w.next(t, "thread/resume"), `{"thread":`+thread+`}`)
		if got := <-out; got.err == nil || len(w.requests) != 0 {
			t.Fatal(got)
		}
	}
}

func TestPiCompactUsesSavedSessionAndNativeControl(t *testing.T) {
	for _, tc := range []struct {
		instructions, rejection, want string
		failed                        bool
	}{
		{"preserve paths\nand results", "", "已压缩", false},
		{"", "Nothing to compact (session too small)", "内容较少", false},
		{"", "Already compacted", "已经压缩", false},
		{"", "permission refused", "permission refused", true},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		w := newPiTestWire()
		done := make(chan compactOutcome, 1)
		go func() {
			r, err := w.rpc().compact(ctx, SessionState{ExternalSessionID: "native", Model: "p/other", Effort: "high"}, &CompactCommand{Instructions: tc.instructions})
			if r.State.Model != "p/other" || r.Command != "compact" {
				t.Error(r)
			}
			done <- compactOutcome{r.Reply, err}
		}()
		w.reply(w.next(t, "get_state"), `{"sessionId":"native","model":{"provider":"p","id":"old"}}`)
		cmd := w.next(t, "compact")
		hint, ok := cmd["customInstructions"]
		if (tc.instructions != "") != ok || ok && hint != tc.instructions {
			t.Fatal(cmd)
		}
		if tc.rejection == "" {
			w.reply(cmd, `{"summary":"private summary","tokensBefore":10000}`)
		} else {
			msg, _ := json.Marshal(tc.rejection)
			w.frames <- piFrame{event: piEvent{ID: piRequestID{text: cmd["id"].(string)}, Type: "response", Command: "compact", Error: msg}}
		}
		got := <-done
		cancel()
		value := got.reply
		if got.err != nil {
			value = got.err.Error()
		}
		if tc.failed != (got.err != nil) || !strings.Contains(value, tc.want) || strings.Contains(value, "private") || len(w.sent) != 0 {
			t.Fatal(got)
		}
	}
}

func TestPiCompactStopsNativeOperationWithoutSendingPrompt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := newPiTestWire()
	done := make(chan error, 1)
	go func() {
		_, err := w.rpc().compact(ctx, SessionState{ExternalSessionID: "native"}, &CompactCommand{})
		done <- err
	}()
	w.reply(w.next(t, "get_state"), `{"sessionId":"native"}`)
	w.next(t, "compact")
	cancel()
	w.reply(w.next(t, "abort"), "{}")
	if err := <-done; !errors.Is(err, context.Canceled) || len(w.sent) != 0 {
		t.Fatal(err)
	}
}

func TestCompactCatalogPreservesSkillsAndRequiresNativeTransport(t *testing.T) {
	rows := []NativeCommand{{Name: "compact", Kind: "extension", Text: "/compact"}, {Name: "compact", Kind: "skill", Text: "$compact"}}
	for _, name := range []AgentName{AgentPi, AgentClaude, AgentCodex} {
		kind := TransportCLI
		if name == AgentCodex {
			kind = TransportAppServer
		}
		got := compactCatalog(&AgentTransport{Agent: name, Kind: kind}, rows)
		if len(got) != 2 || got[0].Kind != "control" || got[1] != rows[1] || (got[0].Hint != "") != (name != AgentCodex) {
			t.Fatal(got)
		}
	}
	if got := compactCatalog(&AgentTransport{Agent: AgentCodex, Kind: TransportACP}, rows); !slices.Equal(rows, got) {
		t.Fatal(got)
	}
	if ParseCompactCommand("/compaction", false) != nil || ParseCompactCommand("/COMPACT\n keep  history ", true).Instructions != "keep  history" {
		t.Fatal("incorrect command recognition")
	}
}

type compactBindingClient struct {
	reconnectingACPClient
	ids  []string
	fail bool
}

func (c *compactBindingClient) compactThread(_ context.Context, id, cwd string, cmd *CompactCommand) (string, error) {
	c.ids = append(c.ids, id)
	if cmd.Started != nil {
		cmd.Started()
	}
	if c.fail {
		return "", errors.New("rejected")
	}
	return "compressed " + id, nil
}
func TestCompactBrokerKeepsBindingsAndDoesNotCommitNextModelChoice(t *testing.T) {
	for _, fail := range []bool{false, true} {
		root := t.TempDir()
		store := NewStateStore(root)
		codex := SessionState{BQSessionID: "bq", Agent: AgentCodex, ExternalSessionID: "codex-native", Transport: TransportAppServer, Model: "original", Effort: "medium"}
		pi := SessionState{BQSessionID: "bq", Agent: AgentPi, ExternalSessionID: "pi-native", Transport: TransportCLI, Model: "p/pi"}
		if err := store.Save(codex); err != nil {
			t.Fatal(err)
		}
		if err := store.Save(pi); err != nil {
			t.Fatal(err)
		}
		broker := NewBroker(store, map[AgentName]DetectionResult{AgentCodex: {Preferred: &AgentTransport{Agent: AgentCodex, Kind: TransportAppServer}}}, nil)
		c := &compactBindingClient{fail: fail}
		broker.acpClients[acpClientKey{sessionID: "bq", agent: AgentCodex}] = c
		started := 0
		req := TurnRequest{BQSessionID: "bq", Agent: AgentCodex, Prompt: "/compact", Model: "future", Effort: "future-effort", CWD: root, Compact: &CompactCommand{Started: func() { started++ }}}
		out, err := broker.SendTurn(context.Background(), req)
		broker.Close()
		if (err != nil) != fail || !slices.Equal(c.ids, []string{"codex-native"}) || started != 1 || c.closeCount != 1 {
			t.Fatal(out, err, c)
		}
		got, err := store.stateForAgent("bq", AgentCodex)
		if err != nil || got != codex {
			t.Fatal(got, err)
		}
		got, err = store.stateForAgent("bq", AgentPi)
		if err != nil || got != pi {
			t.Fatal(got, err)
		}
		active, err := store.Load("bq")
		if err != nil || fail && active != pi || !fail && (active != codex || out.Command != "compact" || out.Reply != "compressed codex-native") {
			t.Fatal(active, err, out)
		}
	}
}

func TestPiCompactRefusesForeignOrBusySession(t *testing.T) {
	for _, state := range []string{`{"sessionId":"other"}`, `{"sessionId":"native","isCompacting":true}`} {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		w := newPiTestWire()
		done := make(chan error, 1)
		go func() {
			_, err := w.rpc().compact(ctx, SessionState{ExternalSessionID: "native"}, &CompactCommand{Started: func() { t.Error("started on wrong session") }})
			done <- err
		}()
		w.reply(w.next(t, "get_state"), state)
		err := <-done
		cancel()
		if err == nil || len(w.sent) != 0 {
			t.Fatal(err)
		}
	}
}

func TestClaudeCompactNeedsManualBoundaryAndResultForResumedSession(t *testing.T) {
	const start = `{"type":"system","subtype":"init","session_id":"saved"}`
	const marker = `{"type":"system","subtype":"compact_boundary","session_id":"saved","compact_metadata":{"trigger":"manual","pre_tokens":8000}}`
	const result = `{"type":"result","subtype":"success","session_id":"saved","result":"private summary"}`
	const failed = `{"type":"result","subtype":"error_during_execution","session_id":"saved","is_error":true,"errors":["capacity"]}`
	for _, tc := range []struct {
		name    string
		frames  []string
		want    string
		success bool
	}{
		{"success", []string{start, marker, result}, "已压缩", true},
		{"auto", []string{strings.Replace(marker, "manual", "auto", 1), result}, "未确认", false},
		{"foreign boundary", []string{strings.Replace(marker, "saved", "foreign", 1), result}, "未确认", false},
		{"foreign result", []string{marker, strings.Replace(result, "saved", "foreign", 1)}, "未返回", false},
		{"interrupted without boundary", []string{result}, "未确认", false},
		{"failed", []string{failed}, "capacity", false},
		{"subagent marker", []string{strings.Replace(marker, `"type":"system"`, `"type":"system","parent_tool_use_id":"other"`, 1), result}, "未确认", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reply, err := readClaudeCompact(strings.NewReader(strings.Join(tc.frames, "\n")), "saved")
			text := reply
			if err != nil {
				text = err.Error()
			}
			if (err == nil) != tc.success || !strings.Contains(text, tc.want) || strings.Contains(text, "private summary") {
				t.Fatal(reply, err)
			}
		})
	}
}

func TestClaudeCompactPreservesProviderFlagsButTargetsOnlyTheSavedSession(t *testing.T) {
	spec := []string{"wrap", "-c", "--", "claude", "--resume", "foreign", "--session-id=forced", "--fork-session", "-c", "--no-session-persistence", "--resume", "--model", "p/one", "--plugin-dir", "plugin"}
	got := claudeCompactArgs(spec)
	if !slices.Equal(got, []string{"wrap", "-c", "--", "claude", "--model", "p/one", "--plugin-dir", "plugin"}) || !slices.Contains(spec, "foreign") {
		t.Fatal(got, spec)
	}
	tpt := &AgentTransport{Agent: AgentClaude, Kind: TransportCLI, Command: CommandSpec{Args: []string{"--disable-slash-commands"}}}
	rows, err := cliCommands(context.Background(), tpt, "workspace")
	if err != nil || len(rows) != 0 || compactSupported(tpt) {
		t.Fatal(rows, err)
	}
	tpt.Command.Args = []string{"--disable-slash-commands", "--", "claude"}
	if !compactSupported(tpt) {
		t.Fatal("wrapper option disabled native commands")
	}
}

func TestClaudeCompactEncodesMultilineInstructionsAsPlainUserCommand(t *testing.T) {
	input, err := claudeStreamInput("/compact preserve\nlinks and tests", nil)
	if err != nil {
		t.Fatal(err)
	}
	var frame struct {
		Type    string
		Message struct{ Role, Content string }
	}
	if json.NewDecoder(input).Decode(&frame) != nil || frame.Type != "user" || frame.Message.Role != "user" || frame.Message.Content != "/compact preserve\nlinks and tests" {
		t.Fatal(frame)
	}
}

func TestPiCompactResumeOverridesTransientSessionFlags(t *testing.T) {
	args := []string{"wrapper", "--session", "wrapper-id", "--", "pi", "--mode=json", "--fork=other", "--session-id", "other", "--no-session", "--continue", "--provider", "vendor"}
	want := []string{"wrapper", "--session", "wrapper-id", "--", "pi", "--provider", "vendor", "--mode", "rpc", "--session", "saved"}
	if got := piRPCArgs(args, "saved"); !slices.Equal(got, want) || !slices.Contains(args, "--no-session") {
		t.Fatal(got, args)
	}
}

func TestQueuedCompactRefusesAReplacedNativeSessionOrTransport(t *testing.T) {
	ctx := context.Background()
	store := NewStateStore(t.TempDir())
	d := map[AgentName]DetectionResult{AgentPi: {Preferred: &AgentTransport{Agent: AgentPi, Kind: TransportCLI}}}
	b := NewBroker(store, d, nil)
	defer b.Close()
	state := SessionState{BQSessionID: "bq", Agent: AgentPi, ExternalSessionID: "old", Transport: TransportCLI}
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	bound, err := b.QueuedCompactBinding(ctx, AgentPi, "bq", "/compact keep logs")
	if err != nil || bound != "old" {
		t.Fatal(bound, err)
	}
	state.ExternalSessionID = "new"
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	req := TurnRequest{BQSessionID: "bq", Agent: AgentPi, Prompt: "/compact keep logs", ExpectedSession: bound, Compact: &CompactCommand{NativeOnly: true}}
	if _, err = b.SendTurn(ctx, req); err == nil || !strings.Contains(err.Error(), "已经更换") {
		t.Fatal(err)
	}
	b2 := NewBroker(store, map[AgentName]DetectionResult{AgentPi: {Preferred: &AgentTransport{Agent: AgentPi, Kind: TransportACP}}}, func(context.Context, CommandSpec, string) (ACPClient, error) {
		t.Fatal("native-only command opened an ACP client")
		return nil, nil
	})
	defer b2.Close()
	state.Transport = TransportACP
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	req.ExpectedSession = "new"
	if _, err = b2.SendTurn(ctx, req); err == nil || !strings.Contains(err.Error(), "无法执行排队") {
		t.Fatal(err)
	}
}
