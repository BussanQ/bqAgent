# bqagent 原生 ChatUI 插件

[插件目录](./bqagent/) 包含 `orca-plugin.json`。它向 Orca 声明一个本地 ACP Agent，启动命令为 `bqagent --acp`。启用后，可在原生 ChatUI 发送消息、查看流式回复和工具卡片、停止回合、切换模型或恢复会话。bqagent 按自己的策略自动执行工具。

**宿主要求**：Orca 官方格式 v2 把 `contributes.agentHarnesses` 保留为空数组。需要先使用支持这项声明的 Orca 构建。仓库提供针对 `stablyai/orca@48e78a6d5df89f28956892486d6ef164617366c2` 的[宿主兼容补丁](./host/agent-harness.patch)。补丁中的适配器支持 bqagent，插件清单及 Orca 记录的授权决定它能否用于 ChatUI。

## 安装插件

1. 在执行聊天的主机上安装支持 `--acp` 的 `bqagent`，并将它加入 PATH。启动带宿主兼容补丁的 Orca。
2. 使用该构建对应的 Orca CLI，安装插件目录。路径指向包含 `orca-plugin.json` 的 `bqagent` 子目录。

   ```sh
   orca plugin install /path/to/bqAgent/plugins/orca/bqagent
   ```

3. 首次安装时核对权限 `agent:launch`，用 Orca 本次返回的指纹完成授权。

   ```sh
   orca plugin install /path/to/bqAgent/plugins/orca/bqagent --approve <fingerprint>
   ```

4. 在 **Settings → Plugins** 确认 `local.bqagent` 已启用。完整退出并重新打开 Orca 以刷新 Agent 选择器，在 **Settings → Agents → bqagent** 选择 **Chat**，然后新建聊天。Chat 启动器固定从执行主机的 PATH 解析 `bqagent`，并添加 `--acp` 参数。

本地开发时也可在 **Settings → Plugins → Development folders…** 添加并批准同一目录。安装时的 `agent:launch` 授权允许 Orca 启动 Agent。聊天过程中的工具按 bqagent 的策略自动执行。

## 构建支持插件的 Orca

从固定版本的干净 Orca 源码开始。在使用过旧版直接注册补丁的工作区，可以另用一个干净 checkout，避免两个补丁重复修改同一文件。

```sh
git clone --no-checkout https://github.com/stablyai/orca.git /path/to/orca
cd /path/to/orca
git checkout --detach 48e78a6d5df89f28956892486d6ef164617366c2
git apply --check /path/to/bqAgent/plugins/orca/host/agent-harness.patch
git apply /path/to/bqAgent/plugins/orca/host/agent-harness.patch
pnpm install --frozen-lockfile
pnpm -C mobile install --frozen-lockfile
pnpm dev
```

固定基线的构建依赖见 Orca 的 `.github/CONTRIBUTING.md` 与 `docs/development.md`。桌面发行构建可按平台运行 `pnpm build:mac`、`pnpm build:linux` 或 `pnpm build:win`。

## 会话与权限

- 插件是声明式包，申请一项 `agent:launch` 权限。宿主校验贡献中指定的 `bqagent` 和 `--acp`，并根据当前插件授权决定是否允许新启动。
- 会话持久化在执行主机的 bqagent 存储中，沿用 `~/.agent` 配置。`session/load` 和 `bqagent --resume <id>` 使用同一原生 ID。外部 CLI 新增的记录不会自动补写到已存在的 Orca 聊天记录。
- 模型列表来自活跃会话的 `models` 和 `configOptions`。首版支持文本、工具结果和取消，能力边界见 [ACP 文档](../../docs/ACP.md)。
