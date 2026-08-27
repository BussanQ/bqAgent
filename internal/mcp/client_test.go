package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// mcpHandler is a tiny in-memory MCP server. When sse is true it replies with
// text/event-stream, otherwise application/json.
func mcpHandler(t *testing.T, sse bool) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var req struct {
			ID     *int            `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("server got bad json: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		// Notifications (no id) get an empty 202.
		if req.ID == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}

		var result any
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "sess-123")
			result = map[string]any{
				"protocolVersion": protocolVersion,
				"capabilities":    map[string]any{},
				"serverInfo":      map[string]any{"name": "fake", "version": "1"},
			}
		case "tools/list":
			result = map[string]any{
				"tools": []map[string]any{
					{
						"name":        "search",
						"description": "Search the web",
						"inputSchema": map[string]any{
							"type":       "object",
							"properties": map[string]any{"query": map[string]any{"type": "string"}},
							"required":   []string{"query"},
						},
					},
				},
			}
		case "tools/call":
			var params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			_ = json.Unmarshal(req.Params, &params)
			result = map[string]any{
				"content": []map[string]any{
					{"type": "text", "text": fmt.Sprintf("called %s with query=%v", params.Name, params.Arguments["query"])},
				},
			}
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}

		envelope := map[string]any{"jsonrpc": "2.0", "id": *req.ID, "result": result}
		payload, _ := json.Marshal(envelope)
		if sse {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			fmt.Fprintf(w, ": keep-alive\n\n")
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", payload)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}
}

func runDiscoverAgainst(t *testing.T, server *httptest.Server) (string, error) {
	t.Helper()
	cfg := Config{Servers: map[string]ServerConfig{
		"fake": {Type: "streamable-http", URL: server.URL},
	}}
	defs, fns := Discover(context.Background(), cfg, nil, server.Client(), nil)
	if len(defs) != 1 {
		t.Fatalf("expected 1 definition, got %d", len(defs))
	}
	wantName := "mcp__fake__search"
	if defs[0].Function.Name != wantName {
		t.Fatalf("definition name = %q, want %q", defs[0].Function.Name, wantName)
	}
	if !strings.Contains(string(defs[0].Function.RawParameters), "\"query\"") {
		t.Fatalf("raw schema not passed through: %s", defs[0].Function.RawParameters)
	}
	fn, ok := fns[wantName]
	if !ok {
		t.Fatalf("function %q not registered", wantName)
	}
	return fn(context.Background(), map[string]any{"query": "golang"})
}

func TestDiscoverAndCallJSON(t *testing.T) {
	server := httptest.NewServer(mcpHandler(t, false))
	defer server.Close()

	result, err := runDiscoverAgainst(t, server)
	if err != nil {
		t.Fatalf("tool call returned error: %v", err)
	}
	if !strings.Contains(result, "called search with query=golang") {
		t.Fatalf("unexpected tool result: %q", result)
	}
}

func TestDiscoverAndCallSSE(t *testing.T) {
	server := httptest.NewServer(mcpHandler(t, true))
	defer server.Close()

	result, err := runDiscoverAgainst(t, server)
	if err != nil {
		t.Fatalf("tool call returned error: %v", err)
	}
	if !strings.Contains(result, "called search with query=golang") {
		t.Fatalf("unexpected tool result: %q", result)
	}
}

func TestDiscoverSkipsUnreachableServer(t *testing.T) {
	cfg := Config{Servers: map[string]ServerConfig{
		"down": {Type: "streamable-http", URL: "http://127.0.0.1:0/mcp"},
	}}
	defs, fns := Discover(context.Background(), cfg, nil, nil, nil)
	if len(defs) != 0 || len(fns) != 0 {
		t.Fatalf("expected no tools from unreachable server, got %d defs / %d fns", len(defs), len(fns))
	}
}

func TestDiscoverSkipsInvalidServerAndKeepsValidServer(t *testing.T) {
	server := httptest.NewServer(mcpHandler(t, false))
	defer server.Close()
	cfg := Config{Servers: map[string]ServerConfig{
		"good": {Type: "streamable-http", URL: server.URL},
		"bad": {
			Type: "streamable-http", URL: "https://invalid.test/mcp",
			Headers: map[string]string{"Authorization": "Bearer ${UNAUTHORIZED}"},
		},
	}}
	var logs strings.Builder
	defs, fns := Discover(context.Background(), cfg, func(key string) string {
		if key == "UNAUTHORIZED" {
			return "must-not-appear-in-logs"
		}
		return ""
	}, server.Client(), func(format string, args ...any) { fmt.Fprintf(&logs, format, args...) })
	if len(defs) != 1 || len(fns) != 1 {
		t.Fatalf("expected valid server tools only, got %d defs / %d fns", len(defs), len(fns))
	}
	if !strings.Contains(logs.String(), `server "bad": invalid configuration`) {
		t.Fatalf("logs = %q, want invalid server warning", logs.String())
	}
	if strings.Contains(logs.String(), "must-not-appear-in-logs") {
		t.Fatalf("logs leaked secret: %q", logs.String())
	}
}

func TestDiscoverProducesDeterministicToolJSONAcrossServerOrder(t *testing.T) {
	listCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var rpc struct {
			ID     *int   `json:"id"`
			Method string `json:"method"`
		}
		if err := json.NewDecoder(request.Body).Decode(&rpc); err != nil {
			t.Fatal(err)
		}
		if rpc.ID == nil {
			writer.WriteHeader(http.StatusAccepted)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		switch rpc.Method {
		case "initialize":
			fmt.Fprintf(writer, `{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":"%s","capabilities":{},"serverInfo":{"name":"ordered","version":"1"}}}`, *rpc.ID, protocolVersion)
		case "tools/list":
			listCalls++
			if listCalls == 1 {
				fmt.Fprintf(writer, `{"jsonrpc":"2.0","id":%d,"result":{"tools":[{"name":"zeta","description":"  zeta description\r\n","inputSchema":{"type":"object","properties":{"z":{"type":"string"},"a":{"type":"number"}}}},{"name":"alpha","description":"alpha description","inputSchema":{"properties":{"q":{"description":"query","type":"string"}},"type":"object"}}]}}`, *rpc.ID)
				return
			}
			fmt.Fprintf(writer, `{"jsonrpc":"2.0","id":%d,"result":{"tools":[{"inputSchema":{"type":"object","properties":{"q":{"type":"string","description":"query"}}},"description":"alpha description\n","name":"alpha"},{"inputSchema":{"properties":{"a":{"type":"number"},"z":{"type":"string"}},"type":"object"},"description":"zeta description","name":"zeta"}]}}`, *rpc.ID)
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	cfg := Config{Servers: map[string]ServerConfig{"ordered": {Type: "streamable-http", URL: server.URL}}}
	first, _ := Discover(context.Background(), cfg, nil, server.Client(), nil)
	second, _ := Discover(context.Background(), cfg, nil, server.Client(), nil)
	firstJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	secondJSON, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	if string(firstJSON) != string(secondJSON) {
		t.Fatalf("tool JSON differs by MCP return order:\nfirst:  %s\nsecond: %s", firstJSON, secondJSON)
	}
	if len(first) != 2 || first[0].Function.Name != "mcp__ordered__alpha" || first[1].Function.Name != "mcp__ordered__zeta" {
		t.Fatalf("tool order = %#v", first)
	}
}
