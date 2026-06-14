# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`bqagent` is a small Go agent that drives an **OpenAI-compatible chat completions API** in a tool-calling loop: send messages → model picks tools → run tools locally → feed results back → repeat until done. The same core loop is reused across four entry modes — single-shot CLI, interactive `--chat`, background process, and a long-lived HTTP `--server` that fronts chat channels (ServerChan, QQ, WeChat/iLink). It can also delegate a turn to an external coding agent (Claude Code, Codex, Cursor, OpenCode) instead of running the loop itself.

Module `bqagent`, Go 1.26. Only two non-stdlib deps: `nhooyr.io/websocket` (QQ gateway) and `mdp/qrterminal` (WeChat login QR).

## Commands

```bash
make build          # go build -trimpath -ldflags '-s -w' -o bqagent ./cmd/agent
make build-amd      # CGO_ENABLED=0 GOOS=linux  GOARCH=amd64  (Linux artifact)
make build-windows  # CGO_ENABLED=0 GOOS=windows GOARCH=amd64
make test           # go test ./...
make clean

# Single package / single test
go test ./internal/agent
go test ./internal/agent -run TestRunConversation -v
```

There is no linter config beyond the Go toolchain; use `go vet ./...` and `gofmt`. `make build-amd` and a `publish` (upload to server) flow also exist as `.claude/skills`.

## Running

```bash
go run ./cmd/agent "task"             # single-shot (defaults to "Hello" with no args)
go run ./cmd/agent --chat             # interactive; /exit or Ctrl-D to quit
go run ./cmd/agent --plan "task"      # decompose into steps, then execute each
go run ./cmd/agent --background "task"# fork a child process, print session id
go run ./cmd/agent --server           # HTTP on 127.0.0.1:8080
go run ./cmd/agent --resume <id> "…"  # continue a persisted session
```

Config is read from the environment and from a `.env` file at the workspace root (auto-loaded; real shell env wins over `.env`). `OPENAI_API_KEY` is required for server mode. `OPENAI_MODEL` defaults to `agent.DefaultModel` (`MiniMax-M2.5`) when unset. **`.env` here contains live secrets — never print or commit its contents.**

## Architecture

The dependency direction is `cmd/agent → internal/{server,runtime} → internal/{agent,workspace,session,tools,extagent}`. Startup wiring (in [cmd/agent/main.go](cmd/agent/main.go) `runWithDeps`) is the fastest way to see how the pieces connect:

1. `workspace.Discover(cwd)` walks **up** until it finds `.agent`, `.git`, or `go.mod` — that directory becomes the **workspace root**, and *all* relative tool paths, `execute_bash`, and memory writes resolve from it.
2. `runtime.LoadDotEnv` + `MergeEnv` layer `.env` under the real environment.
3. `ws.EnsureDefaults()` scaffolds missing `.agent/*` context files from embedded `defaults/` (existing files are never overwritten).
4. `ws.BuildSystemPrompt(...)` assembles the system prompt (see below).
5. `runtime.Factory.Build()` constructs the `Client`, optional `Planner`, the tool `Catalog`, and the context-window config into a `Runtime`, which mints `*agent.Agent` instances.
6. Mode dispatch runs the loop foreground / in chat / in server / as a background child.

### The agent loop — `internal/agent`

[internal/agent/loop.go](internal/agent/loop.go) `runConversation` is the heart of the system. The iteration cap is a **runaway safety valve, not a task limit**: there is a single canonical constant `agent.DefaultMaxIterations = 1000`, used as both the in-loop fallback and the default for `runtime.MaxIterations` (env `AGENT_MAX_ITERATIONS`); all modes share it. Each iteration:

- builds the request payload via `buildRequestMessages` (context management, below),
- calls the model (streaming or not),
- appends the assistant message and records it,
- if there are no tool calls → returns final content,
- otherwise executes each tool call and appends a `tool` result message, then loops.

`plan` and `run_skill` are handled specially inside the loop (they recurse into a child `runConversation`); all other tools dispatch through the `functions` map. Unknown tools and malformed JSON args become tool error messages rather than aborting the run.

**Messages are untyped `[]map[string]any`** in OpenAI chat shape (`role`, `content`, `tool_calls`, `tool_call_id`). This convention runs end to end — session persistence, context pruning, and channel state all operate on these maps.

