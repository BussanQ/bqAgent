package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"bqagent/internal/globalconfig"
)

func TestDoctorCLIValidationAndReadOnlyExitCodes(t *testing.T) {
	for _, args := range [][]string{{"--doctor-json"}, {"--doctor-active"}, {"--doctor", "--chat"}, {"--doctor", "task"}, {"--doctor", "--server"}} {
		if _, _, err := parseCLI(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	home := t.TempDir()
	root := t.TempDir()
	env := func(key string) string {
		if key == "HOME" {
			return home
		}
		return ""
	}
	var out, errs bytes.Buffer
	deps := runDeps{getwd: func() (string, error) { return root, nil }, setenv: func(string, string) error { t.Fatal("doctor mutates environment"); return nil }}
	code := runWithDeps(context.Background(), nil, &out, &errs, []string{"--doctor", "--doctor-json"}, env, deps)
	if code != 1 {
		t.Fatalf("exit=%d: %s", code, errs.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".agent")); !os.IsNotExist(err) {
		t.Fatal("doctor initialized configuration")
	}
	store := globalconfig.NewStore(filepath.Join(home, ".agent"))
	if err := store.EnsureDefaults(); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	code = runWithDeps(context.Background(), nil, &out, &errs, []string{"--doctor", "--doctor-json"}, env, deps)
	if code != 0 || !bytes.Contains(out.Bytes(), []byte(`"ready": true`)) {
		t.Fatalf("exit=%d %s", code, out.String())
	}
	if code := runWithDeps(context.Background(), nil, &out, &errs, []string{"--doctor-json"}, env, deps); code != 2 {
		t.Fatal(code)
	}
}
