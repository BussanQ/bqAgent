package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// TodoItem is one entry in a task list, mirroring Claude Code's TodoWrite shape.
type TodoItem struct {
	Content    string `json:"content"`
	Status     string `json:"status"`
	ActiveForm string `json:"activeForm,omitempty"`
}

// TodoStore holds the current task list in memory (process-scoped). It is shared
// by the TodoWrite tool so the list survives across turns within a run.
type TodoStore struct {
	mu    sync.Mutex
	items []TodoItem
}

func NewTodoStore() *TodoStore {
	return &TodoStore{}
}

func (store *TodoStore) set(items []TodoItem) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.items = items
}

// TodoWriteWithStore replaces the task list with the provided todos (a JSON
// array string) and returns a rendered view. The JSON-string parameter sidesteps
// the flat schema, matching the codebase's string-arg convention.
func TodoWriteWithStore(store *TodoStore) Function {
	return func(ctx context.Context, args map[string]any) (string, error) {
		raw, err := requireString(args, "todos")
		if err != nil {
			return "", err
		}
		var items []TodoItem
		if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &items); err != nil {
			return "", fmt.Errorf("todos must be a JSON array of {content,status,activeForm}: %w", err)
		}
		for index, item := range items {
			if strings.TrimSpace(item.Content) == "" {
				return "", fmt.Errorf("todo %d is missing content", index+1)
			}
			switch item.Status {
			case "pending", "in_progress", "completed":
			default:
				return "", fmt.Errorf("todo %d has invalid status %q (want pending|in_progress|completed)", index+1, item.Status)
			}
		}
		if store != nil {
			store.set(items)
		}
		return renderTodos(items), nil
	}
}

func renderTodos(items []TodoItem) string {
	if len(items) == 0 {
		return "Todo list cleared."
	}
	var builder strings.Builder
	builder.WriteString("Todos:\n")
	for _, item := range items {
		marker := "[ ]"
		switch item.Status {
		case "in_progress":
			marker = "[~]"
		case "completed":
			marker = "[x]"
		}
		builder.WriteString(fmt.Sprintf("%s %s\n", marker, item.Content))
	}
	return strings.TrimRight(builder.String(), "\n")
}
