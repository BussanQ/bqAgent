package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"bqagent/internal/taskstate"
	"bqagent/internal/tools"
	"bqagent/internal/turn"
)

func evidenceRun() (context.Context, *runState) {
	run := &runState{outcome: turn.Outcome{TaskRelation: turn.ContinueTask}}
	return context.WithValue(context.Background(), runStateKey{}, run), run
}

func bashCall(id, command string) ToolCall {
	arguments, _ := json.Marshal(map[string]string{"command": command})
	return ToolCall{ID: id, Function: FunctionCall{Name: "execute_bash", Arguments: string(arguments)}}
}

func editCall(id string) ToolCall {
	return ToolCall{ID: id, Function: FunctionCall{Name: "edit_file", Arguments: `{"path":"a.go"}`}}
}

func completedWith(checks ...taskstate.Verification) taskstate.State {
	return taskstate.State{Goal: "implement fast", Status: "completed", Criteria: []taskstate.Criterion{{Description: "switch works", Done: true, Evidence: "ran tests"}}, Verification: checks}
}

func evidenceApp(t *testing.T, failCommands ...string) (*Agent, *memoryTaskStore) {
	t.Helper()
	active := activeTestTask()
	store := &memoryTaskStore{state: &active}
	app := NewWithOptions(&stubClient{}, "test", Options{TaskStore: store, Functions: map[string]tools.Function{
		"execute_bash": func(_ context.Context, args map[string]any) (string, error) {
			command, _ := args["command"].(string)
			for _, failing := range failCommands {
				if command == failing {
					return "FAIL", fmt.Errorf("exit status 1")
				}
			}
			return "ok", nil
		},
		"edit_file":  func(context.Context, map[string]any) (string, error) { return "edited", nil },
		"write_file": func(context.Context, map[string]any) (string, error) { return "written", nil },
	}})
	app.task = &active
	return app, store
}

func submitCompletion(app *Agent, ctx context.Context, state taskstate.State) error {
	raw, _ := json.Marshal(state)
	_, err := app.functions["task_update"](ctx, map[string]any{"state": string(raw)})
	return err
}

