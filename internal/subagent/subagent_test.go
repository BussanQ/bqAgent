package subagent

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"bqagent/internal/extagent"
)

type fakeACP struct{}

type canceledACP struct{}

func (canceledACP) Initialize(ctx context.Context) error { return ctx.Err() }
func (canceledACP) LoadSessionSupported() bool           { return true }
func (canceledACP) NewSession(context.Context, string) (string, error) {
	return "", context.Canceled
}
func (canceledACP) LoadSession(context.Context, string, string) (string, error) {
	return "", context.Canceled
}
func (canceledACP) Prompt(context.Context, string, string) (string, error) {
	return "", context.Canceled
}
func (canceledACP) Close() error { return nil }

func (fakeACP) Initialize(context.Context) error                            { return nil }
func (fakeACP) LoadSessionSupported() bool                                  { return true }
func (fakeACP) NewSession(context.Context, string) (string, error)          { return "external-1", nil }
func (fakeACP) LoadSession(context.Context, string, string) (string, error) { return "external-1", nil }
func (fakeACP) Prompt(context.Context, string, string) (string, error)      { return "subagent result", nil }
func (fakeACP) Close() error                                                { return nil }

func TestManagerSpawnsPersistedWorktreeTask(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init")
	runGit(t, root, "config", "user.email", "test@example.com")
	runGit(t, root, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "README.md")
	runGit(t, root, "commit", "-m", "init")
	spec := extagent.CommandSpec{Command: "fake"}
	broker := extagent.NewBroker(extagent.NewStateStore(root), map[extagent.AgentName]extagent.DetectionResult{extagent.AgentClaude: {Agent: extagent.AgentClaude, Preferred: &extagent.AgentTransport{Agent: extagent.AgentClaude, Kind: extagent.TransportACP, Command: spec}}}, func(extagent.CommandSpec, string) (extagent.ACPClient, error) { return fakeACP{}, nil })
	defer broker.Close()
	manager := NewManager(root, broker, false)
	task, err := manager.Spawn(SpawnOptions{ParentSessionID: "session-1", Agent: extagent.AgentClaude, Prompt: "inspect", Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done, err := manager.Wait(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != StatusCompleted || done.Result != "subagent result" || done.WorktreePath == "" {
		t.Fatalf("task=%+v", done)
	}
	if _, err := os.Stat(filepath.Join(root, ".agent", "subagents", task.ID, "meta.json")); err != nil {
		t.Fatal(err)
	}
}
func TestTaskLockOnlyOwnerTokenCanUnlockAndReclaimsDeadOwner(t *testing.T) {
	dir := t.TempDir()
	unlock, err := lockTaskDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	lockDir := filepath.Join(dir, ".meta.lock")
	other, err := json.Marshal(taskLockOwner{Token: "other-owner", PID: os.Getpid()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lockDir, "owner.json"), other, 0o600); err != nil {
		t.Fatal(err)
	}
	unlock()
	if _, err := os.Stat(lockDir); err != nil {
		t.Fatalf("unlock removed a lock it no longer owns: %v", err)
	}
	if err := os.RemoveAll(lockDir); err != nil {
		t.Fatal(err)
	}

	if err := os.Mkdir(lockDir, 0o700); err != nil {
		t.Fatal(err)
	}
	dead, err := json.Marshal(taskLockOwner{Token: "dead-owner", PID: 99999999})
	if err != nil {
		t.Fatal(err)
	}
	ownerPath := filepath.Join(lockDir, "owner.json")
	if err := os.WriteFile(ownerPath, dead, 0o600); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-leaseLockStaleAfter - time.Second)
	if err := os.Chtimes(lockDir, stale, stale); err != nil {
		t.Fatal(err)
	}
	reclaimed, err := lockTaskDir(dir)
	if err != nil {
		t.Fatalf("failed to reclaim dead-owner stale lock: %v", err)
	}
	reclaimed()
}

func TestRunPersistedParentCancellationMarksTaskInterrupted(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init")
	runGit(t, root, "config", "user.email", "test@example.com")
	runGit(t, root, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "README.md")
	runGit(t, root, "commit", "-m", "init")
	store := NewStore(root)
	task, err := store.Create(SpawnOptions{ParentSessionID: "parent", Agent: extagent.AgentClaude, Prompt: "task", Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(task.ID, task.LeaseID, []Status{StatusQueued}, func(current *Task) error {
		current.WorktreePath = root
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	broker := extagent.NewBroker(extagent.NewStateStore(root), map[extagent.AgentName]extagent.DetectionResult{
		extagent.AgentClaude: {Agent: extagent.AgentClaude, Preferred: &extagent.AgentTransport{Agent: extagent.AgentClaude, Kind: extagent.TransportACP, Command: extagent.CommandSpec{Command: "fake"}}},
	}, func(extagent.CommandSpec, string) (extagent.ACPClient, error) { return canceledACP{}, nil })
	defer broker.Close()
	manager := NewWorkerManager(root, broker, false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := manager.RunPersisted(ctx, task.ID, task.LeaseID); err != nil {
		t.Fatalf("RunPersisted returned error: %v", err)
	}
	current, err := store.Load(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != StatusInterrupted {
		t.Fatalf("canceled worker task status = %s, want interrupted", current.Status)
	}
}

func TestStoreRejectsTraversalAndMismatchedMetadataID(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	if _, err := store.Dir("../other"); err == nil {
		t.Fatal("Dir accepted a traversal task ID")
	}
	if _, err := store.Load("../other"); err == nil {
		t.Fatal("Load accepted a traversal task ID")
	}

	id := "agent-safe"
	dir := filepath.Join(root, ".agent", "subagents", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content, err := json.Marshal(Task{ID: "agent-other", Status: StatusQueued})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(id); err == nil {
		t.Fatal("Load accepted mismatched metadata ID")
	}
}

func TestStoreRejectsExternalTaskDirectorySymlink(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	id := "agent-external-link"
	external := filepath.Join(t.TempDir(), id)
	if err := os.MkdirAll(external, 0o755); err != nil {
		t.Fatal(err)
	}
	metadata, err := json.Marshal(Task{ID: id, Status: StatusQueued})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(external, "meta.json"), metadata, 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, ".agent", "subagents", id)
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	makeTestSymlink(t, external, link)

	if _, err := store.Load(id); err == nil {
		t.Fatal("Load accepted an external task-directory symlink")
	}
	if err := store.Save(&Task{ID: id, Status: StatusQueued}); err == nil {
		t.Fatal("Save accepted an external task-directory symlink")
	}
	if _, err := store.Update(id, "", nil, nil); err == nil {
		t.Fatal("Update accepted an external task-directory symlink")
	}
	if err := store.Event(id, "queued", nil); err == nil {
		t.Fatal("Event accepted an external task-directory symlink")
	}
	if err := store.writeCompletionFiles(id, &completionFiles{}); err == nil {
		t.Fatal("artifact write accepted an external task-directory symlink")
	}
}

func TestStoreTaskRootRemainsPinnedAfterSymlinkSwap(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	task, err := store.Create(SpawnOptions{ParentSessionID: "parent", Agent: extagent.AgentClaude, Prompt: "task"})
	if err != nil {
		t.Fatal(err)
	}
	taskRoot, _, err := store.openTaskRoot(task.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	defer taskRoot.Close()
	taskDir, err := store.Dir(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	original := taskDir + ".original"
	if err := os.Rename(taskDir, original); err != nil {
		t.Fatal(err)
	}
	external := t.TempDir()
	makeTestSymlink(t, external, taskDir)

	content, err := taskRoot.ReadFile("meta.json")
	if err != nil {
		t.Fatalf("pinned task root followed swapped symlink: %v", err)
	}
	var loaded Task
	if err := json.Unmarshal(content, &loaded); err != nil || loaded.ID != task.ID {
		t.Fatalf("pinned task root read unexpected metadata: task=%+v err=%v", loaded, err)
	}
	if _, err := store.Load(task.ID); err == nil {
		t.Fatal("Load accepted a task directory swapped to an external symlink")
	}
	if _, err := store.Update(task.ID, task.LeaseID, []Status{StatusQueued}, nil); err == nil {
		t.Fatal("Update accepted a task directory swapped to an external symlink")
	}
}

func makeTestSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("Windows symlink permission is unavailable: %v", err)
		}
		t.Fatalf("creating symlink: %v", err)
	}
}

func TestCopyUntrackedRejectsSymbolicLinks(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init")
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	makeTestSymlink(t, outside, filepath.Join(root, "untracked-link"))
	worktree := t.TempDir()
	manager := &Manager{workspaceRoot: root}
	if err := manager.copyUntracked(worktree); err == nil {
		t.Fatal("copyUntracked accepted a symbolic link")
	}
}

func TestCopyUntrackedRejectsEscapingTargetSymlink(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init")
	if err := os.MkdirAll(filepath.Join(root, "untracked"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "untracked", "file.txt"), []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}
	worktree := t.TempDir()
	makeTestSymlink(t, t.TempDir(), filepath.Join(worktree, "untracked"))
	manager := &Manager{workspaceRoot: root}
	if err := manager.copyUntracked(worktree); err == nil {
		t.Fatal("copyUntracked accepted an escaping target symlink")
	}
}

func TestResumeOnlyAllowsFailedOrInterruptedAndRejectsDuplicate(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init")
	runGit(t, root, "config", "user.email", "test@example.com")
	runGit(t, root, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "README.md")
	runGit(t, root, "commit", "-m", "init")
	broker := testBroker(root)
	defer broker.Close()
	manager := NewManager(root, broker, false)
	store := NewStore(root)

	for _, status := range []Status{StatusQueued, StatusRunning, StatusCompleted, StatusCanceled} {
		task := createTaskWithStatus(t, store, status)
		if _, err := manager.Resume(task.ID, "again"); err == nil {
			t.Fatalf("Resume unexpectedly accepted %s", status)
		}
	}
	failed := createTaskWithStatus(t, store, StatusFailed)
	oldLease, oldExecution := failed.LeaseID, failed.ExecutionID
	resumed, err := manager.Resume(failed.ID, "again")
	if err != nil {
		t.Fatal(err)
	}
	if resumed.LeaseID == oldLease || resumed.ExecutionID == oldExecution {
		t.Fatalf("resume did not replace execution fencing fields: before=%+v after=%+v", failed, resumed)
	}
	if _, err := manager.Resume(failed.ID, "duplicate"); err == nil {
		t.Fatal("duplicate Resume unexpectedly succeeded")
	}
}

func TestCancelWinsOverLateCompletionAndOldLeaseWrites(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	task := createTaskWithStatus(t, store, StatusRunning)
	manager := newManager(root, nil, false, false, true)
	oldLease := task.LeaseID
	if err := manager.Cancel(task.ID); err != nil {
		t.Fatal(err)
	}
	if err := manager.finishCompleted(task.ID, oldLease, task, extagent.TurnResponse{Reply: "late result"}); err == nil {
		t.Fatal("late completion with revoked lease unexpectedly succeeded")
	}
	current, err := store.Load(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != StatusCanceled || current.Result != "" || current.LeaseID != "" {
		t.Fatalf("late completion overwrote cancellation: %+v", current)
	}

	resumed := createTaskWithStatus(t, store, StatusRunning)
	resumedOldLease := resumed.LeaseID
	if _, err := store.Update(resumed.ID, resumedOldLease, []Status{StatusRunning}, func(current *Task) error {
		current.Status, current.LeaseID = StatusInterrupted, ""
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	newLease := newLeaseID()
	if _, err := store.Update(resumed.ID, "", []Status{StatusInterrupted}, func(current *Task) error {
		current.Status, current.LeaseID, current.ExecutionID = StatusQueued, newLease, newExecutionID()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(resumed.ID, resumedOldLease, []Status{StatusQueued}, func(current *Task) error {
		now := time.Now().UTC()
		current.HeartbeatAt = &now
		return nil
	}); err == nil {
		t.Fatal("old lease heartbeat unexpectedly succeeded")
	}
	if err := manager.finishCompleted(resumed.ID, resumedOldLease, resumed, extagent.TurnResponse{Reply: "old result"}); err == nil {
		t.Fatal("old lease finish unexpectedly succeeded")
	}
	if err := manager.finishFailed(resumed.ID, resumedOldLease, context.Canceled, StatusQueued, StatusRunning); err == nil {
		t.Fatal("old lease failed finish unexpectedly succeeded")
	}
}

func TestStateTransitionGuards(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	manager := newManager(root, nil, false, false, true)
	queued := createTaskWithStatus(t, store, StatusQueued)
	if err := manager.Interrupt(queued.ID); err != nil {
		t.Fatal(err)
	}
	interrupted, err := store.Load(queued.ID)
	if err != nil || interrupted.Status != StatusInterrupted {
		t.Fatalf("queued interrupt result=%+v err=%v", interrupted, err)
	}
	if err := manager.Cancel(queued.ID); err == nil {
		t.Fatal("Cancel unexpectedly accepted interrupted task")
	}
	completed := createTaskWithStatus(t, store, StatusCompleted)
	if _, err := store.Update(completed.ID, completed.LeaseID, []Status{StatusCompleted}, func(current *Task) error {
		current.Status = StatusQueued
		return nil
	}); err == nil {
		t.Fatal("Store accepted completed -> queued transition")
	}
}

func TestRunPersistedClaimsQueuedOnly(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	manager := NewWorkerManager(root, nil, false)
	queued := createTaskWithStatus(t, store, StatusQueued)
	if err := manager.RunPersisted(context.Background(), queued.ID, "wrong-lease"); err == nil {
		t.Fatal("RunPersisted unexpectedly claimed a queued task with another lease")
	}
	if current, err := store.Load(queued.ID); err != nil || current.Status != StatusQueued {
		t.Fatalf("wrong-lease worker changed queued task: task=%+v err=%v", current, err)
	}
	completed := createTaskWithStatus(t, store, StatusCompleted)
	if err := manager.RunPersisted(context.Background(), completed.ID, completed.LeaseID); err == nil {
		t.Fatal("RunPersisted unexpectedly claimed a completed task")
	}
}

func TestReconcileRestoresAndReleasesExistingCapacity(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	task := createTaskWithStatus(t, store, StatusRunning)
	manager := NewManager(root, nil, false)
	if count := managerGlobalCount(manager); count != 1 {
		t.Fatalf("activeGlobal=%d, want 1 after reconcile", count)
	}
	stale := time.Now().UTC().Add(-16 * time.Second)
	if _, err := store.Update(task.ID, task.LeaseID, []Status{StatusRunning}, func(current *Task) error {
		current.HeartbeatAt = &stale
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, func() bool {
		current, err := store.Load(task.ID)
		return err == nil && current.Status == StatusInterrupted && managerGlobalCount(manager) == 0
	})
}

func TestReconcileAdoptsLiveLegacyLeaseAndInterruptsStaleLegacyTask(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	live := createTaskWithStatus(t, store, StatusRunning)
	if _, err := store.Update(live.ID, live.LeaseID, []Status{StatusRunning}, func(current *Task) error {
		current.LeaseID, current.ExecutionID = "", ""
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	stale := createTaskWithStatus(t, store, StatusRunning)
	old := time.Now().UTC().Add(-16 * time.Second)
	if _, err := store.Update(stale.ID, stale.LeaseID, []Status{StatusRunning}, func(current *Task) error {
		current.LeaseID, current.HeartbeatAt = "", &old
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(root, nil, false)
	adopted, err := store.Load(live.ID)
	if err != nil {
		t.Fatal(err)
	}
	if adopted.LeaseID == "" || adopted.ExecutionID == "" {
		t.Fatalf("live legacy task was not adopted: %+v", adopted)
	}
	interrupted, err := store.Load(stale.ID)
	if err != nil {
		t.Fatal(err)
	}
	if interrupted.Status != StatusInterrupted {
		t.Fatalf("stale legacy task=%+v, want interrupted", interrupted)
	}
	if managerGlobalCount(manager) != 1 {
		t.Fatalf("activeGlobal=%d, want only adopted live task", managerGlobalCount(manager))
	}
}

func TestReconcileOverCapacityBlocksNewStart(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	for i := 0; i < maxGlobalSubagents+1; i++ {
		createTaskWithStatus(t, store, StatusRunning)
	}
	manager := NewManager(root, nil, false)
	if count := managerGlobalCount(manager); count != maxGlobalSubagents+1 {
		t.Fatalf("activeGlobal=%d, want restored overcapacity %d", count, maxGlobalSubagents+1)
	}
	queued := createTaskWithStatus(t, store, StatusQueued)
	manager.start(queued.ID)
	time.Sleep(100 * time.Millisecond)
	current, err := store.Load(queued.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != StatusQueued {
		t.Fatalf("overcapacity started queued task: %+v", current)
	}
}

func testBroker(root string) *extagent.Broker {
	spec := extagent.CommandSpec{Command: "fake"}
	return extagent.NewBroker(extagent.NewStateStore(root), map[extagent.AgentName]extagent.DetectionResult{extagent.AgentClaude: {Agent: extagent.AgentClaude, Preferred: &extagent.AgentTransport{Agent: extagent.AgentClaude, Kind: extagent.TransportACP, Command: spec}}}, func(extagent.CommandSpec, string) (extagent.ACPClient, error) { return fakeACP{}, nil })
}

func createTaskWithStatus(t *testing.T, store *Store, status Status) *Task {
	t.Helper()
	task, err := store.Create(SpawnOptions{ParentSessionID: newID(), Agent: extagent.AgentClaude, Prompt: "task", Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if status == StatusQueued {
		return task
	}
	now := time.Now().UTC()
	updated, err := store.Update(task.ID, task.LeaseID, []Status{StatusQueued}, func(current *Task) error {
		if status == StatusCanceled || status == StatusInterrupted {
			current.Status = status
			return nil
		}
		current.Status, current.PID, current.HeartbeatAt = StatusRunning, os.Getpid(), &now
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if status == StatusRunning || status == StatusCanceled || status == StatusInterrupted {
		return updated
	}
	updated, err = store.Update(task.ID, task.LeaseID, []Status{StatusRunning}, func(current *Task) error {
		current.Status = status
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return updated
}

func managerGlobalCount(manager *Manager) int {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return manager.activeGlobal
}

func waitFor(t *testing.T, timeout time.Duration, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition was not satisfied before timeout")
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}
