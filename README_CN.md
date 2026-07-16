# bqagent

[English](./README.md) | 中文

> *"问题不在于你看到了什么，而在于你看见了什么。"* — 梭罗

这是一个面向本地工作流的小型 Go 智能体。现在它在保留极简执行循环的同时，增加了工作区上下文、Markdown 技能定义、轻量记忆、规划、持久会话、请求级上下文管理、基于 checkpoint 的压缩恢复，以及最小后台模式。

## 它现在能做什么

bqagent 的核心仍然很简单：

1. 通过 OpenAI Chat Completions、OpenAI Responses 或 Anthropic Messages 接口发送消息
2. 让模型选择工具
3. 在本地执行工具
4. 把工具结果回传给模型
5. 重复直到任务结束

新增的能力主要来自对 `agent-claudecode.py` 和 OpenClaw 思路的吸收：

- 基于 workspace 的 system prompt 装配
- 以 `.agent/AGENT.md`、`SOUL.md`、`TOOLS.md`、`USER.md` 作为主布局
- 兼容旧版 `workspace/AGENT.md`、`SOUL.md`、`TOOLS.md`、`USER.md`
- 兼容旧版 `workspace/memory/MEMORY.md` 和 `workspace/memory/YYYY-MM-DD.md`
- 继续支持 `.agent/rules/*.md` 和 `.agent/skills/*/SKILL.md`
- 使用 `--plan` 先拆步骤再执行
- 使用 `--chat` 进行交互式多轮对话
- 使用 `--resume` 恢复持久会话
- 对长对话做请求时上下文裁剪
- 可选地对旧对话做请求时摘要压缩
- 通过 checkpoint 进行紧凑恢复，同时保留原始会话历史
- 使用 `--background` 启动最小后台会话
- 使用 `--server` 启动常驻 HTTP 对话服务，并可选通过 ServerChan 推送回复

## 安装

安装 Go 1.22+，然后构建 CLI：

```bash
go build -o bqagent ./cmd/agent
```

## 环境变量配置

bqagent 会读取进程环境变量和工作区根目录的 `.env` 文件；进程中已经存在的同名变量优先于 `.env`。

推荐使用工作区 `.env`：

```dotenv
LLM_API_TYPE=openai
LLM_API_KEY=your-key-here
LLM_BASE_URL=https://api.openai.com/v1
LLM_MODEL=gpt-4o-mini
```

也可以直接在 shell 中设置：

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

### LLM 供应商

通用 `LLM_*` 配置优先于供应商兼容变量。

| 变量 | 默认值 | 说明 |
|---|---|---|
| `LLM_API_TYPE` | `openai` | 接口协议：`openai`、`openai-response` 或 `anthropic`。 |
| `LLM_API_KEY` | — | 通用 API Key；Server 模式必填。 |
| `LLM_BASE_URL` | 供应商默认值 | 通用供应商端点覆盖。 |
| `LLM_MODEL` | `MiniMax-M2.5` | 内置 LLM 的有效模型。 |
| `OPENAI_API_TYPE` | — | `LLM_API_TYPE` 的兼容别名。 |
| `OPENAI_API_KEY` | — | OpenAI 兼容 API Key 回退值。 |
| `OPENAI_BASE_URL` | 供应商默认值 | OpenAI 兼容端点回退值。 |
| `OPENAI_MODEL` | — | OpenAI 兼容模型回退值。 |
| `ANTHROPIC_API_KEY` | — | `anthropic` 协议使用的 Key 回退值。 |
| `ANTHROPIC_BASE_URL` | 供应商默认值 | Anthropic 端点回退值。 |
| `ANTHROPIC_MODEL` | — | Anthropic 模型回退值。 |

OpenAI Responses API 示例：

```dotenv
LLM_API_TYPE=openai-response
LLM_API_KEY=your-key-here
LLM_BASE_URL=https://api.openai.com/v1
LLM_MODEL=gpt-5
```

Anthropic Messages API 示例：

```dotenv
LLM_API_TYPE=anthropic
LLM_API_KEY=your-key-here
LLM_BASE_URL=https://api.anthropic.com/v1
LLM_MODEL=claude-sonnet-4-5
```

有效模型和 API 类型会注入每次内置 LLM 调用的 system prompt，并通过不包含密钥的 `GET /api/v1/status` 返回。

### 搜索

| 变量 | 默认值 | 说明 |
|---|---|---|
| `SEARCH_API_KEY` | — | Tavily 兼容搜索 Key，优先于 Firecrawl 配置。 |
| `SEARCH_BASE_URL` | 供应商默认值 | Tavily 兼容端点覆盖。 |
| `FIRECRAWL_API_KEY` | — | 未配置 `SEARCH_*` 时使用的 Firecrawl Key。 |
| `FIRECRAWL_BASE_URL` | 供应商默认值 | Firecrawl 端点覆盖。 |

