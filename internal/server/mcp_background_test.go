package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"bqagent/internal/mcp"
	"bqagent/internal/tools"
)

type serviceMCPTransport func(*http.Request) (*http.Response, error)

func (transport serviceMCPTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func TestServiceUsesMCPToolsAfterBackgroundDiscovery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	release := make(chan struct{})
	transport := serviceMCPTransport(func(request *http.Request) (*http.Response, error) {
		var rpc struct {
			ID     *int
			Method string
		}
		if err := json.NewDecoder(request.Body).Decode(&rpc); err != nil {
			return nil, err
		}
		if rpc.Method == "initialize" {
			select {
			case <-release:
			case <-request.Context().Done():
				return nil, request.Context().Err()
			}
		}
		if rpc.ID == nil {
			return &http.Response{StatusCode: http.StatusAccepted, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		result := `{"protocolVersion":"2024-11-05","capabilities":{},"serverInfo":{"name":"mock","version":"1"}}`
		if rpc.Method == "tools/list" {
			result = `{"tools":[{"name":"search","inputSchema":{"type":"object"}}]}`
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":%s}`, *rpc.ID, result)))}, nil
	})
	discovery := mcp.StartDiscovery(ctx, mcp.Config{Servers: map[string]mcp.ServerConfig{"search": {URL: "http://mock.invalid/mcp", Type: "streamable-http"}}}, nil, &http.Client{Transport: transport}, nil, nil, 15*time.Second)
	client := &chatModeRecordingClient{}
	service := NewService(ServiceOptions{WorkspaceRoot: t.TempDir(), Model: "test", Client: client, MCP: discovery, ToolDefinitions: []tools.Definition{{Type: "function", Function: tools.FunctionDefinition{Name: "read_file"}}}})
	done := make(chan struct{})
	var first TurnResponse
	var turnErr error
	go func() {
		first, turnErr = service.HandleTurn(ctx, TurnRequest{Message: "hello"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ordinary turn waited for MCP")
	}
	if turnErr != nil {
		t.Fatal(turnErr)
	}
	if len(client.definitions) == 0 {
		t.Fatalf("initial definitions = %#v", client.definitions)
	}
	initialJSON, _ := json.Marshal(client.definitions[0])
	if strings.Contains(string(initialJSON), "mcp__") || !strings.Contains(string(initialJSON), "read_file") {
		t.Fatal("pending MCP replaced builtins or leaked tools")
	}
	close(release)
	select {
	case <-discovery.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("MCP discovery stalled")
	}
	_, err := service.HandleTurn(ctx, TurnRequest{SessionID: first.SessionID, Message: "search now"})
	if err != nil {
		t.Fatal(err)
	}
	latest := client.definitions[len(client.definitions)-1]
	found := false
	for _, definition := range latest {
		if definition.Function.Name == "mcp__search__search" {
			found = true
		}
	}
	afterJSON, _ := json.Marshal(client.definitions[0])
	if !found || string(afterJSON) != string(initialJSON) {
		t.Fatalf("turn snapshots = %#v", client.definitions)
	}
	if statuses := service.MCPStatus(); len(statuses) != 1 || statuses[0].State != "available" {
		t.Fatalf("status = %#v", statuses)
	}
	// The same chat-mode policy must still filter late-discovered tools.
	defs, functions := discovery.Snapshot()
	filtered, registry := toolsetForChatMode(ChatModeAsk, defs, functions)
	if len(filtered) != 0 || len(registry) != 0 {
		t.Fatal("background tools bypassed ask-mode filtering")
	}
}
