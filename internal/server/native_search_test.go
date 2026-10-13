package server

import (
	"context"
	"testing"

	"bqagent/internal/agent"
	"bqagent/internal/tools"
)

type nativeSearchRecordingClient struct {
	chatModeRecordingClient
}

func (*nativeSearchRecordingClient) SupportsNativeWebSearch(model string) bool {
	return model == "search-model"
}
func (*nativeSearchRecordingClient) NativeWebSearch(context.Context, string, string) (string, error) {
	return "search result", nil
}

func TestServiceNativeWebSearchFollowsSessionModelAndChatMode(t *testing.T) {
	t.Setenv("SEARCH_API_KEY", "")
	t.Setenv("FIRECRAWL_API_KEY", "")
	for _, mode := range []ChatMode{ChatModeAsk, ChatModeRun} {
		t.Run(string(mode), func(t *testing.T) {
			root := t.TempDir()
			catalog := tools.NewCatalog(tools.Options{WorkspaceRoot: root})
			client := &nativeSearchRecordingClient{}
			service := NewService(ServiceOptions{WorkspaceRoot: root, Client: client, Model: "search-model", Models: []string{"search-model", "plain-model"}, SystemPrompt: "base prompt", ToolDefinitions: catalog.Definitions(), Functions: catalog.Registry()})
			response, err := service.HandleTurn(context.Background(), TurnRequest{Message: "hello", Mode: mode})
			if err != nil {
				t.Fatal(err)
			}
			for i, model := range []string{"plain-model", "search-model"} {
				_, err = service.HandleTurn(context.Background(), TurnRequest{SessionID: response.SessionID, Message: "/model " + model})
				if err != nil {
					t.Fatal(err)
				}
				_, err = service.HandleTurn(context.Background(), TurnRequest{SessionID: response.SessionID, Message: "hello again"})
				if err != nil {
					t.Fatal(err)
				}
				if len(client.definitions) != i+2 {
					t.Fatalf("model calls = %d", len(client.definitions))
				}
			}
			for i, definitions := range client.definitions {
				found := false
				for _, definition := range definitions {
					name := definition.Function.Name
					if name == "web_search" {
						found = true
					}
					if mode == ChatModeAsk && (name == "write_file" || name == "execute_bash") {
						t.Fatal("Ask mode gained write tools")
					}
				}
				if found != (i != 1) {
					t.Fatalf("model call %d: web_search = %v", i, found)
				}
			}
		})
	}
}

func TestServiceNativeWebSearchProviderSwitchPreservesMode(t *testing.T) {
	for _, mode := range []string{"", "auto", "on", "off"} {
		t.Run(mode, func(t *testing.T) {
			options := agent.ClientOptions{NativeWebSearch: mode}
			service := NewService(ServiceOptions{WorkspaceRoot: t.TempDir(), ClientOptions: &options})
			for _, apiType := range []agent.APIType{agent.APITypeOpenAIResponse, agent.APITypeAnthropic, agent.APITypeOpenAI} {
				service.ConfigureLLM("magpie", apiType, "", "http://127.0.0.1:3425/v1", "custom/model", nil, nil)
				want := mode != "off"
				if got := service.client.(*agent.Client).SupportsNativeWebSearch("custom/model"); got != want {
					t.Fatalf("provider switch: protocol=%s mode=%q native search=%v, want %v", apiType, mode, got, want)
				}
			}
			service.ConfigureLLM("plain", agent.APITypeOpenAI, "other-key", "https://other.example/v1", "custom/model", nil, nil)
			if service.client.(*agent.Client).SupportsNativeWebSearch("custom/model") || service.clientOptions.ProviderID != "plain" {
				t.Fatal("Magpie search leaked into another provider after switching")
			}
			service.ConfigureLLM("magpie", agent.APITypeOpenAI, "", "http://127.0.0.1:3425/v1", "custom/model", nil, nil)
			if service.client.(*agent.Client).SupportsNativeWebSearch("custom/model") != (mode != "off") {
				t.Fatal("switching back to Magpie lost search configuration")
			}
		})
	}
}
