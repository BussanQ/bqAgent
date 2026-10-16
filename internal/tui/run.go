package tui

import (
	"context"
	"fmt"
	"io"
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
	finalModel, err := program.Run()
	flushErr := output.finish()
	if err != nil {
		return err
	}
	if flushErr != nil {
		return flushErr
	}
	return printResumeCommand(config.Output, finalModel)
}

func printResumeCommand(output io.Writer, finalModel tea.Model) error {
	inline, ok := finalModel.(inlineModel)
	if !ok {
		return nil
	}
	model, ok := inline.model.(Model)
	if !ok || strings.TrimSpace(model.sessionID) == "" {
		return nil
	}
	command := model.styles.dim.Render("bqagent --resume " + model.sessionID)
	_, err := fmt.Fprintf(output, "\n%s\n", command)
	return err
}
