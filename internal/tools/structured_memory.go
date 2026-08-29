package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	appmemory "bqagent/internal/memory"
	apptrace "bqagent/internal/trace"
)

func StructuredMemoryDefinition() Definition {
	return Definition{Type: "function", Function: FunctionDefinition{
		Name: "memory", Description: "Manage structured persistent memory. Recall only with action=search or action=list; do not prefetch at session start or before exploring a codebase. Write with add/replace. Default target is workspace; use target=global only when the user explicitly asks for global memory. Other actions: remove, confirm, compact.",
		Parameters: JSONSchema{Type: "object", Properties: map[string]JSONSchemaProperty{
			"action": {Type: "string", Description: "search|list to recall; add|replace|remove|confirm|compact to manage"},
			"id":     {Type: "string"}, "kind": {Type: "string"}, "content": {Type: "string"}, "query": {Type: "string"},
			"target":     {Type: "string", Description: "workspace (default) or global. Use global only when the user asks for global memory."},
			"confidence": {Type: "string"}, "sensitivity": {Type: "string"}, "supersedes": {Type: "string", Description: "Comma-separated memory ids"}, "limit": {Type: "string"},
		}, Required: []string{"action"}},
	}}
}

func StructuredMemory(workspace, global *appmemory.Store) Function {
	return func(ctx context.Context, args map[string]any) (string, error) {
		target, store, err := resolveMemoryStore(args, workspace, global)
		if err != nil {
			return "", err
		}
		action, _ := args["action"].(string)
		action = strings.ToLower(strings.TrimSpace(action))
		source := apptrace.RunIDFromContext(ctx)
		encode := func(value any) (string, error) {
			content, err := json.MarshalIndent(value, "", "  ")
			return string(content), err
		}
		scoped := func(payload map[string]any) (string, error) {
			payload["target"] = target
			payload["path"] = store.EntriesPath()
			return encode(payload)
		}
		switch action {
		case "add":
			kind, _ := args["kind"].(string)
			content, _ := args["content"].(string)
			confidence := parseFloatArg(args["confidence"], .8)
			sensitivity, _ := args["sensitivity"].(string)
			entry, err := store.Add(appmemory.Kind(kind), content, source, confidence, sensitivity, splitIDs(args["supersedes"]))
			if err != nil {
				return "", err
			}
			return scoped(map[string]any{"entry": entry})
		case "replace":
			id, _ := args["id"].(string)
			kind, _ := args["kind"].(string)
			content, _ := args["content"].(string)
			entry, err := store.Replace(id, appmemory.Kind(kind), content, source, parseFloatArg(args["confidence"], 0), splitIDs(args["supersedes"]))
			if err != nil {
				return "", err
			}
			return scoped(map[string]any{"entry": entry})
		case "remove":
			id, _ := args["id"].(string)
			entry, err := store.Remove(id, source, "removed")
			if err != nil {
				return "", err
			}
			return scoped(map[string]any{"entry": entry})
		case "confirm":
			id, _ := args["id"].(string)
			entry, err := store.Confirm(id, source)
			if err != nil {
				return "", err
			}
			return scoped(map[string]any{"entry": entry})
		case "search":
			query, _ := args["query"].(string)
			limit := parseIntArg(args["limit"], appmemory.DefaultLimit)
			results, err := store.Search(query, nil, limit)
			if err != nil {
				return "", err
			}
			return scoped(map[string]any{"results": results})
		case "list":
			entries, err := store.ListAll()
			if err != nil {
				return "", err
			}
			return scoped(map[string]any{"entries": entries})
		case "compact":
			report, err := store.Compact()
			if err != nil {
				return "", err
			}
			return scoped(map[string]any{"report": report})
		default:
			return "", fmt.Errorf("memory action must be add, replace, remove, search, list, confirm, or compact")
		}
	}
}

func resolveMemoryStore(args map[string]any, workspace, global *appmemory.Store) (string, *appmemory.Store, error) {
	target, _ := args["target"].(string)
	target = strings.ToLower(strings.TrimSpace(target))
	if target == "" {
		target = "workspace"
	}
	if target != "workspace" && target != "global" {
		return "", nil, fmt.Errorf("memory target must be workspace or global")
	}
	if target == "global" {
		if global == nil {
			return "", nil, fmt.Errorf("global memory store is unavailable")
		}
		return target, global, nil
	}
	if workspace == nil {
		return "", nil, fmt.Errorf("memory store is unavailable")
	}
	return target, workspace, nil
}

func StructuredMemSave(store *appmemory.Store) Function {
	return func(ctx context.Context, args map[string]any) (string, error) {
		target, _ := args["target"].(string)
		content, _ := args["content"].(string)
		kind := appmemory.KindLesson
		if target == "longterm" {
			kind = appmemory.KindProjectFact
		}
		entry, err := store.Add(kind, content, apptrace.RunIDFromContext(ctx), .7, "normal", nil)
		if err != nil {
			return "", err
		}
		return "Saved structured memory " + entry.ID, nil
	}
}
func StructuredMemGet(store *appmemory.Store) Function {
	return func(ctx context.Context, args map[string]any) (string, error) {
		entries, err := store.Active()
		if err != nil {
			return "", err
		}
		var b strings.Builder
		for _, entry := range entries {
			fmt.Fprintf(&b, "- [%s] %s: %s\n", entry.ID, entry.Kind, entry.Content)
		}
		if b.Len() == 0 {
			return "No memory found.", nil
		}
		return b.String(), nil
	}
}

func parseFloatArg(value any, fallback float64) float64 {
	text, _ := value.(string)
	if strings.TrimSpace(text) == "" {
		return fallback
	}
	parsed, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return fallback
	}
	return parsed
}
func parseIntArg(value any, fallback int) int {
	text, _ := value.(string)
	if strings.TrimSpace(text) == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(text)
	if err != nil {
		return fallback
	}
	return parsed
}
func splitIDs(value any) []string {
	text, _ := value.(string)
	var out []string
	for _, part := range strings.Split(text, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
