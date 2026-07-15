package runtime

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"bqagent/internal/agent"
	"bqagent/internal/session"
)

func TestAddUserMessageWithImagesPlainText(t *testing.T) {
	conversation, err := PrepareConversation(nil, "", nil, "system prompt")
	if err != nil {
		t.Fatalf("PrepareConversation returned error: %v", err)
	}
	if err := conversation.AddUserMessageWithImages("hello", nil); err != nil {
		t.Fatalf("AddUserMessageWithImages returned error: %v", err)
	}
	last := conversation.Messages[len(conversation.Messages)-1]
	if content, ok := last["content"].(string); !ok || content != "hello" {
		t.Fatalf("content = %#v, want plain string \"hello\"", last["content"])
	}
}

func TestAddUserMessageWithImagesBuildsMultimodalContent(t *testing.T) {
	conversation, err := PrepareConversation(nil, "", nil, "system prompt")
	if err != nil {
		t.Fatalf("PrepareConversation returned error: %v", err)
	}
	images := []agent.ImageAttachment{{MIMEType: "image/png", Data: []byte{1, 2, 3}}}
	if err := conversation.AddUserMessageWithImages("describe", images); err != nil {
		t.Fatalf("AddUserMessageWithImages returned error: %v", err)
	}
	last := conversation.Messages[len(conversation.Messages)-1]
	parts, ok := last["content"].([]any)
	if !ok {
		t.Fatalf("content = %#v, want []any", last["content"])
	}
	if len(parts) != 2 {
		t.Fatalf("parts = %d, want 2 (text + image)", len(parts))
	}
	textPart, _ := parts[0].(map[string]any)
	if textPart["type"] != "text" || textPart["text"] != "describe" {
		t.Fatalf("text part = %#v", parts[0])
	}
	imagePart, _ := parts[1].(map[string]any)
	if imagePart["type"] != "image_url" {
		t.Fatalf("image part type = %#v, want image_url", imagePart["type"])
	}
	imageURL, _ := imagePart["image_url"].(map[string]any)
	url, _ := imageURL["url"].(string)
	if !strings.HasPrefix(url, "data:image/png;base64,") {
		t.Fatalf("image url = %q, want data:image/png;base64, prefix", url)
	}
}

func TestAddUserMessageWithImagesImageOnly(t *testing.T) {
	conversation, err := PrepareConversation(nil, "", nil, "system prompt")
	if err != nil {
		t.Fatalf("PrepareConversation returned error: %v", err)
	}
	images := []agent.ImageAttachment{{MIMEType: "image/jpeg", Data: []byte{9}}}
	if err := conversation.AddUserMessageWithImages("", images); err != nil {
		t.Fatalf("AddUserMessageWithImages returned error: %v", err)
	}
	parts, ok := conversation.Messages[len(conversation.Messages)-1]["content"].([]any)
	if !ok {
		t.Fatalf("content not multimodal: %#v", conversation.Messages[len(conversation.Messages)-1]["content"])
	}
	if len(parts) != 1 {
		t.Fatalf("parts = %d, want 1 (image only)", len(parts))
	}
}

func TestPrepareConversationInitializesSystemMessageWithoutSession(t *testing.T) {
	conversation, err := PrepareConversation(nil, "", nil, "system prompt")
	if err != nil {
		t.Fatalf("PrepareConversation returned error: %v", err)
	}
	if conversation.Session != nil {
		t.Fatal("conversation session = non-nil, want nil")
	}
	if len(conversation.Messages) != 1 {
		t.Fatalf("messages length = %d, want 1", len(conversation.Messages))
	}
	if conversation.Messages[0]["role"] != "system" {
		t.Fatalf("first message role = %#v, want system", conversation.Messages[0]["role"])
	}
	if conversation.Messages[0]["content"] != "system prompt" {
		t.Fatalf("first message content = %#v, want system prompt", conversation.Messages[0]["content"])
	}
}

