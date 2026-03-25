# TODO

本文件用于沉淀当前项目的优化计划，聚焦结构收敛、稳定性、约定统一、可观测性和开发体验，不包含工具安全边界相关项。

## 已完成进度

- [x] 已明确 `.agent/` 为 Workspace 主布局，`workspace/` 仅作为兼容布局
- [x] 已完成 `.agent/` 优先、`workspace/` 回退读取的兼容实现
- [x] 已更新 `README.md` 与 `README_CN.md`，统一说明 `.agent/` 为主布局
- [x] 已补充 Workspace 兼容与优先级测试
- [x] 已抽离共享运行时构造，统一 `client / planner / catalog` 的组装
- [x] 已抽离共享会话流程，统一 session 创建、恢复、system message 注入与状态更新
- [x] 已初步优化工具错误处理，让常见工具错误优先回传为 tool message
- [x] 已完成 P1 第一批错误处理改造：plan 工具错误改为可恢复、统一部分工具错误文案、改进迭代超限反馈、补充 planner / tool / recorder 相关测试

## P0 主流程收敛

目标：统一 CLI、Chat、Server 三条执行链路，减少重复逻辑，降低后续功能演进成本。

- [x] 抽离共享运行入口
  - [x] 将 `client`、`planner`、`catalog` 的组装从 `cmd/agent/main.go` 和 `internal/server/service.go` 中抽离
  - [x] 形成统一的共享运行时层 `internal/runtime/runtime.go`
- [x] 合并重复会话流程
  - [x] 统一 session 加载与创建逻辑
  - [x] 统一 system message 注入逻辑
  - [x] 统一 user message 记录逻辑
  - [x] 统一会话完成、失败、恢复状态更新逻辑
  - [x] 继续收敛 assistant / tool message 相关流程，评估是否还需要进一步抽象（已评估：`appendToolMessage` 已抽取，assistant message 录制模式一致，无需进一步抽象）
- [x] 收口配置读取
  - [x] 集中读取 `OPENAI_API_KEY`、`OPENAI_BASE_URL`、`OPENAI_MODEL`、`SEARCH_API_KEY`、`SEARCH_BASE_URL`
  - [x] 避免 CLI 与 Server 分别拼装同一套依赖
- [x] 降低入口文件复杂度
  - [x] 让 `cmd/agent/main.go` 更多只处理参数解析与模式分发
  - [x] 让 `internal/server/service.go` 更多只处理 HTTP 场景的输入输出适配

验收标准：

- [x] `main.go` 与 `service.go` 中重复代码明显减少
- [x] CLI、Chat、Server 共用同一套核心运行逻辑
- [x] 现有行为保持兼容

## P1 错误处理重构

目标：提升 agent 对工具失败、规划失败、持久化失败的容错能力，避免“一出错整轮终止”。

- [x] 建立错误分层
  - [x] 区分工具参数错误、工具执行错误、模型调用错误、会话持久化错误
  - [x] 明确哪些错误可恢复，哪些错误必须终止
- [x] 调整工具失败策略
  - [x] 工具参数 JSON 错误优先返回 tool message，而不是直接终止整轮
  - [x] 工具执行失败时优先返回 tool message，而不是直接终止整轮
  - [x] 继续梳理所有工具错误文案与返回格式，统一可恢复错误语义
- [~] 统一 planner 与普通对话的失败语义
  - [ ] 对齐 `RunConversation`、`RunConversationTurn`、`RunPlannedConversation` 的错误行为
  - [x] 明确 `plan` 工具失败后的返回形式
- [x] 优化迭代终止反馈
  - [x] 评估 `Max iterations reached` 的输出是否足够清晰
  - [x] 为超限场景补充更明确的上下文信息
- [~] 补测试覆盖
  - [x] 非法 JSON 参数时的行为
  - [x] 工具返回错误时的 transcript 行为
  - [x] plan 工具缺参与 planner 失败时回传 tool message 的行为
  - [x] planner 直接失败 / 无步骤时的行为
  - [x] recorder 失败时的中止行为
  - [ ] planner 失败时的状态更新
  - [ ] session 持久化失败时的状态更新

验收标准：

- [x] 常见工具错误不再直接中断整个 agent 循环
- [~] planner 与普通对话路径的错误语义已部分对齐（`plan` 工具路径已收敛，`RunPlannedConversation` 仍待进一步评估）
- [~] 关键失败路径已补充主要测试，状态更新相关路径仍待覆盖

