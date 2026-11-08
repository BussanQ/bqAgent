package extagent

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"slices"
	"testing"

	"bqagent/internal/session"
)

func TestCodexRewindRecoversLostReplyAndResumesOriginalThread(t *testing.T) {
	for _, first := range []bool{false, true} {
		name := "retained-turn"
		if first {
			name = "first-turn"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			store := NewStateStore(root)
			s, err := store.sessions.Create(session.CreateOptions{Chat: true, Task: "rewind"})
			if err != nil {
				t.Fatal(err)
			}
			state := SessionState{BQSessionID: s.ID(), Agent: AgentCodex, Transport: TransportAppServer, ExternalSessionID: "native", Model: "my-model", Effort: "high"}
			if err := store.Save(state); err != nil {
				t.Fatal(err)
			}
			spec := &AgentTransport{Agent: AgentCodex, Kind: TransportAppServer}
			detections := map[AgentName]DetectionResult{AgentCodex: {Agent: AgentCodex, Preferred: spec, AppServer: spec}}
			b := NewBroker(store, detections, nil)
			defer b.Close()
			newClient := func() *codexAppServerClient {
				c := &codexAppServerClient{cmd: &exec.Cmd{Cancel: func() error { return nil }}, responses: map[string]chan rpcEnvelope{}, done: make(chan struct{}), waitDone: make(chan struct{})}
				close(c.waitDone)
				return c
			}
			c := newClient()
			b.acpClients[acpClientKey{sessionID: s.ID(), agent: AgentCodex}] = c
			applied, reverts := false, 0
			c.stdin = modelWire(func(msg rpcEnvelope) {
				var params struct {
					ThreadID, BeforeTurnID string
					Cursor                 *string
				}
				if json.Unmarshal(msg.Params, &params) != nil || params.ThreadID != "native" {
					t.Fatal(msg.Method, string(msg.Params))
				}
				var reply rpcEnvelope
				switch msg.Method {
				case "thread/resume", "thread/read":
					reply.Result = json.RawMessage(`{"thread":{"id":"native","historyMode":"paginated","status":{"type":"idle"}}}`)
				case "thread/turns/list":
					switch {
					case applied && first:
						reply.Result = json.RawMessage(`{"data":[]}`)
					case applied || params.Cursor != nil:
						reply.Result = json.RawMessage(`{"data":[{"id":"keep"}]}`)
					case first:
						reply.Result = json.RawMessage(`{"data":[{"id":"tail"},{"id":"cut"}]}`)
					default:
						reply.Result = json.RawMessage(`{"data":[{"id":"tail"},{"id":"cut"}],"nextCursor":"previous"}`)
					}
				case "thread/revert":
					p, err := s.PendingRewind()
					if err != nil || p == nil || p.Before != "cut" || p.Head != "tail" || (p.Kept == "") != first || params.BeforeTurnID != "cut" {
						t.Fatal(p, err, params)
					}
					applied = true
					reverts++
					reply.Error = &rpcError{Code: -32000, Message: "reply lost"}
				default:
					t.Fatal(msg.Method)
				}
				c.responses[string(msg.ID)] <- reply
			})
			intent := session.PendingRewind{Operation: "op", Target: "message", Thread: "native", Before: "cut"}
			run := func() error {
				return b.RewindCodex(t.Context(), s, intent, []string{"cut", "tail"}, root, func(next *session.Session) error {
					return next.RewriteMessages([]map[string]any{{"role": "user", "content": "retained"}})
				})
			}
			if err := run(); !errors.Is(err, session.ErrRewindPending) || s.ContextNumber() != 0 {
				t.Fatal(err, s.ContextNumber())
			}
			reopened, err := store.sessions.Open(s.ID())
			if err != nil {
				t.Fatal(err)
			}
			if f, err := reopened.OpenContextLock(false); !errors.Is(err, session.ErrRewindPending) {
				if f != nil {
					f.Close()
				}
				t.Fatalf("pending native mutation admitted a turn: %v", err)
			}
			wire := c.stdin
			b.Close()
			b = NewBroker(NewStateStore(root), detections, nil)
			defer b.Close()
			c = newClient()
			c.stdin = wire
			s = reopened
			b.acpClients[acpClientKey{sessionID: s.ID(), agent: AgentCodex}] = c
			if err := run(); err != nil {
				t.Fatal(err)
			}
			got, err := store.Load(s.ID())
			if err != nil || got != state || s.ContextNumber() != 1 || reverts != 1 {
				t.Fatal(got, state, err, s.ContextNumber(), reverts)
			}
			if f, err := reopened.OpenContextLock(false); err != nil {
				t.Fatal(err)
			} else {
				f.Close()
			}
		})
	}
}

