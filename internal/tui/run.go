package tui

import (
	"context"
	"fmt"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// Run starts the inline Bubble Tea program. It deliberately avoids AltScreen;
// mouse tracking is enabled only while an interactive collapsed tool drawer is
// visible, and is disabled again when that drawer is committed or cleared.
func Run(backend Backend, config Config) error {
	if backend == nil {
		return fmt.Errorf("tui backend is required")
	}
	if config.Input == nil {
		config.Input = os.Stdin
	}
	if config.Output == nil {
		config.Output = os.Stdout
	}
	if config.Context == nil {
		config.Context = context.Background()
	}
	config.NoColor = config.NoColor || strings.TrimSpace(os.Getenv("NO_COLOR")) != ""
	program := tea.NewProgram(
		NewModel(backend, config),
		tea.WithContext(config.Context),
		tea.WithInput(config.Input),
		tea.WithOutput(config.Output),
	)
	_, err := program.Run()
	return err
}
