import { bindAgentDraft, readAgentDraft, saveModelDraft, type ModelDraft } from "./agent-drafts";
import type { AgentContextUsage } from "./context-usage";

interface ModelPreferences {
  model?: string;
  custom?: boolean;
  efforts?: Record<string, string>;
  new_session?: boolean;
}
export interface ModelPick { model: string; custom?: boolean; effort?: string }

export interface AgentModelCatalog {
  supports_interaction_mode?: boolean;
  interaction_modes?: string[] | null;
  interaction_mode?: string;
  supports_service_tier?: boolean;
  service_tiers?: Record<string, { id: string; name: string; description?: string }[]>;
  selected_service_tier?: string;
  preferred_service_tier?: string;
  default_service_tier?: string;
  context_usage?: AgentContextUsage;
  models: { id: string; name: string }[] | null;
  selected?: string;
  error?: string;
  allow_custom?: boolean;
  supports_images?: boolean;
  supports_compact?: boolean;
  default_model?: string;
  selected_effort?: string;
  session_scoped_effort?: boolean;
  efforts?: Record<string, { options: { id: string; name: string }[] | null; default?: string }>;
  preferences?: ModelPreferences;
}

export interface AgentTurnSelection {
  agent_interaction_mode?: string;
  agent?: string;
  agent_model?: string;
  agent_effort?: string;
  agent_service_tier?: string;
}

interface ModelMenu extends ModelDraft {
  revision: symbol;
  request?: AbortController;
  capabilities?: AgentModelCatalog;
  refreshAfterBind?: boolean;
  catalog?: AgentModelCatalog;
  listedAt?: number;
  confirmed?: string;
  confirmation?: symbol;
  confirmedEffort?: string;
  confirmedTier?: string;
  confirmedMode?: string;
  confirmedPreferredTier?: string;
  error?: string;
  metadataRead?: Promise<void>;
  listingRead?: Promise<void>;
  lastCustomWrite?: string;
  pickedEfforts?: Map<string, string>;
}

const customValue = "\u0001custom";
const catalogLifetimeMs = 5 * 60 * 1000;

function withCachedModels(catalog: AgentModelCatalog, cached?: AgentModelCatalog): AgentModelCatalog {
  if (!catalog.error || catalog.models?.length || !cached?.models?.length ||
    !!cached.allow_custom !== !!catalog.allow_custom || !!cached.session_scoped_effort !== !!catalog.session_scoped_effort) return catalog;
  return { ...catalog, models: cached.models, efforts: catalog.efforts ?? cached.efforts,
    service_tiers: catalog.service_tiers ?? cached.service_tiers };
}

export class AgentModelPicker {
  private scope?: string;
  private agent = "";
  private busy = false;
  private allowCustomDuringLoad = false;
  private menus = new Map<string, ModelMenu>();
  private preview?: AbortController;
  private previewRead?: Promise<void>;
  private previewError = "";
  private readonly settings?: HTMLDetailsElement;
  private readonly modelName: HTMLElement | null;

