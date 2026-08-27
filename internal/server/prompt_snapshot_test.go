package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"bqagent/internal/agent"
	"bqagent/internal/workspace"
)

func TestServiceReusesPromptSnapshotWhenMemoryChanges(t *testing.T) {
	var requestBodies []map[string]any
	llm := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		requestBodies = append(requestBodies, body)
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprint(writer, `{"choices":[{"message":{"role":"assistant","content":"done"}}]}`)
	}))
	defer llm.Close()

	memory := "memory snapshot v1"
	service := NewService(ServiceOptions{
		WorkspaceRoot: t.TempDir(),
		Client:        agent.NewClient("", llm.URL, llm.Client()),
		Model:         "gpt-5.5",
		PromptSectionsBuilder: func() (workspace.PromptSections, error) {
			return workspace.PromptSections{Stable: "stable instructions", SessionContext: memory}, nil
		},
	})
	first, err := service.HandleTurn(context.Background(), TurnRequest{Message: "first question"})
	if err != nil {
		t.Fatal(err)
	}
	memory = "memory snapshot v2"
	if _, err := service.HandleTurn(context.Background(), TurnRequest{SessionID: first.SessionID, Message: "second question"}); err != nil {
		t.Fatal(err)
	}
	if len(requestBodies) != 2 {
		t.Fatalf("request count = %d", len(requestBodies))
	}
	firstMessages, _ := requestBodies[0]["messages"].([]any)
	secondMessages, _ := requestBodies[1]["messages"].([]any)
	if len(firstMessages) < 2 || len(secondMessages) < 2 {
		t.Fatalf("request messages = %#v / %#v", firstMessages, secondMessages)
	}
	firstPrefix, _ := json.Marshal(firstMessages[:2])
	secondPrefix, _ := json.Marshal(secondMessages[:2])
	if string(firstPrefix) != string(secondPrefix) {
		t.Fatalf("prompt prefix changed after memory update:\nfirst:  %s\nsecond: %s", firstPrefix, secondPrefix)
	}
	if !strings.Contains(string(secondPrefix), "memory snapshot v1") || strings.Contains(string(secondPrefix), "memory snapshot v2") {
		t.Fatalf("second prefix = %s, want original memory snapshot", secondPrefix)
	}
}

func TestGenerationMetricsIncludesWholeTurnCacheMetrics(t *testing.T) {
	generation := generationMetricsFromAgent(agent.TurnGenerationMetrics{
		CacheMetrics: agent.TurnCacheMetrics{
			Available: true, Calls: 3, InputTokens: 600,
			CacheReadTokens: 360, CacheWriteTokens: 120, UncachedInputTokens: 120, HitRate: .6,
		},
	})
	if generation == nil || generation.CacheMetrics == nil {
		t.Fatalf("generation metrics = %#v", generation)
	}
	cache := generation.CacheMetrics
	if !cache.Available || cache.Calls != 3 || cache.InputTokens != 600 || cache.CacheReadTokens != 360 || cache.CacheWriteTokens != 120 || cache.UncachedInputTokens != 120 || cache.HitRate != .6 {
		t.Fatalf("cache metrics = %#v", cache)
	}
}
