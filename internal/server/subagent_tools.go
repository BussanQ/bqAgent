package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"bqagent/internal/extagent"
	"bqagent/internal/subagent"
	"bqagent/internal/tools"
	apptrace "bqagent/internal/trace"
)

func subagentToolDefinitions(enabled bool) []tools.Definition {
	if !enabled {
		return nil
	}
	object := func(name, description string, properties map[string]tools.JSONSchemaProperty, required ...string) tools.Definition {
		return tools.Definition{Type: "function", Function: tools.FunctionDefinition{Name: name, Description: description, Parameters: tools.JSONSchema{Type: "object", Properties: properties, Required: required}}}
	}
	id := map[string]tools.JSONSchemaProperty{"id": {Type: "string", Description: "Subagent task id"}}
	return []tools.Definition{
		object("agent_spawn", "Spawn an isolated asynchronous Claude/Codex/Cursor/OpenCode subagent.", map[string]tools.JSONSchemaProperty{
			"agent": {Type: "string"}, "task": {Type: "string"}, "timeout": {Type: "string"}, "retries": {Type: "string"}, "include_dirty": {Type: "string"},
		}, "agent", "task"),
		object("agent_list", "List persisted subagent tasks.", map[string]tools.JSONSchemaProperty{"status": {Type: "string"}}),
		object("agent_wait", "Wait briefly for a subagent and return its persisted state.", map[string]tools.JSONSchemaProperty{"id": {Type: "string"}, "timeout": {Type: "string"}}, "id"),
		object("agent_interrupt", "Interrupt a running subagent while keeping it resumable.", id, "id"),
		object("agent_cancel", "Cancel a subagent permanently.", id, "id"),
		object("agent_resume", "Resume an interrupted or failed subagent.", map[string]tools.JSONSchemaProperty{"id": {Type: "string"}, "message": {Type: "string"}}, "id"),
		object("agent_result", "Read a subagent result and artifact metadata.", id, "id"),
	}
}

func (service *Service) subagentFunctions(sessionID string) map[string]tools.Function {
	if service == nil || service.subagents == nil {
		return nil
	}
	requireID := func(args map[string]any) (string, error) {
		id, _ := args["id"].(string)
		if strings.TrimSpace(id) == "" {
			return "", fmt.Errorf("id is required")
		}
		return id, nil
	}
	encode := func(value any) (string, error) {
		content, err := json.MarshalIndent(value, "", "  ")
		return string(content), err
	}
	return map[string]tools.Function{
		"agent_spawn": func(ctx context.Context, args map[string]any) (string, error) {
			agentName, _ := args["agent"].(string)
			taskText, _ := args["task"].(string)
			timeout, err := time.ParseDuration(toolString(args["timeout"], "30m"))
			if err != nil {
				return "", err
			}
			retries, err := strconv.Atoi(toolString(args["retries"], "1"))
			if err != nil {
				return "", err
			}
			includeDirty, _ := strconv.ParseBool(toolString(args["include_dirty"], "false"))
			task, err := service.subagents.Spawn(subagent.SpawnOptions{ParentSessionID: sessionID, ParentRunID: apptrace.RunIDFromContext(ctx), Agent: extagent.AgentName(strings.ToLower(agentName)), Prompt: taskText, Timeout: timeout, Retries: retries, IncludeDirty: includeDirty})
			if err != nil {
				return "", err
			}
			return encode(task)
		},
		"agent_list": func(ctx context.Context, args map[string]any) (string, error) {
			status, _ := args["status"].(string)
			tasks, err := service.subagents.List(subagent.Status(status))
			if err != nil {
				return "", err
			}
			return encode(tasks)
		},
		"agent_wait": func(ctx context.Context, args map[string]any) (string, error) {
			id, err := requireID(args)
			if err != nil {
				return "", err
			}
			timeout, err := time.ParseDuration(toolString(args["timeout"], "30s"))
			if err != nil {
				return "", err
			}
			waitCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			task, waitErr := service.subagents.Wait(waitCtx, id)
			if waitErr != nil && task == nil {
				return "", waitErr
			}
			return encode(task)
		},
		"agent_interrupt": func(ctx context.Context, args map[string]any) (string, error) {
			id, err := requireID(args)
			if err != nil {
				return "", err
			}
			err = service.subagents.Interrupt(id)
			return "interrupted " + id, err
		},
		"agent_cancel": func(ctx context.Context, args map[string]any) (string, error) {
			id, err := requireID(args)
			if err != nil {
				return "", err
			}
			err = service.subagents.Cancel(id)
			return "canceled " + id, err
		},
		"agent_resume": func(ctx context.Context, args map[string]any) (string, error) {
			id, err := requireID(args)
			if err != nil {
				return "", err
			}
			message, _ := args["message"].(string)
			task, err := service.subagents.Resume(id, message)
			if err != nil {
				return "", err
			}
			return encode(task)
		},
		"agent_result": func(ctx context.Context, args map[string]any) (string, error) {
			id, err := requireID(args)
			if err != nil {
				return "", err
			}
			task, err := service.subagents.Status(id)
			if err != nil {
				return "", err
			}
			return encode(task)
		},
	}
}

func toolString(value any, fallback string) string {
	if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
		return text
	}
	return fallback
}
