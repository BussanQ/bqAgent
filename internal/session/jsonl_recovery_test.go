package session

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestJSONLRecoversOnlyIncompleteFinalRecord(t *testing.T) {
	prefix := "{\"role\":\"user\",\"content\":\"keep\"}\n"
	for _, tt := range []struct {
		name, tail string
		recover    bool
	}{
		{"partial object", `{"role":"assistant","content":"unfinished`, true},
		{"partial literal", `{"content":tru`, true},
		{"malformed final", `{"content":invalid}`, false},
		{"terminated partial", "{\"content\":\n", false},
		{"middle corruption", "{\"content\":\n{\"content\":\"later\"}\n", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "messages.jsonl")
			original := prefix + tt.tail
			if err := os.WriteFile(path, []byte(original), 0600); err != nil {
				t.Fatal(err)
			}
			var logs bytes.Buffer
			old := log.Writer()
			log.SetOutput(&logs)
			defer log.SetOutput(old)
			messages, err := readMessagesJSONL(path)
			if !tt.recover {
				if err == nil {
					t.Fatal("corruption silently accepted")
				}
				data, _ := os.ReadFile(path)
				if string(data) != original {
					t.Fatal("corrupt file changed")
				}
				return
			}
			if err != nil || len(messages) != 1 || messages[0]["content"] != "keep" {
				t.Fatalf("messages=%#v err=%v", messages, err)
			}
			backups, _ := filepath.Glob(path + ".incomplete-*")
			if len(backups) != 1 {
				t.Fatalf("backups=%v", backups)
			}
			backup, _ := os.ReadFile(backups[0])
			if string(backup) != original {
				t.Fatal("original not preserved")
			}
			if !strings.Contains(logs.String(), backups[0]) {
				t.Fatal("recovery warning omitted backup path")
			}
			if err := appendJSONL(path, map[string]any{"content": "next"}); err != nil {
				t.Fatal(err)
			}
			messages, err = readMessagesJSONL(path)
			if err != nil || len(messages) != 2 || messages[1]["content"] != "next" {
				t.Fatalf("append after recovery: %#v %v", messages, err)
			}
		})
	}
}

func TestJSONLAppendRepairsUnterminatedTail(t *testing.T) {
	for _, tail := range []string{`{"content":"complete"}`, `{"content":"partial`} {
		path := filepath.Join(t.TempDir(), "messages.jsonl")
		if err := os.WriteFile(path, []byte(tail), 0600); err != nil {
			t.Fatal(err)
		}
		if err := appendJSONL(path, map[string]any{"content": "next"}); err != nil {
			t.Fatal(err)
		}
		messages, err := readMessagesJSONL(path)
		if err != nil || len(messages) == 0 || messages[len(messages)-1]["content"] != "next" {
			t.Fatalf("messages=%#v err=%v", messages, err)
		}
	}
}

func TestSessionCorruptJournalDoesNotFallBackToOlderWorkingSnapshot(t *testing.T) {
	saved, err := NewStore(t.TempDir()).Create(CreateOptions{Task: "corruption"})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeMessagesJSONL(saved.WorkingMessagesPath(), []map[string]any{{"content": "old"}}); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(saved.WorkingMessagesPath(), old, old); err != nil {
		t.Fatal(err)
	}
	corrupt := []byte("{\"content\":\"keep\"}\ninvalid\n{\"content\":\"later\"}\n")
	if err := os.WriteFile(saved.MessagesPath(), corrupt, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := saved.LoadResumableMessages(); err == nil {
		t.Fatal("corrupt journal silently replaced by stale working snapshot")
	}
	after, err := os.ReadFile(saved.MessagesPath())
	if err != nil || !bytes.Equal(after, corrupt) {
		t.Fatal("corrupt journal was modified")
	}
}

func TestJSONLAppendEncodingFailureDoesNotWritePrefix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "messages.jsonl")
	if err := appendJSONL(path, map[string]any{"content": "keep"}); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	if err := appendJSONL(path, map[string]any{"content": "new"}, map[string]any{"bad": make(chan int)}); err == nil {
		t.Fatal("wanted encoding error")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("failed batch partially appended")
	}
}
