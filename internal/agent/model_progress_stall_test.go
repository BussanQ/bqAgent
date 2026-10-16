package agent

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"bqagent/internal/tools"
)

func TestModelProgressReporterStallAndRecovery(t *testing.T) {
	ticker, clock := installModelProgressTestHooks(t)
	original := modelProgressStallThreshold
	modelProgressStallThreshold = 30 * time.Second
	t.Cleanup(func() { modelProgressStallThreshold = original })

	progress := &stallTestBuffer{}
	reporter := startModelProgressReporter(context.Background(), progress, true)
	defer reporter.stop()

	clock.Advance(45 * time.Second)
	fireModelProgressTick(t, ticker, clock)
	waitForStallBufferContains(t, progress, "距上次有效进展已 45s，暂无有效进展")
	if strings.Contains(progress.String(), "Still working") {
		t.Fatalf("progress = %q, want stall message only", progress.String())
	}

	reporter.markActivity()
	waitForStallBufferContains(t, progress, modelProgressRecoveredMessage)
	reporter.markActivity()
	if strings.Count(progress.String(), modelProgressRecoveredMessage) != 1 {
		t.Fatalf("progress = %q, want one recovery line", progress.String())
	}
}

// stallTestBuffer guards reads and writes with one lock for race-safe assertions.
type stallTestBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *stallTestBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *stallTestBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *stallTestBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Len()
}

func waitForStallBufferContains(t *testing.T, b *stallTestBuffer, expected string) {
	t.Helper()
	deadline := time.After(time.Second)
	check := time.NewTicker(time.Millisecond)
	defer check.Stop()
	for {
		if strings.Contains(b.String(), expected) {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("progress = %q, want %q", b.String(), expected)
		case <-check.C:
		}
	}
}

type observerStreamClient struct {
	blockingModelClient
	onStream func(ctx context.Context, onChunk func(string))
}

func (c *observerStreamClient) CreateChatCompletionStream(ctx context.Context, _ string, _ []map[string]any, _ []tools.Definition, onChunk func(string)) (AssistantMessage, error) {
	c.onStream(ctx, onChunk)
	return c.wait(ctx)
}

func runObserverStream(t *testing.T, onStream func(ctx context.Context, onChunk func(string))) (*manualModelProgressTicker, *fakeModelProgressClock, *stallTestBuffer, *observerStreamClient, chan struct{}) {
	ticker, clock := installModelProgressTestHooks(t)
	client := &observerStreamClient{blockingModelClient: *newBlockingModelClient(AssistantMessage{Content: "done"}, nil), onStream: onStream}
	var logs bytes.Buffer
	progress := &stallTestBuffer{}
	wrapped := instrumentChatCompletionClient(client, &logs, progress)
	done := make(chan struct{})
	go func() {
		_, _ = wrapped.CreateChatCompletionStream(context.Background(), testModel, nil, nil, func(string) {})
		close(done)
	}()
	<-client.started
	return ticker, clock, progress, client, done
}

func TestInstrumentedStreamObserverRenewsOnReportModelProgress(t *testing.T) {
	ticker, clock, progress, client, done := runObserverStream(t, func(ctx context.Context, _ func(string)) {
		reportModelProgress(ctx)
	})
	clock.Advance(500 * time.Millisecond)
	fireModelProgressTick(t, ticker, clock)
	if progress.Len() != 0 {
		t.Fatalf("progress = %q, want none after observer renewal", progress.String())
	}
	close(client.release)
	<-done
}

func TestInstrumentedStreamEmptyChunkDoesNotRenew(t *testing.T) {
	ticker, clock, progress, client, done := runObserverStream(t, func(_ context.Context, onChunk func(string)) {
		onChunk("")
	})
	clock.Advance(time.Second)
	fireModelProgressTick(t, ticker, clock)
	waitForStallBufferContains(t, progress, "Still working... test status.")
	close(client.release)
	<-done
}
