# bqAgent 内联终端 TUI

在终端执行 `bqagent -h` 或 `bqagent --help` 可查看启动参数、默认值和使用示例；帮助写入标准输出并以退出码 0 结束，不初始化工作区或启动模型。进入 TUI 后使用 `/help` 查看快捷键和交互命令。启动选项应放在任务描述前，`--` 后的内容按任务文本处理。

无任务启动 `bqagent` 时，要求 stdin、stdout 均为 TTY 且 `TERM` 不是 `dumb`，满足后进入内联 TUI；有任务则一次执行后退出。对话保持在主屏幕；查看已完成回合的工具详情时临时使用备用屏幕，关闭后恢复原对话；已完成的用户消息、Markdown 回复和工具摘要会进入终端原生 scrollback。运行中出现可点击的折叠工具组时会临时开启鼠标追踪；回合正常完成、取消或失败后，统一收起工具区并关闭主屏鼠标追踪，恢复终端原生滚轮，工具组提交或清理时也会关闭。回合结束后使用 `Ctrl+O` 查看工具详情（包括取消或失败的回合），详情页内支持鼠标操作，返回对话后恢复原生滚动。

```text
bqAgent  Harness
工作区  D:\Dev\my-project



── ❯❯ ─────────────────────────────────────────────────

▸ 请检查测试失败原因

失败来自配置加载顺序……

⚙ #1 read_file path="go.mod"
✓ #1 read_file · 8ms

────────────────────────────────────────────────────────
❯ 输入消息，/ 查看命令
openai/gpt-5 · Run · 20260827   Enter 发送 · Alt+Enter 换行 · / 命令
```

每轮问题前保留两行空白，并显示 `── ❯❯ ──` 风格的纯符号分隔线，按终端宽度延伸；分隔线与问题之间、问题与后续输出之间各留一行空白，便于在长历史中定位最近的问题和回答。恢复会话历史使用相同分隔，关闭颜色时仍保留横线和箭头符号。

聊天界面按终端高度固定布局，输入框和状态栏始终贴近底部；内部保留一行供原生历史插入使用。未用满屏幕时，启动信息和进行中的提问从终端上部开始排列，无需等到回答或工具出现后才移到顶部；工具紧接提问，回复紧接提问或工具记录，空余行放在回复下方，候选面板和输入区仍靠近底部。回答完成后，屏内保留的启动信息、对话历史和当前回复继续从顶部连续排列，不因任务结束或没有工具活动而整体切换到底部对齐，提问和回复之间也不会插入填充空白。最近的提问、已完成回复和工具记录会先保留在屏内，只有放不下的前部才移入终端原生 scrollback。流式输出、工具展开/收起和短回复完成不再改变整帧高度，避免反复清屏和输入框上下跳动。窗口缩小时只移出溢出内容，放大时不会重复打印已进入历史的记录。历史插入和随后生成的最终画面会合并为一次终端写入，避免暴露逐行插入时的滚屏和旧帧；支持 synchronized output 的终端将整批内容作为一次画面更新显示。

首次进入新会话时，中间留白区域会居中显示带 `bqAgent` 标识的 ASCII 机器人：圆角头壳、双眼和带 `>_` 提示符的胸前终端面板。造型随空间在 32–44 列、16–20 行内适配，大窗口下保持紧凑比例。浅紫色轮廓搭配青色眼睛，底座使用暗紫色，品牌文字高亮；`NO_COLOR` 下保留相同的纯字符造型。发送第一条消息后隐藏，恢复已有会话或窗口空间不足时不显示，字符画不会写入对话历史。