  constructor(private controls: HTMLElement, private select: HTMLSelectElement, private input: HTMLInputElement,
    private retry: HTMLButtonElement, private effort: HTMLSelectElement,
    private load: (agent: string, signal: AbortSignal, model?: string) => Promise<AgentModelCatalog>,
    private loadCapabilities?: (agent: string, signal: AbortSignal) => Promise<AgentModelCatalog> | undefined,
    private onSelectionChange?: () => void,
    private remember?: (agent: string, pick: ModelPick) => void,
    private tier?: HTMLSelectElement, private mode?: HTMLSelectElement) {
    this.modelName = controls.querySelector(".agent-model-name");
    if (controls instanceof HTMLDetailsElement) {
      this.settings = controls;
      controls.ownerDocument.addEventListener("pointerdown", event => {
        if (controls.open && !event.composedPath().includes(controls)) controls.open = false;
      });
      controls.addEventListener("keydown", event => {
        if (event.key !== "Escape" || !controls.open) return;
        event.stopPropagation();
        controls.open = false;
        controls.querySelector("summary")?.focus();
      });
      controls.addEventListener("invalid", () => { controls.open = true; }, true);
    }
    mode?.addEventListener("change", () => {
      this.selectInteractionMode(this.agent,mode.value);
    });
    tier?.addEventListener("change", () => {
      const menu = this.menus.get(this.agent);
      if (!menu || !tier.value) return;
      menu.revision = Symbol();
      menu.submittedTurn = undefined;
      menu.draft = this.effectiveModel(menu);
      menu.draftTier = tier.value;
      this.saveDraft(menu);
      this.render();
      this.onSelectionChange?.();
    });
    select.addEventListener("change", () => {
      const menu = this.menus.get(this.agent);
      if (!menu) return;
      menu.revision = Symbol();
      menu.submittedTurn = undefined;
      menu.custom = select.value === customValue;
      menu.draft = menu.custom ? "" : select.value;
      menu.draftEffort = undefined;
      menu.draftTier = undefined;
      menu.lastCustomWrite = undefined;
      this.saveDraft(menu);
      if (!menu.custom) this.remember?.(this.agent, { model: select.value });
      this.previewEffort();
      if (menu.custom) input.focus();
      this.onSelectionChange?.();
    });
    input.addEventListener("input", () => {
      const menu = this.menus.get(this.agent);
      if (menu?.custom) {
        menu.revision = Symbol();
        menu.submittedTurn = undefined;
        menu.draft = input.value.trim();
        this.updateModelName(menu);
        this.saveDraft(menu);
        input.setCustomValidity("");
        this.onSelectionChange?.();
      }
    });
    input.addEventListener("change", () => this.commitCustom());
    effort.addEventListener("change", () => {
      const menu = this.menus.get(this.agent);
      if (menu) {
        menu.revision = Symbol();
        menu.submittedTurn = undefined;
        const model = this.effectiveModel(menu);
        if (!model) return;
        menu.draft = model;
        menu.draftEffort = effort.value;
        (menu.pickedEfforts ??= new Map()).set(model, effort.value);
        this.saveDraft(menu);
        this.remember?.(this.agent, { model, effort: effort.value });
        this.onSelectionChange?.();
      }
    });
    retry.addEventListener("click", () => this.previewError ? this.previewEffort() : this.refresh());
  }

  private saveDraft(menu: ModelMenu): void {
    if (this.remember && this.scope) saveModelDraft(this.scope, this.agent, menu);
  }

  private commitCustom(): void {
    const menu = this.menus.get(this.agent);
    if (menu?.custom && this.canEnterModelID(menu) && menu.draft && menu.draft !== menu.lastCustomWrite) {
      menu.lastCustomWrite = menu.draft;
      this.remember?.(this.agent, { model: menu.draft, custom: true });
    }
  }

  private canEnterModelID(menu?: ModelMenu): boolean {
    const catalog = menu?.catalog ?? menu?.capabilities;
    return catalog ? !!catalog.allow_custom : this.allowCustomDuringLoad;
  }

  private launchModel(menu: ModelMenu): string | undefined {
    const catalog = menu.catalog ?? menu.capabilities;
    const prefs = catalog?.preferences;
    // A pending reset and a provider acknowledgement both supersede the
    // launch setting. A removed listed model must not become a custom ID.
    if (!prefs?.model || menu.draft === "" || menu.confirmed !== undefined) return;
    if (prefs.custom && catalog?.allow_custom || catalog?.models?.some(row => row.id === prefs.model)) return prefs.model;
  }

  private effectiveModel(menu: ModelMenu): string {
    const catalog = menu.catalog ?? menu.capabilities;
    return menu.draft || menu.confirmed || this.launchModel(menu) || catalog?.selected || catalog?.default_model || "";
  }

