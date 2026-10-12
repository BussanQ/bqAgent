// Magpie Agent plugin: https://usemagpie.ai/docs/zh/plugins#agents
// Runs in Magpie's JavaScript engine, without Node/Bun APIs or dependencies.
export const agent = {
  id: "bqagent",
  name: "bqAgent",
  bin: "bqagent",
  config: "~/.agent/magpie.json",
  format: "json",
  model: "model",
  notice: "切换模型或关闭接入后，请重启 bqagent（包括运行中的 WebUI 服务）。",
  library: {
    instructions: "~/.agent/AGENT.md",
    skills: "~/.agent/skills",
  },
}

export function connect({ gateway, model, models = [] }) {
  if (!gateway || typeof gateway.v1 !== "string" || !gateway.v1.trim() ||
      typeof gateway.key !== "string" || !gateway.key.trim()) {
    throw new Error("Magpie 未提供有效的网关地址或 Agent key")
  }
  if (!model || typeof model.id !== "string" || !model.id.trim()) {
    throw new Error("请选择一个 Magpie 模型")
  }

  const ids = []
  const names = {}
  // Keep the selected model even when it is absent from the supplied catalog.
  for (const item of [model, ...models]) {
    if (!item || typeof item.id !== "string" || !item.id.trim()) continue
    const id = item.id.trim()
    if (ids.includes(id)) continue
    ids.push(id)
    names[id] = typeof item.name === "string" && item.name.trim() ? item.name.trim() : id
  }

  // Magpie writes agent.model itself. No prefix: bqagent sends the complete
  // provider/model identifier directly to the OpenAI-compatible gateway.
  return {
    base_url: gateway.v1.trim().replace(/\/+$/, ""),
    api_key: gateway.key,
    models: ids,
    model_display_names: names,
  }
}
