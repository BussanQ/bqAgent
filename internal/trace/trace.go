package trace

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

type contextKey string

const runIDContextKey contextKey = "bqagent-run-id"

func WithRunID(ctx context.Context, runID string) context.Context {
	return context.WithValue(ctx, runIDContextKey, strings.TrimSpace(runID))
}
func RunIDFromContext(ctx context.Context) string {
	value, _ := ctx.Value(runIDContextKey).(string)
	return value
}

type Status string

const (
	StatusRunning   Status = "running"
	StatusCompleted Status = "completed"
	StatusFailed    Status = "failed"
	StatusCanceled  Status = "canceled"
)

type TokenUsage struct {
	PromptTokens     int  `json:"prompt_tokens,omitempty"`
	CompletionTokens int  `json:"completion_tokens,omitempty"`
	TotalTokens      int  `json:"total_tokens,omitempty"`
	Estimated        bool `json:"estimated,omitempty"`
}

type ErrorInfo struct {
	Category string `json:"category"`
	Message  string `json:"message"`
}

type Artifact struct {
	Path   string `json:"path"`
	Kind   string `json:"kind,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
	Size   int64  `json:"size,omitempty"`
}

type VerifierResult struct {
	Name    string `json:"name"`
	Passed  bool   `json:"passed"`
	Message string `json:"message,omitempty"`
}

type RunTrace struct {
	SchemaVersion  int              `json:"schema_version"`
	RunID          string           `json:"run_id"`
	ParentRunID    string           `json:"parent_run_id,omitempty"`
	SessionID      string           `json:"session_id"`
	TurnID         string           `json:"turn_id"`
	Kind           string           `json:"kind"`
	Status         Status           `json:"status"`
	Model          string           `json:"model,omitempty"`
	PromptVersion  string           `json:"prompt_version,omitempty"`
	ContextVersion string           `json:"context_version,omitempty"`
	Usage          TokenUsage       `json:"usage,omitempty"`
	RetryCount     int              `json:"retry_count,omitempty"`
	Error          *ErrorInfo       `json:"error,omitempty"`
	FinalSummary   string           `json:"final_summary,omitempty"`
	Artifacts      []Artifact       `json:"artifacts,omitempty"`
	Verifiers      []VerifierResult `json:"verifiers,omitempty"`
	StartedAt      time.Time        `json:"started_at"`
	FinishedAt     *time.Time       `json:"finished_at,omitempty"`
}

type Event struct {
	Sequence int64          `json:"sequence"`
	Time     time.Time      `json:"time"`
	Type     string         `json:"type"`
	Data     map[string]any `json:"data,omitempty"`
}

type Feedback struct {
	RunID     string    `json:"run_id"`
	Rating    string    `json:"rating"`
	Comment   string    `json:"comment,omitempty"`
	Source    string    `json:"source,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type Store struct {
	root string
}

func NewStore(workspaceRoot string) *Store {
	return &Store{root: filepath.Join(workspaceRoot, ".agent", "runs")}
}

func (s *Store) Create(sessionID, turnID, parentRunID, kind, model, prompt string) (*Recorder, error) {
	if strings.TrimSpace(sessionID) == "" {
		sessionID = NewID("session")
	}
	if strings.TrimSpace(turnID) == "" {
		turnID = NewID("turn")
	}
	meta := RunTrace{
		SchemaVersion: 1,
		RunID:         NewID("run"),
		ParentRunID:   strings.TrimSpace(parentRunID),
		SessionID:     sessionID,
		TurnID:        turnID,
		Kind:          firstNonEmpty(kind, "agent"),
		Status:        StatusRunning,
		Model:         model,
		PromptVersion: HashText(normalizeText(prompt)),
		StartedAt:     time.Now().UTC(),
	}
	recorder := &Recorder{store: s, meta: meta}
	if err := recorder.persistMeta(); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(recorder.dir(), "output.log"), nil, 0o644); err != nil {
		return nil, err
	}
	_ = recorder.Event("run_started", map[string]any{"kind": meta.Kind})
	return recorder, nil
}

