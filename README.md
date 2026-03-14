# nanoAgent

[中文](./README_CN.md) | English

> *"The question is not what you look at, but what you see."* — Henry David Thoreau

The simplest way to build an agent that can interact with your system.

A minimal AI agent written in Go using an OpenAI-compatible chat completions API. The agent can execute bash commands, read files, and write files.

If you want to learn more (e.g. what MCP is, and how to fetch tools in a more modern way), see: https://github.com/sanbuphy/nanoMCP

## install

Install Go 1.22+ and build the CLI:

```bash
go build -o nanoagent ./cmd/agent
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

If `OPENAI_MODEL` is not set, nanoAgent defaults to `MiniMax-M2.5`.

## quick start

```bash
go run ./cmd/agent "list all python files in current directory"
go run ./cmd/agent "create a file called hello.txt with 'Hello World'"
go run ./cmd/agent "read the contents of README.md"
```

## how it works

The agent uses OpenAI-compatible tool calling to:
1. Receive a task from the user
2. Decide which tools to use (`execute_bash`, `read_file`, `write_file`)
3. Execute the tools locally
4. Return tool results to the model
5. Repeat until the task is complete

That's it. A few small Go files.

The core is still just a loop: call model → execute tools → repeat.

Current behavior intentionally matches the literal `agent.py` implementation:

- unknown tools are returned to the model as `Error: Unknown tool '...'`
- malformed JSON tool arguments stop the current run with an error
- file read/write failures also stop the current run with an error

## capabilities

- `execute_bash`: Run any bash command
- `read_file`: Read file contents
- `write_file`: Write content to files

## examples

```bash
# System operations
go run ./cmd/agent "what's my current directory and what files are in it?"

# File operations
go run ./cmd/agent "create a python script that prints hello world"

# Combined tasks
go run ./cmd/agent "find all .py files and count total lines of code"
```

---

## license

MIT

────────────────────────────────────────

⏺ *Like a single seed that grows into a forest, a few small Go files become infinite possibilities.*