  private rememberedEffort(menu: ModelMenu): string | undefined {
    const model = this.effectiveModel(menu), catalog = menu.catalog;
    const launching = menu.confirmed === undefined && (catalog?.preferences?.new_session || this.launchModel(menu));
    if (!model || menu.custom || !(launching || menu.draft && menu.draft !== (menu.confirmed || catalog?.selected))) return;
    const level = menu.pickedEfforts?.get(model) ?? catalog?.preferences?.efforts?.[model];
    if (level && catalog?.efforts?.[model]?.options?.some(row => row.id === level)) return level;
  }

  async readyForSend(agent: string | undefined): Promise<boolean> {
    if (!agent) return true;
    const menu = this.menus.get(agent);
    if (!menu || agent !== this.agent) return false;
    while (this.agent === agent && this.menus.get(agent) === menu) {
      const revision = menu.revision;
      if (!menu.catalog && (!menu.custom || menu.draftMode || !this.canEnterModelID(menu))) await menu.metadataRead;
      if (menu.custom && !menu.catalog && !this.canEnterModelID(menu)) await menu.listingRead;
      if (menu.draftMode && !menu.capabilities && !menu.catalog) await menu.listingRead;
      // A live list confirms a remembered model and its reasoning levels.
      const prefs = (menu.catalog ?? menu.capabilities)?.preferences;
      const launchPick = menu.draft === undefined && menu.confirmed === undefined && prefs?.model;
      const model = this.effectiveModel(menu);
      const savedEffort = model ? prefs?.efforts?.[model] || menu.pickedEfforts?.get(model) : prefs?.efforts;
      const checkEffort = (menu.draft || prefs?.new_session) && savedEffort;
      if (!menu.custom && (menu.draftTier || launchPick || checkEffort || !menu.catalog && !menu.capabilities)) {
        await menu.listingRead;
        await this.previewRead;
      }
      if (this.agent !== agent || this.menus.get(agent) !== menu) return false;
      if (menu.revision === revision) {
        this.commitCustom();
        return this.validFor(agent);
      }
    }
    return false;
  }

  configure(scope: string, agent: string, busy: boolean, allowCustomDuringLoad = false): void {
    const changed = scope !== this.scope || agent !== this.agent;
    if (!changed && busy === this.busy && allowCustomDuringLoad === this.allowCustomDuringLoad) return;
    if (scope !== this.scope) {
      this.cancelCatalogs();
      this.menus.clear();
    }
    this.scope = scope;
    this.agent = agent;
    this.busy = busy;
    this.allowCustomDuringLoad = allowCustomDuringLoad;
    const menu = this.menus.get(agent);
    if (changed) {
      if (this.settings) this.settings.open = false;
      this.preview?.abort();
      this.previewError = "";
      const expired = menu?.listedAt !== undefined && Date.now() - menu.listedAt >= catalogLifetimeMs;
      if (agent && !menu?.request && (!menu?.catalog?.models?.length || menu.error || expired || (menu.refreshAfterBind && !busy))) {
        this.refresh();
        return;
      }
      this.previewEffort();
      return;
    }
    if (!busy && menu?.refreshAfterBind && !menu.request) {
      this.refresh();
      return;
    }
    this.render();
  }

  // A newly created conversation gets its ID after the first request. Keep
  // its unsent picks, but fetch session-specific catalogs under the new ID.
  bindDraft(scope: string): void {
    if (this.remember && this.scope) bindAgentDraft(this.scope, scope);
    this.cancelCatalogs();
    this.preview?.abort();
    this.previewError = "";
    this.scope = scope;
    for (const menu of this.menus.values()) menu.refreshAfterBind = true;
    if (this.agent && !this.menus.get(this.agent)?.catalog) this.refresh();
  }

  private cancelCatalogs(): void {
    for (const menu of this.menus.values()) {
      menu.request?.abort();
      menu.request = undefined;
    }
  }

