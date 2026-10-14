package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
)

func TestAnthropicStreamToolArguments(t *testing.T) {
	const first = `{"command":"echo first"}`
	const second = `{"command":"echo second"}`
	start := func(index any, id string, input any) map[string]any {
		return map[string]any{"type": "content_block_start", "index": index, "content_block": map[string]any{"type": "tool_use", "id": id, "name": "execute_bash", "input": input}}
	}
	delta := func(index any, args string) map[string]any {
		return map[string]any{"type": "content_block_delta", "index": index, "delta": map[string]any{"type": "input_json_delta", "partial_json": args}}
	}
	stop := func(index int) map[string]any {
		return map[string]any{"type": "content_block_stop", "index": index}
	}
	call := func(id, args string) ToolCall {
		return ToolCall{ID: id, Type: "function", Function: FunctionCall{Name: "execute_bash", Arguments: args}}
	}
	empty := map[string]any{}
	tests := []struct {
		name    string
		events  []map[string]any
		want    []ToolCall
		wantErr bool
	}{
		{name: "empty input without deltas", events: []map[string]any{start(1, "tool_1", empty), stop(1)}, want: []ToolCall{call("tool_1", "{}")}},
		{name: "complete initial input", events: []map[string]any{start(1, "tool_1", map[string]any{"command": "echo first"}), stop(1)}, want: []ToolCall{call("tool_1", first)}},
		{name: "empty delta keeps initial input", events: []map[string]any{start(1, "tool_1", empty), delta(1, ""), stop(1)}, want: []ToolCall{call("tool_1", "{}")}},
		{name: "deltas replace initial input", events: []map[string]any{start(1, "tool_1", map[string]any{"command": "echo first"}), delta(1, second), stop(1)}, want: []ToolCall{call("tool_1", second)}},
		{name: "empty placeholder is not prepended", events: []map[string]any{start(1, "tool_1", empty), delta(1, first[:15]), delta(1, first[15:]), stop(1)}, want: []ToolCall{call("tool_1", first)}},
		{name: "interleaved tool blocks", events: []map[string]any{start(2, "tool_2", empty), start(1, "tool_1", empty), delta(1, first[:15]), delta(2, second), stop(2), delta(1, first[15:]), stop(1)}, want: []ToolCall{call("tool_1", first), call("tool_2", second)}},
		{name: "concatenated objects preserved for recovery", events: []map[string]any{start(1, "tool_1", empty), delta(1, first), delta(1, second), stop(1)}, want: []ToolCall{call("tool_1", first+second)}},
		{name: "truncated delta does not fall back to initial input", events: []map[string]any{start(1, "tool_1", map[string]any{"command": "echo first"}), delta(1, `{"command":`), stop(1)}, want: []ToolCall{call("tool_1", `{"command":`)}},
		{name: "missing input does not become an empty object", events: []map[string]any{start(1, "tool_1", nil), stop(1)}, want: []ToolCall{call("tool_1", "")}},
		{name: "missing start index", events: []map[string]any{start(nil, "tool_1", empty)}, wantErr: true},
		{name: "negative start index", events: []map[string]any{start(-1, "tool_1", empty)}, wantErr: true},
		{name: "missing delta index", events: []map[string]any{start(0, "tool_1", empty), delta(nil, first)}, wantErr: true},
		{name: "delta before start", events: []map[string]any{delta(1, first)}, wantErr: true},
		{name: "duplicate start index", events: []map[string]any{start(1, "tool_1", empty), delta(1, first), start(1, "tool_2", empty)}, wantErr: true},
		{name: "delta after block stop", events: []map[string]any{start(1, "tool_1", empty), delta(1, first), stop(1), delta(1, second)}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stream strings.Builder
			stream.WriteString("data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":3}}}\n\n")
			stream.WriteString("data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"checking\"}}\n\n")
			for _, event := range test.events {
				encoded, err := json.Marshal(event)
				if err != nil {
					t.Fatal(err)
				}
				fmt.Fprintf(&stream, "data: %s\n\n", encoded)
			}
			stream.WriteString("data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":2}}\n\ndata: {\"type\":\"message_stop\"}\n\n")
			requests := 0
			httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				requests++
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(stream.String()))}, nil
			})}
			client := newTestResilientClient(APITypeAnthropic, "https://stream.test", httpClient, 0)
			var chunks strings.Builder
			message, err := client.CreateChatCompletionStream(context.Background(), "test-model", nil, nil, func(chunk string) { chunks.WriteString(chunk) })
			if requests != 1 {
				t.Fatalf("got %d requests, want no replay of tool fragments", requests)
			}
			if test.wantErr {
				var providerErr *ProviderError
				if !errors.As(err, &providerErr) || providerErr.Category != ProviderErrorProtocol || len(message.ToolCalls) != 0 {
					t.Fatalf("expected protocol error without partial calls, got %#v, %v", message, err)
				}
				return
			}
			if err != nil || !slices.Equal(message.ToolCalls, test.want) {
				t.Fatalf("tool calls = %#v, %v; want %#v", message.ToolCalls, err, test.want)
			}
			if chunks.String() != "checking" || message.FinalContent() != "checking" || message.Usage.TotalTokens != 5 || message.Completion.StopReason != "tool_use" {
				t.Fatalf("content, usage or stop reason lost: %#v", message)
			}
		})
	}
}

