package agent

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bqagent/internal/tools"
)

type stubClient struct {
	messages        [][]map[string]any
	responses       []AssistantMessage
	optionMessages  [][]map[string]any
	optionResponses []AssistantMessage
	optionErrors    []error
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

func (s *stubClient) CreateChatCompletionStream(_ context.Context, _ string, messages []map[string]any, _ []tools.Definition, _ func(string)) (AssistantMessage, error) {
	return s.CreateChatCompletion(context.Background(), "", messages, nil)
}

func (s *stubClient) CreateChatCompletionWithOptions(ctx context.Context, _ string, messages []map[string]any, _ []tools.Definition, _ ChatCompletionOptions) (AssistantMessage, error) {
	s.optionMessages = append(s.optionMessages, cloneMessages(messages))
	if len(s.optionErrors) > 0 {
		err := s.optionErrors[0]
		s.optionErrors = s.optionErrors[1:]
		if err != nil {
			return AssistantMessage{}, err
		}
	}
	if len(s.optionResponses) > 0 {
		response := s.optionResponses[0]
		s.optionResponses = s.optionResponses[1:]
		return response, nil
	}
	return s.CreateChatCompletion(ctx, "", messages, nil)
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
	if !strings.Contains(logs.String(), "[Tool] name=missing_tool") || !strings.Contains(logs.String(), "status=error") {
		t.Fatalf("logs did not include tool timing: %q", logs.String())
	}
	if !strings.Contains(logs.String(), "[Turn] iterations=2 allow_plan=false") {
		t.Fatalf("logs did not include turn timing: %q", logs.String())
	}
}

func TestRunReturnsMalformedArgumentsAsToolError(t *testing.T) {
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
			{Content: "recovered"},
		},
	}

	var logs bytes.Buffer
	app := New(client, "", &logs)

	result, err := app.Run(context.Background(), "test invalid args", 2)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result != "recovered" {
		t.Fatalf("Run returned %q, want %q", result, "recovered")
	}
	if len(client.messages) != 2 {
		t.Fatalf("client saw %d requests, want 2", len(client.messages))
	}
	toolMessages := extractToolMessages(client.messages[1])
	if len(toolMessages) != 1 {
		t.Fatalf("saw %d tool messages, want 1", len(toolMessages))
	}
	content, _ := toolMessages[0]["content"].(string)
	if !strings.Contains(content, `Error: Invalid JSON arguments for tool "read_file"`) {
		t.Fatalf("tool content = %q, want malformed JSON tool error", content)
	}
	if strings.Contains(logs.String(), "[Tool] name=read_file") {
		t.Fatalf("logs = %q, did not want tool timing for invalid JSON", logs.String())
	}
	if !strings.Contains(logs.String(), "[Turn] iterations=2 allow_plan=false") {
		t.Fatalf("logs did not include turn timing: %q", logs.String())
	}
}

func TestRunReturnsAssistantContentWithoutToolCalls(t *testing.T) {
	client := &stubClient{responses: []AssistantMessage{{Content: "done"}}}
	var logs bytes.Buffer
	app := NewWithOptions(client, "", Options{LogWriter: &logs, Context: ContextConfig{Enabled: false}})

	result, err := app.Run(context.Background(), "hello", 5)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result != "done" {
		t.Fatalf("Run returned %q, want %q", result, "done")
	}
	if !strings.Contains(logs.String(), "[Turn] iterations=1 allow_plan=false") {
		t.Fatalf("logs did not include turn timing: %q", logs.String())
	}
}

