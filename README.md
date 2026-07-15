# bqagent

[中文](./README_CN.md) | English

> *"The question is not what you look at, but what you see."* — Henry David Thoreau

A small Go agent for local work, now with workspace-aware context, Markdown skill definitions, lightweight memory, planning, persistent sessions, request-time context management, checkpoint-based resume compaction, and a minimal background mode.

## What it can do

bqagent still keeps the same core loop:

1. send messages through OpenAI Chat Completions, OpenAI Responses, or Anthropic Messages
2. let the model choose tools
3. run the tools locally
4. return tool results to the model
5. repeat until the task is done

The difference now is that the loop can be wrapped with extra capabilities inspired by `agent-claudecode.py` and OpenClaw:

- workspace-rooted system prompt assembly
- primary `.agent/` workspace layout with `AGENT.md`, `SOUL.md`, `TOOLS.md`, and `USER.md`
- compatibility with legacy `workspace/AGENT.md`, `SOUL.md`, `TOOLS.md`, and `USER.md`
- compatibility with legacy `workspace/memory/MEMORY.md` and `workspace/memory/YYYY-MM-DD.md`
- continued support for `.agent/rules/*.md` and `.agent/skills/*/SKILL.md`
- optional planning with `--plan`
- interactive multi-turn conversation with `--chat`
- persistent sessions with `--resume`
- request-time context pruning for long conversations
- optional request-time summary compaction for older turns
- checkpoint-based compact resume while keeping raw session history intact
- minimal background execution with `--background`
- a long-lived HTTP server with `--server`, including optional ServerChan reply delivery

## Install

Install Go 1.22+ and build the CLI:

```bash
go build -o bqagent ./cmd/agent
```

Set your environment variables:

`LLM_API_TYPE` selects the wire protocol. Supported values are `openai` (default),
`openai-response`, and `anthropic`. The existing `OPENAI_*` variables remain
compatible; generic `LLM_*` variables take precedence.

**macOS/Linux:**
```bash
export OPENAI_API_KEY='your-key-here'
export OPENAI_BASE_URL='https://api.openai.com/v1'  # optional
export OPENAI_MODEL='gpt-4o-mini'  # optional
export LLM_API_TYPE='openai'  # optional
```

**Windows (PowerShell):**
```powershell
$env:OPENAI_API_KEY='your-key-here'
$env:OPENAI_BASE_URL='https://api.openai.com/v1'  # optional
$env:OPENAI_MODEL='gpt-4o-mini'  # optional
$env:LLM_API_TYPE='openai'  # optional
```

**Windows (CMD):**
```cmd
set OPENAI_API_KEY=your-key-here
set OPENAI_BASE_URL=https://api.openai.com/v1
set OPENAI_MODEL=gpt-4o-mini
set LLM_API_TYPE=openai
```

You can also put the same variables in a `.env` file at the workspace root. bqagent will load that file automatically on startup.

```dotenv
OPENAI_API_KEY=your-key-here
OPENAI_BASE_URL=https://api.openai.com/v1
OPENAI_MODEL=gpt-4o-mini
LLM_API_TYPE=openai
```

OpenAI Responses API example:

```dotenv
LLM_API_TYPE=openai-response
OPENAI_API_KEY=your-key-here
OPENAI_BASE_URL=https://api.openai.com/v1
OPENAI_MODEL=gpt-5
```

Anthropic Messages API example:

```dotenv
LLM_API_TYPE=anthropic
ANTHROPIC_API_KEY=your-key-here
ANTHROPIC_BASE_URL=https://api.anthropic.com/v1
ANTHROPIC_MODEL=claude-sonnet-4-5
```

You may use `LLM_API_KEY`, `LLM_BASE_URL`, and `LLM_MODEL` instead of the
provider-specific variables. `OPENAI_API_TYPE` is accepted as a compatibility
alias for `LLM_API_TYPE`.

Environment variables that are already set in the shell take precedence over values from `.env`.

If no model variable is set, bqagent defaults to `MiniMax-M2.5`.

The effective model and API type are injected into the system prompt for every
built-in LLM turn, so the assistant can answer model-identity questions from
runtime configuration instead of guessing. Resumed sessions refresh this line
after a restart, and run traces record the same effective model.

For ServerChan Bot webhook conversations, the server mode also supports:

```bash
export SERVERCHAN_BOT_TOKEN='your-bot-token'
export SERVERCHAN_BOT_WEBHOOK_SECRET='your-webhook-secret'  # optional but recommended
```

