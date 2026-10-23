package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"bqagent/internal/turn"
)

func assertRequestExtendsHistory(t *testing.T, request, history []map[string]any) {
	t.Helper()
	if len(request) < len(history) {
		t.Fatalf("request has %d messages, history has %d", len(request), len(history))
	}
	prefix, _ := json.Marshal(request[:len(history)])
	previous, _ := json.Marshal(history)
	if string(prefix) != string(previous) {
		t.Fatalf("request changed history:\nprevious %s\nprefix %s", previous, prefix)
	}
}

func TestTaskContextHistoryAppendsOnlyChanges(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "no budget", true: "budgeted"}[enabled], func(t *testing.T) {
			task := activeTestTask()
			app := NewWithOptions(&stubClient{}, "test", Options{TaskStore: &memoryTaskStore{state: &task}, Context: ContextConfig{Enabled: enabled}})
			app.task = &task
			build := func(history []map[string]any) []map[string]any {
				t.Helper()
				request, compacted, err := app.buildRequestMessagesChecked(context.Background(), history, nil, ChatCompletionOptions{})
				if err != nil || compacted != nil {
					t.Fatalf("unexpected compaction or error: %v, %v", compacted, err)
				}
				assertRequestExtendsHistory(t, request, history)
				return request
			}
			first := build([]map[string]any{{"role": "user", "content": "work"}})
			history := append(duplicateMessages(first), map[string]any{"role": "assistant", "content": "working"}, map[string]any{"role": "user", "content": "continue"})
			unchanged := build(history)
			if len(unchanged) != len(history) || countSnapshots(unchanged) != 1 {
				t.Fatal("unchanged context was appended again")
			}
			task.Remaining = []string{"verify the change"}
			changed := build(unchanged)
			_, state := app.taskContextParts(context.Background())
			if len(changed) != len(unchanged)+1 || countSnapshots(changed) != 1 || contentText(changed[len(changed)-1]["content"]) != taskContextUpdate(state) {
				t.Fatal("changed task state did not append exactly one state-only update")
			}
			if strings.Contains(contentText(changed[len(changed)-1]["content"]), taskProgressInstructions) {
				t.Fatal("state-only update repeated the task tracking instructions")
			}
			if repeated := build(changed); len(repeated) != len(changed) {
				t.Fatal("current task state was duplicated")
			}
		})
	}
}

