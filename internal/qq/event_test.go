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

func TestParseGatewayDispatchParsesImageAttachment(t *testing.T) {
	update, err := ParseGatewayDispatch(strings.NewReader(`{
		"id":"event-1",
		"op":0,
		"t":"C2C_MESSAGE_CREATE",
		"d":{
			"author":{"user_openid":"user-1"},
			"content":"look at this",
			"id":"message-1",
			"attachments":[
				{"content_type":"image/jpeg","filename":"a.jpg","url":"gchat.qpic.cn/x/a.jpg","width":100,"height":80},
				{"content_type":"voice/silk","url":"gchat.qpic.cn/v/b.silk"}
			]
		}
	}`))
	if err != nil {
		t.Fatalf("ParseGatewayDispatch() error = %v", err)
	}
	if update.Text != "look at this" {
		t.Fatalf("Text = %q", update.Text)
	}
	if len(update.Images) != 1 {
		t.Fatalf("Images = %d, want 1 (non-image attachment ignored)", len(update.Images))
	}
	if update.Images[0].URL != "https://gchat.qpic.cn/x/a.jpg" {
		t.Fatalf("image URL = %q, want scheme prepended", update.Images[0].URL)
	}
	if update.Images[0].ContentType != "image/jpeg" {
		t.Fatalf("image ContentType = %q", update.Images[0].ContentType)
	}
}

func TestParseGatewayDispatchAcceptsImageOnlyMessage(t *testing.T) {
	update, err := ParseGatewayDispatch(strings.NewReader(`{
		"id":"event-1",
		"op":0,
		"t":"C2C_MESSAGE_CREATE",
		"d":{
			"author":{"user_openid":"user-1"},
			"content":"   ",
			"id":"message-1",
			"attachments":[{"content_type":"image/png","url":"https://gchat.qpic.cn/x/c.png"}]
		}
	}`))
	if err != nil {
		t.Fatalf("ParseGatewayDispatch() error = %v", err)
	}
	if update.Text != "" {
		t.Fatalf("Text = %q, want empty", update.Text)
	}
	if len(update.Images) != 1 || update.Images[0].URL != "https://gchat.qpic.cn/x/c.png" {
		t.Fatalf("images = %+v", update.Images)
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
