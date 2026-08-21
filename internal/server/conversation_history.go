package server

import (
	"errors"
	"net/http"
	"os"
	"strings"
	"time"
)

type conversationListItem struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Status    string    `json:"status"`
	UpdatedAt time.Time `json:"updated_at"`
}

type conversationMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func (handler *handler) handleConversations(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writeError(writer, http.StatusMethodNotAllowed, chatResponse{Error: "method not allowed"})
		return
	}
	service, err := handler.serviceForWorkspace(request.URL.Query().Get("workspace_id"))
	if err != nil {
		writeError(writer, http.StatusNotFound, chatResponse{Error: err.Error()})
		return
	}
	metas, err := service.store.List(100)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, chatResponse{Error: err.Error()})
		return
	}
	items := make([]conversationListItem, 0, len(metas))
	for _, meta := range metas {
		title := strings.TrimSpace(meta.Task)
		if title == "" {
			title = "新会话"
		}
		if runes := []rune(title); len(runes) > 48 {
			title = string(runes[:48]) + "…"
		}
		items = append(items, conversationListItem{ID: meta.ID, Title: title, Status: string(meta.Status), UpdatedAt: meta.UpdatedAt})
	}
	writeJSON(writer, http.StatusOK, map[string]any{"conversations": items})
}

func (handler *handler) handleConversationHistory(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writeError(writer, http.StatusMethodNotAllowed, chatResponse{Error: "method not allowed"})
		return
	}
	id := strings.Trim(strings.TrimPrefix(request.URL.Path, "/api/v1/webui/conversations/"), "/")
	service, err := handler.serviceForWorkspace(request.URL.Query().Get("workspace_id"))
	if err != nil {
		writeError(writer, http.StatusNotFound, chatResponse{Error: err.Error()})
		return
	}
	saved, err := service.store.Open(id)
	if errors.Is(err, os.ErrNotExist) {
		writeError(writer, http.StatusNotFound, chatResponse{Error: "conversation not found"})
		return
	}
	if err != nil {
		writeError(writer, http.StatusBadRequest, chatResponse{Error: err.Error()})
		return
	}
	messages, err := saved.LoadMessages()
	if err != nil {
		writeError(writer, http.StatusInternalServerError, chatResponse{Error: err.Error()})
		return
	}
	history := make([]conversationMessage, 0, len(messages))
	for _, message := range messages {
		role, _ := message["role"].(string)
		content := conversationMessageText(message["content"])
		if (role == "user" || role == "assistant") && strings.TrimSpace(content) != "" {
			history = append(history, conversationMessage{Role: role, Content: content})
		}
	}
	writeJSON(writer, http.StatusOK, map[string]any{"id": saved.ID(), "title": saved.Meta().Task, "messages": history})
}

func conversationMessageText(content any) string {
	if text, ok := content.(string); ok {
		return text
	}
	parts, ok := content.([]any)
	if !ok {
		return ""
	}
	texts := make([]string, 0, len(parts))
	images := 0
	for _, raw := range parts {
		part, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		switch part["type"] {
		case "text":
			if text, ok := part["text"].(string); ok && strings.TrimSpace(text) != "" {
				texts = append(texts, text)
			}
		case "image_url", "image":
			images++
		}
	}
	if images > 0 {
		texts = append(texts, "[图片]")
	}
	return strings.Join(texts, "\n")
}
