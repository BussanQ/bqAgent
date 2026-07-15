# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`bqagent` is a small Go agent that drives **OpenAI Chat Completions, OpenAI Responses, or Anthropic Messages** in a tool-calling loop: send messages → model picks tools → run tools locally → feed results back → repeat until done. The same core loop is reused across four entry modes — single-shot CLI, interactive `--chat`, background process, and a long-lived HTTP `--server` that fronts chat channels (ServerChan, QQ, WeChat/iLink). It can also delegate a turn to an external coding agent (Claude Code, Codex, Cursor, OpenCode) instead of running the loop itself.

Module `bqagent`, Go 1.26. Only two non-stdlib deps: `nhooyr.io/websocket` (QQ gateway) and `mdp/qrterminal` (WeChat login QR).

## Commands

```bash
make build          # go build -trimpath -ldflags '-s -w' -o bqagent ./cmd/agent
make build-amd      # CGO_ENABLED=0 GOOS=linux  GOARCH=amd64  (Linux artifact)
make build-windows  # CGO_ENABLED=0 GOOS=windows GOARCH=amd64
make clean

# Functional unit test matching the changed behavior
go test ./internal/agent -run TestRunConversation -v
```

Only run concrete functional unit tests that directly match the behavior being changed. Do not run broad `go test ./...`, unfiltered package tests, integration/end-to-end/live-network tests, or compile-only checks unless the user explicitly requests them. If no matching unit test exists, add or adjust one instead of widening the test scope. There is no linter config beyond the Go toolchain; use `gofmt` when needed. `make build-amd` and a `publish` (upload to server) flow also exist as `.claude/skills`.

## Running

```bash
go run ./cmd/agent "task"             # single-shot (defaults to "Hello" with no args)
go run ./cmd/agent --chat             # interactive; /exit or Ctrl-D to quit
go run ./cmd/agent --plan "task"      # decompose into steps, then execute each
go run ./cmd/agent --background "task"# fork a child process, print session id
go run ./cmd/agent --server           # HTTP on 127.0.0.1:8080
go run ./cmd/agent --resume <id> "…"  # continue a persisted session
```

Config is read from the environment and from a `.env` file at the workspace root (auto-loaded; real shell env wins over `.env`). `LLM_API_TYPE` selects `openai` (default), `openai-response`, or `anthropic`; generic `LLM_*` variables override the compatible `OPENAI_*` / `ANTHROPIC_*` variables. A matching API key is required for server mode. The model defaults to `agent.DefaultModel` (`MiniMax-M2.5`) when unset. **`.env` here contains live secrets — never print or commit its contents.**

## Architecture

The dependency direction is `cmd/agent → internal/{server,runtime} → internal/{agent,workspace,session,tools,extagent}`. Startup wiring (in [cmd/agent/main.go](cmd/agent/main.go) `runWithDeps`) is the fastest way to see how the pieces connect:

1. `workspace.Discover(cwd)` walks **up** until it finds `.agent`, `.git`, or `go.mod` — that directory becomes the **workspace root**, and *all* relative tool paths, `execute_bash`, and memory writes resolve from it.
2. `runtime.LoadDotEnv` + `MergeEnv` layer `.env` under the real environment.
3. `ws.EnsureDefaults()` scaffolds missing `.agent/*` context files from embedded `defaults/` (existing files are never overwritten).
4. `ws.BuildSystemPrompt(...)` assembles the system prompt (see below).
5. `runtime.Factory.Build()` constructs the `Client`, optional `Planner`, the tool `Catalog`, and the context-window config into a `Runtime`, which mints `*agent.Agent` instances.
6. Mode dispatch runs the loop foreground / in chat / in server / as a background child.

### The agent loop — `internal/agent`

[internal/agent/loop.go](internal/agent/loop.go) `runConversation` is the heart of the system. The CLI iteration cap is a **runaway safety valve, not a task limit**: `agent.DefaultMaxIterations = 1000` is the in-loop fallback and the default for `runtime.MaxIterations` (env `AGENT_MAX_ITERATIONS`). Interactive channels default to `30` iterations and can be overridden with `CHANNEL_AGENT_MAX_ITERATIONS`; WebUI uses `WEBUI_STAGE_*` checkpoint budgets, while QQ and WeChat/iLink use `CHANNEL_STAGE_*` (both default to 20 iterations / 90s). Each iteration:

