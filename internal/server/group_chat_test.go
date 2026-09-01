package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"bqagent/internal/agent"
	"bqagent/internal/extagent"
	"bqagent/internal/session"
	"bqagent/internal/tools"
)

type groupTestClient struct {
	mu        sync.Mutex
	responses []agent.AssistantMessage
	messages  [][]map[string]any
}

func (client *groupTestClient) CreateChatCompletion(ctx context.Context, _ string, messages []map[string]any, _ []tools.Definition) (agent.AssistantMessage, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.messages = append(client.messages, messages)
	if err := ctx.Err(); err != nil {
		return agent.AssistantMessage{}, err
	}
	response := client.responses[0]
	client.responses = client.responses[1:]
	return response, nil
}

func (client *groupTestClient) CreateChatCompletionStream(ctx context.Context, model string, messages []map[string]any, definitions []tools.Definition, onChunk func(string)) (agent.AssistantMessage, error) {
	response, err := client.CreateChatCompletion(ctx, model, messages, definitions)
	if err == nil && onChunk != nil {
		onChunk(response.FinalContent())
	}
	return response, err
}

type groupACPLog struct {
	mu      sync.Mutex
	prompts map[string][]string
}

type groupFakeACP struct {
	name string
	log  *groupACPLog
}

type blockingGroupACP struct {
	started   chan struct{}
	startOnce sync.Once
	mu        sync.Mutex
	closed    int
}

func (client *blockingGroupACP) Initialize(context.Context) error { return nil }
func (client *blockingGroupACP) LoadSessionSupported() bool       { return true }
func (client *blockingGroupACP) NewSession(context.Context, string) (string, error) {
	return "blocking-session", nil
}
func (client *blockingGroupACP) LoadSession(_ context.Context, id, _ string) (string, error) {
	return id, nil
}
func (client *blockingGroupACP) Prompt(ctx context.Context, _ string, _ string) (string, error) {
	client.startOnce.Do(func() { close(client.started) })
	<-ctx.Done()
	return "", ctx.Err()
}
func (client *blockingGroupACP) Close() error {
	client.mu.Lock()
	client.closed++
	client.mu.Unlock()
	return nil
}
func (client *blockingGroupACP) closeCount() int {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.closed
}

func (client *groupFakeACP) Initialize(context.Context) error { return nil }
func (client *groupFakeACP) LoadSessionSupported() bool       { return true }
func (client *groupFakeACP) NewSession(context.Context, string) (string, error) {
	return client.name + "-session", nil
}
func (client *groupFakeACP) LoadSession(_ context.Context, id, _ string) (string, error) {
	return id, nil
}
func (client *groupFakeACP) Prompt(_ context.Context, _ string, prompt string) (string, error) {
	client.log.mu.Lock()
	client.log.prompts[client.name] = append(client.log.prompts[client.name], prompt)
	client.log.mu.Unlock()
	return client.name + " 结论", nil
}
func (client *groupFakeACP) Close() error { return nil }

type recordingGroupSink struct{ events []GroupEvent }

func (sink *recordingGroupSink) EmitGroupEvent(event GroupEvent) {
	sink.events = append(sink.events, event)
}

func newGroupTestBroker(root string, log *groupACPLog, names ...extagent.AgentName) *extagent.Broker {
	detections := map[extagent.AgentName]extagent.DetectionResult{}
	for _, name := range names {
		detections[name] = extagent.DetectionResult{Agent: name, Preferred: &extagent.AgentTransport{
			Agent: name, Kind: extagent.TransportACP, Command: extagent.CommandSpec{Command: string(name)},
		}}
	}
	return extagent.NewBroker(extagent.NewStateStore(root), detections, func(spec extagent.CommandSpec, _ string) (extagent.ACPClient, error) {
		return &groupFakeACP{name: spec.Command, log: log}, nil
	})
}

