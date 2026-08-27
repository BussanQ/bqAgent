package agent

import (
	"context"
	"math"
	"sync"
	"testing"
	"time"

	"bqagent/internal/tools"
)

type generationMetricsStub struct {
	mu        sync.Mutex
	responses []AssistantMessage
	chunks    [][]string
	index     int
}

func (stub *generationMetricsStub) next(onChunk func(string)) (AssistantMessage, error) {
	stub.mu.Lock()
	index := stub.index
	stub.index++
	message := stub.responses[index]
	chunks := append([]string(nil), stub.chunks[index]...)
	stub.mu.Unlock()

	time.Sleep(time.Millisecond)
	for _, chunk := range chunks {
		if onChunk != nil {
			onChunk(chunk)
		}
		time.Sleep(time.Millisecond)
	}
	return message, nil
}

func (stub *generationMetricsStub) CreateChatCompletion(_ context.Context, _ string, _ []map[string]any, _ []tools.Definition) (AssistantMessage, error) {
	return stub.next(nil)
}

func (stub *generationMetricsStub) CreateChatCompletionStream(_ context.Context, _ string, _ []map[string]any, _ []tools.Definition, onChunk func(string)) (AssistantMessage, error) {
	return stub.next(onChunk)
}

func TestRunConversationTurnWithGenerationMetrics(t *testing.T) {
	stub := &generationMetricsStub{
		responses: []AssistantMessage{{
			Role:    "assistant",
			Content: "hello world",
			Usage: TokenUsage{
				PromptTokens:        100,
				CachedPromptTokens:  75,
				CacheUsageAvailable: true,
				CompletionTokens:    12,
				ReasoningTokens:     2,
				TotalTokens:         112,
			},
		}},
		chunks: [][]string{{"hello", " world"}},
	}
	app := NewWithOptions(stub, testModel, Options{Stream: true})

	result, _, metrics, err := app.RunConversationTurnWithMetrics(context.Background(), []map[string]any{{"role": "user", "content": "hi"}}, 2)
	if err != nil {
		t.Fatalf("RunConversationTurnWithMetrics returned error: %v", err)
	}
	if result != "hello world" {
		t.Fatalf("result = %q, want hello world", result)
	}
	if !metrics.Available {
		t.Fatal("metrics are unavailable")
	}
	if metrics.FirstTokenLatency <= 0 {
		t.Fatalf("first token latency = %s, want positive", metrics.FirstTokenLatency)
	}
	if metrics.CompletionTokens != 12 || metrics.ReasoningTokens != 2 {
		t.Fatalf("usage metrics = %#v", metrics)
	}
	if !metrics.CacheUsageAvailable || metrics.PromptTokens != 100 || metrics.CachedPromptTokens != 75 {
		t.Fatalf("cache metrics = %#v", metrics)
	}
	if metrics.GenerationDuration <= 0 || metrics.TokensPerSecond <= 0 {
		t.Fatalf("generation metrics = %#v, want positive duration and rate", metrics)
	}
}

func TestRunConversationTurnWithGenerationMetricsUsesFinalTextCall(t *testing.T) {
	stub := &generationMetricsStub{
		responses: []AssistantMessage{
			{
				Role:    "assistant",
				Content: "checking",
				ToolCalls: []ToolCall{{
					ID: "call-1", Type: "function", Function: FunctionCall{Name: "lookup", Arguments: `{}`},
				}},
				Usage: TokenUsage{CompletionTokens: 50, TotalTokens: 60},
			},
			{
				Role:    "assistant",
				Content: "final answer",
				Usage:   TokenUsage{CompletionTokens: 8, TotalTokens: 18},
			},
		},
		chunks: [][]string{{"checking"}, {"final", " answer"}},
	}
	app := NewWithOptions(stub, testModel, Options{
		Stream: true,
		Functions: map[string]tools.Function{
			"lookup": func(context.Context, map[string]any) (string, error) { return "found", nil },
		},
	})

	result, _, metrics, err := app.RunConversationTurnWithMetrics(context.Background(), []map[string]any{{"role": "user", "content": "search"}}, 3)
	if err != nil {
		t.Fatalf("RunConversationTurnWithMetrics returned error: %v", err)
	}
	if result != "final answer" {
		t.Fatalf("result = %q, want final answer", result)
	}
	if !metrics.Available || metrics.CompletionTokens != 8 {
		t.Fatalf("metrics = %#v, want final call usage", metrics)
	}
}

func TestGenerationMetricsRateExcludesReasoningTokens(t *testing.T) {
	collector := &turnGenerationCollector{}
	collector.record(generationCandidate{
		content:            "answer",
		firstTokenLatency:  250 * time.Millisecond,
		completionTokens:   12,
		reasoningTokens:    2,
		generationDuration: time.Second,
	})

	metrics := collector.metricsFor("answer")
	if !metrics.Available {
		t.Fatal("metrics are unavailable")
	}
	if math.Abs(metrics.TokensPerSecond-9) > 0.0001 {
		t.Fatalf("tokens per second = %v, want 9", metrics.TokensPerSecond)
	}
	if mismatched := collector.metricsFor("different answer"); mismatched.Available {
		t.Fatalf("mismatched metrics = %#v, want unavailable", mismatched)
	}
}

func TestGenerationMetricsWithoutUsageKeepsFirstTokenLatency(t *testing.T) {
	collector := &turnGenerationCollector{}
	collector.record(generationCandidate{
		content:            "answer",
		firstTokenLatency:  100 * time.Millisecond,
		generationDuration: time.Second,
	})

	metrics := collector.metricsFor("answer")
	if !metrics.Available || metrics.FirstTokenLatency != 100*time.Millisecond {
		t.Fatalf("metrics = %#v, want first token latency", metrics)
	}
	if metrics.TokensPerSecond != 0 {
		t.Fatalf("tokens per second = %v, want 0 without usage", metrics.TokensPerSecond)
	}
}
