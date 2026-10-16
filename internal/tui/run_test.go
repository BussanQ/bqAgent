package tui

import (
	"bytes"
	"testing"
)

func TestExitResumeCommandUsesFinalSession(t *testing.T) {
	model := NewModel(&fakeBackend{}, Config{Workspace: t.TempDir(), AgentDir: t.TempDir(), NoColor: true})
	assertOutput := func(want string) {
		t.Helper()
		var output bytes.Buffer
		if err := printResumeCommand(&output, newInlineModel(model)); err != nil {
			t.Fatal(err)
		}
		if output.String() != want {
			t.Fatalf("exit output = %q, want %q", output.String(), want)
		}
	}
	assertOutput("")
	model.sessionID = "saved-session"
	assertOutput("\nbqagent --resume saved-session\n")
	model.clearSession()
	assertOutput("")
	model.sessionID = "new-session"
	assertOutput("\nbqagent --resume new-session\n")
}
