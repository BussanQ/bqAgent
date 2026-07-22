# Pi 与 bqAgent 完整功能对比报告

## 一、结论概览

本次分析基于：

- Pi 默认分支 `main`，基线 commit `75e6123`
- 当前 bqAgent 代码与项目文档

两者定位并不完全相同：

> **Pi 是面向终端编码场景的可嵌入 Agent Harness；bqAgent 是面向本地执行、多渠道接入和长任务持久化的轻量 Agent 服务。**

简单概括：

| 项目 | 核心优势 |
|---|---|
| Pi | Provider 深度、TypeScript 扩展生态、终端体验、会话树、模型认证、SDK/RPC、供应链治理 |
| bqAgent | 单文件部署、HTTP/WebUI、QQ/微信/ServerChan、内建 MCP、外部编码 Agent、worktree 子代理、结构化记忆、运行 Trace |

Pi 更像“Agent 开发平台和编码终端”；bqAgent 更像“本地 Agent 服务与聊天渠道中枢”。

因此最合理的方向不是把 bqAgent 改写成 Pi，而是吸收 Pi 的 **Provider 抽象、生命周期 Hook、工具安全策略、会话树、存储抽象和协议化接口**，同时保留 bqAgent 的 Go 单二进制、多渠道和内建子代理优势。

---

## 二、Pi 的产品与代码架构

### 2.1 Monorepo 包结构

Pi 是 TypeScript ESM npm workspace，主要由四个核心包组成：

| 包 | 作用 |
|---|---|
| `@earendil-works/pi-ai` | 多 Provider、认证、模型目录、流式协议、Token 和成本 |
| `@earendil-works/pi-agent-core` | 状态化 Agent、工具循环、事件流、会话抽象 |
| `@earendil-works/pi-coding-agent` | 编码 CLI、工具、会话、扩展、Prompt、Skills |
| `@earendil-works/pi-tui` | 终端 UI、编辑器、Markdown、Overlay、图像和差分渲染 |

其他重要模块：

- `packages/storage/sqlite-node`：SQLite 会话后端
- `packages/server`：实验性的本地 IPC/RPC 服务
- `packages/coding-agent/examples/extensions`：Subagent、Plan Mode、Sandbox、Gondolin 等扩展示例

这种拆分让用户可以只使用 Provider 层、Agent Core，或者完整的 Coding Agent，而不必绑定整个 CLI。

相比之下，bqAgent 采用 Go 单模块结构，主要依赖方向为：

```text
cmd/agent
  → internal/server、internal/runtime
  → internal/agent、tools、session、workspace、extagent
```

bqAgent 主要入口装配位于 `cmd/agent/main.go`，Runtime 构造位于 `internal/runtime/runtime.go`。

### 2.2 运行模式

Pi Coding Agent 支持：

1. 默认交互式 TUI
2. `pi -p` 单次 Print 模式
3. JSONL 事件输出模式
4. stdin/stdout RPC 模式
5. TypeScript SDK 嵌入
6. 实验性本地 IPC Server

bqAgent 支持：

1. 单次 CLI
2. `--chat` 交互模式
3. `--plan` 计划执行
4. `--background` 后台进程
5. `--resume` 持久会话恢复
6. `--server` HTTP 服务
7. WebUI SSE
8. QQ、微信 iLink、ServerChan
9. 内部持久化 Subagent Worker

#### 结论

- Pi 更适合终端编码、进程集成和作为 SDK 嵌入。
- bqAgent 更适合常驻服务、浏览器访问和消息渠道接入。
- Pi 的 RPC/JSON 模式值得 bqAgent 吸收。
- bqAgent 的 HTTP/WebUI/聊天渠道则是 Pi 当前核心仓库不具备的优势。

---

## 三、模型 Provider 能力对比

### 3.1 Pi 的 Provider 体系

Pi 的模型抽象核心在 `packages/ai/src/models.ts`：

- `Provider<TApi>`
- `Models`
- `MutableModels`
- `createProvider()`
- `calculateCost()`
- `getSupportedThinkingLevels()`
- `clampThinkingLevel()`

支持的协议和 Provider 非常广泛：

- Anthropic Messages
- OpenAI Chat Completions
- OpenAI Responses
- OpenAI Codex Responses
- Azure OpenAI Responses
- Google Gemini
- Google Vertex
- AWS Bedrock
- Mistral
- OpenRouter
- GitHub Copilot
- Vercel AI Gateway
- Cloudflare AI
- DeepSeek
- Groq
- Cerebras
- Fireworks
- Together
- NVIDIA
- Hugging Face
- Kimi、MiniMax、Moonshot、Qwen、ZAI 等

