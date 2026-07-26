package mcp

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"regexp"
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

var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// EnabledServers returns valid, enabled servers. Environment placeholders are
// accepted only in header values and only for names listed in MCP_ALLOWED_ENV.
// Invalid servers are omitted; Discover logs the corresponding validation error.
func (c Config) EnabledServers(getenv func(string) string) map[string]ServerConfig {
	enabled, _ := c.enabledServers(getenv)
	return enabled
}

func (c Config) enabledServers(getenv func(string) string) (map[string]ServerConfig, map[string]error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	allowedEnv := parseAllowedEnv(getenv("MCP_ALLOWED_ENV"))
	enabled := make(map[string]ServerConfig)
	invalid := make(map[string]error)
	for name, server := range c.Servers {
		if server.Disabled {
			continue
		}
		expanded, err := validateAndExpandServer(server, getenv, allowedEnv)
		if err != nil {
			invalid[name] = err
			continue
		}
		enabled[name] = expanded
	}
	return enabled, invalid
}

func parseAllowedEnv(raw string) map[string]struct{} {
	allowed := make(map[string]struct{})
	for _, name := range strings.Split(raw, ",") {
		name = strings.TrimSpace(name)
		if envNamePattern.MatchString(name) {
			allowed[name] = struct{}{}
		}
	}
	return allowed
}

func validateAndExpandServer(server ServerConfig, getenv func(string) string, allowedEnv map[string]struct{}) (ServerConfig, error) {
	serverType := strings.ToLower(strings.TrimSpace(server.Type))
	if serverType != "" && serverType != "streamable-http" && serverType != "http" {
		return ServerConfig{}, fmt.Errorf("unsupported transport type %q", server.Type)
	}
	serverURL := strings.TrimSpace(server.URL)
	if serverURL == "" {
		return ServerConfig{}, fmt.Errorf("missing URL")
	}
	if strings.Contains(serverURL, "$") {
		return ServerConfig{}, fmt.Errorf("URL must not contain environment placeholders")
	}
	parsed, err := url.Parse(serverURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return ServerConfig{}, fmt.Errorf("invalid Streamable HTTP URL")
	}

	expanded := ServerConfig{Type: server.Type, URL: serverURL}
	if len(server.Headers) > 0 {
		expanded.Headers = make(map[string]string, len(server.Headers))
		for key, value := range server.Headers {
			if strings.TrimSpace(key) == "" {
				return ServerConfig{}, fmt.Errorf("header name is empty")
			}
			resolved, err := expandAllowedEnv(value, getenv, allowedEnv)
			if err != nil {
				return ServerConfig{}, fmt.Errorf("header %q: %w", key, err)
			}
			expanded.Headers[key] = resolved
		}
	}
	return expanded, nil
}

func expandAllowedEnv(raw string, getenv func(string) string, allowedEnv map[string]struct{}) (string, error) {
	var builder strings.Builder
	for position := 0; position < len(raw); {
		if raw[position] != '$' {
			builder.WriteByte(raw[position])
			position++
			continue
		}

		name, next, err := parseEnvPlaceholder(raw, position)
		if err != nil {
			return "", err
		}
		if _, ok := allowedEnv[name]; !ok {
			return "", fmt.Errorf("environment variable %q is not allowed", name)
		}
		value := getenv(name)
		if value == "" {
			return "", fmt.Errorf("environment variable %q is undefined", name)
		}
		builder.WriteString(value)
		position = next
	}
	return builder.String(), nil
}

func parseEnvPlaceholder(raw string, start int) (string, int, error) {
	if start+1 >= len(raw) {
		return "", 0, fmt.Errorf("malformed environment placeholder")
	}
	if raw[start+1] == '{' {
		end := strings.IndexByte(raw[start+2:], '}')
		if end < 0 {
			return "", 0, fmt.Errorf("malformed environment placeholder")
		}
		end += start + 2
		name := raw[start+2 : end]
		if !envNamePattern.MatchString(name) {
			return "", 0, fmt.Errorf("malformed environment placeholder")
		}
		return name, end + 1, nil
	}
	end := start + 1
	for end < len(raw) && (raw[end] == '_' || raw[end] >= 'a' && raw[end] <= 'z' || raw[end] >= 'A' && raw[end] <= 'Z' || end > start+1 && raw[end] >= '0' && raw[end] <= '9') {
		end++
	}
	name := raw[start+1 : end]
	if !envNamePattern.MatchString(name) {
		return "", 0, fmt.Errorf("malformed environment placeholder")
	}
	return name, end, nil
}

// HasEnabledServers reports whether any non-disabled server needs validation.
// It lets callers skip discovery only when no active server entries exist.
func (c Config) HasEnabledServers() bool {
	for _, server := range c.Servers {
		if !server.Disabled {
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
