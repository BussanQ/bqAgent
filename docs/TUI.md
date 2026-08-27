# bqAgent 内联终端 TUI

不带参数启动 `bqagent`（或显式使用 `bqagent --chat`）时，真实 TTY 默认进入内联 TUI。它不会切换备用屏幕；已完成的用户消息、Markdown 回复和工具摘要会进入终端原生 scrollback。只有出现可点击的折叠工具组时才临时开启鼠标追踪，工具组提交或清理后会自动关闭。

```text
bqAgent  Harness
工作区  D:\Dev\my-project
模型    openai/gpt-5

────────────────────────────────────────────────────────
你
请检查测试失败原因

失败来自配置加载顺序……

⚙ #1 read_file path="go.mod"
✓ #1 read_file · 8ms

────────────────────────────────────────────────────────
❯ 输入消息，/ 查看命令
openai/gpt-5 · 20260827   Enter 发送 · Alt+Enter 换行 · / 命令
```

实时 token 会先显示在底部活动区域，块结束后再用 Glamour 渲染 Markdown 并写入 scrollback。状态栏显示 Provider、模型、短 Session ID、当前阶段、耗时和实时 token 估算；完成后显示首 token 延迟、token 数及 TPS。工具调用按 Tool ID 合并开始/完成事件，显示序号、关键参数、状态和耗时；前 5 次逐项展示，从第 6 次开始自动合并为成功/失败/运行中汇总，点击汇总行或按 `Ctrl+T` 可展开/收起详情。失败预览限制为 2 KiB/8 行，`todo_write` 会显示任务进度。

## 输入与快捷键

| 按键 | 行为 |
| --- | --- |
| `Enter` | 发送；运行中进入单一排队槽 |
| `Alt+Enter` | 插入换行 |
| `Tab` / `Shift+Tab` | 打开、补全或切换命令候选 |
| `↑` / `↓` | 移动多行光标或召回当前工作区历史 |
| `Esc` | 打断当前回复；空闲时关闭候选面板或收起工具详情 |
| `Ctrl+C` | 取消活动轮次；空闲时三秒内按两次退出 |
| `Ctrl+D` | 仅在空输入且空闲时退出 |
| `Ctrl+L` | 清理当前视口与草稿，不改变 Session |
| `Ctrl+T` | 展开或收起超过 5 次的工具调用组 |

达到 5 行或 200 字符的粘贴内容会折叠为原子 Chip；发送和历史召回时仍使用完整原文。自然完成后会自动发送排队内容，取消后则把它恢复到输入框。普通排队消息可按换行合并，Slash 命令不会与其他内容跨边界合并。

输入历史保存在 `~/.agent/tui/history/<workspace-sha256>.jsonl`，每个工作区独立，最多 500 条且不超过 1 MiB。目录和文件权限分别为 `0700`、`0600`（受操作系统权限模型约束）。

## 命令面板

- 本地命令：`/help`、`/clear`、`/exit`。
- Provider/能力命令：`/model`、`/skill`、`/memory`、`/feedback`、`/agent`。
- 外部 Agent：`/claude`、`/codex`、`/cursor`、`/opencode`、`/default`、`/stop`。

`/model` 参数来自当前 Provider 配置；`/skill` 候选会从工作区与全局 Skill/alias 动态刷新。`/clear` 保留旧 Session 文件，但清理当前显示、队列和 Session ID，下一条消息使用默认模型创建新 Session。`/exit` 会先取消并等待活动后台轮次退出。

使用 `--chat --resume <session-id>` 时，只回放最近的用户和助手文本，总显示预算为 200 KiB；会提示省略条数，单条超限消息只在 TUI 中截断，不修改持久 Session。

## TTY 回退与兼容性

stdin/stdout 不是 TTY，或 `TERM=dumb` 时，会自动使用原有逐行模式，便于管道、重定向和自动化测试。TUI 始终流式输出；`--stream` 继续接受，在 TUI 中与默认行为等价。设置 `NO_COLOR` 可关闭 TUI 与 Markdown 颜色。

交互模型参考并致谢 [CodeHamr TUI](https://github.com/codehamr/codehamr/tree/main/internal/tui)：本实现沿用其 Bubble Tea 同代技术栈和“原生 scrollback + 底部常驻输入区”的设计方向，并按 bqAgent 的 Provider、Session、工具事件、Skills 与外部 Agent 语义重新实现。

## Inline terminal TUI (English summary)

On a real TTY, no-argument startup and `--chat` use an inline, always-streaming Bubble Tea UI without an alternate screen. Mouse tracking is enabled only for a clickable collapsed tool group and disabled when the group is committed or cleared. Redirected input/output and `TERM=dumb` fall back to the legacy line mode. See the tables above for shortcuts, commands, queue behavior, persisted workspace history, resume limits, `NO_COLOR`, and the CodeHamr design acknowledgment.
