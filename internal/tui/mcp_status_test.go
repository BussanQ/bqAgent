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
	polls    int
}

func (backend *mcpStatusBackend) MCPStatus() []mcp.ServerStatus {
	backend.polls++
	return append([]mcp.ServerStatus(nil), backend.statuses...)
}

func TestMCPStatusHidesTenSecondsAfterStartup(t *testing.T) {
	backend := &mcpStatusBackend{statuses: []mcp.ServerStatus{
		{Name: "search", State: "available"},
		{Name: "files", State: "available"},
		{Name: "other", State: "error"},
	}}
	before := time.Now()
	model := NewModel(backend, Config{Workspace: t.TempDir(), AgentDir: t.TempDir(), NoColor: true})
	deadline := model.mcpStatusUntil
	if deadline.Before(before.Add(10*time.Second)) || deadline.After(time.Now().Add(10*time.Second)) {
		t.Fatalf("MCP visibility deadline = %v, want startup + 10s", deadline)
	}
	updated, _ := model.Update(tickMsg(deadline.Add(-time.Nanosecond)))
	model = updated.(Model)
	if status := model.renderStatus(200); !strings.Contains(status, "MCP 已连接 2· 失败 1") {
		t.Fatalf("status before deadline = %q", status)
	}
	polls := backend.polls + 1 // The deadline tick checks for a final state change before hiding.
	for _, now := range []time.Time{deadline, deadline.Add(time.Second), deadline.Add(time.Hour)} {
		updated, command := model.Update(tickMsg(now))
		model = updated.(Model)
		if status := model.renderStatus(200); strings.Contains(status, "MCP") || !strings.Contains(status, "Enter 发送") {
			t.Fatalf("status after deadline = %q", status)
		}
		if backend.polls != polls {
			t.Fatal("MCP status polling continued after hiding")
		}
		if command == nil || len(model.printQueue) != 0 || model.phase != phaseIdle {
			t.Fatal("hiding MCP status stopped ticks, appended history or blocked input")
		}
	}
	model.submit("分析项目")
	defer model.active.cancel()
	if status := model.renderStatus(200); strings.Contains(status, "MCP") {
		t.Fatalf("new turn restored MCP status: %q", status)
	}
}

func TestMCPStatusWaitsForBackgroundDiscovery(t *testing.T) {
	for _, pending := range []string{"connecting", "unverified"} {
		t.Run(pending, func(t *testing.T) {
			backend := &mcpStatusBackend{statuses: []mcp.ServerStatus{
				{Name: "fast", State: pending},
				{Name: "slow", State: pending},
			}}
			model := NewModel(backend, Config{Workspace: t.TempDir(), AgentDir: t.TempDir(), NoColor: true})
			now := time.Now().Add(time.Minute)
			update := func(at time.Time) {
				updated, _ := model.Update(tickMsg(at))
				model = updated.(Model)
			}
			update(now)
			if !model.mcpStatusUntil.IsZero() || !strings.Contains(model.renderStatus(200), "MCP 连接中 2") {
				t.Fatal("countdown started while discovery was still running")
			}
			backend.statuses[0].State = "available"
			update(now.Add(time.Second))
			if !model.mcpStatusUntil.IsZero() || !strings.Contains(model.renderStatus(200), "MCP 连接中 1· 已连接 1") {
				t.Fatal("partial completion started countdown before all servers finished")
			}
			backend.statuses[1].State = "error"
			finished := now.Add(20 * time.Second)
			update(finished)
			deadline := finished.Add(10 * time.Second)
			if !model.mcpStatusUntil.Equal(deadline) {
				t.Fatalf("deadline = %v, want %v", model.mcpStatusUntil, deadline)
			}
			backend.statuses[0].CheckedAt = finished.Add(time.Second)
			update(deadline.Add(-time.Nanosecond))
			if !model.mcpStatusUntil.Equal(deadline) || !strings.Contains(model.renderStatus(200), "MCP 已连接 1· 失败 1") {
				t.Fatal("final status was hidden early or timestamp-only change reset countdown")
			}
			update(deadline)
			if strings.Contains(model.renderStatus(200), "MCP") {
				t.Fatal("final status remained after ten seconds")
			}
		})
	}
}

func TestMCPStatusRestartsCountdownOnStateChange(t *testing.T) {
	backend := &mcpStatusBackend{statuses: []mcp.ServerStatus{{Name: "search", State: "available"}}}
	model := NewModel(backend, Config{Workspace: t.TempDir(), AgentDir: t.TempDir(), NoColor: true})
	now := model.mcpStatusUntil
	backend.statuses[0].State = "error"
	updated, _ := model.Update(tickMsg(now))
	model = updated.(Model)
	if !model.mcpStatusUntil.Equal(now.Add(10*time.Second)) || !strings.Contains(model.renderStatus(200), "MCP 失败 1") {
		t.Fatal("state change at the deadline did not restart countdown")
	}
	backend.statuses[0].State = "connecting"
	updated, _ = model.Update(tickMsg(now.Add(time.Second)))
	model = updated.(Model)
	if !model.mcpStatusUntil.IsZero() || !strings.Contains(model.renderStatus(200), "MCP 连接中 1") {
		t.Fatal("background discovery restarting did not suspend countdown")
	}
}

func TestMCPStatusHidesBeforeNextTickAfterDeadline(t *testing.T) {
	backend := &mcpStatusBackend{statuses: []mcp.ServerStatus{{Name: "search", State: "available"}}}
	model := NewModel(backend, Config{Workspace: t.TempDir(), AgentDir: t.TempDir(), NoColor: true})
	model.mcpStatusUntil = time.Now().Add(-time.Nanosecond)
	if status := model.renderStatus(160); strings.Contains(status, "MCP") {
		t.Fatalf("expired MCP status remained before next tick: %q", status)
	}
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
	if status := model.renderStatus(160); !strings.Contains(status, "MCP 已连接 1· 失败 1") || strings.Contains(status, "连接中") {
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
