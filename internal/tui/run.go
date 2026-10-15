package tui

import (
	"context"
	"fmt"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// Run starts the inline Bubble Tea conversation. Completed-turn tool inspection
// temporarily uses AltScreen to preserve the native transcript and its position.
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
	output := newFrameOutput(config.Output)
	model := newInlineModel(NewModel(backend, config))
	model.output = output
	program := tea.NewProgram(
		model,
		tea.WithContext(config.Context),
		tea.WithInput(config.Input),
		tea.WithOutput(output.writer()),
	)
	_, err := program.Run()
	flushErr := output.finish()
	if err != nil {
		return err
	}
	return flushErr
}
