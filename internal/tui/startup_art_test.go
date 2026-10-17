package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestStartupRobotKeepsCompactProportions(t *testing.T) {
	for _, size := range []struct {
		width, height         int
		wantWidth, wantHeight int
	}{
		{34, 18, 32, 16},
		{70, 17, 0, 0},
		{70, 18, 32, 16},
		{98, 53, 37, 20},
		{158, 73, 44, 20},
		{238, 93, 44, 20},
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
				for _, char := range row {
					if char < ' ' || char > '~' {
						t.Fatalf("non-ASCII character in mascot: %q", char)
					}
				}
				if len(row) > size.width-2 {
					t.Fatal("robot exceeds available width")
				}
			}
			if len(art) > 0 {
				text := strings.Join(art, "\n")
				for _, want := range []string{"[o]    [o]", ">_", "[=======]"} {
					if !strings.Contains(text, want) {
						t.Fatalf("mascot missing %q", want)
					}
				}
				if strings.Count(text, "bqAgent") != 1 {
					t.Fatal("mascot should have one brand label")
				}
			}
			if len(art) > size.height-2 {
				t.Fatal("robot must leave vertical breathing room")
			}
		})
	}
}

func TestStartupRobotRespectsNoColor(t *testing.T) {
	var colored string
	for _, noColor := range []bool{false, true} {
		model := NewModel(&fakeBackend{}, Config{Workspace: t.TempDir(), AgentDir: t.TempDir(), NoColor: noColor})
		model.width = 100
		space := make([]string, 53)
		model.drawStartupArt(space)
		plain := strings.Join(space, "\n")
		if noColor && ansi.Strip(plain) != plain {
			t.Fatal("NO_COLOR robot contains escape codes")
		}
		if !noColor {
			colored = plain
			if ansi.Strip(plain) == plain {
				t.Fatal("colored mascot lost all styling")
			}
		} else if ansi.Strip(colored) != plain {
			t.Fatal("color changed the mascot geometry")
		}
		for _, row := range space {
			if ansi.StringWidth(row) > model.contentWidth() {
				t.Fatal("styled robot wraps outside the frame")
			}
		}
	}
}
