package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestResponsesStreamFinalToolArguments(t *testing.T) {
	const first = `{"command":"sed -n '120,190p' /tmp/service.log"}`
	const second = `{"command":"sed -n '804,942p' /tmp/service.log"}`
	item := func(id, args string) map[string]any {
		return map[string]any{"type": "function_call", "id": "fc_" + id, "call_id": id, "name": "bash", "arguments": args}
	}
	itemEvent := func(kind string, index int, id, args string) map[string]any {
		return map[string]any{"type": "response.output_item." + kind, "output_index": index, "item": item(id, args)}
	}
	delta := func(index int, args string) map[string]any {
		return map[string]any{"type": "response.function_call_arguments.delta", "output_index": index, "delta": args}
	}
	done := func(index int, args string) map[string]any {
		return map[string]any{"type": "response.function_call_arguments.done", "output_index": index, "arguments": args}
	}
	terminal := func(kind string, output ...any) map[string]any {
		response := map[string]any{"status": kind, "usage": map[string]any{"input_tokens": 3, "output_tokens": 2}}
		if output != nil {
			response["output"] = output
		}
		if kind == "incomplete" {
			response["incomplete_details"] = map[string]any{"reason": "max_output_tokens"}
		}
		return map[string]any{"type": "response." + kind, "response": response}
	}
	call := func(id, args string) ToolCall {
		return ToolCall{ID: id, Type: "function", Function: FunctionCall{Name: "bash", Arguments: args}}
	}
	tests := []struct {
		name      string
		events    []map[string]any
		want      []ToolCall
		truncated bool
	}{
		{
			name:   "done replaces concatenated argument deltas",
			events: []map[string]any{itemEvent("added", 1, "call_1", ""), delta(1, first), delta(1, second), done(1, first), terminal("completed")},
			want:   []ToolCall{call("call_1", first)},
		},
		{
			name:   "done with empty arguments replaces deltas",
			events: []map[string]any{itemEvent("added", 1, "call_1", ""), delta(1, first), done(1, ""), terminal("completed")},
			want:   []ToolCall{call("call_1", "")},
		},
		{
			name:   "terminal output replaces merged calls",
			events: []map[string]any{itemEvent("added", 0, "call_1", ""), delta(0, first+second), terminal("completed", item("call_1", first), item("call_2", second))},
			want:   []ToolCall{call("call_1", first), call("call_2", second)},
		},
		{
			name:   "terminal output alone",
			events: []map[string]any{terminal("completed", item("call_1", first))},
			want:   []ToolCall{call("call_1", first)},
		},
		{
			name:   "terminal empty output preserves streamed calls",
			events: []map[string]any{itemEvent("added", 0, "call_1", ""), delta(0, first), terminal("completed", []any{}...)},
			want:   []ToolCall{call("call_1", first)},
		},
		{
			name:   "invalid final arguments remain invalid for recovery",
			events: []map[string]any{itemEvent("added", 0, "call_1", ""), delta(0, first), terminal("completed", item("call_1", first+second))},
			want:   []ToolCall{call("call_1", first+second)},
		},
		{
			name:   "delta only fallback preserves split chunks and ordering",
			events: []map[string]any{itemEvent("added", 2, "call_2", ""), itemEvent("added", 1, "call_1", ""), delta(1, first[:15]), delta(2, second), delta(1, first[15:]), terminal("completed")},
			want:   []ToolCall{call("call_1", first), call("call_2", second)},
		},
		{
			name:   "output item done snapshot still replaces deltas",
			events: []map[string]any{itemEvent("added", 0, "call_1", ""), delta(0, first+second), itemEvent("done", 0, "call_1", second), terminal("completed")},
			want:   []ToolCall{call("call_1", second)},
		},
		{
			name:   "incomplete final snapshot preserves truncation guard",
			events: []map[string]any{terminal("incomplete", item("call_1", first))},
			want:   []ToolCall{call("call_1", first)}, truncated: true,
		},
		{
			name:   "incomplete empty snapshot preserves truncation guard",
			events: []map[string]any{itemEvent("added", 0, "call_1", ""), delta(0, first), terminal("incomplete", []any{}...)},
			want:   []ToolCall{call("call_1", first)}, truncated: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stream strings.Builder
			stream.WriteString("data: {\"type\":\"response.output_text.delta\",\"delta\":\"checking\"}\n\n")
			for _, event := range test.events {
				encoded, err := json.Marshal(event)
				if err != nil {
					t.Fatal(err)
				}
				fmt.Fprintf(&stream, "data: %s\n\n", encoded)
			}
			httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(stream.String()))}, nil
			})}
			client := newTestResilientClient(APITypeOpenAIResponse, "https://stream.test", httpClient, 0)
			var chunks strings.Builder
			message, err := client.CreateChatCompletionStream(context.Background(), "test-model", nil, nil, func(chunk string) { chunks.WriteString(chunk) })
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(message.ToolCalls, test.want) {
				t.Fatalf("tool calls = %#v, want %#v", message.ToolCalls, test.want)
			}
			wantContent := "checking"
			// A nonempty terminal snapshot replaces intermediate commentary.
			terminalResponse := test.events[len(test.events)-1]["response"].(map[string]any)
			if output, _ := terminalResponse["output"].([]any); len(output) > 0 {
				wantContent = ""
			}
			if chunks.String() != "checking" || message.FinalContent() != wantContent || message.Usage.TotalTokens != 5 || message.Completion.OutputTruncated != test.truncated {
				t.Fatalf("stream content, usage or completion lost: chunks=%q message=%#v", chunks.String(), message)
			}
		})
	}
}

