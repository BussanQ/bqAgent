import type { ChatMode } from "./types";

export function normalizeChatMode(value: unknown): ChatMode {
  return String(value || "").toLowerCase() === "ask" ? "ask" : "run";
}

export function chatModePlaceholder(mode: ChatMode): string {
  return mode === "ask"
    ? "Ask 模式：只读问答，可粘贴图片或添加文件"
    : "Run 模式：可使用完整工具能力，可粘贴图片或添加文件";
}

export function chatModeLabel(mode: ChatMode): string {
  return mode === "ask" ? "Ask" : "Run";
}