`.agent/mcp.json` 中的 MCP header 值可以使用 `${NAME}` 引用任意环境变量，例如 `Bearer ${DASHSCOPE_API_KEY}`。

### Server 与渠道

| 变量 | 默认值 | 说明 |
|---|---|---|
| `WEBUI_ENABLED` | `true` | 设为 `false`、`0`、`no` 或 `off` 时关闭 `GET /`。 |
| `SERVERCHAN_BOT_TOKEN` | — | ServerChan Bot webhook 回复使用的 Token。 |
| `SERVERCHAN_BOT_WEBHOOK_SECRET` | — | 可选；设置后请求必须携带 `X-Sc3Bot-Webhook-Secret`。 |
| `QQ_BOT_ENABLED` | 自动 | 凭据存在时自动启用；false-like 值可强制关闭。 |
| `QQ_BOT_APP_ID` | — | QQ Bot 应用 ID。 |
| `QQ_BOT_CLIENT_SECRET` | — | QQ Bot Client Secret。 |
| `QQ_BOT_TOKEN_BASE_URL` | `https://bots.qq.com` | QQ Token 端点覆盖。 |
| `QQ_BOT_API_BASE_URL` | `https://api.sgroup.qq.com` | QQ API 与 Gateway 端点覆盖。 |
| `WEIXIN_ILINK_ENABLED` | `true` | 设为 false-like 值时关闭微信 iLink 渠道。 |
| `WEIXIN_ILINK_BASE_URL` | `https://ilinkai.weixin.qq.com` | iLink API 端点覆盖。 |
| `WEIXIN_ILINK_CHANNEL_VERSION` | `1.0.2` | iLink 渠道协议版本。 |
| `WEIXIN_ILINK_CDN_BASE_URL` | `https://novac2c.cdn.weixin.qq.com/c2c` | 入站媒体 CDN 覆盖。 |

### 运行时、上下文、Session 与 Trace

| 变量 | 默认值 | 说明 |
|---|---|---|
| `AGENT_MAX_ITERATIONS` | `1000` | 全局循环失控保险上限。 |
| `CHANNEL_AGENT_MAX_ITERATIONS` | `30` | 渠道/WebUI 单轮最大迭代数。 |
| `CHANNEL_TURN_TIMEOUT` | `10m` | 渠道整轮超时。 |
| `CHANNEL_STAGE_MAX_ITERATIONS` | `20` | QQ/iLink 生成阶段 checkpoint 前的迭代预算。 |
| `CHANNEL_STAGE_TIMEOUT` | `90s` | QQ/iLink 阶段时间预算。 |
| `WEBUI_STAGE_MAX_ITERATIONS` | `20` | WebUI 生成阶段 checkpoint 前的迭代预算。 |
| `WEBUI_STAGE_TIMEOUT` | `90s` | WebUI 阶段时间预算。 |
| `CONTEXT_MANAGEMENT_ENABLED` | `true` | 启用请求时上下文预算管理。 |
| `CONTEXT_MAX_INPUT_TOKENS` | `24000` | 模型输入 token 估算上限。 |
| `CONTEXT_TARGET_INPUT_TOKENS` | `20000` | 裁剪或摘要后的目标大小。 |
| `CONTEXT_RESPONSE_RESERVE_TOKENS` | `4000` | 为模型回复预留的 token。 |
| `CONTEXT_KEEP_LAST_TURNS` | `6` | 压缩时保留的最近轮次。 |
| `CONTEXT_SUMMARIZATION_ENABLED` | `true` | 启用旧对话摘要。 |
| `CONTEXT_SUMMARY_TRIGGER_TOKENS` | `20000` | 触发摘要的输入大小。 |
| `CONTEXT_SUMMARY_MODEL` | 主模型 | 可选的低成本摘要模型。 |
| `SESSION_TRANSCRIPT_MODE` | `compact` | `compact` 限制 `messages.jsonl`；`full` 保留 append-only 审计历史。 |
| `SESSION_OUTPUT_MAX_BYTES` | `1048576` | 每个 `output.log` 保留的尾部大小；`0` 禁用裁剪。 |
| `RUN_TRACE_ENABLED` | `false` | 保存 `.agent/runs/<run-id>/` Trace 并启用运行反馈接口。 |

### 外部编码 Agent

