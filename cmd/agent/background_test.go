package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunWithBackgroundCreatesSessionAndLaunchesChild(t *testing.T) {
	root := t.TempDir()
	var launched struct {
		executable string
		args       []string
		dir        string
		outputPath string
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runWithDeps(context.Background(), nil, &stdout, &stderr, []string{"--background", "summarize", "README"}, func(string) string { return "" }, runDeps{
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

	entries, err := os.ReadDir(filepath.Join(root, ".agent", "sessions"))
	if err != nil {
		t.Fatalf("failed to read sessions dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("sessions dir contains %d entries, want 1", len(entries))
	}
	if launched.outputPath != filepath.Join(root, ".agent", "sessions", entries[0].Name(), "output.log") {
		t.Fatalf("output path = %q, want session output path", launched.outputPath)
	}
}

func TestRunWithResumeRequiresTask(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".agent", "sessions", "demo"), 0o755); err != nil {
		t.Fatalf("failed to create session dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".agent", "sessions", "demo", "meta.json"), []byte(`{"id":"demo","workspace_root":"`+strings.ReplaceAll(root, `\\`, `\\\\`)+`","status":"created","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`), 0o644); err != nil {
		t.Fatalf("failed to write meta.json: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runWithDeps(context.Background(), nil, &stdout, &stderr, []string{"--resume", "demo"}, func(string) string { return "" }, runDeps{
		getwd:           func() (string, error) { return root, nil },
		executable:      func() (string, error) { return "bqagent-test", nil },
		startBackground: func(string, []string, string, string) error { return nil },
	})
	if code != 1 {
		t.Fatalf("runWithDeps returned code %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "resume requires a follow-up task") {
		t.Fatalf("stderr = %q, want resume validation error", stderr.String())
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
