package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"bqagent/internal/tools"
	"bqagent/internal/turn"
)

func decisionResponse(action string) AssistantMessage {
	data, _ := json.Marshal(investigationDecision{Action: action, Facts: "a.go:1 confirms the entry point", Deliverable: "fix the handler", NextStep: "edit a.go and run its focused test", Blocker: "need the handler signature", Report: "已确认入口；其余工作尚未完成。"})
	return AssistantMessage{Content: string(data)}
}

func investigationResponses(n int) []AssistantMessage {
	var responses []AssistantMessage
	for i := 0; i < n; i++ {
		call := investigationTestCall("read_file", map[string]any{"path": fmt.Sprintf("%d.go", i)})
		call.ID = fmt.Sprintf("read-%d", i)
		responses = append(responses, AssistantMessage{Content: "I will implement next.", ToolCalls: []ToolCall{call}})
	}
	return responses
}

func TestInvestigationEscalationDecisionRestoresTools(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			client := &stubClient{responses: investigationResponses(21)}
			edit := investigationTestCall("edit_file", map[string]any{"path": "a.go", "old_string": "old", "new_string": "new"})
			edit.ID = "edit"
			client.responses = append(client.responses, decisionResponse("implement"), AssistantMessage{ToolCalls: []ToolCall{edit}}, AssistantMessage{Content: "done"})
			reads, edits := 0, 0
			recorder := &capturingRecorder{}
			app := NewWithOptions(client, "test", Options{Stream: stream, Context: ContextConfig{Enabled: false}, Recorder: recorder, Functions: map[string]tools.Function{
				"read_file": func(context.Context, map[string]any) (string, error) { reads++; return "source", nil },
				"edit_file": func(context.Context, map[string]any) (string, error) { edits++; return "edited", nil },
			}})
			result, err := app.RunTurn(context.Background(), []map[string]any{{"role": "user", "content": "fix handler"}}, 30)
			if err != nil || result.Reply != "done" || reads != 21 || edits != 1 {
				t.Fatalf("result=%+v err=%v reads=%d edits=%d", result, err, reads, edits)
			}
			for i, opt := range client.options {
				if opt.DisableTools != (i == 21) {
					t.Fatalf("request %d DisableTools=%v", i, opt.DisableTools)
				}
			}
			for _, history := range [][]map[string]any{result.Messages, recorder.recorded, client.messages[22]} {
				found := false
				for _, m := range history {
					if strings.Contains(contentText(m["content"]), "调查结论：a.go:1") {
						found = true
					}
				}
				if !found {
					t.Fatal("decision facts were not persisted")
				}
			}
		})
	}
}

func TestInvestigationEscalationStopsAfterDecisionBudget(t *testing.T) {
	client := &stubClient{responses: investigationResponses(21)}
	client.responses = append(client.responses, decisionResponse("investigate"))
	client.responses = append(client.responses, investigationResponses(3)...)
	client.responses = append(client.responses, decisionResponse("blocked"))
	reads := 0
	app := NewWithOptions(client, "test", Options{Context: ContextConfig{Enabled: false}, Functions: map[string]tools.Function{"read_file": func(context.Context, map[string]any) (string, error) { reads++; return "source", nil }}})
	result, err := app.RunTurn(context.Background(), []map[string]any{{"role": "user", "content": "inspect"}}, 40)
	if err != nil || result.Outcome.Status != turn.Blocked || result.Outcome.Reason != "investigation_stalled" || reads != 24 || len(client.options) != 26 {
		t.Fatalf("result=%+v err=%v reads=%d calls=%d", result, err, reads, len(client.options))
	}
	if !client.options[21].DisableTools || !client.options[25].DisableTools {
		t.Fatal("decision or final report allowed tools")
	}
}

func TestInvestigationEscalationRejectsToolsAndMalformedDecisions(t *testing.T) {
	for _, response := range []AssistantMessage{{Content: "I'll keep looking"}, {ToolCalls: []ToolCall{{ID: "bad", Function: FunctionCall{Name: "edit_file", Arguments: `{"path":"a.go"}`}}}}, {Content: `{"action":"investigate","facts":"known"}`}} {
		client := &stubClient{responses: investigationResponses(21)}
		client.responses = append(client.responses, response)
		edits := 0
		app := NewWithOptions(client, "test", Options{Context: ContextConfig{Enabled: false}, Functions: map[string]tools.Function{
			"read_file": func(context.Context, map[string]any) (string, error) { return "source", nil },
			"edit_file": func(context.Context, map[string]any) (string, error) { edits++; return "edited", nil },
		}})
		result, err := app.RunTurn(context.Background(), []map[string]any{{"role": "user", "content": "fix"}}, 30)
		if err != nil || result.Outcome.Status != turn.Blocked || edits != 0 {
			t.Fatalf("result=%+v err=%v edits=%d", result, err, edits)
		}
		for _, m := range result.Messages {
			if calls, ok := m["tool_calls"].([]ToolCall); ok {
				for _, call := range calls {
					if call.ID == "bad" {
						t.Fatal("unexecuted decision tool saved")
					}
				}
			}
		}
	}
}

