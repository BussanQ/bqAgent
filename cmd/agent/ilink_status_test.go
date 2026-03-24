package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRunWithIlinkStatusGetsFromServer(t *testing.T) {
	var called bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		called = true
		if request.Method != http.MethodGet {
			t.Fatalf("method = %s, want GET", request.Method)
		}
		if request.URL.Path != "/api/v1/weixin/ilink/status" {
			t.Fatalf("path = %q, want %q", request.URL.Path, "/api/v1/weixin/ilink/status")
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"logged_in":true,"login_status":"confirmed","poller_running":true}`))
	}))
	defer server.Close()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runWithDeps(context.Background(), nil, &stdout, &stderr, []string{"--ilink-status", "--server-url", server.URL}, func(string) string { return "" }, runDeps{})
	if code != 0 {
		t.Fatalf("runWithDeps returned code %d, want 0; stderr=%q", code, stderr.String())
	}
	if !called {
		t.Fatal("status endpoint was not called")
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	if !strings.Contains(stdout.String(), `"login_status": "confirmed"`) {
		t.Fatalf("stdout = %q, want pretty JSON response", stdout.String())
	}
	if !strings.Contains(stdout.String(), `"poller_running": true`) {
		t.Fatalf("stdout = %q, want poller_running", stdout.String())
	}
}

func TestIlinkStatusCannotAcceptTask(t *testing.T) {
	_, _, err := parseCLI([]string{"--ilink-status", "hello"})
	if err == nil {
		t.Fatal("parseCLI returned nil error, want ilink-status/task conflict")
	}
	if !strings.Contains(err.Error(), "--ilink-status does not accept a task") {
		t.Fatalf("error = %q, want ilink-status task validation", err.Error())
	}
}

func TestIlinkStatusCannotCombineWithIlinkLogin(t *testing.T) {
	_, _, err := parseCLI([]string{"--ilink-status", "--ilink-login"})
	if err == nil {
		t.Fatal("parseCLI returned nil error, want ilink-status/ilink-login conflict")
	}
	if !strings.Contains(err.Error(), "--ilink-login cannot be combined with --ilink-status") {
		t.Fatalf("error = %q, want ilink status/login validation", err.Error())
	}
}