func TestTaskCompletionEvidenceMustMatchRealExecution(t *testing.T) {
	passed := func(command, id string) taskstate.Verification {
		return taskstate.Verification{Command: command, ToolCallID: id, Status: "passed", Details: "ok"}
	}
	tests := []struct {
		name    string
		run     func(app *Agent, ctx context.Context)
		check   taskstate.Verification
		wantErr bool
	}{
		{"model claim without any execution", func(*Agent, context.Context) {}, passed("go test ./x", ""), true},
		{"matching successful command", func(app *Agent, ctx context.Context) { app.runRegularToolCall(ctx, bashCall("b1", "go test ./x")) }, passed("go test ./x", ""), false},
		{"matching command and call id", func(app *Agent, ctx context.Context) { app.runRegularToolCall(ctx, bashCall("b1", "go test ./x")) }, passed("go test ./x", "b1"), false},
		{"call id of another command", func(app *Agent, ctx context.Context) {
			app.runRegularToolCall(ctx, bashCall("b1", "go test ./x"))
			app.runRegularToolCall(ctx, bashCall("b2", "go vet ./x"))
		}, passed("go test ./x", "b2"), true},
		{"different command than executed", func(app *Agent, ctx context.Context) { app.runRegularToolCall(ctx, bashCall("b1", "go test ./y")) }, passed("go test ./x", ""), true},
		{"command failed", func(app *Agent, ctx context.Context) { app.runRegularToolCall(ctx, bashCall("b1", "go test ./bad")) }, passed("go test ./bad", ""), true},
		{"edit after verification", func(app *Agent, ctx context.Context) {
			app.runRegularToolCall(ctx, bashCall("b1", "go test ./x"))
			app.runRegularToolCall(ctx, editCall("e1"))
		}, passed("go test ./x", ""), true},
		{"edit before verification", func(app *Agent, ctx context.Context) {
			app.runRegularToolCall(ctx, editCall("e1"))
			app.runRegularToolCall(ctx, bashCall("b1", "go test ./x"))
		}, passed("go test ./x", ""), false},
		{"side-effecting shell after verification", func(app *Agent, ctx context.Context) {
			app.runRegularToolCall(ctx, bashCall("b1", "go test ./x"))
			app.runRegularToolCall(ctx, bashCall("b2", "./generate.sh"))
		}, passed("go test ./x", ""), true},
		{"read-only shell after verification", func(app *Agent, ctx context.Context) {
			app.runRegularToolCall(ctx, bashCall("b1", "go test ./x"))
			app.runRegularToolCall(ctx, bashCall("b2", "git diff --stat"))
		}, passed("go test ./x", ""), false},
		{"failure masked by || true", func(app *Agent, ctx context.Context) {
			app.runRegularToolCall(ctx, bashCall("b1", "go test ./bad || true"))
		}, passed("go test ./bad || true", ""), true},
		{"failure masked by semicolon", func(app *Agent, ctx context.Context) {
			app.runRegularToolCall(ctx, bashCall("b1", "go test ./x; echo done"))
		}, passed("go test ./x; echo done", ""), true},
		{"failure masked by pipeline", func(app *Agent, ctx context.Context) {
			app.runRegularToolCall(ctx, bashCall("b1", "go test ./x 2>&1 | tail -5"))
		}, passed("go test ./x 2>&1 | tail -5", ""), true},
		{"pipeline with pipefail", func(app *Agent, ctx context.Context) {
			app.runRegularToolCall(ctx, bashCall("b1", "set -o pipefail; go test ./x 2>&1 | tail -5"))
		}, passed("set -o pipefail; go test ./x 2>&1 | tail -5", ""), false},
		{"and-chain of checks", func(app *Agent, ctx context.Context) {
			app.runRegularToolCall(ctx, bashCall("b1", "go vet ./x && go test ./x"))
		}, passed("go vet ./x && go test ./x", ""), false},
		{"echo is not verification", func(app *Agent, ctx context.Context) { app.runRegularToolCall(ctx, bashCall("b1", "echo PASS")) }, passed("echo PASS", ""), true},
		{"failed edit may have partially written", func(app *Agent, ctx context.Context) {
			app.runRegularToolCall(ctx, bashCall("b1", "go test ./x"))
			run := turnRun(ctx)
			run.ledger.finish(run.ledger.begin("e1", "edit_file", nil), false)
		}, passed("go test ./x", ""), true},
		{"rerun after edit restores evidence", func(app *Agent, ctx context.Context) {
			app.runRegularToolCall(ctx, bashCall("b1", "go test ./x"))
			app.runRegularToolCall(ctx, editCall("e1"))
			app.runRegularToolCall(ctx, bashCall("b2", "go test ./x"))
		}, passed("go test ./x", "b2"), false},
		{"stale call id stays stale after edit", func(app *Agent, ctx context.Context) {
			app.runRegularToolCall(ctx, bashCall("b1", "go test ./x"))
			app.runRegularToolCall(ctx, editCall("e1"))
			app.runRegularToolCall(ctx, bashCall("b2", "go test ./x"))
		}, passed("go test ./x", "b1"), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app, store := evidenceApp(t, "go test ./bad")
			ctx, _ := evidenceRun()
			tt.run(app, ctx)
			err := submitCompletion(app, ctx, completedWith(tt.check))
			if (err != nil) != tt.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tt.wantErr)
			}
			if tt.wantErr && store.state.Status != "active" {
				t.Fatalf("rejected completion was saved: %+v", store.state)
			}
		})
	}
	// Without a turn record, completion cannot be proven at all.
	app, _ := evidenceApp(t)
	if err := submitCompletion(app, context.Background(), completedWith(passed("go test ./x", ""))); err == nil {
		t.Fatal("completion accepted without an execution record")
	}
}