func TestInvestigationEscalationCannotResetWithPromisesOrOpaqueTools(t *testing.T) {
	for _, tool := range []string{"", "task_update", "bash"} {
		t.Run(tool, func(t *testing.T) {
			client := &stubClient{responses: investigationResponses(21)}
			client.responses = append(client.responses, decisionResponse("implement"))
			for i := 0; i < 3; i++ {
				response := AssistantMessage{Content: "I will implement now", Phase: "commentary"}
				if tool != "" {
					call := investigationTestCall(tool, map[string]any{"command": fmt.Sprintf("python3 inspect%d.py", i)})
					call.ID = fmt.Sprint(i)
					response.ToolCalls = []ToolCall{call}
				}
				client.responses = append(client.responses, response)
			}
			client.responses = append(client.responses, decisionResponse("blocked"))
			app := NewWithOptions(client, "test", Options{Context: ContextConfig{Enabled: false}, Functions: map[string]tools.Function{
				"read_file":   func(context.Context, map[string]any) (string, error) { return "source", nil },
				"task_update": func(context.Context, map[string]any) (string, error) { return "saved", nil },
				"bash":        func(context.Context, map[string]any) (string, error) { return "source", nil },
			}})
			result, err := app.RunTurn(context.Background(), []map[string]any{{"role": "user", "content": "fix"}}, 40)
			if err != nil || result.Outcome.Status != turn.Blocked || len(client.options) != 26 || !client.options[25].DisableTools {
				t.Fatalf("result=%+v err=%v calls=%d", result, err, len(client.options))
			}
		})
	}
}

func TestInvestigationEscalationCancellationWins(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := &stubClient{responses: investigationResponses(21)}
	client.responses = append(client.responses, decisionResponse("implement"))
	reads := 0
	app := NewWithOptions(client, "test", Options{Context: ContextConfig{Enabled: false}, Functions: map[string]tools.Function{
		"read_file": func(context.Context, map[string]any) (string, error) {
			reads++
			if reads == 21 {
				cancel()
			}
			return "source", nil
		},
	}})
	result, err := app.RunTurn(ctx, []map[string]any{{"role": "user", "content": "fix"}}, 30)
	if !errors.Is(err, context.Canceled) || result.Outcome.Status != turn.Interrupted || len(client.options) != 21 {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, len(client.options))
	}
}

func TestInvestigationEscalationReadOnlyCompletion(t *testing.T) {
	client := &stubClient{responses: investigationResponses(21)}
	response := decisionResponse("complete")
	response.Content = `{"action":"complete","facts":"a.go:1 proves the issue","report":"结论：入口未检查空值，证据为 a.go:1。"}`
	client.responses = append(client.responses, response)
	app := NewWithOptions(client, "test", Options{Context: ContextConfig{Enabled: false}, Functions: map[string]tools.Function{"read_file": func(context.Context, map[string]any) (string, error) { return "source", nil }}})
	result, err := app.RunTurn(context.Background(), []map[string]any{{"role": "user", "content": "review only"}}, 30)
	if err != nil || result.Outcome.Status != turn.Completed || !strings.HasPrefix(result.Reply, "结论：") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestInvestigationEscalationMilestoneLowersOnlyOneLevel(t *testing.T) {
	g := investigationGuard{limit: 12, level: 3, rounds: 2, decisionUsed: true}
	investigationTestRound(&g, guardCall("write_file", `{"path":"/tmp/plan","content":"todo"}`), "ok", true)
	if g.level != 3 {
		t.Fatal("temporary output reset escalation")
	}
	investigationTestRound(&g, guardCall("edit_file", `{"path":"a.go"}`), "ok", true)
	if g.level != 2 || g.rounds != 0 || g.decisionUsed {
		t.Fatalf("guard=%+v", g)
	}
	investigationTestRound(&g, guardCall("bash", `{"command":"go test ./x -run '^TestOne$'"}`), "ok", true)
	if g.level != 0 || g.edited {
		t.Fatalf("verified edit did not reset stage: %+v", g)
	}
}
