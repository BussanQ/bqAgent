package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func newModelPickerTestModel(t *testing.T) Model {
	t.Helper()
	var arguments []CommandArgument
	for i := 0; i < 12; i++ {
		arguments = append(arguments, CommandArgument{Value: fmt.Sprintf("id-%02d", i), Label: fmt.Sprintf("Model %02d", i)})
	}
	backend := &fakeBackend{commands: []Command{{Name: "/model", Arguments: arguments}}}
	return NewModel(backend, Config{Context: context.Background(), Workspace: t.TempDir(), AgentDir: t.TempDir(), NoColor: true})
}

func TestModelPickerEnterConfirmsSelectedModel(t *testing.T) {
	for _, input := range []string{"/model", "/model ", "/MODEL"} {
		t.Run(input, func(t *testing.T) {
			model := newModelPickerTestModel(t)
			model.input.setValue(input)
			model.refreshSuggestions()
			if !model.panelOpen || len(model.suggestions) != 12 {
				t.Fatalf("missing model choices: %+v", model.suggestions)
			}
			model.handleKey(tea.KeyPressMsg{Code: tea.KeyDown})
			commands := model.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
			if len(commands) != 1 || model.active == nil || model.panelOpen || model.input.value() != "" || model.history[len(model.history)-1] != "/model id-01" {
				t.Fatalf("Enter did not submit selected model: input=%q active=%v history=%v", model.input.value(), model.active != nil, model.history)
			}
			model.active.cancel()
		})
	}
}

func TestModelPickerScrollsBeyondFirstEightChoices(t *testing.T) {
	model := newModelPickerTestModel(t)
	model.input.setValue("/model")
	model.refreshSuggestions()
	model.handleKey(tea.KeyPressMsg{Code: tea.KeyUp})
	view := model.renderSuggestions(100)
	if !strings.Contains(view, "› /model Model 11") || strings.Contains(view, "Model 00") || strings.Count(view, "\n") != maxSuggestions {
		t.Fatalf("last model not visible in fixed height picker: %q", view)
	}
	model.handleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	if model.suggested != 0 || !strings.Contains(model.renderSuggestions(100), "› /model Model 00") {
		t.Fatal("selection did not wrap to first model")
	}
	for i := 0; i < 10; i++ {
		model.handleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	model.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if model.active == nil || model.history[len(model.history)-1] != "/model id-10" {
		t.Fatal("could not submit a model beyond the first eight choices")
	}
	model.active.cancel()
}

func TestModelPickerTabCompletesAndEnterQueuesDuringRun(t *testing.T) {
	model := newModelPickerTestModel(t)
	model.input.setValue("/model Model 09")
	model.refreshSuggestions()
	model.handleKey(tea.KeyPressMsg{Code: tea.KeyTab})
	if model.input.value() != "/model id-09" || model.active != nil {
		t.Fatal("Tab should complete without submitting")
	}
	model.active = &activeTurn{cancel: func() {}}
	model.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if model.queued == nil || model.queued.value != "/model id-09" || model.input.value() != "" {
		t.Fatal("selected model should queue behind active turn")
	}
	model.active = nil
	model.input.setValue("/fa")
	model.refreshSuggestions()
	model.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if model.input.value() != "/fast" || model.active != nil {
		t.Fatal("other command completion behavior changed")
	}
}
