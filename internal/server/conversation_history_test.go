package server

import (
	"path/filepath"
	"strings"
	"testing"

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
	if _, err := other.ConversationHistory(saved.ID(), 128); err == nil || !strings.Contains(err.Error(), "another workspace") {
		t.Fatalf("cross-workspace error = %v", err)
	}
	models := service.ModelOptions()
	if len(models) != 3 || models[0].Alias != "default" || models[1].Alias != "fast" || models[2].ID != "model-direct" {
		t.Fatalf("models = %#v", models)
	}
}
