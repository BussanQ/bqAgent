package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"bqagent/internal/tools"
)

func TestOpenAIResponseClientConvertsRequestAndResponse(t *testing.T) {
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/responses" {
			t.Fatalf("request path = %q, want /responses", request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer response-key" {
			t.Fatalf("Authorization = %q, want bearer token", got)
		}
		if err := json.NewDecoder(request.Body).Decode(&requestBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprint(writer, `{
			"output":[
				{"type":"message","role":"assistant","content":[{"type":"output_text","text":"checking"}]},
				{"type":"function_call","id":"fc_1","call_id":"call_1","name":"web_search","arguments":"{\"query\":\"news\"}"}
			],
			"usage":{"input_tokens":12,"output_tokens":4,"total_tokens":16}
		}`)
	}))
	defer server.Close()

	messages := []map[string]any{
		{"role": "system", "content": "be useful"},
		{"role": "user", "content": "hello"},
		AssistantMessage{Role: "assistant", ToolCalls: []ToolCall{{
			ID: "prior_call", Type: "function", Function: FunctionCall{Name: "read_file", Arguments: `{"path":"README.md"}`},
		}}}.RequestMessage(),
		{"role": "tool", "tool_call_id": "prior_call", "content": "file contents"},
	}
	client := NewClientWithAPIType("response-key", server.URL, APIType("OpenAI-Response"), server.Client())
	message, err := client.CreateChatCompletionWithOptions(context.Background(), "gpt-test", messages, tools.Definitions()[:1], ChatCompletionOptions{
		ResponseFormat: map[string]any{"type": "json_object"},
	})
	if err != nil {
		t.Fatalf("CreateChatCompletionWithOptions: %v", err)
	}

	input, _ := requestBody["input"].([]any)
	if len(input) != 4 {
		t.Fatalf("input length = %d, want 4", len(input))
	}
	functionCall, _ := input[2].(map[string]any)
	if functionCall["type"] != "function_call" || functionCall["call_id"] != "prior_call" {
		t.Fatalf("function call input = %#v", functionCall)
	}
	toolOutput, _ := input[3].(map[string]any)
	if toolOutput["type"] != "function_call_output" || toolOutput["output"] != "file contents" {
		t.Fatalf("function output input = %#v", toolOutput)
	}
	requestTools, _ := requestBody["tools"].([]any)
	firstTool, _ := requestTools[0].(map[string]any)
	if firstTool["name"] != "execute_bash" || firstTool["function"] != nil {
		t.Fatalf("responses tool = %#v, want flat function tool", firstTool)
	}
	textOptions, _ := requestBody["text"].(map[string]any)
	format, _ := textOptions["format"].(map[string]any)
	if format["type"] != "json_object" {
		t.Fatalf("text.format = %#v", format)
	}
	if message.FinalContent() != "checking" || len(message.ToolCalls) != 1 {
		t.Fatalf("message = %#v", message)
	}
	if message.ToolCalls[0].ID != "call_1" || message.ToolCalls[0].Function.Name != "web_search" {
		t.Fatalf("tool call = %#v", message.ToolCalls[0])
	}
	if message.Usage.TotalTokens != 16 {
		t.Fatalf("usage = %#v", message.Usage)
	}
}

func TestOpenAIResponseClientStreamsTextAndToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintln(writer, `data: {"type":"response.output_text.delta","delta":"hel"}`)
		fmt.Fprintln(writer, `data: {"type":"response.output_text.delta","delta":"lo"}`)
		fmt.Fprintln(writer, `data: {"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","call_id":"call_1","name":"read_file"}}`)
		fmt.Fprintln(writer, `data: {"type":"response.function_call_arguments.delta","output_index":1,"delta":"{\"path\":"}`)
		fmt.Fprintln(writer, `data: {"type":"response.function_call_arguments.delta","output_index":1,"delta":"\"README.md\"}"}`)
		fmt.Fprintln(writer, `data: {"type":"response.completed","response":{"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}}`)
	}))
	defer server.Close()

	client := NewClientWithAPIType("", server.URL, APITypeOpenAIResponse, server.Client())
	var chunks strings.Builder
	message, err := client.CreateChatCompletionStream(context.Background(), "gpt-test", []map[string]any{{"role": "user", "content": "hi"}}, nil, func(chunk string) {
		chunks.WriteString(chunk)
	})
	if err != nil {
		t.Fatalf("CreateChatCompletionStream: %v", err)
	}
	if chunks.String() != "hello" || message.FinalContent() != "hello" {
		t.Fatalf("chunks = %q, content = %q", chunks.String(), message.FinalContent())
	}
	if len(message.ToolCalls) != 1 || message.ToolCalls[0].Function.Arguments != `{"path":"README.md"}` {
		t.Fatalf("tool calls = %#v", message.ToolCalls)
	}
	if message.Usage.TotalTokens != 5 {
		t.Fatalf("usage = %#v", message.Usage)
	}
}

