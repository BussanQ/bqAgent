import type { ACPPermissionToolCall } from "./types";

export function acpPermissionTitle(toolCall: ACPPermissionToolCall | null | undefined): string {
  if (!toolCall) return "外部 Agent 请求执行操作";
  if (typeof toolCall.title === "string" && toolCall.title.trim()) return toolCall.title.trim();
  if (typeof toolCall.kind === "string" && toolCall.kind.trim()) return "请求执行 " + toolCall.kind.trim() + " 操作";
  return "外部 Agent 请求执行操作";
}

export function isRejectPermissionOption(kind: string | undefined): boolean {
  return typeof kind === "string" && kind.indexOf("reject_") === 0;
}

export function formatACPPermissionInput(rawInput: unknown): string {
  if (rawInput == null) return "";
  if (typeof rawInput === "string") return rawInput;
  try {
    return JSON.stringify(rawInput, null, 2);
  } catch (_) {
    return String(rawInput);
  }
}