func TestAnthropicMalformedArgumentsReplay(t *testing.T) {
	for _, raw := range []string{`{"command":"echo first"}{"command":"echo second"}`, `{"command":`, `[]`, `null`, `"command"`, ""} {
		t.Run(raw, func(t *testing.T) {
			messages := []map[string]any{
				{"role": "assistant", "tool_calls": []ToolCall{
					{ID: "bad", Function: FunctionCall{Name: "execute_bash", Arguments: raw}},
					{ID: "good", Function: FunctionCall{Name: "read_file", Arguments: `{"path":"README.md"}`}},
				}},
				{"role": "tool", "tool_call_id": "bad", "content": "Error: Invalid tool arguments. Reissue separate tool calls."},
				{"role": "tool", "tool_call_id": "good", "content": "file content"},
			}
			_, converted, _ := anthropicMessages(messages, PromptCacheOptions{})
			if len(converted) != 2 || len(converted[0].Content) != 2 || len(converted[1].Content) != 2 {
				t.Fatalf("unexpected message structure: %#v", converted)
			}
			bad := converted[0].Content[0].(map[string]any)
			if input, ok := bad["input"].(map[string]any); !ok || len(input) != 0 || bad["id"] != "bad" {
				t.Fatalf("invalid tool_use envelope: %#v", bad)
			}
			result := converted[1].Content[0].(map[string]any)
			content, _ := result["content"].(string)
			if result["tool_use_id"] != "bad" || result["is_error"] != true || !strings.Contains(content, fmt.Sprintf("Original invalid arguments (this call was not executed): %q", raw)) {
				t.Fatalf("invalid arguments lost from error result: %#v", result)
			}
			good := converted[0].Content[1].(map[string]any)
			if good["input"].(map[string]any)["path"] != "README.md" {
				t.Fatalf("valid input changed: %#v", good)
			}
			goodResult := converted[1].Content[1].(map[string]any)
			if goodResult["is_error"] != nil || goodResult["content"] != "file content" || goodResult["tool_use_id"] != "good" {
				t.Fatalf("valid tool result changed: %#v", goodResult)
			}
			if messageToolCalls(messages[0])[0].Function.Arguments != raw || messages[1]["content"] != "Error: Invalid tool arguments. Reissue separate tool calls." {
				t.Fatal("request conversion mutated recorded history")
			}
		})
	}
}

func TestAnthropicNonStreamingToolInputs(t *testing.T) {
	for _, input := range []string{`{}`, `{"command":"echo '{\"a\":1}{\"b\":2}'"}`} {
		t.Run(input, func(t *testing.T) {
			payload := fmt.Sprintf(`{"content":[{"type":"tool_use","id":"tool_1","name":"execute_bash","input":%s},{"type":"tool_use","id":"tool_2","name":"execute_bash","input":{"command":"echo second"}}],"stop_reason":"tool_use"}`, input)
			httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload))}, nil
			})}
			client := newTestResilientClient(APITypeAnthropic, "https://response.test", httpClient, 0)
			message, err := client.CreateChatCompletion(context.Background(), "test-model", nil, nil)
			if err != nil || len(message.ToolCalls) != 2 {
				t.Fatalf("message = %#v, %v", message, err)
			}
			if message.ToolCalls[0].ID != "tool_1" || message.ToolCalls[0].Function.Arguments != input || message.ToolCalls[1].ID != "tool_2" || message.ToolCalls[1].Function.Arguments != `{"command":"echo second"}` {
				t.Fatalf("tool inputs were merged or changed: %#v", message.ToolCalls)
			}
		})
	}
}