  validFor(responder: string | undefined): boolean {
    if (!responder || responder !== this.agent) return true;
    const menu = this.menus.get(responder);
    const capabilities = menu?.catalog ?? menu?.capabilities;
    if (menu?.draftMode && !this.supportsInteractionMode(responder,menu.draftMode) && (capabilities || menu.error || !menu.request)) {
      this.mode?.setCustomValidity("当前接口未确认支持交互模式，请刷新目录或选择沿用会话模式");
      this.mode?.reportValidity();
      return false;
    }
    if (menu?.custom) {
      if (!this.canEnterModelID(menu)) {
        this.select.setCustomValidity("当前 Agent 接口未提供这个模型，请重新选择模型或刷新目录");
        return this.select.reportValidity();
      }
      this.select.setCustomValidity("");
      this.input.setCustomValidity(this.input.value.trim() ? "" : "请输入模型 ID");
      return this.input.reportValidity();
    }
    const catalog = menu?.catalog;
    const missing = menu?.draft && catalog && !catalog.error && !catalog.allow_custom && !catalog.models?.some(row => row.id === menu.draft);
    const levels = menu && catalog?.efforts?.[this.effectiveModel(menu)];
    const invalidEffort = menu?.draftEffort && levels && !levels.options?.some(row => row.id === menu.draftEffort);
    const invalidTier = menu?.draftTier && this.displayedTier(menu) !== menu.draftTier;
    this.select.setCustomValidity(missing ? "此会话已不提供所选模型，请重新选择"
      : invalidEffort ? "所选模型的推理强度已变化，请重新选择模型或强度"
      : invalidTier ? "所选模型的服务档位已变化，请重新选择档位" : "");
    return this.select.reportValidity();
  }

  selectionFor(responder: string | undefined): string | undefined {
    if (!responder || responder !== this.agent) return;
    const menu = this.menus.get(responder);
    // An effort pick refers to the model whose choices were shown, even if
    // this is the first turn and the model was chosen by the agent's config.
    if (menu) return menu.draft || this.launchModel(menu) || (menu.draftEffort || this.rememberedEffort(menu) ? this.effectiveModel(menu) : undefined);
  }

  effortFor(responder: string | undefined): string | undefined {
    const menu = responder && responder === this.agent && this.menus.get(responder);
    return menu ? (menu.draftEffort ?? this.rememberedEffort(menu)) || undefined : undefined;
  }

  isCompactCommand(agent: string | undefined, query: string, fallback: boolean): boolean {
    return !!agent && /^\/compact(?:\s|$)/i.test(query) && (this.menus.get(agent)?.catalog?.supports_compact ?? fallback);
  }

  turnOptions(selected: string | undefined, responder: string | undefined, compact = false): AgentTurnSelection {
    if (compact) return { agent: responder || selected };
    const tier = responder === "codex" && responder === this.agent ? this.menus.get(responder)?.draftTier : undefined;
    const menu = responder === this.agent && responder ? this.menus.get(responder) : undefined;
    const mode = menu && (menu.draftMode || this.displayedMode(menu));
    return { agent: responder || selected, agent_model: this.selectionFor(responder), agent_effort: this.effortFor(responder),
      ...(mode && { agent_interaction_mode: mode }),
      ...(tier && { agent_service_tier: tier }) };
  }

  // A later turn may run with different defaults before this card is
  // delivered. Send the displayed choice with the card, including its effort.
  queueSelection(agent: string): Omit<AgentTurnSelection, "agent"> & { tier_inherited?: boolean } {
    if (agent !== this.agent) return {};
    const menu = this.menus.get(agent);
    const tier = agent === "codex" && menu ? this.displayedTier(menu) : undefined;
    const mode = menu && this.displayedMode(menu);
    return {
      agent_model: menu && this.effectiveModel(menu) || undefined,
      agent_effort: menu?.draftEffort || (!this.effort.hidden && this.effort.value) || undefined,
      ...(mode && { agent_interaction_mode: mode }),
      ...(tier && { agent_service_tier: tier, ...(!menu?.draftTier && {tier_inherited: true}) }),
    };
  }

