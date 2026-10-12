package tui

import (
	"context"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// A small terminal surface for checking text survival, not just whether a
// Println command was emitted. The fixture stays away from screen boundaries.
type historyScreen struct {
	x, y int
	rows map[int]map[int]string
}

func (s *historyScreen) write(output string) {
	var state byte
	for output != "" {
		seq, width, n, next := ansi.DecodeSequence(output, state, nil)
		output, state = output[n:], next
		switch seq {
		case "\r":
			s.x = 0
		case "\n", "\x1bD":
			s.y++
		case "\x1bM":
			s.y--
		case "\b":
			s.x--
		default:
			if strings.HasPrefix(seq, "\x1b[") {
				last := seq[len(seq)-1]
				parameter := seq[2 : len(seq)-1]
				count, _ := strconv.Atoi(parameter)
				switch last {
				case 'A':
					s.y -= max(1, count)
				case 'B':
					s.y += max(1, count)
				case 'C':
					s.x += max(1, count)
				case 'D':
					s.x -= max(1, count)
				case 'G':
					s.x = max(1, count) - 1
				case 'K':
					for x := range s.rows[s.y] {
						if count == 2 || (count == 0 && x >= s.x) || (count == 1 && x <= s.x) {
							delete(s.rows[s.y], x)
						}
					}
				case 'J':
					for y := range s.rows {
						if count == 2 || (count == 0 && y > s.y) {
							delete(s.rows, y)
						}
					}
					if count == 0 {
						for x := range s.rows[s.y] {
							if x >= s.x {
								delete(s.rows[s.y], x)
							}
						}
					}
				case 'L':
					rows := make(map[int]map[int]string)
					for y, row := range s.rows {
						if y >= s.y {
							y += max(1, count)
						}
						rows[y] = row
					}
					s.rows = rows
				}
			} else if width > 0 {
				if s.rows[s.y] == nil {
					s.rows[s.y] = make(map[int]string)
				}
				s.rows[s.y][s.x] = seq
				for i := 1; i < width; i++ {
					s.rows[s.y][s.x+i] = ""
				}
				s.x += width
			}
		}
	}
}

func (s *historyScreen) text() string {
	var out strings.Builder
	for y := 0; y < 1000; y++ {
		for x := 0; x < 100; x++ {
			out.WriteString(s.rows[y][x])
		}
		out.WriteByte('\n')
	}
	return out.String()
}

func TestInlineHistoryKeepsQuestionsAcrossTurns(t *testing.T) {
	for _, test := range []struct {
		name   string
		queued bool
		answer string
	}{
		{name: "next question", answer: "line two line three"},
		{name: "multiline answer", answer: "line two\n\nline three"},
		{name: "queued question", queued: true, answer: "line two\n\nline three"},
	} {
		for _, flushBetween := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/flush=%t", test.name, flushBetween), func(t *testing.T) {
				base := NewModel(&fakeBackend{}, Config{Context: context.Background(), Workspace: t.TempDir(), AgentDir: t.TempDir(), NoColor: true})
				base.width, base.height = 72, 24
				base.resizeInput()
				var model tea.Model = newInlineModel(base)
				probe := newInlineRendererProbe(base.width)
				screen := historyScreen{y: 100, rows: make(map[int]map[int]string)}
				draw := func() {
					view := model.View()
					content := uv.NewStyledString(view.Content)
					if content.Height() != probe.buffer.Height() {
						probe.renderer.Erase()
						probe.buffer.Touched = nil
						probe.buffer.Resize(base.width, content.Height())
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
					screen.write(probe.output.String())
					probe.output.Reset()
				}
				draw()
				var dispatch func(tea.Cmd)
				dispatch = func(cmd tea.Cmd) {
					if cmd == nil {
						return
					}
					message := cmd()
					if _, ok := message.(tea.BatchMsg); ok {
						return
					} // No model I/O or timers in this renderer unit test.
					v := reflect.ValueOf(message)
					commands := reflect.TypeOf([]tea.Cmd{})
					if v.IsValid() && v.Type().ConvertibleTo(commands) {
						for _, child := range v.Convert(commands).Interface().([]tea.Cmd) {
							dispatch(child)
						}
						return
					}
					if reflect.TypeOf(message) == reflect.TypeOf(tea.Println("")()) {
						text := v.FieldByName("messageBody").String()
						_, y := probe.renderer.Position()
						h := probe.buffer.Height()
						lines := strings.Split(text, "\n")
						offset := len(lines)
						for _, line := range lines {
							if width := ansi.StringWidth(line); width > base.width {
								offset += width / base.width
							}
						}
						var output strings.Builder
						output.WriteByte('\r')
						if down := h - y - 1; down > 0 {
							output.WriteString(ansi.CursorDown(down))
						}
						output.WriteString(strings.Repeat("\n", offset))
						output.WriteString(ansi.CursorUp(offset + h - 1))
						output.WriteString(ansi.InsertLine(offset))
						for _, line := range lines {
							output.WriteString(line + ansi.EraseLineRight + "\r\n")
						}
						screen.write(output.String())
						probe.renderer.SetPosition(0, 0)
					} else if reflect.TypeOf(message) == reflect.TypeOf(tea.ClearScreen()) {
						probe.renderer.MoveTo(0, 0)
						probe.renderer.Erase()
					}
					var next tea.Cmd
					model, next = model.Update(message)
					if flushBetween {
						draw()
					}
					dispatch(next)
				}
				update := func(message tea.Msg) {
					var cmd tea.Cmd
					model, cmd = model.Update(message)
					if event, ok := message.(turnEvent); ok && event.kind == "token" {
						// Token handling schedules a blocking backend read; rendering alone is under test.
						draw()
						return
					}
					if flushBetween {
						draw()
					}
					dispatch(cmd)
					draw()
				}
				ask := func(i int) {
					inner := model.(inlineModel)
					m := inner.model.(Model)
					m.input.setValue(fmt.Sprintf("question-%d", i))
					inner.model = m
					model = inner
					update(tea.KeyPressMsg{Code: tea.KeyEnter})
				}
				for i := 1; i <= 3; i++ {
					if !test.queued || i == 1 {
						ask(i)
					}
					answer := fmt.Sprintf("answer-%d\n\n%s", i, test.answer)
					if test.queued {
						update(turnEvent{sequence: uint64(i), kind: "token", text: answer})
						if i < 3 {
							ask(i + 1)
						}
					}
					update(turnEvent{sequence: uint64(i), kind: "done", result: TurnResult{Reply: answer}})
				}
				text := screen.text()
				for i := 1; i <= 3; i++ {
					if n := strings.Count(text, fmt.Sprintf("question-%d", i)); n != 1 {
						t.Fatalf("question-%d appears %d times in terminal:\n%s", i, n, strings.TrimSpace(text))
					}
				}
				for i := 1; i <= 2; i++ {
					if n := strings.Count(text, fmt.Sprintf("answer-%d", i)); n != 1 {
						t.Fatalf("answer-%d appears %d times: %s", i, n, strings.TrimSpace(text))
					}
				}
			})
		}
	}
}
