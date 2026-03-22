package serverchan

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestParseBotWebhookPayloadUsesChatID(t *testing.T) {
	update, err := ParseBotWebhookPayload(strings.NewReader(`{"ok":true,"update_id":1,"message":{"message_id":2,"text":"hello","chat_id":42}}`))
	if err != nil {
		t.Fatalf("ParseBotWebhookPayload returned error: %v", err)
	}
	if update.ChatID != 42 {
		t.Fatalf("ChatID = %d, want 42", update.ChatID)
	}
	if update.Text != "hello" {
		t.Fatalf("Text = %q, want %q", update.Text, "hello")
	}
}

func TestParseBotWebhookPayloadUsesNestedChatID(t *testing.T) {
	update, err := ParseBotWebhookPayload(strings.NewReader(`{"ok":true,"update_id":1,"message":{"message_id":2,"text":"hello","chat":{"id":43}}}`))
	if err != nil {
		t.Fatalf("ParseBotWebhookPayload returned error: %v", err)
	}
	if update.ChatID != 43 {
		t.Fatalf("ChatID = %d, want 43", update.ChatID)
	}
}

func TestParseBotWebhookPayloadIgnoresEmptyText(t *testing.T) {
	_, err := ParseBotWebhookPayload(strings.NewReader(`{"ok":true,"update_id":1,"message":{"message_id":2,"chat_id":42}}`))
	if !errors.Is(err, ErrIgnoreBotUpdate) {
		t.Fatalf("error = %v, want %v", err, ErrIgnoreBotUpdate)
	}
}

func TestParseBotWebhookPayloadRequiresUpdateID(t *testing.T) {
	_, err := ParseBotWebhookPayload(strings.NewReader(`{"ok":true,"message":{"message_id":2,"text":"hello","chat_id":42}}`))
	if err == nil || !strings.Contains(err.Error(), "update_id is required") {
		t.Fatalf("error = %v, want update_id validation", err)
	}
}

func TestVerifyWebhookSecret(t *testing.T) {
	header := http.Header{}
	header.Set("X-Sc3Bot-Webhook-Secret", "demo-secret")
	if !VerifyWebhookSecret(header, "demo-secret") {
		t.Fatal("VerifyWebhookSecret returned false, want true")
	}
	if VerifyWebhookSecret(header, "wrong-secret") {
		t.Fatal("VerifyWebhookSecret returned true, want false")
	}
	if !VerifyWebhookSecret(http.Header{}, "") {
		t.Fatal("VerifyWebhookSecret returned false for empty expected secret, want true")
	}
}
