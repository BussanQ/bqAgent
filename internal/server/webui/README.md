# WebUI architecture / WebUI 架构

`internal/server/webui` is a dependency-free-at-runtime, vanilla TypeScript + Vite application. The production path is:

```text
index.html + src/*.ts + src/styles.css + public/favicon.*
                    │ npm run build
                    ▼
          dist/index.html + dist/assets/<hash>.*
                    │ go:embed webui/dist
                    ▼
              one Go executable
```

`src/main.ts` wires the DOM and feature modules. Transport/SSE parsing, Markdown safety, workspace session handling, Provider and conversation state, attachments, theme, and particles remain browser-side code.

Production serves `/` with `Cache-Control: no-store`, hashed `/assets/*` with a one-year immutable cache, and favicons with a one-day cache. Unknown files, path traversal, and methods other than GET/HEAD are rejected. The executable reads only the embedded filesystem at runtime.

Commands:

```bash
npm ci
npm run dev
npm run build
npx vitest run src/stream.test.ts src/markdown.test.ts src/workspace.test.ts src/model-selection.test.ts src/attachments.test.ts src/tool-groups.test.ts
```

The development server proxies `/api` to `http://127.0.0.1:8080`. `dist` and `node_modules` are generated locally and ignored by Git.

---

`internal/server/webui` 是原生 TypeScript + Vite 工程，运行时没有前端依赖。生产链路为“源码 → Vite 哈希产物 → `go:embed webui/dist` → 单一 Go 可执行文件”。

`src/main.ts` 负责连接 DOM 与各功能模块；传输/SSE、Markdown 安全、工作区会话、Provider、历史会话、附件、主题和粒子效果仍在浏览器端运行。

生产环境中，根页面使用 `no-store`，哈希资源使用一年 immutable 缓存，favicon 使用一天缓存；未知路径、路径穿越以及非 GET/HEAD 请求均会被拒绝。运行时只读取二进制内的嵌入文件系统，不需要 Node.js、Vite、CDN 或磁盘上的 `dist`。


### Slash commands / 斜杠命令

单聊的「对话 Agent」下拉框复用当前工作区的可用成员列表。向 `POST /api/v1/webui/chat` 发送独立的 `agent` 字段（如 `pi`、`codex` 或 `bqagent`）。省略时沿用会话绑定。SSE 的 `done.reply_agent` 标识实际回复的外部 Agent，`done.active_agent` 与历史接口的 `active_agent` 用来恢复选择。外部 Agent 的文本增量走 `message`，工具更新走 `tool_start` 和 `tool_result`。WebUI 按原生调用 ID 更新卡片，并从历史回复的 `tools` 字段恢复卡片。ACP 和 Codex App Server 的权限请求继续使用 `acp_permission`。

Type `/` in the composer to discover commands; use ↑/↓ to select, Enter/Tab or a click to complete, and Escape to dismiss. Completion does not send a message. Skills include descriptions and aliases, refreshed when the menu opens using the selected workspace. Commands use the existing chat endpoint and shared backend execution.

模型选择器通过 `GET /api/v1/webui/agent-models` 按工作区、会话与 Agent 读取原生目录。聊天请求用 `agent_model` 和 `agent_effort` 提交显式选择，完成事件用 `reply_agent_model` 和 `reply_agent_effort` 返回已保存的设置。支持图片的接口可接收单聊图片附件。历史消息中的 `images` 提供图片引用，WebUI 从当前工作区与会话按需读取。

Codex App Server 的模型可以公布 Fast 等服务档位。单聊中选择档位后，下一条消息通过 `agent_service_tier` 将原生档位 ID 传给当前线程的 `turn/start`。选择「标准速度」会显式传入 `default`。如果切换到不提供所选档位的模型，当前消息使用标准速度，切回支持该档位的模型会恢复原选择。队列保存每条消息的实际档位，队列自动沿用的速度不会覆盖聊天中后来明确选择的档位。

下拉框和显式 `/codex`、`/pi` 路由共用同一个模型选择器。预览和发送时均使用实际回答者的目录。服务端为新聊天分配会话 ID 后，前端保留各 Agent 尚未发送的模型选择，并在回合结束后重新读取该会话的选项。

已有会话可用同一模型接口的 `view=capabilities` 读取绑定的传输方式所支持的功能与上次选择的模型。读取完整目录期间，支持自定义模型的 Agent 已可输入模型 ID。生成期间修改的模型和推理强度会随下一条普通消息发送，当前回合结束或重连时会保留这次预选。

