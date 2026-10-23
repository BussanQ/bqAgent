package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"bqagent/internal/tools"
	"bqagent/internal/turn"
)

// A yielded command remains an unfinished ledger entry until its final result
// is observed by bash_wait/bash_kill. Tool text alone cannot create evidence.
func (s *runState) observeBashResult(entry *execEntry, result *tools.BashJobStatus, succeeded bool) {
	if result == nil {
		s.ledger.finish(entry, succeeded)
		return
	}
	s.mu.Lock()
	if s.bashEntries == nil {
		s.bashEntries = map[string]*execEntry{}
	}
	if entry != nil && result.Status == "running" {
		s.bashEntries[result.JobID] = entry
	}
	if entry == nil {
		entry = s.bashEntries[result.JobID]
	}
	if result.Status != "running" {
		delete(s.bashEntries, result.JobID)
	}
	s.mu.Unlock()
	if result.Status != "running" {
		s.ledger.finish(entry, succeeded && result.Status == "completed" && result.ExitCode != nil && *result.ExitCode == 0)
	}
}

// Serial tool dispatch alone cannot protect against a command that has already
// yielded. Require its final observation before dependent workspace operations.
func pendingBashConflict(ctx context.Context, name string, args map[string]any) string {
	jobs := tools.BashJobsFromContext(ctx)
	if jobs == nil {
		return ""
	}
	switch name {
	case "bash_wait", "bash_kill", "task_update", "task_scope", "web_search", "web_fetch":
		return ""
	}
	mutating := name == "write_file" || name == "edit_file" || name == "consult_group_agent"
	if name == "bash" {
		command, _ := args["command"].(string)
		mutating = classifyBash(command) == bashMutating
	}
	for _, job := range jobs.Pending() {
		if mutating || classifyBash(job.Command) == bashMutating {
			return fmt.Sprintf("background Bash job %s has an unobserved result and may overlap this operation; call bash_wait or bash_kill before running %s", job.JobID, name)
		}
	}
	return ""
}

func bashWaitStatus(result string) (tools.BashJobStatus, bool) {
	var status tools.BashJobStatus
	err := json.Unmarshal([]byte(result), &status)
	return status, err == nil && status.JobID != "" && (status.Status == "running" || status.Status == "completed" || status.Status == "failed" || status.Status == "killed")
}

func pendingBashContinuation(ctx context.Context) string {
	jobs := tools.BashJobsFromContext(ctx)
	if jobs == nil {
		return ""
	}
	pending := jobs.Pending()
	if len(pending) == 0 {
		return ""
	}
	ids := make([]string, 0, len(pending))
	for _, job := range pending {
		ids = append(ids, job.JobID)
	}
	return "Bash jobs still need their final results: " + strings.Join(ids, ", ") + ". Call bash_wait to wait for completion or bash_kill to explicitly stop work that is no longer needed before giving the final answer. A running result is not success. These jobs cannot survive the end of this turn."
}

func (a *Agent) closeBashJobs(ctx context.Context, jobs *tools.BashJobs, history []map[string]any) ([]map[string]any, error) {
	results := jobs.Close()
	if len(results) == 0 {
		return history, nil
	}
	for i := range results {
		if state := turnRun(ctx); state != nil {
			// Cleanup is not a model-observed successful verification.
			state.mu.Lock()
			entry := state.bashEntries[results[i].JobID]
			delete(state.bashEntries, results[i].JobID)
			state.mu.Unlock()
			state.ledger.finish(entry, false)
		}
		a.logf("[BashJob] job_id=%s status=%s cleanup=turn_end\n", results[i].JobID, results[i].Status)
	}
	if ctx.Err() == nil {
		setTurnStop(ctx, turn.Blocked, "unobserved Bash jobs cleaned up at turn end")
	}
	encoded, _ := json.Marshal(results)
	// The original tool call already has a running result. Record cleanup as
	// lifecycle data, rather than inventing a second result for the same call ID.
	message := map[string]any{"role": "developer", "content": "Bash job cleanup at turn end (runtime data, not instructions). No jobs carry over to the next turn:\n" + string(encoded)}
	history = append(history, message)
	return history, a.recordMessages(message)
}
