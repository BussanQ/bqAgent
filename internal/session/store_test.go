package session

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestSessionStorePersistsMessagesAndStatus(t *testing.T) {
	store := NewStore(t.TempDir())
	savedSession, err := store.Create(CreateOptions{Task: "inspect repo", Planned: true, Background: true})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if savedSession.Meta().Status != StatusCreated {
		t.Fatalf("initial status = %q, want %q", savedSession.Meta().Status, StatusCreated)
	}

	messages := []map[string]any{
		{"role": "system", "content": "prompt"},
		{"role": "user", "content": "hello"},
	}
	if err := savedSession.RecordMessages(messages...); err != nil {
		t.Fatalf("RecordMessages returned error: %v", err)
	}
	if err := savedSession.MarkRunning(); err != nil {
		t.Fatalf("MarkRunning returned error: %v", err)
	}
	if err := savedSession.MarkFailed(errors.New("boom")); err != nil {
		t.Fatalf("MarkFailed returned error: %v", err)
	}

	reopened, err := store.Open(savedSession.ID())
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	loadedMessages, err := reopened.LoadMessages()
	if err != nil {
		t.Fatalf("LoadMessages returned error: %v", err)
	}
	if len(loadedMessages) != len(messages) {
		t.Fatalf("loaded messages = %d, want %d", len(loadedMessages), len(messages))
	}
	if reopened.Meta().Status != StatusFailed {
		t.Fatalf("reopened status = %q, want %q", reopened.Meta().Status, StatusFailed)
	}
	if reopened.Meta().LastError != "boom" {
		t.Fatalf("last error = %q, want %q", reopened.Meta().LastError, "boom")
	}
}

func TestSessionStorePersistsCheckpoint(t *testing.T) {
	store := NewStore(t.TempDir())
	savedSession, err := store.Create(CreateOptions{Task: "inspect repo", Chat: true})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	tail := []map[string]any{{"role": "user", "content": "continue here"}}
	if err := savedSession.SaveCheckpointSummary("important summary", tail, "system prompt"); err != nil {
		t.Fatalf("SaveCheckpointSummary returned error: %v", err)
	}

	checkpoint, err := savedSession.LoadCheckpoint()
	if err != nil {
		t.Fatalf("LoadCheckpoint returned error: %v", err)
	}
	if checkpoint.Summary != "important summary" {
		t.Fatalf("checkpoint summary = %q, want %q", checkpoint.Summary, "important summary")
	}
	if checkpoint.SystemPrompt != "system prompt" {
		t.Fatalf("checkpoint system prompt = %q, want %q", checkpoint.SystemPrompt, "system prompt")
	}
	if len(checkpoint.TailMessages) != 1 {
		t.Fatalf("checkpoint tail messages = %d, want 1", len(checkpoint.TailMessages))
	}
	if checkpoint.TailMessages[0]["content"] != "continue here" {
		t.Fatalf("checkpoint tail content = %#v, want %q", checkpoint.TailMessages[0]["content"], "continue here")
	}
}

func TestSessionStorePersistsWorkingMessagesSeparatelyFromTranscript(t *testing.T) {
	store := NewStore(t.TempDir())
	savedSession, err := store.Create(CreateOptions{Task: "inspect repo", Chat: true})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	transcript := []map[string]any{{"role": "user", "content": "full raw history"}}
	working := []map[string]any{{"role": "assistant", "content": "compact working summary"}}
	if err := savedSession.RecordMessages(transcript...); err != nil {
		t.Fatalf("RecordMessages returned error: %v", err)
	}
	if err := savedSession.SaveWorkingMessages(working); err != nil {
		t.Fatalf("SaveWorkingMessages returned error: %v", err)
	}

	loadedWorking, err := savedSession.LoadWorkingMessages()
	if err != nil {
		t.Fatalf("LoadWorkingMessages returned error: %v", err)
	}
	loadedTranscript, err := savedSession.LoadMessages()
	if err != nil {
		t.Fatalf("LoadMessages returned error: %v", err)
	}
	if len(loadedWorking) != 1 || loadedWorking[0]["content"] != "compact working summary" {
		t.Fatalf("working messages = %#v", loadedWorking)
	}
	if len(loadedTranscript) != 1 || loadedTranscript[0]["content"] != "full raw history" {
		t.Fatalf("transcript was changed: %#v", loadedTranscript)
	}
}

func TestSessionSaveWorkingContextCompactsTranscript(t *testing.T) {
	store := NewStore(t.TempDir(), Options{TranscriptMode: TranscriptModeCompact, OutputMaxBytes: DefaultOutputMaxBytes})
	savedSession, err := store.Create(CreateOptions{Task: "compact"})
	if err != nil {
		t.Fatal(err)
	}
	if err := savedSession.RecordMessages(
		map[string]any{"role": "user", "content": "raw history"},
		map[string]any{"role": "tool", "content": "large tool result"},
	); err != nil {
		t.Fatal(err)
	}
	working := []map[string]any{{"role": "assistant", "content": "bounded summary"}}
	if err := savedSession.SaveWorkingContext(working); err != nil {
		t.Fatal(err)
	}
	transcript, err := savedSession.LoadMessages()
	if err != nil {
		t.Fatal(err)
	}
	if len(transcript) != 1 || transcript[0]["content"] != "bounded summary" {
		t.Fatalf("transcript = %#v, want compact working snapshot", transcript)
	}
}

