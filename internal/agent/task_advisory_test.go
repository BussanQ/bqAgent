package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"bqagent/internal/taskstate"
	"bqagent/internal/tools"
	"bqagent/internal/turn"
)

type taskDefinitionClient struct{ stubClient }

func (client *taskDefinitionClient) CreateChatCompletionWithOptions(ctx context.Context, model string, messages []map[string]any, definitions []tools.Definition, _ ChatCompletionOptions) (AssistantMessage, error) {
	return client.CreateChatCompletion(ctx, model, messages, definitions)
}

func (client *taskDefinitionClient) CreateChatCompletionStreamWithOptions(ctx context.Context, model string, messages []map[string]any, definitions []tools.Definition, _ ChatCompletionOptions, _ func(string)) (AssistantMessage, error) {
	return client.CreateChatCompletion(ctx, model, messages, definitions)
}

func TestTaskRecordingFailureDoesNotBlockActualWork(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, failure := range []string{"evidence mismatch", "save failure"} {
			t.Run(fmt.Sprintf("stream=%t/%s", stream, failure), func(t *testing.T) {
				active := activeTestTask()
				memory := &memoryTaskStore{state: &active}
				var store taskstate.Store = memory
				command := "go test ./x -run '^(TestA|TestB)$'"
				recorded := command
				if failure == "save failure" {
					store = &failingSaveStore{memoryTaskStore: memory}
				} else {
					recorded = "go test ./x -run TestDifferent"
				}
				complete := completedWith(taskstate.Verification{Command: recorded, Status: "passed", Details: "ok"})
				client := &taskDefinitionClient{stubClient{responses: []AssistantMessage{
					{ToolCalls: []ToolCall{editCall("edit")}},
					{ToolCalls: []ToolCall{bashCall("verify", command)}},
					taskUpdateMessage(complete), taskUpdateMessage(complete), taskUpdateMessage(complete),
					{ToolCalls: []ToolCall{{ID: "read", Function: FunctionCall{Name: "read_file", Arguments: `{"path":"a.go"}`}}}},
					{Content: "修改及验证已完成；进度登记失败。", Phase: "final_answer"},
				}}}
				edits, checks, reads := 0, 0, 0
				app := NewWithOptions(client, "test", Options{TaskStore: store, Stream: stream,
					Stage: StageConfig{LoopProtection: true, RepeatedFailureLimit: 2, DuplicateCallLimit: 2},
					Functions: map[string]tools.Function{
						"edit_file":    func(context.Context, map[string]any) (string, error) { edits++; return "edited", nil },
						"execute_bash": func(context.Context, map[string]any) (string, error) { checks++; return "ok", nil },
						"read_file":    func(context.Context, map[string]any) (string, error) { reads++; return "source", nil },
					}})
				result, err := app.RunTurn(context.Background(), []map[string]any{{"role": "user", "content": active.Goal}}, 12)
				if err != nil || result.Outcome.Status != turn.Completed || result.Outcome.Reason != "" || edits != 1 || checks != 1 || reads != 1 || len(client.messages) != 7 || !strings.Contains(result.Reply, "修改及验证已完成") {
					t.Fatalf("result=%+v edits=%d checks=%d reads=%d calls=%d err=%v", result, edits, checks, reads, len(client.messages), err)
				}
				if memory.state.Status != "active" {
					t.Fatalf("unproven completion was persisted: %+v", memory.state)
				}
				for _, step := range []struct {
					request int
					present bool
				}{{4, true}, {5, false}, {6, true}} {
					present := false
					for _, definition := range client.definitions[step.request] {
						present = present || definition.Function.Name == "task_update"
					}
					if present != step.present {
						t.Fatalf("task_update availability at request %d = %t, want %t", step.request, present, step.present)
					}
				}
			})
		}
	}
}

func TestTaskUpdateSuspensionOnlyHidesBookkeepingUntilProgress(t *testing.T) {
	for _, result := range []string{"Task state saved.", "Error: evidence mismatch", "Error: Invalid JSON arguments for tool task_update"} {
		t.Run(result, func(t *testing.T) {
			guard := newLoopGuard(StageConfig{LoopProtection: true, RepeatedFailureLimit: 2, DuplicateCallLimit: 2})
			definitions := []tools.Definition{
				{Function: tools.FunctionDefinition{Name: "task_update"}},
				{Function: tools.FunctionDefinition{Name: "edit_file"}},
			}
			call := taskUpdateStallCall("update", 1)
			for i := 0; i < 6; i++ {
				if action := guard.observeTool(call, result); action.checkpointReason != "" {
					t.Fatalf("progress recording stopped work: %+v", action)
				}
			}
			filtered := guard.filterDefinitions(definitions)
			if len(filtered) != 1 || filtered[0].Function.Name != "edit_file" || len(definitions) != 2 {
				t.Fatalf("suspension removed substantive tools or mutated original definitions: %+v", filtered)
			}
			guard.observeTool(ToolCall{Function: FunctionCall{Name: "read_file", Arguments: `{"path":"a.go"}`}}, "source")
			if guard.taskUpdateSuspended() || len(guard.filterDefinitions(definitions)) != 2 {
				t.Fatal("substantive progress did not restore bookkeeping")
			}
		})
	}
}
