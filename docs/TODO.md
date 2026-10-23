# TODO

本文件记录当前仍待推进的工作项。历史上的 P0 主流程收敛、P1 错误处理第一批改造、P2 Workspace 约定统一、Server 诊断与就绪检查、后台日志排障与新特性文档主体已经完成，相关内容归档在文末「已归档」一节，不再逐项跟踪。

最近一次对照代码核实：2026-10-23。

## 待办

### 1. 文档补测试入口

`README.md` / `README_CN.md` 已改为项目介绍页，详细说明链接到 `docs/doc.md` / `docs/en/doc.md`。两份手册的「安装」一节只介绍了 `make build` / `make webui-build`，所有文档都没有提到 `make test` 或 `go test`。

- [ ] 在 `docs/doc.md` 与 `docs/en/doc.md` 的「安装」一节后补充测试命令（`make test`、按包 / 按用例运行单测）与前置条件（如需先 `make webui-build` 生成嵌入资源）

验收标准：

- [ ] 新人能从手册直接找到运行测试的方式

### 2. 手册薄弱章节补齐

新增模块在 `docs/doc.md` 中已基本有对应章节（MCP、子 Agent、外部编码 Agent、QQ / iLink / ServerChan 渠道、WebUI、TUI、RunTrace 与 eval、上下文压缩、结构化 Memory、prompt 前缀缓存）。以下内容仍然偏薄：

- [ ] MCP：只说明了 `MCP_ALLOWED_ENV`，缺少 `mcp.json` 的格式、传输方式与示例
- [ ] Provider 弹性重试：仅描述了搜索相关重试，缺少模型请求的重试、降级与超时策略
- [ ] 任务工具：`ENABLE_TASK_TOOL` 只在环境变量表中出现，缺少 `task_update` / `task_scope` 的行为、任务上下文注入与回合结果（blocked / awaiting_input）说明
- [ ] Bash 后台命令：缺少长命令转后台、`bash_wait` / `bash_kill` 与轮次结束清理的说明

验收标准：

- [ ] 上述模块在中英文手册中各有可独立阅读的说明，并与实现一致

## 已归档

以下内容已完成或已被更好的实现取代，保留结论以免重复评估。

- **P0 主流程收敛**：`internal/runtime/runtime.go` 统一了 client / catalog 组装与配置读取，`internal/runtime/conversation.go` 统一了 session 创建、恢复、system message 注入与状态更新，CLI / Chat / Server 共用同一套核心运行逻辑。
- **P1 工具错误处理**：错误分层已建立，工具参数错误与执行错误优先回传 tool message，迭代超限反馈已补充上下文，非法 JSON / 工具报错 / recorder 失败均有测试。
- **P2 Workspace 约定统一**：`.agent/` 为主布局、`workspace/` 为兼容布局，文档与实现已对齐，memory 启用条件已收紧。探测逻辑在 `Discover` 中明确为「本地 `.agent/` > `.git` > `go.mod`」逐级向上，并排除把全局 `~/.agent` 误判为工作区标记，对应测试为 `TestDiscoverFindsNearestWorkspaceMarker` 与 `TestDiscoverDoesNotTreatGlobalAgentDirectoryAsWorkspaceMarker`。
- **结构化日志**：原计划在文本日志里补字段，实际由 `internal/trace` 的 RunTrace 取代。`RunTrace` 已记录 `run_id`、`parent_run_id`、`session_id`、`turn_id`、`kind`、`status`、`model`、`usage`、`retry_count`、`error`，事件以 JSONL 落盘，`tool_call` 事件带工具名、参数、结果哈希、`duration_ms` 与 `error_category`。RunTrace 默认关闭（`RUN_TRACE_ENABLED=false`），默认情况下的排障依赖会话 `output.log`，见下一条。
- **耗时指标**：`internal/agent/observability.go` 输出 `[Model]`、`[Tool]`、`[Turn]` 三类耗时日志。
- **后台日志排障**：后台子进程（`--session-run`）的 stdout / stderr 由 `startBackgroundProcess` 重定向到会话 `output.log` 并带时间戳，启动后的失败原因与 `[Model]` / `[Tool]` / `[Turn]` 耗时日志都会落盘。运行开始时写入 `[Session] id=… mode=background|foreground` 与 `[Runtime] api_type=… model=… provider=… workspace=… max_iterations=… task_tool=… run_trace=… context=…`（含上下文预算），不开启 RunTrace 也能从 `output.log` 判断运行配置，对应测试为 `TestBackgroundSessionLogsRunConfigWithoutAutoMemory`。
- **Server 诊断与就绪检查**：`internal/doctor` 提供本地只读快照与主动探测（存储写入、外部 Agent 初始化、MCP 工具发现），入口为 `bqagent --doctor [--json]` 与 `/api/v1/webui/doctor`。外部监控使用无需登录的 `GET /readyz`，基于 doctor 只读快照返回 `{"ready":true/false}`（就绪 200、未就绪 503，不含配置详情）；`/healthz` 保持为固定返回 `ok` 的存活探针。说明见 `docs/doc.md`「系统诊断与就绪检查」。
- **新特性文档主体**：MCP 客户端、子 Agent、外部编码 Agent、各渠道、嵌入式 WebUI、内联 TUI、RunTrace 与 eval、上下文压缩、结构化 Memory 均已纳入 `docs/doc.md`，TUI 另有 `docs/TUI.md`；尚薄弱的章节见待办 2。
- **开发命令与脚本**：`Makefile` 提供 `webui-deps` / `webui-build` / `webui-verify` / `build` / `build-amd` / `build-windows` / `test` / `eval` / `eval-all` / `clean`。
- **文档结构与编码**：`README.md` 与 `README_CN.md` 为章节一一对应的项目介绍页，`README_CN.md` 为无 BOM 的 UTF-8；完整手册见 `docs/doc.md`（中文）与 `docs/en/doc.md`（英文），另有 `docs/TUI.md` 与 `docs/CHANGELOG.md`。