func TestResponsesStreamMarksHiddenReasoning(t *testing.T) {
	for _, test := range []struct {
		name     string
		item     string
		delta    string
		hidden   bool
		streamed bool
	}{
		{name: "encrypted reasoning item", item: "reasoning", hidden: true},
		{name: "message item", item: "message"},
		// A summary is written after hidden reasoning, not the reasoning itself.
		{name: "reasoning summary", item: "message", delta: "response.reasoning_summary_text.delta", hidden: true},
		{name: "raw reasoning text", item: "message", delta: "response.reasoning_text.delta", streamed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			stream := fmt.Sprintf("data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":%q,\"id\":\"item_1\"}}\n\n", test.item)
			if test.delta != "" {
				stream += fmt.Sprintf("data: {\"type\":%q,\"delta\":\"think\"}\n\n", test.delta)
			}
			stream +=
				"data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n" +
					"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":3,\"output_tokens\":2}}}\n\n"
			httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(stream))}, nil
			})}
			client := newTestResilientClient(APITypeOpenAIResponse, "https://stream.test", httpClient, 0)
			sampler := &streamSampler{now: time.Now}
			ctx := context.WithValue(context.Background(), outputSamplerKey{}, sampler)
			if _, err := client.CreateChatCompletionStream(ctx, "test-model", nil, nil, nil); err != nil {
				t.Fatal(err)
			}
			if sampler.hiddenReasoning != test.hidden || sampler.sawReasoning != test.streamed {
				t.Fatalf("hidden = %v, streamed = %v; want %v, %v", sampler.hiddenReasoning, sampler.sawReasoning, test.hidden, test.streamed)
			}
		})
	}
}

func TestAnthropicStreamMarksHiddenReasoning(t *testing.T) {
	for _, test := range []struct {
		name     string
		block    string
		hidden   bool
		streamed bool
	}{
		{name: "redacted thinking", block: `{"type":"content_block_start","index":0,"content_block":{"type":"redacted_thinking"}}`, hidden: true},
		{name: "thinking delta", block: `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"think"}}`, streamed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			stream := ""
			for _, event := range []string{
				`{"type":"message_start","message":{"usage":{"input_tokens":3,"output_tokens":0}}}`,
				test.block,
				`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"ok"}}`,
				`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
				`{"type":"message_stop"}`,
			} {
				stream += "data: " + event + "\n\n"
			}
			httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(stream))}, nil
			})}
			client := newTestResilientClient(APITypeAnthropic, "https://stream.test", httpClient, 0)
			sampler := &streamSampler{now: time.Now}
			ctx := context.WithValue(context.Background(), outputSamplerKey{}, sampler)
			if _, err := client.CreateChatCompletionStream(ctx, "test-model", nil, nil, nil); err != nil {
				t.Fatal(err)
			}
			if sampler.hiddenReasoning != test.hidden || sampler.sawReasoning != test.streamed {
				t.Fatalf("hidden = %v, streamed = %v; want %v, %v", sampler.hiddenReasoning, sampler.sawReasoning, test.hidden, test.streamed)
			}
		})
	}
}
