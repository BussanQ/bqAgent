package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"bqagent/internal/taskstate"
	"bqagent/internal/tools"
)

type memoryTaskStore struct {
	state *taskstate.State
	err   error
}

func TestRunOrdinaryAnswerDoesNotRestartAfterToolCalls(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, phase := range []string{"", "final_answer"} {
			for _, count := range []int{2, 10} {
				t.Run(fmt.Sprintf("stream=%t/phase=%s/calls=%d", stream, phase, count), func(t *testing.T) {
					const answer = "主要用 Shell/Bash 编写，Python 为辅助语言。"
					client := &stubClient{}
					for i := 0; i < count; i++ {
						client.responses = append(client.responses, AssistantMessage{ToolCalls: []ToolCall{{ID: fmt.Sprint(i), Function: FunctionCall{Name: "execute_bash", Arguments: `{"command":"ls"}`}}}})
					}
					client.responses = append(client.responses, AssistantMessage{Content: answer, Phase: phase}, AssistantMessage{Content: "unsolicited follow-up"})
					store := &memoryTaskStore{}
					app := NewWithOptions(client, "test", Options{TaskStore: store, Stream: stream, Context: ContextConfig{Enabled: false}, Functions: map[string]tools.Function{"execute_bash": func(context.Context, map[string]any) (string, error) { return "build.sh\nnetwork.py", nil }}})
					reply, history, err := app.RunConversationTurn(context.Background(), []map[string]any{{"role": "user", "content": "当前是什么编程语言写的"}}, 20)
					if err != nil || reply != answer || len(client.messages) != count+1 || store.state != nil {
						t.Fatalf("ordinary answer restarted: reply=%q requests=%d task=%+v err=%v", reply, len(client.messages), store.state, err)
					}
					for _, message := range history {
						if message["role"] == "developer" && strings.Contains(contentText(message["content"]), "Before ending, review") {
							t.Fatal("ordinary question acquired an unsolicited closeout instruction")
						}
					}
				})
			}
		}
	}
}

func TestRunProductiveContinuationsCompleteInOneTurn(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			active := activeTestTask()
			store := &memoryTaskStore{state: &active}
			var responses []AssistantMessage
			// Match the reported failure: two empty replies, then a successful
			// silent edit. Repeat beyond the former three-continuation cap.
			for i := 0; i < 6; i++ {
				responses = append(responses, AssistantMessage{Content: ""}, AssistantMessage{Content: ""},
					AssistantMessage{ToolCalls: []ToolCall{{ID: fmt.Sprintf("edit-%d", i), Function: FunctionCall{Name: "edit_file", Arguments: fmt.Sprintf(`{"step":%d}`, i)}}}},
					AssistantMessage{Content: "working", Phase: "commentary"})
				// Each following cycle starts with work to reset the consecutive
				// empty-reply budget before the next pair of empty responses.
				responses = append(responses, AssistantMessage{ToolCalls: []ToolCall{{ID: fmt.Sprintf("read-%d", i), Function: FunctionCall{Name: "read_file", Arguments: fmt.Sprintf(`{"step":%d}`, i)}}}})
			}
			complete := taskstate.State{Goal: active.Goal, Status: "completed", Criteria: []taskstate.Criterion{{Description: "switch works", Done: true, Evidence: "targeted verification passed"}}, Verification: []taskstate.Verification{{Command: "go test ./switch", Status: "passed", Details: "ok"}}}
			responses = append(responses, AssistantMessage{ToolCalls: []ToolCall{{ID: "verify", Function: FunctionCall{Name: "execute_bash", Arguments: `{"command":"go test ./switch"}`}}}}, taskUpdateMessage(complete), AssistantMessage{Content: "done", Phase: "final_answer"})
			expectedCalls := len(responses)
			client := &stubClient{responses: responses}
			var output bytes.Buffer
			edits, checks := 0, 0
			app := NewWithOptions(client, "test", Options{TaskStore: store, Stream: stream, TokenSink: &output, Functions: map[string]tools.Function{
				"edit_file":    func(context.Context, map[string]any) (string, error) { edits++; return "", nil },
				"read_file":    func(context.Context, map[string]any) (string, error) { return "source", nil },
				"execute_bash": func(context.Context, map[string]any) (string, error) { checks++; return "ok", nil },
			}})
			reply, err := app.Run(context.Background(), "implement fast", 50)
			if err != nil || reply != "done" || edits != 6 || checks != 1 || len(client.messages) != expectedCalls || store.state.Status != "completed" || strings.Contains(output.String(), "尚未确认完成") {
				t.Fatalf("calls=%d edits=%d checks=%d task=%+v reply=%q err=%v", len(client.messages), edits, checks, store.state, reply, err)
			}
		})
	}
}

func TestRunStagnantContinuationRecoversWithoutPausing(t *testing.T) {
	client := &stubClient{responses: []AssistantMessage{{Content: "working", Phase: "commentary"}, {Content: "working", Phase: "commentary"}, {Content: "working", Phase: "commentary"}, {Content: "done", Phase: "final_answer"}}}
	var output bytes.Buffer
	app := NewWithOptions(client, "test", Options{Stream: true, TokenSink: &output})
	reply, err := app.Run(context.Background(), "work", 50)
	if err != nil || len(client.messages) != 4 || reply != "done" || strings.Contains(output.String(), "尚未确认完成") {
		t.Fatalf("calls=%d reply=%q output=%q err=%v", len(client.messages), reply, output.String(), err)
	}
	last := client.messages[3]
	if !strings.Contains(contentText(last[len(last)-1]["content"]), "Change approach now") {
		t.Fatal("missing escalated recovery instruction")
	}
}

