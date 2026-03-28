package runtime

import (
	"errors"
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
