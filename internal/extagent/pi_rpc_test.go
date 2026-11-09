package extagent

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"bqagent/internal/media"
	"bqagent/internal/turn"
)

type piTestWire struct {
	frames chan piFrame
	sent   chan map[string]any
}

func newPiTestWire() *piTestWire {
	return &piTestWire{make(chan piFrame, 32), make(chan map[string]any, 32)}
}
func (w *piTestWire) rpc() piRPC {
	return piRPC{frames: w.frames, write: func(cmd map[string]any) error { w.sent <- cmd; return nil }}
}
func (w *piTestWire) next(t *testing.T, command string) map[string]any {
	t.Helper()
	select {
	case cmd := <-w.sent:
		if cmd["type"] != command {
			t.Fatalf("expected %s, got %v", command, cmd)
		}
		return cmd
	case <-time.After(2 * time.Second):
		t.Fatalf("Pi did not receive %s", command)
		return nil
	}
}
func (w *piTestWire) reply(cmd map[string]any, data string) {
	w.frames <- piFrame{event: piEvent{Type: "response", ID: piRequestID{text: cmd["id"].(string)}, Command: cmd["type"].(string), Success: true, Data: json.RawMessage(data)}}
}
func (w *piTestWire) event(kind string) { w.frames <- piFrame{event: piEvent{Type: kind}} }
func (w *piTestWire) message(role, text string) {
	data, _ := json.Marshal(map[string]any{"role": role, "content": []map[string]string{{"type": "text", "text": text}}})
	w.frames <- piFrame{event: piEvent{Type: "message_end", Message: data}}
}

func TestPiRPCSteeringAndFollowUpConsumeEchoesInProviderOrder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	w := newPiTestWire()
	ready := make(chan func(turn.Input) error, 1)
	applied := make(chan turn.Input, 3)
	hooks := &TurnSteering{Target: func(state SessionState) {
		if state.Agent != AgentPi || state.Model != "p/new" || state.ExternalSessionID != "native" || state.Effort != "high" {
			t.Errorf("unexpected Pi settings: %+v", state)
		}
	}, Ready: func(send func(turn.Input) error, kinds ...turn.InputKind) {
		if !slices.Equal(kinds, []turn.InputKind{turn.Steering, turn.FollowUp}) {
			t.Errorf("input kinds = %v", kinds)
		}
		ready <- send
	}, Applied: func(input turn.Input) { applied <- input }, Unconsumed: func(inputs []turn.Input) { t.Errorf("unexpected pending inputs %+v", inputs) }}
	completed := make(chan error, 1)
	go func() {
		state := SessionState{Agent: AgentPi, ExternalSessionID: "native", Model: "p/new", Effort: "high"}
		got, err := w.rpc().prompt(ctx, state, "original", []media.Image{{MIMEType: "image/png", Data: []byte{1, 2, 3}}}, hooks)
		if err == nil && (got.Reply != "finished" || got.State.ExternalSessionID != "native") {
			t.Errorf("reply=%+v", got)
		}
		completed <- err
	}()
	const idle = `{"sessionId":"native","isStreaming":false,"pendingMessageCount":0}`
	w.reply(w.next(t, "get_state"), `{"sessionId":"native","model":{"provider":"p","id":"old"},"thinkingLevel":"low"}`)
	model := w.next(t, "set_model")
	if model["modelId"] != "new" || model["provider"] != "p" {
		t.Fatal(model)
	}
	w.reply(model, `{"id":"new","provider":"p"}`)
	effort := w.next(t, "set_thinking_level")
	if effort["level"] != "high" {
		t.Fatal(effort)
	}
	w.reply(effort, "{}")
	initial := w.next(t, "prompt")
	images, _ := initial["images"].([]map[string]string)
	if initial["message"] != "original" || len(images) != 1 || images[0]["data"] != "AQID" || images[0]["mimeType"] != "image/png" {
		t.Fatal(initial)
	}
	w.reply(initial, `{"disposition":"started"}`)
	w.event("agent_start")
	w.message("user", "original")
	var send func(turn.Input) error
	select {
	case send = <-ready:
	case <-ctx.Done():
		t.Fatal("no native input capability")
	}
	for _, tc := range []struct {
		input    turn.Input
		behavior string
	}{
		{turn.Input{ID: "f", Kind: turn.FollowUp, Text: "same"}, "followUp"},
		{turn.Input{ID: "s", Kind: turn.Steering, Text: "same", Images: []media.Image{{MIMEType: "image/png", Data: []byte{1, 2, 3}}}}, "steer"},
	} {
		result := make(chan error, 1)
		go func(input turn.Input) { result <- send(input) }(tc.input)
		cmd := w.next(t, "prompt")
		if cmd["streamingBehavior"] != tc.behavior || cmd["id"] != "input-"+tc.input.ID || cmd["message"] != "same" {
			t.Fatal(cmd)
		}
		if tc.input.ID == "s" {
			blocks, _ := cmd["images"].([]map[string]string)
			if len(blocks) != 1 || blocks[0]["data"] != "AQID" || blocks[0]["mimeType"] != "image/png" {
				t.Fatal(cmd)
			}
		}
		w.reply(cmd, `{"disposition":"queued"}`)
		if err := <-result; err != nil {
			t.Fatal(err)
		}
	}
	select {
	case i := <-applied:
		t.Fatalf("queued input marked applied: %+v", i)
	default:
	}
	w.message("user", "same")
	w.message("assistant", "first answer")
	w.message("user", "same")
	w.message("assistant", "finished")
	w.event("agent_end") // Modern Pi may still retry after this event.
	w.event("agent_settled")
	stale := w.next(t, "get_state")
	w.event("agent_start")
	w.reply(stale, idle)
	w.event("agent_settled")
	w.reply(w.next(t, "get_state"), idle)
	if err := <-completed; err != nil {
		t.Fatal(err)
	}
	if len(applied) != 2 || (<-applied).ID != "s" || (<-applied).ID != "f" {
		t.Fatal("Pi echoes were attributed in the wrong order")
	}
	if err := send(turn.Input{Kind: turn.FollowUp, Text: "late"}); !errors.Is(err, turn.ErrInputClosed) {
		t.Fatal(err)
	}
	if args := piRPCArgs([]string{"--mode", "json", "--provider", "p"}, "native"); !slices.Equal(args, []string{"--provider", "p", "--mode", "rpc", "--session", "native"}) {
		t.Fatal(args)
	}
}

