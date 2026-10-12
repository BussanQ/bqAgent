package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"bqagent/internal/tools"
)

func TestResponsesStreamEmptyTerminalOutput(t *testing.T) {
	const delta = `data: {"type":"response.output_text.delta","output_index":0,"delta":"answer"}` + "\n\n"
	const added = `data: {"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_1","role":"assistant","phase":"final_answer","content":[]}}` + "\n\n"
	const done = `data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"msg_1","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"answer"}]}}` + "\n\n"
	const empty = `data: {"type":"response.completed","response":{"status":"completed","output":[],"usage":{"input_tokens":3,"output_tokens":2}}}` + "\n\n"
	const omitted = `data: {"type":"response.completed","response":{"status":"completed"}}` + "\n\n"
	const revised = `data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"revised answer"}]}]}}` + "\n\n"
	const reasoning = `data: {"type":"response.output_item.done","output_index":1,"item":{"type":"reasoning","id":"rs_1","summary":[]}}` + "\n\n"
	for _, test := range []struct {
		name, stream, want, chunks, phase string
	}{
		{"delta with empty snapshot", delta + empty, "answer", "answer", ""},
		{"delta with omitted snapshot", delta + omitted, "answer", "answer", ""},
		{"message phase with empty snapshot", added + delta + empty, "answer", "answer", "final_answer"},
		{"completed item without deltas", done + empty, "answer", "answer", "final_answer"},
		{"completed item and deltas do not duplicate", added + delta + done + empty, "answer", "answer", "final_answer"},
		{"reasoning item does not hide text deltas", reasoning + delta + empty, "answer", "answer", ""},
		{"nonempty snapshot remains authoritative", delta + revised, "revised answer", "answer", "final_answer"},
		{"truly empty response remains empty", empty, "", "", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := newTestResilientClient(APITypeOpenAIResponse, "https://stream.test", &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(test.stream))}, nil
			})}, 0)
			var chunks strings.Builder
			message, err := client.CreateChatCompletionStream(context.Background(), "test-model", nil, nil, func(chunk string) { chunks.WriteString(chunk) })
			if err != nil {
				t.Fatal(err)
			}
			if message.FinalContent() != test.want || chunks.String() != test.chunks || message.Phase != test.phase {
				t.Fatalf("content=%q chunks=%q phase=%q", message.FinalContent(), chunks.String(), message.Phase)
			}
			var diagnostic map[string]any
			if err := json.Unmarshal([]byte(message.ResponseDiagnostics), &diagnostic); err != nil {
				t.Fatalf("invalid diagnostics: %v", err)
			}
			if strings.Contains(message.ResponseDiagnostics, "answer") {
				t.Fatal("diagnostics leaked response text")
			}
			if strings.HasSuffix(test.stream, empty) && (diagnostic["terminal_output_items"] != float64(0) || diagnostic["terminal_output_present"] != true || message.Usage.TotalTokens != 5) {
				t.Fatalf("lost terminal diagnostics or usage: %s usage=%+v", message.ResponseDiagnostics, message.Usage)
			}
			if test.phase != "" {
				persisted := message.RequestMessage()
				if persisted["phase"] != test.phase || len(message.ResponseItems) != 1 {
					t.Fatalf("lost replay metadata: %#v", persisted)
				}
			}
		})
	}
}

func TestResponsesStreamEmptyOutputDoesNotMaskProtocolErrors(t *testing.T) {
	for _, terminal := range []string{
		"",
		`data: {"type":"response.failed","error":{"message":"upstream failure"}}` + "\n\n",
	} {
		t.Run(terminal, func(t *testing.T) {
			stream := `data: {"type":"response.output_text.delta","delta":"partial"}` + "\n\n" + terminal
			client := newTestResilientClient(APITypeOpenAIResponse, "https://stream.test", &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(stream))}, nil
			})}, 0)
			if _, err := client.CreateChatCompletionStream(context.Background(), "test-model", nil, nil, nil); err == nil {
				t.Fatal("partial text must not mask a failed or unterminated response")
			}
		})
	}
}

func TestRunConversationPreservesToolsAndAnswerWithEmptyTerminalOutput(t *testing.T) {
	const terminal = `data: {"type":"response.completed","response":{"status":"completed","output":[]}}` + "\n\n"
	requests, toolRuns := 0, 0
	client := newTestResilientClient(APITypeOpenAIResponse, "https://stream.test", &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		requests++
		var stream string
		switch requests {
		case 1:
			stream = `data: {"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","call_id":"read_1","name":"read_file","arguments":"{\"path\":\"SKILL.md\"}"}}` + "\n\n" + terminal
		case 2:
			stream = `data: {"type":"response.output_text.delta","output_index":0,"delta":"Task completed."}` + "\n\n" + terminal
		default:
			t.Fatal("unexpected empty-response retry")
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(stream))}, nil
	})}, 0)
	app := NewWithOptions(client, "test-model", Options{
		Context: ContextConfig{Enabled: false}, Stream: true,
		Functions: map[string]tools.Function{"read_file": func(context.Context, map[string]any) (string, error) {
			toolRuns++
			return "Skill instructions", nil
		}},
	})
	result, _, err := app.RunConversationTurn(context.Background(), []map[string]any{{"role": "user", "content": "read the skill and finish"}}, 6)
	if err != nil || result != "Task completed." || requests != 2 || toolRuns != 1 {
		t.Fatalf("result=%q err=%v requests=%d toolRuns=%d", result, err, requests, toolRuns)
	}
}
