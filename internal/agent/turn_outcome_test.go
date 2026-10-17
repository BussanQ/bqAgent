package agent

import (
	"context"
	"errors"
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
