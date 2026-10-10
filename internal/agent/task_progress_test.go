package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"bqagent/internal/taskstate"
	"bqagent/internal/tools"
)

type memoryTaskStore struct {
	state *taskstate.State
	err   error
}

func TestRunCloseoutReviewForUnregisteredLongTask(t *testing.T) {
	client := &stubClient{responses: []AssistantMessage{
		{ToolCalls: []ToolCall{{ID: "read1", Function: FunctionCall{Name: "read_file", Arguments: `{}`}}}},
		{ToolCalls: []ToolCall{{ID: "read2", Function: FunctionCall{Name: "read_file", Arguments: `{}`}}}},
		{Content: "I will implement it"}, taskUpdateMessage(activeTestTask()),
		{Content: "still working"}, {Content: "still working"},
	}}
	store := &memoryTaskStore{}
	app := NewWithOptions(client, "test", Options{TaskStore: store, Functions: map[string]tools.Function{"read_file": func(context.Context, map[string]any) (string, error) { return "source", nil }}})
	reply, err := app.Run(context.Background(), "implement fast", 20)
	if err != nil || !strings.Contains(reply, "尚未确认完成") || store.state == nil || store.state.Status != "active" {
		t.Fatalf("reply=%q task=%+v err=%v", reply, store.state, err)
	}
	found := false
	for _, m := range client.messages[3] {
		if strings.Contains(contentText(m["content"]), "Before ending, review") {
			found = true
		}
	}
	if !found {
		t.Fatal("missing closeout review")
	}
}

func TestRunContinuationTotalBudgetAndStreamPause(t *testing.T) {
	var responses []AssistantMessage
	for i := 0; i < 4; i++ {
		responses = append(responses, AssistantMessage{ToolCalls: []ToolCall{{ID: "read", Function: FunctionCall{Name: "read_file", Arguments: `{}`}}}}, AssistantMessage{Content: "working", Phase: "commentary"})
	}
	client := &stubClient{responses: responses}
	var output bytes.Buffer
	app := NewWithOptions(client, "test", Options{Stream: true, TokenSink: &output, Functions: map[string]tools.Function{"read_file": func(context.Context, map[string]any) (string, error) { return "source", nil }}})
	reply, err := app.Run(context.Background(), "work", 50)
	if err != nil || len(client.messages) != 8 || !strings.Contains(reply, "尚未确认完成") || !strings.Contains(output.String(), reply) {
		t.Fatalf("calls=%d reply=%q output=%q err=%v", len(client.messages), reply, output.String(), err)
	}
}

