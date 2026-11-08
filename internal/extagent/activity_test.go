package extagent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestExternalCLIReportsToolBeforeProcessCompletes(t *testing.T) {
	for _, tc := range []struct {
		name            string
		read            func(io.Reader, ActivitySink) (string, string, error)
		start, rest, id string
	}{
		{"pi", readPiJSON, `{"type":"tool_execution_start","toolCallId":"c1","toolName":"Read","args":{"path":"f"}}`, `{"type":"tool_execution_end","toolCallId":"c1","toolName":"Read","result":{"content":[{"type":"text","text":"output"}]}}
{"type":"session","id":"pi-id"}
{"type":"message_end","message":{"role":"assistant","stopReason":"stop","content":[{"type":"text","text":"answer"}]}}`, "pi-id"},
		{"claude", readClaudeStream, `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"c1","name":"Read","input":{"path":"f"}}]}}`, `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"c1","content":"output"}]}}
{"type":"stream_event","event":{"delta":{"type":"text_delta","text":"answer"}}}
{"type":"assistant","message":{"content":[{"type":"text","text":"answer"}]}}
{"type":"result","subtype":"success","session_id":"claude-id","result":"answer"}`, "claude-id"},
		{"codex", readCodexCLI, `{"type":"item.started","item":{"id":"c1","type":"command_execution","command":"ls","status":"in_progress"}}`, `{"type":"thread.started","thread_id":"codex-id"}
{"type":"item.completed","item":{"id":"c1","type":"command_execution","command":"ls","status":"completed","aggregated_output":"output","exit_code":0}}
{"type":"item.completed","item":{"id":"m","type":"agent_message","text":"answer"}}`, "codex-id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, w := io.Pipe()
			defer r.Close()
			defer w.Close()
			first := make(chan Activity, 1)
			var got []Activity
			done := make(chan error, 1)
			go func() {
				reply, id, err := tc.read(r, func(a Activity) {
					if len(got) == 0 {
						first <- a
					}
					got = append(got, a)
				})
				if err == nil && (reply != "answer" || id != tc.id) {
					err = errors.New("unexpected final reply/session: " + reply + "/" + id)
				}
				done <- err
			}()
			if _, err := io.WriteString(w, tc.start+"\n"); err != nil {
				t.Fatal(err)
			}
			select {
			case a := <-first:
				if a.CallID != "c1" || a.Status != "running" || len(a.Input) == 0 {
					t.Fatalf("start: %+v", a)
				}
			case <-time.After(time.Second):
				t.Fatal("tool was buffered until EOF")
			}
			if _, err := io.WriteString(w, tc.rest+"\n"); err != nil {
				t.Fatal(err)
			}
			w.Close()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if len(got) < 2 || got[1].Output != "output" || got[1].Status != "succeeded" || got[1].CallID != "c1" {
				t.Fatalf("events: %+v", got)
			}
			if tc.name == "claude" && (len(got) != 3 || got[2].Text != "answer") {
				t.Fatalf("Claude text replayed: %+v", got)
			}
		})
	}
}

func TestACPActivityOnlyForPromptSession(t *testing.T) {
	c := &stdioACPClient{responses: map[string]chan rpcEnvelope{}, collectors: map[string]*strings.Builder{}}
	c.stdin = modelWire(func(msg rpcEnvelope) {
		for _, raw := range []string{
			`{"sessionId":"other","update":{"sessionUpdate":"tool_call","toolCallId":"foreign"}}`,
			`{"sessionId":"s","update":{"sessionUpdate":"agent_thought_chunk","content":{"text":"private"}}}`,
			`{"sessionId":"s","update":{"sessionUpdate":"tool_call","toolCallId":"x","title":"Read","rawInput":{"path":"f"}}}`,
			`{"sessionId":"s","update":{"sessionUpdate":"tool_call_update","toolCallId":"x","status":"failed","content":[{"type":"content","content":{"type":"text","text":"denied"}}]}}`,
			`{"sessionId":"s","update":{"sessionUpdate":"agent_message_chunk","content":{"text":"a"}}}`,
			`{"sessionId":"s","update":{"sessionUpdate":"agent_message_chunk","content":{"text":" "}}}`,
			`{"sessionId":"s","update":{"sessionUpdate":"agent_message_chunk","content":{"text":"b"}}}`,
		} {
			c.handleSessionUpdate(json.RawMessage(raw))
		}
		c.responses[string(msg.ID)] <- rpcEnvelope{}
	})
	var events []Activity
	ctx := context.WithValue(context.Background(), activityContextKey{}, ActivitySink(func(a Activity) { events = append(events, a) }))
	reply, err := c.Prompt(ctx, "s", "hi")
	c.handleSessionUpdate(json.RawMessage(`{"sessionId":"s","update":{"sessionUpdate":"tool_call","toolCallId":"late"}}`))
	if err != nil || reply != "a b" || len(events) != 5 || events[0].CallID != "x" || events[1].Status != "failed" || events[1].Output != "denied" {
		t.Fatalf("reply=%q events=%+v err=%v", reply, events, err)
	}
}

