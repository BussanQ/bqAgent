# Orca / bqagent ACP 接入补丁

`bqagent.patch` 面向 [stablyai/orca](https://github.com/stablyai/orca) 的固定基线 **`48e78a6d5df89f28956892486d6ef164617366c2`**。补丁仅修改 Orca 源码，不包含 bqAgent 服务端实现，也不是已发布 Orca 的运行时插件。

## 应用

在独立 Orca checkout 中应用，不要在 bqAgent 仓库根目录运行 `git apply`。以下示例使用 detached HEAD，不创建修复分支。

```sh
git clone --no-checkout https://github.com/stablyai/orca.git /path/to/orca
cd /path/to/orca
git checkout --detach 48e78a6d5df89f28956892486d6ef164617366c2
git rev-parse HEAD
git apply --check /path/to/bqAgent/plugins/orca/bqagent.patch
git apply /path/to/bqAgent/plugins/orca/bqagent.patch
```

已有 checkout 应先确认工作区干净、HEAD 为上述完整 commit；其他版本不保证补丁上下文及接口兼容。撤销本补丁可先运行 `git apply --reverse --check`，确认后再运行 `git apply --reverse`，参数均为同一补丁绝对路径。

## 构建和使用

下列步骤供应用补丁后执行，本次交付没有安装依赖、执行上游脚本或构建 Orca。

固定基线要求 Node.js 24、pnpm 12.14.0、Bun 1.4.4，以及 Python 3 和平台 C++ 工具链。macOS 需要 Xcode Command Line Tools；其他平台依赖参考该基线的 `.github/CONTRIBUTING.md` 和 `docs/development.md`。`pnpm install` 会执行上游的原生模块构建脚本。

```sh
# 在 Orca checkout 根目录
pnpm install --frozen-lockfile
pnpm -C mobile install --frozen-lockfile

# 开发运行
pnpm dev

# 若需桌面发行构建，按平台选择一个
pnpm build:mac
# pnpm build:linux
# pnpm build:win
```

在运行 Chat 的执行主机上安装包含 ACP 实现的 bqagent，并确保 Orca 的启动环境能通过 PATH 找到它；也可使用 Orca 的 Agent Command 设置指定可执行文件绝对路径。该命令应指向 `bqagent` 本身，不附加 `--acp`，因为 Chat 启动器会自动添加。终端入口运行无参数 `bqagent` TUI。

重新启动使用补丁构建的 Orca 后，在 Agent 设置和 Chat agent 选择中选择 `bqagent`。原生注册表从 `ACP_LAUNCH_SPECS` 自动生成，renderer 的 `isTuiAgent` 过滤也已覆盖它。远端执行主机同样需要更新 Orca host 代码并安装 bqagent；补丁沿用现有 ACP 执行位置限制，不声称额外开放新的远程传输。

配置继续使用执行主机的 `~/.agent`。默认环境沿用 Orca 的执行主机环境及用户 Agent 环境配置，不创建 `BQAGENT_HOME`。为使恢复仍指向同一持久化存储，账户绑定遵循 bqagent 的解析顺序：优先使用 `HOME`，缺失时使用 `USERPROFILE`，再回退到执行主机的用户根目录。解析结果固定为会话的 `HOME`，启动/恢复时重放，其他环境变量保留。Windows 同时设置两个变量时也优先使用 `HOME`，避免恢复时切换配置目录。这里绑定的是用户根目录，不是将 HOME 改成 `~/.agent`。

## 行为及权限语义

- ACP Chat 运行 `bqagent --acp`，使用标准 ACP dialect；新建、恢复、文本流、工具卡片、停止和模型选择复用 Orca 通用实现。
- `session/new` 的原生持久化 ID 直接保存为 ACP handle，恢复使用 `session/load`。CLI 恢复命令为 `bqagent --resume <id>`。
- 新增通用 ACP `ownApprovalMode` 能力。bqagent 标记 `approvalEnforcement: provider`，会话报告 `reportedPermissionMode: own`，不提供 Orca 的 ask/auto/full-access 切换，也不设置虚构的权限 config option。显式要求非 own 模式会在进程启动前被拒绝。
- bqagent 自己自动执行工具，Orca 不提供手动审批或强制权限控制。如果实现意外发送 `session/request_permission`，Orca 回复 `cancelled`，不弹手动审批卡片，也不自动批准该请求。这不限制 bqagent 本来就自行执行的工具。
- `imagePrompts` 在静态注册时即关闭，不等待 initialize 后再隐藏。

## 协议约定

服务端应使用标准 ACP JSON-RPC stdio 帧，stdout 只输出协议消息，日志写入 stderr。

1. `initialize` 返回 `protocolVersion: 1`、`agentCapabilities.loadSession: true`。首版关闭图片、音频、embedded context，不宣称支持 fork、session list/delete 或 MCP 接入。
2. `session/new` 接收绝对 `cwd` 和 `mcpServers: []`，返回非空、可持久恢复的 `sessionId`。`session/load` 接收同一 ID 和 cwd，先发送历史 `session/update`，再返回会话状态。Orca 会抑制 load 期间的回放，避免重复写入已有 Chat journal。
3. new/load 返回 `models: { currentModelId, availableModels: [{ modelId, name }] }`。推荐同时返回标准 `configOptions` 模型 select：`id: model`、`category: model`、`type: select`、`currentValue`、`options: [{ value, name }]`。
4. 存在模型 config option 时 Orca 优先调用 `session/set_config_option`；响应须返回更新后的完整 `configOptions`。仅提供 models 时使用 `session/set_model`，参数为 `sessionId` 和 `modelId`。不需要提供权限 `modes` 或 mode config option。
5. `session/prompt` 接收文本 content block；回复通过标准 `session/update` 的文本和工具调用消息流式发送，最终返回 `stopReason`。应容忍 Orca 附带的可选 `_meta` 字段。
6. `session/cancel` 是通知；停止当前工作后，应结束仍未完成的 prompt 请求并返回 `stopReason: cancelled`，不要为 cancel 通知发送 JSON-RPC response。

## 限制

- 无手动权限、fork、compact、图片、原生会话 list/delete、原生 transcript 导入/扫描或全文搜索支持。补齐 History 类型与恢复命令是为了正确识别原生 ID，不代表会扫描 `~/.agent` 中的所有旧会话。
- 新聊天启动前不进行模型发现：`modelDiscovery.kind` 为 `unavailable`。模型菜单从真实聊天 new/load 返回的 models/configOptions 更新。这样不会为了探测模型创建无法删除的持久化会话。
- `keepsEverySession: true` 禁用 Orca 的自动命名辅助会话，避免持久化无用会话。删除 Orca 本地 Chat 记录不等于删除 bqagent 原生会话。
- 未接入终端 hooks、标题状态专用识别或 skills CLI 安装目标；TUI 使用通用启动/输入行为。未进行实际 TUI、跨平台或远端运行验证。
- 已有 Orca Chat 的恢复能够继续原生会话；在外部 CLI 追加的历史目前不会自动补写到 Orca journal，因为未配置带稳定 turn identity 的 history catch-up。
- 未声明最低 bqagent 版本。使用不支持 `--acp` 的旧二进制会启动失败，应先更新 bqagent。

## 验证记录

补丁包含 22 个 Orca 文件的修改。已完成以下离线校验：

- 从固定 commit 的原始源码归档提取所有被修改文件，新增文件在基线中不存在。
- `git apply --check --whitespace=error-all` 通过。
- 在该干净文件集合上实际应用补丁通过，随后 `git apply --reverse --check` 通过。
- 应用后 22 个文件的 Git 内容哈希均与最终补丁中记录的哈希匹配。

源码归档来自 `https://codeload.github.com/stablyai/orca/tar.gz/48e78a6d5df89f28956892486d6ef164617366c2`，本次下载 SHA-256 为 `6b9b33fef2b50db56049f8e23177a4d9623273b2c9633a10b0f833ddee430ac2`。

新增/调整了 11 个具体单元测试用例，覆盖注册后的权限/能力、环境继承与 home 绑定、恢复参数、新建/load 回放、模型 config/set_model 路由、拒绝非 own 启动、意外权限请求取消和停止。**这些 Orca 单元测试尚未执行**：临时源码和 bqAgent 工作目录均无所需 Node 依赖；没有安装依赖来绕过限制，也没有运行整包、集成、e2e、联网测试或编译检查。

在已准备好 Orca 依赖和原生测试运行时的环境中，可仅运行本次具体用例。这里直接调用 Vitest，避免 `pnpm test` 包装器自动触发构建：

```sh
ORCA_BACKGROUND_LAUNCH=1 pnpm exec vitest run --config config/vitest.config.ts \
  src/main/bqagent/bqagent-acp-launch-spec.test.ts \
  src/main/acp/session/acp-structured-bqagent.test.ts \
  src/shared/ai-vault/resume/ai-vault-resume-command.test.ts \
  -t bqagent --maxWorkers=2
```

应用校验仅证明补丁可应用，不代表 TypeScript 类型检查、单元测试或实际 Orca/bqagent 联调已经通过。
