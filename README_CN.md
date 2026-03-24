# bqagent

[English](./README.md) | 中文

> *"问题不在于你看到了什么，而在于你看见了什么。"* — 梭罗

这是一个面向本地工作流的小型 Go 智能体。现在它在保留极简执行循环的同时，增加了工作区上下文、Markdown 技能定义、轻量记忆、规划、持久会话和最小后台模式。

## 它现在能做什么

bqagent 的核心仍然很简单：

1. 调用 OpenAI 兼容 chat completions 接口
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
- 使用 `--background` 启动最小后台会话
- 使用 `--server` 启动常驻 HTTP 对话服务，并可选通过 ServerChan 推送回复

## 安装

安装 Go 1.22+，然后构建 CLI：

```bash
go build -o bqagent ./cmd/agent
```

设置环境变量：

**macOS/Linux:**
```bash
export OPENAI_API_KEY='your-key-here'
export OPENAI_BASE_URL='https://api.openai.com/v1'  # 可选
export OPENAI_MODEL='gpt-4o-mini'  # 可选
```

**Windows (PowerShell):**
```powershell
$env:OPENAI_API_KEY='your-key-here'
$env:OPENAI_BASE_URL='https://api.openai.com/v1'  # 可选
$env:OPENAI_MODEL='gpt-4o-mini'  # 可选
```

**Windows (CMD):**
```cmd
set OPENAI_API_KEY=your-key-here
set OPENAI_BASE_URL=https://api.openai.com/v1
set OPENAI_MODEL=gpt-4o-mini
```

如果没有设置 `OPENAI_MODEL`，bqagent 默认使用 `MiniMax-M2.5`。

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
  - 可恢复会话的追加式 transcript
- `.agent/sessions/<session-id>/output.log`
  - 人类可读的执行日志
- `.agent/mcp.json`
  - 为后续 MCP 风格工具定义预留的路径
  - 当前 **还没有** 实现 live MCP 传输层

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
- `--server` 模式默认不暴露本地 shell / 文件 / memory 工具，只保留纯对话能力（以及可选的 `plan`）

## 会话与后台模式

`--chat` 启动交互式多轮对话模式。在终端中逐条输入消息，智能体会在整个会话过程中保持完整的对话上下文。输入 `/exit` 或按 Ctrl-D（EOF）结束会话。对话会自动持久化到 `.agent/sessions/` 目录下。

`--background` 会启动一个”最小后台会话”：通过同一二进制拉起子进程，并把输出写入：

- `.agent/sessions/<session-id>/meta.json`
- `.agent/sessions/<session-id>/messages.jsonl`
- `.agent/sessions/<session-id>/output.log`

命令会立即返回 session ID、session 目录和日志路径。

`--resume <session-id> "..."` 会读取 `messages.jsonl`，追加新的 follow-up 任务，然后从该上下文继续执行。

`--server` 会启动一个常驻 HTTP 服务，默认监听 `127.0.0.1:8080`，提供：

- `GET /healthz`
- `POST /api/v1/chat`
- `POST /api/v1/serverchan/chat`
- `POST /api/v1/serverchan/bot/webhook`

其中 `/api/v1/chat` 用于基于 `session_id` 的接口对话。

`/api/v1/serverchan/chat` 保留为现有的 sendkey 推送型接口：它会生成回复，然后按 demo 中的 `text` / `desp` / `sendkey` 语义把结果推送出去。

`/api/v1/serverchan/bot/webhook` 则用于 ServerChan Bot / 微信回复回流：它接收 Bot webhook 的 JSON update，用入站 `chat_id` 绑定到持久化的 bqagent session，并通过 `SERVERCHAN_BOT_TOKEN` 调用 Bot `sendMessage` API 把回复发回去。如果设置了 `SERVERCHAN_BOT_WEBHOOK_SECRET`，请求还必须带上 `X-Sc3Bot-Webhook-Secret`。

`--server --background` 会把该服务放到后台运行，并把服务日志写入 `.agent/server/server.log`。如果要真正接 webhook，需要把 `/api/v1/serverchan/bot/webhook` 通过公网 HTTPS 地址或反向代理暴露出去。

这里仍然刻意保持简单：

- 最小后台任务模式本身仍然不是 daemon
- 不做队列服务
- 不做 live MCP runtime
- 不做向量记忆

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
