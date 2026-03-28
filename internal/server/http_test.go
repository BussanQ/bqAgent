package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"bqagent/internal/agent"
	"bqagent/internal/extagent"
	serverchanclient "bqagent/internal/serverchan"
	"bqagent/internal/session"
	"bqagent/internal/tools"
)

func TestChatEndpointCreatesAndResumesSession(t *testing.T) {
	var requestCount int
	llmServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestCount++
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(fmt.Sprintf(`{"choices":[{"message":{"role":"assistant","content":"reply-%d"}}]}`, requestCount)))
	}))
	defer llmServer.Close()

	root := t.TempDir()
	handler := NewHandler(HandlerOptions{Service: newTestService(root, llmServer.URL)})
	apiServer := httptest.NewServer(handler)
	defer apiServer.Close()

	first := postJSON(t, apiServer.URL+"/api/v1/chat", `{"message":"hello"}`)
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first status = %d, want 200", first.StatusCode)
	}
	var firstResponse chatResponse
	if err := json.NewDecoder(first.Body).Decode(&firstResponse); err != nil {
		t.Fatalf("failed to decode first response: %v", err)
	}
	_ = first.Body.Close()
	if firstResponse.SessionID == "" {
		t.Fatal("first session_id was empty")
	}
	if firstResponse.Reply != "reply-1" {
		t.Fatalf("first reply = %q, want %q", firstResponse.Reply, "reply-1")
	}

	second := postJSON(t, apiServer.URL+"/api/v1/chat", fmt.Sprintf(`{"session_id":%q,"message":"again"}`, firstResponse.SessionID))
	if second.StatusCode != http.StatusOK {
		t.Fatalf("second status = %d, want 200", second.StatusCode)
	}
	var secondResponse chatResponse
	if err := json.NewDecoder(second.Body).Decode(&secondResponse); err != nil {
		t.Fatalf("failed to decode second response: %v", err)
	}
	_ = second.Body.Close()
	if secondResponse.SessionID != firstResponse.SessionID {
		t.Fatalf("second session_id = %q, want %q", secondResponse.SessionID, firstResponse.SessionID)
	}
	if secondResponse.Reply != "reply-2" {
		t.Fatalf("second reply = %q, want %q", secondResponse.Reply, "reply-2")
	}
	if requestCount != 2 {
		t.Fatalf("LLM request count = %d, want 2", requestCount)
	}

	store := session.NewStore(root)
	savedSession, err := store.Open(firstResponse.SessionID)
	if err != nil {
		t.Fatalf("failed to open saved session: %v", err)
	}
	messages, err := savedSession.LoadMessages()
	if err != nil {
		t.Fatalf("failed to load messages: %v", err)
	}
	if len(messages) != 5 {
		t.Fatalf("messages length = %d, want 5", len(messages))
	}
}

