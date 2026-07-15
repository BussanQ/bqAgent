package main

import (
	"context"
	"fmt"
	"io"

	"bqagent/internal/agent"
	"bqagent/internal/extagent"
	appmemory "bqagent/internal/memory"
	appruntime "bqagent/internal/runtime"
	appserver "bqagent/internal/server"
	"bqagent/internal/subagent"
	"bqagent/internal/workspace"
)

func newConversationService(ctx context.Context, getenv func(string) string, ws *workspace.Workspace, systemPrompt string, includePlan bool, statusWriter io.Writer) (*appserver.Service, *extagent.Broker) {
	runtime := appruntime.Factory{
		Config:        appruntime.ConfigFromEnv(getenv),
		WorkspaceRoot: ws.Root,
		MemoryDir:     ws.WorkspaceMemoryDir(),
		Getenv:        getenv,
		MCPConfigPath: ws.MCPConfigPath(),
		LogWriter:     statusWriter,
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
		Client:          runtime.Client,
		APIType:         runtime.APIType,
		Model:           runtime.Model,
		DefaultMaxTurns: runtime.MaxIterations,
		SystemPrompt:    systemPrompt,
		SystemPromptBuilder: func() (string, error) {
			return ws.BuildSystemPrompt(agent.DefaultSystemPrompt)
		},
		Planner:         runtime.Planner,
		ToolDefinitions: runtime.Catalog.Definitions(),
		Functions:       runtime.Catalog.Registry(),
		ExternalBroker:  externalBroker,
		MemoryAppend:    memoryAppend,
		Context:         runtime.Context,
		RunTraceEnabled: runtime.RunTraceEnabled,
		Subagents:       subagentManager,
		MemoryStore:     runtime.Memory,
	})
	return service, externalBroker
}
