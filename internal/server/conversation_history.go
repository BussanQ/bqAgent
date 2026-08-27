package server

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"bqagent/internal/session"
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

// ConversationHistoryMessage is the provider-neutral text representation used by
// terminal and web clients. Tool and system messages are intentionally omitted.
type ConversationHistoryMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ConversationHistory is a read-only view of a persisted chat session.
type ConversationHistory struct {
	ID       string
	Title    string
	Messages []ConversationHistoryMessage
	Omitted  int
}

type conversationHistoryLoadError struct{ err error }

func (loadErr conversationHistoryLoadError) Error() string { return loadErr.err.Error() }
func (loadErr conversationHistoryLoadError) Unwrap() error { return loadErr.err }

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
	id := strings.Trim(strings.TrimPrefix(request.URL.Path, "/api/v1/webui/conversations/"), "/")
	service, err := handler.serviceForWorkspace(request.URL.Query().Get("workspace_id"))
	if err != nil {
		writeError(writer, http.StatusNotFound, chatResponse{Error: err.Error()})
		return
	}
	if request.Method == http.MethodDelete {
		handler.deleteConversation(writer, service, id)
		return
	}
	if request.Method != http.MethodGet {
		writeError(writer, http.StatusMethodNotAllowed, chatResponse{Error: "method not allowed"})
		return
	}
	history, err := service.ConversationHistory(id, 0)
	if errors.Is(err, os.ErrNotExist) {
		writeError(writer, http.StatusNotFound, chatResponse{Error: "conversation not found"})
		return
	}
	var loadErr conversationHistoryLoadError
	if errors.As(err, &loadErr) {
		writeError(writer, http.StatusInternalServerError, chatResponse{Error: err.Error()})
		return
	}
	if err != nil {
		writeError(writer, http.StatusBadRequest, chatResponse{Error: err.Error()})
		return
	}
	webMessages := make([]conversationMessage, 0, len(history.Messages))
	for _, message := range history.Messages {
		webMessages = append(webMessages, conversationMessage(message))
	}
	writeJSON(writer, http.StatusOK, map[string]any{"id": history.ID, "title": history.Title, "messages": webMessages})
}

// ConversationHistory returns recent user and assistant text. When maxBytes is
// positive, complete messages are selected from newest to oldest until the
// budget is exhausted. A single oversized newest message is truncated only in
// this returned view; the persisted session is never modified.
func (service *Service) ConversationHistory(id string, maxBytes int) (ConversationHistory, error) {
	canonicalID, err := session.CanonicalID(id)
	if err != nil {
		return ConversationHistory{}, err
	}
	unlock := service.locker.Lock(canonicalID)
	defer unlock()
	saved, err := service.store.Open(canonicalID)
	if err != nil {
		return ConversationHistory{}, err
	}
	messages, err := saved.LoadMessages()
	if err != nil {
		return ConversationHistory{}, conversationHistoryLoadError{err: fmt.Errorf("load conversation history: %w", err)}
	}
	filtered := make([]ConversationHistoryMessage, 0, len(messages))
	for _, message := range messages {
		role, _ := message["role"].(string)
		content := conversationMessageText(message["content"])
		if (role == "user" || role == "assistant") && strings.TrimSpace(content) != "" {
			filtered = append(filtered, ConversationHistoryMessage{Role: role, Content: content})
		}
	}
	result := ConversationHistory{ID: saved.ID(), Title: saved.Meta().Task, Messages: filtered}
	if maxBytes <= 0 || historyBytes(filtered) <= maxBytes {
		return result, nil
	}
	selected := make([]ConversationHistoryMessage, 0, len(filtered))
	used := 0
	for index := len(filtered) - 1; index >= 0; index-- {
		message := filtered[index]
		size := len(message.Role) + len(message.Content)
		if used+size > maxBytes {
			if len(selected) == 0 {
				message.Content = truncateHistoryText(message.Content, maxBytes-len(message.Role))
				selected = append(selected, message)
				result.Omitted = index
			} else {
				result.Omitted = index + 1
			}
			break
		}
		selected = append(selected, message)
		used += size
	}
	for left, right := 0, len(selected)-1; left < right; left, right = left+1, right-1 {
		selected[left], selected[right] = selected[right], selected[left]
	}
	result.Messages = selected
	return result, nil
}

func historyBytes(messages []ConversationHistoryMessage) int {
	total := 0
	for _, message := range messages {
		total += len(message.Role) + len(message.Content)
	}
	return total
}

func truncateHistoryText(value string, budget int) string {
	const suffix = "\n\n… [消息过长，显示已截断]"
	if budget <= len(suffix) {
		return historyUTF8Prefix(suffix, budget)
	}
	limit := budget - len(suffix)
	if len(value) <= limit {
		return value
	}
	for limit > 0 && limit < len(value) && value[limit]&0xc0 == 0x80 {
		limit--
	}
	return value[:limit] + suffix
}

func historyUTF8Prefix(value string, limit int) string {
	limit = min(max(0, limit), len(value))
	for limit > 0 && limit < len(value) && value[limit]&0xc0 == 0x80 {
		limit--
	}
	return value[:limit]
}

func (handler *handler) deleteConversation(writer http.ResponseWriter, service *Service, id string) {
	canonicalID, err := session.CanonicalID(id)
	if err != nil {
		writeError(writer, http.StatusBadRequest, chatResponse{Error: err.Error()})
		return
	}
	unlock := service.locker.Lock(canonicalID)
	defer unlock()
	saved, err := service.store.Open(canonicalID)
	if errors.Is(err, os.ErrNotExist) {
		writeError(writer, http.StatusNotFound, chatResponse{Error: "conversation not found"})
		return
	}
	if err != nil {
		writeError(writer, http.StatusBadRequest, chatResponse{Error: err.Error()})
		return
	}
	if !saved.Meta().Chat {
		writeError(writer, http.StatusNotFound, chatResponse{Error: "conversation not found"})
		return
	}
	if err := service.store.Delete(canonicalID); err != nil {
		writeError(writer, http.StatusInternalServerError, chatResponse{Error: err.Error()})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"deleted": true, "id": canonicalID})
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