## Quick start

```bash
# default single-run task
go run ./cmd/agent "list all Go files in this repo"

# interactive multi-turn conversation
go run ./cmd/agent --chat

# start a chat with an initial task
go run ./cmd/agent --chat "read README.md and summarize it"

# plan first, then execute the steps
go run ./cmd/agent --plan "inspect the current project structure and summarize it"

# start a background session
go run ./cmd/agent --background "read README.md and summarize it"

# start the long-lived HTTP server
go run ./cmd/agent --server

# start the HTTP server in background
go run ./cmd/agent --server --background

# resume a previous session
go run ./cmd/agent --resume <session-id> "continue from the previous result"

# resume a previous session in chat mode
go run ./cmd/agent --chat --resume <session-id>
```

If you run `bqagent` without any arguments, it still defaults to `Hello`.

## Workspace layout

bqagent resolves a workspace root by walking upward from the current directory until it finds one of:

- `.agent`
- `.git`
- `go.mod`

Relative tool paths and shell commands run from that resolved workspace root.

The primary workspace layout is `.agent/`. Legacy `workspace/` files are still read as a compatibility fallback when the corresponding `.agent/` files are absent.

```text
project/
└─ .agent/
   ├─ AGENT.md
   ├─ SOUL.md
   ├─ TOOLS.md
   ├─ USER.md
   ├─ memory/
   │  ├─ MEMORY.md
   │  └─ YYYY-MM-DD.md
   ├─ rules/
   │  └─ *.md
   ├─ skills/
   │  └─ <skill>/
   │     └─ SKILL.md
   ├─ sessions/
   │  └─ <session-id>/
   │     ├─ meta.json
   │     ├─ messages.jsonl
   │     ├─ context_checkpoint.json
   │     └─ output.log
   └─ mcp.json
├─ workspace/  # legacy compatible layout
│  ├─ AGENT.md
│  ├─ SOUL.md
│  ├─ TOOLS.md
│  ├─ USER.md
│  └─ memory/
│     ├─ MEMORY.md
│     └─ YYYY-MM-DD.md
└─ agent_memory.md
```

### Files and directories

- `.agent/AGENT.md`, `SOUL.md`, `TOOLS.md`, `USER.md`
  - OpenClaw-style context files
  - loaded into the system prompt by default when present
  - when both `.agent/` and `workspace/` exist, `.agent/` takes precedence
- `.agent/memory/MEMORY.md`
  - long-term memory file
  - loaded into the prompt at startup
- `.agent/memory/YYYY-MM-DD.md`
  - diary-style memory files
  - today's and yesterday's files are loaded automatically at startup
  - new task results are appended to today's `.agent/memory/YYYY-MM-DD.md`
- `workspace/AGENT.md`, `workspace/memory/*`
  - legacy compatibility layout
  - read only when the corresponding `.agent/` file is absent
- `agent_memory.md`
  - compatibility path for the older layout
  - still loaded when present; if both memory files exist, both are included in the prompt
- `.agent/rules/*.md`
  - full rule documents injected into the prompt
- `.agent/skills/*/SKILL.md`
  - Markdown skill definitions summarized into the prompt
- `.agent/sessions/<session-id>/messages.jsonl`
  - append-only raw transcript for resumable conversations
- `.agent/sessions/<session-id>/context_checkpoint.json`
  - compact checkpoint with summary plus recent tail for faster resume context reconstruction
  - does not replace or rewrite the raw `messages.jsonl` history
- `.agent/sessions/<session-id>/output.log`
  - human-readable execution log
- `.agent/mcp.json`
  - MCP server config (`mcpServers` map). **Streamable HTTP** servers listed here are connected at
    startup; their tools are discovered via `tools/list` and exposed to the model as
    `mcp__<server>__<tool>`. Header values support `${ENV}` expansion (e.g. `Bearer ${DASHSCOPE_API_KEY}`).
  - Discovery is best-effort: a server marked `"disabled": true`, missing, or unreachable is skipped
    (a warning is logged) and never blocks startup. Only the Streamable HTTP transport is supported.

## Built-in tools

Default built-in tools:

- `execute_bash`
- `read_file`
- `write_file`

When planner support is enabled, the agent can also use:

- `plan`

Behavior notes:

