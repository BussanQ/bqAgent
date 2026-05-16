package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallSkillWritesFetchedMarkdown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/markdown")
		_, _ = writer.Write([]byte("# Demo Skill\n\nUse this skill for demos."))
	}))
	defer server.Close()

	root := t.TempDir()
	result, err := InstallSkillToRootWithClient(root, server.Client(), true)(context.Background(), map[string]any{"url": server.URL + "/demo-skill"})
	if err != nil {
		t.Fatalf("InstallSkill returned error: %v", err)
	}
	if !strings.Contains(result, `Installed skill "demo-skill"`) {
		t.Fatalf("InstallSkill result = %q, want installed message", result)
	}

	content, err := os.ReadFile(filepath.Join(root, ".agent", "skills", "demo-skill", "SKILL.md"))
	if err != nil {
		t.Fatalf("failed to read installed skill: %v", err)
	}
	if string(content) != "# Demo Skill\n\nUse this skill for demos.\n" {
		t.Fatalf("installed content = %q", string(content))
	}
}

func TestInstallSkillDerivesNameFromURLAndNormalizesHTML(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = writer.Write([]byte(`<html><head><title>HTML Skill</title></head><body><p>Follow these instructions.</p></body></html>`))
	}))
	defer server.Close()

	root := t.TempDir()
	_, err := InstallSkillToRootWithClient(root, server.Client(), true)(context.Background(), map[string]any{"url": server.URL + "/AIHot%20Skill/"})
	if err != nil {
		t.Fatalf("InstallSkill returned error: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(root, ".agent", "skills", "aihot-skill", "SKILL.md"))
	if err != nil {
		t.Fatalf("failed to read installed skill: %v", err)
	}
	if !strings.HasPrefix(string(content), "# HTML Skill\n\n") || !strings.Contains(string(content), "Follow these instructions.") {
		t.Fatalf("installed content = %q", string(content))
	}
}

func TestInstallSkillRefusesOverwriteByDefault(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/markdown")
		_, _ = writer.Write([]byte("# Replacement"))
	}))
	defer server.Close()

	root := t.TempDir()
	skillDir := filepath.Join(root, ".agent", "skills", "demo")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("failed to create skill dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# Existing\n"), 0o644); err != nil {
		t.Fatalf("failed to create existing skill: %v", err)
	}

	_, err := InstallSkillToRootWithClient(root, server.Client(), true)(context.Background(), map[string]any{"url": server.URL + "/demo"})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("InstallSkill error = %v, want already exists", err)
	}
}

func TestInstallSkillOverwritesWhenRequested(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/markdown")
		_, _ = writer.Write([]byte("# Replacement"))
	}))
	defer server.Close()

	root := t.TempDir()
	skillDir := filepath.Join(root, ".agent", "skills", "demo")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("failed to create skill dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# Existing\n"), 0o644); err != nil {
		t.Fatalf("failed to create existing skill: %v", err)
	}

	_, err := InstallSkillToRootWithClient(root, server.Client(), true)(context.Background(), map[string]any{"url": server.URL + "/demo", "overwrite": "true"})
	if err != nil {
		t.Fatalf("InstallSkill returned error: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(skillDir, "SKILL.md"))
	if err != nil {
		t.Fatalf("failed to read overwritten skill: %v", err)
	}
	if string(content) != "# Replacement\n" {
		t.Fatalf("installed content = %q", string(content))
	}
}
