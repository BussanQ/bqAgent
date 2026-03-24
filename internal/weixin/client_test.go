package weixin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientGetBotQRCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			t.Fatalf("method = %s, want GET", request.Method)
		}
		if request.URL.Path != "/ilink/bot/get_bot_qrcode" {
			t.Fatalf("path = %q, want %q", request.URL.Path, "/ilink/bot/get_bot_qrcode")
		}
		if request.URL.Query().Get("bot_type") != loginBotType {
			t.Fatalf("bot_type = %q, want %q", request.URL.Query().Get("bot_type"), loginBotType)
		}
		if request.Header.Get("AuthorizationType") != "ilink_bot_token" {
			t.Fatalf("AuthorizationType = %q, want ilink_bot_token", request.Header.Get("AuthorizationType"))
		}
		if request.Header.Get("X-WECHAT-UIN") == "" {
			t.Fatal("X-WECHAT-UIN was empty")
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"ret":0,"qrcode":"qr-1","qrcode_img_content":"img-1"}`))
	}))
	defer server.Close()

	client := NewClientWithBaseURL(server.URL, defaultChannelVersion, server.Client())
	response, err := client.GetBotQRCode(context.Background())
	if err != nil {
		t.Fatalf("GetBotQRCode returned error: %v", err)
	}
	if response.QRCode != "qr-1" {
		t.Fatalf("QRCode = %q, want %q", response.QRCode, "qr-1")
	}
	if response.QRCodeImgBase64 != "img-1" {
		t.Fatalf("QRCodeImgBase64 = %q, want %q", response.QRCodeImgBase64, "img-1")
	}
}

func TestClientGetUpdates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", request.Method)
		}
		if request.URL.Path != "/ilink/bot/getupdates" {
			t.Fatalf("path = %q, want %q", request.URL.Path, "/ilink/bot/getupdates")
		}
		if request.Header.Get("Authorization") != "Bearer token-1" {
			t.Fatalf("Authorization = %q, want %q", request.Header.Get("Authorization"), "Bearer token-1")
		}
		var payload GetUpdatesRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}
		if payload.GetUpdatesBuf != "cursor-1" {
			t.Fatalf("GetUpdatesBuf = %q, want %q", payload.GetUpdatesBuf, "cursor-1")
		}
		if payload.BaseInfo.ChannelVersion != "1.2.3" {
			t.Fatalf("ChannelVersion = %q, want %q", payload.BaseInfo.ChannelVersion, "1.2.3")
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"ret":0,"get_updates_buf":"cursor-2"}`))
	}))
	defer server.Close()

	client := NewClientWithBaseURL(server.URL, "1.2.3", server.Client())
	response, err := client.GetUpdates(context.Background(), server.URL, "token-1", "cursor-1")
	if err != nil {
		t.Fatalf("GetUpdates returned error: %v", err)
	}
	if response.GetUpdatesBuf != "cursor-2" {
		t.Fatalf("GetUpdatesBuf = %q, want %q", response.GetUpdatesBuf, "cursor-2")
	}
}

func TestClientSendTextMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", request.Method)
		}
		if request.URL.Path != "/ilink/bot/sendmessage" {
			t.Fatalf("path = %q, want %q", request.URL.Path, "/ilink/bot/sendmessage")
		}
		if request.Header.Get("Authorization") != "Bearer token-1" {
			t.Fatalf("Authorization = %q, want %q", request.Header.Get("Authorization"), "Bearer token-1")
		}
		var payload SendMessageRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}
		if payload.Msg.ToUserID != "user-1" {
			t.Fatalf("ToUserID = %q, want %q", payload.Msg.ToUserID, "user-1")
		}
		if payload.Msg.ClientID != "client-1" {
			t.Fatalf("ClientID = %q, want %q", payload.Msg.ClientID, "client-1")
		}
		if payload.Msg.ContextToken != "ctx-1" {
			t.Fatalf("ContextToken = %q, want %q", payload.Msg.ContextToken, "ctx-1")
		}
		if got := payload.Msg.ItemList[0].TextItem.Text; got != "assistant reply" {
			t.Fatalf("text = %q, want %q", got, "assistant reply")
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"ret":0}`))
	}))
	defer server.Close()

	client := NewClientWithBaseURL(server.URL, defaultChannelVersion, server.Client())
	if err := client.SendTextMessage(context.Background(), server.URL, "token-1", "user-1", "client-1", "ctx-1", "assistant reply"); err != nil {
		t.Fatalf("SendTextMessage returned error: %v", err)
	}
}
