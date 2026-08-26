package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	"bqagent/internal/agent"
	appserver "bqagent/internal/server"
	apptui "bqagent/internal/tui"
	"bqagent/internal/workspace"
	"golang.org/x/term"
)

func runChat(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string, ws *workspace.Workspace, systemPrompt, initialTask string, options cliOptions) int {
	if !interactiveTerminal(stdin, stdout, getenv) {
		return runChatLegacy(ctx, stdin, stdout, stderr, getenv, ws, systemPrompt, initialTask, options)
	}
	service, externalBroker := newConversationService(ctx, getenv, ws, systemPrompt, options.plan, nil)
	defer externalBroker.Close()
	backend := &tuiBackend{service: service, workspace: ws}
	noColor := getenv != nil && strings.TrimSpace(getenv("NO_COLOR")) != ""
	err := apptui.Run(backend, apptui.Config{
		Context:     ctx,
		Input:       stdin,
		Output:      stdout,
		Workspace:   ws.Root,
		AgentDir:    ws.AgentDir(),
		InitialTask: initialTask,
		SessionID:   effectiveSessionID(options),
		NoColor:     noColor,
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
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
}

func (backend *tuiBackend) RunTurn(ctx context.Context, sessionID, message string, events apptui.TurnEvents) (apptui.TurnResult, error) {
	response, err := backend.service.HandleTurnWithOptions(ctx, appserver.TurnRequest{
		SessionID: sessionID,
		Message:   message,
	}, appserver.TurnOptions{
		OutputWriter:   eventWriter{write: events.Progress},
		TokenSink:      eventWriter{write: events.Token},
		ProgressWriter: eventWriter{write: events.Progress},
		ToolEventSink:  toolEventSink{emit: events.Tool},
		Stream:         true,
	})
	result := apptui.TurnResult{
		SessionID: response.SessionID,
		Reply:     response.Reply,
		Model:     response.Model,
		Streamed:  response.Streamed,
	}
	if response.Generation != nil {
		result.Metrics = &apptui.GenerationMetrics{
			FirstTokenLatencyMS:  response.Generation.FirstTokenLatencyMS,
			CompletionTokens:     response.Generation.CompletionTokens,
			ReasoningTokens:      response.Generation.ReasoningTokens,
			GenerationDurationMS: response.Generation.GenerationDurationMS,
			TokensPerSecond:      response.Generation.TokensPerSecond,
		}
	}
	return result, err
}

func (backend *tuiBackend) RuntimeInfo(sessionID string) apptui.RuntimeInfo {
	info := backend.service.RuntimeLLMInfoForSession(sessionID)
	return apptui.RuntimeInfo{Provider: info.ProviderID, APIType: string(info.APIType), Model: info.Model}
}

func (backend *tuiBackend) History(sessionID string, budget int) (apptui.History, error) {
	history, err := backend.service.ConversationHistory(sessionID, budget)
	if err != nil {
		return apptui.History{}, err
	}
	result := apptui.History{ID: history.ID, Title: history.Title, Omitted: history.Omitted, Messages: make([]apptui.HistoryMessage, 0, len(history.Messages))}
	for _, message := range history.Messages {
		result.Messages = append(result.Messages, apptui.HistoryMessage{Role: message.Role, Content: message.Content})
	}
	return result, nil
}

func (backend *tuiBackend) Commands() []apptui.Command {
	commands := []apptui.Command{{Name: "/model", Description: "查看或切换模型"}, {Name: "/skill", Description: "使用 Skill 或 alias"}}
	for _, option := range backend.service.ModelOptions() {
		description := option.ID
		if option.Alias == "default" {
			description = "恢复默认模型 " + option.ID
		}
		commands[0].Arguments = append(commands[0].Arguments, apptui.CommandArgument{Value: option.Alias, Description: description})
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
