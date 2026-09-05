package main

import (
	"path/filepath"
	"testing"
	"time"

	"bqagent/internal/agent"
	"bqagent/internal/globalconfig"
)

func TestRuntimeConfigFromSourcesPrefersSavedProvider(t *testing.T) {
	agentDir := filepath.Join(t.TempDir(), ".agent")
	store := globalconfig.NewStore(agentDir)
	secret, err := store.EncryptAPIKey("saved-key")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(globalconfig.Config{ActiveProvider: "saved", Providers: []globalconfig.Provider{{ID: "saved", Name: "Saved", APIType: "anthropic", BaseURL: "https://saved.example/v1", Models: []string{"claude-saved"}, DefaultModel: "claude-saved", APIKey: secret}}}); err != nil {
		t.Fatal(err)
	}
	environment := map[string]string{"LLM_API_KEY": "env-key", "LLM_MODEL": "env-model"}
	config := runtimeConfigFromSources(func(key string) string { return environment[key] }, agentDir)
	if config.APIType != agent.APITypeAnthropic || config.APIKey != "saved-key" || config.Model != "claude-saved" || config.BaseURL != "https://saved.example/v1" {
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
