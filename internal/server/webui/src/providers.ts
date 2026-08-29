import type { ProviderView } from "./types";

export function providerPayload(value: ProviderView): ProviderView {
  return {
    id: value.id,
    name: value.name,
    api_type: value.api_type,
    base_url: value.base_url || "",
    api_key: "",
    models: value.models || [],
    default_model: value.default_model,
  };
}
