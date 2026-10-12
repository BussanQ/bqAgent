# bqAgent 内联终端 TUI

不带参数启动 `bqagent`（或显式使用 `bqagent --chat`）时，真实 TTY 默认进入内联 TUI。它不会切换备用屏幕；已完成的用户消息、Markdown 回复和工具摘要会进入终端原生 scrollback。只有出现可点击的折叠工具组时才临时开启鼠标追踪，工具组提交或清理后会自动关闭。

```text
bqAgent  Harness
工作区  D:\Dev\my-project

────────────────────────────────────────────────────────
▸ 请检查测试失败原因

失败来自配置加载顺序……

⚙ #1 read_file path="go.mod"
✓ #1 read_file · 8ms

────────────────────────────────────────────────────────
❯ 输入消息，/ 查看命令
openai/gpt-5 · Run · 20260827   Enter 发送 · Alt+Enter 换行 · / 命令
```

流式文本会先显示在底部活动区域，块结束后再用 Glamour 渲染 Markdown 并写入 scrollback。状态栏动态显示 Provider、模型、当前模式、当前阶段、耗时，以及 `in / out` token 数；完成后保留计数并显示首 token 延迟及 TPS。工具调用按 Tool ID 合并开始/完成事件，显示序号、关键参数、状态和耗时；前 5 次逐项展示，从第 6 次开始自动合并为成功/失败/运行中汇总，点击汇总行或按 `Ctrl+O` 可展开/收起详情。失败预览限制为 2 KiB/8 行，`todo_write` 会显示任务进度。 工具详情限制在当前终端可显示的行数内，超出时用 `PgUp` / `PgDn` 翻页；展开后收起会保留活动区域高度，避免旧内容进入终端历史而形成重复记录。翻页和展开/收起不会写入历史，工具组在下一轮或退出时只提交一次。流式生成时活动区显示最新部分；回答不再分页，能完整放下的短回答直接显示，长回答结束后立即完整写入终端历史，使用终端原生滚动查看，无需等待下一轮。窗口缩小导致回答放不下时也会完整输出一次，后续刷新、工具展开/收起或下一轮不会重复输出回答。工具详情与流式文本复用渲染缓存，空闲时停止 spinner 动画。

