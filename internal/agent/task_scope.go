package agent

import (
	"context"
	"fmt"
	"strings"

	"bqagent/internal/tools"
	"bqagent/internal/turn"
)

func isSideQuestion(ctx context.Context) bool {
	s := turnRun(ctx)
	return s != nil && s.outcome.TaskRelation == turn.SideQuestion
}

func (a *Agent) installTaskScopeTool() {
	a.toolDefinitions = append(a.toolDefinitions, tools.Definition{Type: "function", Function: tools.FunctionDefinition{
		Name: "task_scope", Description: "Associate this turn with the saved programming task. Use side_question only for an unrelated user question, before task_update; preserve the saved task. Continue/status requests remain continue. This does not change or complete the task.",
		Parameters: tools.JSONSchema{Type: "object", Properties: map[string]tools.JSONSchemaProperty{
			"relation": {Type: "string", Description: "continue or side_question"},
			"reason":   {Type: "string", Description: "Explain how the current user request relates to the saved task."},
		}, Required: []string{"relation", "reason"}},
	}})
	a.functions["task_scope"] = func(ctx context.Context, args map[string]any) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		s := turnRun(ctx)
		if s == nil || a.task == nil {
			return "", fmt.Errorf("no saved task to associate")
		}
		raw, _ := args["relation"].(string)
		reason, _ := args["reason"].(string)
		relation := turn.Relation(raw)
		if relation == "" || !relation.Valid() || strings.TrimSpace(reason) == "" {
			return "", fmt.Errorf("valid relation and reason are required")
		}
		if s.scopeLocked {
			return "", fmt.Errorf("task scope is fixed after task progress changes")
		}
		// Detaching after this turn already changed the workspace would let work
		// done for the saved task escape its acceptance and verification rules.
		if relation == turn.SideQuestion && s.hasMutationSinceInput() {
			return "", fmt.Errorf("cannot switch to side_question after this turn modified the workspace; keep relation=continue and update the saved task")
		}
		s.outcome.TaskRelation = relation
		s.outcome.TaskRelationReason = strings.TrimSpace(reason)
		return "Turn association updated. The saved task is unchanged.", nil
	}
}
