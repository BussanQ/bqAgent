//go:build !windows

package tools

import (
	"context"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestExecuteBashUnblocksWhenGrandchildHoldsPipe reproduces the production hang:
// the shell exits immediately but a backgrounded grandchild inherits the stdout
// pipe and keeps it open far longer than the turn deadline. ExecuteBash must
// still return promptly (via process-group kill / WaitDelay) instead of blocking
// on the pipe copy until the grandchild exits.
func TestExecuteBashUnblocksWhenGrandchildHoldsPipe(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	output, err := ExecuteBash(ctx, map[string]any{"command": "sleep 60 & echo $!; echo started"})
	if err != nil {
		t.Fatalf("ExecuteBash error: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("ExecuteBash blocked too long: %s", elapsed)
	}
	fields := strings.Fields(output)
	if len(fields) != 2 || fields[1] != "started" {
		t.Fatalf("output = %q", output)
	}
	pid, convErr := strconv.Atoi(fields[0])
	if convErr != nil {
		t.Fatalf("pid %q: %v", fields[0], convErr)
	}
	deadline := time.Now().Add(2 * time.Second)
	for syscall.Kill(pid, 0) == nil && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if syscall.Kill(pid, 0) == nil {
		t.Fatal("grandchild still alive after shell exit")
	}
}
