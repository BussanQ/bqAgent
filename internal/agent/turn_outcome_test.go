package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"bqagent/internal/tools"
	"bqagent/internal/turn"
)

func turnOutcomeMessage(status turn.Status, reason string) AssistantMessage {
	return AssistantMessage{ToolCalls: []ToolCall{{ID: "outcome", Function: FunctionCall{
		Name: "turn_outcome", Arguments: mustJSON(map[string]string{"status": string(status), "reason": reason}),
	}}}}
}

func TestTurnOutcomeDeclarationIndependentOfProgress(t *testing.T) {
	for _, status := range []turn.Status{turn.Blocked, turn.AwaitingInput} {
		for _, progress := range []string{"none", "save failure", "load failure", "suspended"} {
			t.Run(string(status)+"/"+progress, func(t *testing.T) {
				client := &taskDefinitionClient{}
				options := Options{}
				active := activeTestTask()
				switch progress {
				case "save failure", "suspended":
					options.TaskStore = &failingSaveStore{memoryTaskStore: &memoryTaskStore{state: &active}}
					count := 1
					if progress == "suspended" {
						count = 3
					}
					changed := active
					changed.Remaining = []string{"verify"}
					for i := 0; i < count; i++ {
						client.responses = append(client.responses, taskUpdateMessage(changed))
					}
				case "load failure":
					options.TaskStore = &memoryTaskStore{err: errors.New("disk unavailable")}
				}
				client.responses = append(client.responses, turnOutcomeMessage(status, "missing deployment address"), AssistantMessage{Content: "请补充部署地址。"})
				app := NewWithOptions(client, "test", options)
				result, err := app.RunTurn(context.Background(), []map[string]any{{"role": "user", "content": "deploy"}}, 10)
				if err != nil || result.Outcome.Status != status || result.Outcome.Reason != "missing deployment address" || result.Outcome.Notice == "" {
					t.Fatalf("result=%+v err=%v", result, err)
				}
				definitions := client.definitions[len(client.definitions)-2]
				found := false
				for _, definition := range definitions {
					found = found || definition.Function.Name == "turn_outcome"
					if progress == "suspended" && definition.Function.Name == "task_update" {
						t.Fatal("progress tool was not suspended")
					}
				}
				if !found {
					t.Fatal("outcome declaration unavailable")
				}
			})
		}
	}
}

func TestCurrentTaskObstacleAffectsTurnButHistoryAndFailedSaveDoNot(t *testing.T) {
	for _, status := range []turn.Status{turn.Blocked, turn.AwaitingInput} {
		for _, scenario := range []string{"current", "unchanged reassessment", "history", "failed save", "resolved"} {
			t.Run(string(status)+"/"+scenario, func(t *testing.T) {
				active := activeTestTask()
				blocked := active
				blocked.Status, blocked.Reason = string(status), "credentials required"
				memory := &memoryTaskStore{state: &active}
				client := &stubClient{}
				options := Options{TaskStore: memory}
				want := status
				switch scenario {
				case "history":
					memory.state, want = &blocked, turn.Completed
				case "failed save":
					options.TaskStore = &failingSaveStore{memoryTaskStore: memory}
					client.responses = append(client.responses, taskUpdateMessage(blocked))
					want = turn.Completed
				case "unchanged reassessment":
					memory.state = &blocked
					client.responses = append(client.responses, taskUpdateMessage(blocked))
				case "resolved":
					client.responses = append(client.responses, taskUpdateMessage(blocked), taskUpdateMessage(active))
					want = turn.Completed
				default:
					client.responses = append(client.responses, taskUpdateMessage(blocked))
				}
				client.responses = append(client.responses, AssistantMessage{Content: "result"})
				app := NewWithOptions(client, "test", options)
				result, err := app.RunTurn(context.Background(), []map[string]any{{"role": "user", "content": "continue"}}, 10)
				if err != nil || result.Outcome.Status != want {
					t.Fatalf("result=%+v err=%v", result, err)
				}
			})
		}
	}
}