对 `CLAUDE`、`CODEX`、`CURSOR`、`OPENCODE` 中的每个名称，可使用 `AGENT_<NAME>_ACP_CMD` 和 `AGENT_<NAME>_ACP_ARGS` 覆盖 ACP 启动命令。CLI 传输目前只为 Claude 和 Codex 实现，可通过 `AGENT_<NAME>_CLI_CMD` 和 `AGENT_<NAME>_CLI_ARGS` 覆盖。

Claude 默认使用 `claude -p --output-format json`；Codex 默认使用 `codex exec --json --skip-git-repo-check`。OpenCode 默认通过 `opencode acp` 使用 ACP，Cursor 仍需显式配置 ACP 传输。启动 bqAgent 的进程必须能从 PATH 找到 `opencode`；安装 OpenCode 或修改传输环境变量后，需要重启常驻 chat/server 进程以重新探测。

在 `--chat` 或 `--server` 会话中，使用 `/opencode <任务>` 将当前轮次路由到 OpenCode；后续普通消息会保持绑定，直到通过 `/default` 返回内置 Agent。OpenCode 仅支持 ACP；配置 `AGENT_OPENCODE_CLI_CMD/ARGS` 不会启用 CLI 传输。

## 快速开始

```bash
# 默认单次任务
go run ./cmd/agent "列出当前仓库里的所有 Go 文件"

# 交互式多轮对话
go run ./cmd/agent --chat

# 带初始任务的交互对话
go run ./cmd/agent --chat "读取 README.md 并总结"

# 先规划再执行
go run ./cmd/agent --plan "梳理当前项目结构并总结"

# 启动后台会话
go run ./cmd/agent --background "读取 README.md 并总结"

# 启动常驻 HTTP 服务
go run ./cmd/agent --server

# 后台启动 HTTP 服务
go run ./cmd/agent --server --background

# 恢复之前的会话
go run ./cmd/agent --resume <session-id> "基于刚才的结果继续"

# 以对话模式恢复之前的会话
go run ./cmd/agent --chat --resume <session-id>
```

如果不传任何参数，bqagent 仍然会默认使用 `Hello`。

## 工作区布局

bqagent 会从当前目录向上查找，直到命中以下任一标记为止：

- `.agent`
- `.git`
- `go.mod`

找到后就把它当作 workspace root。相对路径工具和 shell 命令都会以这个目录为基准执行。

工作区主布局现在使用 `.agent/`。如果对应的 `.agent/` 文件不存在，仍会兼容读取旧版 `workspace/` 布局。

```text
project/
├─ .agent/
│  ├─ AGENT.md
│  ├─ SOUL.md
│  ├─ TOOLS.md
│  ├─ USER.md
│  ├─ memory/
│  │  ├─ MEMORY.md
│  │  └─ YYYY-MM-DD.md
│  ├─ rules/
│  │  └─ *.md
│  ├─ skills/
│  │  └─ <skill>/
│  │     └─ SKILL.md
│  ├─ sessions/
│  │  └─ <session-id>/
│  │     ├─ meta.json
│  │     ├─ messages.jsonl
│  │     ├─ context_checkpoint.json
│  │     └─ output.log
│  └─ mcp.json
├─ workspace/  # 兼容旧布局
│  ├─ AGENT.md
│  ├─ SOUL.md
│  ├─ TOOLS.md
│  ├─ USER.md
│  └─ memory/
│     ├─ MEMORY.md
│     └─ YYYY-MM-DD.md
└─ agent_memory.md
```

### 这些文件分别做什么

- `.agent/AGENT.md`、`SOUL.md`、`TOOLS.md`、`USER.md`
  - OpenClaw 风格的上下文文件
  - 当前会默认加载进 system prompt
  - 若 `.agent/` 与 `workspace/` 同时存在，优先使用 `.agent/`
- `.agent/memory/MEMORY.md`
  - 长期记忆文件
  - 启动时会加载进 prompt
- `.agent/memory/YYYY-MM-DD.md`
  - 日记型记忆文件
  - 启动时会自动加载今天和昨天的文件
  - 新的任务结果会优先追加到今天的 `.agent/memory/YYYY-MM-DD.md`
- `workspace/AGENT.md`、`workspace/memory/*`
  - 旧布局兼容路径
  - 仅当对应的 `.agent/` 文件不存在时才会读取
- `agent_memory.md`
  - 兼容旧布局的轻量记忆文件
  - 当 `workspace/memory/MEMORY.md` 不存在时仍会读取；若两者都存在，会一并注入 prompt
- `.agent/rules/*.md`
  - 规则全文注入 prompt
- `.agent/skills/*/SKILL.md`
  - Markdown 技能定义，当前会以摘要形式注入 prompt