func TestRunRepairsFailedVerificationAfterEmptyReplies(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			active := activeTestTask()
			store := &memoryTaskStore{state: &active}
			complete := taskstate.State{Goal: active.Goal, Status: "completed", Criteria: []taskstate.Criterion{{Description: "switch works", Done: true, Evidence: "targeted test passed after repair"}}, Verification: []taskstate.Verification{{Command: "go test -run TestSwitch", Status: "passed", Details: "ok"}}}
			client := &stubClient{responses: []AssistantMessage{
				{Content: ""}, {Content: ""},
				{ToolCalls: []ToolCall{{ID: "failed-test", Function: FunctionCall{Name: "execute_bash", Arguments: `{"command":"go test -run TestSwitch"}`}}}},
				{Content: ""}, {Content: ""}, {Content: "will fix", Phase: "commentary"}, {Content: ""},
				{ToolCalls: []ToolCall{{ID: "fix", Function: FunctionCall{Name: "edit_file", Arguments: `{}`}}}},
				{ToolCalls: []ToolCall{{ID: "passed-test", Function: FunctionCall{Name: "execute_bash", Arguments: `{"command":"go test -run TestSwitch"}`}}}},
				taskUpdateMessage(complete), {Content: "verified", Phase: "final_answer"},
			}}
			var output bytes.Buffer
			fixed, checks := false, 0
			app := NewWithOptions(client, "test", Options{TaskStore: store, Stream: stream, TokenSink: &output, Functions: map[string]tools.Function{
				"execute_bash": func(context.Context, map[string]any) (string, error) {
					checks++
					if !fixed {
						return "disabled: service_tier should be omitted", errors.New("test failed")
					}
					return "ok", nil
				},
				"edit_file": func(context.Context, map[string]any) (string, error) { fixed = true; return "", nil },
			}})
			reply, err := app.Run(context.Background(), active.Goal, 20)
			if err != nil || reply != "verified" || checks != 2 || !fixed || store.state.Status != "completed" || len(client.messages) != 11 || strings.Contains(output.String(), "尚未确认完成") {
				t.Fatalf("reply=%q checks=%d fixed=%v task=%+v calls=%d err=%v", reply, checks, fixed, store.state, len(client.messages), err)
			}
			request := client.messages[7]
			found := false
			for _, message := range request {
				text := contentText(message["content"])
				if message["role"] == "developer" && strings.Contains(text, "diagnose its actual output") && strings.Contains(text, "Recovery attempt") {
					found = true
				}
			}
			if !found {
				t.Fatal("missing failure recovery instruction")
			}
			encoded, _ := json.Marshal(request)
			if !strings.Contains(string(encoded), "service_tier should be omitted") {
				t.Fatal("failed verification evidence lost before repair")
			}
		})
	}
}

func TestContinuationProgressClassification(t *testing.T) {
	for _, tt := range []struct {
		name, result string
		progress     bool
	}{
		{"execute_bash", "", true}, {"edit_file", "  \n", true},
		{"read_file", "source", true}, {"execute_bash", "Error: command failed", false},
		{"grep", "No matches found.", false}, {"glob", "No files matched.", false},
		{"task_update", "saved", false}, {"todo_write", "updated", false},
	} {
		t.Run(tt.name+tt.result, func(t *testing.T) {
			guard := &continuationGuard{}
			guard.recoveryPrompt("continue")
			guard.recoveryPrompt("continue")
			guard.observe(ToolCall{Function: FunctionCall{Name: tt.name}}, tt.result)
			if got := strings.Contains(guard.recoveryPrompt("continue"), "Change approach now"); got == tt.progress {
				t.Fatalf("escalated recovery after %s = %v, progress=%v", tt.name, got, tt.progress)
			}
		})
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
	client := &stubClient{responses: []AssistantMessage{{Role: "assistant", Content: "will test"}, taskUpdateMessage(invalid), {Role: "assistant", ToolCalls: []ToolCall{{ID: "test", Function: FunctionCall{Name: "execute_bash", Arguments: `{"command":"go test -run TestSwitch"}`}}}}, taskUpdateMessage(complete), {Role: "assistant", Content: "done"}}}
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
			reply, err := app.Run(context.Background(), "status", 3)
			if err != nil {
				t.Fatal(err)
			}
			if status == "active" {
				if !strings.Contains(reply, "maximum of 3 iterations") || len(client.messages) != 3 || store.state.Status != "active" {
					t.Fatalf("reply=%q calls=%d", reply, len(client.messages))
				}
			} else if status == "blocked" || status == "awaiting_input" {
				// A continued blocked task must be re-assessed by task_update; a bare
				// acknowledgement is prompted twice, then ends with an explicit
				// blocked outcome rather than silently passing.
				if reply != "answer" || len(client.messages) != 3 || store.state.Status != status {
					t.Fatalf("blocked task not re-assessed: %q calls=%d", reply, len(client.messages))
				}
			} else if reply != "answer" || len(client.messages) != 1 {
				t.Fatalf("ordinary/blocked answer retried: %q calls=%d", reply, len(client.messages))
			}
		})
	}
	client := &stubClient{responses: []AssistantMessage{{Content: "working", Phase: "commentary"}, {Content: "working", Phase: "commentary"}, {Content: "working", Phase: "commentary"}}}
	app := NewWithOptions(client, "test", Options{})
	reply, err := app.Run(context.Background(), "work", 3)
	if err != nil || !strings.Contains(reply, "maximum of 3 iterations") || len(client.messages) != 3 {
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
