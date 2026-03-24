package weixin

import (
	"errors"
	"testing"
)

func TestParseUpdateUsesTextItem(t *testing.T) {
	update, err := ParseUpdate(InboundMessage{
		FromUserID:   "user-1",
		ClientID:     "client-1",
		MessageType:  inboundMessageType,
		ContextToken: "ctx-1",
		ItemList: []MessageItem{{
			Type:     1,
			TextItem: &TextItem{Text: " hello "},
		}},
	})
	if err != nil {
		t.Fatalf("ParseUpdate returned error: %v", err)
	}
	if update.UserID != "user-1" {
		t.Fatalf("UserID = %q, want %q", update.UserID, "user-1")
	}
	if update.ClientID != "client-1" {
		t.Fatalf("ClientID = %q, want %q", update.ClientID, "client-1")
	}
	if update.ContextToken != "ctx-1" {
		t.Fatalf("ContextToken = %q, want %q", update.ContextToken, "ctx-1")
	}
	if update.Text != "hello" {
		t.Fatalf("Text = %q, want %q", update.Text, "hello")
	}
}

func TestParseUpdateIgnoresUnsupportedMessage(t *testing.T) {
	_, err := ParseUpdate(InboundMessage{FromUserID: "user-1", MessageType: outboundMessageType, ContextToken: "ctx-1"})
	if !errors.Is(err, ErrIgnoreUpdate) {
		t.Fatalf("error = %v, want %v", err, ErrIgnoreUpdate)
	}
}
