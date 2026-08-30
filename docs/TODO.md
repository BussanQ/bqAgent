# TODO

本文件记录当前仍待推进的工作项。历史上的 P0 主流程收敛、P1 错误处理第一批改造、P2 Workspace 约定统一已经完成，相关内容归档在文末「已归档」一节，不再逐项跟踪。

最近一次对照代码核实：2026-08-30。

## 待办

### 1. 统一 planner 与普通对话的失败语义

`runConversation` 已经具备工具错误转 tool message、截断恢复、阶段检查点等容错能力，但 `runPlannedConversation` 仍然是任一步骤出错就整体终止（`internal/agent/loop.go:328`）。

- [ ] 对齐 `RunConversation` / `RunConversationTurn` / `RunPlannedConversation` 的错误行为
- [ ] 明确 planner 步骤失败时是中止整个计划还是跳过该步并继续
- [ ] 补充 planner 失败时的 session 状态更新测试
- [ ] 补充 session 持久化失败时的状态更新测试

验收标准：

- [ ] planned 路径与普通对话路径对同类错误给出一致的可恢复 / 终止判定
- [ ] 失败路径的 session 状态更新具备测试覆盖

### 2. 后台日志可排障

`output.log` 由 `trace.Create` 创建为空文件，启动时间、参数、运行模式目前只打印到 stdout（`cmd/agent/main.go:330`），后台任务跑完之后难以回溯。

- [ ] 在 `output.log` 头部写入启动时间、运行模式、模型、session_id、关键参数
- [ ] 启动失败时把失败原因也写入日志而不只是 stderr

验收标准：

- [ ] 仅凭 `output.log` 能判断一次后台运行的启动配置与失败原因

### 3. Server 诊断能力

`/healthz` 仍然只返回 `{"status":"ok"}`（`internal/server/http.go:120`）。`/api/v1/status` 已经能返回 workspace 与 session 维度的 LLM 运行时信息，但没有覆盖依赖自检。

- [ ] 增加轻量自检：配置是否完整、session store 是否可写、planner 是否可用
- [ ] 决定自检放在 `/healthz`、`/api/v1/status` 还是独立端点

验收标准：

- [ ] server 启动后可以通过一次 HTTP 调用判断核心依赖是否就绪

### 4. README 补测试入口

`Makefile` 已有 `test` 目标，但 `README.md` 与 `README_CN.md` 都没有提到 `make test` 或 `go test ./...`。

- [ ] 在两份 README 的构建章节后补充测试命令与本地验证前置条件

验收标准：

- [ ] 新人能从 README 直接找到运行测试的方式

### 5. 新特性文档与待办补齐

自本文件上次更新以来新增的模块尚未纳入任何计划跟踪：MCP 客户端、子 Agent（`internal/subagent`）、外部编码 Agent 接入（`internal/extagent`）、QQ / 微信 / ServerChan 渠道、嵌入式 WebUI、内联 TUI、RunTrace 与 eval harness、prompt 缓存与上下文压缩、结构化 memory、provider 弹性重试。

- [ ] 逐模块确认 `docs/doc.md` 覆盖是否完整
- [ ] 为覆盖不足的模块补充待办条目

## 已归档

以下内容已完成或已被更好的实现取代，保留结论以免重复评估。

- **P0 主流程收敛**：`internal/runtime/runtime.go` 统一了 client / planner / catalog 组装与配置读取，`internal/runtime/conversation.go` 统一了 session 创建、恢复、system message 注入与状态更新，CLI / Chat / Server 共用同一套核心运行逻辑。
- **P1 工具错误处理**：错误分层已建立，工具参数错误与执行错误优先回传 tool message，迭代超限反馈已补充上下文，非法 JSON / 工具报错 / plan 工具缺参 / planner 失败 / recorder 失败均有测试。
- **P2 Workspace 约定统一**：`.agent/` 为主布局、`workspace/` 为兼容布局，文档与实现已对齐，memory 启用条件已收紧。探测逻辑在 `Discover` 中明确为「本地 `.agent/` > `.git` > `go.mod`」逐级向上，并排除把全局 `~/.agent` 误判为工作区标记，对应测试为 `TestDiscoverFindsNearestWorkspaceMarker` 与 `TestDiscoverDoesNotTreatGlobalAgentDirectoryAsWorkspaceMarker`。
- **结构化日志**：原计划在文本日志里补字段，实际由 `internal/trace` 的 RunTrace 取代。`RunTrace` 已记录 `run_id`、`parent_run_id`、`session_id`、`turn_id`、`kind`、`status`、`model`、`usage`、`retry_count`、`error`，事件以 JSONL 落盘，`tool_call` 事件带工具名、参数、结果哈希、`duration_ms` 与 `error_category`。
- **耗时指标**：`internal/agent/observability.go` 输出 `[Model]`、`[Tool]`、`[Turn]` 三类耗时日志。
- **开发命令与脚本**：`Makefile` 提供 `build` / `build-amd` / `build-windows` / `webui-build` / `test` / `eval` / `eval-all` / `clean`。
- **文档结构与编码**：`README.md` 与 `README_CN.md` 章节一一对应同步维护，`README_CN.md` 为无 BOM 的 UTF-8；完整手册见 `docs/doc.md`，另有 `docs/TUI.md` 与 `docs/CHANGELOG.md`。