  restoreQueuedSelection(agent: string, picks: { model?: string; effort?: string; service_tier?: string; tier_inherited?: boolean; interaction_mode?: string }): void {
    const menu = this.menus.get(agent);
    if (agent !== this.agent || !menu || menu.custom || menu.draft || menu.draftEffort || menu.draftTier || menu.draftMode) return;
    menu.revision = Symbol();
    menu.submittedTurn = undefined;
    menu.draft = picks.model;
    menu.draftEffort = picks.effort;
    menu.draftMode = picks.interaction_mode;
    menu.draftTier = picks.tier_inherited && picks.service_tier === this.displayedTier(menu) ? undefined : picks.service_tier;
    this.saveDraft(menu);
    this.previewEffort();
    this.onSelectionChange?.();
  }

  supportsImages(agent: string): boolean | undefined {
    return this.menus.get(agent)?.catalog?.supports_images;
  }

  draftRevision(agent: string | undefined): symbol | undefined {
    return agent ? this.menus.get(agent)?.revision : undefined;
  }

  submitted(agent: string | undefined, turn: string): void {
    const menu = agent && this.menus.get(agent);
    if (menu && (menu.draft || menu.draftEffort || menu.draftTier || menu.draftMode || menu.custom)) {
      menu.submittedTurn = turn;
      if (this.remember && this.scope && agent) saveModelDraft(this.scope, agent, menu);
    }
  }

  hasNextTurnSelection(agent: string | undefined, sentRevision: symbol | undefined, running?: AgentTurnSelection): boolean {
    const menu = agent && this.menus.get(agent);
    if (!menu || menu.revision === sentRevision) return false;
    if (menu.custom && !menu.draft) return true;
    const current = running?.agent === agent ? running : undefined;
    const model = this.selectionFor(agent);
    return !!(model && model !== current?.agent_model ||
      menu.draftMode && menu.draftMode !== current?.agent_interaction_mode ||
      menu.draftEffort && menu.draftEffort !== current?.agent_effort ||
      menu.draftTier && menu.draftTier !== current?.agent_service_tier);
  }

  confirm(agent?: string, model?: string, effort?: string, command?: string, sentRevision?: symbol, turn?: string,
    tier?: string, preferredTier?: string, mode?: string): void {
    if (command === "compact") return;
    const menu = agent && this.menus.get(agent);
    if (!menu) {
      if (this.remember && this.scope && agent && turn && readAgentDraft(this.scope).models?.[agent]?.submittedTurn === turn) {
        saveModelDraft(this.scope, agent, {});
      }
      return;
    }
    menu.confirmation = Symbol();
    menu.confirmed = model || "";
    menu.confirmedEffort = effort || undefined;
    menu.confirmedTier = tier;
    menu.confirmedMode = mode;
    menu.confirmedPreferredTier = preferredTier;
    // A replay or a previous turn's acknowledgement can update the saved
    // model, but only the request that submitted this draft can consume it.
    if (sentRevision === menu.revision || turn && turn === menu.submittedTurn) {
      menu.draft = undefined;
      menu.draftEffort = undefined;
      menu.draftTier = undefined;
      menu.draftMode = undefined;
      menu.custom = false;
      menu.submittedTurn = undefined;
      if (this.remember && this.scope && agent) saveModelDraft(this.scope, agent, menu);
    }
    this.render();
  }