func TestServerChanChatEndpointSendsReply(t *testing.T) {
	llmServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"<think>hidden</think>\nassistant reply"}}]}`))
	}))
	defer llmServer.Close()

	var delivered struct {
		text string
		desp string
	}
	deliveryServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := request.ParseForm(); err != nil {
			t.Fatalf("failed to parse delivery form: %v", err)
		}
		delivered.text = request.Form.Get("text")
		delivered.desp = request.Form.Get("desp")
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"code":0}`))
	}))
	defer deliveryServer.Close()

	targetURL, err := url.Parse(deliveryServer.URL)
	if err != nil {
		t.Fatalf("failed to parse delivery server URL: %v", err)
	}
	serverChanHTTPClient := &http.Client{Transport: rewriteTransport{target: targetURL, base: http.DefaultTransport}}

	root := t.TempDir()
	service := newTestService(root, llmServer.URL)
	handler := NewHandler(HandlerOptions{
		Service: service,
		Channels: []Channel{
			NewServerChanChannel(service, serverchanclient.NewClient(serverChanHTTPClient), nil),
		},
	})
	apiServer := httptest.NewServer(handler)
	defer apiServer.Close()

	form := url.Values{}
	form.Set("key", "sendkey-demo")
	form.Set("text", "hello title")
	form.Set("desp", "hello body")
	response, err := http.Post(apiServer.URL+"/api/v1/serverchan/chat", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("failed to post serverchan chat request: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}

	var payload chatResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if payload.SessionID == "" {
		t.Fatal("session_id was empty")
	}
	if payload.Reply != "assistant reply" {
		t.Fatalf("reply = %q, want %q", payload.Reply, "assistant reply")
	}
	if payload.ServerChanResponse != `{"code":0}` {
		t.Fatalf("serverchan response = %q, want %q", payload.ServerChanResponse, `{"code":0}`)
	}
	if delivered.text != "Re: hello title" {
		t.Fatalf("delivered text = %q, want %q", delivered.text, "Re: hello title")
	}
	if delivered.desp != "assistant reply" {
		t.Fatalf("delivered desp = %q, want %q", delivered.desp, "assistant reply")
	}
}

func TestServerChanBotWebhookProcessesConversation(t *testing.T) {
	var llmCount atomic.Int32
	llmServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		count := llmCount.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(fmt.Sprintf(`{"choices":[{"message":{"role":"assistant","content":"reply-%d"}}]}`, count)))
	}))
	defer llmServer.Close()

	var botCount atomic.Int32
	sentMessages := make(chan serverchanclient.BotSendMessageRequest, 4)
	botServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		botCount.Add(1)
		var payload serverchanclient.BotSendMessageRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatalf("failed to decode bot payload: %v", err)
		}
		sentMessages <- payload
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"ok":true,"result":{"message_id":1,"chat_id":42,"text":"ok"}}`))
	}))
	defer botServer.Close()

	root := t.TempDir()
	handler := newTestHandlerWithBot(root, llmServer.URL, botServer.URL, "bot-token", "secret")
	apiServer := httptest.NewServer(handler)
	defer apiServer.Close()

	status, body := postBotWebhook(t, apiServer.URL+"/api/v1/serverchan/bot/webhook", "secret", `{"ok":true,"update_id":1,"message":{"message_id":11,"text":"hello","chat_id":42}}`)
	if status != http.StatusOK {
		t.Fatalf("first status = %d, want 200", status)
	}
	if body != "ok" {
		t.Fatalf("first body = %q, want %q", body, "ok")
	}
	firstSend := waitForBotSend(t, sentMessages)
	if firstSend.ChatID != 42 {
		t.Fatalf("first send chat_id = %d, want 42", firstSend.ChatID)
	}
	if firstSend.Text != "reply-1" {
		t.Fatalf("first send text = %q, want %q", firstSend.Text, "reply-1")
	}

	status, body = postBotWebhook(t, apiServer.URL+"/api/v1/serverchan/bot/webhook", "secret", `{"ok":true,"update_id":2,"message":{"message_id":12,"text":"again","chat":{"id":42}}}`)
	if status != http.StatusOK {
		t.Fatalf("second status = %d, want 200", status)
	}
	if body != "ok" {
		t.Fatalf("second body = %q, want %q", body, "ok")
	}
	secondSend := waitForBotSend(t, sentMessages)
	if secondSend.Text != "reply-2" {
		t.Fatalf("second send text = %q, want %q", secondSend.Text, "reply-2")
	}

	waitForCondition(t, 2*time.Second, func() bool { return llmCount.Load() == 2 && botCount.Load() == 2 }, "webhook processing to complete")

	stateStore := serverchanclient.NewBotStateStore(root)
	waitForCondition(t, 2*time.Second, func() bool {
		state, err := stateStore.Load(42)
		return err == nil && state.SessionID != "" && state.LastCompletedUpdateID == 2 && state.PendingReply == ""
	}, "bot state to persist completed update")
	state, err := stateStore.Load(42)
	if err != nil {
		t.Fatalf("failed to load bot state: %v", err)
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
	if len(messages) != 5 {
		t.Fatalf("messages length = %d, want 5", len(messages))
	}
}

func TestServerChanBotWebhookDedupesUpdateID(t *testing.T) {
	var llmCount atomic.Int32
	llmServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		count := llmCount.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(fmt.Sprintf(`{"choices":[{"message":{"role":"assistant","content":"reply-%d"}}]}`, count)))
	}))
	defer llmServer.Close()

	var botCount atomic.Int32
	sentMessages := make(chan serverchanclient.BotSendMessageRequest, 2)
	botServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		botCount.Add(1)
		var payload serverchanclient.BotSendMessageRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatalf("failed to decode bot payload: %v", err)
		}
		sentMessages <- payload
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"ok":true,"result":{"message_id":1,"chat_id":42,"text":"ok"}}`))
	}))
	defer botServer.Close()

	root := t.TempDir()
	handler := newTestHandlerWithBot(root, llmServer.URL, botServer.URL, "bot-token", "secret")
	apiServer := httptest.NewServer(handler)
	defer apiServer.Close()

	status, body := postBotWebhook(t, apiServer.URL+"/api/v1/serverchan/bot/webhook", "secret", `{"ok":true,"update_id":1,"message":{"message_id":11,"text":"hello","chat_id":42}}`)
	if status != http.StatusOK || body != "ok" {
		t.Fatalf("first webhook = (%d, %q), want (200, ok)", status, body)
	}
	_ = waitForBotSend(t, sentMessages)

	status, body = postBotWebhook(t, apiServer.URL+"/api/v1/serverchan/bot/webhook", "secret", `{"ok":true,"update_id":1,"message":{"message_id":11,"text":"hello","chat_id":42}}`)
	if status != http.StatusOK || body != "ok" {
		t.Fatalf("duplicate webhook = (%d, %q), want (200, ok)", status, body)
	}
	time.Sleep(100 * time.Millisecond)
	if llmCount.Load() != 1 {
		t.Fatalf("LLM request count = %d, want 1", llmCount.Load())
	}
	if botCount.Load() != 1 {
		t.Fatalf("bot send count = %d, want 1", botCount.Load())
	}
}

