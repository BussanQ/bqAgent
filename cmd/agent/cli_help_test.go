package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestCLIHelpPrintsParametersWithoutStartingRuntime(t *testing.T) {
	var first string
	for _, args := range [][]string{{"-h"}, {"--help"}, {"--doctor", "-h"}, {"--server", "--listen", ":9090", "--help"}, {"--resume", "--help"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			// Nil dependencies would panic if help attempted workspace/runtime setup.
			code := runWithDeps(context.Background(), nil, &stdout, &stderr, args, func(string) string {
				t.Fatal("help must not read runtime configuration")
				return ""
			}, runDeps{})
			if code != 0 || stderr.Len() != 0 {
				t.Fatalf("exit=%d stderr=%q", code, stderr.String())
			}
			text := stdout.String()
			for _, want := range []string{"用法:", "bqagent [选项] [任务描述]", "-h, --help", "--resume [sessionID]", "--background", "--server", "-d", "--listen string", "127.0.0.1:8080", "--server-url", "http://127.0.0.1:8080", "--doctor", "--doctor-active", "--doctor-json", "--ilink-login", "--ilink-status", "示例:", "/help"} {
				if !strings.Contains(text, want) {
					t.Errorf("help missing %q: %s", want, text)
				}
			}
			for _, hidden := range []string{"--session-id", "--session-run", "--server-run", "--subagent-run", "--subagent-lease"} {
				if strings.Contains(text, hidden) {
					t.Errorf("help exposed internal flag %q", hidden)
				}
			}
			if first == "" {
				first = text
			} else if text != first {
				t.Fatal("help aliases or preceding options changed default help")
			}
		})
	}
}

func TestCLIHelpPreservesErrorsAndLiteralTaskArguments(t *testing.T) {
	for _, args := range [][]string{{"--unknown"}, {"--listen"}} {
		var stdout, stderr bytes.Buffer
		code := runWithDeps(context.Background(), nil, &stdout, &stderr, args, nil, runDeps{})
		if code == 0 || stdout.Len() != 0 || stderr.Len() == 0 {
			t.Fatalf("args=%v exit=%d stdout=%q stderr=%q", args, code, stdout.String(), stderr.String())
		}
	}
	for _, args := range [][]string{{"--", "--help"}, {"explain", "-h"}, {"--resume=--help"}} {
		var output bytes.Buffer
		_, _, err := parseCLIWithHelp(args, &output)
		if err != nil || output.Len() != 0 {
			t.Fatalf("literal help treated as flag: args=%v output=%q err=%v", args, output.String(), err)
		}
	}
}