切换 Agent 后，模型下拉框仍在输入框旁。点「刷新」可重读该 Agent 的模型目录和会话设置。切回同一 Agent 时，如果目录已超过五分钟，WebUI 会在后台刷新。刷新期间和读取失败后仍可从当前会话上次取得的目录选择模型。来回选择 Agent 会复用各自正在加载的目录，切换会话或工作区会取消旧请求。生成期间改选模型或推理强度后按 Enter，会把普通消息和所选设置一起排入新回合。

打开新聊天或恢复历史时，Codex App Server、Pi 和 Claude CLI 可以先显示同一工作区、同一启动配置最近五分钟读到的模型列表，再用实时结果更新。每个会话独立恢复已选模型和推理强度。ACP 的可选模型随原生会话变化，WebUI 从该会话读取。

在单聊里点选模型，WebUI 会把它保存为该工作区、该 Agent 的新会话默认模型。推理强度按模型分别记忆。在已有聊天里只修改推理强度会保留新会话的默认模型。下一次打开同一工作区时，选择器用 Agent 返回的目录核对模型和强度。手工输入的模型 ID 仅在支持自定义模型的接口上使用。「沿用会话模型」可以清除新会话的模型偏好。

服务端通过 `POST /api/v1/webui/agent-models` 保存单次选择，聊天消息仍通过原来的发送接口执行。浏览器在当前标签页按聊天和上下文保留尚未发送的回答者、模型和强度。刷新或离开聊天后返回可以继续使用，首条消息创建会话后草稿会随会话迁移。

Run 单聊的输入框旁会显示当前 Agent 的上下文占用率。点击百分比可以看已用 token 数和窗口大小。Codex App Server 使用 `thread/tokenUsage/updated` 的最近一次请求数据。Pi RPC 展示当前模型的输入及缓存估算，再用 `get_session_stats` 的会话读数更新。ACP 使用 `usage_update`。支持双向 stream-json 的 Claude CLI 会在主回复结束后响应 `get_context_usage` 摘要查询；有分类或自动压缩阈值时会一并展示。没有摘要时，WebUI 可以用主模型返回的输入用量和窗口给出估算。

用量通过 `external_context_usage` 事件更新，并按工作区、聊天上下文、Agent 和原生会话保存。切换回答者或刷新页面可恢复对应读数。压缩和模型切换会使旧读数失效；拿到新读数后才重新显示。没有报告容量的 Agent 不显示百分比。

Codex App Server 的分页线程 支持「回退到这里」。点选一条用户消息后，WebUI 会回到它发送前的对话，把原文、图片和当时的文件内容放回输入框。工作区文件保留现状。目标必须位于最近一次 `/clear` 之后，待移除的消息须能在同一 Agent 的原生历史中核对，操作时会话和队列须空闲。Pi 当前没有回退入口。

WebUI 用 `POST /api/v1/webui/rewind` 提交消息标识和预期上下文版本。Codex 通过 `thread/revert` 核对并回退原线程。重新打开聊天后可点「取回回退草稿」。

Run 单聊的「从这里创建分支」位于已完成的助手回复下方。Codex App Server 用 `thread/fork`，保留到选中回复所属回合。声明支持 `session/fork` 和恢复会话的 OpenCode ACP 可以从最新完成的回复创建分支。分支继承该位置的历史、附件和会话设置，可以独立继续。「返回来源回复」会定位原聊天中的对应消息。来源聊天删除后，分支仍可用。缺少原生回合或消息标识的旧回复不会显示分支入口。

Run 单聊在 `POST /api/v1/webui/chat` 处理 `/clear`，接受可选的 Agent 前缀。请求使用 `turn_id` 作为清空操作的幂等键。WebUI 使用 `Accept: application/json` 获取回执，流式客户端收到终止事件。历史中的 `kind: "context_clear"` 标记旧消息与新上下文的边界，历史图片引用保留所属上下文。其他标签页收到清空通知后更新历史，其他设备上打开的空闲会话通过 `turns?view=context` 检查边界变化。

命令菜单按工作区、会话和 Agent 分别请求 `GET /api/v1/webui/commands`。有 `agent` 参数时返回原生命令，`insert` 是带回答者前缀的完整补全文本。选中 Agent 后输入 `/` 可同时浏览内置和原生条目，输入 `/pi /` 等显式路由时只列目标 Agent 的条目。Pi RPC 使用 `get_commands`，Claude 使用 stream-json 的 `initialize` 控制响应，ACP 使用 `available_commands_update` 的整份命令清单。Codex 用 `skills/list` 查询工作区，发送时根据当前子进程的目录为前导 `$名称` 添加原生 Skill 输入块。