func TestRunExecutesInlineToolCallContent(t *testing.T) {
	client := &stubClient{
		responses: []AssistantMessage{
			{
				Content: "<think>用户让我用 web_search 搜索天气。我来调用这个工具。</think>\n\n<tool_call>\n{\"name\":\"read_file\",\"parameters\":{\"path\":\"README.md\"}}\n</tool_call>",
			},
			{Content: "done"},
		},
	}
	var logs bytes.Buffer
	app := New(client, "", &logs)

	result, err := app.Run(context.Background(), "search", 3)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result != "done" {
		t.Fatalf("Run returned %q, want %q", result, "done")
	}
	if len(client.messages) != 2 {
		t.Fatalf("client saw %d requests, want 2", len(client.messages))
	}
	assistantMessage := client.messages[1][2]
	if content := assistantMessage["content"]; content != nil {
		t.Fatalf("assistant content = %#v, want nil for tool call follow-up", content)
	}
	if _, ok := assistantMessage["tool_calls"]; !ok {
		t.Fatal("assistant message missing tool_calls in follow-up request")
	}
	toolMessages := extractToolMessages(client.messages[1])
	if len(toolMessages) != 1 {
		t.Fatalf("saw %d tool messages, want 1", len(toolMessages))
	}
	content, _ := toolMessages[0]["content"].(string)
	if !strings.Contains(content, "Error:") || !strings.Contains(content, "README.md") {
		t.Fatalf("tool content = %q, want executed inline tool result", content)
	}
	if !strings.Contains(logs.String(), "[Tool] read_file(map[path:README.md])") {
		t.Fatalf("logs did not include inline tool call: %q", logs.String())
	}
}

