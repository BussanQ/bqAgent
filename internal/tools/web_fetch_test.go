package tools

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebFetchReturnsMarkdownByDefault(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = writer.Write([]byte(`<!doctype html><html><head><title>Hello Page</title></head><body><h1>Hello</h1><p>World</p><ul><li>One</li></ul><a href="https://example.com/docs">Docs</a><script>ignored()</script></body></html>`))
	}))
	defer server.Close()

	result, err := WebFetchWithClient(server.Client(), true)(context.Background(), map[string]any{"url": server.URL})
	if err != nil {
		t.Fatalf("WebFetch returned error: %v", err)
	}
	if !strings.Contains(result, "Final-URL: "+server.URL) {
		t.Fatalf("WebFetch result = %q, want final URL", result)
	}
	if !strings.Contains(result, "Title: Hello Page") {
		t.Fatalf("WebFetch result = %q, want title", result)
	}
	if !strings.Contains(result, "# Hello") || !strings.Contains(result, "- One") {
		t.Fatalf("WebFetch result = %q, want markdown output", result)
	}
	if !strings.Contains(result, "[Docs](https://example.com/docs)") {
		t.Fatalf("WebFetch result = %q, want markdown link", result)
	}
	if strings.Contains(result, "ignored()") {
		t.Fatalf("WebFetch result = %q, should strip script content", result)
	}
}

func TestWebFetchReturnsTextWhenRequested(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = writer.Write([]byte(`<!doctype html><html><body><h1>Hello</h1><p>World</p></body></html>`))
	}))
	defer server.Close()

	result, err := WebFetchWithClient(server.Client(), true)(context.Background(), map[string]any{"url": server.URL, "extract_mode": "text"})
	if err != nil {
		t.Fatalf("WebFetch returned error: %v", err)
	}
	if strings.Contains(result, "# Hello") {
		t.Fatalf("WebFetch result = %q, should not contain markdown heading", result)
	}
	if !strings.Contains(result, "Hello") || !strings.Contains(result, "World") {
		t.Fatalf("WebFetch result = %q, want text output", result)
	}
}

func TestWebFetchReturnsPlainText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/plain")
		_, _ = writer.Write([]byte("hello from text"))
	}))
	defer server.Close()

	result, err := WebFetchWithClient(server.Client(), true)(context.Background(), map[string]any{"url": server.URL})
	if err != nil {
		t.Fatalf("WebFetch returned error: %v", err)
	}
	if !strings.Contains(result, "hello from text") {
		t.Fatalf("WebFetch result = %q, want plain text body", result)
	}
}

func TestWebFetchReturnsRedirectFinalURL(t *testing.T) {
	final := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = writer.Write([]byte(`<html><body><h1>Target</h1></body></html>`))
	}))
	defer final.Close()

	redirect := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, final.URL, http.StatusFound)
	}))
	defer redirect.Close()

	result, err := WebFetchWithClient(redirect.Client(), true)(context.Background(), map[string]any{"url": redirect.URL})
	if err != nil {
		t.Fatalf("WebFetch returned error: %v", err)
	}
	if !strings.Contains(result, "URL: "+redirect.URL) {
		t.Fatalf("WebFetch result = %q, want original URL", result)
	}
	if !strings.Contains(result, "Final-URL: "+final.URL) {
		t.Fatalf("WebFetch result = %q, want redirected final URL", result)
	}
}

func TestWebFetchTruncatesContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/plain")
		_, _ = writer.Write([]byte("abcdefghijklmnopqrstuvwxyz"))
	}))
	defer server.Close()

	result, err := WebFetchWithClient(server.Client(), true)(context.Background(), map[string]any{"url": server.URL, "max_chars": 10})
	if err != nil {
		t.Fatalf("WebFetch returned error: %v", err)
	}
	if !strings.Contains(result, "Truncated: true") {
		t.Fatalf("WebFetch result = %q, want truncation marker", result)
	}
	if !strings.Contains(result, "abcdefghij") {
		t.Fatalf("WebFetch result = %q, want truncated content", result)
	}
}

func TestWebFetchRejectsInvalidExtractMode(t *testing.T) {
	_, err := WebFetch(context.Background(), map[string]any{"url": "https://example.com", "extract_mode": "html"})
	if err == nil || !strings.Contains(err.Error(), "unsupported extract_mode") {
		t.Fatalf("WebFetch error = %v, want unsupported extract_mode", err)
	}
}

func TestWebFetchRejectsInvalidURL(t *testing.T) {
	_, err := WebFetch(context.Background(), map[string]any{"url": "://bad"})
	if err == nil || !strings.Contains(err.Error(), "invalid url") {
		t.Fatalf("WebFetch error = %v, want invalid url", err)
	}
}

func TestWebFetchRejectsUnsupportedScheme(t *testing.T) {
	_, err := WebFetch(context.Background(), map[string]any{"url": "file:///tmp/test.txt"})
	if err == nil || !strings.Contains(err.Error(), "unsupported url scheme") {
		t.Fatalf("WebFetch error = %v, want unsupported scheme", err)
	}
}

func TestWebFetchRejectsLocalhost(t *testing.T) {
	_, err := WebFetch(context.Background(), map[string]any{"url": "http://127.0.0.1/test"})
	if err == nil || !strings.Contains(err.Error(), "refusing to fetch private or local address") {
		t.Fatalf("WebFetch error = %v, want private address rejection", err)
	}
}

func TestWebFetchRejectsNonSuccessStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		http.Error(writer, "<html><body><h1>Bad Gateway</h1><p>nope</p></body></html>", http.StatusBadGateway)
	}))
	defer server.Close()

	_, err := WebFetchWithClient(server.Client(), true)(context.Background(), map[string]any{"url": server.URL})
	if err == nil || !strings.Contains(err.Error(), "502 Bad Gateway") || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("WebFetch error = %v, want status failure details", err)
	}
}

func TestWebFetchRejectsUnsupportedContentType(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/octet-stream")
		_, _ = writer.Write([]byte("\x00\x01"))
	}))
	defer server.Close()

	_, err := WebFetchWithClient(server.Client(), true)(context.Background(), map[string]any{"url": server.URL})
	if err == nil || !strings.Contains(err.Error(), "unsupported content type") {
		t.Fatalf("WebFetch error = %v, want unsupported content type", err)
	}
}

func TestWebFetchRejectsOversizedBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/plain")
		_, _ = writer.Write([]byte(strings.Repeat("a", maxFetchBodyBytes+1)))
	}))
	defer server.Close()

	_, err := WebFetchWithClient(server.Client(), true)(context.Background(), map[string]any{"url": server.URL})
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("exceeded %d bytes", maxFetchBodyBytes)) {
		t.Fatalf("WebFetch error = %v, want oversized body rejection", err)
	}
}