不仅是 Provider 数量多，Pi 还覆盖了：

- OAuth 登录
- API Key
- 动态模型目录
- Thinking Level
- Prompt Cache
- Cache Read/Write 计费
- Token 成本计算
- Provider 特有消息格式
- 工具兼容性配置
- 图片能力
- 自定义 Headers
- 自定义 Provider
- Ollama、LM Studio、vLLM 等本地服务

### 3.2 bqAgent 的 Provider 体系

bqAgent 支持三种协议：

- OpenAI Chat Completions
- OpenAI Responses
- Anthropic Messages

主要实现：

- `internal/agent/client.go`
- `internal/agent/client_responses.go`
- `internal/agent/client_anthropic.go`
- `internal/runtime/runtime.go`

依赖 `LLM_BASE_URL` 和 OpenAI-compatible 接口，可以间接使用 MiniMax、DeepSeek、Qwen 等兼容服务。

#### bqAgent 的不足

1. Provider 没有统一能力描述。
2. 没有动态模型目录。
3. 没有 OAuth。
4. Thinking、Cache、图片等能力没有统一 capability matrix。
5. Token 主要通过字符数估算。
6. 没有统一成本核算。
7. Retry、429、超时和错误分类比较基础。
8. Anthropic 高级能力覆盖有限。
9. 内部消息统一使用 `[]map[string]any`，类型安全较弱。

### 3.3 对比结论

| 能力 | Pi | bqAgent |
|---|---:|---:|
| Provider 数量 | 强 | 中 |
| Provider 原生协议深度 | 强 | 中 |
| OAuth | 有 | 无 |
| 动态模型目录 | 有 | 无 |
| Thinking 能力映射 | 有 | 无统一抽象 |
| Prompt Cache | 有 | 无 |
| Token 成本计算 | 有 | 基础 |
| 私有兼容网关 | 有 | 有 |
| 实现复杂度 | 高 | 低 |
| 维护成本 | 高 | 低 |

Pi 在 Provider 层明显领先，但它也需要维护大量协议、模型目录和认证逻辑。

---

## 四、Agent Loop 对比

### 4.1 Pi Agent Loop

Pi 核心实现位于 `packages/agent/src/agent-loop.ts` 和 `agent.ts`。

主要能力：

- 流式消息事件
- Agent/Turn/Message/Tool 生命周期事件
- 并行或串行工具执行
- 工具结果保持原始调用顺序
- `steer()` 队列
- `followUp()` 队列
- `abort()`
- `waitForIdle()`
- `transformContext()`
- `convertToLlm()`
- `prepareNextTurn()`
- `beforeToolCall`
- `afterToolCall`
- 工具可终止整个 Turn
- 可替换消息和工具结果
- Agent 状态订阅

一个很值得借鉴的安全细节是：

> 如果 Assistant 消息因 Token 上限截断且其中包含工具调用，Pi 会拒绝执行整批工具调用，而不是尝试执行可能只解析出一部分参数的调用。

### 4.2 bqAgent Agent Loop

核心位于 `internal/agent/loop.go`。

已有能力：

- 流式和非流式调用
- 工具调用循环
- 普通工具并行执行
- 写文件、编辑、计划、Skill 顺序执行
- 工具结果按原始顺序回填
- 未知工具和参数错误转成 Tool Result
- Context cancellation
- 最大迭代保护
- 重复工具调用保护
- 连续失败检测
- Stage Budget
- Stage Checkpoint
- “继续”恢复长任务
- Plan 和 Skill 子循环
- Tool Progress

### 4.3 对比

bqAgent 的长任务保护和聊天渠道 Stage Checkpoint 是 Pi 核心不具备的产品化能力。

Pi 更强的是：

- 生命周期 Hook
- Steering 和 Follow-up Queue
- 可替换 Context
- 可替换 Provider Request
- 工具调用前拦截
- 截断工具调用 fail-closed
- Agent 状态订阅

---

## 五、工具系统与安全

### 5.1 Pi 工具系统

内建工具：

- read
- edit
- write
- grep
- find
- ls
- bash

支持：

