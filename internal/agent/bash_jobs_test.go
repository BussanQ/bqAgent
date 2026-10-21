//go:build !windows

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bqagent/internal/tools"
)

func bashTestCall(id, name string, args map[string]any) ToolCall {
	encoded, _ := json.Marshal(args)
	return ToolCall{ID: id, Type: "function", Function: FunctionCall{Name: name, Arguments: string(encoded)}}
}

func TestBashJobLedgerWaitsForFinalResult(t *testing.T) {
	root := t.TempDir()
	// The local shim provides deterministic execution of a verifier-shaped
	// command without running a build or unrelated test suite.
	if err := os.WriteFile(filepath.Join(root, "go"), []byte("#!/bin/sh\nsleep 0.1\nprintf verified\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, jobs := tools.WithBashJobs(context.Background(), tools.BashJobOptions{YieldTimeout: time.Millisecond})
	defer jobs.Close()
	state := &runState{}
	ctx = context.WithValue(ctx, runStateKey{}, state)
	app := NewWithOptions(&stubClient{}, "", Options{})
	_, result := app.runRegularToolCall(ctx, bashTestCall("verify", "bash", map[string]any{"command": "go test -run TestTarget"}))
	status, ok := bashWaitStatus(result)
	if !ok || status.Status != "running" {
		t.Fatalf("did not yield: %s", result)
	}
	if state.ledger.verified("verify", "go test -run TestTarget") {
		t.Fatal("running command counted as verification")
	}
	_, result = app.runRegularToolCall(ctx, bashTestCall("wait", "bash_wait", map[string]any{"job_id": status.JobID, "wait_seconds": 2}))
	if strings.HasPrefix(result, "Error:") || !state.ledger.verified("verify", "go test -run TestTarget") {
		t.Fatalf("completed verification not linked to original call: %s", result)
	}
	if len(jobs.Pending()) != 0 {
		t.Fatal("completed job still pending")
	}
}

func TestBashJobMutationBlocksDependentToolsUntilObserved(t *testing.T) {
	ctx, jobs := tools.WithBashJobs(context.Background(), tools.BashJobOptions{YieldTimeout: time.Millisecond})
	defer jobs.Close()
	state := &runState{}
	ctx = context.WithValue(ctx, runStateKey{}, state)
	reads := 0
	app := NewWithOptions(&stubClient{}, "", Options{Functions: map[string]tools.Function{
		"bash":      tools.ExecuteBash,
		"bash_kill": tools.BashKill,
		"read_file": func(context.Context, map[string]any) (string, error) { reads++; return "read", nil },
	}})
	_, output := app.runRegularToolCall(ctx, bashTestCall("start", "bash", map[string]any{"command": "sleep 60"}))
	status, ok := bashWaitStatus(output)
	if !ok {
		t.Fatalf("missing job: %s", output)
	}
	read := bashTestCall("read", "read_file", map[string]any{"path": "file"})
	_, output = app.runRegularToolCall(ctx, read)
	if reads != 0 || !strings.Contains(output, "bash_wait or bash_kill") {
		t.Fatalf("read overlapped mutation: reads=%d output=%s", reads, output)
	}
	_, output = app.runRegularToolCall(ctx, bashTestCall("kill", "bash_kill", map[string]any{"job_id": status.JobID}))
	if strings.HasPrefix(output, "Error:") {
		t.Fatal(output)
	}
	app.runRegularToolCall(ctx, read)
	if reads != 1 {
		t.Fatal("read remained blocked after kill")
	}
	if entry := state.ledger.entries[0]; entry.end == 0 || entry.success || !entry.mutating {
		t.Fatalf("killed mutation lost its ledger semantics: %#v", entry)
	}
}

type bashTurnClient struct {
	step int
	run  func(context.Context, []map[string]any, int) (AssistantMessage, error)
}

func (c *bashTurnClient) CreateChatCompletion(ctx context.Context, _ string, messages []map[string]any, _ []tools.Definition) (AssistantMessage, error) {
	c.step++
	return c.run(ctx, messages, c.step)
}
func (c *bashTurnClient) CreateChatCompletionStream(ctx context.Context, model string, messages []map[string]any, definitions []tools.Definition, _ func(string)) (AssistantMessage, error) {
	return c.CreateChatCompletion(ctx, model, messages, definitions)
}

func TestBashJobTurnDefersFinalAnswerUntilJobResolved(t *testing.T) {
	var jobID string
	client := &bashTurnClient{run: func(_ context.Context, messages []map[string]any, step int) (AssistantMessage, error) {
		switch step {
		case 1:
			return AssistantMessage{ToolCalls: []ToolCall{bashTestCall("start", "bash", map[string]any{"command": "sleep 60"})}}, nil
		case 2:
			results := extractToolMessages(messages)
			status, ok := bashWaitStatus(fmt.Sprint(results[0]["content"]))
			if !ok {
				t.Fatal("missing running result")
			}
			jobID = status.JobID
			return AssistantMessage{Content: "premature final"}, nil
		case 3:
			found := false
			for _, message := range messages {
				text := fmt.Sprint(message["content"])
				if strings.Contains(text, "Bash jobs still need their final results") && strings.Contains(text, jobID) {
					found = true
				}
			}
			if !found {
				t.Fatalf("missing pending job continuation: %#v", messages)
			}
			return AssistantMessage{ToolCalls: []ToolCall{bashTestCall("kill", "bash_kill", map[string]any{"job_id": jobID})}}, nil
		default:
			return AssistantMessage{Content: "stopped explicitly"}, nil
		}
	}}
	app := NewWithOptions(client, "", Options{Context: ContextConfig{Enabled: false}, BashJobs: tools.BashJobOptions{YieldTimeout: time.Millisecond}})
	result, err := app.RunTurn(context.Background(), []map[string]any{{"role": "user", "content": "run"}}, 5)
	if err != nil || result.Reply != "stopped explicitly" || client.step != 4 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if len(extractToolMessages(result.Messages)) != 2 {
		t.Fatal("tool results missing from history")
	}
}

func TestBashJobTurnCleansUpOnErrorCancellationAndBudget(t *testing.T) {
	for _, mode := range []string{"model_error", "cancel", "budget"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var jobs *tools.BashJobs
			client := &bashTurnClient{run: func(ctx context.Context, _ []map[string]any, step int) (AssistantMessage, error) {
				jobs = tools.BashJobsFromContext(ctx)
				if step == 1 {
					return AssistantMessage{ToolCalls: []ToolCall{bashTestCall("start", "bash", map[string]any{"command": "sleep 60"})}}, nil
				}
				if mode == "cancel" {
					cancel()
					return AssistantMessage{}, context.Canceled
				}
				return AssistantMessage{}, errors.New("model failed")
			}}
			app := NewWithOptions(client, "", Options{Context: ContextConfig{Enabled: false}, BashJobs: tools.BashJobOptions{YieldTimeout: time.Millisecond}})
			limit := 3
			if mode == "budget" {
				limit = 1
			}
			result, _ := app.RunTurn(ctx, []map[string]any{{"role": "user", "content": "run"}}, limit)
			if jobs == nil || len(jobs.Pending()) != 0 {
				t.Fatal("turn left a pending job")
			}
			var cleanup string
			for _, message := range result.Messages {
				if strings.Contains(fmt.Sprint(message["content"]), "Bash job cleanup at turn end") {
					cleanup = fmt.Sprint(message["content"])
				}
			}
			if !strings.Contains(cleanup, `"status":"killed"`) {
				t.Fatalf("missing cleanup outcome: %s", cleanup)
			}
		})
	}
}

func TestBashWaitLoopGuardUsesOutputNotElapsedTime(t *testing.T) {
	guard := newLoopGuard(normalizeStageConfig(StageConfig{LoopProtection: true}))
	call := bashTestCall("wait", "bash_wait", map[string]any{"job_id": "job", "wait_seconds": 0})
	for i := 0; i < 8; i++ {
		encoded, _ := json.Marshal(tools.BashJobStatus{JobID: "job", Status: "running", ElapsedSeconds: float64(i), OutputBytes: 0})
		action := guard.observeTool(call, string(encoded))
		if action.checkpointReason != "" {
			t.Fatal("silence automatically stopped a long command")
		}
		if i >= 2 && action.recoveryPrompt == "" {
			t.Fatal("silent polling did not produce guidance")
		}
	}
	for i := 1; i < 8; i++ {
		encoded, _ := json.Marshal(tools.BashJobStatus{JobID: "job", Status: "running", OutputBytes: int64(i)})
		action := guard.observeTool(call, string(encoded))
		if action.checkpointReason != "" || action.recoveryPrompt != "" {
			t.Fatalf("new output treated as stagnation: %#v", action)
		}
	}
}