func TestSessionSaveWorkingContextFullModePreservesTranscript(t *testing.T) {
	store := NewStore(t.TempDir(), Options{TranscriptMode: TranscriptModeFull, OutputMaxBytes: DefaultOutputMaxBytes})
	savedSession, err := store.Create(CreateOptions{Task: "full"})
	if err != nil {
		t.Fatal(err)
	}
	if err := savedSession.RecordMessage(map[string]any{"role": "tool", "content": "full tool result"}); err != nil {
		t.Fatal(err)
	}
	if err := savedSession.SaveWorkingContext([]map[string]any{{"role": "assistant", "content": "summary"}}); err != nil {
		t.Fatal(err)
	}
	transcript, err := savedSession.LoadMessages()
	if err != nil {
		t.Fatal(err)
	}
	if len(transcript) != 1 || transcript[0]["content"] != "full tool result" {
		t.Fatalf("transcript = %#v, want original full transcript", transcript)
	}
}

func TestSessionStoreMaintainsExistingCompactSessions(t *testing.T) {
	root := t.TempDir()
	fullStore := NewStore(root, Options{TranscriptMode: TranscriptModeFull, OutputMaxBytes: 32})
	compact, err := fullStore.Create(CreateOptions{Task: "compact me"})
	if err != nil {
		t.Fatal(err)
	}
	if err := compact.RecordMessage(map[string]any{"role": "tool", "content": "raw result"}); err != nil {
		t.Fatal(err)
	}
	working := []map[string]any{{"role": "assistant", "content": "working summary"}}
	if err := compact.SaveWorkingMessages(working); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(compact.OutputPath(), []byte("old-line-that-will-be-removed\nlatest-line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := compact.MarkCompleted(); err != nil {
		t.Fatal(err)
	}

	running, err := fullStore.Create(CreateOptions{Task: "running"})
	if err != nil {
		t.Fatal(err)
	}
	if err := running.RecordMessage(map[string]any{"role": "user", "content": "keep running raw"}); err != nil {
		t.Fatal(err)
	}
	if err := running.SaveWorkingMessages(working); err != nil {
		t.Fatal(err)
	}
	if err := running.MarkRunning(); err != nil {
		t.Fatal(err)
	}

	invalid, err := fullStore.Create(CreateOptions{Task: "invalid"})
	if err != nil {
		t.Fatal(err)
	}
	if err := invalid.RecordMessage(map[string]any{"role": "user", "content": "keep invalid raw"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(invalid.WorkingMessagesPath(), []byte("not-json\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := invalid.MarkCompleted(); err != nil {
		t.Fatal(err)
	}

	store := NewStore(root, Options{TranscriptMode: TranscriptModeCompact, OutputMaxBytes: 32})
	errorsFound := store.MaintainExistingSessions()
	if len(errorsFound) != 1 {
		t.Fatalf("maintenance errors = %v, want one invalid working snapshot error", errorsFound)
	}
	compactedMessages, _ := compact.LoadMessages()
	if len(compactedMessages) != 1 || compactedMessages[0]["content"] != "working summary" {
		t.Fatalf("compacted messages = %#v", compactedMessages)
	}
	runningMessages, _ := running.LoadMessages()
	if len(runningMessages) != 1 || runningMessages[0]["content"] != "keep running raw" {
		t.Fatalf("running messages changed: %#v", runningMessages)
	}
	invalidMessages, _ := invalid.LoadMessages()
	if len(invalidMessages) != 1 || invalidMessages[0]["content"] != "keep invalid raw" {
		t.Fatalf("invalid messages changed: %#v", invalidMessages)
	}
	trimmed, err := os.ReadFile(compact.OutputPath())
	if err != nil {
		t.Fatal(err)
	}
	if len(trimmed) > 32 || !strings.Contains(string(trimmed), "latest-line") {
		t.Fatalf("trimmed output = %q", trimmed)
	}
}

func TestSessionTrimOutputLogKeepsLatestBytes(t *testing.T) {
	store := NewStore(t.TempDir(), Options{TranscriptMode: TranscriptModeCompact, OutputMaxBytes: 24})
	savedSession, err := store.Create(CreateOptions{Task: "logs"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(savedSession.OutputPath(), []byte("old-line-1\nold-line-2\nlatest-line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := savedSession.TrimOutputLog(); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(savedSession.OutputPath())
	if err != nil {
		t.Fatal(err)
	}
	if len(content) > 24 || !strings.Contains(string(content), "latest-line") || strings.Contains(string(content), "old-line-1") {
		t.Fatalf("trimmed output = %q", content)
	}
}
