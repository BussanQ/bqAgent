package mcp

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigMissingFileIsEmpty(t *testing.T) {
	cfg, err := LoadConfig(filepath.Join(t.TempDir(), "mcp.json"), nil)
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}
	if cfg.HasEnabledServers() {
		t.Fatalf("expected no enabled servers for missing file")
	}
}

func TestEnabledServersSkipsDisabledAndExpandsEnv(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "mcp.json")
	content := `{
  "mcpServers": {
    "live": {
      "type": "streamable-http",
      "url": "https://example.test/${PATH_SEGMENT}/mcp",
      "headers": { "Authorization": "Bearer ${TEST_KEY}" }
    },
    "off": { "type": "streamable-http", "url": "https://example.test/x", "disabled": true }
  }
}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfig(path, nil)
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}
	if !cfg.HasEnabledServers() {
		t.Fatalf("expected an enabled server")
	}

	getenv := func(key string) string {
		switch key {
		case "PATH_SEGMENT":
			return "WebSearch"
		case "TEST_KEY":
			return "secret"
		default:
			return ""
		}
	}
	enabled := cfg.EnabledServers(getenv)
	if len(enabled) != 1 {
		t.Fatalf("expected 1 enabled server, got %d", len(enabled))
	}
	live, ok := enabled["live"]
	if !ok {
		t.Fatalf("expected 'live' server to be enabled")
	}
	if live.URL != "https://example.test/WebSearch/mcp" {
		t.Fatalf("URL not expanded: %q", live.URL)
	}
	if live.Headers["Authorization"] != "Bearer secret" {
		t.Fatalf("header not expanded: %q", live.Headers["Authorization"])
	}
}
