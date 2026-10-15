package herdr

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func testEnv(key string) string {
	return map[string]string{"HERDR_ENV": "1", "HERDR_BIN_PATH": "/path with spaces/herdr", "HERDR_PANE_ID": "w1:p1", "HERDR_SOCKET_PATH": "/tmp/test.sock"}[key]
}

func awaitCall(t *testing.T, calls <-chan []string) []string {
	t.Helper()
	select {
	case args := <-calls:
		return args
	case <-time.After(reportTimeout + time.Second):
		t.Fatal("report worker did not make progress")
		return nil
	}
}

func argument(args []string, flag string) string {
	for index, arg := range args {
		if arg == flag && index+1 < len(args) {
			return args[index+1]
		}
	}
	return ""
}

func TestReporterDisabledOutsideHerdr(t *testing.T) {
	for _, missing := range []string{"HERDR_ENV", "HERDR_BIN_PATH", "HERDR_PANE_ID", "HERDR_SOCKET_PATH"} {
		r := newReporter(func(key string) string {
			if key == missing {
				return ""
			}
			return testEnv(key)
		}, []string{"bqagent", "--chat"}, func(context.Context, string, []string) error { t.Error("disabled reporter ran a command"); return nil })
		if r != nil {
			r.Close()
			t.Fatalf("reporter enabled without %s", missing)
		}
		r.Report("working", "session")
		r.Close()
	}
	if New(nil, nil) != nil {
		t.Fatal("nil environment enabled reporter")
	}
}

func TestReporterCoalescesWithoutBlockingAndReleases(t *testing.T) {
	calls := make(chan []string, 8)
	gate := make(chan struct{})
	first := true
	r := newReporter(testEnv, []string{"bqagent", "--chat"}, func(ctx context.Context, bin string, args []string) error {
		if bin != testEnv("HERDR_BIN_PATH") {
			t.Errorf("binary path was split: %q", bin)
		}
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > reportTimeout {
			t.Error("report lacks bounded deadline")
		}
		calls <- args
		if first {
			first = false
			<-gate
		}
		return errors.New("Herdr unavailable")
	})
	r.Report("working", "one")
	initial := awaitCall(t, calls)
	posted := make(chan struct{})
	go func() {
		for index := 0; index < 1000; index++ {
			r.Report("blocked", strconv.Itoa(index))
		}
		r.Report("idle", "two")
		r.Report("idle", "two")
		close(posted)
	}()
	select {
	case <-posted:
	case <-time.After(time.Second):
		close(gate)
		t.Fatal("Report blocked behind Herdr")
	}
	close(gate)
	latest := awaitCall(t, calls)
	if argument(initial, "--state") != "working" || argument(latest, "--state") != "idle" || argument(latest, "--agent-session-id") != "two" {
		t.Fatalf("snapshots: %v / %v", initial, latest)
	}
	separator := 0
	for index, arg := range latest {
		if arg == "--" {
			separator = index + 1
			break
		}
	}
	if !reflect.DeepEqual(latest[separator:], []string{"bqagent", "--chat", "--resume", "two"}) {
		t.Fatalf("resume argv=%v", latest)
	}
	r.Close()
	release := awaitCall(t, calls)
	if release[1] != "release-agent" || argument(release, "--source") != "bqagent" || argument(release, "--agent") != "bqAgent" {
		t.Fatalf("release=%v", release)
	}
	previous := int64(0)
	for _, args := range [][]string{initial, latest, release} {
		seq, err := strconv.ParseInt(argument(args, "--seq"), 10, 64)
		if err != nil || seq <= previous || seq < time.Now().Add(-time.Minute).UnixMicro() {
			t.Fatalf("sequence not monotonic/time-based: %v", args)
		}
		previous = seq
	}
	r.Report("working", "ignored")
	r.Close()
	if len(calls) != 0 {
		t.Fatalf("duplicate or post-close command: %v", <-calls)
	}
}

func TestReporterCloseCancelsInFlightAndUsesFreshReleaseContext(t *testing.T) {
	started := make(chan struct{})
	released := make(chan struct{})
	r := newReporter(testEnv, []string{"bqagent", "--chat"}, func(ctx context.Context, _ string, args []string) error {
		if args[1] == "report-agent" {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		}
		if ctx.Err() != nil {
			t.Error("release inherited cancelled request context")
		}
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > releaseTimeout {
			t.Error("release must retain its short, independent deadline")
		}
		close(released)
		return nil
	})
	r.Report("working", "one")
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("worker not started")
	}
	r.Close()
	select {
	case <-released:
	default:
		t.Fatal("exit did not attempt release")
	}
	select {
	case <-r.done:
	default:
		t.Fatal("worker did not stop")
	}
}

