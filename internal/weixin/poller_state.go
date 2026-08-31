package weixin

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"bqagent/internal/atomicfile"
)

type PollerState struct {
	GetUpdatesBuf string    `json:"get_updates_buf,omitempty"`
	LastError     string    `json:"last_error,omitempty"`
	UpdatedAt     time.Time `json:"updated_at,omitempty"`
}

type PollerStateStore struct {
	path string
}

func NewPollerStateStore(agentDir string) *PollerStateStore {
	return &PollerStateStore{path: filepath.Join(agentDir, "server", "weixin", "poller.json")}
}

func (store *PollerStateStore) Load() (PollerState, error) {
	content, err := os.ReadFile(store.path)
	if os.IsNotExist(err) {
		return PollerState{}, nil
	}
	if err != nil {
		return PollerState{}, err
	}
	if len(bytes.TrimSpace(content)) == 0 {
		return PollerState{}, nil
	}
	var state PollerState
	if err := json.Unmarshal(content, &state); err != nil {
		return PollerState{}, err
	}
	return state, nil
}

func (store *PollerStateStore) Save(state PollerState) error {
	state.UpdatedAt = time.Now().UTC()
	content, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	content = append(content, '\n')
	return atomicfile.Write(store.path, content, 0o644)
}
