package tui

import "strings"

// Use three eighths of each available dimension, retaining enough room for
// the face and chest details in smaller terminals.
func startupRobot(width, height int) []string {
	if width < 34 || height < 18 {
		return nil
	}
	width = min(width-2, max(32, (width*3+7)/8))
	height = min(height-2, max(16, (height*3+7)/8))
	center := func(text string, size int) string {
		left := (size - len(text)) / 2
		return strings.Repeat(" ", left) + text + strings.Repeat(" ", size-len(text)-left)
	}
	head := width - 6
	chest := width * 2 / 3
	eye := strings.Repeat("=", max(3, head/6))
	face := func(text string) string { return "[|" + center(text, head) + "|]" }
	body := func(text string) string { return " /|" + center(text, chest) + "|\\ " }
	art := []string{
		".---.",
		"'--+--'",
		"|",
		"." + strings.Repeat("-", head) + ".",
	}
	facePadding := (height - 16) / 2
	for i := 0; i < facePadding/2; i++ {
		art = append(art, face(""))
	}
	art = append(art, face("["+eye+"]    ["+eye+"]"))
	for i := 0; i < facePadding-facePadding/2; i++ {
		art = append(art, face(""))
	}
	art = append(art,
		face("'---v---'"),
		"'"+strings.Repeat("-", head)+"'",
		"|___|",
		"."+strings.Repeat("-", chest)+".",
		body("bqAgent"),
	)
	bodyPadding := height - 16 - facePadding
	for i := 0; i < bodyPadding; i++ {
		art = append(art, body(""))
	}
	art = append(art,
		body("[ + ]     [:::]"),
		"(_|"+strings.Repeat("_", chest)+"|_)",
		"|  |"+strings.Repeat(" ", chest/2)+"|  |",
		"|__|"+strings.Repeat(" ", chest/2)+"|__|",
		"/____\\"+strings.Repeat(" ", chest/2-2)+"/____\\",
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
	top := (len(space) - len(art)) / 2
	left := strings.Repeat(" ", (model.contentWidth()-len(art[0]))/2)
	for i, row := range art {
		style := model.styles.user
		if strings.Contains(row, "=") || strings.Contains(row, "bqAgent") {
			style = model.styles.accent
		}
		space[top+i] = style.Render(left + strings.TrimRight(row, " "))
	}
}