`in / out` 参考 [pi-tps](https://github.com/monotykamary/pi-tps) 按当前用户轮次累计，包括工具调用前后的模型请求。`in` 是各次请求的完整输入之和（包含缓存读取、写入，不等于当前上下文占用）；`out` 使用供应商返回的输出总量，包含其 usage 计入的推理 token。数字保留整数精度，例如 `in 12,345 · out 678`，下一轮发送时重新计数。

OpenAI Chat、OpenAI Responses、Anthropic 三条接口都在收到流式 usage 时立即校准。usage 尚未返回时，输入按请求内容估算，输出随文本、推理和工具参数增量更新，带 `~` 标记；供应商通常只在请求结束时提供准确输出量，因此不会把中途的字符估算冒充精确 token。缺失 usage 时保留 `~`，明确返回的 `0` 则显示为准确的零。此显示不额外调用 token 计数接口。

完成后，TPS 沿用最后一次文本回复的计算口径：`(输出 token − 推理 token − 1) / 首段至末段文本的秒数`，中途停顿仍计入耗时。TUI 不再按 `1000 tok/s` 等速度上限过滤，而是参考 pi-tps 的采样可靠性检测：首段之后至少 5 次非空更新、平均更新间隔至少 1ms、扣除 ≥500ms 间隙后的观测窗口至少 200ms，且这些间隙不能占据窗口的大部分。这里只用间隙判断采样质量，不从 TPS 分母中扣除。样本不足、短时集中补发、缺少输出 usage 等情况下显示 `TPS —`；通过检测的高速输出正常显示。该检测是客户端启发式判断，无法还原供应商隐藏的服务端生成时间。

## 输入与快捷键

| 按键 | 行为 |
| --- | --- |
| `Enter` | 发送；运行中进入单一排队槽 |
| `Alt+Enter` | 插入换行 |
| `Tab` / `Shift+Tab` | 打开、补全或切换命令候选 |
| `↑` / `↓` | 移动多行光标或召回当前工作区历史 |
| `Esc` | 打断当前回复；空闲时关闭候选面板或收起工具详情 |
| `Ctrl+C` | 首次复制选中文本并提示；2 秒内连续再按一次安全退出 |
| `Ctrl+V` | 从系统剪贴板粘贴到输入框 |
| `Ctrl+Q` | 安全退出；有活动轮次时先取消并等待结束 |
| `Ctrl+L` | 清理当前视口与草稿，不改变 Session |
| `Ctrl+O` | 展开或收起超过 5 次的工具调用组 |
| `PgUp` / `PgDn` | 工具详情显示时，上下翻页查看超出窗口的内容 |

达到 5 行或 200 字符的粘贴内容会折叠为原子 Chip；发送和历史召回时仍使用完整原文。自然完成后会自动发送排队内容，取消后则把它恢复到输入框。普通排队消息可按换行合并，Slash 命令不会与其他内容跨边界合并。

两次 `Ctrl+C` 退出也适用于 Provider 配置界面。首次按下不取消当前任务；确认退出后，有活动任务时先取消并等待结束，不再启动排队任务。按其他键、粘贴、鼠标点击或超过 2 秒都会取消此次退出确认。

输入历史保存在 `~/.agent/tui/history/<workspace-sha256>.jsonl`，每个工作区独立，最多 500 条且不超过 1 MiB。目录和文件权限分别为 `0700`、`0600`（受操作系统权限模型约束）。

## 命令面板

- 本地命令：`/help`、`/provider`、`/clear`、`/exit`。
- Provider/能力命令：`/model`、`/effort`、`/fast`、`/skill`、`/memory`、`/feedback`、`/agent`。
- 外部 Agent：`/claude`、`/codex`、`/cursor`、`/opencode`、`/pi`、`/default`、`/stop`。

`/provider` 会打开独立的分步配置向导，依次设置 Provider ID、API 协议、API 地址、API Key 和模型；Provider 名称直接使用第一步输入的 ID。模型既可从 Provider 的 `/models` 接口自动获取，也可手工输入；多个自定义模型使用英文逗号分隔，第一个模型自动成为默认模型。API Key 使用密码输入并加密保存到全局 `~/.agent/config.json`；编辑已有 Provider 时留空可保留原密钥。保存后新 Provider 会立即成为当前 Provider，并刷新 `/model` 候选。

输入 `/model` 即展示当前 Provider 的模型列表，↑/↓ 选择、Enter 直接切换，Tab 仅补全到输入框；模型超过 8 个时列表随选择滚动。模型列表和右下角统一显示 `provider/模型名称`：优先保留模型 ID 的来源前缀（如 `group`），无前缀时使用当前配置的 Provider，未配置时回退到 API 协议；实际请求仍使用原始模型 ID。`/model` 参数来自当前 Provider 配置；`/skill` 候选会从工作区与全局 Skill/alias 动态刷新。`/clear` 保留旧 Session 文件，但清理当前显示、队列和 Session ID，下一条消息使用默认模型创建新 Session。`/exit` 会先取消并等待活动后台轮次退出。

使用 `--chat --resume <session-id>` 时，只回放最近的用户和助手文本，总显示预算为 200 KiB；会提示省略条数，单条超限消息只在 TUI 中截断，不修改持久 Session。

## Herdr 集成

在 Herdr 窗格中启动 bqAgent 即可自动启用，无需安装插件或配置额外依赖。遵循 [Herdr 接入协议](https://herdr.dev/zh-cn/docs/add-herdr-support/)，仅当 `HERDR_ENV=1` 且 `HERDR_PANE_ID`、`HERDR_BIN_PATH`、`HERDR_SOCKET_PATH` 均存在时启用；其他终端不启动发送器、不执行 Herdr 命令。

- TUI 和兼容逐行模式：等待输入为 `idle`，处理请求为 `working`；TUI 的 Provider 配置等待用户选择时为 `blocked`。
- 前台单次任务：运行期间为 `working`。退出时释放窗格；HTTP/Bot 服务、后台任务和内部子智能体 worker 不接管窗格。
- 会话创建后、模型开始工作前更新恢复信息。恢复命令为 `bqagent --chat [--plan] [--stream] --resume <session-id>`，保留相应启动选项；模型和会话模式使用已有的持久会话配置。`/clear` 改为恢复一个新的空会话，不会重新打开旧会话。
- 使用单个后台发送器，仅保留最新待发送状态，重复状态不发送，也不逐 token 上报。每次状态上报限时 1 秒，为 CLI 启动和 IPC 留出时间，错误静默忽略；退出取消在途上报并等待释放完成，释放调用使用独立的 1 秒超时，整个退出额外等待上限为 1.2 秒，完成即返回。运行期间主流程不等待 Herdr 响应，不执行重试循环。

恢复要求 Herdr 0.9.2+，并且 `bqagent` 在 `PATH` 中；不能使用绝对路径作为恢复命令首项。单次任务恢复后进入交互会话，保留上下文并等待新的指令。需要验证时，可在 Herdr 窗格中运行 `herdr pane get "$HERDR_PANE_ID"` 或 `herdr agent list`。进程被强制终止而无法释放时，由 Herdr 自身清理机制兜底。上报仅含状态、会话 ID 和恢复参数，不含提示词、回复或工具输出。

## TTY 回退与兼容性

stdin/stdout 不是 TTY，或 `TERM=dumb` 时，会自动使用原有逐行模式，便于管道、重定向和自动化测试。TUI 始终流式输出；`--stream` 继续接受，在 TUI 中与默认行为等价。设置 `NO_COLOR` 可关闭 TUI 与 Markdown 颜色。

交互模型参考并致谢 [CodeHamr TUI](https://github.com/codehamr/codehamr/tree/main/internal/tui)：本实现沿用其 Bubble Tea 同代技术栈和“原生 scrollback + 底部常驻输入区”的设计方向，并按 bqAgent 的 Provider、Session、工具事件、Skills 与外部 Agent 语义重新实现。

## Inline terminal TUI (English summary)

On a real TTY, no-argument startup and `--chat` use an inline, always-streaming Bubble Tea UI without an alternate screen. Mouse tracking is enabled only for a clickable collapsed tool group and disabled when the group is committed or cleared. `/provider` opens a sequential Provider setup wizard with encrypted API-key storage and either automatic model discovery or comma-separated custom models; the first custom model becomes the default. Redirected input/output and `TERM=dumb` fall back to the legacy line mode. See the tables above for shortcuts, commands, queue behavior, persisted workspace history, resume limits, `NO_COLOR`, and the CodeHamr design acknowledgment.

The status bar shows full-precision `in / out` totals for the current user turn, including tool-call rounds. Input includes cached tokens; output includes reasoning counted by the provider. All three native API adapters publish usage immediately when received. Until then, estimates update with streamed text, reasoning, and tool arguments and remain marked `~`. Missing usage stays estimated; explicit zero is authoritative. Counts persist after completion and reset on the next turn. No extra token-count API requests are made.

Completed-turn TPS uses the final text response and keeps pauses in its generation duration. The TUI applies no fixed TPS ceiling. Inspired by pi-tps, the agent checks sample count, spacing, observable streaming duration, and pause-dominated bursts before publishing a rate; insufficient evidence displays `TPS —`. These client-side heuristics cannot recover hidden server-side timing.

Press `Ctrl+C` twice within two seconds to exit safely, including in Provider setup. The first press still copies selected prompt text and shows a confirmation hint; the second cancels any active turn and waits for it to finish. Other keys, paste, mouse clicks, or timeout cancel the confirmation. `Ctrl+Q` remains a one-step safe exit shortcut.

Herdr support is automatic only when all four Herdr environment variables above are present and `HERDR_ENV=1`. Foreground TUI, line mode, and one-shot tasks report activity and session recovery metadata; Provider setup reports `blocked`. A single background sender coalesces pending states, ignores failures, and allows each status report up to 1 second for CLI startup and IPC; shutdown cancels pending work and waits for release with a separate 1-second timeout and at most 1.2 seconds of extra waiting, returning as soon as release finishes. Reporting never blocks model work. No token-by-token reporting or retry loop is used. Background jobs, internal workers, and HTTP/Bot services do not claim a pane. Recovery requires Herdr 0.9.2+ and `bqagent` on `PATH`; it opens the saved session in chat mode, preserving plan/stream flags and persisted model/mode settings. Clearing the session updates recovery to a new empty chat. Prompts, replies, and tool output are never reported.

`/fast` 在开启、关闭之间循环切换，默认关闭；也支持 `/fast on`、`/fast off`、`/fast status`。设置按会话保存，恢复会话后继续生效，新会话默认关闭。开启后，OpenAI Chat Completions / Responses 请求携带 `service_tier=priority`，关闭后省略此参数。适用于支持该字段的 Codex、Cursor 等兼容 API；是否提供加速及其计费由上游决定，不支持的上游可能返回错误。Anthropic 协议暂不支持开启。此命令控制 bqAgent 自身 API 请求，不控制 `/codex`、`/cursor` 外部 Agent 进程。

## 长编程任务的连续执行

Run 模式与命令行任务提供内部工具 `task_update`，用于多步骤编程任务。模型应在修改前登记原始目标、验收项和剩余工作，并在取得进展后记录实际验证结果。状态保存在当前会话的 `task.json`，独立于对话压缩；恢复会话或发送“继续”“改完了吗”时会重新注入当前目标与未完成项。普通问答和小型单步修改不必创建任务，Ask 模式不暴露此写入工具。

Responses 接口会保留原始输出项、顺序及 assistant 的 `phase`，包括流式响应和历史回传。只有进度说明（`commentary`）时继续执行；输出截断按未完成处理，其他 incomplete 响应返回错误。对已登记且仍 active 的任务，最终文字回复会触发收尾检查；标记 completed 要求验收项均有完成证据、无剩余工作，并有通过的验证记录。模型遗忘登记时，多步工具工作后的第一次收尾也会要求复核原始需求；分析或普通问答可直接给出答案。

自动续行不限制累计次数；只要有实质工具进展，就在同一轮对话中继续实现、验证和收尾。成功但没有输出的工具调用（例如写入文件的脚本）也算进展。连续 2 次续行仍无实质工具进展时，后续回复会触发恢复指令，要求检查最新失败输出和剩余工作、改变方法并执行下一步，不再因此提前结束或要求用户发送“继续”。失败调用、空搜索和仅更新任务/待办不会重置恢复计数；测试失败后应修复原因并重跑允许的定向测试。既有轮次、阶段时间和取消限制仍然有效。真正受阻或需要用户决策时记录 blocked / awaiting_input 及原因即可结束；修改原始目标或删除验收项需显式注明用户变更范围的原因。验证记录来自模型对工具结果的归纳，不能替代实际执行对应验证。外部 `/codex`、`/cursor` 进程仍由各自运行时管理。

输入 `/effort` 展示努力程度列表，↑/↓ 选择、Enter 直接确认，Tab 仅补全。`/effort auto|low|medium|high|max` 设置后续请求的档位。与 WebUI 复用努力程度解析及模型请求链路，`auto` 不发送显式 effort；`max` 请求最高档，实际支持取决于模型和上游。不支持参数时沿用已有兼容回退。设置保留在当前 TUI 进程中，切换或新建会话后继续使用，退出重启恢复 auto；不修改 `/codex`、`/cursor` 外部 CLI/ACP 的配置。

输入 `/` 可浏览完整命令列表，`/effort` 在首屏可见。命令和参数候选每屏显示 8 项，↑/↓ 滚动并循环选择，标题显示当前位置和总数；Skill/alias 和 `/agent` 子命令也保留全部候选。
