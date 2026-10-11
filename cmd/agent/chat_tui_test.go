package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"bqagent/internal/globalconfig"
	"bqagent/internal/modelcatalog"
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
	if authorization != "Bearer secret-token" || len(models) != 2 || models[0].ID != "model-a" || models[1].ID != "model-z" {
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
	store := globalconfig.NewStore(agentDir)
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

func TestTUIBackendProviderDisplayNames(t *testing.T) {
	ws := &workspace.Workspace{Root: t.TempDir(), GlobalAgentDir: t.TempDir()}
	backend := &tuiBackend{service: appserver.NewService(appserver.ServiceOptions{WorkspaceRoot: ws.Root, AgentDir: ws.AgentDir()}), workspace: ws}
	info, err := backend.SaveProvider(context.Background(), "", apptui.ProviderInput{ID: "p", Name: "P", APIType: "openai", Models: []string{"model-id"}, DefaultModel: "model-id", ModelDisplayNames: map[string]string{"model-id": "Friendly Model"}})
	if err != nil {
		t.Fatal(err)
	}
	if info.Model != "model-id" || info.ModelDisplayName != "Friendly Model" {
		t.Fatalf("runtime = %#v", info)
	}
	settings, err := backend.ProviderSettings(context.Background())
	if err != nil || settings.Providers[0].ModelDisplayNames["model-id"] != "Friendly Model" {
		t.Fatalf("settings = %#v, err=%v", settings, err)
	}
	args := backend.Commands()[0].Arguments
	if len(args) != 2 || args[1].Value != "model-id" || args[1].Label != "p/Friendly Model" {
		t.Fatalf("arguments = %#v", args)
	}
}

func TestTUIModelChoicesKeepProviderAndRequestIdentity(t *testing.T) {
	ws := &workspace.Workspace{Root: t.TempDir(), GlobalAgentDir: t.TempDir()}
	service := appserver.NewService(appserver.ServiceOptions{
		WorkspaceRoot: ws.Root, AgentDir: ws.AgentDir(), ProviderID: "gateway", Model: "plain",
		Models:            []string{"plain", "group/auto-deepseek", "other/deepseek", "short=workbuddy/ai/gpt-6-sol"},
		ModelDisplayNames: modelcatalog.Names{"plain": "Plain", "group/auto-deepseek": "Deepseek", "other/deepseek": "Deepseek"},
	})
	backend := &tuiBackend{service: service, workspace: ws}
	args := backend.Commands()[0].Arguments
	wants := []string{"default", "gateway/Plain", "group/Deepseek", "other/Deepseek", "workbuddy/ai/gpt-6-sol"}
	if len(args) != len(wants) || args[0].Description != "恢复默认模型 gateway/Plain" {
		t.Fatalf("choices=%+v", args)
	}
	for i, want := range wants {
		if args[i].Label != want {
			t.Fatalf("choice %d=%+v want label %q", i, args[i], want)
		}
	}
	if args[2].Value != "group/auto-deepseek" || args[4].Value != "short" {
		t.Fatal("presentation changed selectable request IDs/aliases")
	}
	result, err := backend.RunTurn(context.Background(), "", "/model "+args[2].Value, apptui.TurnEvents{})
	if err != nil {
		t.Fatal(err)
	}
	info := backend.RuntimeInfo(result.SessionID)
	if info.Provider != "gateway" || info.Model != "group/auto-deepseek" || info.ModelDisplayName != "Deepseek" {
		t.Fatalf("selected identity=%+v", info)
	}
}
