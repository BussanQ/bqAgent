package main

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRedirectStandardLogKeepsTUIOutputClean(t *testing.T) {
	var terminal bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&terminal)
	t.Cleanup(func() { log.SetOutput(previous) })
	path := filepath.Join(t.TempDir(), "tui", "tui.log")
	restore := redirectStandardLog(path)
	log.Printf("memory append failed: boom")
	restore()
	if terminal.Len() != 0 {
		t.Fatalf("log reached the terminal during TUI: %q", terminal.String())
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "memory append failed: boom") {
		t.Fatalf("log file = %q, err=%v", data, err)
	}
	log.Printf("after")
	if !strings.Contains(terminal.String(), "after") {
		t.Fatal("previous log output was not restored")
	}
}