func TestRunExecutesShorthandInlineToolCallContent(t *testing.T) {
	client := &stubClient{
		responses: []AssistantMessage{
			{
				Content: `<tool_call>read_file path="README.md"</tool_call>`,
			},
			{Content: "done"},
		},
	}
	var logs bytes.Buffer
	app := New(client, "", &logs)

	result, err := app.Run(context.Background(), "search", 3)
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
	content, _ := toolMessages[0]["content"].(string)
	if !strings.Contains(content, "README.md") {
		t.Fatalf("tool content = %q, want executed shorthand inline tool result", content)
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
	var logs bytes.Buffer
	app := NewWithOptions(client, "", Options{LogWriter: &logs, Context: ContextConfig{Enabled: false}})

	result, err := app.Run(context.Background(), "hello", 1)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !strings.Contains(result, "Agent stopped") || !strings.Contains(result, "1 iterations") {
		t.Fatalf("Run returned %q, want max iterations message with count", result)
	}
	if !strings.Contains(logs.String(), "[Turn] iterations=1 allow_plan=false") {
		t.Fatalf("logs did not include turn timing: %q", logs.String())
	}
}

func TestRunConversationTurnReturnsUpdatedMessages(t *testing.T) {
	client := &stubClient{responses: []AssistantMessage{{Role: "assistant", Content: "reply"}}}
	var logs bytes.Buffer
	app := NewWithOptions(client, "", Options{LogWriter: &logs, Context: ContextConfig{Enabled: false}})

	messages := []map[string]any{
		{"role": "system", "content": "sys"},
		{"role": "user", "content": "hello"},
	}

	result, updated, err := app.RunConversationTurn(context.Background(), messages, 5)
	if err != nil {
		t.Fatalf("RunConversationTurn returned error: %v", err)
	}
	if result != "reply" {
		t.Fatalf("result = %q, want %q", result, "reply")
	}
	if len(updated) != 3 {
		t.Fatalf("updated messages = %d, want 3 (system + user + assistant)", len(updated))
	}
	if updated[2]["role"] != "assistant" {
		t.Fatalf("last message role = %q, want assistant", updated[2]["role"])
	}
	// Original messages should not be mutated
	if len(messages) != 2 {
		t.Fatalf("original messages mutated: len = %d, want 2", len(messages))
	}
	if !strings.Contains(logs.String(), "[Turn] iterations=1 allow_plan=false") {
		t.Fatalf("logs did not include turn timing: %q", logs.String())
	}
}

func TestRunConversationTurnSanitizesCompletedToolHistory(t *testing.T) {
	client := &stubClient{responses: []AssistantMessage{{Role: "assistant", Content: "next reply"}}}
	var logs bytes.Buffer
	app := NewWithOptions(client, "", Options{LogWriter: &logs, Context: ContextConfig{Enabled: false}})

	messages := []map[string]any{
		{"role": "system", "content": "sys"},
		{"role": "user", "content": "search news"},
		{"role": "assistant", "content": nil, "tool_calls": []ToolCall{{ID: "tc-1", Type: "function", Function: FunctionCall{Name: "web_search", Arguments: `{"query":"today news"}`}}}},
		{"role": "tool", "tool_call_id": "tc-1", "content": "search results"},
		{"role": "assistant", "content": "today's headlines"},
		{"role": "user", "content": "continue"},
	}

	result, updated, err := app.RunConversationTurn(context.Background(), messages, 5)
	if err != nil {
		t.Fatalf("RunConversationTurn returned error: %v", err)
	}
	if result != "next reply" {
		t.Fatalf("result = %q, want %q", result, "next reply")
	}
	if len(client.messages) != 1 {
		t.Fatalf("client saw %d requests, want 1", len(client.messages))
	}
	request := client.messages[0]
	if len(request) != 4 {
		t.Fatalf("request messages = %d, want 4 after sanitizing tool history", len(request))
	}
	for _, message := range request {
		role, _ := message["role"].(string)
		if role == "tool" {
			t.Fatalf("request still contains tool message: %#v", message)
		}
		if role == "assistant" {
			if _, ok := message["tool_calls"]; ok {
				t.Fatalf("request still contains assistant tool call scaffolding: %#v", message)
			}
		}
	}
	if len(updated) != 7 {
		t.Fatalf("updated messages = %d, want 7 after appending final assistant reply to full conversation history", len(updated))
	}
}

func TestRunSanitizesEarlierCompletedToolHistoryBetweenIterations(t *testing.T) {
	client := &stubClient{
		responses: []AssistantMessage{
			{
				Content: "",
				ToolCalls: []ToolCall{
					{ID: "tc-1", Type: "function", Function: FunctionCall{Name: "read_file", Arguments: `{"path":"missing-1.txt"}`}},
				},
			},
			{
				Content: "",
				ToolCalls: []ToolCall{
					{ID: "tc-2", Type: "function", Function: FunctionCall{Name: "read_file", Arguments: `{"path":"missing-2.txt"}`}},
				},
			},
			{Content: "done"},
		},
	}
	var logs bytes.Buffer
	app := NewWithOptions(client, "", Options{LogWriter: &logs, Context: ContextConfig{Enabled: false}})

	result, err := app.Run(context.Background(), "search", 5)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result != "done" {
		t.Fatalf("Run returned %q, want %q", result, "done")
	}
	if len(client.messages) != 3 {
		t.Fatalf("client saw %d requests, want 3", len(client.messages))
	}
	secondRequest := client.messages[1]
	assistantToolCalls := 0
	for _, message := range secondRequest {
		role, _ := message["role"].(string)
		if role == "assistant" {
			if _, ok := message["tool_calls"]; ok {
				assistantToolCalls++
			}
		}
	}
	if assistantToolCalls != 1 {
		t.Fatalf("second request assistant tool call count = %d, want 1 pending call", assistantToolCalls)
	}
	thirdRequest := client.messages[2]
	assistantToolCalls = 0
	toolMessages := 0
	for _, message := range thirdRequest {
		role, _ := message["role"].(string)
		if role == "tool" {
			toolMessages++
		}
		if role == "assistant" {
			if _, ok := message["tool_calls"]; ok {
				assistantToolCalls++
			}
		}
	}
	if assistantToolCalls != 1 {
		t.Fatalf("third request assistant tool call count = %d, want 1 latest call", assistantToolCalls)
	}
	if toolMessages != 1 {
		t.Fatalf("third request tool message count = %d, want 1 latest tool result", toolMessages)
	}
}

func TestRunContextPrunesOldMessages(t *testing.T) {
	client := &stubClient{responses: []AssistantMessage{{Role: "assistant", Content: "reply"}}}
	var logs bytes.Buffer
	app := NewWithOptions(client, "", Options{
		LogWriter: &logs,
		Context: ContextConfig{
			Enabled:               true,
			MaxInputTokens:        60,
			TargetInputTokens:     20,
			ResponseReserveTokens: 10,
			KeepLastTurns:         1,
		},
	})

	messages := []map[string]any{
		{"role": "system", "content": "system prompt"},
		{"role": "user", "content": strings.Repeat("older-user-", 8)},
		{"role": "assistant", "content": strings.Repeat("older-assistant-", 8)},
		{"role": "user", "content": strings.Repeat("recent-user-", 8)},
	}

	_, _, err := app.RunConversationTurn(context.Background(), messages, 2)
	if err != nil {
		t.Fatalf("RunConversationTurn returned error: %v", err)
	}
	if len(client.messages) != 1 {
		t.Fatalf("client saw %d requests, want 1", len(client.messages))
	}
	request := client.messages[0]
	if len(request) != 2 {
		t.Fatalf("request messages = %d, want 2 after pruning", len(request))
	}
	if request[0]["role"] != "system" {
		t.Fatalf("first message role = %#v, want system", request[0]["role"])
	}
	if request[1]["content"] != strings.Repeat("recent-user-", 8) {
		t.Fatalf("last kept content = %#v, want most recent user turn", request[1]["content"])
	}
	if !strings.Contains(logs.String(), "[Context]") || !strings.Contains(logs.String(), "pruned=true") {
		t.Fatalf("logs did not include context budget line: %q", logs.String())
	}
}

func TestRunContextSummarizesOldMessages(t *testing.T) {
	client := &stubClient{
		responses:       []AssistantMessage{{Role: "assistant", Content: "reply"}},
		optionResponses: []AssistantMessage{{Role: "assistant", Content: "carry forward the prior intent and constraints"}},
	}
	var logs bytes.Buffer
	app := NewWithOptions(client, "", Options{
		LogWriter: &logs,
		Context: ContextConfig{
			Enabled:               true,
			MaxInputTokens:        60,
			TargetInputTokens:     10,
			ResponseReserveTokens: 10,
			KeepLastTurns:         1,
			SummarizationEnabled:  true,
			SummaryTriggerTokens:  18,
		},
	})

	messages := []map[string]any{
		{"role": "system", "content": "system prompt"},
		{"role": "user", "content": strings.Repeat("older-user-", 10)},
		{"role": "assistant", "content": strings.Repeat("older-assistant-", 10)},
		{"role": "user", "content": strings.Repeat("recent-user-", 8)},
	}

	_, _, err := app.RunConversationTurn(context.Background(), messages, 2)
	if err != nil {
		t.Fatalf("RunConversationTurn returned error: %v", err)
	}
	if len(client.optionMessages) != 1 {
		t.Fatalf("summary call count = %d, want 1", len(client.optionMessages))
	}
	if len(client.messages) != 1 {
		t.Fatalf("client saw %d requests, want 1", len(client.messages))
	}
	request := client.messages[0]
	if len(request) != 3 {
		t.Fatalf("request messages = %d, want 3 after summarization", len(request))
	}
	if request[0]["role"] != "system" {
		t.Fatalf("first message role = %#v, want system", request[0]["role"])
	}
	summary, _ := request[1]["content"].(string)
	if !strings.Contains(summary, "Summary of earlier conversation:") {
		t.Fatalf("summary message = %q, want summary prefix", summary)
	}
	if request[2]["content"] != strings.Repeat("recent-user-", 8) {
		t.Fatalf("last kept content = %#v, want recent tail", request[2]["content"])
	}
	if !strings.Contains(logs.String(), "summarized=true") {
		t.Fatalf("logs did not include summarized context line: %q", logs.String())
	}
}

func TestRunContextFallsBackWhenSummarizationFails(t *testing.T) {
	client := &stubClient{
		responses:    []AssistantMessage{{Role: "assistant", Content: "reply"}},
		optionErrors: []error{errors.New("summary failed")},
	}
	var logs bytes.Buffer
	app := NewWithOptions(client, "", Options{
		LogWriter: &logs,
		Context: ContextConfig{
			Enabled:               true,
			MaxInputTokens:        60,
			TargetInputTokens:     10,
			ResponseReserveTokens: 10,
			KeepLastTurns:         1,
			SummarizationEnabled:  true,
			SummaryTriggerTokens:  18,
		},
	})

	messages := []map[string]any{
		{"role": "system", "content": "system prompt"},
		{"role": "user", "content": strings.Repeat("older-user-", 10)},
		{"role": "assistant", "content": strings.Repeat("older-assistant-", 10)},
		{"role": "user", "content": strings.Repeat("recent-user-", 8)},
	}

	_, _, err := app.RunConversationTurn(context.Background(), messages, 2)
	if err != nil {
		t.Fatalf("RunConversationTurn returned error: %v", err)
	}
	if len(client.messages) != 1 {
		t.Fatalf("client saw %d requests, want 1", len(client.messages))
	}
	request := client.messages[0]
	if len(request) != 2 {
		t.Fatalf("request messages = %d, want 2 after fallback pruning", len(request))
	}
	if request[1]["content"] != strings.Repeat("recent-user-", 8) {
		t.Fatalf("fallback kept content = %#v, want recent tail", request[1]["content"])
	}
	if strings.Contains(logs.String(), "summarized=true") {
		t.Fatalf("logs unexpectedly reported summarization success: %q", logs.String())
	}
}

func TestRunSkillToolExecutesWorkspaceSkill(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".agent", "skills", "demo"), 0o755); err != nil {
		t.Fatalf("failed to create skill directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".agent", "skills", "demo", "SKILL.md"), []byte("# Demo Skill\n\nRead README.md and summarize it."), 0o644); err != nil {
		t.Fatalf("failed to write skill file: %v", err)
	}

	client := &stubClient{
		responses: []AssistantMessage{
			{ToolCalls: []ToolCall{{ID: "skill-1", Function: FunctionCall{Name: "run_skill", Arguments: `{"skill":"demo","args":"focus on setup"}`}}}},
			{Content: "skill result"},
			{Content: "final answer"},
		},
	}
	catalog := tools.NewCatalog(tools.Options{WorkspaceRoot: root})
	app := NewWithOptions(client, "", Options{
		ToolDefinitions: catalog.Definitions(),
		Functions:       catalog.Registry(),
		WorkspaceRoot:   root,
	})

	result, err := app.RunConversation(context.Background(), []map[string]any{{"role": "system", "content": "sys"}, {"role": "user", "content": "help me with demo skill"}}, 5)
	if err != nil {
		t.Fatalf("RunConversation returned error: %v", err)
	}
	if result != "final answer" {
		t.Fatalf("result = %q, want %q", result, "final answer")
	}
	if len(client.messages) != 3 {
		t.Fatalf("client saw %d requests, want 3", len(client.messages))
	}
	toolMessages := extractToolMessages(client.messages[2])
	if len(toolMessages) != 1 {
		t.Fatalf("tool messages = %d, want 1", len(toolMessages))
	}
	if content, _ := toolMessages[0]["content"].(string); content != "skill result" {
		t.Fatalf("tool content = %q, want %q", content, "skill result")
	}
	childRequest := client.messages[1]
	if len(childRequest) < 2 {
		t.Fatalf("child request messages = %d, want at least 2", len(childRequest))
	}
	childUser, _ := childRequest[1]["content"].(string)
	if !strings.Contains(childUser, "Read README.md and summarize it.") || !strings.Contains(childUser, "focus on setup") {
		t.Fatalf("child skill prompt = %q, want skill body and args", childUser)
	}
}

