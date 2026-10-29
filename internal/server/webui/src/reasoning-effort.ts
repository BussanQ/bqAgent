import type { ReasoningEffort } from "./types";

export const REASONING_EFFORT_VALUES: ReasoningEffort[] = ["auto", "low", "medium", "high", "max"];
export const DEFAULT_REASONING_EFFORT: ReasoningEffort = "high";
export const REASONING_EFFORT_LABELS: Record<ReasoningEffort, string> = { auto: "自动", low: "低", medium: "中", high: "高", max: "最高" };

export function normalizeReasoningEffort(value: unknown): ReasoningEffort {
  const normalized = String(value || "").trim().toLowerCase() as ReasoningEffort;
  return REASONING_EFFORT_VALUES.includes(normalized) ? normalized : "auto";
}