func TestTaskEvidenceConcurrentCallsDoNotShareEvidence(t *testing.T) {
	app, _ := evidenceApp(t, "go test ./bad")
	ctx, run := evidenceRun()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			app.runRegularToolCall(ctx, bashCall(fmt.Sprintf("ok-%d", i), fmt.Sprintf("go test ./pkg%d", i)))
			app.runRegularToolCall(ctx, bashCall(fmt.Sprintf("bad-%d", i), "go test ./bad"))
		}(i)
	}
	wg.Wait()
	for i := 0; i < 8; i++ {
		if !run.ledger.verified(fmt.Sprintf("ok-%d", i), fmt.Sprintf("go test ./pkg%d", i)) {
			t.Fatalf("call %d lost its own evidence", i)
		}
		if run.ledger.verified(fmt.Sprintf("ok-%d", i), "go test ./pkg9") || run.ledger.verified(fmt.Sprintf("bad-%d", i), "go test ./bad") {
			t.Fatalf("call %d produced foreign or failed evidence", i)
		}
	}
	// A mutation that overlaps a verification (still running) invalidates it.
	entry := run.ledger.begin("slow", "execute_bash", map[string]any{"command": "./slow.sh"})
	if run.ledger.verified("ok-0", "go test ./pkg0") {
		t.Fatal("evidence stayed valid while a mutation is running")
	}
	run.ledger.finish(entry, true)
	if run.ledger.verified("ok-0", "go test ./pkg0") {
		t.Fatal("evidence stayed valid after a later mutation")
	}
	app.runRegularToolCall(ctx, bashCall("late", "go test ./pkg0"))
	if !run.ledger.verified("late", "go test ./pkg0") {
		t.Fatal("rerun after mutation was not accepted")
	}
}

func TestClassifyBashIsConservative(t *testing.T) {
	for command, want := range map[string]bashKind{
		"go test ./...": bashVerify,
		"go test ./internal/agent -run '^TestX$'": bashVerify,
		"cd sub && go test ./... 2>&1":            bashVerify,
		"go vet ./... && go build ./...":          bashVerify,
		"pytest -q tests/test_a.py":               bashVerify,
		"npm run test":                            bashVerify,
		"git diff --stat":                         bashReadOnly,
		"grep -rn foo . | head":                   bashReadOnly,
		"go test ./... > out.txt":                 bashMutating,
		"go test ./... | tee out.txt":             bashMutating,
		"go test ./... && rm -rf build":           bashMutating,
		"go build -o bin/app ./cmd/app":           bashMutating,
		"sed -i 's/a/b/' f.go":                    bashMutating,
		"go test ./... $(touch x)":                bashMutating,
		"go test ./... &":                         bashMutating,
		"git checkout main":                       bashMutating,
		"./verify.sh":                             bashMutating,
		"find . -name '*.go' -delete":             bashMutating,
		"":                                        bashMutating,
		"go mod tidy":                             bashMutating,
		"make deploy":                             bashMutating,
		"gofmt -w a.go":                           bashMutating,
		"FOO=1 go test ./...":                     bashVerify,
		"go test ./... >/dev/null 2>&1; echo $?":  bashVerify,
		"go test ./... || echo failed":            bashVerify,
		"python3 -m pytest -x":                    bashVerify,
		"python3 script.py":                       bashMutating,
		"git status && git commit -am x":          bashMutating,
		"cat a.go | go test ./...":                bashVerify,
		"echo hi >> notes.txt":                    bashMutating,
		"cargo test --workspace":                  bashVerify,
		"cargo install foo":                       bashMutating,
		"find . -name a.go":                       bashMutating,
		"git stash":                               bashMutating,
		"yarn install":                            bashMutating,
		"npm run deploy":                          bashMutating,
	} {
		if got := classifyBash(command); got != want {
			t.Errorf("classifyBash(%q)=%v want %v", command, got, want)
		}
	}
}

func TestHasSpecialToolCallsSerializesSideEffectingShell(t *testing.T) {
	app := NewWithOptions(&stubClient{}, "test", Options{})
	read := ToolCall{ID: "r", Function: FunctionCall{Name: "read_file", Arguments: `{}`}}
	if app.hasSpecialToolCalls([]ToolCall{bashCall("a", "go test ./a"), bashCall("b", "go test ./b"), read}) {
		t.Fatal("independent verifiers should stay parallel")
	}
	if !app.hasSpecialToolCalls([]ToolCall{bashCall("a", "go test ./a"), bashCall("b", "./gen.sh")}) {
		t.Fatal("side-effecting shell must run in model order")
	}
	if !app.hasSpecialToolCalls([]ToolCall{bashCall("a", "go test ./a"), {ID: "bad", Function: FunctionCall{Name: "execute_bash", Arguments: `{`}}}) {
		t.Fatal("unparseable shell arguments must be treated as side-effecting")
	}
	if app.hasSpecialToolCalls([]ToolCall{bashCall("a", "./gen.sh")}) {
		t.Fatal("a lone call needs no serialization")
	}
}

