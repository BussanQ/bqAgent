import { APIClient } from "./api-client";
import type { ChatMode, ConversationType } from "./types";

export function parseClear(text: string) {
  const match = /^(?:\/(codex|pi|claude|opencode|cursor)\s+)?\/clear(?:\s+([\s\S]*))?$/i.exec(text.trim());
  return match ? { agent: match[1]?.toLowerCase(), args: match[2] || "" } : undefined;
}

export function clearRefusal(args: string, mode: ChatMode, type: ConversationType, attachments: boolean, busy: boolean) {
  if (type === "group" || mode !== "run") return "/clear 仅支持 Run 单聊";
  if (args || attachments) return "/clear 不接受参数或附件";
  if (busy) return "当前回合正在运行，请先停止或等待回复结束后再清空上下文";
}

// Keep the operation ID until the divider has been read back. Retrying a lost
// HTTP response, including after reload, must not insert a second divider.
export class ClearContextRequests {
  private pending = new Map<string, string>();

  constructor(private api: APIClient, private newID: () => string) {}

  async clear(workspace: string, session: string, text: string, agent?: string) {
    const key = "bqagent.clear." + JSON.stringify([workspace, session, agent || ""]);
    let id = this.pending.get(key);
    try { id ||= sessionStorage.getItem(key) || undefined; } catch { /* In-memory retry still works. */ }
    id ||= this.newID();
    this.pending.set(key, id);
    try { sessionStorage.setItem(key, id); } catch { /* Storage can be disabled. */ }
    await this.api.json("/api/v1/webui/chat", { method: "POST", headers: { "Content-Type": "application/json", Accept: "application/json" },
      body: JSON.stringify({ workspace_id: workspace, session_id: session, message: text, agent, turn_id: id, mode: "run" }) });
    return () => {
      this.pending.delete(key);
      try { if (sessionStorage.getItem(key) === id) sessionStorage.removeItem(key); } catch { /* Optional storage. */ }
    };
  }
}

export function clearDivider(content: string): HTMLElement {
  const row = document.createElement("div");
  row.className = "context-clear-divider";
  row.setAttribute("role", "separator");
  row.setAttribute("aria-label", content);
  row.textContent = content;
  return row;
}
