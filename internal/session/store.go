package session

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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
	LastRunID     string    `json:"last_run_id,omitempty"`
}

type Store struct {
	workspaceRoot string
	options       Options
}

type TranscriptMode string

const (
	TranscriptModeCompact TranscriptMode = "compact"
	TranscriptModeFull    TranscriptMode = "full"
	DefaultOutputMaxBytes int64          = 1 << 20
)

type Options struct {
	TranscriptMode TranscriptMode
	OutputMaxBytes int64
}

func DefaultOptions() Options {
	return Options{TranscriptMode: TranscriptModeCompact, OutputMaxBytes: DefaultOutputMaxBytes}
}

func NormalizeTranscriptMode(value string) TranscriptMode {
	switch TranscriptMode(strings.ToLower(strings.TrimSpace(value))) {
	case TranscriptModeFull:
		return TranscriptModeFull
	default:
		return TranscriptModeCompact
	}
}

func NormalizeOptions(options Options) Options {
	options.TranscriptMode = NormalizeTranscriptMode(string(options.TranscriptMode))
	if options.OutputMaxBytes < 0 {
		options.OutputMaxBytes = DefaultOutputMaxBytes
	}
	return options
}

type ContextCheckpoint struct {
	Summary      string           `json:"summary"`
	TailMessages []map[string]any `json:"tail_messages"`
	SystemPrompt string           `json:"system_prompt,omitempty"`
	UpdatedAt    time.Time        `json:"updated_at"`
}

type Session struct {
	store *Store
	meta  Meta
}

