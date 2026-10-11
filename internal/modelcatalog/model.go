// Package modelcatalog keeps provider model identities separate from their labels.
package modelcatalog

import "strings"

type Model struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name,omitempty"`
}

type Names map[string]string

// QualifiedName retains routing namespaces hidden by friendly model names.
// Unqualified IDs use the configured provider (or protocol fallback).
func QualifiedName(provider, id, name string) string {
	id, name = strings.TrimSpace(id), strings.TrimSpace(name)
	if name == "" {
		name = id
	}
	if prefix, _, ok := strings.Cut(id, "/"); ok && prefix != "" {
		provider = prefix
	}
	provider = strings.TrimSpace(provider)
	if provider == "" {
		provider = "provider"
	}
	if strings.HasPrefix(name, provider+"/") {
		return name
	}
	return provider + "/" + name
}

// Name falls back to the request ID for providers and old configs without labels.
func (names Names) Name(id string) string {
	if name := strings.TrimSpace(names[id]); name != "" {
		return name
	}
	return id
}

// ForModels copies only names belonging to the selected models.
func (names Names) ForModels(ids []string) Names {
	result := Names{}
	for _, id := range ids {
		if name := strings.TrimSpace(names[id]); name != "" {
			result[id] = name
		}
	}
	return result
}
