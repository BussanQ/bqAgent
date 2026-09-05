package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"bqagent/internal/tools"
)

// toolNamePrefix namespaces MCP tools so they never collide with builtins.
const toolNamePrefix = "mcp__"

var unsafeNameChars = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

// Logf is a best-effort logging callback for discovery warnings.
type Logf func(format string, args ...any)

// Discover connects to every enabled server, lists its tools, and adapts each
// into a tools.Definition (sent to the model) plus a tools.Function (executed
// locally). Discovery is best-effort: a server that fails to initialize or list
// tools is logged and skipped, never aborting the caller.
//
// A nil httpClient lets each Client pick its own default; tests inject one.
func Discover(ctx context.Context, cfg Config, getenv func(string) string, httpClient *http.Client, logf Logf) ([]tools.Definition, map[string]tools.Function) {
	return DiscoverWithStatus(ctx, cfg, getenv, httpClient, logf, nil)
}

func DiscoverWithStatus(ctx context.Context, cfg Config, getenv func(string) string, httpClient *http.Client, logf Logf, report func(ServerStatus)) ([]tools.Definition, map[string]tools.Function) {
	return discover(ctx, cfg, getenv, httpClient, logf, report, 0)
}

// Probe uses independent clients and a per-server timeout. Its discovered tools
// are intentionally not registered with a running agent.
func Probe(ctx context.Context, cfg Config, getenv func(string) string, httpClient *http.Client, report func(ServerStatus)) {
	discover(ctx, cfg, getenv, httpClient, nil, report, 5*time.Second)
}

func discover(ctx context.Context, cfg Config, getenv func(string) string, httpClient *http.Client, logf Logf, report func(ServerStatus), timeout time.Duration) ([]tools.Definition, map[string]tools.Function) {
	if report == nil {
		report = func(ServerStatus) {}
	}
	for _, status := range cfg.Status(getenv) {
		report(status)
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	var definitions []tools.Definition
	functions := make(map[string]tools.Function)
	// seen maps a sanitized tool name to the "server/tool" that first claimed it,
	// so collisions (two servers whose names differ only in special chars) are
	// detected and skipped rather than silently producing a schema/function mismatch.
	seen := make(map[string]string)

	enabled, invalid := cfg.enabledServers(getenv)
	invalidNames := sortedMapKeys(invalid)
	for _, name := range invalidNames {
		err := invalid[name]
		logf("[MCP] server %q: invalid configuration, skipping: %v\n", name, err)
	}
	serverNames := sortedMapKeys(enabled)
	for _, name := range serverNames {
		server := enabled[name]
		client := NewClient(httpClient, server.URL, server.Headers)
		probeCtx, cancel := ctx, func() {}
		if timeout > 0 {
			probeCtx, cancel = context.WithTimeout(ctx, timeout)
		}
		if err := client.Initialize(probeCtx); err != nil {
			cancel()
			report(ServerStatus{Name: name, State: "error", Reason: "initialize_" + FailureReason(err), CheckedAt: time.Now().UTC()})
			logf("[MCP] server %q: initialize failed: %v\n", name, err)
			continue
		}
		specs, err := client.ListTools(probeCtx)
		cancel()
		if err != nil {
			report(ServerStatus{Name: name, State: "error", Reason: "tools_list_" + FailureReason(err), CheckedAt: time.Now().UTC()})
			logf("[MCP] server %q: tools/list failed: %v\n", name, err)
			continue
		}
		sort.SliceStable(specs, func(left, right int) bool {
			leftName := toolNamePrefix + sanitizeName(name) + "__" + sanitizeName(specs[left].Name)
			rightName := toolNamePrefix + sanitizeName(name) + "__" + sanitizeName(specs[right].Name)
			if leftName != rightName {
				return leftName < rightName
			}
			return specs[left].Name < specs[right].Name
		})
		added := 0
		for _, spec := range specs {
			toolName := toolNamePrefix + sanitizeName(name) + "__" + sanitizeName(spec.Name)
			if prior, clash := seen[toolName]; clash {
				logf("[MCP] server %q tool %q: name %q collides with %s, skipping\n", name, spec.Name, toolName, prior)
				continue
			}
			seen[toolName] = fmt.Sprintf("%q/%q", name, spec.Name)
			definitions = append(definitions, tools.Definition{
				Type: "function",
				Function: tools.FunctionDefinition{
					Name:          toolName,
					Description:   normalizeDescription(spec.Description),
					RawParameters: canonicalJSON(spec.InputSchema),
				},
			})
			functions[toolName] = makeToolFunc(client, spec.Name)
			added++
		}
		logf("[MCP] server %q: registered %d tool(s)\n", name, added)
		status := ServerStatus{Name: name, State: "available", Tools: added, CheckedAt: time.Now().UTC()}
		if added != len(specs) {
			status.State = "error"
			status.Reason = "tool_name_collision"
		}
		report(status)
	}
	return definitions, functions
}

func sortedMapKeys[Value any](values map[string]Value) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func normalizeDescription(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	lines := strings.Split(value, "\n")
	for index := range lines {
		lines[index] = strings.TrimRight(lines[index], " \t")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func canonicalJSON(raw json.RawMessage) json.RawMessage {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return append(json.RawMessage(nil), raw...)
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return append(json.RawMessage(nil), raw...)
	}
	return canonical
}

// makeToolFunc binds an MCP client + remote tool name into a tools.Function.
func makeToolFunc(client *Client, remoteName string) tools.Function {
	return func(ctx context.Context, args map[string]any) (string, error) {
		return client.CallTool(ctx, remoteName, args)
	}
}

func sanitizeName(name string) string {
	cleaned := unsafeNameChars.ReplaceAllString(name, "_")
	if cleaned == "" {
		return "tool"
	}
	return cleaned
}
