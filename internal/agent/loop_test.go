package agent

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"bqagent/internal/tools"
)

type stubClient struct {
	messages  [][]map[string]any
	responses []AssistantMessage
}

func (s *stubClient) CreateChatCompletion(_ context.Context, _ string, messages []map[string]any, _ []tools.Definition) (AssistantMessage, error) {
	s.messages = append(s.messages, cloneMessages(messages))
	if len(s.responses) == 0 {
		return AssistantMessage{}, errors.New("no response configured")
	}
	response := s.responses[0]
	s.responses = s.responses[1:]
	return response, nil
}

func cloneMessages(messages []map[string]any) []map[string]any {
	cloned := make([]map[string]any, len(messages))
	for index, message := range messages {
		copyMessage := make(map[string]any, len(message))
		for key, value := range message {
			copyMessage[key] = value
		}
		cloned[index] = copyMessage
	}
	return cloned
}

func extractToolMessages(messages []map[string]any) []map[string]any {
	toolMessages := make([]map[string]any, 0)
	for _, message := range messages {
		role, _ := message["role"].(string)
		if role == "tool" {
			toolMessages = append(toolMessages, message)
		}
	}
	return toolMessages
}

func TestRunReturnsUnknownToolErrorToLoop(t *testing.T) {
	client := &stubClient{
		responses: []AssistantMessage{
			{
				Content: "",
				ToolCalls: []ToolCall{
					{
						ID: "tc-1",
						Function: FunctionCall{
							Name:      "missing_tool",
							Arguments: "{}",
						},
					},
				},
			},
			{Content: "done"},
		},
	}

	var logs bytes.Buffer
	app := New(client, "", &logs)

	result, err := app.Run(context.Background(), "test unknown tool", 2)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result != "done" {
		t.Fatalf("Run returned %q, want %q", result, "done")
	}
	if len(client.messages) != 2 {
		t.Fatalf("client saw %d requests, want 2", len(client.messages))
	}

	toolMessages := extractToolMessages(client.messages[1])
	if len(toolMessages) != 1 {
		t.Fatalf("saw %d tool messages, want 1", len(toolMessages))
	}
	if content, _ := toolMessages[0]["content"].(string); content != "Error: Unknown tool 'missing_tool'" {
		t.Fatalf("tool content = %q, want unknown tool error", content)
	}
	if !strings.Contains(logs.String(), "[Tool] missing_tool(map[])") {
		t.Fatalf("logs did not include tool call: %q", logs.String())
	}
}

func TestRunReturnsErrorForMalformedArguments(t *testing.T) {
	client := &stubClient{
		responses: []AssistantMessage{
			{
				Content: "",
				ToolCalls: []ToolCall{
					{
						ID: "tc-1",
						Function: FunctionCall{
							Name:      "read_file",
							Arguments: "{\"path\":",
						},
					},
				},
			},
		},
	}

	app := New(client, "", &bytes.Buffer{})

	_, err := app.Run(context.Background(), "test invalid args", 2)
	if err == nil {
		t.Fatal("Run returned nil error, want malformed JSON error")
	}
	if !strings.Contains(err.Error(), "unexpected end of JSON input") {
		t.Fatalf("Run error = %q, want malformed JSON detail", err.Error())
	}
	if len(client.messages) != 1 {
		t.Fatalf("client saw %d requests, want 1", len(client.messages))
	}
}

func TestRunReturnsAssistantContentWithoutToolCalls(t *testing.T) {
	client := &stubClient{responses: []AssistantMessage{{Content: "done"}}}
	app := New(client, "", &bytes.Buffer{})

	result, err := app.Run(context.Background(), "hello", 5)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result != "done" {
		t.Fatalf("Run returned %q, want %q", result, "done")
	}
}

func TestRunReturnsMaxIterationsReached(t *testing.T) {
	client := &stubClient{
		responses: []AssistantMessage{
			{
				ToolCalls: []ToolCall{
					{
						ID:       "tc-1",
						Function: FunctionCall{Name: "missing_tool", Arguments: "{}"},
					},
				},
			},
		},
	}
	app := New(client, "", &bytes.Buffer{})

	result, err := app.Run(context.Background(), "hello", 1)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result != "Max iterations reached" {
		t.Fatalf("Run returned %q, want %q", result, "Max iterations reached")
	}
}