func TestPiRPCStopReturnsOnlyUnconsumedNativeInputs(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	w := newPiTestWire()
	var applied, remaining []turn.Input
	pending := func(id string, kind turn.InputKind) *nativeInputRequest {
		return &nativeInputRequest{input: turn.Input{ID: id, Kind: kind, Text: id}}
	}
	waiting := pending("waiting", turn.Steering)
	waiting.result = make(chan error, 1)
	run := piRPCTurn{rpc: w.rpc(), initialSeen: true, hooks: &TurnSteering{
		Applied:    func(input turn.Input) { applied = append(applied, input) },
		Unconsumed: func(inputs []turn.Input) { remaining = inputs },
	}, pending: []*nativeInputRequest{pending("queued", turn.FollowUp), pending("consumed", turn.Steering), waiting}}
	done := make(chan error, 1)
	go func() { _, err := run.run(ctx); done <- err }()
	cancel()
	abort := w.next(t, "abort")
	w.message("user", "consumed")
	w.reply(abort, "{}")
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if len(remaining) != 1 || remaining[0].ID != "queued" || len(applied) != 1 || applied[0].ID != "consumed" {
		t.Fatal(applied, remaining)
	}
	if err := <-waiting.result; !errors.Is(err, turn.ErrInputClosed) {
		t.Fatal(err)
	}
}

func TestPiRPCRefusesForeignSessionAndNeverReplaysARejectedPrompt(t *testing.T) {
	for _, tc := range []struct {
		sessionID    string
		wrongSession bool
	}{{"other", true}, {"native", false}} {
		w := newPiTestWire()
		result := make(chan error, 1)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		go func() {
			_, err := w.rpc().prompt(ctx, SessionState{ExternalSessionID: "native"}, "only once", nil, nil)
			result <- err
		}()
		w.reply(w.next(t, "get_state"), `{"sessionId":"`+tc.sessionID+`"}`)
		if !tc.wrongSession {
			cmd := w.next(t, "prompt")
			w.frames <- piFrame{event: piEvent{ID: piRequestID{text: cmd["id"].(string)}, Type: "response", Command: "prompt", Error: json.RawMessage(`"refused"`)}}
		}
		if err := <-result; err == nil || errors.Is(err, errPiRPCUnavailable) {
			t.Fatal(err)
		}
		cancel()
		if len(w.sent) != 0 {
			t.Fatal("unexpected replay")
		}
	}
}

