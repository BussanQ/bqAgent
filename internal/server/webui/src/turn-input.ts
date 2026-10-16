export type TurnInputKind = "steering" | "follow_up";
export type TurnInputState = "sending" | "queued" | "applied" | "unconsumed" | "failed";

export interface TurnInputItem {
  localId: string;
  id: string;
  kind: TurnInputKind;
  text: string;
  state: TurnInputState;
}

export interface PendingTurnInput {
  id: string;
  kind: string;
  text: string;
}

export interface TurnInputKeyEvent {
  key: string;
  shiftKey?: boolean;
  ctrlKey?: boolean;
  metaKey?: boolean;
  altKey?: boolean;
  isComposing?: boolean;
}

// Enter corrects the running turn; Ctrl/Cmd/Alt+Enter queues follow-up work.
export function inputKindForKey(event: TurnInputKeyEvent): TurnInputKind | null {
  if (event.key !== "Enter" || event.shiftKey || event.isComposing) return null;
  return event.ctrlKey || event.metaKey || event.altKey ? "follow_up" : "steering";
}

export function isInputAppliedEvent(event: { kind: string }): boolean {
  return event.kind === "input.applied";
}

export const TURN_INPUT_STATE_LABELS: Record<TurnInputState, string> = {
  sending: "发送中",
  queued: "已排队",
  applied: "已应用",
  unconsumed: "未消费",
  failed: "发送失败",
};

export type TurnInputOutcome = "accepted" | "closed" | "notfound" | "rejected";

interface InputResponse { status?: string; id?: string; error?: string }

export interface TurnInputResult {
  outcome: TurnInputOutcome;
  id: string;
  error: string;
}

// Posts a text-only input. Attachments are intentionally never sent; the server rejects them.
export async function postTurnInput(
  request: (url: string, init: RequestInit) => Promise<Response>,
  payload: { turnId: string; workspaceId: string; kind: TurnInputKind; text: string },
): Promise<TurnInputResult> {
  let response: Response;
  try {
    response = await request("/api/v1/chat/input", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ turn_id: payload.turnId, workspace_id: payload.workspaceId, kind: payload.kind, text: payload.text }),
    });
  } catch (error) {
    const status = (error as { status?: number }).status;
    const message = error instanceof Error ? error.message : String(error);
    if (status === 404) return { outcome: "notfound", id: "", error: message };
    if (status === 409) return { outcome: "closed", id: "", error: message };
    if (status === 400) return { outcome: "rejected", id: "", error: message };
    throw error;
  }
  const body = (await response.json()) as InputResponse;
  if (body.status === "accepted" && body.id) return { outcome: "accepted", id: body.id, error: "" };
  return { outcome: "rejected", id: "", error: body.error || "输入未被接受" };
}

// Tracks inputs submitted during one turn. input.applied can arrive before the
// HTTP acceptance response, so applied ids are remembered until matched.
export class TurnInputTracker {
  readonly items: TurnInputItem[] = [];
  private appliedEarly = new Set<string>();
  private closed = false;
  private sequence = 0;

  add(kind: TurnInputKind, text: string): TurnInputItem {
    const item: TurnInputItem = { localId: "local-" + ++this.sequence, id: "", kind, text, state: "sending" };
    this.items.push(item);
    return item;
  }

  accepted(item: TurnInputItem, id: string): void {
    item.id = id;
    if (item.state === "applied") this.appliedEarly.delete(id);
    else if (this.appliedEarly.delete(id)) item.state = "applied";
    else if (this.closed) item.state = "unconsumed";
    else if (item.state === "sending") item.state = "queued";
  }

  failed(item: TurnInputItem): void {
    item.state = "failed";
  }

  // Returns the matching item, or null when the id is not known yet.
  // input.applied can beat the acceptance response; then the item is matched by
  // kind and text so it leaves the queued list and is never restored later.
  applied(id: string, kind?: string, text?: string): TurnInputItem | null {
    let item = this.items.find((candidate) => candidate.id === id);
    if (!item && kind !== undefined && text !== undefined) {
      item = this.items.find((candidate) => candidate.state === "sending" && candidate.kind === kind && candidate.text === text);
      if (item) item.id = id;
    }
    if (!item) {
      this.appliedEarly.add(id);
      return null;
    }
    item.state = "applied";
    return item;
  }

