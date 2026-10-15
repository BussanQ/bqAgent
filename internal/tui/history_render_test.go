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
// Println command was emitted. Finite-height mode also models native scrollback
// and cursor clamping, so oversized history insertions cannot hide duplicates.
type historyScreen struct {
	x, y        int
	top, height int
	rows        map[int]map[int]string
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
			if s.height > 0 && s.y >= s.top+s.height {
				s.top++
			}
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
					if s.height > 0 {
						s.y = max(s.top, s.y)
					}
				case 'B':
					s.y += max(1, count)
					if s.height > 0 {
						s.y = min(s.top+s.height-1, s.y)
					}
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
						if (count == 2 && (s.height == 0 || y >= s.top)) || (count == 0 && y > s.y) {
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
						if s.height == 0 || y < s.top+s.height {
							rows[y] = row
						}
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
		name     string
		queued   bool
		streamed bool
		tools    int
		answer   string
	}{
		{name: "next question", answer: "line two line three"},
		{name: "multiline answer", answer: "line two\n\nline three"},
		{name: "queued question", queued: true, answer: "line two\n\nline three"},
		{name: "long answer with few tools", tools: 4, answer: strings.Repeat("long paragraph\n\n", 60) + "last paragraph"},
		{name: "long queued answer with tools", tools: 10, queued: true, answer: strings.Repeat("long paragraph\n\n", 60) + "last paragraph"},
		{name: "long streamed answer with tools", tools: 10, streamed: true, answer: strings.Repeat("long paragraph\n\n", 60) + "last paragraph"},
		{name: "long streamed answer", streamed: true, answer: strings.Repeat("long paragraph\n\n", 60) + "last paragraph"},
		{name: "long answer", answer: strings.Repeat("long paragraph\n\n", 60) + "last paragraph"},
		{name: "long queued answer", queued: true, answer: strings.Repeat("long paragraph\n\n", 60) + "last paragraph"},
	} {
		for _, flushBetween := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/flush=%t", test.name, flushBetween), func(t *testing.T) {
				base := NewModel(&fakeBackend{}, Config{Context: context.Background(), Workspace: t.TempDir(), AgentDir: t.TempDir(), NoColor: !test.streamed})
				base.width, base.height = 72, 24
				base.resizeInput()
				var model tea.Model = newInlineModel(base)
				probe := newInlineRendererProbe(base.width)
				screen := historyScreen{y: 100, top: 100, height: base.height, rows: make(map[int]map[int]string)}
				draw := func() {
					view := model.View()
					if view.AltScreen {
						return // The inspector uses a separate screen, preserving the main buffer.
					}
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
						children := v.Convert(commands).Interface().([]tea.Cmd)
						if len(children) == 3 {
							if _, history := children[1]().(historyPrintedMsg); history {
								children = children[:2] // The final command resumes backend I/O.
							}
						}
						for _, child := range children {
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
					if event, ok := message.(turnEvent); ok && event.kind == "token" && model.(inlineModel).model.(Model).historyPrints == 0 {
						// Without transcript overflow, this command only waits for backend I/O.
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
					if test.tools > 0 {
						inner := model.(inlineModel)
						m := inner.model.(Model)
						m.tools = toolDrawerModel(t, test.tools).tools
						inner.model = m
						model = inner
					}
					answer := fmt.Sprintf("answer-%d\n\n%s", i, test.answer)
					if test.queued || test.streamed {
						update(turnEvent{sequence: uint64(i), kind: "token", text: answer})
						if test.queued && i < 3 {
							ask(i + 1)
						}
					}
					update(turnEvent{sequence: uint64(i), kind: "done", result: TurnResult{Reply: answer}})
					if strings.HasPrefix(test.name, "long") && (!strings.Contains(screen.text(), fmt.Sprintf("answer-%d", i)) || strings.Count(screen.text(), "last paragraph") != i || strings.Count(screen.text(), "long paragraph") != 60*i) {
						t.Fatalf("long reply incomplete after turn %d: answer=%t last=%d paragraphs=%d", i, strings.Contains(screen.text(), fmt.Sprintf("answer-%d", i)), strings.Count(screen.text(), "last paragraph"), strings.Count(screen.text(), "long paragraph"))
					}
					if test.tools > 0 {
						text := screen.text()
						start := strings.Index(text, fmt.Sprintf("question-%d", i))
						if start < 0 {
							t.Fatalf("question-%d disappeared before checking turn order", i)
						}
						answerStart := strings.Index(text, fmt.Sprintf("answer-%d", i))
						toolStart := strings.Index(text[start:], "工具活动") + start
						if toolStart <= start || answerStart <= toolStart || strings.Count(text, "工具活动") != i {
							t.Fatalf("turn %d must show question, tools, then answer exactly once: %s", i, strings.TrimSpace(text))
						}
					}
					if test.streamed {
						// Repaint after completion and exercise the still-interactive drawer.
						update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
						update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
						draw()
						visible := strings.Join(strings.Split(screen.text(), "\n")[screen.top:screen.top+screen.height], "\n")
						if !strings.Contains(visible, "last paragraph") || strings.Count(visible, "long paragraph") < 3 {
							t.Fatalf("final conclusion disappeared from visible terminal: %s", visible)
						}
					}
				}
				text := screen.text()
				for i := 1; i <= 3; i++ {
					if n := strings.Count(text, fmt.Sprintf("question-%d", i)); n != 1 {
						t.Fatalf("question-%d appears %d times in terminal:\n%s", i, n, strings.TrimSpace(text))
					}
				}
				for i := 1; i <= 3; i++ {
					if n := strings.Count(text, fmt.Sprintf("answer-%d", i)); n != 1 {
						t.Fatalf("answer-%d appears %d times: %s", i, n, strings.TrimSpace(text))
					}
				}
			})
		}
	}
}

