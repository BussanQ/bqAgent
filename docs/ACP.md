# ACP 与 Orca 接入

bqagent 可以作为 ACP（Agent Client Protocol）服务端，由 Orca 等客户端启动并驱动原生聊天界面：

```sh
bqagent --acp
```

客户端通过 stdin/stdout 交换逐行 JSON-RPC 2.0 消息，不需要 HTTP 服务或监听端口。该入口不要求 TTY，不能与任务参数、`--server`、`--resume` 等选项组合。日志不写入协议 stdout。

## 会话与配置

`initialize` 只进行协议握手，不读取工作区、不启动模型。`session/new` 或 `session/load` 的 `cwd` 必须是已存在的绝对目录；bqagent 按正常 CLI 规则从该目录发现工作区，加载工作区 `.env` 和已有 Provider/Magpie 配置。

不同 ACP 会话可以使用不同工作区，进程不会为某个会话改变当前工作目录或全局环境变量。默认全局配置仍在 `~/.agent`。新会话保存 `created_via: "acp"`，保留原生 bqagent session ID。

加载会话使用 `session/load`，在返回加载结果前通过 `session/update` 回放用户消息、助手消息及工具活动。加载错误不会静默创建新会话，不能从另一个工作区加载会话。已有单聊 CLI/TUI 会话可以通过其原生 ID 加载；ACP 会话也可以用 `bqagent --resume <sessionID>` 从对应工作区继续。

每个会话同一时间只接受一个执行/加载/模型变更操作。正在运行的回合通过 `session/cancel` 通知取消，原 `session/prompt` 请求返回 `stopReason: "cancelled"`；取消后仍可继续同一会话。

## 支持的协议

协议版本为 1，支持以下方法：

| 方法 | 行为 |
| --- | --- |
| `initialize` | 返回 bqagent 标识、协议版本和支持的能力 |
| `session/new` | 创建持久化会话，返回会话 ID、模型列表及模型配置选项 |
| `session/load` | 加载并回放已有单聊会话 |
| `session/prompt` | 执行文本输入，推送文本与工具更新，并返回停止原因 |
| `session/cancel` | 取消当前回合；这是 notification，没有独立响应 |
| `session/set_model` | 按配置的真实模型 ID 选择模型，并持久化到会话 |
| `session/set_config_option` | 支持 `configId: "model"` 的模型选项 |

文本输出使用 `agent_message_chunk`；工具执行使用 `tool_call` 与 `tool_call_update`，包含调用参数、结果和状态。完整消息快照转换为增量文本，避免客户端重复追加。ACP 没有消息撤回操作；上游重试或修订已发送的内容时，会显示简短说明并开启新的消息段。

工具由 bqagent 自动执行，不发送 `session/request_permission`，不支持 Orca 手动审批模式。Orca 补丁会将其标记为 Agent 自主管理权限的模式。

当前仅接受文本 prompt。图片、音频、嵌入资源、由客户端传入的 MCP server、ACP 会话列表/删除/分叉和结构化人工问答尚未支持；这些能力不会在握手中声明。bqagent 自己配置的 MCP 工具仍由原运行时管理。执行预算耗尽返回 `max_turn_requests`；受阻或等待输入的回合会显示原生说明，不能将 `end_turn` 理解为整个任务已经完成。

## Orca 接入

在支持 Agent 插件的 Orca 构建中，安装并批准本仓库的 `plugins/orca/bqagent` 插件，重启 Orca 后，在 **Settings → Agents → bqagent** 选择 **Chat**。宿主通过 ACP 启动本地 `bqagent --acp`，使用原生 ChatUI 显示文本和工具卡片。插件安装授权与 bqagent 的工具执行策略分别生效。

Orca 官方格式 v2 仍将 `agentHarnesses` 保留为空数组。本仓库为固定 Orca 版本提供兼容补丁和本地插件安装命令。参见 [Orca 插件安装说明](../plugins/orca/README.md)。

模型列表来自活跃 ACP 会话；不为后台模型探测创建无法删除的临时会话。Orca 对已有 CLI 历史的自动扫描、Chat/Terminal 对话复制和分叉是独立功能，不等同于按已知 session ID 恢复，本次不提供这些能力。
