# bqagent

[中文](./README_CN.md) | English

![bqagent WebUI](./docs/images/webui-overview.png)

> *"The question is not what you look at, but what you see."* — Henry David Thoreau

bqagent is a Go Agent Runtime built for real local workflows: a small, legible execution core with multi-agent orchestration, durable memory, full context management, and conversations across channels.

See the [project documentation](./docs/en/doc.md) for complete usage instructions and the [inline TUI guide](./docs/TUI.md) for shortcuts, commands, TTY fallback, and scrollback behavior.

## Why bqagent?

> **One binary. A whole team of agents that can collaborate, remember, and keep working.**
>
> bqagent is more than an LLM wired to a shell. It assembles models, tools, skills, memory, workspaces, and multiple interaction channels into a complete local Agent Harness.

|  | Core capability | What makes it different |
| :---: | --- | --- |
| 🧭 | **Multi-agent orchestration** | Natively orchestrate Claude, Codex, Cursor, and OpenCode: route work precisely with @ mentions, let bqagent coordinate and synthesize, or run subagents in isolated Git worktrees that return logs and reviewable patches. |
| 🛠️ | **A powerful Agent Harness** | Planning, tool use, progressively disclosed Skills, rules, MCP, web search, cancellation, retries, graceful fallback, and RunTrace all live in one execution loop—turning a model that can answer into an agent that can finish the job. |
| 🖥️ | **TUI + WebUI, side by side** | The terminal offers an always-streaming inline TUI with native scrollback, a command palette, and task queue. The browser adds SSE streaming, Markdown, attachments, a workspace tree, model switching, and one-click stop. |
| 🦞 | **OpenClaw-grade soul and memory** | OpenClaw-style `AGENT.md`, `SOUL.md`, `USER.md`, `TOOLS.md`, Rules, and Skills shape the agent. Structured Memory adds revisions, provenance, confidence, supersession, sensitive-data confirmation, and Chinese search. |
| 📦 | **One binary, minimal deployment** | The Go backend and complete WebUI are bundled with `go:embed`. Runtime needs no Node.js, Vite, CDN, or external static files—copy one `bqagent` binary and start. |
| 🧠 | **Context and workspaces as first-class concepts** | Workspace roots are resolved automatically while global and project configuration layers compose cleanly. Long conversations use budget-aware pruning, summaries, checkpoints, and persistent resume to stay compact without losing continuity. |
| 🌐 | **Conversations across channels** | The same agent runs through CLI, TUI, WebUI, WeChat iLink, QQ Bot, and ServerChan Bot. The interface changes; the workflow, sessions, and memory remain coherent. |
| 🔌 | **Multiple models and protocols** | Use OpenAI Chat Completions, OpenAI Responses, or Anthropic Messages, connect compatible endpoints, and switch configured models inside a session. |
| 🔍 | **Observable, recoverable, evaluable** | Persistent sessions, structured RunTrace, feedback, and replay/live eval make executions inspectable. Streaming idle watchdogs, loop guards, and resilient retries keep long-running work under control. |

Whether you are making one code change in the terminal, running a long-lived project from the WebUI, or coordinating multiple agents across chat channels, bqagent provides the same controllable, extensible, and recoverable execution foundation.
