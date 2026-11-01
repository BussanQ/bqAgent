package agent

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestSummaryProgressRetainsCompactionLabel(t *testing.T) {
	ticker, clock := installModelProgressTestHooks(t)
	client := newBlockingModelClient(AssistantMessage{Content: "summary"}, nil)
	progress := &stallTestBuffer{}
	app := NewWithOptions(client, "test", Options{Context: DefaultContextConfig(), ProgressWriter: progress})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := app.generateSummary(ctx, []map[string]any{{"role": "user", "content": "remember the task"}})
		done <- err
	}()
	select {
	case <-client.started:
	case <-time.After(time.Second):
		t.Fatal("summary request did not start")
	}
	clock.Advance(30 * time.Second)
	fireModelProgressTick(t, ticker, clock)
	waitForStallBufferContains(t, progress, "正在压缩上下文…")
	close(client.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := progress.String(); got != "正在压缩上下文…\n" {
		t.Fatalf("summary progress=%q", got)
	}

	// The per-request label must not leak into subsequent ordinary requests.
	reporter := startModelProgressReporter(context.Background(), progress, false)
	reporter.emit()
	reporter.stop()
	if !strings.Contains(progress.String(), "Still working... test status.") {
		t.Fatalf("normal request lost its progress: %q", progress.String())
	}
}