func TestCodexAppServerActivityFiltersEarlyAndForeignTurns(t *testing.T) {
	c := &codexAppServerClient{responses: map[string]chan rpcEnvelope{}, done: make(chan struct{})}
	c.stdin = modelWire(func(msg rpcEnvelope) {
		for _, v := range []struct{ method, params string }{
			{"item/started", `{"threadId":"thread","turnId":"foreign","item":{"type":"commandExecution","id":"foreign","command":"wrong"}}`},
			{"item/started", `{"threadId":"thread","turnId":"live","item":{"type":"commandExecution","id":"cmd","command":"test"}}`},
			{"item/commandExecution/outputDelta", `{"threadId":"thread","turnId":"live","itemId":"cmd","delta":"log"}`},
			{"item/completed", `{"threadId":"thread","turnId":"live","item":{"type":"commandExecution","id":"cmd","aggregatedOutput":"log","exitCode":1,"status":"failed"}}`},
			{"turn/completed", `{"threadId":"thread","turn":{"id":"live","status":"completed","items":[{"id":"m","type":"agentMessage","text":"answer"}]}}`},
		} {
			c.notification(rpcEnvelope{Method: v.method, Params: json.RawMessage(v.params)})
		}
		c.responses[string(msg.ID)] <- rpcEnvelope{Result: json.RawMessage(`{"turn":{"id":"live"}}`)}
	})
	var got []Activity
	ctx := context.WithValue(context.Background(), activityContextKey{}, ActivitySink(func(e Activity) { got = append(got, e) }))
	reply, err := c.Prompt(ctx, "thread", "test")
	if err != nil || reply != "answer" || len(got) != 4 || got[3].TextID != "m" || !got[3].TextSnapshot || got[0].Input["command"] != "test" || !got[1].AppendOutput || got[2].Status != "failed" || !slices.ContainsFunc(got, func(e Activity) bool { return e.Output == "log" }) {
		t.Fatalf("reply=%q events=%+v err=%v", reply, got, err)
	}
}

type disconnectedToolClient struct {
	reconnectingACPClient
	calls int
}

func (c *disconnectedToolClient) Prompt(ctx context.Context, _, _ string) (string, error) {
	c.calls++
	ActivitySinkFromContext(ctx)(Activity{CallID: "edit", Status: "running"})
	return "", io.EOF
}

func TestACPDisconnectAfterToolDoesNotReplayPrompt(t *testing.T) {
	client := &disconnectedToolClient{reconnectingACPClient: reconnectingACPClient{sessionID: "native-session"}}
	store := NewStateStore(t.TempDir())
	b := NewBroker(store, map[AgentName]DetectionResult{AgentCursor: {Preferred: &AgentTransport{Agent: AgentCursor, Kind: TransportACP}}}, func(context.Context, CommandSpec, string) (ACPClient, error) { return client, nil })
	defer b.Close()
	_, err := b.SendTurn(context.Background(), TurnRequest{BQSessionID: "chat", Agent: AgentCursor, Prompt: "edit"})
	saved, readErr := store.Load("chat")
	if err == nil || client.calls != 1 || readErr != nil || saved.ExternalSessionID != "native-session" {
		t.Fatalf("calls=%d saved=%+v errors=%v,%v", client.calls, saved, err, readErr)
	}
}

func TestClaudeBlockIdentitiesPreserveTextOnBothSidesOfATool(t *testing.T) {
	frames := []string{
		`{"type":"stream_event","event":{"type":"message_start","message":{"id":"m"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"lead"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"tool","name":"read","input":{}}}}`,
		`{"type":"stream_event","event":{"type":"content_block_start","index":2,"content_block":{"type":"text","text":""}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"next"}}}`,
		`{"type":"assistant","message":{"id":"m","content":[{"type":"text","text":"next"}]}}`,
		`{"type":"assistant","message":{"id":"m","content":[{"type":"text","text":"lead"},{"type":"tool_use","id":"tool","name":"read","input":{"path":"f"}},{"type":"text","text":"next"}]}}`,
		`{"type":"result","result":"next","session_id":"s"}`,
	}
	var text []string
	_, _, err := readClaudeStream(strings.NewReader(strings.Join(frames, "\n")), func(e Activity) {
		if e.Text != "" {
			text = append(text, e.TextID+":"+e.Text)
		} else if e.CallID != "" {
			text = append(text, "tool:"+e.CallID)
		}
	})
	if err != nil || !slices.Equal(text, []string{"m/block/0:lead", "tool:tool", "m/block/2:next", "tool:tool"}) {
		t.Fatal(text, err)
	}
}
