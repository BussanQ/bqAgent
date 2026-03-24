package weixin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

type PollerState struct {
	GetUpdatesBuf string    `json:"get_updates_buf,omitempty"`
	LastError     string    `json:"last_error,omitempty"`
	UpdatedAt     time.Time `json:"updated_at,omitempty"`
}

type PollerStateStore struct {
	path string
}

func NewPollerStateStore(workspaceRoot string) *PollerStateStore {
	return &PollerStateStore{path: filepath.Join(workspaceRoot, ".agent", "server", "weixin", "poller.json")}
}

func (store *PollerStateStore) Load() (PollerState, error) {
	content, err := os.ReadFile(store.path)
	if os.IsNotExist(err) {
		return PollerState{}, nil
	}
	if err != nil {
		return PollerState{}, err
	}
	var state PollerState
	if err := json.Unmarshal(content, &state); err != nil {
		return PollerState{}, err
	}
	return state, nil
}

func (store *PollerStateStore) Save(state PollerState) error {
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
