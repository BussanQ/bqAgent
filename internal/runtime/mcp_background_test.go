package runtime

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type pendingMCPTransport func(*http.Request) (*http.Response, error)

func (transport pendingMCPTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func TestFactoryBuildBackgroundDoesNotWaitForMCP(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(path, []byte(`{"mcpServers":{"slow":{"type":"streamable-http","url":"http://slow.invalid"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	started := make(chan time.Duration, 1)
	factory := Factory{WorkspaceRoot: dir, AgentDir: dir, MemoryDir: filepath.Join(dir, "memory"), MCPConfigPath: path,
		MCPHTTPClient: &http.Client{Transport: pendingMCPTransport(func(request *http.Request) (*http.Response, error) {
			deadline, _ := request.Context().Deadline()
			started <- time.Until(deadline)
			<-request.Context().Done()
			return nil, request.Context().Err()
		})},
	}
	result := make(chan Runtime, 1)
	go func() { result <- factory.BuildBackground(ctx, false) }()
	var runtime Runtime
	select {
	case runtime = <-result:
	case <-time.After(2 * time.Second):
		t.Fatal("interactive runtime blocked on MCP")
	}
	select {
	case budget := <-started:
		if budget < 13*time.Second || budget > 15*time.Second {
			t.Fatalf("discovery budget changed: %s", budget)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("background discovery did not start")
	}
	if runtime.Catalog.Registry()["execute_bash"] == nil || runtime.MCP.Status()[0].State != "connecting" {
		t.Fatal("builtins or connection status unavailable during discovery")
	}
	cancel()
	select {
	case <-runtime.MCP.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("background discovery outlived parent")
	}
}
