package tui

import (
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
)

type terminalWrites struct{ writes []string }

func (sink *terminalWrites) Write(p []byte) (int, error) {
	sink.writes = append(sink.writes, string(p))
	return len(p), nil
}

func TestHistoryOutputWaitsForRenderedFrame(t *testing.T) {
	sink := &terminalWrites{}
	output := newFrameOutput(sink)
	output.begin()
	for i := 0; i < 100; i++ {
		_, _ = output.Write([]byte("\r\n" + ansi.CursorUp(23) + ansi.InsertLine(1) + "history\r\n"))
		_, _ = output.Write([]byte(ansi.SetModeSynchronizedOutput + "old-frame" + ansi.ResetModeSynchronizedOutput))
	}
	fence := output.arm()
	if len(sink.writes) != 0 {
		t.Fatal("history acknowledgement exposed intermediate terminal scrolling")
	}
	_, _ = output.Write([]byte("another-old-frame"))
	if len(sink.writes) != 0 {
		t.Fatal("unrelated renderer output prematurely committed history")
	}
	_, err := output.Write([]byte(ansi.SetModeSynchronizedOutput + ansi.SetWindowTitle(fence) + "final-frame" + ansi.ResetModeSynchronizedOutput))
	if err != nil || len(sink.writes) != 1 {
		t.Fatalf("history and final frame must be one write: writes=%d err=%v", len(sink.writes), err)
	}
	text := sink.writes[0]
	if strings.Count(text, "history") != 100 || !strings.Contains(text, "final-frame") || strings.Contains(text, "bqagent-frame:") {
		t.Fatal("atomic output lost history, missed the final frame or leaked its private fence")
	}
	if strings.Count(text, ansi.SetModeSynchronizedOutput) != 1 || strings.Count(text, ansi.ResetModeSynchronizedOutput) != 1 || !strings.HasSuffix(text, "final-frame"+ansi.ResetModeSynchronizedOutput) {
		t.Fatal("nested renderer updates ended the atomic transaction early")
	}
}

func TestHistoryOutputIgnoresSupersededFrame(t *testing.T) {
	sink := &terminalWrites{}
	output := newFrameOutput(sink)
	output.begin()
	_, _ = output.Write([]byte("first-history"))
	old := output.arm()
	output.begin() // More history arrives before the previous final frame flushes.
	_, _ = output.Write([]byte(ansi.SetWindowTitle(old) + "stale-frame"))
	if len(sink.writes) != 0 {
		t.Fatal("old frame fence committed an unfinished history transaction")
	}
	_, _ = output.Write([]byte("second-history"))
	current := output.arm()
	_, _ = output.Write([]byte(ansi.SetWindowTitle(current) + "current-frame"))
	if len(sink.writes) != 1 || !strings.Contains(sink.writes[0], "first-history") || !strings.Contains(sink.writes[0], "second-history") || strings.Contains(sink.writes[0], "bqagent-frame:") {
		t.Fatal("overlapping history transactions lost or exposed partial output")
	}
}

func TestInlineHistoryArmsFenceOnlyAfterLastBatch(t *testing.T) {
	base := toolDrawerModel(t, 0)
	base.sequence = 1
	base.active = &activeTurn{sequence: 1, cancel: func() {}}
	sink := &terminalWrites{}
	output := newFrameOutput(sink)
	wrapper := newInlineModel(base)
	wrapper.output = output
	var model tea.Model = wrapper
	model, _ = model.Update(turnEvent{sequence: 1, kind: "done", result: TurnResult{Reply: strings.Repeat("answer\n\n", 50) + "end"}})
	_, _ = output.Write([]byte("history-insertion"))
	if !output.active || len(sink.writes) != 0 {
		t.Fatal("wrapper failed to buffer native history before its command runs")
	}
	inner := model.(inlineModel)
	state := inner.model.(Model)
	state.historyQueue = append(state.historyQueue, historyPrint{text: "another batch"})
	inner.model = state
	model = inner
	model, _ = model.Update(historyPrintedMsg{})
	if model.View().WindowTitle != "" || !output.active {
		t.Fatal("wrapper armed a fence before the final queued batch")
	}
	model, _ = model.Update(historyPrintedMsg{})
	view := model.View()
	if view.WindowTitle == "" || !strings.Contains(view.Content, "end") || len(sink.writes) != 0 {
		t.Fatal("history acknowledgement must publish the final frame without flushing output yet")
	}
	_, _ = output.Write([]byte(ansi.SetWindowTitle(view.WindowTitle) + view.Content))
	if len(sink.writes) != 1 || !strings.Contains(sink.writes[0], "end") {
		t.Fatal("rendered final frame did not commit buffered history")
	}
}

