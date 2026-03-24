package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRunWithIlinkLoginPostsToServer(t *testing.T) {
	originalRender := renderTerminalQR
	t.Cleanup(func() { renderTerminalQR = originalRender })
	renderTerminalQR = func(text string, writer io.Writer) {
		_, _ = io.WriteString(writer, "[QR:"+text+"]\n")
	}

	var called bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		called = true
		if request.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", request.Method)
		}
		if request.URL.Path != "/api/v1/weixin/ilink/login" {
			t.Fatalf("path = %q, want %q", request.URL.Path, "/api/v1/weixin/ilink/login")
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"logged_in":false,"login_status":"pending","qrcode_img_content":"https://example.com/qr?foo=1\u0026bar=2"}`))
	}))
	defer server.Close()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runWithDeps(context.Background(), nil, &stdout, &stderr, []string{"--ilink-login", "--server-url", server.URL}, func(string) string { return "" }, runDeps{})
	if code != 0 {
		t.Fatalf("runWithDeps returned code %d, want 0; stderr=%q", code, stderr.String())
	}
	if !called {
		t.Fatal("login endpoint was not called")
	}
	if !strings.Contains(stdout.String(), `"login_status": "pending"`) {
		t.Fatalf("stdout = %q, want pretty JSON response", stdout.String())
	}
	if !strings.Contains(stdout.String(), `"qrcode_img_content": "https://example.com/qr?foo=1\u0026bar=2"`) {
		t.Fatalf("stdout = %q, want qrcode_img_content JSON", stdout.String())
	}
	if !strings.Contains(stderr.String(), "请用微信扫描以下二维码：") {
		t.Fatalf("stderr = %q, want QR prompt", stderr.String())
	}
	if !strings.Contains(stderr.String(), `[QR:https://example.com/qr?foo=1&bar=2]`) {
		t.Fatalf("stderr = %q, want decoded QR content", stderr.String())
	}
	if !strings.Contains(stderr.String(), "二维码内容：") {
		t.Fatalf("stderr = %q, want QR content label", stderr.String())
	}
}

func TestRunWithIlinkLoginWritesServerErrorsToStderr(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusBadGateway)
		_, _ = writer.Write([]byte(`{"error":"upstream failed"}`))
	}))
	defer server.Close()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runWithDeps(context.Background(), nil, &stdout, &stderr, []string{"--ilink-login", "--server-url", server.URL}, func(string) string { return "" }, runDeps{})
	if code != 1 {
		t.Fatalf("runWithDeps returned code %d, want 1", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), "upstream failed") {
		t.Fatalf("stderr = %q, want upstream error", stderr.String())
	}
}

func TestIlinkLoginCannotAcceptTask(t *testing.T) {
	_, _, err := parseCLI([]string{"--ilink-login", "hello"})
	if err == nil {
		t.Fatal("parseCLI returned nil error, want ilink-login/task conflict")
	}
	if !strings.Contains(err.Error(), "--ilink-login does not accept a task") {
		t.Fatalf("error = %q, want ilink-login task validation", err.Error())
	}
}

func TestIlinkLoginCannotCombineWithServer(t *testing.T) {
	_, _, err := parseCLI([]string{"--ilink-login", "--server"})
	if err == nil {
		t.Fatal("parseCLI returned nil error, want ilink-login/server conflict")
	}
	if !strings.Contains(err.Error(), "cannot be combined with execution or server flags") {
		t.Fatalf("error = %q, want ilink-login combination validation", err.Error())
	}
}
