package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestResponsesPreserveOrderedItemsAndPhase(t *testing.T) {
	output := `[{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"opaque"},{"type":"message","id":"msg_1","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"checking"}]},{"type":"function_call","call_id":"call_1","name":"read_file","arguments":"{\"path\":\"x\"}"}]`
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "json", true: "stream"}[stream], func(t *testing.T) {
			payload := `{"status":"completed","output":` + output + `}`
			if stream {
				payload = "data: {\"type\":\"response.completed\",\"response\":" + payload + "}\n\n"
			}
			client := NewClientWithAPIType("", "http://test.invalid", APITypeOpenAIResponse, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload))}, nil
			})})
			var message AssistantMessage
			var err error
			if stream {
				message, err = client.CreateChatCompletionStream(context.Background(), "test", nil, nil, nil)
			} else {
				message, err = client.CreateChatCompletion(context.Background(), "test", nil, nil)
			}
			if err != nil {
				t.Fatal(err)
			}
			if message.Phase != "commentary" || len(message.ToolCalls) != 1 {
				t.Fatalf("message=%+v", message)
			}
			// Round-trip through JSON exactly as a saved session does.
			encoded, _ := json.Marshal(message.RequestMessage())
			var restored map[string]any
			if err = json.Unmarshal(encoded, &restored); err != nil {
				t.Fatal(err)
			}
			history := []map[string]any{restored, {"role": "tool", "tool_call_id": "call_1", "content": "contents"}, {"role": "user", "content": "continue"}}
			bounded := BoundWorkingMessages(history, ContextConfig{Enabled: false})
			input := openAIResponseInput(bounded, PromptCacheOptions{})
			encoded, _ = json.Marshal(input)
			var actual []map[string]any
			_ = json.Unmarshal(encoded, &actual)
			if len(actual) != 5 || actual[0]["encrypted_content"] != "opaque" || actual[1]["phase"] != "commentary" || actual[2]["call_id"] != "call_1" || actual[3]["type"] != "function_call_output" || actual[4]["role"] != "user" {
				t.Fatalf("replay=%s", encoded)
			}
			chat := openAIChatMessagesWithCache(bounded, PromptCacheOptions{})
			if chat[0]["response_items"] != nil || chat[0]["phase"] != nil || restored["phase"] != "commentary" {
				t.Fatal("metadata leaked or source mutated")
			}
		})
	}
}

func TestResponsesStreamPhaseWithoutTerminalOutput(t *testing.T) {
	payload := `data: {"type":"response.output_item.added","output_index":0,"item":{"type":"message","role":"assistant","phase":"commentary","content":[]}}

data: {"type":"response.output_text.delta","output_index":0,"delta":"working"}

data: {"type":"response.completed","response":{"status":"completed"}}

`
	client := NewClientWithAPIType("", "http://test.invalid", APITypeOpenAIResponse, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload))}, nil
	})})
	message, err := client.CreateChatCompletionStream(context.Background(), "test", nil, nil, nil)
	if err != nil || message.Phase != "commentary" || message.FinalContent() != "working" {
		t.Fatalf("message=%+v err=%v", message, err)
	}
	encoded, _ := json.Marshal(openAIResponseInput([]map[string]any{message.RequestMessage()}, PromptCacheOptions{}))
	if !strings.Contains(string(encoded), "working") || !strings.Contains(string(encoded), "commentary") {
		t.Fatalf("replay=%s", encoded)
	}
}

func TestResponsesSeparateFinalAnswerFromCommentary(t *testing.T) {
	var response openAIResponseEnvelope
	err := json.Unmarshal([]byte(`{"output":[{"type":"message","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"working"}]},{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"done"}]}]}`), &response)
	if err != nil {
		t.Fatal(err)
	}
	message := assistantFromOpenAIResponse(response)
	if message.FinalContent() != "done" || len(responseItemsFromMessage(message.RequestMessage())) != 2 {
		t.Fatalf("message=%+v", message)
	}
	input := openAIResponseInput([]map[string]any{{"role": "assistant", "phase": "commentary", "content": "legacy"}, {"role": "user", "content": "go", "phase": "commentary"}}, PromptCacheOptions{})
	if input[0].(map[string]any)["phase"] != "commentary" || input[1].(map[string]any)["phase"] != nil {
		t.Fatalf("input=%v", input)
	}
}

func TestNativeResponseHistoryPrunesWholeToolGroups(t *testing.T) {
	var response openAIResponseEnvelope
	payload := `{"output":[{"type":"reasoning","summary":[],"encrypted_content":"` + strings.Repeat("x", 4000) + `"},{"type":"function_call","call_id":"large","name":"read_file","arguments":"{}"}]}`
	if err := json.Unmarshal([]byte(payload), &response); err != nil {
		t.Fatal(err)
	}
	message := assistantFromOpenAIResponse(response).RequestMessage()
	history := []map[string]any{{"role": "user", "content": "work"}, message, {"role": "tool", "tool_call_id": "large", "content": "read result"}, {"role": "user", "content": "continue"}}
	pruned := hardPruneMessagesToBudget(history, 100)
	if estimateMessagesTokens(pruned) > 100 {
		t.Fatalf("oversized native payload retained: %d", estimateMessagesTokens(pruned))
	}
	for _, item := range pruned {
		if item["role"] == "tool" || item["response_items"] != nil {
			t.Fatal("oversized tool group left partially present")
		}
	}
	if len(responseItemsFromMessage(message)) != 2 {
		t.Fatal("original history mutated")
	}
}

func TestNativeResponseHistoryReplaysRecoveredCalls(t *testing.T) {
	var response openAIResponseEnvelope
	if err := json.Unmarshal([]byte(`{"output":[{"type":"message","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"work"}]},{"type":"function_call","id":"fc1","call_id":"a","name":"bash","arguments":"{}"},{"type":"function_call","id":"fc2","call_id":"b","name":"read_file","arguments":"{}"}]}`), &response); err != nil {
		t.Fatal(err)
	}
	message := assistantFromOpenAIResponse(response)
	message.ToolCalls = []ToolCall{{ID: "a", Function: FunctionCall{Name: "bash", Arguments: `{"command":"one"}`}}, {ID: "split", Function: FunctionCall{Name: "bash", Arguments: `{"command":"two"}`}}, {ID: "b", Function: FunctionCall{Name: "read_file", Arguments: `{}`}}}
	items := responseItemsFromMessage(message.RequestMessage())
	if len(items) != 4 {
		t.Fatalf("items=%s", items)
	}
	for i, want := range []string{"a", "split", "b"} {
		var item map[string]any
		_ = json.Unmarshal(items[i+1], &item)
		if item["call_id"] != want || item["id"] != nil {
			t.Fatalf("recovered item=%v", item)
		}
	}
}