func TestTaskScopeSideQuestionRejectedAfterMutation(t *testing.T) {
	for name, mutate := range map[string]func(*Agent, context.Context){
		"edit":           func(app *Agent, ctx context.Context) { app.runRegularToolCall(ctx, editCall("e")) },
		"mutating shell": func(app *Agent, ctx context.Context) { app.runRegularToolCall(ctx, bashCall("b", "./gen.sh")) },
	} {
		t.Run(name, func(t *testing.T) {
			app, _ := evidenceApp(t)
			ctx, run := evidenceRun()
			mutate(app, ctx)
			if _, err := app.functions["task_scope"](ctx, map[string]any{"relation": "side_question", "reason": "unrelated"}); err == nil || run.outcome.TaskRelation != turn.ContinueTask {
				t.Fatalf("detached after a write: err=%v relation=%s", err, run.outcome.TaskRelation)
			}
		})
	}
	app, _ := evidenceApp(t)
	ctx, run := evidenceRun()
	app.runRegularToolCall(ctx, bashCall("b", "go test ./x"))
	app.runRegularToolCall(ctx, ToolCall{ID: "failed", Function: FunctionCall{Name: "edit_file", Arguments: `{"path":"x"}`}})
	run.ledger.finish(run.ledger.begin("rej", "edit_file", nil), false)
	if _, err := app.functions["task_scope"](ctx, map[string]any{"relation": "side_question", "reason": "unrelated"}); err == nil {
		t.Fatal("successful edit above should have blocked detaching")
	}
	app, _ = evidenceApp(t)
	ctx, run = evidenceRun()
	app.runRegularToolCall(ctx, bashCall("b", "git status"))
	if _, err := app.functions["task_scope"](ctx, map[string]any{"relation": "side_question", "reason": "unrelated"}); err != nil || run.outcome.TaskRelation != turn.SideQuestion {
		t.Fatalf("read-only turn could not detach: %v", err)
	}
}

func TestReplaceGoalRequiresUserQuoteNotKeywords(t *testing.T) {
	replaced := activeTestTask()
	replaced.Goal = "rewrite the CLI in Rust"
	replaced.Reason = "user changed the objective"
	raw, _ := json.Marshal(replaced)
	update := func(app *Agent, ctx context.Context, args map[string]any) error {
		args["state"] = string(raw)
		_, err := app.functions["task_update"](ctx, args)
		return err
	}
	app, store := evidenceApp(t)
	ctx, run := evidenceRun()
	run.acceptUserInput("先别管之前的，把整个 CLI 用 Rust 重写一遍")
	for name, args := range map[string]map[string]any{
		"no flag":        {"user_quote": "把整个 CLI 用 Rust 重写一遍"},
		"no quote":       {"replace_goal": true},
		"invented quote": {"replace_goal": true, "user_quote": "the user approved a rewrite"},
		"single word":    {"replace_goal": true, "user_quote": "Rust"},
	} {
		if err := update(app, ctx, args); err == nil || store.state.Goal != "implement fast" {
			t.Fatalf("%s: goal replaced without grounded authorization (err=%v)", name, err)
		}
	}
	if err := update(app, ctx, map[string]any{"replace_goal": true, "user_quote": "把整个  CLI 用 Rust\n重写一遍"}); err != nil || store.state.Goal != replaced.Goal {
		t.Fatalf("grounded replacement rejected: %v", err)
	}
	// Old evidence must not carry over, and the new goal cannot complete at once.
	app, _ = evidenceApp(t)
	ctx, run = evidenceRun()
	run.acceptUserInput("please switch the goal to a full rewrite")
	done := replaced
	done.Status = "completed"
	done.Criteria = []taskstate.Criterion{{Description: "switch works", Done: true, Evidence: "x"}}
	done.Remaining = nil
	done.Verification = []taskstate.Verification{{Command: "go test ./x", Status: "passed", Details: "ok"}}
	raw, _ = json.Marshal(done)
	app.runRegularToolCall(ctx, bashCall("b", "go test ./x"))
	if err := update(app, ctx, map[string]any{"replace_goal": true, "user_quote": "switch the goal to a full rewrite"}); err == nil {
		t.Fatal("replaced goal completed in the same update")
	}
}

