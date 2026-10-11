package tui

import (
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
