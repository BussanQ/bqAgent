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