func TestServerChanBotWebhookRetriesPendingReplyWithoutRerunningModel(t *testing.T) {
	var llmCount atomic.Int32
	llmServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		llmCount.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"assistant reply"}}]}`))
	}))
	defer llmServer.Close()

	var botCount atomic.Int32
	botServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		count := botCount.Add(1)
		if count == 1 {
			writer.WriteHeader(http.StatusBadGateway)
			_, _ = writer.Write([]byte(`temporary failure`))
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"ok":true,"result":{"message_id":1,"chat_id":42,"text":"ok"}}`))
	}))
	defer botServer.Close()

	root := t.TempDir()
	handler := newTestHandlerWithBot(root, llmServer.URL, botServer.URL, "bot-token", "secret")
	apiServer := httptest.NewServer(handler)
	defer apiServer.Close()

	status, body := postBotWebhook(t, apiServer.URL+"/api/v1/serverchan/bot/webhook", "secret", `{"ok":true,"update_id":1,"message":{"message_id":11,"text":"hello","chat_id":42}}`)
	if status != http.StatusOK || body != "ok" {
		t.Fatalf("first webhook = (%d, %q), want (200, ok)", status, body)
	}
	waitForCondition(t, 2*time.Second, func() bool { return botCount.Load() == 1 }, "first bot send attempt")

	stateStore := serverchanclient.NewBotStateStore(root)
	waitForCondition(t, 2*time.Second, func() bool {
		state, err := stateStore.Load(42)
		return err == nil && state.PendingUpdateID == 1 && state.PendingReply == "assistant reply"
	}, "pending reply to be saved")

	status, body = postBotWebhook(t, apiServer.URL+"/api/v1/serverchan/bot/webhook", "secret", `{"ok":true,"update_id":1,"message":{"message_id":11,"text":"hello","chat_id":42}}`)
	if status != http.StatusOK || body != "ok" {
		t.Fatalf("retry webhook = (%d, %q), want (200, ok)", status, body)
	}
	waitForCondition(t, 2*time.Second, func() bool {
		state, err := stateStore.Load(42)
		return err == nil && state.LastCompletedUpdateID == 1 && state.PendingReply == "" && botCount.Load() == 2
	}, "retry send to complete")

	if llmCount.Load() != 1 {
		t.Fatalf("LLM request count = %d, want 1", llmCount.Load())
	}
}

