package agent

import (
	"context"
	"fmt"
	"strings"

	"bqagent/internal/tools"
)

type nativeWebSearcher interface {
	SupportsNativeWebSearch(model string) bool
	NativeWebSearch(ctx context.Context, model, query string) (string, error)
}

// Bind at agent creation, after the session model has been resolved. The catalog
// remains the API fallback; switching models/providers never retains an old
// model's native-search closure.
func (a *Agent) installNativeWebSearch(client nativeWebSearcher) {
	if client == nil || !client.SupportsNativeWebSearch(a.model) {
		return
	}
	fallback := a.functions["web_search"]
	found := false
	for _, definition := range a.toolDefinitions {
		if definition.Function.Name == "web_search" {
			found = true
			break
		}
	}
	if !found {
		a.toolDefinitions = append(a.toolDefinitions, tools.WebSearchDefinition())
		fallback = nil // Do not bypass a toolset that excluded the API definition.
	}
	a.webSearchAvailable = func() bool {
		return fallback != nil || client.SupportsNativeWebSearch(a.model)
	}
	a.functions["web_search"] = func(ctx context.Context, args map[string]any) (string, error) {
		query, ok := args["query"].(string)
		if !ok || strings.TrimSpace(query) == "" {
			query, ok = args["search"].(string)
		}
		if !ok || strings.TrimSpace(query) == "" {
			return "", fmt.Errorf("web_search requires a non-empty query string")
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
		var nativeErr error
		if client.SupportsNativeWebSearch(a.model) {
			var result string
			result, nativeErr = client.NativeWebSearch(ctx, a.model, strings.TrimSpace(query))
			if nativeErr == nil {
				return result, nil
			}
			if err := ctx.Err(); err != nil {
				return "", err
			}
		}
		if fallback != nil {
			result, err := fallback(ctx, map[string]any{"query": query})
			if err != nil && nativeErr != nil {
				return result, fmt.Errorf("native web search failed (%v); search API fallback: %w", nativeErr, err)
			}
			return result, err
		}
		if nativeErr != nil {
			return "", fmt.Errorf("native web search failed; no search API fallback configured: %w", nativeErr)
		}
		return "", fmt.Errorf("web_search is unavailable for this model; use another model or configure a search API key")
	}
}