func TestTaskContextHistoryPreservesChangesBetweenToolRounds(t *testing.T) {
	task := activeTestTask()
	changed := task
	changed.Remaining = []string{"verify the change"}
	client := &stubClient{responses: []AssistantMessage{taskUpdateMessage(changed), {Content: "done"}}}
	recorder := &capturingRecorder{}
	app := NewWithOptions(client, "test", Options{TaskStore: &memoryTaskStore{state: &task}, Recorder: recorder, Context: ContextConfig{Enabled: false}})
	_, working, err := app.RunConversationTurn(context.Background(), []map[string]any{{"role": "user", "content": "work"}}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(client.messages) != 2 {
		t.Fatalf("model requests = %d, want 2", len(client.messages))
	}
	assertRequestExtendsHistory(t, client.messages[1], client.messages[0])
	for name, messages := range map[string][]map[string]any{"request": client.messages[1], "working": working, "recorded": recorder.recorded} {
		if countSnapshots(messages) != 1 || countTaskContextUpdates(messages) != 1 {
			t.Fatalf("%s: task changes must be appended as one update and persisted in order", name)
		}
	}
}

func countTaskContextUpdates(messages []map[string]any) int {
	n := 0
	for _, message := range messages {
		if isTaskContextMessage(message) && isTaskContextUpdate(contentText(message["content"])) {
			n++
		}
	}
	return n
}

func TestTaskContextHistoryAppendsFullSnapshotWhenInstructionsChange(t *testing.T) {
	task := activeTestTask()
	app := NewWithOptions(&stubClient{}, "test", Options{TaskStore: &memoryTaskStore{state: &task}, Context: ContextConfig{Enabled: false}})
	app.task = &task
	header, state := app.taskContextParts(context.Background())
	old := header + "\nObsolete instructions for a removed tool." + state
	history := []map[string]any{
		{"role": "user", "content": "work"},
		{"role": "developer", "content": old},
		{"role": "developer", "content": taskContextUpdate(state)},
	}
	request, _, err := app.buildRequestMessagesChecked(context.Background(), history, nil, ChatCompletionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	assertRequestExtendsHistory(t, request, history)
	if len(request) != len(history)+1 || contentText(request[len(request)-1]["content"]) != header+state {
		t.Fatal("changed instructions did not append a full current snapshot")
	}
}

func TestTaskContextHistorySideQuestionAppendsOnlyState(t *testing.T) {
	task := activeTestTask()
	app := NewWithOptions(&stubClient{}, "test", Options{TaskStore: &memoryTaskStore{state: &task}, Context: ContextConfig{Enabled: false}})
	app.task = &task
	history := []map[string]any{{"role": "user", "content": "work"}, {"role": "developer", "content": app.taskContext(context.Background())}}
	run := &runState{}
	run.outcome.TaskRelation = turn.SideQuestion
	side, _, _ := app.buildRequestMessagesChecked(context.WithValue(context.Background(), runStateKey{}, run), history, nil, ChatCompletionOptions{})
	back, _, _ := app.buildRequestMessagesChecked(context.Background(), side, nil, ChatCompletionOptions{})
	if countSnapshots(back) != 1 || countTaskContextUpdates(back) != 2 {
		t.Fatalf("side question round trip should append two state-only updates, got %d snapshots and %d updates", countSnapshots(back), countTaskContextUpdates(back))
	}
	if !strings.Contains(contentText(side[len(side)-1]["content"]), sideQuestionTaskState) {
		t.Fatal("side question update lost its scope note")
	}
}

func TestTaskContextHistorySurvivesResume(t *testing.T) {
	ctx := context.Background()
	user := map[string]any{"role": "user", "content": "hello"}
	recorder := &capturingRecorder{}
	client := &stubClient{responses: []AssistantMessage{{Content: "first answer"}}}
	app := NewWithOptions(client, "test", Options{TaskStore: &memoryTaskStore{}, Recorder: recorder, Context: ContextConfig{Enabled: false}})
	_, working, err := app.RunConversationTurn(ctx, []map[string]any{user}, 3)
	if err != nil {
		t.Fatal(err)
	}
	replay := append(app.prompt.Messages(), user)
	replay = append(replay, recorder.recorded...)
	assertRequestExtendsHistory(t, replay, working)
	if len(replay) != len(working) || lastTaskContextMessage(replay) == nil {
		t.Fatal("transcript and working context did not retain the injected context")
	}
	for name, history := range map[string][]map[string]any{"working context": working, "transcript": replay} {
		t.Run(name, func(t *testing.T) {
			resumedClient := &stubClient{responses: []AssistantMessage{{Content: "second answer"}}}
			resumedRecorder := &capturingRecorder{}
			resumed := NewWithOptions(resumedClient, "test", Options{TaskStore: &memoryTaskStore{}, Recorder: resumedRecorder, Context: ContextConfig{Enabled: false}})
			history = append(duplicateMessages(history), map[string]any{"role": "user", "content": "next question"})
			if _, _, err := resumed.RunConversationTurn(ctx, history, 3); err != nil {
				t.Fatal(err)
			}
			assertRequestExtendsHistory(t, resumedClient.messages[0], history)
			if len(resumedClient.messages[0]) != len(history) || lastTaskContextMessage(resumedRecorder.recorded) != nil {
				t.Fatal("resume appended or recorded unchanged context again")
			}
		})
	}
}

func TestTaskContextHistoryCompactionProtectsCurrentSnapshot(t *testing.T) {
	for _, summarize := range []bool{false, true} {
		t.Run(map[bool]string{false: "prune", true: "summarize"}[summarize], func(t *testing.T) {
			task := activeTestTask()
			client := &stubClient{optionResponses: []AssistantMessage{{Content: "short summary"}}}
			app := NewWithOptions(client, "test", Options{TaskStore: &memoryTaskStore{state: &task}, Context: ContextConfig{Enabled: true, MaxInputTokens: 2000, TargetInputTokens: 1200, KeepLastTurns: 1, SummarizationEnabled: summarize}})
			app.task = &task
			old := app.taskContext(context.Background())
			task.Remaining = []string{"verify current acceptance criteria"}
			current := app.taskContext(context.Background())
			history := []map[string]any{
				{"role": "user", "content": "work"},
				{"role": "developer", "content": old},
				{"role": "assistant", "content": "progress"},
				{"role": "developer", "content": current},
				{"role": "developer", "content": taskContextUpdate("")},
				{"role": "assistant", "content": strings.Repeat("older work ", 2000)},
				{"role": "user", "content": "continue"},
			}
			request, compacted, err := app.buildRequestMessagesChecked(context.Background(), history, nil, ChatCompletionOptions{})
			if err != nil || compacted == nil {
				t.Fatalf("compaction failed: %v", err)
			}
			if countSnapshots(request) != 1 || countTaskContextUpdates(request) != 0 || contentText(request[len(request)-1]["content"]) != current {
				t.Fatal("compaction lost the complete current snapshot or retained obsolete snapshots")
			}
			if tokens := estimateMessagesTokens(request); tokens > 2000 {
				t.Fatalf("compacted request exceeds budget: %d", tokens)
			}
			if summarize && len(client.optionMessages) == 0 {
				t.Fatal("summarization was not exercised")
			}
		})
	}
}
