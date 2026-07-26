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
	"bqagent/internal/safepath"
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

	maxGlobalSubagents  = 6
	leaseLockStaleAfter = 30 * time.Second
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
	ExecutionID       string              `json:"execution_id,omitempty"`
	LeaseID           string              `json:"lease_id,omitempty"`
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

type Store struct{ workspaceRoot string }

func NewStore(workspaceRoot string) *Store { return &Store{workspaceRoot: workspaceRoot} }

func canonicalTaskID(id string) (string, error) {
	id = strings.TrimSpace(id)
	if err := safepath.ValidateComponent(id); err != nil {
		return "", fmt.Errorf("invalid subagent task id: %w", err)
	}
	return id, nil
}

func (s *Store) taskDir(id string) (string, error) {
	id, err := canonicalTaskID(id)
	if err != nil {
		return "", err
	}
	return safepath.Resolve(s.workspaceRoot, filepath.Join(".agent", "subagents", id))
}

// openTaskRoot pins the task directory beneath workspaceRoot. In particular, it
// does not follow a task-directory symlink to a location outside the workspace.
func (s *Store) openTaskRoot(id string, create bool) (*os.Root, string, error) {
	canonicalID, err := canonicalTaskID(id)
	if err != nil {
		return nil, "", err
	}
	workspace, err := os.OpenRoot(s.workspaceRoot)
	if err != nil {
		return nil, "", err
	}
	defer workspace.Close()
	relative := filepath.Join(".agent", "subagents", canonicalID)
	if create {
		if err := workspace.MkdirAll(relative, 0o755); err != nil {
			return nil, "", err
		}
	}
	taskRoot, err := workspace.OpenRoot(relative)
	if err != nil {
		return nil, "", fmt.Errorf("open subagent task directory: %w", err)
	}
	return taskRoot, canonicalID, nil
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
		ExecutionID:     newExecutionID(),
		LeaseID:         newLeaseID(),
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
	taskRoot, _, err := s.openTaskRoot(task.ID, false)
	if err != nil {
		return nil, err
	}
	defer taskRoot.Close()
	if err := taskRoot.WriteFile("output.log", nil, 0o644); err != nil {
		return nil, err
	}
	_ = s.Event(task.ID, "queued", map[string]any{"agent": task.Agent})
	return task, nil
}

// Save creates a new task snapshot. Existing tasks must use Update so a stale
// in-memory task can never overwrite a newer on-disk snapshot.
func (s *Store) Save(task *Task) error {
	if task == nil {
		return fmt.Errorf("task is required")
	}
	normalizeTask(task)
	taskRoot, canonicalID, err := s.openTaskRoot(task.ID, true)
	if err != nil {
		return err
	}
	defer taskRoot.Close()
	unlock, err := lockTaskRoot(taskRoot)
	if err != nil {
		return err
	}
	defer unlock()
	if _, err := taskRoot.Stat("meta.json"); err == nil {
		return fmt.Errorf("subagent task %s already exists; use Update", canonicalID)
	} else if !os.IsNotExist(err) {
		return err
	}
	return writeJSONRoot(taskRoot, "meta.json", task)
}

func normalizeTask(task *Task) {
	if task.Budget.Timeout > 0 {
		task.Budget.TimeoutSec = int64(task.Budget.Timeout.Seconds())
	} else if task.Budget.TimeoutSec > 0 {
		task.Budget.Timeout = time.Duration(task.Budget.TimeoutSec) * time.Second
	}
}

func (s *Store) Load(id string) (*Task, error) {
	taskRoot, canonicalID, err := s.openTaskRoot(id, false)
	if err != nil {
		return nil, err
	}
	defer taskRoot.Close()
	return loadTaskRoot(taskRoot, canonicalID)
}

func loadTaskRoot(taskRoot *os.Root, canonicalID string) (*Task, error) {
	content, err := taskRoot.ReadFile("meta.json")
	if err != nil {
		return nil, err
	}
	var task Task
	if err := json.Unmarshal(content, &task); err != nil {
		return nil, err
	}
	taskID, err := canonicalTaskID(task.ID)
	if err != nil {
		return nil, fmt.Errorf("invalid subagent metadata id: %w", err)
	}
	if taskID != canonicalID {
		return nil, fmt.Errorf("subagent metadata id %q does not match requested id %q", task.ID, canonicalID)
	}
	task.ID = canonicalID
	normalizeTask(&task)
	return &task, nil
}

