package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func quitCommandPresent(commands []tea.Cmd) bool {
	for _, command := range commands {
		if command != nil {
			if _, ok := command().(tea.QuitMsg); ok {
				return true
			}
		}
	}
	return false
}

func TestDoubleCtrlCExitsIdleTUI(t *testing.T) {
	model := NewModel(&fakeBackend{}, Config{Workspace: t.TempDir(), AgentDir: t.TempDir(), NoColor: true})
	key := tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	model.input.setValue("clear draft")
	if quitCommandPresent(model.handleKey(key)) {
		t.Fatal("first Ctrl+C should not exit")
	}
	if !model.quitConfirmationActive() || model.pendingQuit || model.input.value() != "" {
		t.Fatal("first Ctrl+C should arm confirmation and clear draft")
	}
	if status := model.renderStatus(100); !strings.Contains(status, quitConfirmHint) {
		t.Fatalf("missing confirmation hint: %q", status)
	}
	if !quitCommandPresent(model.handleKey(key)) || model.quitConfirmationActive() {
		t.Fatal("second Ctrl+C should exit and clear confirmation")
	}
}

func TestFirstCtrlCClearsPromptState(t *testing.T) {
	for _, name := range []string{"empty", "multiline", "folded_paste", "suggestions"} {
		t.Run(name, func(t *testing.T) {
			model := NewModel(&fakeBackend{}, Config{Workspace: t.TempDir(), AgentDir: t.TempDir(), NoColor: true})
			model.width, model.height = 100, 30
			switch name {
			case "multiline":
				model.input.setValue("first line\nsecond line\nthird line")
			case "folded_paste":
				model.input.update(tea.PasteMsg{Content: strings.Repeat("pasted line\n", 6)})
				if len(model.input.chips) == 0 {
					t.Fatal("expected folded paste")
				}
			case "suggestions":
				model.input.setValue("/")
				model.refreshSuggestions()
				if !model.panelOpen || len(model.suggestions) == 0 {
					t.Fatal("expected command suggestions")
				}
			}
			model.resizeInput()
			if name == "multiline" && model.input.area.Height() <= 1 {
				t.Fatal("expected expanded multiline input")
			}
			commands := model.handleKey(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
			if quitCommandPresent(commands) || !model.quitConfirmationActive() {
				t.Fatal("first Ctrl+C should arm confirmation without exiting")
			}
			if model.input.value() != "" || model.input.displayValue() != "" || len(model.input.chips) != 0 {
				t.Fatal("first Ctrl+C should clear text and folded paste")
			}
			if model.panelOpen || len(model.suggestions) != 0 || model.suggested != 0 {
				t.Fatal("first Ctrl+C should close command suggestions")
			}
			if model.input.area.Height() != 1 {
				t.Fatalf("cleared input height = %d, want 1", model.input.area.Height())
			}
		})
	}
}

func TestDoubleCtrlCSafelyCancelsActiveTurn(t *testing.T) {
	model := NewModel(&fakeBackend{}, Config{Workspace: t.TempDir(), AgentDir: t.TempDir(), NoColor: true})
	cancelled := 0
	model.sequence = 1
	model.active = &activeTurn{sequence: 1, cancel: func() { cancelled++ }}
	model.phase = phaseStreaming
	model.queued = &queuedPrompt{value: "must not run"}
	model.input.setValue("clear draft while running")
	// Quitting must also take precedence over an earlier /clear request.
	model.pendingClear = true
	key := tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	model.handleKey(key)
	if cancelled != 0 || model.pendingQuit {
		t.Fatal("first Ctrl+C cancelled the running task")
	}
	if model.input.value() != "" {
		t.Fatal("first Ctrl+C should clear draft while the task is running")
	}
	if !strings.Contains(model.renderStatus(100), quitConfirmHint) {
		t.Fatal("active task status hid confirmation")
	}
	commands := model.handleKey(key)
	if len(commands) != 0 || cancelled != 1 || !model.active.cancelled || !model.pendingQuit || model.pendingClear {
		t.Fatalf("second Ctrl+C must cancel and wait: commands=%d cancelled=%d model=%+v", len(commands), cancelled, model.active)
	}
	if model.phase != phaseCancelling || model.progress != "正在安全退出" {
		t.Fatalf("exit state: phase=%v progress=%q", model.phase, model.progress)
	}
	commands = model.handleTurnEvent(turnEvent{sequence: 1, kind: "done", err: context.Canceled})
	if !quitCommandPresent(commands) || model.active != nil || model.sequence != 1 {
		t.Fatal("completion must exit without starting queued task")
	}
}

func TestDoubleCtrlCConfirmationResets(t *testing.T) {
	for _, name := range []string{"expired_before_next_press", "tick", "other_key", "paste", "mouse"} {
		t.Run(name, func(t *testing.T) {
			model := NewModel(&fakeBackend{}, Config{Workspace: t.TempDir(), AgentDir: t.TempDir(), NoColor: true})
			model.statusNotice = "原有状态"
			key := tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
			model.handleKey(key)
			switch name {
			case "expired_before_next_press":
				model.quitConfirmUntil = time.Now().Add(-time.Second)
			case "other_key":
				model.handleKey(tea.KeyPressMsg{Code: tea.KeyRight})
			default:
				var message tea.Msg
				switch name {
				case "tick":
					message = tickMsg(model.quitConfirmUntil.Add(time.Millisecond))
				case "paste":
					message = tea.PasteMsg{Content: "hello"}
				case "mouse":
					message = tea.MouseClickMsg{Button: tea.MouseLeft}
				}
				updated, _ := model.Update(message)
				model = updated.(Model)
			}
			if model.quitConfirmationActive() || strings.Contains(model.renderStatus(100), quitConfirmHint) {
				t.Fatal("confirmation was not reset")
			}
			if !strings.Contains(model.renderStatus(100), "原有状态") {
				t.Fatal("confirmation overwrote previous status")
			}
			model.handleKey(key)
			if !model.quitConfirmationActive() || model.pendingQuit {
				t.Fatal("next Ctrl+C should arm a fresh confirmation")
			}
			if !quitCommandPresent(model.handleKey(key)) {
				t.Fatal("fresh double Ctrl+C did not exit")
			}
		})
	}
}

func TestDoubleCtrlCExitsProviderWizard(t *testing.T) {
	model := NewModel(&fakeBackend{}, Config{Workspace: t.TempDir(), AgentDir: t.TempDir(), NoColor: true})
	model.provider = newProviderWizard(true, 80)
	key := tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	model.handleKey(key)
	if model.provider == nil || !strings.Contains(model.View().Content, quitConfirmHint) {
		t.Fatal("first Ctrl+C should preserve wizard and show hint")
	}
	if !quitCommandPresent(model.handleKey(key)) || model.provider != nil {
		t.Fatal("second Ctrl+C should exit provider wizard")
	}
}
