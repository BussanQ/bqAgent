package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestStartupRobotUsesHalfPreviousDimensions(t *testing.T) {
	for _, size := range []struct {
		width, height         int
		wantWidth, wantHeight int
	}{
		{34, 18, 32, 16},
		{70, 17, 0, 0},
		{70, 18, 32, 16},
		{98, 53, 37, 20},
		{158, 73, 60, 28},
		{238, 93, 90, 35},
	} {
		t.Run(fmt.Sprintf("%dx%d", size.width, size.height), func(t *testing.T) {
			art := startupRobot(size.width, size.height)
			if len(art) != size.wantHeight {
				t.Fatalf("robot height = %d, want %d", len(art), size.wantHeight)
			}
			for _, row := range art {
				if len(row) != size.wantWidth {
					t.Fatalf("robot width = %d, want %d", len(row), size.wantWidth)
				}
				if len(row) > size.width-2 {
					t.Fatal("robot exceeds available width")
				}
			}
			if len(art) > size.height-2 {
				t.Fatal("robot must leave vertical breathing room")
			}
		})
	}
}

func TestStartupRobotRespectsNoColor(t *testing.T) {
	for _, noColor := range []bool{false, true} {
		model := NewModel(&fakeBackend{}, Config{Workspace: t.TempDir(), AgentDir: t.TempDir(), NoColor: noColor})
		model.width = 100
		space := make([]string, 53)
		model.drawStartupArt(space)
		plain := strings.Join(space, "\n")
		if noColor && ansi.Strip(plain) != plain {
			t.Fatal("NO_COLOR robot contains escape codes")
		}
		for _, row := range space {
			if ansi.StringWidth(row) > model.contentWidth() {
				t.Fatal("styled robot wraps outside the frame")
			}
		}
	}
}