  // Marks every non-applied accepted item unconsumed at turn end. The server list
  // is authoritative; locally still-queued items are also unconsumed because the
  // turn can no longer apply them.
  finish(pending: PendingTurnInput[] = []): TurnInputItem[] {
    this.closed = true;
    const ids = new Set(pending.map((entry) => entry.id));
    const unconsumed: TurnInputItem[] = [];
    for (const item of this.items) {
      if (item.state === "applied" || item.state === "failed" || item.state === "unconsumed") continue;
      if (item.state === "queued" || ids.has(item.id)) {
        item.state = "unconsumed";
        unconsumed.push(item);
      }
    }
    return unconsumed;
  }

  queued(): TurnInputItem[] {
    return this.items.filter((item) => item.state === "sending" || item.state === "queued");
  }
}

// Restores unconsumed text ahead of whatever the user has since typed.
export function restoreDraft(current: string, items: Pick<TurnInputItem, "text">[]): string {
  const restored = items.map((item) => item.text).join("\n\n");
  if (!restored) return current;
  return current.trim() ? restored + "\n\n" + current : restored;
}

export interface TurnInputHost {
  request(url: string, init: RequestInit): Promise<Response>;
  // Identifies the conversation shown now; a changed scope means drafts must not be restored.
  scope(): string;
  takeDraft(): string;
  getDraft(): string;
  setDraft(text: string): void;
  renderQueue(items: TurnInputItem[]): void;
  renderApplied(kind: string, text: string): void;
  notify(message: string): void;
}

// One running turn's input state. It owns the scope captured at start so async
// completions can never restore a draft into another conversation.
export class TurnInputSession {
  readonly tracker = new TurnInputTracker();
  private readonly scope: string;

  constructor(readonly turnId: string, readonly workspaceId: string, private readonly host: TurnInputHost) {
    this.scope = host.scope();
  }

  private sameScope(): boolean {
    return this.host.scope() === this.scope;
  }

  private render(): void {
    this.host.renderQueue(this.tracker.queued());
  }

  private restore(items: TurnInputItem[], notice: string): void {
    if (!items.length) return;
    if (!this.sameScope()) {
      this.host.notify("会话已切换，" + items.length + " 条未发送的输入未恢复");
      return;
    }
    this.host.setDraft(restoreDraft(this.host.getDraft(), items));
    this.host.notify(notice);
  }

  async submit(kind: TurnInputKind): Promise<void> {
    if (!this.sameScope()) return;
    const text = this.host.takeDraft().trim();
    if (!text) return;
    const item = this.tracker.add(kind, text);
    this.render();
    let failure = "";
    try {
      const result = await postTurnInput((url, init) => this.host.request(url, init), { turnId: this.turnId, workspaceId: this.workspaceId, kind, text });
      if (result.outcome === "accepted") {
        this.tracker.accepted(item, result.id);
        if (item.state === "unconsumed") this.restore([item], "任务已结束，输入未被消费，已恢复到输入框");
      } else if (result.outcome === "closed" || result.outcome === "notfound") {
        failure = "任务已结束，输入未被接受，已恢复到输入框";
      } else {
        failure = result.error || "输入被拒绝";
      }
    } catch (error) {
      failure = "输入发送失败：" + (error instanceof Error ? error.message : String(error));
    }
    if (failure) {
      this.tracker.failed(item);
      this.restore([item], failure);
    }
    this.render();
  }

  // Handles an input.applied snapshot: renders the user boundary exactly once.
  onApplied(event: { item_id: string; phase: string; text: string }): void {
    const item = this.tracker.applied(event.item_id, event.phase, event.text);
    this.host.renderApplied(item ? item.kind : event.phase, item ? item.text : event.text);
    this.render();
  }

  finish(pending: PendingTurnInput[] = []): void {
    this.restore(this.tracker.finish(pending), "有输入未被消费，已恢复到输入框");
    this.render();
  }
}
