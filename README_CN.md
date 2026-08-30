# bqagent

[English](./README.md) | 中文

![bqagent WebUI 界面](./docs/images/webui-overview.png)

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
- 继续支持 `.agent/rules/*.md` 和 `.agent/skills/*/SKILL.md`
- 使用 `--plan` 先拆步骤再执行
- 默认启动始终流式的内联终端 TUI（也可显式使用 `--chat`）
- 使用 `--resume` 恢复持久会话
- 对长对话做请求时上下文裁剪
- 可选地对旧对话做请求时摘要压缩
- 通过 checkpoint 进行紧凑恢复，同时保留原始会话历史
- 使用 `--background` 启动最小后台会话
- 使用 `--server` 启动常驻 HTTP 对话服务，并可选通过 ServerChan 推送回复

## 安装

安装 Go 1.24+、Node.js 24+ 和 npm 11+，然后通过 Makefile 构建 CLI：

```bash
make build
```

`make build` 会根据 `internal/server/webui/package-lock.json` 安装前端依赖，执行 TypeScript 严格检查和 Vite 生产构建，再把 `internal/server/webui/dist` 嵌入 Go 可执行文件。`dist` 和 `node_modules` 不提交到仓库；最终运行和分发仍只需要一个 `bqagent` 文件，不依赖 Node.js、Vite、CDN 或磁盘静态文件。Linux amd64 和 Windows amd64 分别使用 `make build-amd`、`make build-windows`。

仅开发 WebUI 时，可在 `internal/server/webui` 中运行 `npm run dev`；Vite 会把 `/api` 代理到 `http://127.0.0.1:8080`。全新检出后不要直接运行原始 `go build`，应先执行 `make webui-build` 或直接使用上述 Makefile 构建目标。

## 环境变量配置

bqagent 会读取进程环境变量和工作区根目录的 `.env` 文件；进程中已经存在的同名变量优先于 `.env`。加载到的 `.env` 值也会写入进程环境，使 shell 工具和外部智能体子进程能够继承这些变量。

推荐使用工作区 `.env`：

