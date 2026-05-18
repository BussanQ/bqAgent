package qq

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	eventC2CMessageCreate     = "C2C_MESSAGE_CREATE"
	eventGroupAtMessageCreate = "GROUP_AT_MESSAGE_CREATE"
)

var ErrIgnoreUpdate = errors.New("ignore qq update")

type UpdateKind string

const (
	UpdateKindC2C   UpdateKind = "c2c"
	UpdateKindGroup UpdateKind = "group"
)

type GatewayPayload struct {
	ID string          `json:"id,omitempty"`
	Op int             `json:"op"`
	D  json.RawMessage `json:"d"`
	S  *int64          `json:"s,omitempty"`
	T  string          `json:"t,omitempty"`
}

type Update struct {
	EventID      string
	EventType    string
	MessageID    string
	Kind         UpdateKind
	PeerKey      string
	DedupeKey    string
	Text         string
	UserOpenID   string
	MemberOpenID string
	GroupOpenID  string
	Timestamp    string
}

type messageEvent struct {
	Author struct {
		UserOpenID   string `json:"user_openid,omitempty"`
		MemberOpenID string `json:"member_openid,omitempty"`
	} `json:"author"`
	Content     string `json:"content,omitempty"`
	ID          string `json:"id,omitempty"`
	GroupOpenID string `json:"group_openid,omitempty"`
	Timestamp   string `json:"timestamp,omitempty"`
}

func ParseGatewayDispatchPayload(payload GatewayPayload) (Update, error) {
	switch strings.TrimSpace(payload.T) {
	case eventC2CMessageCreate:
		return parseC2CUpdate(payload)
	case eventGroupAtMessageCreate:
		return parseGroupUpdate(payload)
	default:
		return Update{}, ErrIgnoreUpdate
	}
}

func ParseGatewayDispatchBytes(body []byte) (Update, error) {
	var payload GatewayPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return Update{}, err
	}
	return ParseGatewayDispatchPayload(payload)
}

func ParseGatewayDispatch(reader io.Reader) (Update, error) {
	body, err := io.ReadAll(reader)
	if err != nil {
		return Update{}, err
	}
	return ParseGatewayDispatchBytes(body)
}

func parseC2CUpdate(payload GatewayPayload) (Update, error) {
	message, err := parseMessageEvent(payload.D)
	if err != nil {
		return Update{}, err
	}
	openid := strings.TrimSpace(message.Author.UserOpenID)
	if openid == "" {
		return Update{}, fmt.Errorf("user_openid is required")
	}
	if strings.TrimSpace(message.ID) == "" {
		return Update{}, fmt.Errorf("message id is required")
	}
	text := strings.TrimSpace(message.Content)
	if text == "" {
		return Update{}, ErrIgnoreUpdate
	}
	return Update{
		EventID:    strings.TrimSpace(payload.ID),
		EventType:  eventC2CMessageCreate,
		MessageID:  strings.TrimSpace(message.ID),
		Kind:       UpdateKindC2C,
		PeerKey:    "qq:c2c:" + openid,
		DedupeKey:  firstNonEmpty(strings.TrimSpace(payload.ID), strings.TrimSpace(message.ID)),
		Text:       text,
		UserOpenID: openid,
		Timestamp:  strings.TrimSpace(message.Timestamp),
	}, nil
}

func parseGroupUpdate(payload GatewayPayload) (Update, error) {
	message, err := parseMessageEvent(payload.D)
	if err != nil {
		return Update{}, err
	}
	groupOpenID := strings.TrimSpace(message.GroupOpenID)
	if groupOpenID == "" {
		return Update{}, fmt.Errorf("group_openid is required")
	}
	memberOpenID := strings.TrimSpace(message.Author.MemberOpenID)
	if memberOpenID == "" {
		return Update{}, fmt.Errorf("member_openid is required")
	}
	if strings.TrimSpace(message.ID) == "" {
		return Update{}, fmt.Errorf("message id is required")
	}
	text := strings.TrimSpace(message.Content)
	if text == "" {
		return Update{}, ErrIgnoreUpdate
	}
	return Update{
		EventID:      strings.TrimSpace(payload.ID),
		EventType:    eventGroupAtMessageCreate,
		MessageID:    strings.TrimSpace(message.ID),
		Kind:         UpdateKindGroup,
		PeerKey:      "qq:group:" + groupOpenID + ":" + memberOpenID,
		DedupeKey:    firstNonEmpty(strings.TrimSpace(payload.ID), strings.TrimSpace(message.ID)),
		Text:         text,
		MemberOpenID: memberOpenID,
		GroupOpenID:  groupOpenID,
		Timestamp:    strings.TrimSpace(message.Timestamp),
	}, nil
}

func parseMessageEvent(raw json.RawMessage) (messageEvent, error) {
	if len(raw) == 0 {
		return messageEvent{}, fmt.Errorf("event data is required")
	}
	var message messageEvent
	if err := json.Unmarshal(raw, &message); err != nil {
		return messageEvent{}, err
	}
	return message, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
