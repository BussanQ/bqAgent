package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
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
	// The reported Bash commands, with only the private path replaced. Tool
	// implementations are stubs; no shell commands or filesystem reads run.
	commands := []string{
		"f=/tmp/service.log\nsed -n '805,853p' $f | cut -c1-250; echo ======; sed -n '854,891p' $f | cut -c1-250",
		"f=/tmp/service.log\nsed -n '93,186p' $f | cut -c1-250; echo =====; sed -n '467,612p' $f | cut -c1-250",
	}
	arguments := make([]string, len(commands))
	for i, command := range commands {
		encoded, err := json.Marshal(map[string]string{"command": command})
		if err != nil {
			t.Fatal(err)
		}
		arguments[i] = string(encoded)
	}
	// Exercise both concurrent and special/sequential dispatch branches.
	for _, name := range []string{"execute_bash", "write_file"} {
		t.Run(name, func(t *testing.T) {
			call := func(id, args string) ToolCall {
				return ToolCall{ID: id, Type: "function", Function: FunctionCall{Name: name, Arguments: args}}
			}
			client := &stubClient{responses: []AssistantMessage{
				{ToolCalls: []ToolCall{call("bad", arguments[0]+arguments[1])}},
				{ToolCalls: []ToolCall{call("fixed-1", arguments[0]), call("fixed-2", arguments[1])}},
				{Content: "done"},
			}}
			wantRequests := 3
			if name == "execute_bash" {
				// The model never repairs its response. The runtime must execute
				// both commands before asking the model for the final answer.
				client.responses = append(client.responses[:1], AssistantMessage{Content: "done"})
				wantRequests = 2
			}
			var executions [2]atomic.Int32
			app := NewWithOptions(client, "", Options{
				Context: ContextConfig{Enabled: false},
				Functions: map[string]tools.Function{name: func(_ context.Context, args map[string]any) (string, error) {
					for i, command := range commands {
						if args["command"] == command {
							executions[i].Add(1)
							return "ok", nil
						}
					}
					t.Errorf("unexpected command: %#v", args)
					return "", nil
				}},
				ToolDefinitions: []tools.Definition{{Type: "function", Function: tools.FunctionDefinition{Name: name}}},
			})
			result, err := app.Run(context.Background(), "analyze the service log", 3)
			if err != nil || result != "done" || len(client.messages) != wantRequests {
				t.Fatalf("Run = %q, %v, requests=%d", result, err, len(client.messages))
			}
			for i := range executions {
				if executions[i].Load() != 1 {
					t.Fatalf("command %d executed %d times, want exactly once after correction", i, executions[i].Load())
				}
			}
			messages := extractToolMessages(client.messages[1])
			if name == "execute_bash" {
				if len(messages) != 2 || messages[0]["tool_call_id"] == messages[1]["tool_call_id"] {
					t.Fatalf("expected two distinct results after automatic recovery: %#v", messages)
				}
				for _, message := range messages {
					if message["content"] != "ok" {
						t.Fatalf("automatic recovery failed: %#v", message)
					}
				}
				return
			}
			if len(messages) != 1 || messages[0]["tool_call_id"] != "bad" {
				t.Fatalf("recovery tool results = %#v", messages)
			}
			content, _ := messages[0]["content"].(string)
			for _, expected := range []string{"2 concatenated JSON objects", "This tool call was not executed", "separate tool call", "unique call ID"} {
				if !strings.Contains(content, expected) {
					t.Fatalf("tool error %q missing %q", content, expected)
				}
			}
		})
	}
}
