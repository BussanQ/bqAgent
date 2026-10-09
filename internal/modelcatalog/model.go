// Package modelcatalog keeps provider model identities separate from their labels.
package modelcatalog

import "strings"

type Model struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name,omitempty"`
}

type Names map[string]string

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
