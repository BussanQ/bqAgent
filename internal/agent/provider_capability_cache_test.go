package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// reasoningRejectingServer rejects any request that carries reasoning effort
// and reports how many requests arrived with it.
func reasoningRejectingServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var withReasoning atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(request.Body).Decode(&body)
		if body["reasoning_effort"] != nil {
			withReasoning.Add(1)
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprint(writer, `{"error":{"message":"unsupported reasoning_effort parameter"}}`)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(writer, providerJSONSuccess(APITypeOpenAI, "done"))
	}))
	t.Cleanup(server.Close)
	return server, &withReasoning
}

func TestProviderCapabilityCacheIsScopedToClient(t *testing.T) {
	server, withReasoning := reasoningRejectingServer(t)
	options := ChatCompletionOptions{ReasoningEffort: ReasoningEffortHigh}
	messages := []map[string]any{{"role": "user", "content": "hi"}}

	first := newTestResilientClient(APITypeOpenAI, server.URL, server.Client(), 0)
	if _, err := first.CreateChatCompletionWithOptions(context.Background(), "shared-model", messages, nil, options); err != nil {
		t.Fatal(err)
	}
	// A second client for the same URL and model (e.g. another credential) must
	// probe the capability itself instead of inheriting the first client's downgrade.
	second := newTestResilientClient(APITypeOpenAI, server.URL, server.Client(), 0)
	message, err := second.CreateChatCompletionWithOptions(context.Background(), "shared-model", messages, nil, options)
	if err != nil {
		t.Fatal(err)
	}
	if message.Request.ReasoningDowngradeSource != "provider_rejection" {
		t.Fatalf("second client downgrade source = %q, want its own provider_rejection", message.Request.ReasoningDowngradeSource)
	}
	if withReasoning.Load() != 2 {
		t.Fatalf("requests with reasoning = %d, want 2 (one probe per client)", withReasoning.Load())
	}
}

func TestProviderCapabilityCacheExpiresAfterTTL(t *testing.T) {
	server, withReasoning := reasoningRejectingServer(t)
	options := ChatCompletionOptions{ReasoningEffort: ReasoningEffortHigh}
	messages := []map[string]any{{"role": "user", "content": "hi"}}

	now := time.Now()
	client := newTestResilientClient(APITypeOpenAI, server.URL, server.Client(), 0)
	client.capabilities.now = func() time.Time { return now }

	if _, err := client.CreateChatCompletionWithOptions(context.Background(), "ttl-model", messages, nil, options); err != nil {
		t.Fatal(err)
	}
	cached, err := client.CreateChatCompletionWithOptions(context.Background(), "ttl-model", messages, nil, options)
	if err != nil || cached.Request.ReasoningDowngradeSource != "capability_cache" {
		t.Fatalf("within TTL: source=%q err=%v, want capability_cache", cached.Request.ReasoningDowngradeSource, err)
	}

	now = now.Add(providerCapabilityTTL + time.Second)
	reprobed, err := client.CreateChatCompletionWithOptions(context.Background(), "ttl-model", messages, nil, options)
	if err != nil || reprobed.Request.ReasoningDowngradeSource != "provider_rejection" {
		t.Fatalf("after TTL: source=%q err=%v, want provider re-probe", reprobed.Request.ReasoningDowngradeSource, err)
	}
	if withReasoning.Load() != 2 {
		t.Fatalf("requests with reasoning = %d, want initial probe plus one re-probe after expiry", withReasoning.Load())
	}
}
