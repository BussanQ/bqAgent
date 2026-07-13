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

设置环境变量：

`LLM_API_TYPE` 用于选择接口协议，支持 `openai`（默认）、`openai-response`
和 `anthropic`。原有 `OPENAI_*` 环境变量继续兼容；通用的 `LLM_*` 变量优先级更高。

**macOS/Linux:**
```bash
export OPENAI_API_KEY='your-key-here'
export OPENAI_BASE_URL='https://api.openai.com/v1'  # 可选
export OPENAI_MODEL='gpt-4o-mini'  # 可选
export LLM_API_TYPE='openai'  # 可选
```

**Windows (PowerShell):**
```powershell
$env:OPENAI_API_KEY='your-key-here'
$env:OPENAI_BASE_URL='https://api.openai.com/v1'  # 可选
$env:OPENAI_MODEL='gpt-4o-mini'  # 可选
$env:LLM_API_TYPE='openai'  # 可选
```

**Windows (CMD):**
```cmd
set OPENAI_API_KEY=your-key-here
set OPENAI_BASE_URL=https://api.openai.com/v1
set OPENAI_MODEL=gpt-4o-mini
set LLM_API_TYPE=openai
```

也可以把同样的变量写在工作区根目录下的 `.env` 文件中，bqagent 会在启动时自动加载。

```dotenv
OPENAI_API_KEY=your-key-here
OPENAI_BASE_URL=https://api.openai.com/v1
OPENAI_MODEL=gpt-4o-mini
LLM_API_TYPE=openai
```

OpenAI Responses API 示例：

```dotenv
LLM_API_TYPE=openai-response
OPENAI_API_KEY=your-key-here
OPENAI_BASE_URL=https://api.openai.com/v1
OPENAI_MODEL=gpt-5
```

Anthropic Messages API 示例：

```dotenv
LLM_API_TYPE=anthropic
ANTHROPIC_API_KEY=your-key-here
ANTHROPIC_BASE_URL=https://api.anthropic.com/v1
ANTHROPIC_MODEL=claude-sonnet-4-5
```

也可以使用 `LLM_API_KEY`、`LLM_BASE_URL`、`LLM_MODEL` 替代供应商专用变量。
`OPENAI_API_TYPE` 可作为 `LLM_API_TYPE` 的兼容别名。

如果 shell 里已经设置了同名环境变量，则以 shell 中的值为准，不会被 `.env` 覆盖。

如果没有设置任何模型变量，bqagent 默认使用 `MiniMax-M2.5`。

如果要启用 ServerChan Bot webhook 对话，还需要设置：

```bash
export SERVERCHAN_BOT_TOKEN='your-bot-token'
export SERVERCHAN_BOT_WEBHOOK_SECRET='your-webhook-secret'  # 可选但推荐
```

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
  - 可恢复会话的追加式原始 transcript
- `.agent/sessions/<session-id>/context_checkpoint.json`
  - 保存“摘要 + 最近 tail”的紧凑 checkpoint，用于恢复时重建工作上下文
  - 不会替换或重写原始 `messages.jsonl`
- `.agent/sessions/<session-id>/output.log`
  - 人类可读的执行日志
- `.agent/mcp.json`
  - MCP 服务器配置（`mcpServers` 映射）。这里配置的 **Streamable HTTP** 服务器会在启动时连接，
    通过 `tools/list` 发现其工具，并以 `mcp__<server>__<tool>` 的形式暴露给大模型。header 值支持
    `${ENV}` 展开（例如 `Bearer ${DASHSCOPE_API_KEY}`）。
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
- 即使请求 payload 被缩短，磁盘上的原始会话历史仍然保留

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
- `POST /api/v1/chat`
- `POST /api/v1/webui/chat`
- `POST /api/v1/chat/stop`
- `POST /api/v1/serverchan/chat`
- `POST /api/v1/serverchan/bot/webhook`

其中 `/api/v1/chat` 用于基于 `session_id` 的接口对话。

`GET /` 提供一个自包含的单页网页对话界面（HTML/CSS/JS 全部内嵌进二进制，无外部依赖）。浏览器打开 `http://127.0.0.1:8080` 即可直接对话。界面支持明暗主题，并会安全渲染 Markdown 标题、列表、任务列表、表格、引用、链接、图片与带复制按钮的代码块，适合直接阅读 README 等 `.md` 内容。回复通过 `POST /api/v1/webui/chat` 以 Server-Sent Events 逐字流式返回；发送后按钮会切换为停止按钮，通过与渠道无关的 `POST /api/v1/chat/stop` 接口按 `turn_id` 取消当前模型请求和工具执行。取消注册表位于共享对话服务中，其他通道后续接入时无需依赖 WebUI。`event: progress` 会持续报告迭代轮次、工具活动和阶段 checkpoint。长任务达到阶段预算后会返回并持久化阶段总结，用户回复“继续”即可沿用同一 `session_id` 继续，而不会重新探索。该网页渠道**默认开启**；设置 `WEBUI_ENABLED=false` 可关闭（此时 `GET /` 返回 404）。

