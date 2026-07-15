package subagent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"bqagent/internal/extagent"
	apptrace "bqagent/internal/trace"
)

type Status string

const (
	StatusQueued      Status = "queued"
	StatusRunning     Status = "running"
	StatusCompleted   Status = "completed"
	StatusFailed      Status = "failed"
	StatusInterrupted Status = "interrupted"
	StatusCanceled    Status = "canceled"
)

type Budget struct {
	Timeout    time.Duration `json:"-"`
	TimeoutSec int64         `json:"timeout_sec"`
	Retries    int           `json:"retries"`
	MaxLogSize int64         `json:"max_log_size"`
}

type Task struct {
	ID                string              `json:"id"`
	ParentSessionID   string              `json:"parent_session_id"`
	ParentTurnID      string              `json:"parent_turn_id,omitempty"`
	ParentRunID       string              `json:"parent_run_id,omitempty"`
	RunID             string              `json:"run_id,omitempty"`
	Agent             extagent.AgentName  `json:"agent"`
	Prompt            string              `json:"prompt"`
	FollowUps         []string            `json:"follow_ups,omitempty"`
	Status            Status              `json:"status"`
	Attempt           int                 `json:"attempt"`
	Budget            Budget              `json:"budget"`
	IncludeDirty      bool                `json:"include_dirty,omitempty"`
	BaseCommit        string              `json:"base_commit,omitempty"`
	HeadCommit        string              `json:"head_commit,omitempty"`
	WorktreePath      string              `json:"worktree_path,omitempty"`
	ExternalSessionID string              `json:"external_session_id,omitempty"`
	PID               int                 `json:"pid,omitempty"`
	Result            string              `json:"result,omitempty"`
	Error             string              `json:"error,omitempty"`
	DegradedResume    bool                `json:"degraded_resume,omitempty"`
	CreatedAt         time.Time           `json:"created_at"`
	StartedAt         *time.Time          `json:"started_at,omitempty"`
	FinishedAt        *time.Time          `json:"finished_at,omitempty"`
	HeartbeatAt       *time.Time          `json:"heartbeat_at,omitempty"`
	Artifacts         []apptrace.Artifact `json:"artifacts,omitempty"`
}

type SpawnOptions struct {
	ParentSessionID string
	ParentTurnID    string
	ParentRunID     string
	Agent           extagent.AgentName
	Prompt          string
	Timeout         time.Duration
	Retries         int
	IncludeDirty    bool
}

type Store struct{ root string }

func NewStore(workspaceRoot string) *Store {
	return &Store{root: filepath.Join(workspaceRoot, ".agent", "subagents")}
}

func (s *Store) Create(options SpawnOptions) (*Task, error) {
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	retries := options.Retries
	if retries < 0 {
		retries = 0
	}
	task := &Task{
		ID:              newID(),
		ParentSessionID: options.ParentSessionID,
		ParentTurnID:    options.ParentTurnID,
		ParentRunID:     options.ParentRunID,
		Agent:           options.Agent,
		Prompt:          strings.TrimSpace(options.Prompt),
		Status:          StatusQueued,
		Budget:          Budget{Timeout: timeout, TimeoutSec: int64(timeout.Seconds()), Retries: retries, MaxLogSize: 10 << 20},
		IncludeDirty:    options.IncludeDirty,
		CreatedAt:       time.Now().UTC(),
	}
	if task.Prompt == "" {
		return nil, fmt.Errorf("subagent task is required")
	}
	if err := s.Save(task); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(s.Dir(task.ID), "output.log"), nil, 0o644); err != nil {
		return nil, err
	}
	_ = s.Event(task.ID, "queued", map[string]any{"agent": task.Agent})
	return task, nil
}

func (s *Store) Save(task *Task) error {
	if task == nil {
		return fmt.Errorf("task is required")
	}
	if task.Budget.Timeout > 0 {
		task.Budget.TimeoutSec = int64(task.Budget.Timeout.Seconds())
	} else if task.Budget.TimeoutSec > 0 {
		task.Budget.Timeout = time.Duration(task.Budget.TimeoutSec) * time.Second
	}
	return writeJSONAtomic(filepath.Join(s.Dir(task.ID), "meta.json"), task)
}

