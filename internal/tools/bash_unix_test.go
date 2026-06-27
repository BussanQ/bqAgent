//go:build !windows

package tools

import (
	"context"
	"testing"
	"time"
)

// TestExecuteBashUnblocksWhenGrandchildHoldsPipe reproduces the production hang:
// the shell exits immediately but a backgrounded grandchild inherits the stdout
// pipe and keeps it open far longer than the turn deadline. ExecuteBash must
// still return promptly (via process-group kill / WaitDelay) instead of blocking
// on the pipe copy until the grandchild exits.
func TestExecuteBashUnblocksWhenGrandchildHoldsPipe(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	start := time.Now()
	go func() {
		_, err := ExecuteBash(ctx, map[string]any{"command": "sleep 60 & echo started"})
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected cancellation/timeout error")
		}
		if elapsed := time.Since(start); elapsed > commandWaitDelay+3*time.Second {
			t.Fatalf("ExecuteBash blocked too long: %s", elapsed)
		}
	case <-time.After(commandWaitDelay + 5*time.Second):
		t.Fatal("ExecuteBash did not return; grandchild wedged the pipe")
	}
}
