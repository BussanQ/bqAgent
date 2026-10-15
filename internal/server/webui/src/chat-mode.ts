import type { ChatMode } from "./types";

export function normalizeChatMode(value: unknown): ChatMode {
  return String(value || "").toLowerCase() === "ask" ? "ask" : "run";
}

export function chatModePlaceholder(mode: ChatMode): string {
  return mode === "ask"
    ? "Ask 模式：只读问答，可粘贴或拖入图片和文件"
    : "Run 模式：可使用完整工具能力，可粘贴或拖入图片和文件";
}
