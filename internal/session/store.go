package session

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type Status string

const (
	StatusCreated   Status = "created"
	StatusRunning   Status = "running"
	StatusCompleted Status = "completed"
	StatusFailed    Status = "failed"
)

type Meta struct {
	ID            string    `json:"id"`
	WorkspaceRoot string    `json:"workspace_root"`
	Task          string    `json:"task,omitempty"`
	Planned       bool      `json:"planned,omitempty"`
	Background    bool      `json:"background,omitempty"`
	Chat          bool      `json:"chat,omitempty"`
	Status        Status    `json:"status"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	LastError     string    `json:"last_error,omitempty"`
}

type Store struct {
	workspaceRoot string
}

type Session struct {
	store *Store
	meta  Meta
}

func NewStore(workspaceRoot string) *Store {
	return &Store{workspaceRoot: workspaceRoot}
}

type CreateOptions struct {
	Task       string
	Planned    bool
	Background bool
	Chat       bool
}

func (s *Store) Create(options CreateOptions) (*Session, error) {
	id, err := newSessionID()
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	session := &Session{
		store: s,
		meta: Meta{
			ID:            id,
			WorkspaceRoot: s.workspaceRoot,
			Task:          options.Task,
			Planned:       options.Planned,
			Background:    options.Background,
			Chat:          options.Chat,
			Status:        StatusCreated,
			CreatedAt:     now,
			UpdatedAt:     now,
		},
	}
	if err := session.persistMeta(); err != nil {
		return nil, err
	}
	return session, nil
}

func (s *Store) Open(id string) (*Session, error) {
	content, err := os.ReadFile(s.metaPath(id))
	if err != nil {
		return nil, err
	}

	var meta Meta
	if err := json.Unmarshal(content, &meta); err != nil {
		return nil, err
	}
	if meta.WorkspaceRoot == "" {
		meta.WorkspaceRoot = s.workspaceRoot
	}
	return &Session{store: s, meta: meta}, nil
}

func (s *Store) metaPath(id string) string {
	return filepath.Join(s.workspaceRoot, ".agent", "sessions", id, "meta.json")
}

func (session *Session) ID() string {
	return session.meta.ID
}

func (session *Session) Meta() Meta {
	return session.meta
}

func (session *Session) Dir() string {
	return filepath.Join(session.store.workspaceRoot, ".agent", "sessions", session.meta.ID)
}

func (session *Session) MessagesPath() string {
	return filepath.Join(session.Dir(), "messages.jsonl")
}

func (session *Session) OutputPath() string {
	return filepath.Join(session.Dir(), "output.log")
}

func (session *Session) LoadMessages() ([]map[string]any, error) {
	return readMessagesJSONL(session.MessagesPath())
}

func (session *Session) RecordMessage(message map[string]any) error {
	return session.RecordMessages(message)
}

func (session *Session) RecordMessages(messages ...map[string]any) error {
	if err := os.MkdirAll(session.Dir(), 0o755); err != nil {
		return err
	}

	entries := make([]any, 0, len(messages))
	for _, message := range messages {
		entries = append(entries, message)
	}
	return appendJSONL(session.MessagesPath(), entries...)
}

func (session *Session) OpenOutputFile() (*os.File, error) {
	if err := os.MkdirAll(session.Dir(), 0o755); err != nil {
		return nil, err
	}
	return os.OpenFile(session.OutputPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
}

func (session *Session) MarkRunning() error {
	return session.updateStatus(StatusRunning, "")
}

func (session *Session) MarkCompleted() error {
	return session.updateStatus(StatusCompleted, "")
}

func (session *Session) MarkFailed(err error) error {
	message := ""
	if err != nil {
		message = err.Error()
	}
	return session.updateStatus(StatusFailed, message)
}

func (session *Session) updateStatus(status Status, lastError string) error {
	session.meta.Status = status
	session.meta.LastError = lastError
	session.meta.UpdatedAt = time.Now().UTC()
	return session.persistMeta()
}

func (session *Session) persistMeta() error {
	if err := os.MkdirAll(session.Dir(), 0o755); err != nil {
		return err
	}

	content, err := json.MarshalIndent(session.meta, "", "  ")
	if err != nil {
		return err
	}
	content = append(content, '\n')
	return os.WriteFile(filepath.Join(session.Dir(), "meta.json"), content, 0o644)
}

func newSessionID() (string, error) {
	random := make([]byte, 4)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s-%s", time.Now().UTC().Format("20060102T150405Z"), hex.EncodeToString(random)), nil
}