func TestReporterTimeoutDoesNotPreventLaterReports(t *testing.T) {
	calls := make(chan []string, 4)
	r := newReporter(testEnv, []string{"bqagent", "--chat"}, func(ctx context.Context, _ string, args []string) error {
		calls <- args
		if argument(args, "--state") == "working" {
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	})
	defer r.Close()
	r.Report("working", "one")
	awaitCall(t, calls)
	r.Report("idle", "one")
	if args := awaitCall(t, calls); argument(args, "--state") != "idle" {
		t.Fatalf("timeout lost latest snapshot: %v", args)
	}
}

func TestReporterCloseWaitsForSlowRelease(t *testing.T) {
	started := make(chan struct{})
	released := make(chan error, 1)
	r := newReporter(testEnv, []string{"bqagent", "--chat"}, func(ctx context.Context, _ string, args []string) error {
		if args[1] == "report-agent" {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		}
		timer := time.NewTimer(300 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
			released <- nil
			return nil
		case <-ctx.Done():
			released <- ctx.Err()
			return ctx.Err()
		}
	})
	defer r.Close()
	r.Report("idle", "session")
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("report worker did not start")
	}
	r.Close()
	select {
	case err := <-released:
		if err != nil {
			t.Fatalf("healthy release cancelled before completion: %v", err)
		}
	default:
		t.Fatal("Close returned before healthy release completed")
	}
}

func TestReporterCloseBoundsUnresponsiveRelease(t *testing.T) {
	released := make(chan error, 1)
	r := newReporter(testEnv, nil, func(ctx context.Context, _ string, args []string) error {
		if args[1] == "release-agent" {
			<-ctx.Done()
			released <- ctx.Err()
		}
		return ctx.Err()
	})
	closed := make(chan struct{})
	go func() { r.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(closeTimeout + time.Second):
		t.Fatal("Close hung on unresponsive release")
	}
	select {
	case err := <-released:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("release error=%v, want deadline exceeded", err)
		}
	case <-time.After(time.Second):
		t.Fatal("release worker did not stop after deadline")
	}
}

func TestReporterAllowsSlowStartupReport(t *testing.T) {
	completed := make(chan error, 1)
	r := newReporter(testEnv, []string{"bqagent", "--chat"}, func(ctx context.Context, _ string, args []string) error {
		if args[1] != "report-agent" {
			return nil
		}
		// The user's Herdr CLI took 196ms. Leave room for startup and IPC
		// without cancelling a healthy report at the old 150ms deadline.
		timer := time.NewTimer(300 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
			completed <- nil
			return nil
		case <-ctx.Done():
			completed <- ctx.Err()
			return ctx.Err()
		}
	})
	defer r.Close()
	r.Report("idle", "")
	select {
	case err := <-completed:
		if err != nil {
			t.Fatalf("healthy slow startup report cancelled: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("startup report did not finish")
	}
}

func TestReporterCloseHasHardWaitBound(t *testing.T) {
	started, unblock := make(chan struct{}), make(chan struct{})
	r := newReporter(testEnv, nil, func(ctx context.Context, _ string, args []string) error {
		if args[1] == "report-agent" {
			close(started)
			<-unblock
		}
		return nil
	})
	r.Report("working", "")
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("worker not started")
	}
	closed := make(chan struct{})
	go func() { r.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(closeTimeout + time.Second):
		close(unblock)
		t.Fatal("Close waited indefinitely for broken transport")
	}
	close(unblock)
	select {
	case <-r.done:
	case <-time.After(time.Second):
		t.Fatal("worker leaked after transport returned")
	}
}

func TestReporterResumeValidationAndClear(t *testing.T) {
	for _, argv := range [][]string{nil, {"/usr/bin/bqagent"}, {".."}, {"bqagent", "bad'value"}, {"bqagent", "bad\nvalue"}, append([]string{"bqagent"}, make([]string, 64)...), {"bqagent", strings.Repeat("x", 8192)}} {
		if validResume(argv) {
			t.Fatalf("accepted invalid argv: %v", argv)
		}
	}
	r := &Reporter{pane: "w1:p1", resume: []string{"bqagent", "--chat"}}
	for _, id := range []string{"", "bad'value", "bad\x00value"} {
		args := r.arguments(snapshot{state: "idle", sessionID: id}, false)
		if argument(args, "--state") != "idle" || argument(args, "--agent-session-id") != "" {
			t.Fatalf("bad session invalidated status: %v", args)
		}
		if !reflect.DeepEqual(args[len(args)-3:], []string{"--", "bqagent", "--chat"}) {
			t.Fatalf("clear/invalid session retained stale resume: %v", args)
		}
	}
}

func TestReporterConcurrentCloseAndUpdates(t *testing.T) {
	calls := make(chan []string, 1000)
	r := newReporter(testEnv, []string{"bqagent", "--chat"}, func(_ context.Context, _ string, args []string) error { calls <- args; return nil })
	var writers sync.WaitGroup
	for index := 0; index < 8; index++ {
		writers.Add(1)
		go func() {
			defer writers.Done()
			for count := 0; count < 100; count++ {
				r.Report("working", strconv.Itoa(count))
			}
			r.Close()
		}()
	}
	writers.Wait()
	select {
	case <-r.done:
	default:
		t.Fatal("worker not stopped")
	}
	close(calls)
	releases := 0
	for args := range calls {
		if releases > 0 {
			t.Fatal("command sent after release")
		}
		if args[1] == "release-agent" {
			releases++
		}
	}
	if releases != 1 {
		t.Fatalf("release count=%d", releases)
	}
}