- unknown tools are returned to the model as `Error: Unknown tool '...'`
- malformed JSON tool arguments stop the current run with an error
- file read/write failures also stop the current run with an error
- relative `read_file` / `write_file` paths are resolved from the workspace root
- `execute_bash` also runs from the workspace root
- `--server` and `--chat` now share the same built-in local tool set, including shell, file, web search, and memory tools

## Sessions and background mode

`--chat` starts an interactive multi-turn conversation in the terminal. Type your messages one at a time; the agent keeps the conversation going across turns. Type `/exit` or press Ctrl-D (EOF) to end the session. Chat sessions are automatically persisted under `.agent/sessions/`.

Long conversations now use request-time context management before each model call:

- completed historical tool-call scaffolding is stripped from the request payload
- older turns can be pruned to stay within a target input budget
- optional summarization can replace older dialogue with a synthetic summary message
- the raw on-disk transcript remains intact even when the request payload is shortened

`--background` starts a minimal background session by launching the same binary as a child process and writing output to:

- `.agent/sessions/<session-id>/meta.json`
- `.agent/sessions/<session-id>/messages.jsonl`
- `.agent/sessions/<session-id>/context_checkpoint.json` (when a summary checkpoint has been created)
- `.agent/sessions/<session-id>/output.log`

The command immediately prints the session ID, session directory, and log path.

`--resume <session-id> "..."` restores the session, refreshes the current system prompt, reuses `context_checkpoint.json` when compatible, appends your follow-up task, and continues from there.

`--server` starts a long-lived HTTP service on `127.0.0.1:8080` by default and exposes:

- `GET /` (embedded web chat UI)
- `GET /healthz`
- `GET /api/v1/status` (effective built-in LLM API type and model)
- `POST /api/v1/chat`
- `POST /api/v1/webui/chat`
- `POST /api/v1/chat/stop`
- `POST /api/v1/serverchan/chat`
- `POST /api/v1/serverchan/bot/webhook`

`/api/v1/chat` continues conversations by `session_id`.

`GET /api/v1/status` returns the effective built-in LLM runtime identity, for
example `{"status":"ok","llm":{"api_type":"openai","model":"MiniMax-M2.5"}}`.
It never exposes API keys or provider endpoint URLs. The WebUI displays this
identity under the bqagent title when the endpoint is available.

`GET /` serves a self-contained, single-page chat UI (HTML/CSS/JS embedded in the binary, no external assets). Open `http://127.0.0.1:8080` in a browser and chat directly. The UI supports light/dark themes and safely renders Markdown headings, lists, task lists, tables, blockquotes, links, images, and copyable fenced code blocks, making README-style `.md` content easy to read. Replies stream token-by-token over Server-Sent Events from `POST /api/v1/webui/chat`; while a turn is running, the send button becomes a stop button backed by the channel-independent `POST /api/v1/chat/stop` endpoint, which cancels the active model request and tool execution identified by `turn_id`. The cancellation registry lives in the shared conversation service, so other channels can opt in later without WebUI-specific stop logic. `event: progress` reports iterations, tool activity, and stage checkpoints. Long WebUI work pauses with a persisted stage summary, so replying "继续" resumes the same `session_id` instead of restarting exploration. The web UI is **enabled by default**; set `WEBUI_ENABLED=false` to disable it (then `GET /` returns 404).

`/api/v1/serverchan/chat` is the existing sendkey-based push adapter: it generates a reply and forwards it through ServerChan using the `text` / `desp` / `sendkey` shape from the Go demo.

`/api/v1/serverchan/bot/webhook` is the conversational webhook endpoint for ServerChan Bot / WeChat replies. It accepts the Bot webhook JSON update format, maps each inbound `chat_id` onto a persisted bqagent session, and sends the assistant reply back through the Bot `sendMessage` API using `SERVERCHAN_BOT_TOKEN`. If `SERVERCHAN_BOT_WEBHOOK_SECRET` is set, requests must include `X-Sc3Bot-Webhook-Secret`.

`--server --background` runs this server in the background and writes service logs to `.agent/server/server.log`. For real webhook use, expose `/api/v1/serverchan/bot/webhook` through a public HTTPS endpoint or reverse proxy.

By default the loop behaves like an auto-compacting agent: when the conversation
approaches the input-token budget it summarizes (compacts) the older turns and
**continues** on the compacted context, rather than stopping at a fixed turn
count. The iteration cap is therefore just a runaway safety valve (defaults to a
high `1000`). Summarization is enabled
by default — set `CONTEXT_SUMMARY_MODEL` to summarize with a cheaper model on long
tasks, or `CONTEXT_SUMMARIZATION_ENABLED=false` to fall back to plain pruning.