func TestHistoryOutputWaitsForPendingRepaint(t *testing.T) {
	base := toolDrawerModel(t, 0)
	sink := &terminalWrites{}
	output := newFrameOutput(sink)
	wrapper := newInlineModel(base)
	wrapper.output = output
	var model tea.Model = wrapper
	model, _ = model.Update(tea.WindowSizeMsg{Width: 72, Height: 12})
	if !model.(inlineModel).pending {
		t.Fatal("resize must set up a pending shorter frame")
	}
	inner := model.(inlineModel)
	state := inner.model.(Model)
	state.enqueuePrint(strings.Repeat("history\n", 30))
	inner.model = state
	model, _ = inner.Update(nil)
	_, _ = output.Write([]byte("inserted-history"))
	model, _ = model.Update(historyPrintedMsg{})
	if model.View().WindowTitle != "" || !model.(inlineModel).pending {
		t.Fatal("history acknowledgement armed a fence on the stale pre-resize frame")
	}
	model, _ = model.Update(inlineRepaintMsg{})
	view := model.View()
	if view.WindowTitle == "" || strings.Count(view.Content, "\n")+1 != 11 || len(sink.writes) != 0 {
		t.Fatal("pending repaint did not arm the final resized frame")
	}
	_, _ = output.Write([]byte(ansi.SetWindowTitle(view.WindowTitle) + view.Content))
	if len(sink.writes) != 1 {
		t.Fatal("resized frame did not atomically release history")
	}
}

func TestFrameFenceDoesNotChangeTerminalTitle(t *testing.T) {
	sink := &terminalWrites{}
	output := newFrameOutput(sink)
	output.begin()
	fence := output.arm()
	_, _ = output.Write([]byte(ansi.SetWindowTitle(fence) + "frame"))
	_, _ = output.Write([]byte(ansi.SetWindowTitle("")))
	if len(sink.writes) != 1 || strings.Contains(strings.Join(sink.writes, ""), "\x1b]2;") {
		t.Fatal("private frame title or shutdown reset escaped to the terminal")
	}
}

func TestFrameOutputPreservesTerminalAndShutdown(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "terminal")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	output := newFrameOutput(file)
	wrapped, ok := output.writer().(term.File)
	if !ok || wrapped.Fd() != file.Fd() {
		t.Fatal("output wrapping lost the terminal descriptor used for size and raw mode")
	}
	output.begin()
	_, _ = output.Write([]byte("pending-history"))
	if err := output.finish(); err != nil {
		t.Fatal(err)
	}
	if err := output.finish(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(file.Name())
	if err != nil || strings.Count(string(data), "pending-history") != 1 {
		t.Fatalf("shutdown lost or duplicated buffered output: %q, %v", data, err)
	}
	sink := &terminalWrites{}
	if _, ok := newFrameOutput(sink).writer().(term.File); ok {
		t.Fatal("ordinary writer must not be presented as a terminal")
	}
}

type shortTerminalWriter struct{}

func (shortTerminalWriter) Write(p []byte) (int, error) { return len(p) / 2, nil }

func TestFrameOutputReportsShortWrites(t *testing.T) {
	output := newFrameOutput(shortTerminalWriter{})
	if _, err := output.Write([]byte("ordinary-output")); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("ordinary write error = %v", err)
	}
	if !errors.Is(output.finish(), io.ErrShortWrite) {
		t.Fatal("shutdown discarded an earlier output failure")
	}
	output = newFrameOutput(shortTerminalWriter{})
	output.begin()
	_, _ = output.Write([]byte("history"))
	fence := output.arm()
	if _, err := output.Write([]byte(ansi.SetWindowTitle(fence) + "frame")); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("atomic write error = %v", err)
	}
	if !errors.Is(output.finish(), io.ErrShortWrite) {
		t.Fatal("shutdown discarded an earlier atomic output failure")
	}
}