func (s *Store) Load(runID string) (RunTrace, error) {
	var meta RunTrace
	content, err := os.ReadFile(filepath.Join(s.root, safeID(runID), "meta.json"))
	if err != nil {
		return meta, err
	}
	err = json.Unmarshal(content, &meta)
	return meta, err
}

func (s *Store) AddFeedback(runID, rating, comment, source string) (Feedback, error) {
	rating = strings.ToLower(strings.TrimSpace(rating))
	if rating != "up" && rating != "down" {
		return Feedback{}, fmt.Errorf("rating must be up or down")
	}
	if _, err := s.Load(runID); err != nil {
		return Feedback{}, err
	}
	feedback := Feedback{RunID: safeID(runID), Rating: rating, Comment: strings.TrimSpace(comment), Source: source, CreatedAt: time.Now().UTC()}
	if err := appendJSONL(filepath.Join(s.root, safeID(runID), "feedback.jsonl"), feedback); err != nil {
		return Feedback{}, err
	}
	return feedback, nil
}

type Recorder struct {
	store *Store
	mu    sync.Mutex
	seq   int64
	meta  RunTrace
}

func (r *Recorder) RunID() string  { return r.meta.RunID }
func (r *Recorder) TurnID() string { return r.meta.TurnID }
func (r *Recorder) OpenOutputFile() (*os.File, error) {
	return os.OpenFile(filepath.Join(r.dir(), "output.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
}

func (r *Recorder) Event(eventType string, data map[string]any) error {
	if r == nil || r.store == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	event := Event{Sequence: r.seq, Time: time.Now().UTC(), Type: eventType, Data: RedactMap(data)}
	return appendJSONL(filepath.Join(r.dir(), "events.jsonl"), event)
}

func (r *Recorder) ModelCall(contextHash string, usage TokenUsage, duration time.Duration, err error) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.meta.ContextVersion = contextHash
	r.meta.Usage.PromptTokens += usage.PromptTokens
	r.meta.Usage.CompletionTokens += usage.CompletionTokens
	r.meta.Usage.TotalTokens += usage.TotalTokens
	r.meta.Usage.Estimated = r.meta.Usage.Estimated || usage.Estimated
	_ = r.persistMetaLocked()
	r.mu.Unlock()
	data := map[string]any{"context_version": contextHash, "usage": usage, "duration_ms": duration.Milliseconds()}
	if err != nil {
		data["error"] = err.Error()
		data["error_category"] = ClassifyError(err)
	}
	_ = r.Event("model_call", data)
}

func (r *Recorder) ToolCall(name string, arguments map[string]any, result string, duration time.Duration, err error) {
	data := map[string]any{
		"name":           name,
		"arguments":      arguments,
		"result_summary": Summarize(result, 4096),
		"result_size":    len(result),
		"result_sha256":  HashText(result),
		"duration_ms":    duration.Milliseconds(),
	}
	if err != nil {
		data["error"] = err.Error()
		data["error_category"] = ClassifyError(err)
	}
	_ = r.Event("tool_call", data)
}

func (r *Recorder) AddArtifact(path, kind string) {
	if r == nil || strings.TrimSpace(path) == "" {
		return
	}
	artifact := Artifact{Path: path, Kind: kind}
	if info, err := os.Stat(path); err == nil && !info.IsDir() {
		artifact.Size = info.Size()
		if content, readErr := os.ReadFile(path); readErr == nil {
			artifact.SHA256 = HashBytes(content)
		}
	}
	r.mu.Lock()
	r.meta.Artifacts = append(r.meta.Artifacts, artifact)
	_ = r.persistMetaLocked()
	_ = writeJSONAtomic(filepath.Join(r.dir(), "artifacts.json"), r.meta.Artifacts)
	r.mu.Unlock()
	_ = r.Event("artifact", map[string]any{"artifact": artifact})
}

func (r *Recorder) AddVerifier(result VerifierResult) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.meta.Verifiers = append(r.meta.Verifiers, result)
	_ = r.persistMetaLocked()
	r.mu.Unlock()
	_ = r.Event("verifier", map[string]any{"name": result.Name, "passed": result.Passed, "message": result.Message})
}

