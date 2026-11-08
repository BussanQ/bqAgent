package extagent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestCodexOnlyEmitsPublicSummaryFramesForAcceptedTurn(t *testing.T) {
	c := &codexAppServerClient{responses: map[string]chan rpcEnvelope{}, done: make(chan struct{})}
	c.stdin = modelWire(func(msg rpcEnvelope) {
		send := func(method, params string) {
			c.notification(rpcEnvelope{Method: method, Params: json.RawMessage(params)})
		}
		send("item/reasoning/summaryTextDelta", `{"threadId":"thread","turnId":"foreign","itemId":"r","delta":"private foreign","summaryIndex":0}`)
		send("item/reasoning/summaryTextDelta", `{"threadId":"other","turnId":"live","itemId":"r","delta":"private thread","summaryIndex":0}`)
		send("item/started", `{"threadId":"thread","turnId":"live","item":{"id":"r","type":"reasoning","content":["private raw"],"encryptedContent":"private encrypted"}}`)
		send("item/reasoning/textDelta", `{"threadId":"thread","turnId":"live","itemId":"r","delta":"private delta","contentIndex":0}`)
		send("item/reasoning/summaryTextDelta", `{"threadId":"thread","turnId":"live","itemId":"r","delta":"private unindexed"}`)
		send("item/reasoning/summaryPartAdded", `{"threadId":"thread","turnId":"live","itemId":"r","summaryIndex":1}`)
		send("item/reasoning/summaryTextDelta", `{"threadId":"thread","turnId":"live","itemId":"r","delta":"public second","summaryIndex":1}`)
		send("item/completed", `{"threadId":"thread","turnId":"live","item":{"id":"r","type":"reasoning","content":["private raw"],"summary":["first","second"]}}`)
		send("turn/completed", `{"threadId":"thread","turn":{"id":"live","status":"completed","items":[{"id":"r","type":"reasoning","summary":["first","revised"],"content":["private raw"]},{"id":"m","type":"agentMessage","text":"answer","summary":{"unrelated":true}}]}}`)
		c.responses[string(msg.ID)] <- rpcEnvelope{Result: json.RawMessage(`{"turn":{"id":"live"}}`)}
	})
	var got []Activity
	ctx := context.WithValue(t.Context(), activityContextKey{}, ActivitySink(func(e Activity) { got = append(got, e) }))
	reply, err := c.Prompt(ctx, "thread", "hello")
	if err != nil || reply != "answer" || len(got) != 4 || got[0].Summary == nil || got[0].Summary.Index != 1 || got[0].Summary.Delta != "public second" ||
		got[1].Summary == nil || !got[1].Summary.Ended || got[2].Summary == nil || got[2].Summary.Sections[1] != "revised" || got[3].Text != "answer" {
		t.Fatal(reply, err, got)
	}
	data, _ := json.Marshal(got)
	if strings.Contains(string(data), "private") {
		t.Fatal("raw or foreign event reached the activity stream")
	}
}

func TestCodexCLIJSONSummaryIsSeparateFromFinalReplyWithAndWithoutObserver(t *testing.T) {
	for _, tc := range []struct {
		name             string
		observe, missing bool
	}{
		{"live", true, false}, {"no observer", false, false}, {"summary only", true, true}, {"unobserved summary only", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mode := "cli-codex-summary"
			if tc.missing {
				mode = "cli-codex-summary-only"
			}
			spec := helperSpec(t, mode)
			spec.Args = append(spec.Args, "--json")
			ctx := t.Context()
			var updates []Activity
			if tc.observe {
				ctx = context.WithValue(ctx, activityContextKey{}, ActivitySink(func(e Activity) { updates = append(updates, e) }))
			}
			got, err := (CLIAdapter{}).SendTurn(ctx, spec, SessionState{Agent: AgentCodex}, t.TempDir(), "read")
			if got.State.ExternalSessionID != "summary-thread" || (err != nil) != tc.missing {
				t.Fatal(got, err)
			}
			if tc.missing && (got.Reply != "" || !strings.Contains(err.Error(), "empty final")) || !tc.missing && got.Reply != "final answer" {
				t.Fatal(got, err)
			}
			if tc.observe {
				encoded, _ := json.Marshal(updates)
				if len(updates) < 2 || updates[0].Summary.Sections[0] != "draft" || updates[1].Summary.Sections[0] != "public summary" ||
					!updates[1].Summary.Ended || strings.Contains(string(encoded), "private") {
					t.Fatal(string(encoded))
				}
			}
		})
	}
}
