package agent

import "testing"

func guardCall(name, args string) ToolCall {
	var c ToolCall
	c.Function.Name = name
	c.Function.Arguments = args
	return c
}

func TestLoopGuardEditAllowsSameTestRerun(t *testing.T) {
	guard := newLoopGuard(StageConfig{LoopProtection: true, DuplicateCallLimit: 3, RepeatedFailureLimit: 3})
	test := guardCall("bash", `{"command":"go test ./x"}`)
	if a := guard.observeTool(test, "Error: FAIL"); a.checkpointReason != "" {
		t.Fatal("unexpected checkpoint 1")
	}
	if a := guard.observeTool(test, "Error: FAIL"); a.checkpointReason != "" {
		t.Fatal("unexpected checkpoint 2")
	}
	guard.observeTool(guardCall("edit_file", `{"path":"a.go","old_string":"1","new_string":"2"}`), "ok")
	if a := guard.observeTool(test, "Error: FAIL"); a.checkpointReason != "" {
		t.Fatalf("test rerun after edit should be allowed: %q", a.checkpointReason)
	}
}

func TestLoopGuardRepeatedIdenticalEditDoesNotReset(t *testing.T) {
	guard := newLoopGuard(StageConfig{LoopProtection: true, DuplicateCallLimit: 3, RepeatedFailureLimit: 3})
	test := guardCall("bash", `{"command":"t"}`)
	editA := guardCall("edit_file", `{"path":"a","old_string":"1","new_string":"2"}`)
	var last loopGuardAction
	for i := 0; i < 4 && last.checkpointReason == ""; i++ {
		guard.observeTool(editA, "ok")
		last = guard.observeTool(test, "ok")
	}
	if last.checkpointReason == "" {
		t.Fatal("identical repeated edits must not keep resetting duplicate counts")
	}
}

func TestLoopGuardRepeatedSameReadDoesNotResetEmptySearch(t *testing.T) {
	guard := newLoopGuard(StageConfig{})
	glob := guardCall("glob", `{"pattern":"*.zz"}`)
	read := guardCall("read_file", `{"path":"a"}`)
	guard.observeTool(read, "same")
	guard.observeTool(glob, "No files matched.")
	guard.observeTool(glob, "No files matched.") // count 2
	// same read result again must not reset
	guard.observeTool(read, "same")
	if a := guard.observeTool(glob, "No files matched."); a.checkpointReason == "" {
		t.Fatalf("expected checkpoint, got %+v", a)
	}
}

func TestLoopGuardNewReadResultStillResetsEmptySearch(t *testing.T) {
	guard := newLoopGuard(StageConfig{})
	glob := guardCall("glob", `{"pattern":"*.zz"}`)
	guard.observeTool(glob, "No files matched.")
	guard.observeTool(guardCall("read_file", `{"path":"a"}`), "one")
	if a := guard.observeTool(glob, "No files matched."); a != (loopGuardAction{}) {
		t.Fatalf("new read progress should reset, got %+v", a)
	}
}

func TestProgressTrackerDedupAndBookkeeping(t *testing.T) {
	tr := newProgressTracker()
	read := guardCall("read_file", `{"path":"a"}`)
	if !tr.observe(read, "x") || tr.observe(read, "x") || !tr.observe(read, "y") {
		t.Fatal("dedup by result fingerprint failed")
	}
	if tr.observe(guardCall("task_update", `{}`), "ok") || tr.observe(guardCall("task_scope", `{}`), "ok") || tr.observe(read, "Error: x") {
		t.Fatal("bookkeeping/errors must not count")
	}
}

func TestLoopGuardEditThenSameTestRerunNormalEdit(t *testing.T) {
	guard := newLoopGuard(StageConfig{LoopProtection: true, DuplicateCallLimit: 2, RepeatedFailureLimit: 5})
	test := guardCall("bash", `{"command":"go test"}`)
	guard.observeTool(test, "Error: FAIL")
	guard.observeTool(guardCall("edit_file", `{"path":"a","old_string":"1","new_string":"2"}`), "ok")
	if a := guard.observeTool(test, "ok"); a.checkpointReason != "" {
		t.Fatalf("rerun after edit blocked: %q", a.checkpointReason)
	}
}

func TestLoopGuardWhitespaceVariantEditDoesNotBypass(t *testing.T) {
	guard := newLoopGuard(StageConfig{LoopProtection: true, DuplicateCallLimit: 3, RepeatedFailureLimit: 5})
	test := guardCall("bash", `{"command":"t"}`)
	edits := []string{
		`{"path":"a","content":"x"}`,
		`{ "content": "x",  "path": "a" }`,
		"{\"path\":\"a\",\n\"content\":\"x\"}",
	}
	guard.observeTool(guardCall("write_file", edits[0]), "Wrote to a")
	var last loopGuardAction
	for i := 0; i < 4 && last.checkpointReason == ""; i++ {
		guard.observeTool(guardCall("write_file", edits[i%3]), "Wrote to a")
		last = guard.observeTool(test, "ok")
	}
	if last.checkpointReason == "" {
		t.Fatal("whitespace/key-order variants of the same write must not reset")
	}
}

func TestLoopGuardEditResetsTrackerEvenWithoutLoopProtection(t *testing.T) {
	guard := newLoopGuard(StageConfig{})
	read := guardCall("read_file", `{"path":"a"}`)
	if !guard.progress.observe(read, "same") || guard.progress.observe(read, "same") {
		t.Fatal("setup")
	}
	guard.observeTool(guardCall("edit_file", `{"path":"a","old_string":"1","new_string":"2"}`), "ok")
	if !guard.progress.observe(read, "same") {
		t.Fatal("edit must clear seen read fingerprints even when LoopProtection is off")
	}
}

func TestLoopGuardSameReadResultAfterNoEditStaysNonProgress(t *testing.T) {
	guard := newLoopGuard(StageConfig{})
	read := guardCall("read_file", `{ "path": "a" }`)
	guard.observeTool(read, "same")
	if guard.progress.observe(guardCall("read_file", `{"path":"a"}`), "same") {
		t.Fatal("canonical-equal repeated read must not count as progress")
	}
}
