//go:build !windows

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

func jobCtx(t *testing.T, opts BashJobOptions) (context.Context, *BashJobs) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ctx, mgr := WithBashJobs(ctx, opts)
	t.Cleanup(func() { mgr.Close(); cancel() })
	return ctx, mgr
}

func decodeJobStatus(t *testing.T, text string) BashJobStatus {
	t.Helper()
	var st BashJobStatus
	if err := json.Unmarshal([]byte(text), &st); err != nil {
		t.Fatalf("decode %q: %v", text, err)
	}
	return st
}

func TestBashJobsShortCommandKeepsPlainTextAndErrorBehavior(t *testing.T) {
	ctx, mgr := jobCtx(t, BashJobOptions{YieldTimeout: 5 * time.Second})
	out, err := ExecuteBash(ctx, map[string]any{"command": "printf hello"})
	if err != nil || out != "hello" {
		t.Fatalf("out=%q err=%v", out, err)
	}
	out, err = ExecuteBash(ctx, map[string]any{"command": "printf sentinel; exit 7"})
	if err == nil || !strings.Contains(err.Error(), "status 7") || out != "sentinel" {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if p := mgr.Pending(); len(p) != 0 {
		t.Fatalf("pending = %+v", p)
	}
}

func TestBashJobsLongCommandYieldsThenWaitCompletesWithoutRerun(t *testing.T) {
	dir := t.TempDir()
	ctx, mgr := jobCtx(t, BashJobOptions{YieldTimeout: 50 * time.Millisecond})
	ctx, capture := WithBashResultCapture(ctx)
	cmd := "echo run >> " + dir + "/count; echo first; sleep 0.5; echo second"
	out, err := ExecuteBash(ctx, map[string]any{"command": cmd})
	if err != nil {
		t.Fatal(err)
	}
	st := decodeJobStatus(t, out)
	if st.Status != "running" || st.JobID == "" || !strings.Contains(st.Output, "first") {
		t.Fatalf("status = %+v", st)
	}
	if capture.Result == nil || capture.Result.JobID != st.JobID {
		t.Fatalf("capture = %+v", capture.Result)
	}
	if p := mgr.Pending(); len(p) != 1 || p[0].JobID != st.JobID {
		t.Fatalf("pending = %+v", p)
	}
	for i := 0; i < 2; i++ {
		out, err = BashWait(ctx, map[string]any{"job_id": st.JobID, "wait_seconds": float64(5)})
		if err != nil {
			t.Fatal(err)
		}
		final := decodeJobStatus(t, out)
		if final.Status != "completed" || final.ExitCode == nil || *final.ExitCode != 0 || !strings.Contains(final.Output, "second") {
			t.Fatalf("final = %+v", final)
		}
		if i == 0 && strings.Contains(final.NewOutput, "first") {
			t.Fatalf("new output repeated consumed text: %q", final.NewOutput)
		}
	}
	data, _ := os.ReadFile(dir + "/count")
	if strings.Count(string(data), "run") != 1 {
		t.Fatalf("command reran: %q", data)
	}
	if p := mgr.Pending(); len(p) != 0 {
		t.Fatalf("pending after wait = %+v", p)
	}
}

func TestBashJobsFinishedUnwaitedJobStaysPendingAndFailedWaitReturnsErrorWithJSON(t *testing.T) {
	ctx, mgr := jobCtx(t, BashJobOptions{YieldTimeout: 20 * time.Millisecond})
	out, _ := ExecuteBash(ctx, map[string]any{"command": "sleep 0.15; echo bad; exit 3"})
	st := decodeJobStatus(t, out)
	time.Sleep(400 * time.Millisecond)
	if p := mgr.Pending(); len(p) != 1 || p[0].Status != "failed" {
		t.Fatalf("pending = %+v", p)
	}
	out, err := BashWait(ctx, map[string]any{"job_id": st.JobID, "wait_seconds": float64(0)})
	if err == nil || !strings.Contains(err.Error(), "status 3") {
		t.Fatalf("err = %v", err)
	}
	final := decodeJobStatus(t, out)
	if final.Status != "failed" || final.ExitCode == nil || *final.ExitCode != 3 || !strings.Contains(final.Output, "bad") {
		t.Fatalf("final = %+v", final)
	}
}

func TestBashWaitZeroSecondsQueriesImmediatelyAndRejectsBadInput(t *testing.T) {
	ctx, _ := jobCtx(t, BashJobOptions{YieldTimeout: 20 * time.Millisecond})
	out, _ := ExecuteBash(ctx, map[string]any{"command": "sleep 5"})
	st := decodeJobStatus(t, out)
	start := time.Now()
	out, err := BashWait(ctx, map[string]any{"job_id": st.JobID, "wait_seconds": float64(0)})
	if err != nil || time.Since(start) > time.Second || decodeJobStatus(t, out).Status != "running" {
		t.Fatalf("out=%q err=%v", out, err)
	}
	for _, bad := range []any{float64(-1), 1.5, 301.0, 1e300, "abc", "5", nil, true, "NaN", "Inf"} {
		if _, err := BashWait(ctx, map[string]any{"job_id": st.JobID, "wait_seconds": bad}); err == nil {
			t.Errorf("wait_seconds %v accepted", bad)
		}
	}
	if _, err := BashWait(ctx, map[string]any{}); err == nil {
		t.Error("missing job_id accepted")
	}
}

func TestBashKillTerminatesWholeProcessGroup(t *testing.T) {
	dir := t.TempDir()
	ctx, mgr := jobCtx(t, BashJobOptions{YieldTimeout: 50 * time.Millisecond})
	out, _ := ExecuteBash(ctx, map[string]any{"command": "sleep 60 & echo $! > " + dir + "/pid; wait"})
	st := decodeJobStatus(t, out)
	out, err := BashKill(ctx, map[string]any{"job_id": st.JobID})
	if err != nil || decodeJobStatus(t, out).Status != "killed" {
		t.Fatalf("out=%q err=%v", out, err)
	}
	data, _ := os.ReadFile(dir + "/pid")
	var pid int
	if _, err := fmt.Sscan(strings.TrimSpace(string(data)), &pid); err != nil || pid == 0 {
		t.Fatalf("pid %q", data)
	}
	deadline := time.Now().Add(2 * time.Second)
	for syscall.Kill(pid, 0) == nil && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if syscall.Kill(pid, 0) == nil {
		t.Fatal("grandchild still alive after bash_kill")
	}
	if p := mgr.Pending(); len(p) != 0 {
		t.Fatalf("pending = %+v", p)
	}
}

func TestBashJobsNormalShellExitKillsLeftoverChildren(t *testing.T) {
	dir := t.TempDir()
	ctx, _ := jobCtx(t, BashJobOptions{YieldTimeout: 5 * time.Second})
	start := time.Now()
	if _, err := ExecuteBash(ctx, map[string]any{"command": "sleep 60 & echo $! > " + dir + "/pid"}); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("blocked on pipe held by leftover child")
	}
	data, _ := os.ReadFile(dir + "/pid")
	var pid int
	fmt.Sscan(strings.TrimSpace(string(data)), &pid)
	deadline := time.Now().Add(2 * time.Second)
	for syscall.Kill(pid, 0) == nil && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if syscall.Kill(pid, 0) == nil {
		t.Fatal("leftover child survived shell exit")
	}
}

