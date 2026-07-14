package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"bqagent/internal/agent"
	"bqagent/internal/extagent"
	serverchanclient "bqagent/internal/serverchan"
	"bqagent/internal/session"
	"bqagent/internal/tools"
	"bqagent/internal/workspace"
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
	output, err := os.ReadFile(savedSession.OutputPath())
	if err != nil {
		t.Fatalf("failed to read output log: %v", err)
	}
	if !hasTimestampedLineContaining(string(output), "reply-1") {
		t.Fatalf("output log = %q, want timestamped reply line", string(output))
	}
}

func TestStatusEndpointReturnsEffectiveRuntimeModelWithoutSecrets(t *testing.T) {
	service := NewService(ServiceOptions{
		WorkspaceRoot: t.TempDir(),
		APIType:       agent.APITypeAnthropic,
		Model:         "claude-test",
	})
	apiServer := httptest.NewServer(NewHandler(HandlerOptions{Service: service}))
	defer apiServer.Close()

	response, err := http.Get(apiServer.URL + "/api/v1/status")
	if err != nil {
		t.Fatalf("GET status failed: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
	var payload statusResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("decode status failed: %v", err)
	}
	if payload.Status != "ok" || payload.LLM.APIType != agent.APITypeAnthropic || payload.LLM.Model != "claude-test" {
		t.Fatalf("status payload = %#v", payload)
	}
	encoded, _ := json.Marshal(payload)
	for _, forbidden := range []string{"api_key", "base_url", "secret"} {
		if strings.Contains(strings.ToLower(string(encoded)), forbidden) {
			t.Fatalf("status payload exposes forbidden field %q: %s", forbidden, encoded)
		}
	}

	request, err := http.NewRequest(http.MethodPost, apiServer.URL+"/api/v1/status", nil)
	if err != nil {
		t.Fatal(err)
	}
	methodResponse, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("POST status failed: %v", err)
	}
	defer methodResponse.Body.Close()
	if methodResponse.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want 405", methodResponse.StatusCode)
	}

	healthResponse, err := http.Get(apiServer.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET health failed: %v", err)
	}
	defer healthResponse.Body.Close()
	var health map[string]string
	if err := json.NewDecoder(healthResponse.Body).Decode(&health); err != nil {
		t.Fatalf("decode health failed: %v", err)
	}
	if len(health) != 1 || health["status"] != "ok" {
		t.Fatalf("health payload = %#v, want unchanged status response", health)
	}
}

func TestChatEndpointInjectsCurrentModelIdentityForNewAndResumedSessions(t *testing.T) {
	var systemPrompts []string
	llmServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var payload struct {
			Messages []map[string]any `json:"messages"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Errorf("decode LLM request: %v", err)
		} else if len(payload.Messages) > 0 {
			content, _ := payload.Messages[0]["content"].(string)
			systemPrompts = append(systemPrompts, content)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer llmServer.Close()

	root := t.TempDir()
	service := NewService(ServiceOptions{
		WorkspaceRoot: root,
		Client:        agent.NewClient("", llmServer.URL, nil),
		APIType:       agent.APITypeOpenAIResponse,
		Model:         "gpt-test",
		SystemPrompt:  "Base prompt",
	})
	apiServer := httptest.NewServer(NewHandler(HandlerOptions{Service: service}))
	defer apiServer.Close()

	first := postJSON(t, apiServer.URL+"/api/v1/chat", `{"message":"hello"}`)
	var firstPayload chatResponse
	if err := json.NewDecoder(first.Body).Decode(&firstPayload); err != nil {
		t.Fatal(err)
	}
	_ = first.Body.Close()
	second := postJSON(t, apiServer.URL+"/api/v1/chat", fmt.Sprintf(`{"session_id":%q,"message":"again"}`, firstPayload.SessionID))
	_ = second.Body.Close()

	if len(systemPrompts) != 2 {
		t.Fatalf("system prompts = %#v, want 2", systemPrompts)
	}
	for _, prompt := range systemPrompts {
		if !strings.Contains(prompt, "Current runtime model: gpt-test (API type: openai-response).") {
			t.Fatalf("system prompt = %q, want current model identity", prompt)
		}
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

func TestChatEndpointRoutesSkillSlashToRunSkill(t *testing.T) {
	var requestCount atomic.Int32
	llmServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestCount.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"demo skill result"}}]}`))
	}))
	defer llmServer.Close()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".agent", "skills", "demo"), 0o755); err != nil {
		t.Fatalf("failed to create skill directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".agent", "skills", "demo", "SKILL.md"), []byte("# Demo Skill\n\nReply with the prepared demo result."), 0o644); err != nil {
		t.Fatalf("failed to write skill file: %v", err)
	}

	service := newTestService(root, llmServer.URL)
	handler := NewHandler(HandlerOptions{Service: service})
	apiServer := httptest.NewServer(handler)
	defer apiServer.Close()

	response := postJSON(t, apiServer.URL+"/api/v1/chat", `{"message":"/skill demo concise"}`)
	defer response.Body.Close()

	var payload chatResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200, error=%q", response.StatusCode, payload.Error)
	}
	if payload.Reply != "demo skill result" {
		t.Fatalf("reply = %q, want %q", payload.Reply, "demo skill result")
	}
	if requestCount.Load() != 1 {
		t.Fatalf("LLM request count = %d, want 1", requestCount.Load())
	}
}

