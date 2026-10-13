package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type discoveryTransport func(*http.Request) (*http.Response, error)

func (transport discoveryTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func TestDiscoveryParallelProgressAndCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan string, 2)
	release := make(chan struct{})
	ready := make(chan struct{}, 1)
	handler := mcpHandler(t, false)
	client := &http.Client{Transport: discoveryTransport(func(request *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(request.Body)
		request.Body = io.NopCloser(strings.NewReader(string(body)))
		var rpc rpcRequest
		if err := json.Unmarshal(body, &rpc); err != nil {
			return nil, err
		}
		if rpc.Method == "initialize" {
			started <- request.URL.Host
			if request.URL.Host == "slow.invalid" {
				<-request.Context().Done()
				return nil, request.Context().Err()
			}
			select {
			case <-release:
			case <-request.Context().Done():
				return nil, request.Context().Err()
			}
		}
		writer := httptest.NewRecorder()
		handler.ServeHTTP(writer, request)
		return writer.Result(), nil
	})}
	cfg := Config{Servers: map[string]ServerConfig{
		"a-slow":   {URL: "http://slow.invalid/mcp", Type: "streamable-http"},
		"z-fast":   {URL: "http://fast.invalid/mcp", Type: "streamable-http"},
		"disabled": {Disabled: true},
		"invalid":  {URL: "file:///invalid"},
	}}
	discovery := StartDiscovery(ctx, cfg, nil, client, nil, func(status ServerStatus) {
		if status.Name == "z-fast" && status.State == "available" {
			select {
			case ready <- struct{}{}:
			default:
			}
		}
	}, 15*time.Second)
	// Both initialize requests must begin before either is allowed to finish.
	for range 2 {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("servers initialized serially")
		}
	}
	if statuses := discovery.Status(); len(statuses) != 4 || statuses[0].State != "connecting" || statuses[1].State != "disabled" || statuses[2].State != "error" {
		t.Fatalf("initial statuses = %#v", statuses)
	}
	if defs, _ := discovery.Snapshot(); len(defs) != 0 {
		t.Fatal("premature tools")
	}
	close(release)
	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		t.Fatal("fast server blocked behind slow server")
	}
	defs, functions := discovery.Snapshot()
	if len(defs) != 1 || defs[0].Function.Name != "mcp__z-fast__search" {
		t.Fatalf("partial tools = %#v", defs)
	}
	output, err := functions[defs[0].Function.Name](ctx, map[string]any{"query": "hello"})
	if err != nil || !strings.Contains(output, "hello") {
		t.Fatalf("discovered tool: %q %v", output, err)
	}
	delete(functions, defs[0].Function.Name)
	defs[0].Function.Name = "modified"
	cancel()
	select {
	case <-discovery.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("discovery ignored cancellation")
	}
	defs, functions = discovery.Snapshot()
	if len(defs) != 1 || functions["mcp__z-fast__search"] == nil {
		t.Fatal("snapshot was mutable or successful tools were discarded")
	}
	statuses := discovery.Status()
	if statuses[0].State != "error" || statuses[0].Reason != "initialize_cancelled" || statuses[3].State != "available" {
		t.Fatalf("final statuses = %#v", statuses)
	}
}

func TestDiscoveryDeadlineAndDeterministicCollisions(t *testing.T) {
	t.Run("deadline", func(t *testing.T) {
		client := &http.Client{Transport: discoveryTransport(func(request *http.Request) (*http.Response, error) {
			<-request.Context().Done()
			return nil, request.Context().Err()
		})}
		discovery := StartDiscovery(context.Background(), Config{Servers: map[string]ServerConfig{"slow": {URL: "http://slow.invalid", Type: "streamable-http"}}}, nil, client, nil, nil, 20*time.Millisecond)
		select {
		case <-discovery.Done():
		case <-time.After(2 * time.Second):
			t.Fatal("deadline did not bound discovery")
		}
		if status := discovery.Status()[0]; status.State != "error" || status.Reason != "initialize_timeout" {
			t.Fatalf("status = %#v", status)
		}
	})
	t.Run("deterministic catalog", func(t *testing.T) {
		results := map[string]discoveryResult{}
		spec := ToolSpec{Name: "search", InputSchema: json.RawMessage(`{"type":"object"}`)}
		results["same_name"] = discoveryResult{client: NewClient(nil, "http://second.invalid", nil), specs: []ToolSpec{spec}}
		first, _ := collectDiscoveredTools(results, func(string, ...any) {}, func(ServerStatus) {})
		results["same.name"] = discoveryResult{client: NewClient(nil, "http://first.invalid", nil), specs: []ToolSpec{spec}}
		var statuses []ServerStatus
		defs, functions := collectDiscoveredTools(results, func(string, ...any) {}, func(status ServerStatus) { statuses = append(statuses, status) })
		if len(defs) != 1 || len(functions) != 1 || len(first) != 1 || statuses[0].Name != "same.name" || statuses[0].Tools != 1 || statuses[1].Reason != "tool_name_collision" {
			t.Fatalf("collision handling = %#v %#v", defs, statuses)
		}
	})
}
