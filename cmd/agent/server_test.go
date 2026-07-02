package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appserver "bqagent/internal/server"
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
	if !hasTimestampPrefix(stderr.String()) {
		t.Fatalf("stderr = %q, want timestamp prefix", stderr.String())
	}
}

func TestConfigureServerChannelLimitsReadsMaxIterations(t *testing.T) {
	previousMax := appserver.ChannelMaxIterations()
	previousTimeout := appserver.ChannelTurnTimeout()
	defer appserver.SetChannelMaxIterations(previousMax)
	defer appserver.SetChannelTurnTimeout(previousTimeout)

	var stderr bytes.Buffer
	configureServerChannelLimits(&stderr, func(key string) string {
		switch key {
		case "CHANNEL_AGENT_MAX_ITERATIONS":
			return "12"
		case "CHANNEL_TURN_TIMEOUT":
			return "3s"
		default:
			return ""
		}
	})
	if appserver.ChannelMaxIterations() != 12 {
		t.Fatalf("ChannelMaxIterations = %d, want 12", appserver.ChannelMaxIterations())
	}
	if appserver.ChannelTurnTimeout() != 3*time.Second {
		t.Fatalf("ChannelTurnTimeout = %s, want 3s", appserver.ChannelTurnTimeout())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestConfigureServerChannelLimitsWarnsOnInvalidMaxIterations(t *testing.T) {
	previousMax := appserver.ChannelMaxIterations()
	defer appserver.SetChannelMaxIterations(previousMax)
	appserver.SetChannelMaxIterations(9)

	var stderr bytes.Buffer
	configureServerChannelLimits(&stderr, func(key string) string {
		if key == "CHANNEL_AGENT_MAX_ITERATIONS" {
			return "bad"
		}
		return ""
	})
	if appserver.ChannelMaxIterations() != 9 {
		t.Fatalf("ChannelMaxIterations = %d, want unchanged 9", appserver.ChannelMaxIterations())
	}
	if !strings.Contains(stderr.String(), "invalid CHANNEL_AGENT_MAX_ITERATIONS") {
		t.Fatalf("stderr = %q, want invalid max iterations warning", stderr.String())
	}
}

func hasTimestampPrefix(line string) bool {
	if len(line) < len("2006-01-02 15:04:05 ") {
		return false
	}
	return line[4] == '-' && line[7] == '-' && line[10] == ' ' && line[13] == ':' && line[16] == ':' && line[19] == ' '
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