func TestChatEndpointRoutesSkillIDFirstTokenToRunSkill(t *testing.T) {
	var requestCount atomic.Int32
	llmServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestCount.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"demo skill result"}}]}`))
	}))
	defer llmServer.Close()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".agent", "skills", "demo"), 0o755); err != nil {
		t.Fatalf("failed to create skill directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".agent", "skills", "demo", "SKILL.md"), []byte("# Demo Skill\n\nReply with the prepared demo result."), 0o644); err != nil {
		t.Fatalf("failed to write skill file: %v", err)
	}

	service := newTestService(root, llmServer.URL)
	handler := NewHandler(HandlerOptions{Service: service})
	apiServer := httptest.NewServer(handler)
	defer apiServer.Close()

	response := postJSON(t, apiServer.URL+"/api/v1/chat", `{"message":"demo concise"}`)
	defer response.Body.Close()

	var payload chatResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200, error=%q", response.StatusCode, payload.Error)
	}
	if payload.Reply != "demo skill result" {
		t.Fatalf("reply = %q, want %q", payload.Reply, "demo skill result")
	}
	if requestCount.Load() != 1 {
		t.Fatalf("LLM request count = %d, want 1", requestCount.Load())
	}
}

func TestChatEndpointRoutesSkillAliasFirstTokenToRunSkill(t *testing.T) {
	var requestCount atomic.Int32
	var requestBody []byte
	llmServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestCount.Add(1)
		var err error
		requestBody, err = io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("failed to read request body: %v", err)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"aihot skill result"}}]}`))
	}))
	defer llmServer.Close()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".agent", "skills", "aihot-skill"), 0o755); err != nil {
		t.Fatalf("failed to create skill directory: %v", err)
	}
	skillContent := "---\nalias: aihot\n---\n\n# AIHot Skill\n\nReply with the prepared aihot result."
	if err := os.WriteFile(filepath.Join(root, ".agent", "skills", "aihot-skill", "SKILL.md"), []byte(skillContent), 0o644); err != nil {
		t.Fatalf("failed to write skill file: %v", err)
	}

	service := newTestService(root, llmServer.URL)
	handler := NewHandler(HandlerOptions{Service: service})
	apiServer := httptest.NewServer(handler)
	defer apiServer.Close()

	response := postJSON(t, apiServer.URL+"/api/v1/chat", `{"message":"aihot 获取AI日报"}`)
	defer response.Body.Close()

	var payload chatResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200, error=%q", response.StatusCode, payload.Error)
	}
	if payload.Reply != "aihot skill result" {
		t.Fatalf("reply = %q, want %q", payload.Reply, "aihot skill result")
	}
	if requestCount.Load() != 1 {
		t.Fatalf("LLM request count = %d, want 1", requestCount.Load())
	}
	if !strings.Contains(string(requestBody), "获取AI日报") {
		t.Fatalf("request body = %s, want skill args", requestBody)
	}
}

