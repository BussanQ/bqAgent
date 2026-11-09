import { updateToolTimeline, type ToolCards } from "./tool-timeline";
import { renderAgentPlan } from "./external-plan";
import { renderAgentSummary, updateSummary, type SummaryView } from "./external-summary";
import type { AgentPlan, ToolEventPayload } from "./types";

export interface ExternalTextEvent { id: string; agent: string; text: string; append?: boolean }
export interface ExternalPlanEvent extends ExternalTextEvent { plan: AgentPlan }
export interface ExternalSummaryEvent extends ExternalTextEvent { index: number; ended?: boolean; truncated?: boolean }
interface TextItem { bubble: HTMLDivElement; text: string }
interface ToolGroup { bubble: HTMLDivElement; cards: ToolCards }

// Native updates keep the DOM node allocated on first observation, including
// when an earlier tool completes after more text or a user correction.
export class ExternalTimelineView {
  private texts = new Map<string, TextItem>();
  private plans = new Map<string,TextItem>();
  private summaries = new Map<string, { bubble: HTMLDivElement; view: SummaryView }>();
  private calls = new Map<string, ToolGroup>();
  private activeText?: TextItem;
  private group?: ToolGroup;

  constructor(private create: (sender: string) => HTMLDivElement,
    private paint: (bubble: HTMLDivElement, text: string, streaming: boolean) => void,
    private paintPlan = renderAgentPlan, private paintSummary = renderAgentSummary) {}

  get used(): boolean { return this.calls.size > 0 || this.texts.size > 0 || this.plans.size > 0 || this.summaries.size > 0; }

  summary(event: ExternalSummaryEvent): void {
    if (!event.id || !Number.isInteger(event.index) || event.index < 0 || event.index >= 32) return;
    let item = this.summaries.get(event.id);
    if (!item) {
      this.boundary();
      item = { bubble: this.create(event.agent), view: { sections: new Map(), ended: false } };
      this.summaries.set(event.id, item);
    }
    updateSummary(item.view, event.index, event.text, !!event.append, event.ended, event.truncated);
    this.paintSummary(item.bubble, item.view);
  }

  text(event: ExternalTextEvent): void {
    if (!event.id) return;
    let item = this.texts.get(event.id);
    if (!item) {
      this.boundary();
      item = { bubble: this.create(event.agent), text: "" };
      this.texts.set(event.id, item);
      this.activeText = item;
    }
    item.text = event.append ? item.text + event.text : event.text;
    const row = item.bubble.closest<HTMLElement>(".msg");
    if (row) row.hidden = !item.text;
    this.paint(item.bubble, item.text, this.activeText === item);
  }

  plan(event: ExternalPlanEvent): void {
    if (!event.id || !event.plan) return;
    let item = this.plans.get(event.id);
    if (!item) {
      this.boundary();
      item = { bubble:this.create(event.agent), text:"" };
      this.plans.set(event.id,item);
    }
    item.text = event.append ? item.text + event.text : event.text;
    this.paintPlan(item.bubble,event.plan,item.text);
  }

  planBubble(id?: string): HTMLDivElement | undefined {
    return id ? this.plans.get(id)?.bubble : undefined;
  }

  tool(name: string, payload: ToolEventPayload, sender: string): void {
    if (!payload.id) return;
    let group = this.calls.get(payload.id);
    if (!group) {
      if (!this.group) {
        this.boundary();
        this.group = { bubble: this.create(sender), cards: Object.create(null) as ToolCards };
        this.group.bubble.hidden = true;
        this.group.bubble.classList.add("is-empty");
      }
      group = this.group;
      this.calls.set(payload.id, group);
    }
    updateToolTimeline(group.bubble, group.cards, name, payload);
  }

  boundary(): void {
    if (this.activeText) this.paint(this.activeText.bubble, this.activeText.text, false);
    this.activeText = undefined;
    this.group = undefined;
  }

  finish(): TextItem | undefined {
    this.boundary();
    return [...this.texts.values()].reverse().find(item => item.text.trim());
  }
}
