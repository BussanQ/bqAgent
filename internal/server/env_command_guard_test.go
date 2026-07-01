package server

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"bqagent/internal/tools"
)

func TestEnvironmentCommandGuardBlocksRepeatedRepairCommand(t *testing.T) {
	var calls atomic.Int32
	service := NewService(ServiceOptions{
		WorkspaceRoot: t.TempDir(),
		Functions: map[string]tools.Function{
			"execute_bash": func(context.Context, map[string]any) (string, error) {
				calls.Add(1)
				return "download stalled", context.DeadlineExceeded
			},
		},
	})
	wrapped := service.functionsForTurn("peer-1", "session-1")["execute_bash"]
	args := map[string]any{"command": "rustup toolchain install stable"}

	output, err := wrapped(context.Background(), args)
	if err == nil {
		t.Fatal("first call returned nil error, want timeout")
	}
	if output != "download stalled" {
		t.Fatalf("first output = %q, want original output", output)
	}
	output, err = wrapped(context.Background(), args)
	if err == nil {
		t.Fatal("second call returned nil error, want blocked error")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("execute_bash calls = %d, want 1", got)
	}
	if !strings.Contains(output, "already failed or timed out") || !strings.Contains(output, "Do not retry") || !strings.Contains(output, "Fall back") {
		t.Fatalf("blocked output = %q, want fallback guidance", output)
	}
}

func TestEnvironmentCommandGuardAllowsRepeatedNormalCommands(t *testing.T) {
	var calls atomic.Int32
	service := NewService(ServiceOptions{
		WorkspaceRoot: t.TempDir(),
		Functions: map[string]tools.Function{
			"execute_bash": func(context.Context, map[string]any) (string, error) {
				calls.Add(1)
				return "failed build", context.DeadlineExceeded
			},
		},
	})
	wrapped := service.functionsForTurn("peer-1", "session-1")["execute_bash"]
	args := map[string]any{"command": "cargo build"}
	_, _ = wrapped(context.Background(), args)
	_, _ = wrapped(context.Background(), args)
	if got := calls.Load(); got != 2 {
		t.Fatalf("execute_bash calls = %d, want 2", got)
	}
}

func TestClassifyEnvironmentCommand(t *testing.T) {
	tests := []struct {
		command string
		want    bool
	}{
		{"rustup install stable", true},
		{"rustup update stable", true},
		{"rustup toolchain install stable", true},
		{"rustup toolchain repair stable", true},
		{"npm install", true},
		{"python -m pip install -r requirements.txt", true},
		{"cargo build", false},
		{"cargo test", false},
		{"go test ./...", false},
		{"npm test", false},
		{"rustc --version", false},
		{"which rustc cargo", false},
	}
	for _, testCase := range tests {
		_, got := classifyEnvironmentCommand(testCase.command)
		if got != testCase.want {
			t.Fatalf("classifyEnvironmentCommand(%q) = %v, want %v", testCase.command, got, testCase.want)
		}
	}
}