- `--tools`
- `--exclude-tools`
- `--no-tools`
- `--no-builtin-tools`
- 工具生命周期 Hook
- 文件 Mutation Queue
- Bash 输出流
- 超时与 Abort
- Process Tree Kill
- 输出长度/字节限制
- 完整输出保存到临时文件
- `BashOperations` 注入
- 将命令转发到 SSH、容器或 VM

Pi 有 Project Trust：

- 项目扩展、Skill、Settings、Package 需要信任
- 信任记录保存到本地
- 非交互模式可使用 `--approve` / `--no-approve`

但 Pi 明确说明：

> Project Trust 不是工具权限系统，也不是安全沙箱。

Pi 默认依旧以当前 OS 用户权限运行。

### 5.2 bqAgent 工具系统

主要定义在 `internal/tools/tools.go`：

- execute_bash
- read_file
- write_file
- edit_file
- grep
- glob
- todo_write
- web_search
- web_fetch
- install_skill
- memory
- mem_save / mem_get
- plan
- Skill metadata index + `read_file` on-demand loading
- agent tools
- MCP tools

已有安全能力：

- Workspace 路径限制
- Symlink Escape 检查
- SSRF 防护
- Private IP 拒绝
- Redirect 检查
- Web Body 大小限制
- Bash cancellation
- Unix Process Group Kill
- 环境安装命令重复失败保护
- Subagent 默认独立 Worktree
- Subagent Apply 前要求工作区干净

#### bqAgent 的主要安全短板

- Bash 没有沙箱。
- 没有 Tool Policy 层。
- 没有 Read/Write/Shell/Network 权限档位。
- 没有执行前确认机制。
- HTTP Server 没有统一认证。
- 外部聊天渠道可以触发高权限本地工具。
- `install_skill` 可以将远程内容持久化为未来 Prompt。
- Web、MCP、Skill 内容没有明确的不可信输入边界。

### 5.3 对比结论

两者默认都不是安全沙箱。

Pi 的优势是：

- 明确的 Project Trust
- 工具 Allow/Deny
- 生命周期 Hook
- Bash Operation 注入
- 官方 VM/容器隔离示例

bqAgent 的优势是：

- 文件路径边界更明确
- SSRF 防护已经内建
- Worktree Subagent
- 环境命令重复失败保护

---

## 六、上下文管理与会话

### 6.1 Pi 会话树

Pi 会话不是线性聊天记录，而是树状 JSONL：

- Active Leaf
- Fork
- Clone
- Tree Navigation
- Branch Summary
- Label
- Session Name
- Model Change
- Thinking Change
- Tool Set Change
- Extension Custom Entry
- Import/Export JSONL
- Export HTML

可选 SQLite 后端支持：

- WAL
- Busy Timeout
- Session Repo
- Entries
- Branches
- Materialized State
- Migration

### 6.2 Pi Context Compaction

主要能力：

- 基于模型 Context Window
- 为输出预留 Token
- 保留最近约 20k Token
- 自动压缩
- 增量摘要
- 保持 Tool Call 与 Tool Result 边界
- Branch Summary
- Extension 可拦截或替换摘要
- 摘要模板明确记录目标、约束、决策、文件和下一步

### 6.3 bqAgent 会话

主要位于 `internal/session/store.go`：

- `meta.json`
- `messages.jsonl`
- `working_messages.jsonl`
- `context_checkpoint.json`
- `output.log`

支持：

- created/running/completed/failed
- compact/full transcript
- Working Snapshot
- Context Checkpoint
- 中断恢复
- 后台进程
- Output 大小限制
- Server 启动维护
- 每 Session 串行锁

### 6.4 bqAgent Context Management

主要位于 `internal/agent/loop.go`：

- Tool Scaffold 转 Assistant Evidence
- Token Budget
- Summary Trigger
- Tail Pruning
- Hard Guard
- System Prompt / Summary / Latest Request 优先
- 超大 Tool Result 截断
- Context Checkpoint

### 6.5 对比结论

| 能力 | Pi | bqAgent |
|---|---:|---:|
| 线性会话恢复 | 有 | 有 |
| 树状会话 | 有 | 无 |
| Fork/Clone | 有 | 无 |
| Branch Summary | 有 | 无 |
| JSONL | 有 | 有 |
| SQLite Backend | 有 | 无 |
| Compact Transcript | 有 | 有 |
| 长任务 Checkpoint | 一般 | 强 |
| Stage Continue | 无核心实现 | 有 |
| Token 精度 | 较强 | `chars/4` |
| Context Hook | 有 | 无 |

