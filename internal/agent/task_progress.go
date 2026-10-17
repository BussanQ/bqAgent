package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"bqagent/internal/taskstate"
	"bqagent/internal/tools"
	"bqagent/internal/turn"
)

// taskProgressInstructions holds only task-tracking rules. The general rules on
// effort, verification and final answers live in the cacheable workspace core
// instructions, so this per-turn developer message stays short.
const taskProgressInstructions = `Task tracking: task_update is the only persistent progress record. Ordinary Q&A and single-step edits need no task. For a multi-step programming task, create one before editing with the original goal, acceptance criteria and remaining implementation/verification work. Update it only at a stage change, an acceptance result, a blocker, or a scope change, then keep working; rewriting an unchanged plan is not progress.
An active task stays the objective when the user says continue or asks for status: answer status briefly and keep working unless told to stop or wait. For a new unrelated question, call task_scope with relation=side_question and a reason; it detaches only this turn and leaves the saved task unchanged. Never use side_question to drop work requested in the current turn. Replace the goal only when the user changes or cancels it, using replace_goal with a reason.
Mark a task completed only when every criterion has tool evidence, nothing remains and the targeted verification passed. Never remove criteria just to finish. Each passed verification must be a command this turn's execute_bash actually ran successfully after the last edit (optionally add its tool_call_id); a later edit makes it stale and it must be rerun. Re-assess saved blockers against the current request and continue when resolved. Progress tracking is advisory: a missing, stale or failed task_update must never stop substantive work or prevent a final answer. Do not repeat verification solely to repair progress metadata, and never record blocked or awaiting_input solely because progress recording failed. Reserve those statuses for concrete obstacles to the actual work or required user input. If tracking fails, explain the recording limitation briefly and report the actual work and verification results honestly.`

