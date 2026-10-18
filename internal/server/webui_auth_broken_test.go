package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bqagent/internal/globalconfig"
)

func TestWebUIAuthBrokenConfigFailsClosed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	auth := loadWebUIAuth(globalconfig.NewStore(dir))
	if !auth.broken {
		t.Fatal("corrupt config must mark authentication as broken")
	}

	protected := auth.protect(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	for _, path := range []string{"/api/v1/chat", "/api/v1/chat/input", "/api/v1/status", "/api/v1/runs/abc"} {
		response := httptest.NewRecorder()
		protected.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://app.test"+path, nil))
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s: status=%d, want 503", path, response.Code)
		}
		if !strings.Contains(response.Body.String(), "鉴权配置读取失败") {
			t.Fatalf("%s: missing guidance in %q", path, response.Body.String())
		}
	}

	// Unprotected endpoints keep working.
	response := httptest.NewRecorder()
	protected.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://app.test/healthz", nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("healthz: status=%d, want 204", response.Code)
	}

	// The status endpoint reports the broken state instead of a silent passwordless mode.
	response = httptest.NewRecorder()
	auth.handle(response, httptest.NewRequest(http.MethodGet, "http://app.test/api/v1/webui/auth", nil))
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, `"required":true`) ||
		!strings.Contains(body, `"authenticated":false`) || !strings.Contains(body, `"error"`) {
		t.Fatalf("auth status=%d body=%s", response.Code, body)
	}

	// Login cannot succeed while the config is unreadable.
	response = httptest.NewRecorder()
	auth.handle(response, httptest.NewRequest(http.MethodPost, "http://app.test/api/v1/webui/auth", strings.NewReader(`{"password":"admin123"}`)))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("login: status=%d, want 503", response.Code)
	}
}

func TestWebUIAuthMissingConfigStaysPasswordless(t *testing.T) {
	auth := loadWebUIAuth(globalconfig.NewStore(t.TempDir()))
	if auth.broken || auth.required {
		t.Fatalf("missing config must stay passwordless: broken=%v required=%v", auth.broken, auth.required)
	}
	protected := auth.protect(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	response := httptest.NewRecorder()
	protected.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://app.test/api/v1/status", nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("status=%d, want 204", response.Code)
	}
}
