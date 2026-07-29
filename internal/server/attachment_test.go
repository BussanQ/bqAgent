package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bqagent/internal/session"
)

func TestAttachmentUploadPersistsAndInjectsText(t *testing.T) {
	root := t.TempDir()
	client := &modelRecordingClient{}
	service := NewService(ServiceOptions{WorkspaceRoot: root, Client: client, Model: "test-model", SystemPrompt: "base prompt"})
	content := "attachment secret text"

	response, err := service.HandleTurn(context.Background(), TurnRequest{
		Message: "summarize",
		Files:   []FileAttachment{{Name: "notes.txt", Data: []byte(content)}},
	})
	if err != nil {
		t.Fatalf("HandleTurn returned error: %v", err)
	}
	relative := filepath.Join(".agent", "uploads", response.SessionID, "notes.txt")
	stored, err := os.ReadFile(filepath.Join(root, relative))
	if err != nil {
		t.Fatalf("read uploaded file: %v", err)
	}
	if string(stored) != content {
		t.Fatalf("stored content = %q, want %q", stored, content)
	}
	userText := recordedUserText(t, client.messages[0])
	if !strings.Contains(userText, content) || !strings.Contains(userText, filepath.ToSlash(relative)) {
		t.Fatalf("user text = %q, want attachment content and path", userText)
	}
	transcript, err := os.ReadFile(filepath.Join(root, ".agent", "sessions", response.SessionID, "messages.jsonl"))
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	if strings.Contains(string(transcript), "data_base64") {
		t.Fatalf("transcript contains upload base64 field: %s", transcript)
	}
}

func TestAttachmentWorkspacePathReadsWithoutCopy(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "design.md"), []byte("workspace design"), 0o644); err != nil {
		t.Fatal(err)
	}
	client := &modelRecordingClient{}
	service := NewService(ServiceOptions{WorkspaceRoot: root, Client: client, Model: "test-model", SystemPrompt: "base prompt"})

	if _, err := service.HandleTurn(context.Background(), TurnRequest{Files: []FileAttachment{{Path: "docs/design.md"}}}); err != nil {
		t.Fatalf("HandleTurn returned error: %v", err)
	}
	userText := recordedUserText(t, client.messages[0])
	if !strings.Contains(userText, "workspace design") || !strings.Contains(userText, `path="docs/design.md"`) {
		t.Fatalf("user text = %q", userText)
	}
	if _, err := os.Stat(filepath.Join(root, ".agent", "uploads")); !os.IsNotExist(err) {
		t.Fatalf("path attachment unexpectedly created upload directory: %v", err)
	}
}

func TestAttachmentRejectsNonRegularWorkspacePathBeforeLLM(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	client := &modelRecordingClient{}
	service := NewService(ServiceOptions{WorkspaceRoot: root, Client: client, Model: "test-model", SystemPrompt: "base prompt"})

	if _, err := service.HandleTurn(context.Background(), TurnRequest{Files: []FileAttachment{{Path: "docs"}}}); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("directory attachment error = %v, want regular file error", err)
	}
	if len(client.models) != 0 {
		t.Fatalf("LLM calls = %d, want 0", len(client.models))
	}
}

func TestAttachmentRejectsPathOutsideWorkspaceBeforeLLM(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	client := &modelRecordingClient{}
	service := NewService(ServiceOptions{WorkspaceRoot: root, Client: client, Model: "test-model", SystemPrompt: "base prompt"})

	if _, err := service.HandleTurn(context.Background(), TurnRequest{Files: []FileAttachment{{Path: outside}}}); err == nil {
		t.Fatal("HandleTurn accepted path outside workspace")
	}
	if len(client.models) != 0 {
		t.Fatalf("LLM calls = %d, want 0", len(client.models))
	}
}

func TestAttachmentBinaryAndTruncationContext(t *testing.T) {
	root := t.TempDir()
	service := NewService(ServiceOptions{WorkspaceRoot: root, Model: "test-model", SystemPrompt: "base prompt"})
	savedSession, err := service.store.Create(sessionCreateOptions("attachments"))
	if err != nil {
		t.Fatal(err)
	}

	resolved, err := service.materializeFiles(savedSession.ID(), []FileAttachment{
		{Name: "binary.bin", Data: []byte{0xff, 0x00, 0x01}},
		{Name: "long.txt", Data: []byte(strings.Repeat("界", maxAttachmentInlineBytes))},
	})
	if err != nil {
		t.Fatalf("materializeFiles returned error: %v", err)
	}
	contextText := formatAttachmentContext(resolved)
	if !strings.Contains(contextText, "二进制或非 UTF-8") {
		t.Fatalf("binary context = %q", contextText)
	}
	if !strings.Contains(contextText, "内容已截断") {
		t.Fatalf("truncated context missing marker")
	}
	if !strings.Contains(contextText, "</attachment>") {
		t.Fatalf("attachment context malformed: %q", contextText)
	}
}

func TestAttachmentSameNameDoesNotOverwrite(t *testing.T) {
	root := t.TempDir()
	client := &modelRecordingClient{}
	service := NewService(ServiceOptions{WorkspaceRoot: root, Client: client, Model: "test-model", SystemPrompt: "base prompt"})
	first, err := service.HandleTurn(context.Background(), TurnRequest{Files: []FileAttachment{{Name: "notes.txt", Data: []byte("first")}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.HandleTurn(context.Background(), TurnRequest{SessionID: first.SessionID, Files: []FileAttachment{{Name: "notes.txt", Data: []byte("second")}}}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, ".agent", "uploads", first.SessionID)
	firstData, err := os.ReadFile(filepath.Join(dir, "notes.txt"))
	if err != nil {
		t.Fatal(err)
	}
	secondData, err := os.ReadFile(filepath.Join(dir, "notes-2.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(firstData) != "first" || string(secondData) != "second" {
		t.Fatalf("uploaded contents = %q, %q", firstData, secondData)
	}
}

func recordedUserText(t *testing.T, messages []map[string]any) string {
	t.Helper()
	for _, message := range messages {
		if message["role"] == "user" {
			content, ok := message["content"].(string)
			if !ok {
				t.Fatalf("user content = %#v, want string", message["content"])
			}
			return content
		}
	}
	t.Fatalf("messages = %#v, missing user message", messages)
	return ""
}

func sessionCreateOptions(task string) session.CreateOptions {
	return session.CreateOptions{Task: task, Chat: true}
}