func (a *Agent) installTaskTool() {
	if a.taskStore == nil {
		return
	}
	a.installTaskScopeTool()
	a.toolDefinitions = append(a.toolDefinitions, tools.Definition{Type: "function", Function: tools.FunctionDefinition{
		Name: "task_update", Description: "The only persistent progress record. Use it for multi-step programming work, not for ordinary Q&A or single-step edits. Call it only at a stage change, an acceptance result, a blocker, or a scope change; do not resend an unchanged plan (identical state is ignored and is not progress). state is JSON containing goal, status (active/completed/blocked/awaiting_input/cancelled), criteria [{description,done,evidence}], remaining [string], verification [{command,tool_call_id (optional),status (passed/failed/unavailable),details}], reason. Completion requires all criteria backed by evidence, no remaining work, and passed verification whose command was actually run successfully by execute_bash in this turn after the last change. This record is advisory: recording failures must not block actual work or the final answer, and are not task blockers. Preserve the original goal and criteria unless the user changes scope.",
		Parameters: tools.JSONSchema{Type: "object", Properties: map[string]tools.JSONSchemaProperty{
			"state":        {Type: "string", Description: "Complete task state as a JSON object."},
			"replace_goal": {Type: "boolean", Description: "Only true for a new user-authorized objective or changed acceptance criteria; include reason in state and user_quote."},
			"user_quote":   {Type: "string", Description: "With replace_goal: a verbatim excerpt (at least 6 characters, or the whole short message) of the user's message in this turn that requests the change."},
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
		run := turnRun(ctx)
		replace, _ := args["replace_goal"].(bool)
		replaced := false
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
			if changed {
				if !replace || strings.TrimSpace(next.Reason) == "" {
					return "", fmt.Errorf("preserve the original goal and criteria; replacing them requires replace_goal and the user's scope-change reason")
				}
				quote, _ := args["user_quote"].(string)
				if run == nil || !run.quotedByUser(quote) {
					return "", fmt.Errorf("replacing the goal requires user_quote: a verbatim excerpt (at least %d characters, or the whole short message) of the user's request in this turn", minQuoteRunes)
				}
				if next.Status == "completed" {
					return "", fmt.Errorf("a replaced goal cannot be completed in the same update; record it active, do the work, then verify")
				}
				// Evidence for the old goal says nothing about the new one.
				next.Verification = nil
				replaced = true
			}
		}
		// Evidence is verified even for an unchanged state: a repeated
		// completion record must still be backed by real execution.
		if err = verifyCompletionEvidence(run, next); err != nil {
			return "", err
		}
		unchanged := a.task != nil && sameTaskState(*a.task, next)
		if !unchanged {
			if err = a.taskStore.SaveTaskState(next); err != nil {
				return "", err
			}
			a.task = &next
			run.emitTask(a.task)
		}
		if run != nil {
			run.scopeLocked = true
			run.mu.Lock()
			run.taskUpdated = true
			run.completionSaved = next.Status == "completed"
			run.completedClock = run.ledger.now()
			run.mu.Unlock()
			if replaced {
				run.ledger.barrier()
			}
		}
		if run != nil {
			switch next.Status {
			case "blocked":
				run.declareOutcome(turn.Blocked, next.Reason, false)
			case "awaiting_input":
				run.declareOutcome(turn.AwaitingInput, next.Reason, false)
			case "cancelled":
				run.declareOutcome(turn.Interrupted, next.Reason, false)
			default:
				run.declareOutcome(turn.Completed, "", false)
			}
		}
		if unchanged {
			return "Task state unchanged (no new information recorded). Continue substantive work instead of rewriting the plan; finish only after acceptance and verification, or explain the recorded blocker.", nil
		}
		return "Task state saved. Continue substantive work while active; finish only after acceptance and verification, or explain the recorded blocker.", nil
	}
}

// sameTaskState compares task records semantically, ignoring UpdatedAt and
// nil-versus-empty slices.
func sameTaskState(a, b taskstate.State) bool {
	normalize := func(state taskstate.State) string {
		state.UpdatedAt = time.Time{}
		if state.Criteria == nil {
			state.Criteria = []taskstate.Criterion{}
		}
		if state.Remaining == nil {
			state.Remaining = []string{}
		}
		if state.Verification == nil {
			state.Verification = []taskstate.Verification{}
		}
		data, _ := json.Marshal(state)
		return string(data)
	}
	return normalize(a) == normalize(b)
}

// emitTask publishes the full task state (or "null") as a task.updated event.
func (s *runState) emitTask(task *taskstate.State) {
	if s == nil || s.sink == nil {
		return
	}
	text, phase := "null", ""
	if task != nil {
		phase = task.Status
		if data, err := json.Marshal(task); err == nil {
			text = string(data)
		}
	}
	s.emit(s.id+"/task", "task.updated", phase, text)
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
		return turnOutcomeInstructions
	}
	text := taskProgressInstructions + "\n" + turnOutcomeInstructions
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
	tracker    *progressTracker
}

// observe counts only new substantive progress: bookkeeping tools and exact
// repeats of an earlier (tool, arguments, result) do not reset the stall count.
func (guard *continuationGuard) observe(call ToolCall, result string) {
	if guard.tracker == nil {
		guard.tracker = newProgressTracker()
	}
	if guard.tracker.observe(call, result) {
		guard.progressed = true
	}
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
	// Saved progress is advisory. Only an incomplete/intermediate response
	// warrants continuation; task metadata must not hold a final answer open.
	return ""
}

func (a *Agent) appendContinuation(messages []map[string]any, reason string) ([]map[string]any, error) {
	message := map[string]any{"role": "developer", "content": reason}
	if err := a.recordMessages(message); err != nil {
		return messages, err
	}
	return append(messages, message), nil
}

// verifyCompletionEvidence rejects a completed task whose passed verification
// is not backed by a real, still-valid execution recorded by the agent itself.
func verifyCompletionEvidence(run *runState, next taskstate.State) error {
	if next.Status != "completed" {
		return nil
	}
	if run == nil {
		return fmt.Errorf("completion requires verification executed in this turn; no turn execution record is available")
	}
	for _, check := range next.Verification {
		if check.Status == "passed" && !run.ledger.verified(check.ToolCallID, check.Command) {
			return fmt.Errorf("verification %q is not backed by a successful execute_bash call in this turn that ran exactly this command (matching tool_call_id when given) after the last edit or side-effecting command; run it now with execute_bash and record that command", check.Command)
		}
	}
	return nil
}

// reopenStaleCompletion keeps a completion record honest: if the workspace was
// changed after task_update saved "completed" and it was not re-verified, the
// saved task goes back to active so the next turn finishes the verification.
func (a *Agent) reopenStaleCompletion(run *runState) bool {
	if a.taskStore == nil || a.task == nil || a.task.Status != "completed" || run == nil {
		return false
	}
	run.mu.Lock()
	stale := run.completionSaved && run.ledger.mutatedSince(run.completedClock)
	run.mu.Unlock()
	if !stale {
		return false
	}
	reopened := *a.task
	reopened.Status = "active"
	reopened.Remaining = append(append([]string(nil), reopened.Remaining...), "re-run verification: files or commands changed after the completion record")
	reopened.Reason = "changed after completion was recorded; verification is stale"
	if err := a.taskStore.SaveTaskState(reopened); err != nil {
		a.logf("[Task] could not reopen stale completion: %v\n", err)
		return true
	}
	a.task = &reopened
	run.emitTask(a.task)
	run.mu.Lock()
	run.completionSaved = false
	run.mu.Unlock()
	return true
}
