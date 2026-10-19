package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"bqagent/internal/taskstate"
)

// taskUpdateParameters takes state as an object rather than a JSON string, so
// the model does not escape every quote twice, and offers update so a progress
// step resends only what changed instead of the whole record.
const taskUpdateParameters = `{
  "type": "object",
  "properties": {
    "state": {
      "type": "object",
      "description": "Complete task state. Use it to create the task or, with replace_goal, to change its scope.",
      "properties": {
        "goal": {"type": "string"},
        "status": {"type": "string", "enum": ["active", "completed", "blocked", "awaiting_input", "cancelled"]},
        "criteria": {"type": "array", "items": {"type": "object", "properties": {
          "description": {"type": "string"}, "done": {"type": "boolean"}, "evidence": {"type": "string"}
        }, "required": ["description"]}},
        "remaining": {"type": "array", "items": {"type": "string"}},
        "verification": {"type": "array", "items": {"type": "object", "properties": {
          "command": {"type": "string", "description": "The exact command string execute_bash ran."},
          "tool_call_id": {"type": "string"},
          "status": {"type": "string", "enum": ["passed", "failed", "unavailable"]},
          "details": {"type": "string"}
        }, "required": ["command", "status", "details"]}},
        "reason": {"type": "string"}
      },
      "required": ["goal", "status", "criteria"]
    },
    "update": {
      "type": "object",
      "description": "Changes to the saved task; omitted fields stay unchanged. Cannot change the goal or criterion descriptions.",
      "properties": {
        "status": {"type": "string", "enum": ["active", "completed", "blocked", "awaiting_input", "cancelled"]},
        "reason": {"type": "string"},
        "criteria": {"type": "array", "description": "Criteria to mark, by zero-based index in the saved task.", "items": {"type": "object", "properties": {
          "index": {"type": "integer"}, "done": {"type": "boolean"}, "evidence": {"type": "string"}
        }, "required": ["index", "done"]}},
        "remaining": {"type": "array", "items": {"type": "string"}, "description": "Replaces the remaining work; [] clears it."},
        "verification": {"type": "array", "description": "Added checks; an entry replaces a saved one with the same command.", "items": {"type": "object", "properties": {
          "command": {"type": "string", "description": "The exact command string execute_bash ran."},
          "tool_call_id": {"type": "string"},
          "status": {"type": "string", "enum": ["passed", "failed", "unavailable"]},
          "details": {"type": "string"}
        }, "required": ["command", "status", "details"]}}
      }
    },
    "replace_goal": {"type": "boolean", "description": "Only with state, for a new user-authorized objective or changed acceptance criteria; include reason in state and user_quote."},
    "user_quote": {"type": "string", "description": "With replace_goal: a verbatim excerpt (at least 6 characters, or the whole short message) of the user's message in this turn that requests the change."}
  }
}`

type taskCriterionUpdate struct {
	Index    *int   `json:"index"`
	Done     bool   `json:"done"`
	Evidence string `json:"evidence"`
}

type taskStateUpdate struct {
	Status       *string                  `json:"status"`
	Reason       *string                  `json:"reason"`
	Criteria     []taskCriterionUpdate    `json:"criteria"`
	Remaining    *[]string                `json:"remaining"`
	Verification []taskstate.Verification `json:"verification"`
}

// taskUpdateState resolves the arguments to the complete next state. The
// result goes through the same validation and evidence checks either way.
func (a *Agent) taskUpdateState(args map[string]any) (taskstate.State, error) {
	state, hasState := args["state"]
	update, hasUpdate := args["update"]
	switch {
	case hasState && hasUpdate:
		return taskstate.State{}, fmt.Errorf("pass either state or update, not both")
	case hasState:
		var next taskstate.State
		raw, err := argumentJSON(state)
		if err != nil {
			return next, fmt.Errorf("state: %w", err)
		}
		if err := json.Unmarshal(raw, &next); err != nil {
			return next, fmt.Errorf("state must be a task object: %w", err)
		}
		return next, nil
	case hasUpdate:
		if a.task == nil {
			return taskstate.State{}, fmt.Errorf("no saved task to update; create one with state first")
		}
		if replace, _ := args["replace_goal"].(bool); replace {
			return taskstate.State{}, fmt.Errorf("replace_goal requires the complete state, not update")
		}
		raw, err := argumentJSON(update)
		if err != nil {
			return taskstate.State{}, fmt.Errorf("update: %w", err)
		}
		var changes taskStateUpdate
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&changes); err != nil {
			return taskstate.State{}, fmt.Errorf("update must be an object with status, reason, criteria, remaining or verification: %w", err)
		}
		return applyTaskUpdate(*a.task, changes)
	}
	return taskstate.State{}, fmt.Errorf("tool %q requires state (to create or rescope the task) or update (to change the saved task)", "task_update")
}

// argumentJSON accepts the object form and, for older calls, a JSON string.
func argumentJSON(value any) ([]byte, error) {
	if text, ok := value.(string); ok {
		text = strings.TrimSpace(text)
		if !strings.HasPrefix(text, "{") {
			return nil, fmt.Errorf("must be a JSON object")
		}
		return []byte(text), nil
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, fmt.Errorf("must be a JSON object")
	}
	return json.Marshal(value)
}

func applyTaskUpdate(saved taskstate.State, changes taskStateUpdate) (taskstate.State, error) {
	next := saved
	next.Criteria = append([]taskstate.Criterion(nil), saved.Criteria...)
	next.Remaining = append([]string(nil), saved.Remaining...)
	next.Verification = append([]taskstate.Verification(nil), saved.Verification...)
	if changes.Status != nil {
		next.Status = *changes.Status
	}
	if changes.Reason != nil {
		next.Reason = *changes.Reason
	}
	for _, change := range changes.Criteria {
		if change.Index == nil || *change.Index < 0 || *change.Index >= len(next.Criteria) {
			return next, fmt.Errorf("criteria index must be 0-%d for the saved task", len(next.Criteria)-1)
		}
		criterion := &next.Criteria[*change.Index]
		criterion.Done = change.Done
		if change.Evidence != "" || !change.Done {
			criterion.Evidence = change.Evidence
		}
	}
	if changes.Remaining != nil {
		next.Remaining = append([]string(nil), (*changes.Remaining)...)
	}
	for _, check := range changes.Verification {
		replaced := false
		for i := range next.Verification {
			if strings.TrimSpace(next.Verification[i].Command) == strings.TrimSpace(check.Command) {
				next.Verification[i], replaced = check, true
			}
		}
		if !replaced {
			next.Verification = append(next.Verification, check)
		}
	}
	return next, nil
}
