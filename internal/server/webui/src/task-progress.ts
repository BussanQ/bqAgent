import type { MessageSnapshot, TaskState } from "./types";

export const TASK_UPDATED_KIND = "task.updated";

const STATUS_LABELS: Record<string, string> = {
  active: "进行中",
  completed: "已完成",
  blocked: "已阻塞",
  awaiting_input: "等待输入",
  cancelled: "已取消",
};

export function isTaskUpdatedEvent(event: { kind: string }): boolean {
  return event.kind === TASK_UPDATED_KIND;
}

export type ParsedTask = { ok: true; state: TaskState | null } | { ok: false };

// Strict parse. JSON null / undefined is an explicit "no task"; malformed input is { ok: false }.
export function parseTaskState(value: unknown): ParsedTask {
  if (typeof value === "string") {
    try { value = JSON.parse(value); } catch (_) { return { ok: false }; }
  }
  if (value === null || value === undefined) return { ok: true, state: null };
  if (typeof value !== "object" || Array.isArray(value)) return { ok: false };
  const raw = value as Record<string, unknown>;
  if (typeof raw.goal !== "string" || !raw.goal.trim()) return { ok: false };
  if (typeof raw.status !== "string" || !Object.prototype.hasOwnProperty.call(STATUS_LABELS, raw.status)) return { ok: false };
  if (!Array.isArray(raw.criteria) || raw.criteria.length === 0) return { ok: false };
  // Go marshals a nil Remaining slice as null; treat null/missing as empty.
  const remainingRaw = raw.remaining === null || raw.remaining === undefined ? [] : raw.remaining;
  if (!Array.isArray(remainingRaw)) return { ok: false };
  const criteria: TaskState["criteria"] = [];
  for (const item of raw.criteria) {
    if (!item || typeof item !== "object") return { ok: false };
    const c = item as Record<string, unknown>;
    if (typeof c.description !== "string" || typeof c.done !== "boolean") return { ok: false };
    if (c.evidence !== undefined && typeof c.evidence !== "string") return { ok: false };
    criteria.push({ description: c.description, done: c.done, evidence: (c.evidence as string | undefined) || "" });
  }
  if (!remainingRaw.every((item) => typeof item === "string")) return { ok: false };
  if (raw.verification !== undefined && raw.verification !== null && !Array.isArray(raw.verification)) return { ok: false };
  if (raw.reason !== undefined && typeof raw.reason !== "string") return { ok: false };
  if (raw.updated_at !== undefined && typeof raw.updated_at !== "string") return { ok: false };
  return {
    ok: true,
    state: {
      goal: raw.goal.trim(),
      status: raw.status,
      criteria,
      remaining: (remainingRaw as string[]).filter((item) => item.trim() !== ""),
      verification: (Array.isArray(raw.verification) ? raw.verification : []) as TaskState["verification"],
      reason: (raw.reason as string | undefined) || "",
      updated_at: (raw.updated_at as string | undefined) || "",
    },
  };
}

// Single-task store. Content comes only from task.updated snapshots and restored history.
export class TaskProgress {
  state: TaskState | null = null;
  // Highest accepted task seq per turn, shared by every item_id of that turn.
  private seqs = new Map<string, number>();

  constructor(private scope = "") {}

  // Switching sessions (or any new scope) drops task data and sequence memory.
  reset(scope = this.scope): void {
    this.state = null;
    this.seqs.clear();
    this.scope = scope;
  }

  // Invalid persisted history is treated as no task.
  restore(value: unknown, scope = this.scope): void {
    this.reset(scope);
    const parsed = parseTaskState(value);
    this.state = parsed.ok ? parsed.state : null;
  }

  // expectedTurnId rejects snapshots from a different turn inside the same scope.
  apply(event: MessageSnapshot, scope = this.scope, expectedTurnId = ""): boolean {
    if (!isTaskUpdatedEvent(event) || scope !== this.scope) return false;
    if (!event.turn_id || event.item_id !== event.turn_id + "/task" || !Number.isFinite(event.seq)) return false;
    if (expectedTurnId && event.turn_id !== expectedTurnId) return false;
    const old = this.seqs.get(event.turn_id);
    if (old !== undefined && event.seq <= old) return false;
    const parsed = parseTaskState(event.text);
    if (!parsed.ok) return false;
    this.seqs.set(event.turn_id, event.seq);
    this.state = parsed.state;
    return true;
  }
}

function el(tag: string, className: string, text?: string): HTMLElement {
  const node = document.createElement(tag);
  node.className = className;
  if (text !== undefined) node.textContent = text;
  return node;
}

export function renderTaskPanel(container: HTMLElement, state: TaskState | null): void {
  container.textContent = "";
  if (!state) {
    container.hidden = true;
    delete container.dataset.status;
    return;
  }
  container.hidden = false;
  container.dataset.status = state.status;
  const doneCount = state.criteria.filter((item) => item.done).length;

  const head = el("div", "task-panel-head");
  head.appendChild(el("span", "task-panel-label", "当前任务"));
  head.appendChild(el("span", "task-panel-status", STATUS_LABELS[state.status] || state.status || "未知"));
  if (state.criteria.length) head.appendChild(el("span", "task-panel-count", "验收 " + doneCount + "/" + state.criteria.length));
  container.appendChild(head);
  container.appendChild(el("div", "task-panel-goal", state.goal));

  if (state.criteria.length) {
    const list = el("ul", "task-panel-criteria");
    state.criteria.forEach((item) => {
      const row = el("li", "task-panel-criterion" + (item.done ? " done" : ""));
      row.appendChild(el("span", "task-panel-mark", item.done ? "✓" : "○"));
      const body = el("span", "task-panel-text", item.description);
      if (item.done && item.evidence) body.appendChild(el("small", "task-panel-evidence", item.evidence));
      row.appendChild(body);
      list.appendChild(row);
    });
    container.appendChild(list);
  }
  if (state.remaining.length) {
    const section = el("div", "task-panel-remaining");
    section.appendChild(el("strong", "task-panel-subtitle", "剩余"));
    const list = el("ul", "task-panel-remaining-list");
    state.remaining.forEach((item) => list.appendChild(el("li", "", item)));
    section.appendChild(list);
    container.appendChild(section);
  }
  if (state.reason) container.appendChild(el("div", "task-panel-reason", "原因：" + state.reason));
}
