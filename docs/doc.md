# bqagent 文档

[English Documentation](./en/doc.md)

<a id="中文文档"></a>

# 中文文档

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
- 以 `.agent/AGENT.md`、`SOUL.md`、`USER.md` 作为主布局
- 继续支持 `.agent/rules/*.md` 和 `.agent/skills/*/SKILL.md`
- 使用 `--plan` 先拆步骤再执行
- 默认启动始终流式的内联终端 TUI（也可显式使用 `--chat`）
- [Herdr 自动集成](#herdr)：异步状态上报、会话恢复及退出释放，失败不阻塞主功能
- [Magpie Agent 插件](#magpie)：在 Magpie 中选择模型，通过统一网关接入，关闭接入后恢复原有配置
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

## 外部应用集成

### Herdr

在 Herdr 窗格中启动 `bqagent` 即可自动接入，无需安装插件。仅当 `HERDR_ENV=1`，且 `HERDR_PANE_ID`、`HERDR_BIN_PATH`、`HERDR_SOCKET_PATH` 均存在时启用。

前台 TUI、逐行对话和单次任务会异步上报工作状态与会话恢复信息，退出时释放窗格；TUI 的 Provider 配置等待选择时会上报 `blocked`。上报失败不阻塞模型工作，不发送提示词、回答或工具输出。HTTP/Bot 服务、后台任务和内部 worker 不接管窗格。

从 Herdr 恢复对话要求 **Herdr 0.9.2+**，并确保 `bqagent` 在 `PATH` 中。恢复后进入交互会话，保留原有会话上下文。协议条件、状态映射和退出行为见 [TUI 指南中的 Herdr 集成](./TUI.md#herdr-集成)。

### Magpie

仓库提供 [bqa-magpie Agent 插件](../plugins/bqa-magpie/README.md)。使用包含 Magpie 后台适配的 bqagent 版本，并确保 `bqagent` 在 `PATH` 中；在仓库根目录添加插件：

```sh
magpie plugin add ./plugins/bqa-magpie
```

也可以在 Magpie 插件页面添加该文件夹的绝对路径。随后在 Magpie 的 Agent 页面找到 **bqAgent**，开启接入、选择模型，再启动 bqagent。切换模型或关闭接入后，需要重启运行中的 bqagent，包括 WebUI 服务；旧版 bqagent 不读取此接入配置。

插件使用独立的 `~/.agent/magpie.json` 保存网关地址、此 Agent 的专属 key、选中的模型及可用模型列表。后台通过 OpenAI Responses（`openai-response`，`/v1/responses`）请求网关，模型 ID 保持 `供应商/模型` 形式，TUI、WebUI 和单次任务使用同一配置读取逻辑。启动时的优先级为：**有效的 Magpie 接入配置 → 全局当前 Provider → 环境变量**。

原有 `~/.agent/config.json` 及 Provider 加密密钥不被改写。关闭接入时，Magpie 恢复原来的键值或删除首次接入时新增的键；没有完整的 Magpie 接入配置时，bqagent 回到原有 Provider 或环境变量。若此前手动配置过 `magpie.json`，关闭接入会恢复那份配置。该文件内的网关 key 为明文，不应提交到仓库。

Magpie 资源库可同步 `~/.agent/AGENT.md` 和 `~/.agent/skills`。当前插件未声明 MCP 同步，因为 bqagent 仅支持 Streamable HTTP，不能完整接收通用 MCP 配置中的 stdio 服务。插件安装方式、配置行为和定向测试命令见 [插件说明](../plugins/bqa-magpie/README.md)。

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

启动时，单次任务、Chat、后台任务、子任务和 Server 优先使用有效的 [Magpie 接入配置](#magpie)；未启用时读取全局 `~/.agent/config.json` 中的当前 Provider，再回退到环境变量。

交互式 TUI 中可使用 `/provider` 打开分步配置向导；WebUI 可通过右下角的 Provider 设置入口完成同样的配置。两者共用加密配置存储和模型自动发现逻辑。

首次启动会自动创建全局 `~/.agent/config.json`，它是 bqagent 的系统级全局配置文件，不局限于 Provider。默认内容如下，WebUI 初始登录密码为 `admin123`：

```json
{
  "version": 1,
  "providers": [],
  "webui": {
    "password": "admin123"
  }
}
```

初始化只补充缺失文件，不会覆盖已有的 `config.json`。已有 Provider 配置时，`webui` 与 `providers` 位于同一层。请在首次使用后立即修改默认密码；登录成功后浏览器使用 24 小时有效的 HttpOnly、SameSite=Strict 会话 Cookie。页面右上角的账户菜单提供“修改密码”和“退出登录”：修改密码需输入当前密码、新密码及确认密码，新密码须为 6–128 个字符、首尾不能有空格且不能与原密码相同。保存只更新全局配置中的 `webui.password`，立即生效，无需重启，并使当前服务的全部浏览器登录会话失效；请用新密码重新登录。直接手动编辑配置文件仍需重启 bqagent 才会生效。`config.json` 默认以 `0600` 权限创建并包含明文登录密码，请始终确保它仅当前用户可读。登录保护覆盖 WebUI 使用的聊天、工作区、Provider、状态、停止和 Trace API，不影响微信、QQ、ServerChan 等独立 Channel 的入口。将 `webui.password` 留空或删除 `webui` 后重启可关闭登录验证，此时不显示账户菜单。

通用 `LLM_*` 配置优先于供应商兼容变量。

| 变量 | 默认值 | 说明 |
|---|---|---|
| `LLM_API_TYPE` | `openai` | 接口协议：`openai`、`openai-response` 或 `anthropic`。 |
| `LLM_API_KEY` | — | 通过环境变量接入时使用的通用 API Key；已配置有效的 Provider 或 Magpie 接入时无需重复设置。 |
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
| `AGENT_NATIVE_WEB_SEARCH` | `auto` | 原生搜索：`auto` 按协议启用，包含兼容网关；`on` 显式启用；`off` 仅使用搜索 API。原生搜索支持 `openai-response` / `anthropic`；Magpie 另支持供应商代搜。 |
| `SEARCH_API_KEY` | — | Tavily 兼容搜索 Key，优先于 Firecrawl 配置。 |
| `SEARCH_BASE_URL` | 供应商默认值 | Tavily 兼容端点覆盖。 |
| `FIRECRAWL_API_KEY` | — | 未配置 `SEARCH_*` 时使用的 Firecrawl Key。 |
| `FIRECRAWL_BASE_URL` | 供应商默认值 | Firecrawl 端点覆盖。 |

`web_search(query)` 在普通供应商上优先使用当前会话模型的原生搜索：单独发送带搜索工具的模型请求，将摘要和来源链接返回给主模型。Responses 使用 `web_search`，Anthropic 使用 `web_search_20250305` 并支持 `pause_turn` 续行。TUI、WebUI、Ask/Run 和命令行使用同一入口，切换模型或供应商后重新判断能力。

原生搜索复用模型的 API 凭据，即使没有 `SEARCH_API_KEY` / `FIRECRAWL_API_KEY` 也会开放 `web_search`；这不代表免除模型供应商的搜索及 token 费用。默认 `auto` 根据 `openai-response` / `anthropic` 协议开放原生搜索，不限制官方端点、模型名称或本地网关的认证方式；Magpie 等兼容网关无需额外设置 `on`。实际搜索能力由供应商决定，不支持时按下述规则回退。Responses 使用流式请求并保留搜索事件与来源，兼容 Codex 经 Magpie 转发时结束事件的空 `output`，也兼容返回普通 JSON 的网关。

通过 `~/.agent/magpie.json` 接入或当前 Provider ID 为 `magpie` 时，Responses 会话的搜索子请求直接使用同一网关、同一模型和凭据的 `/v1/responses`，声明 `web_search`，不再绕到 `/v1/messages`。显式选择 Anthropic 协议时仍使用 Messages；Chat Completions 保留 Messages 代搜兼容路径。具体搜索供应商由 Magpie 的联网搜索配置决定，无需配置 MCP，两个搜索 API Key 均为空时仍开放搜索。不依据模型名称或端口猜测 Magpie。

搜索子请求及其回退不设置强制 `tool_choice`，避免与 thinking 模式冲突，也让 Magpie 内部取得搜索结果后能够结束工具调用并返回摘要。提示词要求先搜索、有相关来源后结束搜索；最终仍校验实际搜索记录和来源 URL，不接受未搜索的普通回答。

Magpie 的 Messages 搜索使用流式请求。收到完整、相互匹配的搜索调用和结果块，且结果包含有效来源 URL 后，立即将来源列表返回主模型并关闭该搜索请求，不再等待网关继续搜索或生成摘要。主模型可按需抓取来源页面作答；搜索前的计划文字不会冒充搜索结论。仍兼容网关返回完整 JSON；不完整结果、无来源或服务端搜索错误继续走原有回退。提前返回时仅累计流中已经上报的 token 用量。

Magpie Responses 搜索失败时，直接回退到已有的 Tavily / Firecrawl 配置，不再尝试 Messages。Anthropic 和 Chat Completions 的 Messages 搜索失败后也直接回退到搜索 API。原生搜索不额外设置固定总超时，各次尝试继承调用方的取消信号和主动设置的 deadline；任务取消或调用方 deadline 到期时不再回退。流式请求复用 `LLM_STREAM_IDLE_TIMEOUT`（默认 `2m`，`0` 禁用）：等待响应头或连续收不到响应体字节时触发，数据和心跳会续期；空闲超时仍可触发搜索回退。非流式请求保留 HTTP 客户端自身的超时。原生搜索明确不受支持的结果在当前客户端内按模型缓存 30 分钟，Magpie Responses 同样遵循该缓存；其他协议保留的 Magpie Messages 代搜入口不受该缓存影响。缺少可用搜索能力且两个搜索 Key 均为空时禁用 `web_search`；限流、网络故障和单次无结果不缓存为不支持。非 Magpie 的 Chat Completions 仍使用搜索 API，`web_fetch` 不受影响。

Responses 默认请求最多 3 次内置工具调用和 4096 个输出 token。若 Codex 或兼容网关明确以 HTTP 400/422 拒绝 `max_tool_calls` / `max_output_tokens`，只移除被拒绝的参数并重试，每个参数最多重试一次，合计最多 3 次请求；兼容结果按当前客户端和模型缓存 30 分钟。这类参数拒绝不会禁用搜索能力；被省略的限制改用服务端默认值。Anthropic 每次请求最多搜索 3 次，`pause_turn` 最多续行 2 次，合计最多 3 次请求。结果必须包含实际搜索记录和有效来源 URL，普通模型回答不会被当作搜索结果。模型侧搜索请求返回的 token 用量计入当前对话统计。

协议参考：[Responses 原生搜索](https://developers.openai.com/api/docs/guides/tools-web-search)、[Anthropic 原生搜索](https://platform.claude.com/docs/en/agents-and-tools/tool-use/web-search-tool)。

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
| `GROUP_EXTERNAL_AGENT_TIMEOUT` | `10m` | 群聊中每次外部 Agent 调用的独立超时；超时后取消并作废该群聊的 ACP session。 |
| `WEBUI_WORKSPACE_ROOTS` | — | WebUI 可额外浏览和选择的服务端目录根列表，使用操作系统路径列表分隔符（Unix/macOS 为 `:`，Windows 为 `;`）；用户主目录和启动工作区始终允许。 |
| `CONTEXT_MANAGEMENT_ENABLED` | `true` | 启用请求时上下文预算管理。 |
| `CONTEXT_MAX_INPUT_TOKENS` | `204000` | 上下文 token 估算总预算，包含回复预留。 |
| `CONTEXT_COMPRESSION_TRIGGER_TOKENS` | `200000` | 完整请求输入超过该值才开始压缩；不超过总预算减去回复预留。 |
| `CONTEXT_TARGET_INPUT_TOKENS` | `18000` | 裁剪或摘要后的输入目标，为默认触发阈值的 9%；不会作为提前压缩的触发条件。 |
| `CONTEXT_RESPONSE_RESERVE_TOKENS` | `4000` | 为模型回复预留的 token。 |
| `CONTEXT_KEEP_LAST_TURNS` | `6` | 压缩时保留的最近轮次。 |
| `CONTEXT_EXACT_COUNT_TRIGGER_PERCENT` | `85` | 完整请求的本地/usage 估算达到压缩触发阈值的该百分比后，尝试调用 Provider 精确计数（OpenAI Responses、Anthropic）；不支持或失败时自动回退估算。 |
| `CONTEXT_SUMMARIZATION_ENABLED` | `true` | 启用旧对话摘要。 |
| `CONTEXT_SUMMARY_TRIGGER_TOKENS` | `200000` | 已进入压缩流程后，超过此值才尝试摘要；失败时回退裁剪。 |
| `CONTEXT_SUMMARY_MODEL` | 主模型 | 可选的低成本摘要模型。 |
| `SESSION_TRANSCRIPT_MODE` | `compact` | `compact` 仅使用有界的 `messages.jsonl`；`full` 保留 append-only 审计历史和独立工作快照。 |
| `SESSION_OUTPUT_MAX_BYTES` | `1048576` | 每个 `output.log` 保留的尾部大小；`0` 禁用裁剪。 |
| `RUN_TRACE_ENABLED` | `false` | 保存 `.agent/runs/<run-id>/` Trace 并启用运行反馈接口。 |

默认请求超过 200K token 后压缩到 18K（9%）目标；精确计数从约 170K 开始尝试。固定系统指令不会被裁剪，因此系统指令或工具定义本身超过目标时无法保证压到 18K。TUI 状态栏展示最近请求的上下文占比（相对于压缩触发阈值），以 `45%/200K` 格式显示，数值可能为估算，压缩后刷新；它不同于累计 `in/out` 用量。

### 外部编码 Agent

对 `CLAUDE`、`CODEX`、`CURSOR`、`OPENCODE`、`PI` 中的每个名称，可使用 `AGENT_<NAME>_ACP_CMD` 和 `AGENT_<NAME>_ACP_ARGS` 覆盖 ACP 启动命令。CLI 传输目前为 Claude、Codex 和 Pi 实现，可通过 `AGENT_<NAME>_CLI_CMD` 和 `AGENT_<NAME>_CLI_ARGS` 覆盖。

Claude 默认使用 `claude -p --output-format json`；Codex 默认优先复用共享 App Server，旧版兼容的 CLI 后备命令是 `codex exec --json --skip-git-repo-check`。Cursor 默认通过 `cursor-agent acp` 使用 ACP，OpenCode 默认通过 `opencode acp` 使用 ACP。启动 bqAgent 的进程必须能从 PATH 找到对应的可执行文件；安装外部 Agent 或修改传输环境变量后，需要重启常驻 chat/server 进程以重新探测。

在 `--chat` 或 `--server` 会话中，使用 `/opencode <任务>` 将当前轮次路由到 OpenCode；后续普通消息会保持绑定，直到通过 `/default` 返回内置 Agent。OpenCode 仅支持 ACP；配置 `AGENT_OPENCODE_CLI_CMD/ARGS` 不会启用 CLI 传输。

Codex 默认自动探测共享 App Server，无需设置环境变量。bqAgent 先通过 `codex app-server proxy` 连接已有服务；连接失败时调用幂等的 `codex app-server daemon start`，成功后重新连接。旧版 Codex 不支持 daemon/proxy，或共享服务不可用时，尝试独立的 `codex app-server` stdio 进程，再回退到 ACP/CLI。探测只进行协议初始化，不创建线程或发送模型任务。关闭 bqAgent 只关闭代理连接，不停止共享服务。

也可以显式指定连接已有共享服务，设置后重启 bqAgent：

```bash
export AGENT_CODEX_APP_SERVER_CMD=codex
export AGENT_CODEX_APP_SERVER_ARGS='app-server proxy'
```

显式配置 `AGENT_CODEX_APP_SERVER_CMD/ARGS` 后按指定命令执行，不自动启动 daemon 或替换成独立 App Server；启动探测失败仍可回退到 ACP/CLI。只设置 CMD 时，ARGS 保持兼容默认值 `app-server`；只设置 ARGS 时，CMD 默认为 `codex`。需要独立进程时设置 `AGENT_CODEX_APP_SERVER_ARGS='app-server'`。要恢复自动探测，取消设置这两个变量。已有自定义 `AGENT_CODEX_ACP_CMD/ARGS` 或 `AGENT_CODEX_CLI_CMD/ARGS` 且未显式配置 App Server 时，保持原有 ACP/CLI 路径；例如设置 `AGENT_CODEX_CLI_CMD=codex` 可继续使用原 CLI 模式。

适配器继承 Codex 的登录、模型和安全配置，共享模式使用服务端配置。通过 `thread/start`、`thread/resume` 和 `turn/start` 运行，接收流式事件并以 `turn/completed` 的状态确认结果，取消时发送 `turn/interrupt`。命令、文件修改和权限请求复用现有审批界面；没有审批接收端时不自动批准。其他服务端交互请求（例如结构化提问）暂不支持，会明确返回协议错误。

已有 Codex CLI 的 thread ID 可用于 App Server 恢复；已有 ACP 会话仍使用其 ACP 适配器，以免混用会话 ID。任务开始后不会因报错或断线自动重跑或改走 CLI；新建线程的失败轮次也保留 thread ID，供后续继续。群聊、普通外部路由和子任务共用 Broker，会话状态继续按原方式保存。

Pi 默认使用 `pi --mode json`，通过标准输入接收任务，并通过 `--session <ID>` 延续会话，复用统一外部 Agent 探测、Broker 和会话存储。安装并配置 Pi 后，重启 bqAgent 即可在新群聊中使用 `@pi`；已有群聊可通过“添加成员”加入 Pi。普通会话可使用 `/pi <任务>`，通过 `/default` 返回内置 Agent。可用 `AGENT_PI_CLI_CMD` 指定可执行文件；`AGENT_PI_CLI_ARGS` 覆盖参数时需保留 `--mode json`，不要使用 `--no-session`、`--continue`、`--resume` 或指定固定 session，以免破坏群聊之间的会话隔离。

WebUI 的“新会话”菜单还可以创建群聊。外部 Agent 在服务启动后异步检测，不阻塞服务启动；需要检测结果的请求会等待后台探测完成，再将可用 Agent 与 bqagent 一起加入初始成员名单。成员栏的“添加成员”按钮可以把创建时未加入、当前已检测可用的 Agent 追加到现有群聊；鼠标悬停在外部成员上会显示删除按钮。成员变更会持久化，删除成员不会清除其历史发言和外部 session，重新添加后仍可延续上下文；调度员 bqagent 不可删除。未 @ 任何成员的任务由 bqagent 直接处理，不调度外部成员。直接发送给 `@codex`、`@opencode` 等外部成员的任务只由对应成员处理，bqagent 不再参与分析或自动汇总；只有用户明确 `@bqagent` 时，bqagent 才会调度外部成员并在其完成后给出最终汇总，也可以在后续轮次 `@bqagent` 汇总共享上下文里的既有结论。成员顺序共享同一个 workspace 和群聊上下文，各自结论会单独显示。群聊仅支持 Run 模式；普通会话中的 `/codex`、`/opencode` 粘性路由保持不变。群聊编排属于通用服务能力，本期仅 WebUI 提供入口。

## 系统诊断与就绪检查

WebUI 顶栏的“系统诊断”入口可查看全局配置、存储、外部 Agent、MCP 和对话渠道的状态、失败原因及处理建议。默认读取快照；“主动检测”由用户手动触发，不自动轮询。

```bash
# 本地只读诊断：不创建配置，不启动会话，也不连接运行中的服务器
bqagent --doctor

# 机器可读报告
bqagent --doctor --doctor-json

# 显式执行存储写入、外部 Agent 初始化与 MCP 工具发现探测（可能启动 Codex 共享服务）
bqagent --doctor --doctor-active
```

`--doctor-active`、`--doctor-json` 必须配合 `--doctor`，可同时使用；诊断不能与聊天、后台、服务模式或任务文本混用。退出码：`0` 表示服务就绪（可以有降级项），`1` 表示未就绪，`2` 表示参数错误、取消、超时或诊断执行失败。

| 接口 | 用途 | 认证 |
| --- | --- | --- |
| `GET /healthz` | 进程存活检查，原有语义不变 | 无 |
| `GET /readyz` | 最小就绪结果 `{"ready":true/false}`；就绪 200，未就绪 503 | 无，不返回配置详情 |
| `GET /api/v1/webui/doctor` | 详细诊断快照 | 遵循 WebUI 登录设置 |
| `POST /api/v1/webui/doctor` | 显式主动探测 | 遵循 WebUI 登录及同源检查 |

doctor 接口接受可选的 `workspace_id` 查询参数，只读取已打开工作区，不隐式创建工作区运行时。`/readyz` 检查服务启动时的默认工作区。CLI 检查当前本地环境，不读取另一进程的实时连接状态；未观察到的渠道连接明确标记为“未验证”。

就绪是**服务级**判断：核心配置无法读取或解析、核心存储存在明确故障时未就绪；未配置默认模型、可选 MCP 或外部 Agent 异常会显示降级，不阻止进入设置页。就绪不保证模型可以生成回复。快照不执行写入，存储检查会注明写入未实测；尚未创建的会话、记忆目录检查其最近的已有父目录。

主动检测最多 15 秒，普通外部 Agent 单项最多 3 秒、MCP 单项最多 5 秒；Codex 自动发现可使用剩余的整体时间预算。存储仅使用并清理临时文件；外部 Agent 仅进行协议初始化并关闭探测连接，Codex 自动发现可能启动共享服务，但不停止已有服务；MCP 仅初始化和获取工具列表，不改变正在使用的工具目录。不发送模型生成请求，不执行 MCP 工具，不发送渠道消息，不触发登录或重连。没有无副作用探测接口的渠道仍显示运行时快照。同一服务实例不重复并发执行主动检测。

报告区分可用、异常、禁用、检测中及未验证，包含检测时间和证据来源；不会返回密码、API Key、敏感请求头、二维码、命令参数或带凭据的 URL。

全局配置由内部 `globalconfig` 模块统一管理。用户仍使用 `~/.agent/config.json`，首次启动仍初始化 `version: 1`、`webui.password: "admin123"`、空 `providers`，已有文件不覆盖；Provider 密文及 `.config.key` 不需要迁移。Provider 和 WebUI 更新各自配置段，进程内串行保存，避免覆盖其他模块配置。

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

会话按工作区分组保存：`<workspace-key>` 由规范化绝对工作区路径的可读编码和哈希组成，`<session-id>` 仍为时间戳加短 ID。旧的 `sessions/<session-id>/` 目录保持原位，恢复、列表、维护和删除操作继续兼容。移动或重命名工作区后需要单独迁移会话归属。

```text
~/.agent/
├─ sessions/
│  └─ <workspace-key>/
│     └─ <session-id>/
│        ├─ meta.json
│        ├─ messages.jsonl
│        ├─ working_messages.jsonl  # 仅 full 模式；旧 compact 文件会自动迁移
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
├─ workspace/  # 兼容旧布局
│  ├─ AGENT.md
│  ├─ SOUL.md
│  ├─ USER.md
│  └─ memory/
│     ├─ MEMORY.md
│     └─ YYYY-MM-DD.md
└─ agent_memory.md
```

### 这些文件分别做什么

工具使用约定统一放在 `AGENT.md`，工具参数以代码随请求发送的定义为准。旧版 `TOOLS.md` 不再生成或加载；已有自定义约定请合入同一目录的 `AGENT.md` 后再删除旧文件。初始化不会覆盖已自定义的 `AGENT.md`、`SOUL.md`；与旧版默认内容逐字节一致（从未修改过）的文件会自动升级为新默认内容。

system prompt 按以下顺序装配，前面的部分不随会话变化，可被模型提供方缓存：

1. 内置核心指令：优先级（用户请求 > 仓库规则 > 全局默认）、按需投入、改动范围与 git 安全、校验与最终回复规范、提示注入防护；以及内置 Memory 策略
2. 全局 `~/.agent/` 的 `AGENT.md`、`SOUL.md`、`USER.md`（HTML 注释会被去除，只剩标题的模板文件不加载）和全局 Rules
3. 工作区根目录的 `AGENTS.md`（不存在时读取 `CLAUDE.md`），超过 32 KiB 截断；其中的项目规则优先于全局默认
4. 运行环境：操作系统/架构、`execute_bash` 使用的 shell、是否为 git 仓库
5. 工作区路径索引（旧版 `workspace/`、`agent_memory.md` 仅在存在时列出）、工作区 `.agent/` 上下文和 Skills 索引

各部分按共享范围从大到小排列（内置 → 用户级 → 工作区级），同一用户在不同工作区之间可复用前两部分的前缀缓存。

会话开始日期和 memory 快照放在会话级上下文中，不影响上述可缓存前缀。启用结构化 memory 时，模型只看到 `memory` 工具，旧的 `mem_save` 不再出现在工具定义中，但仍可调用以兼容旧会话。

- `.agent/AGENT.md`、`SOUL.md`、`USER.md`
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
- `~/.agent/sessions/<workspace-key>/<session-id>/messages.jsonl`
  - compact 模式唯一的消息文件：执行中追加消息，每轮结束后原子替换为有界工作上下文；full 模式保留完整追加历史
- `~/.agent/sessions/<workspace-key>/<session-id>/working_messages.jsonl`
  - 仅 full 模式单独保存有界工作上下文；旧 compact 会话的同名文件在恢复或启动维护时迁移后删除
- `~/.agent/sessions/<workspace-key>/<session-id>/context_checkpoint.json`
  - 保存“摘要 + 最近 tail”的紧凑 checkpoint，用于恢复时重建工作上下文
- `~/.agent/sessions/<workspace-key>/<session-id>/output.log`
  - 人类可读的执行日志
- `.agent/mcp.json`
  - MCP 服务器配置（`mcpServers` 映射）。这里配置的 **Streamable HTTP** 服务器会在启动时并行连接，
    通过 `tools/list` 发现其工具，并以 `mcp__<server>__<tool>` 的形式暴露给大模型。header 环境变量展开
    统一说明在[环境变量配置](#环境变量配置)章节。
  - 默认文件不配置远程 server。Server URL 必须是字面量 `http` 或 `https` URL；只有 header 值可以使用
    allowlist 中的环境变量占位符。
  - TUI、Chat 和 WebUI 服务先完成本地初始化，后台发现 MCP 工具；TUI 状态栏显示连接中、已连接和失败数量，WebUI 系统诊断可查看各服务状态。每个服务发现完成后立即发布工具，新轮次使用最新快照，已开始的轮次保持原有工具集合。普通问答无需等待 MCP。
  - 后台发现保留整体 15 秒超时，多个服务并行使用这段预算；退出界面或关闭服务时取消未完成连接。单次命令仍在执行任务前等待发现，但服务间也并行连接。没有跨进程工具缓存。
  - 发现过程是 best-effort：标记 `"disabled": true`、文件缺失、服务器不可达或配置非法都会被跳过（仅记录一条
    警告），不会导致启动失败。当前仅支持 Streamable HTTP 传输。

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

- `~/.agent/sessions/<workspace-key>/<session-id>/meta.json`
- `~/.agent/sessions/<workspace-key>/<session-id>/messages.jsonl`
- `~/.agent/sessions/<workspace-key>/<session-id>/context_checkpoint.json`（当已生成摘要 checkpoint 时）
- `~/.agent/sessions/<workspace-key>/<session-id>/output.log`

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

`GET /` 提供一个自包含的单页网页对话界面。页面以原生 TypeScript + Vite 开发，生产构建生成哈希化 JS/CSS；入口、资源和 favicon 全部通过 `go:embed` 打入同一个二进制，运行时没有 Node.js、CDN 或磁盘静态文件依赖。浏览器打开 `http://127.0.0.1:8080` 即可直接对话。界面支持明暗主题，并会安全渲染 Markdown 标题、列表、任务列表、表格、引用、链接、图片与带复制按钮的代码块，适合直接阅读 README 等 `.md` 内容。回复通过 `POST /api/v1/webui/chat` 以 Server-Sent Events 逐字流式返回；发送后按钮会切换为停止按钮，通过与渠道无关的 `POST /api/v1/chat/stop` 接口按 `turn_id` 取消当前模型请求和工具执行。取消注册表位于共享对话服务中，其他通道后续接入时无需依赖 WebUI。`event: progress` 会持续报告迭代轮次和工具活动。WebUI 默认不设置固定阶段轮数、阶段时间或整轮超时，会在同一次请求中持续执行到最终回复。流式模型 HTTP 请求不使用 `http.Client` 总时限，但从请求发出起受 `LLM_STREAM_IDLE_TIMEOUT` watchdog 约束；响应头、SSE 数据和 heartbeat 都会续期。请求生命周期还受浏览器断连、显式停止、主动启用的阶段 deadline 或服务关闭控制。非流式模型请求仍保留默认两分钟客户端超时。重复工具调用和连续失败仍受循环保护，整体循环仍受 `AGENT_MAX_ITERATIONS`（默认 1000）的失控安全阀约束。需要人工分阶段时，可显式配置正值的 `WEBUI_STAGE_MAX_ITERATIONS` 或 `WEBUI_STAGE_TIMEOUT`，恢复持久化阶段总结和“继续”机制。该网页渠道默认开启，可在[环境变量配置](#环境变量配置)中关闭。

“新会话”菜单支持普通会话和群聊。外部 Agent 在服务启动后异步检测，Broker 立即可用；外部路由、子 Agent 和群聊成员查询会在需要时等待探测结果。群聊使用独立的 `conversation_type=group` session，初始成员名单为 bqagent 加当前检测可用的外部 Agent；`POST /api/v1/webui/group/participants` 和成员栏的“添加成员”按钮可以追加成员，`DELETE` 同一路径或成员标签悬停显示的删除按钮可以移除外部成员。成员变更持久化，但移除不会清除历史发言或外部 session；调度员 bqagent 不可移除。无 @ 的任务由 bqagent 直接处理且不开放外部成员调度。`@成员` 是硬路由且按出现顺序执行；仅 @ 外部成员时，任务直接由对应成员处理并结束该轮，bqagent 不参与分析或汇总。只有当前轮明确包含 `@bqagent` 时，bqagent 才能通过 `consult_group_agent` 邀请或追问成员，并在成员完成后汇总。所有成员使用同一 workspace，前序成员结论会加入后续成员的受预算约束共享上下文，并通过 `participant_start`、`participant_message`、`participant_error` SSE 事件单独展示。每个外部成员在同一群聊下保留独立外部 session。群聊仅允许 Run，TUI 暂不提供群聊入口。

输入区还提供“推理强度”选择器，包含**自动、低、中、高、最高（max）**五档。默认“自动”不会向上游发送 effort 参数，因此保持模型或供应商的默认行为；选择会保存在浏览器 `localStorage` 中，并随每次 WebUI 请求显式发送，但不会写入 session 元数据。非自动档位在 OpenAI Chat Completions 中映射为 `reasoning_effort`，在 OpenAI Responses 中映射为 `reasoning.effort`，在 Anthropic Messages 中映射为 adaptive `thinking` 与 `output_config.effort`。

三种内置协议会对 429、5xx、可能恢复的网络故障和结构化流内限流/过载错误自动重试一次。流式请求一旦收到文本、推理或工具调用增量就不再重试，以免重放已产生的内容。429 与 503 会采用最长 10 秒的 `Retry-After`。如果上游以 400/422 明确拒绝 reasoning 参数，客户端会省略该参数重试；降级成功后会在当前进程中按协议、端点和模型记住该能力限制。重试和降级只写入模型日志与 Run Trace，不改变回复正文。

WebUI 的工作区按钮会打开桌面侧栏或移动端抽屉。侧栏标题中的目录选择按钮可以在运行 bqagent 的机器上浏览用户主目录、启动工作区以及 `WEBUI_WORKSPACE_ROOTS` 追加的允许根目录；确认后的目录会被直接作为工作区根，切换本身不会创建 `.agent`。需要工作区次级配置时，使用侧栏标题中的 `.agent` 创建按钮。每个浏览器分别保存当前工作区，每个工作区分别保存 `session_id`，QQ、微信等其他通道仍固定使用启动工作区。切换期间不会重新加载新目录的 `.env`，模型和进程环境继续使用服务启动时的配置。目录按需分页加载；点击普通文件后，侧栏原位切换到只读预览，再通过返回按钮恢复原文件树位置。UTF-8 文本最多预览前 512 KiB，PNG、JPEG、GIF 和 WebP 图片最多预览 3 MiB，其他二进制文件只显示元信息。文件树在每轮对话结束后自动刷新，也可手动刷新；外部程序在空闲期间产生的改动需要手动刷新才能显示。除任意层级的 `.git` 外，包括 `.env`、`.agent` 和被 `.gitignore` 忽略的内容都会显示，因此不要把 WebUI 暴露给不受信任的访问者。符号链接会显示，但不能展开、预览、选择为工作区或从文件树加入附件。允许选择用户主目录意味着 WebUI 能把其中任意普通目录作为 Agent 工具边界，切勿将未认证的 WebUI 暴露给不受信任的访问者。

WebUI 输入区的 “+” 按钮支持上传浏览器本地文件，或引用运行 bqagent 的服务端 workspace 内路径。每轮最多 5 个文件，单文件最多 2 MiB、合计最多 6 MiB；上传文件保存在 `.agent/uploads/<session-id>/`，同名文件不会覆盖。UTF-8 文本会内联到当轮上下文（每个文件最多 64 KiB，超出时截断并保留可供 `read_file` 使用的完整路径）；二进制文件只注入路径说明。服务端路径必须位于 workspace 内，并通过根目录约束读取，不能借助 `..` 或符号链接逃逸。

`/api/v1/serverchan/chat` 保留为现有的 sendkey 推送型接口：它会生成回复，然后按 demo 中的 `text` / `desp` / `sendkey` 语义把结果推送出去。

`/api/v1/serverchan/bot/webhook` 则用于 ServerChan Bot / 微信回复回流：它接收 Bot webhook 的 JSON update，用入站 `chat_id` 绑定到持久化的 bqagent session，并通过已配置的 Bot 凭据发送回复。可选的 webhook 鉴权配置集中在[环境变量配置](#环境变量配置)章节。

`-d` 是 `--server --background` 的快捷方式。两种写法都会把该服务放到后台运行，并把服务日志写入 `~/.agent/server/server.log`。微信 iLink、QQ Bot 和 ServerChan Bot 的持久状态也统一保存在全局 `~/.agent/server/`，不再写入工作区 `.agent`。如果要真正接 webhook，需要把 `/api/v1/serverchan/bot/webhook` 通过公网 HTTPS 地址或反向代理暴露出去。

默认情况下循环表现为"自动压缩续跑"：当对话接近输入 token 预算时，会把更早的对话摘要（压缩）后**继续**在压缩上下文上推进，而不是在固定轮数处停下。因此轮数上限只是失控保险（默认很高，为 `1000`）。所有上下文预算和摘要模型覆盖均集中在[环境变量配置](#环境变量配置)章节。

Session 用于保存会话 ID、渠道用户映射、消息、任务状态和可恢复的上下文 checkpoint。默认 compact 模式只保存一个 `messages.jsonl`：执行中逐条追加消息，每轮结束后原子替换为受预算控制的工作上下文。旧会话若同时存在 `working_messages.jsonl`，恢复或启动维护时会按更新时间和可读性选择可恢复内容，成功保存到 `messages.jsonl` 后删除旧快照，保留中断任务中新追加的消息；启动维护跳过运行中的会话，迁移写入失败不会删除旧快照。可选的 full 模式仍保留完整追加历史和独立工作快照。Session 日志上限和存储模式集中在[环境变量配置](#环境变量配置)章节。微信 iLink 只发送最终回复，不发送中间 progress，以免同一个 `context_token` 被提前消耗。

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
