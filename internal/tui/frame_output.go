package tui

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
)

// frameOutput keeps native history insertions and the ensuing frame in one
// terminal write. Bubble Tea writes Println immediately, outside its normal
// synchronized frame flush. Its View has no after-render callback, so we use a
// private window-title sequence as a frame fence and remove it before output.
// The fence travels in the same write as the actual rendered frame; a command
// acknowledgement alone would race the renderer's asynchronous ticker.
type frameOutput struct {
	mu      sync.Mutex
	target  io.Writer
	pending bytes.Buffer
	active  bool
	serial  uint64
	fence   string
	err     error
}

func newFrameOutput(target io.Writer) *frameOutput {
	return &frameOutput{target: target}
}

// Preserve the complete terminal file interface used for raw mode, dimensions
// and color detection, without claiming that an ordinary writer is a TTY.
type frameOutputFile struct {
	term.File
	output *frameOutput
}

func (output frameOutputFile) Write(p []byte) (int, error) { return output.output.Write(p) }

func (output *frameOutput) writer() io.Writer {
	if file, ok := output.target.(term.File); ok {
		return frameOutputFile{File: file, output: output}
	}
	return output
}

func (output *frameOutput) begin() {
	output.mu.Lock()
	defer output.mu.Unlock()
	output.active = true
	output.fence = ""
}

func (output *frameOutput) arm() string {
	output.mu.Lock()
	defer output.mu.Unlock()
	output.serial++
	output.fence = fmt.Sprintf("bqagent-frame:%d", output.serial)
	return output.fence
}

func (output *frameOutput) Write(p []byte) (int, error) {
	output.mu.Lock()
	defer output.mu.Unlock()
	if output.err != nil {
		return 0, output.err
	}
	text := string(p)
	if output.serial > 0 {
		// The renderer resets its private title on close. The actual terminal
		// title was never changed, so do not erase it during shutdown.
		text = strings.ReplaceAll(text, ansi.SetWindowTitle(""), "")
	}
	ready := output.fence != "" && strings.Contains(text, ansi.SetWindowTitle(output.fence))
	// Each renderer write contains complete control sequences. Remove old as
	// well as current fences: a queued old frame can arrive after begin().
	const prefix = "\x1b]2;bqagent-frame:"
	for {
		start := strings.Index(text, prefix)
		if start < 0 {
			break
		}
		end := strings.IndexByte(text[start:], '\a')
		if end < 0 {
			break
		}
		text = text[:start] + text[start+end+1:]
	}
	if output.active {
		output.pending.WriteString(text)
		if !ready {
			return len(p), nil
		}
		if err := output.flushLocked(); err != nil {
			return 0, err
		}
		return len(p), nil
	}
	if text == "" {
		return len(p), nil
	}
	n, err := io.WriteString(output.target, text)
	if err == nil && n != len(text) {
		err = io.ErrShortWrite
	}
	if err != nil {
		output.err = err
		return 0, err
	}
	return len(p), nil
}

func (output *frameOutput) flushLocked() error {
	text := output.pending.String()
	// Inner renderer frames must not end the outer synchronized update early.
	text = strings.ReplaceAll(text, ansi.SetModeSynchronizedOutput, "")
	text = strings.ReplaceAll(text, ansi.ResetModeSynchronizedOutput, "")
	text = ansi.SetModeSynchronizedOutput + text + ansi.ResetModeSynchronizedOutput
	output.pending.Reset()
	output.active = false
	n, err := io.WriteString(output.target, text)
	if err == nil && n != len(text) {
		err = io.ErrShortWrite
	}
	output.err = err
	return err
}

// Flush on shutdown as well, including cancellation before the final frame.
func (output *frameOutput) finish() error {
	output.mu.Lock()
	defer output.mu.Unlock()
	if output.err != nil {
		return output.err
	}
	if !output.active {
		return nil
	}
	return output.flushLocked()
}