---

## 七、Prompt、Skills 和 Memory

### 7.1 Pi

支持：

- SYSTEM.md
- APPEND_SYSTEM.md
- AGENTS.md
- CLAUDE.md
- Prompt Templates
- Agent Skills
- 全局与项目资源
- Package 内资源
- Progressive Disclosure

Pi 只在 Prompt 中加入 Skill 的名称、描述和路径，模型需要时再读取完整 `SKILL.md`。

Pi 没有核心长期结构化记忆库。

### 7.2 bqAgent

Prompt 来源：

- `.agent/AGENT.md`
- `.agent/SOUL.md`
- `.agent/TOOLS.md`
- `.agent/USER.md`
- `.agent/rules/*.md`
- `.agent/skills/*/SKILL.md`
- Markdown Memory
- Structured Memory Search

结构化记忆位于 `internal/memory/store.go`，支持：

- add
- replace
- remove
- search
- list
- confirm
- compact
- Revision
- Supersedes
- Confidence
- Source Run
- Sensitive Pending
- 中英文检索
- 过期策略
- Legacy Migration

### 7.3 结论

Pi 的 Prompt Template、资源发现和 Skill 生态更成熟。

bqAgent 的结构化长期记忆明显更强，是当前项目不应放弃的差异化能力。

---

## 八、扩展系统

### 8.1 Pi Extensions

Pi Extension 可以：

- 注册工具
- 注册命令
- 注册快捷键
- 注册参数
- 自定义消息渲染
- 自定义会话 Entry
- 拦截工具调用
- 拦截 Provider 请求
- 注入 Context
- 替换 Compaction
- 管理 Model 和 Thinking
- 自定义 TUI
- 注册 Provider
- 增加 Package、Skill、Theme、Prompt

Extension 是普通 TypeScript 代码，具有当前用户的完整权限。

### 8.2 bqAgent 扩展方式

bqAgent 当前主要通过：

- `tools.Catalog`
- `ExtraDefinitions`
- `ExtraFunctions`
- MCP
- Skills
- External Agent Broker
- Subagent Manager
- Server Channels

进行扩展。

优势是静态、类型明确、部署稳定。

不足是：

- 没有统一 Extension ABI。
- 没有生命周期 Hook。
- 新增扩展通常需要修改 Go 源码并重新编译。
- UI、Provider、Session、Tool 和 Prompt 扩展方式分散。

### 8.3 结论

Pi 的 Extension 平台是最值得 bqAgent 借鉴的能力之一。

但不建议 bqAgent 直接引入可执行 TypeScript 扩展，否则会破坏：

- 单二进制
- 低依赖
- Go 静态部署
- 更清晰的供应链边界

更适合吸收的是 **Hook 设计和 Extension 接口思想**，而不是照搬 npm/jiti 运行时。

---

## 九、MCP、Subagent 和外部 Agent

### 9.1 Pi

- 核心没有 MCP。
- 核心没有内建 Subagent。
- Subagent 是官方 Extension 示例。
- Plan Mode、Todo、Background Shell 也主要通过 Extension。
- 有 RPC、SDK，外部编排方便。

### 9.2 bqAgent

- 内建 Streamable HTTP MCP Client
- Tool Discovery
- Raw JSON Schema
- MCP Tool Proxy
- Claude/Codex/Cursor/OpenCode 路由
- ACP/CLI Transport
- Sticky External Session
- 持久化异步 Subagent
- 独立 Git Worktree
- Heartbeat
- Retry
- Result/Artifacts/Diff Patch
- Explicit Apply

### 9.3 结论

这是 bqAgent 对 Pi 最明显的功能优势：

> Pi 倾向把多代理和 MCP 留给扩展；bqAgent 已将它们做成核心产品能力。

---

## 十、UI、Server 和渠道

### 10.1 Pi

优势集中在终端：

- 差分渲染
- Editor
- Markdown
- Overlay
- Autocomplete
- Kitty Keyboard
- Bracketed Paste
- CJK/IME
- Terminal Image
- 模型/OAuth 选择
- 会话树 UI
- Cost/Token Footer

Pi Server 是实验性的本地 IPC/RPC，不是 HTTP Web 服务。

Pi 没有核心 WebUI、QQ、微信或 ServerChan。

### 10.2 bqAgent

已有：

