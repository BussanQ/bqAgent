package evalharness

import "testing"

func TestRunReplay(t *testing.T) {
	tasks := make([]Task, 20)
	for i := range tasks {
		tasks[i] = Task{ID: "task", Category: "test", Suite: "smoke", ReplayOutput: "ok", Verifiers: []Verifier{{Type: "contains", Value: "ok", Required: true}}}
	}
	report := RunReplay(Manifest{Version: 1, Tasks: tasks}, "smoke", t.TempDir())
	if report.Passed != 20 || report.Failed != 0 {
		t.Fatalf("report=%+v", report)
	}
}