func (r *Recorder) Finish(result string, err error) error {
	if r == nil {
		return nil
	}
	now := time.Now().UTC()
	r.mu.Lock()
	r.meta.FinishedAt = &now
	r.meta.FinalSummary = Summarize(result, 4096)
	if err != nil {
		r.meta.Status = StatusFailed
		r.meta.Error = &ErrorInfo{Category: ClassifyError(err), Message: Summarize(err.Error(), 2048)}
	} else {
		r.meta.Status = StatusCompleted
	}
	persistErr := r.persistMetaLocked()
	r.mu.Unlock()
	data := map[string]any{"status": r.meta.Status}
	if err != nil {
		data["error"] = err.Error()
	}
	_ = r.Event("run_finished", data)
	return persistErr
}

func (r *Recorder) dir() string { return filepath.Join(r.store.root, r.meta.RunID) }

func (r *Recorder) persistMeta() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.persistMetaLocked()
}

func (r *Recorder) persistMetaLocked() error {
	return writeJSONAtomic(filepath.Join(r.dir(), "meta.json"), r.meta)
}

func HashText(value string) string { return HashBytes([]byte(value)) }

func HashBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func HashJSON(value any) string {
	content, _ := json.Marshal(value)
	return HashBytes(content)
}

func NewID(prefix string) string {
	random := make([]byte, 5)
	_, _ = rand.Read(random)
	return fmt.Sprintf("%s_%s_%s", prefix, time.Now().UTC().Format("20060102T150405.000000000Z"), hex.EncodeToString(random))
}

var sensitiveKey = regexp.MustCompile(`(?i)(token|password|passwd|secret|authorization|cookie|api[_-]?key|credential)`)

func RedactMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	output := make(map[string]any, len(input))
	for key, value := range input {
		if sensitiveKey.MatchString(key) {
			output[key] = "[REDACTED]"
			continue
		}
		output[key] = redactValue(value)
	}
	return output
}

func redactValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return RedactMap(typed)
	case []any:
		result := make([]any, len(typed))
		for i, item := range typed {
			result[i] = redactValue(item)
		}
		return result
	case string:
		return Summarize(typed, 16*1024)
	default:
		return value
	}
}

func Summarize(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || len(value) <= limit {
		return value
	}
	half := limit / 2
	return value[:half] + "\n... [truncated] ...\n" + value[len(value)-(limit-half):]
}

func ClassifyError(err error) string {
	if err == nil {
		return ""
	}
	text := strings.ToLower(err.Error())
	switch {
	case errors.Is(err, os.ErrPermission), strings.Contains(text, "permission"):
		return "persistence"
	case strings.Contains(text, "deadline"), strings.Contains(text, "timeout"):
		return "timeout"
	case strings.Contains(text, "canceled"), strings.Contains(text, "cancelled"):
		return "canceled"
	case strings.Contains(text, "invalid json"), strings.Contains(text, "arguments"):
		return "tool_arguments"
	case strings.Contains(text, "unknown tool"):
		return "tool_unknown"
	case strings.Contains(text, "chat completions request"):
		return "model_transport"
	case strings.Contains(text, "response contained no choices"), strings.Contains(text, "decode"):
		return "model_protocol"
	case strings.Contains(text, "external agent"), strings.Contains(text, "acp"):
		return "external_agent"
	case strings.Contains(text, "verifier"):
		return "verifier"
	default:
		return "tool_execution"
	}
}

func writeJSONAtomic(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	content, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	content = append(content, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, content, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func appendJSONL(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	content, err := json.Marshal(value)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.Write(append(content, '\n'))
	return err
}

func normalizeText(value string) string { return strings.Join(strings.Fields(value), " ") }
func safeID(value string) string        { return filepath.Base(strings.TrimSpace(value)) }
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
