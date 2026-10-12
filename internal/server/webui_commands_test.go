package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"bqagent/internal/chatcommand"
)

func TestWebUICommandsFiltersButtonsAndRefreshesWorkspaceSkills(t *testing.T) {
	global := filepath.Join(t.TempDir(), ".agent")
	first := &Service{workspaceRoot: t.TempDir(), agentDir: global}
	second := &Service{workspaceRoot: t.TempDir(), agentDir: global}
	h := &handler{service: first, workspaces: &WorkspaceRegistry{defaultID: "first", entries: map[string]*workspaceRegistryEntry{
		"first": {service: first}, "second": {service: second},
	}}}
	writeSkill := func(root, name, description string) {
		t.Helper()
		dir := filepath.Join(root, "skills", name)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("\\---\ndescription: |\n  "+description+"\nalias: explain\n\\---\nPrivate instructions"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	writeSkill(global, "demo", "全局描述")
	writeSkill(filepath.Join(second.workspaceRoot, ".agent"), "demo", "工作区描述")
	load := func(id string) map[string]chatcommand.Command {
		t.Helper()
		response := httptest.NewRecorder()
		h.handleWebUICommands(response, httptest.NewRequest(http.MethodGet, "/api/v1/webui/commands?workspace_id="+id, nil))
		if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("response: %d %s", response.Code, response.Body.String())
		}
		var payload struct {
			Commands []chatcommand.Command `json:"commands"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		commands := map[string]chatcommand.Command{}
		for _, command := range payload.Commands {
			commands[command.Name] = command
		}
		return commands
	}
	commands := load("first")
	for _, name := range []string{"/model", "/effort", "/provider", "/run", "/ask", "/clear", "/feedback", "/stop", "/exit", "/help"} {
		if _, ok := commands[name]; ok {
			t.Fatalf("redundant command: %s", name)
		}
	}
	for _, name := range []string{"/skill", "/fast", "/memory", "/agent", "/codex", "/claude", "/cursor", "/opencode", "/pi", "/default"} {
		if _, ok := commands[name]; !ok {
			t.Fatalf("missing command: %s", name)
		}
	}
	if args := commands["/skill"].Arguments; len(args) != 2 || args[0].Description != "全局描述" || args[1].Value != "explain" {
		t.Fatalf("skill arguments: %+v", args)
	}
	if args := load("second")["/skill"].Arguments; args[0].Description != "工作区描述" {
		t.Fatalf("workspace arguments: %+v", args)
	}
	writeSkill(global, "demo", "更新后的描述")
	if args := load("first")["/skill"].Arguments; args[0].Description != "更新后的描述" {
		t.Fatalf("stale arguments: %+v", args)
	}
	for _, test := range []struct {
		method, id string
		status     int
	}{{http.MethodPost, "first", 405}, {http.MethodGet, "missing", 404}} {
		response := httptest.NewRecorder()
		h.handleWebUICommands(response, httptest.NewRequest(test.method, "/api/v1/webui/commands?workspace_id="+test.id, nil))
		if response.Code != test.status {
			t.Fatalf("status=%d want=%d", response.Code, test.status)
		}
	}
}

func TestWebUIGroupCommandsHideSubagentsAndExternalAgents(t *testing.T) {
	h := &handler{service: &Service{workspaceRoot: t.TempDir(), agentDir: filepath.Join(t.TempDir(), ".agent")}}
	for _, kind := range []string{"group", "default", ""} {
		t.Run(kind, func(t *testing.T) {
			response := httptest.NewRecorder()
			h.handleWebUICommands(response, httptest.NewRequest(http.MethodGet, "/api/v1/webui/commands?conversation_type="+kind, nil))
			if response.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			var payload struct {
				Commands []chatcommand.Command `json:"commands"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			found := map[string]bool{}
			for _, command := range payload.Commands {
				found[command.Name] = true
			}
			for _, name := range []string{"/agent", "/claude", "/codex", "/cursor", "/opencode", "/pi", "/default"} {
				if found[name] != (kind != "group") {
					t.Fatalf("type=%q command=%s present=%t", kind, name, found[name])
				}
			}
			for _, name := range []string{"/skill", "/fast", "/memory"} {
				if !found[name] {
					t.Fatalf("missing command: %s", name)
				}
			}
		})
	}
}
