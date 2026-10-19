package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"bqagent/internal/taskstate"
)

func TestTaskUpdateDefinitionUsesObjectArguments(t *testing.T) {
	app, _ := evidenceApp(t)
	for _, definition := range app.toolDefinitions {
		if definition.Function.Name != "task_update" {
			continue
		}
		var schema struct {
			Properties map[string]struct {
				Type string `json:"type"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(definition.Function.RawParameters, &schema); err != nil {
			t.Fatal(err)
		}
		if schema.Properties["state"].Type != "object" || schema.Properties["update"].Type != "object" {
			t.Fatalf("state and update must be objects: %+v", schema.Properties)
		}
		return
	}
	t.Fatal("task_update missing")
}

func TestTaskUpdateAcceptsStateObject(t *testing.T) {
	app, store := evidenceApp(t)
	app.task, store.state = nil, nil
	ctx, _ := evidenceRun()
	state := map[string]any{"goal": "ship", "status": "active", "criteria": []any{map[string]any{"description": "works"}}, "remaining": []any{"implement"}}
	if _, err := app.functions["task_update"](ctx, map[string]any{"state": state}); err != nil {
		t.Fatal(err)
	}
	if store.state == nil || store.state.Goal != "ship" || store.state.Remaining[0] != "implement" {
		t.Fatalf("saved=%+v", store.state)
	}
}

func TestTaskUpdateIncrementallyCompletesSavedTask(t *testing.T) {
	app, store := evidenceApp(t)
	ctx, _ := evidenceRun()
	app.runRegularToolCall(ctx, bashCall("t1", "go test ./x"))
	update := map[string]any{
		"status":       "completed",
		"criteria":     []any{map[string]any{"index": 0, "done": true, "evidence": "go test ./x passed"}},
		"remaining":    []any{},
		"verification": []any{map[string]any{"command": "go test ./x", "status": "passed", "details": "ok"}},
	}
	if _, err := app.functions["task_update"](ctx, map[string]any{"update": update}); err != nil {
		t.Fatal(err)
	}
	saved := store.state
	if saved.Goal != "implement fast" || saved.Status != "completed" || !saved.Criteria[0].Done || len(saved.Remaining) != 0 || len(saved.Verification) != 1 {
		t.Fatalf("saved=%+v", saved)
	}
}

func TestTaskUpdateIncrementalChangesKeepUnmentionedFields(t *testing.T) {
	saved := taskstate.State{Goal: "g", Status: "active", Criteria: []taskstate.Criterion{{Description: "a"}, {Description: "b"}}, Remaining: []string{"x"},
		Verification: []taskstate.Verification{{Command: "go test ./a", Status: "failed", Details: "boom"}}}
	index := 1
	next, err := applyTaskUpdate(saved, taskStateUpdate{
		Criteria:     []taskCriterionUpdate{{Index: &index, Done: true, Evidence: "ok"}},
		Verification: []taskstate.Verification{{Command: "go test ./a", Status: "passed", Details: "ok"}, {Command: "go vet ./a", Status: "passed", Details: "ok"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if next.Criteria[0].Done || !next.Criteria[1].Done || next.Remaining[0] != "x" || len(next.Verification) != 2 || next.Verification[0].Status != "passed" {
		t.Fatalf("next=%+v", next)
	}
	if saved.Criteria[1].Done || saved.Verification[0].Status != "failed" {
		t.Fatal("update mutated the saved task")
	}
}

func TestTaskUpdateRejectsInvalidIncrementalArguments(t *testing.T) {
	app, _ := evidenceApp(t)
	ctx, _ := evidenceRun()
	for _, tc := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{}, "requires state"},
		{map[string]any{"state": map[string]any{}, "update": map[string]any{}}, "either state or update"},
		{map[string]any{"update": map[string]any{"goal": "new"}}, "unknown field"},
		{map[string]any{"update": map[string]any{"criteria": []any{map[string]any{"index": 3, "done": true, "evidence": "x"}}}}, "criteria index"},
		{map[string]any{"update": map[string]any{"status": "done"}, "replace_goal": true}, "complete state"},
	} {
		_, err := app.functions["task_update"](ctx, tc.args)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("args=%v err=%v, want %q", tc.args, err, tc.want)
		}
	}
	app.task = nil
	if _, err := app.functions["task_update"](ctx, map[string]any{"update": map[string]any{"status": "active"}}); err == nil || !strings.Contains(err.Error(), "no saved task") {
		t.Fatalf("update without saved task: %v", err)
	}
}
