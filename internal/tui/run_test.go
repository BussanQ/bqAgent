package tui

import (
	"bytes"
	"testing"
)

func TestExitResumeCommandUsesDimColor(t *testing.T) {
	model := NewModel(&fakeBackend{}, Config{Workspace: t.TempDir(), AgentDir: t.TempDir()})
	model.sessionID = "saved-session"
	var output bytes.Buffer
	if err := printResumeCommand(&output, newInlineModel(model)); err != nil {
		t.Fatal(err)
	}
	want := "\n\x1b[38;2;119;119;119mbqagent --resume saved-session\x1b[m\n"
	if output.String() != want {
		t.Fatalf("exit output = %q, want %q", output.String(), want)
	}
}

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
