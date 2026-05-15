package main

import (
	"context"
	"fmt"
	"io"

	"bqagent/internal/agent"
	"bqagent/internal/extagent"
	appruntime "bqagent/internal/runtime"
	appserver "bqagent/internal/server"
	"bqagent/internal/workspace"
)

func newConversationService(ctx context.Context, getenv func(string) string, ws *workspace.Workspace, systemPrompt string, includePlan bool, statusWriter io.Writer) (*appserver.Service, *extagent.Broker) {
	runtime := appruntime.Factory{
		Config:        appruntime.ConfigFromEnv(getenv),
		WorkspaceRoot: ws.Root,
		MemoryDir:     ws.WorkspaceMemoryDir(),
	}.Build(includePlan)

	externalConfig := extagent.ConfigFromEnv(getenv, ws.Root)
	detections := extagent.Detect(ctx, externalConfig, nil)
	if statusWriter != nil {
		for _, status := range extagent.FormatStatuses(detections) {
			fmt.Fprintf(statusWriter, "external-agent %s\n", status)
		}
	}

	externalBroker := extagent.NewBroker(extagent.NewStateStore(ws.Root), detections, nil)
	var memoryAppend func(task, result string) error
	if ws.MemoryEnabled() {
		memoryAppend = ws.AppendMemory
	}

	service := appserver.NewService(appserver.ServiceOptions{
		WorkspaceRoot: ws.Root,
		Client:        runtime.Client,
		Model:         runtime.Model,
		SystemPrompt:  systemPrompt,
		SystemPromptBuilder: func() (string, error) {
			return ws.BuildSystemPrompt(agent.DefaultSystemPrompt)
		},
		Planner:         runtime.Planner,
		ToolDefinitions: runtime.Catalog.Definitions(),
		Functions:       runtime.Catalog.Registry(),
		ExternalBroker:  externalBroker,
		MemoryAppend:    memoryAppend,
		Context:         runtime.Context,
		ServerLogWriter: statusWriter,
	})
	return service, externalBroker
}
