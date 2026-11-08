import { APIClient, APIError } from "./api-client";
import type { ForkAction, ForkOrigin } from "./types";

export class ForkRequests {
  private operations = new Map<string,string>();
  private flights = new Map<string,Promise<string>>();
  constructor(private api: APIClient, private newID: () => string) {}

  create(workspace: string, session: string, action: ForkAction): Promise<string> {
    const key = "bqagent.fork." + JSON.stringify([workspace,session,action.expected_context,action.target]);
    const flight = this.flights.get(key);
    if (flight) return flight;
    let id = this.operations.get(key);
    try { id ||= sessionStorage.getItem(key) || undefined; } catch { /* Storage is optional. */ }
    id ||= this.newID();
    this.operations.set(key,id);
    try { sessionStorage.setItem(key,id); } catch { /* Keep in-memory retry. */ }
    const forget = () => {
      this.operations.delete(key);
      try { if (sessionStorage.getItem(key) === id) sessionStorage.removeItem(key); } catch { /* Optional. */ }
    };
    const result = this.api.json<{session_id:string}>("/api/v1/webui/fork", { method:"POST",
      headers:{"Content-Type":"application/json"}, body:JSON.stringify({...action,workspace_id:workspace,session_id:session,operation:id}) })
      .then(reply => {
        if (!reply.session_id || reply.session_id === session) throw new Error("服务未返回独立的聊天");
        forget();
        return reply.session_id;
      }).catch(error => {
        if (error instanceof APIError && (error.status === 424 || error.status === 410)) forget();
        throw error;
      }).finally(() => this.flights.delete(key));
    this.flights.set(key,result);
    return result;
  }
}

export function forkSourceMarker(origin: ForkOrigin, open: (origin: ForkOrigin) => void): HTMLElement {
  const marker = document.createElement("div");
  marker.className = "fork-origin";
  const button = document.createElement("button");
  button.type = "button";
  button.textContent = "返回来源回复";
  button.addEventListener("click", () => open(origin));
  marker.append(button);
  return marker;
}

export function replyAnchor(thread: Element, target: string): HTMLElement | undefined {
  return Array.from(thread.querySelectorAll<HTMLElement>("[data-reply-anchor]")).find(el => el.dataset.replyAnchor === target);
}
