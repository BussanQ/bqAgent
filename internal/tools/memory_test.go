package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMemSaveDailyAppendsToTodayFile(t *testing.T) {
	memDir := t.TempDir()
	fixed := time.Date(2026, 3, 21, 10, 30, 0, 0, time.UTC)
	memoryNow = func() time.Time { return fixed }
	defer func() { memoryNow = time.Now }()

	fn := MemSaveInDir(memDir)
	result, err := fn(map[string]any{"target": "daily", "content": "user prefers dark mode"})
	if err != nil {
		t.Fatalf("mem_save daily returned error: %v", err)
	}
	if !strings.Contains(result, "daily") {
		t.Fatalf("unexpected result: %s", result)
	}

	path := filepath.Join(memDir, "2026-03-21.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read daily file: %v", err)
	}
	if !strings.Contains(string(data), "user prefers dark mode") {
		t.Fatalf("daily file missing content, got: %s", string(data))
	}
}

func TestMemSaveLongtermAppendsToMemoryFile(t *testing.T) {
	memDir := t.TempDir()
	fixed := time.Date(2026, 3, 21, 10, 30, 0, 0, time.UTC)
	memoryNow = func() time.Time { return fixed }
	defer func() { memoryNow = time.Now }()

	fn := MemSaveInDir(memDir)
	result, err := fn(map[string]any{"target": "longterm", "content": "project uses Go 1.22"})
	if err != nil {
		t.Fatalf("mem_save longterm returned error: %v", err)
	}
	if !strings.Contains(result, "longterm") {
		t.Fatalf("unexpected result: %s", result)
	}

	path := filepath.Join(memDir, "MEMORY.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read longterm file: %v", err)
	}
	if !strings.Contains(string(data), "project uses Go 1.22") {
		t.Fatalf("longterm file missing content, got: %s", string(data))
	}
}

func TestMemSaveAppendsMultipleEntries(t *testing.T) {
	memDir := t.TempDir()
	fixed := time.Date(2026, 3, 21, 10, 30, 0, 0, time.UTC)
	memoryNow = func() time.Time { return fixed }
	defer func() { memoryNow = time.Now }()

	fn := MemSaveInDir(memDir)
	_, _ = fn(map[string]any{"target": "daily", "content": "first entry"})
	_, _ = fn(map[string]any{"target": "daily", "content": "second entry"})

	path := filepath.Join(memDir, "2026-03-21.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read daily file: %v", err)
	}
	if !strings.Contains(string(data), "first entry") || !strings.Contains(string(data), "second entry") {
		t.Fatalf("daily file missing entries, got: %s", string(data))
	}
}

func TestMemSaveRejectsInvalidTarget(t *testing.T) {
	fn := MemSaveInDir(t.TempDir())
	_, err := fn(map[string]any{"target": "invalid", "content": "test"})
	if err == nil {
		t.Fatal("expected error for invalid target")
	}
}

func TestMemGetReadsLongtermMemory(t *testing.T) {
	memDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(memDir, "MEMORY.md"), []byte("# Long-term\nuser likes Go"), 0o644); err != nil {
		t.Fatal(err)
	}

	fn := MemGetInDir(memDir)
	result, err := fn(map[string]any{"target": "longterm"})
	if err != nil {
		t.Fatalf("mem_get longterm returned error: %v", err)
	}
	if !strings.Contains(result, "user likes Go") {
		t.Fatalf("unexpected result: %s", result)
	}
}

func TestMemGetReadsDailyMemory(t *testing.T) {
	memDir := t.TempDir()
	fixed := time.Date(2026, 3, 21, 10, 30, 0, 0, time.UTC)
	memoryNow = func() time.Time { return fixed }
	defer func() { memoryNow = time.Now }()

	if err := os.WriteFile(filepath.Join(memDir, "2026-03-21.md"), []byte("today's notes"), 0o644); err != nil {
		t.Fatal(err)
	}

	fn := MemGetInDir(memDir)
	result, err := fn(map[string]any{"target": "daily"})
	if err != nil {
		t.Fatalf("mem_get daily returned error: %v", err)
	}
	if !strings.Contains(result, "today's notes") {
		t.Fatalf("unexpected result: %s", result)
	}
}

func TestMemGetReadsYesterdayMemory(t *testing.T) {
	memDir := t.TempDir()
	fixed := time.Date(2026, 3, 21, 10, 30, 0, 0, time.UTC)
	memoryNow = func() time.Time { return fixed }
	defer func() { memoryNow = time.Now }()

	if err := os.WriteFile(filepath.Join(memDir, "2026-03-20.md"), []byte("yesterday's work"), 0o644); err != nil {
		t.Fatal(err)
	}

	fn := MemGetInDir(memDir)
	result, err := fn(map[string]any{"target": "yesterday"})
	if err != nil {
		t.Fatalf("mem_get yesterday returned error: %v", err)
	}
	if !strings.Contains(result, "yesterday's work") {
		t.Fatalf("unexpected result: %s", result)
	}
}

func TestMemGetReturnsFriendlyMessageWhenFileNotFound(t *testing.T) {
	fn := MemGetInDir(t.TempDir())
	result, err := fn(map[string]any{"target": "longterm"})
	if err != nil {
		t.Fatalf("mem_get returned error: %v", err)
	}
	if !strings.Contains(result, "No longterm memory found") {
		t.Fatalf("unexpected result for missing file: %s", result)
	}
}

func TestMemGetRejectsInvalidTarget(t *testing.T) {
	fn := MemGetInDir(t.TempDir())
	_, err := fn(map[string]any{"target": "invalid"})
	if err == nil {
		t.Fatal("expected error for invalid target")
	}
}