func TestServerChanBotWebhookRequiresConfiguredToken(t *testing.T) {
	root := t.TempDir()
	service := newTestService(root, "http://example.invalid")
	handler := NewHandler(HandlerOptions{
		Service: service,
		Channels: []Channel{
			NewServerChanChannel(
				service,
				nil,
				NewBotWebhookProcessor(
					service,
					serverchanclient.NewBotClient("", nil),
					serverchanclient.NewBotStateStore(root),
					"secret",
				),
			),
		},
	})
	apiServer := httptest.NewServer(handler)
	defer apiServer.Close()

	status, body := postBotWebhook(t, apiServer.URL+"/api/v1/serverchan/bot/webhook", "secret", `{"ok":true,"update_id":1,"message":{"message_id":11,"text":"hello","chat_id":42}}`)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", status)
	}
	if body != "serverchan bot is not configured" {
		t.Fatalf("body = %q, want %q", body, "serverchan bot is not configured")
	}
}

func TestChatEndpointRoutesSlashPrefixedMessageToExternalAgent(t *testing.T) {
	root := t.TempDir()
	service := newTestServiceWithExternal(root)
	handler := NewHandler(HandlerOptions{Service: service})
	apiServer := httptest.NewServer(handler)
	defer apiServer.Close()

	response := postJSON(t, apiServer.URL+"/api/v1/chat", `{"message":"/codex hello external"}`)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
	defer response.Body.Close()

	var payload chatResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if payload.Reply != "codex reply" {
		t.Fatalf("reply = %q, want %q", payload.Reply, "codex reply")
	}
}

func TestChatEndpointContinuesExternalAgentSessionWithoutSlashPrefix(t *testing.T) {
	root := t.TempDir()
	service := newTestServiceWithPersistentExternal(root)
	defer func() {
		if service.externalBroker != nil {
			_ = service.externalBroker.Close()
		}
	}()
	handler := NewHandler(HandlerOptions{Service: service})
	apiServer := httptest.NewServer(handler)
	defer apiServer.Close()

	first := postJSON(t, apiServer.URL+"/api/v1/chat", `{"message":"/claude hello external"}`)
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first status = %d, want 200", first.StatusCode)
	}
	var firstPayload chatResponse
	if err := json.NewDecoder(first.Body).Decode(&firstPayload); err != nil {
		t.Fatalf("failed to decode first response: %v", err)
	}
	_ = first.Body.Close()
	if firstPayload.SessionID == "" {
		t.Fatal("first session_id was empty")
	}

	second := postJSON(t, apiServer.URL+"/api/v1/chat", fmt.Sprintf(`{"session_id":%q,"message":"follow up"}`, firstPayload.SessionID))
	if second.StatusCode != http.StatusOK {
		t.Fatalf("second status = %d, want 200", second.StatusCode)
	}
	defer second.Body.Close()

	var secondPayload chatResponse
	if err := json.NewDecoder(second.Body).Decode(&secondPayload); err != nil {
		t.Fatalf("failed to decode second response: %v", err)
	}
	if secondPayload.SessionID != firstPayload.SessionID {
		t.Fatalf("second session_id = %q, want %q", secondPayload.SessionID, firstPayload.SessionID)
	}
	if secondPayload.Reply != "reply:follow up" {
		t.Fatalf("second reply = %q, want %q", secondPayload.Reply, "reply:follow up")
	}

	state, err := extagent.NewStateStore(root).Load(firstPayload.SessionID)
	if err != nil {
		t.Fatalf("failed to load external agent state: %v", err)
	}
	if state.Agent != extagent.AgentClaude {
		t.Fatalf("agent = %q, want %q", state.Agent, extagent.AgentClaude)
	}
	if state.ExternalSessionID != "acp-session-1" {
		t.Fatalf("external session id = %q, want %q", state.ExternalSessionID, "acp-session-1")
	}
}

