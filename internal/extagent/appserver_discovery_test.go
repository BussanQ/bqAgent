package extagent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestStartCodexDaemonUsesIdempotentCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture requires POSIX")
	}
	root := t.TempDir()
	command := filepath.Join(root, "fake-codex")
	if err := os.WriteFile(command, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > daemon-args\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := startCodexDaemon(ctx, command, root); err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(filepath.Join(root, "daemon-args"))
	if err != nil || string(args) != "app-server\ndaemon\nstart\n" {
		t.Fatalf("launcher arguments=%q err=%v", args, err)
	}
	cancel()
	if err := startCodexDaemon(ctx, command, root); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled launch = %v", err)
	}
}

func TestCodexAppServerDefaultAndExplicitConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name          string
		env           map[string]string
		command, args string
		auto          bool
	}{
		{name: "default", command: "codex", args: "app-server proxy", auto: true},
		{name: "explicit app server", env: map[string]string{"AGENT_CODEX_APP_SERVER_CMD": "custom"}, command: "custom", args: "app-server"},
		{name: "explicit proxy", env: map[string]string{"AGENT_CODEX_APP_SERVER_CMD": "codex", "AGENT_CODEX_APP_SERVER_ARGS": "app-server proxy"}, command: "codex", args: "app-server proxy"},
		{name: "args only", env: map[string]string{"AGENT_CODEX_APP_SERVER_ARGS": "app-server proxy --sock /tmp/custom.sock"}, command: "codex", args: "app-server proxy --sock /tmp/custom.sock"},
		{name: "custom CLI", env: map[string]string{"AGENT_CODEX_CLI_CMD": "wrapper"}},
		{name: "custom CLI args", env: map[string]string{"AGENT_CODEX_CLI_ARGS": "exec --json"}},
		{name: "custom ACP", env: map[string]string{"AGENT_CODEX_ACP_CMD": "codex-acp"}},
		{name: "custom ACP args", env: map[string]string{"AGENT_CODEX_ACP_ARGS": "--flag"}},
		{name: "explicit app wins", env: map[string]string{"AGENT_CODEX_CLI_CMD": "wrapper", "AGENT_CODEX_APP_SERVER_CMD": "custom"}, command: "custom", args: "app-server"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := ConfigFromEnv(func(key string) string { return tc.env[key] }, t.TempDir()).Agents[AgentCodex]
			if cfg.AppServer.Command != tc.command || strings.Join(cfg.AppServer.Args, " ") != tc.args || cfg.AutoAppServer != tc.auto {
				t.Fatalf("configuration = %+v", cfg)
			}
			if want := tc.env["AGENT_CODEX_CLI_CMD"]; want != "" && cfg.CLI.Command != want {
				t.Fatalf("custom CLI overwritten: %+v", cfg.CLI)
			}
			if want := tc.env["AGENT_CODEX_ACP_CMD"]; want != "" && cfg.ACP.Command != want {
				t.Fatalf("custom ACP overwritten: %+v", cfg.ACP)
			}
		})
	}
}

func TestCodexSharedAppServerDiscovery(t *testing.T) {
	failure := errors.New("unsupported or unavailable")
	for _, tc := range []struct {
		name        string
		probeErrors []error
		startError  error
		calls       []string
		wantArgs    string
		wantError   bool
	}{
		{name: "reuse running daemon", probeErrors: []error{nil}, calls: []string{"app-server proxy"}, wantArgs: "app-server proxy"},
		{name: "start missing daemon", probeErrors: []error{failure, nil}, calls: []string{"app-server proxy", "start", "app-server proxy"}, wantArgs: "app-server proxy"},
		{name: "old version without daemon", probeErrors: []error{failure, nil}, startError: failure, calls: []string{"app-server proxy", "start", "app-server"}, wantArgs: "app-server"},
		{name: "proxy unavailable after start", probeErrors: []error{failure, failure, nil}, calls: []string{"app-server proxy", "start", "app-server proxy", "app-server"}, wantArgs: "app-server"},
		{name: "old version without app server", probeErrors: []error{failure, failure}, startError: failure, calls: []string{"app-server proxy", "start", "app-server"}, wantArgs: "app-server", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cwd := t.TempDir()
			cfg := ConfigFromEnv(func(string) string { return "" }, cwd).Agents[AgentCodex]
			var calls []string
			probes := 0
			check := func(ctx context.Context, command, root string, limit time.Duration) {
				t.Helper()
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > limit || command != "codex" || root != cwd {
					t.Fatalf("invalid bounded invocation: %q %q %v", command, root, deadline)
				}
			}
			spec, err := detectCodexAppServer(context.Background(), cfg, cwd,
				func(ctx context.Context, spec CommandSpec, root string) error {
					check(ctx, spec.Command, root, 3*time.Second)
					calls = append(calls, strings.Join(spec.Args, " "))
					if probes >= len(tc.probeErrors) {
						t.Fatal("unexpected additional probe")
					}
					err := tc.probeErrors[probes]
					probes++
					return err
				}, func(ctx context.Context, command, root string) error {
					check(ctx, command, root, 15*time.Second)
					calls = append(calls, "start")
					return tc.startError
				})
			if (err != nil) != tc.wantError || strings.Join(spec.Args, " ") != tc.wantArgs || !reflect.DeepEqual(calls, tc.calls) {
				t.Fatalf("spec=%+v err=%v calls=%v", spec, err, calls)
			}
		})
	}
}

func TestCodexSharedAppServerCancellationAndExplicitOverride(t *testing.T) {
	for _, mode := range []string{"explicit", "cancelled before probe", "cancelled during start"} {
		t.Run(mode, func(t *testing.T) {
			cfg := AgentConfig{AppServer: CommandSpec{Command: "custom", Args: []string{"app-server", "proxy", "--sock", "/tmp/custom.sock"}}, AutoAppServer: mode != "explicit"}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancelled before probe" {
				cancel()
			}
			probes, starts := 0, 0
			_, err := detectCodexAppServer(ctx, cfg, t.TempDir(), func(_ context.Context, spec CommandSpec, _ string) error {
				probes++
				if !reflect.DeepEqual(spec, cfg.AppServer) {
					t.Fatalf("override changed: %+v", spec)
				}
				return errors.New("connection failed")
			}, func(context.Context, string, string) error {
				starts++
				cancel()
				return context.Canceled
			})
			wantProbes, wantStarts := 1, 0
			if mode == "cancelled before probe" {
				wantProbes = 0
			}
			if mode == "cancelled during start" {
				wantStarts = 1
			}
			if err == nil || probes != wantProbes || starts != wantStarts {
				t.Fatalf("err=%v probes=%d starts=%d", err, probes, starts)
			}
			if mode != "explicit" && !errors.Is(err, context.Canceled) {
				t.Fatalf("lost cancellation: %v", err)
			}
		})
	}
}
