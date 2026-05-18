package qq

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

type GatewaySessionState struct {
	SessionID string    `json:"session_id,omitempty"`
	Seq       int64     `json:"seq,omitempty"`
	LastError string    `json:"last_error,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

type GatewayStateStore struct {
	path string
}

func NewGatewayStateStore(workspaceRoot string) *GatewayStateStore {
	return &GatewayStateStore{path: filepath.Join(workspaceRoot, ".agent", "server", "qq-bot", "gateway.json")}
}

func (store *GatewayStateStore) Load() (GatewaySessionState, error) {
	content, err := os.ReadFile(store.path)
	if os.IsNotExist(err) {
		return GatewaySessionState{}, nil
	}
	if err != nil {
		return GatewaySessionState{}, err
	}
	var state GatewaySessionState
	if err := json.Unmarshal(content, &state); err != nil {
		return GatewaySessionState{}, err
	}
	return state, nil
}

func (store *GatewayStateStore) Save(state GatewaySessionState) error {
	state.UpdatedAt = time.Now().UTC()
	if err := os.MkdirAll(filepath.Dir(store.path), 0o755); err != nil {
		return err
	}
	content, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	content = append(content, '\n')
	return os.WriteFile(store.path, content, 0o644)
}

func (store *GatewayStateStore) ClearSession() error {
	state, err := store.Load()
	if err != nil {
		return err
	}
	state.SessionID = ""
	state.Seq = 0
	return store.Save(state)
}
