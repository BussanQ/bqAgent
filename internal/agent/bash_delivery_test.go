//go:build !windows

package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"bqagent/internal/tools"
	apptrace "bqagent/internal/trace"
)

func TestBashJobDeliverySaveFailureRetainsResult(t *testing.T) {
	for _, action := range []string{"retry", "cleanup"} {
		t.Run(action, func(t *testing.T) {
			root := t.TempDir()
			ctx, jobs := tools.WithBashJobs(context.Background(), tools.BashJobOptions{YieldTimeout: time.Millisecond})
			defer jobs.Close()
			state := &runState{}
			ctx = context.WithValue(ctx, runStateKey{}, state)
			sink := &recordingToolEventSink{}
			var log bytes.Buffer
			app := NewWithOptions(&stubClient{}, "", Options{ToolEventSink: sink, LogWriter: &log, Functions: map[string]tools.Function{"bash": tools.ExecuteBashInDir(root)}})
			// More than the retained observed-job limit: failed saves must not shrink it.
			output := strings.Repeat("x", 80<<10) + "TAIL"
			if err := os.WriteFile(filepath.Join(root, "output"), []byte(output), 0600); err != nil {
				t.Fatal(err)
			}
			_, text := app.runRegularToolCall(ctx, bashTestCall("start", "bash", map[string]any{"command": "while [ ! -f release ]; do sleep 0.01; done; cat output"}))
			job, ok := bashWaitStatus(text)
			if !ok || job.Status != "running" {
				t.Fatalf("not running: %.200s", text)
			}
			if err := os.WriteFile(filepath.Join(root, "release"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			select {
			case <-jobs.Done(job.JobID):
			case <-time.After(5 * time.Second):
				t.Fatal("job did not finish")
			}
			before := jobs.Finished()
			if len(before) != 1 || before[0].Output != output {
				t.Fatal("missing full result")
			}
			eventCount := len(sink.events)
			app.recorder = &failingRecorder{}
			history, err := app.deliverFinishedBashJobs(ctx, nil)
			if err == nil || len(history) != 0 {
				t.Fatalf("history=%v err=%v", history, err)
			}
			pending := jobs.Pending()
			if len(pending) != 1 || pending[0].Output != output || pending[0].NewOutput != before[0].NewOutput {
				t.Fatal("failed save consumed result")
			}
			if len(sink.events) != eventCount || strings.Contains(log.String(), "delivery=auto") {
				t.Fatal("failed save published delivery")
			}
			if pendingBashConflict(ctx, "write_file", nil) == "" || state.bashEntries[job.JobID] == nil || state.bashEntries[job.JobID].end != 0 {
				t.Fatal("failed save released conflict or finished ledger")
			}
			recorder := &capturingRecorder{}
			app.recorder = recorder
			if action == "retry" {
				history, err = app.deliverFinishedBashJobs(ctx, history)
			} else {
				history, err = app.closeBashJobs(ctx, jobs, history)
			}
			if err != nil || len(history) != 1 || len(recorder.recorded) != 1 {
				t.Fatalf("history=%v err=%v", history, err)
			}
			results, delivered, ok := BashJobOutcomes(fmt.Sprint(history[0]["content"]))
			if !ok || len(results) != 1 || results[0].Output != output || delivered != (action == "retry") {
				t.Fatal("recovery lost final output or delivery state")
			}
			if len(jobs.Pending()) != 0 {
				t.Fatal("recovered job still pending")
			}
			if action == "retry" {
				history, err = app.deliverFinishedBashJobs(ctx, history)
				if err != nil || len(history) != 1 || len(recorder.recorded) != 1 {
					t.Fatal("duplicate delivery")
				}
			}
		})
	}
}

func TestBashErrorBoundsAllTextFields(t *testing.T) {
	for _, fields := range []string{"command", "error", "all"} {
		t.Run(fields, func(t *testing.T) {
			code := 3
			status := tools.BashJobStatus{JobID: "job", Status: "failed", ExitCode: &code, Output: "TAIL"}
			long := strings.Repeat("脚本\x00<&>\"", 6000)
			if fields == "command" || fields == "all" {
				status.Command = long
			}
			if fields == "error" || fields == "all" {
				status.Error = long
			}
			if fields == "all" {
				status.Output = long + "TAIL"
				status.NewOutput = long
			}
			data, _ := json.Marshal(status)
			text := formatToolError(fmt.Errorf("command failed"), boundedBashJobJSON(string(data)))
			_, body, ok := strings.Cut(text, "Output before failure:\n")
			final, parsed := bashWaitStatus(body)
			if !ok || !parsed || len(body) > maxToolErrorOutputChars || !utf8.ValidString(body) || !final.Truncated || final.JobID != "job" || final.ExitCode == nil || *final.ExitCode != 3 || !strings.HasSuffix(final.Output, "TAIL") {
				t.Fatalf("lost bounded state: bytes=%d parsed=%v", len(body), parsed)
			}
		})
	}
}

func TestHistoryBashOutcomeSkipsUnrelatedLargeOutput(t *testing.T) {
	result := tools.BashJobStatus{JobID: "job", Status: "completed", Output: strings.Repeat("x", 1<<20)}
	cards := make([]HistoryTool, 16)
	for i := range cards {
		cards[i].ToolExecution = apptrace.ToolExecution{JobID: fmt.Sprint("other-", i), ExecutionStatus: "running"}
	}
	allocations := testing.AllocsPerRun(3, func() {
		for i := range cards {
			cards[i].ApplyBashOutcome(result, true)
		}
	})
	if allocations != 0 {
		t.Fatalf("unrelated cards allocated %g times", allocations)
	}
	match := HistoryTool{ToolExecution: apptrace.ToolExecution{JobID: "job", ExecutionStatus: "running"}}
	match.ApplyBashOutcome(result, true)
	if match.Status != "succeeded" || !match.Truncated || match.ResultDelivered == nil || !*match.ResultDelivered {
		t.Fatal("matching card did not resolve")
	}
}
