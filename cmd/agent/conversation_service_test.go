package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"bqagent/internal/agent"
	"bqagent/internal/globalconfig"
)

func TestRuntimeConfigFromSourcesMagpieConnectAndDisconnect(t *testing.T) {
	for _, savedProvider := range []bool{false, true} {
		t.Run(map[bool]string{false: "environment", true: "saved provider"}[savedProvider], func(t *testing.T) {
			dir := t.TempDir()
			store := globalconfig.NewStore(dir)
			if savedProvider {
				secret, err := store.EncryptAPIKey("saved-key")
				if err != nil {
					t.Fatal(err)
				}
				if err := store.Save(globalconfig.Config{ActiveProvider: "saved", Providers: []globalconfig.Provider{{ID: "saved", Name: "Saved", APIType: "anthropic", BaseURL: "https://saved.example/v1", DefaultModel: "saved-model", Models: []string{"saved-model"}, APIKey: secret}}}); err != nil {
					t.Fatal(err)
				}
			}
			env := map[string]string{"LLM_API_KEY": "env-key", "LLM_MODEL": "env-model", "LLM_API_TYPE": "anthropic"}
			getenv := func(key string) string { return env[key] }
			before := runtimeConfigFromSources(getenv, dir)
			path := filepath.Join(dir, "magpie.json")
			if err := os.WriteFile(path, []byte(`{"model":"p/m","base_url":"http://localhost:12345/v1","api_key":"magpie-test-key","models":["p/m","p/other"],"model_display_names":{"p/m":"Magpie model"}}`), 0600); err != nil {
				t.Fatal(err)
			}
			connected := runtimeConfigFromSources(getenv, dir)
			if connected.ProviderID != "magpie" || connected.APIType != agent.APITypeOpenAIResponse || connected.APIKey != "magpie-test-key" || connected.BaseURL != "http://localhost:12345/v1" || connected.Model != "p/m" || len(connected.Models) != 2 || connected.ModelDisplayNames.Name(connected.Model) != "Magpie model" {
				t.Fatal("Magpie did not override the saved provider/environment")
			}
			// Magpie disconnects by removing the keys it originally introduced.
			if err := os.WriteFile(path, []byte(`{}`), 0600); err != nil {
				t.Fatal(err)
			}
			after := runtimeConfigFromSources(getenv, dir)
			if after.ProviderID != before.ProviderID || after.APIKey != before.APIKey || after.Model != before.Model || after.APIType != before.APIType || after.BaseURL != before.BaseURL {
				t.Fatal("disconnect did not restore the original runtime configuration")
			}
		})
	}
}

func TestRuntimeConfigFromSourcesPrefersSavedProvider(t *testing.T) {
	agentDir := filepath.Join(t.TempDir(), ".agent")
	store := globalconfig.NewStore(agentDir)
	secret, err := store.EncryptAPIKey("saved-key")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(globalconfig.Config{ActiveProvider: "saved", Providers: []globalconfig.Provider{{ID: "saved", Name: "Saved", APIType: "anthropic", BaseURL: "https://saved.example/v1", Models: []string{"claude-saved"}, ModelDisplayNames: map[string]string{"claude-saved": "Claude Saved"}, DefaultModel: "claude-saved", APIKey: secret}}}); err != nil {
		t.Fatal(err)
	}
	environment := map[string]string{"LLM_API_KEY": "env-key", "LLM_MODEL": "env-model"}
	config := runtimeConfigFromSources(func(key string) string { return environment[key] }, agentDir)
	if config.ProviderID != "saved" || config.APIType != agent.APITypeAnthropic || config.APIKey != "saved-key" || config.Model != "claude-saved" || config.BaseURL != "https://saved.example/v1" || config.ModelDisplayNames.Name(config.Model) != "Claude Saved" {
		t.Fatalf("runtime config = %#v", config)
	}
}

func TestGroupExternalAgentTimeoutFromEnv(t *testing.T) {
	if got := groupExternalAgentTimeoutFromEnv(func(string) string { return "45s" }); got != 45*time.Second {
		t.Fatalf("configured timeout = %s, want 45s", got)
	}
	if got := groupExternalAgentTimeoutFromEnv(func(string) string { return "invalid" }); got != 10*time.Minute {
		t.Fatalf("invalid timeout fallback = %s, want 10m", got)
	}
}