func TestGroupChatExternalMentionsReturnDirectlyWithoutCoordinator(t *testing.T) {
	root := t.TempDir()
	log := &groupACPLog{prompts: map[string][]string{}}
	broker := newGroupTestBroker(root, log, extagent.AgentCodex, extagent.AgentOpenCode)
	defer broker.Close()
	client := &groupTestClient{}
	service := NewService(ServiceOptions{WorkspaceRoot: root, Client: client, SystemPrompt: "base", ExternalBroker: broker})
	sink := &recordingGroupSink{}

	response, err := service.HandleTurnWithOptions(context.Background(), TurnRequest{
		Message: "@codex @opencode 分析这个项目", ConversationType: ConversationTypeGroup,
	}, TurnOptions{GroupEventSink: sink})
	if err != nil {
		t.Fatal(err)
	}
	if response.ConversationType != ConversationTypeGroup || response.ReplyKind != groupReplyKindParticipants {
		t.Fatalf("response = %#v", response)
	}
	if !strings.Contains(response.Reply, "@codex:\ncodex 结论") || !strings.Contains(response.Reply, "@opencode:\nopencode 结论") {
		t.Fatalf("direct reply = %q", response.Reply)
	}
	client.mu.Lock()
	coordinatorCalls := len(client.messages)
	client.mu.Unlock()
	if coordinatorCalls != 0 {
		t.Fatalf("bqagent calls = %d, want 0", coordinatorCalls)
	}
	if len(sink.events) != 4 || sink.events[0].Participant != "codex" || sink.events[2].Participant != "opencode" {
		t.Fatalf("events = %#v", sink.events)
	}
	log.mu.Lock()
	opencodePrompt := log.prompts["opencode"][0]
	log.mu.Unlock()
	if !strings.Contains(opencodePrompt, "@codex: codex 结论") {
		t.Fatalf("opencode prompt does not contain codex conclusion:\n%s", opencodePrompt)
	}

	history, err := service.ConversationHistory(response.SessionID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if history.ConversationType != ConversationTypeGroup || history.Group == nil {
		t.Fatalf("history metadata = %#v", history)
	}
	wantSenders := []string{"user", "codex", "opencode"}
	if len(history.Messages) != len(wantSenders) {
		t.Fatalf("history messages = %#v", history.Messages)
	}
	for index, sender := range wantSenders {
		if history.Messages[index].Sender != sender {
			t.Fatalf("history sender[%d] = %q, want %q", index, history.Messages[index].Sender, sender)
		}
	}
}

func TestGroupChatBqagentMentionLetsCoordinatorConsultAndSummarize(t *testing.T) {
	root := t.TempDir()
	log := &groupACPLog{prompts: map[string][]string{}}
	broker := newGroupTestBroker(root, log, extagent.AgentCodex)
	defer broker.Close()
	client := &groupTestClient{responses: []agent.AssistantMessage{
		{Role: "assistant", ToolCalls: []agent.ToolCall{{ID: "consult-1", Type: "function", Function: agent.FunctionCall{Name: groupConsultTool, Arguments: `{"participant":"codex","task":"检查实现"}`}}}},
		{Role: "assistant", Content: "综合完成"},
	}}
	service := NewService(ServiceOptions{WorkspaceRoot: root, Client: client, SystemPrompt: "base", ExternalBroker: broker})

	response, err := service.HandleTurn(context.Background(), TurnRequest{Message: "@bqagent 分析当前实现", ConversationType: ConversationTypeGroup})
	if err != nil {
		t.Fatal(err)
	}
	if response.Reply != "综合完成" || response.ReplyKind != groupReplyKindCoordinator {
		t.Fatalf("reply = %q", response.Reply)
	}
	log.mu.Lock()
	count := len(log.prompts["codex"])
	log.mu.Unlock()
	if count != 1 {
		t.Fatalf("codex calls = %d, want 1", count)
	}
}

func TestGroupChatWithoutMentionIsHandledDirectlyByBqagent(t *testing.T) {
	root := t.TempDir()
	log := &groupACPLog{prompts: map[string][]string{}}
	broker := newGroupTestBroker(root, log, extagent.AgentCodex)
	defer broker.Close()
	client := &groupTestClient{responses: []agent.AssistantMessage{{Role: "assistant", Content: "bqagent 直接处理完成"}}}
	service := NewService(ServiceOptions{WorkspaceRoot: root, Client: client, SystemPrompt: "base", ExternalBroker: broker})

	response, err := service.HandleTurn(context.Background(), TurnRequest{Message: "分析当前实现", ConversationType: ConversationTypeGroup})
	if err != nil {
		t.Fatal(err)
	}
	if response.Reply != "bqagent 直接处理完成" || response.ReplyKind != groupReplyKindCoordinator {
		t.Fatalf("response = %#v", response)
	}
	client.mu.Lock()
	coordinatorCalls := len(client.messages)
	client.mu.Unlock()
	if coordinatorCalls != 1 {
		t.Fatalf("bqagent calls = %d, want 1", coordinatorCalls)
	}
	log.mu.Lock()
	externalCalls := len(log.prompts["codex"])
	log.mu.Unlock()
	if externalCalls != 0 {
		t.Fatalf("codex calls = %d, want 0", externalCalls)
	}
	coordinator := newGroupTurnCoordinator(service, response.SessionID, session.GroupConfig{Participants: []string{"bqagent", "codex"}}, nil, nil, nil, nil)
	if _, ok := coordinator.toolDefinition(); ok {
		t.Fatal("no-mention turn must not expose external consultation")
	}
}

func TestGroupChatExternalAgentHasExplicitTimeout(t *testing.T) {
	root := t.TempDir()
	external := &blockingGroupACP{started: make(chan struct{})}
	broker := extagent.NewBroker(extagent.NewStateStore(root), map[extagent.AgentName]extagent.DetectionResult{
		extagent.AgentCursor: {Agent: extagent.AgentCursor, Preferred: &extagent.AgentTransport{Agent: extagent.AgentCursor, Kind: extagent.TransportACP, Command: extagent.CommandSpec{Command: "cursor-agent"}}},
	}, func(extagent.CommandSpec, string) (extagent.ACPClient, error) { return external, nil })
	defer broker.Close()
	service := NewService(ServiceOptions{WorkspaceRoot: root, ExternalBroker: broker, GroupExternalAgentTimeout: 20 * time.Millisecond})

	response, err := service.HandleTurn(context.Background(), TurnRequest{Message: "@cursor 检查项目", ConversationType: ConversationTypeGroup})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(response.Reply, "执行超时") || external.closeCount() != 1 {
		t.Fatalf("reply = %q, close count = %d", response.Reply, external.closeCount())
	}
}

func TestStoppingGroupTurnInvalidatesACPSession(t *testing.T) {
	root := t.TempDir()
	external := &blockingGroupACP{started: make(chan struct{})}
	broker := extagent.NewBroker(extagent.NewStateStore(root), map[extagent.AgentName]extagent.DetectionResult{
		extagent.AgentCursor: {Agent: extagent.AgentCursor, Preferred: &extagent.AgentTransport{Agent: extagent.AgentCursor, Kind: extagent.TransportACP, Command: extagent.CommandSpec{Command: "cursor-agent"}}},
	}, func(extagent.CommandSpec, string) (extagent.ACPClient, error) { return external, nil })
	defer broker.Close()
	service := NewService(ServiceOptions{WorkspaceRoot: root, ExternalBroker: broker, GroupExternalAgentTimeout: time.Minute})
	saved, err := service.store.Create(session.CreateOptions{Task: "group", Chat: true, ConversationType: string(ConversationTypeGroup)})
	if err != nil {
		t.Fatal(err)
	}
	if err := saved.SaveGroupConfig(session.GroupConfig{Version: session.GroupConfigVersion, Scheduler: groupScheduler, Participants: []string{groupScheduler, "cursor"}}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, turnErr := service.HandleTurn(context.Background(), TurnRequest{SessionID: saved.ID(), TurnID: "cancel-group-turn", Message: "@cursor 检查项目"})
		done <- turnErr
	}()
	select {
	case <-external.started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for external ACP prompt")
	}
	if !service.StopTurn("cancel-group-turn") {
		t.Fatal("StopTurn reported no active group turn")
	}
	select {
	case turnErr := <-done:
		if !errors.Is(turnErr, context.Canceled) {
			t.Fatalf("turn error = %v, want context.Canceled", turnErr)
		}
	case <-time.After(time.Second):
		t.Fatal("group turn did not stop")
	}
	if external.closeCount() != 1 {
		t.Fatalf("ACP close count = %d, want 1", external.closeCount())
	}
	state, err := extagent.NewStateStore(root).LoadGroup(saved.ID(), extagent.AgentCursor)
	if err != nil || state.ExternalSessionID != "" {
		t.Fatalf("group ACP state after stop = %#v, error = %v", state, err)
	}
}

func TestWebUIMutatesParticipantsInExistingGroup(t *testing.T) {
	root := t.TempDir()
	log := &groupACPLog{prompts: map[string][]string{}}
	broker := newGroupTestBroker(root, log, extagent.AgentCodex, extagent.AgentOpenCode)
	defer broker.Close()
	service := NewService(ServiceOptions{WorkspaceRoot: root, SystemPrompt: "base", ExternalBroker: broker})
	saved, err := service.store.Create(session.CreateOptions{Task: "existing group", Chat: true, ConversationType: string(ConversationTypeGroup)})
	if err != nil {
		t.Fatal(err)
	}
	if err := saved.SaveGroupConfig(session.GroupConfig{Version: session.GroupConfigVersion, Scheduler: groupScheduler, Participants: []string{groupScheduler, "codex"}}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewHandler(HandlerOptions{Service: service, Channels: []Channel{NewWebUIChannel(service, true)}}))
	defer server.Close()

	requestBody := `{"session_id":"` + saved.ID() + `","participant":"opencode"}`
	response, err := http.Post(server.URL+"/api/v1/webui/group/participants", "application/json", strings.NewReader(requestBody))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), `"id":"opencode"`) {
		t.Fatalf("add participant status=%d body=%s", response.StatusCode, body)
	}
	config, err := saved.LoadGroupConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Participants) != 3 || config.Participants[2] != "opencode" {
		t.Fatalf("participants = %#v", config.Participants)
	}

	response, err = http.Post(server.URL+"/api/v1/webui/group/participants", "application/json", strings.NewReader(requestBody))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("idempotent add status = %d", response.StatusCode)
	}
	config, _ = saved.LoadGroupConfig()
	if len(config.Participants) != 3 {
		t.Fatalf("duplicate participant was persisted: %#v", config.Participants)
	}

	response, err = http.Post(server.URL+"/api/v1/webui/group/participants", "application/json", strings.NewReader(`{"session_id":"`+saved.ID()+`","participant":"cursor"}`))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("unavailable participant status = %d", response.StatusCode)
	}

	deleteRequest, err := http.NewRequest(http.MethodDelete, server.URL+"/api/v1/webui/group/participants", strings.NewReader(requestBody))
	if err != nil {
		t.Fatal(err)
	}
	deleteRequest.Header.Set("Content-Type", "application/json")
	response, err = http.DefaultClient.Do(deleteRequest)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || strings.Contains(string(body), `"id":"opencode"`) {
		t.Fatalf("remove participant status=%d body=%s", response.StatusCode, body)
	}
	config, _ = saved.LoadGroupConfig()
	if len(config.Participants) != 2 || config.Participants[1] != "codex" {
		t.Fatalf("participants after remove = %#v", config.Participants)
	}

	deleteRequest, _ = http.NewRequest(http.MethodDelete, server.URL+"/api/v1/webui/group/participants", strings.NewReader(`{"session_id":"`+saved.ID()+`","participant":"bqagent"}`))
	deleteRequest.Header.Set("Content-Type", "application/json")
	response, err = http.DefaultClient.Do(deleteRequest)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("remove scheduler status = %d", response.StatusCode)
	}
}

