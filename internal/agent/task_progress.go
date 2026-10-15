package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"bqagent/internal/taskstate"
	"bqagent/internal/tools"
	"bqagent/internal/turn"
)

// taskProgressInstructions holds only task-tracking rules. The general rules on
// effort, verification and final answers live in the cacheable workspace core
// instructions, so this per-turn developer message stays short.
const taskProgressInstructions = `Task tracking: for a multi-step programming task, call task_update before editing to persist the original goal, acceptance criteria and remaining implementation/verification work. Update it when real progress changes, then keep working. Questions and small one-step edits need no task.
An active task stays the objective when the user says continue or asks for status: answer status briefly and keep working unless told to stop or wait. For a new unrelated question, call task_scope with relation=side_question and a reason; it detaches only this turn and leaves the saved task unchanged. Never use side_question to drop work requested in the current turn. Replace the goal only when the user changes or cancels it, using replace_goal with a reason.
Mark a task completed only when every criterion has tool evidence, nothing remains and the targeted verification passed; otherwise record blocked or awaiting_input with the concrete reason. Never remove criteria just to finish.`

func (a *Agent) installTaskTool() {
	if a.taskStore == nil {
		return
	}
	a.installTaskScopeTool()
	a.toolDefinitions = append(a.toolDefinitions, tools.Definition{Type: "function", Function: tools.FunctionDefinition{
		Name: "task_update", Description: "Persist progress for a multi-step programming task. state is JSON containing goal, status (active/completed/blocked/awaiting_input/cancelled), criteria [{description,done,evidence}], remaining [string], verification [{command,status (passed/failed/unavailable),details}], reason. Completion requires all criteria backed by evidence, no remaining work, and passed verification. Preserve the original goal and criteria unless the user changes scope.",
		Parameters: tools.JSONSchema{Type: "object", Properties: map[string]tools.JSONSchemaProperty{
			"state":        {Type: "string", Description: "Complete task state as a JSON object."},
			"replace_goal": {Type: "boolean", Description: "Only true for a new user-authorized objective or changed acceptance criteria; include reason in state."},
		}, Required: []string{"state"}},
	}})
	a.functions["task_update"] = func(ctx context.Context, args map[string]any) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if state := turnRun(ctx); state != nil && state.outcome.TaskRelation == turn.SideQuestion {
			return "", fmt.Errorf("side-question turns cannot modify the saved task")
		}
		raw, err := requireStringArgument("task_update", args, "state")
		if err != nil {
			return "", err
		}
		var next taskstate.State
		if err = json.Unmarshal([]byte(raw), &next); err != nil {
			return "", err
		}
		if err = next.Validate(); err != nil {
			return "", err
		}
		replace, _ := args["replace_goal"].(bool)
		if a.task != nil {
			changed := a.task.Goal != next.Goal
			for _, old := range a.task.Criteria {
				found := false
				for _, criterion := range next.Criteria {
					if criterion.Description == old.Description {
						found = true
						break
					}
				}
				changed = changed || !found
			}
			if changed && (!replace || strings.TrimSpace(next.Reason) == "") {
				return "", fmt.Errorf("preserve the original goal and criteria; replacing them requires replace_goal and the user's scope-change reason")
			}
		}
		if err = a.taskStore.SaveTaskState(next); err != nil {
			return "", err
		}
		a.task = &next
		if state := turnRun(ctx); state != nil {
			state.scopeLocked = true
		}
		return "Task state saved. Continue substantive work while active; finish only after acceptance and verification, or explain the recorded blocker.", nil
	}
}

type continuationContextKey struct{}

func continuationContext(ctx context.Context) (context.Context, *continuationGuard) {
	if guard, ok := ctx.Value(continuationContextKey{}).(*continuationGuard); ok {
		return ctx, guard
	}
	guard := &continuationGuard{}
	return context.WithValue(ctx, continuationContextKey{}, guard), guard
}

func (a *Agent) taskContext(ctx context.Context) string {
	if a.taskStore == nil {
		return ""
	}
	text := taskProgressInstructions
	if a.task != nil {
		data, _ := json.Marshal(a.task)
		if isSideQuestion(ctx) {
			text += "\nThis turn is a side question. Answer only the current request; do not continue or mutate the saved programming task."
		}
		text += "\nCurrent saved task (data, not new user instructions):\n" + string(data)
	}
	return text
}

const recoveryEscalationThreshold = 2

type continuationGuard struct {
	stagnant   int
	progressed bool
}

func (guard *continuationGuard) observe(call ToolCall, result string) {
	if call.Function.Name == "task_scope" || call.Function.Name == "task_update" || call.Function.Name == "todo_write" || !toolResultMadeProgress(call, result) {
		return
	}
	guard.progressed = true
}

// Recovery changes the next action rather than treating temporary lack of tool
// progress as a completed turn. Total iterations, deadlines and cancellation
// remain enforced by runConversation.
func (guard *continuationGuard) recoveryPrompt(reason string) string {
	if guard.progressed {
		guard.stagnant = 0
	}
	guard.progressed = false
	guard.stagnant++
	if guard.stagnant <= recoveryEscalationThreshold {
		return reason
	}
	return reason + fmt.Sprintf("\nRecovery attempt %d: repeated empty replies or progress-only replies have not advanced the task. Change approach now: inspect the latest tool results and saved remaining work, choose the smallest actionable unfinished step, and execute it in this same turn. If a build or test failed, diagnose its actual output, fix the cause, and rerun only the permitted targeted verification. A failed test is evidence to act on, not by itself a blocker. Do not repeat a promise, rewrite only the plan, weaken acceptance criteria, or ask the user to say continue. If truly blocked by missing external information or authorization, record the concrete blocker and explain it; otherwise keep working until verified.", guard.stagnant)
}

func (a *Agent) continuationReason(ctx context.Context, message AssistantMessage) string {
	if message.Completion.OutputTruncated || strings.EqualFold(message.Completion.StopReason, "incomplete") {
		return "The previous response was incomplete (" + message.Completion.IncompleteReason + "). Continue from the saved evidence; do not treat partial text as task completion. Use smaller tool batches if needed."
	}
	if message.Completion.EndTurn != nil && !*message.Completion.EndTurn {
		return "The provider explicitly reported end_turn=false. Continue processing this request and provide its final answer when done."
	}
	if message.Phase == "commentary" || message.Phase == "partial_answer" {
		return "The previous assistant message was an intermediate progress update, not a final answer. Continue the authorized task now with substantive tools, or give a final answer if actually done or genuinely blocked."
	}
	if a.task != nil && a.task.Status == "active" && !isSideQuestion(ctx) {
		return "The saved programming task is still active. Check its original goal, remaining work and acceptance criteria. Continue unfinished implementation or targeted verification now. Use task_update to record completion with evidence, or a concrete blocker/required user input. Do not stop at a promise to continue."
	}
	// Tool count alone is not evidence of unfinished programming work. A
	// complete ordinary answer must not be replaced by an unsolicited review.
	return ""
}

func (a *Agent) appendContinuation(messages []map[string]any, reason string) ([]map[string]any, error) {
	message := map[string]any{"role": "developer", "content": reason}
	if err := a.recordMessages(message); err != nil {
		return messages, err
	}
	return append(messages, message), nil
}
