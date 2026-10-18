package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWebUIChatInputRequiresAuthentication(t *testing.T) {
	auth := newWebUIAuth("test-password")
	auth.sessions["valid"] = time.Now().Add(time.Hour)
	auth.sessions["expired"] = time.Now().Add(-time.Hour)
	for _, tt := range []struct {
		name, token, origin string
		want                int
	}{
		{"anonymous", "", "", 401}, {"invalid", "invalid", "", 401}, {"expired", "expired", "", 401},
		{"authenticated", "valid", "", 204}, {"same origin", "valid", "http://app.test", 204}, {"cross origin", "valid", "http://other.test", 403},
	} {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			handler := auth.protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true; w.WriteHeader(http.StatusNoContent) }))
			req := httptest.NewRequest(http.MethodPost, "http://app.test/api/v1/chat/input", strings.NewReader(`{"turn_id":"active-turn","kind":"steering","text":"change instructions"}`))
			if tt.token != "" {
				req.AddCookie(&http.Cookie{Name: webUIAuthCookie, Value: tt.token})
			}
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			if response.Code != tt.want || called != (tt.want == 204) {
				t.Fatalf("status=%d handler called=%v", response.Code, called)
			}
		})
	}
}
