package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"bqagent/internal/tools"
)

func TestRecoverConcatenatedBashCalls(t *testing.T) {
	const first = `{"command":"echo '{\"a\":1}{\"b\":2}'"}`
	const second = `{"command":"echo second"}`
	tests := []struct {
		name string
		raw  string
		want []string
	}{
		{name: "complete commands", raw: first + second, want: []string{first, second}},
		{name: "whitespace", raw: "\n" + first + " \t\n" + second + "\n", want: []string{first, second}},
		{name: "single object unchanged", raw: first},
		{name: "duplicate command", raw: first + first, want: []string{first, first}},
		{name: "ten identical commands", raw: strings.Repeat(`{"command":"ls"}`, 10), want: slices.Repeat([]string{`{"command":"ls"}`}, 10)},
		{name: "maximum identical commands", raw: strings.Repeat(first, maxRecoveredBashCalls), want: slices.Repeat([]string{first}, maxRecoveredBashCalls)},
		{name: "too many identical commands", raw: strings.Repeat(first, maxRecoveredBashCalls+1)},
		{name: "duplicate command keys", raw: `{"command":"echo first","command":"echo hidden"}` + second},
		{name: "truncated suffix", raw: first + `{"command":`},
		{name: "garbage suffix", raw: first + second + "junk"},
		{name: "nonobject suffix", raw: first + `[]`},
		{name: "missing command", raw: first + `{}`},
		{name: "null command", raw: first + `{"command":null}`},
		{name: "empty command", raw: first + `{"command":" "}`},
		{name: "extra fields", raw: first + `{"command":"echo second","timeout":1}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			original := ToolCall{ID: "tool_original", Type: "function", Function: FunctionCall{Name: "execute_bash", Arguments: test.raw}}
			input := []ToolCall{original}
			calls, changed := recoverConcatenatedBashCalls(input)
			if input[0] != original {
				t.Fatal("recovery mutated the provider response")
			}
			if test.want == nil {
				if changed || !slices.Equal(calls, input) {
					t.Fatalf("ambiguous input recovered: %#v", calls)
				}
				return
			}
			if !changed || len(calls) != len(test.want) || calls[0].ID != original.ID {
				t.Fatalf("unexpected recovered calls: %#v", calls)
			}
			ids := map[string]bool{}
			for i, call := range calls {
				if call.Function.Arguments != test.want[i] || call.Function.Name != "execute_bash" || call.Type != "function" || call.ID == "" || len(call.ID) > 64 || ids[call.ID] {
					t.Fatalf("invalid recovered call: %#v", call)
				}
				ids[call.ID] = true
			}
			again, changedAgain := recoverConcatenatedBashCalls(calls)
			if changedAgain || !slices.Equal(again, calls) {
				t.Fatal("recovery is not idempotent")
			}
			collision := ToolCall{ID: calls[1].ID, Function: FunctionCall{Name: "read_file", Arguments: `{"path":"README.md"}`}}
			withCollision, _ := recoverConcatenatedBashCalls([]ToolCall{original, collision})
			if withCollision[1].ID == collision.ID || withCollision[len(test.want)] != collision {
				t.Fatalf("recovered ID collided with another call: %#v", withCollision)
			}
		})
	}
	var excessive strings.Builder
	for i := 0; i <= maxRecoveredBashCalls; i++ {
		fmt.Fprintf(&excessive, `{"command":"echo %d"}`, i)
	}
	if parts := concatenatedBashCommands(excessive.String()); parts != nil {
		t.Fatal("oversized batch was recovered")
	}
}

func TestRunRecoversBashArgumentsAcrossProviders(t *testing.T) {
	for _, commands := range [][]string{{"echo first", "echo second"}, slices.Repeat([]string{"ls"}, 10)} {
		var raw string
		for _, command := range commands {
			encoded, err := json.Marshal(map[string]string{"command": command})
			if err != nil {
				t.Fatal(err)
			}
			raw += string(encoded)
		}
		for _, apiType := range []APIType{APITypeOpenAI, APITypeOpenAIResponse, APITypeAnthropic} {
			t.Run(fmt.Sprintf("%s/%d_commands", apiType, len(commands)), func(t *testing.T) {
				var executions []string
				var mu sync.Mutex
				requests := 0
				httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					requests++
					body := ""
					if requests == 1 {
						switch apiType {
						case APITypeOpenAI:
							body = fmt.Sprintf("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"original\",\"type\":\"function\",\"function\":{\"name\":\"execute_bash\",\"arguments\":%q}}]}}]}\n\ndata: [DONE]\n\n", raw)
						case APITypeOpenAIResponse:
							// The invalid data is in the authoritative final snapshot,
							// not only in deltas. No provider-level repair is possible.
							body = fmt.Sprintf("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"function_call\",\"call_id\":\"original\",\"name\":\"execute_bash\",\"arguments\":%q}]}}\n\n", raw)
						case APITypeAnthropic:
							body = "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"original\",\"name\":\"execute_bash\",\"input\":{}}}\n\n"
							body += fmt.Sprintf("data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":%q}}\n\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\ndata: {\"type\":\"message_stop\"}\n\n", raw)
						}
					} else {
						mu.Lock()
						if !slices.Equal(executions, commands) {
							t.Errorf("commands did not execute exactly once in order before the next model request: %#v", executions)
						}
						mu.Unlock()
						body = providerStreamSuccess(apiType, "done")
					}
					return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
				})}
				client := newTestResilientClient(apiType, "https://stream.test", httpClient, 0)
				recorder := &capturingRecorder{}
				app := NewWithOptions(client, "test-model", Options{
					Stream: true, APIType: apiType, Context: ContextConfig{Enabled: false}, Recorder: recorder,
					Functions: map[string]tools.Function{"execute_bash": func(_ context.Context, args map[string]any) (string, error) {
						mu.Lock()
						defer mu.Unlock()
						command := args["command"].(string)
						executions = append(executions, command)
						return command, nil
					}},
				})
				result, history, err := app.RunConversationTurn(context.Background(), []map[string]any{{"role": "user", "content": "analyze"}}, 4)
				if err != nil || result != "done" || requests != 2 {
					t.Fatalf("Run = %q, %v; requests=%d", result, err, requests)
				}
				var calls []ToolCall
				for _, message := range history {
					calls = append(calls, messageToolCalls(message)...)
				}
				results := extractToolMessages(history)
				if len(calls) != len(commands) || len(results) != len(commands) {
					t.Fatalf("unpaired recovery history: %#v", history)
				}
				for i, call := range calls {
					if !json.Valid([]byte(call.Function.Arguments)) || results[i]["tool_call_id"] != call.ID || results[i]["content"] != executions[i] {
						t.Fatalf("invalid recovery history: %#v, %#v", call, results[i])
					}
				}
				if recorded := messageToolCalls(recorder.recorded[0]); !slices.Equal(recorded, calls) {
					t.Fatalf("persisted calls differ from executed calls: %#v", recorded)
				}
			})
		}
	}
}

func TestRecoveredBashLoopProtection(t *testing.T) {
	call := ToolCall{ID: "original", Function: FunctionCall{Name: "execute_bash", Arguments: `{"command":"ls"}`}}
	for _, test := range []struct {
		name     string
		result   string
		newBatch bool
		stopAt   int
		toolName string
	}{
		{name: "one successful batch", result: "listing", stopAt: 0},
		{name: "repeated batches", result: "listing", newBatch: true, stopAt: 4},
		{name: "failures within a batch", result: "Error: command failed", stopAt: 3},
		{name: "other tools retain protection", result: "contents", toolName: "read_file", stopAt: 4},
	} {
		t.Run(test.name, func(t *testing.T) {
			call := call
			if test.toolName != "" {
				call.Function.Name = test.toolName
			}
			guard := newLoopGuard(StageConfig{LoopProtection: true, DuplicateCallLimit: 4, RepeatedFailureLimit: 3})
			successes := map[string]bool{}
			for i := 1; i <= 10; i++ {
				if test.newBatch {
					successes = map[string]bool{}
				}
				action := guard.observeRecoveredTool(call, test.result, successes)
				wantStop := test.stopAt > 0 && i >= test.stopAt
				if (action.checkpointReason != "") != wantStop {
					t.Fatalf("call %d: checkpoint=%q, wantStop=%t", i, action.checkpointReason, wantStop)
				}
			}
		})
	}
}

func TestRunDoesNotRecoverTruncatedBashBatch(t *testing.T) {
	client := &stubClient{responses: []AssistantMessage{
		{Completion: CompletionState{OutputTruncated: true}, ToolCalls: []ToolCall{{ID: "truncated", Function: FunctionCall{
			Name: "execute_bash", Arguments: `{"command":"echo first"}{"command":"echo second"}`,
		}}}},
		{Content: "done"},
	}}
	app := NewWithOptions(client, "", Options{
		Context: ContextConfig{Enabled: false},
		Functions: map[string]tools.Function{"execute_bash": func(context.Context, map[string]any) (string, error) {
			t.Error("truncated batch executed")
			return "", nil
		}},
	})
	result, err := app.Run(context.Background(), "analyze", 3)
	if err != nil || result != "done" || len(client.messages) != 2 {
		t.Fatalf("Run = %q, %v", result, err)
	}
	request := client.messages[1]
	if !strings.Contains(fmt.Sprint(request[len(request)-1]["content"]), "No tools from that batch were executed") {
		t.Fatalf("truncation guard was bypassed: %#v", request)
	}
}

func TestRunRecoversTenIdenticalBashCalls(t *testing.T) {
	for _, protection := range []bool{false, true} {
		t.Run(fmt.Sprintf("loop_protection=%t", protection), func(t *testing.T) {
			const args = `{"command": "ls"}`
			client := &stubClient{responses: []AssistantMessage{
				{ToolCalls: []ToolCall{{ID: "original", Type: "function", Function: FunctionCall{Name: "execute_bash", Arguments: strings.Repeat(args, 10)}}}},
				{Content: "done"},
			}}
			executions := 0
			recorder := &capturingRecorder{}
			app := NewWithOptions(client, "", Options{
				Context: ContextConfig{Enabled: false}, Recorder: recorder,
				Stage: StageConfig{LoopProtection: protection},
				Functions: map[string]tools.Function{"execute_bash": func(_ context.Context, arguments map[string]any) (string, error) {
					if arguments["command"] != "ls" {
						t.Fatalf("unexpected arguments: %#v", arguments)
					}
					executions++
					return fmt.Sprintf("listing %d", executions), nil
				}},
			})
			result, history, err := app.RunConversationTurn(context.Background(), []map[string]any{{"role": "user", "content": "执行10次ls工具"}}, 3)
			if err != nil || result != "done" || executions != 10 || len(client.messages) != 2 {
				t.Fatalf("Run = %q, %v; executions=%d, requests=%d", result, err, executions, len(client.messages))
			}
			calls := messageToolCalls(recorder.recorded[0])
			results := extractToolMessages(history)
			if len(calls) != 10 || len(results) != 10 || len(extractToolMessages(client.messages[1])) != 10 {
				t.Fatalf("recovered calls/results lost: calls=%d, results=%d", len(calls), len(results))
			}
			ids := map[string]bool{}
			for i, call := range calls {
				if call.ID == "" || ids[call.ID] || call.Function.Arguments != args || results[i]["tool_call_id"] != call.ID || results[i]["content"] != fmt.Sprintf("listing %d", i+1) {
					t.Fatalf("invalid recovered history: %#v, %#v", call, results[i])
				}
				ids[call.ID] = true
			}
		})
	}
}

func TestRunStopsRepeatedInvalidArgumentsWithoutStageProtection(t *testing.T) {
	for _, name := range []string{"execute_bash", "write_file"} {
		for _, raw := range []string{`{"command":`, `{"command":"echo repeated"}{"command":null}`, `[]`} {
			t.Run(name+raw, func(t *testing.T) {
				client := &stubClient{}
				for i := 0; i < 3; i++ {
					client.responses = append(client.responses, AssistantMessage{ToolCalls: []ToolCall{{ID: fmt.Sprintf("retry-%d", i), Function: FunctionCall{Name: name, Arguments: raw}}}})
				}
				client.responses = append(client.responses, AssistantMessage{Content: "stopped invalid argument loop"})
				app := NewWithOptions(client, "", Options{
					Context: ContextConfig{Enabled: false},
					Functions: map[string]tools.Function{name: func(context.Context, map[string]any) (string, error) {
						t.Error("invalid arguments reached a tool")
						return "", nil
					}},
				})
				result, err := app.Run(context.Background(), "analyze", 20)
				if err != nil || result != "stopped invalid argument loop" || len(client.messages) != 4 || len(client.definitions[3]) != 0 {
					t.Fatalf("invalid argument loop did not stop with a tool-free summary: %q, %v", result, err)
				}
				lastRequest := client.messages[3]
				if !strings.Contains(fmt.Sprint(lastRequest[len(lastRequest)-1]["content"]), "repeated invalid arguments") {
					t.Fatalf("missing loop stop reason: %#v", lastRequest)
				}
			})
		}
	}
}
