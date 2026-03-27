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
	appruntime "bqagent/internal/runtime"
	"bqagent/internal/session"
	"bqagent/internal/workspace"
)

type cliOptions struct {
	plan        bool
	background  bool
	chat        bool
	server      bool
	stream      bool
	ilinkLogin  bool
	ilinkStatus bool
	listen      string
	serverURL   string
	resumeID    string
	sessionID   string
	sessionRun  bool
	serverRun   bool
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

	if options.ilinkLogin {
		return runIlinkLogin(ctx, stdout, stderr, options)
	}
	if options.ilinkStatus {
		return runIlinkStatus(ctx, stdout, stderr, options)
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
	getenv = appruntime.MergeEnv(getenv, appruntime.LoadDotEnv(ws.Root))

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
	if strings.TrimSpace(task) == "" && !options.plan && !options.background && !options.chat && !options.server && !options.serverRun && effectiveSessionID(options) == "" {
		task = "Hello"
	}

	if options.background {
		if options.server {
			return runServerInBackground(stdout, stderr, deps, ws, options)
		}
		return runInBackground(stdout, stderr, deps, ws, options, taskArgs)
	}

	if options.server || options.serverRun {
		return runServer(ctx, stdout, stderr, getenv, ws, systemPrompt, options)
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
	fs.BoolVar(&options.server, "server", false, "run a long-lived HTTP server")
	fs.BoolVar(&options.stream, "stream", false, "stream responses token by token (requires --chat)")
	fs.BoolVar(&options.ilinkLogin, "ilink-login", false, "trigger the running server's iLink login flow")
	fs.BoolVar(&options.ilinkStatus, "ilink-status", false, "fetch the running server's iLink login status")
	fs.StringVar(&options.listen, "listen", "0.0.0.0:8080", "HTTP listen address for server mode")
	fs.StringVar(&options.serverURL, "server-url", "http://127.0.0.1:8080", "base URL of a running server for client-style commands")
	fs.StringVar(&options.resumeID, "resume", "", "resume an existing session")
	fs.StringVar(&options.sessionID, "session-id", "", "internal session identifier")
	fs.BoolVar(&options.sessionRun, "session-run", false, "internal background session runner")
	fs.BoolVar(&options.serverRun, "server-run", false, "internal background server runner")

	if err := fs.Parse(args); err != nil {
		return cliOptions{}, nil, err
	}
	if options.resumeID != "" && options.sessionID != "" {
		return cliOptions{}, nil, fmt.Errorf("--resume and --session-id cannot be used together")
	}
	if options.background && options.sessionRun {
		return cliOptions{}, nil, fmt.Errorf("--background cannot be combined with --session-run")
	}
	if options.background && options.serverRun {
		return cliOptions{}, nil, fmt.Errorf("--background cannot be combined with --server-run")
	}
	if options.chat && options.background {
		return cliOptions{}, nil, fmt.Errorf("--chat cannot be combined with --background")
	}
	if options.server && options.chat {
		return cliOptions{}, nil, fmt.Errorf("--server cannot be combined with --chat")
	}
	if options.server && effectiveSessionID(options) != "" {
		return cliOptions{}, nil, fmt.Errorf("--server cannot be combined with --resume or --session-id")
	}
	if options.server && options.stream {
		return cliOptions{}, nil, fmt.Errorf("--server cannot be combined with --stream")
	}
	if options.stream && !options.chat {
		return cliOptions{}, nil, fmt.Errorf("--stream requires --chat")
	}
	if options.ilinkLogin && options.ilinkStatus {
		return cliOptions{}, nil, fmt.Errorf("--ilink-login cannot be combined with --ilink-status")
	}
	if (options.ilinkLogin || options.ilinkStatus) && (options.plan || options.background || options.chat || options.server || options.stream || options.sessionRun || options.serverRun || effectiveSessionID(options) != "") {
		return cliOptions{}, nil, fmt.Errorf("--ilink-login and --ilink-status cannot be combined with execution or server flags")
	}
	if options.ilinkLogin && len(fs.Args()) > 0 {
		return cliOptions{}, nil, fmt.Errorf("--ilink-login does not accept a task")
	}
	if options.ilinkStatus && len(fs.Args()) > 0 {
		return cliOptions{}, nil, fmt.Errorf("--ilink-status does not accept a task")
	}
	if (options.server || options.serverRun) && len(fs.Args()) > 0 {
		return cliOptions{}, nil, fmt.Errorf("server mode does not accept a task")
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
		conversation *appruntime.Conversation
		outputWriter io.Writer = stdout
		errorWriter  io.Writer = stderr
		logFile      *os.File
		err          error
	)

	sessionID := effectiveSessionID(options)
	if sessionID != "" {
		conversation, err = appruntime.PrepareConversation(session.NewStore(ws.Root), sessionID, nil, systemPrompt)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		defer func() {
			if logFile != nil {
				_ = logFile.Close()
			}
		}()

		if !options.sessionRun {
			logFile, err = conversation.Session.OpenOutputFile()
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			outputWriter = io.MultiWriter(stdout, logFile)
			errorWriter = io.MultiWriter(stderr, logFile)
		}
	} else {
		conversation, err = appruntime.PrepareConversation(nil, "", nil, systemPrompt)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}

	runtime := appruntime.Factory{
		Config:        appruntime.ConfigFromEnv(getenv),
		WorkspaceRoot: ws.Root,
		MemoryDir:     ws.WorkspaceMemoryDir(),
	}.Build(true, false)
	app := runtime.NewAgent(outputWriter, systemPrompt, conversation.Recorder(), false)

	if strings.TrimSpace(task) != "" && !options.plan {
		if err := conversation.AddUserMessage(task); err != nil {
			fmt.Fprintln(errorWriter, err)
			return 1
		}
	}

	var result string
	if options.plan {
		result, err = app.RunPlannedConversation(ctx, conversation.Messages, task, agent.DefaultMaxIterations)
	} else {
		result, err = app.RunConversation(ctx, conversation.Messages, agent.DefaultMaxIterations)
	}
	if err != nil {
		_ = conversation.MarkFailed(err)
		fmt.Fprintln(errorWriter, err)
		return 1
	}

	_ = conversation.MarkCompleted()
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
	var err error

	sessionID := effectiveSessionID(options)
	createOptions := &session.CreateOptions{Task: initialTask, Planned: options.plan, Chat: true}
	conversation, err := appruntime.PrepareConversation(store, sessionID, createOptions, systemPrompt)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	// Open output log file so chat sessions are persisted like foreground runs.
	logFile, err := conversation.Session.OpenOutputFile()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer logFile.Close()
	outputWriter := io.MultiWriter(stdout, logFile)
	errorWriter := io.MultiWriter(stderr, logFile)

	runtime := appruntime.Factory{
		Config:        appruntime.ConfigFromEnv(getenv),
		WorkspaceRoot: ws.Root,
		MemoryDir:     ws.WorkspaceMemoryDir(),
	}.Build(options.plan, false)
	app := runtime.NewAgent(outputWriter, systemPrompt, conversation.Recorder(), options.stream)

	streamMode := options.stream
	memoryEnabled := ws.MemoryEnabled()

	executeTurn := func(input string) ([]map[string]any, error) {
		if err := conversation.AddUserMessage(input); err != nil {
			return conversation.Messages, err
		}

		result, updatedMessages, runErr := app.RunConversationTurn(ctx, conversation.Messages, agent.DefaultMaxIterations)
		if runErr != nil {
			return updatedMessages, runErr
		}
		conversation.Messages = updatedMessages
		if streamMode {
			// content already printed chunk by chunk; just add trailing newline
			fmt.Fprint(outputWriter, "\n")
		} else {
			fmt.Fprintln(outputWriter, result)
		}

		if memoryEnabled && strings.TrimSpace(input) != "" {
			if memErr := ws.AppendMemory(input, result); memErr != nil {
				fmt.Fprintln(errorWriter, memErr)
			}
		}

		return conversation.Messages, nil
	}

	if strings.TrimSpace(initialTask) != "" {
		_, err = executeTurn(initialTask)
		if err != nil {
			_ = conversation.MarkFailed(err)
			fmt.Fprintln(errorWriter, err)
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

		_, err = executeTurn(line)
		if err != nil {
			_ = conversation.MarkFailed(err)
			fmt.Fprintln(errorWriter, err)
			return 1
		}
	}

	if scanErr := scanner.Err(); scanErr != nil {
		fmt.Fprintln(errorWriter, scanErr)
		_ = conversation.MarkFailed(scanErr)
		return 1
	}

	_ = conversation.MarkCompleted()
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
