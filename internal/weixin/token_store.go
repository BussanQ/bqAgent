package weixin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

type TokenState struct {
	BotToken  string    `json:"bot_token,omitempty"`
	BaseURL   string    `json:"base_url,omitempty"`
	AccountID string    `json:"account_id,omitempty"`
	UserID    string    `json:"user_id,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

type TokenStore struct {
	path string
}

func NewTokenStore(workspaceRoot string) *TokenStore {
	return &TokenStore{path: filepath.Join(workspaceRoot, ".agent", "server", "weixin", "token.json")}
}

func (store *TokenStore) Load() (TokenState, error) {
	content, err := os.ReadFile(store.path)
	if os.IsNotExist(err) {
		return TokenState{}, nil
	}
	if err != nil {
		return TokenState{}, err
	}
	var state TokenState
	if err := json.Unmarshal(content, &state); err != nil {
		return TokenState{}, err
	}
	return state, nil
}

func (store *TokenStore) Save(state TokenState) error {
	state.UpdatedAt = time.Now().UTC()
	if err := os.MkdirAll(filepath.Dir(store.path), 0o755); err != nil {
		return err
	}
	content, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	content = append(content, '\n')
	return os.WriteFile(store.path, content, 0o600)
}