func TestAcceptUserInputRearmsEvidenceAndQuotes(t *testing.T) {
	app, _ := evidenceApp(t)
	ctx, run := evidenceRun()
	app.runRegularToolCall(ctx, bashCall("b", "go test ./x"))
	if !run.ledger.verified("b", "go test ./x") {
		t.Fatal("setup failed")
	}
	run.acceptUserInput("steering: also handle the edge case")
	if run.ledger.verified("b", "go test ./x") {
		t.Fatal("verification gathered before new user input still counts")
	}
	if run.ledger.hasMutation(0) {
		t.Fatal("a user-input barrier must not count as a workspace mutation")
	}
	if !run.quotedByUser("also handle the edge case") || run.quotedByUser("edge") {
		t.Fatal("quote grounding wrong")
	}
	run.mu.Lock()
	run.taskUpdated = true
	run.mu.Unlock()
	run.acceptUserInput("again")
	if run.taskUpdated {
		t.Fatal("new input must require the saved task to be re-assessed")
	}
}

func reassessReply(status string) AssistantMessage {
	state := activeTestTask()
	state.Status = status
	if status != "active" {
		state.Reason = "still waiting for credentials"
	}
	return taskUpdateMessage(state)
}

func TestBlockedTaskContinueRequiresReassessment(t *testing.T) {
	for _, status := range []string{"blocked", "awaiting_input"} {
		blocked := activeTestTask()
		blocked.Status = status
		blocked.Reason = "waiting for credentials"
		t.Run(status+"/reassessed active then continues", func(t *testing.T) {
			store := &memoryTaskStore{state: &blocked}
			client := &stubClient{responses: []AssistantMessage{{Content: "收到"}, reassessReply("active"), {Content: "继续修复中", Phase: "commentary"}}}
			app := NewWithOptions(client, "test", Options{TaskStore: store})
			result, err := app.RunTurn(context.Background(), []map[string]any{{"role": "user", "content": "凭据已经提供了，继续"}}, 3)
			if err != nil || store.state.Status != "active" || len(client.messages) != 3 {
				t.Fatalf("status=%s calls=%d err=%v", store.state.Status, len(client.messages), err)
			}
			if !requestHasText(client.messages[1], "acknowledging is not enough") || result.Outcome.TaskStatus != "active" {
				t.Fatalf("missing reassessment prompt or status: %+v", result.Outcome)
			}
		})
		t.Run(status+"/same blocker re-recorded passes", func(t *testing.T) {
			store := &memoryTaskStore{state: &blocked}
			client := &stubClient{responses: []AssistantMessage{reassessReply(status), {Content: "仍缺少凭据"}}}
			app := NewWithOptions(client, "test", Options{TaskStore: store})
			result, err := app.RunTurn(context.Background(), []map[string]any{{"role": "user", "content": "继续"}}, 10)
			if err != nil || len(client.messages) != 2 || result.Reply != "仍缺少凭据" {
				t.Fatalf("calls=%d reply=%q err=%v", len(client.messages), result.Reply, err)
			}
		})
		t.Run(status+"/ignoring prompts ends blocked explicitly", func(t *testing.T) {
			store := &memoryTaskStore{state: &blocked}
			client := &stubClient{responses: []AssistantMessage{{Content: "收到"}, {Content: "收到"}, {Content: "收到"}}}
			app := NewWithOptions(client, "test", Options{TaskStore: store})
			result, err := app.RunTurn(context.Background(), []map[string]any{{"role": "user", "content": "继续"}}, 10)
			if err != nil || len(client.messages) != 3 || result.Outcome.Status != turn.Blocked || result.Outcome.Reason != "task_reassessment_required" {
				t.Fatalf("calls=%d outcome=%+v err=%v", len(client.messages), result.Outcome, err)
			}
		})
		t.Run(status+"/side question is exempt", func(t *testing.T) {
			store := &memoryTaskStore{state: &blocked}
			client := &stubClient{responses: []AssistantMessage{{Content: "answer"}}}
			app := NewWithOptions(client, "test", Options{TaskStore: store, TaskRelation: turn.SideQuestion})
			if _, err := app.RunTurn(context.Background(), []map[string]any{{"role": "user", "content": "你是谁"}}, 10); err != nil || len(client.messages) != 1 {
				t.Fatalf("side question prompted: calls=%d err=%v", len(client.messages), err)
			}
		})
	}
}

