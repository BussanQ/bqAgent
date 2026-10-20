# English Documentation

[中文文档](../doc.md#中文文档)

This is a small Go agent for local workflows. It preserves a minimal execution loop while adding workspace context, Markdown-defined skills, lightweight memory, persistent sessions, request-time context management, checkpoint-based compact resume, and a minimal background mode.

## What it can do

bqagent keeps its core intentionally simple:

1. Send messages through OpenAI Chat Completions, OpenAI Responses, or Anthropic Messages
2. Let the model choose tools
3. Execute those tools locally
4. Return tool results to the model
5. Repeat until the task is complete

Additional capabilities draw on ideas from `agent-claudecode.py` and OpenClaw:

- Workspace-rooted system prompt assembly
- A primary `.agent/` layout built around `AGENT.md`, `SOUL.md`, and `USER.md`
- Continued support for `.agent/rules/*.md` and `.agent/skills/*/SKILL.md`
- An always-streaming inline terminal TUI when started without a task on a TTY (stdin and stdout) with `TERM` not `dumb`; a task runs once and exits
- [Automatic Herdr support](#herdr): asynchronous activity reports, session recovery, and bounded release without blocking model work
- [Magpie Agent plugin](#magpie): select models in Magpie, connect through its gateway, and restore the original configuration on disconnect
- Persistent session recovery with `--resume`
- Request-time context pruning for long conversations
- Optional request-time summary compaction for older turns
- Compact checkpoint-based resume while retaining raw session history
- Minimal background sessions with `--background`
- A long-lived HTTP conversation server with `--server`, including optional ServerChan reply delivery

## Install

Install Go 1.24+, Node.js 24+, and npm 11+, then build through the Makefile:

```bash
make build
```

`make build` installs the locked frontend dependencies from `internal/server/webui/package-lock.json`, runs strict TypeScript checking and the Vite production build, and then embeds `internal/server/webui/dist` into the Go executable. `dist` and `node_modules` are not committed. Runtime distribution still consists of one `bqagent` binary and does not require Node.js, Vite, a CDN, or static files on disk. Use `make build-amd` for Linux amd64 and `make build-windows` for Windows amd64.

For WebUI-only development, run `npm run dev` in `internal/server/webui`; Vite proxies `/api` to `http://127.0.0.1:8080`. On a fresh checkout, do not invoke the raw `go build` command before generating the WebUI. Run `make webui-build` first or use one of the Makefile build targets above.

## External application integrations

### Herdr

Start `bqagent` in a Herdr pane to enable automatic integration; no plugin is required. It activates only when `HERDR_ENV=1` and `HERDR_PANE_ID`, `HERDR_BIN_PATH`, and `HERDR_SOCKET_PATH` are all present.

Foreground TUI and single-run tasks report activity and session recovery metadata asynchronously, then release the pane on exit. TUI Provider setup reports `blocked` while waiting for a selection. Reporting failures do not block model work, and prompts, replies, and tool output are not sent. HTTP/Bot services, background jobs, and internal workers do not claim a pane.

Recovery requires **Herdr 0.9.2+** and `bqagent` on `PATH`. It runs `bqagent --resume <session-id>`, opening the interactive TUI with the saved session context. See the [Herdr section in the TUI guide](../TUI.md#herdr-集成) for activation conditions, state mapping, and shutdown behavior; the guide also includes an English summary.

### Magpie

The repository includes the [bqa-magpie Agent plugin](../../plugins/bqa-magpie/README.md). Use a bqagent version that includes the Magpie backend adapter and ensure `bqagent` is on `PATH`. From the repository root, install the plugin:

```sh
magpie plugin add ./plugins/bqa-magpie
```

Alternatively, add the folder's absolute path in Magpie's plugin page. Find **bqAgent** on its Agent page, enable the connection, select a model, and start bqagent. Restart running bqagent processes, including the WebUI server, after changing models or disconnecting. Older bqagent versions do not read this connection configuration.

The plugin stores the gateway URL, dedicated Agent key, selected model, and available model catalog in a separate `~/.agent/magpie.json` file. The backend uses OpenAI Responses (`openai-response`, `/v1/responses`) with unchanged `provider/model` IDs. TUI, WebUI, and single-run tasks share this configuration loading behavior. Startup precedence is: **valid Magpie connection → active global provider → environment variables**.

The original `~/.agent/config.json` and encrypted provider keys remain intact. On disconnect, Magpie restores previous values or removes keys introduced by the connection. Without a complete Magpie connection, bqagent returns to the original provider or environment configuration. If `magpie.json` was configured manually before connecting, those earlier values are restored. The gateway key is stored as plaintext in this file; do not commit it to a repository.

Magpie's resource library can synchronize `~/.agent/AGENT.md` and `~/.agent/skills`. The plugin does not declare MCP synchronization: bqagent currently supports only Streamable HTTP and cannot consume stdio servers from general MCP configurations. See the [plugin README](../../plugins/bqa-magpie/README.md) for installation details, configuration behavior, and focused test commands.

## Environment variables

bqagent reads configuration from the process environment and from a `.env` file in the workspace root. Values already present in the process environment take precedence over `.env`. Loaded `.env` values are also exported to the process environment so shell tools and external-agent child processes inherit them.

The workspace `.env` format is recommended:

```dotenv
LLM_API_TYPE=openai
LLM_API_KEY=your-key-here
LLM_BASE_URL=https://api.openai.com/v1
LLM_MODEL=gpt-4o-mini
LLM_MODELS=gpt-4o-mini,fast=gpt-5.1
```

The same variables can be set directly in a shell:

**macOS/Linux:**
```bash
export LLM_API_KEY='your-key-here'
```

**Windows (PowerShell):**
```powershell
$env:LLM_API_KEY='your-key-here'
```

**Windows (CMD):**
```cmd
set LLM_API_KEY=your-key-here
```

### LLM provider

At startup, single-run tasks, TUI, background tasks, subagent workers, and Server prefer a valid [Magpie connection](#magpie), then the active provider in global `~/.agent/config.json`, and finally environment variables.

In the interactive TUI, use `/provider` to open the sequential setup wizard. The WebUI exposes the same configuration through its Provider settings control. Both interfaces share encrypted configuration storage and automatic model discovery.

On first startup, bqagent creates global `~/.agent/config.json` as its system-wide configuration file, not merely a Provider store. Its default content enables the WebUI password-only login with `admin123`:

```json
{
  "version": 1,
  "providers": [],
  "webui": {
    "password": "admin123"
  }
}
```

Initialization creates missing files only and never overwrites an existing `config.json`. When providers already exist, `webui` sits beside `providers`. Change the well-known default password immediately after first use. A successful login creates a 24-hour HttpOnly, SameSite=Strict browser session. The account menu in the top-right corner provides Change password and Log out. Changing a password requires the current password, a new password, and confirmation. New passwords must contain 6–128 characters, have no leading or trailing whitespace, and differ from the current password. Saving updates only `webui.password`, takes effect immediately without a restart, and invalidates all browser login sessions on the current server; log in again with the new password. Manual configuration edits still require restarting bqagent. `config.json` is created with mode `0600` and contains the login password as plain text, so keep it readable only by the current OS user. Authentication protects the chat, workspace, Provider, status, stop, and trace APIs used by the WebUI; independent WeChat, QQ, and ServerChan channel endpoints are unchanged. Set `webui.password` to an empty string or remove `webui`, then restart to disable authentication; the account menu is hidden in this mode.

Generic `LLM_*` values take precedence over provider-specific compatibility variables.

| Variable | Default | Description |
|---|---|---|
| `LLM_API_TYPE` | `openai` | Wire protocol: `openai`, `openai-response`, or `anthropic`. |
| `LLM_API_KEY` | — | Generic API key for environment-based configuration; not needed separately when a valid provider or Magpie connection supplies it. |
| `LLM_BASE_URL` | provider default | Generic provider endpoint override. |
| `LLM_MODEL` | empty | Model ID for the built-in LLM provider; configure it explicitly. |
| `LLM_MODELS` | empty | Comma-separated switchable models for the same provider; supports `alias=model-id`. Use `/model` to list, `/model <name-or-alias>` to switch the current session, and `/model default` to restore the default. |
| `LLM_STREAM_IDLE_TIMEOUT` | `2m` | Idle watchdog for streaming model requests while no response headers or body bytes arrive. Uses Go duration syntax; set to `0` to disable. |
| `LLM_STREAM_PROGRESS_TIMEOUT` | `2m` | Stops normal model generation streams with no text, reasoning, or tool-call progress. Heartbeats and empty deltas do not renew it; expiration does not automatically retry. Uses Go duration syntax; `0` disables it. Native search streams are unaffected. After 30 seconds without progress, the UI reports the waiting duration. |
| `OPENAI_API_TYPE` | — | Compatibility alias for `LLM_API_TYPE`. |
| `OPENAI_API_KEY` | — | OpenAI-compatible API key fallback. |
| `OPENAI_BASE_URL` | provider default | OpenAI-compatible endpoint fallback. |
| `OPENAI_MODEL` | — | OpenAI-compatible model fallback. |
| `ANTHROPIC_API_KEY` | — | Anthropic key fallback when the API type is `anthropic`. |
| `ANTHROPIC_BASE_URL` | provider default | Anthropic endpoint fallback. |
| `ANTHROPIC_MODEL` | — | Anthropic model fallback. |

OpenAI Responses API example:

```dotenv
LLM_API_TYPE=openai-response
LLM_API_KEY=your-key-here
LLM_BASE_URL=https://api.openai.com/v1
LLM_MODEL=gpt-5
```

Anthropic Messages API example:

```dotenv
LLM_API_TYPE=anthropic
LLM_API_KEY=your-key-here
LLM_BASE_URL=https://api.anthropic.com/v1
LLM_MODEL=claude-sonnet-4-5
```

The effective model and API type are injected into every built-in LLM system prompt and exposed without secrets by `GET /api/v1/status`.

### Search

| Variable | Default | Description |
|---|---|---|
| `AGENT_NATIVE_WEB_SEARCH` | `auto` | Native search: `auto` enables by protocol, including compatible gateways; `on` explicitly enables native search; `off` uses search APIs only. Native search requires `openai-response` or `anthropic`; Magpie also supports delegated provider search. |
| `SEARCH_API_KEY` | — | Tavily-compatible search key; takes precedence over Firecrawl settings. |
| `SEARCH_BASE_URL` | provider default | Tavily-compatible endpoint override. |
| `FIRECRAWL_API_KEY` | — | Firecrawl key used when `SEARCH_*` is not configured. |
| `FIRECRAWL_BASE_URL` | provider default | Firecrawl endpoint override. |

For ordinary providers, `web_search(query)` first delegates to the current session model through a separate native-search request, returning a summary and source links to the main model. Responses uses `web_search`; Anthropic uses `web_search_20250305` and continues `pause_turn` responses. TUI, WebUI, Ask/Run, and CLI share this entry point; changing models or providers recomputes availability.

Native search reuses the model provider credentials, so `web_search` remains available without `SEARCH_API_KEY` or `FIRECRAWL_API_KEY`. Provider search and token charges still apply. The default `auto` enables native search for `openai-response` / `anthropic`, regardless of endpoint, model name, or local gateway authentication. Compatible gateways such as Magpie need no `on` override. Actual search support depends on the provider, with fallback as described below. Responses requests use streaming to retain search events and sources, including Codex streams relayed by Magpie whose terminal event has an empty `output`. Gateways returning ordinary JSON are also supported.

When connected through `~/.agent/magpie.json` or a provider with ID `magpie`, Responses search subrequests use `/v1/responses` directly with `web_search`, the same gateway, model, and credentials. They no longer detour through `/v1/messages`. Explicit Anthropic configurations still use Messages; Chat Completions retains the Messages search compatibility path. Magpie chooses its configured search provider. No MCP setup or search API key is required. Magpie is not inferred from model names or ports.

Search subrequests and their fallbacks do not force `tool_choice`. This avoids conflicts with thinking models and lets Magpie finish its internal tool loop and return a summary once results are available. The prompt requires searching first and stopping once relevant sources are found. Results still require actual search records and source URLs; ordinary answers without a search are rejected.

Magpie Messages search uses streaming. Once a complete, matching search call and result block contain valid source URLs, their source list is returned to the main model and the search request is closed, without waiting for further gateway searches or a summary. The main model can fetch those pages as needed; planning text preceding the search is not presented as a conclusion. Complete JSON replies remain supported. Incomplete results, missing sources and server search errors retain the existing fallback behavior. Early returns count only token usage already reported in the stream.

If Magpie Responses search fails, it falls back directly to configured Tavily / Firecrawl search without trying Messages. Failed Messages searches for Anthropic and Chat Completions also fall back directly to search APIs. Native search adds no fixed total timeout. Every attempt inherits the caller's cancellation and any explicit deadline; cancellation or expiration of that deadline stops without fallback. Streaming requests use `LLM_STREAM_IDLE_TIMEOUT` (default `2m`, `0` disables it), covering the wait for response headers and gaps without response-body bytes. Data and heartbeats renew it; an idle timeout can still trigger search fallback. Non-streaming requests retain the HTTP client's timeout. Explicit unsupported native-search responses are cached per model for 30 minutes within the current client, including Magpie Responses. The Magpie Messages compatibility path for other protocols remains independent of this cache. Search is disabled when neither native search nor a nonblank search API key is available; rate limits, network failures, and individual empty results do not disable it. Non-Magpie Chat Completions continues to use search APIs; `web_fetch` is unaffected.

Responses requests limits of three built-in tool calls and 4096 output tokens by default. If Codex or a compatible gateway explicitly rejects `max_tool_calls` / `max_output_tokens` with HTTP 400/422, only the rejected parameter is removed and retried, at most once per parameter and three requests in total. Compatibility is cached per client and model for 30 minutes. Parameter rejection does not disable search; omitted limits use the server defaults. Anthropic allows three searches per request and up to two `pause_turn` continuations, for at most three requests. Results require an actual search record and valid source URLs; ordinary model answers are not treated as search results. Token usage reported by search responses is included in the conversation's usage totals.

Protocol references: [Responses web search](https://developers.openai.com/api/docs/guides/tools-web-search), [Anthropic web search](https://platform.claude.com/docs/en/agents-and-tools/tool-use/web-search-tool).

### MCP

| Variable | Default | Description |
|---|---|---|
| `MCP_ALLOWED_ENV` | empty | Comma-separated process-level allowlist for `${NAME}` / `$NAME` references in `.agent/mcp.json` header values. |

Set `MCP_ALLOWED_ENV` before using placeholders in `.agent/mcp.json` header values, for example `MCP_ALLOWED_ENV=DASHSCOPE_API_KEY`. URL values never expand environment variables; unlisted, undefined, or malformed placeholders cause only that MCP server to be skipped.

### Server and channels

| Variable | Default | Description |
|---|---|---|
| `WEBUI_ENABLED` | `true` | Set to `false`, `0`, `no`, or `off` to disable `GET /`. |
| `SERVERCHAN_BOT_TOKEN` | — | Token used by the ServerChan Bot webhook reply path. |
| `SERVERCHAN_BOT_WEBHOOK_SECRET` | — | Required non-blank value for `X-Sc3Bot-Webhook-Secret`; without it the bot webhook endpoint returns `503`. |
| `QQ_BOT_ENABLED` | automatic | QQ is enabled when credentials exist; false-like values force-disable it. |
| `QQ_BOT_APP_ID` | — | QQ Bot application ID. |
| `QQ_BOT_CLIENT_SECRET` | — | QQ Bot client secret. |
| `QQ_BOT_TOKEN_BASE_URL` | `https://bots.qq.com` | QQ token endpoint override. |
| `QQ_BOT_API_BASE_URL` | `https://api.sgroup.qq.com` | QQ API and gateway endpoint override. |
| `WEIXIN_ILINK_ENABLED` | `true` | Set to a false-like value to disable the WeChat iLink channel. |
| `WEIXIN_ILINK_BASE_URL` | `https://ilinkai.weixin.qq.com` | iLink API endpoint override. |
| `WEIXIN_ILINK_CHANNEL_VERSION` | `1.0.2` | iLink channel protocol version. |
| `WEIXIN_ILINK_CDN_BASE_URL` | `https://novac2c.cdn.weixin.qq.com/c2c` | Inbound media CDN override. |

### Runtime, context, sessions, and tracing

| Variable | Default | Description |
|---|---|---|
| `AGENT_MAX_ITERATIONS` | `1000` | Global loop runaway safety limit. |
| `BASH_OUTPUT_MAX_BYTES` | `1048576` | Maximum combined stdout/stderr bytes retained by `execute_bash`; excess output is drained and discarded, then the result ends with a truncation marker. Non-positive or invalid values use the default. |
| `BASH_YIELD_TIMEOUT` | `30s` | How long `execute_bash` waits for a command; after that it keeps running in the background and returns `status=running` with a `job_id` (not a success). Use `bash_wait`/`bash_kill`. Must be a positive duration; invalid values use the default. Jobs do not survive the turn and are reclaimed when it ends. |
| `BASH_MAX_TIMEOUT` | `0` | Hard lifetime cap per bash job; `0` disables it. Must not be negative; invalid values use the default. Jobs are never killed for lack of progress. |
| `BASH_MAX_CONCURRENT` | `3` | Maximum concurrently running background bash jobs per turn. Non-positive or invalid values use the default. |
| `READ_FILE_MAX_BYTES` | `1048576` | Maximum file-content bytes returned by `read_file`; excess content is drained and discarded, then the result ends with a truncation marker. Non-positive or invalid values use the default. |
| `CHANNEL_AGENT_MAX_ITERATIONS` | `30` | Maximum iterations per turn for QQ, iLink, ServerChan Bot, and other message channels; does not limit WebUI. |
| `CHANNEL_TURN_TIMEOUT` | `10m` | Whole-turn timeout for QQ, iLink, ServerChan Bot, and other message channels; does not limit WebUI. |
| `CHANNEL_STAGE_MAX_ITERATIONS` | `20` | QQ/iLink iterations before a persisted stage checkpoint. |
| `CHANNEL_STAGE_TIMEOUT` | `90s` | QQ/iLink stage time budget. |
| `WEBUI_STAGE_MAX_ITERATIONS` | `0` (disabled) | Optional WebUI iteration budget before a stage checkpoint; enabled only by a positive value. |
| `WEBUI_STAGE_TIMEOUT` | `0` (disabled) | Optional WebUI stage time budget; enabled only by a positive duration. |
| `GROUP_EXTERNAL_AGENT_TIMEOUT` | `10m` | Independent timeout for each external Agent call in group chat; expiry cancels and invalidates the group's ACP session. |
| `WEBUI_WORKSPACE_ROOTS` | — | Additional server-side roots the WebUI may browse and select, separated with the OS path-list separator (`:` on Unix/macOS, `;` on Windows). The user home and startup workspace are always allowed. |
| `CONTEXT_MANAGEMENT_ENABLED` | `true` | Enables request-time context budgeting. |
| `CONTEXT_MAX_INPUT_TOKENS` | `200000` | Total estimated context budget (200K), including the response reserve. |
| `CONTEXT_COMPRESSION_TRIGGER_PERCENT` | `85` | Compaction trigger as a percentage of the total budget, range 1..100; invalid values fall back to 85. Default 200000 x 85% = 170000. |
| `CONTEXT_COMPRESSION_TRIGGER_TOKENS` | derived | Derived by default as `min(total*percent/100, total-reserve)` (170000 by default; compaction starts when input reaches it). An explicit positive value overrides the derived one. |
| `CONTEXT_TARGET_INPUT_TOKENS` | `18000` | Post-compaction input target (9% of the 200K total budget); does not trigger early compaction. |
| `CONTEXT_RESPONSE_RESERVE_TOKENS` | `4000` | Tokens reserved for the model response. |
| `CONTEXT_KEEP_LAST_TURNS` | `6` | Recent turns retained during compaction. |
| `CONTEXT_EXACT_COUNT_TRIGGER_PERCENT` | `100` | Independent legacy setting (default 100: exact counting only at the compaction trigger). Attempts provider-side exact counting (OpenAI Responses and Anthropic) after the full-request local/usage estimate reaches this percentage of the compaction trigger; unsupported or failed counts fall back safely. |
| `CONTEXT_SUMMARIZATION_ENABLED` | `true` | Enables summarization of older dialogue. |
| `CONTEXT_SUMMARY_TRIGGER_TOKENS` | follows trigger | Follows the effective compaction trigger (170000) by default; an explicit positive value overrides. Once compaction starts, attempts summarization at this input size; falls back to pruning on failure. |
| `CONTEXT_SUMMARY_MODEL` | main model | Optional cheaper model used for summaries. |
| `SESSION_TRANSCRIPT_MODE` | `compact` | `compact` uses only bounded `messages.jsonl`; `full` keeps append-only audit history and a separate working snapshot. |
| `SESSION_OUTPUT_MAX_BYTES` | `1048576` | Retained tail of each `output.log`; `0` disables trimming. |
| `RUN_TRACE_ENABLED` | `false` | Persists `.agent/runs/<run-id>/` traces and enables run feedback APIs. |
| `ENABLE_TASK_TOOL` | `false` | Enables the `task_update` / `task_scope` persistent task record, task prompts, session restore and TUI/WebUI task progress. When off, these tools are not exposed, saved tasks are not injected, `task.json` is neither read nor written (existing files are left untouched), and `turn_outcome` is unaffected. |

By default the total budget is 200K and the compaction trigger is derived at 85% = 170000 (reaching it triggers compaction), compacting toward 18K (9% of the total); exact counting defaults to 100, so it only runs at the compaction trigger. Fixed system instructions are preserved, so instructions or tool definitions exceeding the target can prevent reaching 18K. The TUI shows the latest request size relative to the total context budget, using the format `45%/200K`, and refreshes after compaction; values may be estimated. This differs from cumulative `in/out` usage.

### External coding agents

For each name in `CLAUDE`, `CODEX`, `CURSOR`, `OPENCODE`, and `PI`, `AGENT_<NAME>_ACP_CMD` and `AGENT_<NAME>_ACP_ARGS` override the ACP launch command. CLI transport is currently implemented for Claude, Codex, and Pi and can be overridden with `AGENT_<NAME>_CLI_CMD` and `AGENT_<NAME>_CLI_ARGS`.

Claude defaults to `claude -p --output-format json`; Codex prefers the shared App Server by default, with `codex exec --json --skip-git-repo-check` as its legacy CLI fallback; Pi defaults to `pi --mode json`, receives prompts on stdin, and resumes with `--session <ID>`. Cursor defaults to ACP via `cursor-agent acp`, and OpenCode defaults to ACP via `opencode acp`. The corresponding executable must be visible in the PATH of the process that starts bqAgent; restart a long-lived chat/server process after installing an external agent or changing its transport environment.

In TUI or `--server` sessions, use `/opencode <task>` to route the turn to OpenCode. Later messages remain bound to it until `/default` switches back to the built-in agent. OpenCode is ACP-only; configuring `AGENT_OPENCODE_CLI_CMD/ARGS` does not enable a CLI transport.

Codex automatically discovers the shared App Server without environment overrides. bqAgent first connects using `codex app-server proxy`. If that fails, it runs the idempotent `codex app-server daemon start` command and reconnects on success. Older Codex versions without daemon/proxy support, or unavailable shared services, fall back to a standalone `codex app-server` stdio process, then ACP/CLI. Probes only initialize the protocol; they never create threads or send model tasks. Closing bqAgent disconnects its proxies without stopping the shared daemon.

To explicitly connect to an existing shared service, set these variables and restart bqAgent:

```bash
export AGENT_CODEX_APP_SERVER_CMD=codex
export AGENT_CODEX_APP_SERVER_ARGS='app-server proxy'
```

Explicit `AGENT_CODEX_APP_SERVER_CMD/ARGS` overrides run exactly as configured: they do not automatically start the daemon or substitute a standalone App Server, but failed startup probes can still fall back to ACP/CLI. With CMD alone, ARGS retains its compatible default of `app-server`; with ARGS alone, CMD defaults to `codex`. Set `AGENT_CODEX_APP_SERVER_ARGS='app-server'` for a standalone process, or unset both variables to restore automatic discovery. Existing custom `AGENT_CODEX_ACP_CMD/ARGS` or `AGENT_CODEX_CLI_CMD/ARGS` settings preserve their original ACP/CLI path unless App Server is explicitly configured. For example, `AGENT_CODEX_CLI_CMD=codex` retains CLI mode.

The adapter inherits Codex authentication, model and security settings (server-side settings in shared mode); it creates/resumes threads, receives streamed events, checks the final turn status, and interrupts cancelled turns. Command, file-change and permission approvals use the existing approval UI; no approval receiver means no automatic grant. Other server requests, such as structured user questions, currently receive an explicit unsupported-method error. Existing CLI thread IDs can resume through App Server; ACP sessions retain their original adapter. Failed or disconnected turns are never automatically replayed or rerouted to CLI, and newly created thread IDs remain saved after failed turns. Group chat, ordinary routing and subtasks share the same Broker.

Pi uses the shared external-agent detection, Broker, and session storage. Install and configure Pi, then restart bqAgent to use `@pi` in new groups or add it to existing groups with the member picker. Ordinary chats support `/pi <task>` and `/default`. Override the executable with `AGENT_PI_CLI_CMD`; custom `AGENT_PI_CLI_ARGS` must retain `--mode json` and avoid `--no-session`, `--continue`, `--resume`, or a fixed session to preserve conversation isolation.

The WebUI's **New conversation** menu can also create a group conversation. Available external agents detected asynchronously after startup join bqagent in the initial roster; requests that need detection wait for that background probe without delaying service startup. The member bar can add currently available agents that were not part of the initial roster. Hovering an external member reveals a remove control; membership changes persist while prior messages and external session state remain available if that member is re-added. bqagent is the permanent coordinator and cannot be removed. A group task without a mention is handled directly by bqagent without consulting external members. A task addressed to `@codex`, `@opencode`, `@pi`, or another external member is handled directly by those members without bqagent analysis or synthesis. bqagent coordinates external agents and produces a final synthesis only when the user explicitly mentions `@bqagent`; a later `@bqagent` turn can also synthesize conclusions already in the shared context. Members run sequentially in the same workspace and receive the shared group context. Group conversations are Run-only. Existing sticky `/codex` and `/opencode` routing remains unchanged in ordinary conversations. The orchestration is channel-neutral, although this release exposes it only in the WebUI.

## System diagnostics and readiness

Open **System diagnostics** in the WebUI header to inspect configuration, storage, external Agents, MCP servers, and channels, including failure reasons and suggested actions. Refresh reads a snapshot; active probes require an explicit click. There is no automatic polling.

```bash
# Read-only local inspection: no initialization, conversations, or server connection
bqagent --doctor

# Machine-readable report
bqagent --doctor --doctor-json

# Explicit storage, external Agent initialization, and MCP discovery probes (may start the shared Codex daemon)
bqagent --doctor --doctor-active
```

`--doctor-active` and `--doctor-json` require `--doctor` and may be combined. Diagnostic mode cannot be combined with a task, chat, background, or server mode. Exit codes: `0` ready (possibly degraded), `1` not ready, `2` invalid arguments, cancellation, timeout, or diagnostic execution failure.

| Endpoint | Purpose | Authentication |
| --- | --- | --- |
| `GET /healthz` | Existing process liveness check | None |
| `GET /readyz` | Minimal `{"ready":true/false}` result; HTTP 200 when ready, 503 otherwise | None; no configuration details |
| `GET /api/v1/webui/doctor` | Detailed snapshot | Existing WebUI authentication policy |
| `POST /api/v1/webui/doctor` | Explicit active probes | Existing WebUI authentication and same-origin checks |

Doctor endpoints accept an optional `workspace_id` query parameter for an already-open workspace; reading diagnostics never creates a workspace runtime. `/readyz` uses the server's default workspace. CLI diagnostics inspect the local environment, not live connections in another process; unobserved channel connections are marked unverified.

Readiness is **service-level**, not a guarantee that a model can generate a reply. Unreadable or invalid core configuration and known core storage failures make the service not ready. Missing models and optional external Agent/MCP failures cause degradation without blocking configuration access. Snapshot storage checks do not test writes. Missing lazy session/memory directories are checked through their nearest existing parent directory.

Active probes have a 15-second overall timeout, at most 3 seconds per ordinary external Agent and 5 seconds per MCP server. Automatic Codex discovery can use the remaining overall budget. Storage probes create and remove a temporary file. External Agent probes only initialize the protocol and close their probe connections; automatic Codex discovery may start the shared daemon, but never stops an existing service. MCP probes only initialize and list tools, without changing the live tool catalog. They never generate model replies, execute MCP tools, send channel messages, log in, or reconnect channels. Channels without a side-effect-free probe continue to report runtime snapshots. Concurrent active probes on the same service instance are rejected.

Reports distinguish available, error, disabled, detecting, and unverified states, with timestamps and evidence sources. Passwords, API keys, sensitive headers, QR codes, command arguments, and credential-bearing URLs are excluded.

The internal `globalconfig` module now owns system configuration. The user-facing `~/.agent/config.json` path, JSON schema, initial `version: 1`, `webui.password: "admin123"`, and empty `providers` remain unchanged. Existing files are never overwritten at initialization; encrypted Provider credentials and `.config.key` require no migration. Section updates are serialized within the process so Provider changes preserve WebUI settings.

## Quick start

```bash
# single-run task
go run ./cmd/agent "list all Go files in this repo"

# start the interactive TUI when no task is given (requires TTY stdin/stdout and TERM not dumb)
go run ./cmd/agent

# start a background session
go run ./cmd/agent --background "read README.md and summarize it"

# start the long-lived HTTP server
go run ./cmd/agent --server

# start the HTTP server in background
go run ./cmd/agent --server --background

# equivalent shortcut
go run ./cmd/agent -d

# start WeChat iLink login through the running server
go run ./cmd/agent --ilink-login

# check WeChat iLink login status
go run ./cmd/agent --ilink-status

# resume a previous session
go run ./cmd/agent --resume <session-id> "continue from the previous result"

# resume the most recently updated session in the current workspace
go run ./cmd/agent --resume

# resume a specific session into the TUI (no task)
go run ./cmd/agent --resume <session-id>
```

If you run `bqagent` without a task, it starts the interactive TUI when stdin and stdout are TTYs and `TERM` is not `dumb`; otherwise it exits with an error. A task, including one passed with `--resume`, runs once and exits.

## Workspace layout

bqagent resolves a workspace root by walking upward from the current directory until it finds one of these workspace markers (the global `~/.agent` in the user home is not treated as a workspace marker):

- `.agent`
- `.git`
- `go.mod`

Relative tool paths and shell commands run from that resolved workspace root.

Primary configuration is fixed at `~/.agent/`, where missing defaults are initialized at startup. A workspace `.agent/` is an optional secondary layer and is not created when switching workspaces. When present, its `AGENT.md`, `SOUL.md`, `USER.md`, memory content, skills, and `mcp.json` are merged after the global configuration; same-named workspace skills and MCP servers override global definitions. The WebUI create button generates only `.agent/memory/`, `mcp.json`, `AGENT.md`, `SOUL.md`, and `USER.md`. Otherwise, workspace `.agent/memory/` is created lazily only when existing Markdown memory must be migrated or memory is explicitly written for the first time; read-only queries against an empty memory store do not create it.

Sessions are grouped by workspace. `<workspace-key>` combines a readable encoding of the cleaned absolute workspace path with a hash; `<session-id>` remains a timestamp plus a short ID. Legacy `sessions/<session-id>/` directories remain in place and support resume, listing, maintenance, and deletion. Moving or renaming a workspace requires a separate session ownership migration.

```text
~/.agent/
├─ sessions/
│  └─ <workspace-key>/
│     └─ <session-id>/
│        ├─ meta.json
│        ├─ messages.jsonl
│        ├─ working_messages.jsonl  # full mode only; legacy compact files are migrated
│        ├─ context_checkpoint.json
│        └─ output.log
└─ server/
   ├─ server.log
   ├─ weixin/
   │  ├─ token.json
   │  ├─ poller.json
   │  └─ chats/
   ├─ qq-bot/
   │  ├─ gateway.json
   │  └─ chats/
   └─ serverchan-bot/
      └─ chats/

project/
├─ .agent/
│  ├─ AGENT.md
│  ├─ SOUL.md
│  ├─ USER.md
│  ├─ memory/
│  │  ├─ MEMORY.md
│  │  └─ YYYY-MM-DD.md
│  ├─ rules/
│  │  └─ *.md
│  ├─ skills/
│  │  └─ <skill>/
│  │     └─ SKILL.md
│  └─ mcp.json
├─ workspace/  # legacy compatible layout
│  ├─ AGENT.md
│  ├─ SOUL.md
│  ├─ USER.md
│  └─ memory/
│     ├─ MEMORY.md
│     └─ YYYY-MM-DD.md
└─ agent_memory.md
```

### Files and directories

Tool usage guidance lives in `AGENT.md`; tool parameters come from the definitions sent with each request. Legacy `TOOLS.md` files are no longer generated or loaded. Move any custom guidance into `AGENT.md` in the same directory before deleting the old file. Initialization never overwrites a customized `AGENT.md` or `SOUL.md`; files that are byte-identical to a previously shipped default (never edited) are upgraded to the current default automatically.

The system prompt is assembled in this order. Everything listed here is stable across a session and can be cached by the provider:

1. Built-in core instructions: priorities (user request > repository rules > global defaults), effort matching, change scope and git safety, verification and final-answer rules, prompt-injection guidance; plus the built-in memory policy
2. Global `~/.agent/` `AGENT.md`, `SOUL.md`, `USER.md` (HTML comments are stripped; templates with only headings are skipped) and global rules
3. `AGENTS.md` at the workspace root (falling back to `CLAUDE.md`), truncated at 32 KiB; its project rules override the global defaults
4. Environment: OS/architecture, the shell used by `execute_bash`, and whether the workspace is a git repository
5. Workspace path index (legacy `workspace/` and `agent_memory.md` are listed only when present), workspace `.agent/` context, and the skills index

Parts are ordered from the most to the least widely shared (built-in, user, workspace), so the first two parts form a cacheable prefix reused across all workspaces of the same user.

The session start date and the memory snapshot live in the per-session context, so they do not change the cacheable prefix. With structured memory enabled the model sees only the `memory` tool; `mem_save` is no longer advertised but remains callable for older sessions.

- `.agent/AGENT.md`, `SOUL.md`, `USER.md`
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
  - global `~/.agent/skills` and workspace `.agent/skills` are merged; a same-named workspace skill fully replaces the global version
  - only the canonical name, frontmatter `description`, and workspace-relative path are indexed in the system prompt
  - when a skill is relevant, the model reads the complete `SKILL.md` on demand with `read_file`
  - explicit `/skill <name-or-alias> [args]` and leading skill IDs/aliases route through the same main conversation loop
- `~/.agent/sessions/<workspace-key>/<session-id>/messages.jsonl`
  - the only message file in compact mode: messages are appended during a turn and atomically replaced with bounded working context after the turn; full mode retains append-only history
- `~/.agent/sessions/<workspace-key>/<session-id>/working_messages.jsonl`
  - separate bounded working context in full mode only; legacy compact snapshots are migrated and removed on resume or startup maintenance
- `~/.agent/sessions/<workspace-key>/<session-id>/context_checkpoint.json`
  - compact checkpoint with summary plus recent tail for faster resume context reconstruction
- `~/.agent/sessions/<workspace-key>/<session-id>/output.log`
  - human-readable execution log
- `.agent/mcp.json`
  - MCP server config (`mcpServers` map). **Streamable HTTP** servers listed here are connected in parallel at
    startup; their tools are discovered via `tools/list` and exposed to the model as
    `mcp__<server>__<tool>`. Header environment expansion is described under
    [environment variables](#environment-variables).
  - The default file has no remote servers. Server URLs must be literal `http` or `https` URLs; only header
    values can use allowlisted environment placeholders.
  - TUI and WebUI services finish local initialization before waiting for any remote MCP server. Discovery runs in the background. The TUI status bar shows connecting, connected, and failed counts; WebUI diagnostics exposes per-server status. Each completed server publishes its tools for new turns; in-flight turns retain their original tool snapshot. Ordinary chat does not wait for MCP.
  - Parallel background discovery retains the shared 15-second budget and is cancelled when the UI or service closes. Single-run commands still wait for discovery before executing their task, with servers connected in parallel. There is no cross-process tool cache.
  - Discovery is best-effort: a server marked `"disabled": true`, missing, unreachable, or invalid is skipped
    (a warning is logged) and never aborts startup. Only the Streamable HTTP transport is supported.

## Built-in tools

Default built-in tools:

- `execute_bash`
- `read_file`
- `write_file`

Behavior notes:

- unknown tools are returned to the model as `Error: Unknown tool '...'`
- malformed JSON tool arguments stop the current run with an error
- file read/write failures also stop the current run with an error
- relative `read_file` / `write_file` paths are resolved from the workspace root
- `execute_bash` also runs from the workspace root
- `--server` and the interactive TUI now share the same built-in local tool set, including shell, file, web search, and memory tools

Workspace skills use progressive disclosure. A recommended skill starts with metadata such as:

```yaml
---
description: Summarize repository changes and prepare a concise release note.
aliases:
  - release-notes
---
```

The discovery prompt does not include the skill body or aliases. The model first calls `read_file` for the listed `.agent/skills/<name>/SKILL.md` path; workspace skills are read first with global skills as fallback. It then follows the complete instructions in the same conversation. `/skill <name-or-alias> [args]` is an explicit selection shortcut, not a separate skill runner. `install_skill` installs globally by default; pass `target=workspace` to install into the current workspace.

## Sessions and background mode

Startup without a task (including `--resume` without a task) enters an always-streaming inline TUI on a TTY (stdin and stdout) with `TERM` not `dumb`. Completed messages remain in native scrollback while the input, completion panel, queue, and status bar stay at the bottom; no alternate screen is used. More than five tool calls are merged into a clickable detail group, with mouse tracking enabled only while that group is interactive. There is no line-mode fallback. Sessions remain under global `~/.agent/sessions/`; resume validates the workspace and replays recent user/assistant text within a 200 KiB display budget. See the [inline TUI guide](../TUI.md) for shortcuts, commands, paste chips, queueing, prompt history, and `NO_COLOR`.

Long conversations now use request-time context management before each model call:

- completed historical tool-call scaffolding is stripped from the request payload
- older turns can be pruned to stay within a target input budget
- optional summarization can replace older dialogue with a synthetic summary message
- bounded working snapshots are persisted for reliable resume

`--background` starts a minimal background session by launching the same binary as a child process and writing output to:

- `~/.agent/sessions/<workspace-key>/<session-id>/meta.json`
- `~/.agent/sessions/<workspace-key>/<session-id>/messages.jsonl`
- `~/.agent/sessions/<workspace-key>/<session-id>/context_checkpoint.json` (when a summary checkpoint has been created)
- `~/.agent/sessions/<workspace-key>/<session-id>/output.log`

The command immediately prints the session ID, session directory, and log path.

`--resume <session-id> "..."` restores the session, refreshes the current system prompt, reuses `context_checkpoint.json` when compatible, appends your follow-up task, and continues from there.

`--server` starts a long-lived HTTP service on `127.0.0.1:8080` by default and exposes:

- `GET /` (embedded web chat UI)
- `GET /healthz`
- `GET /api/v1/status` (effective built-in LLM API type and model)
- `POST /api/v1/chat`
- `POST /api/v1/webui/chat`
- `GET /api/v1/webui/workspace` (paginated workspace directory listing)
- `GET /api/v1/webui/workspace/preview` (read-only workspace file preview)
- `GET /api/v1/webui/workspaces`, `GET /api/v1/webui/workspaces/directories`, and `POST /api/v1/webui/workspaces/open` (server-local workspace selection)
- `POST /api/v1/chat/stop`
- `POST /api/v1/serverchan/chat`
- `POST /api/v1/serverchan/bot/webhook`

`/api/v1/chat` continues conversations by `session_id`.
WebUI chat, explorer, preview, status, stop, and trace requests carry a `workspace_id`; omitting it remains backward compatible and selects the startup workspace.

`GET /api/v1/status` returns the effective built-in LLM runtime identity, for
example `{"status":"ok","llm":{"api_type":"openai","model":"gpt-4o-mini"}}`.
It never exposes API keys or provider endpoint URLs. The WebUI displays this
identity under the bqagent title when the endpoint is available.

`GET /` serves a self-contained, single-page chat UI. It is developed as vanilla TypeScript + Vite; the production build emits hashed JavaScript and CSS, and the entry page, assets, and favicons are all embedded into the same executable with `go:embed`. Runtime needs no Node.js, CDN, or static files on disk. Open `http://127.0.0.1:8080` in a browser and chat directly. The UI supports light/dark themes and safely renders Markdown headings, lists, task lists, tables, blockquotes, links, images, and copyable fenced code blocks, making README-style `.md` content easy to read. Replies stream token-by-token over Server-Sent Events from `POST /api/v1/webui/chat`; while a turn is running, the send button becomes a stop button backed by the channel-independent `POST /api/v1/chat/stop` endpoint, which cancels the active model request and tool execution identified by `turn_id`. The cancellation registry lives in the shared conversation service, so other channels can opt in later without WebUI-specific stop logic. `event: progress` reports iterations and tool activity. By default, WebUI has no fixed stage iteration budget, stage timeout, or whole-turn timeout, so one request continues until the model returns a final answer. Streaming LLM HTTP requests do not use an `http.Client` total timeout, but `LLM_STREAM_IDLE_TIMEOUT` watches them from request dispatch onward; response headers, SSE data, and heartbeats renew the timer. Browser disconnect, explicit stop, an opted-in stage deadline, and server shutdown also cancel the request. Non-streaming LLM requests retain the default two-minute client timeout. Duplicate tool calls and repeated failures remain protected by the loop guard, and the full loop still uses `AGENT_MAX_ITERATIONS` (default `1000`) as a runaway safety valve. Set a positive `WEBUI_STAGE_MAX_ITERATIONS` or `WEBUI_STAGE_TIMEOUT` to opt back into persisted stage summaries and the "继续" workflow. The web UI is enabled by default and can be disabled through the [environment variables](#environment-variables) configuration.

The composer also provides a reasoning-effort selector with **Auto**, **Low**, **Medium**, and **High**. Auto is the default and omits the upstream effort setting, preserving the provider or model default. The preference is stored in browser `localStorage` and sent explicitly with each WebUI turn; it is not written into session metadata. Non-auto values map to `reasoning_effort` for OpenAI Chat Completions, `reasoning.effort` for OpenAI Responses, and adaptive `thinking` plus `output_config.effort` for Anthropic Messages.

All three built-in protocols retry 429, 5xx, potentially transient network failures, and structured in-stream rate-limit/overload errors once. A streaming request stops being retryable after any text, reasoning, or tool-call delta, preventing generated output from being replayed. HTTP 429 and 503 honor `Retry-After` up to 10 seconds. When a 400/422 response explicitly rejects the reasoning parameter, the client retries without it and, after a successful fallback, remembers the protocol/endpoint/model limitation for the current process. Retries and downgrades are recorded only in model logs and Run Trace; they do not alter reply content.

The workspace button opens a desktop sidebar or mobile drawer. The directory picker in the sidebar title browses the bqagent host's user home, startup workspace, and roots added through `WEBUI_WORKSPACE_ROOTS`. A confirmed directory becomes the exact workspace root without creating `.agent`; use the `.agent` create button in the sidebar title when a workspace-specific secondary configuration is wanted. Each browser remembers its selected workspace and keeps a separate `session_id` per workspace; QQ, WeChat, and other channels remain on the startup workspace. Switching does not reload the selected directory's `.env`: model and process configuration remain fixed at server startup. Directories load lazily in pages; selecting a regular file switches the sidebar in place to a read-only preview, and the back button restores the previous tree position. UTF-8 text previews include up to the first 512 KiB, while PNG, JPEG, GIF, and WebP previews are limited to 3 MiB; other binary files show metadata only. The tree refreshes after every completed chat turn and also has a manual refresh button. Changes made by an external program while the UI is idle require a manual refresh. Everything except a `.git` component is visible—including `.env`, `.agent`, and `.gitignore`-excluded content—so do not expose the WebUI to untrusted users. Symbolic links are listed but cannot be expanded, previewed, selected as a workspace, or attached from the explorer. Allowing the user home as a selection root means the WebUI can make any ordinary directory beneath it an Agent tool boundary; never expose an unauthenticated WebUI to untrusted users.

The “+” button in the WebUI composer can upload browser-local files or reference paths inside the server workspace. A turn accepts up to 5 files, 2 MiB each and 6 MiB total. Uploads are stored under `.agent/uploads/<session-id>/` without overwriting same-name files. UTF-8 text is inlined into the turn context up to 64 KiB per file, with a truncation note and the full `read_file`-accessible path; binary files contribute path metadata only. Server paths must remain inside the workspace and cannot escape through `..` components or symbolic links.

`/api/v1/serverchan/chat` is the existing sendkey-based push adapter: it generates a reply and forwards it through ServerChan using the `text` / `desp` / `sendkey` shape from the Go demo.

`/api/v1/serverchan/bot/webhook` is the conversational webhook endpoint for ServerChan Bot / WeChat replies. It accepts the Bot webhook JSON update format, maps each inbound `chat_id` onto a persisted bqagent session, and sends the assistant reply through the configured Bot credentials. Optional webhook authentication is documented under [environment variables](#environment-variables).

`-d` is a shortcut for `--server --background`. Both forms run this server in the background and write service logs to `~/.agent/server/server.log`. Persistent WeChat iLink, QQ Bot, and ServerChan Bot state is also kept globally under `~/.agent/server/`, never in a workspace `.agent`. For real webhook use, expose `/api/v1/serverchan/bot/webhook` through a public HTTPS endpoint or reverse proxy.

By default the loop behaves like an auto-compacting agent: when the conversation
approaches the input-token budget it summarizes (compacts) the older turns and
**continues** on the compacted context, rather than stopping at a fixed turn
count. The iteration cap is therefore just a runaway safety valve (defaults to a
high `1000`). Summarization is enabled
by default. All context budgets and summary-model overrides are listed in the [environment variables](#environment-variables) chapter.

Sessions persist the channel/user mapping, status, messages, and resumable context checkpoints. On cancellation, timeout, or upstream failure, received reply text is saved with an unfinished-reply marker for resume. The turn remains interrupted or failed, and unfinished tool calls are never persisted as executed actions. This does not cover abrupt process termination or power loss. Default compact mode uses one `messages.jsonl`: messages are appended during a turn, then atomically replaced with bounded working context when the turn ends. If a legacy `working_messages.jsonl` exists, resume or startup maintenance selects recoverable content by modification time and readability, persists it in `messages.jsonl`, and removes the old snapshot. Newer messages from interrupted turns are preserved; startup maintenance skips running sessions, and a failed migration write leaves the legacy snapshot intact. Opt-in full mode retains append-only audit history and a separate working snapshot. Session log limits and storage modes are documented under [environment variables](#environment-variables). WeChat/iLink sends only the final reply because its context token must not be consumed by intermediate progress messages.

This is still intentionally a small implementation:

- the one-shot background task path is not a daemon
- no queue server
- MCP support is client-side and Streamable-HTTP-only (no stdio/SSE transports, no MCP server mode)
- no vector memory

## Run traces, evaluation, and feedback

Run tracing is disabled by default and can be enabled through the [environment variables](#environment-variables) configuration. Enabled runs persist a structured trace under `.agent/runs/<run-id>/`, including model/context versions, token usage, tool summaries, timings, errors, artifacts, verifier results, and feedback. When disabled, responses omit `run_id` and the run trace/feedback endpoints are unavailable.

```bash
go run ./cmd/eval --suite smoke --mode replay
go run ./cmd/eval --suite all --mode live --repeats 3
```

Use `/feedback up|down [comment]` or `POST /api/v1/runs/<run-id>/feedback` to rate a run.

## Subagents

`/agent spawn <claude|codex|cursor|opencode|pi> -- <task>` starts an asynchronous external agent in an isolated Git worktree. Use `/agent list`, `wait`, `result`, `interrupt`, `cancel`, `resume`, `apply`, and `cleanup` to manage it. Results and patches are persisted under `.agent/subagents/<id>/`; patches are never applied automatically.

## Structured memory

Structured, revisioned memory is stored in `.agent/memory/entries.jsonl` with source-run provenance, confidence, supersession, sensitive-entry confirmation, and Chinese/English full-text indexing. Existing Markdown memory is imported idempotently. `/memory list|search|confirm|compact` provides direct management. Recall uses `memory` `search` or `list`; `mem_save` remains a write-only fallback.

## Examples

```bash
# Ask the agent to inspect the repo
go run ./cmd/agent "what files are in this repository?"

# Interactive TUI
go run ./cmd/agent

# Use workspace rules and skills
go run ./cmd/agent "follow the workspace rules and summarize the available skills"

# Run in background
go run ./cmd/agent --background "scan the codebase and summarize the key files"
```

---

## License

MIT