## P2 Workspace 约定统一

目标：统一 README 与实现中的 Workspace 目录语义，降低理解成本和迁移成本。

- [x] 明确主推荐布局
  - [x] 确定以 `.agent/` 作为主推荐布局
  - [x] 如需兼容 `workspace/`，将其标记为兼容布局而非主布局
- [x] 修正代码与文档不一致
  - [x] 对齐 README 中的目录说明与 `internal/workspace` 的实现，统一以 `.agent/` 为中心描述
  - [x] 对齐上下文文件、memory 文件、skills/rules 的定位逻辑
- [ ] 调整 workspace 探测逻辑
  - [ ] 明确根目录判定条件
  - [ ] 明确多种标记同时存在时的优先级
- [x] 收紧 memory 启用条件
  - [x] 不再因为目录存在就默认开启 memory
  - [x] 改为基于具体文件或明确目录结构判断
- [x] 补迁移说明
  - [x] 说明旧 `workspace/` 布局如何迁移到 `.agent/`
  - [x] 说明两套布局同时存在时默认以 `.agent/` 为准

验收标准：

- [x] 文档和代码中的 Workspace 概念一致，并明确 `.agent/` 是主布局
- [x] memory 启用逻辑可预测
- [x] Workspace 兼容与优先级具备测试覆盖
- [ ] workspace 根目录发现逻辑继续补充测试

## P3 可观测性补齐

目标：让前台、后台、服务模式的运行状态更容易排查和诊断。

- [ ] 增加结构化日志信息
  - [ ] 记录 `session_id`
  - [ ] 记录运行模式：`foreground` / `chat` / `background` / `server`
  - [ ] 记录 `model`、`iteration`、`tool_name`、`status`
- [x] 增加耗时指标
  - [x] 记录模型请求耗时
  - [x] 记录工具执行耗时
  - [x] 记录单轮对话耗时
- [ ] 增强后台日志可读性
  - [ ] 在 `output.log` 头部记录启动时间、参数、模式
  - [ ] 启动失败时输出更清晰的原因
- [ ] 增强服务诊断能力
  - [ ] 在 `healthz` 之外增加轻量诊断信息或启动自检
  - [ ] 至少能判断配置是否完整、session store 是否可写、planner 是否可用
- [ ] 统一日志格式
  - [ ] 约定 agent 输出、tool 输出、系统错误输出的格式
  - [ ] 避免不同模式下日志风格差异过大

验收标准：

- [ ] `output.log` 能支持基本排障
- [ ] server 模式具备最小诊断能力
- [ ] 核心耗时数据可见

## P4 文档与开发体验

目标：让项目更容易上手、运行、测试和维护。

- [~] 重构 README / README_CN 结构
  - [x] 已对齐 Workspace 主布局说明
  - [ ] 继续整理快速开始、模式说明、session 生命周期、server 使用方式、常见问题
- [~] 对齐中英文文档内容
  - [x] 已同步 `.agent/` 主布局与兼容布局说明
  - [ ] 继续检查参数、示例、目录结构与行为说明的一致性
- [ ] 检查中文文档编码与显示
  - [ ] 确认 `README_CN.md` 使用 UTF-8
  - [ ] 确认在 Windows PowerShell 下显示正常
- [ ] 增加开发辅助命令
  - [ ] 构建命令
  - [ ] 测试命令
  - [ ] 本地 server 启动命令
  - [ ] 示例调用命令
- [ ] 增加开发脚本
  - [ ] 视项目风格补充 `Makefile`、`.cmd`、或其他简单任务脚本
  - [ ] 降低重复输入成本
- [ ] 明确测试入口与验证方式
  - [ ] README 中说明如何运行 `go test ./...`
  - [ ] 说明本地验证前置条件

验收标准：

- [ ] 新人可以按 README 在较短时间内跑通项目
- [ ] 中英文文档保持同步
- [ ] 本地构建与测试路径清晰

## 建议执行顺序

- [x] 第 1 阶段：主流程收敛
- [~] 第 2 阶段：错误处理重构
- [x] 第 3 阶段：Workspace 约定统一
- [ ] 第 4 阶段：可观测性补齐
- [ ] 第 5 阶段：文档与开发体验

## 可选后续

- [ ] 为每个阶段拆成独立 issue 或 milestone
- [ ] 在完成阶段后补充对应 changelog
- [ ] 对每个验收标准补自动化测试或手工验证记录