func TestRunReturnsToolExecutionErrorToLoop(t *testing.T) {
	client := &stubClient{
		responses: []AssistantMessage{
			{
				Content: "",
				ToolCalls: []ToolCall{
					{
						ID: "tc-1",
						Function: FunctionCall{
							Name:      "read_file",
							Arguments: `{"path":"missing.txt"}`,
						},
					},
				},
			},
			{Content: "done"},
		},
	}

	var logs bytes.Buffer
	app := New(client, "", &logs)

	result, err := app.Run(context.Background(), "test tool error", 2)
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
	content, _ := toolMessages[0]["content"].(string)
	if !strings.Contains(content, "Error:") {
		t.Fatalf("tool content = %q, want error prefix", content)
	}
	if !strings.Contains(content, "missing.txt") {
		t.Fatalf("tool content = %q, want missing path detail", content)
	}
	if !strings.Contains(logs.String(), "[Tool] name=read_file") || !strings.Contains(logs.String(), "status=error") {
		t.Fatalf("logs did not include tool timing: %q", logs.String())
	}
	if !strings.Contains(logs.String(), "[Turn] iterations=2 allow_plan=false") {
		t.Fatalf("logs did not include turn timing: %q", logs.String())
	}
}

type failingRecorder struct {
	callCount int
	failAfter int
}

