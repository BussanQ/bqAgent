import { describe, expect, it } from "vitest";
import {
  filterProviderModels,
  providerModelOptions,
  providerPayload,
  modelDisplayName,
  selectedProviderModelNames,
  providerAPIKeyPlaceholder,
  providerAPITypeLabel,
  providerListSubtitle,
  remainingActiveProvider,
  resolveDefaultModel,
  resolveProviderEditorIndex,
  selectedProviderModelIDs,
  toggleProviderModel,
  upsertProviderModel,
} from "./providers";

describe("Provider 设置页辅助逻辑", () => {
  it("解析协议标签", () => {
    expect(providerAPITypeLabel("openai")).toBe("OpenAI Chat Completions");
    expect(providerAPITypeLabel("openai-response", "short")).toBe("Responses");
    expect(providerAPITypeLabel("custom")).toBe("custom");
  });

  it("打开设置时优先选中当前 Provider，没有列表则进入新增", () => {
    const providers = [{ id: "openai" }, { id: "deepseek" }];
    expect(resolveProviderEditorIndex(providers, "deepseek")).toBe(1);
    expect(resolveProviderEditorIndex(providers, "missing", 0)).toBe(0);
    expect(resolveProviderEditorIndex([], "openai")).toBe(-1);
  });

  it("模型目录支持添加、勾选和筛选", () => {
    let models = upsertProviderModel([], "  gpt-4o  ", true);
    models = upsertProviderModel(models, "deepseek-chat", false);
    models = upsertProviderModel(models, "gpt-4o", true);
    models = toggleProviderModel(models, "deepseek-chat", true);
    expect(selectedProviderModelIDs(models)).toEqual(["gpt-4o", "deepseek-chat"]);
    expect(filterProviderModels(models, "DEEP").map((item) => item.id)).toEqual(["deepseek-chat"]);
    expect(upsertProviderModel(models, "   ", true)).toEqual(models);
  });

  it("默认模型回落到第一个已启用项", () => {
    expect(resolveDefaultModel(["a", "b"], "b")).toBe("b");
    expect(resolveDefaultModel(["a", "b"], "missing")).toBe("a");
    expect(resolveDefaultModel([], "a")).toBe("");
  });

  it("列表副标题和删除后的当前 Provider", () => {
    expect(providerListSubtitle({ api_type: "anthropic", models: ["claude"] })).toBe("Anthropic · 1 个模型");
    expect(providerListSubtitle({ api_type: "openai", models: [] })).toBe("OpenAI · 未配置模型");
    expect(remainingActiveProvider([{ id: "b" }], "a")).toBe("b");
    expect(remainingActiveProvider([{ id: "a" }, { id: "b" }], "a")).toBe("a");
    expect(providerAPIKeyPlaceholder(true)).toBe("已保存；留空保持不变");
    expect(providerAPIKeyPlaceholder(false)).toBe("输入 API Key");
  });
});


describe("模型展示名称", () => {
  it("保留名称，按名称或 ID 筛选，并提交原始 ID", () => {
    let models = providerModelOptions(["m", "legacy"], true, { m: "Friendly Model" });
    models = upsertProviderModel(models, { id: "m", display_name: "Updated Name" }, true);
    models = toggleProviderModel(models, "m", false);
    expect(models[0].display_name).toBe("Updated Name");
    models = toggleProviderModel(models, "m", true);
    expect(filterProviderModels(models, "UPDATED").map(item => item.id)).toEqual(["m"]);
    expect(filterProviderModels(models, "legacy").map(item => item.id)).toEqual(["legacy"]);
    expect(selectedProviderModelIDs(models)).toEqual(["m", "legacy"]);
    expect(selectedProviderModelNames(models)).toEqual({ m: "Updated Name" });
    expect(modelDisplayName("legacy", "  ")).toBe("legacy");
    const provider = providerPayload({ id: "p", name: "P", api_type: "openai", base_url: "", models: ["m"], default_model: "m", model_display_names: { m: "Friendly Model" } });
    expect(provider.model_display_names).toEqual({ m: "Friendly Model" });
    expect(provider.default_model).toBe("m");
  });
});
