package extagent

import "bqagent/internal/transcript"

type codexPlanStep struct{ Step, Status string }
type acpPlanEntry struct{ Content, Status, Priority string }
type codexTodo struct {
	Text      string
	Completed bool
}

func codexSteps(explanation string, entries []codexPlanStep) *PlanUpdate {
	p := &PlanUpdate{ID: "steps", Text: explanation, Plan: transcript.Plan{Mode: "steps"}}
	for _, entry := range entries {
		status := entry.Status
		if status == "inProgress" {
			status = "in_progress"
		}
		p.Plan.Steps = append(p.Plan.Steps, transcript.PlanStep{Text: entry.Step, Status: status})
	}
	return p
}

func acpSteps(entries []acpPlanEntry) *PlanUpdate {
	p := &PlanUpdate{ID: "steps", Plan: transcript.Plan{Mode: "steps"}}
	for _, entry := range entries {
		p.Plan.Steps = append(p.Plan.Steps, transcript.PlanStep{Text: entry.Content, Status: entry.Status, Priority: entry.Priority})
	}
	return p
}

func cliSteps(id string, entries []codexTodo) *PlanUpdate {
	p := &PlanUpdate{ID: "todo/" + id, Plan: transcript.Plan{Mode: "steps"}}
	for _, entry := range entries {
		status := "pending"
		if entry.Completed {
			status = "completed"
		}
		p.Plan.Steps = append(p.Plan.Steps, transcript.PlanStep{Text: entry.Text, Status: status})
	}
	return p
}

func proposedPlan(id, text string, appendText bool) *PlanUpdate {
	return &PlanUpdate{ID: "item/" + id, Text: text, Append: appendText, Plan: transcript.Plan{Mode: "proposal"}}
}
