package agent

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"bqagent/internal/tools"
)

func TestRunPlannedConversationExecutesPlannerSteps(t *testing.T) {
	client := &stubClient{
		responses: []AssistantMessage{
			{Content: `{"steps":["read file","summarize result"]}`},
			{Content: "read done"},
			{Content: "summary done"},
		},
	}

	catalog := tools.NewCatalog(tools.Options{IncludePlan: true})
	var logs bytes.Buffer
	app := NewWithOptions(client, "", Options{
		LogWriter:       &logs,
		Planner:         NewPlanner(client, ""),
		ToolDefinitions: catalog.Definitions(),
		Functions:       catalog.Registry(),
	})

	result, err := app.RunPlannedConversation(context.Background(), []map[string]any{{"role": "system", "content": "system"}}, "finish task", 5)
	if err != nil {
		t.Fatalf("RunPlannedConversation returned error: %v", err)
	}
	if result != "read done\nsummary done" {
		t.Fatalf("result = %q, want joined step results", result)
	}
	if len(client.messages) != 3 {
		t.Fatalf("client saw %d requests, want 3", len(client.messages))
	}
	if client.messages[1][1]["content"] != "read file" {
		t.Fatalf("first step content = %#v, want %#v", client.messages[1][1]["content"], "read file")
	}
	lastMessage := client.messages[2][len(client.messages[2])-1]
	if lastMessage["content"] != "summarize result" {
		t.Fatalf("last message content = %#v, want %#v", lastMessage["content"], "summarize result")
	}
	if !strings.Contains(logs.String(), "[Plan] Created 2 steps") {
		t.Fatalf("logs = %q, want plan creation log", logs.String())
	}
}

func TestRunConversationHandlesPlanTool(t *testing.T) {
	client := &stubClient{
		responses: []AssistantMessage{
			{
				ToolCalls: []ToolCall{{
					ID: "plan-1",
					Function: FunctionCall{Name: "plan", Arguments: `{"task":"inspect repo"}`},
				}},
			},
			{Content: `{"steps":["inspect README"]}`},
			{Content: "done"},
		},
	}

	catalog := tools.NewCatalog(tools.Options{IncludePlan: true})
	app := NewWithOptions(client, "", Options{
		Planner:         NewPlanner(client, ""),
		ToolDefinitions: catalog.Definitions(),
		Functions:       catalog.Registry(),
	})

	result, err := app.RunConversation(context.Background(), []map[string]any{{"role": "system", "content": "system"}, {"role": "user", "content": "start"}}, 5)
	if err != nil {
		t.Fatalf("RunConversation returned error: %v", err)
	}
	if result != "done" {
		t.Fatalf("result = %q, want %q", result, "done")
	}
	if len(client.messages) != 3 {
		t.Fatalf("client saw %d requests, want 3", len(client.messages))
	}
	toolMessages := extractToolMessages(client.messages[2])
	if len(toolMessages) != 1 {
		t.Fatalf("tool messages = %d, want 1", len(toolMessages))
	}
	if toolMessages[0]["content"] != "Plan created with 1 steps. Executing now..." {
		t.Fatalf("tool message content = %#v, want plan execution note", toolMessages[0]["content"])
	}
}

func TestRunPlannedConversationPlannerFailure(t *testing.T) {
	// Planner Generate gets invalid JSON → returns parse error
	client := &stubClient{
		responses: []AssistantMessage{
			{Content: "not valid json at all"},
		},
	}

	app := NewWithOptions(client, "", Options{
		Planner: NewPlanner(client, ""),
	})

	_, err := app.RunPlannedConversation(context.Background(), []map[string]any{{"role": "system", "content": "sys"}}, "do task", 5)
	if err == nil {
		t.Fatal("RunPlannedConversation returned nil error, want planner parse error")
	}
}

func TestRunPlannedConversationNoSteps(t *testing.T) {
	client := &stubClient{
		responses: []AssistantMessage{
			{Content: `{"steps":[]}`},
		},
	}

	app := NewWithOptions(client, "", Options{
		Planner: NewPlanner(client, ""),
	})

	_, err := app.RunPlannedConversation(context.Background(), []map[string]any{{"role": "system", "content": "sys"}}, "do task", 5)
	if err == nil {
		t.Fatal("RunPlannedConversation returned nil error, want no steps error")
	}
	if !strings.Contains(err.Error(), "no steps") {
		t.Fatalf("error = %q, want 'no steps' message", err.Error())
	}
}
