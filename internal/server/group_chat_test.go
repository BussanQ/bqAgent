package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"bqagent/internal/agent"
	"bqagent/internal/extagent"
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

func TestGroupChatExplicitMentionsShareConclusionsAndHistory(t *testing.T) {
	root := t.TempDir()
	log := &groupACPLog{prompts: map[string][]string{}}
	broker := newGroupTestBroker(root, log, extagent.AgentCodex, extagent.AgentOpenCode)
	defer broker.Close()
	client := &groupTestClient{responses: []agent.AssistantMessage{{Role: "assistant", Content: "bqagent 汇总结论"}}}
	service := NewService(ServiceOptions{WorkspaceRoot: root, Client: client, SystemPrompt: "base", ExternalBroker: broker})
	sink := &recordingGroupSink{}

	response, err := service.HandleTurnWithOptions(context.Background(), TurnRequest{
		Message: "@codex @opencode 分析这个项目", ConversationType: ConversationTypeGroup,
	}, TurnOptions{GroupEventSink: sink})
	if err != nil {
		t.Fatal(err)
	}
	if response.ConversationType != ConversationTypeGroup || response.Reply != "bqagent 汇总结论" {
		t.Fatalf("response = %#v", response)
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
	wantSenders := []string{"user", "codex", "opencode", "bqagent"}
	if len(history.Messages) != len(wantSenders) {
		t.Fatalf("history messages = %#v", history.Messages)
	}
	for index, sender := range wantSenders {
		if history.Messages[index].Sender != sender {
			t.Fatalf("history sender[%d] = %q, want %q", index, history.Messages[index].Sender, sender)
		}
	}
}

func TestGroupChatWithoutMentionsLetsCoordinatorConsult(t *testing.T) {
	root := t.TempDir()
	log := &groupACPLog{prompts: map[string][]string{}}
	broker := newGroupTestBroker(root, log, extagent.AgentCodex)
	defer broker.Close()
	client := &groupTestClient{responses: []agent.AssistantMessage{
		{Role: "assistant", ToolCalls: []agent.ToolCall{{ID: "consult-1", Type: "function", Function: agent.FunctionCall{Name: groupConsultTool, Arguments: `{"participant":"codex","task":"检查实现"}`}}}},
		{Role: "assistant", Content: "综合完成"},
	}}
	service := NewService(ServiceOptions{WorkspaceRoot: root, Client: client, SystemPrompt: "base", ExternalBroker: broker})

	response, err := service.HandleTurn(context.Background(), TurnRequest{Message: "分析当前实现", ConversationType: ConversationTypeGroup})
	if err != nil {
		t.Fatal(err)
	}
	if response.Reply != "综合完成" {
		t.Fatalf("reply = %q", response.Reply)
	}
	log.mu.Lock()
	count := len(log.prompts["codex"])
	log.mu.Unlock()
	if count != 1 {
		t.Fatalf("codex calls = %d, want 1", count)
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
	client := &groupTestClient{responses: []agent.AssistantMessage{{Role: "assistant", Content: "最终汇总"}}}
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
	if response.StatusCode != http.StatusOK || !strings.Contains(text, "event: participant_start") || !strings.Contains(text, `"conversation_type":"group"`) {
		t.Fatalf("SSE status=%d body=%s", response.StatusCode, text)
	}
}
