package mcp

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Config mirrors the standard MCP client config shape:
//
//	{ "mcpServers": { "<name>": { "type": "streamable-http", "url": "...", "headers": {...} } } }
type Config struct {
	Servers map[string]ServerConfig `json:"mcpServers"`
}

// ServerConfig describes a single MCP server. Only the Streamable HTTP
// transport is supported; Type is accepted as "streamable-http" (or "http").
type ServerConfig struct {
	Type     string            `json:"type"`
	URL      string            `json:"url"`
	Headers  map[string]string `json:"headers,omitempty"`
	Disabled bool              `json:"disabled,omitempty"`
}

// EnabledServers returns the configured servers that are not disabled, with
// ${VAR}/$VAR placeholders in URL and header values expanded via getenv.
func (c Config) EnabledServers(getenv func(string) string) map[string]ServerConfig {
	if getenv == nil {
		getenv = os.Getenv
	}
	enabled := make(map[string]ServerConfig)
	for name, server := range c.Servers {
		if server.Disabled || strings.TrimSpace(server.URL) == "" {
			continue
		}
		expanded := ServerConfig{
			Type: server.Type,
			URL:  expandEnv(server.URL, getenv),
		}
		if len(server.Headers) > 0 {
			expanded.Headers = make(map[string]string, len(server.Headers))
			for key, value := range server.Headers {
				expanded.Headers[key] = expandEnv(value, getenv)
			}
		}
		enabled[name] = expanded
	}
	return enabled
}

// HasEnabledServers reports whether any non-disabled server is configured. It
// is cheap and lets callers skip MCP discovery entirely when nothing is set.
func (c Config) HasEnabledServers() bool {
	for _, server := range c.Servers {
		if !server.Disabled && strings.TrimSpace(server.URL) != "" {
			return true
		}
	}
	return false
}

// LoadConfig reads and parses .agent/mcp.json. A missing file is not an error:
// it yields an empty config so callers degrade silently.
func LoadConfig(path string, getenv func(string) string) (Config, error) {
	if strings.TrimSpace(path) == "" {
		return Config{}, nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, nil
		}
		return Config{}, err
	}
	var cfg Config
	if err := json.Unmarshal(content, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return cfg, nil
}

// expandEnv replaces ${VAR} and $VAR with getenv(VAR). Unknown variables expand
// to the empty string, matching os.Expand semantics.
func expandEnv(raw string, getenv func(string) string) string {
	if !strings.Contains(raw, "$") {
		return raw
	}
	return os.Expand(raw, getenv)
}