func NewStore(workspaceRoot string, configured ...Options) *Store {
	options := DefaultOptions()
	if len(configured) > 0 {
		options = NormalizeOptions(configured[0])
	}
	return &Store{workspaceRoot: workspaceRoot, options: options}
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

func (session *Session) CheckpointPath() string {
	return filepath.Join(session.Dir(), "context_checkpoint.json")
}

func (session *Session) WorkingMessagesPath() string {
	return filepath.Join(session.Dir(), "working_messages.jsonl")
}

func (session *Session) LoadMessages() ([]map[string]any, error) {
	return readMessagesJSONL(session.MessagesPath())
}

func (session *Session) LoadWorkingMessages() ([]map[string]any, error) {
	if _, err := os.Stat(session.WorkingMessagesPath()); err != nil {
		return nil, err
	}
	return readMessagesJSONL(session.WorkingMessagesPath())
}

func (session *Session) LoadResumableMessages() ([]map[string]any, bool, error) {
	workingInfo, workingErr := os.Stat(session.WorkingMessagesPath())
	messageInfo, messageErr := os.Stat(session.MessagesPath())
	if workingErr == nil && (messageErr != nil || !messageInfo.ModTime().After(workingInfo.ModTime())) {
		messages, err := session.LoadWorkingMessages()
		if err == nil {
			return messages, true, nil
		}
		if messageErr == nil {
			fallback, fallbackErr := session.LoadMessages()
			if fallbackErr == nil {
				return fallback, false, nil
			}
			return nil, false, errors.Join(err, fallbackErr)
		}
		return nil, true, err
	}
	if messageErr == nil {
		messages, err := session.LoadMessages()
		if err == nil {
			return messages, false, nil
		}
		if workingErr == nil {
			fallback, fallbackErr := session.LoadWorkingMessages()
			if fallbackErr == nil {
				return fallback, true, nil
			}
			return nil, false, errors.Join(err, fallbackErr)
		}
		return nil, false, err
	}
	if workingErr == nil {
		messages, err := session.LoadWorkingMessages()
		return messages, true, err
	}
	if !os.IsNotExist(messageErr) {
		return nil, false, messageErr
	}
	if !os.IsNotExist(workingErr) {
		return nil, false, workingErr
	}
	return nil, false, nil
}

func (session *Session) SaveWorkingMessages(messages []map[string]any) error {
	if err := os.MkdirAll(session.Dir(), 0o755); err != nil {
		return err
	}
	return writeMessagesJSONL(session.WorkingMessagesPath(), messages)
}

func (session *Session) SaveWorkingContext(messages []map[string]any) error {
	if session.store.options.TranscriptMode == TranscriptModeCompact {
		if err := writeMessagesJSONL(session.MessagesPath(), messages); err != nil {
			return err
		}
	}
	return session.SaveWorkingMessages(messages)
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

func (session *Session) RewriteMessages(messages []map[string]any) error {
	if err := os.MkdirAll(session.Dir(), 0o755); err != nil {
		return err
	}
	return writeMessagesJSONL(session.MessagesPath(), messages)
}

func (session *Session) SaveCheckpoint(checkpoint ContextCheckpoint) error {
	if err := os.MkdirAll(session.Dir(), 0o755); err != nil {
		return err
	}
	content, err := json.MarshalIndent(checkpoint, "", "  ")
	if err != nil {
		return err
	}
	content = append(content, '\n')
	return os.WriteFile(session.CheckpointPath(), content, 0o644)
}

func (session *Session) LoadCheckpoint() (ContextCheckpoint, error) {
	content, err := os.ReadFile(session.CheckpointPath())
	if err != nil {
		return ContextCheckpoint{}, err
	}
	var checkpoint ContextCheckpoint
	if err := json.Unmarshal(content, &checkpoint); err != nil {
		return ContextCheckpoint{}, err
	}
	return checkpoint, nil
}

func (session *Session) SaveCheckpointSummary(summary string, tailMessages []map[string]any, systemPrompt string) error {
	clonedTail := make([]map[string]any, len(tailMessages))
	for i, message := range tailMessages {
		copyMessage := make(map[string]any, len(message))
		for key, value := range message {
			copyMessage[key] = value
		}
		clonedTail[i] = copyMessage
	}
	checkpoint := ContextCheckpoint{
		Summary:      strings.TrimSpace(summary),
		TailMessages: clonedTail,
		SystemPrompt: systemPrompt,
		UpdatedAt:    time.Now().UTC(),
	}
	return session.SaveCheckpoint(checkpoint)
}

func (session *Session) OpenOutputFile() (*os.File, error) {
	if err := os.MkdirAll(session.Dir(), 0o755); err != nil {
		return nil, err
	}
	return os.OpenFile(session.OutputPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
}

func (session *Session) TrimOutputLog() error {
	limit := session.store.options.OutputMaxBytes
	if limit <= 0 {
		return nil
	}
	file, err := os.Open(session.OutputPath())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return err
	}
	if info.Size() <= limit {
		return file.Close()
	}
	if _, err := file.Seek(info.Size()-limit, io.SeekStart); err != nil {
		_ = file.Close()
		return err
	}
	tail, err := io.ReadAll(file)
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if index := bytes.IndexByte(tail, '\n'); index >= 0 && index+1 < len(tail) {
		tail = tail[index+1:]
	}
	return writeFileAtomic(session.OutputPath(), tail, 0o644)
}

func (s *Store) MaintainExistingSessions() []error {
	if s == nil || s.options.TranscriptMode != TranscriptModeCompact {
		return nil
	}
	root := filepath.Join(s.workspaceRoot, ".agent", "sessions")
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return []error{err}
	}
	errorsFound := make([]error, 0)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		saved, openErr := s.Open(entry.Name())
		if openErr != nil {
			errorsFound = append(errorsFound, fmt.Errorf("session %s: %w", entry.Name(), openErr))
			continue
		}
		if saved.Meta().Status == StatusRunning {
			continue
		}
		working, loadErr := saved.LoadWorkingMessages()
		if loadErr == nil && len(working) > 0 {
			if rewriteErr := saved.RewriteMessages(working); rewriteErr != nil {
				errorsFound = append(errorsFound, fmt.Errorf("session %s compact transcript: %w", entry.Name(), rewriteErr))
			}
		} else if loadErr != nil && !os.IsNotExist(loadErr) {
			errorsFound = append(errorsFound, fmt.Errorf("session %s load working messages: %w", entry.Name(), loadErr))
		}
		if trimErr := saved.TrimOutputLog(); trimErr != nil {
			errorsFound = append(errorsFound, fmt.Errorf("session %s trim output: %w", entry.Name(), trimErr))
		}
	}
	return errorsFound
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

func (session *Session) SetLastRunID(runID string) error {
	session.meta.LastRunID = strings.TrimSpace(runID)
	session.meta.UpdatedAt = time.Now().UTC()
	return session.persistMeta()
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