- `.agent/sessions/<session-id>/messages.jsonl`
  - 当前执行轮的 journal；compact 模式会在每轮结束后将其收敛为 bounded snapshot
- `.agent/sessions/<session-id>/working_messages.jsonl`
  - 正常恢复使用的稳定 bounded snapshot
- `.agent/sessions/<session-id>/context_checkpoint.json`
  - 保存“摘要 + 最近 tail”的紧凑 checkpoint，用于恢复时重建工作上下文
- `.agent/sessions/<session-id>/output.log`
  - 人类可读的执行日志
- `.agent/mcp.json`
  - MCP 服务器配置（`mcpServers` 映射）。这里配置的 **Streamable HTTP** 服务器会在启动时连接，
    通过 `tools/list` 发现其工具，并以 `mcp__<server>__<tool>` 的形式暴露给大模型。header 环境变量展开
    统一说明在[环境变量配置](#环境变量配置)章节。
  - 发现过程是 best-effort：标记 `"disabled": true`、文件缺失或服务器不可达都会被跳过（仅记录一条
    警告），不会阻塞启动。当前仅支持 Streamable HTTP 传输。

## 内建工具

默认内建工具：

- `execute_bash`
- `read_file`
- `write_file`

启用 planner 后，模型还可以调用：

- `plan`

当前行为说明：

- 未知工具会作为 `Error: Unknown tool '...'` 返回给模型
- 非法 JSON 工具参数会直接让当前运行失败
- 文件读写失败也会直接让当前运行失败
- 相对路径的 `read_file` / `write_file` 会按 workspace root 解析
- `execute_bash` 也会在 workspace root 下运行
- `--server` 和 `--chat` 现在共用同一套内置本地工具，包括 shell、文件、网页搜索和 memory 工具

## 会话与后台模式

`--chat` 启动交互式多轮对话模式。在终端中逐条输入消息，智能体会在整个会话过程中持续接续上下文。输入 `/exit` 或按 Ctrl-D（EOF）结束会话。对话会自动持久化到 `.agent/sessions/` 目录下。

长对话现在会在每次请求模型前做上下文管理：

- 已完成的历史 tool call 脚手架不会继续带入请求 payload
- 旧对话会按目标输入预算裁剪
- 可选地把更早的普通对话压缩成一条 synthetic summary message
- 持久化受预算约束的 working snapshot，确保会话可以恢复

`--background` 会启动一个”最小后台会话”：通过同一二进制拉起子进程，并把输出写入：

- `.agent/sessions/<session-id>/meta.json`
- `.agent/sessions/<session-id>/messages.jsonl`
- `.agent/sessions/<session-id>/context_checkpoint.json`（当已生成摘要 checkpoint 时）
- `.agent/sessions/<session-id>/output.log`

命令会立即返回 session ID、session 目录和日志路径。

`--resume <session-id> "..."` 会恢复已有会话、刷新当前 system prompt、在兼容时复用 `context_checkpoint.json`，再追加新的 follow-up 任务并继续执行。

`--server` 会启动一个常驻 HTTP 服务，默认监听 `127.0.0.1:8080`，提供：

- `GET /`（内嵌网页对话界面）
- `GET /healthz`
- `GET /api/v1/status`（内置 LLM 的有效 API 类型和模型）
- `POST /api/v1/chat`
- `POST /api/v1/webui/chat`
- `POST /api/v1/chat/stop`
- `POST /api/v1/serverchan/chat`
- `POST /api/v1/serverchan/bot/webhook`

其中 `/api/v1/chat` 用于基于 `session_id` 的接口对话。

`GET /api/v1/status` 返回内置 LLM 的有效运行时身份，例如 `{"status":"ok","llm":{"api_type":"openai","model":"MiniMax-M2.5"}}`。该接口不会暴露 API Key 或供应商端点 URL；WebUI 会在 bqagent 标题下展示这项信息。

`GET /` 提供一个自包含的单页网页对话界面（HTML/CSS/JS 全部内嵌进二进制，无外部依赖）。浏览器打开 `http://127.0.0.1:8080` 即可直接对话。界面支持明暗主题，并会安全渲染 Markdown 标题、列表、任务列表、表格、引用、链接、图片与带复制按钮的代码块，适合直接阅读 README 等 `.md` 内容。回复通过 `POST /api/v1/webui/chat` 以 Server-Sent Events 逐字流式返回；发送后按钮会切换为停止按钮，通过与渠道无关的 `POST /api/v1/chat/stop` 接口按 `turn_id` 取消当前模型请求和工具执行。取消注册表位于共享对话服务中，其他通道后续接入时无需依赖 WebUI。`event: progress` 会持续报告迭代轮次、工具活动和阶段 checkpoint。长任务达到阶段预算后会返回并持久化阶段总结，用户回复“继续”即可沿用同一 `session_id` 继续，而不会重新探索。该网页渠道默认开启，可在[环境变量配置](#环境变量配置)中关闭。

`/api/v1/serverchan/chat` 保留为现有的 sendkey 推送型接口：它会生成回复，然后按 demo 中的 `text` / `desp` / `sendkey` 语义把结果推送出去。

`/api/v1/serverchan/bot/webhook` 则用于 ServerChan Bot / 微信回复回流：它接收 Bot webhook 的 JSON update，用入站 `chat_id` 绑定到持久化的 bqagent session，并通过已配置的 Bot 凭据发送回复。可选的 webhook 鉴权配置集中在[环境变量配置](#环境变量配置)章节。

`--server --background` 会把该服务放到后台运行，并把服务日志写入 `.agent/server/server.log`。如果要真正接 webhook，需要把 `/api/v1/serverchan/bot/webhook` 通过公网 HTTPS 地址或反向代理暴露出去。

默认情况下循环表现为"自动压缩续跑"：当对话接近输入 token 预算时，会把更早的对话摘要（压缩）后**继续**在压缩上下文上推进，而不是在固定轮数处停下。因此轮数上限只是失控保险（默认很高，为 `1000`）。所有上下文预算和摘要模型覆盖均集中在[环境变量配置](#环境变量配置)章节。

Session 用于保存会话 ID、渠道用户映射、消息、任务状态和可恢复的上下文 checkpoint。默认 compact 模式会在每轮结束后用受预算控制的 `working_messages.jsonl` 收敛 `messages.jsonl`，避免原始工具结果无限累计；也可选择 append-only 完整审计模式。若任务中断后 transcript 比 working snapshot 更新，恢复时会优先使用较新的 transcript。Session 日志上限和存储模式集中在[环境变量配置](#环境变量配置)章节。微信 iLink 只发送最终回复，不发送中间 progress，以免同一个 `context_token` 被提前消耗。

这里仍然刻意保持简单：

- 最小后台任务模式本身仍然不是 daemon
- 不做队列服务
- MCP 仅做客户端、且只支持 Streamable HTTP 传输（不支持 stdio/SSE，也不做 MCP server 端）
- 不做向量记忆

## RunTrace、评估与反馈

RunTrace 默认关闭，可在[环境变量配置](#环境变量配置)中开启。开启后，每次任务会在 `.agent/runs/<run-id>/` 保存结构化追踪，包括模型和上下文版本、token、工具摘要、耗时、错误分类、artifact 和 verifier。关闭时，响应不返回 `run_id`，运行追踪与反馈接口不可用。

```bash
go run ./cmd/eval --suite smoke --mode replay
go run ./cmd/eval --suite all --mode replay
go run ./cmd/eval --suite all --mode live --repeats 3

/feedback up 很有帮助
/feedback <run-id> down 没有修改目标文件
```

## 子 Agent

`/agent` 会在独立 Git worktree 中异步运行 Claude、Codex、Cursor 或 OpenCode；结果以回复、日志和 `diff.patch` 返回，不会自动修改主工作区。

```text
/agent spawn codex -- 修复指定测试并说明原因
/agent list --status running
/agent wait <id> --timeout 30s
/agent result <id>
/agent interrupt <id>
/agent resume <id> -- 继续并补充测试
/agent apply <id>
/agent cleanup <id>
```

主工作区默认必须干净；只有显式传入 `--include-dirty` 才会把当前 tracked diff 和安全的未跟踪文件复制到子 worktree。

## 结构化 Memory

Memory 的事实源为 `.agent/memory/entries.jsonl`，支持 revision、来源 run、置信度、supersedes、敏感确认和中文 n-gram 检索。旧的 `MEMORY.md` 与最近两天 daily memory 会幂等迁移并保留原文件。

```text
/memory list
/memory search Go 项目约定
/memory confirm <mem-id>
/memory compact
```

旧的 `mem_save`、`mem_get` 工具继续可用，并会转接到结构化存储。

## 示例

```bash
# 让智能体检查仓库
go run ./cmd/agent "当前仓库里有哪些文件？"

# 交互式对话
go run ./cmd/agent --chat

# 使用 workspace 规则和技能
go run ./cmd/agent "遵循当前 workspace 规则并总结可用技能"

# 先规划再执行
go run ./cmd/agent --plan "分析当前 Go 项目并说明主要包的职责"

# 后台运行
go run ./cmd/agent --background "扫描代码库并总结关键文件"
```

---

## 许可证

MIT
