//go:build !windows

package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bqagent/internal/tools"
	apptrace "bqagent/internal/trace"
)

func TestBashExecutionStatusAcrossLogEventAndTrace(t *testing.T) {
	for _, outcome := range []string{"completed", "failed", "killed"} {
		t.Run(outcome, func(t *testing.T) {
			root := t.TempDir()
			recorder, err := apptrace.NewStore(root).Create("session", "turn", "", "test", "", "")
			if err != nil {
				t.Fatal(err)
			}
			var log bytes.Buffer
			sink := &recordingToolEventSink{}
			ctx, jobs := tools.WithBashJobs(context.Background(), tools.BashJobOptions{YieldTimeout: time.Millisecond})
			defer jobs.Close()
			app := NewWithOptions(&stubClient{}, "", Options{LogWriter: &log, ToolEventSink: sink, Trace: recorder, Functions: map[string]tools.Function{"bash": tools.ExecuteBashInDir(root), "bash_wait": tools.BashWait, "bash_kill": tools.BashKill}})
			_, result := app.runRegularToolCall(ctx, bashTestCall("start", "bash", map[string]any{"command": "while [ ! -f release ]; do sleep 0.01; done; read code < release; exit \"$code\""}))
			job, ok := bashWaitStatus(result)
			if !ok {
				t.Fatalf("not running: %s", result)
			}
			running := sink.events[len(sink.events)-1]
			if running.Status != "running" || running.CallStatus != "success" || running.ExecutionStatus != "running" || running.JobID != job.JobID || running.ExitCode != nil || running.ResultDelivered == nil || *running.ResultDelivered {
				t.Fatalf("running event: %+v", running)
			}
			if !strings.Contains(log.String(), "status=running call_status=success execution_status=running job_id="+job.JobID) || !strings.Contains(log.String(), "result_delivered=false") {
				t.Fatal(log.String())
			}
			name := "bash_wait"
			if outcome == "killed" {
				name = "bash_kill"
			} else {
				code := "0\n"
				if outcome == "failed" {
					code = "7\n"
				}
				if err := os.WriteFile(filepath.Join(root, "release"), []byte(code), 0600); err != nil {
					t.Fatal(err)
				}
			}
			app.runRegularToolCall(ctx, bashTestCall("finish", name, map[string]any{"job_id": job.JobID, "wait_seconds": 2}))
			final := sink.events[len(sink.events)-1]
			wantStatus, wantCall := outcome, "success"
			if outcome == "completed" {
				wantStatus = "succeeded"
			}
			if outcome == "failed" {
				wantCall = "error"
			}
			if final.Status != wantStatus || final.CallStatus != wantCall || final.ExecutionStatus != outcome || final.JobID != job.JobID || final.ResultDelivered == nil || !*final.ResultDelivered {
				t.Fatalf("final event: %+v", final)
			}
			if outcome != "killed" && (final.ExitCode == nil || (outcome == "failed" && *final.ExitCode != 7) || (outcome == "completed" && *final.ExitCode != 0)) {
				t.Fatalf("exit code: %+v", final)
			}
			if !strings.Contains(log.String(), "status="+outcome+" call_status="+wantCall) || !strings.Contains(log.String(), "result_delivered=true") {
				t.Fatal(log.String())
			}
			data, err := os.ReadFile(filepath.Join(root, ".agent", "runs", recorder.RunID(), "events.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			var states []map[string]any
			for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
				var e struct {
					Type string         `json:"type"`
					Data map[string]any `json:"data"`
				}
				if err := json.Unmarshal([]byte(line), &e); err != nil {
					t.Fatal(err)
				}
				if e.Type == "tool_call" {
					states = append(states, e.Data)
				}
			}
			if len(states) != 2 || states[0]["execution_status"] != "running" || states[0]["result_delivered"] != false || states[1]["execution_status"] != outcome || states[1]["call_status"] != wantCall || states[1]["result_delivered"] != true {
				t.Fatalf("trace states: %+v", states)
			}
		})
	}
}

func TestBashWaitLongFailureKeepsJobState(t *testing.T) {
	root := t.TempDir()
	ctx, jobs := tools.WithBashJobs(context.Background(), tools.BashJobOptions{YieldTimeout: time.Millisecond})
	defer jobs.Close()
	app := NewWithOptions(&stubClient{}, "", Options{Functions: map[string]tools.Function{"bash": tools.ExecuteBashInDir(root), "bash_wait": tools.BashWait}})
	_, result := app.runRegularToolCall(ctx, bashTestCall("start", "bash", map[string]any{"command": "sleep 0.05; head -c 40000 /dev/zero | tr '\\0' x; printf TAIL; exit 3"}))
	job, ok := bashWaitStatus(result)
	if !ok || job.Status != "running" {
		t.Fatalf("not running: %s", result)
	}
	_, result = app.runRegularToolCall(ctx, bashTestCall("wait", "bash_wait", map[string]any{"job_id": job.JobID, "wait_seconds": 5}))
	if !strings.HasPrefix(result, "Error:") || strings.Contains(result, "\n... [truncated]\n{") {
		t.Fatalf("unexpected result head: %.200s", result)
	}
	tool := HistoryToolFromCall(bashTestCall("wait", "bash_wait", map[string]any{"job_id": job.JobID}), result)
	if tool.JobID != job.JobID || tool.ExecutionStatus != "failed" || tool.Status != "failed" || tool.ExitCode == nil || *tool.ExitCode != 3 {
		t.Fatalf("job state lost: %+v", tool.ToolExecution)
	}
	_, output, _ := strings.Cut(result, "Output before failure:\n")
	final, _ := bashWaitStatus(output)
	if len(output) > maxToolErrorOutputChars || !final.Truncated || !strings.HasSuffix(final.Output, "TAIL") {
		t.Fatalf("output not bounded by tail: len=%d truncated=%v", len(output), final.Truncated)
	}
	start := HistoryToolFromCall(bashTestCall("start", "bash", map[string]any{}), `{"job_id":"`+job.JobID+`","status":"running"}`)
	start.ResolveBashJob(tool)
	if start.Status != "failed" || start.ExecutionStatus != "failed" {
		t.Fatalf("original job not resolved: %+v", start)
	}
}

func TestHistoryBashExecutionStatus(t *testing.T) {
	for _, name := range []string{"bash", "bash_wait", "bash_kill", "read_file"} {
		for _, status := range []string{"running", "completed", "failed", "killed"} {
			t.Run(name+"/"+status, func(t *testing.T) {
				result := `{"job_id":"job","status":"` + status + `","exit_code":0}`
				tool := HistoryToolFromCall(ToolCall{Function: FunctionCall{Name: name, Arguments: `{}`}}, result)
				want := status
				if name == "read_file" || status == "completed" {
					want = "succeeded"
				}
				if tool.Status != want {
					t.Fatalf("tool=%+v", tool)
				}
				if name == "read_file" && tool.JobID != "" {
					t.Fatal("file JSON mistaken for job")
				}
			})
		}
	}
	for _, status := range []string{"failed", "killed"} {
		t.Run("error_prefix/"+status, func(t *testing.T) {
			result := "Error: context canceled\n\nOutput before failure:\n" + `{"job_id":"job","status":"` + status + `"}`
			tool := HistoryToolFromCall(ToolCall{Function: FunctionCall{Name: "bash_kill", Arguments: `{}`}}, result)
			if tool.Status != status || tool.CallStatus != "error" || tool.ExecutionStatus != status || tool.JobID != "job" {
				t.Fatalf("tool=%+v", tool)
			}
		})
	}
}