func (r *failingRecorder) RecordMessage(_ map[string]any) error {
	r.callCount++
	if r.callCount > r.failAfter {
		return errors.New("disk write failed")
	}
	return nil
}

func TestPlanToolMissingTaskReturnsToolMessage(t *testing.T) {
	// Response 1: model calls plan tool with empty args (no "task" key)
	// Response 2: model recovers after seeing the tool error
	client := &stubClient{
		responses: []AssistantMessage{
			{
				ToolCalls: []ToolCall{{
					ID:       "plan-1",
					Function: FunctionCall{Name: "plan", Arguments: `{}`},
				}},
			},
			{Content: "recovered"},
		},
	}

	catalog := tools.NewCatalog(tools.Options{IncludePlan: true})
	app := NewWithOptions(client, "", Options{
		Planner:         NewPlanner(client, ""),
		ToolDefinitions: catalog.Definitions(),
		Functions:       catalog.Registry(),
	})

	result, err := app.RunConversation(context.Background(), []map[string]any{
		{"role": "system", "content": "sys"},
		{"role": "user", "content": "do something"},
	}, 5)
	if err != nil {
		t.Fatalf("RunConversation returned error: %v", err)
	}
	if result != "recovered" {
		t.Fatalf("result = %q, want %q", result, "recovered")
	}
	// Client should see 2 requests: initial + retry after tool error
	if len(client.messages) != 2 {
		t.Fatalf("client saw %d requests, want 2", len(client.messages))
	}
	toolMessages := extractToolMessages(client.messages[1])
	if len(toolMessages) != 1 {
		t.Fatalf("tool messages = %d, want 1", len(toolMessages))
	}
	content, _ := toolMessages[0]["content"].(string)
	if !strings.Contains(content, "Error:") || !strings.Contains(content, "task") {
		t.Fatalf("tool content = %q, want error about missing task", content)
	}
}

