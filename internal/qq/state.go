package qq

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type ChatState struct {
	PeerKey          string    `json:"peer_key"`
	SessionID        string    `json:"session_id,omitempty"`
	LastCompletedKey string    `json:"last_completed_key,omitempty"`
	PendingKey       string    `json:"pending_key,omitempty"`
	PendingReply     string    `json:"pending_reply,omitempty"`
	LastError        string    `json:"last_error,omitempty"`
	UpdatedAt        time.Time `json:"updated_at,omitempty"`
}

type StateStore struct {
	root string
}

func NewStateStore(workspaceRoot string) *StateStore {
	return &StateStore{root: filepath.Join(workspaceRoot, ".agent", "server", "qq-bot", "chats")}
}

func (store *StateStore) Load(peerKey string) (ChatState, error) {
	peerKey = strings.TrimSpace(peerKey)
	if peerKey == "" {
		return ChatState{}, fmt.Errorf("peer_key is required")
	}
	content, err := os.ReadFile(store.statePath(peerKey))
	if os.IsNotExist(err) {
		return ChatState{PeerKey: peerKey}, nil
	}
	if err != nil {
		return ChatState{}, err
	}
	var state ChatState
	if err := json.Unmarshal(content, &state); err != nil {
		return ChatState{}, err
	}
	if strings.TrimSpace(state.PeerKey) == "" {
		state.PeerKey = peerKey
	}
	return state, nil
}

func (store *StateStore) Save(state ChatState) error {
	state.PeerKey = strings.TrimSpace(state.PeerKey)
	if state.PeerKey == "" {
		return fmt.Errorf("peer_key is required")
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
	return os.WriteFile(store.statePath(state.PeerKey), content, 0o644)
}

func (store *StateStore) statePath(peerKey string) string {
	encoded := base64.RawURLEncoding.EncodeToString([]byte(peerKey))
	return filepath.Join(store.root, encoded+".json")
}