func TestChatEndpointRoutesSkillSlashAliasToRunSkill(t *testing.T) {
	var requestCount atomic.Int32
	llmServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestCount.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"alias skill result"}}]}`))
	}))
	defer llmServer.Close()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".agent", "skills", "aihot-skill"), 0o755); err != nil {
		t.Fatalf("failed to create skill directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".agent", "skills", "aihot-skill", "SKILL.md"), []byte("---\nalias: aihot\n---\n\n# AIHot Skill\n\nReply with the prepared result."), 0o644); err != nil {
		t.Fatalf("failed to write skill file: %v", err)
	}

	service := newTestService(root, llmServer.URL)
	handler := NewHandler(HandlerOptions{Service: service})
	apiServer := httptest.NewServer(handler)
	defer apiServer.Close()

	response := postJSON(t, apiServer.URL+"/api/v1/chat", `{"message":"/skill aihot 获取AI日报"}`)
	defer response.Body.Close()

	var payload chatResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200, error=%q", response.StatusCode, payload.Error)
	}
	if payload.Reply != "alias skill result" {
		t.Fatalf("reply = %q, want %q", payload.Reply, "alias skill result")
	}
	if requestCount.Load() != 1 {
		t.Fatalf("LLM request count = %d, want 1", requestCount.Load())
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

func TestServiceHandleTurnHotReloadsSkillSectionForExistingSession(t *testing.T) {
	root := t.TempDir()
	catalog := tools.NewCatalog(tools.Options{WorkspaceRoot: root})
	var requestBodies []string
	llmServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("failed to read request body: %v", err)
		}
		requestBodies = append(requestBodies, string(body))
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"memory reply"}}]}`))
	}))
	defer llmServer.Close()

	service := NewService(ServiceOptions{
		WorkspaceRoot: root,
		Client:        agent.NewClient("", llmServer.URL, nil),
		SystemPrompt:  "You are a helpful assistant. Be concise.",
		SystemPromptBuilder: func() (string, error) {
			return (&workspace.Workspace{Root: root}).BuildSystemPrompt(agent.DefaultSystemPrompt)
		},
		ToolDefinitions: catalog.Definitions(),
		Functions:       catalog.Registry(),
	})

	first, err := service.HandleTurn(context.Background(), TurnRequest{Message: "hello"})
	if err != nil {
		t.Fatalf("first HandleTurn returned error: %v", err)
	}
	if len(requestBodies) != 1 {
		t.Fatalf("request body count = %d, want 1", len(requestBodies))
	}
	if strings.Contains(requestBodies[0], "# Skills") {
		t.Fatalf("first request body unexpectedly contained skills section: %s", requestBodies[0])
	}
	if err := os.MkdirAll(filepath.Join(root, ".agent", "skills", "demo"), 0o755); err != nil {
		t.Fatalf("failed to create skill directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".agent", "skills", "demo", "SKILL.md"), []byte("# Demo Skill\n\nHelps with demo tasks."), 0o644); err != nil {
		t.Fatalf("failed to write skill file: %v", err)
	}

	_, err = service.HandleTurn(context.Background(), TurnRequest{SessionID: first.SessionID, Message: "hello again"})
	if err != nil {
		t.Fatalf("second HandleTurn returned error: %v", err)
	}
	if len(requestBodies) != 2 {
		t.Fatalf("request body count = %d, want 2", len(requestBodies))
	}
	if !strings.Contains(requestBodies[1], "# Skills") || !strings.Contains(requestBodies[1], "demo (Demo Skill): Helps with demo tasks.") {
		t.Fatalf("second request body = %s, want refreshed skills section", requestBodies[1])
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

type failingChatClient struct{}

func (failingChatClient) CreateChatCompletion(context.Context, string, []map[string]any, []tools.Definition) (agent.AssistantMessage, error) {
	return agent.AssistantMessage{}, errors.New("upstream unavailable")
}

func (failingChatClient) CreateChatCompletionStream(context.Context, string, []map[string]any, []tools.Definition, func(string)) (agent.AssistantMessage, error) {
	return agent.AssistantMessage{}, errors.New("upstream unavailable")
}

func TestServiceHandleTurnReturnsUpstreamError(t *testing.T) {
	root := t.TempDir()
	service := NewService(ServiceOptions{
		WorkspaceRoot:   root,
		Client:          failingChatClient{},
		SystemPrompt:    "You are a helpful assistant. Be concise.",
		ToolDefinitions: tools.NewCatalog(tools.Options{WorkspaceRoot: root}).Definitions(),
		Functions:       tools.NewCatalog(tools.Options{WorkspaceRoot: root}).Registry(),
	})

	response, err := service.HandleTurn(context.Background(), TurnRequest{Message: "hello"})
	if err == nil {
		t.Fatal("HandleTurn returned nil error, want upstream unavailable")
	}
	if !strings.Contains(err.Error(), "upstream unavailable") {
		t.Fatalf("error = %q, want upstream unavailable", err.Error())
	}
	if response.SessionID != "" {
		t.Fatalf("session id = %q, want empty on failure response", response.SessionID)
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

func hasTimestampedLineContaining(content string, needle string) bool {
	for _, line := range strings.Split(content, "\n") {
		if !strings.Contains(line, needle) {
			continue
		}
		if len(line) < len("2006-01-02 15:04:05 ") {
			continue
		}
		if line[4] == '-' && line[7] == '-' && line[10] == ' ' && line[13] == ':' && line[16] == ':' && line[19] == ' ' {
			return true
		}
	}
	return false
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

type sequenceChatClient struct {
	responses []agent.AssistantMessage
	requests  int
}

func TestServicePersistsWorkingContextSnapshot(t *testing.T) {
	root := t.TempDir()
	client := &sequenceChatClient{responses: []agent.AssistantMessage{{Content: "reply"}}}
	service := NewService(ServiceOptions{
		WorkspaceRoot: root,
		Client:        client,
		SystemPrompt:  "system prompt",
	})

	response, err := service.HandleTurn(context.Background(), TurnRequest{Message: "hello"})
	if err != nil {
		t.Fatalf("HandleTurn returned error: %v", err)
	}
	savedSession, err := session.NewStore(root).Open(response.SessionID)
	if err != nil {
		t.Fatalf("Open session returned error: %v", err)
	}
	working, err := savedSession.LoadWorkingMessages()
	if err != nil {
		t.Fatalf("LoadWorkingMessages returned error: %v", err)
	}
	if len(working) != 3 || working[len(working)-1]["content"] != "reply" {
		t.Fatalf("working messages = %#v", working)
	}
	raw, err := savedSession.LoadMessages()
	if err != nil {
		t.Fatalf("LoadMessages returned error: %v", err)
	}
	if len(raw) != 3 {
		t.Fatalf("raw transcript messages = %d, want 3", len(raw))
	}
}

func (client *sequenceChatClient) CreateChatCompletion(ctx context.Context, _ string, _ []map[string]any, _ []tools.Definition) (agent.AssistantMessage, error) {
	client.requests++
	if err := ctx.Err(); err != nil {
		return agent.AssistantMessage{}, err
	}
	if len(client.responses) == 0 {
		return agent.AssistantMessage{}, errors.New("no response configured")
	}
	response := client.responses[0]
	client.responses = client.responses[1:]
	return response, nil
}

func (client *sequenceChatClient) CreateChatCompletionStream(ctx context.Context, model string, messages []map[string]any, definitions []tools.Definition, _ func(string)) (agent.AssistantMessage, error) {
	return client.CreateChatCompletion(ctx, model, messages, definitions)
}

func TestServiceHandleTurnOptionsMaxIterationsOverridesDefault(t *testing.T) {
	root := t.TempDir()
	client := &sequenceChatClient{responses: []agent.AssistantMessage{
		{ToolCalls: []agent.ToolCall{{ID: "tool-1", Function: agent.FunctionCall{Name: "missing_tool", Arguments: `{}`}}}},
	}}
	service := NewService(ServiceOptions{
		WorkspaceRoot:   root,
		Client:          client,
		SystemPrompt:    "You are a helpful assistant. Be concise.",
		ToolDefinitions: []tools.Definition{{Type: "function", Function: tools.FunctionDefinition{Name: "missing_tool"}}},
		DefaultMaxTurns: 100,
	})

	response, err := service.HandleTurnWithOptions(context.Background(), TurnRequest{Message: "loop"}, TurnOptions{MaxIterations: 1})
	if err != nil {
		t.Fatalf("HandleTurnWithOptions returned error: %v", err)
	}
	if client.requests != 1 {
		t.Fatalf("requests = %d, want 1", client.requests)
	}
	if !strings.Contains(response.Reply, "reached maximum of 1 iterations") {
		t.Fatalf("reply = %q, want max iteration override", response.Reply)
	}
}

func TestDefaultChannelMaxIterationsIsInteractiveBudget(t *testing.T) {
	if DefaultChannelMaxIterations != 30 {
		t.Fatalf("DefaultChannelMaxIterations = %d, want 30", DefaultChannelMaxIterations)
	}
	if DefaultChannelMaxIterations >= agent.DefaultMaxIterations {
		t.Fatalf("channel default %d should be lower than agent safety valve %d", DefaultChannelMaxIterations, agent.DefaultMaxIterations)
	}
}

func TestInteractiveChannelStageConfigUsesCheckpointBudget(t *testing.T) {
	stage := InteractiveChannelStageConfig()
	if stage.MaxIterations != ChannelStageMaxIterations() || stage.Timeout != ChannelStageTimeout() {
		t.Fatalf("stage = %+v, want channel stage settings", stage)
	}
	if !stage.LoopProtection {
		t.Fatal("interactive channel loop protection is disabled")
	}
}

func TestChannelTurnRunnerProducesStageCheckpoint(t *testing.T) {
	root := t.TempDir()
	client := &sequenceChatClient{responses: []agent.AssistantMessage{
		{ToolCalls: []agent.ToolCall{{ID: "tool-1", Function: agent.FunctionCall{Name: "missing_tool", Arguments: `{}`}}}},
		{Content: "已发现\n- 已检查入口\n\n未完成\n- 深入分析\n\n建议下一步\n- 回复“继续”"},
	}}
	service := NewService(ServiceOptions{
		WorkspaceRoot:   root,
		Client:          client,
		SystemPrompt:    "You are a helpful assistant. Be concise.",
		ToolDefinitions: []tools.Definition{{Type: "function", Function: tools.FunctionDefinition{Name: "missing_tool"}}},
		DefaultMaxTurns: 30,
	})
	runner := NewChannelTurnRunner(service)
	var state ChannelConversationState
	var replies []string
	var progress []string
	_, err := runner.Process(context.Background(), ChannelTurnOptions{
		PeerKey:   "peer-stage",
		DedupeKey: "ctx-stage",
		Message:   "分析架构",
		Stage:     agent.StageConfig{MaxIterations: 1, LoopProtection: true, EmitProgress: true},
		LoadState: func() (ChannelConversationState, error) { return state, nil },
		SaveState: func(next ChannelConversationState) error {
			state = next
			return nil
		},
		SendReply: func(_ context.Context, reply string) error {
			replies = append(replies, reply)
			return nil
		},
		SendProgress: func(_ context.Context, message string) error {
			progress = append(progress, message)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Process returned error: %v", err)
	}
	if client.requests != 2 {
		t.Fatalf("requests = %d, want tool round plus checkpoint summary", client.requests)
	}
	if len(replies) != 1 || !strings.Contains(replies[0], "已发现") || !strings.Contains(replies[0], "继续") {
		t.Fatalf("replies = %#v, want stage checkpoint", replies)
	}
	joinedProgress := strings.Join(progress, "\n")
	for _, want := range []string{"iteration 1", "missing_tool", "Preparing stage summary"} {
		if !strings.Contains(joinedProgress, want) {
			t.Fatalf("progress = %q, missing %q", joinedProgress, want)
		}
	}
	if state.SessionID == "" || state.LastCompletedKey != "ctx-stage" {
		t.Fatalf("state = %+v, want persisted session/checkpoint turn", state)
	}
}

func TestChannelTurnRunnerUsesChannelMaxIterations(t *testing.T) {
	root := t.TempDir()
	client := &sequenceChatClient{responses: []agent.AssistantMessage{
		{ToolCalls: []agent.ToolCall{{ID: "tool-1", Function: agent.FunctionCall{Name: "missing_tool", Arguments: `{}`}}}},
	}}
	service := NewService(ServiceOptions{
		WorkspaceRoot:   root,
		Client:          client,
		SystemPrompt:    "You are a helpful assistant. Be concise.",
		ToolDefinitions: []tools.Definition{{Type: "function", Function: tools.FunctionDefinition{Name: "missing_tool"}}},
		DefaultMaxTurns: 100,
	})
	runner := NewChannelTurnRunner(service)
	previous := ChannelMaxIterations()
	SetChannelMaxIterations(1)
	defer SetChannelMaxIterations(previous)

	var state ChannelConversationState
	var replies []string
	_, err := runner.Process(context.Background(), ChannelTurnOptions{
		PeerKey:   "peer-1",
		DedupeKey: "ctx-1",
		Message:   "loop",
		LoadState: func() (ChannelConversationState, error) { return state, nil },
		SaveState: func(next ChannelConversationState) error {
			state = next
			return nil
		},
		SendReply: func(_ context.Context, reply string) error {
			replies = append(replies, reply)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Process returned error: %v", err)
	}
	if client.requests != 1 {
		t.Fatalf("requests = %d, want 1", client.requests)
	}
	if len(replies) != 1 || !strings.Contains(replies[0], "reached maximum of 1 iterations") {
		t.Fatalf("replies = %#v, want max iteration reply", replies)
	}
	if state.LastCompletedKey != "ctx-1" {
		t.Fatalf("LastCompletedKey = %q, want ctx-1", state.LastCompletedKey)
	}
}

func TestChannelTurnRunnerStopCancelsRunningProcessGroup(t *testing.T) {
	root := t.TempDir()
	client := &sequenceChatClient{responses: []agent.AssistantMessage{
		{ToolCalls: []agent.ToolCall{{ID: "tool-1", Function: agent.FunctionCall{Name: "execute_bash", Arguments: `{"command":"sleep 60"}`}}}},
		{Content: "should not be used"},
	}}
	catalog := tools.NewCatalog(tools.Options{WorkspaceRoot: root})
	functions := catalog.Registry()
	toolStarted := make(chan struct{})
	toolCanceled := make(chan struct{})
	functions["execute_bash"] = func(ctx context.Context, _ map[string]any) (string, error) {
		close(toolStarted)
		<-ctx.Done()
		close(toolCanceled)
		return "", ctx.Err()
	}
	service := NewService(ServiceOptions{
		WorkspaceRoot:   root,
		Client:          client,
		SystemPrompt:    "You are a helpful assistant. Be concise.",
		ToolDefinitions: catalog.Definitions(),
		Functions:       functions,
	})
	runner := NewChannelTurnRunner(service)

	var mu sync.Mutex
	var state ChannelConversationState
	loadState := func() (ChannelConversationState, error) {
		mu.Lock()
		defer mu.Unlock()
		return state, nil
	}
	saveState := func(next ChannelConversationState) error {
		mu.Lock()
		defer mu.Unlock()
		state = next
		return nil
	}

	firstDone := make(chan error, 1)
	go func() {
		_, err := runner.Process(context.Background(), ChannelTurnOptions{
			PeerKey:   "peer-1",
			DedupeKey: "ctx-1",
			Message:   "run a shell command",
			LoadState: loadState,
			SaveState: saveState,
			SendReply: func(context.Context, string) error { return nil },
		})
		firstDone <- err
	}()

	select {
	case <-toolStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for execute_bash to start")
	}

	var stopReplies []string
	stopState, stopErr := runner.TryProcess(context.Background(), ChannelTurnOptions{
		PeerKey:   "peer-1",
		DedupeKey: "ctx-stop",
		Message:   "/stop",
		LoadState: loadState,
		SaveState: saveState,
		SendReply: func(_ context.Context, reply string) error {
			stopReplies = append(stopReplies, reply)
			return nil
		},
	})
	if stopErr != nil {
		t.Fatalf("/stop returned error: %v", stopErr)
	}
	if len(stopReplies) != 1 || stopReplies[0] != stopCommandStoppedReply {
		t.Fatalf("stop replies = %#v, want stopped reply", stopReplies)
	}
	if stopState.LastCompletedKey != "ctx-stop" {
		t.Fatalf("stop LastCompletedKey = %q, want ctx-stop", stopState.LastCompletedKey)
	}

	select {
	case <-toolCanceled:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for execute_bash context cancellation")
	}
	select {
	case err := <-firstDone:
		if err != nil {
			t.Fatalf("first turn returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first turn to finish")
	}
}

func TestChannelTurnRunnerReleasesPeerLockAfterToolTimeout(t *testing.T) {
	root := t.TempDir()
	client := &sequenceChatClient{responses: []agent.AssistantMessage{
		{ToolCalls: []agent.ToolCall{{ID: "tool-1", Function: agent.FunctionCall{Name: "execute_bash", Arguments: `{"command":"ignored"}`}}}},
		{Content: "second reply"},
	}}
	catalog := tools.NewCatalog(tools.Options{WorkspaceRoot: root})
	functions := catalog.Registry()
	functions["execute_bash"] = func(ctx context.Context, _ map[string]any) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}
	service := NewService(ServiceOptions{
		WorkspaceRoot:   root,
		Client:          client,
		SystemPrompt:    "You are a helpful assistant. Be concise.",
		ToolDefinitions: catalog.Definitions(),
		Functions:       functions,
	})
	runner := NewChannelTurnRunner(service)

	var state ChannelConversationState
	loadState := func() (ChannelConversationState, error) { return state, nil }
	saveState := func(next ChannelConversationState) error {
		state = next
		return nil
	}

	firstCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	firstState, firstErr := runner.Process(firstCtx, ChannelTurnOptions{
		PeerKey:   "peer-1",
		DedupeKey: "ctx-1",
		Message:   "first",
		LoadState: loadState,
		SaveState: saveState,
		SendReply: func(context.Context, string) error { return nil },
	})
	if firstErr != nil {
		t.Fatalf("first Process returned error: %v", firstErr)
	}
	if firstState.LastCompletedKey != "ctx-1" {
		t.Fatalf("first LastCompletedKey = %q, want ctx-1", firstState.LastCompletedKey)
	}
	if !strings.Contains(firstState.LastError, context.DeadlineExceeded.Error()) {
		t.Fatalf("first LastError = %q, want deadline error", firstState.LastError)
	}

	secondCtx, secondCancel := context.WithTimeout(context.Background(), time.Second)
	defer secondCancel()
	var replies []string
	secondState, secondErr := runner.Process(secondCtx, ChannelTurnOptions{
		PeerKey:   "peer-1",
		DedupeKey: "ctx-2",
		Message:   "second",
		LoadState: loadState,
		SaveState: saveState,
		SendReply: func(_ context.Context, reply string) error {
			replies = append(replies, reply)
			return nil
		},
	})
	if secondErr != nil {
		t.Fatalf("second Process returned error: %v", secondErr)
	}
	if len(replies) != 1 || replies[0] != "second reply" {
		t.Fatalf("replies = %#v, want second reply", replies)
	}
	if secondState.LastCompletedKey != "ctx-2" {
		t.Fatalf("LastCompletedKey = %q, want %q", secondState.LastCompletedKey, "ctx-2")
	}
	if secondState.PendingReply != "" {
		t.Fatalf("PendingReply = %q, want empty", secondState.PendingReply)
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