// Update serializes a read-modify-write operation across processes. expectedLease
// must match when non-empty; allowed must contain the current status. The callback
// runs while the per-task lock is held and may perform small associated writes.
func (s *Store) Update(id, expectedLease string, allowed []Status, mutate func(*Task) error) (*Task, error) {
	taskRoot, canonicalID, err := s.openTaskRoot(id, false)
	if err != nil {
		return nil, err
	}
	defer taskRoot.Close()
	unlock, err := lockTaskRoot(taskRoot)
	if err != nil {
		return nil, err
	}
	defer unlock()
	task, err := loadTaskRoot(taskRoot, canonicalID)
	if err != nil {
		return nil, err
	}
	if expectedLease != "" && task.LeaseID != expectedLease {
		return nil, fmt.Errorf("subagent task %s lease no longer matches", canonicalID)
	}
	if len(allowed) > 0 && !hasStatus(allowed, task.Status) {
		return nil, fmt.Errorf("subagent task %s cannot transition from %s", canonicalID, task.Status)
	}
	previousStatus := task.Status
	if mutate != nil {
		if err := mutate(task); err != nil {
			return nil, err
		}
	}
	if !validStatusTransition(previousStatus, task.Status) {
		return nil, fmt.Errorf("subagent task %s cannot transition from %s to %s", canonicalID, previousStatus, task.Status)
	}
	normalizeTask(task)
	if err := writeJSONRoot(taskRoot, "meta.json", task); err != nil {
		return nil, err
	}
	return task, nil
}

type taskLockOwner struct {
	Token string `json:"token"`
	PID   int    `json:"pid"`
}

