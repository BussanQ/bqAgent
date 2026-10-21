//go:build windows

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// These tests exercise the Windows Job object containment of bash commands.
// The "processes" they spawn are this very test executable re-invoked with
// -test.run=^TestWindowsBashProcessHelper$ and a role selected through
// environment variables, so no PowerShell, network or external tools are needed.
// Coordination uses pid/ready/release files rather than sleeps, and process
// exit is confirmed with OpenProcess(SYNCHRONIZE)+WaitForSingleObject.

const (
	winHelperRoleEnv       = "BQ_WIN_BASH_HELPER_ROLE"
	winHelperDirEnv        = "BQ_WIN_BASH_HELPER_DIR"
	winHelperParentExitEnv = "BQ_WIN_BASH_HELPER_PARENT_EXIT"
	winHelperDetachedIOEnv = "BQ_WIN_BASH_HELPER_DETACHED_IO"

	winHelperRunArg    = "-test.run=^TestWindowsBashProcessHelper$"
	winHelperHardLimit = 2 * time.Minute
	winFileTimeout     = 30 * time.Second
	winExitTimeout     = 15 * time.Second
)

// ---------------------------------------------------------------------------
// Helper subprocess side
// ---------------------------------------------------------------------------

// TestWindowsBashProcessHelper is not a real test: it is the entry point of the
// helper subprocesses. Without the role env var it returns immediately.
//
// Roles:
//
//	parent: spawns child, waits for it, publishes parent.pid/parent.ready, then
//	        waits for "release" (or, with PARENT_EXIT=1, for "parent.exit" and
//	        exits 0 leaving its descendants alive).
//	child:  spawns leaf, waits for it, publishes child.pid/child.ready, waits.
//	leaf:   publishes leaf.pid/leaf.ready, waits for "release".
//	marker: publishes marker.pid then the "marker" file (a visible side effect),
//	        then waits for "release".
func TestWindowsBashProcessHelper(t *testing.T) {
	role := os.Getenv(winHelperRoleEnv)
	if role == "" {
		return
	}
	dir := os.Getenv(winHelperDirEnv)
	deadline := time.Now().Add(winHelperHardLimit)
	publish := func(name, content string) {
		tmp := filepath.Join(dir, name+".tmp")
		if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
			os.Exit(10)
		}
		if err := os.Rename(tmp, filepath.Join(dir, name)); err != nil {
			os.Exit(11)
		}
	}
	waitFor := func(name string) {
		path := filepath.Join(dir, name)
		for {
			if _, err := os.Stat(path); err == nil {
				return
			}
			if time.Now().After(deadline) {
				os.Exit(12) // never outlive a failed test for long
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	spawn := func(childRole string) {
		exe, err := os.Executable()
		if err != nil {
			os.Exit(13)
		}
		cmd := exec.CommandContext(context.Background(), exe, winHelperRunArg)
		cmd.Env = append(os.Environ(), winHelperRoleEnv+"="+childRole)
		if os.Getenv(winHelperDetachedIOEnv) != "1" {
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
		}
		if err := cmd.Start(); err != nil {
			os.Exit(14)
		}
	}
	pid := strconv.Itoa(os.Getpid())

	switch role {
	case "parent":
		spawn("child")
		waitFor("child.ready")
		publish("parent.pid", pid)
		publish("parent.ready", "1")
		fmt.Println("parent-ready")
		if os.Getenv(winHelperParentExitEnv) == "1" {
			waitFor("parent.exit")
			fmt.Println("parent-done")
			os.Exit(0)
		}
		waitFor("release")
	case "child":
		spawn("leaf")
		waitFor("leaf.ready")
		publish("child.pid", pid)
		publish("child.ready", "1")
		waitFor("release")
	case "leaf":
		publish("leaf.pid", pid)
		publish("leaf.ready", "1")
		waitFor("release")
	case "marker":
		publish("marker.pid", pid)
		publish("marker", "ran")
		waitFor("release")
	default:
		os.Exit(15)
	}
	os.Exit(0)
}

// ---------------------------------------------------------------------------
// Test side utilities
// ---------------------------------------------------------------------------

func winWaitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(winFileTimeout)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func winReadPID(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("parse pid in %s (%q): %v", path, data, err)
	}
	return pid
}

func winOpenProcess(pid int) (syscall.Handle, error) {
	return syscall.OpenProcess(syscall.SYNCHRONIZE|syscall.PROCESS_TERMINATE, false, uint32(pid))
}

func winProcessRunning(h syscall.Handle) bool {
	ev, err := syscall.WaitForSingleObject(h, 0)
	return err == nil && ev == syscall.WAIT_TIMEOUT
}

func winWaitExited(t *testing.T, name string, h syscall.Handle) {
	t.Helper()
	ev, err := syscall.WaitForSingleObject(h, uint32(winExitTimeout/time.Millisecond))
	if err != nil {
		t.Fatalf("wait for %s: %v", name, err)
	}
	if ev != syscall.WAIT_OBJECT_0 {
		t.Fatalf("%s is still running (wait result %#x)", name, ev)
	}
}

func winHelperCommand(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	// Expand the path inside quotes, without depending on filesystem short
	// names. The @ prefix prevents cmd /C from stripping the outer quotes.
	t.Setenv("BQ_WIN_BASH_HELPER_EXE", exe)
	return `@"%BQ_WIN_BASH_HELPER_EXE%" "` + winHelperRunArg + `"`
}

func winCleanupJob(t *testing.T, cleanup func() error) {
	t.Helper()
	if err := cleanup(); err != nil {
		t.Errorf("cleanup Windows job: %v", err)
	}
}

// winProcessTree is a parent -> child -> leaf helper chain started through
// bash. Handles are opened as soon as the chain is ready so that pid
// reuse cannot confuse liveness checks, and every process is force-terminated at
// test cleanup so failures never leave stragglers behind.
type winProcessTree struct {
	dir     string
	names   []string
	handles map[string]syscall.Handle
}

// newWinProcessTree configures the helper env for the calling test and returns
// the tree plus the shell command that starts its root. Register any
// BashJobs.Close cleanup BEFORE calling this so the force-kill runs first.
func newWinProcessTree(t *testing.T, parentExits bool) (*winProcessTree, string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(winHelperRoleEnv, "parent")
	t.Setenv(winHelperDirEnv, dir)
	if parentExits {
		t.Setenv(winHelperParentExitEnv, "1")
	} else {
		t.Setenv(winHelperParentExitEnv, "")
	}
	tree := &winProcessTree{
		dir:     dir,
		names:   []string{"leaf", "child", "parent"},
		handles: map[string]syscall.Handle{},
	}
	t.Cleanup(tree.forceKill)
	command := winHelperCommand(t)
	return tree, command
}

func (tr *winProcessTree) forceKill() {
	for _, name := range tr.names {
		h, ok := tr.handles[name]
		if !ok {
			// Failure path: attach never finished; open whatever was published.
			data, err := os.ReadFile(filepath.Join(tr.dir, name+".pid"))
			if err != nil {
				continue
			}
			pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
			if err != nil {
				continue
			}
			if h, err = winOpenProcess(pid); err != nil {
				continue
			}
			tr.handles[name] = h
		}
		_ = syscall.TerminateProcess(h, 1)
		_ = syscall.CloseHandle(h)
	}
	tr.handles = map[string]syscall.Handle{}
}

// attach waits for the whole chain to be alive and opens a handle to each.
func (tr *winProcessTree) attach(t *testing.T) {
	t.Helper()
	winWaitForFile(t, filepath.Join(tr.dir, "parent.ready"))
	for _, name := range tr.names {
		pid := winReadPID(t, filepath.Join(tr.dir, name+".pid"))
		h, err := winOpenProcess(pid)
		if err != nil {
			t.Fatalf("open %s (pid %d): %v", name, pid, err)
		}
		tr.handles[name] = h
	}
	for _, name := range tr.names {
		if !winProcessRunning(tr.handles[name]) {
			t.Fatalf("%s exited before the test acted on it", name)
		}
	}
}

func (tr *winProcessTree) requireAllExited(t *testing.T) {
	t.Helper()
	for _, name := range tr.names {
		event, err := syscall.WaitForSingleObject(tr.handles[name], 0)
		if err != nil || event != syscall.WAIT_OBJECT_0 {
			t.Errorf("%s has not exited when cleanup returned: event=%#x err=%v", name, event, err)
		}
	}
}

func (tr *winProcessTree) signal(t *testing.T, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(tr.dir, name), []byte("1"), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// startTreeJob runs the tree as a long-running background job and returns its id.
func startTreeJob(t *testing.T, ctx context.Context, command string) string {
	t.Helper()
	out, err := ExecuteBash(ctx, map[string]any{"command": command})
	if err != nil {
		t.Fatalf("ExecuteBash: %v (output %q)", err, out)
	}
	var status BashJobStatus
	if err := json.Unmarshal([]byte(out), &status); err != nil {
		t.Fatalf("expected running job JSON, got %q: %v", out, err)
	}
	if status.Status != BashJobStatusRunning || status.JobID == "" {
		t.Fatalf("expected running job, got %+v", status)
	}
	return status.JobID
}

func winWaitJobDone(t *testing.T, mgr *BashJobs, id string) {
	t.Helper()
	done := mgr.Done(id)
	if done == nil {
		t.Fatalf("job %s unknown", id)
	}
	select {
	case <-done:
	case <-time.After(winExitTimeout):
		t.Fatalf("job %s did not finish", id)
	}
}

func newWinManager(t *testing.T, parent context.Context, options BashJobOptions) (context.Context, *BashJobs) {
	t.Helper()
	if options.YieldTimeout == 0 {
		options.YieldTimeout = 100 * time.Millisecond
	}
	ctx, mgr := WithBashJobs(parent, options)
	t.Cleanup(func() { mgr.Close() })
	return ctx, mgr
}

// ---------------------------------------------------------------------------
// 1. bash_kill terminates cmd and all descendants
// ---------------------------------------------------------------------------

func TestWindowsBashKillTerminatesShellAndDescendants(t *testing.T) {
	ctx, mgr := newWinManager(t, context.Background(), BashJobOptions{})
	tree, command := newWinProcessTree(t, false)

	id := startTreeJob(t, ctx, command)
	tree.attach(t)

	out, err := BashKill(ctx, map[string]any{"job_id": id})
	if err != nil {
		t.Fatalf("BashKill: %v", err)
	}
	var status BashJobStatus
	if err := json.Unmarshal([]byte(out), &status); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	if status.Status != BashJobStatusKilled {
		t.Fatalf("status = %q, want killed (%+v)", status.Status, status)
	}
	tree.requireAllExited(t)
	if pending := mgr.Close(); len(pending) != 0 {
		t.Fatalf("observed job reported as pending: %+v", pending)
	}
}

// ---------------------------------------------------------------------------
// 2. manager.Close / parent cancel / MaxTimeout reclaim the tree
// ---------------------------------------------------------------------------

func TestWindowsBashJobsReclaimTreeOnClose(t *testing.T) {
	ctx, mgr := newWinManager(t, context.Background(), BashJobOptions{})
	tree, command := newWinProcessTree(t, false)

	id := startTreeJob(t, ctx, command)
	tree.attach(t)

	pending := mgr.Close()
	tree.requireAllExited(t)
	if len(pending) != 1 || pending[0].JobID != id || pending[0].Status != BashJobStatusKilled {
		t.Fatalf("Close pending = %+v, want one killed job %s", pending, id)
	}
}

func TestWindowsBashJobsReclaimTreeOnParentCancel(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ctx, mgr := newWinManager(t, parent, BashJobOptions{})
	tree, command := newWinProcessTree(t, false)

	id := startTreeJob(t, ctx, command)
	tree.attach(t)

	cancel()
	winWaitJobDone(t, mgr, id)
	tree.requireAllExited(t)
	if snap, ok := mgr.Snapshot(id); !ok || snap.Status != BashJobStatusKilled {
		t.Fatalf("snapshot = %+v ok=%v, want killed", snap, ok)
	}
}

func TestWindowsBashJobsReclaimTreeOnMaxTimeout(t *testing.T) {
	// The timeout must comfortably exceed helper start-up; the tree only has to
	// become ready once, then it is waiting for the timeout to fire.
	ctx, mgr := newWinManager(t, context.Background(), BashJobOptions{MaxTimeout: 8 * time.Second})
	tree, command := newWinProcessTree(t, false)

	id := startTreeJob(t, ctx, command)
	tree.attach(t)

	winWaitJobDone(t, mgr, id)
	tree.requireAllExited(t)
	snap, ok := mgr.Snapshot(id)
	if !ok || snap.Status != BashJobStatusFailed || !strings.Contains(snap.Error, "maximum background job timeout") {
		t.Fatalf("snapshot = %+v ok=%v, want failed with max-timeout error", snap, ok)
	}
}

// ---------------------------------------------------------------------------
// 3. normal shell/parent exit still reaps surviving descendants and the pipe
// ---------------------------------------------------------------------------

func runParentExitScenario(t *testing.T, useManager bool) {
	ctx := context.Background()
	if useManager {
		// Default yield time: the job is expected to finish well inside it.
		ctx, _ = newWinManager(t, ctx, BashJobOptions{YieldTimeout: winFileTimeout})
	}
	tree, command := newWinProcessTree(t, true)

	type result struct {
		out string
		err error
	}
	resCh := make(chan result, 1)
	go func() {
		out, err := ExecuteBash(ctx, map[string]any{"command": command})
		resCh <- result{out, err}
	}()

	tree.attach(t)
	// Descendants hold the inherited stdout pipe; let the parent exit now.
	tree.signal(t, "parent.exit")
	winWaitExited(t, "parent", tree.handles["parent"])
	exitedAt := time.Now()

	select {
	case res := <-resCh:
		// Returning well before commandWaitDelay shows the pipe was released by
		// killing the survivors, not by the forced pipe close.
		if elapsed := time.Since(exitedAt); elapsed >= commandWaitDelay-time.Second {
			t.Fatalf("ExecuteBash returned %s after the parent exited; descendants were not reaped", elapsed)
		}
		if res.err != nil {
			t.Fatalf("ExecuteBash error: %v (output %q)", res.err, res.out)
		}
		if !strings.Contains(res.out, "parent-done") {
			t.Fatalf("output = %q, want it to contain parent-done", res.out)
		}
	case <-time.After(commandWaitDelay + 5*time.Second):
		t.Fatal("ExecuteBash did not return after the parent exited")
	}
	tree.requireAllExited(t)
}

func TestWindowsBashParentExitReapsDescendants(t *testing.T) {
	runParentExitScenario(t, false)
}

func TestWindowsBashParentExitReapsDescendantsWithJobs(t *testing.T) {
	runParentExitScenario(t, true)
}

func TestWindowsBashParentExitReapsDescendantsWithDetachedOutput(t *testing.T) {
	for _, withJobs := range []bool{false, true} {
		t.Run(fmt.Sprintf("jobs=%v", withJobs), func(t *testing.T) {
			t.Setenv(winHelperDetachedIOEnv, "1")
			runParentExitScenario(t, withJobs)
		})
	}
}

// ---------------------------------------------------------------------------
// 4. short commands: stdout and non-zero exit
// ---------------------------------------------------------------------------

func TestWindowsBashShortCommandOutputAndExitCode(t *testing.T) {
	for _, withJobs := range []bool{false, true} {
		t.Run(fmt.Sprintf("jobs=%v", withJobs), func(t *testing.T) {
			ctx := context.Background()
			if withJobs {
				ctx, _ = newWinManager(t, ctx, BashJobOptions{YieldTimeout: winFileTimeout})
			}

			out, err := ExecuteBash(ctx, map[string]any{"command": "echo hello"})
			if err != nil {
				t.Fatalf("echo: %v", err)
			}
			if strings.TrimSpace(out) != "hello" {
				t.Fatalf("stdout = %q, want hello", out)
			}

			out, err = ExecuteBash(ctx, map[string]any{"command": "echo oops 1>&2 & exit 3"})
			if err == nil || !strings.Contains(err.Error(), "status 3") {
				t.Fatalf("err = %v, want exit status 3", err)
			}
			if !strings.Contains(out, "oops") {
				t.Fatalf("stderr output = %q, want oops", out)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 5. prepareBashProcess directly
// ---------------------------------------------------------------------------

func winMarkerCmd(t *testing.T, dir string) *exec.Cmd {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	cmd := exec.CommandContext(context.Background(), exe, winHelperRunArg)
	cmd.Env = append(os.Environ(), winHelperRoleEnv+"=marker", winHelperDirEnv+"="+dir)
	return cmd
}

func TestWindowsPrepareBashProcessDefersSideEffectsUntilAfterStart(t *testing.T) {
	dir := t.TempDir()
	cmd := winMarkerCmd(t, dir)

	afterStart, cleanup, err := prepareBashProcess(cmd)
	if err != nil {
		t.Fatalf("prepareBashProcess: %v", err)
	}
	t.Cleanup(func() { winCleanupJob(t, cleanup) })
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	h, err := winOpenProcess(cmd.Process.Pid)
	if err != nil {
		t.Fatalf("open process: %v", err)
	}
	t.Cleanup(func() {
		_ = syscall.TerminateProcess(h, 1)
		_ = syscall.CloseHandle(h)
	})

	// Suspended: for a bounded observation window the process must neither run
	// code (no marker file) nor exit. A negative can only be observed over time.
	window := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(window) {
		if _, err := os.Stat(filepath.Join(dir, "marker")); err == nil {
			t.Fatal("process executed before afterStart resumed it")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !winProcessRunning(h) {
		t.Fatal("suspended process exited before afterStart")
	}

	if err := afterStart(); err != nil {
		t.Fatalf("afterStart: %v", err)
	}
	winWaitForFile(t, filepath.Join(dir, "marker"))
	if pid := winReadPID(t, filepath.Join(dir, "marker.pid")); pid != cmd.Process.Pid {
		t.Fatalf("marker pid = %d, want %d", pid, cmd.Process.Pid)
	}

	// cleanup closes the kill-on-close job and is idempotent.
	winCleanupJob(t, cleanup)
	winWaitExited(t, "marker process", h)
	winCleanupJob(t, cleanup)
	winCleanupJob(t, cleanup)
	_ = cmd.Wait()
}

func TestWindowsPrepareBashProcessCleanupAfterStartFailure(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), filepath.Join(t.TempDir(), "does-not-exist.exe"))
	afterStart, cleanup, err := prepareBashProcess(cmd)
	if err != nil {
		t.Fatalf("prepareBashProcess: %v", err)
	}
	if afterStart == nil || cleanup == nil {
		t.Fatalf("afterStart=%v cleanup=%v, want both non-nil", afterStart != nil, cleanup != nil)
	}
	if err := cmd.Start(); err == nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatal("Start of a missing executable unexpectedly succeeded")
	}
	// Cleanup after a failed Start (and repeatedly) must be safe.
	winCleanupJob(t, cleanup)
	winCleanupJob(t, cleanup)
}

func TestWindowsPrepareBashProcessCleanupWithoutStart(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), os.Args[0], winHelperRunArg)
	_, cleanup, err := prepareBashProcess(cmd)
	if err != nil {
		t.Fatalf("prepareBashProcess: %v", err)
	}
	winCleanupJob(t, cleanup)
	winCleanupJob(t, cleanup)
}

// A context canceled after Start but before afterStart must kill the still
// suspended shell (not just terminate an empty job), and afterStart must then
// refuse to assign/resume, so the command never runs.
func TestWindowsPrepareBashProcessCancelBeforeAfterStartKillsSuspendedShell(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	cmd := exec.CommandContext(ctx, exe, winHelperRunArg)
	cmd.Env = append(os.Environ(), winHelperRoleEnv+"=marker", winHelperDirEnv+"="+dir)

	afterStart, cleanup, err := prepareBashProcess(cmd)
	if err != nil {
		t.Fatalf("prepareBashProcess: %v", err)
	}
	t.Cleanup(func() { winCleanupJob(t, cleanup) })
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	h, err := winOpenProcess(cmd.Process.Pid)
	if err != nil {
		t.Fatalf("open process: %v", err)
	}
	t.Cleanup(func() {
		_ = syscall.TerminateProcess(h, 1)
		_ = syscall.CloseHandle(h)
	})

	cancel()
	winWaitExited(t, "suspended shell", h)

	if err := afterStart(); err == nil {
		t.Fatal("afterStart succeeded after the command was canceled")
	}
	if _, err := os.Stat(filepath.Join(dir, "marker")); err == nil {
		t.Fatal("canceled command executed")
	}
	_ = cmd.Wait()
	winCleanupJob(t, cleanup)
	winCleanupJob(t, cleanup)
}