func TestPlanToolPlannerFailureReturnsToolMessage(t *testing.T) {
	// Response 1: model calls plan tool
	// Response 2: planner.Generate call returns invalid JSON → parse error
	// Response 3: model recovers
	client := &stubClient{
		responses: []AssistantMessage{
			{
				ToolCalls: []ToolCall{{
					ID:       "plan-1",
					Function: FunctionCall{Name: "plan", Arguments: `{"task":"do stuff"}`},
				}},
			},
			{Content: "not valid json"},
			{Content: "recovered"},
		},
	}

	catalog := tools.NewCatalog(tools.Options{IncludePlan: true})
	app := NewWithOptions(client, "", Options{
		Planner:         NewPlanner(client, ""),
		ToolDefinitions: catalog.Definitions(),
		Functions:       catalog.Registry(),
	})

	result, err := app.RunConversation(context.Background(), []map[string]any{
		{"role": "system", "content": "sys"},
		{"role": "user", "content": "start"},
	}, 5)
	if err != nil {
		t.Fatalf("RunConversation returned error: %v", err)
	}
	if result != "recovered" {
		t.Fatalf("result = %q, want %q", result, "recovered")
	}
	// Request 1: initial, Request 2: planner Generate, Request 3: retry after error
	if len(client.messages) != 3 {
		t.Fatalf("client saw %d requests, want 3", len(client.messages))
	}
	toolMessages := extractToolMessages(client.messages[2])
	if len(toolMessages) != 1 {
		t.Fatalf("tool messages = %d, want 1", len(toolMessages))
	}
	content, _ := toolMessages[0]["content"].(string)
	if !strings.Contains(content, "Error:") || !strings.Contains(content, "plan generation failed") {
		t.Fatalf("tool content = %q, want plan generation error", content)
	}
}

