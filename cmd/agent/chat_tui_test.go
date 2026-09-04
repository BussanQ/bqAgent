package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"bqagent/internal/providerconfig"
	appserver "bqagent/internal/server"
	apptui "bqagent/internal/tui"
	"bqagent/internal/workspace"
)

func TestShouldUseTUI(t *testing.T) {
	for _, test := range []struct {
		inputTTY  bool
		outputTTY bool
		term      string
		want      bool
	}{
		{true, true, "xterm-256color", true},
		{false, true, "xterm-256color", false},
		{true, false, "xterm-256color", false},
		{true, true, "dumb", false},
	} {
		if got := shouldUseTUI(test.inputTTY, test.outputTTY, test.term); got != test.want {
			t.Fatalf("shouldUseTUI(%v,%v,%q) = %v, want %v", test.inputTTY, test.outputTTY, test.term, got, test.want)
		}
	}
	options, _, err := parseCLI([]string{"--chat", "--stream"})
	if err != nil || !options.chat || !options.stream {
		t.Fatalf("--stream compatibility: options=%#v err=%v", options, err)
	}
}

func TestTUIBackendProviderSaveAndDiscovery(t *testing.T) {
	var authorization string
	providerServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		authorization = request.Header.Get("Authorization")
		writer.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(writer, `{"data":[{"id":"model-z"},{"id":"model-a"}]}`)
	}))
	defer providerServer.Close()

	root := t.TempDir()
	agentDir := filepath.Join(t.TempDir(), ".agent")
	ws := &workspace.Workspace{Root: root, GlobalAgentDir: agentDir}
	service := appserver.NewService(appserver.ServiceOptions{WorkspaceRoot: root, AgentDir: agentDir})
	backend := &tuiBackend{service: service, workspace: ws}

	models, err := backend.DiscoverProviderModels(context.Background(), apptui.ProviderInput{
		ID: "custom", APIType: "openai", BaseURL: providerServer.URL, APIKey: "secret-token",
	})
	if err != nil {
		t.Fatal(err)
	}
	if authorization != "Bearer secret-token" || fmt.Sprint(models) != "[model-a model-z]" {
		t.Fatalf("discovery authorization=%q models=%#v", authorization, models)
	}

	runtime, err := backend.SaveProvider(context.Background(), "", apptui.ProviderInput{
		ID: "custom", Name: "Custom", APIType: "openai", BaseURL: providerServer.URL,
		APIKey: "secret-token", Models: []string{"model-z", "model-a", "model-z"}, DefaultModel: "missing",
	})
	if err != nil {
		t.Fatal(err)
	}
	if runtime.Provider != "custom" || runtime.Model != "model-z" {
		t.Fatalf("runtime = %#v", runtime)
	}
	store := providerconfig.NewStore(agentDir)
	config, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Providers) != 1 || config.ActiveProvider != "custom" || fmt.Sprint(config.Providers[0].Models) != "[model-z model-a]" || config.Providers[0].DefaultModel != "model-z" {
		t.Fatalf("saved config = %#v", config)
	}
	if config.Providers[0].APIKey.Ciphertext == "" {
		t.Fatal("API key was not encrypted")
	}
	settings, err := backend.ProviderSettings(context.Background())
	if err != nil || len(settings.Providers) != 1 || !settings.Providers[0].APIKeyConfigured {
		t.Fatalf("settings = %#v, err=%v", settings, err)
	}
}
