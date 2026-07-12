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
		Name: "memory", Description: "Manage structured persistent memory: add, replace, remove, search, list, confirm, or compact.",
		Parameters: JSONSchema{Type: "object", Properties: map[string]JSONSchemaProperty{
			"action": {Type: "string", Description: "add|replace|remove|search|list|confirm|compact"},
			"id":     {Type: "string"}, "kind": {Type: "string"}, "content": {Type: "string"}, "query": {Type: "string"},
			"confidence": {Type: "string"}, "sensitivity": {Type: "string"}, "supersedes": {Type: "string", Description: "Comma-separated memory ids"}, "limit": {Type: "string"},
		}, Required: []string{"action"}},
	}}
}

func StructuredMemory(store *appmemory.Store) Function {
	return func(ctx context.Context, args map[string]any) (string, error) {
		if store == nil {
			return "", fmt.Errorf("memory store is unavailable")
		}
		action, _ := args["action"].(string)
		action = strings.ToLower(strings.TrimSpace(action))
		source := apptrace.RunIDFromContext(ctx)
		encode := func(value any) (string, error) {
			content, err := json.MarshalIndent(value, "", "  ")
			return string(content), err
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
			return encode(entry)
		case "replace":
			id, _ := args["id"].(string)
			kind, _ := args["kind"].(string)
			content, _ := args["content"].(string)
			entry, err := store.Replace(id, appmemory.Kind(kind), content, source, parseFloatArg(args["confidence"], 0), splitIDs(args["supersedes"]))
			if err != nil {
				return "", err
			}
			return encode(entry)
		case "remove":
			id, _ := args["id"].(string)
			entry, err := store.Remove(id, source, "removed")
			if err != nil {
				return "", err
			}
			return encode(entry)
		case "confirm":
			id, _ := args["id"].(string)
			entry, err := store.Confirm(id, source)
			if err != nil {
				return "", err
			}
			return encode(entry)
		case "search":
			query, _ := args["query"].(string)
			limit := parseIntArg(args["limit"], appmemory.DefaultLimit)
			results, err := store.Search(query, nil, limit)
			if err != nil {
				return "", err
			}
			return encode(results)
		case "list":
			entries, err := store.ListAll()
			if err != nil {
				return "", err
			}
			return encode(entries)
		case "compact":
			report, err := store.Compact()
			if err != nil {
				return "", err
			}
			return encode(report)
		default:
			return "", fmt.Errorf("memory action must be add, replace, remove, search, list, confirm, or compact")
		}
	}
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
