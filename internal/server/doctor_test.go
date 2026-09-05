package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"bqagent/internal/doctor"
	"bqagent/internal/globalconfig"
)

func TestProviderSectionHTTPUpdatePreservesWebUIPassword(t *testing.T) {
	dir := t.TempDir()
	store := globalconfig.NewStore(dir)
	if err := store.EnsureDefaults(); err != nil {
		t.Fatal(err)
	}
	handler := &handler{providers: store}
	request := httptest.NewRequest("PUT", "/api/v1/webui/providers", strings.NewReader(`{"active_provider":"p","providers":[{"id":"p","name":"p","api_type":"openai","models":["m"],"default_model":"m","api_key":"private-key"}]}`))
	recorder := httptest.NewRecorder()
	handler.handleProviders(recorder, request)
	if recorder.Code != 200 {
		t.Fatal(recorder.Code, recorder.Body.String())
	}
	config, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if config.WebUI.Password != "admin123" || config.ActiveProvider != "p" {
		t.Fatal("section lost")
	}
	key, err := store.DecryptAPIKey(config.Providers[0].APIKey)
	if err != nil || key != "private-key" {
		t.Fatal("ciphertext no longer readable")
	}
	if strings.Contains(recorder.Body.String(), "private-key") || strings.Contains(recorder.Body.String(), "admin123") {
		t.Fatal("secret response")
	}
}

func TestDoctorEndpointsRespectAuthAndServiceReadiness(t *testing.T) {
	dir := t.TempDir()
	store := globalconfig.NewStore(dir)
	if err := store.EnsureDefaults(); err != nil {
		t.Fatal(err)
	}
	service := &Service{agentDir: dir, doctor: doctor.New(doctor.Options{Store: store, Storage: []doctor.Storage{{ID: "global", Path: dir}}})}
	h := NewHandler(HandlerOptions{Service: service})
	get := func(method, path string, cookie *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	ready := get("GET", "/readyz", nil)
	if ready.Code != 200 || strings.TrimSpace(ready.Body.String()) != `{"ready":true}` {
		t.Fatal(ready.Code, ready.Body.String())
	}
	for _, method := range []string{"GET", "POST"} {
		if response := get(method, "/api/v1/webui/doctor", nil); response.Code != 401 {
			t.Fatal(response.Code)
		}
	}
	login := httptest.NewRequest("POST", "/api/v1/webui/auth", strings.NewReader(`{"password":"admin123"}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, login)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	response := get("GET", "/api/v1/webui/doctor", w.Result().Cookies()[0])
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	var report doctor.Report
	if err := json.Unmarshal(response.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if !report.Ready {
		t.Fatal(report)
	}
	if active := get("POST", "/api/v1/webui/doctor", w.Result().Cookies()[0]); active.Code != 200 {
		t.Fatal(active.Code, active.Body.String())
	}
	if strings.Contains(response.Body.String(), "admin123") {
		t.Fatal("password leaked")
	}
	os.WriteFile(store.Path(), []byte("invalid"), 0600)
	if response := get("GET", "/readyz", nil); response.Code != 503 || strings.Contains(response.Body.String(), dir) {
		t.Fatal(response.Code, response.Body.String())
	}
	if response := get("GET", "/healthz", nil); response.Code != 200 {
		t.Fatal("readiness changed liveness")
	}
}
