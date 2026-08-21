package main

import (
	"path/filepath"
	"testing"

	"bqagent/internal/agent"
	"bqagent/internal/providerconfig"
)

func TestRuntimeConfigFromSourcesPrefersSavedProvider(t *testing.T) {
	agentDir := filepath.Join(t.TempDir(), ".agent")
	store := providerconfig.NewStore(agentDir)
	secret, err := store.EncryptAPIKey("saved-key")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(providerconfig.Config{ActiveProvider: "saved", Providers: []providerconfig.Provider{{ID: "saved", Name: "Saved", APIType: "anthropic", BaseURL: "https://saved.example/v1", Models: []string{"claude-saved"}, DefaultModel: "claude-saved", APIKey: secret}}}); err != nil {
		t.Fatal(err)
	}
	environment := map[string]string{"LLM_API_KEY": "env-key", "LLM_MODEL": "env-model"}
	config := runtimeConfigFromSources(func(key string) string { return environment[key] }, agentDir)
	if config.APIType != agent.APITypeAnthropic || config.APIKey != "saved-key" || config.Model != "claude-saved" || config.BaseURL != "https://saved.example/v1" {
		t.Fatalf("runtime config = %#v", config)
	}
}
