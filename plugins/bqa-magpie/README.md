# bqAgent × Magpie

根据 [Magpie Agent 插件文档](https://usemagpie.ai/docs/zh/plugins#agents) 实现。插件在 Magpie 的 Agent 页面添加 bqAgent，将选中的模型、网关地址和专属 Agent key 写入 `~/.agent/magpie.json`。

## 安装

先使用包含本目录配套后台改动的 bqagent 版本，并确保 `bqagent` 在 PATH 中。在仓库根目录执行：

```sh
magpie plugin add ./plugins/bqa-magpie
```

也可以在 Magpie 的插件页面添加本目录的绝对路径。入口由 `package.json` 中的 `magpie.agent` 指定；不要直接添加名为 `agent.js` 的单文件（单文件识别要求 `*.agent.js`）。

随后在 Magpie 的 Agent 页面找到 **bqAgent**，开启接入并选择模型，再重新启动 bqagent。修改模型或关闭接入后，也需要重启正在运行的 TUI、Chat 或 WebUI 服务。

## 配置行为

- 使用 Magpie 提供的 `gateway.v1` 和专属 `gateway.key`，通过 OpenAI Chat Completions 接入网关。
- 模型 ID 保持 Magpie 的 `供应商/模型` 形式，不添加 `openai/` 前缀。可用模型及显示名称同时写入配置。
- 有效的 `magpie.json` 优先于原有全局 Provider 和环境变量；原有 `~/.agent/config.json` 及加密密钥不被修改。插件配置与后台读取逻辑需要一起使用，旧版 bqagent 不会读取此文件。
- WebUI 的 Provider 设置会显示 Magpie 的网关地址、模型目录和显示名称，并标明由 Magpie 管理；连接配置只读，密钥不会返回页面。底部模型选择器可切换 Magpie 模型或普通 Provider。WebUI 的切换不写回 Magpie 配置，重启服务仍以 Magpie 的默认设置为准。
- Magpie 关闭接入时按其协议恢复原来的键值；首次接入前不存在的键会删除。没有完整网关配置时，bqagent 回到原有 Provider/环境变量。曾手动配置本文件的用户会恢复到原来的文件键值。
- `api_key` 是 Magpie 签发给此 Agent 的网关 key，以明文保存在此文件中，请勿提交到仓库。插件不读取、解密或复制原有 Provider 密钥。
- 可通过 Magpie 资源库同步 `~/.agent/AGENT.md` 和 `~/.agent/skills`。未声明 MCP 同步：bqagent 当前只支持 Streamable HTTP，不能完整接收通用 Claude MCP 配置中的 stdio 服务。
- 插件入口只使用标准 JavaScript，不依赖 Node/Bun API，不直接读写文件或发起网络请求。文件写入与恢复由 Magpie 管理。

## 定向验证

```sh
node --test plugins/bqa-magpie/agent.test.js
go test ./internal/globalconfig -run '^TestLoadMagpie(Connection|MissingDisconnectedAndInvalid)$' -count=1
go test ./cmd/agent -run '^TestRuntimeConfigFromSources(PrefersSavedProvider|MagpieConnectAndDisconnect)$' -count=1
go test ./internal/server -run '^(TestApplySavedProviderPrefersMagpieUntilDisconnected|TestMagpieProviderSettingsAndSelection|TestMagpieProviderSettingsPreserveExternalConfig|TestMagpieProviderSettingsPreserveShadowedProvider)$' -count=1
```

上述单元测试使用内存数据和临时目录，不改动真实用户配置，也不请求模型或启动 Magpie。
