package extagent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type StateStore struct {
	root       string
	legacyRoot string
}

func NewStateStore(workspaceRoot string) *StateStore {
	return &StateStore{
		root:       filepath.Join(workspaceRoot, ".agent", "external-agents", "sessions"),
		legacyRoot: filepath.Join(workspaceRoot, ".agent", "server", "external-agents", "sessions"),
	}
}

func (store *StateStore) Load(sessionID string) (SessionState, error) {
	sessionID = filepath.Base(sessionID)
	if sessionID == "." || sessionID == "" {
		return SessionState{}, fmt.Errorf("session_id is required")
	}
	content, err := os.ReadFile(store.path(sessionID))
	if os.IsNotExist(err) && store.legacyRoot != "" {
		content, err = os.ReadFile(store.legacyPath(sessionID))
	}
	if os.IsNotExist(err) {
		return SessionState{BQSessionID: sessionID}, nil
	}
	if err != nil {
		return SessionState{}, err
	}
	var state SessionState
	if err := json.Unmarshal(content, &state); err != nil {
		return SessionState{}, err
	}
	if state.BQSessionID == "" {
		state.BQSessionID = sessionID
	}
	return state, nil
}

func (store *StateStore) Save(state SessionState) error {
	state.BQSessionID = filepath.Base(state.BQSessionID)
	if state.BQSessionID == "." || state.BQSessionID == "" {
		return fmt.Errorf("session_id is required")
	}
	if err := os.MkdirAll(store.root, 0o755); err != nil {
		return err
	}
	content, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	content = append(content, '\n')
	return os.WriteFile(store.path(state.BQSessionID), content, 0o644)
}

func (store *StateStore) Clear(sessionID string) error {
	sessionID = filepath.Base(sessionID)
	if sessionID == "." || sessionID == "" {
		return fmt.Errorf("session_id is required")
	}
	err := os.Remove(store.path(sessionID))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if store.legacyRoot != "" {
		err = os.Remove(store.legacyPath(sessionID))
		if err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func (store *StateStore) path(sessionID string) string {
	return filepath.Join(store.root, filepath.Base(sessionID)+".json")
}

func (store *StateStore) legacyPath(sessionID string) string {
	return filepath.Join(store.legacyRoot, filepath.Base(sessionID)+".json")
}
