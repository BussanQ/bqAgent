package agent

import (
	"context"
	"fmt"
	"strings"

	"bqagent/internal/tools"
	"bqagent/internal/turn"
)

const turnOutcomeInstructions = `Use turn_outcome to report a concrete obstacle to this request (blocked), required user information or authorization (awaiting_input), or to clear a resolved obstacle (completed). It works independently of task progress and requires no task record. Give the final answer explaining the obstacle after declaring it. Never declare a blocker solely because progress recording failed. Historical task status alone does not declare this turn's outcome. A successful task_update recording blocked/awaiting_input also declares that current obstacle unless turn_outcome was used explicitly. Clear an explicit declaration with turn_outcome(status=completed) once resolved.`

type outcomeDeclaration struct {
	status   turn.Status
	reason   string
	explicit bool
}

func (a *Agent) installTurnOutcomeTool() {
	a.toolDefinitions = append(a.toolDefinitions, tools.Definition{Type: "function", Function: tools.FunctionDefinition{
		Name: "turn_outcome", Description: "Declare the outcome of the current request independently of task_update or progress storage. Use blocked for a concrete obstacle, awaiting_input for necessary user information/authorization, and completed to clear a resolved obstacle. Include a concrete reason for blocked/awaiting_input, then give the final answer. Do not use for progress recording errors. Does not stop tools or override runtime failures, cancellation or execution limits.",
		Parameters: tools.JSONSchema{Type: "object", Properties: map[string]tools.JSONSchemaProperty{
			"status": {Type: "string", Description: "blocked, awaiting_input, or completed"},
			"reason": {Type: "string", Description: "Concrete obstacle or required user input; required unless status is completed."},
		}, Required: []string{"status"}},
	}})
	a.functions["turn_outcome"] = func(ctx context.Context, args map[string]any) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		run := turnRun(ctx)
		if run == nil {
			return "", fmt.Errorf("no current turn to declare")
		}
		raw, _ := args["status"].(string)
		reason, _ := args["reason"].(string)
		status := turn.Status(raw)
		if status != turn.Completed && status != turn.Blocked && status != turn.AwaitingInput {
			return "", fmt.Errorf("status must be completed, blocked or awaiting_input")
		}
		reason = strings.TrimSpace(reason)
		if status != turn.Completed && reason == "" {
			return "", fmt.Errorf("a concrete reason is required for %s", status)
		}
		run.declareOutcome(status, reason, true)
		return "Current turn outcome declared. Continue if the obstacle can be resolved; otherwise give the final answer explaining what is needed.", nil
	}
}

// A successful task update can declare a current obstacle for compatibility.
// An explicit turn_outcome declaration takes precedence over progress metadata.
func (s *runState) declareOutcome(status turn.Status, reason string, explicit bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.declaration == nil || explicit || !s.declaration.explicit {
		s.declaration = &outcomeDeclaration{status: status, reason: reason, explicit: explicit}
	}
}

func (s *runState) canAcceptFollowUp() bool {
	if s == nil {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.outcome.Status == turn.Completed && (s.declaration == nil || s.declaration.status == turn.Completed)
}

func (s *runState) applyDeclaredOutcome() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.declaration != nil && s.outcome.Status == turn.Completed {
		s.outcome.Set(s.declaration.status, s.declaration.reason)
	}
}
