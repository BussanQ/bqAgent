package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	qqclient "bqagent/internal/qq"
	"bqagent/internal/session"
)

type qqSendCapture struct {
	Path    string
	Auth    string
	Payload qqclient.SendMessageRequest
}

type fakeQQGateway struct {
	configured bool
	updates    []qqclient.Update
	calls      atomic.Int32
}

func (gateway *fakeQQGateway) Configured() bool {
	return gateway != nil && gateway.configured
}

func (gateway *fakeQQGateway) Connect(ctx context.Context, state qqclient.GatewaySessionState, handler func(context.Context, qqclient.Update) error) (qqclient.GatewaySessionState, error) {
	gateway.calls.Add(1)
	state.SessionID = "gateway-session-1"
	state.Seq = 1
	for _, update := range gateway.updates {
		if err := handler(ctx, update); err != nil {
			return state, err
		}
		state.Seq++
	}
	return state, context.Canceled
}

func TestQQChannelStartProcessesC2CConversation(t *testing.T) {
	var llmCount atomic.Int32
	llmServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		count := llmCount.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(fmt.Sprintf(`{"choices":[{"message":{"role":"assistant","content":"reply-%d"}}]}`, count)))
	}))
	defer llmServer.Close()

	sentMessages := make(chan qqSendCapture, 2)
	qqServer := newTestQQAPIServer(t, sentMessages, nil)
	defer qqServer.Close()

	root := t.TempDir()
	service := newTestService(root, llmServer.URL)
	channel := newTestQQChannel(root, service, qqServer.URL, &fakeQQGateway{configured: true, updates: []qqclient.Update{c2cUpdate("event-1", "message-1", "hello")}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	channel.Start(ctx)

	send := waitForQQSend(t, sentMessages)
	if send.Path != "/v2/users/user-1/messages" {
		t.Fatalf("send path = %q", send.Path)
	}
	if send.Auth != "QQBot token-1" {
		t.Fatalf("auth = %q", send.Auth)
	}
	if send.Payload.Content != "reply-1" || send.Payload.MsgID != "message-1" || send.Payload.MsgSeq != 1 {
		t.Fatalf("payload = %+v", send.Payload)
	}

	stateStore := qqclient.NewStateStore(root)
	waitForCondition(t, 2*time.Second, func() bool {
		state, err := stateStore.Load("qq:c2c:user-1")
		return err == nil && state.SessionID != "" && state.LastCompletedKey == "event-1" && state.PendingReply == ""
	}, "qq state to persist completed event")
	gatewayStore := qqclient.NewGatewayStateStore(root)
	waitForCondition(t, 2*time.Second, func() bool {
		state, err := gatewayStore.Load()
		return err == nil && state.SessionID == "gateway-session-1" && state.Seq >= 2
	}, "qq gateway state to persist")

	state, err := stateStore.Load("qq:c2c:user-1")
	if err != nil {
		t.Fatalf("failed to load qq state: %v", err)
	}
	store := session.NewStore(root)
	savedSession, err := store.Open(state.SessionID)
	if err != nil {
		t.Fatalf("failed to open saved session: %v", err)
	}
	messages, err := savedSession.LoadMessages()
	if err != nil {
		t.Fatalf("failed to load messages: %v", err)
	}
	if len(messages) != 3 {
		t.Fatalf("messages length = %d, want 3", len(messages))
	}
	if llmCount.Load() != 1 {
		t.Fatalf("LLM request count = %d, want 1", llmCount.Load())
	}
}

func TestQQChannelProcessesGroupConversation(t *testing.T) {
	llmServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"group reply"}}]}`))
	}))
	defer llmServer.Close()

	sentMessages := make(chan qqSendCapture, 1)
	qqServer := newTestQQAPIServer(t, sentMessages, nil)
	defer qqServer.Close()

	root := t.TempDir()
	service := newTestService(root, llmServer.URL)
	channel := newTestQQChannel(root, service, qqServer.URL, &fakeQQGateway{configured: true})
	if err := channel.processUpdate(context.Background(), groupUpdate("event-1", "message-1", "hello")); err != nil {
		t.Fatalf("processUpdate() error = %v", err)
	}

	send := waitForQQSend(t, sentMessages)
	if send.Path != "/v2/groups/group-1/messages" {
		t.Fatalf("send path = %q", send.Path)
	}
	if send.Payload.Content != "group reply" || send.Payload.MsgSeq != 1 {
		t.Fatalf("payload = %+v", send.Payload)
	}
}

func TestQQChannelDedupesEventID(t *testing.T) {
	var llmCount atomic.Int32
	llmServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		llmCount.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"reply"}}]}`))
	}))
	defer llmServer.Close()

	var sendCount atomic.Int32
	sentMessages := make(chan qqSendCapture, 2)
	qqServer := newTestQQAPIServer(t, sentMessages, &sendCount)
	defer qqServer.Close()

	root := t.TempDir()
	service := newTestService(root, llmServer.URL)
	channel := newTestQQChannel(root, service, qqServer.URL, &fakeQQGateway{configured: true})
	update := c2cUpdate("event-1", "message-1", "hello")
	if err := channel.processUpdate(context.Background(), update); err != nil {
		t.Fatalf("first processUpdate() error = %v", err)
	}
	_ = waitForQQSend(t, sentMessages)
	if err := channel.processUpdate(context.Background(), update); err != nil {
		t.Fatalf("second processUpdate() error = %v", err)
	}
	if llmCount.Load() != 1 {
		t.Fatalf("LLM request count = %d, want 1", llmCount.Load())
	}
	if sendCount.Load() != 1 {
		t.Fatalf("send count = %d, want 1", sendCount.Load())
	}
}

