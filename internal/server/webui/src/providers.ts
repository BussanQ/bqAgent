import type { ProviderView, ProviderModel } from "./types";

export const PROVIDER_API_TYPE_LABELS: Record<string, { full: string; short: string }> = {
  openai: { full: "OpenAI Chat Completions", short: "OpenAI" },
  "openai-response": { full: "OpenAI Responses", short: "Responses" },
  anthropic: { full: "Anthropic Messages", short: "Anthropic" },
};

export interface ProviderModelOption extends ProviderModel {
  selected: boolean;
}

export function providerPayload(value: ProviderView): ProviderView {
  return {
    id: value.id,
    name: value.name,
    api_type: value.api_type,
    base_url: value.base_url || "",
    api_key: "",
    models: value.models || [],
    model_display_names: value.model_display_names,
    default_model: value.default_model,
  };
}

export function providerAPITypeLabel(apiType: string, variant: "full" | "short" = "full"): string {
  const labels = PROVIDER_API_TYPE_LABELS[apiType];
  return labels ? labels[variant] : apiType;
}

export function resolveProviderEditorIndex(providers: Array<{ id: string }>, activeID: string, selected?: number): number {
  if (typeof selected === "number") return selected;
  if (!providers.length) return -1;
  return Math.max(0, providers.findIndex((provider) => provider.id === activeID));
}

export function providerModelOptions(models: string[], selected = true, names: Record<string, string> = {}): ProviderModelOption[] {
  return (models || []).map((id) => ({ id, selected, display_name: names[id] }));
}

export function upsertProviderModel(models: ProviderModelOption[], model: string | ProviderModel, selected: boolean): ProviderModelOption[] {
  const id = (typeof model === "string" ? model : model.id).trim();
  const displayName = typeof model === "string" ? undefined : model.display_name?.trim();
  if (!id) return models.map((item) => ({ ...item }));
  const next = models.map((item) => ({ ...item }));
  const existing = next.find((item) => item.id === id);
  if (existing) {
    if (selected) existing.selected = true;
    if (displayName) existing.display_name = displayName;
    return next;
  }
  next.push({ id, selected, display_name: displayName });
  return next;
}

export function toggleProviderModel(models: ProviderModelOption[], model: string, selected: boolean): ProviderModelOption[] {
  return models.map((item) => item.id === model ? { ...item, selected } : { ...item });
}

export function selectedProviderModelIDs(models: ProviderModelOption[]): string[] {
  return models.filter((item) => item.selected).map((item) => item.id);
}

export function resolveDefaultModel(models: string[], preferred = ""): string {
  if (models.indexOf(preferred) >= 0) return preferred;
  return models[0] || "";
}

export function filterProviderModels(models: ProviderModelOption[], query: string): ProviderModelOption[] {
  const needle = query.trim().toLowerCase();
  if (!needle) return models;
  return models.filter((item) => (item.id.toLowerCase().includes(needle) || modelDisplayName(item.id, item.display_name).toLowerCase().includes(needle)));
}

export function providerListSubtitle(provider: Pick<ProviderView, "api_type" | "models">): string {
  const count = (provider.models || []).length;
  return providerAPITypeLabel(provider.api_type, "short") + " · " + (count ? count + " 个模型" : "未配置模型");
}

export function remainingActiveProvider(providers: Array<{ id: string }>, currentActive: string): string {
  if (providers.some((provider) => provider.id === currentActive)) return currentActive;
  return providers[0] ? providers[0].id : "";
}

export function providerAPIKeyPlaceholder(configured: boolean): string {
  return configured ? "已保存；留空保持不变" : "输入 API Key";
}

export function modelDisplayName(id: string, name?: string): string {
  return name?.trim() || id;
}

export function selectedProviderModelNames(models: ProviderModelOption[]): Record<string, string> {
  return Object.fromEntries(models.filter(item => item.selected && item.display_name?.trim()).map(item => [item.id, item.display_name!.trim()]));
}