func TestPlanToolNoStepsReturnsToolMessage(t *testing.T) {
	// Response 1: model calls plan tool
	// Response 2: planner returns empty steps
	// Response 3: model recovers
	client := &stubClient{
		responses: []AssistantMessage{
			{
				ToolCalls: []ToolCall{{
					ID:       "plan-1",
					Function: FunctionCall{Name: "plan", Arguments: `{"task":"do stuff"}`},
				}},
			},
			{Content: `{"steps":[]}`},
			{Content: "recovered"},
		},
	}

	catalog := tools.NewCatalog(tools.Options{IncludePlan: true})
	app := NewWithOptions(client, "", Options{
		Planner:         NewPlanner(client, ""),
		ToolDefinitions: catalog.Definitions(),
		Functions:       catalog.Registry(),
	})

	result, err := app.RunConversation(context.Background(), []map[string]any{
		{"role": "system", "content": "sys"},
		{"role": "user", "content": "start"},
	}, 5)
	if err != nil {
		t.Fatalf("RunConversation returned error: %v", err)
	}
	if result != "recovered" {
		t.Fatalf("result = %q, want %q", result, "recovered")
	}
	if len(client.messages) != 3 {
		t.Fatalf("client saw %d requests, want 3", len(client.messages))
	}
	toolMessages := extractToolMessages(client.messages[2])
	if len(toolMessages) != 1 {
		t.Fatalf("tool messages = %d, want 1", len(toolMessages))
	}
	content, _ := toolMessages[0]["content"].(string)
	if !strings.Contains(content, "Error:") || !strings.Contains(content, "no steps") {
		t.Fatalf("tool content = %q, want no steps error", content)
	}
}

func TestMaxIterationsMessageIncludesCount(t *testing.T) {
	toolCallResponse := AssistantMessage{
		ToolCalls: []ToolCall{
			{
				ID:       "tc-1",
				Function: FunctionCall{Name: "missing_tool", Arguments: "{}"},
			},
		},
	}
	client := &stubClient{
		responses: []AssistantMessage{toolCallResponse, toolCallResponse, toolCallResponse},
	}
	app := New(client, "", &bytes.Buffer{})

	result, err := app.Run(context.Background(), "hello", 3)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !strings.Contains(result, "3 iterations") {
		t.Fatalf("result = %q, want message containing iteration count", result)
	}
}

func TestRecorderFailureIsFatal(t *testing.T) {
	client := &stubClient{
		responses: []AssistantMessage{{Content: "done"}},
	}

	recorder := &failingRecorder{failAfter: 0}
	app := NewWithOptions(client, "", Options{
		LogWriter: &bytes.Buffer{},
		Recorder:  recorder,
	})

	_, err := app.Run(context.Background(), "hello", 5)
	if err == nil {
		t.Fatal("Run returned nil error, want recorder failure")
	}
	if !strings.Contains(err.Error(), "disk write failed") {
		t.Fatalf("error = %q, want disk write failed", err.Error())
	}
}