func TestComplexTurnWithoutTaskRecordIsCheckedAtCloseout(t *testing.T) {
	bash := func(id, command string) AssistantMessage {
		return AssistantMessage{ToolCalls: []ToolCall{bashCall(id, command)}}
	}
	functions := func() map[string]tools.Function {
		return map[string]tools.Function{
			"execute_bash": func(context.Context, map[string]any) (string, error) { return "ok", nil },
			"edit_file":    func(context.Context, map[string]any) (string, error) { return "edited", nil },
			"todo_write":   func(context.Context, map[string]any) (string, error) { return "Todo list updated.", nil },
		}
	}
	complete := completedWith(taskstate.Verification{Command: "go test ./x", Status: "passed", Details: "ok"})
	t.Run("edits and verification force one record", func(t *testing.T) {
		store := &memoryTaskStore{}
		client := &stubClient{responses: []AssistantMessage{
			{ToolCalls: []ToolCall{editCall("e1")}}, {ToolCalls: []ToolCall{editCall("e2")}}, bash("b1", "go test ./x"),
			{Content: "done"}, taskUpdateMessage(complete), {Content: "recorded and done"},
		}}
		app := NewWithOptions(client, "test", Options{TaskStore: store, Functions: functions()})
		reply, err := app.Run(context.Background(), "implement it", 20)
		if err != nil || reply != "recorded and done" || store.state == nil || store.state.Status != "completed" {
			t.Fatalf("reply=%q task=%+v err=%v", reply, store.state, err)
		}
	})
	t.Run("two edits without any check are forced and cannot silently complete", func(t *testing.T) {
		store := &memoryTaskStore{}
		client := &stubClient{responses: []AssistantMessage{{ToolCalls: []ToolCall{editCall("e1")}}, {ToolCalls: []ToolCall{editCall("e2")}}, {Content: "done"}, {Content: "done"}, {Content: "done"}}}
		app := NewWithOptions(client, "test", Options{TaskStore: store, Functions: functions()})
		result, err := app.RunTurn(context.Background(), []map[string]any{{"role": "user", "content": "edit"}}, 20)
		if err != nil || len(client.messages) != 5 || result.Outcome.Status != turn.Blocked || result.Outcome.Reason != "task_record_missing" {
			t.Fatalf("calls=%d outcome=%+v err=%v", len(client.messages), result.Outcome, err)
		}
	})
	t.Run("three todos force a record", func(t *testing.T) {
		store := &memoryTaskStore{}
		todos := `{"todos":"[{\"content\":\"a\",\"status\":\"pending\"},{\"content\":\"b\",\"status\":\"pending\"},{\"content\":\"c\",\"status\":\"pending\"}]"}`
		client := &stubClient{responses: []AssistantMessage{{ToolCalls: []ToolCall{{ID: "t", Function: FunctionCall{Name: "todo_write", Arguments: todos}}}}, {Content: "done"}, {Content: "done"}, {Content: "done"}}}
		app := NewWithOptions(client, "test", Options{TaskStore: store, Functions: functions()})
		result, err := app.RunTurn(context.Background(), []map[string]any{{"role": "user", "content": "plan it"}}, 20)
		if err != nil || len(client.messages) != 4 || result.Outcome.Status != turn.Blocked || result.Outcome.Reason != "task_record_missing" {
			t.Fatalf("calls=%d outcome=%+v err=%v", len(client.messages), result.Outcome, err)
		}
	})
	for name, responses := range map[string][]AssistantMessage{
		"single edit":              {{ToolCalls: []ToolCall{editCall("e1")}}, {Content: "done"}},
		"verification only":        {bash("b1", "go test ./x"), bash("b2", "go test ./y"), {Content: "done"}},
		"two todos only":           {{ToolCalls: []ToolCall{{ID: "t", Function: FunctionCall{Name: "todo_write", Arguments: `{"todos":[{"content":"a","status":"pending"},{"content":"b","status":"pending"}]}`}}}}, {Content: "done"}},
		"ordinary question + read": {bash("b1", "ls"), {Content: "done"}},
	} {
		t.Run(name+" is not forced", func(t *testing.T) {
			store := &memoryTaskStore{}
			client := &stubClient{responses: responses}
			app := NewWithOptions(client, "test", Options{TaskStore: store, Functions: functions()})
			reply, err := app.Run(context.Background(), "do it", 20)
			if err != nil || reply != "done" || len(client.messages) != len(responses) || store.state != nil {
				t.Fatalf("reply=%q calls=%d/%d task=%+v err=%v", reply, len(client.messages), len(responses), store.state, err)
			}
		})
	}
}

