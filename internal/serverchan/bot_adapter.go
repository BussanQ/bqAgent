package serverchan

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

var ErrIgnoreBotUpdate = errors.New("ignore bot update")

type BotWebhookPayload struct {
	OK       bool              `json:"ok"`
	UpdateID int64             `json:"update_id"`
	Message  BotWebhookMessage `json:"message"`
}

type BotWebhookMessage struct {
	MessageID int64           `json:"message_id"`
	Text      string          `json:"text"`
	ChatID    int64           `json:"chat_id"`
	Chat      *BotWebhookChat `json:"chat,omitempty"`
}

type BotWebhookChat struct {
	ID int64 `json:"id"`
}

type BotUpdate struct {
	UpdateID  int64
	MessageID int64
	ChatID    int64
	Text      string
}

func ParseBotWebhookPayload(reader io.Reader) (BotUpdate, error) {
	var payload BotWebhookPayload
	if err := json.NewDecoder(reader).Decode(&payload); err != nil {
		return BotUpdate{}, err
	}
	if payload.UpdateID <= 0 {
		return BotUpdate{}, fmt.Errorf("update_id is required")
	}
	chatID := payload.Message.ChatID
	if chatID <= 0 && payload.Message.Chat != nil {
		chatID = payload.Message.Chat.ID
	}
	if chatID <= 0 {
		return BotUpdate{}, fmt.Errorf("chat_id is required")
	}
	text := strings.TrimSpace(payload.Message.Text)
	if text == "" {
		return BotUpdate{}, ErrIgnoreBotUpdate
	}
	return BotUpdate{
		UpdateID:  payload.UpdateID,
		MessageID: payload.Message.MessageID,
		ChatID:    chatID,
		Text:      text,
	}, nil
}

func VerifyWebhookSecret(header http.Header, expected string) bool {
	expected = strings.TrimSpace(expected)
	if expected == "" {
		return true
	}
	provided := strings.TrimSpace(firstNonEmpty(header.Get("X-Sc3Bot-Webhook-Secret"), header.Get("x-sc3bot-webhook-secret")))
	if provided == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
}