func (s *Store) Load(id string) (*Task, error) {
	content, err := os.ReadFile(filepath.Join(s.Dir(id), "meta.json"))
	if err != nil {
		return nil, err
	}
	var task Task
	if err := json.Unmarshal(content, &task); err != nil {
		return nil, err
	}
	if task.Budget.TimeoutSec > 0 {
		task.Budget.Timeout = time.Duration(task.Budget.TimeoutSec) * time.Second
	}
	return &task, nil
}

func (s *Store) List() ([]Task, error) {
	entries, err := os.ReadDir(s.root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var tasks []Task
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == "worktrees" {
			continue
		}
		task, loadErr := s.Load(entry.Name())
		if loadErr == nil {
			tasks = append(tasks, *task)
		}
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].CreatedAt.After(tasks[j].CreatedAt) })
	return tasks, nil
}

func (s *Store) Dir(id string) string {
	return filepath.Join(s.root, filepath.Base(strings.TrimSpace(id)))
}

func (s *Store) Event(id, eventType string, data map[string]any) error {
	return appendJSONL(filepath.Join(s.Dir(id), "events.jsonl"), map[string]any{"time": time.Now().UTC(), "type": eventType, "data": data})
}

type Manager struct {
	workspaceRoot string
	store         *Store
	broker        *extagent.Broker
	traceStore    *apptrace.Store
	global        chan struct{}
	mu            sync.Mutex
	cancels       map[string]context.CancelFunc
	runningParent map[string]int
}

func NewManager(workspaceRoot string, broker *extagent.Broker, runTraceEnabled bool) *Manager {
	return newManager(workspaceRoot, broker, runTraceEnabled, true)
}

func NewWorkerManager(workspaceRoot string, broker *extagent.Broker, runTraceEnabled bool) *Manager {
	return newManager(workspaceRoot, broker, runTraceEnabled, false)
}

func newManager(workspaceRoot string, broker *extagent.Broker, runTraceEnabled, reconcile bool) *Manager {
	manager := &Manager{
		workspaceRoot: workspaceRoot,
		store:         NewStore(workspaceRoot), broker: broker,
		global:  make(chan struct{}, 6),
		cancels: map[string]context.CancelFunc{}, runningParent: map[string]int{},
	}
	if runTraceEnabled {
		manager.traceStore = apptrace.NewStore(workspaceRoot)
	}
	if reconcile {
		manager.reconcile()
	}
	return manager
}

func (m *Manager) Spawn(options SpawnOptions) (*Task, error) {
	if m == nil || m.broker == nil {
		return nil, fmt.Errorf("subagent manager is not configured")
	}
	if options.Agent == extagent.AgentDefault || m.broker.Detection(options.Agent).Preferred == nil {
		return nil, fmt.Errorf("agent %q is unavailable", options.Agent)
	}
	if err := m.validateGit(options.IncludeDirty); err != nil {
		return nil, err
	}
	m.mu.Lock()
	if m.runningParent[options.ParentSessionID] >= 3 {
		m.mu.Unlock()
		return nil, fmt.Errorf("parent session already has 3 active subagents")
	}
	m.runningParent[options.ParentSessionID]++
	m.mu.Unlock()
	task, err := m.store.Create(options)
	if err != nil {
		m.releaseParent(options.ParentSessionID)
		return nil, err
	}
	m.start(task.ID)
	return task, nil
}

func (m *Manager) start(id string) {
	executable, err := os.Executable()
	if err != nil || strings.Contains(strings.ToLower(filepath.Base(executable)), ".test") {
		go m.run(id)
		return
	}
	go m.launchWorker(executable, id)
}

