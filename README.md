# bqagent

[中文](./README_CN.md) | English

> *"The question is not what you look at, but what you see."* — Henry David Thoreau

A small Go agent for local work, now with workspace-aware context, Markdown skill definitions, lightweight memory, planning, persistent sessions, and a minimal background mode.

## What it can do

bqagent still keeps the same core loop:

1. send messages to an OpenAI-compatible chat completions API
2. let the model choose tools
3. run the tools locally
4. return tool results to the model
5. repeat until the task is done

The difference now is that the loop can be wrapped with extra capabilities inspired by `agent-claudecode.py` and OpenClaw:

- workspace-rooted system prompt assembly
- compatibility with `workspace/AGENT.md`, `SOUL.md`, `TOOLS.md`, and `USER.md`
- compatibility with `workspace/memory/MEMORY.md` and `workspace/memory/YYYY-MM-DD.md`
- continued support for `.agent/rules/*.md` and `.agent/skills/*/SKILL.md`
- optional planning with `--plan`
- persistent sessions with `--resume`
- minimal background execution with `--background`

## Install

Install Go 1.22+ and build the CLI:

```bash
go build -o bqagent ./cmd/agent
```

Set your environment variables:

**macOS/Linux:**
```bash
export OPENAI_API_KEY='your-key-here'
export OPENAI_BASE_URL='https://api.openai.com/v1'  # optional
export OPENAI_MODEL='gpt-4o-mini'  # optional
```

**Windows (PowerShell):**
```powershell
$env:OPENAI_API_KEY='your-key-here'
$env:OPENAI_BASE_URL='https://api.openai.com/v1'  # optional
$env:OPENAI_MODEL='gpt-4o-mini'  # optional
```

**Windows (CMD):**
```cmd
set OPENAI_API_KEY=your-key-here
set OPENAI_BASE_URL=https://api.openai.com/v1
set OPENAI_MODEL=gpt-4o-mini
```

If `OPENAI_MODEL` is not set, bqagent defaults to `MiniMax-M2.5`.

## Quick start

```bash
# default single-run task
go run ./cmd/agent "list all Go files in this repo"

# plan first, then execute the steps
go run ./cmd/agent --plan "inspect the current project structure and summarize it"

# start a background session
go run ./cmd/agent --background "read README.md and summarize it"

# resume a previous session
go run ./cmd/agent --resume <session-id> "continue from the previous result"
```

If you run `bqagent` without any arguments, it still defaults to `Hello`.

## Workspace layout

bqagent resolves a workspace root by walking upward from the current directory until it finds one of:

- `.agent`
- `.git`
- `go.mod`

Relative tool paths and shell commands run from that resolved workspace root.

Optional workspace files now support two layouts:

```text
project/
├─ workspace/
│  ├─ AGENT.md
│  ├─ SOUL.md
│  ├─ TOOLS.md
│  ├─ USER.md
│  └─ memory/
│     ├─ MEMORY.md
│     └─ YYYY-MM-DD.md
├─ agent_memory.md
└─ .agent/
   ├─ rules/
   │  └─ *.md
   ├─ skills/
   │  └─ <skill>/
   │     └─ SKILL.md
   ├─ sessions/
   │  └─ <session-id>/
   │     ├─ meta.json
   │     ├─ messages.jsonl
   │     └─ output.log
   └─ mcp.json
```

### Files and directories

- `workspace/AGENT.md`, `SOUL.md`, `TOOLS.md`, `USER.md`
  - OpenClaw-style context files
  - loaded into the system prompt by default when present
- `workspace/memory/MEMORY.md`
  - long-term memory file
  - loaded into the prompt at startup
- `workspace/memory/YYYY-MM-DD.md`
  - diary-style memory files
  - today's and yesterday's files are loaded automatically at startup
  - when `workspace/` exists, new task results are appended to today's file first
- `agent_memory.md`
  - compatibility path for the older layout
  - still loaded when present; if both memory files exist, both are included in the prompt
- `.agent/rules/*.md`
  - full rule documents injected into the prompt
- `.agent/skills/*/SKILL.md`
  - Markdown skill definitions summarized into the prompt
- `.agent/sessions/<session-id>/messages.jsonl`
  - append-only transcript for resumable conversations
- `.agent/sessions/<session-id>/output.log`
  - human-readable execution log
- `.agent/mcp.json`
  - reserved path for future MCP-style tool definitions
  - live MCP transport is **not** implemented yet

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

## Sessions and background mode

`--background` starts a minimal background session by launching the same binary as a child process and writing output to:

- `.agent/sessions/<session-id>/meta.json`
- `.agent/sessions/<session-id>/messages.jsonl`
- `.agent/sessions/<session-id>/output.log`

The command immediately prints the session ID, session directory, and log path.

`--resume <session-id> "..."` loads `messages.jsonl`, appends your follow-up task, and continues from there.

This is intentionally a small implementation:

- no daemon
- no queue server
- no live MCP runtime
- no vector memory

## Examples

```bash
# Ask the agent to inspect the repo
go run ./cmd/agent "what files are in this repository?"

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
