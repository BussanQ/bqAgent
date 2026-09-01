# bqagent 文档 / Documentation

[中文](#中文文档) | [English](#english-documentation)

<a id="中文文档"></a>

# 中文文档

## 快速开始

```bash
# 单次任务
go run ./cmd/agent "列出当前仓库里的所有 Go 文件"

# 默认启动交互式多轮对话（也可显式传入 --chat）
go run ./cmd/agent

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

# 等价的快捷方式
go run ./cmd/agent -d

# 通过运行中的服务发起微信 iLink 登录
go run ./cmd/agent --ilink-login

# 查询微信 iLink 登录状态
go run ./cmd/agent --ilink-status

# 恢复之前的会话
go run ./cmd/agent --resume <session-id> "基于刚才的结果继续"

# 以对话模式恢复之前的会话
go run ./cmd/agent --chat --resume <session-id>
```

如果不传任何参数，bqagent 默认启动交互式多轮对话，等同于传入 `--chat`。

## 工作区布局

bqagent 会从当前目录向上查找，直到命中以下任一工作区标记为止（用户主目录中的全局 `~/.agent` 不作为工作区标记）：

- `.agent`
- `.git`
- `go.mod`

找到后就把它当作 workspace root。相对路径工具和 shell 命令都会以这个目录为基准执行。

主配置固定使用 `~/.agent/`，启动时会在这里补齐默认文件。工作区中的 `.agent/` 是可选的次级配置层，不会在切换工作区时自动创建；存在时，其 `AGENT.md`、`SOUL.md`、`USER.md`、memory 内容、skills 和 `mcp.json` 会在全局配置之后合并加载，同名工作区 Skill 和 MCP server 覆盖全局定义。WebUI 侧栏中的创建按钮只会生成 `.agent/memory/`、`mcp.json`、`AGENT.md`、`SOUL.md` 和 `USER.md`。除此之外，工作区 `.agent/memory/` 只会在发现已有 Markdown memory 需要迁移或首次显式写入 memory 时按需创建；对空 memory 的只读查询不会创建目录。

```text
~/.agent/
├─ sessions/
│  └─ <session-id>/
│     ├─ meta.json
│     ├─ messages.jsonl
│     ├─ working_messages.jsonl
│     ├─ context_checkpoint.json
│     └─ output.log
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
  - 全局 `~/.agent/skills` 与工作区 `.agent/skills` 会合并；目录名称相同时工作区版本完整覆盖全局版本
  - system prompt 只索引规范名称、frontmatter `description` 和工作区相对路径
  - 当 Skill 与任务相关时，模型通过 `read_file` 按需读取完整 `SKILL.md`
  - 显式 `/skill <名称或别名> [参数]` 及消息开头的 Skill ID/alias 都进入同一个主会话循环
- `~/.agent/sessions/<session-id>/messages.jsonl`
  - 当前执行轮的 journal；compact 模式会在每轮结束后将其收敛为 bounded snapshot
- `~/.agent/sessions/<session-id>/working_messages.jsonl`
  - 正常恢复使用的稳定 bounded snapshot
- `~/.agent/sessions/<session-id>/context_checkpoint.json`
  - 保存“摘要 + 最近 tail”的紧凑 checkpoint，用于恢复时重建工作上下文
- `~/.agent/sessions/<session-id>/output.log`
  - 人类可读的执行日志
- `.agent/mcp.json`
  - MCP 服务器配置（`mcpServers` 映射）。这里配置的 **Streamable HTTP** 服务器会在启动时连接，
    通过 `tools/list` 发现其工具，并以 `mcp__<server>__<tool>` 的形式暴露给大模型。header 环境变量展开
    统一说明在[环境变量配置](../README_CN.md#环境变量配置)章节。
  - 默认文件不配置远程 server。Server URL 必须是字面量 `http` 或 `https` URL；只有 header 值可以使用
    allowlist 中的环境变量占位符。
  - 发现过程是 best-effort：标记 `"disabled": true`、文件缺失、服务器不可达或配置非法都会被跳过（仅记录一条
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

Workspace Skill 使用渐进式披露。推荐在 Skill 顶部声明元数据：

```yaml
---
description: 汇总仓库改动并生成简洁的发布说明。
aliases:
  - release-notes
---
```

发现阶段的 system prompt 不包含 Skill 正文或 alias。模型会先对索引中的 `.agent/skills/<name>/SKILL.md` 路径调用 `read_file`；读取时工作区 Skill 优先、全局 Skill 兜底，再在当前主会话中遵循完整指令。`/skill <名称或别名> [参数]` 只是显式选择 Skill 的快捷入口，不会创建独立 Skill Runner。`install_skill` 默认安装到全局目录，传入 `target=workspace` 可安装到当前工作区。

## 会话与后台模式

`--chat`（以及不带参数启动）在真实终端中进入始终流式的内联 TUI：已完成消息进入原生 scrollback，底部保留输入框、候选面板、排队区和状态栏，不切换备用屏幕。超过 5 次的工具调用会合并为可点击的详情组，仅在该工具组可交互时临时开启鼠标追踪。stdin/stdout 重定向或 `TERM=dumb` 时自动回退原有逐行模式。对话会自动持久化到全局 `~/.agent/sessions/`；恢复会校验工作区边界并按 200 KiB 显示预算回放最近用户/助手文本。快捷键、命令面板、粘贴 Chip、队列、历史文件和 `NO_COLOR` 说明见[内联 TUI 指南](./TUI.md)。

长对话现在会在每次请求模型前做上下文管理：

- 已完成的历史 tool call 脚手架不会继续带入请求 payload
- 旧对话会按目标输入预算裁剪
- 可选地把更早的普通对话压缩成一条 synthetic summary message
- 持久化受预算约束的 working snapshot，确保会话可以恢复

`--background` 会启动一个”最小后台会话”：通过同一二进制拉起子进程，并把输出写入：

- `~/.agent/sessions/<session-id>/meta.json`
- `~/.agent/sessions/<session-id>/messages.jsonl`
- `~/.agent/sessions/<session-id>/context_checkpoint.json`（当已生成摘要 checkpoint 时）
- `~/.agent/sessions/<session-id>/output.log`

命令会立即返回 session ID、session 目录和日志路径。

`--resume <session-id> "..."` 会恢复已有会话、刷新当前 system prompt、在兼容时复用 `context_checkpoint.json`，再追加新的 follow-up 任务并继续执行。

`--server` 会启动一个常驻 HTTP 服务，默认监听 `127.0.0.1:8080`，提供：

- `GET /`（内嵌网页对话界面）
- `GET /healthz`
- `GET /api/v1/status`（内置 LLM 的有效 API 类型和模型）
- `POST /api/v1/chat`
- `POST /api/v1/webui/chat`
- `GET /api/v1/webui/workspace`（分页列出工作区目录）
- `GET /api/v1/webui/workspace/preview`（只读预览工作区文件）
- `GET /api/v1/webui/workspaces`、`GET /api/v1/webui/workspaces/directories`、`POST /api/v1/webui/workspaces/open`（选择服务端本地工作区）
- `POST /api/v1/chat/stop`
- `POST /api/v1/serverchan/chat`
- `POST /api/v1/serverchan/bot/webhook`

其中 `/api/v1/chat` 用于基于 `session_id` 的接口对话。
WebUI 的聊天、文件树、预览、状态、停止和 Trace 请求会携带 `workspace_id`；省略该字段时保持兼容并使用启动工作区。

`GET /api/v1/status` 返回内置 LLM 的有效运行时身份，例如 `{"status":"ok","llm":{"api_type":"openai","model":"gpt-4o-mini"}}`。该接口不会暴露 API Key 或供应商端点 URL；WebUI 会在 bqagent 标题下展示这项信息。

`GET /` 提供一个自包含的单页网页对话界面。页面以原生 TypeScript + Vite 开发，生产构建生成哈希化 JS/CSS；入口、资源和 favicon 全部通过 `go:embed` 打入同一个二进制，运行时没有 Node.js、CDN 或磁盘静态文件依赖。浏览器打开 `http://127.0.0.1:8080` 即可直接对话。界面支持明暗主题，并会安全渲染 Markdown 标题、列表、任务列表、表格、引用、链接、图片与带复制按钮的代码块，适合直接阅读 README 等 `.md` 内容。回复通过 `POST /api/v1/webui/chat` 以 Server-Sent Events 逐字流式返回；发送后按钮会切换为停止按钮，通过与渠道无关的 `POST /api/v1/chat/stop` 接口按 `turn_id` 取消当前模型请求和工具执行。取消注册表位于共享对话服务中，其他通道后续接入时无需依赖 WebUI。`event: progress` 会持续报告迭代轮次和工具活动。WebUI 默认不设置固定阶段轮数、阶段时间或整轮超时，会在同一次请求中持续执行到最终回复。流式模型 HTTP 请求不使用 `http.Client` 总时限，但从请求发出起受 `LLM_STREAM_IDLE_TIMEOUT` watchdog 约束；响应头、SSE 数据和 heartbeat 都会续期。请求生命周期还受浏览器断连、显式停止、主动启用的阶段 deadline 或服务关闭控制。非流式模型请求仍保留默认两分钟客户端超时。重复工具调用和连续失败仍受循环保护，整体循环仍受 `AGENT_MAX_ITERATIONS`（默认 1000）的失控安全阀约束。需要人工分阶段时，可显式配置正值的 `WEBUI_STAGE_MAX_ITERATIONS` 或 `WEBUI_STAGE_TIMEOUT`，恢复持久化阶段总结和“继续”机制。该网页渠道默认开启，可在[环境变量配置](../README_CN.md#环境变量配置)中关闭。

“新会话”菜单支持普通会话和群聊。外部 Agent 在服务启动后异步检测，Broker 立即可用；外部路由、子 Agent 和群聊成员查询会在需要时等待探测结果。群聊使用独立的 `conversation_type=group` session，初始成员名单为 bqagent 加当前检测可用的外部 Agent；`POST /api/v1/webui/group/participants` 和成员栏的“添加成员”按钮可以追加成员，`DELETE` 同一路径或成员标签悬停显示的删除按钮可以移除外部成员。成员变更持久化，但移除不会清除历史发言或外部 session；调度员 bqagent 不可移除。无 @ 的任务由 bqagent 直接处理且不开放外部成员调度。`@成员` 是硬路由且按出现顺序执行；仅 @ 外部成员时，任务直接由对应成员处理并结束该轮，bqagent 不参与分析或汇总。只有当前轮明确包含 `@bqagent` 时，bqagent 才能通过 `consult_group_agent` 邀请或追问成员，并在成员完成后汇总。所有成员使用同一 workspace，前序成员结论会加入后续成员的受预算约束共享上下文，并通过 `participant_start`、`participant_message`、`participant_error` SSE 事件单独展示。每个外部成员在同一群聊下保留独立外部 session。群聊仅允许 Run，TUI 暂不提供群聊入口。

输入区还提供“推理强度”选择器，包含**自动、低、中、高**四档。默认“自动”不会向上游发送 effort 参数，因此保持模型或供应商的默认行为；选择会保存在浏览器 `localStorage` 中，并随每次 WebUI 请求显式发送，但不会写入 session 元数据。非自动档位在 OpenAI Chat Completions 中映射为 `reasoning_effort`，在 OpenAI Responses 中映射为 `reasoning.effort`，在 Anthropic Messages 中映射为 adaptive `thinking` 与 `output_config.effort`。

三种内置协议会对 429、5xx、可能恢复的网络故障和结构化流内限流/过载错误自动重试一次。流式请求一旦收到文本、推理或工具调用增量就不再重试，以免重放已产生的内容。429 与 503 会采用最长 10 秒的 `Retry-After`。如果上游以 400/422 明确拒绝 reasoning 参数，客户端会省略该参数重试；降级成功后会在当前进程中按协议、端点和模型记住该能力限制。重试和降级只写入模型日志与 Run Trace，不改变回复正文。

WebUI 的工作区按钮会打开桌面侧栏或移动端抽屉。侧栏标题中的目录选择按钮可以在运行 bqagent 的机器上浏览用户主目录、启动工作区以及 `WEBUI_WORKSPACE_ROOTS` 追加的允许根目录；确认后的目录会被直接作为工作区根，切换本身不会创建 `.agent`。需要工作区次级配置时，使用侧栏标题中的 `.agent` 创建按钮。每个浏览器分别保存当前工作区，每个工作区分别保存 `session_id`，QQ、微信等其他通道仍固定使用启动工作区。切换期间不会重新加载新目录的 `.env`，模型和进程环境继续使用服务启动时的配置。目录按需分页加载；点击普通文件后，侧栏原位切换到只读预览，再通过返回按钮恢复原文件树位置。UTF-8 文本最多预览前 512 KiB，PNG、JPEG、GIF 和 WebP 图片最多预览 3 MiB，其他二进制文件只显示元信息。文件树在每轮对话结束后自动刷新，也可手动刷新；外部程序在空闲期间产生的改动需要手动刷新才能显示。除任意层级的 `.git` 外，包括 `.env`、`.agent` 和被 `.gitignore` 忽略的内容都会显示，因此不要把 WebUI 暴露给不受信任的访问者。符号链接会显示，但不能展开、预览、选择为工作区或从文件树加入附件。允许选择用户主目录意味着 WebUI 能把其中任意普通目录作为 Agent 工具边界，切勿将未认证的 WebUI 暴露给不受信任的访问者。

WebUI 输入区的 “+” 按钮支持上传浏览器本地文件，或引用运行 bqagent 的服务端 workspace 内路径。每轮最多 5 个文件，单文件最多 2 MiB、合计最多 6 MiB；上传文件保存在 `.agent/uploads/<session-id>/`，同名文件不会覆盖。UTF-8 文本会内联到当轮上下文（每个文件最多 64 KiB，超出时截断并保留可供 `read_file` 使用的完整路径）；二进制文件只注入路径说明。服务端路径必须位于 workspace 内，并通过根目录约束读取，不能借助 `..` 或符号链接逃逸。

`/api/v1/serverchan/chat` 保留为现有的 sendkey 推送型接口：它会生成回复，然后按 demo 中的 `text` / `desp` / `sendkey` 语义把结果推送出去。

`/api/v1/serverchan/bot/webhook` 则用于 ServerChan Bot / 微信回复回流：它接收 Bot webhook 的 JSON update，用入站 `chat_id` 绑定到持久化的 bqagent session，并通过已配置的 Bot 凭据发送回复。可选的 webhook 鉴权配置集中在[环境变量配置](../README_CN.md#环境变量配置)章节。

`-d` 是 `--server --background` 的快捷方式。两种写法都会把该服务放到后台运行，并把服务日志写入 `~/.agent/server/server.log`。微信 iLink、QQ Bot 和 ServerChan Bot 的持久状态也统一保存在全局 `~/.agent/server/`，不再写入工作区 `.agent`。如果要真正接 webhook，需要把 `/api/v1/serverchan/bot/webhook` 通过公网 HTTPS 地址或反向代理暴露出去。

默认情况下循环表现为"自动压缩续跑"：当对话接近输入 token 预算时，会把更早的对话摘要（压缩）后**继续**在压缩上下文上推进，而不是在固定轮数处停下。因此轮数上限只是失控保险（默认很高，为 `1000`）。所有上下文预算和摘要模型覆盖均集中在[环境变量配置](../README_CN.md#环境变量配置)章节。

Session 用于保存会话 ID、渠道用户映射、消息、任务状态和可恢复的上下文 checkpoint。默认 compact 模式会在每轮结束后用受预算控制的 `working_messages.jsonl` 收敛 `messages.jsonl`，避免原始工具结果无限累计；也可选择 append-only 完整审计模式。若任务中断后 transcript 比 working snapshot 更新，恢复时会优先使用较新的 transcript。Session 日志上限和存储模式集中在[环境变量配置](../README_CN.md#环境变量配置)章节。微信 iLink 只发送最终回复，不发送中间 progress，以免同一个 `context_token` 被提前消耗。

这里仍然刻意保持简单：

- 最小后台任务模式本身仍然不是 daemon
- 不做队列服务
- MCP 仅做客户端、且只支持 Streamable HTTP 传输（不支持 stdio/SSE，也不做 MCP server 端）
- 不做向量记忆

## RunTrace、评估与反馈

RunTrace 默认关闭，可在[环境变量配置](../README_CN.md#环境变量配置)中开启。开启后，每次任务会在 `.agent/runs/<run-id>/` 保存结构化追踪，包括模型和上下文版本、token、工具摘要、耗时、错误分类、artifact 和 verifier。关闭时，响应不返回 `run_id`，运行追踪与反馈接口不可用。

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

召回只用 `memory` 的 `search` / `list`；写入可用 `memory` 的 `add` / `replace`，或兼容的 `mem_save`。

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

---

<a id="english-documentation"></a>

# English Documentation

## Quick start

```bash
# single-run task
go run ./cmd/agent "list all Go files in this repo"

# start interactive multi-turn conversation by default (--chat is also accepted)
go run ./cmd/agent

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

# equivalent shortcut
go run ./cmd/agent -d

# start WeChat iLink login through the running server
go run ./cmd/agent --ilink-login

# check WeChat iLink login status
go run ./cmd/agent --ilink-status

# resume a previous session
go run ./cmd/agent --resume <session-id> "continue from the previous result"

# resume a previous session in chat mode
go run ./cmd/agent --chat --resume <session-id>
```

If you run `bqagent` without any arguments, it starts an interactive multi-turn conversation, equivalent to passing `--chat`.

## Workspace layout

bqagent resolves a workspace root by walking upward from the current directory until it finds one of these workspace markers (the global `~/.agent` in the user home is not treated as a workspace marker):

- `.agent`
- `.git`
- `go.mod`

Relative tool paths and shell commands run from that resolved workspace root.

Primary configuration is fixed at `~/.agent/`, where missing defaults are initialized at startup. A workspace `.agent/` is an optional secondary layer and is not created when switching workspaces. When present, its `AGENT.md`, `SOUL.md`, `USER.md`, memory content, skills, and `mcp.json` are merged after the global configuration; same-named workspace skills and MCP servers override global definitions. The WebUI create button generates only `.agent/memory/`, `mcp.json`, `AGENT.md`, `SOUL.md`, and `USER.md`. Otherwise, workspace `.agent/memory/` is created lazily only when existing Markdown memory must be migrated or memory is explicitly written for the first time; read-only queries against an empty memory store do not create it.

```text
~/.agent/
├─ sessions/
│  └─ <session-id>/
│     ├─ meta.json
│     ├─ messages.jsonl
│     ├─ working_messages.jsonl
│     ├─ context_checkpoint.json
│     └─ output.log
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
│  └─ mcp.json
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
  - global `~/.agent/skills` and workspace `.agent/skills` are merged; a same-named workspace skill fully replaces the global version
  - only the canonical name, frontmatter `description`, and workspace-relative path are indexed in the system prompt
  - when a skill is relevant, the model reads the complete `SKILL.md` on demand with `read_file`
  - explicit `/skill <name-or-alias> [args]` and leading skill IDs/aliases route through the same main conversation loop
- `~/.agent/sessions/<session-id>/messages.jsonl`
  - current-turn journal; compact mode converges it to the bounded snapshot after each turn
- `~/.agent/sessions/<session-id>/working_messages.jsonl`
  - stable bounded snapshot used for normal resume
- `~/.agent/sessions/<session-id>/context_checkpoint.json`
  - compact checkpoint with summary plus recent tail for faster resume context reconstruction
- `~/.agent/sessions/<session-id>/output.log`
  - human-readable execution log
- `.agent/mcp.json`
  - MCP server config (`mcpServers` map). **Streamable HTTP** servers listed here are connected at
    startup; their tools are discovered via `tools/list` and exposed to the model as
    `mcp__<server>__<tool>`. Header environment expansion is described under
    [environment variables](../README.md#environment-variables).
  - The default file has no remote servers. Server URLs must be literal `http` or `https` URLs; only header
    values can use allowlisted environment placeholders.
  - Discovery is best-effort: a server marked `"disabled": true`, missing, unreachable, or invalid is skipped
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

`--chat` (and no-argument startup) enters an always-streaming inline TUI on a real terminal. Completed messages remain in native scrollback while the input, completion panel, queue, and status bar stay at the bottom; no alternate screen is used. More than five tool calls are merged into a clickable detail group, with mouse tracking enabled only while that group is interactive. Redirected stdin/stdout and `TERM=dumb` automatically fall back to the legacy line mode. Sessions remain under global `~/.agent/sessions/`; resume validates the workspace and replays recent user/assistant text within a 200 KiB display budget. See the [inline TUI guide](./TUI.md) for shortcuts, commands, paste chips, queueing, prompt history, and `NO_COLOR`.

Long conversations now use request-time context management before each model call:

- completed historical tool-call scaffolding is stripped from the request payload
- older turns can be pruned to stay within a target input budget
- optional summarization can replace older dialogue with a synthetic summary message
- bounded working snapshots are persisted for reliable resume

`--background` starts a minimal background session by launching the same binary as a child process and writing output to:

- `~/.agent/sessions/<session-id>/meta.json`
- `~/.agent/sessions/<session-id>/messages.jsonl`
- `~/.agent/sessions/<session-id>/context_checkpoint.json` (when a summary checkpoint has been created)
- `~/.agent/sessions/<session-id>/output.log`

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

`GET /` serves a self-contained, single-page chat UI. It is developed as vanilla TypeScript + Vite; the production build emits hashed JavaScript and CSS, and the entry page, assets, and favicons are all embedded into the same executable with `go:embed`. Runtime needs no Node.js, CDN, or static files on disk. Open `http://127.0.0.1:8080` in a browser and chat directly. The UI supports light/dark themes and safely renders Markdown headings, lists, task lists, tables, blockquotes, links, images, and copyable fenced code blocks, making README-style `.md` content easy to read. Replies stream token-by-token over Server-Sent Events from `POST /api/v1/webui/chat`; while a turn is running, the send button becomes a stop button backed by the channel-independent `POST /api/v1/chat/stop` endpoint, which cancels the active model request and tool execution identified by `turn_id`. The cancellation registry lives in the shared conversation service, so other channels can opt in later without WebUI-specific stop logic. `event: progress` reports iterations and tool activity. By default, WebUI has no fixed stage iteration budget, stage timeout, or whole-turn timeout, so one request continues until the model returns a final answer. Streaming LLM HTTP requests do not use an `http.Client` total timeout, but `LLM_STREAM_IDLE_TIMEOUT` watches them from request dispatch onward; response headers, SSE data, and heartbeats renew the timer. Browser disconnect, explicit stop, an opted-in stage deadline, and server shutdown also cancel the request. Non-streaming LLM requests retain the default two-minute client timeout. Duplicate tool calls and repeated failures remain protected by the loop guard, and the full loop still uses `AGENT_MAX_ITERATIONS` (default `1000`) as a runaway safety valve. Set a positive `WEBUI_STAGE_MAX_ITERATIONS` or `WEBUI_STAGE_TIMEOUT` to opt back into persisted stage summaries and the "继续" workflow. The web UI is enabled by default and can be disabled through the [environment variables](../README.md#environment-variables) configuration.

The composer also provides a reasoning-effort selector with **Auto**, **Low**, **Medium**, and **High**. Auto is the default and omits the upstream effort setting, preserving the provider or model default. The preference is stored in browser `localStorage` and sent explicitly with each WebUI turn; it is not written into session metadata. Non-auto values map to `reasoning_effort` for OpenAI Chat Completions, `reasoning.effort` for OpenAI Responses, and adaptive `thinking` plus `output_config.effort` for Anthropic Messages.

All three built-in protocols retry 429, 5xx, potentially transient network failures, and structured in-stream rate-limit/overload errors once. A streaming request stops being retryable after any text, reasoning, or tool-call delta, preventing generated output from being replayed. HTTP 429 and 503 honor `Retry-After` up to 10 seconds. When a 400/422 response explicitly rejects the reasoning parameter, the client retries without it and, after a successful fallback, remembers the protocol/endpoint/model limitation for the current process. Retries and downgrades are recorded only in model logs and Run Trace; they do not alter reply content.

The workspace button opens a desktop sidebar or mobile drawer. The directory picker in the sidebar title browses the bqagent host's user home, startup workspace, and roots added through `WEBUI_WORKSPACE_ROOTS`. A confirmed directory becomes the exact workspace root without creating `.agent`; use the `.agent` create button in the sidebar title when a workspace-specific secondary configuration is wanted. Each browser remembers its selected workspace and keeps a separate `session_id` per workspace; QQ, WeChat, and other channels remain on the startup workspace. Switching does not reload the selected directory's `.env`: model and process configuration remain fixed at server startup. Directories load lazily in pages; selecting a regular file switches the sidebar in place to a read-only preview, and the back button restores the previous tree position. UTF-8 text previews include up to the first 512 KiB, while PNG, JPEG, GIF, and WebP previews are limited to 3 MiB; other binary files show metadata only. The tree refreshes after every completed chat turn and also has a manual refresh button. Changes made by an external program while the UI is idle require a manual refresh. Everything except a `.git` component is visible—including `.env`, `.agent`, and `.gitignore`-excluded content—so do not expose the WebUI to untrusted users. Symbolic links are listed but cannot be expanded, previewed, selected as a workspace, or attached from the explorer. Allowing the user home as a selection root means the WebUI can make any ordinary directory beneath it an Agent tool boundary; never expose an unauthenticated WebUI to untrusted users.

The “+” button in the WebUI composer can upload browser-local files or reference paths inside the server workspace. A turn accepts up to 5 files, 2 MiB each and 6 MiB total. Uploads are stored under `.agent/uploads/<session-id>/` without overwriting same-name files. UTF-8 text is inlined into the turn context up to 64 KiB per file, with a truncation note and the full `read_file`-accessible path; binary files contribute path metadata only. Server paths must remain inside the workspace and cannot escape through `..` components or symbolic links.

`/api/v1/serverchan/chat` is the existing sendkey-based push adapter: it generates a reply and forwards it through ServerChan using the `text` / `desp` / `sendkey` shape from the Go demo.

`/api/v1/serverchan/bot/webhook` is the conversational webhook endpoint for ServerChan Bot / WeChat replies. It accepts the Bot webhook JSON update format, maps each inbound `chat_id` onto a persisted bqagent session, and sends the assistant reply through the configured Bot credentials. Optional webhook authentication is documented under [environment variables](../README.md#environment-variables).

`-d` is a shortcut for `--server --background`. Both forms run this server in the background and write service logs to `~/.agent/server/server.log`. Persistent WeChat iLink, QQ Bot, and ServerChan Bot state is also kept globally under `~/.agent/server/`, never in a workspace `.agent`. For real webhook use, expose `/api/v1/serverchan/bot/webhook` through a public HTTPS endpoint or reverse proxy.

By default the loop behaves like an auto-compacting agent: when the conversation
approaches the input-token budget it summarizes (compacts) the older turns and
**continues** on the compacted context, rather than stopping at a fixed turn
count. The iteration cap is therefore just a runaway safety valve (defaults to a
high `1000`). Summarization is enabled
by default. All context budgets and summary-model overrides are listed in the [environment variables](../README.md#environment-variables) chapter.

Sessions persist the channel/user mapping, status, messages, and resumable context checkpoints. The default compact mode rewrites `messages.jsonl` to the bounded `working_messages.jsonl` snapshot after each turn, preventing raw tool results from accumulating indefinitely; full append-only audit history remains available as an opt-in. If a transcript is newer than its working snapshot after an interrupted turn, recovery uses the newer transcript. Session log limits and storage modes are documented under [environment variables](../README.md#environment-variables). WeChat/iLink sends only the final reply because its context token must not be consumed by intermediate progress messages.

This is still intentionally a small implementation:

- the one-shot background task path is not a daemon
- no queue server
- MCP support is client-side and Streamable-HTTP-only (no stdio/SSE transports, no MCP server mode)
- no vector memory

## Run traces, evaluation, and feedback

Run tracing is disabled by default and can be enabled through the [environment variables](../README.md#environment-variables) configuration. Enabled runs persist a structured trace under `.agent/runs/<run-id>/`, including model/context versions, token usage, tool summaries, timings, errors, artifacts, verifier results, and feedback. When disabled, responses omit `run_id` and the run trace/feedback endpoints are unavailable.

```bash
go run ./cmd/eval --suite smoke --mode replay
go run ./cmd/eval --suite all --mode live --repeats 3
```

Use `/feedback up|down [comment]` or `POST /api/v1/runs/<run-id>/feedback` to rate a run.

## Subagents

`/agent spawn <claude|codex|cursor|opencode> -- <task>` starts an asynchronous external agent in an isolated Git worktree. Use `/agent list`, `wait`, `result`, `interrupt`, `cancel`, `resume`, `apply`, and `cleanup` to manage it. Results and patches are persisted under `.agent/subagents/<id>/`; patches are never applied automatically.

## Structured memory

Structured, revisioned memory is stored in `.agent/memory/entries.jsonl` with source-run provenance, confidence, supersession, sensitive-entry confirmation, and Chinese/English full-text indexing. Existing Markdown memory is imported idempotently. `/memory list|search|confirm|compact` provides direct management. Recall uses `memory` `search` or `list`; `mem_save` remains a write-only fallback.

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