`/api/v1/serverchan/chat` 保留为现有的 sendkey 推送型接口：它会生成回复，然后按 demo 中的 `text` / `desp` / `sendkey` 语义把结果推送出去。

`/api/v1/serverchan/bot/webhook` 则用于 ServerChan Bot / 微信回复回流：它接收 Bot webhook 的 JSON update，用入站 `chat_id` 绑定到持久化的 bqagent session，并通过 `SERVERCHAN_BOT_TOKEN` 调用 Bot `sendMessage` API 把回复发回去。如果设置了 `SERVERCHAN_BOT_WEBHOOK_SECRET`，请求还必须带上 `X-Sc3Bot-Webhook-Secret`。

`--server --background` 会把该服务放到后台运行，并把服务日志写入 `.agent/server/server.log`。如果要真正接 webhook，需要把 `/api/v1/serverchan/bot/webhook` 通过公网 HTTPS 地址或反向代理暴露出去。

默认情况下循环表现为"自动压缩续跑"：当对话接近输入 token 预算时，会把更早的对话摘要（压缩）后**继续**在压缩上下文上推进，而不是在固定轮数处停下。因此轮数上限只是失控保险（默认很高，为 `1000`）；磁盘上的原始 transcript 仍保持完整。摘要默认开启——长任务可设 `CONTEXT_SUMMARY_MODEL` 用更便宜的模型做摘要，或设 `CONTEXT_SUMMARIZATION_ENABLED=false` 退回纯裁剪。

Session 用于保存会话 ID、渠道用户映射、完整消息记录、任务状态和可恢复的上下文 checkpoint。`messages.jsonl` 作为审计与排障记录可以长期保留；各通道实际恢复和推理优先读取受预算控制的 `working_messages.jsonl`，请求侧还会优先保留系统提示、摘要和最新用户请求，并对超大工具结果执行硬裁剪。因此原始 session 文件可以较大，但每轮不再重新加载或发送全部历史。微信 iLink 只发送最终回复，不发送中间 progress，以免同一个 `context_token` 被提前消耗。

上下文管理可通过环境变量配置：

- `AGENT_MAX_ITERATIONS`（循环失控保险上限，对所有模式生效，默认 `1000`）
- `CHANNEL_AGENT_MAX_ITERATIONS`（渠道/WebUI 单轮迭代上限，默认 `30`）
- `CHANNEL_TURN_TIMEOUT`（渠道整轮超时，默认 `10m`）
- `CHANNEL_STAGE_MAX_ITERATIONS`（QQ/微信 iLink 阶段迭代预算，默认 `20`）
- `CHANNEL_STAGE_TIMEOUT`（QQ/微信 iLink 阶段时间预算，默认 `90s`）
- `WEBUI_STAGE_MAX_ITERATIONS`（WebUI 阶段迭代预算，默认 `20`）
- `WEBUI_STAGE_TIMEOUT`（WebUI 阶段时间预算，默认 `90s`）
- `CONTEXT_MANAGEMENT_ENABLED`
- `CONTEXT_MAX_INPUT_TOKENS`
- `CONTEXT_TARGET_INPUT_TOKENS`
- `CONTEXT_RESPONSE_RESERVE_TOKENS`
- `CONTEXT_KEEP_LAST_TURNS`
- `CONTEXT_SUMMARIZATION_ENABLED`
- `CONTEXT_SUMMARY_TRIGGER_TOKENS`
- `CONTEXT_SUMMARY_MODEL`

这里仍然刻意保持简单：

- 最小后台任务模式本身仍然不是 daemon
- 不做队列服务
- MCP 仅做客户端、且只支持 Streamable HTTP 传输（不支持 stdio/SSE，也不做 MCP server 端）
- 不做向量记忆

## RunTrace、评估与反馈

每次任务都会在 `.agent/runs/<run-id>/` 保存结构化追踪，包括模型和上下文版本、token、工具摘要、耗时、错误分类、artifact 和 verifier。HTTP 与 WebUI 回复会返回 `run_id`。

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
