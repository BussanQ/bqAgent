package extagent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestCodexNativePlanOnlyTurn(t *testing.T) {
	for _, tc := range []struct{ name, items, status, reply string }{
		{"deltas", "[]", "completed", "Draft plan"},
		{"revised", `[{"type":"plan","id":"p","text":"Revised plan"}]`, "completed", "Revised plan"},
		{"final", `[{"type":"agentMessage","id":"m","phase":"final_answer","text":"Final answer"}]`, "completed", "Final answer"},
		{"legacy answer", `[{"type":"agentMessage","id":"m","text":"Legacy answer"}]`, "completed", "Legacy answer"},
		{"interrupted", "[]", "interrupted", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &codexAppServerClient{responses: map[string]chan rpcEnvelope{}, done: make(chan struct{})}
			c.stdin = modelWire(func(msg rpcEnvelope) {
				c.responses[string(msg.ID)] <- rpcEnvelope{Result: json.RawMessage(`{"turn":{"id":"live"}}`)}
				notify := func(method, params string) {
					c.notification(rpcEnvelope{Method: method, Params: json.RawMessage(params)})
				}
				const run = `{"threadId":"thread","turnId":"live",`
				notify("turn/plan/updated", `{"threadId":"thread","turnId":"foreign","plan":[{"step":"wrong"}]}`)
				notify("turn/plan/updated", run+`"explanation":"missing steps"}`)
				notify("turn/plan/updated", run+`"explanation":"why","plan":[{"step":"one","status":"inProgress"}]}`)
				notify("turn/plan/updated", run+`"plan":[]}`)
				notify("item/started", run+`"item":{"type":"plan","id":"p","text":""}}`)
				notify("item/plan/delta", run+`"itemId":"p","delta":"Draft "}`)
				notify("item/plan/delta", run+`"itemId":"p","delta":"plan"}`)
				notify("turn/completed", `{"threadId":"thread","turn":{"id":"live","status":"`+tc.status+`","items":`+tc.items+`}}`)
			})
			var plans []*PlanUpdate
			ctx := context.WithValue(context.Background(), activityContextKey{}, ActivitySink(func(e Activity) {
				if e.Plan != nil {
					plans = append(plans, e.Plan)
				}
			}))
			reply, err := c.Prompt(ctx, "thread", "plan")
			if reply != tc.reply || (err != nil) != (tc.status == "interrupted") {
				t.Fatalf("reply=%q error=%v", reply, err)
			}
			wantCount := 5
			if tc.name == "revised" {
				wantCount++
			}
			if len(plans) != wantCount || plans[0].ID != "steps" || plans[0].Plan.Steps[0].Status != "in_progress" ||
				len(plans[1].Plan.Steps) != 0 || plans[2].ID != "item/p" || !plans[3].Append || plans[3].Text+plans[4].Text != "Draft plan" {
				t.Fatalf("plan updates: %+v", plans)
			}
			if tc.name == "revised" && (plans[5].Append || plans[5].Text != tc.reply) {
				t.Fatal(plans[5])
			}
		})
	}
}

func TestACPPlanReplacesStepsAndClearsPerSession(t *testing.T) {
	c := &stdioACPClient{responses: map[string]chan rpcEnvelope{}, collectors: map[string]*strings.Builder{}}
	c.stdin = modelWire(func(msg rpcEnvelope) {
		for _, update := range []string{
			`{"sessionId":"foreign","update":{"sessionUpdate":"plan","entries":[{"content":"wrong"}]}}`,
			`{"sessionId":"s","update":{"sessionUpdate":"plan"}}`,
			`{"sessionId":"s","update":{"sessionUpdate":"plan","entries":[{"content":"old","status":"pending","priority":"low"}]}}`,
			`{"sessionId":"s","update":{"sessionUpdate":"plan","entries":[]}}`,
			`{"sessionId":"s","update":{"sessionUpdate":"plan","entries":[{"content":"new","status":"completed","priority":"high"}]}}`,
		} {
			c.handleSessionUpdate(json.RawMessage(update))
		}
		c.responses[string(msg.ID)] <- rpcEnvelope{Result: json.RawMessage(`{}`)}
	})
	var plans []*PlanUpdate
	ctx := context.WithValue(context.Background(), activityContextKey{}, ActivitySink(func(e Activity) { plans = append(plans, e.Plan) }))
	reply, err := c.Prompt(ctx, "s", "plan")
	if err != nil || reply != "- [x] new" || len(plans) != 3 || len(plans[1].Plan.Steps) != 0 || plans[2].Plan.Steps[0].Priority != "high" || len(c.plans) != 0 {
		t.Fatalf("reply=%q plans=%+v err=%v", reply, plans, err)
	}
}

func TestCodexCLITodoListReply(t *testing.T) {
	frames := []string{
		`{"type":"thread.started","thread_id":"t"}`,
		`{"type":"item.started","item":{"type":"todo_list","id":"list","items":[{"text":"old","completed":false}]}}`,
		`{"type":"item.updated","item":{"type":"todo_list","id":"list","items":[]}}`,
		`{"type":"item.completed","item":{"type":"todo_list","id":"list","items":[{"text":"new","completed":true}]}}`,
	}
	for _, final := range []bool{false, true} {
		want := "- [x] new"
		if final {
			frames = append(frames, `{"type":"item.completed","item":{"type":"agent_message","id":"m","text":"answer"}}`)
			want = "answer"
		}
		var plans []*PlanUpdate
		reply, id, err := readCodexCLI(strings.NewReader(strings.Join(frames, "\n")), func(e Activity) {
			if e.Plan != nil {
				plans = append(plans, e.Plan)
			}
		})
		if err != nil || reply != want || id != "t" || len(plans) != 3 || plans[0].ID != "todo/list" || len(plans[1].Plan.Steps) != 0 {
			t.Fatalf("reply=%q id=%s err=%v plans=%+v", reply, id, err, plans)
		}
	}
}
