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
- 兼容 `workspace/AGENT.md`、`SOUL.md`、`TOOLS.md`、`USER.md`
- 兼容 `workspace/memory/MEMORY.md` 和 `workspace/memory/YYYY-MM-DD.md`
- 继续支持 `.agent/rules/*.md` 和 `.agent/skills/*/SKILL.md`
- 使用 `--plan` 先拆步骤再执行
- 使用 `--chat` 进行交互式多轮对话
- 使用 `--resume` 恢复持久会话
- 使用 `--background` 启动最小后台会话

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

可选的工作区文件布局现在支持两种：

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

### 这些文件分别做什么

- `workspace/AGENT.md`、`SOUL.md`、`TOOLS.md`、`USER.md`
  - OpenClaw 风格的上下文文件
  - 当前会默认加载进 system prompt
- `workspace/memory/MEMORY.md`
  - 长期记忆文件
  - 启动时会加载进 prompt
- `workspace/memory/YYYY-MM-DD.md`
  - 日记型记忆文件
  - 启动时会自动加载今天和昨天的文件
  - 当 `workspace/` 存在时，新的任务结果会优先追加到今天的这个文件
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

## 会话与后台模式

`--chat` 启动交互式多轮对话模式。在终端中逐条输入消息，智能体会在整个会话过程中保持完整的对话上下文。输入 `/exit` 或按 Ctrl-D（EOF）结束会话。对话会自动持久化到 `.agent/sessions/` 目录下。

`--background` 会启动一个”最小后台会话”：通过同一二进制拉起子进程，并把输出写入：

- `.agent/sessions/<session-id>/meta.json`
- `.agent/sessions/<session-id>/messages.jsonl`
- `.agent/sessions/<session-id>/output.log`

命令会立即返回 session ID、session 目录和日志路径。

`--resume <session-id> "..."` 会读取 `messages.jsonl`，追加新的 follow-up 任务，然后从该上下文继续执行。

这里刻意保持简单：

- 不做 daemon
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
