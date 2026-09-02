# bqagent

[English](./README.md) | 中文

![bqagent WebUI 界面](./docs/images/webui-overview.png)

> *"问题不在于你看到了什么，而在于你看见了什么。"* — 梭罗

bqagent 是一个为真实本地工作流而生的 Agent Runtime：既保留小而清晰的执行核心，也具备多 Agent 编排、长期记忆、完整上下文管理与多通道交互能力。

完整使用说明请参阅[项目文档](./docs/doc.md)；终端快捷键、命令面板、TTY 回退与 scrollback 行为请参阅[内联 TUI 指南](./docs/TUI.md)。

## 为什么是 bqagent？

> **一个二进制，装下一支会协作、会记忆、能长期工作的 Agent 团队。**
>
> bqagent 不只是把大模型接到 Shell，而是把模型、工具、技能、记忆、工作区与多种交互通道组装成一套完整的本地 Agent Harness。

|  | 核心特性 | 让它与众不同的地方 |
| :---: | --- | --- |
| 🧭 | **多 Agent 调度** | 原生编排 Claude、Codex、Cursor 与 OpenCode：既能通过 @ 精确路由，也能由 bqagent 协调成员并汇总结论；还可在隔离 Git worktree 中并行执行子任务，以日志和补丁安全交付结果。 |
| 🛠️ | **强大的 Agent Harness** | 将规划、工具调用、渐进式 Skill、规则、MCP、网页搜索、取消控制、重试降级与 RunTrace 串成统一执行循环，让模型从“能回答”进化到“能把事情做完”。 |
| 🖥️ | **TUI + WebUI 双轨体验** | 终端侧提供始终流式的内联 TUI、原生 scrollback、命令面板与任务队列；浏览器侧提供 SSE 流式对话、Markdown、附件、工作区文件树、模型切换和一键停止。 |
| 🦞 | **龙虾级灵魂与记忆** | 继承 OpenClaw 风格的 `AGENT.md`、`SOUL.md`、`USER.md`、`TOOLS.md`、Rules 与 Skills；结构化 Memory 支持版本、来源、置信度、替代关系、敏感确认和中文检索。 |
| 📦 | **单一二进制，极简部署** | Go 后端与完整 WebUI 通过 `go:embed` 打进同一个可执行文件；运行时不需要 Node.js、Vite、CDN 或外置静态资源，复制一个 `bqagent` 即可启动。 |
| 🧠 | **上下文与工作区是一等公民** | 自动识别 workspace root，叠加全局与项目级配置；长对话支持预算裁剪、摘要压缩、checkpoint 和持久恢复，在控制上下文体积的同时保住任务连续性。 |
| 🌐 | **多 Channel 对话** | 同一套 Agent 能力可运行于 CLI、TUI、WebUI、微信 iLink、QQ Bot 与 ServerChan Bot；渠道不同，工作流、会话和记忆依然连贯。 |
| 🔌 | **多模型、多协议接入** | 同时支持 OpenAI Chat Completions、OpenAI Responses 与 Anthropic Messages，可配置兼容端点并在会话中切换模型。 |
| 🔍 | **可观测、可恢复、可评估** | 持久 Session、结构化 RunTrace、反馈与 replay/live eval 让每次执行都有迹可循；流式 idle watchdog、循环保护与错误重试为长期运行兜底。 |

无论是在终端里完成一次代码修改、在 WebUI 中经营长期项目，还是让多个 Agent 与聊天通道共同协作，bqagent 都提供同一套可控、可扩展、可恢复的执行底座。
