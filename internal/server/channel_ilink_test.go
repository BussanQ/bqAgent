package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"bqagent/internal/session"
	"bqagent/internal/weixin"
)

func TestIlinkChannelLoginEndpoints(t *testing.T) {
	var statusCalls atomic.Int32
	var ilinkServer *httptest.Server
	ilinkServer = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/ilink/bot/get_bot_qrcode":
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"ret":0,"qrcode":"qr-1","qrcode_img_content":"img-1"}`))
		case "/ilink/bot/get_qrcode_status":
			statusCalls.Add(1)
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(fmt.Sprintf(`{"ret":0,"status":"confirmed","bot_token":"token-1","baseurl":%q,"account_id":"account-1","user_id":"user-1"}`, ilinkServer.URL)))
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ilinkServer.Close()

	root := t.TempDir()
	service := newTestService(root, "http://example.invalid")
	channel := NewIlinkChannel(
		service,
		weixin.NewClientWithBaseURL(ilinkServer.URL, "1.0.2", ilinkServer.Client()),
		weixin.NewTokenStore(root),
		weixin.NewPollerStateStore(root),
		weixin.NewChatStateStore(root),
	)
	handler := NewHandler(HandlerOptions{Service: service, Channels: []Channel{channel}})
	apiServer := httptest.NewServer(handler)
	defer apiServer.Close()

	response, err := http.Post(apiServer.URL+"/api/v1/weixin/ilink/login", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("failed to post login request: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
	var loginStatus IlinkStatus
	if err := json.NewDecoder(response.Body).Decode(&loginStatus); err != nil {
		t.Fatalf("failed to decode login response: %v", err)
	}
	if loginStatus.QRCode != "qr-1" {
		t.Fatalf("QRCode = %q, want %q", loginStatus.QRCode, "qr-1")
	}
	if loginStatus.QRCodeImgContent != "img-1" {
		t.Fatalf("QRCodeImgContent = %q, want %q", loginStatus.QRCodeImgContent, "img-1")
	}

	tokenStore := weixin.NewTokenStore(root)
	waitForCondition(t, 2*time.Second, func() bool {
		state, err := tokenStore.Load()
		return err == nil && state.BotToken == "token-1"
	}, "ilink token to persist")
	if statusCalls.Load() == 0 {
		t.Fatal("expected get_qrcode_status to be called at least once")
	}

	statusResponse, err := http.Get(apiServer.URL + "/api/v1/weixin/ilink/status")
	if err != nil {
		t.Fatalf("failed to get status: %v", err)
	}
	defer statusResponse.Body.Close()
	if statusResponse.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", statusResponse.StatusCode)
	}
	var status IlinkStatus
	if err := json.NewDecoder(statusResponse.Body).Decode(&status); err != nil {
		t.Fatalf("failed to decode status response: %v", err)
	}
	if !status.LoggedIn {
		t.Fatal("LoggedIn = false, want true")
	}
	if status.AccountID != "account-1" {
		t.Fatalf("AccountID = %q, want %q", status.AccountID, "account-1")
	}
}

func TestIlinkChannelProcessesConversation(t *testing.T) {
	var llmCount atomic.Int32
	llmServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		llmCount.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"assistant reply"}}]}`))
	}))
	defer llmServer.Close()

	var updatesCount atomic.Int32
	sentMessages := make(chan weixin.SendMessageRequest, 2)
	ilinkServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/ilink/bot/getupdates":
			count := updatesCount.Add(1)
			writer.Header().Set("Content-Type", "application/json")
			if count == 1 {
				_, _ = writer.Write([]byte(`{"ret":0,"get_updates_buf":"cursor-1","msgs":[{"from_user_id":"user-1","client_id":"client-1","message_type":1,"context_token":"ctx-1","item_list":[{"type":1,"text_item":{"text":"hello"}}]}]}`))
				return
			}
			time.Sleep(10 * time.Millisecond)
			_, _ = writer.Write([]byte(`{"ret":0,"get_updates_buf":"cursor-1","msgs":[]}`))
		case "/ilink/bot/sendmessage":
			var payload weixin.SendMessageRequest
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Fatalf("failed to decode sendmessage payload: %v", err)
			}
			sentMessages <- payload
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"ret":0}`))
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ilinkServer.Close()

	root := t.TempDir()
	if err := weixin.NewTokenStore(root).Save(weixin.TokenState{BotToken: "token-1", BaseURL: ilinkServer.URL, AccountID: "account-1", UserID: "user-1"}); err != nil {
		t.Fatalf("failed to seed token store: %v", err)
	}
	service := newTestService(root, llmServer.URL)
	channel := NewIlinkChannel(
		service,
		weixin.NewClientWithBaseURL(ilinkServer.URL, "1.0.2", ilinkServer.Client()),
		weixin.NewTokenStore(root),
		weixin.NewPollerStateStore(root),
		weixin.NewChatStateStore(root),
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	channel.Start(ctx)

	sent := waitForIlinkSend(t, sentMessages)
	if sent.Msg.ToUserID != "user-1" {
		t.Fatalf("ToUserID = %q, want %q", sent.Msg.ToUserID, "user-1")
	}
	if sent.Msg.ClientID != "client-1" {
		t.Fatalf("ClientID = %q, want %q", sent.Msg.ClientID, "client-1")
	}
	if sent.Msg.ContextToken != "ctx-1" {
		t.Fatalf("ContextToken = %q, want %q", sent.Msg.ContextToken, "ctx-1")
	}
	if got := sent.Msg.ItemList[0].TextItem.Text; got != "assistant reply" {
		t.Fatalf("reply text = %q, want %q", got, "assistant reply")
	}

	chatStore := weixin.NewChatStateStore(root)
	waitForCondition(t, 2*time.Second, func() bool {
		state, err := chatStore.Load("user-1")
		return err == nil && state.SessionID != "" && state.LastCompletedContextToken == "ctx-1" && state.PendingReply == ""
	}, "ilink chat state to persist")

	pollerStore := weixin.NewPollerStateStore(root)
	waitForCondition(t, 2*time.Second, func() bool {
		state, err := pollerStore.Load()
		return err == nil && state.GetUpdatesBuf == "cursor-1"
	}, "ilink poller cursor to persist")

	state, err := chatStore.Load("user-1")
	if err != nil {
		t.Fatalf("failed to load ilink chat state: %v", err)
	}
	store := session.NewStore(root)
	savedSession, err := store.Open(state.SessionID)
	if err != nil {
		t.Fatalf("failed to open saved session: %v", err)
	}
	messages, err := savedSession.LoadMessages()
	if err != nil {
		t.Fatalf("failed to load session messages: %v", err)
	}
	if len(messages) != 3 {
		t.Fatalf("messages length = %d, want 3", len(messages))
	}
	if llmCount.Load() != 1 {
		t.Fatalf("LLM request count = %d, want 1", llmCount.Load())
	}
}

func waitForIlinkSend(t *testing.T, requests <-chan weixin.SendMessageRequest) weixin.SendMessageRequest {
	t.Helper()
	select {
	case request := <-requests:
		return request
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for ilink send")
		return weixin.SendMessageRequest{}
	}
}
