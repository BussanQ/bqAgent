package runtime

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"bqagent/internal/agent"
	"bqagent/internal/mcp"
	appmemory "bqagent/internal/memory"
	"bqagent/internal/modelcatalog"
	"bqagent/internal/session"
	"bqagent/internal/taskstate"
	"bqagent/internal/tools"
)

// mcpDiscoveryTimeout bounds the startup connect+list round trip to all
// configured MCP servers so a slow or unreachable server cannot hang startup.
const mcpDiscoveryTimeout = 15 * time.Second

type Config struct {
	ProviderID                      string
	ModelDisplayNames               modelcatalog.Names
	APIType                         agent.APIType
	APIKey                          string
	BaseURL                         string
	Model                           string
	Models                          []string
	StreamIdleTimeout               time.Duration
	StreamProgressTimeout           time.Duration
	MaxIterations                   int
	RunTraceEnabled                 bool
	SessionTranscriptMode           session.TranscriptMode
	SessionOutputMaxBytes           int64
	BashOutputMaxBytes              int64
	ReadFileMaxBytes                int64
	SearchProvider                  string
	SearchAPIKey                    string
	SearchBaseURL                   string
	NativeWebSearch                 string
	ContextManagementEnabled        bool
	ContextMaxInputTokens           int
	ContextCompressionTriggerTokens int
	ContextTargetInputTokens        int
	ContextResponseReserveTokens    int
	ContextKeepLastTurns            int
	ContextExactCountTriggerPercent int
	ContextSummarizationEnabled     bool
	ContextSummaryTriggerTokens     int
	ContextSummaryModel             string
}

type Factory struct {
	Config        Config
	WorkspaceRoot string
	AgentDir      string
	MemoryDir     string
	// GlobalMemoryDir is ~/.agent/memory when the workspace has a distinct
	// global agent directory. Empty falls back to AgentDir/memory, then MemoryDir.
	GlobalMemoryDir string
	// Getenv resolves environment variables for MCP config ${VAR} expansion.
	// Nil falls back to os.Getenv inside the MCP loader.
	Getenv func(string) string
	// MCPConfigPath points at .agent/mcp.json. Empty disables MCP discovery.
	MCPConfigPath string
	// MCPConfigPaths are loaded in order; later files override same-named servers.
	MCPConfigPaths []string
	MCPReport      func(mcp.ServerStatus)
	MCPHTTPClient  *http.Client
	// LogWriter receives best-effort MCP discovery warnings. Nil discards them.
	LogWriter io.Writer
}

