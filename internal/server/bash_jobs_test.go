//go:build !windows

package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bqagent/internal/tools"
)

func TestServerBashYieldPreservesProcessAndStopRegistration(t *testing.T) {
	service := NewService(ServiceOptions{WorkspaceRoot: t.TempDir(), Functions: map[string]tools.Function{"execute_bash": tools.ExecuteBash}})
	ctx, jobs := tools.WithBashJobs(context.Background(), tools.BashJobOptions{YieldTimeout: time.Millisecond})
	defer jobs.Close()
	ctx, _ = tools.WithBashResultCapture(ctx)
	wrapped := service.functionsForTurn("peer", "session")["execute_bash"]
	_, err := wrapped(ctx, map[string]any{"command": "sleep 60"})
	if err != nil {
		t.Fatal(err)
	}
	result := tools.BashResultFromContext(ctx)
	if result == nil || result.Status != "running" {
		t.Fatalf("missing running result: %#v", result)
	}
	// A short wait must leave the job running after the wrapper has returned.
	_, err = tools.BashWait(ctx, map[string]any{"job_id": result.JobID, "wait_seconds": 0})
	if err != nil {
		t.Fatal(err)
	}
	if status, _ := jobs.Snapshot(result.JobID); status.Status != "running" {
		t.Fatalf("wrapper canceled yielded job: %#v", status)
	}
	if service.stopProcessGroup("peer", "session") != 1 {
		t.Fatal("yielded process lost stop registration")
	}
	select {
	case <-jobs.Done(result.JobID):
	case <-time.After(3 * time.Second):
		t.Fatal("stop failed to end background process")
	}
	status, _ := jobs.Snapshot(result.JobID)
	if status.Status != "killed" {
		t.Fatalf("stop status: %#v", status)
	}
}

func TestServerBashYieldPreservesEnvironmentTimeout(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "npm"), []byte("#!/bin/sh\nsleep 60\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	service := NewService(ServiceOptions{WorkspaceRoot: root, Functions: map[string]tools.Function{"execute_bash": tools.ExecuteBash}})
	service.environmentCommands = newEnvironmentCommandGuard(50 * time.Millisecond)
	ctx, jobs := tools.WithBashJobs(context.Background(), tools.BashJobOptions{YieldTimeout: time.Millisecond})
	defer jobs.Close()
	ctx, _ = tools.WithBashResultCapture(ctx)
	_, err := service.functionsForTurn("peer", "session")["execute_bash"](ctx, map[string]any{"command": "npm install"})
	if err != nil {
		t.Fatal(err)
	}
	result := tools.BashResultFromContext(ctx)
	if result == nil || result.Status != "running" {
		t.Fatalf("did not yield: %#v", result)
	}
	select {
	case <-jobs.Done(result.JobID):
	case <-time.After(3 * time.Second):
		t.Fatal("environment deadline lost after yield")
	}
	status, _ := jobs.Snapshot(result.JobID)
	if status.Status != "killed" || !strings.Contains(status.Error, "deadline") {
		t.Fatalf("timeout reason lost: %#v", status)
	}
	deadline := time.After(time.Second)
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if message, blocked := service.environmentCommands.BlockedMessage(stopKeys("peer", "session"), "npm install"); blocked {
			if !strings.Contains(message, "deadline") {
				t.Fatalf("guard lost deadline: %s", message)
			}
			break
		}
		select {
		case <-deadline:
			t.Fatal("background failure not recorded")
		case <-ticker.C:
		}
	}
}