```dotenv
LLM_API_TYPE=openai
LLM_API_KEY=your-key-here
LLM_BASE_URL=https://api.openai.com/v1
LLM_MODEL=gpt-4o-mini
LLM_MODELS=gpt-4o-mini,fast=gpt-5.1
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

单次任务、Chat、后台任务、子任务和 Server 都会优先读取全局 `~/.agent/config.json` 中的当前 Provider；未配置当前 Provider 时才回退到环境变量。

通用 `LLM_*` 配置优先于供应商兼容变量。

| 变量 | 默认值 | 说明 |
|---|---|---|
| `LLM_API_TYPE` | `openai` | 接口协议：`openai`、`openai-response` 或 `anthropic`。 |
| `LLM_API_KEY` | — | 通用 API Key；Server 模式必填。 |
| `LLM_BASE_URL` | 供应商默认值 | 通用供应商端点覆盖。 |
| `LLM_MODEL` | 空 | 内置 LLM 使用的模型 ID，需要显式配置。 |
| `LLM_MODELS` | 空 | 同一供应商下可切换的模型列表，逗号分隔；支持 `别名=模型ID`。聊天中使用 `/model` 查看，`/model <名称或别名>` 切换当前会话，`/model default` 恢复默认。 |
| `LLM_STREAM_IDLE_TIMEOUT` | `2m` | 流式模型请求连续无响应头或响应体字节时的 idle watchdog；使用 Go duration 格式，设为 `0` 禁用。 |
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

### MCP

| 变量 | 默认值 | 说明 |
|---|---|---|
| `MCP_ALLOWED_ENV` | 空 | 进程级、逗号分隔的 allowlist，用于 `.agent/mcp.json` header 值中 `${NAME}` / `$NAME` 引用。 |

使用 `.agent/mcp.json` header 中的占位符前，须设置 `MCP_ALLOWED_ENV`，例如 `MCP_ALLOWED_ENV=DASHSCOPE_API_KEY`。URL 值不会展开环境变量；未获授权、未定义或格式错误的占位符只会使对应 MCP server 被跳过。

### Server 与渠道

| 变量 | 默认值 | 说明 |
|---|---|---|
| `WEBUI_ENABLED` | `true` | 设为 `false`、`0`、`no` 或 `off` 时关闭 `GET /`。 |
| `SERVERCHAN_BOT_TOKEN` | — | ServerChan Bot webhook 回复使用的 Token。 |
| `SERVERCHAN_BOT_WEBHOOK_SECRET` | — | 必填且不能全为空白；请求必须携带 `X-Sc3Bot-Webhook-Secret`，否则 bot webhook 端点返回 `503`。 |
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
| `BASH_OUTPUT_MAX_BYTES` | `1048576` | `execute_bash` 保留的 stdout/stderr 合并输出字节上限；超额输出仍会被消费并丢弃，结果末尾会附加截断标记。非正数或非法值使用默认值。 |
| `READ_FILE_MAX_BYTES` | `1048576` | `read_file` 返回的文件内容字节上限；超额内容仍会被消费并丢弃，结果末尾会附加截断标记。非正数或非法值使用默认值。 |
| `CHANNEL_AGENT_MAX_ITERATIONS` | `30` | QQ、iLink、ServerChan Bot 等消息渠道的单轮最大迭代数；不限制 WebUI。 |
| `CHANNEL_TURN_TIMEOUT` | `10m` | QQ、iLink、ServerChan Bot 等消息渠道的整轮超时；不限制 WebUI。 |
| `CHANNEL_STAGE_MAX_ITERATIONS` | `20` | QQ/iLink 生成阶段 checkpoint 前的迭代预算。 |
| `CHANNEL_STAGE_TIMEOUT` | `90s` | QQ/iLink 阶段时间预算。 |
| `WEBUI_STAGE_MAX_ITERATIONS` | `0`（默认禁用） | 可选的 WebUI 阶段 checkpoint 迭代预算；仅正值启用。 |
| `WEBUI_STAGE_TIMEOUT` | `0`（默认禁用） | 可选的 WebUI 阶段时间预算；仅正 duration 启用。 |
| `WEBUI_WORKSPACE_ROOTS` | — | WebUI 可额外浏览和选择的服务端目录根列表，使用操作系统路径列表分隔符（Unix/macOS 为 `:`，Windows 为 `;`）；用户主目录和启动工作区始终允许。 |
| `CONTEXT_MANAGEMENT_ENABLED` | `true` | 启用请求时上下文预算管理。 |
| `CONTEXT_MAX_INPUT_TOKENS` | `132000` | 上下文 token 估算总预算，包含回复预留。 |
| `CONTEXT_TARGET_INPUT_TOKENS` | `128000` | 裁剪或摘要后的目标大小。 |
| `CONTEXT_RESPONSE_RESERVE_TOKENS` | `4000` | 为模型回复预留的 token。 |
| `CONTEXT_KEEP_LAST_TURNS` | `6` | 压缩时保留的最近轮次。 |
| `CONTEXT_EXACT_COUNT_TRIGGER_PERCENT` | `80` | 完整请求的本地/usage 估算达到目标预算的该百分比后，尝试调用 Provider 精确计数（OpenAI Responses、Anthropic）；不支持或失败时自动回退估算。 |
| `CONTEXT_SUMMARIZATION_ENABLED` | `true` | 启用旧对话摘要。 |
| `CONTEXT_SUMMARY_TRIGGER_TOKENS` | `128000` | 触发摘要的输入大小。 |
| `CONTEXT_SUMMARY_MODEL` | 主模型 | 可选的低成本摘要模型。 |
| `SESSION_TRANSCRIPT_MODE` | `compact` | `compact` 限制 `messages.jsonl`；`full` 保留 append-only 审计历史。 |
| `SESSION_OUTPUT_MAX_BYTES` | `1048576` | 每个 `output.log` 保留的尾部大小；`0` 禁用裁剪。 |
| `RUN_TRACE_ENABLED` | `false` | 保存 `.agent/runs/<run-id>/` Trace 并启用运行反馈接口。 |

### 外部编码 Agent

对 `CLAUDE`、`CODEX`、`CURSOR`、`OPENCODE` 中的每个名称，可使用 `AGENT_<NAME>_ACP_CMD` 和 `AGENT_<NAME>_ACP_ARGS` 覆盖 ACP 启动命令。CLI 传输目前只为 Claude 和 Codex 实现，可通过 `AGENT_<NAME>_CLI_CMD` 和 `AGENT_<NAME>_CLI_ARGS` 覆盖。

Claude 默认使用 `claude -p --output-format json`；Codex 默认使用 `codex exec --json --skip-git-repo-check`。OpenCode 默认通过 `opencode acp` 使用 ACP，Cursor 仍需显式配置 ACP 传输。启动 bqAgent 的进程必须能从 PATH 找到 `opencode`；安装 OpenCode 或修改传输环境变量后，需要重启常驻 chat/server 进程以重新探测。

在 `--chat` 或 `--server` 会话中，使用 `/opencode <任务>` 将当前轮次路由到 OpenCode；后续普通消息会保持绑定，直到通过 `/default` 返回内置 Agent。OpenCode 仅支持 ACP；配置 `AGENT_OPENCODE_CLI_CMD/ARGS` 不会启用 CLI 传输。

## 文档

完整使用说明请参阅[项目文档](./docs/doc.md#中文文档)；终端快捷键、命令面板、TTY 回退与 scrollback 行为请参阅[内联 TUI 指南](./docs/TUI.md)。
