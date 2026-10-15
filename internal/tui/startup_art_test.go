package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestStartupRobotFillsHalfAvailableSpace(t *testing.T) {
	for _, size := range [][2]int{{34, 18}, {70, 17}, {70, 18}, {98, 53}, {158, 73}, {238, 93}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			art := startupRobot(size[0], size[1])
			if size[1] < 18 {
				if len(art) != 0 {
					t.Fatal("short welcome area should hide the robot")
				}
				return
			}
			left, right := size[0], 0
			for _, row := range art {
				left = min(left, len(row)-len(strings.TrimLeft(row, " ")))
				right = max(right, len(strings.TrimRight(row, " ")))
				if len(row) > size[0]-2 {
					t.Fatal("robot exceeds available width")
				}
			}
			if (right-left)*len(art)*2 < size[0]*size[1] {
				t.Fatalf("robot bounds %dx%d occupy less than half of %v", right-left, len(art), size)
			}
			if len(art) > size[1]-2 {
				t.Fatal("robot must leave vertical breathing room")
			}
			if size == [2]int{98, 53} {
				t.Log("\n" + strings.Join(art, "\n"))
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
