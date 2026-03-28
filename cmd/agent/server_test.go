package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunWithServerBackgroundLaunchesChild(t *testing.T) {
	root := t.TempDir()
	var launched struct {
		executable string
		args       []string
		dir        string
		outputPath string
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runWithDeps(context.Background(), nil, &stdout, &stderr, []string{"--server", "--background", "--listen", "127.0.0.1:9090"}, func(string) string { return "" }, runDeps{
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
		t.Fatalf("runWithDeps returned code %d, want 0; stderr=%q", code, stderr.String())
	}
	if launched.executable != "bqagent-test" {
		t.Fatalf("launched executable = %q, want %q", launched.executable, "bqagent-test")
	}
	if launched.dir != root {
		t.Fatalf("launched dir = %q, want %q", launched.dir, root)
	}
	if !containsAll(launched.args, []string{"--server-run", "--listen", "127.0.0.1:9090"}) {
		t.Fatalf("launched args = %#v, want server-run args", launched.args)
	}
	wantOutputPath := filepath.Join(root, ".agent", "server", "server.log")
	if launched.outputPath != wantOutputPath {
		t.Fatalf("output path = %q, want %q", launched.outputPath, wantOutputPath)
	}
	if !strings.Contains(stdout.String(), "listen: 127.0.0.1:9090") {
		t.Fatalf("stdout = %q, want listen output", stdout.String())
	}
}

func TestServerCannotAcceptTask(t *testing.T) {
	_, _, err := parseCLI([]string{"--server", "hello"})
	if err == nil {
		t.Fatal("parseCLI returned nil error, want server/task conflict")
	}
	if !strings.Contains(err.Error(), "server mode does not accept a task") {
		t.Fatalf("error = %q, want server task validation", err.Error())
	}
}

func TestServerCannotCombineWithChat(t *testing.T) {
	_, _, err := parseCLI([]string{"--server", "--chat"})
	if err == nil {
		t.Fatal("parseCLI returned nil error, want server/chat conflict")
	}
	if !strings.Contains(err.Error(), "--server cannot be combined with --chat") {
		t.Fatalf("error = %q, want server/chat validation", err.Error())
	}
}

func TestServerListenDefaultsToLoopback(t *testing.T) {
	options, _, err := parseCLI([]string{"--server"})
	if err != nil {
		t.Fatalf("parseCLI returned error: %v", err)
	}
	if options.listen != "127.0.0.1:8080" {
		t.Fatalf("options.listen = %q, want %q", options.listen, "127.0.0.1:8080")
	}
}

func TestRunServerRequiresAPIKey(t *testing.T) {
	root := t.TempDir()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runWithDeps(context.Background(), nil, &stdout, &stderr, []string{"--server-run"}, func(string) string { return "" }, runDeps{
		getwd:           func() (string, error) { return root, nil },
		executable:      func() (string, error) { return "bqagent-test", nil },
		startBackground: func(string, []string, string, string) error { return nil },
	})
	if code != 1 {
		t.Fatalf("runWithDeps returned code %d, want 1", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), "OPENAI_API_KEY is required for server mode") {
		t.Fatalf("stderr = %q, want missing api key error", stderr.String())
	}
}

func TestEnvEnabledDefaultsToTrue(t *testing.T) {
	if !envEnabled("") {
		t.Fatal("envEnabled(\"\") = false, want true")
	}
}

func TestEnvEnabledSupportsExplicitDisable(t *testing.T) {
	for _, value := range []string{"0", "false", "off", "no"} {
		if envEnabled(value) {
			t.Fatalf("envEnabled(%q) = true, want false", value)
		}
	}
}

func TestEnvEnabledSupportsExplicitEnable(t *testing.T) {
	for _, value := range []string{"1", "true", "on", "yes"} {
		if !envEnabled(value) {
			t.Fatalf("envEnabled(%q) = false, want true", value)
		}
	}
}