func TestContinuationGuardIgnoresRepeatedIdenticalProgress(t *testing.T) {
	guard := &continuationGuard{}
	guard.recoveryPrompt("continue")
	guard.recoveryPrompt("continue")
	read := ToolCall{ID: "r", Function: FunctionCall{Name: "read_file", Arguments: `{"path":"a.go"}`}}
	guard.observe(read, "same")
	guard.recoveryPrompt("continue") // new evidence resets the counter
	guard.recoveryPrompt("continue")
	guard.recoveryPrompt("continue")
	guard.observe(read, "same") // identical call and result: no new progress
	if !strings.Contains(guard.recoveryPrompt("continue"), "Change approach now") {
		t.Fatal("repeated identical read kept resetting recovery escalation")
	}
}

func TestAcceptUserInputResetsInferredScopeAndOnlyLatestQuoteCounts(t *testing.T) {
	_, run := evidenceRun()
	run.acceptUserInput("old request: switch everything to Rust")
	run.outcome.TaskRelation, run.outcome.TaskRelationReason, run.scopeLocked = turn.SideQuestion, "unrelated", true
	run.acceptUserInput("new request: continue the saved task")
	if run.outcome.TaskRelation != turn.ContinueTask || run.scopeLocked || run.outcome.TaskRelationReason != "" {
		t.Fatalf("inferred side_question survived new input: %+v locked=%v", run.outcome, run.scopeLocked)
	}
	if run.quotedByUser("switch everything to Rust") || !run.quotedByUser("continue the saved task") {
		t.Fatal("only the latest user message may ground a goal change")
	}
	run.explicitScope = true
	run.outcome.TaskRelation, run.scopeLocked = turn.SideQuestion, true
	run.acceptUserInput("another input")
	if run.outcome.TaskRelation != turn.SideQuestion || !run.scopeLocked {
		t.Fatal("explicit caller scope must not be reset by new input")
	}
}

