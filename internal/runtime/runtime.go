package runtime

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"bqagent/internal/agent"
	"bqagent/internal/mcp"
	appmemory "bqagent/internal/memory"
	"bqagent/internal/tools"
)

// mcpDiscoveryTimeout bounds the startup connect+list round trip to all
// configured MCP servers so a slow or unreachable server cannot hang startup.
const mcpDiscoveryTimeout = 15 * time.Second

type Config struct {
	APIType                      agent.APIType
	APIKey                       string
	BaseURL                      string
	Model                        string
	MaxIterations                int
	SearchProvider               string
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
	// Getenv resolves environment variables for MCP config ${VAR} expansion.
	// Nil falls back to os.Getenv inside the MCP loader.
	Getenv func(string) string
	// MCPConfigPath points at .agent/mcp.json. Empty disables MCP discovery.
	MCPConfigPath string
	// LogWriter receives best-effort MCP discovery warnings. Nil discards them.
	LogWriter io.Writer
}

type Runtime struct {
	Client        agent.ChatCompletionClient
	Planner       *agent.Planner
	Catalog       tools.Catalog
	APIType       agent.APIType
	Model         string
	MaxIterations int
	WorkspaceRoot string
	Context       agent.ContextConfig
	Memory        *appmemory.Store
}

func ConfigFromEnv(getenv func(string) string) Config {
	defaults := agent.DefaultContextConfig()
	apiType := agent.NormalizeAPIType(firstNonEmpty(getenv("LLM_API_TYPE"), getenv("OPENAI_API_TYPE")))
	apiKey := firstNonEmpty(getenv("LLM_API_KEY"), getenv("OPENAI_API_KEY"))
	baseURL := firstNonEmpty(getenv("LLM_BASE_URL"), getenv("OPENAI_BASE_URL"))
	model := firstNonEmpty(getenv("LLM_MODEL"), getenv("OPENAI_MODEL"))
	if apiType == agent.APITypeAnthropic {
		apiKey = firstNonEmpty(getenv("LLM_API_KEY"), getenv("ANTHROPIC_API_KEY"), getenv("OPENAI_API_KEY"))
		baseURL = firstNonEmpty(getenv("LLM_BASE_URL"), getenv("ANTHROPIC_BASE_URL"), getenv("OPENAI_BASE_URL"))
		model = firstNonEmpty(getenv("LLM_MODEL"), getenv("ANTHROPIC_MODEL"), getenv("OPENAI_MODEL"))
	}
	model = agent.EffectiveModel(model)
	searchProvider := searchProviderFromEnv(getenv)
	searchAPIKey := firstNonEmpty(getenv("SEARCH_API_KEY"), getenv("FIRECRAWL_API_KEY"))
	searchBaseURL := firstNonEmpty(getenv("SEARCH_BASE_URL"), getenv("FIRECRAWL_BASE_URL"))
	return Config{
		APIType:                      apiType,
		APIKey:                       apiKey,
		BaseURL:                      baseURL,
		Model:                        model,
		MaxIterations:                envInt(getenv("AGENT_MAX_ITERATIONS"), agent.DefaultMaxIterations),
		SearchProvider:               searchProvider,
		SearchAPIKey:                 searchAPIKey,
		SearchBaseURL:                searchBaseURL,
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

func searchProviderFromEnv(getenv func(string) string) string {
	if firstNonEmpty(getenv("SEARCH_API_KEY"), getenv("SEARCH_BASE_URL")) != "" {
		return "tavily"
	}
	if firstNonEmpty(getenv("FIRECRAWL_API_KEY"), getenv("FIRECRAWL_BASE_URL")) != "" {
		return "firecrawl"
	}
	return "tavily"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
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
	apiType := agent.NormalizeAPIType(string(factory.Config.APIType))
	model := agent.EffectiveModel(factory.Config.Model)
	client := agent.NewClientWithAPIType(factory.Config.APIKey, factory.Config.BaseURL, apiType, nil)
	factory.logf("[Runtime] api_type=%s model=%s\n", apiType, model)

	var planner *agent.Planner
	if includePlan {
		planner = agent.NewPlanner(client, model)
	}

	mcpDefinitions, mcpFunctions := factory.discoverMCPTools()
	memoryStore := appmemory.NewStore(factory.MemoryDir,
		filepath.Join(factory.MemoryDir, "MEMORY.md"),
		filepath.Join(factory.MemoryDir, time.Now().Format("2006-01-02")+".md"),
		filepath.Join(factory.MemoryDir, time.Now().AddDate(0, 0, -1).Format("2006-01-02")+".md"),
	)
	_ = memoryStore.Migrate()

	catalog := tools.NewCatalog(tools.Options{
		WorkspaceRoot:    factory.WorkspaceRoot,
		IncludePlan:      includePlan,
		SearchProvider:   factory.Config.SearchProvider,
		SearchAPIKey:     factory.Config.SearchAPIKey,
		SearchBaseURL:    factory.Config.SearchBaseURL,
		MemoryDir:        factory.MemoryDir,
		MemoryStore:      memoryStore,
		ExtraDefinitions: mcpDefinitions,
		ExtraFunctions:   mcpFunctions,
	})

	return Runtime{
		Client:        client,
		Planner:       planner,
		Catalog:       catalog,
		APIType:       apiType,
		Model:         model,
		MaxIterations: factory.Config.MaxIterations,
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
		Memory: memoryStore,
	}
}

// discoverMCPTools loads .agent/mcp.json and, when it lists enabled servers,
// connects to each to discover its tools. It is best-effort: a missing config
// or an unreachable server yields no tools and never aborts startup.
func (factory Factory) discoverMCPTools() ([]tools.Definition, map[string]tools.Function) {
	cfg, err := mcp.LoadConfig(factory.MCPConfigPath, factory.Getenv)
	if err != nil {
		factory.logf("[MCP] failed to load config: %v\n", err)
		return nil, nil
	}
	if !cfg.HasEnabledServers() {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), mcpDiscoveryTimeout)
	defer cancel()
	return mcp.Discover(ctx, cfg, factory.Getenv, nil, factory.logf)
}

func (factory Factory) logf(format string, args ...any) {
	if factory.LogWriter == nil {
		return
	}
	fmt.Fprintf(factory.LogWriter, format, args...)
}

func (runtime Runtime) NewAgent(logWriter io.Writer, systemPrompt string, recorder agent.MessageRecorder, stream bool) *agent.Agent {
	return runtime.NewAgentWithProgress(logWriter, logWriter, systemPrompt, recorder, stream)
}

func (runtime Runtime) NewAgentWithProgress(logWriter io.Writer, progressWriter io.Writer, systemPrompt string, recorder agent.MessageRecorder, stream bool) *agent.Agent {
	return agent.NewWithOptions(runtime.Client, runtime.Model, agent.Options{
		SystemPrompt:    systemPrompt,
		APIType:         runtime.APIType,
		LogWriter:       logWriter,
		ProgressWriter:  progressWriter,
		ToolDefinitions: runtime.Catalog.Definitions(),
		Functions:       runtime.Catalog.Registry(),
		Planner:         runtime.Planner,
		Recorder:        recorder,
		Stream:          stream,
		WorkspaceRoot:   runtime.WorkspaceRoot,
		Context:         runtime.Context,
	})
}
