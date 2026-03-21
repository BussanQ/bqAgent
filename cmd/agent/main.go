package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"bqagent/internal/agent"
	"bqagent/internal/session"
	"bqagent/internal/tools"
	"bqagent/internal/workspace"
)

type cliOptions struct {
	plan       bool
	background bool
	chat       bool
	resumeID   string
	sessionID  string
	sessionRun bool
}

type runDeps struct {
	getwd           func() (string, error)
	executable      func() (string, error)
	startBackground func(executable string, args []string, dir, outputPath string) error
}

func main() {
	os.Exit(run(context.Background(), os.Stdin, os.Stdout, os.Stderr, os.Args[1:], os.Getenv))
}

func run(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args []string, getenv func(string) string) int {
	return runWithDeps(ctx, stdin, stdout, stderr, args, getenv, runDeps{
		getwd:           os.Getwd,
		executable:      os.Executable,
		startBackground: startBackgroundProcess,
	})
}

func runWithDeps(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args []string, getenv func(string) string, deps runDeps) int {
	options, taskArgs, err := parseCLI(args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	cwd, err := deps.getwd()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	ws, err := workspace.Discover(cwd)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	if err := ws.EnsureDefaults(); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	systemPrompt, err := ws.BuildSystemPrompt(agent.DefaultSystemPrompt)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	task := strings.Join(taskArgs, " ")
	if strings.TrimSpace(task) == "" && !options.plan && !options.background && !options.chat && effectiveSessionID(options) == "" {
		task = "Hello"
	}

	if options.background {
		return runInBackground(stdout, stderr, deps, ws, options, taskArgs)
	}

	if options.chat {
		return runChat(ctx, stdin, stdout, stderr, getenv, ws, systemPrompt, task, options)
	}

	return runForeground(ctx, stdout, stderr, getenv, ws, systemPrompt, task, options)
}

func parseCLI(args []string) (cliOptions, []string, error) {
	fs := flag.NewFlagSet("bqagent", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var options cliOptions
	fs.BoolVar(&options.plan, "plan", false, "break the task into steps before execution")
	fs.BoolVar(&options.background, "background", false, "run the task in the background")
	fs.BoolVar(&options.chat, "chat", false, "interactive multi-turn conversation mode")
	fs.StringVar(&options.resumeID, "resume", "", "resume an existing session")
	fs.StringVar(&options.sessionID, "session-id", "", "internal session identifier")
	fs.BoolVar(&options.sessionRun, "session-run", false, "internal background session runner")

	if err := fs.Parse(args); err != nil {
		return cliOptions{}, nil, err
	}
	if options.resumeID != "" && options.sessionID != "" {
		return cliOptions{}, nil, fmt.Errorf("--resume and --session-id cannot be used together")
	}
	if options.background && options.sessionRun {
		return cliOptions{}, nil, fmt.Errorf("--background cannot be combined with --session-run")
	}
	if options.chat && options.background {
		return cliOptions{}, nil, fmt.Errorf("--chat cannot be combined with --background")
	}
	return options, fs.Args(), nil
}

func runInBackground(stdout, stderr io.Writer, deps runDeps, ws *workspace.Workspace, options cliOptions, taskArgs []string) int {
	task := strings.Join(taskArgs, " ")
	if strings.TrimSpace(task) == "" {
		fmt.Fprintln(stderr, "background mode requires a task")
		return 1
	}

	store := session.NewStore(ws.Root)
	sessionID := effectiveSessionID(options)
	var (
		savedSession *session.Session
		err          error
	)
	if sessionID == "" {
		savedSession, err = store.Create(session.CreateOptions{Task: task, Planned: options.plan, Background: true})
	} else {
		savedSession, err = store.Open(sessionID)
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	executable, err := deps.executable()
	if err != nil {
		_ = savedSession.MarkFailed(err)
		fmt.Fprintln(stderr, err)
		return 1
	}

	childArgs := []string{"--session-run", "--session-id", savedSession.ID()}
	if options.plan {
		childArgs = append(childArgs, "--plan")
	}
	childArgs = append(childArgs, taskArgs...)

	if err := deps.startBackground(executable, childArgs, ws.Root, savedSession.OutputPath()); err != nil {
		_ = savedSession.MarkFailed(err)
		fmt.Fprintln(stderr, err)
		return 1
	}

	fmt.Fprintf(stdout, "session_id: %s\nsession_dir: %s\noutput_log: %s\n", savedSession.ID(), savedSession.Dir(), savedSession.OutputPath())
	return 0
}

func runForeground(ctx context.Context, stdout, stderr io.Writer, getenv func(string) string, ws *workspace.Workspace, systemPrompt, task string, options cliOptions) int {
	if effectiveSessionID(options) != "" && strings.TrimSpace(task) == "" {
		fmt.Fprintln(stderr, "resume requires a follow-up task")
		return 1
	}
	if options.plan && strings.TrimSpace(task) == "" {
		fmt.Fprintln(stderr, "plan mode requires a task")
		return 1
	}

	var (
		savedSession *session.Session
		messages     []map[string]any
		outputWriter io.Writer = stdout
		errorWriter  io.Writer = stderr
		logFile      *os.File
		err          error
	)

	sessionID := effectiveSessionID(options)
	if sessionID != "" {
		store := session.NewStore(ws.Root)
		savedSession, err = store.Open(sessionID)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if err := savedSession.MarkRunning(); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		defer func() {
			if logFile != nil {
				_ = logFile.Close()
			}
		}()

		if !options.sessionRun {
			logFile, err = savedSession.OpenOutputFile()
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			outputWriter = io.MultiWriter(stdout, logFile)
			errorWriter = io.MultiWriter(stderr, logFile)
		}

		messages, err = savedSession.LoadMessages()
		if err != nil {
			fmt.Fprintln(errorWriter, err)
			return 1
		}
	}

	if len(messages) == 0 {
		systemMessage := map[string]any{"role": "system", "content": systemPrompt}
		messages = append(messages, systemMessage)
		if savedSession != nil {
			if err := savedSession.RecordMessage(systemMessage); err != nil {
				fmt.Fprintln(errorWriter, err)
				return 1
			}
		}
	}

	chatClient := agent.NewClient(getenv("OPENAI_API_KEY"), getenv("OPENAI_BASE_URL"), nil)
	planner := agent.NewPlanner(chatClient, getenv("OPENAI_MODEL"))
	catalog := tools.NewCatalog(tools.Options{WorkspaceRoot: ws.Root, IncludePlan: true, SearchAPIKey: getenv("SEARCH_API_KEY"), SearchBaseURL: getenv("SEARCH_BASE_URL"), MemoryDir: ws.WorkspaceMemoryDir()})
	var recorder agent.MessageRecorder
	if savedSession != nil {
		recorder = savedSession
	}
	app := agent.NewWithOptions(chatClient, getenv("OPENAI_MODEL"), agent.Options{
		SystemPrompt:    systemPrompt,
		LogWriter:       outputWriter,
		ToolDefinitions: catalog.Definitions(),
		Functions:       catalog.Registry(),
		Planner:         planner,
		Recorder:        recorder,
	})

	if strings.TrimSpace(task) != "" && !options.plan {
		userMessage := map[string]any{"role": "user", "content": task}
		messages = append(messages, userMessage)
		if savedSession != nil {
			if err := savedSession.RecordMessage(userMessage); err != nil {
				fmt.Fprintln(errorWriter, err)
				return 1
			}
		}
	}

	var result string
	if options.plan {
		result, err = app.RunPlannedConversation(ctx, messages, task, agent.DefaultMaxIterations)
	} else {
		result, err = app.RunConversation(ctx, messages, agent.DefaultMaxIterations)
	}
	if err != nil {
		if savedSession != nil {
			_ = savedSession.MarkFailed(err)
		}
		fmt.Fprintln(errorWriter, err)
		return 1
	}

	if savedSession != nil {
		_ = savedSession.MarkCompleted()
	}
	if ws.MemoryEnabled() && strings.TrimSpace(task) != "" {
		if memoryErr := ws.AppendMemory(task, result); memoryErr != nil {
			fmt.Fprintln(errorWriter, memoryErr)
			return 1
		}
	}

	fmt.Fprintln(outputWriter, result)
	return 0
}

func runChat(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string, ws *workspace.Workspace, systemPrompt, initialTask string, options cliOptions) int {
	store := session.NewStore(ws.Root)
	var (
		savedSession *session.Session
		messages     []map[string]any
		err          error
	)

	sessionID := effectiveSessionID(options)
	if sessionID != "" {
		savedSession, err = store.Open(sessionID)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		messages, err = savedSession.LoadMessages()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	} else {
		savedSession, err = store.Create(session.CreateOptions{Task: initialTask, Planned: options.plan, Chat: true})
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}

	if err := savedSession.MarkRunning(); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	if len(messages) == 0 {
		systemMessage := map[string]any{"role": "system", "content": systemPrompt}
		messages = append(messages, systemMessage)
		if err := savedSession.RecordMessage(systemMessage); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}

	chatClient := agent.NewClient(getenv("OPENAI_API_KEY"), getenv("OPENAI_BASE_URL"), nil)
	planner := agent.NewPlanner(chatClient, getenv("OPENAI_MODEL"))
	catalog := tools.NewCatalog(tools.Options{WorkspaceRoot: ws.Root, IncludePlan: options.plan, SearchAPIKey: getenv("SEARCH_API_KEY"), SearchBaseURL: getenv("SEARCH_BASE_URL"), MemoryDir: ws.WorkspaceMemoryDir()})
	app := agent.NewWithOptions(chatClient, getenv("OPENAI_MODEL"), agent.Options{
		SystemPrompt:    systemPrompt,
		LogWriter:       stdout,
		ToolDefinitions: catalog.Definitions(),
		Functions:       catalog.Registry(),
		Planner:         planner,
		Recorder:        savedSession,
	})

	executeTurn := func(input string) ([]map[string]any, error) {
		userMessage := map[string]any{"role": "user", "content": input}
		messages = append(messages, userMessage)
		if err := savedSession.RecordMessage(userMessage); err != nil {
			return messages, err
		}

		result, updatedMessages, err := app.RunConversationTurn(ctx, messages, agent.DefaultMaxIterations)
		if err != nil {
			return updatedMessages, err
		}
		messages = updatedMessages
		fmt.Fprintln(stdout, result)
		return messages, nil
	}

	if strings.TrimSpace(initialTask) != "" {
		var err error
		messages, err = executeTurn(initialTask)
		if err != nil {
			_ = savedSession.MarkFailed(err)
			fmt.Fprintln(stderr, err)
			return 1
		}
	}

	scanner := bufio.NewScanner(stdin)
	for {
		fmt.Fprint(stdout, "> ")
		if !scanner.Scan() {
			break
		}
		line := scanner.Text()
		if line == "/exit" {
			break
		}
		if strings.TrimSpace(line) == "" {
			continue
		}

		var err error
		messages, err = executeTurn(line)
		if err != nil {
			_ = savedSession.MarkFailed(err)
			fmt.Fprintln(stderr, err)
			return 1
		}
	}

	if scanErr := scanner.Err(); scanErr != nil {
		fmt.Fprintln(stderr, scanErr)
		_ = savedSession.MarkFailed(scanErr)
		return 1
	}

	_ = savedSession.MarkCompleted()
	return 0
}

func effectiveSessionID(options cliOptions) string {
	if options.sessionID != "" {
		return options.sessionID
	}
	return options.resumeID
}

func startBackgroundProcess(executable string, args []string, dir, outputPath string) error {
	logFile, err := os.OpenFile(outputPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer logFile.Close()

	devNull, err := os.Open(os.DevNull)
	if err != nil {
		return err
	}
	defer devNull.Close()

	command := exec.Command(executable, args...)
	command.Dir = dir
	command.Stdout = logFile
	command.Stderr = logFile
	command.Stdin = devNull
	if err := command.Start(); err != nil {
		return err
	}
	return command.Process.Release()
}