- builds the request payload via `buildRequestMessages` (context management, below),
- calls the model (streaming or not),
- appends the assistant message and records it,
- if there are no tool calls → returns final content,
- otherwise executes each tool call and appends a `tool` result message, then loops.

`plan` and `run_skill` are handled specially inside the loop (they recurse into a child `runConversation`); all other tools dispatch through the `functions` map. Unknown tools and malformed JSON args become tool error messages rather than aborting the run.

**Messages are untyped `[]map[string]any`** in OpenAI chat shape (`role`, `content`, `tool_calls`, `tool_call_id`). This convention runs end to end — session persistence, context pruning, and channel state all operate on these maps.

**Context management** (`buildRequestMessages`): it converts completed tool scaffolding into assistant evidence that retains tool names, arguments, and results, prunes to a token budget, and applies a hard request-size guard when a recent turn or tool result alone is oversized. The hard guard prioritizes the system prompt, conversation summary, and latest user request, then fits or truncates recent evidence. When over `SummaryTriggerTokens` and summarization is enabled (**on by default**) it summarizes older turns into a synthetic message and writes `context_checkpoint.json`. The full on-disk `messages.jsonl` remains an audit trail and is not sent verbatim to the model. All tunable via `CONTEXT_*` env vars (see `runtime.ConfigFromEnv`); estimation is a crude `chars/4`.

### System prompt assembly — `internal/workspace`

`BuildSystemPrompt` concatenates, in order: base prompt → workspace section → `.agent/{AGENT,SOUL,TOOLS,USER}.md` → `.agent/rules/*.md` → a summary of `.agent/skills/*/SKILL.md` → memory tail. **Two memory layouts coexist:** the primary `.agent/memory/{MEMORY.md, YYYY-MM-DD.md}` and a legacy fallback (`workspace/...` and `agent_memory.md`) read only when the primary file is absent. Task/result pairs auto-append to today's daily memory file. Skills can be invoked by id or by alias (parsed from SKILL.md frontmatter).

### Sessions — `internal/session`

Each session is a directory under `.agent/sessions/<id>/` with `meta.json` (status: created/running/completed/failed), append-only `messages.jsonl` (the complete audit transcript), bounded `working_messages.jsonl` (the context all server channels resume from), `context_checkpoint.json`, and `output.log`. `runtime.PrepareConversation` prefers the working snapshot, falls back to the raw transcript/checkpoint for legacy sessions, and ensures the system message is current. Successful turns persist a fresh working snapshot; the raw transcript remains append-only.

### Server & channels — `internal/server`

`Service.HandleTurn` orchestrates one turn for any caller and is guarded by a **per-session keyed lock** so concurrent requests to the same session serialize. A caller may assign `TurnRequest.TurnID`; the shared service then registers the whole turn for cancellation through `Service.StopTurn` / `POST /api/v1/chat/stop`, independent of any channel. Channels implement the `Channel` interface (`Name/Enabled/RegisterRoutes/Start`) and are registered in [cmd/agent/server.go](cmd/agent/server.go); each is enabled by its own env vars. The **web UI channel** ([internal/server/webui.go](internal/server/webui.go)) is **on by default** (`WEBUI_ENABLED=false` to disable): it serves a self-contained `go:embed`-ed page at `/`, streams replies token-by-token, emits detailed `progress` SSE events for iterations, tools, and checkpoints, and currently supplies the stop UI. QQ suppresses internal iteration/tool progress but retains short long-wait notices, loop protection, and final persisted checkpoints. WeChat/iLink also suppresses intermediate progress because its context token must be reserved for the single final reply. Reaching a stage budget produces an assistant checkpoint (`已发现 / 未完成 / 建议下一步`), after which “继续” resumes the same session. The whole turn is bounded by `CHANNEL_TURN_TIMEOUT` (default 10m); individual LLM HTTP calls have their own client timeout.

### External agents — `internal/extagent`

