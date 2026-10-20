package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
)

func TestResponsesStreamToolCallIdentity(t *testing.T) {
	const a = `{"command":"a"}`
	const b = `{"command":"b"}`
	added := func(idx, id, call string) string {
		return `{"type":"response.output_item.added",` + idx + `"item":{"type":"function_call","id":"` + id + `","call_id":"` + call + `","name":"execute_bash"}}`
	}
	delta := func(idx, itemID, d string) string {
		return `{"type":"response.function_call_arguments.delta",` + idx + `"item_id":"` + itemID + `","delta":` + quoteJSON(d) + `}`
	}
	done := `{"type":"response.completed","response":{"status":"completed"}}`
	call := func(id, args string) ToolCall {
		return ToolCall{ID: id, Type: "function", Function: FunctionCall{Name: "execute_bash", Arguments: args}}
	}
	tests := []struct {
		name    string
		events  []string
		want    []ToolCall
		wantErr string
	}{
		{"distinct calls stay separate", []string{added(`"output_index":0,`, "fc_1", "c1"), added(`"output_index":1,`, "fc_2", "c2"), delta(`"output_index":0,`, "fc_1", a), delta(`"output_index":1,`, "fc_2", b), done}, []ToolCall{call("c1", a), call("c2", b)}, ""},
		{"missing index routes by item_id not zero", []string{added(`"output_index":0,`, "fc_1", "c1"), added(`"output_index":1,`, "fc_2", "c2"), delta("", "fc_2", b), done}, []ToolCall{call("c1", ""), call("c2", b)}, ""},
		{"new call without index rejected", []string{added(`"output_index":0,`, "fc_1", "c1"), added("", "fc_2", "c2"), done}, nil, "missing output_index"},
		{"empty delta without identity ignored", []string{added(`"output_index":0,`, "fc_1", "c1"), delta("", "", ""), done}, []ToolCall{call("c1", "")}, ""},
		{"negative index rejected", []string{added(`"output_index":-1,`, "fc_1", "c1"), done}, nil, "negative output_index"},
		{"arguments.done item_id conflict rejected", []string{added(`"output_index":0,`, "fc_1", "c1"), `{"type":"response.function_call_arguments.done","output_index":0,"item_id":"fc_9","arguments":"{}"}`, done}, nil, "identity conflict"},
		{"arguments.done locates by item_id", []string{added(`"output_index":0,`, "fc_1", "c1"), added(`"output_index":1,`, "fc_2", "c2"), `{"type":"response.function_call_arguments.done","item_id":"fc_2","arguments":` + quoteJSON(b) + `}`, done}, []ToolCall{call("c1", ""), call("c2", b)}, ""},
		{"item.done conflicting call_id rejected", []string{added(`"output_index":0,`, "fc_1", "c1"), `{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"c9","name":"execute_bash","arguments":"{}"}}`, done}, nil, "identity conflict"},
		{"item.done tool name change rejected", []string{added(`"output_index":0,`, "fc_1", "c1"), `{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"c1","name":"other","arguments":"{}"}}`, done}, nil, "tool name changed"},
		{"item.id and item_id mismatch rejected", []string{`{"type":"response.output_item.added","output_index":0,"item_id":"fc_2","item":{"type":"function_call","id":"fc_1","call_id":"c1","name":"execute_bash"}}`, done}, nil, "differs from item_id"},
		{"identity ending in colon routes correctly", []string{added(`"output_index":0,`, "fc_1:", "c1"), delta("", "fc_1:", a), done}, []ToolCall{call("c1", a)}, ""},
		{"null index routes by item_id", []string{added(`"output_index":0,`, "fc_1", "c1"), added(`"output_index":1,`, "fc_2", "c2"), delta(`"output_index":null,`, "fc_2", b), done}, []ToolCall{call("c1", ""), call("c2", b)}, ""},
		{"missing index unknown item rejected", []string{added(`"output_index":0,`, "fc_1", "c1"), delta("", "fc_x", a), done}, nil, "missing output_index"},
		{"duplicate added rejected", []string{added(`"output_index":0,`, "fc_1", "c1"), added(`"output_index":0,`, "fc_1", "c1"), done}, nil, "added twice"},
		{"different call at same index rejected", []string{added(`"output_index":0,`, "fc_1", "c1"), added(`"output_index":0,`, "fc_2", "c2"), done}, nil, "identity conflict"},
		{"item_id bound to other index rejected", []string{added(`"output_index":0,`, "fc_1", "c1"), added(`"output_index":1,`, "fc_2", "c2"), delta(`"output_index":1,`, "fc_1", a), done}, nil, "identity conflict"},
		{"delta item_id mismatching index rejected", []string{added(`"output_index":0,`, "fc_1", "c1"), delta(`"output_index":0,`, "fc_2", a), done}, nil, "identity conflict"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stream strings.Builder
			for _, event := range test.events {
				stream.WriteString("data: " + event + "\n\n")
			}
			httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(stream.String()))}, nil
			})}
			client := newTestResilientClient(APITypeOpenAIResponse, "https://stream.test", httpClient, 0)
			message, err := client.CreateChatCompletionStream(context.Background(), "test-model", nil, nil, nil)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("error = %v, want containing %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(message.ToolCalls, test.want) {
				t.Fatalf("tool calls = %#v, want %#v", message.ToolCalls, test.want)
			}
		})
	}
}

func quoteJSON(s string) string {
	encoded, _ := json.Marshal(s)
	return string(encoded)
}
