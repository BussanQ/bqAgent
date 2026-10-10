// Package herdr provides best-effort, bounded reporting to the parent terminal.
// No network, process creation, or waiting occurs on the Report call path.
package herdr

import (
	"context"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Reporting is asynchronous: allow normal CLI startup/IPC latency without
// increasing the independent shutdown budget.
const reportTimeout = time.Second
const releaseTimeout = 150 * time.Millisecond
const closeTimeout = 200 * time.Millisecond

type snapshot struct{ state, sessionID string }
type commandRunner func(context.Context, string, []string) error

type Reporter struct {
	mu            sync.Mutex
	bin, pane     string
	resume        []string
	run           commandRunner
	latest        snapshot
	dirty, closed bool
	cancel        context.CancelFunc
	wake, done    chan struct{}
	seq           int64
}

// New returns nil outside a fully configured Herdr pane. A nil Reporter is a
// no-op; disabled integrations allocate neither a worker nor subprocesses.
func New(getenv func(string) string, resume []string) *Reporter {
	return newReporter(getenv, resume, nil)
}

func newReporter(getenv func(string) string, resume []string, runner commandRunner) *Reporter {
	if getenv == nil || getenv("HERDR_ENV") != "1" {
		return nil
	}
	bin, pane, socket := getenv("HERDR_BIN_PATH"), getenv("HERDR_PANE_ID"), getenv("HERDR_SOCKET_PATH")
	if strings.TrimSpace(bin) == "" || strings.TrimSpace(pane) == "" || strings.TrimSpace(socket) == "" {
		return nil
	}
	if runner == nil {
		runner = func(ctx context.Context, bin string, args []string) error {
			cmd := exec.CommandContext(ctx, bin, args...)
			cmd.Env = append(os.Environ(), "HERDR_ENV=1", "HERDR_BIN_PATH="+bin, "HERDR_PANE_ID="+pane, "HERDR_SOCKET_PATH="+socket)
			// Nil stdio uses the null device: no terminal corruption, output
			// buffering, or inherited pipe that can keep Wait blocked.
			cmd.WaitDelay = 25 * time.Millisecond
			return cmd.Run()
		}
	}
	r := &Reporter{bin: bin, pane: pane, resume: append([]string(nil), resume...), run: runner, wake: make(chan struct{}, 1), done: make(chan struct{})}
	go r.loop()
	return r
}

// Report replaces the pending snapshot instead of growing a queue. Calls are
// deduplicated; a stalled/broken Herdr never delays model or UI processing.
func (r *Reporter) Report(state, sessionID string) {
	if r == nil || (state != "idle" && state != "working" && state != "blocked") {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	next := snapshot{state: state, sessionID: sessionID}
	if r.closed || next == r.latest {
		return
	}
	r.latest, r.dirty = next, true
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// Close cancels an in-flight status update and attempts release with a fresh,
// short-lived context. Shutdown never waits indefinitely for Herdr.
func (r *Reporter) Close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.closed = true
	if r.cancel != nil {
		r.cancel()
	}
	select {
	case r.wake <- struct{}{}:
	default:
	}
	r.mu.Unlock()
	timer := time.NewTimer(closeTimeout)
	defer timer.Stop()
	select {
	case <-r.done:
	case <-timer.C:
	}
}

func (r *Reporter) loop() {
	defer close(r.done)
	for range r.wake {
		r.mu.Lock()
		if r.closed {
			r.mu.Unlock()
			ctx, cancel := context.WithTimeout(context.Background(), releaseTimeout)
			_ = r.run(ctx, r.bin, r.arguments(snapshot{}, true))
			cancel()
			return
		}
		if !r.dirty {
			r.mu.Unlock()
			continue
		}
		current := r.latest
		r.dirty = false
		ctx, cancel := context.WithTimeout(context.Background(), reportTimeout)
		r.cancel = cancel
		r.mu.Unlock()
		_ = r.run(ctx, r.bin, r.arguments(current, false))
		cancel()
		r.mu.Lock()
		r.cancel = nil
		r.mu.Unlock()
	}
}

func (r *Reporter) arguments(current snapshot, release bool) []string {
	// Microseconds fit within JavaScript's exact integer range, unlike UnixNano.
	// Timestamp-based numbering survives normal process/session restarts; the
	// local max also guarantees ordering for changes within the same clock tick.
	r.seq = max(time.Now().UnixMicro(), r.seq+1)
	operation := "report-agent"
	if release {
		operation = "release-agent"
	}
	args := []string{"pane", operation, r.pane, "--source", "bqagent", "--agent", "bqAgent", "--seq", strconv.FormatInt(r.seq, 10)}
	if release {
		return args
	}
	args = append(args, "--state", current.state)
	resume := append([]string(nil), r.resume...)
	if current.sessionID != "" {
		resume = append(resume, "--resume", current.sessionID)
	}
	if !validResume(resume) {
		// An invalid session must not invalidate the entire status report.
		resume = r.resume
		current.sessionID = ""
	}
	if validResume(resume) {
		if current.sessionID != "" {
			args = append(args, "--agent-session-id", current.sessionID)
		}
		args = append(args, "--")
		args = append(args, resume...)
	}
	return args
}

var plainCommand = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`)

func validResume(argv []string) bool {
	if len(argv) == 0 || len(argv) > 64 || !plainCommand.MatchString(argv[0]) || argv[0] == "." || argv[0] == ".." {
		return false
	}
	total := 0
	for _, arg := range argv {
		total += len(arg) + 1
		if strings.ContainsRune(arg, '\'') || strings.ContainsFunc(arg, unicode.IsControl) {
			return false
		}
	}
	return total <= 8*1024
}
