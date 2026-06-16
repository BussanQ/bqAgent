package weixin

import (
	"encoding/json"
	"errors"
	"strings"
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

func TestParseUpdateExtractsTypedImage(t *testing.T) {
	update, err := ParseUpdate(InboundMessage{
		FromUserID:   "user-1",
		MessageType:  inboundMessageType,
		ContextToken: "ctx-1",
		ItemList: []MessageItem{
			{Type: 1, TextItem: &TextItem{Text: "look"}},
			{Type: 2, ImageItem: &ImageItem{
				AESKey: "00112233445566778899aabbccddeeff",
				Media:  &CDNMedia{EncryptQueryParam: "qp-1", AESKey: "Zm9v"},
			}},
		},
	})
	if err != nil {
		t.Fatalf("ParseUpdate returned error: %v", err)
	}
	if update.Text != "look" {
		t.Fatalf("Text = %q, want %q", update.Text, "look")
	}
	if len(update.Images) != 1 {
		t.Fatalf("Images = %d, want 1", len(update.Images))
	}
	image := update.Images[0]
	if image.EncryptQueryParam != "qp-1" {
		t.Fatalf("EncryptQueryParam = %q, want qp-1", image.EncryptQueryParam)
	}
	if image.AESKeyHex != "00112233445566778899aabbccddeeff" {
		t.Fatalf("AESKeyHex = %q", image.AESKeyHex)
	}
	if image.AESKeyBase64 != "Zm9v" {
		t.Fatalf("AESKeyBase64 = %q, want Zm9v", image.AESKeyBase64)
	}
}

func TestParseUpdateImageOnly(t *testing.T) {
	update, err := ParseUpdate(InboundMessage{
		FromUserID:   "user-1",
		MessageType:  inboundMessageType,
		ContextToken: "ctx-1",
		ItemList: []MessageItem{
			{Type: 2, ImageItem: &ImageItem{Media: &CDNMedia{EncryptQueryParam: "qp-1"}}},
		},
	})
	if err != nil {
		t.Fatalf("ParseUpdate returned error: %v", err)
	}
	if update.Text != "" {
		t.Fatalf("Text = %q, want empty", update.Text)
	}
	if len(update.Images) != 1 {
		t.Fatalf("Images = %d, want 1", len(update.Images))
	}
}

func TestParseUpdateIgnoresImageWithoutQueryParam(t *testing.T) {
	_, err := ParseUpdate(InboundMessage{
		FromUserID:   "user-1",
		MessageType:  inboundMessageType,
		ContextToken: "ctx-1",
		ItemList: []MessageItem{
			{Type: 2, ImageItem: &ImageItem{Media: &CDNMedia{}}},
		},
	})
	if !errors.Is(err, ErrIgnoreUpdate) {
		t.Fatalf("error = %v, want %v", err, ErrIgnoreUpdate)
	}
}

func TestParseUpdateDecodesRealisticImagePayload(t *testing.T) {
	raw := []byte(`{
		"from_user_id": "user-1",
		"message_type": 1,
		"context_token": "ctx-1",
		"item_list": [
			{"type": 2, "image_item": {"aeskey": "00112233445566778899aabbccddeeff", "media": {"encrypt_query_param": "qp-xyz", "encrypt_type": 1}}}
		]
	}`)
	var message InboundMessage
	if err := json.Unmarshal(raw, &message); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	update, err := ParseUpdate(message)
	if err != nil {
		t.Fatalf("ParseUpdate returned error: %v", err)
	}
	if len(update.Images) != 1 || update.Images[0].EncryptQueryParam != "qp-xyz" {
		t.Fatalf("images = %+v, want one with encrypt_query_param qp-xyz", update.Images)
	}
	if update.Images[0].AESKeyHex != "00112233445566778899aabbccddeeff" {
		t.Fatalf("AESKeyHex = %q", update.Images[0].AESKeyHex)
	}
}

func TestUnhandledItemsJSONReportsUnknownItems(t *testing.T) {
	raw := []byte(`{
		"from_user_id": "user-1",
		"message_type": 1,
		"context_token": "ctx-1",
		"item_list": [
			{"type": 1, "text_item": {"text": "hi"}},
			{"type": 9, "sticker_item": {"id": "s1"}}
		]
	}`)
	var message InboundMessage
	if err := json.Unmarshal(raw, &message); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	payload, ok := UnhandledItemsJSON(message)
	if !ok {
		t.Fatal("expected unhandled items to be reported")
	}
	if !strings.Contains(payload, "sticker_item") {
		t.Fatalf("payload = %q, want it to contain sticker_item", payload)
	}
}