func TestChatEndpointCanSwitchBackToDefaultModel(t *testing.T) {
	var requestCount int
	llmServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestCount++
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"default reply"}}]}`))
	}))
	defer llmServer.Close()

	root := t.TempDir()
	service := newTestServiceWithExternal(root)
	service.client = agent.NewClient("", llmServer.URL, nil)
	handler := NewHandler(HandlerOptions{Service: service})
	apiServer := httptest.NewServer(handler)
	defer apiServer.Close()

	first := postJSON(t, apiServer.URL+"/api/v1/chat", `{"message":"/claude hello external"}`)
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first status = %d, want 200", first.StatusCode)
	}
	var firstPayload chatResponse
	if err := json.NewDecoder(first.Body).Decode(&firstPayload); err != nil {
		t.Fatalf("failed to decode first response: %v", err)
	}
	_ = first.Body.Close()

	reset := postJSON(t, apiServer.URL+"/api/v1/chat", fmt.Sprintf(`{"session_id":%q,"message":"/default"}`, firstPayload.SessionID))
	if reset.StatusCode != http.StatusOK {
		t.Fatalf("reset status = %d, want 200", reset.StatusCode)
	}
	var resetPayload chatResponse
	if err := json.NewDecoder(reset.Body).Decode(&resetPayload); err != nil {
		t.Fatalf("failed to decode reset response: %v", err)
	}
	_ = reset.Body.Close()
	if resetPayload.Reply != "switched to default model" {
		t.Fatalf("reset reply = %q, want %q", resetPayload.Reply, "switched to default model")
	}

	second := postJSON(t, apiServer.URL+"/api/v1/chat", fmt.Sprintf(`{"session_id":%q,"message":"hello model"}`, firstPayload.SessionID))
	if second.StatusCode != http.StatusOK {
		t.Fatalf("second status = %d, want 200", second.StatusCode)
	}
	defer second.Body.Close()
	var secondPayload chatResponse
	if err := json.NewDecoder(second.Body).Decode(&secondPayload); err != nil {
		t.Fatalf("failed to decode second response: %v", err)
	}
	if secondPayload.Reply != "default reply" {
		t.Fatalf("second reply = %q, want %q", secondPayload.Reply, "default reply")
	}
	if requestCount != 1 {
		t.Fatalf("LLM request count = %d, want 1 after /default", requestCount)
	}

	state, err := extagent.NewStateStore(root).Load(firstPayload.SessionID)
	if err != nil {
		t.Fatalf("failed to load external agent state: %v", err)
	}
	if state.Agent != "" {
		t.Fatalf("agent = %q, want empty after /default", state.Agent)
	}
}

func TestChatEndpointRejectsEmptySlashPrefixedMessage(t *testing.T) {
	root := t.TempDir()
	service := newTestServiceWithExternal(root)
	handler := NewHandler(HandlerOptions{Service: service})
	apiServer := httptest.NewServer(handler)
	defer apiServer.Close()

	response := postJSON(t, apiServer.URL+"/api/v1/chat", `{"message":"/claude"}`)
	if response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.StatusCode)
	}
	defer response.Body.Close()

	var payload chatResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if !strings.Contains(payload.Error, "message is required after /claude") {
		t.Fatalf("error = %q, want validation message", payload.Error)
	}
}

func TestServiceHandleTurnAppendsMemory(t *testing.T) {
	llmServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"memory reply"}}]}`))
	}))
	defer llmServer.Close()

	root := t.TempDir()
	var (
		task   string
		result string
	)
	service := NewService(ServiceOptions{
		WorkspaceRoot:   root,
		Client:          agent.NewClient("", llmServer.URL, nil),
		SystemPrompt:    "You are a helpful assistant. Be concise.",
		ToolDefinitions: tools.NewCatalog(tools.Options{WorkspaceRoot: root}).Definitions(),
		Functions:       tools.NewCatalog(tools.Options{WorkspaceRoot: root}).Registry(),
		MemoryAppend: func(savedTask, savedResult string) error {
			task = savedTask
			result = savedResult
			return nil
		},
	})

	response, err := service.HandleTurn(context.Background(), TurnRequest{Message: "remember this"})
	if err != nil {
		t.Fatalf("HandleTurn returned error: %v", err)
	}
	if response.Reply != "memory reply" {
		t.Fatalf("reply = %q, want %q", response.Reply, "memory reply")
	}
	if task != "remember this" {
		t.Fatalf("memory task = %q, want %q", task, "remember this")
	}
	if result != "memory reply" {
		t.Fatalf("memory result = %q, want %q", result, "memory reply")
	}
}