Codex App Server、Pi RPC 和 Claude CLI 的模型目录用 `supports_compact` 表示手动压缩能力。WebUI 通过普通聊天请求发送 `/compact`，SSE 的 `external_command` 事件标识正在处理命令的 Agent，`done.reply_agent_command` 让模型选择器保留下次发消息要用的选择。Codex 恢复原线程后发送 `thread/compact/start` 并等待压缩标记和回合结束。Pi 恢复原会话后调用 `compact`，附加说明放在 `customInstructions`。Claude 使用 `--resume` 和 stream-json，按会话 ID 核对手动 `compact_boundary` 与终止结果。ACP 的同名命令交给原适配器。

生成期间的普通文本和原生 `/compact` 共用 `/api/v1/webui/command-queue`。POST 的 `enqueue` 使用父回合 ID 和稳定的操作 ID；普通文本使用 `kind: "message"`，可附带 `agent_model` 和 `agent_effort`。GET 返回当前会话快照，可按操作 ID 查询回执；DELETE 撤回仍在等待的条目；POST 的 `resume` 恢复暂停的队列。服务端持久化接收时的目标和模型选择，派发前记录子回合 ID。SSE 的 `command_queue` 提供带修订号的队列快照，终止事件的 `next_turn_id` 用于接续子回合。停止、失败或重启后保留未发送内容；已派发但未确认的内容不会自动重发。

单卡「立即发送」使用队列接口的 `send_now`，携带条目 ID、`body_version`、`expected_turn_id` 和 `operation_id`。回执的 `submission_id` 标识这一次交付；重复点击和响应丢失后的重试可以核对同一回执。服务端按原模型和原生线程决定是否向活动回合发送纠偏。需要新回合的提交按点击顺序优先发送。消费确认后，`queued_input_applied` 会在当前回复中显示原文和附件。收到确定拒绝时保留卡片供手动重发，结果未知的提交不会自动重试。已退回和未确认的交付通过 `queue_delivery` 事件和同名历史消息展示，从队列移除后仍能查看原文和附件。

等待条目的 `body_version` 从 0 递增。编辑器通过 `hold`、`renew`、`release` 暂停该条目的自动发送，使用标签页生成的 `edit_id` 标识本次编辑。服务端在两分钟后释放未续期的编辑；如果队首因此恢复可发送状态，服务端会自行接续。保存使用 `edit` 并携带打开时的 `body_version`；撤回使用同一版本作为 DELETE 查询参数。`move` 使用 `anchor_id` 和 `before` 或 `after` 指定相邻等待条目。保存和移动均使用稳定的 `operation_id`，丢失响应后可按原参数核对回执。编辑器的本地草稿按工作区和会话保存在标签页中。

消息入队时可以提交与普通聊天相同的 `images` 和 `files`。服务端校验原生图片能力、文件范围、类型和容量，并将工作区路径解析为当时的文件快照。私有附件文件保存在会话目录，队列 SSE 只包含元数据。GET 的 `view=attachment&index=N` 返回该条目的单个附件，`view=attachments` 用于取回整组附件。编辑请求的 `keep_attachments` 指定保留的旧索引，`images` 和 `files` 指定新增内容；省略 `keep_attachments` 时只改文字。保存响应丢失后，可用 `id` 和 `operation_id` 查询 `operation_committed`。撤回的附件保留到取回或显式移除，通过 `release_attachments` 确认释放。客户端用 IndexedDB 暂存尚未确认的上传，使刷新后的重试沿用同一操作与附件。

Codex App Server 在 `turn/start` 返回后发送 `message_snapshot` 的 `input.available` 事件。WebUI 此时开放文本纠偏，复用 `POST /api/v1/chat/input`。服务端通过 `turn/steer` 指定原生 thread 和预期 turn，核对确认的 turn 后发出 `input.applied`。工具跨过纠偏消息时，后续更新会落在最初的卡片上。

Pi 单聊使用 RPC 的 `prompt.streamingBehavior` 发送纠偏和后续任务。WebUI 分别从 `input.available` 事件读取能力，收到 Pi 的队列确认后显示「已排队」，收到原生用户消息后显示「已应用」。停止或结束事件的 `pending_inputs` 用于恢复草稿。Pi 扩展的 `select`、`confirm`、`input`、`editor` 由会话中的交互卡片处理。