func TestGroupChatRejectsAskMode(t *testing.T) {
	service := NewService(ServiceOptions{WorkspaceRoot: t.TempDir(), SystemPrompt: "base"})
	_, err := service.HandleTurn(context.Background(), TurnRequest{Message: "只读分析", ConversationType: ConversationTypeGroup, Mode: ChatModeAsk})
	if err == nil || !strings.Contains(err.Error(), "仅支持 Run") {
		t.Fatalf("error = %v", err)
	}
}

func TestWebUIGroupParticipantsAndSSE(t *testing.T) {
	root := t.TempDir()
	log := &groupACPLog{prompts: map[string][]string{}}
	broker := newGroupTestBroker(root, log, extagent.AgentCodex)
	defer broker.Close()
	client := &groupTestClient{}
	service := NewService(ServiceOptions{WorkspaceRoot: root, Client: client, SystemPrompt: "base", ExternalBroker: broker})
	server := httptest.NewServer(NewHandler(HandlerOptions{Service: service, Channels: []Channel{NewWebUIChannel(service, true)}}))
	defer server.Close()

	participantsResponse, err := http.Get(server.URL + "/api/v1/webui/group/participants")
	if err != nil {
		t.Fatal(err)
	}
	participantsBody, _ := io.ReadAll(participantsResponse.Body)
	participantsResponse.Body.Close()
	if participantsResponse.StatusCode != http.StatusOK || !strings.Contains(string(participantsBody), `"id":"codex"`) {
		t.Fatalf("participants status=%d body=%s", participantsResponse.StatusCode, participantsBody)
	}

	response, err := http.Post(server.URL+"/api/v1/webui/chat", "application/json", strings.NewReader(`{"message":"@codex 检查","conversation_type":"group","turn_id":"group-http-turn"}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	text := string(body)
	if response.StatusCode != http.StatusOK || !strings.Contains(text, "event: participant_start") || !strings.Contains(text, `"conversation_type":"group"`) || !strings.Contains(text, `"reply_kind":"participant_results"`) {
		t.Fatalf("SSE status=%d body=%s", response.StatusCode, text)
	}
}