- HTTP API
- SSE WebUI
- 科技蓝 Canvas 粒子页面
- Stop Turn
- Run Feedback
- QQ Gateway
- 微信 iLink
- ServerChan
- Per-peer Session
- 去重
- Pending Reply
- 媒体输入

### 10.3 结论

- Pi 的 TUI 体验远强于 bqAgent CLI Chat。
- bqAgent 的浏览器和消息渠道能力远强于 Pi 核心。
- bqAgent 没必要照搬整个 Pi TUI，但可以吸收其事件模型、状态 Footer、会话树和工具输出折叠设计。

---

## 十一、Trace、Eval 和供应链

### 11.1 Pi

已有：

- Agent/Turn/Message/Tool Event Stream
- Token、Cache、Cost
- Diagnostics
- JSON/RPC Event
- Output Guard
- ANSI Log
- Offline Mode
- Version Check 控制

供应链治理较强：

- 精确版本锁定
- `npm ci --ignore-scripts`
- npm Audit
- Signature Audit
- Shrinkwrap
- Release SHA256
- Draft Release Staging
- Trusted Publishing

但没有成熟的：

- Agent Benchmark
- LLM Judge
- 任务成功率 Dashboard
- 集中式 Trace Exporter

### 11.2 bqAgent

已有：

- Run Trace
- Model/Tool Events
- Prompt Hash
- Context Hash
- Token Usage
- Tool Duration
- Error Category
- Argument Redaction
- Result Hash
- Artifact
- Feedback
- Replay/Live Eval Manifest

bqAgent 的本地运行 Trace 和 Eval 骨架比 Pi 核心更完整。

但仍缺少：

- OpenTelemetry
- Metrics
- Dashboard
- 成本阈值
- 多模型矩阵
- 轨迹断言
- Prompt Injection Eval
- LLM Judge

---

## 十二、双方优劣势总结

### 12.1 Pi 的主要优势

1. Provider 覆盖广且协议实现深。
2. OAuth 和模型目录成熟。
3. Agent Loop 生命周期丰富。
4. Extension API 强大。
5. 会话树和 Branch Summary 完整。
6. SDK、JSON、RPC 接口成熟。
7. TUI 体验强。
8. Tool Allow/Deny 和 Project Trust。
9. SQLite 存储抽象。
10. Token、Cache、Cost 管理较好。
11. Bash 输出流和截断机制成熟。
12. 供应链治理完善。

### 12.2 Pi 的主要劣势

1. 默认仍有完整本机权限。
2. 没有内建工具审批和真正 Sandbox。
3. 没有核心 MCP。
4. 没有核心持久化 Subagent。
5. 没有 WebUI。
6. 没有 QQ/微信等渠道。
7. 没有独立长期结构化记忆。
8. Server 仍是实验性。
9. Extension 具有任意代码执行风险。
10. Provider 维护面很大。
11. 仍有兼容层迁移技术债。
12. Agent Eval 和集中式 Observability 较弱。

### 12.3 bqAgent 的主要优势

1. Go 单二进制，依赖极少。
2. CLI、Server、WebUI 和渠道打通。
3. Stage Budget 和继续机制适合长任务。
4. Session Resume 和 Checkpoint 完整。
5. 结构化长期记忆。
6. 内建 MCP。
7. 外部 Coding Agent 路由。
8. Worktree Subagent 和显式 Apply。
9. 本地 Run Trace。
10. Eval Harness 已具骨架。
11. 文件路径和 SSRF 防护较好。
12. 部署简单，适合 Windows/Linux 私有环境。

### 12.4 bqAgent 的主要劣势

1. Provider 原生能力深度不足。
2. 内部消息模型无类型。
3. 没有统一扩展 Hook。
4. Tool 权限策略缺失。
5. HTTP Server 没有统一认证。
6. Shell 没有 Sandbox。
7. 会话只有线性结构。
8. 没有模型目录、OAuth 和成本管理。
9. Storage 仅文件系统。
10. CLI/TUI 体验较基础。
11. Prompt 来源缺少信任边界。
12. Server Service 职责偏多。

---

## 十三、bqAgent 最值得吸收的 Pi 特性

### P0：优先吸收

#### 1. Provider Capability Registry

为每个 Provider/Model 描述：

```go
type ModelCapabilities struct {
    ToolUse       bool
    Vision        bool
    Thinking      bool
    PromptCache   bool
    StructuredOut bool
    MaxContext    int
    MaxOutput     int
}
```

