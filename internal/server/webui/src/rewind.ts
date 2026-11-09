import type { APIClient } from "./api-client";
import type { SentAttachments } from "./attachments";
import type { RewindAction } from "./types";

export function requestRewind(api: APIClient, workspace: string, session: string, action: RewindAction, replay: boolean) {
  // A target belongs to one immutable user submission and one visible epoch.
  // All tabs can retry that cut after losing the HTTP response.
  const operation = action.operation || "rewind-" + action.expected_context + "-" + action.target;
  return api.json<Partial<SentAttachments> & { text: string; context_number: number; agent?: "codex" | "claude" }>("/api/v1/webui/rewind", {
    method: "POST", headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ ...action, operation, workspace_id: workspace, session_id: session, replay }),
  });
}

export function rewindButton(action: RewindAction, replay: boolean, run: () => void): HTMLButtonElement {
  const btn = document.createElement("button");
  btn.type = "button";
  btn.className = "rewind-action";
  btn.textContent = replay ? "取回回退草稿" : action.operation ? "重试回退" : "回退到这里";
  btn.title = replay ? "把当时的消息和附件放入输入框" : "回到这条消息发送前的 Codex 上下文";
  btn.addEventListener("click", run);
  return btn;
}
