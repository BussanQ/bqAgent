package weixin

import (
	"encoding/json"
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
	Images       []InboundImage
}

// InboundImage is an image reference parsed out of an inbound message, before the
// encrypted bytes are downloaded from the CDN and decrypted (see Client.FetchImage).
type InboundImage struct {
	EncryptQueryParam string
	AESKeyHex         string // image_item.aeskey (hex), preferred
	AESKeyBase64      string // image_item.media.aes_key (base64)
	FileName          string
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
	var images []InboundImage
	for _, item := range message.ItemList {
		if text == "" {
			if item.TextItem != nil && strings.TrimSpace(item.TextItem.Text) != "" {
				text = strings.TrimSpace(item.TextItem.Text)
				continue
			}
			if item.VoiceItem != nil && strings.TrimSpace(item.VoiceItem.Text) != "" {
				text = strings.TrimSpace(item.VoiceItem.Text)
				continue
			}
		}
		if image, ok := extractInboundImage(item); ok {
			images = append(images, image)
		}
	}

	if text == "" && len(images) == 0 {
		return Update{}, ErrIgnoreUpdate
	}
	return Update{
		UserID:       userID,
		ClientID:     strings.TrimSpace(message.ClientID),
		ContextToken: contextToken,
		Text:         text,
		Images:       images,
	}, nil
}

// extractInboundImage pulls an image reference (item type 2) from a message item.
// The full image lives at media.encrypt_query_param on the CDN; the decryption
// key is image_item.aeskey (hex) or media.aes_key (base64).
func extractInboundImage(item MessageItem) (InboundImage, bool) {
	if item.ImageItem == nil || item.ImageItem.Media == nil {
		return InboundImage{}, false
	}
	media := item.ImageItem.Media
	queryParam := strings.TrimSpace(media.EncryptQueryParam)
	if queryParam == "" {
		return InboundImage{}, false
	}
	return InboundImage{
		EncryptQueryParam: queryParam,
		AESKeyHex:         strings.TrimSpace(item.ImageItem.AESKey),
		AESKeyBase64:      strings.TrimSpace(media.AESKey),
	}, true
}

// UnhandledItemsJSON returns the JSON of inbound items that are neither text nor
// voice nor a recognized image, so an operator can capture an unknown wire format
// for images. The bool is false when there is nothing noteworthy to report.
func UnhandledItemsJSON(message InboundMessage) (string, bool) {
	var unhandled []MessageItem
	for _, item := range message.ItemList {
		if item.TextItem != nil && strings.TrimSpace(item.TextItem.Text) != "" {
			continue
		}
		if item.VoiceItem != nil && strings.TrimSpace(item.VoiceItem.Text) != "" {
			continue
		}
		if _, ok := extractInboundImage(item); ok {
			continue
		}
		unhandled = append(unhandled, item)
	}
	if len(unhandled) == 0 {
		return "", false
	}
	raw := make([]map[string]json.RawMessage, 0, len(unhandled))
	for _, item := range unhandled {
		if item.Raw != nil {
			raw = append(raw, item.Raw)
		}
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return "", false
	}
	return string(encoded), true
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
