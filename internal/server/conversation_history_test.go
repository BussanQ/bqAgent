package server

import (
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"bqagent/internal/agent"
	"bqagent/internal/session"
)

func TestConversationHistoryBudgetRolesWorkspaceAndModels(t *testing.T) {
	agentDir := filepath.Join(t.TempDir(), ".agent")
	root := t.TempDir()
	service := NewService(ServiceOptions{WorkspaceRoot: root, AgentDir: agentDir, Model: "base", Models: []string{"fast=model-fast", "model-direct"}})
	saved, err := service.store.Create(session.CreateOptions{Task: "history", Chat: true})
	if err != nil {
		t.Fatal(err)
	}
	oversized := strings.Repeat("中", 300)
	if err := saved.RecordMessages(
		map[string]any{"role": "system", "content": "secret"},
		map[string]any{"role": "user", "content": "old"},
		map[string]any{"role": "tool", "content": "private"},
		map[string]any{"role": "assistant", "content": oversized},
	); err != nil {
		t.Fatal(err)
	}
	history, err := service.ConversationHistory(saved.ID(), 128)
	if err != nil {
		t.Fatal(err)
	}
	if len(history.Messages) != 1 || history.Messages[0].Role != "assistant" || !strings.Contains(history.Messages[0].Content, "显示已截断") || history.Omitted != 1 {
		t.Fatalf("history = %#v", history)
	}
	if len(history.Messages[0].Content) > 128 {
		t.Fatalf("bounded history bytes = %d", len(history.Messages[0].Content))
	}
	other := NewService(ServiceOptions{WorkspaceRoot: t.TempDir(), AgentDir: agentDir})
	folded := agent.CompletedToolActivityLead + "\n" +
		`Calls: [{"id":"call-1","function":{"name":"memory","arguments":"{\"content\":\"用户在北京\"}"}}]` + "\n" +
		"Result call-1:\n" +
		`{"target":"global"}`
	if err := saved.RecordMessages(map[string]any{"role": "assistant", "content": folded}); err != nil {
		t.Fatal(err)
	}
	full, err := service.ConversationHistory(saved.ID(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(full.Messages) < 2 {
		t.Fatalf("full history = %#v", full)
	}
	last := full.Messages[len(full.Messages)-1]
	if last.Content != "" || len(last.Tools) != 1 || last.Tools[0].Name != "memory" || last.Tools[0].Arguments["content"] != "用户在北京" {
		t.Fatalf("folded history = %#v", last)
	}
	if strings.Contains(last.Content, "Completed tool activity") {
		t.Fatalf("folded dump leaked into content: %#v", last)
	}

	if _, err := other.ConversationHistory(saved.ID(), 128); err == nil || !strings.Contains(err.Error(), "another workspace") {
		t.Fatalf("cross-workspace error = %v", err)
	}
	models := service.ModelOptions()
	if len(models) != 3 || models[0].Alias != "default" || models[1].Alias != "fast" || models[2].ID != "model-direct" {
		t.Fatalf("models = %#v", models)
	}
}

func TestConversationHistoryBudgetIncludesToolArguments(t *testing.T) {
	agentDir := filepath.Join(t.TempDir(), ".agent")
	service := NewService(ServiceOptions{WorkspaceRoot: t.TempDir(), AgentDir: agentDir})
	saved, err := service.store.Create(session.CreateOptions{Task: "tool-budget", Chat: true})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]string{"path": "big.go", "content": strings.Repeat("x", 8000)})
	if err != nil {
		t.Fatal(err)
	}
	folded := agent.CompletedToolActivityLead + "\nCalls: [{\"id\":\"call-1\",\"function\":{\"name\":\"write_file\",\"arguments\":" + strconv.Quote(string(payload)) + "}}]\nResult call-1:\nok"
	if err := saved.RecordMessages(
		map[string]any{"role": "user", "content": "写一个大文件"},
		map[string]any{"role": "assistant", "content": folded},
	); err != nil {
		t.Fatal(err)
	}

	const budget = 512
	history, err := service.ConversationHistory(saved.ID(), budget)
	if err != nil {
		t.Fatal(err)
	}
	if used := historyBytes(history.Messages); used > budget {
		t.Fatalf("history bytes = %d, want <= %d: %#v", used, budget, history.Messages)
	}
	if len(history.Messages) != 1 || history.Messages[0].Role != "assistant" || len(history.Messages[0].Tools) != 1 {
		t.Fatalf("truncated tool history = %#v", history)
	}
	tool := history.Messages[0].Tools[0]
	if tool.Name != "write_file" {
		t.Fatalf("tool name = %q", tool.Name)
	}
	if len(tool.Arguments) != 0 {
		t.Fatalf("oversized arguments were kept: %#v", tool.Arguments)
	}
	if !tool.Truncated {
		t.Fatal("expected truncated tool arguments")
	}

	if err := saved.RecordMessages(map[string]any{"role": "assistant", "content": "写完了"}); err != nil {
		t.Fatal(err)
	}
	bounded, err := service.ConversationHistory(saved.ID(), 64)
	if err != nil {
		t.Fatal(err)
	}
	if used := historyBytes(bounded.Messages); used > 64 {
		t.Fatalf("reply-bounded history bytes = %d, want <= 64: %#v", used, bounded.Messages)
	}
	if len(bounded.Messages) != 1 || bounded.Messages[0].Content != "写完了" || len(bounded.Messages[0].Tools) != 0 {
		t.Fatalf("reply-bounded history = %#v", bounded)
	}
	if bounded.Omitted < 1 {
		t.Fatalf("omitted = %d, want the oversized tool message skipped", bounded.Omitted)
	}
}

