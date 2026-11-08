import { SSEBuffer } from "./stream";
import type { SSEEvent } from "./types";

export function turnBookmark(workspace: string): string {
  try { return sessionStorage.getItem("bqagent.web-turn." + workspace) || ""; } catch { return ""; }
}

export function rememberTurn(workspace: string, id: string): void {
  try {
    if (id) sessionStorage.setItem("bqagent.web-turn." + workspace, id);
    else sessionStorage.removeItem("bqagent.web-turn." + workspace);
  } catch { /* A session can also be rediscovered through its history. */ }
}

function retryDelay(ms: number, signal: AbortSignal): Promise<void> {
  signal.throwIfAborted();
  return new Promise((resolve, reject) => {
    const cancel = () => { clearTimeout(timer); reject(signal.reason); };
    const timer = setTimeout(() => { signal.removeEventListener("abort", cancel); resolve(); }, ms);
    signal.addEventListener("abort", cancel, { once: true });
  });
}

// POST is attempted exactly once. A lost response, partial frame, or dropped
// subscription is recovered using GET and the last fully delivered event ID.
export async function* followTurn(
  start: (() => Promise<Response>) | undefined,
  subscribe: (cursor: number) => Promise<Response>,
  signal: AbortSignal,
  reconnecting: () => void,
  wait: (ms: number, signal: AbortSignal) => Promise<void> = retryDelay,
): AsyncGenerator<SSEEvent> {
  let cursor = 0, failures = 0, unknownStart = false;
  for (;;) {
    signal.throwIfAborted();
    const open = start;
    start = undefined;
    let reader: ReadableStreamDefaultReader<Uint8Array> | undefined;
    try {
      const response = await (open ? open() : subscribe(cursor));
      if (!response.ok || !response.body) throw Object.assign(new Error("HTTP " + response.status), { status: response.status });
      unknownStart = false;
      reader = response.body.getReader();
      const decoder = new TextDecoder(), buffer = new SSEBuffer();
      for (;;) {
        signal.throwIfAborted();
        const chunk = await reader.read();
        if (chunk.done) break;
        for (const event of buffer.push(decoder.decode(chunk.value, { stream: true }))) {
          signal.throwIfAborted();
          if (event.id === undefined || event.id <= cursor) continue;
          if (event.id !== cursor + 1) throw new Error("事件序号不连续");
          failures = 0;
          cursor = event.id;
          yield event;
          if (["done", "error", "stopped"].includes(event.event)) return;
        }
      }
    } catch (error) {
      const status = (error as { status?: number }).status;
      if (open && !status) unknownStart = true;
      const registering = status === 404 && unknownStart && failures < 4;
      if (signal.aborted || (error as Error).name === "AbortError" || (status && status >= 400 && status < 500 && !registering)) throw error;
    } finally {
      if (reader) { void reader.cancel().catch(() => {}); reader.releaseLock(); }
    }
    reconnecting();
    await wait(Math.min(4000, 250 * 2 ** Math.min(failures++, 4)), signal);
  }
}

// Navigation waits for the old renderer to release the composer. Its abort
// signal belongs exclusively to the subscription, never to the agent turn.
export class TurnViewer {
  private current?: { id: string; controller: AbortController; done: Promise<void>; leaving: boolean };

  async observe<T>(id: string, render: (controller: AbortController) => Promise<T>): Promise<T | undefined> {
    if (this.current) throw new Error("已有回合正在显示");
    const view = { id, controller: new AbortController(), done: Promise.resolve(), leaving: false };
    this.current = view;
    let result: T | undefined;
    view.done = render(view.controller).then(value => { result = value; })
      .finally(() => { if (this.current === view) this.current = undefined; });
    await view.done;
    return view.leaving ? undefined : result;
  }

  leaving(id: string): boolean { return this.current?.id === id && this.current.leaving; }

  async detach(): Promise<void> {
    const view = this.current;
    if (!view) return;
    view.leaving = true;
    view.controller.abort();
    await view.done;
  }
}

function draftKey(workspace: string, chat: string): string {
  return "bqagent.web-draft." + JSON.stringify([workspace,chat]);
}
export function readChatDraft(workspace: string, chat: string): string {
  try { return sessionStorage.getItem(draftKey(workspace,chat)) || ""; } catch { return ""; }
}
export function saveChatDraft(workspace: string, chat: string, text: string): void {
  try {
    const key = draftKey(workspace,chat);
    if (text) sessionStorage.setItem(key,text); else sessionStorage.removeItem(key);
  } catch { /* The draft is still available in the visible composer. */ }
}

export function clearChatViewData(): void {
  try {
    for (let i = sessionStorage.length - 1; i >= 0; i--) {
      const key = sessionStorage.key(i);
      if (key?.startsWith("bqagent.web-turn.") || key?.startsWith("bqagent.web-draft.") || key?.startsWith("bqagent.web-agent-draft.") || key?.startsWith("bqagent.web-command.")) sessionStorage.removeItem(key);
    }
  } catch { /* Storage can be disabled by browser policy. */ }
}
