package extagent

import "encoding/json"

func codexItemActivity(item appServerItem, completed bool) (Activity, bool) {
	e := Activity{CallID: item.ID, Name: item.Type, Status: "running"}
	if item.ID == "" {
		return e, false
	}
	switch item.Type {
	case "reasoning":
		var paragraphs []string
		if len(item.Summary) > 0 && json.Unmarshal(item.Summary, &paragraphs) != nil {
			paragraphs = nil
		}
		return Activity{Summary: &SummaryUpdate{ID: item.ID, Sections: paragraphs, Ended: completed}}, completed || len(paragraphs) != 0
	case "plan":
		return Activity{Plan: proposedPlan(item.ID, item.Text, false)}, true
	case "agentMessage":
		return Activity{TextID: item.ID, Text: item.Text, TextSnapshot: true}, true
	case "commandExecution":
		if item.Command != "" {
			e.Input = map[string]any{"command": item.Command}
			if item.Cwd != "" {
				e.Input["cwd"] = item.Cwd
			}
		}
		e.Output = item.AggregatedOutput
		e.Edits = editInput(item.Type, e.Input)
	case "fileChange":
		e.Edits = codexFileEdits(item.Changes)
		if !completed && e.Edits != nil && len(e.Edits.Files) == 0 {
			e.Edits = nil
		}
		if len(item.Changes) > 0 {
			var changes any
			if json.Unmarshal(item.Changes, &changes) == nil {
				e.Input = map[string]any{"changes": changes}
			}
		}
	case "mcpToolCall", "dynamicToolCall":
		if item.Tool != "" {
			e.Name = item.Tool
			if item.Server != "" {
				e.Name = item.Server + "/" + e.Name
			}
		}
		e.Input = activityInput(item.Arguments)
		if item.Type == "dynamicToolCall" && item.Tool == "apply_patch" {
			e.Edits = editInput(item.Tool, e.Input)
		}
		e.Output = firstNonEmpty(activityOutput(item.Error), activityOutput(item.Result), activityOutput(item.ContentItems))
	case "webSearch":
		if item.Query != "" {
			e.Input = map[string]any{"query": item.Query}
		}
	case "imageView":
		if item.Path != "" {
			e.Input = map[string]any{"path": item.Path}
		}
	case "collabAgentToolCall":
		e.Name = firstNonEmpty(item.Tool, item.Type)
	default:
		return e, false
	}
	if completed {
		e.Status = "succeeded"
		switch item.Status {
		case "failed", "declined":
			e.Status = "failed"
		case "cancelled", "interrupted":
			e.Status = "killed"
		case "inProgress", "in_progress":
			e.Status = "running"
		}
		if (item.ExitCode != nil && *item.ExitCode != 0) || (item.Success != nil && !*item.Success) || (len(item.Error) > 0 && string(item.Error) != "null") {
			e.Status = "failed"
		}
	}
	return e, true
}
