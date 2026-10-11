package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestEffortPickerEnterConfirmsSelectedLevel(t *testing.T) {
	levels := []string{"auto", "low", "medium", "high", "max"}
	var arguments []CommandArgument
	for _, level := range levels {
		arguments = append(arguments, CommandArgument{Value: level})
	}
	for index, level := range levels {
		t.Run(level, func(t *testing.T) {
			backend := &fakeBackend{commands: []Command{{Name: "/effort", Arguments: arguments}}}
			model := NewModel(backend, Config{Context: context.Background(), Workspace: t.TempDir(), AgentDir: t.TempDir(), NoColor: true})
			model.input.setValue("/effort")
			model.refreshSuggestions()
			if len(model.suggestions) != len(levels) || !strings.Contains(model.renderSuggestions(100), "选择推理努力程度") {
				t.Fatalf("missing effort picker: %+v", model.suggestions)
			}
			for i := 0; i < index; i++ {
				model.handleKey(tea.KeyPressMsg{Code: tea.KeyDown})
			}
			commands := model.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
			if len(commands) != 1 || model.active == nil || model.panelOpen || model.input.value() != "" || model.history[len(model.history)-1] != "/effort "+level {
				t.Fatalf("Enter did not submit effort: input=%q history=%v", model.input.value(), model.history)
			}
			model.active.cancel()
		})
	}
}

func TestEffortPickerTabOnlyCompletes(t *testing.T) {
	backend := &fakeBackend{commands: []Command{{Name: "/effort", Arguments: []CommandArgument{{Value: "auto"}, {Value: "max"}}}}}
	model := NewModel(backend, Config{Context: context.Background(), Workspace: t.TempDir(), AgentDir: t.TempDir(), NoColor: true})
	model.input.setValue("/EFFORT ")
	model.refreshSuggestions()
	model.handleKey(tea.KeyPressMsg{Code: tea.KeyUp})
	model.handleKey(tea.KeyPressMsg{Code: tea.KeyTab})
	if model.input.value() != "/effort max" || model.active != nil {
		t.Fatal("Tab should fill the selected effort without submitting")
	}
	model.active = &activeTurn{cancel: func() {}}
	model.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if model.queued == nil || model.queued.value != "/effort max" || model.panelOpen || model.input.value() != "" {
		t.Fatal("Enter should queue the selected effort behind the active turn")
	}
}
