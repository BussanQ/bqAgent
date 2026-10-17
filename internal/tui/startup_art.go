package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// Keep the mascot compact on large terminals instead of stretching its face
// and body to fill the available space.
func startupRobot(width, height int) []string {
	if width < 34 || height < 18 {
		return nil
	}
	width = min(44, max(32, (width*3+7)/8))
	height = min(20, max(16, (height*3+7)/8))
	center := func(text string, size int) string {
		left := (size - len(text)) / 2
		return strings.Repeat(" ", left) + text + strings.Repeat(" ", size-len(text)-left)
	}
	head := width - 10
	chest := head - 6
	face := func(text string) string { return "| " + center(text, head-2) + " |" }
	body := func(text string) string { return "| |" + center(text, chest) + "| |" }
	art := []string{
		".-o-.",
		"|",
		"." + strings.Repeat("-", head) + ".",
		"/" + strings.Repeat(" ", head) + "\\",
		face("[o]    [o]"),
	}
	for i := 0; i < (height-16)/2; i++ {
		art = append(art, face(""))
	}
	art = append(art,
		face("`----'"),
		"'"+strings.Repeat("-", head)+"'",
		"|____|",
		".-"+strings.Repeat("-", chest)+"-.",
		body(">_"),
		body("[=======]"),
	)
	for i := 0; i < height-16-(height-16)/2; i++ {
		art = append(art, body(""))
	}
	art = append(art,
		"'-|"+strings.Repeat("_", chest)+"|-'",
		"|__|    |__|",
		"/____\\  /____\\",
		"",
		"bqAgent",
	)
	for i, row := range art {
		art[i] = center(row, width)
	}
	return art
}

func (model Model) drawStartupArt(space []string) {
	art := startupRobot(model.contentWidth(), len(space))
	if len(art) == 0 {
		return
	}
	shell, glow, detail := lipgloss.NewStyle(), lipgloss.NewStyle(), lipgloss.NewStyle()
	if !model.config.NoColor {
		shell = shell.Foreground(lipgloss.Color("#B59BE8"))
		glow = glow.Foreground(lipgloss.Color("#7DDDD3")).Bold(true)
		detail = detail.Foreground(lipgloss.Color("#77668F"))
	}
	top := (len(space) - len(art)) / 2
	left := strings.Repeat(" ", (model.contentWidth()-len(art[0]))/2)
	for i, row := range art {
		row = strings.TrimRight(row, " ")
		if row == "" {
			continue
		}
		switch {
		case strings.Contains(row, "bqAgent"):
			row = model.styles.accent.Render(row)
		case strings.Contains(row, "[o]"):
			parts := strings.Split(row, "[o]")
			row = shell.Render(parts[0]) + glow.Render("[o]") + shell.Render(parts[1]) + glow.Render("[o]") + shell.Render(parts[2])
		case strings.Contains(row, ">_"):
			parts := strings.SplitN(row, ">_", 2)
			row = shell.Render(parts[0]) + model.styles.accent.Render(">_") + shell.Render(parts[1])
		case strings.ContainsAny(row, "=_"):
			row = detail.Render(row)
		default:
			row = shell.Render(row)
		}
		space[top+i] = left + row
	}
}