func TestQQChannelRetriesPendingReplyWithoutRerunningModel(t *testing.T) {
	var llmCount atomic.Int32
	llmServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		llmCount.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"assistant reply"}}]}`))
	}))
	defer llmServer.Close()

	var sendCount atomic.Int32
	qqServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/app/getAppAccessToken":
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"access_token":"token-1","expires_in":"7200"}`))
		case "/v2/users/user-1/messages":
			count := sendCount.Add(1)
			if count == 1 {
				writer.WriteHeader(http.StatusBadGateway)
				_, _ = writer.Write([]byte("temporary failure"))
				return
			}
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"id":"sent-1"}`))
		default:
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
	}))
	defer qqServer.Close()

	root := t.TempDir()
	service := newTestService(root, llmServer.URL)
	channel := newTestQQChannel(root, service, qqServer.URL, &fakeQQGateway{configured: true})
	update := c2cUpdate("event-1", "message-1", "hello")
	if err := channel.processUpdate(context.Background(), update); err == nil {
		t.Fatalf("first processUpdate() error = nil, want send error")
	}
	waitForCondition(t, 2*time.Second, func() bool { return sendCount.Load() == 1 }, "first qq send attempt")

	stateStore := qqclient.NewStateStore(root)
	state, err := stateStore.Load("qq:c2c:user-1")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if state.PendingKey != "event-1" || state.PendingReply != "assistant reply" {
		t.Fatalf("state = %+v", state)
	}

	if err := channel.processUpdate(context.Background(), update); err != nil {
		t.Fatalf("retry processUpdate() error = %v", err)
	}
	if sendCount.Load() != 2 {
		t.Fatalf("send count = %d, want 2", sendCount.Load())
	}
	state, err = stateStore.Load("qq:c2c:user-1")
	if err != nil {
		t.Fatalf("Load() retry error = %v", err)
	}
	if state.LastCompletedKey != "event-1" || state.PendingReply != "" {
		t.Fatalf("state after retry = %+v", state)
	}
	if llmCount.Load() != 1 {
		t.Fatalf("LLM request count = %d, want 1", llmCount.Load())
	}
}

func TestQQChannelRequiresConfiguredGateway(t *testing.T) {
	root := t.TempDir()
	service := newTestService(root, "http://example.invalid")
	tokenSource := qqclient.NewCachedTokenSource(qqclient.NewTokenClient("", "", "", nil))
	channel := NewQQChannel(service, qqclient.NewClient(tokenSource, "", nil), &fakeQQGateway{}, qqclient.NewStateStore(root), qqclient.NewGatewayStateStore(root))
	if channel.Configured() {
		t.Fatal("Configured() = true, want false")
	}
}

func TestQQUpdateSenderSendsProgressWithIncrementingMsgSeq(t *testing.T) {
	sentMessages := make(chan qqSendCapture, 2)
	qqServer := newTestQQAPIServer(t, sentMessages, nil)
	defer qqServer.Close()

	tokenClient := qqclient.NewTokenClient("app-1", "secret-1", qqServer.URL, nil)
	sender := newQQUpdateSender(
		qqclient.NewClient(qqclient.NewCachedTokenSource(tokenClient), qqServer.URL, nil),
		c2cUpdate("event-1", "message-1", "hello"),
	)
	if err := sender.SendProgress(context.Background(), "仍在推理中：后台任务仍在运行。"); err != nil {
		t.Fatalf("SendProgress() error = %v", err)
	}
	if err := sender.SendReply(context.Background(), "reply"); err != nil {
		t.Fatalf("SendReply() error = %v", err)
	}

	progress := waitForQQSend(t, sentMessages)
	if progress.Payload.Content != "仍在推理中：后台任务仍在运行。" || progress.Payload.MsgID != "message-1" || progress.Payload.MsgSeq != 1 {
		t.Fatalf("progress payload = %+v", progress.Payload)
	}
	reply := waitForQQSend(t, sentMessages)
	if reply.Payload.Content != "reply" || reply.Payload.MsgID != "message-1" || reply.Payload.MsgSeq != 2 {
		t.Fatalf("reply payload = %+v", reply.Payload)
	}
}

func newTestQQChannel(root string, service *Service, qqBaseURL string, gateway qqGateway) *QQChannel {
	tokenClient := qqclient.NewTokenClient("app-1", "secret-1", qqBaseURL, nil)
	tokenSource := qqclient.NewCachedTokenSource(tokenClient)
	return NewQQChannel(
		service,
		qqclient.NewClient(tokenSource, qqBaseURL, nil),
		gateway,
		qqclient.NewStateStore(root),
		qqclient.NewGatewayStateStore(root),
	)
}

func newTestQQAPIServer(t *testing.T, sends chan<- qqSendCapture, sendCount *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/app/getAppAccessToken":
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"access_token":"token-1","expires_in":"7200"}`))
		case "/v2/users/user-1/messages", "/v2/groups/group-1/messages":
			if sendCount != nil {
				sendCount.Add(1)
			}
			var payload qqclient.SendMessageRequest
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Fatalf("failed to decode qq payload: %v", err)
			}
			sends <- qqSendCapture{Path: request.URL.Path, Auth: request.Header.Get("Authorization"), Payload: payload}
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"id":"sent-1"}`))
		default:
			t.Fatalf("unexpected qq path: %s", request.URL.Path)
		}
	}))
}

func c2cUpdate(eventID, messageID, text string) qqclient.Update {
	return qqclient.Update{
		EventID:    eventID,
		EventType:  "C2C_MESSAGE_CREATE",
		MessageID:  messageID,
		Kind:       qqclient.UpdateKindC2C,
		PeerKey:    "qq:c2c:user-1",
		DedupeKey:  eventID,
		Text:       text,
		UserOpenID: "user-1",
	}
}

func groupUpdate(eventID, messageID, text string) qqclient.Update {
	return qqclient.Update{
		EventID:      eventID,
		EventType:    "GROUP_AT_MESSAGE_CREATE",
		MessageID:    messageID,
		Kind:         qqclient.UpdateKindGroup,
		PeerKey:      "qq:group:group-1:member-1",
		DedupeKey:    eventID,
		Text:         text,
		MemberOpenID: "member-1",
		GroupOpenID:  "group-1",
	}
}

func waitForQQSend(t *testing.T, sends <-chan qqSendCapture) qqSendCapture {
	t.Helper()
	select {
	case send := <-sends:
		return send
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for qq send")
		return qqSendCapture{}
	}
}