  private refresh(): void {
    const agent = this.agent;
    const draft = this.remember && this.scope ? readAgentDraft(this.scope).models?.[agent] : undefined;
    const menu: ModelMenu = this.menus.get(agent) || { revision: Symbol(), ...draft };
    this.menus.set(agent, menu);
    menu.request?.abort();
    const request = new AbortController();
    const confirmation = menu.confirmation;
    menu.request = request;
    menu.error = undefined;
    this.render();
    const capabilities = !menu.catalog && !menu.capabilities
      ? this.loadCapabilities?.(agent, request.signal)?.then(capabilities => {
        if (request.signal.aborted) return;
        menu.capabilities = capabilities;
        if (menu.catalog) menu.catalog = withCachedModels(menu.catalog, capabilities);
        if (agent === this.agent) this.render();
      }).catch(() => { /* The full catalog request reports discovery errors. */ }) : undefined;
    const listing = this.load(agent, request.signal).then(catalog => {
      if (request.signal.aborted) return;
      // A failed refresh can still report the bound model and transport.
      // Keep usable choices only within this session and selection mode.
      if (!menu.refreshAfterBind) catalog = withCachedModels(catalog, menu.catalog ?? menu.capabilities);
      menu.refreshAfterBind = false;
      if (!catalog.error && (catalog.selected || catalog.selected_service_tier || catalog.interaction_mode) && menu.confirmation === confirmation) {
        // A completed refresh sees changes made elsewhere. A turn completing
        // after the listing started owns its newer model acknowledgement.
        menu.confirmed = undefined;
        menu.confirmedEffort = undefined;
        menu.confirmedTier = undefined;
        menu.confirmedMode = undefined;
        menu.confirmedPreferredTier = undefined;
      }
      menu.catalog = catalog;
      if (menu.draftTier && !catalog.supports_service_tier) {
        menu.draftTier = undefined;
        if (this.remember && this.scope) saveModelDraft(this.scope, agent, menu);
      }
      if (!catalog.error) menu.listedAt = Date.now();
      menu.error = catalog.error;
      if (menu.custom && !catalog.allow_custom && catalog.models?.some(row => row.id === menu.draft)) {
        menu.custom = false;
        if (this.remember && this.scope) saveModelDraft(this.scope,agent,menu);
      }
      if (agent === this.agent) this.previewEffort();
    }).catch((error: unknown) => {
      if (request.signal.aborted) return;
      menu.error = error instanceof Error ? error.message : String(error);
      if (agent === this.agent) this.render();
    });
    menu.metadataRead = capabilities ?? listing;
    menu.listingRead = listing;
    void Promise.all([capabilities, listing]).then(() => {
      if (menu.request !== request) return;
      menu.request = undefined;
      if (agent === this.agent) this.retry.disabled = false;
    });
  }

  private previewEffort(): void {
    this.preview?.abort();
    this.previewRead = undefined;
    this.previewError = "";
    this.render();
    const menu = this.menus.get(this.agent);
    const catalog = menu?.catalog;
    const model = menu && this.effectiveModel(menu);
    if (!menu || !catalog?.session_scoped_effort || !model || menu.custom ||
      Object.hasOwn(catalog.efforts || {}, model)) return;
    const request = new AbortController();
    this.preview = request;
    this.previewRead = this.load(this.agent, request.signal, model).then(result => {
      if (request.signal.aborted) return;
      if (result.error) throw new Error(result.error);
      const choices = result.efforts?.[model] || { options: [] };
      catalog.efforts = { ...catalog.efforts, [model]: { ...choices, default: result.selected_effort || choices.default } };
      this.render();
    }).catch((error: unknown) => {
      if (request.signal.aborted) return;
      this.previewError = error instanceof Error ? error.message : String(error);
      this.render();
    });
  }

  private displayedTier(menu: ModelMenu, includeDraft = true): string | undefined {
    const catalog = menu.catalog ?? menu.capabilities;
    const reported = menu.confirmed !== undefined ? menu.confirmedTier : catalog?.selected_service_tier;
    const preferred = menu.confirmed !== undefined ? menu.confirmedPreferredTier : catalog?.preferred_service_tier;
    const tier = (includeDraft && menu.draftTier) || preferred || reported || catalog?.default_service_tier;
    const rows = catalog?.service_tiers?.[this.effectiveModel(menu)];
    if (!tier || tier === "default" || !rows || rows.some(row => row.id === tier)) return tier;
    const listed = Object.values(catalog?.service_tiers || {}).some(choices => choices.some(row => row.id === tier));
    return !listed && (tier === reported || tier === catalog?.default_service_tier) ? tier : "default";
  }

