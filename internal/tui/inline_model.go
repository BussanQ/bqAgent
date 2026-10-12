package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// inlineModel anchors the renderer before publishing a shorter frame. The
// current renderer resizes its buffer before clearing it, which otherwise
// clamps the old cursor row and leaves the top of the old frame in scrollback.
type inlineModel struct {
	model   tea.Model
	view    tea.View
	pending bool
}

type inlineRepaintMsg struct{}

func newInlineModel(model tea.Model) inlineModel {
	return inlineModel{model: model, view: model.View()}
}

func (model inlineModel) Init() tea.Cmd { return model.model.Init() }

func (model inlineModel) View() tea.View { return model.view }

func (model inlineModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := message.(inlineRepaintMsg); ok {
		model.pending = false
		model.view = model.model.View()
		return model, nil
	}

	var command tea.Cmd
	model.model, command = model.model.Update(message)
	if model.pending {
		// Keep both the old dimensions and the origin cursor until ClearScreen
		// has reached the renderer, even if another event or frame intervenes.
		return model, command
	}
	view := model.model.View()
	if !model.view.AltScreen && !view.AltScreen && strings.Count(view.Content, "\n") < strings.Count(model.view.Content, "\n") {
		model.pending = true
		model.view.Cursor = tea.NewCursor(0, 0)
		// In inline mode ClearScreen moves to the old frame's origin and marks
		// that region for repaint. It neither prints nor clears scrollback.
		return model, tea.Sequence(tea.ClearScreen, func() tea.Msg { return inlineRepaintMsg{} }, command)
	}
	model.view = view
	return model, command
}
