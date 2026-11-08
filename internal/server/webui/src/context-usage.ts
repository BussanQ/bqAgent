export interface AgentContextUsage {
  context?: number;
  used: number;
  window: number;
  captured: number;
  model?: string;
  estimated?: boolean;
  summary?: boolean;
  compact_at?: number;
  percentage?: number;
  categories?: {name:string; tokens:number; deferred?:boolean}[];
}

export class AgentContextMeter {
  private scope = "";
  private agent = "";
  private epoch = 0;
  private readings = new Map<string, AgentContextUsage>();

  constructor(private element: HTMLDetailsElement) {}

  configure(scope: string, agent: string, epoch = 0): void {
    if (this.scope === scope && this.agent === agent && this.epoch === epoch) return;
    if (this.scope !== scope || this.epoch !== epoch) this.readings.clear();
    this.scope = scope;
    this.agent = agent;
    this.epoch = epoch;
    this.element.open = false;
    this.render();
  }

  observe(scope: string, agent: string, usage?: AgentContextUsage): void {
    if (scope !== this.scope || !usage || usage.context !== undefined && usage.context !== this.epoch || !Number.isSafeInteger(usage.captured) ||
      !Number.isSafeInteger(usage.used) || !Number.isSafeInteger(usage.window) || usage.used < 0 || usage.window < 0) return;
    const previous = this.readings.get(agent);
    // A resumed catalog request can finish after a newer streaming update.
    if (previous && previous.captured >= usage.captured) return;
    this.readings.set(agent, usage);
    if (agent === this.agent) this.render();
  }

  captured(scope:string, agent:string): number | undefined {
    return scope === this.scope ? this.readings.get(agent)?.captured : undefined;
  }

  reconcileIdle(scope:string, agent:string, before:number | undefined, usage?:AgentContextUsage): void {
    if (usage) { this.observe(scope,agent,usage); return; }
    if (scope===this.scope && this.captured(scope,agent)===before) {
      this.readings.delete(agent);
      if (agent===this.agent) this.render();
    }
  }

  private render(): void {
    const u = this.readings.get(this.agent);
    this.element.hidden = !u?.window;
    if (!u?.window) { this.element.open = false; return; }
    const percentage = typeof u.percentage === "number" && Number.isFinite(u.percentage) && u.percentage >= 0
      ? u.percentage : Math.round(u.used / u.window * 100);
    const percent = Math.round(percentage * 10) / 10 + "%";
    this.element.style.setProperty("--context-fill", Math.min(100, percentage) + "%");
    this.element.classList.toggle("critical", percentage >= 90);
    this.element.querySelector("summary")!.setAttribute("aria-label", this.agent + " 上下文已用 " + percent + "，查看详情");
    this.element.querySelector("[data-percent]")!.textContent = percent;
    this.element.querySelector("[data-title]")!.textContent = [this.agent, u.model].filter(Boolean).join(" · ");
    const format = new Intl.NumberFormat("zh-CN");
    this.element.querySelector("[data-count]")!.textContent = (u.summary ? "摘要估算已用 " : u.estimated ? "估算已用 " : "已用 ") + format.format(u.used) + " / " + format.format(u.window) + " tokens";
    const compact = this.element.querySelector<HTMLElement>("[data-compact]")!;
    compact.hidden = u.compact_at === undefined;
    compact.textContent = u.compact_at === undefined ? "" : "自动压缩阈值 " + format.format(u.compact_at) + " tokens";
    const list = this.element.querySelector<HTMLUListElement>("[data-categories]")!;
    list.replaceChildren();
    for (const row of u.categories || []) {
      const item = document.createElement("li");
      item.textContent = row.name + (row.deferred ? "（按需加载）" : "") + " · " + format.format(row.tokens);
      list.append(item);
    }
    list.hidden = !list.childElementCount;
  }
}