落点：

- `internal/agent/client.go`
- `internal/runtime/runtime.go`

收益：

- 不再假定所有 Provider 都支持相同工具和 Schema。
- 可统一 Thinking、Vision、Context 和 Cache 降级。
- 为后续动态模型目录奠定基础。

#### 2. 截断工具调用 Fail-Closed

如果模型因为 Token 上限停止，且 Assistant Message 包含未完成 Tool Call：

- 不执行任何该批次工具；
- 回填明确错误；
- 要求模型重新生成完整调用。

这是低成本、高价值的安全增强，适合直接加入 `internal/agent/loop.go`。

#### 3. 工具权限 Policy

增加预设：

```text
read-only
workspace-write
shell-confirm
full-local
channel-restricted
```

策略维度：

- read
- write
- shell
- network
- install_skill
- memory
- MCP
- subagent
- apply

不同入口采用不同默认策略：

| 入口 | 推荐默认 |
|---|---|
| 本地 CLI | workspace-write |
| WebUI loopback | workspace-write |
| QQ/微信 | read-only 或 shell-confirm |
| 公网 Webhook | channel-restricted |
| Subagent | 独立策略 |

#### 4. HTTP 认证

至少增加：

- Bearer Token
- Cookie/Local Session
- Reverse Proxy Identity Header
- Webhook Secret 强制校验

优先保护：

- `/api/v1/chat`
- `/api/v1/chat/stop`
- `/api/v1/runs/*`
- iLink 登录
- WebUI

#### 5. Bash 输出治理

吸收 Pi 的能力：

- stdout/stderr 流式输出
- 字节和行数上限
- 截断后保存完整输出
- 在 Tool Result 中返回完整输出路径
- 可注入执行后端

可将现有 Shell 执行抽象成：

```go
type CommandExecutor interface {
    Execute(ctx context.Context, request CommandRequest) CommandResult
}
```

以后可以接：

- 本机 Shell
- Docker
- SSH
- Windows Sandbox
- VM

### P1：中期吸收

#### 6. Typed Conversation IR

逐步替换 `[]map[string]any`：

```go
type Message struct {
    Role       Role
    Content    []ContentBlock
    ToolCalls  []ToolCall
    ToolResult *ToolResult
}
```

再由 Provider Adapter 转换为：

- OpenAI Chat
- OpenAI Responses
- Anthropic Messages

这是 bqAgent 中长期最重要的内部重构之一。

#### 7. 生命周期 Hook

建议定义：

```text
BeforeModelRequest
AfterModelResponse
BeforeToolCall
AfterToolCall
BeforeCompaction
AfterCompaction
OnSessionEvent
OnTurnStart
OnTurnEnd
```

Hook 能用于：

- 权限确认
- Trace
- Tool Result 过滤
- Prompt Injection 防护
- Provider Header
- Cost Budget
- Extension
- 审计

这可以避免继续把逻辑堆进 `internal/server/service.go` 和 `internal/agent/loop.go`。

#### 8. 会话树、Fork 和 Branch Summary

在现有 Session Store 上增加：

```text
entry_id
parent_id
active_leaf
branch_summary
label
```

先实现：

1. `session fork`
2. `session tree`
3. `session switch`
4. Branch Summary
5. WebUI 会话分支展示

这会显著提升编码探索和回滚体验。

#### 9. JSON/RPC 模式

增加：

```bash
bqagent --mode json
bqagent --mode rpc
```

JSON 模式输出稳定事件：

```json
{"type":"turn_start"}
{"type":"message_delta","delta":"..."}
{"type":"tool_start","name":"read_file"}
{"type":"tool_end","result":"..."}
{"type":"turn_end"}
```

收益：

- IDE 插件
- 桌面客户端
- 外部编排器
- CI
- Python/Node SDK
- 更可靠的 Agent-to-Agent 集成

#### 10. 存储接口与 SQLite

将 Session、Memory、Trace 抽象为接口：

```go
type SessionRepository interface { ... }
type MemoryRepository interface { ... }
type TraceRepository interface { ... }
```

保留文件后端作为默认，再提供 SQLite：

- WAL
- Busy Timeout
- 原子事务
- 并发锁
- 查询索引
- 数据清理
- 多进程一致性

### P2：长期吸收

#### 11. 模型目录与认证中心

吸收 Pi 的：

