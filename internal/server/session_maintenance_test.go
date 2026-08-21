package server

import (
	"os"
	"path/filepath"
	"testing"

	"bqagent/internal/session"
)

func TestNewServiceStoresSessionsInGlobalAgentDirectory(t *testing.T) {
	workspaceRoot := t.TempDir()
	agentDir := filepath.Join(t.TempDir(), ".agent")
	service := NewService(ServiceOptions{WorkspaceRoot: workspaceRoot, AgentDir: agentDir})

	savedSession, err := service.store.Create(session.CreateOptions{Task: "global session"})
	if err != nil {
		t.Fatal(err)
	}
	if savedSession.Dir() != filepath.Join(agentDir, "sessions", savedSession.ID()) {
		t.Fatalf("session dir = %q, want global agent directory", savedSession.Dir())
	}
	if _, err := os.Stat(filepath.Join(workspaceRoot, ".agent")); !os.IsNotExist(err) {
		t.Fatalf("service created workspace .agent: %v", err)
	}
}

func TestNewServiceMaintainsExistingCompactSessions(t *testing.T) {
	root := t.TempDir()
	fullStore := session.NewStore(root, session.Options{TranscriptMode: session.TranscriptModeFull, OutputMaxBytes: session.DefaultOutputMaxBytes})
	savedSession, err := fullStore.Create(session.CreateOptions{Task: "legacy"})
	if err != nil {
		t.Fatal(err)
	}
	if err := savedSession.RecordMessage(map[string]any{"role": "tool", "content": "legacy raw result"}); err != nil {
		t.Fatal(err)
	}
	if err := savedSession.SaveWorkingMessages([]map[string]any{{"role": "assistant", "content": "compact summary"}}); err != nil {
		t.Fatal(err)
	}
	if err := savedSession.MarkCompleted(); err != nil {
		t.Fatal(err)
	}

	options := session.Options{TranscriptMode: session.TranscriptModeCompact, OutputMaxBytes: session.DefaultOutputMaxBytes}
	_ = NewService(ServiceOptions{WorkspaceRoot: root, SessionOptions: &options})
	messages, err := savedSession.LoadMessages()
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0]["content"] != "compact summary" {
		t.Fatalf("maintained messages = %#v", messages)
	}
}