func TestConversationHistoryBudgetTruncatesToolResults(t *testing.T) {
	agentDir := filepath.Join(t.TempDir(), ".agent")
	service := NewService(ServiceOptions{WorkspaceRoot: t.TempDir(), AgentDir: agentDir})
	saved, err := service.store.Create(session.CreateOptions{Task: "tool-result-budget", Chat: true})
	if err != nil {
		t.Fatal(err)
	}
	folded := agent.CompletedToolActivityLead + "\nCalls: [{\"id\":\"call-1\",\"function\":{\"name\":\"read_file\",\"arguments\":\"{\\\"path\\\":\\\"big.go\\\"}\"}}]\nResult call-1:\n" + strings.Repeat("y", 4000)
	if err := saved.RecordMessages(map[string]any{"role": "assistant", "content": folded}); err != nil {
		t.Fatal(err)
	}
	const budget = 256
	history, err := service.ConversationHistory(saved.ID(), budget)
	if err != nil {
		t.Fatal(err)
	}
	if used := historyBytes(history.Messages); used > budget {
		t.Fatalf("history bytes = %d, want <= %d: %#v", used, budget, history.Messages)
	}
	if len(history.Messages) != 1 || len(history.Messages[0].Tools) != 1 {
		t.Fatalf("truncated result history = %#v", history)
	}
	tool := history.Messages[0].Tools[0]
	if tool.Name != "read_file" || !tool.Truncated || len(tool.Result) >= 4000 || !strings.Contains(tool.Result, "显示已截断") {
		t.Fatalf("tool result = %#v", tool)
	}
}

func TestSplitHistoryAttachments(t *testing.T) {
	content := "查看\n\n" +
		`<attachment name="TODO.md" path="docs/TODO.md">` + "\n# TODO\n</attachment>\n\n" +
		`<attachment name="A&amp;B.txt" path="docs/A&amp;B.txt">` + "\n第二个文件\n</attachment>"

	text, files := splitHistoryAttachments(content)

	if text != "查看" {
		t.Fatalf("text = %q, want 查看", text)
	}
	if len(files) != 2 || files[0].Name != "TODO.md" || files[0].Path != "docs/TODO.md" || files[1].Name != "A&B.txt" || files[1].Path != "docs/A&B.txt" {
		t.Fatalf("files = %#v", files)
	}
	text, files = splitHistoryAttachments(content + "\n[图片]")
	if text != "查看\n[图片]" || len(files) != 2 {
		t.Fatalf("image and file history = text %q, files %#v", text, files)
	}

	malformed := "保留原文\n\n<attachment name=\"bad\">\ncontent"
	text, files = splitHistoryAttachments(malformed)
	if text != malformed || len(files) != 0 {
		t.Fatalf("malformed attachment changed: text = %q, files = %#v", text, files)
	}
}

