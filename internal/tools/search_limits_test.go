package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func writeSearchTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSearchDoesNotFollowSymlinksOutsideWorkspace(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeSearchTestFile(t, filepath.Join(outside, "secret.txt"), "TOPSECRET\n")
	writeSearchTestFile(t, filepath.Join(outside, "nested", "deep.txt"), "TOPSECRET\n")
	writeSearchTestFile(t, filepath.Join(root, "inside.txt"), "visible\n")
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "file-link.txt")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "dir-link")); err != nil {
		t.Fatal(err)
	}
	grep := GrepInRoot(root)
	glob := GlobInRoot(root)

	for _, searchPath := range []string{"", "file-link.txt", "dir-link", "dir-link/nested", "dir-link/secret.txt"} {
		out, err := grep(context.Background(), map[string]any{"pattern": "TOPSECRET", "path": searchPath})
		if strings.Contains(out, "TOPSECRET") {
			t.Fatalf("grep path %q leaked outside content: %q", searchPath, out)
		}
		if searchPath != "" && err == nil {
			t.Fatalf("grep path %q should be rejected, got %q", searchPath, out)
		}
	}
	for _, searchPath := range []string{"", "dir-link", "dir-link/nested"} {
		out, _ := glob(context.Background(), map[string]any{"pattern": "**/*.txt", "path": searchPath})
		if strings.Contains(out, "secret.txt") || strings.Contains(out, "deep.txt") {
			t.Fatalf("glob path %q listed outside files: %q", searchPath, out)
		}
	}
}

func TestGrepFollowsSymlinkInsideWorkspace(t *testing.T) {
	root := t.TempDir()
	writeSearchTestFile(t, filepath.Join(root, "real", "a.txt"), "needle\n")
	// os.Root follows relative links that stay inside the root (absolute links
	// are rejected, consistent with read_file).
	if err := os.Symlink("real", filepath.Join(root, "alias")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	out, err := GrepInRoot(root)(context.Background(), map[string]any{"pattern": "needle", "path": "alias"})
	if err != nil {
		t.Fatalf("grep via in-workspace link: %v", err)
	}
	if !strings.Contains(out, "alias/a.txt:1:needle") {
		t.Fatalf("grep output = %q, want match through in-workspace link", out)
	}
}

func TestGrepSkipsUnreadableDirectory(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permission bits not enforced")
	}
	root := t.TempDir()
	writeSearchTestFile(t, filepath.Join(root, "ok.txt"), "needle\n")
	locked := filepath.Join(root, "locked")
	writeSearchTestFile(t, filepath.Join(locked, "hidden.txt"), "needle\n")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	out, err := GrepInRoot(root)(context.Background(), map[string]any{"pattern": "needle"})
	if err != nil {
		t.Fatalf("grep should skip unreadable directory, got error: %v", err)
	}
	if !strings.Contains(out, "ok.txt:1:needle") {
		t.Fatalf("grep output = %q, want readable match", out)
	}
	globOut, err := GlobInRoot(root)(context.Background(), map[string]any{"pattern": "**/*.txt"})
	if err != nil || !strings.Contains(globOut, "ok.txt") {
		t.Fatalf("glob should skip unreadable directory, got %q, %v", globOut, err)
	}
}

func TestGrepCapsMaxResultsAndLineLength(t *testing.T) {
	root := t.TempDir()
	var content strings.Builder
	for i := 0; i < grepMaxResultsLimit+50; i++ {
		content.WriteString("hit\n")
	}
	writeSearchTestFile(t, filepath.Join(root, "many.txt"), content.String())
	writeSearchTestFile(t, filepath.Join(root, "wide.txt"), "wide"+strings.Repeat("界", grepMaxLineBytes)+"\n")

	out, err := GrepInRoot(root)(context.Background(), map[string]any{"pattern": "hit", "max_results": "100000"})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(out, ":hit"); got != grepMaxResultsLimit {
		t.Fatalf("returned %d matches, want hard cap %d", got, grepMaxResultsLimit)
	}
	if !strings.Contains(out, fmt.Sprintf("truncated at %d matches", grepMaxResultsLimit)) {
		t.Fatalf("missing truncation marker: %q", out[len(out)-80:])
	}

	wide, err := GrepInRoot(root)(context.Background(), map[string]any{"pattern": "wide"})
	if err != nil {
		t.Fatal(err)
	}
	if len(wide) > grepMaxLineBytes+len("wide.txt:1:")+len("…") || !strings.HasSuffix(wide, "…") {
		t.Fatalf("wide line not truncated, len=%d", len(wide))
	}
}

func TestGrepStopsAtScannedByteBudget(t *testing.T) {
	root := t.TempDir()
	chunk := strings.Repeat("x", 4<<20) + "\n"
	files := grepMaxScannedBytes/len(chunk) + 2
	for i := 0; i < files; i++ {
		writeSearchTestFile(t, filepath.Join(root, fmt.Sprintf("f%02d.txt", i)), chunk)
	}
	out, err := GrepInRoot(root)(context.Background(), map[string]any{"pattern": "needle"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "search stopped after scanning") {
		t.Fatalf("grep output = %q, want scanned-bytes stop notice", out)
	}
}

func TestGlobKeepsNewestMatchesWhenTruncated(t *testing.T) {
	root := t.TempDir()
	base := time.Now().Add(-time.Hour)
	total := defaultGlobMaxResults + 5
	for i := 0; i < total; i++ {
		path := filepath.Join(root, fmt.Sprintf("f%04d.go", i))
		writeSearchTestFile(t, path, "x")
		stamp := base.Add(time.Duration(i) * time.Second)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	out, err := GlobInRoot(root)(context.Background(), map[string]any{"pattern": "*.go"})
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(out, "\n")
	if len(lines) != defaultGlobMaxResults+1 || !strings.Contains(lines[len(lines)-1], "truncated") {
		t.Fatalf("got %d lines, want %d files plus truncation marker", len(lines), defaultGlobMaxResults)
	}
	if lines[0] != fmt.Sprintf("f%04d.go", total-1) {
		t.Fatalf("first entry = %q, want newest file", lines[0])
	}
	if lines[defaultGlobMaxResults-1] != fmt.Sprintf("f%04d.go", total-defaultGlobMaxResults) {
		t.Fatalf("last kept entry = %q, want oldest of newest %d", lines[defaultGlobMaxResults-1], defaultGlobMaxResults)
	}
	if strings.Contains(out, "f0000.go") {
		t.Fatal("oldest file should be dropped")
	}
}
