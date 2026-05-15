package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebSearchWithConfigCallsFirecrawlSearch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/search" {
			t.Fatalf("path = %s, want /search", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatalf("Authorization = %q, want Bearer test-token", r.Header.Get("Authorization"))
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("Content-Type = %q, want application/json", r.Header.Get("Content-Type"))
		}

		var body searchRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}
		if body.Query != "latest AI news" {
			t.Fatalf("query = %q, want latest AI news", body.Query)
		}
		if body.Limit != 5 {
			t.Fatalf("limit = %d, want 5", body.Limit)
		}
		if len(body.Sources) != 1 || body.Sources[0] != "web" {
			t.Fatalf("sources = %#v, want [web]", body.Sources)
		}
		if len(body.ScrapeOptions.Formats) != 1 || body.ScrapeOptions.Formats[0] != "markdown" {
			t.Fatalf("formats = %#v, want [markdown]", body.ScrapeOptions.Formats)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"data":{"web":[{"title":"Result title","url":"https://example.com","markdown":"Result markdown"}]}}`))
	}))
	defer server.Close()

	result, err := WebSearchWithConfig("test-token", server.URL)(context.Background(), map[string]any{"query": "latest AI news"})
	if err != nil {
		t.Fatalf("WebSearchWithConfig returned error: %v", err)
	}
	if !strings.Contains(result, "**Result title**") || !strings.Contains(result, "https://example.com") || !strings.Contains(result, "Result markdown") {
		t.Fatalf("result = %q, want title, url, and markdown", result)
	}
}

func TestWebSearchWithConfigFallsBackToDescription(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"data":{"web":[{"title":"Result title","url":"https://example.com","description":"Result description"}]}}`))
	}))
	defer server.Close()

	result, err := WebSearchWithConfig("test-token", server.URL)(context.Background(), map[string]any{"query": "latest AI news"})
	if err != nil {
		t.Fatalf("WebSearchWithConfig returned error: %v", err)
	}
	if !strings.Contains(result, "Result description") {
		t.Fatalf("result = %q, want description fallback", result)
	}
}

func TestWebSearchWithConfigReturnsNoResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"data":{"web":[]}}`))
	}))
	defer server.Close()

	result, err := WebSearchWithConfig("test-token", server.URL)(context.Background(), map[string]any{"query": "nothing"})
	if err != nil {
		t.Fatalf("WebSearchWithConfig returned error: %v", err)
	}
	if result != "No results found." {
		t.Fatalf("result = %q, want No results found.", result)
	}
}

func TestWebSearchWithConfigRequiresFirecrawlAPIKey(t *testing.T) {
	_, err := WebSearchWithConfig("", "http://example.test")(context.Background(), map[string]any{"query": "news"})
	if err == nil {
		t.Fatal("expected missing API key error")
	}
	if !strings.Contains(err.Error(), "Firecrawl") {
		t.Fatalf("error = %q, want Firecrawl", err.Error())
	}
}

func TestWebSearchWithConfigIncludesNonOKResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad token", http.StatusUnauthorized)
	}))
	defer server.Close()

	_, err := WebSearchWithConfig("test-token", server.URL)(context.Background(), map[string]any{"query": "news"})
	if err == nil {
		t.Fatal("expected non-OK response error")
	}
	if !strings.Contains(err.Error(), "401 Unauthorized") || !strings.Contains(err.Error(), "bad token") {
		t.Fatalf("error = %q, want status and body", err.Error())
	}
}
