package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"bqagent/internal/session"
	"bqagent/internal/workspace"
)

func TestParseCLIResumeOptionalID(t *testing.T) {
	for _, test := range []struct {
		name      string
		args      []string
		requested bool
		id        string
		task      string
	}{
		{"bare", []string{"--resume"}, true, "", ""},
		{"single dash", []string{"-resume"}, true, "", ""},
		{"empty value", []string{"--resume="}, true, "", ""},
		{"explicit", []string{"--resume", "saved", "continue"}, true, "saved", "continue"},
		{"equals", []string{"--resume=saved"}, true, "saved", ""},
		{"next option", []string{"--resume", "--background"}, true, "", ""},
		{"task separator", []string{"--resume", "--", "continue", "work"}, true, "", "continue work"},
		{"literal task", []string{"--", "--resume"}, false, "", "--resume"},
		{"after task", []string{"explain", "--resume"}, false, "", "explain --resume"},
		{"other option value", []string{"--listen", "--resume"}, false, "", ""},
		{"after other option", []string{"--listen", ":9090", "--resume"}, true, "", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			original := append([]string(nil), test.args...)
			options, args, err := parseCLI(test.args)
			if err != nil || options.resumeRequested != test.requested || options.resumeID != test.id || strings.Join(args, " ") != test.task {
				t.Fatalf("options=%+v args=%q err=%v", options, args, err)
			}
			if !reflect.DeepEqual(original, test.args) {
				t.Fatal("parser mutated caller arguments")
			}
			if test.name == "other option value" && options.listen != "--resume" {
				t.Fatalf("listen value changed: %q", options.listen)
			}
		})
	}
}

func TestParseCLIResumeWithoutIDRejectsConflicts(t *testing.T) {
	for _, args := range [][]string{
		{"--resume", "--doctor"},
		{"--resume", "--server"},
		{"--resume", "--server-run"},
		{"--resume", "-d"},
		{"--resume", "--session-id", "saved"},
		{"--resume", "--ilink-login"},
		{"--resume", "--ilink-status"},
		{"--resume", "--subagent-run", "worker", "--subagent-lease", "lease"},
		{"--resume", "--unknown"},
	} {
		if _, _, err := parseCLI(args); err == nil {
			t.Fatalf("accepted conflicting/invalid arguments %q", args)
		}
	}
}

func TestResolveResumeSelectsLatestWorkspaceConversation(t *testing.T) {
	ws := &workspace.Workspace{Root: t.TempDir(), GlobalAgentDir: filepath.Join(t.TempDir(), ".agent")}
	storeOptions := session.DefaultOptions()
	storeOptions.AgentDir = ws.AgentDir()
	store := session.NewStore(ws.Root, storeOptions)
	create := func(store *session.Store, chat bool) *session.Session {
		t.Helper()
		saved, err := store.Create(session.CreateOptions{Chat: chat})
		if err != nil {
			t.Fatal(err)
		}
		return saved
	}
	older := create(store, true)
	newer := create(store, true)
	// Updating the older conversation must take precedence over creation order.
	if err := older.SetCurrentModel("updated-model"); err != nil {
		t.Fatal(err)
	}
	create(store, false)
	create(session.NewStore(t.TempDir(), storeOptions), true)
	options := cliOptions{resumeRequested: true}
	if err := options.resolveResume(ws); err != nil || options.resumeID != older.ID() {
		t.Fatalf("resume ID=%q want=%q err=%v", options.resumeID, older.ID(), err)
	}
	options = cliOptions{resumeRequested: true, resumeID: newer.ID()}
	if err := options.resolveResume(ws); err != nil || options.resumeID != newer.ID() {
		t.Fatalf("explicit ID changed: %+v err=%v", options, err)
	}
}

func TestResolveResumeWithoutHistory(t *testing.T) {
	ws := &workspace.Workspace{Root: t.TempDir(), GlobalAgentDir: filepath.Join(t.TempDir(), ".agent")}
	options := cliOptions{}
	if err := options.resolveResume(ws); err != nil || options.resumeID != "" {
		t.Fatalf("ordinary startup tried to resume: %+v err=%v", options, err)
	}
	options.resumeRequested = true
	if err := options.resolveResume(ws); err == nil || !strings.Contains(err.Error(), "没有可恢复的历史对话") {
		t.Fatalf("empty history error=%v", err)
	}
	if err := os.MkdirAll(ws.AgentDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws.AgentDir(), "sessions"), []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := options.resolveResume(ws); err == nil || !strings.Contains(err.Error(), "查找最近会话失败") {
		t.Fatalf("storage error=%v", err)
	}
}

func TestRunResumeLatestWithoutHistoryReportsError(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	var stdout, stderr bytes.Buffer
	code := runWithDeps(context.Background(), nil, &stdout, &stderr, []string{"--resume", "--", "continue"}, func(key string) string {
		if key == "HOME" {
			return home
		}
		return ""
	}, runDeps{getwd: func() (string, error) { return root, nil }})
	if code != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "没有可恢复的历史对话") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}