func lockTaskDir(dir string) (func(), error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	lockDir := filepath.Join(dir, ".meta.lock")
	owner := taskLockOwner{Token: newToken(), PID: os.Getpid()}
	deadline := time.Now().Add(leaseLockStaleAfter + 2*time.Second)
	for {
		err := os.Mkdir(lockDir, 0o700)
		if err == nil {
			content, marshalErr := json.Marshal(owner)
			if marshalErr != nil {
				_ = os.Remove(lockDir)
				return nil, marshalErr
			}
			if writeErr := os.WriteFile(filepath.Join(lockDir, "owner.json"), content, 0o600); writeErr != nil {
				_ = os.RemoveAll(lockDir)
				return nil, writeErr
			}
			return func() { unlockTaskDir(lockDir, owner.Token) }, nil
		}
		if !os.IsExist(err) {
			return nil, err
		}
		if info, statErr := os.Stat(lockDir); statErr == nil && time.Since(info.ModTime()) > leaseLockStaleAfter {
			if staleTaskLock(lockDir) {
				_ = os.RemoveAll(lockDir)
				continue
			}
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out waiting for subagent task lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func staleTaskLock(lockDir string) bool {
	content, err := os.ReadFile(filepath.Join(lockDir, "owner.json"))
	if err != nil {
		return false
	}
	var owner taskLockOwner
	if err := json.Unmarshal(content, &owner); err != nil || owner.Token == "" || owner.PID <= 0 {
		return false
	}
	return !workerProcessAlive(owner.PID)
}

func unlockTaskDir(lockDir, token string) {
	content, err := os.ReadFile(filepath.Join(lockDir, "owner.json"))
	if err != nil {
		return
	}
	var owner taskLockOwner
	if json.Unmarshal(content, &owner) != nil || owner.Token != token {
		return
	}
	_ = os.RemoveAll(lockDir)
}

// lockTaskRoot retains the existing lease-lock protocol while keeping every
// lock operation rooted in the already-validated task directory.
func lockTaskRoot(taskRoot *os.Root) (func(), error) {
	const lockDir = ".meta.lock"
	owner := taskLockOwner{Token: newToken(), PID: os.Getpid()}
	deadline := time.Now().Add(leaseLockStaleAfter + 2*time.Second)
	for {
		err := taskRoot.Mkdir(lockDir, 0o700)
		if err == nil {
			content, marshalErr := json.Marshal(owner)
			if marshalErr != nil {
				_ = taskRoot.Remove(lockDir)
				return nil, marshalErr
			}
			if writeErr := taskRoot.WriteFile(filepath.Join(lockDir, "owner.json"), content, 0o600); writeErr != nil {
				_ = taskRoot.RemoveAll(lockDir)
				return nil, writeErr
			}
			return func() { unlockTaskRoot(taskRoot, owner.Token) }, nil
		}
		if !os.IsExist(err) {
			return nil, err
		}
		if info, statErr := taskRoot.Stat(lockDir); statErr == nil && time.Since(info.ModTime()) > leaseLockStaleAfter {
			if staleTaskRootLock(taskRoot) {
				_ = taskRoot.RemoveAll(lockDir)
				continue
			}
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out waiting for subagent task lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func staleTaskRootLock(taskRoot *os.Root) bool {
	content, err := taskRoot.ReadFile(filepath.Join(".meta.lock", "owner.json"))
	if err != nil {
		return false
	}
	var owner taskLockOwner
	if err := json.Unmarshal(content, &owner); err != nil || owner.Token == "" || owner.PID <= 0 {
		return false
	}
	return !workerProcessAlive(owner.PID)
}

func unlockTaskRoot(taskRoot *os.Root, token string) {
	content, err := taskRoot.ReadFile(filepath.Join(".meta.lock", "owner.json"))
	if err != nil {
		return
	}
	var owner taskLockOwner
	if json.Unmarshal(content, &owner) != nil || owner.Token != token {
		return
	}
	_ = taskRoot.RemoveAll(".meta.lock")
}

func hasStatus(statuses []Status, current Status) bool {
	for _, status := range statuses {
		if status == current {
			return true
		}
	}
	return false
}

func validStatusTransition(from, to Status) bool {
	if from == to {
		return true
	}
	switch from {
	case StatusQueued:
		return to == StatusRunning || to == StatusInterrupted || to == StatusCanceled
	case StatusRunning:
		return to == StatusCompleted || to == StatusFailed || to == StatusInterrupted || to == StatusCanceled
	case StatusFailed, StatusInterrupted:
		return to == StatusQueued
	default: // completed and canceled are terminal.
		return false
	}
}

func (s *Store) List() ([]Task, error) {
	root, err := safepath.Resolve(s.workspaceRoot, filepath.Join(".agent", "subagents"))
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var tasks []Task
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == "worktrees" || entry.Name() == ".meta.lock" {
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

func (s *Store) Dir(id string) (string, error) { return s.taskDir(id) }

func (s *Store) Event(id, eventType string, data map[string]any) error {
	taskRoot, _, err := s.openTaskRoot(id, false)
	if err != nil {
		return err
	}
	defer taskRoot.Close()
	return appendJSONLRoot(taskRoot, "events.jsonl", map[string]any{"time": time.Now().UTC(), "type": eventType, "data": data})
}

type activeCancel struct {
	lease  string
	cancel context.CancelFunc
}

type Manager struct {
	workspaceRoot string
	store         *Store
	broker        *extagent.Broker
	traceStore    *apptrace.Store
	worker        bool

	mu            sync.Mutex
	cond          *sync.Cond
	activeGlobal  int
	globalLimit   int
	cancels       map[string]activeCancel
	runningParent map[string]int
	monitored     map[string]string
}

func NewManager(workspaceRoot string, broker *extagent.Broker, runTraceEnabled bool) *Manager {
	return newManager(workspaceRoot, broker, runTraceEnabled, true, false)
}
func NewWorkerManager(workspaceRoot string, broker *extagent.Broker, runTraceEnabled bool) *Manager {
	return newManager(workspaceRoot, broker, runTraceEnabled, false, true)
}

func newManager(workspaceRoot string, broker *extagent.Broker, runTraceEnabled, reconcile, worker bool) *Manager {
	manager := &Manager{
		workspaceRoot: workspaceRoot, store: NewStore(workspaceRoot), broker: broker, worker: worker,
		globalLimit: maxGlobalSubagents, cancels: map[string]activeCancel{}, runningParent: map[string]int{}, monitored: map[string]string{},
	}
	manager.cond = sync.NewCond(&manager.mu)
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
	if !m.reserveParent(options.ParentSessionID) {
		return nil, fmt.Errorf("parent session already has 3 active subagents")
	}
	task, err := m.store.Create(options)
	if err != nil {
		m.releaseParent(options.ParentSessionID)
		return nil, err
	}
	m.start(task.ID)
	return task, nil
}

func (m *Manager) start(id string) {
	task, err := m.store.Load(id)
	if err != nil {
		return
	}
	executable, err := os.Executable()
	if err != nil || strings.Contains(strings.ToLower(filepath.Base(executable)), ".test") {
		go func() { _ = m.run(context.Background(), id, task.LeaseID, true) }()
		return
	}
	go m.launchWorker(executable, id)
}

func (m *Manager) launchWorker(executable, id string) {
	m.acquireGlobal()
	defer m.releaseGlobal()
	task, err := m.store.Load(id)
	if err != nil {
		return
	}
	defer m.releaseParent(task.ParentSessionID)
	taskRoot, _, err := m.store.openTaskRoot(id, false)
	if err != nil {
		m.interruptLaunch(id, task.LeaseID, err)
		return
	}
	logFile, err := taskRoot.OpenFile("output.log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	_ = taskRoot.Close()
	if err != nil {
		m.interruptLaunch(id, task.LeaseID, err)
		return
	}
	defer logFile.Close()
	cmd := exec.Command(executable, "--subagent-run", id, "--subagent-lease", task.LeaseID)
	cmd.Dir = m.workspaceRoot
	cmd.Stdout, cmd.Stderr = logFile, logFile
	configureWorkerProcess(cmd)
	if err := cmd.Start(); err != nil {
		m.interruptLaunch(id, task.LeaseID, err)
		return
	}
	now := time.Now().UTC()
	_, _ = m.store.Update(id, task.LeaseID, []Status{StatusQueued, StatusRunning}, func(current *Task) error {
		current.PID, current.HeartbeatAt = cmd.Process.Pid, &now
		return nil
	})
	err = cmd.Wait()
	latest, loadErr := m.store.Load(id)
	if loadErr != nil || err == nil || terminal(latest.Status) || latest.Status == StatusInterrupted || latest.Status == StatusCanceled {
		return
	}
	m.interruptLaunch(id, task.LeaseID, fmt.Errorf("subagent worker exited: %w", err))
}

// RunPersisted accepts the parent lifecycle context and the lease issued by the
// parent launcher. A queued task never runs unless its non-empty lease matches
// the current persisted lease.
func (m *Manager) RunPersisted(ctx context.Context, id string, leases ...string) error {
	lease := ""
	if len(leases) > 0 {
		lease = leases[0]
	}
	return m.run(ctx, id, lease, !m.worker)
}

func (m *Manager) run(parent context.Context, id, expectedLease string, acquire bool) error {
	if acquire {
		m.acquireGlobal()
		defer m.releaseGlobal()
	}
	task, err := m.claimQueued(id, expectedLease)
	if err != nil {
		return err
	}
	defer m.releaseParent(task.ParentSessionID)
	lease := task.LeaseID
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, task.Budget.Timeout)
	m.mu.Lock()
	m.cancels[id] = activeCancel{lease: lease, cancel: cancel}
	m.mu.Unlock()
	defer func() {
		cancel()
		m.mu.Lock()
		if active, ok := m.cancels[id]; ok && active.lease == lease {
			delete(m.cancels, id)
		}
		m.mu.Unlock()
	}()
	heartbeatStop := make(chan struct{})
	go m.heartbeat(id, lease, heartbeatStop)
	defer close(heartbeatStop)

	if task.WorktreePath == "" {
		if err := m.createWorktree(task, lease); err != nil {
			m.finishFailed(id, lease, err, StatusRunning)
			return err
		}
		latest, loadErr := m.store.Load(id)
		if loadErr != nil || latest.LeaseID != lease || latest.Status != StatusRunning {
			return nil
		}
		task = latest
	}
	var recorder *apptrace.Recorder
	if m.traceStore != nil {
		if recorder, err = m.traceStore.Create(task.ParentSessionID, task.ParentTurnID, task.ParentRunID, "subagent", string(task.Agent), task.Prompt); err == nil {
			_, _ = m.store.Update(id, lease, []Status{StatusRunning}, func(current *Task) error { current.RunID = recorder.RunID(); return nil })
		}
	}

	var response extagent.TurnResponse
	for attempt := 0; attempt <= task.Budget.Retries; attempt++ {
		_, updateErr := m.store.Update(id, lease, []Status{StatusRunning}, func(current *Task) error { current.Attempt = attempt + 1; return nil })
		if updateErr != nil {
			return nil
		}
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
			if current, loadErr := m.store.Load(id); loadErr == nil && current.Status == StatusCanceled {
				if recorder != nil {
					_ = recorder.Finish("", ctx.Err())
				}
				return nil
			}
			err = m.finishInterrupted(id, lease, err)
		} else {
			err = m.finishFailed(id, lease, err, StatusRunning)
		}
		if recorder != nil {
			_ = recorder.Finish("", err)
		}
		return err
	}
	if err := m.finishCompleted(id, lease, task, response); err != nil {
		return err
	}
	if recorder != nil {
		taskDir, _ := m.store.Dir(id)
		recorder.AddArtifact(filepath.Join(taskDir, "diff.patch"), "git_diff")
		_ = recorder.Finish(response.Reply, nil)
	}
	return nil
}

func (m *Manager) claimQueued(id, expectedLease string) (*Task, error) {
	if expectedLease == "" {
		return nil, fmt.Errorf("subagent task %s has no execution lease", id)
	}
	now := time.Now().UTC()
	task, err := m.store.Update(id, expectedLease, []Status{StatusQueued}, func(current *Task) error {
		current.Status, current.StartedAt, current.HeartbeatAt, current.PID = StatusRunning, &now, &now, os.Getpid()
		return nil
	})
	if err == nil {
		_ = m.store.Event(id, "running", nil)
	}
	return task, err
}

type completionFiles struct {
	result     []byte
	output     []byte
	patch      []byte
	artifacts  []byte
	headCommit string
	metadata   []apptrace.Artifact
}

func (m *Manager) finishCompleted(id, lease string, task *Task, response extagent.TurnResponse) error {
	result := response.Reply
	if int64(len(result)) > task.Budget.MaxLogSize {
		result = result[:task.Budget.MaxLogSize] + "\n... [truncated]"
	}
	// Generating a git diff can take seconds on a large worktree. Do all such
	// work before obtaining the metadata lock; the final Update fences the
	// prepared data with the current lease and status so Cancel still wins.
	prepared, err := m.prepareCompletionFiles(task, result)
	if err != nil {
		return err
	}

	finished := time.Now().UTC()
	_, err = m.store.Update(id, lease, []Status{StatusRunning}, func(current *Task) error {
		if err := m.store.writeCompletionFiles(id, prepared); err != nil {
			return err
		}
		current.ExternalSessionID, current.Result = response.State.ExternalSessionID, result
		current.HeadCommit, current.Artifacts = prepared.headCommit, prepared.metadata
		current.FinishedAt, current.Status = &finished, StatusCompleted
		return nil
	})
	if err == nil {
		_ = m.store.Event(id, "completed", map[string]any{"external_session_id": response.State.ExternalSessionID})
	}
	return err
}

func (m *Manager) prepareCompletionFiles(task *Task, result string) (*completionFiles, error) {
	dir, err := m.store.Dir(task.ID)
	if err != nil {
		return nil, err
	}
	head, _ := gitOutput(task.WorktreePath, "rev-parse", "HEAD")
	patch, _ := gitOutput(task.WorktreePath, "diff", "--binary", task.BaseCommit)
	patchPath := filepath.Join(dir, "diff.patch")
	metadata := []apptrace.Artifact{{Path: patchPath, Kind: "git_diff", Size: int64(len(patch)), SHA256: apptrace.HashText(patch)}}
	artifacts, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return nil, err
	}
	return &completionFiles{
		result: []byte(result + "\n"), output: []byte(result + "\n"), patch: []byte(patch),
		artifacts: append(artifacts, '\n'), headCommit: strings.TrimSpace(head), metadata: metadata,
	}, nil
}

func (s *Store) writeCompletionFiles(id string, files *completionFiles) error {
	taskRoot, _, err := s.openTaskRoot(id, false)
	if err != nil {
		return err
	}
	defer taskRoot.Close()
	for _, file := range []struct {
		name    string
		content []byte
	}{
		{"result.md", files.result},
		{"output.log", files.output},
		{"diff.patch", files.patch},
		{"artifacts.json", files.artifacts},
	} {
		if err := writeRootAtomic(taskRoot, file.name, file.content, 0o644); err != nil {
			return err
		}
	}
	return nil
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
	lease := task.LeaseID
	_, err = m.store.Update(id, lease, []Status{StatusQueued, StatusRunning}, func(current *Task) error {
		current.Status, current.Error, current.LeaseID = StatusInterrupted, "interrupted", ""
		return nil
	})
	if err != nil {
		return err
	}
	m.cancel(id, lease)
	return m.store.Event(id, "interrupted", nil)
}

func (m *Manager) Cancel(id string) error {
	task, err := m.store.Load(id)
	if err != nil {
		return err
	}
	lease := task.LeaseID
	finished := time.Now().UTC()
	_, err = m.store.Update(id, lease, []Status{StatusQueued, StatusRunning}, func(current *Task) error {
		current.Status, current.FinishedAt, current.LeaseID = StatusCanceled, &finished, ""
		return nil
	})
	if err != nil {
		return err
	}
	m.cancel(id, lease)
	return m.store.Event(id, "canceled", nil)
}

func (m *Manager) Resume(id, followUp string) (*Task, error) {
	current, err := m.store.Load(id)
	if err != nil {
		return nil, err
	}
	if !m.reserveParent(current.ParentSessionID) {
		return nil, fmt.Errorf("parent session already has 3 active subagents")
	}
	newLease, execution := newLeaseID(), newExecutionID()
	task, err := m.store.Update(id, current.LeaseID, []Status{StatusFailed, StatusInterrupted}, func(task *Task) error {
		if strings.TrimSpace(followUp) != "" {
			task.FollowUps = append(task.FollowUps, strings.TrimSpace(followUp))
		}
		if task.Attempt > 0 && task.ExternalSessionID == "" {
			task.DegradedResume = true
		}
		task.Status, task.Error, task.FinishedAt, task.PID = StatusQueued, "", nil, 0
		task.LeaseID, task.ExecutionID = newLease, execution
		return nil
	})
	if err != nil {
		m.releaseParent(current.ParentSessionID)
		return nil, err
	}
	_ = m.store.Event(id, "queued", map[string]any{"resumed": true})
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
	dir, err := m.store.Dir(id)
	if err != nil {
		return err
	}
	cmd := exec.Command("git", "apply", "--3way", filepath.Join(dir, "diff.patch"))
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
	_, err = m.store.Update(id, task.LeaseID, []Status{task.Status}, func(current *Task) error { current.WorktreePath = ""; return nil })
	return err
}

func (m *Manager) createWorktree(task *Task, lease string) error {
	base, err := gitOutput(m.workspaceRoot, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	dir, err := m.store.Dir(task.ID)
	if err != nil {
		return err
	}
	worktree := filepath.Join(dir, "worktree")
	cmd := exec.Command("git", "worktree", "add", "--detach", worktree, strings.TrimSpace(base))
	cmd.Dir = m.workspaceRoot
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git worktree add failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	if task.IncludeDirty {
		if err := m.copyDirtyToWorktree(worktree, dir); err != nil {
			return err
		}
	}
	_, err = m.store.Update(task.ID, lease, []Status{StatusRunning}, func(current *Task) error {
		current.BaseCommit, current.WorktreePath = strings.TrimSpace(base), worktree
		return nil
	})
	return err
}

func (m *Manager) copyDirtyToWorktree(worktree, dir string) error {
	patch, _ := gitOutput(m.workspaceRoot, "diff", "--binary", "HEAD")
	if strings.TrimSpace(patch) != "" {
		patchPath := filepath.Join(dir, "initial.patch")
		if err := os.WriteFile(patchPath, []byte(patch), 0o600); err != nil {
			return err
		}
		apply := exec.Command("git", "apply", patchPath)
		apply.Dir = worktree
		if output, err := apply.CombinedOutput(); err != nil {
			return fmt.Errorf("copy dirty diff failed: %w: %s", err, strings.TrimSpace(string(output)))
		}
	}
	return m.copyUntracked(worktree)
}

func (m *Manager) copyUntracked(worktree string) error {
	sourceRoot, err := os.OpenRoot(m.workspaceRoot)
	if err != nil {
		return err
	}
	defer sourceRoot.Close()
	targetRoot, err := os.OpenRoot(worktree)
	if err != nil {
		return err
	}
	defer targetRoot.Close()

	output, err := gitOutput(m.workspaceRoot, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return err
	}
	for _, relative := range strings.Split(output, "\x00") {
		relative = filepath.Clean(strings.TrimSpace(relative))
		if relative == "." || relative == "" {
			continue
		}
		lower, baseName := strings.ToLower(filepath.ToSlash(relative)), strings.ToLower(filepath.Base(relative))
		if strings.HasPrefix(baseName, ".env") || strings.HasPrefix(lower, ".agent/") || strings.Contains(lower, "credential") || strings.Contains(lower, "secret") || strings.Contains(lower, "token") {
			continue
		}
		info, statErr := sourceRoot.Lstat(relative)
		if statErr != nil || info.IsDir() || info.Size() > 10<<20 {
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing untracked symbolic link %q", relative)
		}
		if err := targetRoot.MkdirAll(filepath.Dir(relative), 0o755); err != nil {
			return fmt.Errorf("create untracked target %q: %w", relative, err)
		}
		content, readErr := sourceRoot.ReadFile(relative)
		if readErr != nil {
			return fmt.Errorf("read untracked source %q: %w", relative, readErr)
		}
		if err := targetRoot.WriteFile(relative, content, info.Mode().Perm()); err != nil {
			return fmt.Errorf("write untracked target %q: %w", relative, err)
		}
	}
	return nil
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

func (m *Manager) interruptLaunch(id, lease string, cause error) error {
	finished := time.Now().UTC()
	_, err := m.store.Update(id, lease, []Status{StatusQueued, StatusRunning}, func(task *Task) error {
		task.Status, task.Error, task.FinishedAt, task.LeaseID = StatusInterrupted, cause.Error(), &finished, ""
		return nil
	})
	if err == nil {
		_ = m.store.Event(id, "interrupted", map[string]any{"error": cause.Error()})
	}
	return err
}

func (m *Manager) finishFailed(id, lease string, cause error, allowed ...Status) error {
	finished := time.Now().UTC()
	_, err := m.store.Update(id, lease, allowed, func(task *Task) error {
		task.Status, task.Error, task.FinishedAt = StatusFailed, cause.Error(), &finished
		return nil
	})
	if err == nil {
		_ = m.store.Event(id, "failed", map[string]any{"error": cause.Error()})
	}
	return err
}
func (m *Manager) finishInterrupted(id, lease string, cause error) error {
	finished := time.Now().UTC()
	_, err := m.store.Update(id, lease, []Status{StatusRunning}, func(task *Task) error {
		task.Status, task.Error, task.FinishedAt, task.LeaseID = StatusInterrupted, cause.Error(), &finished, ""
		return nil
	})
	if err == nil {
		_ = m.store.Event(id, "interrupted", map[string]any{"error": cause.Error()})
	}
	return err
}

func (m *Manager) cancel(id, lease string) {
	m.mu.Lock()
	active, ok := m.cancels[id]
	m.mu.Unlock()
	if ok && active.lease == lease {
		active.cancel()
	}
	if task, err := m.store.Load(id); err == nil && task.PID > 0 && task.PID != os.Getpid() {
		_ = terminateWorkerPID(task.PID)
	}
	if m.broker != nil {
		_ = m.broker.Clear(id)
	}
}
func (m *Manager) reserveParent(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.runningParent[id] >= 3 {
		return false
	}
	m.runningParent[id]++
	return true
}
func (m *Manager) releaseParent(id string) {
	m.mu.Lock()
	if m.runningParent[id] > 0 {
		m.runningParent[id]--
	}
	m.mu.Unlock()
}
func (m *Manager) acquireGlobal() {
	m.mu.Lock()
	for m.activeGlobal >= m.globalLimit {
		m.cond.Wait()
	}
	m.activeGlobal++
	m.mu.Unlock()
}
func (m *Manager) releaseGlobal() {
	m.mu.Lock()
	if m.activeGlobal > 0 {
		m.activeGlobal--
		m.cond.Broadcast()
	}
	m.mu.Unlock()
}

func (m *Manager) reconcile() {
	tasks, _ := m.store.List()
	for i := range tasks {
		task := tasks[i]
		if task.Status != StatusQueued && task.Status != StatusRunning {
			continue
		}
		healthy := task.Status == StatusRunning && task.PID > 0 && task.HeartbeatAt != nil && time.Since(*task.HeartbeatAt) < 15*time.Second && workerProcessAlive(task.PID)
		if healthy {
			if task.LeaseID == "" {
				adopted, err := m.store.Update(task.ID, "", []Status{StatusRunning}, func(current *Task) error {
					if current.LeaseID != "" {
						return fmt.Errorf("subagent task %s was already adopted", current.ID)
					}
					current.LeaseID = newLeaseID()
					if current.ExecutionID == "" {
						current.ExecutionID = newExecutionID()
					}
					return nil
				})
				if err != nil {
					continue
				}
				task = *adopted
			}
			m.reserveExisting(task)
			continue
		}
		lease := task.LeaseID
		_, _ = m.store.Update(task.ID, lease, []Status{StatusQueued, StatusRunning}, func(current *Task) error {
			current.Status, current.Error, current.LeaseID = StatusInterrupted, "manager restarted while task was active", ""
			return nil
		})
	}
}

func (m *Manager) reserveExisting(task Task) {
	m.mu.Lock()
	if _, exists := m.monitored[task.ID]; exists {
		m.mu.Unlock()
		return
	}
	m.monitored[task.ID] = task.LeaseID
	m.activeGlobal++ // Existing processes count even when a prior manager exceeded the limit.
	m.runningParent[task.ParentSessionID]++
	m.mu.Unlock()
	go m.monitorExisting(task.ID, task.ParentSessionID, task.LeaseID)
}
func (m *Manager) releaseExisting(id, parent, lease string) {
	m.mu.Lock()
	if current, ok := m.monitored[id]; !ok || current != lease {
		m.mu.Unlock()
		return
	}
	delete(m.monitored, id)
	if m.activeGlobal > 0 {
		m.activeGlobal--
	}
	if m.runningParent[parent] > 0 {
		m.runningParent[parent]--
	}
	m.cond.Broadcast()
	m.mu.Unlock()
}
func (m *Manager) monitorExisting(id, parent, lease string) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for range ticker.C {
		task, err := m.store.Load(id)
		if err != nil || terminal(task.Status) || task.Status == StatusInterrupted || task.LeaseID != lease {
			m.releaseExisting(id, parent, lease)
			return
		}
		if task.Status != StatusRunning || task.PID <= 0 || task.HeartbeatAt == nil || time.Since(*task.HeartbeatAt) >= 15*time.Second || !workerProcessAlive(task.PID) {
			_, _ = m.store.Update(id, lease, []Status{StatusRunning}, func(current *Task) error {
				current.Status, current.Error, current.LeaseID = StatusInterrupted, "subagent worker is no longer alive", ""
				return nil
			})
			m.releaseExisting(id, parent, lease)
			return
		}
	}
}
func (m *Manager) heartbeat(id, lease string, stop <-chan struct{}) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case now := <-ticker.C:
			utc := now.UTC()
			if _, err := m.store.Update(id, lease, []Status{StatusRunning}, func(task *Task) error { task.HeartbeatAt, task.PID = &utc, os.Getpid(); return nil }); err != nil {
				return
			}
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
func newID() string          { return "agent_" + newToken() }
func newLeaseID() string     { return "lease_" + newToken() }
func newExecutionID() string { return "exec_" + newToken() }
func newToken() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return time.Now().UTC().Format("20060102T150405.000000000Z") + "_" + hex.EncodeToString(b)
}
func writeJSONRoot(root *os.Root, name string, value any) error {
	content, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writeRootAtomic(root, name, append(content, '\n'), 0o644)
}

func writeRootAtomic(root *os.Root, name string, content []byte, mode os.FileMode) error {
	temp := "." + filepath.Base(name) + "-" + newToken() + ".tmp"
	file, err := root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		_ = root.Remove(temp)
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = root.Remove(temp)
		return err
	}
	if err := file.Close(); err != nil {
		_ = root.Remove(temp)
		return err
	}
	if err := root.Rename(temp, name); err != nil {
		_ = root.Remove(temp)
		return err
	}
	return nil
}

func appendJSONLRoot(root *os.Root, name string, value any) error {
	content, err := json.Marshal(value)
	if err != nil {
		return err
	}
	file, err := root.OpenFile(name, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.Write(append(content, '\n'))
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