流式文本会紧接提问或工具记录显示，块结束后再用 Glamour 渲染 Markdown 并写入 scrollback。状态栏动态显示 Provider、模型、当前模式、当前阶段、耗时，以及 `in / out` token 数；完成后保留计数并显示首 token 延迟及 TPS。工具调用按 Tool ID 合并开始/完成事件，显示序号、关键参数、状态和耗时；序号按本轮工具首次出现的顺序从 `#1` 连续编号，不使用内部事件序号；前 5 次逐项展示，从第 6 次开始自动合并为成功/失败/运行中汇总，点击汇总行或按 `Ctrl+O` 可展开/收起详情。失败预览最多显示 3 个物理行；任务进度由保存成功的 `task_update` 状态驱动，显示在输入框上方。运行中的内联工具区限制在当前终端可显示的行数内，首次按 `Ctrl+O` 只展开每个工具一行的摘要列表，不展开输出或任务正文。点击工具行后才显示该工具的参数和输出，可用鼠标滚轮滚动；点击详情标题返回列表，`Ctrl+O` 收起工具区。列表较长时同样支持滚轮，汇总标题始终保留；不接管输入框的方向键或 `PgUp` / `PgDn`。收起后恢复紧凑布局，展开/收起不会重复写入历史。短回答的工具组在下一轮或退出时提交；长回答首次写入历史前，先提交工具记录，确保始终按“提问 → 工具 → 回答”的顺序展示，每组只提交一次。流式生成时活动区显示最新部分；最终回答在正文第一行前加 `●` 符号，不额外显示“结论”文字或占用一行，与前文进度和工具记录区分；该标识在长回答分段和历史输出中只出现一次。回答整体使用统一的两列前缀区域，后续正文、自动换行和段落与首行文字对齐，保留列表层级与代码内部缩进。回答不再分页，能完整放下的短回答直接显示，长回答结束后将超出屏幕的前部写入终端历史，并在当前屏幕保留结论末尾，使用终端原生滚动即可查看全文，无需等待下一轮。窗口缩小时也只将放不下的前部移入历史，结论末尾继续显示；后续刷新、工具展开/收起或下一轮不会重复输出回答。回合完成后，`Ctrl+O` 在独立的备用屏幕中打开本轮工具列表，每个工具显示一行摘要，即使工具没有输出也可查看参数。若历史仍在打印，会等待打印完成再打开；等待期间继续输入、粘贴或按 `Esc` 会取消待打开操作，输入框保持可用。使用 `↑` / `↓` 选择后按 `Enter`，或直接点击工具行，打开该工具的独立详情。列表随选择滚动，单项详情展示已收到的全部参数和输出，支持方向键、鼠标滚轮、`PgUp` / `PgDn` 滚动及 `Home` / `End` 跳到首尾；工具事件本身已截断的输出会明确提示，无法恢复上游未提供的内容。在详情页按 `Esc` 或点击顶部标题返回列表，再次返回则恢复对话；`Ctrl+O` 始终直接返回对话。无论回答长短，查看详情都不改变结论与工具记录的顺序，不挪动或重复写入原对话；查看期间输入框草稿保持不变。工具详情与流式文本复用渲染缓存，空闲时停止 spinner 动画。

