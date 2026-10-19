package agent

import (
	"context"
	"fmt"
	"strings"

	"bqagent/internal/tools"
	"bqagent/internal/turn"
)

const turnOutcomeInstructions = `Do not call turn_outcome for ordinary questions, successful work, or before a normal final answer; successful completion is automatic. Use it only to report a concrete obstacle to this request (blocked), required user information or authorization (awaiting_input), or to clear an existing blocked/awaiting_input declaration made for the current request once that obstacle is resolved (completed). Without such a current declaration, completed is invalid; historical blockers do not count. It works independently of task progress and requires no task record. Give the final answer explaining the obstacle after declaring it. Never declare a blocker solely because progress recording failed or to make a completed call valid.`

// turnOutcomeTaskInstructions apply only when the task tool is enabled.
const turnOutcomeTaskInstructions = `Historical task status alone does not declare this turn's outcome. A successful task_update recording blocked/awaiting_input also declares that current obstacle unless turn_outcome was used explicitly.`

type outcomeDeclaration struct {
	status   turn.Status
	reason   string
	explicit bool
}

func (a *Agent) installTurnOutcomeTool() {
	a.toolDefinitions = append(a.toolDefinitions, tools.Definition{Type: "function", Function: tools.FunctionDefinition{
		Name: "turn_outcome", Description: "Report a concrete obstacle to the current request or clear that obstacle once resolved. Do not call for ordinary questions or successful work: normal final answers complete automatically. Use blocked for a concrete obstacle or awaiting_input for necessary user information/authorization, with a concrete reason, then give the final answer. Use completed only to clear an existing blocked/awaiting_input declaration for the current request; historical blockers do not count. Do not use for progress recording errors or invent a blocker to enable completed. Does not stop tools or override runtime failures, cancellation or execution limits.",
		Parameters: tools.JSONSchema{Type: "object", Properties: map[string]tools.JSONSchemaProperty{
			"status": {Type: "string", Description: "blocked or awaiting_input; completed only clears an existing blocked/awaiting_input declaration for the current request."},
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
		if err := run.declareOutcome(status, reason, true); err != nil {
			return "", err
		}
		return "Current turn outcome declared. Continue if the obstacle can be resolved; otherwise give the final answer explaining what is needed.", nil
	}
}

// A successful task update can declare a current obstacle for compatibility.
// An explicit turn_outcome declaration takes precedence over progress metadata.
func (s *runState) declareOutcome(status turn.Status, reason string, explicit bool) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if explicit && status == turn.Completed && (s.declaration == nil || (s.declaration.status != turn.Blocked && s.declaration.status != turn.AwaitingInput)) {
		return fmt.Errorf("turn_outcome completed requires an existing blocked/awaiting_input declaration for the current request. Give the final answer directly; successful completion is automatic. Do not invent a blocker or retry this declaration")
	}
	if s.declaration == nil || explicit || !s.declaration.explicit {
		s.declaration = &outcomeDeclaration{status: status, reason: reason, explicit: explicit}
	}
	return nil
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
