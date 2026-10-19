package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func TestStatusPlacesActivityLeftAndRuntimeRight(t *testing.T) {
	for _, state := range []string{"idle", "running", "completed"} {
		t.Run(state, func(t *testing.T) {
			model := NewModel(&fakeBackend{}, Config{Workspace: t.TempDir(), AgentDir: t.TempDir(), NoColor: true})
			model.runtime.Provider = "api"
			model.runtime.Model = "model"
			model.runtime.ModelDisplayName = ""
			model.sessionID = "20261011-session"
			prefix := "Enter 发送"
			if state != "idle" {
				model.usage = &TokenUsage{InputTokens: 398800, OutputTokens: 4500}
				prefix = "in 398.8K · out 4.5K"
			}
			if state == "running" {
				model.phase = phaseThinking
				model.startedAt = time.Now()
			}
			for _, width := range []int{200, 40} {
				status := ansi.Strip(model.renderStatus(width))
				if !strings.HasPrefix(status, prefix) || !strings.HasSuffix(status, "api/model · Run") || strings.Contains(status, "20261011") {
					t.Fatalf("width %d: swapped status = %q", width, status)
				}
				if width == 200 && (strings.Contains(status, "\n") || ansi.StringWidth(status) != width) {
					t.Fatalf("runtime should align to right edge: %q", status)
				}
				if width == 40 && !strings.Contains(status, "\napi/model") {
					t.Fatalf("narrow layout should put runtime below activity: %q", status)
				}
			}
		})
	}
}

func TestStatusShowsProviderNamespaceAfterModelSwitch(t *testing.T) {
	model := NewModel(&fakeBackend{}, Config{Workspace: t.TempDir(), AgentDir: t.TempDir(), NoColor: true})
	model.runtime = RuntimeInfo{Provider: "gateway", APIType: "openai-response", Model: "plain", ModelDisplayName: "Plain"}
	if !strings.HasSuffix(ansi.Strip(model.renderStatus(200)), "gateway/Plain · Run") {
		t.Fatal("missing configured provider for unqualified model")
	}
	for _, provider := range []string{"group", "other"} {
		model.active = &activeTurn{sequence: model.sequence}
		model.handleTurnEvent(turnEvent{sequence: model.sequence, kind: "done", result: TurnResult{Model: provider + "/auto-deepseek", ModelDisplayName: "Deepseek"}})
		if got := ansi.Strip(model.renderStatus(200)); !strings.HasSuffix(got, provider+"/Deepseek · Run") || strings.Contains(got, "openai-response/Deepseek") {
			t.Fatalf("provider did not follow selected model: %q", got)
		}
	}
}

func TestStatusFreezesTurnElapsedAfterFinish(t *testing.T) {
	newModel := func() Model {
		model := NewModel(&fakeBackend{}, Config{Workspace: t.TempDir(), AgentDir: t.TempDir(), NoColor: true})
		model.runtime = RuntimeInfo{Provider: "api", Model: "model"}
		return model
	}
	for _, kind := range []string{"done", "cancel", "fail"} {
		t.Run(kind, func(t *testing.T) {
			model := newModel()
			model.phase = phaseThinking
			model.startedAt = time.Now().Add(-84 * time.Second)
			model.active = &activeTurn{sequence: model.sequence}
			event := turnEvent{sequence: model.sequence, kind: "done"}
			if kind == "cancel" {
				event.err = context.Canceled
			} else if kind == "fail" {
				event.err = errors.New("boom")
			}
			model.handleTurnEvent(event)
			first := ansi.Strip(model.renderStatus(200))
			if !strings.Contains(first, "耗时 1m24s") {
				t.Fatalf("missing frozen elapsed: %q", first)
			}
			model.startedAt = model.startedAt.Add(-time.Hour)
			if second := ansi.Strip(model.renderStatus(200)); second != first {
				t.Fatalf("elapsed kept ticking: %q vs %q", first, second)
			}
			for _, line := range strings.Split(ansi.Strip(model.renderStatus(30)), "\n") {
				if ansi.StringWidth(line) > 30 {
					t.Fatalf("elapsed overflowed narrow status: %q", line)
				}
			}
		})
	}
}

func TestStatusElapsedUpdatesAndResetsOnNextTurn(t *testing.T) {
	model := NewModel(&fakeBackend{}, Config{Workspace: t.TempDir(), AgentDir: t.TempDir(), NoColor: true})
	model.turnElapsed = 84 * time.Second
	model.submit("分析项目")
	defer model.active.cancel()
	if model.turnElapsed != 0 || model.startedAt.IsZero() {
		t.Fatal("next turn did not reset its elapsed time")
	}
	model.startedAt = time.Now().Add(-2 * time.Second)
	first := ansi.Strip(model.renderStatus(200))
	model.startedAt = model.startedAt.Add(-82 * time.Second)
	second := ansi.Strip(model.renderStatus(200))
	if !strings.Contains(first, "耗时 2s") || !strings.Contains(second, "耗时 1m24s") {
		t.Fatalf("running elapsed did not update: %q -> %q", first, second)
	}
}

func TestStatusElapsedResetsForNewSessionState(t *testing.T) {
	model := NewModel(&fakeBackend{}, Config{Workspace: t.TempDir(), AgentDir: t.TempDir(), NoColor: true})
	if strings.Contains(ansi.Strip(model.renderStatus(200)), "耗时") {
		t.Fatal("fresh/resumed session must not show invented elapsed")
	}
	model.turnElapsed = 3 * time.Second
	model.clearSession()
	if model.turnElapsed != 0 || strings.Contains(ansi.Strip(model.renderStatus(200)), "耗时") {
		t.Fatal("clearSession must drop elapsed")
	}
}
