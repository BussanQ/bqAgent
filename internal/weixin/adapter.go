package weixin

import (
	"errors"
	"fmt"
	"strings"
)

var ErrIgnoreUpdate = errors.New("ignore update")

type Update struct {
	UserID       string
	ClientID     string
	ContextToken string
	Text         string
}

func ParseUpdate(message InboundMessage) (Update, error) {
	if message.MessageType != inboundMessageType {
		return Update{}, ErrIgnoreUpdate
	}
	userID := strings.TrimSpace(message.FromUserID)
	if userID == "" {
		return Update{}, fmt.Errorf("from_user_id is required")
	}
	contextToken := strings.TrimSpace(message.ContextToken)
	if contextToken == "" {
		return Update{}, fmt.Errorf("context_token is required")
	}
	text := ""
	for _, item := range message.ItemList {
		if item.TextItem != nil && strings.TrimSpace(item.TextItem.Text) != "" {
			text = strings.TrimSpace(item.TextItem.Text)
			break
		}
		if item.VoiceItem != nil && strings.TrimSpace(item.VoiceItem.Text) != "" {
			text = strings.TrimSpace(item.VoiceItem.Text)
			break
		}
	}
	if text == "" {
		return Update{}, ErrIgnoreUpdate
	}
	return Update{
		UserID:       userID,
		ClientID:     strings.TrimSpace(message.ClientID),
		ContextToken: contextToken,
		Text:         text,
	}, nil
}

func NewTextMessage(toUserID, clientID, contextToken, text string) OutboundMessage {
	return OutboundMessage{
		FromUserID:   "",
		ToUserID:     strings.TrimSpace(toUserID),
		ClientID:     strings.TrimSpace(clientID),
		MessageType:  outboundMessageType,
		MessageState: outboundMessageStateDone,
		ContextToken: strings.TrimSpace(contextToken),
		ItemList: []MessageItem{{
			Type:     1,
			TextItem: &TextItem{Text: strings.TrimSpace(text)},
		}},
	}
}