func TestAnthropicClientConvertsRequestAndResponse(t *testing.T) {
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/messages" {
			t.Fatalf("request path = %q, want /messages", request.URL.Path)
		}
		if got := request.Header.Get("x-api-key"); got != "anthropic-key" {
			t.Fatalf("x-api-key = %q", got)
		}
		if got := request.Header.Get("anthropic-version"); got != "2023-06-01" {
			t.Fatalf("anthropic-version = %q", got)
		}
		if got := request.Header.Get("Authorization"); got != "" {
			t.Fatalf("Authorization = %q, want empty", got)
		}
		if err := json.NewDecoder(request.Body).Decode(&requestBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprint(writer, `{
			"role":"assistant",
			"content":[
				{"type":"text","text":"checking"},
				{"type":"tool_use","id":"toolu_1","name":"web_search","input":{"query":"news"}}
			],
			"usage":{"input_tokens":10,"output_tokens":6}
		}`)
	}))
	defer server.Close()

	messages := []map[string]any{
		{"role": "system", "content": "be useful"},
		{"role": "user", "content": "hello"},
		AssistantMessage{Role: "assistant", ToolCalls: []ToolCall{{
			ID: "toolu_prior", Type: "function", Function: FunctionCall{Name: "read_file", Arguments: `{"path":"README.md"}`},
		}}}.RequestMessage(),
		{"role": "tool", "tool_call_id": "toolu_prior", "content": "file contents"},
	}
	client := NewClientWithAPIType("anthropic-key", server.URL, APITypeAnthropic, server.Client())
	message, err := client.CreateChatCompletion(context.Background(), "claude-test", messages, tools.Definitions()[:1])
	if err != nil {
		t.Fatalf("CreateChatCompletion: %v", err)
	}

	if requestBody["system"] != "be useful" {
		t.Fatalf("system = %#v", requestBody["system"])
	}
	requestMessages, _ := requestBody["messages"].([]any)
	if len(requestMessages) != 3 {
		t.Fatalf("messages length = %d, want 3", len(requestMessages))
	}
	assistant, _ := requestMessages[1].(map[string]any)
	assistantContent, _ := assistant["content"].([]any)
	toolUse, _ := assistantContent[0].(map[string]any)
	if toolUse["type"] != "tool_use" || toolUse["id"] != "toolu_prior" {
		t.Fatalf("tool use = %#v", toolUse)
	}
	toolResultMessage, _ := requestMessages[2].(map[string]any)
	toolResultContent, _ := toolResultMessage["content"].([]any)
	toolResult, _ := toolResultContent[0].(map[string]any)
	if toolResult["type"] != "tool_result" || toolResult["tool_use_id"] != "toolu_prior" {
		t.Fatalf("tool result = %#v", toolResult)
	}
	requestTools, _ := requestBody["tools"].([]any)
	firstTool, _ := requestTools[0].(map[string]any)
	if firstTool["name"] != "execute_bash" || firstTool["input_schema"] == nil {
		t.Fatalf("anthropic tool = %#v", firstTool)
	}
	if message.FinalContent() != "checking" || len(message.ToolCalls) != 1 {
		t.Fatalf("message = %#v", message)
	}
	if message.ToolCalls[0].ID != "toolu_1" || message.ToolCalls[0].Function.Arguments != `{"query":"news"}` {
		t.Fatalf("tool call = %#v", message.ToolCalls[0])
	}
	if message.Usage.TotalTokens != 16 {
		t.Fatalf("usage = %#v", message.Usage)
	}
}

func TestAnthropicClientStreamsTextAndToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintln(writer, `data: {"type":"message_start","message":{"usage":{"input_tokens":7,"output_tokens":0}}}`)
		fmt.Fprintln(writer, `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
		fmt.Fprintln(writer, `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"done"}}`)
		fmt.Fprintln(writer, `data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"read_file","input":{}}}`)
		fmt.Fprintln(writer, `data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"README.md\"}"}}`)
		fmt.Fprintln(writer, `data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}`)
	}))
	defer server.Close()

	client := NewClientWithAPIType("", server.URL, APITypeAnthropic, server.Client())
	var chunks strings.Builder
	message, err := client.CreateChatCompletionStream(context.Background(), "claude-test", []map[string]any{{"role": "user", "content": "hi"}}, nil, func(chunk string) {
		chunks.WriteString(chunk)
	})
	if err != nil {
		t.Fatalf("CreateChatCompletionStream: %v", err)
	}
	if chunks.String() != "done" || message.FinalContent() != "done" {
		t.Fatalf("chunks = %q, content = %q", chunks.String(), message.FinalContent())
	}
	if len(message.ToolCalls) != 1 || message.ToolCalls[0].Function.Arguments != `{"path":"README.md"}` {
		t.Fatalf("tool calls = %#v", message.ToolCalls)
	}
	if message.Usage.TotalTokens != 12 {
		t.Fatalf("usage = %#v", message.Usage)
	}
}

func TestNormalizeAPIType(t *testing.T) {
	tests := map[string]APIType{
		"":                 APITypeOpenAI,
		"OpenAI":           APITypeOpenAI,
		"OpenAI-Response":  APITypeOpenAIResponse,
		"openai_responses": APITypeOpenAIResponse,
		"Anthropic":        APITypeAnthropic,
		"claude":           APITypeAnthropic,
	}
	for input, expected := range tests {
		if actual := NormalizeAPIType(input); actual != expected {
			t.Errorf("NormalizeAPIType(%q) = %q, want %q", input, actual, expected)
		}
	}
}

func TestAlternativeAPIClientsConvertOpenAIMultimodalContent(t *testing.T) {
	content := []any{
		map[string]any{"type": "text", "text": "describe"},
		map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,AQID"}},
	}

	responseContent, _ := openAIResponseContent(content).([]any)
	responseImage, _ := responseContent[1].(map[string]any)
	if responseImage["type"] != "input_image" || responseImage["image_url"] != "data:image/png;base64,AQID" {
		t.Fatalf("responses image = %#v", responseImage)
	}

	anthropicBlocks := anthropicContent(content)
	anthropicImage, _ := anthropicBlocks[1].(map[string]any)
	source, _ := anthropicImage["source"].(map[string]any)
	if source["type"] != "base64" || source["media_type"] != "image/png" || source["data"] != "AQID" {
		t.Fatalf("anthropic image source = %#v", source)
	}
}