type Runtime struct {
	MCP             *mcp.Discovery
	Client          agent.ChatCompletionClient
	ClientOptions   agent.ClientOptions
	Catalog         tools.Catalog
	APIType         agent.APIType
	Model           string
	Models          []string
	MaxIterations   int
	RunTraceEnabled bool
	SessionOptions  session.Options
	WorkspaceRoot   string
	Context         agent.ContextConfig
	Memory          *appmemory.Store
	GlobalMemory    *appmemory.Store
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
		APIType:                         apiType,
		APIKey:                          apiKey,
		BaseURL:                         baseURL,
		Model:                           model,
		Models:                          splitEnvList(getenv("LLM_MODELS")),
		StreamIdleTimeout:               envDurationAllowZero(getenv("LLM_STREAM_IDLE_TIMEOUT"), agent.DefaultStreamIdleTimeout),
		StreamProgressTimeout:           envDurationAllowZero(getenv("LLM_STREAM_PROGRESS_TIMEOUT"), agent.DefaultStreamProgressTimeout),
		MaxIterations:                   envInt(getenv("AGENT_MAX_ITERATIONS"), agent.DefaultMaxIterations),
		RunTraceEnabled:                 envBool(getenv("RUN_TRACE_ENABLED"), false),
		SessionTranscriptMode:           session.NormalizeTranscriptMode(getenv("SESSION_TRANSCRIPT_MODE")),
		SessionOutputMaxBytes:           envInt64(getenv("SESSION_OUTPUT_MAX_BYTES"), session.DefaultOutputMaxBytes),
		BashOutputMaxBytes:              envPositiveInt64(getenv("BASH_OUTPUT_MAX_BYTES"), tools.DefaultBashOutputMaxBytes),
		ReadFileMaxBytes:                envPositiveInt64(getenv("READ_FILE_MAX_BYTES"), tools.DefaultReadFileMaxBytes),
		SearchProvider:                  searchProvider,
		SearchAPIKey:                    searchAPIKey,
		SearchBaseURL:                   searchBaseURL,
		NativeWebSearch:                 strings.TrimSpace(getenv("AGENT_NATIVE_WEB_SEARCH")),
		ContextManagementEnabled:        envBool(getenv("CONTEXT_MANAGEMENT_ENABLED"), defaults.Enabled),
		ContextMaxInputTokens:           envInt(getenv("CONTEXT_MAX_INPUT_TOKENS"), defaults.MaxInputTokens),
		ContextCompressionTriggerTokens: envInt(getenv("CONTEXT_COMPRESSION_TRIGGER_TOKENS"), defaults.CompressionTriggerTokens),
		ContextTargetInputTokens:        envInt(getenv("CONTEXT_TARGET_INPUT_TOKENS"), defaults.TargetInputTokens),
		ContextResponseReserveTokens:    envInt(getenv("CONTEXT_RESPONSE_RESERVE_TOKENS"), defaults.ResponseReserveTokens),
		ContextKeepLastTurns:            envInt(getenv("CONTEXT_KEEP_LAST_TURNS"), defaults.KeepLastTurns),
		ContextExactCountTriggerPercent: envInt(getenv("CONTEXT_EXACT_COUNT_TRIGGER_PERCENT"), defaults.ExactCountTriggerPercent),
		ContextSummarizationEnabled:     envBool(getenv("CONTEXT_SUMMARIZATION_ENABLED"), defaults.SummarizationEnabled),
		ContextSummaryTriggerTokens:     envInt(getenv("CONTEXT_SUMMARY_TRIGGER_TOKENS"), defaults.SummaryTriggerTokens),
		ContextSummaryModel:             getenv("CONTEXT_SUMMARY_MODEL"),
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

func splitEnvList(raw string) []string {
	values := make([]string, 0)
	for _, value := range strings.Split(raw, ",") {
		value = strings.TrimSpace(value)
		if value != "" {
			values = append(values, value)
		}
	}
	return values
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

func envInt64(raw string, fallback int64) int64 {
	text := strings.TrimSpace(raw)
	if text == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(text, 10, 64)
	if err != nil || parsed < 0 {
		return fallback
	}
	return parsed
}

func envPositiveInt64(raw string, fallback int64) int64 {
	text := strings.TrimSpace(raw)
	if text == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(text, 10, 64)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func envDurationAllowZero(raw string, fallback time.Duration) time.Duration {
	text := strings.TrimSpace(raw)
	if text == "" {
		return fallback
	}
	if text == "0" {
		return 0
	}
	parsed, err := time.ParseDuration(text)
	if err != nil || parsed < 0 {
		return fallback
	}
	return parsed
}

func (factory Factory) Build() Runtime {
	return factory.build(nil)
}

// BuildBackground leaves remote MCP discovery off the interactive startup path.
// The caller must cancel ctx when the UI/server workspace closes.
func (factory Factory) BuildBackground(ctx context.Context) Runtime {
	return factory.build(ctx)
}

func (factory Factory) build(background context.Context) Runtime {
	apiType := agent.NormalizeAPIType(string(factory.Config.APIType))
	model := agent.EffectiveModel(factory.Config.Model)
	clientOptions := agent.ClientOptions{StreamIdleTimeout: factory.Config.StreamIdleTimeout, StreamProgressTimeout: factory.Config.StreamProgressTimeout, ProviderID: factory.Config.ProviderID, NativeWebSearch: factory.Config.NativeWebSearch}
	client := agent.NewClientWithOptions(factory.Config.APIKey, factory.Config.BaseURL, apiType, nil, clientOptions)
	factory.logf("[Runtime] api_type=%s model=%s\n", apiType, model)

	var mcpDefinitions []tools.Definition
	var mcpFunctions map[string]tools.Function
	var discovery *mcp.Discovery
	if background == nil {
		mcpDefinitions, mcpFunctions = factory.discoverMCPTools()
	} else {
		discovery = mcp.StartDiscovery(background, factory.loadMCPConfig(), factory.Getenv, factory.MCPHTTPClient, factory.logf, factory.MCPReport, mcpDiscoveryTimeout)
	}
	memoryStore := openMemoryStore(factory.MemoryDir)
	globalMemoryStore := memoryStore
	if globalDir := factory.resolvedGlobalMemoryDir(); globalDir != "" && !sameMemoryPath(globalDir, factory.MemoryDir) {
		globalMemoryStore = openMemoryStore(globalDir)
	}

	catalog := tools.NewCatalog(tools.Options{
		WorkspaceRoot:      factory.WorkspaceRoot,
		AgentDir:           factory.AgentDir,
		BashOutputMaxBytes: factory.Config.BashOutputMaxBytes,
		ReadFileMaxBytes:   factory.Config.ReadFileMaxBytes,
		SearchProvider:     factory.Config.SearchProvider,
		SearchAPIKey:       factory.Config.SearchAPIKey,
		SearchBaseURL:      factory.Config.SearchBaseURL,
		MemoryDir:          factory.MemoryDir,
		MemoryStore:        memoryStore,
		GlobalMemoryStore:  globalMemoryStore,
		ExtraDefinitions:   mcpDefinitions,
		ExtraFunctions:     mcpFunctions,
	})

	return Runtime{
		MCP:             discovery,
		Client:          client,
		ClientOptions:   clientOptions,
		Catalog:         catalog,
		APIType:         apiType,
		Model:           model,
		Models:          append([]string(nil), factory.Config.Models...),
		MaxIterations:   factory.Config.MaxIterations,
		RunTraceEnabled: factory.Config.RunTraceEnabled,
		SessionOptions: session.Options{
			TranscriptMode: factory.Config.SessionTranscriptMode,
			OutputMaxBytes: factory.Config.SessionOutputMaxBytes,
			AgentDir:       factory.AgentDir,
		},
		WorkspaceRoot: factory.WorkspaceRoot,
		Context: agent.ContextConfig{
			Enabled:                  factory.Config.ContextManagementEnabled,
			MaxInputTokens:           factory.Config.ContextMaxInputTokens,
			CompressionTriggerTokens: factory.Config.ContextCompressionTriggerTokens,
			TargetInputTokens:        factory.Config.ContextTargetInputTokens,
			ResponseReserveTokens:    factory.Config.ContextResponseReserveTokens,
			KeepLastTurns:            factory.Config.ContextKeepLastTurns,
			ExactCountTriggerPercent: factory.Config.ContextExactCountTriggerPercent,
			SummarizationEnabled:     factory.Config.ContextSummarizationEnabled,
			SummaryTriggerTokens:     factory.Config.ContextSummaryTriggerTokens,
			SummaryModel:             factory.Config.ContextSummaryModel,
		},
		Memory:       memoryStore,
		GlobalMemory: globalMemoryStore,
	}
}

func (factory Factory) resolvedGlobalMemoryDir() string {
	if dir := strings.TrimSpace(factory.GlobalMemoryDir); dir != "" {
		return dir
	}
	if dir := strings.TrimSpace(factory.AgentDir); dir != "" {
		return filepath.Join(dir, "memory")
	}
	return ""
}

func openMemoryStore(dir string) *appmemory.Store {
	store := appmemory.NewStore(dir,
		filepath.Join(dir, "MEMORY.md"),
		filepath.Join(dir, time.Now().Format("2006-01-02")+".md"),
		filepath.Join(dir, time.Now().AddDate(0, 0, -1).Format("2006-01-02")+".md"),
	)
	_ = store.Migrate()
	return store
}

func sameMemoryPath(left, right string) bool {
	if strings.TrimSpace(left) == "" || strings.TrimSpace(right) == "" {
		return false
	}
	return filepath.Clean(left) == filepath.Clean(right)
}

// loadMCPConfig reads the local MCP configuration without making network requests.
func (factory Factory) loadMCPConfig() mcp.Config {
	paths := append([]string(nil), factory.MCPConfigPaths...)
	if len(paths) == 0 && strings.TrimSpace(factory.MCPConfigPath) != "" {
		paths = append(paths, factory.MCPConfigPath)
	}
	cfg, err := mcp.LoadMergedConfig(paths, factory.Getenv)
	if err != nil {
		factory.logf("[MCP] failed to load config: %v\n", err)
		return mcp.Config{}
	}
	return cfg
}

func (factory Factory) discoverMCPTools() ([]tools.Definition, map[string]tools.Function) {
	cfg := factory.loadMCPConfig()
	if !cfg.HasEnabledServers() {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), mcpDiscoveryTimeout)
	defer cancel()
	return mcp.DiscoverWithStatus(ctx, cfg, factory.Getenv, factory.MCPHTTPClient, factory.logf, factory.MCPReport)
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
	taskStore, _ := recorder.(taskstate.Store)
	return agent.NewWithOptions(runtime.Client, runtime.Model, agent.Options{
		EnableNativeWebSearch: true,

		TaskStore:       taskStore,
		SystemPrompt:    systemPrompt,
		APIType:         runtime.APIType,
		LogWriter:       logWriter,
		ProgressWriter:  progressWriter,
		ToolDefinitions: runtime.Catalog.Definitions(),
		Functions:       runtime.Catalog.Registry(),
		Recorder:        recorder,
		Stream:          stream,
		WorkspaceRoot:   runtime.WorkspaceRoot,
		Context:         runtime.Context,
	})
}
