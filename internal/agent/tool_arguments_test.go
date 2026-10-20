package agent

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"bqagent/internal/tools"
)

func TestParseArgumentsConcatenatedObjects(t *testing.T) {
	for _, test := range []struct {
		name     string
		raw      string
		valid    bool
		recovery bool
	}{
		{name: "two commands", raw: `{"command":"echo first"}{"command":"echo second"}`, recovery: true},
		{name: "three objects with whitespace", raw: "{}\n {}\t{}", recovery: true},
		{name: "same command repeated", raw: `{"command":"echo first"}{"command":"echo first"}`, recovery: true},
		{name: "nested braces and escaped quotes", raw: `{"command":"echo \"}{\"","options":{"nested":true}}{"command":"echo second"}`, recovery: true},
		{name: "valid command containing JSON", raw: `{"command":"echo '{\"a\":1}{\"b\":2}'"}`, valid: true},
		{name: "truncated second object", raw: `{"command":"echo first"}{"command":`},
		{name: "trailing garbage", raw: `{"command":"echo first"}garbage`},
		{name: "trailing nonobject", raw: `{"command":"echo first"}[]`},
		{name: "trailing null", raw: `{"command":"echo first"}null`},
	} {
		t.Run(test.name, func(t *testing.T) {
			parsed, err := parseArguments(test.raw)
			if test.valid {
				if err != nil || parsed == nil {
					t.Fatalf("valid arguments rejected: %v", err)
				}
				return
			}
			if err == nil || parsed != nil {
				t.Fatalf("malformed arguments accepted: %#v, %v", parsed, err)
			}
			if strings.Contains(err.Error(), "Reissue each intended operation as a separate tool call") != test.recovery {
				t.Fatalf("unexpected recovery guidance: %v", err)
			}
		})
	}
}

func TestRunRecoversConcatenatedToolArguments(t *testing.T) {
	for _, name := range []string{"grep", "read_file", "write_file", "custom_tool", "execute_bash"} {
		t.Run(name, func(t *testing.T) {
			arguments := []string{`{"pattern":"foo","path":"internal"}`, `{"pattern":"bar","path":"internal"}`}
			key := "pattern"
			want := []string{"foo", "bar"}
			if name == "execute_bash" {
				arguments = []string{`{"command":"echo first"}`, `{"command":"echo second"}`}
				key = "command"
				want = []string{"echo first", "echo second"}
			}
			client := &stubClient{responses: []AssistantMessage{
				{ToolCalls: []ToolCall{{ID: "original", Type: "function", Function: FunctionCall{Name: name, Arguments: strings.Join(arguments, "")}}}},
				{Content: "done"},
			}}
			var executions []string
			var mu sync.Mutex
			recorder := &capturingRecorder{}
			app := NewWithOptions(client, "", Options{
				Context: ContextConfig{Enabled: false}, Recorder: recorder,
				Functions: map[string]tools.Function{name: func(_ context.Context, args map[string]any) (string, error) {
					mu.Lock()
					defer mu.Unlock()
					value, _ := args[key].(string)
					if key == "pattern" && args["path"] != "internal" {
						t.Errorf("path changed: %#v", args)
					}
					executions = append(executions, value)
					return "result: " + value, nil
				}},
			})
			result, history, err := app.RunConversationTurn(context.Background(), []map[string]any{{"role": "user", "content": "analyze"}}, 3)
			if err != nil || result != "done" || len(client.messages) != 2 || !slices.Equal(executions, want) {
				t.Fatalf("Run = %q, %v; requests=%d, executions=%#v", result, err, len(client.messages), executions)
			}
			for _, messages := range [][]map[string]any{history, recorder.recorded, client.messages[1]} {
				var calls []ToolCall
				for _, message := range messages {
					calls = append(calls, messageToolCalls(message)...)
				}
				results := extractToolMessages(messages)
				if len(calls) != 2 || len(results) != 2 || calls[0].ID == calls[1].ID {
					t.Fatalf("expected two distinct call/result pairs: %#v", messages)
				}
				for i, call := range calls {
					if call.ID == "" || call.Function.Name != name || call.Function.Arguments != arguments[i] || results[i]["tool_call_id"] != call.ID || results[i]["content"] != "result: "+want[i] {
						t.Fatalf("unexpected call/result: %#v, %#v", call, results[i])
					}
				}
			}
		})
	}
}

func TestRunRejectsAmbiguousConcatenatedToolArguments(t *testing.T) {
	for _, name := range []string{"grep", "write_file", "execute_bash"} {
		invalid := []string{
			`{"command":"echo first"}{"command":`,
			`{"command":"echo first"}{"command":"echo second"}junk`,
			`{"command":"echo first"}[]`,
			`{"command":"echo first"}null`,
			`[]{"command":"echo second"}`, `null`, `[]`,
			strings.Repeat(`{"command":"echo first"}`, maxRecoveredToolCalls+1),
		}
		if name == "execute_bash" {
			for _, object := range []string{
				`{}`, `{"command":null}`, `{"command":" "}`, `{"command":1}`,
				`{"command":"echo second","timeout":1}`,
				`{"command":"echo second","command":"echo hidden"}`,
			} {
				invalid = append(invalid, `{"command":"echo first"}`+object)
			}
		}
		for _, raw := range invalid {
			t.Run(name+"/"+raw, func(t *testing.T) {
				client := &stubClient{responses: []AssistantMessage{
					{ToolCalls: []ToolCall{{ID: "invalid", Function: FunctionCall{Name: name, Arguments: raw}}}},
					{Content: "done"},
				}}
				app := NewWithOptions(client, "", Options{
					Context: ContextConfig{Enabled: false},
					Functions: map[string]tools.Function{name: func(context.Context, map[string]any) (string, error) {
						t.Error("ambiguous arguments reached a tool")
						return "", nil
					}},
				})
				result, err := app.Run(context.Background(), "analyze", 3)
				if err != nil || result != "done" || len(client.messages) != 2 {
					t.Fatalf("Run = %q, %v", result, err)
				}
				results := extractToolMessages(client.messages[1])
				if len(results) != 1 || results[0]["tool_call_id"] != "invalid" || !strings.HasPrefix(fmt.Sprint(results[0]["content"]), "Error:") {
					t.Fatalf("missing invalid argument result: %#v", results)
				}
			})
		}
	}
}
