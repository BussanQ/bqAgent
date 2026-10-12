import assert from "node:assert/strict"
import { readFileSync } from "node:fs"
import test from "node:test"
import { agent, connect } from "./agent.js"

test("package exposes a Magpie Agent entry and a separate global config", () => {
  const pkg = JSON.parse(readFileSync(new URL("./package.json", import.meta.url)))
  assert.equal(pkg.magpie.agent, "./agent.js")
  assert.equal(pkg.main, undefined)
  assert.equal(agent.id, "bqagent")
  assert.equal(agent.bin, "bqagent")
  assert.equal(agent.config, "~/.agent/magpie.json")
  assert.equal(agent.model, "model")
  assert.equal(agent.prefix, undefined)
  assert.equal(agent.library.instructions, "~/.agent/AGENT.md")
})

test("connect maps the gateway and keeps selected provider/model IDs intact", () => {
  const input = {
    gateway: { v1: "http://127.0.0.1:12345/v1/", key: "test-agent-key" },
    model: { id: "provider/model", name: "Selected model" },
    models: [{ id: "other/model" }, { id: "provider/model", name: "Duplicate" }, null, { id: "" }],
  }
  const before = JSON.stringify(input)
  // Magpie writes the selected model separately using agent.model.
  const config = { [agent.model]: input.model.id, ...connect(input) }
  assert.deepEqual(config, {
    model: "provider/model",
    base_url: "http://127.0.0.1:12345/v1",
    api_key: "test-agent-key",
    models: ["provider/model", "other/model"],
    model_display_names: { "provider/model": "Selected model", "other/model": "other/model" },
  })
  assert.equal(JSON.stringify(input), before)
})

test("connect accepts a single model and rejects missing connection information", () => {
  const gateway = { v1: "https://magpie.example/v1", key: "test-key" }
  assert.deepEqual(connect({ gateway, model: { id: "p/m" } }).models, ["p/m"])
  for (const input of [{}, { gateway }, { gateway: { ...gateway, key: " " }, model: { id: "p/m" } }]) {
    assert.throws(() => connect(input))
  }
})
