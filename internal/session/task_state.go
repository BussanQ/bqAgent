package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"bqagent/internal/atomicfile"
	"bqagent/internal/taskstate"
)

func (session *Session) LoadTaskState() (*taskstate.State, error) {
	data, err := os.ReadFile(filepath.Join(session.dir, "task.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var state taskstate.State
	if err = json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("load task state: %w", err)
	}
	if err = state.Validate(); err != nil {
		return nil, fmt.Errorf("load task state: %w", err)
	}
	return &state, nil
}

func (session *Session) SaveTaskState(state taskstate.State) error {
	if err := state.Validate(); err != nil {
		return err
	}
	state.UpdatedAt = time.Now().UTC()
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(filepath.Join(session.dir, "task.json"), append(data, '\n'), 0600)
}
