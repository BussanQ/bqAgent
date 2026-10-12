package server

import (
	"os"
	"path/filepath"
	"testing"

	"bqagent/internal/agent"
	"bqagent/internal/globalconfig"
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
	service := &Service{agentDir: dir}
	handler := &handler{providers: store}
	handler.applySavedProvider(service)
	if service.providerID != "magpie" || service.model != "p/m" || service.apiType != agent.APITypeOpenAI {
		t.Fatal("HTTP startup replaced the Magpie connection with the saved provider")
	}
	if err := os.WriteFile(path, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	handler.applySavedProvider(service)
	if service.providerID != "saved" || service.model != "original" || service.apiType != agent.APITypeAnthropic {
		t.Fatal("HTTP startup did not restore the saved provider after disconnect")
	}
}
