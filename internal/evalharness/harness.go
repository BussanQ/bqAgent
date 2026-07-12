package evalharness

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	apptrace "bqagent/internal/trace"
)

type Manifest struct {
	Version int    `json:"version"`
	Tasks   []Task `json:"tasks"`
}
type Task struct {
	ID           string     `json:"id"`
	Category     string     `json:"category"`
	Suite        string     `json:"suite"`
	Input        string     `json:"input"`
	ReplayOutput string     `json:"replay_output"`
	Fixture      string     `json:"fixture,omitempty"`
	Verifiers    []Verifier `json:"verifiers"`
	Tags         []string   `json:"tags,omitempty"`
	Live         bool       `json:"live,omitempty"`
}
type Verifier struct {
	Type     string `json:"type"`
	Value    string `json:"value,omitempty"`
	Path     string `json:"path,omitempty"`
	Required bool   `json:"required"`
}
type VerifyResult struct {
	Type    string `json:"type"`
	Passed  bool   `json:"passed"`
	Message string `json:"message,omitempty"`
}
type TaskResult struct {
	ID         string         `json:"id"`
	RunID      string         `json:"run_id,omitempty"`
	Category   string         `json:"category"`
	Passed     bool           `json:"passed"`
	DurationMS int64          `json:"duration_ms"`
	Output     string         `json:"output"`
	Verifiers  []VerifyResult `json:"verifiers"`
}
type Report struct {
	ID         string       `json:"id"`
	Mode       string       `json:"mode"`
	Suite      string       `json:"suite"`
	StartedAt  time.Time    `json:"started_at"`
	FinishedAt time.Time    `json:"finished_at"`
	Passed     int          `json:"passed"`
	Failed     int          `json:"failed"`
	Results    []TaskResult `json:"results"`
}

func Load(path string) (Manifest, error) {
	var manifest Manifest
	content, err := os.ReadFile(path)
	if err != nil {
		return manifest, err
	}
	err = json.Unmarshal(content, &manifest)
	if err != nil {
		return manifest, err
	}
	if manifest.Version != 1 {
		return manifest, fmt.Errorf("unsupported eval manifest version %d", manifest.Version)
	}
	if len(manifest.Tasks) < 20 || len(manifest.Tasks) > 50 {
		return manifest, fmt.Errorf("eval manifest must contain 20-50 tasks")
	}
	return manifest, nil
}

func RunReplay(manifest Manifest, suite, root string) Report {
	report := Report{ID: "eval_" + time.Now().UTC().Format("20060102T150405Z"), Mode: "replay", Suite: suite, StartedAt: time.Now().UTC()}
	for _, task := range manifest.Tasks {
		if suite != "all" && task.Suite != suite {
			continue
		}
		start := time.Now()
		results := Verify(task, task.ReplayOutput, root)
		passed := true
		for _, result := range results {
			if !result.Passed {
				passed = false
			}
		}
		item := TaskResult{ID: task.ID, Category: task.Category, Passed: passed, DurationMS: time.Since(start).Milliseconds(), Output: task.ReplayOutput, Verifiers: results}
		item.RunID = RecordTaskTrace(root, report.ID, task, item)
		report.Results = append(report.Results, item)
		if passed {
			report.Passed++
		} else {
			report.Failed++
		}
	}
	report.FinishedAt = time.Now().UTC()
	return report
}

func RecordTaskTrace(root, evalID string, task Task, result TaskResult) string {
	recorder, err := apptrace.NewStore(root).Create("eval_"+evalID, apptrace.NewID("turn"), "", "eval", "", task.Input)
	if err != nil {
		return ""
	}
	for _, verifier := range result.Verifiers {
		recorder.AddVerifier(apptrace.VerifierResult{Name: verifier.Type, Passed: verifier.Passed, Message: verifier.Message})
	}
	var finishErr error
	if !result.Passed {
		finishErr = fmt.Errorf("verifier failure")
	}
	_ = recorder.Finish(result.Output, finishErr)
	return recorder.RunID()
}

func Verify(task Task, output, root string) []VerifyResult {
	results := make([]VerifyResult, 0, len(task.Verifiers))
	for _, v := range task.Verifiers {
		result := VerifyResult{Type: v.Type, Passed: true}
		switch v.Type {
		case "exact":
			result.Passed = output == v.Value
		case "contains":
			result.Passed = strings.Contains(output, v.Value)
		case "regex":
			matched, err := regexp.MatchString(v.Value, output)
			result.Passed = err == nil && matched
			if err != nil {
				result.Message = err.Error()
			}
		case "json_field":
			var decoded map[string]any
			err := json.Unmarshal([]byte(output), &decoded)
			if err != nil {
				result.Passed = false
				result.Message = err.Error()
			} else {
				_, result.Passed = decoded[v.Value]
			}
		case "file_exists":
			_, err := os.Stat(filepath.Join(root, v.Path))
			result.Passed = err == nil
		case "file_content":
			content, err := os.ReadFile(filepath.Join(root, v.Path))
			result.Passed = err == nil && strings.Contains(string(content), v.Value)
		case "git_diff", "session_state", "trace_field", "tool_sequence", "memory_recall", "skill_output", "channel_response", "checkpoint_resume":
			result.Passed = strings.Contains(output, v.Value)
		default:
			result.Passed = false
			result.Message = "unknown verifier"
		}
		if !result.Passed && result.Message == "" {
			result.Message = fmt.Sprintf("%s verification failed", v.Type)
		}
		results = append(results, result)
	}
	return results
}

func WriteReport(root string, report Report) (string, error) {
	dir := filepath.Join(root, ".agent", "evals", report.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	content, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "report.json"), append(content, '\n'), 0o644); err != nil {
		return "", err
	}
	var md strings.Builder
	fmt.Fprintf(&md, "# Eval %s\n\n- Mode: %s\n- Suite: %s\n- Passed: %d\n- Failed: %d\n\n", report.ID, report.Mode, report.Suite, report.Passed, report.Failed)
	for _, result := range report.Results {
		mark := "✅"
		if !result.Passed {
			mark = "❌"
		}
		fmt.Fprintf(&md, "- %s %s (%s)\n", mark, result.ID, result.Category)
	}
	if err := os.WriteFile(filepath.Join(dir, "report.md"), []byte(md.String()), 0o644); err != nil {
		return "", err
	}
	return dir, nil
}