func TestCompletionRecordReopenedWhenChangedAfterwards(t *testing.T) {
	verify := bashCall("v1", "go test ./x")
	complete := func(id string) AssistantMessage {
		message := taskUpdateMessage(completedWith(taskstate.Verification{Command: "go test ./x", ToolCallID: id, Status: "passed", Details: "ok"}))
		return message
	}
	functions := map[string]tools.Function{
		"execute_bash": func(context.Context, map[string]any) (string, error) { return "ok", nil },
		"edit_file":    func(context.Context, map[string]any) (string, error) { return "edited", nil },
	}
	t.Run("edit after completion without reverification", func(t *testing.T) {
		active := activeTestTask()
		store := &memoryTaskStore{state: &active}
		client := &stubClient{responses: []AssistantMessage{
			{ToolCalls: []ToolCall{verify}}, complete("v1"), {ToolCalls: []ToolCall{editCall("e1")}},
			{Content: "done"}, {Content: "done"}, {Content: "done"},
		}}
		app := NewWithOptions(client, "test", Options{TaskStore: store, Functions: functions})
		result, err := app.RunTurn(context.Background(), []map[string]any{{"role": "user", "content": "go"}}, 20)
		if err != nil || len(client.messages) != 6 || store.state.Status != "active" || result.Outcome.TaskStatus != "active" || result.Outcome.Status != turn.Blocked || result.Outcome.Reason != "completion_stale_after_changes" {
			t.Fatalf("stale completion persisted: calls=%d task=%+v err=%v", len(client.messages), store.state, err)
		}
		if !requestHasText(client.messages[4], "that record and its verification are stale") {
			t.Fatal("no stale-completion prompt")
		}
	})
	t.Run("same batch edit after completion", func(t *testing.T) {
		active := activeTestTask()
		store := &memoryTaskStore{state: &active}
		batch := complete("v1")
		batch.ToolCalls = append(batch.ToolCalls, editCall("e1"))
		client := &stubClient{responses: []AssistantMessage{{ToolCalls: []ToolCall{verify}}, batch, {Content: "done"}, {Content: "done"}, {Content: "done"}}}
		app := NewWithOptions(client, "test", Options{TaskStore: store, Functions: functions})
		if _, err := app.RunTurn(context.Background(), []map[string]any{{"role": "user", "content": "go"}}, 20); err != nil || store.state.Status != "active" {
			t.Fatalf("task=%+v err=%v", store.state, err)
		}
	})
	t.Run("reverified completion stays completed", func(t *testing.T) {
		active := activeTestTask()
		store := &memoryTaskStore{state: &active}
		rerun := bashCall("v2", "go test ./x")
		client := &stubClient{responses: []AssistantMessage{
			{ToolCalls: []ToolCall{verify}}, complete("v1"), {ToolCalls: []ToolCall{editCall("e1")}}, {ToolCalls: []ToolCall{rerun}}, complete("v2"), {Content: "done"},
		}}
		app := NewWithOptions(client, "test", Options{TaskStore: store, Functions: functions})
		result, err := app.RunTurn(context.Background(), []map[string]any{{"role": "user", "content": "go"}}, 20)
		if err != nil || store.state.Status != "completed" || result.Reply != "done" {
			t.Fatalf("task=%+v reply=%q err=%v", store.state, result.Reply, err)
		}
	})
}

func requestHasText(messages []map[string]any, text string) bool {
	for _, message := range messages {
		if message["role"] == "developer" && strings.Contains(contentText(message["content"]), text) {
			return true
		}
	}
	return false
}

func TestNewUserInputRebaselinesScopeAndCloseout(t *testing.T) {
	app, _ := evidenceApp(t)
	ctx, run := evidenceRun()
	app.runRegularToolCall(ctx, editCall("e1"))
	run.acceptUserInput("an unrelated question about languages")
	if _, err := app.functions["task_scope"](ctx, map[string]any{"relation": "side_question", "reason": "unrelated"}); err != nil {
		t.Fatalf("earlier work blocked a new independent question: %v", err)
	}
	// A completed task does not absorb substantial new work after fresh input.
	done := completedWith(taskstate.Verification{Command: "go test ./x", Status: "passed", Details: "ok"})
	store := &memoryTaskStore{state: &done}
	functions := map[string]tools.Function{"edit_file": func(context.Context, map[string]any) (string, error) { return "edited", nil }}
	client := &stubClient{responses: []AssistantMessage{{ToolCalls: []ToolCall{editCall("e1")}}, {ToolCalls: []ToolCall{editCall("e2")}}, {Content: "done"}, {Content: "done"}, {Content: "done"}}}
	agent := NewWithOptions(client, "test", Options{TaskStore: store, Functions: functions})
	result, err := agent.RunTurn(context.Background(), []map[string]any{{"role": "user", "content": "new feature"}}, 20)
	if err != nil || len(client.messages) != 5 || result.Outcome.Status != turn.Blocked || result.Outcome.Reason != "task_record_missing" {
		t.Fatalf("calls=%d outcome=%+v err=%v", len(client.messages), result.Outcome, err)
	}
}
