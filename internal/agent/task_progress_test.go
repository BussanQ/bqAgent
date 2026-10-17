package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"bqagent/internal/taskstate"
	"bqagent/internal/tools"
	"bqagent/internal/turn"
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
		{"task_update", "saved", false}, {"task_scope", "updated", false},
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
	client := &stubClient{responses: []AssistantMessage{{Role: "assistant", Content: "will test", Phase: "commentary"}, taskUpdateMessage(invalid), {Role: "assistant", ToolCalls: []ToolCall{{ID: "test", Function: FunctionCall{Name: "execute_bash", Arguments: `{"command":"go test -run TestSwitch"}`}}}}, taskUpdateMessage(complete), {Role: "assistant", Content: "done"}}}
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
			// Saved task state is advisory: every status ends after one answer.
			if reply != "answer" || len(client.messages) != 1 || (status != "" && store.state.Status != status) {
				t.Fatalf("task status %q retried or changed: %q calls=%d", status, reply, len(client.messages))
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
	client := &stubClient{responses: []AssistantMessage{{Role: "assistant", Content: "answer"}}}
	app := NewWithOptions(client, "test", Options{TaskStore: store})
	if reply, err := app.Run(context.Background(), "work", 10); err != nil || reply != "answer" || len(client.messages) != 1 {
		t.Fatalf("task load error blocked work: reply=%q err=%v calls=%d", reply, err, len(client.messages))
	}
	store.err = nil
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := app.Run(ctx, "work", 10); !errors.Is(err, context.Canceled) || len(client.messages) != 1 {
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
	changed := active
	changed.Remaining = []string{"verify"}
	raw, _ = json.Marshal(changed)
	if _, err := app.functions["task_update"](context.Background(), map[string]any{"state": string(raw)}); err == nil {
		t.Fatal("save failure ignored")
	}
}

func taskEvents(events []turn.Event) []turn.Event {
	var result []turn.Event
	for _, event := range events {
		if event.Kind == "task.updated" {
			result = append(result, event)
		}
	}
	return result
}

func TestTaskSnapshotOnNewTurnHidesFinishedState(t *testing.T) {
	for _, status := range []string{"completed", "cancelled", "active", "blocked", "awaiting_input"} {
		t.Run(status, func(t *testing.T) {
			state := activeTestTask()
			state.Status = status
			state.Reason = "saved task"
			store := &memoryTaskStore{state: &state}
			var events []turn.Event
			client := &stubClient{responses: []AssistantMessage{{Content: "answer"}}}
			app := NewWithOptions(client, "test", Options{TurnID: "next-turn", TaskStore: store, MessageSink: func(e turn.Event) { events = append(events, e) }})
			if _, err := app.RunTurn(context.Background(), []map[string]any{{"role": "user", "content": "status"}}, 10); err != nil {
				t.Fatal(err)
			}
			got := taskEvents(events)
			if len(got) == 0 {
				t.Fatal("missing initial task snapshot")
			}
			finished := status == "completed" || status == "cancelled"
			if (got[0].Text == "null") != finished {
				t.Fatalf("status %s: initial snapshot = %s", status, got[0].Text)
			}
			if store.state.Status != status || app.task == nil || app.task.Status != status {
				t.Fatal("hiding the snapshot changed the saved task")
			}
		})
	}
}

func TestTaskUpdatedEventsOnLoadSaveAndUnchanged(t *testing.T) {
	active := activeTestTask()
	changed := active
	changed.Remaining = []string{"verify"}
	changed.Status, changed.Reason = "blocked", "needs input"
	sameWithTime := active
	sameWithTime.UpdatedAt = time.Now()
	store := &memoryTaskStore{state: &active}
	var events []turn.Event
	client := &stubClient{responses: []AssistantMessage{taskUpdateMessage(sameWithTime), taskUpdateMessage(changed), {Content: "done"}}}
	app := NewWithOptions(client, "test", Options{TurnID: "run-1", TaskStore: store, MessageSink: func(e turn.Event) { events = append(events, e) }})
	if _, err := app.RunTurn(context.Background(), []map[string]any{{"role": "user", "content": "go on"}}, 20); err != nil {
		t.Fatal(err)
	}
	got := taskEvents(events)
	if len(got) != 2 {
		t.Fatalf("task events = %+v, want initial snapshot and one saved change (unchanged update emits none)", got)
	}
	for _, event := range got {
		if event.ItemID != "run-1/task" {
			t.Fatalf("event = %+v", event)
		}
	}
	if got[0].Phase != "active" || got[1].Phase != "blocked" {
		t.Fatalf("phases = %q, %q, want task statuses", got[0].Phase, got[1].Phase)
	}
	var initial, saved taskstate.State
	if json.Unmarshal([]byte(got[0].Text), &initial) != nil || len(initial.Remaining) != 2 {
		t.Fatalf("initial snapshot = %q", got[0].Text)
	}
	if json.Unmarshal([]byte(got[1].Text), &saved) != nil || len(saved.Remaining) != 1 {
		t.Fatalf("saved snapshot = %q", got[1].Text)
	}
}

func TestTaskUpdatedEventNullForMissingTaskAndNoneOnFailedSave(t *testing.T) {
	store := &memoryTaskStore{}
	var events []turn.Event
	next := activeTestTask()
	client := &stubClient{responses: []AssistantMessage{taskUpdateMessage(next), {Content: "done"}}}
	app := NewWithOptions(client, "test", Options{TaskStore: store, MessageSink: func(e turn.Event) { events = append(events, e) }})
	store.err = nil
	failing := &failingSaveStore{memoryTaskStore: store}
	app.taskStore = failing
	if _, err := app.RunTurn(context.Background(), []map[string]any{{"role": "user", "content": "plan"}}, 20); err != nil {
		t.Fatal(err)
	}
	got := taskEvents(events)
	if len(got) != 1 || got[0].Text != "null" || got[0].Phase != "" {
		t.Fatalf("task events = %+v, want only the null snapshot", got)
	}
}

type failingSaveStore struct{ *memoryTaskStore }

func (s *failingSaveStore) SaveTaskState(taskstate.State) error { return fmt.Errorf("disk full") }

func TestUnchangedTaskUpdateSkipsSaveButCountsAsReassessment(t *testing.T) {
	blocked := activeTestTask()
	blocked.Status, blocked.Reason = "blocked", "needs input"
	store := &countingTaskStore{memoryTaskStore: &memoryTaskStore{state: &blocked}}
	same := blocked
	same.UpdatedAt = time.Now()
	client := &stubClient{responses: []AssistantMessage{taskUpdateMessage(same), {Content: "still blocked"}}}
	app := NewWithOptions(client, "test", Options{TaskStore: store})
	result, err := app.RunTurn(context.Background(), []map[string]any{{"role": "user", "content": "any news?"}}, 20)
	if err != nil || store.saves != 0 || len(client.messages) != 2 || result.Outcome.Reason == "task_reassessment_required" {
		t.Fatalf("saves=%d calls=%d outcome=%+v err=%v", store.saves, len(client.messages), result.Outcome, err)
	}
}

type countingTaskStore struct {
	*memoryTaskStore
	saves int
}

func (s *countingTaskStore) SaveTaskState(state taskstate.State) error {
	s.saves++
	return s.memoryTaskStore.SaveTaskState(state)
}

func TestUnchangedCompletedTaskUpdateStillRequiresRealEvidence(t *testing.T) {
	done := completedWith(taskstate.Verification{Command: "go test ./x", Status: "passed", Details: "ok"})
	store := &countingTaskStore{memoryTaskStore: &memoryTaskStore{state: &done}}
	app := NewWithOptions(&stubClient{}, "test", Options{TaskStore: store})
	app.task = &done
	run := &runState{id: "r"}
	_, err := app.functions["task_update"](context.WithValue(context.Background(), runStateKey{}, run), map[string]any{"state": mustJSON(done)})
	if err == nil || store.saves != 0 {
		t.Fatalf("err=%v saves=%d, want evidence rejection without save", err, store.saves)
	}
}

func mustJSON(value any) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func TestStaleCompletionReopenEmitsTaskEventOnlyWhenSaved(t *testing.T) {
	done := completedWith()
	reopenable := func(store taskstate.Store) (*Agent, *runState, *[]turn.Event) {
		events := &[]turn.Event{}
		app := NewWithOptions(&stubClient{}, "test", Options{TaskStore: store})
		app.task = &done
		run := &runState{id: "run-9", sink: func(e turn.Event) { *events = append(*events, e) }, completionSaved: true}
		entry := run.ledger.begin("e1", "edit_file", nil)
		run.ledger.finish(entry, true)
		return app, run, events
	}
	app, run, events := reopenable(&memoryTaskStore{state: &done})
	if !app.reopenStaleCompletion(run) {
		t.Fatal("stale completion not reopened")
	}
	got := taskEvents(*events)
	if len(got) != 1 || got[0].ItemID != "run-9/task" || got[0].Phase != "active" || !strings.Contains(got[0].Text, "re-run verification") {
		t.Fatalf("reopen events = %+v", got)
	}
	done = completedWith()
	app, run, events = reopenable(&failingSaveStore{memoryTaskStore: &memoryTaskStore{state: &done}})
	if !app.reopenStaleCompletion(run) {
		t.Fatal("stale completion not reported")
	}
	if got := taskEvents(*events); len(got) != 0 || app.task.Status != "completed" {
		t.Fatalf("failed reopen save emitted %+v, task=%+v", got, app.task)
	}
}

func TestTaskSnapshotSurvivesContextCompactionWithDetails(t *testing.T) {
	task := taskstate.State{Goal: "ship the zebra feature", Status: "active",
		Criteria:  []taskstate.Criterion{{Description: "zebra criterion alpha"}},
		Remaining: []string{"remaining step omega", "remaining step sigma"}}
	history := []map[string]any{{"role": "user", "content": "implement"}, {"role": "assistant", "content": strings.Repeat("older work ", 2000)}, {"role": "user", "content": "continue"}}
	for name, config := range map[string]ContextConfig{
		"summarization": {Enabled: true, MaxInputTokens: 1800, TargetInputTokens: 1000, KeepLastTurns: 1, SummarizationEnabled: true, SummaryTriggerTokens: 30},
		"hard prune":    {Enabled: true, MaxInputTokens: 1800, TargetInputTokens: 1000, KeepLastTurns: 1},
	} {
		t.Run(name, func(t *testing.T) {
			client := &stubClient{optionResponses: []AssistantMessage{{Role: "assistant", Content: "short"}}}
			app := NewWithOptions(client, "test", Options{TaskStore: &memoryTaskStore{state: &task}, Context: config})
			app.task = &task
			request, compacted := app.buildRequestMessages(context.Background(), history, nil, ChatCompletionOptions{})
			if compacted == nil {
				t.Fatal("expected compaction")
			}
			if name == "summarization" && len(client.optionMessages) == 0 {
				t.Fatal("summarization path not exercised")
			}
			snapshots := 0
			for _, message := range request {
				if strings.HasPrefix(contentText(message["content"]), taskProgressInstructions) {
					snapshots++
				}
			}
			if snapshots != 1 {
				t.Fatalf("snapshots in request = %d, want exactly 1", snapshots)
			}
			if tokens := estimateMessagesTokens(request); tokens > config.MaxInputTokens {
				t.Fatalf("request tokens = %d, above limit %d", tokens, config.MaxInputTokens)
			}
			last := contentText(request[len(request)-1]["content"])
			if !strings.HasPrefix(last, taskProgressInstructions) {
				t.Fatalf("task snapshot is not the last request message: %q", last)
			}
			for _, want := range []string{"ship the zebra feature", "zebra criterion alpha", "remaining step omega", "remaining step sigma"} {
				if !strings.Contains(last, want) {
					t.Fatalf("snapshot lost %q: %q", want, last)
				}
			}
			for _, message := range compacted {
				if strings.HasPrefix(contentText(message["content"]), taskProgressInstructions) {
					t.Fatal("snapshot persisted in compacted history")
				}
			}
		})
	}
}

func TestTaskSnapshotExceedingRequestBudgetReturnsError(t *testing.T) {
	task := activeTestTask()
	client := &stubClient{}
	app := NewWithOptions(client, "test", Options{TaskStore: &memoryTaskStore{state: &task}, Context: ContextConfig{Enabled: true, MaxInputTokens: 200, TargetInputTokens: 100, KeepLastTurns: 1}})
	app.task = &task
	history := []map[string]any{{"role": "user", "content": "continue"}}
	request, _, err := app.buildRequestMessagesChecked(context.Background(), history, nil, ChatCompletionOptions{})
	if err == nil || !strings.Contains(err.Error(), "context budget too small") || request != nil {
		t.Fatalf("request=%v err=%v, want explicit budget error", request, err)
	}
	if _, runErr := app.RunTurn(context.Background(), history, 5); runErr == nil || !strings.Contains(runErr.Error(), "context budget too small") || len(client.messages) != 0 {
		t.Fatalf("RunTurn err=%v requests=%d, want error before any model request", runErr, len(client.messages))
	}
}

// buildRequestMessages drops the budget error; tests that do not exercise the
// limit use it, production code must call buildRequestMessagesChecked.
func (a *Agent) buildRequestMessages(ctx context.Context, messages []map[string]any, definitions []tools.Definition, options ChatCompletionOptions) (request, compacted []map[string]any) {
	request, compacted, _ = a.buildRequestMessagesChecked(ctx, messages, definitions, options)
	return request, compacted
}

type funcCountingClient struct {
	*stubClient
	count func(messages []map[string]any) InputTokenCount
	seen  [][]map[string]any
}

func (c *funcCountingClient) CountInputTokens(_ context.Context, _ string, messages []map[string]any, _ []tools.Definition, _ ChatCompletionOptions) (InputTokenCount, error) {
	c.seen = append(c.seen, cloneMessages(messages))
	return c.count(messages), nil
}

func countSnapshots(messages []map[string]any) int {
	n := 0
	for _, m := range messages {
		if strings.HasPrefix(contentText(m["content"]), taskProgressInstructions) {
			n++
		}
	}
	return n
}

func TestTaskSnapshotAddedOnceWhenContextDisabled(t *testing.T) {
	task := activeTestTask()
	app := NewWithOptions(&stubClient{}, "test", Options{TaskStore: &memoryTaskStore{state: &task}, Context: ContextConfig{Enabled: false}})
	app.task = &task
	request, compacted, err := app.buildRequestMessagesChecked(context.Background(), []map[string]any{{"role": "user", "content": "go"}}, nil, ChatCompletionOptions{})
	if err != nil || compacted != nil || countSnapshots(request) != 1 {
		t.Fatalf("snapshots=%d compacted=%v err=%v", countSnapshots(request), compacted, err)
	}
}

func TestTaskSnapshotBudgetUsesProviderExactCount(t *testing.T) {
	task := activeTestTask()
	history := []map[string]any{{"role": "user", "content": "continue"}}
	build := func(max, target, reported int) (*funcCountingClient, []map[string]any, error) {
		client := &funcCountingClient{stubClient: &stubClient{}, count: func([]map[string]any) InputTokenCount { return InputTokenCount{Tokens: reported, Exact: true} }}
		app := NewWithOptions(client, "test", Options{TaskStore: &memoryTaskStore{state: &task}, Context: ContextConfig{Enabled: true, MaxInputTokens: max, TargetInputTokens: target, KeepLastTurns: 1, ExactCountTriggerPercent: 1}})
		app.task = &task
		request, _, err := app.buildRequestMessagesChecked(context.Background(), history, nil, ChatCompletionOptions{})
		return client, request, err
	}
	// The local estimate (well above 300 tokens with the snapshot) would reject
	// this, but the provider's exact count is within the limit and is authoritative.
	client, request, err := build(300, 280, 250)
	if err != nil || countSnapshots(request) != 1 {
		t.Fatalf("provider-accepted request rejected: err=%v snapshots=%d", err, countSnapshots(request))
	}
	if len(client.seen) == 0 {
		t.Fatal("exact counter not consulted")
	}
	for _, seen := range client.seen[1:] {
		if countSnapshots(seen) != 1 {
			t.Fatalf("counter saw %d snapshots, want exactly 1 (no double count)", countSnapshots(seen))
		}
	}
	// A small local estimate must not hide a provider count above the limit.
	if _, _, err = build(1800, 1000, 5000); err == nil || !strings.Contains(err.Error(), "provider") || !strings.Contains(err.Error(), "context budget too small") {
		t.Fatalf("err = %v, want provider-measured budget error", err)
	}
}

func TestCheckpointInstructionIsProtectedBudgetOverhead(t *testing.T) {
	task := activeTestTask()
	checkpoint := map[string]any{"role": "developer", "content": "CHECKPOINT " + strings.Repeat("stop now ", 100)}
	history := []map[string]any{{"role": "user", "content": "implement"}, {"role": "assistant", "content": strings.Repeat("older work ", 2000)}, {"role": "user", "content": "continue"}}
	newApp := func(max, target int) *Agent {
		app := NewWithOptions(&stubClient{optionResponses: []AssistantMessage{{Role: "assistant", Content: "short"}}}, "test", Options{TaskStore: &memoryTaskStore{state: &task}, Context: ContextConfig{Enabled: true, MaxInputTokens: max, TargetInputTokens: target, KeepLastTurns: 1, SummarizationEnabled: true, SummaryTriggerTokens: 30}})
		app.task = &task
		return app
	}
	request, compacted, err := newApp(1800, 1000).buildRequestMessagesWithTail(context.Background(), history, nil, ChatCompletionOptions{}, []map[string]any{checkpoint})
	if err != nil || countSnapshots(request) != 1 || request[len(request)-1]["content"] != checkpoint["content"] {
		t.Fatalf("snapshots=%d err=%v", countSnapshots(request), err)
	}
	if tokens := estimateMessagesTokens(request); tokens > 1800 {
		t.Fatalf("request tokens = %d, above limit", tokens)
	}
	for _, message := range compacted {
		if message["content"] == checkpoint["content"] || strings.HasPrefix(contentText(message["content"]), taskProgressInstructions) {
			t.Fatal("protected message persisted in compacted history")
		}
	}
	// Task alone fits, but task plus checkpoint does not: explicit error.
	if _, _, err = newApp(560, 500).buildRequestMessagesWithTail(context.Background(), history, nil, ChatCompletionOptions{}, []map[string]any{checkpoint}); err == nil || !strings.Contains(err.Error(), "context budget too small") {
		t.Fatalf("err = %v, want budget error", err)
	}
}
