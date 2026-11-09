import type { ChatMode, ConversationType, GroupInfo, GroupParticipant } from "./types";

export const builtinAgent = "bqagent";
// The service handles these before external turn routing. Other slash input
// (including Pi skill: names and ACP commands) belongs to the selected agent.
const hostCommands = new Set(["agent", "ask", "default", "fast", "feedback", "memory", "model", "run", "skill"]);

// Selection belongs to the conversation; installed agents belong to the workspace.
// Refreshing the catalog must never reset a choice restored from history.
export class SingleAgentPicker {
  private selected?: string;
  private pending = false;
  private reported?: string;
  private catalog: GroupParticipant[] = [];
  private revision = 0;
  private mode: ChatMode = "run";
  private type: ConversationType = "default";
  private busy = false;
  private catalogError = false;
  private catalogRequest?: Promise<GroupInfo>;

  constructor(private select: HTMLSelectElement, private onChange: () => void) {
    select.addEventListener("change", () => this.selectNext(select.value));
  }

  selectNext(agent: string): void {
    this.selected = agent;
    this.pending = true;
    this.render();
    this.onChange();
  }

  get requestAgent(): string | undefined {
    return this.type === "group" || this.mode === "ask" ? undefined : this.selected;
  }

  // A typed route can override the picker before the server confirms the sender.
  externalResponder(message: string, selected = this.selected): string | undefined {
    if (this.type === "group" || this.mode === "ask") return;
    if (message.startsWith("/")) {
      const name = message.slice(1).split(/\s/, 1)[0].toLowerCase();
      const explicit = this.catalog.find(agent => agent.id === name);
      if (explicit) return explicit.id;
      if (hostCommands.has(name)) return;
    }
    return selected === builtinAgent ? undefined : selected;
  }

  commandContext(value: string): { query: string; agent?: string; qualified?: boolean } {
    const selected = this.requestAgent;
    if (this.type === "group" || this.mode === "ask") return { query: value };
    const route = /^\/(\S+)[ \t]+([\s\S]*)$/.exec(value);
    const target = route && this.catalog.find(option => option.id === route[1].toLowerCase());
    if (route && target) return { query: route[2], agent: target.id, qualified: true };
    return { query: value, agent: selected === builtinAgent ? undefined : selected };
  }

  restore(agent?: string, pending?: string): void {
    this.pending = !!pending;
    this.selected = pending;
    this.observe(agent);
  }

  get pendingAgent(): string | undefined { return this.pending ? this.selected : undefined; }

  observe(agent?: string): void {
    this.reported = agent || undefined;
    if (!this.pending) this.selected = this.reported;
    this.render();
  }

  beginSend(responder?: string): void {
    this.pending = false;
    if (responder) this.observe(responder);
  }

  get reportedAgent(): string { return this.reported || builtinAgent; }

  busyTarget(running?: string): string | undefined {
    if (this.type === "group" || this.mode === "ask") return;
    if (!this.pending) return running;
    if (this.selected === builtinAgent && !running) return;
    return this.selected;
  }

  busyContext(text: string, running: string | undefined, hasAttachments: boolean) {
    const raw = text.trimStart();
    // A bare /pi with an image is a complete routed request.
    const view = this.commandContext(hasAttachments && /^\/\S+$/.test(raw) ? raw + " " : raw);
    if (this.type === "group" || this.mode === "ask") return view;
    const agent = view.qualified ? view.agent : this.busyTarget(running) || (hasAttachments ? builtinAgent : undefined);
    return { ...view, agent };
  }

  supportsImages(agent: string): boolean | undefined {
    return this.catalog.find(option => option.id === agent)?.supports_images;
  }

  supportsCompact(agent?: string): boolean {
    const transport = this.catalog.find(option => option.id === agent)?.transport;
    return (agent === "codex" && transport === "app-server") || ((agent === "pi" || agent === "claude") && transport === "cli");
  }

  allowsCustomModel(agent: string): boolean {
    const transport = this.catalog.find(option => option.id === agent)?.transport;
    return transport === "cli" || transport === "app-server";
  }

  async readyForRoute(text: string): Promise<void> {
    if (text.trimStart().startsWith("/") && this.type !== "group" && this.mode === "run") {
      await this.catalogRequest?.catch(() => {});
    }
  }

  async loadWorkspace(load: () => Promise<GroupInfo>): Promise<void> {
    const revision = ++this.revision;
    this.catalog = [];
    this.catalogError = false;
    this.restore();
    try {
      this.catalogRequest = load();
      const result = await this.catalogRequest;
      if (revision !== this.revision) return;
      this.catalog = result.participants.filter(agent => agent.kind === "external" && agent.available);
    } catch {
      if (revision !== this.revision) return;
      this.catalogError = true;
    } finally {
      if (revision === this.revision) this.catalogRequest = undefined;
    }
    this.render();
  }

  configure(type: ConversationType, mode: ChatMode, busy: boolean): void {
    this.type = type;
    this.mode = mode;
    this.busy = busy;
    this.render();
  }

  private render(): void {
    const current = this.requestAgent || builtinAgent;
    this.select.replaceChildren(new Option(builtinAgent, builtinAgent));
    for (const agent of this.catalog) this.select.add(new Option(agent.name, agent.id));
    if (current !== builtinAgent && !this.catalog.some(agent => agent.id === current)) {
      const missing = new Option(current + "（未检测到）", current);
      missing.disabled = true;
      this.select.add(missing);
    }
    this.select.value = current;
    this.select.disabled = this.mode === "ask" || this.type === "group";
    this.select.title = this.mode === "ask" ? "Ask 模式仅使用 bqagent"
      : this.catalogError ? "外部 Agent 列表加载失败" : this.busy ? "为下一条消息选择回答者，当前回合继续运行" : "选择下一条消息的回答者";
  }
}
