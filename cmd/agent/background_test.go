package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bqagent/internal/session"
)

func TestRunWithBackgroundCreatesSessionAndLaunchesChild(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	var launched struct {
		executable string
		args       []string
		dir        string
		outputPath string
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	getenv := func(key string) string {
		if key == "HOME" {
			return home
		}
		return ""
	}
	code := runWithDeps(context.Background(), nil, &stdout, &stderr, []string{"--background", "summarize", "README"}, getenv, runDeps{
		getwd:      func() (string, error) { return root, nil },
		executable: func() (string, error) { return "bqagent-test", nil },
		startBackground: func(executable string, args []string, dir, outputPath string) error {
			launched.executable = executable
			launched.args = append([]string(nil), args...)
			launched.dir = dir
			launched.outputPath = outputPath
			return nil
		},
	})
	if code != 0 {
		t.Fatalf("runWithDeps returned code %d, want 0", code)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	if launched.executable != "bqagent-test" {
		t.Fatalf("launched executable = %q, want %q", launched.executable, "bqagent-test")
	}
	if launched.dir != root {
		t.Fatalf("launched dir = %q, want %q", launched.dir, root)
	}
	if !containsAll(launched.args, []string{"--session-run", "--session-id", "summarize", "README"}) {
		t.Fatalf("launched args = %#v, want session-run args plus task", launched.args)
	}
	if !strings.Contains(stdout.String(), "session_id:") {
		t.Fatalf("stdout = %q, want session_id output", stdout.String())
	}

	sessionsDir := filepath.Join(home, ".agent", "sessions", session.WorkspaceDirectoryName(root))
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		t.Fatalf("failed to read sessions dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("sessions dir contains %d entries, want 1", len(entries))
	}
	if launched.outputPath != filepath.Join(sessionsDir, entries[0].Name(), "output.log") {
		t.Fatalf("output path = %q, want session output path", launched.outputPath)
	}
	if _, err := os.Stat(filepath.Join(root, ".agent", "sessions")); !os.IsNotExist(err) {
		t.Fatalf("background session created workspace-local sessions: %v", err)
	}
}

func TestRunWithResumeWithoutTaskRequiresTerminalBeforeSessionAccess(t *testing.T) {
	for _, args := range [][]string{{"--resume", "demo"}, {"--resume"}} {
		var stdout, stderr bytes.Buffer
		code := runWithDeps(context.Background(), unreadableStdin{t}, &stdout, &stderr, args, func(string) string { return "xterm" }, noSideEffectDeps(t))
		if code != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "TUI") || !strings.Contains(stderr.String(), "TTY") {
			t.Fatalf("args=%v exit=%d stdout=%q stderr=%q", args, code, stdout.String(), stderr.String())
		}
	}
}

func containsAll(values []string, expected []string) bool {
	for _, want := range expected {
		found := false
		for _, value := range values {
			if value == want {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
