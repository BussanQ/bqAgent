package server

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"bqagent/internal/agent"
	"bqagent/internal/modelcatalog"
	"bqagent/internal/tools"
)

type snapshotTurnClient struct {
	call     int
	models   []string
	prompts  []string
	switched func()
}

func (client *snapshotTurnClient) CreateChatCompletion(_ context.Context, model string, messages []map[string]any, _ []tools.Definition) (agent.AssistantMessage, error) {
	client.models = append(client.models, model)
	client.prompts = append(client.prompts, fmt.Sprint(messages))
	client.call++
	if client.call == 1 {
		client.switched()
		return agent.AssistantMessage{Role: "assistant", ToolCalls: []agent.ToolCall{{ID: "read", Type: "function", Function: agent.FunctionCall{Name: "read_file", Arguments: `{"path":"file"}`}}}}, nil
	}
	return agent.AssistantMessage{Role: "assistant", Content: "done"}, nil
}
func (client *snapshotTurnClient) CreateChatCompletionStream(ctx context.Context, model string, messages []map[string]any, definitions []tools.Definition, _ func(string)) (agent.AssistantMessage, error) {
	return client.CreateChatCompletion(ctx, model, messages, definitions)
}

func TestProviderSwitchPinsActiveTurnSnapshot(t *testing.T) {
	client := &snapshotTurnClient{}
	service := NewService(ServiceOptions{WorkspaceRoot: t.TempDir(), ProviderID: "old", APIType: agent.APITypeOpenAI, Model: "old-model", Client: client,
		Functions: map[string]tools.Function{"read_file": func(context.Context, map[string]any) (string, error) { return "content", nil }},
	})
	client.switched = func() {
		service.ConfigureLLM("new", agent.APITypeAnthropic, "key", "http://new.invalid", "new-model", []string{"new-model"}, nil)
	}
	response, err := service.HandleTurn(context.Background(), TurnRequest{Message: "read file"})
	if err != nil {
		t.Fatal(err)
	}
	if len(client.models) != 2 || response.Model != "old-model" || response.APIType != agent.APITypeOpenAI {
		t.Fatalf("models=%v response=%+v", client.models, response)
	}
	for i, model := range client.models {
		if model != "old-model" || strings.Contains(client.prompts[i], "new-model") {
			t.Fatalf("mixed configuration: model=%s prompt=%s", model, client.prompts[i])
		}
	}
	next := service.withLLMSnapshot()
	if next.model != "new-model" || next.apiType != agent.APITypeAnthropic || next.client == client {
		t.Fatal("next turn did not get new configuration")
	}
}

func TestProviderSwitchDuringPromptConstructionUsesPinnedClient(t *testing.T) {
	oldClient, newClient := &modelRecordingClient{}, &modelRecordingClient{}
	var service *Service
	switched := false
	service = NewService(ServiceOptions{
		WorkspaceRoot: t.TempDir(), Client: oldClient, Model: "old-model", APIType: agent.APITypeOpenAI,
		SystemPromptBuilder: func() (string, error) {
			if !switched {
				switched = true
				service.ConfigureLLM("new", agent.APITypeAnthropic, "", "http://new.invalid", "new-model", nil, nil)
				// Keep this a local unit test even if a regression uses the new
				// client during the first turn.
				config := *service.llm.Load()
				config.client = newClient
				service.llm.Store(&config)
			}
			return "system prompt", nil
		},
	})
	first, err := service.HandleTurn(context.Background(), TurnRequest{Message: "first"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Model != "old-model" || first.APIType != agent.APITypeOpenAI || len(oldClient.models) != 1 || len(newClient.models) != 0 {
		t.Fatalf("first turn mixed configurations: %+v old=%v new=%v", first, oldClient.models, newClient.models)
	}
	second, err := service.HandleTurn(context.Background(), TurnRequest{Message: "second"})
	if err != nil {
		t.Fatal(err)
	}
	if second.Model != "new-model" || second.APIType != agent.APITypeAnthropic || len(newClient.models) != 1 || newClient.models[0] != "new-model" {
		t.Fatalf("next turn missed new configuration: %+v models=%v", second, newClient.models)
	}
}

func TestProviderSnapshotConcurrentReadsAndSwitches(t *testing.T) {
	service := NewService(ServiceOptions{WorkspaceRoot: t.TempDir()})
	configure := func(id string, api agent.APIType) {
		service.ConfigureLLM(id, api, "key", "http://"+id+".invalid", id, []string{id}, modelcatalog.Names{id: id + " label"})
	}
	configure("old", agent.APITypeOpenAI)
	frozen := service.withLLMSnapshot()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			configure("old", agent.APITypeOpenAI)
			configure("new", agent.APITypeAnthropic)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			view := service.withLLMSnapshot()
			info := view.RuntimeLLMInfo()
			expected := agent.APITypeOpenAI
			if info.ProviderID == "new" {
				expected = agent.APITypeAnthropic
			}
			if info.Model != info.ProviderID || info.APIType != expected || info.ModelDisplayName != info.Model+" label" || view.clientOptions.ProviderID != info.ProviderID {
				t.Errorf("mixed snapshot: %+v", info)
				return
			}
			options := view.ModelOptions()
			if len(options) != 2 || options[1].ID != info.Model {
				t.Errorf("mixed model catalog: %+v %+v", info, options)
				return
			}
			prompt, _, err := view.currentPromptSnapshot(info.Model)
			if err != nil || !strings.Contains(prompt.Combined(), "API type: "+string(expected)) {
				t.Errorf("mixed prompt: %s %v", prompt.Combined(), err)
				return
			}
			_ = service.RuntimeLLMInfo()
			_ = service.ModelOptions()
			_ = service.ModelDisplayName("old")
		}
	}()
	wg.Wait()
	if frozen.RuntimeLLMInfo().Model != "old" {
		t.Fatal("published snapshot mutated")
	}
}

func TestProviderSnapshotCopiesModelDisplayNames(t *testing.T) {
	service := NewService(ServiceOptions{WorkspaceRoot: t.TempDir()})
	names := modelcatalog.Names{"model": "original"}
	models := []string{"model"}
	service.ConfigureLLM("p", agent.APITypeOpenAI, "", "http://test.invalid", "model", models, names)
	names["model"] = "changed"
	models[0] = "changed"
	if service.ModelDisplayName("model") != "original" || service.ModelOptions()[1].ID != "model" {
		t.Fatal("snapshot aliases caller-owned collections")
	}
}