Codex App Server 通过 `item/tool/requestUserInput` 向当前回合提问。单聊按原顺序展示每道题、选项和说明。各题可独立作答，支持自填的题目也可直接输入。服务端收齐同组回答后，按原生问题 ID 向 Codex 返回一次结果。「取消本组问题」会撤销其余待答卡片。Codex 发出 `serverRequest/resolved` 或回合结束时，旧卡片会失效。非阻塞问题允许继续向 Agent 发送输入。历史卡片保留题目、备选项和公开选择，文本及密码回答只通过原生请求回传。

Codex 请求授权 MCP 工具调用时，单聊会显示服务名、工具名和本次调用的参数预览。可选择「允许本次」或「拒绝本次调用」，Codex 明确提供会话授权时才显示「本会话允许此工具」。拒绝只针对这次工具调用。授权卡片在决定写回原生请求后记录结果。Codex 撤回 MCP、命令、文件或权限请求时，尚未写回的决定会失效。刷新后仍可查看 MCP 工具、预览与授权范围。普通 MCP 输入表单和 URL 请求返回协议的 `decline`。

浏览器用 `POST /api/v1/webui/chat` 启动回合。SSE 的 `id` 是该回合连续的事件序号。`GET /api/v1/webui/turns?turn_id=…&after=N` 从第 N 个事件之后继续订阅，支持多个独立读者。`session_id` 可查询某个会话的活跃回合，`view=history&turn_id=…` 返回生成开始前的历史视图。所有请求按工作区隔离。`compact=1` 允许浏览器将 `message_patch` 按同一消息的 `base_seq` 还原成完整快照。默认 SSE 仍返回完整的 `message_snapshot`。生成后的回放窗口为十分钟。

外部单聊用 `external_text` 在 `compact=1` 流里传送带 ID 的文字追加或修订，默认 SSE 继续提供文本增量。普通历史接口的 `parts` 列出文字、工具和交互请求的顺序，工具 ID 对应同条回复的 `tools`。前端保留每个项目的首次位置，历史交互卡片只展示已记录的结果。会话读取器会折叠增量记录，工作上下文保留原生 Agent 的最终回复。

成功的文件编辑会在工具卡片上显示可展开的逐行差异，回合末尾按文件汇总所有已确认的编辑。同一路径多次修改按调用顺序累加，重命名会合并旧路径上的编辑。计数反映该回合展示的编辑操作。来自片段或截断预览的计数会标为部分计数。整文件写入缺少原文时只展示写入内容。

差异来自 Codex 的 `fileChange`、Claude 和 Pi 的编辑输入及原生结果、ACP 的 `diff` 内容块，以及专用 `apply_patch` 工具或独立执行的补丁命令。只有能够定位到文件里的行才显示行号。卡片可复制所示内容，刷新后仍读取聊天当时记录的差异。未记录差异的旧消息沿用原工具预览。

`external_plan` 用稳定的项目 ID 发送步骤清单或文字方案。`plan.mode=steps` 替换所有步骤，空清单会收起没有说明文字的卡片。`plan.mode=proposal` 根据 `append` 追加或替换 Markdown。方案是最终答复时，`done.reply_item_id` 指向已显示的卡片，普通历史保留对应的 `plan` 部分。

Codex 公布推理摘要时，单聊会在工具和回答之间显示可展开的「推理摘要」。`external_summary` 按项目 ID 和原生段落序号发送更新，`ended` 表示摘要流已结束。刷新聊天后，历史中的 `summary` 部分会在首次出现的位置恢复，后续追问也保留该位置。App Server 读取 `item/reasoning/summaryTextDelta` 和推理条目公开的 `summary` 数组。CLI 回退路径读取 `codex exec --json` 的 `reasoning` 条目中的 `text` 字段。

输入 `/` 展示命令，↑/↓ 选择，Enter/Tab 或点击补全，Esc 关闭；补全后可继续填写任务并发送。`/skill` 展示当前工作区与全局 Skill 的名称、描述及别名，每次重新打开候选时刷新。`/fast`、`/memory`、`/agent` 和外部 Agent 切换沿用已有聊天执行逻辑。模型、努力程度、Run/Ask、新会话、Provider 设置、停止和反馈已有界面入口，不重复展示；终端专用帮助与退出命令也不展示。群聊另外隐藏 `/agent`、外部 Agent 切换及 `/default`，通过群聊成员和 @ 交互。

The authenticated `GET /api/v1/webui/commands?workspace_id=…&conversation_type=default|group` endpoint supplies discovery metadata from the command catalog shared with TUI. Skill files are read through the existing workspace loader. Regression checks: `npx vitest run src/slash-commands.test.ts` and `go test ./internal/server -run '^TestWebUICommandsFiltersButtonsAndRefreshesWorkspaceSkills$'` from the repository root for the Go test.
