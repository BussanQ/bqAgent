package qq

import (
	"errors"
	"strings"
	"testing"
)

func TestParseGatewayDispatchParsesC2CMessage(t *testing.T) {
	update, err := ParseGatewayDispatch(strings.NewReader(`{
		"id":"event-1",
		"op":0,
		"t":"C2C_MESSAGE_CREATE",
		"d":{
			"author":{"user_openid":"user-1"},
			"content":"  hello  ",
			"id":"message-1",
			"timestamp":"2026-05-18T12:00:00+08:00"
		}
	}`))
	if err != nil {
		t.Fatalf("ParseGatewayDispatch() error = %v", err)
	}
	if update.Kind != UpdateKindC2C {
		t.Fatalf("Kind = %q, want %q", update.Kind, UpdateKindC2C)
	}
	if update.PeerKey != "qq:c2c:user-1" {
		t.Fatalf("PeerKey = %q", update.PeerKey)
	}
	if update.DedupeKey != "event-1" {
		t.Fatalf("DedupeKey = %q", update.DedupeKey)
	}
	if update.MessageID != "message-1" {
		t.Fatalf("MessageID = %q", update.MessageID)
	}
	if update.Text != "hello" {
		t.Fatalf("Text = %q", update.Text)
	}
	if update.UserOpenID != "user-1" {
		t.Fatalf("UserOpenID = %q", update.UserOpenID)
	}
}

func TestParseGatewayDispatchParsesGroupMessage(t *testing.T) {
	update, err := ParseGatewayDispatch(strings.NewReader(`{
		"op":0,
		"t":"GROUP_AT_MESSAGE_CREATE",
		"d":{
			"author":{"member_openid":"member-1"},
			"group_openid":"group-1",
			"content":" group hello ",
			"id":"message-2"
		}
	}`))
	if err != nil {
		t.Fatalf("ParseGatewayDispatch() error = %v", err)
	}
	if update.Kind != UpdateKindGroup {
		t.Fatalf("Kind = %q, want %q", update.Kind, UpdateKindGroup)
	}
	if update.PeerKey != "qq:group:group-1:member-1" {
		t.Fatalf("PeerKey = %q", update.PeerKey)
	}
	if update.DedupeKey != "message-2" {
		t.Fatalf("DedupeKey = %q", update.DedupeKey)
	}
	if update.GroupOpenID != "group-1" {
		t.Fatalf("GroupOpenID = %q", update.GroupOpenID)
	}
	if update.MemberOpenID != "member-1" {
		t.Fatalf("MemberOpenID = %q", update.MemberOpenID)
	}
	if update.Text != "group hello" {
		t.Fatalf("Text = %q", update.Text)
	}
}

func TestParseGatewayDispatchIgnoresUnsupportedEvent(t *testing.T) {
	_, err := ParseGatewayDispatch(strings.NewReader(`{"op":0,"t":"READY","d":{}}`))
	if !errors.Is(err, ErrIgnoreUpdate) {
		t.Fatalf("error = %v, want ErrIgnoreUpdate", err)
	}
}

func TestParseGatewayDispatchIgnoresEmptyContent(t *testing.T) {
	_, err := ParseGatewayDispatch(strings.NewReader(`{
		"id":"event-1",
		"op":0,
		"t":"C2C_MESSAGE_CREATE",
		"d":{"author":{"user_openid":"user-1"},"content":"   ","id":"message-1"}
	}`))
	if !errors.Is(err, ErrIgnoreUpdate) {
		t.Fatalf("error = %v, want ErrIgnoreUpdate", err)
	}
}

func TestParseGatewayDispatchRequiresIdentifiers(t *testing.T) {
	_, err := ParseGatewayDispatch(strings.NewReader(`{
		"id":"event-1",
		"op":0,
		"t":"GROUP_AT_MESSAGE_CREATE",
		"d":{"author":{"member_openid":"member-1"},"content":"hello","id":"message-1"}
	}`))
	if err == nil || errors.Is(err, ErrIgnoreUpdate) {
		t.Fatalf("error = %v, want validation error", err)
	}
}