func TestPiFinalSnapshotStreamsOnlyMissingTextBeforeNextInput(t *testing.T) {
	var output piOutput
	var parts []string
	sink := func(a Activity) { parts = append(parts, a.Text) }
	var delta piEvent
	delta.Type = "message_update"
	delta.AssistantMessageEvent.Type, delta.AssistantMessageEvent.Delta = "text_delta", "pre"
	_, _ = output.observe(delta, sink)
	for _, raw := range []string{
		`{"role":"assistant","content":[{"type":"text","text":"prefix"}]}`,
		`{"role":"user","content":[{"type":"text","text":"follow"}]}`,
		`{"role":"assistant","content":[{"type":"text","text":"next"}]}`,
	} {
		if _, err := output.observe(piEvent{Type: "message_end", Message: json.RawMessage(raw)}, sink); err != nil {
			t.Fatal(err)
		}
	}
	if !slices.Equal(parts, []string{"pre", "fix", "next"}) {
		t.Fatal(parts)
	}
}

func TestPiQueueUpdateIdentifiesWhichIdenticalMessageWasConsumed(t *testing.T) {
	var applied []turn.Input
	run := piRPCTurn{hooks: &TurnSteering{Applied: func(input turn.Input) { applied = append(applied, input) }}}
	f := &nativeInputRequest{input: turn.Input{ID: "f", Kind: turn.FollowUp, Text: "same"}}
	s := &nativeInputRequest{input: turn.Input{ID: "s", Kind: turn.Steering, Text: "same"}}
	run.pending = []*nativeInputRequest{f, s}
	run.updateQueue(turn.FollowUp, []string{"same"})
	run.updateQueue(turn.Steering, []string{"same"})
	run.updateQueue(turn.FollowUp, nil)
	run.consume("same")
	if len(applied) != 1 || applied[0].ID != "f" || len(run.pending) != 1 || run.pending[0] != s {
		t.Fatal(applied, run.pending)
	}
}

func TestPiSettlementWaitsForDetachedCompactionAndReportsFailedRetry(t *testing.T) {
	ctx := context.Background()
	w := newPiTestWire()
	run := piRPCTurn{rpc: w.rpc(), modern: true, output: piOutput{sessionID: "native", reply: "old"}}
	_, _ = run.receive(ctx, piEvent{Type: "agent_settled"})
	stale := w.next(t, "get_state")
	_, _ = run.receive(ctx, piEvent{Type: "auto_compaction_start"})
	idle := json.RawMessage(`{"sessionId":"native","pendingMessageCount":0}`)
	done, err := run.receive(ctx, piEvent{Type: "response", Command: "get_state", ID: piRequestID{text: stale["id"].(string)}, Success: true, Data: idle})
	if err != nil || done {
		t.Fatal("stale state completed the turn", err)
	}
	_, _ = run.receive(ctx, piEvent{Type: "auto_compaction_end"})
	current := w.next(t, "get_state")
	_, _ = run.receive(ctx, piEvent{Type: "auto_retry_end", FinalError: "native retry exhausted"})
	done, err = run.receive(ctx, piEvent{Type: "response", Command: "get_state", ID: piRequestID{text: current["id"].(string)}, Success: true, Data: idle})
	if err != nil || !done {
		t.Fatal("idle state did not settle", err)
	}
	if _, _, err = run.output.final(); err == nil || err.Error() != "pi: native retry exhausted" {
		t.Fatal(err)
	}
}

func TestPiModelSwitchVerifiesBareAcknowledgementOnOriginalSession(t *testing.T) {
	for _, tc := range []struct {
		name, sessionID, model string
		ok                     bool
	}{
		{"applied", "native", "new", true},
		{"unchanged", "native", "old", false},
		{"different session", "other", "new", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			w := newPiTestWire()
			done := make(chan error, 1)
			go func() {
				_, err := w.rpc().setup(ctx, SessionState{ExternalSessionID: "native", Model: "vendor/new"})
				done <- err
			}()
			w.reply(w.next(t, "get_state"), `{"sessionId":"native","model":{"id":"old","provider":"vendor"}}`)
			set := w.next(t, "set_model")
			if set["provider"] != "vendor" || set["modelId"] != "new" {
				t.Fatal(set)
			}
			w.reply(set, `{}`)
			w.reply(w.next(t, "get_state"), `{"sessionId":"`+tc.sessionID+`","model":{"id":"`+tc.model+`","provider":"vendor"}}`)
			if err := <-done; (err == nil) != tc.ok {
				t.Fatal(err)
			}
			if len(w.sent) != 0 {
				t.Fatal("unexpected additional Pi command")
			}
		})
	}
}

