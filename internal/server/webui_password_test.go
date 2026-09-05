package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"bqagent/internal/globalconfig"
)

func passwordTestHandler(t *testing.T) (*handler, http.Handler) {
	t.Helper()
	store := globalconfig.NewStore(t.TempDir())
	if err := store.EnsureDefaults(); err != nil {
		t.Fatal(err)
	}
	h := &handler{providers: store, auth: loadWebUIAuth(store)}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/webui/auth", h.auth.handle)
	mux.HandleFunc("/api/v1/webui/password", h.handleChangePassword)
	mux.HandleFunc("/api/v1/status", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	return h, h.auth.protect(mux)
}

func passwordRequest(h http.Handler, path string, payload any, cookie *http.Cookie, origin string) *httptest.ResponseRecorder {
	data, _ := json.Marshal(payload)
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(data)))
	if cookie != nil {
		r.AddCookie(cookie)
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func passwordLogin(t *testing.T, h http.Handler, password string) *http.Cookie {
	t.Helper()
	w := passwordRequest(h, "/api/v1/webui/auth", map[string]string{"password": password}, nil, "")
	if w.Code != 200 {
		t.Fatalf("login status: %d", w.Code)
	}
	return w.Result().Cookies()[0]
}

func passwordInput(current, next, confirmation string) map[string]string {
	return map[string]string{"current_password": current, "new_password": next, "confirm_password": confirmation}
}

func TestWebUIChangePasswordPersistsAndRevokesSessions(t *testing.T) {
	owner, h := passwordTestHandler(t)
	secret, err := owner.providers.EncryptAPIKey("provider-secret")
	if err != nil {
		t.Fatal(err)
	}
	_, err = owner.providers.UpdateProviders(func(active *string, providers *[]globalconfig.Provider) error {
		*active = "p"
		*providers = []globalconfig.Provider{{ID: "p", Name: "p", Models: []string{"model"}, DefaultModel: "model", APIKey: secret}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	first, second := passwordLogin(t, h, "admin123"), passwordLogin(t, h, "admin123")
	w := passwordRequest(h, "/api/v1/webui/password", passwordInput("admin123", "new-secret", "new-secret"), first, "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "no-store" || w.Result().Cookies()[0].MaxAge != -1 {
		t.Fatal("missing cache/cookie protection")
	}
	cfg, err := owner.providers.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WebUI.Password != "new-secret" || cfg.ActiveProvider != "p" || cfg.Providers[0].APIKey != secret {
		t.Fatal("configuration sections changed unexpectedly")
	}
	for _, cookie := range []*http.Cookie{first, second} {
		r := httptest.NewRequest("GET", "/api/v1/status", nil)
		r.AddCookie(cookie)
		out := httptest.NewRecorder()
		h.ServeHTTP(out, r)
		if out.Code != 401 {
			t.Fatal("old session still valid")
		}
	}
	if w := passwordRequest(h, "/api/v1/webui/auth", map[string]string{"password": "admin123"}, nil, ""); w.Code != 401 {
		t.Fatal("old password still valid")
	}
	passwordLogin(t, h, "new-secret")
	reloaded := loadWebUIAuth(owner.providers)
	passwordLogin(t, http.HandlerFunc(reloaded.handle), "new-secret")
	if strings.Contains(w.Body.String(), "secret") {
		t.Fatal("password leaked in response")
	}
}

func TestWebUIChangePasswordRejectsInvalidRequests(t *testing.T) {
	for _, test := range []struct {
		name          string
		input         map[string]string
		authenticated bool
		origin        string
		status        int
	}{
		{"anonymous", passwordInput("admin123", "new-secret", "new-secret"), false, "", 401},
		{"wrong_current", passwordInput("wrong", "new-secret", "new-secret"), true, "", 403},
		{"mismatch", passwordInput("admin123", "new-secret", "different"), true, "", 400},
		{"empty", passwordInput("admin123", "", ""), true, "", 400},
		{"short", passwordInput("admin123", "abc", "abc"), true, "", 400},
		{"long", passwordInput("admin123", strings.Repeat("x", 129), strings.Repeat("x", 129)), true, "", 400},
		{"whitespace", passwordInput("admin123", " secret ", " secret "), true, "", 400},
		{"unchanged", passwordInput("admin123", "admin123", "admin123"), true, "", 400},
		{"cross_origin", passwordInput("admin123", "new-secret", "new-secret"), true, "https://other.invalid", 403},
	} {
		t.Run(test.name, func(t *testing.T) {
			owner, h := passwordTestHandler(t)
			var cookie *http.Cookie
			if test.authenticated {
				cookie = passwordLogin(t, h, "admin123")
			}
			before, _ := os.ReadFile(owner.providers.Path())
			w := passwordRequest(h, "/api/v1/webui/password", test.input, cookie, test.origin)
			if w.Code != test.status {
				t.Fatal(w.Code, w.Body.String())
			}
			after, _ := os.ReadFile(owner.providers.Path())
			if string(before) != string(after) {
				t.Fatal("rejected request changed configuration")
			}
			if cookie != nil && !owner.auth.authenticated(httptestRequestWithCookie(cookie)) {
				t.Fatal("rejected request revoked valid session")
			}
		})
	}
}

func httptestRequestWithCookie(cookie *http.Cookie) *http.Request {
	r := httptest.NewRequest("GET", "/api/v1/status", nil)
	r.AddCookie(cookie)
	return r
}

func TestWebUIChangePasswordSaveFailurePreservesLiveCredentials(t *testing.T) {
	owner, h := passwordTestHandler(t)
	cookie := passwordLogin(t, h, "admin123")
	// A malformed file makes the transactional update fail without relying on OS privilege.
	if err := os.WriteFile(owner.providers.Path(), []byte("invalid JSON"), 0600); err != nil {
		t.Fatal(err)
	}
	w := passwordRequest(h, "/api/v1/webui/password", passwordInput("admin123", "new-secret", "new-secret"), cookie, "")
	if w.Code != 500 {
		t.Fatal(w.Code, w.Body.String())
	}
	if !owner.auth.authenticated(httptestRequestWithCookie(cookie)) {
		t.Fatal("session revoked after failed save")
	}
	passwordLogin(t, h, "admin123")
	if w := passwordRequest(h, "/api/v1/webui/auth", map[string]string{"password": "new-secret"}, nil, ""); w.Code != 401 {
		t.Fatal("failed password applied to live auth")
	}
}

func TestWebUIConcurrentPasswordChangesOnlyOneSucceeds(t *testing.T) {
	owner, h := passwordTestHandler(t)
	cookie := passwordLogin(t, h, "admin123")
	var wg sync.WaitGroup
	statuses := make(chan int, 2)
	for _, value := range []string{"new-secret-1", "new-secret-2"} {
		wg.Add(1)
		go func(next string) {
			defer wg.Done()
			statuses <- passwordRequest(h, "/api/v1/webui/password", passwordInput("admin123", next, next), cookie, "").Code
		}(value)
	}
	wg.Wait()
	close(statuses)
	succeeded := 0
	for status := range statuses {
		if status == 200 {
			succeeded++
		} else if status != 401 {
			t.Fatal(status)
		}
	}
	if succeeded != 1 {
		t.Fatal("multiple password changes succeeded")
	}
	cfg, err := owner.providers.Load()
	if err != nil {
		t.Fatal(err)
	}
	passwordLogin(t, h, cfg.WebUI.Password)
}

func TestWebUIPasswordChangeRevokesConcurrentOldPasswordLogins(t *testing.T) {
	owner, h := passwordTestHandler(t)
	cookie := passwordLogin(t, h, "admin123")
	var wg sync.WaitGroup
	cookies := make(chan *http.Cookie, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := passwordRequest(h, "/api/v1/webui/auth", map[string]string{"password": "admin123"}, nil, "")
			if w.Code == 200 {
				cookies <- w.Result().Cookies()[0]
			} else if w.Code != 401 {
				t.Errorf("login status: %d", w.Code)
			}
		}()
	}
	w := passwordRequest(h, "/api/v1/webui/password", passwordInput("admin123", "new-secret", "new-secret"), cookie, "")
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	wg.Wait()
	close(cookies)
	for cookie := range cookies {
		if owner.auth.authenticated(httptestRequestWithCookie(cookie)) {
			t.Fatal("concurrent old-password login survived password change")
		}
	}
}
