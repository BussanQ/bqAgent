package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"

	"bqagent/internal/doctor"
	"bqagent/internal/extagent"
	"bqagent/internal/globalconfig"
	"bqagent/internal/workspace"
)

func workspaceDoctor(ws *workspace.Workspace, getenv func(string) string, snapshot func() ([]extagent.DetectionStatus, bool)) *doctor.Engine {
	return doctor.New(doctor.Options{Store: globalconfig.NewStore(ws.AgentDir()), WorkspaceRoot: ws.Root, Getenv: getenv, External: extagent.ConfigFromEnv(getenv, ws.Root), MCPPaths: ws.MCPConfigPaths(), Snapshot: snapshot, Storage: []doctor.Storage{
		{ID: "global", Path: ws.AgentDir(), MustExist: true}, {ID: "workspace", Path: ws.Root, MustExist: true}, {ID: "sessions", Path: ws.SessionsDir()},
		{ID: "memory", Path: ws.WorkspaceMemoryDir()}, {ID: "global_memory", Path: filepath.Join(ws.AgentDir(), "memory")},
	}})
}

func runDoctor(ctx context.Context, stdout, stderr io.Writer, options cliOptions, ws *workspace.Workspace, getenv func(string) string) int {
	report, err := workspaceDoctor(ws, getenv, nil).Inspect(ctx, options.doctorActive)
	if err != nil {
		fmt.Fprintln(stderr, "Diagnostic execution cancelled or timed out.")
		return 2
	}
	if options.doctorJSON {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			return 2
		}
	} else {
		fmt.Fprintf(stdout, "bqagent doctor: %s (ready=%t, %s)\n", report.Status, report.Ready, report.Mode)
		for _, check := range report.Checks {
			fmt.Fprintf(stdout, "[%s] %s/%s: %s (%s)\n", check.State, check.Group, check.ID, check.Reason, check.Source)
			if check.Hint != "" {
				fmt.Fprintf(stdout, "  %s\n", check.Hint)
			}
		}
	}
	if !report.Ready {
		return 1
	}
	return 0
}
