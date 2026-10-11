package server

import (
	"context"
	"strings"
	"testing"

	"bqagent/internal/agent"
	"bqagent/internal/extagent"
)

func TestGroupPiUsesSharedMembershipHistoryAndCoordinator(t *testing.T) {
	root := t.TempDir()
	log := &groupACPLog{prompts: map[string][]string{}}
	broker := newGroupTestBroker(root, log, extagent.AgentPi)
	defer broker.Close()
	client := &groupTestClient{responses: []agent.AssistantMessage{
		{Role: "assistant", ToolCalls: []agent.ToolCall{{ID: "consult-pi", Type: "function", Function: agent.FunctionCall{Name: groupConsultTool, Arguments: `{"participant":"pi","task":"review again"}`}}}},
		{Role: "assistant", Content: "combined result"},
	}}
	service := NewService(ServiceOptions{WorkspaceRoot: root, Client: client, SystemPrompt: "base", ExternalBroker: broker})
	participants := service.GroupParticipants()
	if len(participants) != 2 || participants[1].ID != "pi" || !participants[1].Available {
		t.Fatalf("participants = %+v", participants)
	}
	sink := &recordingGroupSink{}
	response, err := service.HandleTurnWithOptions(context.Background(), TurnRequest{Message: "@pi inspect project", ConversationType: ConversationTypeGroup}, TurnOptions{GroupEventSink: sink})
	if err != nil || !strings.Contains(response.Reply, "@pi:") || response.ReplyKind != groupReplyKindParticipants {
		t.Fatalf("direct Pi turn = %+v, err = %v", response, err)
	}
	if len(client.messages) != 0 || len(sink.events) != 2 || sink.events[1].Participant != "pi" {
		t.Fatalf("direct turn used coordinator or omitted events: %+v", sink.events)
	}
	history, err := service.ConversationHistory(response.SessionID, 0)
	if err != nil || len(history.Messages) != 2 || history.Messages[1].Sender != "pi" {
		t.Fatalf("history = %+v, err = %v", history, err)
	}
	info, err := service.RemoveGroupParticipant(response.SessionID, "pi")
	if err != nil || len(info.Participants) != 1 {
		t.Fatalf("remove Pi = %+v, err = %v", info, err)
	}
	info, err = service.AddGroupParticipant(response.SessionID, "pi")
	if err != nil || len(info.Participants) != 2 {
		t.Fatalf("add Pi = %+v, err = %v", info, err)
	}
	info, err = service.GroupInfoForSession(response.SessionID)
	if err != nil || len(info.Participants) != 2 || info.Participants[1].ID != "pi" {
		t.Fatalf("persisted membership = %+v, err = %v", info, err)
	}
	coordinated, err := service.HandleTurn(context.Background(), TurnRequest{SessionID: response.SessionID, Message: "@bqagent review with pi"})
	if err != nil || coordinated.Reply != "combined result" {
		t.Fatalf("coordinated turn = %+v, err = %v", coordinated, err)
	}
	if len(log.prompts["pi"]) != 2 || !strings.Contains(log.prompts["pi"][1], "@pi:") {
		t.Fatalf("Pi did not receive shared history: %+v", log.prompts)
	}
}

func TestExternalRouteGuardsUseSharedAgentRegistry(t *testing.T) {
	for _, name := range extagent.SupportedAgents() {
		for _, message := range []string{"/" + string(name), "/" + strings.ToUpper(string(name)) + " task"} {
			if !askModeBlockedCommand(message) || !isExternalRouteCommand(message) {
				t.Fatalf("external route %q bypassed shared guards", message)
			}
		}
	}
	if askModeBlockedCommand("/model") || isExternalRouteCommand("/model") {
		t.Fatal("unrelated command treated as an external agent")
	}
}

func TestSharedGroupContextPreservesRawAndCompactedMemberResults(t *testing.T) {
	call := agent.AssistantMessage{Role: "assistant", ToolCalls: []agent.ToolCall{{ID: "raw", Type: "function", Function: agent.FunctionCall{Name: groupConsultTool, Arguments: `{"participant":"pi"}`}}}}.RequestMessage()
	folded := agent.CompletedToolActivityLead + "\n" +
		`Calls: [{"id":"compact","function":{"name":"consult_group_agent","arguments":"{\"participant\":\"codex\"}"}}]` + "\nResult compact:\nGroup participant @codex failed: unavailable"
	text := sharedGroupContext([]map[string]any{
		call,
		{"role": "tool", "tool_call_id": "raw", "content": "Group participant @pi replied:\nprior conclusion"},
		{"role": "assistant", "content": folded},
		{"role": "tool", "tool_call_id": "unrelated", "content": "unrelated tool output"},
	}, nil)
	if !strings.Contains(text, "@pi: prior conclusion") || !strings.Contains(text, "@codex failed: unavailable") || strings.Contains(text, "@bqagent:") || strings.Contains(text, "unrelated tool output") {
		t.Fatalf("shared context lost attribution or leaked tools: %s", text)
	}
}