func TestBashJobsContextCancelAndCloseReapRunningJobs(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	ctx, mgr := WithBashJobs(parent, BashJobOptions{YieldTimeout: 20 * time.Millisecond})
	out, _ := ExecuteBash(ctx, map[string]any{"command": "echo started; sleep 60"})
	st := decodeJobStatus(t, out)
	cancel()
	done := make(chan []BashJobStatus, 1)
	go func() { done <- mgr.Close() }()
	select {
	case pending := <-done:
		if len(pending) != 1 || pending[0].JobID != st.JobID || pending[0].Status != "killed" {
			t.Fatalf("close = %+v", pending)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not reap job")
	}
	if again := mgr.Close(); len(again) != 0 {
		t.Fatalf("second close = %+v", again)
	}
	if _, err := ExecuteBash(ctx, map[string]any{"command": "true"}); err == nil {
		t.Fatal("start after close should fail")
	}
}

func TestBashJobsOutputIsBoundedHeadAndTail(t *testing.T) {
	ctx, _ := jobCtx(t, BashJobOptions{YieldTimeout: 20 * time.Millisecond})
	run := ExecuteBashInDirWithMaxOutput("", 1024)
	out, _ := run(ctx, map[string]any{"command": "printf HEAD; head -c 200000 /dev/zero | tr '\\0' x; sleep 0.3; printf TAIL"})
	st := decodeJobStatus(t, out)
	out, err := BashWait(ctx, map[string]any{"job_id": st.JobID, "wait_seconds": float64(5)})
	if err != nil {
		t.Fatal(err)
	}
	final := decodeJobStatus(t, out)
	if !final.Truncated || final.OutputBytes != 200000+8 {
		t.Fatalf("truncated=%v bytes=%d", final.Truncated, final.OutputBytes)
	}
	if len(final.Output) > 1024+100 || len(final.NewOutput) > 1024+100 {
		t.Fatalf("output len %d / %d", len(final.Output), len(final.NewOutput))
	}
	if !strings.HasPrefix(final.Output, "HEAD") || !strings.HasSuffix(final.Output, "TAIL") {
		t.Fatalf("head/tail lost: %q", final.Output[:20])
	}
}

func TestBashJobsManagersAreIsolatedAcrossTurns(t *testing.T) {
	ctxA, _ := jobCtx(t, BashJobOptions{YieldTimeout: 20 * time.Millisecond})
	ctxB, _ := jobCtx(t, BashJobOptions{YieldTimeout: 20 * time.Millisecond})
	out, _ := ExecuteBash(ctxA, map[string]any{"command": "sleep 5"})
	st := decodeJobStatus(t, out)
	if _, err := BashWait(ctxB, map[string]any{"job_id": st.JobID, "wait_seconds": float64(0)}); err == nil {
		t.Fatal("handle usable from another turn")
	}
	if _, err := BashKill(ctxB, map[string]any{"job_id": st.JobID}); err == nil {
		t.Fatal("kill usable from another turn")
	}
	if _, err := BashWait(context.Background(), map[string]any{"job_id": st.JobID}); err == nil {
		t.Fatal("wait without manager should fail")
	}
}

func TestBashJobsEnforceConcurrentLimitAndFreeSlotAfterKill(t *testing.T) {
	ctx, _ := jobCtx(t, BashJobOptions{YieldTimeout: 20 * time.Millisecond, MaxConcurrent: 2})
	var ids []string
	for i := 0; i < 2; i++ {
		out, err := ExecuteBash(ctx, map[string]any{"command": "sleep 30"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, decodeJobStatus(t, out).JobID)
	}
	if _, err := ExecuteBash(ctx, map[string]any{"command": "sleep 30"}); err == nil || !strings.Contains(err.Error(), "too many") {
		t.Fatalf("err = %v", err)
	}
	if _, err := BashKill(ctx, map[string]any{"job_id": ids[0]}); err != nil {
		t.Fatal(err)
	}
	if _, err := ExecuteBash(ctx, map[string]any{"command": "true"}); err != nil {
		t.Fatalf("slot not freed: %v", err)
	}
}

func TestBashJobsDefaultConcurrentLimitIsThree(t *testing.T) {
	ctx, _ := jobCtx(t, BashJobOptions{YieldTimeout: 10 * time.Millisecond})
	for i := 0; i < 3; i++ {
		if _, err := ExecuteBash(ctx, map[string]any{"command": "sleep 30"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ExecuteBash(ctx, map[string]any{"command": "sleep 30"}); err == nil {
		t.Fatal("fourth job accepted")
	}
}

func TestBashJobsMaxTimeoutHardCapFailsJob(t *testing.T) {
	ctx, _ := jobCtx(t, BashJobOptions{YieldTimeout: 5 * time.Second, MaxTimeout: 150 * time.Millisecond})
	out, err := ExecuteBash(ctx, map[string]any{"command": "echo partial; sleep 30"})
	if err == nil || !strings.Contains(err.Error(), "maximum") || !strings.Contains(out, "partial") {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestBashJobsRunNonInteractiveEnvironmentWithoutCI(t *testing.T) {
	t.Setenv("CI", "")
	os.Unsetenv("CI")
	ctx, _ := jobCtx(t, BashJobOptions{YieldTimeout: 5 * time.Second})
	cmd := `printf '%s|%s|%s|%s|%s' "$GIT_TERMINAL_PROMPT" "$GIT_PAGER" "$PAGER" "$DEBIAN_FRONTEND" "${CI-unset}"; read x </dev/null; echo " eof=$?"`
	for _, c := range []context.Context{ctx, context.Background()} {
		out, err := ExecuteBash(c, map[string]any{"command": cmd})
		if err != nil || !strings.HasPrefix(out, "0|cat|cat|noninteractive|unset") {
			t.Fatalf("out=%q err=%v", out, err)
		}
	}
}

func TestBashJobsLastOutputAgeTracksSilence(t *testing.T) {
	ctx, _ := jobCtx(t, BashJobOptions{YieldTimeout: 20 * time.Millisecond})
	out, _ := ExecuteBash(ctx, map[string]any{"command": "echo hi; sleep 30"})
	st := decodeJobStatus(t, out)
	time.Sleep(300 * time.Millisecond)
	out, _ = BashWait(ctx, map[string]any{"job_id": st.JobID, "wait_seconds": float64(0)})
	later := decodeJobStatus(t, out)
	if later.LastOutputAgeSeconds < 0.2 || later.LastOutputAgeSeconds > later.ElapsedSeconds {
		t.Fatalf("age=%v elapsed=%v", later.LastOutputAgeSeconds, later.ElapsedSeconds)
	}
}

func TestBashJobsDoneSnapshotAndResultFromContextDoNotConsumeJob(t *testing.T) {
	ctx, mgr := jobCtx(t, BashJobOptions{YieldTimeout: 20 * time.Millisecond})
	ctx, _ = WithBashResultCapture(ctx)
	out, _ := ExecuteBash(ctx, map[string]any{"command": "sleep 0.2; echo end"})
	st := decodeJobStatus(t, out)
	if got := BashResultFromContext(ctx); got == nil || got.JobID != st.JobID {
		t.Fatalf("result = %+v", got)
	}
	if mgr.Done("nope") != nil {
		t.Fatal("unknown job should have nil Done")
	}
	select {
	case <-mgr.Done(st.JobID):
	case <-time.After(3 * time.Second):
		t.Fatal("Done not closed")
	}
	snap, ok := mgr.Snapshot(st.JobID)
	if !ok || snap.Status != "completed" || len(mgr.Pending()) != 1 {
		t.Fatalf("snap=%+v ok=%v pending=%d", snap, ok, len(mgr.Pending()))
	}
}

func TestBashJobsInheritToolContextCancellation(t *testing.T) {
	base, mgr := jobCtx(t, BashJobOptions{YieldTimeout: 5 * time.Second})
	toolCtx, cancel := context.WithCancel(base)
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	if _, err := ExecuteBash(toolCtx, map[string]any{"command": "sleep 30"}); err == nil {
		t.Fatal("expected cancellation error")
	}
	if len(mgr.Pending()) != 0 {
		t.Fatalf("pending = %+v", mgr.Pending())
	}
}

func TestBashJobsCommandInStatusIsNotTruncated(t *testing.T) {
	ctx, mgr := jobCtx(t, BashJobOptions{YieldTimeout: 20 * time.Millisecond})
	command := "sleep 5 #" + strings.Repeat("x", 3000) + " > /tmp/tail-redirect"
	out, _ := ExecuteBash(ctx, map[string]any{"command": command})
	if st := decodeJobStatus(t, out); st.Command != command {
		t.Fatalf("status command truncated (%d)", len(st.Command))
	}
	if p := mgr.Pending(); len(p) != 1 || p[0].Command != command {
		t.Fatal("pending command truncated")
	}
}

func TestBashJobsShortCommandsAreNotRetainedByManager(t *testing.T) {
	ctx, mgr := jobCtx(t, BashJobOptions{YieldTimeout: 5 * time.Second})
	for i := 0; i < 5; i++ {
		if _, err := ExecuteBash(ctx, map[string]any{"command": "echo hi"}); err != nil {
			t.Fatal(err)
		}
	}
	mgr.mu.Lock()
	defer mgr.mu.Unlock()
	if len(mgr.jobs) != 0 || len(mgr.order) != 0 {
		t.Fatalf("retained %d jobs", len(mgr.jobs))
	}
}

func TestBashJobsObservedLongJobsAreBoundedInCountAndOutput(t *testing.T) {
	ctx, mgr := jobCtx(t, BashJobOptions{YieldTimeout: 1 * time.Millisecond, MaxConcurrent: 1})
	var firstID string
	for i := 0; i < bashRetainedObservedJobs+5; i++ {
		out, err := ExecuteBash(ctx, map[string]any{"command": "head -c 300000 /dev/zero | tr '\\0' x; sleep 0.05"})
		if err != nil {
			t.Fatal(err)
		}
		st := decodeJobStatus(t, out)
		if firstID == "" {
			firstID = st.JobID
		}
		out, err = BashWait(ctx, map[string]any{"job_id": st.JobID, "wait_seconds": float64(5)})
		if err != nil {
			t.Fatal(err)
		}
		if f := decodeJobStatus(t, out); f.Status != "completed" || f.OutputBytes != 300000 {
			t.Fatalf("final status %s bytes %d", f.Status, f.OutputBytes)
		}
		out, err = BashWait(ctx, map[string]any{"job_id": st.JobID, "wait_seconds": float64(0)})
		if err != nil {
			continue // already evicted
		}
		if f := decodeJobStatus(t, out); len(f.Output) > bashObservedOutputMaxBytes+100 || !f.Truncated {
			t.Fatalf("retained output len %d truncated=%v", len(f.Output), f.Truncated)
		}
	}
	mgr.mu.Lock()
	n := len(mgr.jobs)
	mgr.mu.Unlock()
	if n > bashRetainedObservedJobs {
		t.Fatalf("retained %d jobs", n)
	}
	if _, err := BashWait(ctx, map[string]any{"job_id": firstID, "wait_seconds": float64(0)}); err == nil {
		t.Fatal("oldest observed job should have been evicted")
	}
}

func TestHeadTailBufferNeverRetainsMoreThanLimit(t *testing.T) {
	buf := newHeadTailBuffer(100)
	var want int64
	for i := 0; i < 500; i++ {
		chunk := []byte(strings.Repeat(string(rune('a'+i%26)), 7+i%13))
		want += int64(len(chunk))
		buf.Write(chunk)
		if held := len(buf.head) + len(buf.tail); held > 100 {
			t.Fatalf("held %d bytes", held)
		}
	}
	text, truncated := buf.view()
	if !truncated || buf.Total() != want {
		t.Fatalf("truncated=%v total=%d want=%d", truncated, buf.Total(), want)
	}
	if !strings.HasPrefix(text, "aaaaaaa") {
		t.Fatalf("head lost: %q", text)
	}
	// Tail must be the genuine end of the stream.
	var stream strings.Builder
	for i := 0; i < 500; i++ {
		stream.WriteString(strings.Repeat(string(rune('a'+i%26)), 7+i%13))
	}
	if !strings.HasSuffix(text, stream.String()[stream.Len()-50:]) {
		t.Fatalf("tail wrong: %q", text)
	}
}

func TestBashJobsRunInNewSessionWithoutControllingTTY(t *testing.T) {
	ctx, _ := jobCtx(t, BashJobOptions{YieldTimeout: 5 * time.Second})
	out, err := ExecuteBash(ctx, map[string]any{"command": "echo $$; ps -o pgid= -p $$; (exec 3</dev/tty) 2>/dev/null && echo tty || echo notty"})
	fields := strings.Fields(out)
	if err != nil || len(fields) != 3 || fields[0] != fields[1] || fields[2] != "notty" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestBashJobsToolContextDeadlineIsReportedInError(t *testing.T) {
	base, mgr := jobCtx(t, BashJobOptions{YieldTimeout: 20 * time.Millisecond})
	toolCtx, cancel := context.WithTimeout(base, 150*time.Millisecond)
	defer cancel()
	out, err := ExecuteBash(toolCtx, map[string]any{"command": "sleep 30"})
	if err != nil {
		t.Fatalf("yield should succeed, err=%v", err)
	}
	st := decodeJobStatus(t, out)
	done, snap := mgr.Completion(st.JobID)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("job not reaped after deadline")
	}
	if final := snap(); final.Status != "killed" || final.Error != "context deadline exceeded" {
		t.Fatalf("final = %+v", final)
	}
}

func TestBashJobsRunningSnapshotCarriesOnlySmallTail(t *testing.T) {
	ctx, _ := jobCtx(t, BashJobOptions{YieldTimeout: 300 * time.Millisecond})
	out, _ := ExecuteBash(ctx, map[string]any{"command": "head -c 100000 /dev/zero | tr '\\0' x; printf END; sleep 30"})
	st := decodeJobStatus(t, out)
	if st.Status != "running" || len(st.Output) > bashRunningOutputMaxBytes || !st.Truncated || !strings.HasSuffix(st.Output, "END") {
		t.Fatalf("status=%s len=%d truncated=%v", st.Status, len(st.Output), st.Truncated)
	}
	if st.OutputBytes != 100003 || len(st.NewOutput) > bashNewOutputMaxBytes+100 {
		t.Fatalf("bytes=%d new=%d", st.OutputBytes, len(st.NewOutput))
	}
}

func TestBashJobsCompletionSurvivesEvictionAndUnknownIsNil(t *testing.T) {
	ctx, mgr := jobCtx(t, BashJobOptions{YieldTimeout: 1 * time.Millisecond})
	if d, f := mgr.Completion("nope"); d != nil || f != nil {
		t.Fatal("unknown job should return nil, nil")
	}
	out, _ := ExecuteBash(ctx, map[string]any{"command": "sleep 0.1; echo fin"})
	st := decodeJobStatus(t, out)
	done, snap := mgr.Completion(st.JobID)
	<-done
	mgr.observeFinal(mgr.jobs[st.JobID])
	mgr.mu.Lock()
	delete(mgr.jobs, st.JobID)
	mgr.mu.Unlock()
	if final := snap(); final.Status != "completed" || !strings.Contains(final.Output, "fin") {
		t.Fatalf("final = %+v", final)
	}
}