Context behavior is configurable through environment variables:

Sessions persist the channel/user mapping, status, messages, and resumable context checkpoints. By default, `SESSION_TRANSCRIPT_MODE=compact` rewrites `messages.jsonl` to the bounded `working_messages.jsonl` snapshot after each turn, preventing raw tool results from accumulating indefinitely. Set `SESSION_TRANSCRIPT_MODE=full` to retain the previous append-only audit transcript. If a transcript is newer than its working snapshot after an interrupted turn, recovery uses the newer transcript. `output.log` keeps its latest 1 MiB by default. WeChat/iLink sends only the final reply because its context token must not be consumed by intermediate progress messages.

- `AGENT_MAX_ITERATIONS` (loop runaway safety valve; defaults to `1000`)
- `CHANNEL_AGENT_MAX_ITERATIONS` (optional channel/WebUI override; defaults to `30`)
- `CHANNEL_TURN_TIMEOUT` (whole channel turn timeout; defaults to `10m`)
- `CHANNEL_STAGE_MAX_ITERATIONS` (QQ/iLink iterations before a persisted checkpoint; defaults to `20`)
- `CHANNEL_STAGE_TIMEOUT` (QQ/iLink time before a persisted checkpoint; defaults to `90s`)
- `WEBUI_STAGE_MAX_ITERATIONS` (iterations before a WebUI checkpoint; defaults to `20`)
- `WEBUI_STAGE_TIMEOUT` (time before a WebUI checkpoint; defaults to `90s`)
- `CONTEXT_MANAGEMENT_ENABLED`
- `CONTEXT_MAX_INPUT_TOKENS`
- `CONTEXT_TARGET_INPUT_TOKENS`
- `CONTEXT_RESPONSE_RESERVE_TOKENS`
- `CONTEXT_KEEP_LAST_TURNS`
- `CONTEXT_SUMMARIZATION_ENABLED`
- `CONTEXT_SUMMARY_TRIGGER_TOKENS`
- `CONTEXT_SUMMARY_MODEL`
- `SESSION_TRANSCRIPT_MODE` (`compact` by default; set `full` for append-only audit history)
- `SESSION_OUTPUT_MAX_BYTES` (defaults to `1048576`; `0` disables trimming)
- `RUN_TRACE_ENABLED` (persist structured run traces; defaults to `false`)

This is still intentionally a small implementation:

- the one-shot background task path is not a daemon
- no queue server
- MCP support is client-side and Streamable-HTTP-only (no stdio/SSE transports, no MCP server mode)
- no vector memory

## Run traces, evaluation, and feedback

Run tracing is disabled by default. Set `RUN_TRACE_ENABLED=true` in the environment or workspace `.env` to persist a structured trace under `.agent/runs/<run-id>/` for each task, including model/context versions, token usage, tool summaries, timings, errors, artifacts, verifier results, and feedback. When disabled, responses omit `run_id` and the run trace/feedback endpoints are unavailable.

```bash
go run ./cmd/eval --suite smoke --mode replay
go run ./cmd/eval --suite all --mode live --repeats 3
```

Use `/feedback up|down [comment]` or `POST /api/v1/runs/<run-id>/feedback` to rate a run.

## Subagents

`/agent spawn <claude|codex|cursor|opencode> -- <task>` starts an asynchronous external agent in an isolated Git worktree. Use `/agent list`, `wait`, `result`, `interrupt`, `cancel`, `resume`, `apply`, and `cleanup` to manage it. Results and patches are persisted under `.agent/subagents/<id>/`; patches are never applied automatically.

## Structured memory

Structured, revisioned memory is stored in `.agent/memory/entries.jsonl` with source-run provenance, confidence, supersession, sensitive-entry confirmation, and Chinese/English full-text indexing. Existing Markdown memory is imported idempotently. `/memory list|search|confirm|compact` provides direct management, while `mem_save` and `mem_get` remain compatible.

## Examples

```bash
# Ask the agent to inspect the repo
go run ./cmd/agent "what files are in this repository?"

# Interactive conversation
go run ./cmd/agent --chat

# Use workspace rules and skills
go run ./cmd/agent "follow the workspace rules and summarize the available skills"

# Run a planned task
go run ./cmd/agent --plan "analyze the current Go project and explain the main packages"

# Run in background
go run ./cmd/agent --background "scan the codebase and summarize the key files"
```

---

## License

MIT
