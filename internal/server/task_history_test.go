package server

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"bqagent/internal/agent"
	"bqagent/internal/session"
	"bqagent/internal/taskstate"
)

func TestWebUITaskSnapshotPublishesPersistedState(t *testing.T) {
	task := taskstate.State{Goal: "单一进度来源", Status: "blocked", Reason: "缺少运行参数", Criteria: []taskstate.Criterion{{Description: strings.Repeat("完整验收", 300)}}, Remaining: []string{"提供参数后验证"}}
	client := &taskScriptClient{responses: []agent.AssistantMessage{serverTaskUpdate(task), {Content: "等待参数"}}}
	service := NewService(ServiceOptions{TaskToolEnabled: true, WorkspaceRoot: t.TempDir(), AgentDir: filepath.Join(t.TempDir(), ".agent"), Client: client, Model: "test", SystemPrompt: "test"})
	channel := NewWebUIChannel(service, true)
	h := NewHandler(HandlerOptions{Service: service, Channels: []Channel{channel}})
	writer := httptest.NewRecorder()
	request := httptest.NewRequest("POST", "/api/v1/webui/chat", strings.NewReader(`{"message":"实现单一进度","turn_id":"task-stream"}`))
	request.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(writer, request)
	if writer.Code != 200 {
		t.Fatalf("status=%d body=%s", writer.Code, writer.Body.String())
	}
	var snapshots []taskstate.State
	for _, frame := range strings.Split(writer.Body.String(), "\n\n") {
		if !strings.HasPrefix(frame, "event: message_snapshot\n") {
			continue
		}
		var event struct {
			Kind string `json:"kind"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(frame, "event: message_snapshot\ndata: ")), &event); err != nil {
			t.Fatal(err)
		}
		if event.Kind != "task.updated" || event.Text == "null" {
			continue
		}
		var snapshot taskstate.State
		if err := json.Unmarshal([]byte(event.Text), &snapshot); err != nil {
			t.Fatal(err)
		}
		snapshots = append(snapshots, snapshot)
	}
	if len(snapshots) != 1 || snapshots[0].Status != "blocked" || snapshots[0].Criteria[0].Description != task.Criteria[0].Description || snapshots[0].Remaining[0] != task.Remaining[0] {
		t.Fatalf("saved snapshot missing or truncated: %+v\n%s", snapshots, writer.Body.String())
	}
}

func TestConversationHistoryRestoresTaskOutsideTranscriptBudget(t *testing.T) {
	service := NewService(ServiceOptions{TaskToolEnabled: true, WorkspaceRoot: t.TempDir(), AgentDir: filepath.Join(t.TempDir(), ".agent")})
	saved, err := service.store.Create(session.CreateOptions{Task: "任务恢复", Chat: true})
	if err != nil {
		t.Fatal(err)
	}
	history, err := service.ConversationHistory(saved.ID(), 64)
	if err != nil || history.Task != nil {
		t.Fatalf("ordinary history: task=%+v err=%v", history.Task, err)
	}
	task := taskstate.State{Goal: "跨轮保留验收", Status: "active", Criteria: []taskstate.Criterion{{Description: strings.Repeat("验收条件", 300), Done: true, Evidence: "先前测试通过"}}, Remaining: []string{"修改后再次验证"}}
	if err := saved.SaveTaskState(task); err != nil {
		t.Fatal(err)
	}
	if err := saved.RecordMessages(map[string]any{"role": "assistant", "content": strings.Repeat("历史回答", 300)}); err != nil {
		t.Fatal(err)
	}
	if err := saved.SaveWorkingContext([]map[string]any{{"role": "user", "content": "继续"}}); err != nil {
		t.Fatal(err)
	}
	history, err = service.ConversationHistory(saved.ID(), 64)
	if err != nil || history.Task == nil || history.Task.Goal != task.Goal || history.Task.Criteria[0].Description != task.Criteria[0].Description || history.Task.Remaining[0] != task.Remaining[0] || history.Task.Status != "active" {
		t.Fatalf("task lost/truncated alongside history: task=%+v err=%v", history.Task, err)
	}
	// The HTTP response uses the same persisted task, not old tool arguments.
	h := &handler{service: service}
	writer := httptest.NewRecorder()
	h.handleConversationHistory(writer, httptest.NewRequest("GET", "/api/v1/webui/conversations/"+saved.ID(), nil))
	var payload struct {
		Task *taskstate.State `json:"task"`
	}
	if err := json.Unmarshal(writer.Body.Bytes(), &payload); err != nil || writer.Code != 200 || payload.Task == nil || payload.Task.Goal != task.Goal || payload.Task.Status != "active" || len(payload.Task.Remaining) != 1 {
		t.Fatalf("task response: status=%d body=%s err=%v", writer.Code, writer.Body.String(), err)
	}
}
