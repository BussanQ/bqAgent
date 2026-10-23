package agent

import (
	"context"
	"testing"

	"bqagent/internal/tools"
	"bqagent/internal/turn"
)

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

func TestTaskObstacleClearsOnNewInputAndRuntimeStopsTakePrecedence(t *testing.T) {
	for _, scenario := range []string{"new input", "runtime failure", "budget"} {
		t.Run(scenario, func(t *testing.T) {
			active := activeTestTask()
			blocked := active
			blocked.Status, blocked.Reason = "awaiting_input", "address required"
			client := &stubClient{responses: []AssistantMessage{taskUpdateMessage(blocked)}}
			options := Options{TaskStore: &memoryTaskStore{state: &active}}
			want := turn.Completed
			switch scenario {
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

func TestTaskContextRequiresTaskStore(t *testing.T) {
	if text := NewWithOptions(&stubClient{}, "test", Options{}).taskContext(context.Background()); text != "" {
		t.Fatalf("task context without task store = %q, want empty", text)
	}
	app := NewWithOptions(&stubClient{}, "test", Options{TaskStore: &memoryTaskStore{}})
	if text := app.taskContext(context.Background()); text != taskProgressInstructions {
		t.Fatalf("task context = %q, want only task tracking instructions", text)
	}
}