func TestPiTurnReturnsOnlyTheSettledSessionsModelAndEffort(t *testing.T) {
	for _, tc := range []struct {
		name, settled, model, effort string
	}{
		{"same settings", `{"sessionId":"native","model":{"provider":"a","id":"old"},"thinkingLevel":"low"}`, "a/old", "low"},
		{"different provider after command", `{"sessionId":"native","model":{"provider":"b","id":"org/new"},"thinkingLevel":"high"}`, "b/org/new", "high"},
		{"new model with no effort report", `{"sessionId":"native","model":{"provider":"b","id":"new"}}`, "b/new", ""},
		{"legacy idle reply", `{"sessionId":"native"}`, "a/old", "low"},
		{"unaddressable placeholder", `{"sessionId":"native","model":{"provider":"unknown","id":"new"},"thinkingLevel":"high"}`, "a/old", "low"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newPiTestWire()
			result := make(chan TurnResponse, 1)
			errs := make(chan error, 1)
			go func() {
				out, err := w.rpc().prompt(t.Context(), SessionState{Agent: AgentPi, BQSessionID: "chat", Transport: TransportCLI},
					"/change-model", nil, &TurnSteering{Target: func(s SessionState) {
						if s.ExternalSessionID != "native" || s.Model != "a/old" || s.Effort != "low" {
							t.Errorf("running target = %+v", s)
						}
					}})
				result <- out
				errs <- err
			}()
			w.reply(w.next(t, "get_state"), `{"sessionId":"native","model":{"provider":"a","id":"old"},"thinkingLevel":"low"}`)
			w.reply(w.next(t, "prompt"), `{"disposition":"handled","agentInvoked":false}`)
			stale := w.next(t, "get_state")
			w.event("agent_start") // Invalidates the earlier idle request.
			w.event("agent_settled")
			idle := w.next(t, "get_state")
			w.reply(stale, `{"sessionId":"native","model":{"provider":"stale","id":"m"},"thinkingLevel":"max"}`)
			w.reply(idle, tc.settled)
			out := <-result
			if err := <-errs; err != nil || out.State.ExternalSessionID != "native" || out.State.BQSessionID != "chat" ||
				out.State.Agent != AgentPi || out.State.Model != tc.model || out.State.Effort != tc.effort || !out.settingsAcknowledged {
				t.Fatal(out, err)
			}
			if len(w.sent) != 0 {
				t.Fatal("extra Pi command")
			}
		})
	}
}

func TestPiModelReadbackUsesANewRequestAndKeepsConfirmedOptionsAfterEffortFailure(t *testing.T) {
	w := newPiTestWire()
	type result struct {
		out TurnResponse
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := w.rpc().prompt(t.Context(), SessionState{Agent: AgentPi, ExternalSessionID: "native", Model: "org/new", Effort: "high"},
			"do not send", nil, nil)
		done <- result{out, err}
	}()
	first := w.next(t, "get_state")
	w.reply(first, `{"sessionId":"native","model":{"provider":"p","id":"old"},"thinkingLevel":"high"}`)
	set := w.next(t, "set_model")
	w.reply(set, `{}`)
	readback := w.next(t, "get_state")
	if first["id"] == readback["id"] {
		t.Fatal("model readback reused startup request ID")
	}
	w.reply(first, `{"sessionId":"native","model":{"provider":"p","id":"old"},"thinkingLevel":"high"}`)
	w.reply(readback, `{"sessionId":"native","model":{"provider":"org","id":"new"},"thinkingLevel":"low"}`)
	level := w.next(t, "set_thinking_level")
	if level["level"] != "high" {
		t.Fatal(level)
	}
	w.frames <- piFrame{event: piEvent{Type: "response", Command: "set_thinking_level", ID: piRequestID{text: level["id"].(string)}, Error: json.RawMessage(`"level unavailable"`)}}
	got := <-done
	if got.err == nil || errors.Is(got.err, errPiRPCUnavailable) || !got.out.settingsAcknowledged || got.out.State.Model != "org/new" ||
		got.out.State.Effort != "low" || got.out.State.ExternalSessionID != "native" || len(w.sent) != 0 {
		t.Fatal(got, len(w.sent))
	}
}