func newTestService(root, baseURL string) *Service {
	chatClient := agent.NewClient("", baseURL, nil)
	catalog := tools.NewCatalog(tools.Options{WorkspaceRoot: root})
	return NewService(ServiceOptions{
		WorkspaceRoot:   root,
		Client:          chatClient,
		SystemPrompt:    "You are a helpful assistant. Be concise.",
		ToolDefinitions: catalog.Definitions(),
		Functions:       catalog.Registry(),
	})
}

func newTestServiceWithExternal(root string) *Service {
	service := newTestService(root, "http://example.invalid")
	service.externalBroker = extagent.NewBroker(
		extagent.NewStateStore(root),
		map[extagent.AgentName]extagent.DetectionResult{
			extagent.AgentCodex: {
				Agent:     extagent.AgentCodex,
				Preferred: &extagent.AgentTransport{Agent: extagent.AgentCodex, Kind: extagent.TransportCLI, Command: extagent.CommandSpec{Command: os.Args[0], Args: []string{"-test.run=TestServerExternalHelperProcess", "--", "cli-codex"}}},
			},
			extagent.AgentClaude: {
				Agent:     extagent.AgentClaude,
				Preferred: &extagent.AgentTransport{Agent: extagent.AgentClaude, Kind: extagent.TransportCLI, Command: extagent.CommandSpec{Command: os.Args[0], Args: []string{"-test.run=TestServerExternalHelperProcess", "--", "cli-claude"}}},
			},
		},
		nil,
	)
	return service
}

func newTestServiceWithPersistentExternal(root string) *Service {
	service := newTestService(root, "http://example.invalid")
	service.externalBroker = extagent.NewBroker(
		extagent.NewStateStore(root),
		map[extagent.AgentName]extagent.DetectionResult{
			extagent.AgentClaude: {
				Agent: extagent.AgentClaude,
				Preferred: &extagent.AgentTransport{
					Agent:   extagent.AgentClaude,
					Kind:    extagent.TransportACP,
					Command: extagent.CommandSpec{Command: os.Args[0], Args: []string{"-test.run=TestServerExternalHelperProcess", "--", "acp-claude"}},
				},
			},
		},
		extagent.NewACPClient,
	)
	return service
}

func TestServerExternalHelperProcess(t *testing.T) {
	if len(os.Args) < 4 || os.Args[2] != "--" {
		return
	}
	switch os.Args[3] {
	case "cli-codex":
		outputPath := ""
		for i := 4; i < len(os.Args)-1; i++ {
			if os.Args[i] == "--output-last-message" {
				outputPath = os.Args[i+1]
			}
		}
		if outputPath != "" {
			_ = os.WriteFile(outputPath, []byte("codex reply"), 0o644)
		}
		_, _ = os.Stdout.WriteString("{\"event\":\"session\",\"session_id\":\"codex-session-1\"}\n")
	case "cli-claude":
		_, _ = os.Stdout.WriteString(`{"result":"claude reply","session_id":"claude-session-1"}`)
	case "acp-claude":
		runServerACPHelper()
	}
	os.Exit(0)
}