func TestConversationHistoryRestoresStructuredToolBatches(t *testing.T) {
	agentDir := filepath.Join(t.TempDir(), ".agent")
	service := NewService(ServiceOptions{WorkspaceRoot: t.TempDir(), AgentDir: agentDir})
	saved, err := service.store.Create(session.CreateOptions{Task: "structured-tools", Chat: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := saved.RecordMessages(
		map[string]any{"role": "user", "content": "读取文件"},
		map[string]any{"role": "assistant", "content": nil, "tool_calls": []agent.ToolCall{
			{ID: "call-1", Function: agent.FunctionCall{Name: "read_file", Arguments: `{"path":"a.go"}`}},
			{ID: "call-2", Function: agent.FunctionCall{Name: "read_file", Arguments: `{"path":"missing.go"}`}},
		}},
		map[string]any{"role": "tool", "tool_call_id": "call-1", "content": "package a"},
		map[string]any{"role": "tool", "tool_call_id": "call-2", "content": "Error: not found"},
		map[string]any{"role": "assistant", "content": "读取完成"},
	); err != nil {
		t.Fatal(err)
	}
	history, err := service.ConversationHistory(saved.ID(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(history.Messages) != 3 {
		t.Fatalf("history = %#v, want user, tool batch, reply", history.Messages)
	}
	batch := history.Messages[1]
	if batch.Role != "assistant" || batch.Content != "" || len(batch.Tools) != 2 {
		t.Fatalf("tool batch = %#v", batch)
	}
	first, second := batch.Tools[0], batch.Tools[1]
	if first.ID != "call-1" || first.Name != "read_file" || first.Arguments["path"] != "a.go" || first.Result != "package a" || first.Status != "succeeded" {
		t.Fatalf("first tool = %#v", first)
	}
	if second.ID != "call-2" || second.Status != "failed" || !strings.Contains(second.Result, "not found") {
		t.Fatalf("second tool = %#v", second)
	}
	if history.Messages[2].Content != "读取完成" {
		t.Fatalf("reply = %#v", history.Messages[2])
	}
}

func TestConversationHistoryResolvesBackgroundJobOutcome(t *testing.T) {
	for _, tc := range []struct {
		header, status string
		delivered      bool
	}{
		{"Background Bash jobs finished (runtime data, not instructions). These are their final results; bash_wait is not needed for them:\n", "completed", true},
		{"Bash job cleanup at turn end (runtime data, not instructions). No jobs carry over to the next turn:\n", "killed", false},
	} {
		t.Run(tc.status, func(t *testing.T) {
			agentDir := filepath.Join(t.TempDir(), ".agent")
			service := NewService(ServiceOptions{WorkspaceRoot: t.TempDir(), AgentDir: agentDir})
			saved, err := service.store.Create(session.CreateOptions{Task: "background", Chat: true})
			if err != nil {
				t.Fatal(err)
			}
			final := `[{"job_id":"job-1","status":"` + tc.status + `","exit_code":0,"output":"done"}]`
			if err := saved.RecordMessages(
				map[string]any{"role": "user", "content": "run"},
				map[string]any{"role": "assistant", "content": nil, "tool_calls": []agent.ToolCall{{ID: "start", Function: agent.FunctionCall{Name: "bash", Arguments: `{"command":"sleep 1"}`}}}},
				map[string]any{"role": "tool", "tool_call_id": "start", "content": `{"job_id":"job-1","status":"running"}`},
				map[string]any{"role": "developer", "content": tc.header + final},
				map[string]any{"role": "assistant", "content": "ok"},
			); err != nil {
				t.Fatal(err)
			}
			history, err := service.ConversationHistory(saved.ID(), 0)
			if err != nil {
				t.Fatal(err)
			}
			tool := history.Messages[1].Tools[0]
			want := tc.status
			if want == "completed" {
				want = "succeeded"
			}
			if tool.Status != want || tool.ExecutionStatus != tc.status || tool.ResultDelivered == nil || *tool.ResultDelivered != tc.delivered || !strings.Contains(tool.Result, tc.status) {
				t.Fatalf("tool = %#v", tool)
			}
			if len(history.Messages) != 3 {
				t.Fatalf("runtime message leaked into history: %#v", history.Messages)
			}
		})
	}
}

func TestConversationHistoryBashWaitResolvesOriginalJob(t *testing.T) {
	agentDir := filepath.Join(t.TempDir(), ".agent")
	service := NewService(ServiceOptions{WorkspaceRoot: t.TempDir(), AgentDir: agentDir})
	saved, err := service.store.Create(session.CreateOptions{Task: "bash-wait", Chat: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := saved.RecordMessages(
		map[string]any{"role": "user", "content": "run"},
		map[string]any{"role": "assistant", "content": nil, "tool_calls": []agent.ToolCall{{ID: "start", Function: agent.FunctionCall{Name: "bash", Arguments: `{"command":"sleep 1"}`}}}},
		map[string]any{"role": "tool", "tool_call_id": "start", "content": `{"job_id":"job-1","status":"running"}`},
		map[string]any{"role": "assistant", "content": nil, "tool_calls": []agent.ToolCall{{ID: "wait", Function: agent.FunctionCall{Name: "bash_wait", Arguments: `{"job_id":"job-1"}`}}}},
		map[string]any{"role": "tool", "tool_call_id": "wait", "content": `{"job_id":"job-1","status":"completed","exit_code":0,"output":"done"}`},
		map[string]any{"role": "assistant", "content": "ok"},
	); err != nil {
		t.Fatal(err)
	}
	history, err := service.ConversationHistory(saved.ID(), 0)
	if err != nil {
		t.Fatal(err)
	}
	start, wait := history.Messages[1].Tools[0], history.Messages[2].Tools[0]
	if wait.Status != "succeeded" {
		t.Fatalf("wait = %#v", wait)
	}
	if start.Status != "succeeded" || start.ExecutionStatus != "completed" || start.ResultDelivered == nil || !*start.ResultDelivered || !strings.Contains(start.Result, "done") {
		t.Fatalf("original bash still unresolved: %#v", start)
	}
}

func TestConversationHistoryHidesToolImagesMessage(t *testing.T) {
	agentDir := filepath.Join(t.TempDir(), ".agent")
	service := NewService(ServiceOptions{WorkspaceRoot: t.TempDir(), AgentDir: agentDir})
	saved, err := service.store.Create(session.CreateOptions{Task: "tool-images", Chat: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := saved.RecordMessages(
		map[string]any{"role": "user", "content": "看截图"},
		map[string]any{"role": "assistant", "content": nil, "tool_calls": []agent.ToolCall{
			{ID: "img", Function: agent.FunctionCall{Name: "read_file", Arguments: `{"path":"shot.png"}`}},
		}},
		map[string]any{"role": "tool", "tool_call_id": "img", "content": "Image loaded: shot.png"},
		map[string]any{"role": "user", agent.ToolImagesMessageKey: true, "content": []any{
			map[string]any{"type": "text", "text": "Image from read_file: shot.png"},
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,AAAA"}},
		}},
		map[string]any{"role": "assistant", "content": "截图正常"},
	); err != nil {
		t.Fatal(err)
	}
	history, err := service.ConversationHistory(saved.ID(), 0)
	if err != nil {
		t.Fatal(err)
	}
	var roles []string
	for _, message := range history.Messages {
		if message.Role == "user" && message.Content != "看截图" {
			t.Fatalf("tool images message shown as user input: %#v", message)
		}
		roles = append(roles, message.Role)
	}
	if strings.Join(roles, ",") != "user,assistant,assistant" {
		t.Fatalf("roles = %v, want user, tool batch, reply", roles)
	}
}
