package mcp

import (
	"context"
	"fmt"
	"net/http"
	"regexp"

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
	if logf == nil {
		logf = func(string, ...any) {}
	}
	var definitions []tools.Definition
	functions := make(map[string]tools.Function)
	// seen maps a sanitized tool name to the "server/tool" that first claimed it,
	// so collisions (two servers whose names differ only in special chars) are
	// detected and skipped rather than silently producing a schema/function mismatch.
	seen := make(map[string]string)

	for name, server := range cfg.EnabledServers(getenv) {
		client := NewClient(httpClient, server.URL, server.Headers)
		if err := client.Initialize(ctx); err != nil {
			logf("[MCP] server %q: initialize failed: %v\n", name, err)
			continue
		}
		specs, err := client.ListTools(ctx)
		if err != nil {
			logf("[MCP] server %q: tools/list failed: %v\n", name, err)
			continue
		}
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
					Description:   spec.Description,
					RawParameters: spec.InputSchema,
				},
			})
			functions[toolName] = makeToolFunc(client, spec.Name)
			added++
		}
		logf("[MCP] server %q: registered %d tool(s)\n", name, added)
	}
	return definitions, functions
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
