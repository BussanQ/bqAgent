package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRunUsesDefaultHelloTask(t *testing.T) {
	t.Helper()

	var seenRequest struct {
		Messages []map[string]any `json:"messages"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := json.NewDecoder(request.Body).Decode(&seenRequest); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"}}]}`))
	}))
	defer server.Close()

	getenv := func(key string) string {
		switch key {
		case "OPENAI_BASE_URL":
			return server.URL
		default:
			return ""
		}
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run(context.Background(), &stdout, &stderr, nil, getenv)
	if code != 0 {
		t.Fatalf("run returned code %d, want 0", code)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	if len(seenRequest.Messages) != 2 {
		t.Fatalf("messages length = %d, want 2", len(seenRequest.Messages))
	}
	if seenRequest.Messages[1]["content"] != "Hello" {
		t.Fatalf("user message = %#v, want Hello", seenRequest.Messages[1]["content"])
	}
	if stdout.String() != "[Agent] done\ndone\n" {
		t.Fatalf("stdout = %q, want agent log plus final result", stdout.String())
	}
}

func TestRunJoinsArgumentsIntoSingleTask(t *testing.T) {
	t.Helper()

	var seenRequest struct {
		Messages []map[string]any `json:"messages"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := json.NewDecoder(request.Body).Decode(&seenRequest); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer server.Close()

	getenv := func(key string) string {
		switch key {
		case "OPENAI_BASE_URL":
			return server.URL
		default:
			return ""
		}
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run(context.Background(), &stdout, &stderr, []string{"read", "README.md"}, getenv)
	if code != 0 {
		t.Fatalf("run returned code %d, want 0", code)
	}
	if seenRequest.Messages[1]["content"] != "read README.md" {
		t.Fatalf("user message = %#v, want joined argv string", seenRequest.Messages[1]["content"])
	}
}

func TestRunWritesErrorsToStderr(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusBadGateway)
		_, _ = writer.Write([]byte(`{"error":"upstream failed"}`))
	}))
	defer server.Close()

	getenv := func(key string) string {
		switch key {
		case "OPENAI_BASE_URL":
			return server.URL
		default:
			return ""
		}
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run(context.Background(), &stdout, &stderr, []string{"hello"}, getenv)
	if code != 1 {
		t.Fatalf("run returned code %d, want 1", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if stderr.Len() == 0 {
		t.Fatal("stderr was empty, want error output")
	}
}