func TestPrepareConversationCreatesSessionAndPersistsSystemMessage(t *testing.T) {
	store := session.NewStore(t.TempDir())
	createOptions := &session.CreateOptions{Task: "hello", Chat: true}

	conversation, err := PrepareConversation(store, "", createOptions, "system prompt")
	if err != nil {
		t.Fatalf("PrepareConversation returned error: %v", err)
	}
	if conversation.Session == nil {
		t.Fatal("conversation session = nil, want created session")
	}
	if len(conversation.Messages) != 1 {
		t.Fatalf("messages length = %d, want 1", len(conversation.Messages))
	}

	savedSession, err := store.Open(conversation.Session.ID())
	if err != nil {
		t.Fatalf("failed to reopen session: %v", err)
	}
	if savedSession.Meta().Status != session.StatusRunning {
		t.Fatalf("session status = %q, want %q", savedSession.Meta().Status, session.StatusRunning)
	}
	messages, err := savedSession.LoadMessages()
	if err != nil {
		t.Fatalf("failed to load messages: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("persisted messages length = %d, want 1", len(messages))
	}
	if messages[0]["content"] != "system prompt" {
		t.Fatalf("persisted system message content = %#v, want system prompt", messages[0]["content"])
	}
}

func TestPrepareConversationRefreshesExistingSystemMessage(t *testing.T) {
	store := session.NewStore(t.TempDir())
	createOptions := &session.CreateOptions{Task: "hello", Chat: true}

	conversation, err := PrepareConversation(store, "", createOptions, "old prompt")
	if err != nil {
		t.Fatalf("PrepareConversation returned error: %v", err)
	}
	if err := conversation.AddUserMessage("hi"); err != nil {
		t.Fatalf("AddUserMessage returned error: %v", err)
	}

	refreshed, err := PrepareConversation(store, conversation.Session.ID(), nil, "new prompt")
	if err != nil {
		t.Fatalf("PrepareConversation refresh returned error: %v", err)
	}
	if len(refreshed.Messages) != 2 {
		t.Fatalf("messages length = %d, want 2", len(refreshed.Messages))
	}
	if refreshed.Messages[0]["role"] != "system" {
		t.Fatalf("first message role = %#v, want system", refreshed.Messages[0]["role"])
	}
	if refreshed.Messages[0]["content"] != "new prompt" {
		t.Fatalf("first message content = %#v, want new prompt", refreshed.Messages[0]["content"])
	}

	savedSession, err := store.Open(conversation.Session.ID())
	if err != nil {
		t.Fatalf("failed to reopen session: %v", err)
	}
	messages, err := savedSession.LoadMessages()
	if err != nil {
		t.Fatalf("failed to load messages: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("persisted messages length = %d, want 2", len(messages))
	}
	if messages[0]["content"] != "new prompt" {
		t.Fatalf("persisted first message content = %#v, want new prompt", messages[0]["content"])
	}
}

func TestPrepareConversationPrefersWorkingContextSnapshot(t *testing.T) {
	store := session.NewStore(t.TempDir())
	conversation, err := PrepareConversation(store, "", &session.CreateOptions{Task: "hello", Chat: true}, "system prompt")
	if err != nil {
		t.Fatalf("PrepareConversation returned error: %v", err)
	}
	if err := conversation.Session.RecordMessages(
		map[string]any{"role": "user", "content": "large raw history"},
		map[string]any{"role": "assistant", "content": "raw reply"},
	); err != nil {
		t.Fatalf("RecordMessages returned error: %v", err)
	}
	working := []map[string]any{
		{"role": "system", "content": "system prompt"},
		{"role": "assistant", "content": "compact summary"},
	}
	if err := conversation.Session.SaveWorkingMessages(working); err != nil {
		t.Fatalf("SaveWorkingMessages returned error: %v", err)
	}

	restored, err := PrepareConversation(store, conversation.Session.ID(), nil, "system prompt")
	if err != nil {
		t.Fatalf("PrepareConversation restore returned error: %v", err)
	}
	if !restored.UsingWorkingContext {
		t.Fatal("restored conversation did not use working context")
	}
	if len(restored.Messages) != 2 || restored.Messages[1]["content"] != "compact summary" {
		t.Fatalf("restored messages = %#v", restored.Messages)
	}
	raw, err := restored.Session.LoadMessages()
	if err != nil {
		t.Fatalf("LoadMessages returned error: %v", err)
	}
	if len(raw) != 3 {
		t.Fatalf("raw transcript messages = %d, want 3", len(raw))
	}
}

func TestPrepareConversationUsesNewerTranscriptAfterInterruptedTurn(t *testing.T) {
	store := session.NewStore(t.TempDir(), session.Options{TranscriptMode: session.TranscriptModeCompact, OutputMaxBytes: session.DefaultOutputMaxBytes})
	conversation, err := PrepareConversation(store, "", &session.CreateOptions{Task: "hello", Chat: true}, "system prompt")
	if err != nil {
		t.Fatal(err)
	}
	working := []map[string]any{{"role": "system", "content": "system prompt"}, {"role": "assistant", "content": "previous summary"}}
	if err := conversation.Session.SaveWorkingMessages(working); err != nil {
		t.Fatal(err)
	}
	if err := conversation.Session.RecordMessage(map[string]any{"role": "user", "content": "interrupted request"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := os.Chtimes(conversation.Session.WorkingMessagesPath(), now.Add(-time.Second), now.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(conversation.Session.MessagesPath(), now, now); err != nil {
		t.Fatal(err)
	}
	restored, err := PrepareConversation(store, conversation.Session.ID(), nil, "system prompt")
	if err != nil {
		t.Fatal(err)
	}
	if restored.UsingWorkingContext {
		t.Fatal("restored stale working context instead of newer transcript")
	}
	if restored.Messages[len(restored.Messages)-1]["content"] != "interrupted request" {
		t.Fatalf("restored messages = %#v", restored.Messages)
	}
}

func TestPrepareConversationFallsBackWhenWorkingSnapshotIsInvalid(t *testing.T) {
	store := session.NewStore(t.TempDir())
	conversation, err := PrepareConversation(store, "", &session.CreateOptions{Task: "hello", Chat: true}, "system prompt")
	if err != nil {
		t.Fatal(err)
	}
	if err := conversation.AddUserMessage("recover from transcript"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(conversation.Session.WorkingMessagesPath(), []byte("not-json\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := os.Chtimes(conversation.Session.WorkingMessagesPath(), now, now); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(conversation.Session.MessagesPath(), now.Add(-time.Second), now.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	restored, err := PrepareConversation(store, conversation.Session.ID(), nil, "system prompt")
	if err != nil {
		t.Fatal(err)
	}
	if restored.UsingWorkingContext || restored.Messages[len(restored.Messages)-1]["content"] != "recover from transcript" {
		t.Fatalf("restored messages = %#v", restored.Messages)
	}
}

func TestPrepareConversationFallsBackToFreshSessionWhenMissing(t *testing.T) {
	store := session.NewStore(t.TempDir())
	createOptions := &session.CreateOptions{Task: "hello", Chat: true}

	conversation, err := PrepareConversation(store, "does-not-exist", createOptions, "system prompt")
	if err != nil {
		t.Fatalf("PrepareConversation returned error: %v", err)
	}
	if conversation.Session == nil {
		t.Fatal("expected a fresh session, got nil")
	}
	if id := conversation.Session.ID(); id == "" || id == "does-not-exist" {
		t.Fatalf("session id = %q, want a freshly generated id", id)
	}
	if len(conversation.Messages) != 1 || conversation.Messages[0]["role"] != "system" {
		t.Fatalf("messages = %#v, want a single system message", conversation.Messages)
	}

	if _, err := store.Open(conversation.Session.ID()); err != nil {
		t.Fatalf("fresh session was not persisted: %v", err)
	}
}

func TestPrepareConversationRestoresCheckpointSummaryAndTail(t *testing.T) {
	store := session.NewStore(t.TempDir())
	createOptions := &session.CreateOptions{Task: "hello", Chat: true}

	conversation, err := PrepareConversation(store, "", createOptions, "system prompt")
	if err != nil {
		t.Fatalf("PrepareConversation returned error: %v", err)
	}
	if err := conversation.AddUserMessage("old detail"); err != nil {
		t.Fatalf("AddUserMessage returned error: %v", err)
	}
	if err := conversation.Session.SaveCheckpointSummary("checkpoint summary", []map[string]any{{"role": "user", "content": "recent tail"}}, "system prompt"); err != nil {
		t.Fatalf("SaveCheckpointSummary returned error: %v", err)
	}

	restored, err := PrepareConversation(store, conversation.Session.ID(), nil, "system prompt")
	if err != nil {
		t.Fatalf("PrepareConversation restore returned error: %v", err)
	}
	if len(restored.Messages) != 3 {
		t.Fatalf("restored messages = %d, want 3", len(restored.Messages))
	}
	if restored.Messages[0]["role"] != "system" {
		t.Fatalf("first restored role = %#v, want system", restored.Messages[0]["role"])
	}
	summary, _ := restored.Messages[1]["content"].(string)
	if !strings.Contains(summary, "Summary of earlier conversation:\ncheckpoint summary") {
		t.Fatalf("restored summary = %q, want checkpoint summary message", summary)
	}
	if restored.Messages[2]["content"] != "recent tail" {
		t.Fatalf("restored tail content = %#v, want %q", restored.Messages[2]["content"], "recent tail")
	}
}

func TestPrepareConversationIgnoresCheckpointWhenSystemPromptChanges(t *testing.T) {
	store := session.NewStore(t.TempDir())
	createOptions := &session.CreateOptions{Task: "hello", Chat: true}

	conversation, err := PrepareConversation(store, "", createOptions, "old system prompt")
	if err != nil {
		t.Fatalf("PrepareConversation returned error: %v", err)
	}
	if err := conversation.AddUserMessage("old detail"); err != nil {
		t.Fatalf("AddUserMessage returned error: %v", err)
	}
	if err := conversation.Session.SaveCheckpointSummary("checkpoint summary", []map[string]any{{"role": "user", "content": "recent tail"}}, "old system prompt"); err != nil {
		t.Fatalf("SaveCheckpointSummary returned error: %v", err)
	}

	restored, err := PrepareConversation(store, conversation.Session.ID(), nil, "new system prompt")
	if err != nil {
		t.Fatalf("PrepareConversation restore returned error: %v", err)
	}
	if len(restored.Messages) != 2 {
		t.Fatalf("restored messages = %d, want 2 when checkpoint is ignored", len(restored.Messages))
	}
	if restored.Messages[0]["content"] != "new system prompt" {
		t.Fatalf("first restored content = %#v, want new system prompt", restored.Messages[0]["content"])
	}
	if restored.Messages[1]["content"] != "old detail" {
		t.Fatalf("second restored content = %#v, want original stored message", restored.Messages[1]["content"])
	}
}

func TestConversationEnsureSystemMessagePrependsWhenMissing(t *testing.T) {
	conversation := &Conversation{Messages: []map[string]any{{"role": "user", "content": "hello"}}}

	if err := conversation.EnsureSystemMessage("system prompt"); err != nil {
		t.Fatalf("EnsureSystemMessage returned error: %v", err)
	}
	if len(conversation.Messages) != 2 {
		t.Fatalf("messages length = %d, want 2", len(conversation.Messages))
	}
	if conversation.Messages[0]["role"] != "system" {
		t.Fatalf("first message role = %#v, want system", conversation.Messages[0]["role"])
	}
	if conversation.Messages[0]["content"] != "system prompt" {
		t.Fatalf("first message content = %#v, want system prompt", conversation.Messages[0]["content"])
	}
	if conversation.Messages[1]["role"] != "user" {
		t.Fatalf("second message role = %#v, want user", conversation.Messages[1]["role"])
	}
}

func TestConversationMarkFailedNoOpsWithoutSession(t *testing.T) {
	conversation := &Conversation{Session: nil, Messages: []map[string]any{}}

	if err := conversation.MarkFailed(errors.New("some error")); err != nil {
		t.Fatalf("MarkFailed returned error: %v", err)
	}
	if err := conversation.MarkCompleted(); err != nil {
		t.Fatalf("MarkCompleted returned error: %v", err)
	}
}