**Context management** (`buildRequestMessages`): it sanitizes completed tool scaffolding, prunes to a token budget, and — when over `SummaryTriggerTokens` and summarization is enabled (**on by default**) — summarizes older turns into a synthetic message and writes `context_checkpoint.json`. `buildRequestMessages` returns `(request, compacted)`: the disabled/under-budget/pruned paths return `compacted == nil` and leave the working set untouched (purely request-time), but when summarization fires it returns the compacted set and the loop **adopts it** as its new in-memory working history (`messages`/`updatedMessages`) so subsequent turns continue on the compacted context instead of re-summarizing the full history every turn — auto-compact-and-continue, like Claude Code. The synthetic summary lives only in the working set + checkpoint; it is never recorded, so the on-disk `messages.jsonl` stays complete. All tunable via `CONTEXT_*` env vars (see `runtime.ConfigFromEnv`); estimation is a crude `chars/4`.

### System prompt assembly — `internal/workspace`

`BuildSystemPrompt` concatenates, in order: base prompt → workspace section → `.agent/{AGENT,SOUL,TOOLS,USER}.md` → `.agent/rules/*.md` → a summary of `.agent/skills/*/SKILL.md` → memory tail. **Two memory layouts coexist:** the primary `.agent/memory/{MEMORY.md, YYYY-MM-DD.md}` and a legacy fallback (`workspace/...` and `agent_memory.md`) read only when the primary file is absent. Task/result pairs auto-append to today's daily memory file. Skills can be invoked by id or by alias (parsed from SKILL.md frontmatter).

### Sessions — `internal/session`

Each session is a directory under `.agent/sessions/<id>/` with `meta.json` (status: created/running/completed/failed), append-only `messages.jsonl` (the raw transcript), `context_checkpoint.json`, and `output.log`. `runtime.PrepareConversation` ties a session to an in-memory message slice, ensures the system message is current, and replays a checkpoint when one is compatible with the current system prompt.

### Server & channels — `internal/server`

`Service.HandleTurn` orchestrates one turn for any caller and is guarded by a **per-session keyed lock** so concurrent requests to the same session serialize. Channels implement the `Channel` interface (`Name/Enabled/RegisterRoutes/Start`) and are registered in [cmd/agent/server.go](cmd/agent/server.go); each is enabled by its own env vars. The **web UI channel** ([internal/server/webui.go](internal/server/webui.go)) is **on by default** (`WEBUI_ENABLED=false` to disable): it serves a self-contained `go:embed`-ed page at `/` and streams replies token-by-token over SSE from `/api/v1/webui/chat`. SSE token streaming flows through a dedicated `TokenSink io.Writer` (agent `Options`/`TurnOptions`) so live tokens stay separate from the log writer's `[Tool]`/timing noise; when `TokenSink` is nil the loop falls back to the old log-writer behavior. Async channels (QQ gateway, iLink poller, ServerChan bot webhook) share `ChannelTurnRunner`, which adds dedupe-by-key, busy/timeout/failure replies, and a progress writer on top of `HandleTurn`. The whole turn is bounded by `CHANNEL_TURN_TIMEOUT` (default 10m); individual LLM HTTP calls have their own client timeout.

### External agents — `internal/extagent`

The `Broker` can route a turn to an external coding agent instead of the built-in loop. A message starting with `/claude`, `/codex`, `/cursor`, or `/opencode` routes explicitly (and sticks to that agent for the session); `/default` switches back. Transports are **ACP** (JSON-RPC over the agent's stdio) or **CLI** (one-shot exec), auto-detected from `AGENT_<NAME>_ACP_CMD/ARGS` and `AGENT_<NAME>_CLI_CMD/ARGS`. `.agent/mcp.json` is a reserved path — live MCP transport is **not** implemented.

## Built-in tools — `internal/tools`

`execute_bash`, `read_file`, `write_file`, `web_search` (Tavily, with Firecrawl env vars as fallback), `web_fetch`, `install_skill`, `mem_save`/`mem_get`, plus `plan` and `run_skill` (added conditionally). Tools are assembled into a `Catalog` (definitions + a name→`Function` registry) so the CLI, chat, and server all expose the same set. To add a tool: add its `Definition` in `builtinDefinitions()` and its implementation to `RegistryWithOptions`.

## Conventions & gotchas

- **Dependency injection for testability.** `main` takes a `runDeps` struct; `getenv` is passed as `func(string) string`; HTTP clients accept a `nil *http.Client`; the chat client, planner, and external transports are all interfaces. Tests inject fakes rather than hitting the network — follow this when adding code that touches I/O. Test files are co-located `_test.go` and the suite is extensive.
- **`.agent/`, `.env`, `*.log`, and the `bqagent` binary are gitignored.** The committed `.agent/` you may see locally is untracked working state.
- Per the repo memory file, the user treats the word **"提交" as authorization to commit *and* push together.**
