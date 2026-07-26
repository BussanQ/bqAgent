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

func TestEnabledServersExpandsOnlyAllowedHeaderEnvironment(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "mcp.json")
	content := `{
  "mcpServers": {
    "live": {
      "type": "streamable-http",
      "url": "https://example.test/mcp",
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
		case "MCP_ALLOWED_ENV":
			return " TEST_KEY "
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
		t.Fatal("expected 'live' server to be enabled")
	}
	if live.URL != "https://example.test/mcp" {
		t.Fatalf("URL = %q, want literal configured URL", live.URL)
	}
	if live.Headers["Authorization"] != "Bearer secret" {
		t.Fatalf("header not expanded: %q", live.Headers["Authorization"])
	}
}

func TestEnabledServersSkipsInvalidPlaceholderConfigurationsIndividually(t *testing.T) {
	cfg := Config{Servers: map[string]ServerConfig{
		"good": {
			Type: "streamable-http", URL: "https://good.test/mcp",
			Headers: map[string]string{"Authorization": "Bearer ${ALLOWED}"},
		},
		"url-placeholder": {Type: "streamable-http", URL: "https://bad.test/${ALLOWED}"},
		"unallowed":       {Type: "streamable-http", URL: "https://bad.test/mcp", Headers: map[string]string{"X-Key": "$DENIED"}},
		"undefined":       {Type: "streamable-http", URL: "https://bad.test/mcp", Headers: map[string]string{"X-Key": "${MISSING}"}},
		"malformed":       {Type: "streamable-http", URL: "https://bad.test/mcp", Headers: map[string]string{"X-Key": "${ALLOWED"}},
	}}
	getenv := func(key string) string {
		switch key {
		case "MCP_ALLOWED_ENV":
			return "ALLOWED,MISSING"
		case "ALLOWED":
			return "allowed-value"
		default:
			return ""
		}
	}

	enabled, invalid := cfg.enabledServers(getenv)
	if len(enabled) != 1 || enabled["good"].Headers["Authorization"] != "Bearer allowed-value" {
		t.Fatalf("enabled servers = %#v, want only expanded good server", enabled)
	}
	if len(invalid) != 4 {
		t.Fatalf("invalid servers = %#v, want four invalid configurations", invalid)
	}
	for _, name := range []string{"url-placeholder", "unallowed", "undefined", "malformed"} {
		if invalid[name] == nil {
			t.Fatalf("server %q was not rejected", name)
		}
	}
}
