package memory

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStructuredMemorySearchConflictAndConfirmation(t *testing.T) {
	store := NewStore(t.TempDir())
	first, err := store.Add(KindProjectFact, "项目使用 Go 语言和标准测试工具", "run-1", .9, "normal", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Add(KindProjectFact, "项目使用 Go 语言和标准测试工具", "run-2", .9, "normal", nil); err == nil {
		t.Fatal("expected duplicate rejection")
	}
	results, err := store.Search("Go 项目", nil, 5)
	if err != nil || len(results) == 0 || results[0].Entry.ID != first.ID {
		t.Fatalf("results=%+v err=%v", results, err)
	}
	pending, err := store.Add(KindUserPreference, "用户偏好简洁的技术说明", "run-3", .8, "sensitive", nil)
	if err != nil {
		t.Fatal(err)
	}
	if pending.State != "pending" {
		t.Fatalf("state=%s", pending.State)
	}
	confirmed, err := store.Confirm(pending.ID, "run-4")
	if err != nil || confirmed.State != "active" {
		t.Fatalf("confirmed=%+v err=%v", confirmed, err)
	}
}

func TestLegacyMigrationIsIdempotent(t *testing.T) {
	root := t.TempDir()
	legacy := filepath.Join(root, "MEMORY.md")
	if err := os.WriteFile(legacy, []byte("## Fact\nUses Go\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := NewStore(filepath.Join(root, "structured"), legacy)
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	before, _ := store.ListAll()
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	after, _ := store.ListAll()
	if len(before) != 1 || len(after) != 1 {
		t.Fatalf("before=%d after=%d", len(before), len(after))
	}
}

func TestMigrateWithoutLegacyDataDoesNotCreateMemoryDirectory(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".agent", "memory")
	store := NewStore(dir,
		filepath.Join(dir, "MEMORY.md"),
		filepath.Join(dir, "2026-08-21.md"),
	)

	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".agent")); !os.IsNotExist(err) {
		t.Fatalf("migration without legacy data created .agent: %v", err)
	}
}

func TestSearchEmptyStoreDoesNotCreateMemoryDirectory(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".agent", "memory")
	store := NewStore(dir)

	results, err := store.Search("Go project", nil, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("results=%+v, want empty", results)
	}
	if _, err := os.Stat(filepath.Join(root, ".agent")); !os.IsNotExist(err) {
		t.Fatalf("searching an empty store created .agent: %v", err)
	}
}

func TestAddCreatesMemoryDirectoryOnFirstWrite(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".agent", "memory")
	store := NewStore(dir)

	if _, err := store.Add(KindProjectFact, "项目使用 Go", "run-1", .9, "normal", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "entries.jsonl")); err != nil {
		t.Fatalf("first memory write did not initialize the store: %v", err)
	}
}
