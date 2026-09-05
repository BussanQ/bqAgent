package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"strings"
	"time"

	"bqagent/internal/agent"
	"bqagent/internal/extagent"
	"bqagent/internal/globalconfig"
	appmemory "bqagent/internal/memory"
	appruntime "bqagent/internal/runtime"
	appserver "bqagent/internal/server"
	"bqagent/internal/subagent"
	"bqagent/internal/workspace"
)

func newConversationService(ctx context.Context, getenv func(string) string, ws *workspace.Workspace, systemPrompt string, includePlan bool, statusWriter io.Writer) (*appserver.Service, *extagent.Broker) {
	var externalBroker *extagent.Broker
	diagnostics := workspaceDoctor(ws, getenv, func() ([]extagent.DetectionStatus, bool) { return externalBroker.DetectionSnapshot() })
	runtime := appruntime.Factory{
		Config:          runtimeConfigFromSources(getenv, ws.AgentDir()),
		WorkspaceRoot:   ws.Root,
		AgentDir:        ws.AgentDir(),
		MemoryDir:       ws.WorkspaceMemoryDir(),
		GlobalMemoryDir: ws.GlobalMemoryDir(),
		Getenv:          getenv,
		MCPConfigPaths:  ws.MCPConfigPaths(),
		MCPReport:       diagnostics.RecordMCP,
		LogWriter:       statusWriter,
	}.Build(includePlan)

	externalConfig := extagent.ConfigFromEnv(getenv, ws.Root)
	if statusWriter != nil {
		fmt.Fprintln(statusWriter, "external-agent detection=background")
	}
	externalBroker = extagent.NewDetectingBroker(ctx, extagent.NewStateStore(ws.Root), externalConfig, nil, func(detections map[extagent.AgentName]extagent.DetectionResult) {
		if statusWriter != nil {
			for _, status := range extagent.FormatStatuses(detections) {
				log.Printf("external-agent %s", status)
			}
		}
	})
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
		Doctor:          diagnostics,
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
		Planner:                   runtime.Planner,
		ToolDefinitions:           runtime.Catalog.Definitions(),
		Functions:                 runtime.Catalog.Registry(),
		ExternalBroker:            externalBroker,
		GroupExternalAgentTimeout: groupExternalAgentTimeoutFromEnv(getenv),
		MemoryAppend:              memoryAppend,
		Context:                   runtime.Context,
		RunTraceEnabled:           runtime.RunTraceEnabled,
		SessionOptions:            &runtime.SessionOptions,
		Subagents:                 subagentManager,
		MemoryStore:               runtime.Memory,
		GlobalMemoryStore:         runtime.GlobalMemory,
	})
	return service, externalBroker
}

func groupExternalAgentTimeoutFromEnv(getenv func(string) string) time.Duration {
	const fallback = 10 * time.Minute
	if getenv == nil {
		return fallback
	}
	raw := strings.TrimSpace(getenv("GROUP_EXTERNAL_AGENT_TIMEOUT"))
	if raw == "" {
		return fallback
	}
	timeout, err := time.ParseDuration(raw)
	if err != nil || timeout <= 0 {
		return fallback
	}
	return timeout
}

func runtimeConfigFromSources(getenv func(string) string, agentDir string) appruntime.Config {
	config := appruntime.ConfigFromEnv(getenv)
	store := globalconfig.NewStore(agentDir)
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
