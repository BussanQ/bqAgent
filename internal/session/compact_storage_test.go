package session

import (
	"bytes"
	"os"
	"reflect"
	"testing"
	"time"
)

func TestCompactSessionUsesSingleMessageFile(t *testing.T) {
	for _, saveContext := range []bool{false, true} {
		t.Run(map[bool]string{false: "working_messages_api", true: "working_context_api"}[saveContext], func(t *testing.T) {
			store := NewStore(t.TempDir())
			saved, err := store.Create(CreateOptions{Task: "single file"})
			if err != nil {
				t.Fatal(err)
			}
			working := []map[string]any{{"role": "assistant", "content": "compact summary"}}
			if saveContext {
				err = saved.SaveWorkingContext(working)
			} else {
				err = saved.SaveWorkingMessages(working)
			}
			if err != nil {
				t.Fatal(err)
			}
			assertNoSeparateWorkingFile(t, saved)
			loaded, err := saved.LoadWorkingMessages()
			if err != nil || !reflect.DeepEqual(loaded, working) {
				t.Fatalf("working messages = %#v, %v", loaded, err)
			}
			request := map[string]any{"role": "user", "content": "interrupted request"}
			if err := saved.RecordMessage(request); err != nil {
				t.Fatal(err)
			}
			reopened, err := store.Open(saved.ID())
			if err != nil {
				t.Fatal(err)
			}
			loaded, _, err = reopened.LoadResumableMessages()
			if err != nil || !reflect.DeepEqual(loaded, append(working, request)) {
				t.Fatalf("interrupted turn lost messages: %#v, %v", loaded, err)
			}
			assertNoSeparateWorkingFile(t, reopened)
		})
	}
}

func TestCompactSessionMigratesLegacyMessageFiles(t *testing.T) {
	const transcript = "{\"role\":\"user\",\"content\":\"new request\"}\n"
	const working = "{\"role\":\"assistant\",\"content\":\"summary\"}\n"
	for _, test := range []struct {
		name       string
		transcript string
		working    string
		newer      string
		want       string
		wantErr    bool
	}{
		{name: "newer working snapshot", transcript: transcript, working: working, newer: "working", want: "summary"},
		{name: "newer interrupted transcript", transcript: transcript, working: working, newer: "transcript", want: "new request"},
		{name: "equal times prefer working", transcript: transcript, working: working, want: "summary"},
		{name: "working only", working: working, want: "summary"},
		{name: "invalid working falls back", transcript: transcript, working: "not-json\n", newer: "working", want: "new request"},
		{name: "invalid transcript falls back", transcript: "not-json\n", working: working, newer: "transcript", want: "summary"},
		{name: "both invalid remain intact", transcript: "bad-transcript\n", working: "bad-working\n", wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := NewStore(t.TempDir())
			saved, err := store.Create(CreateOptions{Task: "legacy"})
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().Add(-time.Hour)
			for _, file := range []struct{ path, content, kind string }{
				{saved.MessagesPath(), test.transcript, "transcript"},
				{saved.WorkingMessagesPath(), test.working, "working"},
			} {
				if file.content == "" {
					continue
				}
				if err := os.WriteFile(file.path, []byte(file.content), 0o644); err != nil {
					t.Fatal(err)
				}
				stamp := now
				if test.newer == file.kind {
					stamp = now.Add(time.Second)
				}
				if err := os.Chtimes(file.path, stamp, stamp); err != nil {
					t.Fatal(err)
				}
			}
			loaded, _, err := saved.LoadResumableMessages()
			if test.wantErr {
				if err == nil {
					t.Fatal("expected corrupt files to fail")
				}
				for path, want := range map[string]string{saved.MessagesPath(): test.transcript, saved.WorkingMessagesPath(): test.working} {
					data, readErr := os.ReadFile(path)
					if readErr != nil || string(data) != want {
						t.Fatalf("failed migration modified %s: %q, %v", path, data, readErr)
					}
				}
				return
			}
			if err != nil || len(loaded) != 1 || loaded[0]["content"] != test.want {
				t.Fatalf("resumed messages = %#v, %v", loaded, err)
			}
			assertNoSeparateWorkingFile(t, saved)
			again, _, err := saved.LoadResumableMessages()
			if err != nil || !reflect.DeepEqual(again, loaded) {
				t.Fatalf("second resume changed messages: %#v, %v", again, err)
			}
			if test.want == "new request" {
				data, err := os.ReadFile(saved.MessagesPath())
				if err != nil || string(data) != transcript {
					t.Fatalf("migration changed original transcript bytes: %q, %v", data, err)
				}
			}
		})
	}
}

func TestCompactSessionFailedSavePreservesBothFiles(t *testing.T) {
	saved, err := NewStore(t.TempDir()).Create(CreateOptions{Task: "failed save"})
	if err != nil {
		t.Fatal(err)
	}
	original := []byte("{\"role\":\"user\",\"content\":\"keep\"}\n")
	for _, path := range []string{saved.MessagesPath(), saved.WorkingMessagesPath()} {
		if err := os.WriteFile(path, original, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := saved.SaveWorkingContext([]map[string]any{{"content": make(chan int)}}); err == nil {
		t.Fatal("expected serialization error")
	}
	for _, path := range []string{saved.MessagesPath(), saved.WorkingMessagesPath()} {
		data, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(data, original) {
			t.Fatalf("failed save modified %s: %q, %v", path, data, err)
		}
	}
}

func TestCompactSessionFailedMigrationKeepsLegacySnapshot(t *testing.T) {
	saved, err := NewStore(t.TempDir()).Create(CreateOptions{Task: "failed migration"})
	if err != nil {
		t.Fatal(err)
	}
	original := []byte("{\"role\":\"assistant\",\"content\":\"keep snapshot\"}\n")
	if err := os.WriteFile(saved.WorkingMessagesPath(), original, 0o644); err != nil {
		t.Fatal(err)
	}
	// A directory at the target path makes atomic replacement fail reliably.
	if err := os.Mkdir(saved.MessagesPath(), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := saved.LoadResumableMessages(); err == nil {
		t.Fatal("expected migration write error")
	}
	data, err := os.ReadFile(saved.WorkingMessagesPath())
	if err != nil || !bytes.Equal(data, original) {
		t.Fatalf("failed migration lost legacy snapshot: %q, %v", data, err)
	}
}

func TestCompactMaintenancePreservesNewerTranscript(t *testing.T) {
	store := NewStore(t.TempDir())
	saved, err := store.Create(CreateOptions{Task: "interrupted"})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeMessagesJSONL(saved.WorkingMessagesPath(), []map[string]any{{"content": "old snapshot"}}); err != nil {
		t.Fatal(err)
	}
	if err := saved.RecordMessage(map[string]any{"content": "new request"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := os.Chtimes(saved.WorkingMessagesPath(), now.Add(-time.Second), now.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := saved.MarkFailed(nil); err != nil {
		t.Fatal(err)
	}
	if errs := store.MaintainExistingSessions(); len(errs) != 0 {
		t.Fatal(errs)
	}
	loaded, err := saved.LoadMessages()
	if err != nil || len(loaded) != 1 || loaded[0]["content"] != "new request" {
		t.Fatalf("maintenance restored stale snapshot: %#v, %v", loaded, err)
	}
	assertNoSeparateWorkingFile(t, saved)
}

func assertNoSeparateWorkingFile(t *testing.T, saved *Session) {
	t.Helper()
	if _, err := os.Stat(saved.WorkingMessagesPath()); !os.IsNotExist(err) {
		t.Fatalf("compact session still has a separate working file: %v", err)
	}
}