The `Broker` can route a turn to an external coding agent instead of the built-in loop. A message starting with `/claude`, `/codex`, `/cursor`, or `/opencode` routes explicitly (and sticks to that agent for the session); `/default` switches back. Transports are **ACP** (JSON-RPC over the agent's stdio) or **CLI** (one-shot exec), auto-detected from `AGENT_<NAME>_ACP_CMD/ARGS` and `AGENT_<NAME>_CLI_CMD/ARGS`.

### MCP client — `internal/mcp`

`.agent/mcp.json` (`mcpServers` map) configures **Streamable HTTP** MCP servers. `runtime.Factory.Build` calls `mcp.Discover` (best-effort, bounded by a 15s timeout): for each enabled server it runs the MCP handshake (`initialize` + `notifications/initialized`), lists tools (`tools/list`), and adapts each into a `tools.Definition` (name `mcp__<server>__<tool>`, carrying the server's raw `inputSchema` via `FunctionDefinition.RawParameters`) plus a `tools.Function` that proxies to `tools/call`. These flow into the Catalog through `tools.Options.ExtraDefinitions/ExtraFunctions` (so `internal/tools` never imports `internal/mcp`). Header values support `${ENV}` expansion. A disabled/missing/unreachable server is logged and skipped. Only the Streamable HTTP transport is implemented — no stdio/SSE, no MCP server mode.

### Run traces and evaluation — `internal/trace`, `internal/evalharness`

Run tracing is controlled by `RUN_TRACE_ENABLED` and defaults to off. When enabled, every CLI/server execution receives session, turn, and run IDs. A run persists `meta.json`, append-only `events.jsonl`, artifacts, feedback, and output under `.agent/runs/<run-id>/`. Model calls record context hashes and provider/estimated token usage; regular and special tools record redacted arguments, bounded result summaries, hashes, timings, and error categories. `/feedback` and `/api/v1/runs/<id>` expose the trace lifecycle. `cmd/eval` loads the versioned 28-task manifest in `eval/tasks.json`; replay mode is deterministic and live mode is explicit.

### Subagents — `internal/subagent`

`/agent` is distinct from sticky `/claude` and `/codex` routing. It creates asynchronous, persisted tasks under `.agent/subagents/<id>/`, uses a detached Git worktree per task, and launches the same binary with internal `--subagent-run <id>` worker mode. The worker calls the existing external-agent broker with the task ID as its external-session key and updates a persistent heartbeat. Default limits are three tasks per parent session, six globally, 30 minutes, and one transient retry. Completed work returns `result.md`, artifact metadata, and `diff.patch`; only `/agent apply` modifies the main worktree.

### Structured memory — `internal/memory`

`.agent/memory/entries.jsonl` is an append-only revision log. The derived `index.json` uses normalized English tokens and Chinese 2/3-grams. Runtime prompt assembly injects pinned preference/decision entries plus task-relevant search results within a fixed character budget. The `memory` tool supports add/replace/remove/search/list/confirm/compact; legacy `mem_save` and `mem_get` adapt to this store. Markdown memory migration is idempotent and leaves source files untouched.

## Built-in tools — `internal/tools`

`execute_bash`, `read_file` (with optional `offset`/`limit`), `write_file`, `edit_file` (exact string replacement, like Claude Code's Edit), `grep` (pure-Go regexp content search; no external ripgrep), `glob` (filename match, supports `**`), `todo_write` (session task list; `todos` is a JSON-array string since the `JSONSchema` type only models flat string props), `web_search` (Tavily, with Firecrawl env vars as fallback), `web_fetch`, `install_skill`, `mem_save`/`mem_get`, plus `plan` and `run_skill` (added conditionally). Tools are assembled into a `Catalog` (definitions + a name→`Function` registry) so the CLI, chat, and server all expose the same set. To add a tool: add its `Definition` in `builtinDefinitions()` and its implementation to `RegistryWithOptions`.

Within one assistant turn, independent tool calls run **concurrently** (capped at `maxParallelTools`), with results appended in the original order; a turn containing `plan` or `run_skill` falls back to sequential execution since those recurse and mutate the working history in place. `todo_write` results are mirrored to the agent's `ProgressWriter`.

## Conventions & gotchas

- **Dependency injection for testability.** `main` takes a `runDeps` struct; `getenv` is passed as `func(string) string`; HTTP clients accept a `nil *http.Client`; the chat client, planner, and external transports are all interfaces. Tests inject fakes rather than hitting the network — follow this when adding code that touches I/O. Test files are co-located `_test.go` and the suite is extensive.
- **`.agent/`, `.env`, `*.log`, and the `bqagent` binary are gitignored.** The committed `.agent/` you may see locally is untracked working state.
- Per the repo memory file, the user treats the word **"提交" as authorization to commit *and* push together.**
