package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestChatStreamToolCallIdentity(t *testing.T) {
	const first = `{"command":"echo first"}`
	const second = `{"command":"echo second"}`
	tests := []struct {
		name    string
		deltas  []string
		want    []string
		wantErr bool
	}{
		{
			name: "missing indexes with distinct IDs",
			deltas: []string{
				`[{"id":"call-1","type":"function","function":{"name":"bash","arguments":"{\"command\":\"echo first\"}"}},{"id":"call-2","type":"function","function":{"name":"bash","arguments":"{\"command\":\"echo second\"}"}}]`,
			},
			want: []string{first, second},
		},
		{
			name: "interleaved fragments identified by ID",
			deltas: []string{
				`[{"id":"call-1","function":{"name":"bash","arguments":"{\"command\":"}},{"id":"call-2","function":{"name":"bash","arguments":"{\"command\":"}}]`,
				`[{"id":"call-2","function":{"arguments":"\"echo second\"}"}},{"id":"call-1","function":{"arguments":"\"echo first\"}"}}]`,
			},
			want: []string{first, second},
		},
		{
			name: "indexed fragments retain index order",
			deltas: []string{
				`[{"index":1,"id":"call-2","function":{"name":"bash","arguments":"{\"command\":"}},{"index":0,"id":"call-1","function":{"name":"bash","arguments":"{\"command\":"}}]`,
				`[{"index":0,"function":{"arguments":"\"echo first\"}"}},{"index":1,"function":{"arguments":"\"echo second\"}"}}]`,
			},
			want: []string{first, second},
		},
		{
			name: "single call without continuation identity",
			deltas: []string{
				`[{"id":"call-1","function":{"name":"bash","arguments":"{\"command\":"}}]`,
				`[{"function":{"arguments":"\"echo first\"}"}}]`,
			},
			want: []string{first},
		},
		{
			name: "index supplied after ID",
			deltas: []string{
				`[{"id":"call-1","function":{"name":"bash","arguments":"{\"command\":"}}]`,
				`[{"index":0,"id":"call-1","function":{"arguments":"\"echo "}}]`,
				`[{"index":0,"function":{"arguments":"first\"}"}}]`,
			},
			want: []string{first},
		},
		{
			name: "ID supplied after index",
			deltas: []string{
				`[{"index":0,"function":{"name":"bash","arguments":"{\"command\":"}}]`,
				`[{"index":0,"id":"call-1","function":{"arguments":"\"echo "}}]`,
				`[{"id":"call-1","function":{"arguments":"first\"}"}}]`,
			},
			want: []string{first},
		},
		{
			name: "same ID changing index is rejected",
			deltas: []string{
				`[{"index":0,"id":"call-1","function":{"name":"bash","arguments":"{\"command\":"}}]`,
				`[{"index":1,"id":"call-1","function":{"arguments":"\"echo first\"}"}}]`,
			},
			wantErr: true,
		},
		{
			name: "index and ID pointing to different calls is rejected",
			deltas: []string{
				`[{"index":0,"id":"call-1","function":{"name":"bash","arguments":"{\"command\":"}},{"index":1,"id":"call-2","function":{"name":"bash","arguments":"{\"command\":"}}]`,
				`[{"index":0,"id":"call-2","function":{"arguments":"\"echo first\"}"}}]`,
			},
			wantErr: true,
		},
		{
			name: "ambiguous continuation is rejected",
			deltas: []string{
				`[{"index":0,"id":"call-1","function":{"name":"bash","arguments":"{\"command\":"}},{"index":1,"id":"call-2","function":{"name":"bash","arguments":"{\"command\":"}}]`,
				`[{"function":{"arguments":"\"echo first\"}"}}]`,
			},
			wantErr: true,
		},
		{
			name: "conflicting index is rejected",
			deltas: []string{
				`[{"index":0,"id":"call-1","function":{"name":"bash","arguments":"{\"command\":\"echo first\"}"}}]`,
				`[{"index":0,"id":"call-2","function":{"name":"bash","arguments":"{\"command\":\"echo second\"}"}}]`,
			},
			wantErr: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stream strings.Builder
			for _, delta := range test.deltas {
				fmt.Fprintf(&stream, "data: {\"choices\":[{\"delta\":{\"tool_calls\":%s}}]}\n\n", delta)
			}
			stream.WriteString("data: [DONE]\n\n")
			requests := 0
			httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				requests++
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(stream.String()))}, nil
			})}
			client := newTestResilientClient(APITypeOpenAI, "https://stream.test", httpClient, 0)
			message, err := client.CreateChatCompletionStream(context.Background(), "test-model", nil, nil, nil)
			if requests != 1 {
				t.Fatalf("got %d requests, want 1 without replaying tool call fragments", requests)
			}
			if test.wantErr {
				var providerErr *ProviderError
				if !errors.As(err, &providerErr) || providerErr.Category != ProviderErrorProtocol {
					t.Fatalf("expected tool identity protocol error, got %v", err)
				}
				if len(message.ToolCalls) != 0 {
					t.Fatalf("protocol failure returned partial tool calls: %#v", message.ToolCalls)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(message.ToolCalls) != len(test.want) {
				t.Fatalf("got %d tool calls, want %d: %#v", len(message.ToolCalls), len(test.want), message.ToolCalls)
			}
			for i, call := range message.ToolCalls {
				if call.ID != fmt.Sprintf("call-%d", i+1) || call.Function.Name != "bash" || call.Function.Arguments != test.want[i] {
					t.Fatalf("tool call %d = %#v, want arguments %q with original identity", i, call, test.want[i])
				}
				if _, err := parseArguments(call.Function.Arguments); err != nil {
					t.Fatalf("tool call %d arguments: %v", i, err)
				}
			}
		})
	}
}
