package agent

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"bqagent/internal/tools"
)

// chunkingStub emits its content through the streaming onChunk callback so tests
// can observe where streamed tokens are written.
type chunkingStub struct {
	content string
}

func (s *chunkingStub) CreateChatCompletion(_ context.Context, _ string, _ []map[string]any, _ []tools.Definition) (AssistantMessage, error) {
	return AssistantMessage{Role: "assistant", Content: s.content}, nil
}

func (s *chunkingStub) CreateChatCompletionStream(_ context.Context, _ string, _ []map[string]any, _ []tools.Definition, onChunk func(string)) (AssistantMessage, error) {
	if onChunk != nil {
		onChunk(s.content)
	}
	return AssistantMessage{Role: "assistant", Content: s.content}, nil
}

func TestStreamTokensGoToTokenSink(t *testing.T) {
	var logBuf, tokenBuf bytes.Buffer
	app := NewWithOptions(&chunkingStub{content: "hello world"}, DefaultModel, Options{
		LogWriter: &logBuf,
		TokenSink: &tokenBuf,
		Stream:    true,
	})

	result, err := app.Run(context.Background(), "hi", 2)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result != "hello world" {
		t.Fatalf("result = %q, want %q", result, "hello world")
	}
	if tokenBuf.String() != "hello world" {
		t.Fatalf("token sink = %q, want %q", tokenBuf.String(), "hello world")
	}
	if strings.Contains(logBuf.String(), "hello world") {
		t.Fatalf("log writer unexpectedly contains streamed tokens: %q", logBuf.String())
	}
}

func TestStreamTokensFallBackToLogWriterWithoutSink(t *testing.T) {
	var logBuf bytes.Buffer
	app := NewWithOptions(&chunkingStub{content: "hello world"}, DefaultModel, Options{
		LogWriter: &logBuf,
		Stream:    true,
	})

	if _, err := app.Run(context.Background(), "hi", 2); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !strings.Contains(logBuf.String(), "hello world") {
		t.Fatalf("log writer = %q, want it to contain streamed tokens", logBuf.String())
	}
}
