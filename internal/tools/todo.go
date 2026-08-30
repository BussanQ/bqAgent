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

func (store *TodoStore) set(items []TodoItem) (progressChanged bool) {
	store.mu.Lock()
	defer store.mu.Unlock()
	progressChanged = !sameTodoProgress(store.items, items)
	store.items = items
	return progressChanged
}

func sameTodoProgress(left, right []TodoItem) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if strings.TrimSpace(left[index].Content) != strings.TrimSpace(right[index].Content) || left[index].Status != right[index].Status {
			return false
		}
	}
	return true
}

// TodoWriteWithStore replaces the task list with the provided todos and returns
// a rendered view. The public schema uses a JSON array string to match the
// codebase's flat string-arg convention, but native arrays are accepted as a
// compatibility fallback for providers that decode nested tool arguments.
func TodoWriteWithStore(store *TodoStore) Function {
	return func(ctx context.Context, args map[string]any) (string, error) {
		raw, ok := args["todos"]
		if !ok {
			return "", fmt.Errorf("missing required argument %q", "todos")
		}
		var encoded []byte
		if text, isString := raw.(string); isString {
			encoded = []byte(strings.TrimSpace(text))
		} else {
			var err error
			encoded, err = json.Marshal(raw)
			if err != nil {
				return "", fmt.Errorf("todos must be a JSON array of {content,status,activeForm}: %w", err)
			}
		}
		var items []TodoItem
		if err := json.Unmarshal(encoded, &items); err != nil {
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
		progressChanged := true
		if store != nil {
			progressChanged = store.set(items)
		}
		rendered := renderTodos(items)
		if !progressChanged {
			return "Todo list unchanged: no task content or status changed. Continue with substantive work; do not call todo_write again until progress changes.\n" + rendered, nil
		}
		return "Todo list updated. Planning is recorded; continue now with substantive work using another tool. Do not call todo_write again until task content or status changes.\n" + rendered, nil
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
