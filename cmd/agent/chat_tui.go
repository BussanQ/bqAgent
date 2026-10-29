package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"bqagent/internal/agent"
	"bqagent/internal/globalconfig"
	"bqagent/internal/mcp"
	"bqagent/internal/modelcatalog"
	appserver "bqagent/internal/server"
	apptui "bqagent/internal/tui"
	"bqagent/internal/workspace"
	"golang.org/x/term"
)

const tuiTerminalRequired = "TUI 需要标准输入和标准输出连接交互终端（TTY），且 TERM 不能为 dumb；非交互环境请提供任务描述，例如 bqagent \"分析项目\"，或用 --help 查看帮助。"

func runTUI(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string, ws *workspace.Workspace, systemPrompt string, options cliOptions) int {
	if !interactiveTerminal(stdin, stdout, getenv) {
		fmt.Fprintln(stderr, tuiTerminalRequired)
		return 1
	}
	defer redirectStandardLog(filepath.Join(ws.AgentDir(), "tui", "tui.log"))()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	herdrReporter := newHerdrReporter(getenv, options)
	defer herdrReporter.Close()
	service, externalBroker := newConversationService(ctx, getenv, ws, systemPrompt, nil)
	defer func() { cancel(); _ = externalBroker.Close() }()
	backend := &tuiBackend{service: service, workspace: ws, effort: agent.DefaultReasoningEffort}
	noColor := getenv != nil && strings.TrimSpace(getenv("NO_COLOR")) != ""
	err := apptui.Run(backend, apptui.Config{
		ActivitySink: herdrReporter.Report,
		Context:      ctx,
		Input:        stdin,
		Output:       stdout,
		Workspace:    ws.Root,
		AgentDir:     ws.AgentDir(),
		SessionID:    effectiveSessionID(options),
		NoColor:      noColor,
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

// redirectStandardLog keeps background log.Printf output (session upkeep,
// memory append) off the terminal while the TUI owns it: stray writes break the
// inline renderer's line accounting and leave duplicated lines in scrollback.
// The returned func restores the previous output.
func redirectStandardLog(path string) func() {
	previous := log.Writer()
	var file *os.File
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
		file, _ = os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	}
	if file != nil {
		log.SetOutput(file)
	} else {
		log.SetOutput(io.Discard)
	}
	return func() {
		log.SetOutput(previous)
		if file != nil {
			_ = file.Close()
		}
	}
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
	effortMu  sync.RWMutex
	effort    agent.ReasoningEffort
}

// SupportsTurnInput reports that the server consumes turn.InputQueue from the
// turn context, so the TUI may steer or follow up a running turn.
func (backend *tuiBackend) SupportsTurnInput() bool { return true }

func (backend *tuiBackend) RunTurn(ctx context.Context, sessionID, message string, events apptui.TurnEvents) (apptui.TurnResult, error) {
	return backend.RunTurnWithAttachments(ctx, sessionID, message, apptui.TurnAttachments{}, events)
}

// RunTurnWithAttachments sends pasted terminal images and @-mentioned
// workspace files through the same attachment path the WebUI uses.
func (backend *tuiBackend) RunTurnWithAttachments(ctx context.Context, sessionID, message string, attachments apptui.TurnAttachments, events apptui.TurnEvents) (apptui.TurnResult, error) {
	if err := ctx.Err(); err != nil {
		return apptui.TurnResult{}, err
	}
	if len(attachments.Images) == 0 && len(attachments.Files) == 0 {
		if reply, handled, err := backend.handleEffortCommand(message); handled {
			return apptui.TurnResult{SessionID: sessionID, Reply: reply}, err
		}
	}
	var images []agent.ImageAttachment
	for _, image := range attachments.Images {
		images = append(images, agent.ImageAttachment{MIMEType: image.MIMEType, Data: image.Data})
	}
	var files []appserver.FileAttachment
	for _, file := range attachments.Files {
		files = append(files, appserver.FileAttachment{Path: file})
	}
	if events.Usage != nil {
		ctx = agent.WithLiveTokenUsage(ctx, func(usage agent.LiveTokenUsage) {
			events.Usage(apptui.TokenUsage{
				InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens,
				InputEstimated: usage.InputEstimated, OutputEstimated: usage.OutputEstimated, Calls: usage.Calls,
			})
		})
	}
	ctx = agent.WithLiveSpeed(ctx, events.Speed)
	ctx = agent.WithContextUsage(ctx, events.Context)
	response, err := backend.service.HandleTurnWithOptions(ctx, appserver.TurnRequest{
		SessionID:       sessionID,
		Message:         message,
		Images:          images,
		Files:           files,
		ReasoningEffort: backend.currentEffort(),
	}, appserver.TurnOptions{
		SessionSink:    events.Session,
		MessageSink:    events.Message,
		OutputWriter:   eventWriter{write: events.Progress},
		TokenSink:      eventWriter{write: events.Token},
		ProgressWriter: eventWriter{write: events.Progress},
		ToolEventSink:  toolEventSink{emit: events.Tool},
		Stream:         true,
	})
	result := apptui.TurnResult{
		TurnID: response.TurnID, Outcome: response.Outcome,
		SessionID:        response.SessionID,
		Reply:            response.Reply,
		Model:            response.Model,
		ModelDisplayName: backend.service.ModelDisplayName(response.Model),
		Mode:             string(response.Mode),
		Streamed:         response.Streamed,
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
	return apptui.RuntimeInfo{Provider: info.ProviderID, APIType: string(info.APIType), Model: info.Model, ModelDisplayName: info.ModelDisplayName, Mode: string(info.Mode)}
}

func (backend *tuiBackend) History(sessionID string, budget int) (apptui.History, error) {
	history, err := backend.service.ConversationHistory(sessionID, budget)
	if err != nil {
		return apptui.History{}, err
	}
	result := apptui.History{ID: history.ID, Title: history.Title, Task: history.Task, Omitted: history.Omitted, Messages: make([]apptui.HistoryMessage, 0, len(history.Messages))}
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
	commands := []apptui.Command{{Name: "/model", Description: "查看或切换模型"}, {Name: "/skill", Description: "使用 Skill 或 alias"}, {
		Name: "/effort", Description: "查看或设置模型推理努力程度", Arguments: []apptui.CommandArgument{
			{Value: "auto", Description: "自动（模型默认）"},
			{Value: string(agent.ReasoningEffortLow), Description: "低"},
			{Value: string(agent.ReasoningEffortMedium), Description: "中"},
			{Value: string(agent.ReasoningEffortHigh), Description: "高"},
			{Value: string(agent.ReasoningEffortMax), Description: "最高"},
		},
	}}
	info := backend.RuntimeInfo("")
	provider := info.Provider
	if provider == "" {
		provider = info.APIType
	}
	for _, option := range backend.service.ModelOptions() {
		label := modelcatalog.QualifiedName(provider, option.ID, option.DisplayName)
		description := ""
		if option.DisplayName == option.ID && option.Alias != option.ID {
			description = "别名 " + option.Alias
		}
		if option.Alias == "default" {
			description = "恢复默认模型 " + label
			label = "default"
		}
		commands[0].Arguments = append(commands[0].Arguments, apptui.CommandArgument{Value: option.Alias, Label: label, Description: description})
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
			Models: append([]string(nil), provider.Models...), ModelDisplayNames: provider.ModelDisplayNames.ForModels(provider.Models), DefaultModel: provider.DefaultModel,
			APIKeyConfigured: provider.APIKey.Ciphertext != "",
		})
	}
	return settings, nil
}

func (backend *tuiBackend) DiscoverProviderModels(ctx context.Context, input apptui.ProviderInput) ([]modelcatalog.Model, error) {
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
			Models: models, ModelDisplayNames: input.ModelDisplayNames.ForModels(models), DefaultModel: defaultModel, APIKey: secret,
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
	backend.service.ConfigureLLM(provider.ID, agent.NormalizeAPIType(provider.APIType), apiKey, provider.BaseURL, provider.DefaultModel, provider.Models, provider.ModelDisplayNames)
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

func (backend *tuiBackend) MCPStatus() []mcp.ServerStatus {
	return backend.service.MCPStatus()
}
