package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"bqagent/internal/extagent"
)

type singleModelClient struct{ groupFakeACP }

func TestWebUIAgentModelCapabilitiesUseSavedTransportWithoutStartingAnAgent(t *testing.T) {
	root := t.TempDir()
	store := extagent.NewStateStore(root)
	const id = "saved-chat"
	acp := &extagent.AgentTransport{Agent: extagent.AgentCodex, Kind: extagent.TransportACP}
	native := &extagent.AgentTransport{Agent: extagent.AgentCodex, Kind: extagent.TransportAppServer}
	broker := extagent.NewBroker(store, map[extagent.AgentName]extagent.DetectionResult{
		extagent.AgentCodex: {Agent: extagent.AgentCodex, Preferred: native, ACP: acp, AppServer: native},
	}, func(context.Context, extagent.CommandSpec, string) (extagent.ACPClient, error) {
		t.Fatal("capability discovery started the adapter")
		return nil, nil
	})
	defer broker.Close()
	if err := store.Save(extagent.SessionState{BQSessionID: id, Agent: extagent.AgentCodex, Transport: extagent.TransportACP,
		ExternalSessionID: "original", Model: "saved", Effort: "high"}); err != nil {
		t.Fatal(err)
	}
	ch := NewWebUIChannel(NewService(ServiceOptions{WorkspaceRoot: root, ExternalBroker: broker}), true)
	for _, tc := range []struct {
		session, model, effort string
		custom                 bool
	}{{id, "saved", "high", false}, {"new-chat", "", "", true}} {
		w := httptest.NewRecorder()
		ch.handleAgentModels(w, httptest.NewRequest(http.MethodGet, "/api/v1/webui/agent-models?view=capabilities&agent=codex&session_id="+tc.session, nil))
		var got extagent.ModelCatalog
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.AllowCustom != tc.custom ||
			got.Selected != tc.model || got.SelectedEffort != tc.effort || len(got.Models) != 0 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}

func (c *singleModelClient) PromptWithModel(_ context.Context, _, prompt, model string) (string, error) {
	if model == "invalid" {
		return "", fmt.Errorf("invalid model")
	}
	return c.name + "/" + model + ": " + prompt, nil
}

func (c *singleModelClient) PromptWithOptions(ctx context.Context, id, prompt string, opts extagent.PromptOptions) (string, error) {
	if opts.Effort == "unsupported" {
		return "", fmt.Errorf("invalid effort")
	}
	reply, err := c.PromptWithModel(ctx, id, prompt, opts.Model)
	if err == nil && opts.Effort != "" {
		reply += " (" + opts.Effort + ")"
	}
	return reply, err
}

func TestWebUISingleAgentModelsPersistOnlyForTheReceivingAgent(t *testing.T) {
	root := t.TempDir()
	detections := map[extagent.AgentName]extagent.DetectionResult{}
	for _, name := range []extagent.AgentName{extagent.AgentPi, extagent.AgentCodex} {
		detections[name] = extagent.DetectionResult{Agent: name, Preferred: &extagent.AgentTransport{Agent: name, Kind: extagent.TransportACP, Command: extagent.CommandSpec{Command: string(name)}}}
	}
	log := &groupACPLog{prompts: map[string][]string{}}
	broker := extagent.NewBroker(extagent.NewStateStore(root), detections, func(_ context.Context, spec extagent.CommandSpec, _ string) (extagent.ACPClient, error) {
		return &singleModelClient{groupFakeACP{name: spec.Command, log: log}}, nil
	})
	defer broker.Close()
	channel := NewWebUIChannel(NewService(ServiceOptions{WorkspaceRoot: root, ExternalBroker: broker}), true)
	id := ""
	for _, tc := range []struct{ agent, model, effort, prompt, reply, confirmedEffort string }{
		{"pi", "p/one", "xhigh", "first", "pi/p/one: first (xhigh)", "xhigh"},
		{"pi", "p/two", "", "second", "pi/p/two: second", ""},
		{"pi", "", "minimal", "tune", "pi/p/two: tune (minimal)", "minimal"},
		{"pi", "p/two", "minimal", "/codex separate", "codex 结论", ""},
		{"codex", "alternate", "", "/codex separate", "codex/alternate: separate", ""},
		{"pi", "", "", "return to Pi", "pi/p/two: return to Pi (minimal)", "minimal"},
		{"pi", "invalid", "", "reject", "", ""},
		{"pi", "p/two", "unsupported", "reject effort", "", ""},
		{"pi", "", "", "resume", "pi/p/two: resume (minimal)", "minimal"},
	} {
		data, _ := json.Marshal(map[string]string{"session_id": id, "agent": tc.agent, "agent_model": tc.model, "agent_effort": tc.effort, "message": tc.prompt})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/webui/chat", strings.NewReader(string(data)))
		req.Header.Set("Content-Type", "application/json")
		out := httptest.NewRecorder()
		channel.handleStreamChat(out, req)
		_, start, _ := strings.Cut(out.Body.String(), "event: turn_start\ndata: ")
		start, _, _ = strings.Cut(start, "\n")
		var replay webUIChatRequest
		if json.Unmarshal([]byte(start), &replay) != nil || string(replay.Agent) != tc.agent || replay.AgentModel != tc.model || replay.AgentEffort != tc.effort {
			t.Fatalf("turn %q: replayed settings = %s", tc.prompt, start)
		}
		if tc.reply == "" {
			if !strings.Contains(out.Body.String(), "event: error") || !strings.Contains(out.Body.String(), "invalid ") {
				t.Fatal(out.Body.String())
			}
			continue
		}
		_, tail, _ := strings.Cut(out.Body.String(), "event: done\ndata: ")
		var done webUIDoneEvent
		if json.Unmarshal([]byte(tail), &done) != nil || done.Reply != tc.reply || done.ReplyAgentEffort != tc.confirmedEffort || done.SessionID == "" || (id != "" && id != done.SessionID) {
			t.Fatalf("turn %q: %s", tc.prompt, out.Body.String())
		}
		id = done.SessionID
	}
}

func TestWebUISingleAgentSelectionRoutesMessagesAndRestoresHistory(t *testing.T) {
	root := t.TempDir()
	log := &groupACPLog{prompts: map[string][]string{}}
	broker := newGroupTestBroker(root, log, extagent.AgentCodex, extagent.AgentPi)
	defer broker.Close()
	client := &chatModeRecordingClient{}
	service := NewService(ServiceOptions{WorkspaceRoot: root, SystemPrompt: "test", ExternalBroker: broker, Client: client})
	channel := NewWebUIChannel(service, true)
	sessionID := ""
	for _, tc := range []struct{ agent, message, wantSender string }{
		{"codex", "first question", "codex"},
		{"", "follow up", "codex"},
		{"pi", "second opinion", "pi"},
		{"bqagent", "answer this directly", ""},
		{"bqagent", "/pi typed route wins", "pi"},
		{"codex", "back to codex", "codex"},
		{"codex", "/default", ""},
	} {
		data, _ := json.Marshal(map[string]string{"message": tc.message, "agent": tc.agent, "session_id": sessionID})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/webui/chat", strings.NewReader(string(data)))
		req.Header.Set("Content-Type", "application/json")
		out := httptest.NewRecorder()
		channel.handleStreamChat(out, req)
		_, tail, found := strings.Cut(out.Body.String(), "event: done\ndata: ")
		var done webUIDoneEvent
		if !found || json.Unmarshal([]byte(strings.TrimSpace(tail)), &done) != nil {
			t.Fatalf("turn %q: missing done event in %s", tc.message, out.Body.String())
		}
		if done.SessionID == "" || (sessionID != "" && sessionID != done.SessionID) ||
			string(done.ActiveAgent) != tc.wantSender || string(done.ReplyAgent) != tc.wantSender {
			t.Fatalf("turn %q: unexpected responder or session: %+v", tc.message, done)
		}
		sessionID = done.SessionID
		history, err := service.ConversationHistory(sessionID, 0)
		if err != nil {
			t.Fatal(err)
		}
		last := history.Messages[len(history.Messages)-2:]
		if last[0].Content != tc.message || last[1].Sender != tc.wantSender || string(history.ActiveAgent) != tc.wantSender {
			t.Fatalf("turn %q: restored history = %+v (active %q)", tc.message, last, history.ActiveAgent)
		}
	}
	if len(client.messages) != 1 || !strings.Contains(messageText(client.messages[0][len(client.messages[0])-1]), "answer this directly") {
		t.Fatalf("built-in should answer the switch-back message in the same turn")
	}
	if got := strings.Join(log.prompts["pi"], "|"); got != "second opinion|typed route wins" {
		t.Fatalf("Pi prompts = %q", got)
	}
	if got := strings.Join(log.prompts["codex"], "|"); got != "first question|follow up|back to codex" {
		t.Fatalf("Codex prompts = %q", got)
	}
}

func TestSingleChatAgentSelectionRejectsInvalidRoutes(t *testing.T) {
	root := t.TempDir()
	log := &groupACPLog{prompts: map[string][]string{}}
	broker := newGroupTestBroker(root, log, extagent.AgentCodex)
	defer broker.Close()
	client := &chatModeRecordingClient{}
	service := NewService(ServiceOptions{WorkspaceRoot: root, ExternalBroker: broker, Client: client})
	for _, tc := range []struct {
		agent        extagent.AgentName
		mode         ChatMode
		conversation ConversationType
		want         string
	}{
		{extagent.AgentCodex, ChatModeAsk, ConversationTypeDefault, "Ask 模式"},
		{extagent.AgentCodex, ChatModeRun, ConversationTypeGroup, "群聊"},
		{"unknown", ChatModeRun, ConversationTypeDefault, "未知"},
		{extagent.AgentPi, ChatModeRun, ConversationTypeDefault, "unavailable"},
	} {
		_, err := service.HandleTurn(context.Background(), TurnRequest{Agent: tc.agent, Message: "hi", Mode: tc.mode, ConversationType: tc.conversation})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("agent %s, mode %s, conversation %s: error = %v, want %q", tc.agent, tc.mode, tc.conversation, err, tc.want)
		}
	}
	if len(log.prompts) != 0 || len(client.messages) != 0 {
		t.Fatalf("invalid routes must not execute an agent")
	}
}
