package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"bqagent/internal/agent"
)

func main() {
	os.Exit(run(context.Background(), os.Stdout, os.Stderr, os.Args[1:], os.Getenv))
}

func run(ctx context.Context, stdout, stderr io.Writer, args []string, getenv func(string) string) int {
	task := "Hello"
	if len(args) > 0 {
		task = strings.Join(args, " ")
	}

	client := agent.NewClient(
		getenv("OPENAI_API_KEY"),
		getenv("OPENAI_BASE_URL"),
		nil,
	)
	app := agent.New(client, getenv("OPENAI_MODEL"), stdout)

	result, err := app.Run(ctx, task, agent.DefaultMaxIterations)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	fmt.Fprintln(stdout, result)
	return 0
}
