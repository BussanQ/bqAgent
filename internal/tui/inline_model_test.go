package tui

import (
	"bytes"
	"reflect"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// Exercise the same buffer resize, erase and cursor operations as Bubble Tea's
// inline renderer. Track the emitted terminal movements independently of its
// internal cursor so a lost frame origin cannot pass a View-only assertion.
type inlineRendererProbe struct {
	output     bytes.Buffer
	renderer   *uv.TerminalRenderer
	buffer     uv.ScreenBuffer
	row        int
	width      int
	lastOutput string
}

func newInlineRendererProbe(width int) *inlineRendererProbe {
	probe := &inlineRendererProbe{buffer: uv.NewScreenBuffer(width, 0), width: width}
	probe.renderer = uv.NewTerminalRenderer(&probe.output, []string{"TERM=xterm-256color"})
	probe.renderer.SetRelativeCursor(true)
	probe.renderer.SetTabStops(-1)
	probe.renderer.SetScrollOptim(true)
	return probe
}

func (probe *inlineRendererProbe) draw(t *testing.T, view tea.View) {
	t.Helper()
	content := uv.NewStyledString(view.Content)
	if content.Height() != probe.buffer.Height() {
		probe.renderer.Erase()
		probe.buffer.Touched = nil
		probe.buffer.Resize(probe.width, content.Height())
	}
	probe.buffer.Clear()
	content.Draw(probe.buffer, probe.buffer.Bounds())
	probe.renderer.Render(probe.buffer.RenderBuffer)
	if view.Cursor != nil {
		probe.renderer.MoveTo(view.Cursor.X, view.Cursor.Y)
	}
	if err := probe.renderer.Flush(); err != nil {
		t.Fatal(err)
	}
	output := probe.output.String()
	probe.lastOutput = output
	var state byte
	for remaining := output; len(remaining) > 0; {
		sequence, _, n, next := ansi.DecodeSequence(remaining, state, nil)
		remaining, state = remaining[n:], next
		switch sequence {
		case "\n", "\x1bD":
			probe.row++
		case "\x1bM":
			probe.row--
		default:
			if strings.HasPrefix(sequence, "\x1b[") {
				parameter := strings.TrimSuffix(strings.TrimPrefix(sequence, "\x1b["), sequence[len(sequence)-1:])
				count, _ := strconv.Atoi(parameter)
				count = max(1, count)
				switch sequence[len(sequence)-1] {
				case 'A', 'F':
					probe.row -= count
				case 'B', 'E':
					probe.row += count
				}
			}
		}
	}
	probe.output.Reset()
	if view.Cursor != nil && probe.row != view.Cursor.Y {
		t.Fatalf("inline frame drifted: terminal cursor row=%d, want %d; output=%q", probe.row, view.Cursor.Y, output)
	}
}

func TestInlineToolDrawerRepaintsWithoutAccumulatingRows(t *testing.T) {
	for _, flushIntermediate := range []bool{false, true} {
		t.Run(strconv.FormatBool(flushIntermediate), func(t *testing.T) {
			base := toolDrawerModel(t, 80)
			var model tea.Model = newInlineModel(base)
			probe := newInlineRendererProbe(base.width)
			probe.draw(t, model.View())
			var dispatch func(tea.Cmd)
			dispatch = func(command tea.Cmd) {
				if command == nil {
					return
				}
				message := command()
				// tea.Sequence's message type is private; its commands are public.
				value := reflect.ValueOf(message)
				commands := reflect.TypeOf([]tea.Cmd{})
				if value.IsValid() && value.Type().ConvertibleTo(commands) {
					for _, child := range value.Convert(commands).Interface().([]tea.Cmd) {
						dispatch(child)
					}
					return
				}
				if reflect.TypeOf(message) == reflect.TypeOf(tea.ClearScreen()) {
					// This is the renderer's inline ClearScreen implementation.
					probe.renderer.MoveTo(0, 0)
					probe.renderer.Erase()
				} else if _, ok := message.(inlineRepaintMsg); !ok {
					t.Fatalf("toggle emitted an unexpected message (possibly history output): %T", message)
				}
				var next tea.Cmd
				model, next = model.Update(message)
				if flushIntermediate {
					probe.draw(t, model.View())
				}
				dispatch(next)
			}
			for i := 0; i < 20; i++ {
				var command tea.Cmd
				model, command = model.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
				if flushIntermediate {
					probe.draw(t, model.View())
				}
				dispatch(command)
				probe.draw(t, model.View())
				actual := model.(inlineModel).model.(Model)
				if model.View().Content != actual.View().Content || len(actual.tools.items) != 80 || len(actual.printQueue) != 0 {
					t.Fatal("repaint lost the latest view or changed tool history")
				}
				if i%2 == 1 && strings.Count(model.View().Content, "\n")+1 != 1+len(actual.footerLines()) {
					t.Fatal("collapsed drawer retained blank rows")
				}
			}
		})
	}
}

func TestInlineRepaintKeepsLatestState(t *testing.T) {
	base := toolDrawerModel(t, 80)
	base.tools.expanded = true
	var model tea.Model = newInlineModel(base)
	expanded := model.View().Content
	model, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if command == nil || model.View().Content != expanded || model.View().Cursor.Y != 0 {
		t.Fatal("shrink must retain the old frame at its origin until repaint")
	}
	// Input and another toggle can arrive before the queued repaint message.
	// They must update the model without publishing a shorter frame too soon.
	model, _ = model.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	model, _ = model.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	if model.View().Content != expanded || model.View().Cursor.Y != 0 {
		t.Fatal("intervening event moved the pending frame cursor")
	}
	model, _ = model.Update(inlineRepaintMsg{})
	actual := model.(inlineModel).model.(Model)
	if actual.input.value() != "x" || !actual.tools.expanded || model.View().Content != actual.View().Content {
		t.Fatal("repaint restored stale state over intervening input")
	}
	if model.View().Cursor == nil || model.View().Cursor.Position != actual.View().Cursor.Position {
		t.Fatal("repaint failed to restore the input cursor")
	}
}
