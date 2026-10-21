package agent

import (
	"context"
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

func analysisState(status string, done bool, remaining ...string) map[string]any {
	criterion := map[string]any{"description": "report delivered", "done": done}
	if done {
		criterion["evidence"] = "read loop.go and grep results; findings listed in the report"
	}
	return map[string]any{"kind": "analysis", "goal": "review the loop", "status": status, "criteria": []any{criterion}, "remaining": remaining}
}

func TestAnalysisTaskCompletesWithoutVerification(t *testing.T) {
	app, store := evidenceApp(t)
	app.task, store.state = nil, nil
	ctx, _ := evidenceRun()
	update := func(state map[string]any) error {
		_, err := app.functions["task_update"](ctx, map[string]any{"state": state})
		return err
	}
	if err := update(analysisState("active", false, "write report")); err != nil {
		t.Fatal(err)
	}
	// Read-only shell the classifier cannot parse must not count as a change.
	app.runRegularToolCall(ctx, bashCall("r1", "find . -name '*.go' | xargs wc -l"))
	if err := update(analysisState("completed", false)); err == nil {
		t.Fatal("unfinished criterion must block completion")
	}
	if err := update(analysisState("completed", true, "write report")); err == nil {
		t.Fatal("remaining work must block completion")
	}
	if err := update(analysisState("completed", true)); err != nil {
		t.Fatalf("analysis completion must not need tests: %v", err)
	}
	if store.state == nil || store.state.Status != "completed" || !store.state.IsAnalysis() {
		t.Fatalf("saved state = %+v", store.state)
	}
}

func TestAnalysisKindCannotHideRealChanges(t *testing.T) {
	for name, mutate := range map[string]func(*Agent, context.Context){
		"edit tool":      func(app *Agent, ctx context.Context) { app.runRegularToolCall(ctx, editCall("e1")) },
		"shell redirect": func(app *Agent, ctx context.Context) { app.runRegularToolCall(ctx, bashCall("e1", "echo x > a.go")) },
		"sed -i": func(app *Agent, ctx context.Context) {
			app.runRegularToolCall(ctx, bashCall("e1", "sed -i s/a/b/ a.go"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			app, _ := evidenceApp(t)
			ctx, _ := evidenceRun()
			mutate(app, ctx)
			_, err := app.functions["task_update"](ctx, map[string]any{"state": analysisState("completed", true)})
			if err == nil || strings.Contains(err.Error(), "go test") || strings.Contains(strings.ToLower(err.Error()), "run a relevant check") {
				t.Fatalf("analysis after a write must be rejected without pushing extra tests: %v", err)
			}
		})
	}
}

func TestAnalysisKindCannotBeSwitchedIncrementally(t *testing.T) {
	app, store := evidenceApp(t) // saved task has default (code) kind
	ctx, _ := evidenceRun()
	if _, err := app.functions["task_update"](ctx, map[string]any{"update": map[string]any{"kind": "analysis"}}); err == nil {
		t.Fatal("update must not accept kind")
	}
	state := analysisState("completed", true)
	state["goal"] = store.state.Goal
	state["criteria"] = []any{map[string]any{"description": "switch works", "done": true, "evidence": "x"}}
	if _, err := app.functions["task_update"](ctx, map[string]any{"state": state}); err == nil || !strings.Contains(err.Error(), "preserve the original goal") {
		t.Fatalf("switching a code task to analysis needs replace_goal: %v", err)
	}
}

func TestLegacyTaskWithoutKindStillNeedsVerification(t *testing.T) {
	var legacy taskstate.State
	if err := json.Unmarshal([]byte(`{"goal":"g","status":"completed","criteria":[{"description":"c","done":true,"evidence":"e"}],"remaining":[]}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.IsAnalysis() || legacy.Validate() == nil {
		t.Fatalf("legacy kind-less task must keep code completion rules: %v", legacy.Validate())
	}
	legacy.Kind = "analysis"
	if err := legacy.Validate(); err != nil {
		t.Fatal(err)
	}
	legacy.Kind = "docs"
	if legacy.Validate() == nil {
		t.Fatal("unknown kind must be rejected")
	}
}

func TestAnalysisVerificationMayBeRealReadOnlyExecution(t *testing.T) {
	app, store := evidenceApp(t, "go list ./bad")
	app.task, store.state = nil, nil
	ctx, _ := evidenceRun()
	app.runRegularToolCall(ctx, bashCall("r1", "go list ./..."))
	app.runRegularToolCall(ctx, bashCall("r2", "go list ./bad"))
	with := func(command string) error {
		state := analysisState("completed", true)
		state["verification"] = []any{map[string]any{"command": command, "status": "passed", "details": "listed packages"}}
		_, err := app.functions["task_update"](ctx, map[string]any{"state": state})
		return err
	}
	if err := with("go list ./..."); err != nil {
		t.Fatalf("real read-only run must be accepted for analysis: %v", err)
	}
	if err := with("go list"); err == nil || !strings.Contains(err.Error(), "closest bash") || strings.Contains(err.Error(), "not a test/build/lint") {
		t.Fatalf("analysis command mismatch should request the exact command without requiring tests: %v", err)
	}
	for _, command := range []string{"go list ./never-ran", "go list ./bad"} {
		err := with(command)
		if err == nil || strings.Contains(err.Error(), "rerun the check") || !strings.Contains(err.Error(), "do not rerun") {
			t.Fatalf("%s must be rejected without nudging reruns: %v", command, err)
		}
	}
}

func TestExplicitShellWriteIgnoresQuotedText(t *testing.T) {
	for command, want := range map[string]bool{
		`grep -n "a > b; rm x" file.go`:  false,
		`echo 'rm -rf /' | wc -l`:        false,
		`rg "x|y" internal`:              false,
		`find . -name '*.go' | xargs wc`: false,
		`grep foo $(cat list)`:           false, // unparseable is not a definite write
		`echo hi > out.txt`:              true,
		`ls && rm a.go`:                  true,
		`git commit -m "x > y"`:          true,
		`sed -i.bak s/a/b/ a.go`:         true,
	} {
		if got := explicitShellWrite(command); got != want {
			t.Errorf("explicitShellWrite(%q) = %v, want %v", command, got, want)
		}
	}
}