func TestLongReplyHistoryFinishesBeforeNextQuestion(t *testing.T) {
	model := toolDrawerModel(t, 0)
	model.sequence = 1
	model.active = &activeTurn{sequence: 1, cancel: func() {}}
	updated, first := model.Update(turnEvent{sequence: 1, kind: "done", result: TurnResult{Reply: strings.Repeat("answer paragraph\n\n", 60) + "answer end"}})
	model = updated.(Model)
	model.input.setValue("next question")
	updated, next := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(Model)
	if first == nil || next != nil || model.historyPrints != 1 || len(model.historyQueue) != 1 {
		t.Fatal("next question started printing before the answer finished")
	}
	var printed []string
	var collect func(tea.Cmd)
	collect = func(command tea.Cmd) {
		if command == nil {
			return
		}
		message := command()
		if _, ok := message.(tea.BatchMsg); ok {
			return // Do not launch backend I/O or spinner timers.
		}
		value := reflect.ValueOf(message)
		commands := reflect.TypeOf([]tea.Cmd{})
		if value.IsValid() && value.Type().ConvertibleTo(commands) {
			for _, child := range value.Convert(commands).Interface().([]tea.Cmd) {
				collect(child)
			}
		} else if reflect.TypeOf(message) == reflect.TypeOf(tea.Println("")()) {
			line := value.FieldByName("messageBody").String()
			if strings.Contains(line, "\n") || ansi.StringWidth(line) > model.contentWidth() {
				t.Fatal("history insertion exceeds one physical row")
			}
			printed = append(printed, line)
		}
	}
	collect(first)
	if text := strings.Join(printed, "\n"); !strings.Contains(text, "answer paragraph") || strings.Contains(text, "answer end") || strings.Contains(text, "next question") {
		t.Fatal("answer history was interrupted or mixed with the next question")
	}
	updated, next = model.Update(historyPrintedMsg{})
	model = updated.(Model)
	collect(next)
	text := strings.Join(printed, "\n") + "\n" + model.View().Content
	if strings.Count(text, "answer paragraph") != 60 || strings.Count(text, "next question") != 1 || strings.Index(text, "answer end") > strings.Index(text, "next question") {
		t.Fatal("history lost, duplicated or reordered the answer and next question")
	}
	updated, next = model.Update(historyPrintedMsg{})
	model = updated.(Model)
	if next != nil || model.historyPrints != 0 || len(model.historyQueue) != 0 {
		t.Fatal("history output did not finish")
	}
}
