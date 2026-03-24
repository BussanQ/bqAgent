package weixin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type ChatState struct {
	UserID                    string    `json:"user_id"`
	SessionID                 string    `json:"session_id,omitempty"`
	LastCompletedContextToken string    `json:"last_completed_context_token,omitempty"`
	PendingContextToken       string    `json:"pending_context_token,omitempty"`
	PendingReply              string    `json:"pending_reply,omitempty"`
	LastError                 string    `json:"last_error,omitempty"`
	UpdatedAt                 time.Time `json:"updated_at,omitempty"`
}

type ChatStateStore struct {
	root string
}

func NewChatStateStore(workspaceRoot string) *ChatStateStore {
	return &ChatStateStore{root: filepath.Join(workspaceRoot, ".agent", "server", "weixin", "chats")}
}

func (store *ChatStateStore) Load(userID string) (ChatState, error) {
	userID = filepath.Base(userID)
	if userID == "." || userID == "" {
		return ChatState{}, fmt.Errorf("user_id is required")
	}
	content, err := os.ReadFile(store.statePath(userID))
	if os.IsNotExist(err) {
		return ChatState{UserID: userID}, nil
	}
	if err != nil {
		return ChatState{}, err
	}
	var state ChatState
	if err := json.Unmarshal(content, &state); err != nil {
		return ChatState{}, err
	}
	if state.UserID == "" {
		state.UserID = userID
	}
	return state, nil
}

func (store *ChatStateStore) Save(state ChatState) error {
	state.UserID = filepath.Base(state.UserID)
	if state.UserID == "." || state.UserID == "" {
		return fmt.Errorf("user_id is required")
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
	return os.WriteFile(store.statePath(state.UserID), content, 0o644)
}

func (store *ChatStateStore) statePath(userID string) string {
	return filepath.Join(store.root, filepath.Base(userID)+".json")
}
