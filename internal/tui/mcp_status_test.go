package tui

import (
	"strings"
	"testing"
	"time"

	"bqagent/internal/mcp"
)

type mcpStatusBackend struct {
	fakeBackend
	statuses []mcp.ServerStatus
}

func (backend *mcpStatusBackend) MCPStatus() []mcp.ServerStatus {
	return append([]mcp.ServerStatus(nil), backend.statuses...)
}

func TestMCPStatusUpdatesWithoutAppendingHistory(t *testing.T) {
	backend := &mcpStatusBackend{statuses: []mcp.ServerStatus{{Name: "search", State: "connecting"}}}
	model := NewModel(backend, Config{Workspace: t.TempDir(), AgentDir: t.TempDir(), NoColor: true})
	if status := model.renderStatus(160); !strings.Contains(status, "MCP 连接中 1") {
		t.Fatalf("initial status = %q", status)
	}
	backend.statuses = []mcp.ServerStatus{{Name: "search", State: "available", Tools: 2}, {Name: "other", State: "error", Reason: "initialize_timeout"}}
	for range 3 {
		updated, _ := model.Update(tickMsg(time.Now()))
		model = updated.(Model)
	}
	if status := model.renderStatus(160); !strings.Contains(status, "MCP 已连接 1 · 失败 1") || strings.Contains(status, "连接中") {
		t.Fatalf("updated status = %q", status)
	}
	if len(model.printQueue) != 0 || model.phase != phaseIdle {
		t.Fatal("status polling appended transcript or blocked input")
	}
	backend.statuses = nil
	updated, _ := model.Update(tickMsg(time.Now()))
	model = updated.(Model)
	if strings.Contains(model.renderStatus(160), "MCP") {
		t.Fatal("stale discovery state remained")
	}
}
