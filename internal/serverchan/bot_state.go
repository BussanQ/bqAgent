package serverchan

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type BotChatState struct {
	ChatID                int64     `json:"chat_id"`
	SessionID             string    `json:"session_id,omitempty"`
	LastCompletedUpdateID int64     `json:"last_completed_update_id,omitempty"`
	PendingUpdateID       int64     `json:"pending_update_id,omitempty"`
	PendingReply          string    `json:"pending_reply,omitempty"`
	UpdatedAt             time.Time `json:"updated_at,omitempty"`
	LastError             string    `json:"last_error,omitempty"`
}

type BotStateStore struct {
	root string
}

func NewBotStateStore(workspaceRoot string) *BotStateStore {
	return &BotStateStore{root: filepath.Join(workspaceRoot, ".agent", "server", "serverchan-bot", "chats")}
}

func (store *BotStateStore) Load(chatID int64) (BotChatState, error) {
	if chatID <= 0 {
		return BotChatState{}, fmt.Errorf("chat_id is required")
	}
	path := store.statePath(chatID)
	content, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return BotChatState{ChatID: chatID}, nil
	}
	if err != nil {
		return BotChatState{}, err
	}
	var state BotChatState
	if err := json.Unmarshal(content, &state); err != nil {
		return BotChatState{}, err
	}
	if state.ChatID == 0 {
		state.ChatID = chatID
	}
	return state, nil
}

func (store *BotStateStore) Save(state BotChatState) error {
	if state.ChatID <= 0 {
		return fmt.Errorf("chat_id is required")
	}
	state.UpdatedAt = time.Now().UTC()
	if err := os.MkdirAll(store.root, 0o755); err != nil {
		return err
	}
	content, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	content = append(content, '\n')
	return os.WriteFile(store.statePath(state.ChatID), content, 0o644)
}

func (store *BotStateStore) statePath(chatID int64) string {
	return filepath.Join(store.root, fmt.Sprintf("%d.json", chatID))
}