`in / out` 参考 [pi-tps](https://github.com/monotykamary/pi-tps) 按当前用户轮次累计，包括工具调用前后的模型请求。`in` 是各次请求的完整输入之和（包含缓存读取、写入，不等于当前上下文占用）；`out` 使用供应商返回的输出总量，包含其 usage 计入的推理 token。数字保留整数精度，例如 `in 12,345 · out 678`，下一轮发送时重新计数。

OpenAI Chat、OpenAI Responses、Anthropic 三条接口都在收到流式 usage 时立即校准。usage 尚未返回时，输入按请求内容估算，输出随文本、推理和工具参数增量更新，带 `~` 标记；供应商通常只在请求结束时提供准确输出量，因此不会把中途的字符估算冒充精确 token。缺失 usage 时保留 `~`，明确返回的 `0` 则显示为准确的零。此显示不额外调用 token 计数接口。

完成后，TPS 沿用最后一次文本回复的计算口径：`(输出 token − 推理 token − 1) / 首段至末段文本的秒数`，中途停顿仍计入耗时。TUI 不再按 `1000 tok/s` 等速度上限过滤，而是参考 pi-tps 的采样可靠性检测：首段之后至少 5 次非空更新、平均更新间隔至少 1ms、扣除 ≥500ms 间隙后的观测窗口至少 200ms，且这些间隙不能占据窗口的大部分。这里只用间隙判断采样质量，不从 TPS 分母中扣除。样本不足、短时集中补发、缺少输出 usage 等情况下显示 `TPS —`；通过检测的高速输出正常显示。该检测是客户端启发式判断，无法还原供应商隐藏的服务端生成时间。

## 输入与快捷键

| 按键 | 行为 |
| --- | --- |
| `Enter` | 发送；内建 Agent 运行中发送纠偏，在当前响应及工具执行结束后的下次模型调用前生效 |
| `Alt+Q` / `Ctrl+S` | 内建 Agent 运行中排队后续任务，等当前工作自然结束再处理；空闲时保留草稿并提示按 Enter 发送 |
| `Alt+Enter` | 插入换行 |
| `Tab` / `Shift+Tab` | 打开、补全或切换命令候选；文件候选中 `Tab` 插入选中文件 |
| `@` | 打开工作区文件选择器，继续输入即可模糊搜索 |
| `↑` / `↓` | 移动多行光标或召回当前工作区历史 |
| `Esc` | 打断当前回复；空闲时关闭候选面板或收起工具详情 |
| `Ctrl+C` | 首次清空聊天输入框并提示；2 秒内连续再按一次安全退出 |
| `Ctrl+V` / `Alt+V` | 剪贴板有图片时插入 `[图片 #N]` 附件，否则粘贴文本；Windows Terminal 自身占用 `Ctrl+V`，请用 `Alt+V` |
| `Ctrl+Q` | 安全退出；有活动轮次时先取消并等待结束 |
| `Ctrl+L` | 清理当前视口与草稿，不改变 Session |
| `Ctrl+O` | 展开或收起工具调用组；回合完成后打开工具列表，查看期间直接返回对话 |
| 工具列表内 `↑` / `↓`、`Enter` | 选择工具并打开详情，也可直接点击工具行 |
| 工具详情内方向键、滚轮、`PgUp` / `PgDn` | 滚动单个工具内容；`Home` / `End` 跳到首尾，`Esc` 返回列表 |

macOS 终端若将 `Option+Q` 作为 `œ` 等字符输入，应用收不到 Alt 组合键；此时可直接使用 `Ctrl+S`，或在终端中启用 Option 作为 Alt/Meta。成功排队后，输入框清空并显示“待处理 · 跟进”。后续任务等待当前工作自然结束，并共享当前轮次剩余执行预算；若预算耗尽，未处理内容会恢复到输入框，需要再次发送。

达到 5 行或 200 字符的粘贴内容会折叠为原子 Chip；发送和历史召回时仍使用完整原文。内建 Agent 的纠偏与后续任务逐条保存，纠偏优先于后续任务；消息实际加入模型上下文时才进入对话记录。取消、失败或执行预算耗尽时，尚未处理的输入恢复到输入框。图片、`@文件`、Slash 命令及不支持运行中输入的后端仍使用下一轮排队槽：自然完成后自动发送，取消后恢复；该槽中的普通内容可按换行合并，Slash 命令不跨边界合并。

WebUI 运行中同样允许输入纯文本：`Enter` 或“纠偏”按钮调整当前工作，`Ctrl/Cmd+Enter` 或“排队后续”按钮添加后续任务，停止按钮保持独立。附件仍留待下一轮。API 可向 `POST /api/v1/chat/input` 提交 `turn_id`、`workspace_id`、`kind`（`steering` / `follow_up`）和 `text`；输入直接进入活动轮次队列，不等待会话锁。已结束的队列拒绝新输入，结束事件的 `pending_inputs` 返回尚未处理的消息。

### @ 引用工作区文件

在词首输入 `@` 会打开工作区文件选择器，继续输入即可搜索：文件名完全匹配、文件名前缀、文件名包含、路径前缀、路径包含、按字符顺序的模糊匹配依次排序，同级再按目录层级和路径长度排序；每行显示文件名与所在目录。`↑` / `↓` 选择，`Enter` 或 `Tab` 把当前光标处的 `@词` 替换为 `@相对路径 `（含空白的路径写为 `@"路径"`），`Esc` 关闭本次选择器且不影响正在运行的任务；没有匹配时 `Enter` 正常发送。`a@b.com` 这类词中的 `@` 不会触发。

- 文件索引在首次输入 `@` 时于后台生成：Git 仓库使用 `git ls-files --cached --others --exclude-standard`（遵循 `.gitignore`），否则遍历目录并跳过 `.git`、`node_modules`、`.venv` 等依赖与缓存目录；最多索引 5 万个文件，索引超过 10 秒后在下一次 `@` 时刷新。
- 发送时，消息中指向工作区内普通文件的 `@路径` 会作为附件随消息发送，与 WebUI 的 workspace 路径附件相同：文本内容内联（超过 64 KiB 截断并提示用 `read_file` 读取），二进制文件只提供路径。每条最多 5 个文件，单个不超过 2 MiB、合计不超过 6 MiB；超出限制、不存在、目录或指向工作区外的引用保留为普通文本，不会导致发送失败。末尾标点（如 `@go.mod,`）会被忽略。Slash 命令不附带文件。

### 图片输入

TUI 与 WebUI 使用相同的图片限制：仅支持 PNG、JPEG、GIF，每条消息最多 4 张，单张不超过 3 MiB，合计不超过 8 MiB，单张不超过 2000 万像素。

- 按 `Ctrl+V` / `Alt+V` 时先检查系统剪贴板：截图等图片数据或在文件管理器中复制的图片文件都会作为附件插入，标签形如 `[图片 #1]`；剪贴板中没有图片时按普通文本粘贴（长文本仍折叠为 Chip）。
- 粘贴或拖入终端的图片文件路径（支持引号、反斜杠转义的空格、`file://` 与 `~/`，多个路径以空白分隔）会自动识别为附件；只要有一项不是存在的 PNG/JPEG/GIF 文件，就按原文本粘贴。
- 超过 3 MiB 或 2000 万像素的 PNG/JPEG（如 Retina 截图）会在本地铺白底转为 JPEG 并按需对半缩小后再发送；GIF 不压缩，超限直接提示。WebP 等其他格式暂不支持。
- 图片标签与粘贴 Chip 一样是原子的，退格可整体删除；标签编号在当前进程内递增不复用。发送时标签文字保留在消息中，图片按标签顺序随消息发送；输入历史只记录去掉图片标签后的文字。
- 运行中发送的图片会随排队消息合并，合并后超限时保留在输入框并提示；取消后排队内容连同图片恢复到输入框。Slash 命令不能附带图片。
- 剪贴板读取：macOS 使用系统自带的 `osascript`；Windows 使用 PowerShell；Linux 在 Wayland 下使用 `wl-paste`（wl-clipboard），X11 下使用 `xclip`，均未安装时 `Ctrl+V` 只粘贴文本。状态栏会显示添加结果或失败原因，下一次按键后消失。

两次 `Ctrl+C` 退出也适用于 Provider 配置界面。首次按下不取消当前任务；确认退出后，有活动任务时先取消并等待结束，不再启动排队任务。按其他键、粘贴、鼠标点击或超过 2 秒都会取消此次退出确认。

输入历史保存在 `~/.agent/tui/history/<workspace-sha256>.jsonl`，每个工作区独立，最多 500 条且不超过 1 MiB。目录和文件权限分别为 `0700`、`0600`（受操作系统权限模型约束）。

## 命令面板

- 本地命令：`/help`、`/provider`、`/clear`、`/exit`。
- Provider/能力命令：`/model`、`/effort`、`/fast`、`/skill`、`/memory`、`/feedback`、`/agent`。
- 外部 Agent：`/claude`、`/codex`、`/cursor`、`/opencode`、`/pi`、`/default`、`/stop`。

`/provider` 会打开独立的分步配置向导，依次设置 Provider ID、API 协议、API 地址、API Key 和模型；Provider 名称直接使用第一步输入的 ID。模型既可从 Provider 的 `/models` 接口自动获取，也可手工输入；多个自定义模型使用英文逗号分隔，第一个模型自动成为默认模型。API Key 使用密码输入并加密保存到全局 `~/.agent/config.json`；编辑已有 Provider 时留空可保留原密钥。保存后新 Provider 会立即成为当前 Provider，并刷新 `/model` 候选。

输入 `/model` 即展示当前 Provider 的模型列表，↑/↓ 选择、Enter 直接切换，Tab 仅补全到输入框；模型超过 8 个时列表随选择滚动。模型列表和右下角统一显示 `provider/模型名称`：优先保留模型 ID 的来源前缀（如 `group`），无前缀时使用当前配置的 Provider，未配置时回退到 API 协议；实际请求仍使用原始模型 ID。`/model` 参数来自当前 Provider 配置；`/skill` 候选会从工作区与全局 Skill/alias 动态刷新。 `~/.agent/skills/<名称>` 和工作区 `.agent/skills/<名称>` 支持指向含 `SKILL.md` 目录的符号链接（包括绝对、相对和链式链接）；候选名称使用链接名，别名与描述读取目标文件，同名工作区 Skill 优先。失效、循环及非 Skill 目录链接会跳过；`read_file` 可通过原 `.agent/skills/<名称>/…` 路径读取 Skill 和引用文件，读取范围限制在目标目录内。`/clear` 保留旧 Session 文件，但清理当前显示、队列和 Session ID，下一条消息使用默认模型创建新 Session。`/exit` 会先取消并等待活动后台轮次退出。

`/skill` 描述支持 frontmatter 中的单行文本及 `description: |`、`description: >` 多行块（含常见的保留换行、去除末尾换行及缩进标记）。同时兼容 `---` 和被编辑器转义为 `\---` 的分隔符；多行描述会合并为单行展示，超长内容保留原有长度限制。

使用无任务的 `--resume <session-id>` 进入 TUI 时，只回放最近的用户和助手文本，总显示预算为 200 KiB；会提示省略条数，单条超限消息只在 TUI 中截断，不修改持久 Session。

## Herdr 集成

在 Herdr 窗格中启动 bqAgent 即可自动启用，无需安装插件或配置额外依赖。遵循 [Herdr 接入协议](https://herdr.dev/zh-cn/docs/add-herdr-support/)，仅当 `HERDR_ENV=1` 且 `HERDR_PANE_ID`、`HERDR_BIN_PATH`、`HERDR_SOCKET_PATH` 均存在时启用；其他终端不启动发送器、不执行 Herdr 命令。

- TUI：等待输入为 `idle`，处理请求为 `working`；TUI 的 Provider 配置等待用户选择时为 `blocked`。
- 前台单次任务：运行期间为 `working`。退出时释放窗格；HTTP/Bot 服务、后台任务和内部子智能体 worker 不接管窗格。
- 会话创建后、模型开始工作前更新恢复信息。恢复命令为 `bqagent --resume <session-id>`；模型和会话模式使用已有的持久会话配置。`/clear` 改为恢复一个新的空会话，不会重新打开旧会话。
- 使用单个后台发送器，仅保留最新待发送状态，重复状态不发送，也不逐 token 上报。每次状态上报限时 1 秒，为 CLI 启动和 IPC 留出时间，错误静默忽略；退出取消在途上报并等待释放完成，释放调用使用独立的 1 秒超时，整个退出额外等待上限为 1.2 秒，完成即返回。运行期间主流程不等待 Herdr 响应，不执行重试循环。

恢复要求 Herdr 0.9.2+，并且 `bqagent` 在 `PATH` 中；不能使用绝对路径作为恢复命令首项。恢复命令不带任务，进入交互 TUI，保留上下文并等待新的指令。需要验证时，可在 Herdr 窗格中运行 `herdr pane get "$HERDR_PANE_ID"` 或 `herdr agent list`。进程被强制终止而无法释放时，由 Herdr 自身清理机制兜底。上报仅含状态、会话 ID 和恢复参数，不含提示词、回复或工具输出。

## TTY 要求与兼容性

TUI 正常退出时，如果当前存在会话，会在终端恢复后打印 `bqagent --resume <会话ID>`，可直接复制执行以恢复聊天。不带任务的 `--resume` 自动进入交互聊天；带任务时仍为单次执行。尚未创建会话或 `/clear` 后尚未开始新会话时不打印恢复命令。

无任务启动时，stdin/stdout 不是 TTY 或 `TERM=dumb` 会报错退出，不再提供逐行模式；需要非交互使用时请传入任务描述，一次执行后退出。TUI 默认流式输出，无需额外命令行参数。设置 `NO_COLOR` 可关闭 TUI 与 Markdown 颜色。

交互模型参考并致谢 [CodeHamr TUI](https://github.com/codehamr/codehamr/tree/main/internal/tui)：本实现沿用其 Bubble Tea 同代技术栈和“原生 scrollback + 底部常驻输入区”的设计方向，并按 bqAgent 的 Provider、Session、工具事件、Skills 与外部 Agent 语义重新实现。

## Inline terminal TUI (English summary)

On a real TTY, startup without a task uses an inline, always-streaming Bubble Tea UI on the main screen. Completed-turn tool inspection temporarily uses the alternate screen: select a tool with the arrow keys and Enter, or click its row, to inspect its received arguments and output. Tool details support wheel/arrow scrolling, Page Up/Down, and Home/End. Esc returns to the list, and Ctrl+O restores the conversation. Upstream truncation remains explicitly marked. Mouse tracking is enabled only for a clickable collapsed tool group and disabled when the group is committed or cleared. `/provider` opens a sequential Provider setup wizard with encrypted API-key storage and either automatic model discovery or comma-separated custom models; the first custom model becomes the default. Startup without a task requires TTY stdin/stdout and `TERM` other than `dumb`; otherwise it exits with an error, and there is no line-mode fallback. Pass a task for a one-shot, non-interactive run. See the tables above for shortcuts, commands, queue behavior, persisted workspace history, resume limits, `NO_COLOR`, and the CodeHamr design acknowledgment.

The status bar shows full-precision `in / out` totals for the current user turn, including tool-call rounds. Input includes cached tokens; output includes reasoning counted by the provider. All three native API adapters publish usage immediately when received. Until then, estimates update with streamed text, reasoning, and tool arguments and remain marked `~`. Missing usage stays estimated; explicit zero is authoritative. Counts persist after completion and reset on the next turn. No extra token-count API requests are made.

Completed-turn TPS uses the final text response and keeps pauses in its generation duration. The TUI applies no fixed TPS ceiling. Inspired by pi-tps, the agent checks sample count, spacing, observable streaming duration, and pause-dominated bursts before publishing a rate; insufficient evidence displays `TPS —`. These client-side heuristics cannot recover hidden server-side timing.

Press `Ctrl+C` twice within two seconds to exit safely, including in Provider setup. The first press clears the prompt and shows a confirmation hint; the second cancels any active turn and waits for it to finish. Other keys, paste, mouse clicks, or timeout cancel the confirmation. `Ctrl+Q` remains a one-step safe exit shortcut.

Typing `@` at the start of a word opens a fuzzy workspace file picker (git-aware index, refreshed after 10 seconds); Enter or Tab inserts `@path`, and on send up to five mentioned regular workspace files (2 MiB each, 6 MiB total) are attached with inlined text content, exactly like WebUI workspace-path attachments. `Ctrl+V` / `Alt+V` attach a clipboard image (screenshot data or a copied image file) as an atomic `[图片 #N]` label and otherwise paste text; pasted or dragged PNG/JPEG/GIF paths are attached too. Limits match the WebUI: 4 images, 3 MiB each, 8 MiB total, 20 MP; oversized PNG/JPEG images are re-encoded as JPEG and downscaled locally. Clipboard images use `osascript` on macOS, PowerShell on Windows, and `wl-paste` or `xclip` on Linux.

Herdr support is automatic only when all four Herdr environment variables above are present and `HERDR_ENV=1`. Foreground TUI and one-shot tasks report activity and session recovery metadata; Provider setup reports `blocked`. A single background sender coalesces pending states, ignores failures, and allows each status report up to 1 second for CLI startup and IPC; shutdown cancels pending work and waits for release with a separate 1-second timeout and at most 1.2 seconds of extra waiting, returning as soon as release finishes. Reporting never blocks model work. No token-by-token reporting or retry loop is used. Background jobs, internal workers, and HTTP/Bot services do not claim a pane. Recovery requires Herdr 0.9.2+ and `bqagent` on `PATH`; it opens the saved session in chat mode, preserving persisted model/mode settings. Clearing the session updates recovery to a new empty chat. Prompts, replies, and tool output are never reported.

`/fast` 在开启、关闭之间循环切换，默认关闭；也支持 `/fast on`、`/fast off`、`/fast status`。设置按会话保存，恢复会话后继续生效，新会话默认关闭。开启后，OpenAI Chat Completions / Responses 请求携带 `service_tier=priority`，关闭后省略此参数。适用于支持该字段的 Codex、Cursor 等兼容 API；是否提供加速及其计费由上游决定，不支持的上游可能返回错误。Anthropic 协议暂不支持开启。此命令控制 bqAgent 自身 API 请求，不控制 `/codex`、`/cursor` 外部 Agent 进程。

## 长编程任务的连续执行

Run 模式与命令行任务提供内部工具 `task_update`，用于多步骤编程任务。模型应在修改前登记原始目标、验收项和剩余工作，并在取得进展后记录实际验证结果。状态保存在当前会话的 `task.json`，独立于对话压缩；恢复会话或发送“继续”“改完了吗”时会重新注入当前目标与未完成项。`task_update` 是唯一的持久化进度记录（`todo_write` 已移除）：验收项（criteria）、剩余工作（remaining）和状态（status）驱动 TUI 与 WebUI 的任务进度显示。界面只接收保存成功的完整任务快照，恢复会话时直接读取 `task.json`；验收项全部勾选但状态仍为 active 或仍有剩余验证时，不显示任务已完成。进度面板列出全部验收项，不再只显示前三项。已 completed 或 cancelled 的任务在新一轮开始时不再作为当前进度显示（快照为 null），`task.json` 仍然保留，供历史查看和范围变更判断。模型应在阶段变化、取得验收证据、被阻塞或范围变化时更新，不必在每次工具调用后重复计划。普通问答和小型单步修改不必创建任务，Ask 模式不暴露此写入工具。

Responses 接口会保留原始输出项、顺序及 assistant 的 `phase`，包括流式响应和历史回传。只有进度说明（`commentary`）时继续执行；输出截断按未完成处理，其他 incomplete 响应返回错误。任务进度仅用于展示与恢复，不作为最终回复或本轮执行结果的门槛。标记 completed 仍要求验收项均有完成证据、无剩余工作，并有通过的验证记录；每条验证命令必须与本轮真实成功执行的 `execute_bash` 命令匹配，可额外用 `tool_call_id` 绑定调用。修改或可能产生副作用的命令会使更早的验证失效，重叠执行的修改也不能使用之前的验证；`|| true`、未设置 `set -o pipefail` 的 `| tail` / `| head` / `| grep` 管道等隐藏失败退出状态的命令不能作为通过证据。验证被拒绝时，错误会说明具体原因（命令与实际执行不一致、管道隐藏退出状态、执行失败、只读命令，或被哪次修改使之失效），便于模型直接修正而不是反复重试。引号内的测试正则（如 `-run '^(TestA|TestB)$'`）按字面参数处理，不会误判为管道。完成后再修改且未重验，会将进度记录重新打开为 active，但不会因此阻止最终回复或把本轮判为 blocked。此检查证明执行与时序，不证明测试覆盖了全部需求。

缺少任务记录、进度读取或保存失败、验证证据无法关联时，实际工作和最终回复继续进行，不强制补登记或重跑已成功的验证。模型应如实说明完成内容、真实验证结果和进度记录限制；blocked / awaiting_input 仅用于实际工作遇到的具体阻碍或确需用户输入，不能用来表示进度工具故障。恢复 blocked / awaiting_input 任务时应根据当前请求重评，条件满足则继续工作，保存的旧状态不会自动决定本轮执行结果。当前轮成功调用 `task_update` 明确登记 blocked / awaiting_input（包括确认同一个阻碍仍存在）时，会声明本轮对应结果；恢复 active 或登记 completed 可清除通过进度登记的阻碍。回合结束时若任务未完成，且最近一次 `task_update` 被拒绝或最后一次保存之后工作区又有修改，会以 `stale` 阶段重新发送已保存的快照，TUI 在状态后标注“（记录未更新）”，提示该进度落后于本轮实际工作，以回复为准；下一次保存成功后标注消失。

内部工具 `turn_outcome` 独立声明当前请求的 blocked / awaiting_input / completed，无需创建任务，不依赖进度存储，也不会随 `task_update` 暂停而消失。阻塞和等待输入必须提供具体原因，声明后仍需给出最终说明；问题解决后可声明 completed。显式声明优先于进度登记，在收到新的用户输入时清除；运行错误、取消、预算限制优先于模型声明。当前请求受阻或等待输入时，后续任务继续留在队列中。

自动续行不限制累计次数；只要有新的工具进展，就在同一轮对话中继续实现、验证和收尾。工具名、规范化参数及返回内容构成进展指纹，重复得到相同结果不再持续重置停滞计数；新的成功文件修改允许重新执行相同测试，反复相同修改和无变化的工具循环仍受保护。成功但没有输出的调用首次出现时也可算进展。连续 2 次续行仍无新进展时，后续回复会触发恢复指令，要求检查失败输出和剩余工作、改变方法并执行下一步。失败调用、空搜索和仅更新任务状态不重置恢复计数。相同任务状态（忽略时间戳）不会重复写盘或刷新界面；没有新的实质工作时，连续第 3 次 `task_update` 触发恢复提示，并暂时从可用工具中移除该工具，取得新的实质进展后恢复。进度工具失败或空转不触发中止实际工作的检查点，实际任务完成时可直接给出最终回复。既有轮次、阶段时间和取消限制仍然有效。

修改原始目标或删除验收项需要 `replace_goal`、原因和 `user_quote`，引用必须来自当前最新用户消息；替换后旧验证被清除，不能在同一次更新里直接宣布完成。引用校验用于追溯用户原话，不等于自动理解授权。当前输入之后已发生修改时，不能用 `task_scope(side_question)` 脱离主任务；下一条用户输入仍可独立提出旁支问题。外部 `/codex`、`/cursor` 进程仍由各自运行时管理。

输入 `/effort` 展示努力程度列表，↑/↓ 选择、Enter 直接确认，Tab 仅补全。`/effort auto|low|medium|high|max` 设置后续请求的档位。与 WebUI 复用努力程度解析及模型请求链路，`auto` 不发送显式 effort；`max` 请求最高档，实际支持取决于模型和上游。不支持参数时沿用已有兼容回退。设置保留在当前 TUI 进程中，切换或新建会话后继续使用，退出重启恢复 auto；不修改 `/codex`、`/cursor` 外部 CLI/ACP 的配置。

输入 `/` 可浏览完整命令列表，`/effort` 在首屏可见。命令和参数候选每屏显示 8 项，↑/↓ 滚动并循环选择，标题显示当前位置和总数；Skill/alias 和 `/agent` 子命令也保留全部候选。
