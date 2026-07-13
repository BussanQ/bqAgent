package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"bqagent/internal/agent"
	"bqagent/internal/evalharness"
	appruntime "bqagent/internal/runtime"
)

func main() {
	var suite, mode, manifestPath string
	var repeats int
	flag.StringVar(&suite, "suite", "smoke", "smoke or all")
	flag.StringVar(&mode, "mode", "replay", "replay or live")
	flag.StringVar(&manifestPath, "manifest", filepath.Join("eval", "tasks.json"), "task manifest")
	flag.IntVar(&repeats, "repeats", 1, "live repetitions")
	flag.Parse()
	manifest, err := evalharness.Load(manifestPath)
	if err != nil {
		fatal(err)
	}
	root, cwdErr := os.Getwd()
	if cwdErr != nil {
		fatal(cwdErr)
	}
	var report evalharness.Report
	if mode == "replay" {
		report = evalharness.RunReplay(manifest, suite, root)
	} else if mode == "live" {
		report = runLive(manifest, suite, repeats)
	} else {
		fatal(fmt.Errorf("mode must be replay or live"))
	}
	dir, err := evalharness.WriteReport(root, report)
	if err != nil {
		fatal(err)
	}
	fmt.Printf("eval=%s passed=%d failed=%d report=%s\n", report.ID, report.Passed, report.Failed, dir)
	if report.Failed > 0 {
		os.Exit(1)
	}
}

func runLive(manifest evalharness.Manifest, suite string, repeats int) evalharness.Report {
	llmConfig := appruntime.ConfigFromEnv(os.Getenv)
	if llmConfig.APIKey == "" {
		fatal(fmt.Errorf("LLM API key is required for live eval"))
	}
	if repeats < 1 {
		repeats = 1
	}
	report := evalharness.Report{ID: "eval_" + time.Now().UTC().Format("20060102T150405Z"), Mode: "live", Suite: suite, StartedAt: time.Now().UTC()}
	reportRoot, _ := os.Getwd()
	for _, task := range manifest.Tasks {
		if suite != "all" && task.Suite != suite {
			continue
		}
		for run := 0; run < repeats; run++ {
			temp, err := os.MkdirTemp("", "bqeval-*")
			if err != nil {
				fatal(err)
			}
			runtime := appruntime.Factory{Config: llmConfig, WorkspaceRoot: temp, MemoryDir: filepath.Join(temp, ".agent", "memory"), Getenv: os.Getenv}.Build(false)
			app := agent.NewWithOptions(runtime.Client, runtime.Model, agent.Options{SystemPrompt: agent.DefaultSystemPrompt, ToolDefinitions: runtime.Catalog.Definitions(), Functions: runtime.Catalog.Registry(), WorkspaceRoot: temp, Context: runtime.Context})
			start := time.Now()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			output, runErr := app.Run(ctx, task.Input, 30)
			cancel()
			results := evalharness.Verify(task, output, temp)
			passed := runErr == nil
			for _, v := range results {
				if !v.Passed {
					passed = false
				}
			}
			item := evalharness.TaskResult{ID: task.ID, Category: task.Category, Passed: passed, DurationMS: time.Since(start).Milliseconds(), Output: output, Verifiers: results}
			if runErr != nil {
				item.Output = runErr.Error()
			}
			item.RunID = evalharness.RecordTaskTrace(reportRoot, report.ID, task, item)
			report.Results = append(report.Results, item)
			if passed {
				report.Passed++
			} else {
				report.Failed++
			}
			_ = os.RemoveAll(temp)
		}
	}
	report.FinishedAt = time.Now().UTC()
	return report
}
func fatal(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(2) }
