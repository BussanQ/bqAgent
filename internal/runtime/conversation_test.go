package runtime

import (
	"errors"
	"strings"
	"testing"

	"bqagent/internal/session"
)

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
