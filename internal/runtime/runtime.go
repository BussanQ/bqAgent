package runtime

import (
	"io"

	"bqagent/internal/agent"
	"bqagent/internal/tools"
)

type Config struct {
	APIKey        string
	BaseURL       string
	Model         string
	SearchAPIKey  string
	SearchBaseURL string
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
}

func ConfigFromEnv(getenv func(string) string) Config {
	return Config{
		APIKey:        getenv("OPENAI_API_KEY"),
		BaseURL:       getenv("OPENAI_BASE_URL"),
		Model:         getenv("OPENAI_MODEL"),
		SearchAPIKey:  getenv("SEARCH_API_KEY"),
		SearchBaseURL: getenv("SEARCH_BASE_URL"),
	}
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
	})
}
