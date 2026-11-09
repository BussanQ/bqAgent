import { renderMarkdown } from "./markdown";

export interface SummarySection {
  item: string;
  index: number;
  ended?: boolean;
  truncated?: boolean;
}

export interface SummaryView {
  sections: Map<number, { text: string; truncated: boolean }>;
  ended: boolean;
}

export function updateSummary(view: SummaryView, index: number, text: string, append: boolean, ended?: boolean, truncated?: boolean): void {
  if (!Number.isInteger(index) || index < 0 || index >= 32) return;
  const previous = view.sections.get(index)?.text || "";
  view.sections.set(index, { text: append ? previous + text : text, truncated: !!truncated });
  view.ended ||= !!ended;
}

export function summaryVisible(view: SummaryView): boolean {
  return Array.from(view.sections.values()).some(section => section.text.trim() || section.truncated);
}

const current = new WeakMap<HTMLDetailsElement, SummaryView>();

function paintBody(details: HTMLDetailsElement): void {
  const body = details.querySelector<HTMLElement>(".agent-summary-body")!;
  const view = current.get(details);
  if (!details.open || !view) { body.replaceChildren(); return; }
  const sections = Array.from(view.sections).sort(([a], [b]) => a - b);
  body.innerHTML = renderMarkdown(sections.map(([, section]) => section.text).filter(Boolean).join("\n\n"));
  if (sections.some(([, section]) => section.truncated)) {
    const note = document.createElement("small");
    note.textContent = "摘要已截断";
    body.append(note);
  }
}

export function renderAgentSummary(bubble: HTMLDivElement, view: SummaryView, live = true): void {
  let details = bubble.querySelector<HTMLDetailsElement>("details.agent-summary");
  if (!details) {
    details = document.createElement("details");
    details.className = "agent-summary";
    const label = document.createElement("summary"), body = document.createElement("div");
    body.className = "agent-summary-body";
    details.append(label, body);
    details.addEventListener("toggle", () => paintBody(details!));
    bubble.replaceChildren(details);
  }
  current.set(details, view);
  const row = bubble.closest<HTMLElement>(".msg");
  if (row) row.hidden = !summaryVisible(view);
  bubble.classList.add("rendered", "agent-summary-card");
  details.firstElementChild!.textContent = live && !view.ended ? "正在生成推理摘要" : "推理摘要";
  if (details.open) paintBody(details);
}
