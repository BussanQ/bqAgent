package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bqagent/internal/agent"
	"bqagent/internal/globalconfig"
	"bqagent/internal/session"
)

func TestApplySavedProviderPrefersMagpieUntilDisconnected(t *testing.T) {
	dir := t.TempDir()
	store := globalconfig.NewStore(dir)
	if err := store.Save(globalconfig.Config{ActiveProvider: "saved", Providers: []globalconfig.Provider{{ID: "saved", Name: "Saved", APIType: "anthropic", DefaultModel: "original", Models: []string{"original"}}}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "magpie.json")
	if err := os.WriteFile(path, []byte(`{"model":"p/m","base_url":"http://localhost:12345/v1","api_key":"test-key"}`), 0600); err != nil {
		t.Fatal(err)
	}
	service := NewService(ServiceOptions{AgentDir: dir, WorkspaceRoot: t.TempDir()})
	handler := &handler{providers: store}
	handler.applySavedProvider(service)
	if info := service.RuntimeLLMInfo(); info.ProviderID != "magpie" || info.Model != "p/m" || info.APIType != agent.APITypeOpenAIResponse {
		t.Fatal("HTTP startup replaced the Magpie connection with the saved provider")
	}
	if err := os.WriteFile(path, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	handler.applySavedProvider(service)
	if info := service.RuntimeLLMInfo(); info.ProviderID != "saved" || info.Model != "original" || info.APIType != agent.APITypeAnthropic {
		t.Fatal("HTTP startup did not restore the saved provider after disconnect")
	}
}

func magpieSettingsFixture(t *testing.T) (*handler, string) {
	t.Helper()
	dir := t.TempDir()
	store := globalconfig.NewStore(dir)
	if err := store.Save(globalconfig.Config{ActiveProvider: "saved", Providers: []globalconfig.Provider{{ID: "saved", Name: "Saved", APIType: "openai", Models: []string{"original"}, DefaultModel: "original"}}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "magpie.json")
	if err := os.WriteFile(path, []byte(`{"model":"p/first","models":["p/first","p/second"],"model_display_names":{"p/second":"Second Model"},"base_url":"http://localhost:12345/v1","api_key":"magpie-test-secret"}`), 0600); err != nil {
		t.Fatal(err)
	}
	service := NewService(ServiceOptions{WorkspaceRoot: t.TempDir(), AgentDir: dir})
	h := &handler{service: service, providers: store}
	h.applySavedProvider(service)
	return h, path
}

func TestMagpieProviderSettingsAndSelection(t *testing.T) {
	h, path := magpieSettingsFixture(t)
	before, _ := os.ReadFile(h.providers.Path())
	connection, _ := os.ReadFile(path)
	getSettings := func() providerSettingsResponse {
		t.Helper()
		w := httptest.NewRecorder()
		h.handleProviders(w, httptest.NewRequest(http.MethodGet, "/", nil))
		if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "magpie-test-secret") {
			t.Fatalf("settings: %d %s", w.Code, w.Body.String())
		}
		var result providerSettingsResponse
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	settings := getSettings()
	if settings.ActiveProvider != "magpie" || len(settings.Providers) != 2 {
		t.Fatalf("settings = %#v", settings)
	}
	external := settings.Providers[1]
	if external.ManagedBy != "magpie" || external.APIType != string(agent.APITypeOpenAIResponse) || !external.APIKeyConfigured || external.BaseURL != "http://localhost:12345/v1" || external.ModelDisplayNames["p/second"] != "Second Model" {
		t.Fatalf("external provider = %#v", external)
	}
	saved, err := h.service.store.Create(session.CreateOptions{Task: "model selection", Chat: true})
	if err != nil {
		t.Fatal(err)
	}
	selectModel := func(provider, model string, status int) {
		t.Helper()
		w := httptest.NewRecorder()
		body := fmt.Sprintf(`{"provider_id":%q,"model":%q,"session_id":%q}`, provider, model, saved.Meta().ID)
		h.handleProviderSelection(w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)))
		if w.Code != status {
			t.Fatalf("selection: %d %s", w.Code, w.Body.String())
		}
	}
	selectModel("magpie", "p/second", http.StatusOK)
	if info := h.service.RuntimeLLMInfoForSession(saved.Meta().ID); info.ProviderID != "magpie" || info.APIType != agent.APITypeOpenAIResponse || info.Model != "p/second" || info.ModelDisplayName != "Second Model" {
		t.Fatalf("runtime = %#v", info)
	}
	selectModel("magpie", "unknown", http.StatusBadRequest)
	if settings = getSettings(); settings.Providers[1].DefaultModel != "p/second" {
		t.Fatal("model choice not reflected")
	}
	after, _ := os.ReadFile(h.providers.Path())
	afterConnection, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) || !bytes.Equal(connection, afterConnection) {
		t.Fatal("selection changed persisted connection")
	}
	selectModel("saved", "original", http.StatusOK)
	if getSettings().ActiveProvider != "saved" {
		t.Fatal("settings still mark Magpie active")
	}
	selectModel("magpie", "p/first", http.StatusOK)
	if err := os.WriteFile(path, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	h.applySavedProvider(h.service)
	settings = getSettings()
	if settings.ActiveProvider != "saved" || len(settings.Providers) != 1 {
		t.Fatalf("disconnected: %#v", settings)
	}
	selectModel("magpie", "p/first", http.StatusBadRequest)
}

func TestMagpieProviderSettingsPreserveExternalConfig(t *testing.T) {
	for _, tc := range []struct {
		name, body, active  string
		status, storedCount int
	}{
		{"save ordinary", `{"active_provider":"saved","providers":[{"id":"saved","name":"Renamed","api_type":"openai","models":["original"],"default_model":"original"}]}`, "saved", http.StatusOK, 1},
		{"delete ordinary", `{"active_provider":"magpie","providers":[]}`, "magpie", http.StatusOK, 0},
		{"reject managed edit", `{"active_provider":"magpie","providers":[{"id":"magpie","name":"Overwrite","api_type":"openai","models":["other"],"default_model":"other"}]}`, "magpie", http.StatusBadRequest, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, path := magpieSettingsFixture(t)
			before, _ := os.ReadFile(path)
			w := httptest.NewRecorder()
			h.handleProviders(w, httptest.NewRequest(http.MethodPut, "/", strings.NewReader(tc.body)))
			if w.Code != tc.status {
				t.Fatalf("save: %d %s", w.Code, w.Body.String())
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(before, after) || strings.Contains(w.Body.String(), "magpie-test-secret") {
				t.Fatal("external connection changed or key exposed")
			}
			config, err := h.providers.Load()
			if err != nil {
				t.Fatal(err)
			}
			if len(config.Providers) != tc.storedCount || config.ActiveProvider == "magpie" {
				t.Fatalf("external provider persisted: %#v", config)
			}
			if h.service.RuntimeLLMInfo().ProviderID != tc.active {
				t.Fatal("wrong active provider")
			}
			if tc.status == http.StatusOK {
				var result providerSettingsResponse
				if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				if result.ActiveProvider != tc.active || result.Providers[len(result.Providers)-1].ManagedBy != "magpie" {
					t.Fatalf("save lost managed provider: %#v", result)
				}
			}
		})
	}
}

func TestMagpieProviderSettingsPreserveShadowedProvider(t *testing.T) {
	h, path := magpieSettingsFixture(t)
	secret, err := h.providers.EncryptAPIKey("original-private-key")
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.providers.UpdateProviders(func(active *string, providers *[]globalconfig.Provider) error {
		*providers = append(*providers, globalconfig.Provider{ID: "magpie", Name: "Old ordinary provider", APIType: "openai", Models: []string{"legacy"}, DefaultModel: "legacy", APIKey: secret})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.handleProviders(w, httptest.NewRequest(http.MethodPut, "/", strings.NewReader(`{"active_provider":"magpie","providers":[]}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	var response providerSettingsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Providers) != 1 || response.Providers[0].ManagedBy != "magpie" {
		t.Fatalf("duplicate provider IDs: %#v", response)
	}
	config, err := h.providers.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Providers) != 1 || config.Providers[0].DefaultModel != "legacy" {
		t.Fatalf("original provider lost: %#v", config)
	}
	key, err := h.providers.DecryptAPIKey(config.Providers[0].APIKey)
	if err != nil || key != "original-private-key" {
		t.Fatal("original provider key lost")
	}
	if err := os.WriteFile(path, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	h.applySavedProvider(h.service)
	if h.service.RuntimeLLMInfo().Model != "legacy" {
		t.Fatal("original provider not restored")
	}
}