func TestRunContinuationHonorsIterationAndCancellationBudgets(t *testing.T) {
	client := &stubClient{responses: []AssistantMessage{{Content: "working", Phase: "commentary"}, {Content: "done"}}}
	app := NewWithOptions(client, "test", Options{})
	if _, err := app.Run(context.Background(), "work", 1); err != nil || len(client.messages) != 1 {
		t.Fatalf("iteration budget ignored: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client = &stubClient{responses: []AssistantMessage{{Content: "working", Phase: "commentary"}, {ToolCalls: []ToolCall{{ID: "work", Function: FunctionCall{Name: "edit_file", Arguments: `{}`}}}}, {Content: "done"}}}
	app = NewWithOptions(client, "test", Options{Functions: map[string]tools.Function{"edit_file": func(context.Context, map[string]any) (string, error) { cancel(); return "cancelled", context.Canceled }}})
	if _, err := app.Run(ctx, "work", 10); !errors.Is(err, context.Canceled) || len(client.messages) != 2 {
		t.Fatalf("cancellation ignored: %v calls=%d", err, len(client.messages))
	}
	client = &stubClient{responses: []AssistantMessage{{Content: "partial", Completion: CompletionState{StopReason: "incomplete", IncompleteReason: "content_filter"}}}}
	app = NewWithOptions(client, "test", Options{})
	if _, err := app.Run(context.Background(), "work", 10); err == nil || len(client.messages) != 1 {
		t.Fatal("non-recoverable incomplete treated as completion")
	}
}

func TestPlannedTaskChecksOverallCompletionAtLastStep(t *testing.T) {
	active := activeTestTask()
	store := &memoryTaskStore{state: &active}
	complete := taskstate.State{Goal: active.Goal, Status: "completed", Criteria: []taskstate.Criterion{{Description: "switch works", Done: true, Evidence: "test output ok"}}, Verification: []taskstate.Verification{{Command: "test", Status: "passed", Details: "ok"}}}
	client := &stubClient{responses: []AssistantMessage{
		{Content: `{"steps":["implement","verify"]}`},
		{Content: "implementation done"},
		{ToolCalls: []ToolCall{{ID: "verify", Function: FunctionCall{Name: "execute_bash", Arguments: `{}`}}}},
		taskUpdateMessage(complete), {Content: "verified"},
	}}
	app := NewWithOptions(client, "test", Options{TaskStore: store, Planner: NewPlanner(client, "test"), Functions: map[string]tools.Function{"execute_bash": func(context.Context, map[string]any) (string, error) { return "ok", nil }}})
	reply, _, err := app.RunPlannedConversationTurn(context.Background(), []map[string]any{{"role": "user", "content": active.Goal}}, active.Goal, 10)
	if err != nil || reply != "implementation done\nverified" || store.state.Status != "completed" || len(client.messages) != 5 {
		t.Fatalf("reply=%q status=%s calls=%d err=%v", reply, store.state.Status, len(client.messages), err)
	}
}

func TestPlannedTaskStopsAfterBoundedCommentary(t *testing.T) {
	client := &stubClient{responses: []AssistantMessage{{Content: `{"steps":["implement","verify"]}`}, {Content: "working", Phase: "commentary"}, {Content: "working", Phase: "commentary"}, {Content: "working", Phase: "commentary"}}}
	app := NewWithOptions(client, "test", Options{Planner: NewPlanner(client, "test")})
	reply, _, err := app.RunPlannedConversationTurn(context.Background(), nil, "task", 10)
	if err != nil || !strings.Contains(reply, "尚未确认完成") || len(client.messages) != 4 {
		t.Fatalf("reply=%q calls=%d err=%v", reply, len(client.messages), err)
	}
}

func TestTaskSnapshotSurvivesContextCompaction(t *testing.T) {
	active := activeTestTask()
	app := NewWithOptions(&stubClient{}, "test", Options{TaskStore: &memoryTaskStore{state: &active}, Context: ContextConfig{Enabled: true, TargetInputTokens: 1200, MaxInputTokens: 2000}})
	app.task = &active
	messages := []map[string]any{{"role": "user", "content": "implement fast"}, {"role": "assistant", "content": strings.Repeat("older work ", 2000)}, {"role": "user", "content": "continue"}}
	request, compacted := app.buildRequestMessages(context.Background(), messages, nil, ChatCompletionOptions{})
	if compacted == nil {
		t.Fatal("expected compaction")
	}
	count := func(messages []map[string]any) int {
		n := 0
		for _, m := range messages {
			if strings.HasPrefix(contentText(m["content"]), taskProgressInstructions) {
				n++
			}
		}
		return n
	}
	if count(request) != 1 || count(compacted) != 0 {
		t.Fatal("task snapshot absent or persisted in working history")
	}
	request, _ = app.buildRequestMessages(context.Background(), compacted, nil, ChatCompletionOptions{})
	if count(request) != 1 {
		t.Fatal("task context duplicated on resume")
	}
}

func (s *memoryTaskStore) LoadTaskState() (*taskstate.State, error) { return s.state, s.err }
func (s *memoryTaskStore) SaveTaskState(state taskstate.State) error {
	if s.err != nil {
		return s.err
	}
	s.state = &state
	return nil
}
func activeTestTask() taskstate.State {
	return taskstate.State{Goal: "implement fast", Status: "active", Criteria: []taskstate.Criterion{{Description: "switch works"}}, Remaining: []string{"implement", "verify"}}
}
func taskUpdateMessage(state taskstate.State) AssistantMessage {
	raw, _ := json.Marshal(state)
	args, _ := json.Marshal(map[string]any{"state": string(raw)})
	return AssistantMessage{Role: "assistant", ToolCalls: []ToolCall{{ID: "task-1", Type: "function", Function: FunctionCall{Name: "task_update", Arguments: string(args)}}}}
}

func TestRunContinuesCommentaryAndIncompleteText(t *testing.T) {
	for _, first := range []AssistantMessage{
		{Role: "assistant", Content: "I will implement it", Phase: "commentary"},
		{Role: "assistant", Content: "partial answer", Completion: CompletionState{StopReason: "incomplete", OutputTruncated: true, IncompleteReason: "max_output_tokens"}},
	} {
		client := &stubClient{responses: []AssistantMessage{first, {Role: "assistant", ToolCalls: []ToolCall{{ID: "work", Function: FunctionCall{Name: "edit_file", Arguments: `{}`}}}}, {Role: "assistant", Content: "done", Phase: "final_answer"}}}
		worked := 0
		app := NewWithOptions(client, "test", Options{Functions: map[string]tools.Function{"edit_file": func(context.Context, map[string]any) (string, error) { worked++; return "edited", nil }}})
		reply, err := app.Run(context.Background(), "implement", 10)
		if err != nil || reply != "done" || worked != 1 || len(client.messages) != 3 {
			t.Fatalf("reply=%q worked=%d calls=%d err=%v", reply, worked, len(client.messages), err)
		}
		if client.messages[1][len(client.messages[1])-1]["role"] != "developer" {
			t.Fatal("continuation impersonated user")
		}
	}
}

func TestRunTaskCompletionRequiresAcceptanceAndVerification(t *testing.T) {
	active := activeTestTask()
	store := &memoryTaskStore{state: &active}
	invalid := active
	invalid.Status = "completed"
	complete := taskstate.State{Goal: active.Goal, Status: "completed", Criteria: []taskstate.Criterion{{Description: "switch works", Done: true, Evidence: "targeted test passed"}}, Verification: []taskstate.Verification{{Command: "go test -run TestSwitch", Status: "passed", Details: "ok"}}}
	client := &stubClient{responses: []AssistantMessage{{Role: "assistant", Content: "will test"}, taskUpdateMessage(invalid), {Role: "assistant", ToolCalls: []ToolCall{{ID: "test", Function: FunctionCall{Name: "execute_bash", Arguments: `{}`}}}}, taskUpdateMessage(complete), {Role: "assistant", Content: "done"}}}
	app := NewWithOptions(client, "test", Options{TaskStore: store, Functions: map[string]tools.Function{"execute_bash": func(context.Context, map[string]any) (string, error) { return "ok", nil }}})
	reply, err := app.Run(context.Background(), "改完了吗", 10)
	if err != nil || reply != "done" || store.state.Status != "completed" {
		t.Fatalf("reply=%q task=%+v err=%v", reply, store.state, err)
	}
	if len(client.messages) != 5 {
		t.Fatalf("calls=%d", len(client.messages))
	}
	found := false
	for _, message := range client.messages[2] {
		if strings.Contains(contentText(message["content"]), "acceptance criteria remain unfinished") {
			found = true
		}
	}
	if !found {
		t.Fatal("invalid completion was not rejected")
	}
}

func TestRunContinuationLimitsAndOrdinaryAnswers(t *testing.T) {
	for _, status := range []string{"", "active", "blocked", "awaiting_input", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			store := &memoryTaskStore{}
			if status != "" {
				task := activeTestTask()
				task.Status = status
				task.Reason = "needs user decision"
				store.state = &task
			}
			client := &stubClient{responses: []AssistantMessage{{Role: "assistant", Content: "answer"}, {Role: "assistant", Content: "answer"}, {Role: "assistant", Content: "answer"}}}
			app := NewWithOptions(client, "test", Options{TaskStore: store})
			reply, err := app.Run(context.Background(), "status", 20)
			if err != nil {
				t.Fatal(err)
			}
			if status == "active" {
				if !strings.Contains(reply, "尚未确认完成") || len(client.messages) != 3 || store.state.Status != "active" {
					t.Fatalf("reply=%q calls=%d", reply, len(client.messages))
				}
			} else if reply != "answer" || len(client.messages) != 1 {
				t.Fatalf("ordinary/blocked answer retried: %q calls=%d", reply, len(client.messages))
			}
		})
	}
	client := &stubClient{responses: []AssistantMessage{{Content: "working", Phase: "commentary"}, {Content: "working", Phase: "commentary"}, {Content: "working", Phase: "commentary"}}}
	app := NewWithOptions(client, "test", Options{})
	reply, err := app.Run(context.Background(), "work", 20)
	if err != nil || !strings.Contains(reply, "尚未确认完成") || len(client.messages) != 3 {
		t.Fatalf("commentary limit: %q %v", reply, err)
	}
}

func TestTaskStateErrorsAndCancellationStopWork(t *testing.T) {
	store := &memoryTaskStore{err: errors.New("disk failure")}
	client := &stubClient{}
	app := NewWithOptions(client, "test", Options{TaskStore: store})
	if _, err := app.Run(context.Background(), "work", 10); err == nil || len(client.messages) != 0 {
		t.Fatal("task load error ignored")
	}
	store.err = nil
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := app.Run(ctx, "work", 10); !errors.Is(err, context.Canceled) || len(client.messages) != 0 {
		t.Fatalf("cancel err=%v", err)
	}
	active := activeTestTask()
	store.state = &active
	app.task = &active
	replaced := active
	replaced.Goal = "unrelated"
	raw, _ := json.Marshal(replaced)
	if _, err := app.functions["task_update"](context.Background(), map[string]any{"state": string(raw)}); err == nil {
		t.Fatal("goal silently replaced")
	}
	store.err = errors.New("disk full")
	raw, _ = json.Marshal(active)
	if _, err := app.functions["task_update"](context.Background(), map[string]any{"state": string(raw)}); err == nil {
		t.Fatal("save failure ignored")
	}
}
