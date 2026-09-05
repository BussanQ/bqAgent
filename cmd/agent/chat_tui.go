package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	"bqagent/internal/agent"
	"bqagent/internal/globalconfig"
	appserver "bqagent/internal/server"
	apptui "bqagent/internal/tui"
	"bqagent/internal/workspace"
	"golang.org/x/term"
)

func runChat(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string, ws *workspace.Workspace, systemPrompt, initialTask string, options cliOptions) int {
	if !interactiveTerminal(stdin, stdout, getenv) {
		return runChatLegacy(ctx, stdin, stdout, stderr, getenv, ws, systemPrompt, initialTask, options)
	}
	service, externalBroker := newConversationService(ctx, getenv, ws, systemPrompt, options.plan, nil)
	defer externalBroker.Close()
	backend := &tuiBackend{service: service, workspace: ws}
	noColor := getenv != nil && strings.TrimSpace(getenv("NO_COLOR")) != ""
	err := apptui.Run(backend, apptui.Config{
		Context:     ctx,
		Input:       stdin,
		Output:      stdout,
		Workspace:   ws.Root,
		AgentDir:    ws.AgentDir(),
		InitialTask: initialTask,
		SessionID:   effectiveSessionID(options),
		NoColor:     noColor,
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func interactiveTerminal(stdin io.Reader, stdout io.Writer, getenv func(string) string) bool {
	termName := ""
	if getenv != nil {
		termName = strings.TrimSpace(getenv("TERM"))
	}
	input, inputOK := stdin.(interface{ Fd() uintptr })
	output, outputOK := stdout.(interface{ Fd() uintptr })
	inputTTY := inputOK && term.IsTerminal(int(input.Fd()))
	outputTTY := outputOK && term.IsTerminal(int(output.Fd()))
	return shouldUseTUI(inputTTY, outputTTY, termName)
}

func shouldUseTUI(inputTTY, outputTTY bool, termName string) bool {
	return inputTTY && outputTTY && !strings.EqualFold(strings.TrimSpace(termName), "dumb")
}

type tuiBackend struct {
	service   *appserver.Service
	workspace *workspace.Workspace
}

func (backend *tuiBackend) RunTurn(ctx context.Context, sessionID, message string, events apptui.TurnEvents) (apptui.TurnResult, error) {
	response, err := backend.service.HandleTurnWithOptions(ctx, appserver.TurnRequest{
		SessionID: sessionID,
		Message:   message,
	}, appserver.TurnOptions{
		OutputWriter:   eventWriter{write: events.Progress},
		TokenSink:      eventWriter{write: events.Token},
		ProgressWriter: eventWriter{write: events.Progress},
		ToolEventSink:  toolEventSink{emit: events.Tool},
		Stream:         true,
	})
	result := apptui.TurnResult{
		SessionID: response.SessionID,
		Reply:     response.Reply,
		Model:     response.Model,
		Mode:      string(response.Mode),
		Streamed:  response.Streamed,
	}
	if response.Generation != nil {
		result.Metrics = &apptui.GenerationMetrics{
			FirstTokenLatencyMS:  response.Generation.FirstTokenLatencyMS,
			PromptTokens:         response.Generation.PromptTokens,
			CachedPromptTokens:   response.Generation.CachedPromptTokens,
			CacheUsageAvailable:  response.Generation.CacheUsageAvailable,
			CompletionTokens:     response.Generation.CompletionTokens,
			ReasoningTokens:      response.Generation.ReasoningTokens,
			GenerationDurationMS: response.Generation.GenerationDurationMS,
			TokensPerSecond:      response.Generation.TokensPerSecond,
		}
		if cache := response.Generation.CacheMetrics; cache != nil {
			result.Metrics.CacheMetrics = &apptui.CacheMetrics{
				Available:           cache.Available,
				Calls:               cache.Calls,
				InputTokens:         cache.InputTokens,
				CacheReadTokens:     cache.CacheReadTokens,
				CacheWriteTokens:    cache.CacheWriteTokens,
				UncachedInputTokens: cache.UncachedInputTokens,
				HitRate:             cache.HitRate,
			}
		}
	}
	return result, err
}

func (backend *tuiBackend) RuntimeInfo(sessionID string) apptui.RuntimeInfo {
	info := backend.service.RuntimeLLMInfoForSession(sessionID)
	return apptui.RuntimeInfo{Provider: info.ProviderID, APIType: string(info.APIType), Model: info.Model, Mode: string(info.Mode)}
}

func (backend *tuiBackend) History(sessionID string, budget int) (apptui.History, error) {
	history, err := backend.service.ConversationHistory(sessionID, budget)
	if err != nil {
		return apptui.History{}, err
	}
	result := apptui.History{ID: history.ID, Title: history.Title, Omitted: history.Omitted, Messages: make([]apptui.HistoryMessage, 0, len(history.Messages))}
	for _, message := range history.Messages {
		tools := make([]apptui.ToolEvent, 0, len(message.Tools))
		for _, tool := range message.Tools {
			tools = append(tools, apptui.ToolEvent{
				ID: tool.ID, Name: tool.Name, Status: tool.Status,
				Arguments: tool.Arguments, Preview: tool.Result, Truncated: tool.Truncated,
			})
		}
		result.Messages = append(result.Messages, apptui.HistoryMessage{Role: message.Role, Content: message.Content, Tools: tools})
	}
	return result, nil
}

func (backend *tuiBackend) Commands() []apptui.Command {
	commands := []apptui.Command{{Name: "/model", Description: "查看或切换模型"}, {Name: "/skill", Description: "使用 Skill 或 alias"}}
	for _, option := range backend.service.ModelOptions() {
		description := option.ID
		if option.Alias == "default" {
			description = "恢复默认模型 " + option.ID
		}
		commands[0].Arguments = append(commands[0].Arguments, apptui.CommandArgument{Value: option.Alias, Description: description})
	}
	if skills, err := backend.workspace.LoadSkills(); err == nil {
		for _, skill := range skills {
			commands[1].Arguments = append(commands[1].Arguments, apptui.CommandArgument{Value: skill.ID, Description: skill.Description})
			for _, alias := range skill.Aliases {
				commands[1].Arguments = append(commands[1].Arguments, apptui.CommandArgument{Value: alias, Description: "alias → " + skill.ID})
			}
		}
	}
	return commands
}

func (backend *tuiBackend) ProviderSettings(context.Context) (apptui.ProviderSettings, error) {
	store := globalconfig.NewStore(backend.workspace.AgentDir())
	config, err := store.Load()
	if err != nil {
		return apptui.ProviderSettings{}, err
	}
	settings := apptui.ProviderSettings{ActiveProvider: config.ActiveProvider, Providers: make([]apptui.Provider, 0, len(config.Providers))}
	for _, provider := range config.Providers {
		settings.Providers = append(settings.Providers, apptui.Provider{
			ID: provider.ID, Name: provider.Name, APIType: provider.APIType, BaseURL: provider.BaseURL,
			Models: append([]string(nil), provider.Models...), DefaultModel: provider.DefaultModel,
			APIKeyConfigured: provider.APIKey.Ciphertext != "",
		})
	}
	return settings, nil
}

func (backend *tuiBackend) DiscoverProviderModels(ctx context.Context, input apptui.ProviderInput) ([]string, error) {
	apiKey := strings.TrimSpace(input.APIKey)
	if apiKey == "" {
		store := globalconfig.NewStore(backend.workspace.AgentDir())
		config, err := store.Load()
		if err != nil {
			return nil, err
		}
		providerID := strings.TrimSpace(input.OriginalID)
		if providerID == "" {
			providerID = strings.TrimSpace(input.ID)
		}
		for _, provider := range config.Providers {
			if provider.ID != providerID {
				continue
			}
			apiKey, err = store.DecryptAPIKey(provider.APIKey)
			if err != nil {
				return nil, err
			}
			break
		}
	}
	return appserver.FetchProviderModels(ctx, string(agent.NormalizeAPIType(input.APIType)), strings.TrimSpace(input.BaseURL), apiKey)
}

func (backend *tuiBackend) SaveProvider(ctx context.Context, sessionID string, input apptui.ProviderInput) (apptui.RuntimeInfo, error) {
	if err := ctx.Err(); err != nil {
		return apptui.RuntimeInfo{}, err
	}
	store := globalconfig.NewStore(backend.workspace.AgentDir())
	var provider globalconfig.Provider
	_, err := store.UpdateProviders(func(active *string, providers *[]globalconfig.Provider) error {
		providerIndex := -1
		lookupID := strings.TrimSpace(input.OriginalID)
		if lookupID == "" {
			lookupID = strings.TrimSpace(input.ID)
		}
		secret := globalconfig.Secret{}
		for index, provider := range *providers {
			if provider.ID == lookupID {
				providerIndex = index
				secret = provider.APIKey
				break
			}
		}
		if apiKey := strings.TrimSpace(input.APIKey); apiKey != "" {
			var err error
			secret, err = store.EncryptAPIKey(apiKey)
			if err != nil {
				return err
			}
		}
		models := cleanTUIProviderModels(input.Models)
		defaultModel := strings.TrimSpace(input.DefaultModel)
		if !containsTUIProviderModel(models, defaultModel) {
			if len(models) > 0 {
				defaultModel = models[0]
			} else {
				defaultModel = ""
			}
		}
		provider = globalconfig.Provider{
			ID: strings.TrimSpace(input.ID), Name: strings.TrimSpace(input.Name),
			APIType: string(agent.NormalizeAPIType(input.APIType)), BaseURL: strings.TrimSpace(input.BaseURL),
			Models: models, DefaultModel: defaultModel, APIKey: secret,
		}
		if providerIndex >= 0 {
			(*providers)[providerIndex] = provider
		} else {
			(*providers) = append((*providers), provider)
		}
		*active = provider.ID
		return nil
	})
	if err != nil {
		return apptui.RuntimeInfo{}, err
	}
	apiKey, err := store.DecryptAPIKey(provider.APIKey)
	if err != nil {
		return apptui.RuntimeInfo{}, err
	}
	backend.service.ConfigureLLM(provider.ID, agent.NormalizeAPIType(provider.APIType), apiKey, provider.BaseURL, provider.DefaultModel, provider.Models)
	if strings.TrimSpace(sessionID) != "" {
		if err := backend.service.SelectSessionModel(sessionID, provider.DefaultModel); err != nil {
			return apptui.RuntimeInfo{}, err
		}
	}
	return backend.RuntimeInfo(sessionID), nil
}

func cleanTUIProviderModels(values []string) []string {
	models := make([]string, 0, len(values))
	seen := make(map[string]bool)
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		models = append(models, value)
	}
	return models
}

func containsTUIProviderModel(models []string, target string) bool {
	for _, model := range models {
		if model == target {
			return true
		}
	}
	return false
}

type eventWriter struct {
	write func(string)
}

func (writer eventWriter) Write(content []byte) (int, error) {
	if writer.write != nil && len(content) > 0 {
		writer.write(string(content))
	}
	return len(content), nil
}

type toolEventSink struct {
	emit func(apptui.ToolEvent)
}

func (sink toolEventSink) EmitToolEvent(event agent.ToolEvent) {
	if sink.emit != nil {
		sink.emit(apptui.ToolEvent{
			Kind:       event.Kind,
			Seq:        event.Seq,
			ID:         event.ID,
			Name:       event.Name,
			Status:     event.Status,
			Arguments:  event.Arguments,
			Preview:    event.Preview,
			DurationMS: event.DurationMS,
			Truncated:  event.Truncated,
		})
	}
}