  private renderServiceTier(menu?: ModelMenu): void {
    if (!this.tier) return;
    const catalog = menu?.catalog ?? menu?.capabilities;
    const rows = menu && catalog?.service_tiers?.[this.effectiveModel(menu)] || [];
    const value = menu && this.displayedTier(menu);
    const inherited = menu?.draftTier ? this.displayedTier(menu, false) : value;
    this.tier.hidden = this.agent !== "codex" || !catalog?.supports_service_tier || !rows.length && (!value || value === "default") && !menu?.draftTier;
    if (this.tier.hidden) return;
    this.tier.replaceChildren();
    if (!value) this.tier.add(new Option("沿用 Codex 档位", ""));
    this.tier.add(new Option("标准速度", "default"));
    for (const row of rows) {
      if (row.id === "default") continue;
      const option = new Option(row.name, row.id);
      option.title = row.description || "";
      this.tier.add(option);
    }
    if (value && value !== "default" && !rows.some(row => row.id === value)) this.tier.add(new Option(value, value));
    if (inherited && inherited !== "default" && inherited !== value && !rows.some(row => row.id === inherited)) {
      this.tier.add(new Option(inherited, inherited));
    }
    if (menu?.draftTier && menu.draftTier !== value) {
      this.tier.add(new Option(menu.draftTier + "（已不可用）", menu.draftTier));
    }
    this.tier.value = menu?.draftTier || value || "";
  }

  private displayedMode(menu: ModelMenu): string | undefined {
    const catalog = menu.catalog ?? menu.capabilities;
    if (!catalog?.supports_interaction_mode) return;
    const saved = menu.confirmedMode ?? catalog.interaction_mode;
    return menu.draftMode ?? (saved && this.supportsInteractionMode(this.agent,saved) ? saved : undefined);
  }

  supportsInteractionMode(agent?: string, mode?: string): boolean | undefined {
    const menu = agent && this.menus.get(agent);
    const catalog = menu && (menu.catalog ?? menu.capabilities);
    return catalog ? !!catalog.supports_interaction_mode && (!mode || !catalog.interaction_modes || catalog.interaction_modes.includes(mode)) : undefined;
  }

  async pickPlanCommand(agent: string | undefined, query: string, current: () => boolean): Promise<"applied" | "unknown" | "unavailable" | "stale" | undefined> {
    if ((agent !== "codex" && agent !== "claude") || query.trim() !== "/plan") return;
    const menu = this.menus.get(agent);
    if (this.agent !== agent || !menu) return "unknown";
    if (!menu.catalog && !menu.capabilities) await menu.metadataRead;
    if (!menu.catalog && !menu.capabilities) await menu.listingRead;
    if (!current() || this.agent !== agent || this.menus.get(agent) !== menu) return "stale";
    const supported = this.supportsInteractionMode(agent,"plan");
    if (supported === undefined) return "unknown";
    if (supported && this.selectInteractionMode(agent, "plan")) return "applied";
    if (Array.isArray((menu.catalog ?? menu.capabilities)?.interaction_modes)) return "unavailable";
  }

  selectInteractionMode(agent: string, value: string): boolean {
    const menu = this.menus.get(agent);
    if (!menu || this.agent !== agent || value && (value !== "default" && value !== "plan" || !this.supportsInteractionMode(agent,value))) return false;
    menu.revision = Symbol();
    menu.submittedTurn = undefined;
    menu.draftMode = value || undefined;
    this.saveDraft(menu);
    this.render();
    this.onSelectionChange?.();
    return true;
  }