func runServerACPHelper() {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var request map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			continue
		}
		id, _ := request["id"].(float64)
		method, _ := request["method"].(string)
		switch method {
		case "initialize":
			writeServerACPEnvelope(map[string]any{
				"id": int64(id),
				"result": map[string]any{
					"agentCapabilities": map[string]any{"loadSession": true},
				},
			})
		case "session/new":
			writeServerACPEnvelope(map[string]any{"id": int64(id), "result": map[string]any{"sessionId": "acp-session-1"}})
		case "session/load":
			writeServerACPEnvelope(map[string]any{"id": int64(id), "result": map[string]any{"sessionId": "acp-session-1"}})
		case "session/prompt":
			params, _ := request["params"].(map[string]any)
			sessionID, _ := params["sessionId"].(string)
			prompt := ""
			if prompts, ok := params["prompt"].([]any); ok && len(prompts) > 0 {
				if first, ok := prompts[0].(map[string]any); ok {
					prompt, _ = first["text"].(string)
				}
			}
			writeServerACPEnvelope(map[string]any{
				"method": "session/update",
				"params": map[string]any{
					"sessionId": sessionID,
					"update": map[string]any{
						"sessionUpdate": "agent_message_chunk",
						"content":       map[string]any{"text": "reply:" + prompt},
					},
				},
			})
			writeServerACPEnvelope(map[string]any{"id": int64(id), "result": map[string]any{"stopReason": "end_turn"}})
		}
	}
}

func writeServerACPEnvelope(payload map[string]any) {
	data, _ := json.Marshal(payload)
	_, _ = os.Stdout.Write(append(data, '\n'))
	time.Sleep(5 * time.Millisecond)
}

func newTestHandlerWithBot(root, llmBaseURL, botBaseURL, token, secret string) http.Handler {
	service := newTestService(root, llmBaseURL)
	return NewHandler(HandlerOptions{
		Service: service,
		Channels: []Channel{
			NewServerChanChannel(
				service,
				serverchanclient.NewClient(nil),
				NewBotWebhookProcessor(
					service,
					serverchanclient.NewBotClientWithBaseURL(token, botBaseURL, nil),
					serverchanclient.NewBotStateStore(root),
					secret,
				),
			),
		},
	})
}

func postJSON(t *testing.T, url string, body string) *http.Response {
	t.Helper()
	response, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("failed to post JSON request: %v", err)
	}
	return response
}

func postBotWebhook(t *testing.T, url string, secret string, body string) (int, string) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("failed to create webhook request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	if secret != "" {
		request.Header.Set("X-Sc3Bot-Webhook-Secret", secret)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("failed to post bot webhook request: %v", err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("failed to read webhook response: %v", err)
	}
	return response.StatusCode, strings.TrimSpace(string(payload))
}

func waitForBotSend(t *testing.T, requests <-chan serverchanclient.BotSendMessageRequest) serverchanclient.BotSendMessageRequest {
	t.Helper()
	select {
	case request := <-requests:
		return request
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for bot send")
		return serverchanclient.BotSendMessageRequest{}
	}
}

func waitForCondition(t *testing.T, timeout time.Duration, check func() bool, description string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", description)
}

type rewriteTransport struct {
	target *url.URL
	base   http.RoundTripper
}

func (transport rewriteTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	cloned := request.Clone(request.Context())
	cloned.URL.Scheme = transport.target.Scheme
	cloned.URL.Host = transport.target.Host
	cloned.Host = transport.target.Host
	return transport.base.RoundTrip(cloned)
}
