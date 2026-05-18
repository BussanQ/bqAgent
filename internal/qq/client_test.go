package qq

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakeTokenSource struct {
	token      string
	configured bool
	err        error
}

func (source fakeTokenSource) Token(context.Context) (string, error) {
	return source.token, source.err
}

func (source fakeTokenSource) Configured() bool {
	return source.configured
}

func TestClientSendTextToC2C(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			t.Fatalf("method = %s", request.Method)
		}
		if request.URL.Path != "/v2/users/user-1/messages" {
			t.Fatalf("path = %s", request.URL.Path)
		}
		if request.Header.Get("Authorization") != "QQBot token-1" {
			t.Fatalf("Authorization = %q", request.Header.Get("Authorization"))
		}
		if request.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("Content-Type = %q", request.Header.Get("Content-Type"))
		}
		var body SendMessageRequest
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body.Content != "hello" || body.MsgID != "message-1" || body.MsgType != 0 || body.MsgSeq != 1 {
			t.Fatalf("body = %+v", body)
		}
		_, _ = writer.Write([]byte(`{"id":"sent-1","timestamp":"now"}`))
	}))
	defer server.Close()

	client := NewClient(fakeTokenSource{token: "token-1", configured: true}, server.URL, server.Client())
	response, err := client.SendText(context.Background(), SendTarget{Kind: UpdateKindC2C, UserOpenID: "user-1", MsgID: "message-1"}, " hello ")
	if err != nil {
		t.Fatalf("SendText() error = %v", err)
	}
	if response.ID != "sent-1" {
		t.Fatalf("ID = %q", response.ID)
	}
}

func TestClientSendTextToGroup(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v2/groups/group-1/messages" {
			t.Fatalf("path = %s", request.URL.Path)
		}
		_, _ = writer.Write([]byte(`{}`))
	}))
	defer server.Close()

	client := NewClient(fakeTokenSource{token: "token-1", configured: true}, server.URL, server.Client())
	if _, err := client.SendText(context.Background(), SendTarget{Kind: UpdateKindGroup, GroupOpenID: "group-1", MsgID: "message-1"}, "hello"); err != nil {
		t.Fatalf("SendText() error = %v", err)
	}
}

func TestClientSendTextValidatesTarget(t *testing.T) {
	client := NewClient(fakeTokenSource{token: "token-1", configured: true}, "", nil)
	if _, err := client.SendText(context.Background(), SendTarget{Kind: UpdateKindC2C}, "hello"); err == nil {
		t.Fatalf("SendText() error = nil, want error")
	}
}

func TestClientSendTextReturnsTokenError(t *testing.T) {
	client := NewClient(fakeTokenSource{configured: true, err: fmt.Errorf("token failed")}, "", nil)
	if _, err := client.SendText(context.Background(), SendTarget{Kind: UpdateKindC2C, UserOpenID: "user-1"}, "hello"); err == nil {
		t.Fatalf("SendText() error = nil, want error")
	}
}

func TestClientSendTextReturnsHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Error(writer, "bad", http.StatusBadGateway)
	}))
	defer server.Close()

	client := NewClient(fakeTokenSource{token: "token-1", configured: true}, server.URL, server.Client())
	if _, err := client.SendText(context.Background(), SendTarget{Kind: UpdateKindC2C, UserOpenID: "user-1"}, "hello"); err == nil {
		t.Fatalf("SendText() error = nil, want error")
	}
}

func TestClientRequiresConfiguredTokenSource(t *testing.T) {
	client := NewClient(fakeTokenSource{token: "token-1"}, "", nil)
	if _, err := client.SendText(context.Background(), SendTarget{Kind: UpdateKindC2C, UserOpenID: "user-1"}, "hello"); err == nil {
		t.Fatalf("SendText() error = nil, want error")
	}
}