func (m *Manager) launchWorker(executable, id string) {
	m.global <- struct{}{}
	defer func() { <-m.global }()
	task, err := m.store.Load(id)
	if err != nil {
		return
	}
	defer m.releaseParent(task.ParentSessionID)
	logFile, err := os.OpenFile(filepath.Join(m.store.Dir(id), "output.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		m.finishFailed(task, err)
		return
	}
	defer logFile.Close()
	cmd := exec.Command(executable, "--subagent-run", id)
	cmd.Dir = m.workspaceRoot
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	configureWorkerProcess(cmd)
	if err := cmd.Start(); err != nil {
		m.finishFailed(task, err)
		return
	}
	task.PID = cmd.Process.Pid
	now := time.Now().UTC()
	task.HeartbeatAt = &now
	_ = m.store.Save(task)
	err = cmd.Wait()
	latest, loadErr := m.store.Load(id)
	if loadErr != nil {
		return
	}
	if err != nil && !terminal(latest.Status) && latest.Status != StatusInterrupted {
		m.finishFailed(latest, fmt.Errorf("subagent worker exited: %w", err))
	}
}

func (m *Manager) RunPersisted(id string) error {
	m.run(id)
	task, err := m.store.Load(id)
	if err != nil {
		return err
	}
	if task.Status == StatusFailed {
		return fmt.Errorf("%s", task.Error)
	}
	return nil
}

func (m *Manager) run(id string) {
	m.global <- struct{}{}
	defer func() { <-m.global }()
	task, err := m.store.Load(id)
	if err != nil {
		return
	}
	defer m.releaseParent(task.ParentSessionID)
	if task.Status == StatusCanceled || task.Status == StatusInterrupted {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), task.Budget.Timeout)
	m.mu.Lock()
	m.cancels[id] = cancel
	m.mu.Unlock()
	defer func() {
		cancel()
		m.mu.Lock()
		delete(m.cancels, id)
		m.mu.Unlock()
	}()

	now := time.Now().UTC()
	task.Status = StatusRunning
	task.StartedAt = &now
	task.HeartbeatAt = &now
	task.PID = os.Getpid()
	_ = m.store.Save(task)
	_ = m.store.Event(id, "running", nil)
	heartbeatStop := make(chan struct{})
	go m.heartbeat(id, heartbeatStop)
	defer close(heartbeatStop)

	if task.WorktreePath == "" {
		if err := m.createWorktree(task); err != nil {
			m.finishFailed(task, err)
			return
		}
	}
	var recorder *apptrace.Recorder
	if m.traceStore != nil {
		var traceErr error
		recorder, traceErr = m.traceStore.Create(task.ParentSessionID, task.ParentTurnID, task.ParentRunID, "subagent", string(task.Agent), task.Prompt)
		if traceErr == nil {
			task.RunID = recorder.RunID()
			_ = m.store.Save(task)
		}
	}

	var response extagent.TurnResponse
	for attempt := 0; attempt <= task.Budget.Retries; attempt++ {
		task.Attempt = attempt + 1
		_ = m.store.Save(task)
		prompt := task.Prompt
		if len(task.FollowUps) > 0 {
			prompt = task.FollowUps[len(task.FollowUps)-1]
		}
		response, err = m.broker.SendTurn(ctx, extagent.TurnRequest{BQSessionID: task.ID, Agent: task.Agent, Prompt: prompt, CWD: task.WorktreePath})
		if err == nil || ctx.Err() != nil || !transient(err) {
			break
		}
		_ = m.store.Event(id, "retry", map[string]any{"attempt": attempt + 1, "error": err.Error()})
		time.Sleep(time.Duration(attempt+1) * time.Second)
	}
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			latest, _ := m.store.Load(id)
			if latest != nil && latest.Status == StatusCanceled {
				if recorder != nil {
					_ = recorder.Finish("", ctx.Err())
				}
				return
			}
			task.Status = StatusInterrupted
		} else {
			task.Status = StatusFailed
		}
		task.Error = err.Error()
		finished := time.Now().UTC()
		task.FinishedAt = &finished
		_ = m.store.Save(task)
		_ = m.store.Event(id, string(task.Status), map[string]any{"error": err.Error()})
		if recorder != nil {
			_ = recorder.Finish("", err)
		}
		return
	}

	task.ExternalSessionID = response.State.ExternalSessionID
	loggedResult := response.Reply
	if int64(len(loggedResult)) > task.Budget.MaxLogSize {
		loggedResult = loggedResult[:task.Budget.MaxLogSize] + "\n... [truncated]"
	}
	task.Result = loggedResult
	_ = os.WriteFile(filepath.Join(m.store.Dir(id), "result.md"), []byte(loggedResult+"\n"), 0o644)
	_ = os.WriteFile(filepath.Join(m.store.Dir(id), "output.log"), []byte(loggedResult+"\n"), 0o644)
	m.captureDiff(task)
	finished := time.Now().UTC()
	task.FinishedAt = &finished
	task.Status = StatusCompleted
	_ = m.store.Save(task)
	_ = m.store.Event(id, "completed", map[string]any{"external_session_id": task.ExternalSessionID})
	if recorder != nil {
		recorder.AddArtifact(filepath.Join(m.store.Dir(id), "diff.patch"), "git_diff")
		_ = recorder.Finish(response.Reply, nil)
	}
}

