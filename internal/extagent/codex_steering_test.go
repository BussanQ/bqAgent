package extagent

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"bqagent/internal/media"
	"bqagent/internal/turn"
)

func TestCodexSteeringRequiresMatchingActiveTurnAcknowledgement(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c := &codexAppServerClient{responses: map[string]chan rpcEnvelope{}, done: make(chan struct{})}
	c.threadOptions = SessionState{Agent: AgentCodex, ExternalSessionID: "thread", Model: "default", Effort: "low"}
	var applied, wire []string
	c.stdin = modelWire(func(msg rpcEnvelope) {
		reply := rpcEnvelope{Result: json.RawMessage(`{"turn":{"id":"native-turn"}}`)}
		wire = append(wire, msg.Method)
		switch msg.Method {
		case "turn/start":
		case "turn/steer":
			var p struct {
				ThreadID, ExpectedTurnID, ClientUserMessageID string
				Input                                         []struct{ Type, Text, URL string }
			}
			if json.Unmarshal(msg.Params, &p) != nil || p.ThreadID != "thread" || p.ExpectedTurnID != "native-turn" || len(p.Input) == 0 || p.Input[0].Type != "text" || p.ClientUserMessageID != p.Input[0].Text {
				t.Errorf("invalid steer params %s", msg.Params)
			}
			if p.ClientUserMessageID == "use Go" && (len(p.Input) != 2 || p.Input[1].Type != "image" || p.Input[1].URL != "data:image/png;base64,AQID") {
				t.Errorf("missing steer image: %s", msg.Params)
			}
			reply.Result = json.RawMessage(`{"turnId":"native-turn"}`)
			switch p.Input[0].Text {
			case "stale":
				reply.Result = json.RawMessage(`{"turnId":"different"}`)
			case "reject":
				reply.Error = &rpcError{Message: "no active turn"}
			}
		default:
			t.Errorf("unexpected request %s", msg.Method)
		}
		c.responses[string(msg.ID)] <- reply
	})
	ready := make(chan func(turn.Input) error, 1)
	done := make(chan error, 1)
	go func() {
		reply, err := c.PromptWithOptions(ctx, "thread", "initial", PromptOptions{Steering: &TurnSteering{
			Target: func(state SessionState) {
				if state != c.threadOptions {
					t.Errorf("unexpected steering target: %+v", state)
				}
			},
			Ready:   func(send func(turn.Input) error, _ ...turn.InputKind) { ready <- send },
			Applied: func(input turn.Input) { applied = append(applied, input.Text) },
		}})
		if err == nil && reply != "corrected" {
			t.Errorf("unexpected reply %q", reply)
		}
		done <- err
	}()
	var send func(turn.Input) error
	select {
	case send = <-ready:
	case <-ctx.Done():
		t.Fatal("no input handler")
	}
	for _, tc := range []struct {
		text    string
		kind    turn.InputKind
		wantErr bool
	}{
		{"queued", turn.FollowUp, true}, {"stale", turn.Steering, true}, {"reject", turn.Steering, true}, {"use Go", turn.Steering, false},
	} {
		input := turn.Input{ID: tc.text, Kind: tc.kind, Text: tc.text}
		if tc.text == "use Go" {
			input.Images = []media.Image{{MIMEType: "image/png", Data: []byte{1, 2, 3}}}
		}
		err := send(input)
		if errors.Is(err, turn.ErrInputRejected) != (tc.text == "reject") {
			t.Fatalf("unexpected certainty of %s: %v", tc.text, err)
		}
		if (err != nil) != tc.wantErr {
			t.Fatalf("input %q error=%v", tc.text, err)
		}
	}
	c.notification(rpcEnvelope{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"thread","turn":{"id":"native-turn","status":"completed","items":[{"type":"agentMessage","id":"msg","text":"corrected","phase":"final_answer"}]}}`)})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := send(turn.Input{Kind: turn.Steering, Text: "late"}); !errors.Is(err, turn.ErrInputClosed) {
		t.Fatal(err)
	}
	if !slices.Equal(applied, []string{"use Go"}) || strings.Join(wire, ",") != "turn/start,turn/steer,turn/steer,turn/steer" {
		t.Fatalf("applied=%v methods=%v", applied, wire)
	}
}

func TestCodexSteeringStopsWaitingWhenCanceledAndRejectsCompletedTurns(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &codexAppServerClient{responses: map[string]chan rpcEnvelope{}, done: make(chan struct{})}
	started := make(chan struct{})
	c.stdin = modelWire(func(msg rpcEnvelope) { close(started) })
	run := &appServerRun{ctx: ctx, threadID: "thread", turnID: "active", turns: map[string]*appServerProgress{
		"active": {completed: &appServerTurn{ID: "active", Status: "completed"}},
	}}
	input := turn.Input{Kind: turn.Steering, Text: "change"}
	if err := c.steer(run, input, nil); !errors.Is(err, turn.ErrInputClosed) {
		t.Fatalf("sent to completed turn: %v", err)
	}
	run.turns["active"].completed = nil
	result := make(chan error, 1)
	go func() { result <- c.steer(run, input, nil) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("steering request did not reach native child")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled steering request is still waiting")
	}
}
