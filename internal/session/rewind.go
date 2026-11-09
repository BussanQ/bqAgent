package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"bqagent/internal/privatefile"
)

var ErrRewindPending = errors.New("上次历史回退尚未确认，请重试该回退或用 /clear 新建上下文")

type PendingRewind struct {
	Agent     string `json:"agent,omitempty"`
	Operation string `json:"operation"`
	Target    string `json:"target"`
	Thread    string `json:"thread"`
	Before    string `json:"before"`
	Head      string `json:"head"`
	Kept      string `json:"kept"` // Empty when reverting the first native turn.
}

func (s *Session) PendingRewind() (*PendingRewind, error) {
	data, err := os.ReadFile(filepath.Join(s.ContextDir(), "pending-rewind.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var p PendingRewind
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	if p.Operation == "" || p.Target == "" || p.Thread == "" || p.Before == "" || p.Head == "" {
		return nil, errors.New("历史回退记录不完整")
	}
	return &p, nil
}

func (s *Session) SavePendingRewind(p PendingRewind) error {
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return privatefile.WriteFile(filepath.Join(s.ContextDir(), "pending-rewind.json"), data)
}

func (s *Session) RemovePendingRewind() error {
	return os.Remove(filepath.Join(s.ContextDir(), "pending-rewind.json"))
}