func (m *Manager) List(status Status) ([]Task, error) {
	tasks, err := m.store.List()
	if err != nil || status == "" {
		return tasks, err
	}
	filtered := tasks[:0]
	for _, task := range tasks {
		if task.Status == status {
			filtered = append(filtered, task)
		}
	}
	return filtered, nil
}

func (m *Manager) Status(id string) (*Task, error) { return m.store.Load(id) }

func (m *Manager) Wait(ctx context.Context, id string) (*Task, error) {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		task, err := m.store.Load(id)
		if err != nil {
			return nil, err
		}
		if terminal(task.Status) || task.Status == StatusInterrupted {
			return task, nil
		}
		select {
		case <-ctx.Done():
			return task, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (m *Manager) Interrupt(id string) error {
	task, err := m.store.Load(id)
	if err != nil {
		return err
	}
	if task.Status != StatusRunning && task.Status != StatusQueued {
		return fmt.Errorf("task %s is not running", id)
	}
	task.Status = StatusInterrupted
	_ = m.store.Save(task)
	m.cancel(id)
	return m.store.Event(id, "interrupted", nil)
}

func (m *Manager) Cancel(id string) error {
	task, err := m.store.Load(id)
	if err != nil {
		return err
	}
	if terminal(task.Status) {
		return fmt.Errorf("task %s is already terminal", id)
	}
	task.Status = StatusCanceled
	finished := time.Now().UTC()
	task.FinishedAt = &finished
	_ = m.store.Save(task)
	m.cancel(id)
	return m.store.Event(id, "canceled", nil)
}

func (m *Manager) Resume(id, followUp string) (*Task, error) {
	task, err := m.store.Load(id)
	if err != nil {
		return nil, err
	}
	if task.Status == StatusCanceled || task.Status == StatusCompleted {
		return nil, fmt.Errorf("task %s cannot be resumed from %s", id, task.Status)
	}
	if strings.TrimSpace(followUp) != "" {
		task.FollowUps = append(task.FollowUps, strings.TrimSpace(followUp))
	}
	if task.Attempt > 0 && task.ExternalSessionID == "" {
		task.DegradedResume = true
	}
	m.mu.Lock()
	if m.runningParent[task.ParentSessionID] >= 3 {
		m.mu.Unlock()
		return nil, fmt.Errorf("parent session already has 3 active subagents")
	}
	m.runningParent[task.ParentSessionID]++
	m.mu.Unlock()
	task.Status, task.Error, task.FinishedAt = StatusQueued, "", nil
	if err := m.store.Save(task); err != nil {
		m.releaseParent(task.ParentSessionID)
		return nil, err
	}
	m.start(id)
	return task, nil
}

func (m *Manager) Apply(id string) error {
	task, err := m.store.Load(id)
	if err != nil {
		return err
	}
	if task.Status != StatusCompleted {
		return fmt.Errorf("task %s is not completed", id)
	}
	if dirty, _ := gitOutput(m.workspaceRoot, "status", "--porcelain"); strings.TrimSpace(dirty) != "" {
		return fmt.Errorf("main workspace must be clean before apply")
	}
	patchPath := filepath.Join(m.store.Dir(id), "diff.patch")
	cmd := exec.Command("git", "apply", "--3way", patchPath)
	cmd.Dir = m.workspaceRoot
	if output, applyErr := cmd.CombinedOutput(); applyErr != nil {
		return fmt.Errorf("git apply failed: %w: %s", applyErr, strings.TrimSpace(string(output)))
	}
	return m.store.Event(id, "applied", nil)
}

func (m *Manager) Cleanup(id string) error {
	task, err := m.store.Load(id)
	if err != nil {
		return err
	}
	if !terminal(task.Status) && task.Status != StatusInterrupted {
		return fmt.Errorf("task %s is still active", id)
	}
	if task.WorktreePath != "" {
		cmd := exec.Command("git", "worktree", "remove", "--force", task.WorktreePath)
		cmd.Dir = m.workspaceRoot
		if output, removeErr := cmd.CombinedOutput(); removeErr != nil {
			return fmt.Errorf("git worktree remove failed: %w: %s", removeErr, strings.TrimSpace(string(output)))
		}
	}
	task.WorktreePath = ""
	return m.store.Save(task)
}

func (m *Manager) createWorktree(task *Task) error {
	base, err := gitOutput(m.workspaceRoot, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	task.BaseCommit = strings.TrimSpace(base)
	task.WorktreePath = filepath.Join(m.store.Dir(task.ID), "worktree")
	if err := os.MkdirAll(m.store.Dir(task.ID), 0o755); err != nil {
		return err
	}
	cmd := exec.Command("git", "worktree", "add", "--detach", task.WorktreePath, task.BaseCommit)
	cmd.Dir = m.workspaceRoot
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git worktree add failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	if task.IncludeDirty {
		patch, _ := gitOutput(m.workspaceRoot, "diff", "--binary", "HEAD")
		if strings.TrimSpace(patch) != "" {
			patchPath := filepath.Join(m.store.Dir(task.ID), "initial.patch")
			_ = os.WriteFile(patchPath, []byte(patch), 0o600)
			apply := exec.Command("git", "apply", patchPath)
			apply.Dir = task.WorktreePath
			if output, err := apply.CombinedOutput(); err != nil {
				return fmt.Errorf("copy dirty diff failed: %w: %s", err, strings.TrimSpace(string(output)))
			}
		}
		if err := m.copyUntracked(task.WorktreePath); err != nil {
			return err
		}
	}
	return m.store.Save(task)
}

func (m *Manager) copyUntracked(worktree string) error {
	output, err := gitOutput(m.workspaceRoot, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return err
	}
	for _, relative := range strings.Split(output, "\x00") {
		relative = filepath.Clean(strings.TrimSpace(relative))
		if relative == "." || relative == "" {
			continue
		}
		lower := strings.ToLower(filepath.ToSlash(relative))
		baseName := strings.ToLower(filepath.Base(relative))
		if strings.HasPrefix(baseName, ".env") || strings.HasPrefix(lower, ".agent/") || strings.Contains(lower, "credential") || strings.Contains(lower, "secret") || strings.Contains(lower, "token") {
			continue
		}
		source := filepath.Join(m.workspaceRoot, relative)
		info, statErr := os.Stat(source)
		if statErr != nil || info.IsDir() || info.Size() > 10<<20 {
			continue
		}
		target := filepath.Join(worktree, relative)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		content, readErr := os.ReadFile(source)
		if readErr != nil {
			return readErr
		}
		if err := os.WriteFile(target, content, info.Mode().Perm()); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) captureDiff(task *Task) {
	head, _ := gitOutput(task.WorktreePath, "rev-parse", "HEAD")
	task.HeadCommit = strings.TrimSpace(head)
	patch, _ := gitOutput(task.WorktreePath, "diff", "--binary", task.BaseCommit)
	patchPath := filepath.Join(m.store.Dir(task.ID), "diff.patch")
	_ = os.WriteFile(patchPath, []byte(patch), 0o644)
	if info, err := os.Stat(patchPath); err == nil {
		task.Artifacts = append(task.Artifacts, apptrace.Artifact{Path: patchPath, Kind: "git_diff", Size: info.Size(), SHA256: apptrace.HashText(patch)})
	}
	_ = writeJSONAtomic(filepath.Join(m.store.Dir(task.ID), "artifacts.json"), task.Artifacts)
}

func (m *Manager) validateGit(includeDirty bool) error {
	if _, err := gitOutput(m.workspaceRoot, "rev-parse", "--is-inside-work-tree"); err != nil {
		return fmt.Errorf("subagents require a git workspace: %w", err)
	}
	status, err := gitOutput(m.workspaceRoot, "status", "--porcelain")
	if err != nil {
		return err
	}
	if strings.TrimSpace(status) != "" && !includeDirty {
		return fmt.Errorf("workspace has uncommitted changes; commit them or pass --include-dirty")
	}
	return nil
}

func (m *Manager) finishFailed(task *Task, err error) {
	finished := time.Now().UTC()
	task.Status = StatusFailed
	task.Error = err.Error()
	task.FinishedAt = &finished
	_ = m.store.Save(task)
	_ = m.store.Event(task.ID, "failed", map[string]any{"error": err.Error()})
}

func (m *Manager) cancel(id string) {
	m.mu.Lock()
	cancel := m.cancels[id]
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if task, err := m.store.Load(id); err == nil && task.PID > 0 && task.PID != os.Getpid() {
		_ = terminateWorkerPID(task.PID)
	}
	if m.broker != nil {
		_ = m.broker.Clear(id)
	}
}
func (m *Manager) releaseParent(id string) {
	m.mu.Lock()
	if m.runningParent[id] > 0 {
		m.runningParent[id]--
	}
	m.mu.Unlock()
}

func (m *Manager) reconcile() {
	tasks, _ := m.store.List()
	for i := range tasks {
		if tasks[i].Status == StatusRunning || tasks[i].Status == StatusQueued {
			if tasks[i].HeartbeatAt != nil && time.Since(*tasks[i].HeartbeatAt) < 15*time.Second && tasks[i].PID > 0 {
				m.runningParent[tasks[i].ParentSessionID]++
				go m.monitorExisting(tasks[i].ID, tasks[i].ParentSessionID)
				continue
			}
			tasks[i].Status = StatusInterrupted
			tasks[i].Error = "manager restarted while task was active"
			_ = m.store.Save(&tasks[i])
		}
	}
}

func (m *Manager) monitorExisting(id, parent string) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for range ticker.C {
		task, err := m.store.Load(id)
		if err != nil || terminal(task.Status) || task.Status == StatusInterrupted {
			m.releaseParent(parent)
			return
		}
	}
}

func (m *Manager) heartbeat(id string, stop <-chan struct{}) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case now := <-ticker.C:
			task, err := m.store.Load(id)
			if err != nil || task.Status != StatusRunning {
				return
			}
			utc := now.UTC()
			task.HeartbeatAt = &utc
			task.PID = os.Getpid()
			_ = m.store.Save(task)
		}
	}
}

func terminal(status Status) bool {
	return status == StatusCompleted || status == StatusFailed || status == StatusCanceled
}
func transient(err error) bool {
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "rate") || strings.Contains(text, "tempor") || strings.Contains(text, "connection")
}

func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s failed: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

func newID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return "agent_" + time.Now().UTC().Format("20060102T150405Z") + "_" + hex.EncodeToString(b)
}

func writeJSONAtomic(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	content, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(content, '\n'), 0o644); err != nil {
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
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(content, '\n'))
	return err
}

func ParseDuration(value string, fallback time.Duration) (time.Duration, error) {
	if strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	return time.ParseDuration(value)
}
func ParseInt(value string, fallback int) (int, error) {
	if strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	return strconv.Atoi(value)
}