  private renderMode(menu?: ModelMenu): void {
    if (!this.mode) return;
    const supported = (menu?.catalog ?? menu?.capabilities)?.supports_interaction_mode;
    this.mode.hidden = !supported && !menu?.draftMode;
    this.mode.setCustomValidity("");
    if (this.mode.hidden) return;
    this.mode.replaceChildren(new Option("沿用会话模式", ""));
    if (this.supportsInteractionMode(this.agent,"default")) this.mode.add(new Option("执行", "default"));
    if (this.supportsInteractionMode(this.agent,"plan")) this.mode.add(new Option("规划", "plan"));
    const value = menu?.draftMode || (menu && this.displayedMode(menu)) || "";
    if (value && !this.supportsInteractionMode(this.agent,value)) this.mode.add(new Option((value === "plan" ? "规划" : "执行") + "（待确认）", value));
    this.mode.value = value;
  }

  private updateModelName(menu?: ModelMenu): void {
    if (!this.modelName) return;
    const name = menu?.custom ? menu.draft || "自定义模型" : this.select.selectedOptions[0]?.text || "模型";
    this.modelName.textContent = name;
    this.modelName.title = name;
  }

  private render(): void {
    this.controls.hidden = !this.agent;
    if (!this.agent) return;
    const menu = this.menus.get(this.agent);
    this.renderServiceTier(menu);
    this.renderMode(menu);
    const loading = !menu?.catalog && !menu?.error;
    const catalog = menu?.catalog ?? menu?.capabilities;
    const models = catalog?.models || [];
    const effective = menu ? this.effectiveModel(menu) : "";
    const caps = catalog?.efforts?.[effective];
    const levels = menu?.custom ? [] : caps?.options || [];
    this.effort.hidden = !levels.length;
    this.effort.disabled = !levels.length;
    this.effort.replaceChildren(new Option("沿用会话强度", ""));
    for (const level of levels) this.effort.add(new Option("推理 · " + level.name, level.id));
    const activeModel = menu?.confirmed || catalog?.selected || catalog?.default_model || "";
    const saved = menu?.confirmed !== undefined ? menu.confirmedEffort : catalog?.selected_effort;
    const value = menu?.draftEffort ?? ((menu && this.rememberedEffort(menu)) || (effective === activeModel ? saved : "") || caps?.default);
    this.effort.value = levels.some(level => level.id === value) ? value! : "";
    const emptyLabel = catalog?.allow_custom ? "Agent 未列出模型，可输入模型 ID" : "Agent 未提供可切换的模型";
    const label = loading && !models.length ? "读取模型中…" : menu?.error ? "模型列表读取失败"
      : !models.length ? emptyLabel : "沿用会话模型";
    const placeholder = new Option(label, "");
    this.select.replaceChildren(placeholder);
    for (const option of models) this.select.add(new Option(option.name || option.id, option.id));
    if (effective && !menu?.custom && !models.some(m => m.id === effective)) this.select.add(new Option(effective, effective));
    const allowCustom = this.canEnterModelID(menu);
    if (allowCustom) {
      this.select.add(new Option("输入模型 ID…", customValue));
    } else if (menu?.custom) {
      const draftLabel = menu.draft || "自定义模型";
      this.select.add(new Option(draftLabel + (loading ? "（待确认）" : "（请重新选择）"), customValue));
    }
    this.select.value = menu?.custom ? customValue : effective;
    this.updateModelName(menu);
    this.select.disabled = this.select.length <= 1;
    this.select.title = menu?.error || (loading && models.length ? "正在刷新模型目录，可先选择模型" : this.agent + " 模型 · 下一条消息生效");
    this.select.setAttribute("aria-label", this.agent + " 模型");
    this.retry.hidden = loading && !this.previewError;
    this.retry.disabled = !!menu?.request && !this.previewError;
    this.retry.textContent = menu?.error || this.previewError || !models.length ? "重试" : "刷新";
    this.retry.title = this.previewError || menu?.error || "重新读取模型列表";
    this.input.hidden = !menu?.custom || !allowCustom;
    // Avoid resetting the caret during a catalog refresh while typing an ID.
    if (this.input.value.trim() !== (menu?.draft || "")) this.input.value = menu?.draft || "";
  }
}