func TestCodexRewindRefusesForeignTurnsAndUnverifiedReceipts(t *testing.T) {
	for _, tc := range []struct {
		name, meta, turns string
		prior             bool
	}{
		{"busy", `{"id":"n","historyMode":"paginated","status":{"type":"active"}}`, "", false},
		{"legacy", `{"id":"n","historyMode":"legacy","status":{"type":"idle"}}`, "", false},
		{"foreign-tail", `{"id":"n","historyMode":"paginated","status":{"type":"idle"}}`, `{"data":[{"id":"terminal"},{"id":"cut"}]}`, false},
		{"ambiguous-tip", `{"id":"n","historyMode":"paginated","status":{"type":"idle"}}`, `{"data":[{"id":"other"}]}`, true},
		{"malformed-empty", `{"id":"n","historyMode":"paginated","status":{"type":"idle"}}`, `{"data":null}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := session.NewStore(t.TempDir()).Create(session.CreateOptions{Chat: true, Task: "anchor"})
			if err != nil {
				t.Fatal(err)
			}
			p := session.PendingRewind{Operation: "op", Target: "row", Thread: "n", Before: "cut", Head: "cut", Kept: "keep"}
			if tc.prior {
				if err := s.SavePendingRewind(p); err != nil {
					t.Fatal(err)
				}
			}
			c := &codexAppServerClient{responses: map[string]chan rpcEnvelope{}, done: make(chan struct{})}
			c.stdin = modelWire(func(msg rpcEnvelope) {
				raw := tc.turns
				if msg.Method == "thread/read" {
					raw = `{"thread":` + tc.meta + `}`
				}
				if !slices.Contains([]string{"thread/read", "thread/turns/list"}, msg.Method) {
					t.Fatal("unsafe RPC", msg.Method)
				}
				c.responses[string(msg.ID)] <- rpcEnvelope{Result: json.RawMessage(raw)}
			})
			if err := c.rewind(context.Background(), s, p, []string{"cut"}); err == nil {
				t.Fatal("accepted unverifiable history")
			}
			stored, _ := s.PendingRewind()
			if (stored != nil) != tc.prior {
				t.Fatal(stored)
			}
		})
	}
}

func TestCodexRewindPointBelongsToAcknowledgedRootOnPaginatedThread(t *testing.T) {
	for _, mode := range []string{"paginated", "legacy", ""} {
		t.Run("history-"+mode, func(t *testing.T) {
			c := &codexAppServerClient{responses: map[string]chan rpcEnvelope{}, done: make(chan struct{})}
			c.stdin = modelWire(func(msg rpcEnvelope) {
				switch msg.Method {
				case "thread/start":
					var params struct{ HistoryMode string }
					if json.Unmarshal(msg.Params, &params) != nil || params.HistoryMode != "paginated" {
						t.Fatal(string(msg.Params))
					}
					data, _ := json.Marshal(map[string]any{"thread": map[string]string{"id": "n", "historyMode": mode}})
					c.responses[string(msg.ID)] <- rpcEnvelope{Result: data}
				case "turn/start":
					c.responses[string(msg.ID)] <- rpcEnvelope{Result: json.RawMessage(`{"turn":{"id":"confirmed"}}`)}
					c.notification(rpcEnvelope{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"n","turn":{"id":"confirmed","status":"completed","items":[{"id":"a","type":"agentMessage","text":"ok"}]}}`)})
				default:
					t.Fatal(msg.Method)
				}
			})
			id, err := c.NewSession(t.Context(), "/workspace")
			if err != nil || id != "n" {
				t.Fatal(id, err)
			}
			var thread, turnID string
			reply, err := c.PromptWithOptions(t.Context(), id, "same", PromptOptions{Steering: &TurnSteering{Started: func(state SessionState, id string) { thread, turnID = state.ExternalSessionID, id }}})
			if err != nil || reply != "ok" || (mode == "paginated") != (thread == "n" && turnID == "confirmed") {
				t.Fatal(reply, err, thread, turnID)
			}
		})
	}
}

func TestCodexRewindRefusesChangedTipBeforeDispatch(t *testing.T) {
	s, err := session.NewStore(t.TempDir()).Create(session.CreateOptions{Chat: true, Task: "tip"})
	if err != nil {
		t.Fatal(err)
	}
	c := &codexAppServerClient{responses: map[string]chan rpcEnvelope{}, done: make(chan struct{})}
	c.stdin = modelWire(func(msg rpcEnvelope) {
		var response string
		switch msg.Method {
		case "thread/read":
			response = `{"thread":{"id":"n","historyMode":"paginated","status":{"type":"idle"}}}`
		case "thread/turns/list":
			var params struct{ Limit int }
			if json.Unmarshal(msg.Params, &params) != nil {
				t.Fatal(string(msg.Params))
			}
			response = `{"data":[{"id":"cut"},{"id":"keep"}]}`
			if params.Limit == 1 {
				response = `{"data":[{"id":"from-other-client"}]}`
			}
		default:
			t.Fatal("reverted a thread that changed during pagination", msg.Method)
		}
		c.responses[string(msg.ID)] <- rpcEnvelope{Result: json.RawMessage(response)}
	})
	p := session.PendingRewind{Operation: "op", Target: "row", Thread: "n", Before: "cut"}
	if err := c.rewind(t.Context(), s, p, []string{"cut"}); err == nil {
		t.Fatal("accepted changed head")
	}
	if prior, err := s.PendingRewind(); err != nil || prior != nil {
		t.Fatal(prior, err)
	}
}