func TestTurnOutcomeDeclarationClearsAndRuntimeStopsTakePrecedence(t *testing.T) {
	for _, scenario := range []string{"resolved", "new input", "runtime failure", "budget"} {
		t.Run(scenario, func(t *testing.T) {
			client := &stubClient{responses: []AssistantMessage{turnOutcomeMessage(turn.AwaitingInput, "address required")}}
			options := Options{}
			want := turn.Completed
			switch scenario {
			case "resolved":
				client.responses = append(client.responses, turnOutcomeMessage(turn.Completed, ""), AssistantMessage{Content: "done"})
			case "new input":
				client.responses = append(client.responses, AssistantMessage{ToolCalls: []ToolCall{{ID: "input", Function: FunctionCall{Name: "receive_input", Arguments: `{}`}}}}, AssistantMessage{Content: "done"})
				options.Functions = map[string]tools.Function{"receive_input": func(ctx context.Context, _ map[string]any) (string, error) {
					turnRun(ctx).acceptUserInput("address supplied")
					return "input received", nil
				}}
			case "runtime failure":
				want = turn.Failed
			case "budget":
				client.responses = append(client.responses, AssistantMessage{Content: "working", Phase: "commentary"})
				want = turn.BudgetLimited
			}
			app := NewWithOptions(client, "test", options)
			limit := 10
			if scenario == "budget" {
				limit = 2
			}
			result, err := app.RunTurn(context.Background(), nil, limit)
			if result.Outcome.Status != want || scenario != "runtime failure" && err != nil {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestExplicitTurnOutcomeTakesPrecedenceOverProgress(t *testing.T) {
	for _, resolved := range []bool{false, true} {
		active := activeTestTask()
		blocked := active
		blocked.Status, blocked.Reason = "blocked", "old obstacle"
		client := &stubClient{responses: []AssistantMessage{
			turnOutcomeMessage(turn.AwaitingInput, "address required"),
			taskUpdateMessage(active),
		}}
		want := turn.AwaitingInput
		if resolved {
			client.responses = append(client.responses, turnOutcomeMessage(turn.Completed, ""), taskUpdateMessage(blocked))
			want = turn.Completed
		}
		client.responses = append(client.responses, AssistantMessage{Content: "result"})
		app := NewWithOptions(client, "test", Options{TaskStore: &memoryTaskStore{state: &blocked}})
		result, err := app.RunTurn(context.Background(), nil, 10)
		if err != nil || result.Outcome.Status != want {
			t.Fatalf("resolved=%t result=%+v err=%v", resolved, result, err)
		}
	}
}

func TestTurnOutcomeCompletedRequiresCurrentObstacle(t *testing.T) {
	for _, scenario := range []string{"none", "blocked", "awaiting_input", "task obstacle", "already completed", "new input", "interrupted"} {
		t.Run(scenario, func(t *testing.T) {
			app := NewWithOptions(&stubClient{}, "test", Options{})
			ctx, run := evidenceRun()
			wantSuccess := false
			switch scenario {
			case "blocked", "awaiting_input":
				if _, err := app.functions["turn_outcome"](ctx, map[string]any{"status": scenario, "reason": "address required"}); err != nil {
					t.Fatal(err)
				}
				wantSuccess = true
			case "task obstacle":
				run.declareOutcome(turn.Blocked, "address required", false)
				wantSuccess = true
			case "already completed":
				run.declareOutcome(turn.Blocked, "address required", true)
				run.declareOutcome(turn.Completed, "", true)
			case "new input":
				run.declareOutcome(turn.AwaitingInput, "address required", true)
				run.acceptUserInput("address supplied")
			case "interrupted":
				run.declareOutcome(turn.Interrupted, "cancelled", false)
			}
			before := run.declaration
			_, err := app.functions["turn_outcome"](ctx, map[string]any{"status": "completed"})
			if wantSuccess {
				if err != nil || run.declaration == nil || run.declaration.status != turn.Completed || !run.declaration.explicit {
					t.Fatalf("obstacle not cleared: declaration=%+v err=%v", run.declaration, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "Give the final answer directly") || run.declaration != before {
				t.Fatalf("invalid completion changed declaration: before=%+v after=%+v err=%v", before, run.declaration, err)
			}
		})
	}
}

func TestTurnOutcomeUnnecessaryCompletionDoesNotBlockAnswer(t *testing.T) {
	for _, historical := range []bool{false, true} {
		options := Options{}
		if historical {
			task := activeTestTask()
			task.Status, task.Reason = "blocked", "old obstacle"
			options.TaskStore = &memoryTaskStore{state: &task}
		}
		client := &stubClient{responses: []AssistantMessage{
			turnOutcomeMessage(turn.Completed, ""),
			{Content: "nerdctl ps -q | xargs -r nerdctl stop"},
		}}
		app := NewWithOptions(client, "test", options)
		result, err := app.RunTurn(context.Background(), []map[string]any{{"role": "user", "content": "nerdctl 停止所有容器的命令"}}, 4)
		if err != nil || result.Outcome.Status != turn.Completed || result.Reply != "nerdctl ps -q | xargs -r nerdctl stop" || len(client.messages) != 2 {
			t.Fatalf("history=%t result=%+v calls=%d err=%v", historical, result, len(client.messages), err)
		}
		found := false
		for _, message := range client.messages[1] {
			if message["role"] == "tool" && strings.Contains(contentText(message["content"]), "completed requires an existing blocked/awaiting_input declaration") {
				found = true
			}
		}
		if !found {
			t.Fatalf("history=%t unnecessary completion was not rejected", historical)
		}
	}
}

func TestTurnOutcomeDeclarationRequiresConcreteReasonAndValidStatus(t *testing.T) {
	app := NewWithOptions(&stubClient{}, "test", Options{})
	ctx, run := evidenceRun()
	for _, args := range []map[string]any{
		{"status": "blocked"}, {"status": "awaiting_input", "reason": " "},
		{"status": "failed", "reason": "invented"}, {"status": "unknown"},
	} {
		if _, err := app.functions["turn_outcome"](ctx, args); err == nil || run.declaration != nil {
			t.Fatalf("invalid declaration accepted: %v err=%v", args, err)
		}
	}
	if !app.hasSpecialToolCalls([]ToolCall{{Function: FunctionCall{Name: "turn_outcome"}}}) {
		t.Fatal("outcome declarations must run in model order")
	}
}

func TestDisableTurnOutcomeHidesToolAndInstructions(t *testing.T) {
	for _, withTask := range []bool{false, true} {
		t.Run(fmt.Sprintf("task=%t", withTask), func(t *testing.T) {
			options := Options{DisableTurnOutcome: true, Context: ContextConfig{Enabled: false}}
			if withTask {
				options.TaskStore = &memoryTaskStore{}
			}
			client := &taskDefinitionClient{stubClient{responses: []AssistantMessage{{Content: "done"}}}}
			app := NewWithOptions(client, "test", options)
			if _, ok := app.functions["turn_outcome"]; ok {
				t.Fatal("turn_outcome was installed")
			}
			if _, history, err := app.RunConversationTurn(context.Background(), []map[string]any{{"role": "user", "content": "hi"}}, 3); err != nil || len(history) == 0 {
				t.Fatalf("turn failed: history=%#v err=%v", history, err)
			}
			for _, definition := range client.definitions[0] {
				if definition.Function.Name == "turn_outcome" {
					t.Fatal("turn_outcome was exposed to the model")
				}
			}
			text := app.taskContext(context.Background())
			if strings.Contains(text, "turn_outcome") {
				t.Fatalf("turn_outcome instructions injected: %q", text)
			}
			if !withTask {
				if text != "" {
					t.Fatalf("task context without task store = %q, want empty", text)
				}
				return
			}
			if !strings.HasPrefix(text, taskProgressInstructions) || strings.Contains(text, turnOutcomeTaskInstructions) {
				t.Fatalf("task context = %q", text)
			}
		})
	}
}
