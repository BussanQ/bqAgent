package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	"bqagent/internal/agent"
	"bqagent/internal/extagent"
	appmemory "bqagent/internal/memory"
	"bqagent/internal/providerconfig"
	appruntime "bqagent/internal/runtime"
	appserver "bqagent/internal/server"
	"bqagent/internal/subagent"
	"bqagent/internal/workspace"
)

func newConversationService(ctx context.Context, getenv func(string) string, ws *workspace.Workspace, systemPrompt string, includePlan bool, statusWriter io.Writer) (*appserver.Service, *extagent.Broker) {
	runtime := appruntime.Factory{
		Config:         runtimeConfigFromSources(getenv, ws.AgentDir()),
		WorkspaceRoot:  ws.Root,
		AgentDir:       ws.AgentDir(),
		MemoryDir:      ws.WorkspaceMemoryDir(),
		Getenv:         getenv,
		MCPConfigPaths: ws.MCPConfigPaths(),
		LogWriter:      statusWriter,
	}.Build(includePlan)

	externalConfig := extagent.ConfigFromEnv(getenv, ws.Root)
	detections := extagent.Detect(ctx, externalConfig, nil)
	if statusWriter != nil {
		for _, status := range extagent.FormatStatuses(detections) {
			fmt.Fprintf(statusWriter, "external-agent %s\n", status)
		}
	}

	externalBroker := extagent.NewBroker(extagent.NewStateStore(ws.Root), detections, nil)
	subagentManager := subagent.NewManager(ws.Root, externalBroker, runtime.RunTraceEnabled)
	var memoryAppend func(task, result string) error
	if ws.MemoryEnabled() {
		memoryAppend = func(task, result string) error {
			content := "Task: " + task + "\nResult: " + result
			if len(content) > appmemory.MaxContentSize {
				content = content[:appmemory.MaxContentSize]
			}
			_, err := runtime.Memory.Add(appmemory.KindLesson, content, "", .6, "normal", nil)
			return err
		}
	}

	service := appserver.NewService(appserver.ServiceOptions{
		WorkspaceRoot:   ws.Root,
		AgentDir:        ws.AgentDir(),
		Client:          runtime.Client,
		ClientOptions:   &runtime.ClientOptions,
		APIType:         runtime.APIType,
		Model:           runtime.Model,
		Models:          runtime.Models,
		DefaultMaxTurns: runtime.MaxIterations,
		SystemPrompt:    systemPrompt,
		PromptSectionsBuilder: func() (workspace.PromptSections, error) {
			return ws.BuildPromptSections("")
		},
		Planner:         runtime.Planner,
		ToolDefinitions: runtime.Catalog.Definitions(),
		Functions:       runtime.Catalog.Registry(),
		ExternalBroker:  externalBroker,
		MemoryAppend:    memoryAppend,
		Context:         runtime.Context,
		RunTraceEnabled: runtime.RunTraceEnabled,
		SessionOptions:  &runtime.SessionOptions,
		Subagents:       subagentManager,
		MemoryStore:     runtime.Memory,
	})
	return service, externalBroker
}

func runtimeConfigFromSources(getenv func(string) string, agentDir string) appruntime.Config {
	config := appruntime.ConfigFromEnv(getenv)
	store := providerconfig.NewStore(agentDir)
	saved, err := store.Load()
	if err != nil || strings.TrimSpace(saved.ActiveProvider) == "" {
		return config
	}
	for _, provider := range saved.Providers {
		if provider.ID != saved.ActiveProvider {
			continue
		}
		apiKey, decryptErr := store.DecryptAPIKey(provider.APIKey)
		if decryptErr != nil {
			return config
		}
		config.APIType = agent.NormalizeAPIType(provider.APIType)
		config.APIKey = apiKey
		config.BaseURL = provider.BaseURL
		config.Model = provider.DefaultModel
		config.Models = append([]string(nil), provider.Models...)
		return config
	}
	return config
}
