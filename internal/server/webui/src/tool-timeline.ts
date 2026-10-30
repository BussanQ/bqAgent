import { iconMarkup } from "./icons";
import { shouldGroupToolCalls } from "./tool-groups";
import { applyToolResult, type ToolResultCard, type ToolSegment } from "./tool-status";
import type { ToolEventPayload } from "./types";

interface ToolCard extends ToolResultCard {
  arguments: HTMLPreElement;
}
export type ToolCards = Record<string, ToolCard>;
interface ToolGroup { root: HTMLDivElement; items: HTMLDivElement; status: HTMLSpanElement }
export interface ToolTimeline extends HTMLDivElement { toolGroup?: ToolGroup }
let toolDisclosureSequence = 0;

function ensureToolTimeline(bubble: HTMLDivElement): ToolTimeline | null {
  var stack = bubble && bubble.parentElement;
  if (!stack) return null;
  var timeline = stack.querySelector<ToolTimeline>(".tool-timeline");
  if (!timeline) {
    timeline = document.createElement("div") as ToolTimeline;
    timeline.className = "tool-timeline";
    timeline.setAttribute("aria-label", "工具执行记录");
    stack.insertBefore(timeline, bubble);
  }
  return timeline;
}

function toolCardList(cards: ToolCards): ToolCard[] {
  return Object.keys(cards).map(function (id) { return cards[id]; });
}

export function updateToolGroupSummary(timeline: ToolTimeline, cards: ToolCards): void {
  var group = timeline && timeline.toolGroup;
  if (!group) return;
  var cardList = toolCardList(cards);
  var running = 0;
  var failed = 0;
  var killed = 0;
  cardList.forEach(function (card) {
    if (card.state === "running") running++;
    if (card.state === "failed") failed++;
    if (card.state === "killed") killed++;
  });
  group.root.classList.remove("succeeded", "failed");
  if (running === 0) group.root.classList.add(failed > 0 || killed > 0 ? "failed" : "succeeded");
  var details = [cardList.length + " 次"];
  if (running > 0) details.push(running + " 运行中");
  if (failed > 0) details.push(failed + " 失败");
  if (killed > 0) details.push(killed + " 已终止");
  if (running === 0 && failed === 0 && killed === 0) details.push("全部完成");
  group.status.textContent = details.join("，");
}

function ensureToolGroup(timeline: ToolTimeline, cards: ToolCards): ToolGroup | undefined {
  var cardList = toolCardList(cards);
  if (timeline.toolGroup || !shouldGroupToolCalls(cardList.length)) {
    updateToolGroupSummary(timeline, cards);
    return timeline.toolGroup;
  }
  var root = document.createElement("div");
  root.className = "tool-group";
  var button = document.createElement("button");
  button.type = "button";
  button.className = "tool-group-toggle";
  var itemsID = "tool-group-items-" + (++toolDisclosureSequence);
  button.setAttribute("aria-expanded", "false");
  button.setAttribute("aria-controls", itemsID);
  var chevron = document.createElement("span");
  chevron.className = "tool-chevron";
  chevron.innerHTML = iconMarkup("chevron-right");
  var dot = document.createElement("span");
  dot.className = "tool-state-dot";
  var title = document.createElement("span");
  title.className = "tool-group-title";
  title.textContent = "工具调用";
  var status = document.createElement("span");
  status.className = "tool-status";
  status.setAttribute("role", "status");
  status.setAttribute("aria-live", "polite");
  status.setAttribute("aria-atomic", "true");
  var items = document.createElement("div");
  items.className = "tool-group-items";
  items.id = itemsID;
  button.appendChild(chevron);
  button.appendChild(dot);
  button.appendChild(title);
  button.appendChild(status);
  button.addEventListener("click", function () {
    var expanded = button.getAttribute("aria-expanded") === "true";
    button.setAttribute("aria-expanded", expanded ? "false" : "true");
    items.classList.toggle("open", !expanded);
  });
  root.appendChild(button);
  root.appendChild(items);
  timeline.appendChild(root);
  cardList.forEach(function (card) { items.appendChild(card.root); });
  timeline.toolGroup = { root: root, items: items, status: status };
  updateToolGroupSummary(timeline, cards);
  return timeline.toolGroup;
}

