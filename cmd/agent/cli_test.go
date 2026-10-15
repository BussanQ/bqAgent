package main

import (
	"strings"
	"testing"
)

func TestParseCLIRejectsRemovedPlanFlag(t *testing.T) {
	for _, args := range [][]string{
		{"--plan", "inspect repository"},
		{"--chat", "--plan"},
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
		{"--chat", "--stream"},
		{"--chat", "--stream=false"},
		{"--server", "--stream"},
		{"--resume", "saved-session", "--stream", "continue"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			if _, _, err := parseCLI(args); err == nil || !strings.Contains(err.Error(), "flag provided but not defined: -stream") {
				t.Fatalf("parseCLI(%q) error = %v, want unknown stream flag", args, err)
			}
		})
	}
	options, args, err := parseCLI([]string{"--chat", "--resume", "saved-session"})
	if err != nil || !options.chat || options.resumeID != "saved-session" || len(args) != 0 {
		t.Fatalf("chat resume without stream flag: options=%+v args=%q err=%v", options, args, err)
	}
}

func TestParseCLIPreservesTaskAndChatWithoutPlanMode(t *testing.T) {
	options, args, err := parseCLI([]string{"inspect repository"})
	if err != nil || options.chat || len(args) != 1 || args[0] != "inspect repository" {
		t.Fatalf("ordinary task: options=%+v args=%q err=%v", options, args, err)
	}
	options, args, err = parseCLI(nil)
	if err != nil || !options.chat || len(args) != 0 {
		t.Fatalf("default chat: options=%+v args=%q err=%v", options, args, err)
	}
}