- Model Catalog
- Provider Auth
- OAuth
- Dynamic Refresh
- Cost Metadata
- Thinking Level
- API Compatibility Flags

但不要一开始维护几十个 Provider；建议先支持：

1. OpenAI
2. Anthropic
3. Gemini
4. OpenRouter
5. 本地 OpenAI-compatible

#### 12. Extension API

不建议引入 TypeScript/jiti，而应做 Go 接口或进程协议：

```go
type Extension interface {
    RegisterTools(...)
    RegisterHooks(...)
    RegisterChannels(...)
    RegisterProviders(...)
}
```

或者采用 RPC Extension：

```text
bqagent ↔ extension process
```

这样可以保留：

- 单二进制核心
- 崩溃隔离
- 权限隔离
- 多语言扩展
- 稳定 ABI

#### 13. CLI/TUI 增强

可以吸收 Pi TUI 的部分设计：

- Token/Cost Footer
- Tool Result 折叠
- Session Tree
- Model Picker
- Thinking Level Picker
- Bash 实时输出
- 图片粘贴
- 快捷键
- 主题系统

但优先级应低于安全、Provider 和 Session 重构，因为 bqAgent 当前主交互面已经是 WebUI 和聊天渠道。

---

## 十四、不建议直接照搬的特性

### 1. 不建议改成 TypeScript Monorepo

bqAgent 的 Go 单文件部署是核心优势，不应为了接近 Pi 而放弃。

### 2. 不建议引入任意 TypeScript Extension

会增加：

- npm 供应链风险
- 任意代码执行
- Node Runtime
- 安装复杂度
- 部署体积

更适合采用 Go Hook 或独立 RPC Extension。

### 3. 不建议一次支持几十个 Provider

Provider 数量越多，维护 OAuth、模型目录、Tool Schema 和边缘兼容的成本越高。

建议先建立正确抽象，再逐步增加 Provider。

### 4. 不建议把 MCP/Subagent 降级为扩展

它们已经是 bqAgent 的差异化核心能力，应继续内建维护。

### 5. 不建议用 TUI 取代 WebUI

可以增强 CLI，但 WebUI、多渠道和 HTTP Service 应继续是主产品方向。

---

## 十五、推荐实施路线

### 第一阶段：安全与可靠性

1. Tool Policy
2. HTTP Auth
3. 截断 Tool Call Fail-Closed
4. Bash 输出限制与完整日志
5. Provider Retry/Error 分类
6. Web/MCP/Skill 不可信内容标记

### 第二阶段：内部架构

1. Typed Conversation IR
2. Provider Capability Registry
3. Agent Lifecycle Hooks
4. Storage Repository Interface
5. 拆分 Server Service 职责

### 第三阶段：产品能力

1. JSON/RPC 模式
2. Session Tree/Fork
3. Branch Summary
4. SQLite Backend
5. Model Catalog
6. Token/Cost 统计

### 第四阶段：生态

1. RPC Extension
2. Container/VM Command Executor
3. Provider OAuth
4. 更完整的 TUI
5. OpenTelemetry
6. 安全与轨迹 Eval

---

## 最终判断

Pi 最值得 bqAgent 学习的不是某个具体工具，而是以下四个架构思想：

1. **Provider、Agent Core、UI 和存储分层**
2. **生命周期 Hook 作为扩展和安全控制面**
3. **会话是可分支的状态树，而不仅是消息数组**
4. **所有模型和工具能力都应显式描述，而不是依赖隐式兼容**

bqAgent 不应照搬 Pi 的 TypeScript 和 npm 扩展生态，而应在 Go 单二进制架构中吸收这些思想。

如果完成 P0 和 P1 项目，bqAgent 会形成非常清晰的差异化定位：

> 一个低依赖、可私有部署、支持 Web/聊天渠道、内建 MCP/子代理/结构化记忆，同时具备成熟 Provider 抽象、权限策略和会话分支能力的 Go Agent Runtime。

---

## Sources

- [Pi 根 README](https://github.com/earendil-works/pi/blob/main/README.md)
- [Pi Coding Agent 使用文档](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/usage.md)
- [Pi Provider 和模型配置](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/models.md)
- [Pi Agent Core](https://github.com/earendil-works/pi/blob/main/packages/agent/README.md)
- [Pi Extension API](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/extensions.md)
- [Pi Session Compaction](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/compaction.md)
- [Pi Security Model](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/security.md)
- [Pi Containerization](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/containerization.md)
