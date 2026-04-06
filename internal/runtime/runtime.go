package runtime

import (
	"io"
	"strconv"
	"strings"

	"bqagent/internal/agent"
	"bqagent/internal/tools"
)

type Config struct {
	APIKey                       string
	BaseURL                      string
	Model                        string
	SearchAPIKey                 string
	SearchBaseURL                string
	ContextManagementEnabled     bool
	ContextMaxInputTokens        int
	ContextTargetInputTokens     int
	ContextResponseReserveTokens int
	ContextKeepLastTurns         int
	ContextSummarizationEnabled  bool
	ContextSummaryTriggerTokens  int
	ContextSummaryModel          string
}

type Factory struct {
	Config        Config
	WorkspaceRoot string
	MemoryDir     string
}

type Runtime struct {
	Client        agent.ChatCompletionClient
	Planner       *agent.Planner
	Catalog       tools.Catalog
	Model         string
	WorkspaceRoot string
	Context       agent.ContextConfig
}

func ConfigFromEnv(getenv func(string) string) Config {
	defaults := agent.DefaultContextConfig()
	return Config{
		APIKey:                       getenv("OPENAI_API_KEY"),
		BaseURL:                      getenv("OPENAI_BASE_URL"),
		Model:                        getenv("OPENAI_MODEL"),
		SearchAPIKey:                 getenv("SEARCH_API_KEY"),
		SearchBaseURL:                getenv("SEARCH_BASE_URL"),
		ContextManagementEnabled:     envBool(getenv("CONTEXT_MANAGEMENT_ENABLED"), defaults.Enabled),
		ContextMaxInputTokens:        envInt(getenv("CONTEXT_MAX_INPUT_TOKENS"), defaults.MaxInputTokens),
		ContextTargetInputTokens:     envInt(getenv("CONTEXT_TARGET_INPUT_TOKENS"), defaults.TargetInputTokens),
		ContextResponseReserveTokens: envInt(getenv("CONTEXT_RESPONSE_RESERVE_TOKENS"), defaults.ResponseReserveTokens),
		ContextKeepLastTurns:         envInt(getenv("CONTEXT_KEEP_LAST_TURNS"), defaults.KeepLastTurns),
		ContextSummarizationEnabled:  envBool(getenv("CONTEXT_SUMMARIZATION_ENABLED"), defaults.SummarizationEnabled),
		ContextSummaryTriggerTokens:  envInt(getenv("CONTEXT_SUMMARY_TRIGGER_TOKENS"), defaults.SummaryTriggerTokens),
		ContextSummaryModel:          getenv("CONTEXT_SUMMARY_MODEL"),
	}
}

func envBool(raw string, fallback bool) bool {
	text := strings.TrimSpace(raw)
	if text == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(text)
	if err != nil {
		return fallback
	}
	return parsed
}

func envInt(raw string, fallback int) int {
	text := strings.TrimSpace(raw)
	if text == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(text)
	if err != nil {
		return fallback
	}
	return parsed
}

func (factory Factory) Build(includePlan bool) Runtime {
	client := agent.NewClient(factory.Config.APIKey, factory.Config.BaseURL, nil)

	var planner *agent.Planner
	if includePlan {
		planner = agent.NewPlanner(client, factory.Config.Model)
	}

	catalog := tools.NewCatalog(tools.Options{
		WorkspaceRoot: factory.WorkspaceRoot,
		IncludePlan:   includePlan,
		SearchAPIKey:  factory.Config.SearchAPIKey,
		SearchBaseURL: factory.Config.SearchBaseURL,
		MemoryDir:     factory.MemoryDir,
	})

	return Runtime{
		Client:        client,
		Planner:       planner,
		Catalog:       catalog,
		Model:         factory.Config.Model,
		WorkspaceRoot: factory.WorkspaceRoot,
		Context: agent.ContextConfig{
			Enabled:               factory.Config.ContextManagementEnabled,
			MaxInputTokens:        factory.Config.ContextMaxInputTokens,
			TargetInputTokens:     factory.Config.ContextTargetInputTokens,
			ResponseReserveTokens: factory.Config.ContextResponseReserveTokens,
			KeepLastTurns:         factory.Config.ContextKeepLastTurns,
			SummarizationEnabled:  factory.Config.ContextSummarizationEnabled,
			SummaryTriggerTokens:  factory.Config.ContextSummaryTriggerTokens,
			SummaryModel:          factory.Config.ContextSummaryModel,
		},
	}
}

func (runtime Runtime) NewAgent(logWriter io.Writer, systemPrompt string, recorder agent.MessageRecorder, stream bool) *agent.Agent {
	return agent.NewWithOptions(runtime.Client, runtime.Model, agent.Options{
		SystemPrompt:    systemPrompt,
		LogWriter:       logWriter,
		ToolDefinitions: runtime.Catalog.Definitions(),
		Functions:       runtime.Catalog.Registry(),
		Planner:         runtime.Planner,
		Recorder:        recorder,
		Stream:          stream,
		WorkspaceRoot:   runtime.WorkspaceRoot,
		Context:         runtime.Context,
	})
}
