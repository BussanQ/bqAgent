package session

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"bqagent/internal/atomicfile"
	"bqagent/internal/safepath"
)

type Status string

const (
	StatusCreated   Status = "created"
	StatusRunning   Status = "running"
	StatusCompleted Status = "completed"
	StatusFailed    Status = "failed"
)

type Meta struct {
	ID                  string    `json:"id"`
	WorkspaceRoot       string    `json:"workspace_root"`
	Task                string    `json:"task,omitempty"`
	Planned             bool      `json:"planned,omitempty"`
	Background          bool      `json:"background,omitempty"`
	Chat                bool      `json:"chat,omitempty"`
	Status              Status    `json:"status"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
	LastError           string    `json:"last_error,omitempty"`
	LastRunID           string    `json:"last_run_id,omitempty"`
	CurrentModel        string    `json:"current_model,omitempty"`
	CurrentMode         string    `json:"current_mode,omitempty"`
	ConversationType    string    `json:"conversation_type,omitempty"`
	PromptSchemaVersion int       `json:"prompt_schema_version,omitempty"`
	PromptStableHash    string    `json:"prompt_stable_hash,omitempty"`
	PromptMessageCount  int       `json:"prompt_message_count,omitempty"`
}

type Store struct {
	workspaceRoot string
	agentDir      string
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
	// AgentDir controls where the shared sessions directory is stored. An empty
	// value preserves the legacy workspace-local <workspace>/.agent location.
	AgentDir string
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
	Summary                string           `json:"summary"`
	TailMessages           []map[string]any `json:"tail_messages"`
	SystemPrompt           string           `json:"system_prompt,omitempty"`
	PromptStableHash       string           `json:"prompt_stable_hash,omitempty"`
	PromptMessageCount     int              `json:"prompt_message_count,omitempty"`
	SourceTranscriptSHA256 string           `json:"source_transcript_sha256,omitempty"`
	SourceTranscriptSize   int64            `json:"source_transcript_size,omitempty"`
	UpdatedAt              time.Time        `json:"updated_at"`
}

// TranscriptProvenance identifies the exact raw messages.jsonl content used to
// build a context checkpoint. ModTime is retained only for legacy checkpoints
// that predate the content-based fields.
type TranscriptProvenance struct {
	SHA256  string
	Size    int64
	ModTime time.Time
}

type Session struct {
	store *Store
	meta  Meta
	dir   string
}

func NewStore(workspaceRoot string, configured ...Options) *Store {
	options := DefaultOptions()
	if len(configured) > 0 {
		options = NormalizeOptions(configured[0])
	}
	agentDir := strings.TrimSpace(options.AgentDir)
	if agentDir == "" {
		agentDir = filepath.Join(workspaceRoot, ".agent")
	}
	return &Store{workspaceRoot: filepath.Clean(workspaceRoot), agentDir: filepath.Clean(agentDir), options: options}
}

type CreateOptions struct {
	Task             string
	Planned          bool
	Background       bool
	Chat             bool
	ConversationType string
}

func (s *Store) Create(options CreateOptions) (*Session, error) {
	id, err := newSessionID()
	if err != nil {
		return nil, err
	}

	dir, err := s.sessionDir(id)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	session := &Session{
		store: s,
		dir:   dir,
		meta: Meta{
			ID:               id,
			WorkspaceRoot:    s.workspaceRoot,
			Task:             options.Task,
			Planned:          options.Planned,
			Background:       options.Background,
			Chat:             options.Chat,
			ConversationType: strings.TrimSpace(options.ConversationType),
			Status:           StatusCreated,
			CreatedAt:        now,
			UpdatedAt:        now,
		},
	}
	if err := session.persistMeta(); err != nil {
		return nil, err
	}
	return session, nil
}

func (s *Store) Open(id string) (*Session, error) {
	canonicalID, err := CanonicalID(id)
	if err != nil {
		return nil, err
	}
	dir, err := s.sessionDir(canonicalID)
	if err != nil {
		return nil, err
	}
	content, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		return nil, err
	}

	var meta Meta
	if err := json.Unmarshal(content, &meta); err != nil {
		return nil, err
	}
	metaID, err := CanonicalID(meta.ID)
	if err != nil {
		return nil, fmt.Errorf("invalid session metadata id: %w", err)
	}
	if metaID != canonicalID {
		return nil, fmt.Errorf("session metadata id %q does not match requested id %q", meta.ID, canonicalID)
	}
	meta.ID = canonicalID
	if meta.WorkspaceRoot == "" {
		meta.WorkspaceRoot = s.workspaceRoot
	} else if filepath.Clean(meta.WorkspaceRoot) != s.workspaceRoot {
		return nil, fmt.Errorf("%w: session %q belongs to %q, current workspace is %q", ErrWorkspaceMismatch, canonicalID, meta.WorkspaceRoot, s.workspaceRoot)
	}
	return &Session{store: s, meta: meta, dir: dir}, nil
}

// Delete removes one validated session owned by this store's workspace.
func (s *Store) Delete(id string) error {
	saved, err := s.Open(id)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(filepath.Join(s.agentDir, "sessions"))
	if err != nil {
		return err
	}
	defer root.Close()
	return root.RemoveAll(saved.ID())
}

// List returns sessions belonging to this store's workspace, newest first.
// Invalid entries and sessions owned by another workspace are ignored.
func (s *Store) List(limit int) ([]Meta, error) {
	root := filepath.Join(s.agentDir, "sessions")
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return []Meta{}, nil
	}
	if err != nil {
		return nil, err
	}
	metas := make([]Meta, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		saved, openErr := s.Open(entry.Name())
		if openErr != nil {
			if errors.Is(openErr, ErrWorkspaceMismatch) {
				continue
			}
			continue
		}
		if saved.meta.Chat {
			metas = append(metas, saved.meta)
		}
	}
	sort.Slice(metas, func(left, right int) bool { return metas[left].UpdatedAt.After(metas[right].UpdatedAt) })
	if limit > 0 && len(metas) > limit {
		metas = metas[:limit]
	}
	return metas, nil
}

var ErrWorkspaceMismatch = errors.New("session belongs to another workspace")

// CanonicalID trims an externally supplied session ID and verifies it is one
// safe filesystem path component.
func CanonicalID(id string) (string, error) {
	id = strings.TrimSpace(id)
	if err := safepath.ValidateComponent(id); err != nil {
		return "", fmt.Errorf("invalid session id: %w", err)
	}
	return id, nil
}

func (s *Store) sessionDir(id string) (string, error) {
	id, err := CanonicalID(id)
	if err != nil {
		return "", err
	}
	// Resolve from the agent directory's existing parent so first use can create
	// ~/.agent/sessions without weakening symlink containment checks.
	return safepath.Resolve(filepath.Dir(s.agentDir), filepath.Join(filepath.Base(s.agentDir), "sessions", id))
}

func (session *Session) ID() string {
	return session.meta.ID
}

func (session *Session) Meta() Meta {
	return session.meta
}

func (session *Session) Dir() string {
	return session.dir
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

// LoadTranscriptProvenance returns the SHA-256 and byte size of the raw
// messages.jsonl file. A missing transcript is represented as an empty file;
// this allows a checkpoint made before the first message to be identified too.
func (session *Session) LoadTranscriptProvenance() (TranscriptProvenance, error) {
	content, err := os.ReadFile(session.MessagesPath())
	if os.IsNotExist(err) {
		content = nil
	} else if err != nil {
		return TranscriptProvenance{}, err
	}

	provenance := TranscriptProvenance{
		SHA256: fmt.Sprintf("%x", sha256.Sum256(content)),
		Size:   int64(len(content)),
	}
	if info, statErr := os.Stat(session.MessagesPath()); statErr == nil {
		provenance.ModTime = info.ModTime()
	} else if !os.IsNotExist(statErr) {
		return TranscriptProvenance{}, statErr
	}
	return provenance, nil
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
	return atomicfile.Write(session.CheckpointPath(), content, 0o644)
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
	return session.SaveCheckpointSummaryWithPrompt(summary, tailMessages, systemPrompt, "", 1)
}

func (session *Session) SaveCheckpointSummaryWithPrompt(summary string, tailMessages []map[string]any, systemPrompt, stableHash string, promptMessageCount int) error {
	provenance, err := session.LoadTranscriptProvenance()
	if err != nil {
		return err
	}

	clonedTail := make([]map[string]any, len(tailMessages))
	for i, message := range tailMessages {
		copyMessage := make(map[string]any, len(message))
		for key, value := range message {
			copyMessage[key] = value
		}
		clonedTail[i] = copyMessage
	}
	checkpoint := ContextCheckpoint{
		Summary:                strings.TrimSpace(summary),
		TailMessages:           clonedTail,
		SystemPrompt:           systemPrompt,
		PromptStableHash:       strings.TrimSpace(stableHash),
		PromptMessageCount:     promptMessageCount,
		SourceTranscriptSHA256: provenance.SHA256,
		SourceTranscriptSize:   provenance.Size,
		UpdatedAt:              time.Now().UTC(),
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
	root := filepath.Join(s.agentDir, "sessions")
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
			if errors.Is(openErr, ErrWorkspaceMismatch) {
				continue
			}
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

func (session *Session) SetCurrentModel(model string) error {
	session.meta.CurrentModel = strings.TrimSpace(model)
	session.meta.UpdatedAt = time.Now().UTC()
	return session.persistMeta()
}

func (session *Session) SetCurrentMode(mode string) error {
	session.meta.CurrentMode = strings.TrimSpace(mode)
	session.meta.UpdatedAt = time.Now().UTC()
	return session.persistMeta()
}

func (session *Session) SetConversationType(conversationType string) error {
	session.meta.ConversationType = strings.TrimSpace(conversationType)
	session.meta.UpdatedAt = time.Now().UTC()
	return session.persistMeta()
}

func (session *Session) SetPromptSnapshot(schemaVersion int, stableHash string, messageCount int) error {
	session.meta.PromptSchemaVersion = schemaVersion
	session.meta.PromptStableHash = strings.TrimSpace(stableHash)
	session.meta.PromptMessageCount = messageCount
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
	return atomicfile.Write(filepath.Join(session.Dir(), "meta.json"), content, 0o644)
}

func newSessionID() (string, error) {
	random := make([]byte, 4)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s-%s", time.Now().UTC().Format("20060102T150405Z"), hex.EncodeToString(random)), nil
}
