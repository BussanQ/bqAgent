package main

import (
	"strings"
	"testing"
)

func TestParseCLIRejectsRemovedPlanFlag(t *testing.T) {
	for _, args := range [][]string{
		{"--plan", "inspect repository"},
		{"--plan"},
		{"--server", "--plan"},
		{"--background", "--plan", "inspect repository"},
		{"--resume", "saved-session", "--plan", "continue"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			if _, _, err := parseCLI(args); err == nil || !strings.Contains(err.Error(), "flag provided but not defined: -plan") {
				t.Fatalf("parseCLI(%q) error = %v, want unknown plan flag", args, err)
			}
		})
	}
}

func TestParseCLIRejectsRemovedStreamFlag(t *testing.T) {
	for _, args := range [][]string{
		{"--stream"},
		{"--stream=false"},
		{"--server", "--stream"},
		{"--resume", "saved-session", "--stream", "continue"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			if _, _, err := parseCLI(args); err == nil || !strings.Contains(err.Error(), "flag provided but not defined: -stream") {
				t.Fatalf("parseCLI(%q) error = %v, want unknown stream flag", args, err)
			}
		})
	}
	options, args, err := parseCLI([]string{"--resume", "saved-session"})
	if err != nil || options.resumeID != "saved-session" || len(args) != 0 {
		t.Fatalf("resume without stream flag: options=%+v args=%q err=%v", options, args, err)
	}
}

func TestOptionsUsesTUI(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		task string
		want bool
	}{
		{"no args", nil, "", true},
		{"blank task", nil, " \t\n", true},
		{"explicit task", nil, "inspect", false},
		{"resume without task", []string{"--resume", "saved-session"}, "", true},
		{"resume blank task", []string{"--resume", "saved-session"}, "  ", true},
		{"resume with task", []string{"--resume", "saved-session"}, "continue", false},
		{"background", []string{"--background"}, "", false},
		{"background resume", []string{"--background", "--resume", "saved-session"}, "", false},
		{"server", []string{"--server"}, "", false},
		{"server run", []string{"--server-run"}, "", false},
		{"session run", []string{"--session-run", "--resume", "saved-session"}, "", false},
		{"subagent run", []string{"--subagent-run", "worker", "--subagent-lease", "lease"}, "", false},
		{"doctor", []string{"--doctor"}, "", false},
		{"ilink login", []string{"--ilink-login"}, "", false},
		{"ilink status", []string{"--ilink-status"}, "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			options, _, err := parseCLI(test.args)
			if err != nil {
				t.Fatalf("parseCLI(%q): %v", test.args, err)
			}
			if got := options.usesTUI(test.task); got != test.want {
				t.Fatalf("usesTUI(%q) with %q = %v, want %v", test.task, test.args, got, test.want)
			}
		})
	}
}

func TestParseCLIPreservesTaskArguments(t *testing.T) {
	options, args, err := parseCLI([]string{"inspect repository"})
	if err != nil || len(args) != 1 || args[0] != "inspect repository" || !options.usesTUI("") || options.usesTUI(args[0]) {
		t.Fatalf("ordinary task: options=%+v args=%q err=%v", options, args, err)
	}
	options, args, err = parseCLI([]string{"--resume", "saved-session", "continue", "work"})
	if err != nil || options.resumeID != "saved-session" || strings.Join(args, " ") != "continue work" {
		t.Fatalf("resume task: options=%+v args=%q err=%v", options, args, err)
	}
	options, args, err = parseCLI(nil)
	if err != nil || len(args) != 0 || !options.usesTUI(strings.Join(args, " ")) {
		t.Fatalf("default TUI: options=%+v args=%q err=%v", options, args, err)
	}
}