export function updateToolTimeline(bubble: HTMLDivElement, cards: ToolCards, eventName: string, payload: ToolEventPayload, previous: ToolSegment[] = []): void {
  if (!payload) return;
  // Automatic delivery reuses the original call ID, even after its segment
  // closed. Resolve that card before creating a timeline or another card.
  const callID = payload.id;
  if (eventName === "tool_result" && callID && !cards[callID]) {
    var original = previous.find(segment => segment.cards[callID]);
    if (original) {
      applyToolResult(cards, original.cards[callID], payload, previous);
      var currentTimeline = bubble.parentElement?.querySelector<ToolTimeline>(".tool-timeline");
      if (currentTimeline) updateToolGroupSummary(currentTimeline, cards);
      return;
    }
  }
  var timeline = ensureToolTimeline(bubble);
  if (!timeline) return;
  var cardID = payload.id || ("tool-" + (payload.seq || Object.keys(cards).length + 1));
  var card = cards[cardID];
  if (!card) {
    var root = document.createElement("div");
    root.className = "tool-card";
    var button = document.createElement("button");
    button.type = "button";
    button.className = "tool-toggle";
    var detailsID = "tool-details-" + (++toolDisclosureSequence);
    button.setAttribute("aria-expanded", "false");
    button.setAttribute("aria-controls", detailsID);
    var chevron = document.createElement("span");
    chevron.className = "tool-chevron";
    chevron.innerHTML = iconMarkup("chevron-right");
    var dot = document.createElement("span");
    dot.className = "tool-state-dot";
    var name = document.createElement("span");
    name.className = "tool-name";
    name.textContent = payload.name || "tool";
    var status = document.createElement("span");
    status.className = "tool-status";
    status.setAttribute("role", "status");
    status.setAttribute("aria-live", "polite");
    status.setAttribute("aria-atomic", "true");
    status.textContent = "运行中";
    button.appendChild(chevron);
    button.appendChild(dot);
    button.appendChild(name);
    button.appendChild(status);
    var details = document.createElement("div");
    details.className = "tool-details";
    details.id = detailsID;
    var argumentsSection = document.createElement("div");
    argumentsSection.className = "tool-section";
    var argumentsLabel = document.createElement("div");
    argumentsLabel.className = "tool-section-label";
    argumentsLabel.textContent = "arguments";
    var argumentsPre = document.createElement("pre");
    argumentsPre.className = "tool-json";
    argumentsPre.textContent = payload.arguments ? JSON.stringify(payload.arguments, null, 2) : "{}";
    argumentsSection.appendChild(argumentsLabel);
    argumentsSection.appendChild(argumentsPre);
    var resultSection = document.createElement("div");
    resultSection.className = "tool-section";
    var resultLabel = document.createElement("div");
    resultLabel.className = "tool-section-label";
    resultLabel.textContent = "result";
    var resultPre = document.createElement("pre");
    resultPre.className = "tool-preview";
    resultPre.textContent = "等待执行结果…";
    resultSection.appendChild(resultLabel);
    resultSection.appendChild(resultPre);
    details.appendChild(argumentsSection);
    details.appendChild(resultSection);
    button.addEventListener("click", function () {
      var expanded = button.getAttribute("aria-expanded") === "true";
      button.setAttribute("aria-expanded", expanded ? "false" : "true");
      details.classList.toggle("open", !expanded);
    });
    root.appendChild(button);
    root.appendChild(details);
    var group = timeline.toolGroup;
    (group ? group.items : timeline).appendChild(root);
    card = cards[cardID] = { root: root, status: status, arguments: argumentsPre, result: resultPre, state: "running" };
    ensureToolGroup(timeline, cards);
  }
  if (payload.arguments) card.arguments.textContent = JSON.stringify(payload.arguments, null, 2);
  if (eventName === "tool_result") applyToolResult(cards, card, payload, previous);
  updateToolGroupSummary(timeline, cards);
}
