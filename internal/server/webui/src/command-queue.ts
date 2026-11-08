import { APIClient, isCancellation } from "./api-client";
import { errorMessage } from "./api";
import { readChatDraft, saveChatDraft } from "./turn-replay";
import { builtinAgent } from "./single-agent";
import { QueueEditor } from "./queue-editor";
import { QueueUploadCache } from "./queue-upload-cache";
import type { SentAttachments } from "./attachments";

export interface QueueEntry {
  id: string;
  parent: string;
  agent: string;
  text: string;
  state: string;
  error?: string;
  kind?: "message";
  selection?: { model?: string; effort?: string; service_tier?: string; tier_inherited?: boolean; interaction_mode?: string };
  turn_id?: string;
  body_version?: number;
  editing?: boolean;
  attachments?: { kind: "image" | "file"; mime_type?: string; name?: string; path?: string; size: number }[];
}
type Entry = QueueEntry;

export interface CommandQueueSnapshot {
  session_id: string;
  revision: number;
  paused: boolean;
  items: Entry[];
  receipt?: Entry;
  turn_id?: string;
  operation_committed?: boolean;
  submission_id?: string;
}

interface Submission {
  id: string;
  parent: string;
  session: string;
  agent: string;
  text: string;
  draft: string;
  kind?: "message";
  agent_model?: string;
  agent_effort?: string;
  agent_service_tier?: string;
  agent_interaction_mode?: string;
  tier_inherited?: boolean;
  mediaIDs?: string[];
}

interface Composer {
  turn(): string;
  draft(): string;
  setDraft(text: string): void;
  chooseBuiltin?(): void;
  restoreSelection?(agent: string, picks: NonNullable<Entry["selection"]>): void;
  notify(text: string): void;
  newID(): string;
  watch(id: string): void;
  mediaIDs?(): string[];
  captureMedia?(): SentAttachments;
  removeMedia?(ids: string[]): void;
  restoreMedia?(payload: Partial<SentAttachments>): void;
}

const endpoint = "/api/v1/webui/command-queue";
const storagePrefix = "bqagent.web-command.";
const stateLabels: Record<string, string> = {
  submitted: "已优先提交 · 等待新回合", steering: "已提交本轮 · 等待确认", returned: "Agent 已退回",
  running: "正在执行", unknown: "执行结果未确认", failed: "回合报错", stopped: "已停止", withdrawn: "已撤回 · 待取回",
};

function addressedText(row: Pick<Entry, "agent" | "text">): string {
  const prefix = "/" + row.agent;
  return row.text.split(/\s/, 1)[0].toLowerCase() === prefix ? row.text : prefix + " " + row.text;
}

export class CommandQueue {
  private workspace = "";
  private session = "";
  private revision = -1;
  private hasWork = false;
  private poll?: number;
  private inFlight = new Set<string>();
  private pending = new Map<string, Submission[]>();
  private readonly cards = document.createElement("div");
  private readonly editor: QueueEditor;
  private working = false;
  private stopping = false;
  private waitingForAnswer = false;
  private sendIDs = new Map<string,string>();
  private readonly moveIDs = new Map<string, string>();

  constructor(private readonly root: HTMLElement, private readonly api: APIClient, private readonly composer: Composer,
    private readonly uploads = new QueueUploadCache()) {
    this.cards.className = "queued-cards";
    this.editor = new QueueEditor({
      newID: () => composer.newID(),
      request: (workspace, session, fields) => this.mutate(workspace, session, fields),
      refresh: () => this.refresh(),
      changed: () => this.updateVisibility(),
      restore: (row, added) => {
        if (!added) return this.restoreDraft(row);
        if (this.composer.draft().trim() || this.composer.mediaIDs?.().length || !this.composer.restoreMedia) return false;
        this.composer.restoreMedia(added);
        return this.restoreDraft(row,true);
      },
      confirmed: async (workspace, session, id, operation) => {
        const v = await this.api.json<CommandQueueSnapshot>(this.url(workspace,session,id)+"&operation_id="+encodeURIComponent(operation));
        this.apply(workspace,v);
        return v.operation_committed === true;
      },
    }, this.uploads);
    root.replaceChildren(this.cards, this.editor.root);
  }

  private updateVisibility(): void {
    this.root.hidden = !this.editor.open && this.cards.childElementCount === 0;
  }

  show(workspace: string, session: string): void {
    if (this.workspace === workspace && this.session === session) return;
    this.workspace = workspace;
    this.session = session;
    this.revision = -1;
    this.hasWork = false;
    window.clearTimeout(this.poll);
    this.cards.replaceChildren();
    this.moveIDs.clear();
    this.editor.show(workspace, session);
    this.updateVisibility();
    if (session) {
      for (const op of this.unresolved(workspace)) {
        if (!op.session && op.parent === this.composer.turn()) op.session = session;
      }
      this.store(workspace);
    }
    if (session) void this.refresh();
  }

  private unresolved(workspace: string): Submission[] {
    const known = this.pending.get(workspace);
    if (known) return known;
    let rows: Submission[] = [];
    try {
      const stored = JSON.parse(sessionStorage.getItem(storagePrefix + workspace) || "[]");
      if (Array.isArray(stored)) rows = stored.filter(op => op && typeof op.id === "string" && typeof op.draft === "string");
    } catch { /* The operation IDs remain available in memory when storage is disabled. */ }
    this.pending.set(workspace, rows);
    return rows;
  }

  private store(workspace: string): void {
    const rows = this.unresolved(workspace);
    try {
      if (rows.length) sessionStorage.setItem(storagePrefix + workspace, JSON.stringify(rows));
      else sessionStorage.removeItem(storagePrefix + workspace);
    } catch { /* In-memory retries still use the same ID. */ }
  }

  private forget(workspace: string, id: string): void {
    if (this.unresolved(workspace).some(op => op.id === id && op.mediaIDs?.length)) this.uploads.remove(JSON.stringify([workspace,id]));
    this.pending.set(workspace, this.unresolved(workspace).filter(op => op.id !== id));
    this.store(workspace);
  }

  private accepted(workspace: string, session: string, row: Entry): void {
    const op = this.unresolved(workspace).find(op => op.id === row.id && op.parent === row.parent);
    if (!op) return;
    this.forget(workspace, op.id);
    const sameComposer = this.workspace === workspace && (this.session === session || this.composer.turn() === op.parent);
    const newerMedia = sameComposer && this.composer.mediaIDs?.().some(id => !op.mediaIDs?.includes(id));
    if (sameComposer && op.mediaIDs) this.composer.removeMedia?.(op.mediaIDs);
    if (sameComposer && !newerMedia && this.composer.draft() === op.draft) this.composer.setDraft("");
    for (const key of [session, "turn:" + op.parent]) {
      if (!newerMedia && readChatDraft(workspace, key) === op.draft) saveChatDraft(workspace, key, "");
    }
  }

  apply(workspace: string, view: CommandQueueSnapshot): void {
    if (view.receipt) this.accepted(workspace, view.session_id, view.receipt);
    for (const item of view.items) this.accepted(workspace, view.session_id, item);
    if (workspace !== this.workspace || view.session_id !== this.session || view.revision <= this.revision) return;
    this.revision = view.revision;
    this.cards.replaceChildren();
    const waiting = view.items.filter(item => item.state === "waiting");
    this.editor.sync(view.items);
    for (const item of view.items) {
      const row = document.createElement("div");
      row.className = "turn-input-queued";
      const state = document.createElement("span"), text = document.createElement("span");
      state.className = "queued-state";
      text.className = "queued-content";
      const choices = item.selection;
      state.textContent = [stateLabels[item.state] || (item.editing ? "编辑中" : view.paused ? "已暂停" : "已排队"),
        choices?.interaction_mode === "plan" ? "规划" : choices?.interaction_mode === "default" ? "执行" : undefined,
        choices?.model, choices?.effort, choices?.service_tier === "default" ? "标准速度" : choices?.service_tier].filter(Boolean).join(" · ");
      text.textContent = item.agent === builtinAgent ? builtinAgent + " · " + item.text : addressedText(item);
      row.append(state, text);
      if (item.attachments?.length) {
        const files = document.createElement("div");
        files.className = "queued-attachments";
        item.attachments.forEach((asset, index) => {
          const link = document.createElement("a");
          link.href = this.url(workspace, view.session_id, item.id) + "&view=attachment&index=" + index;
          link.target = "_blank";
          link.rel = "noopener";
          link.textContent = asset.kind === "image" ? "图片 " + (index + 1) : asset.name || "文件";
          link.title = [asset.path || asset.mime_type, Math.ceil(asset.size / 1024) + " KiB"].filter(Boolean).join(" · ");
          files.appendChild(link);
        });
        row.appendChild(files);
      }
      if (item.error) {
        const reason = document.createElement("span");
        reason.className = "queued-error";
        reason.textContent = item.error;
        row.appendChild(reason);
      }
      if (!["running", "steering", "submitted"].includes(item.state)) {
        const actions = document.createElement("div");
        actions.className = "queued-actions";
        if (["waiting", "returned", "failed"].includes(item.state)) {
          const send = this.button(actions, item.state === "waiting" ? "立即发送" : "重新发送", () => this.sendNow(item));
          send.dataset.sendCard = item.kind || "compact";
          this.updateSendButton(send);
        }
        if (item.state === "waiting") {
          if (item.kind === "message" || item.agent !== "codex") {
            this.button(actions, "编辑", () => this.editor.begin(item));
          }
          const index = waiting.indexOf(item);
          if (index > 0) this.button(actions, "上移", () => this.move(item.id, waiting[index - 1].id, "before"));
          if (index + 1 < waiting.length) this.button(actions, "下移", () => this.move(item.id, waiting[index + 1].id, "after"));
        }
        if (item.state === "withdrawn") {
          this.button(actions, "放回输入框", () => this.takeBack(item));
          this.button(actions, "移除", () => this.releaseAttachments(item));
        } else this.button(actions, item.state === "waiting" ? "撤回" : "移除", () => this.withdraw(item));
        row.appendChild(actions);
      }
      this.cards.appendChild(row);
    }
    if (view.paused && view.items.some(row => row.state === "waiting")) {
      this.button(this.cards, "继续队列", () => this.resume());
    }
    this.updateVisibility();
    this.hasWork = view.items.some(row => ["waiting","running","withdrawn","steering","submitted","returned"].includes(row.state));
    this.schedulePoll();
  }

  private button(host: HTMLElement, label: string, act: () => Promise<void> | void): HTMLButtonElement {
    const button = document.createElement("button");
    button.type = "button";
    button.className = "turn-input-btn";
    button.textContent = label;
    button.onclick = async () => {
      button.disabled = true;
      button.dataset.sending = "true";
      try { await act(); } finally {
        delete button.dataset.sending;
        button.disabled = false;
        if (button.dataset.sendCard) this.updateSendButton(button);
      }
    };
    host.appendChild(button);
    return button;
  }

  setSendActivity(working: boolean, waitingForAnswer: boolean): void {
    this.working = working;
    this.waitingForAnswer = waitingForAnswer;
    if (!working) this.stopping = false;
    this.refreshSendButtons();
  }

  setStopping(stopping: boolean): void {
    this.stopping = stopping;
    this.refreshSendButtons();
  }

  private refreshSendButtons(): void {
    this.cards.querySelectorAll<HTMLButtonElement>("button[data-send-card]").forEach(button => this.updateSendButton(button));
  }

  private updateSendButton(button: HTMLButtonElement): void {
    const reason = this.stopping ? "正在停止当前回合" : this.waitingForAnswer ? "请先回答 Agent 的请求" : this.working && button.dataset.sendCard === "compact" ? "当前回合结束后可以发送命令" : "";
    button.disabled = !!reason || !!button.dataset.sending;
    button.title = reason || "优先发送这条消息";
  }

  private async sendNow(item: Entry): Promise<void> {
    const workspace = this.workspace, session = this.session;
    const version = item.body_version || 0, previous = item.turn_id || "";
    const key = "bqagent.queue-send." + JSON.stringify([workspace,session,item.id,version,previous]);
    let operation = this.sendIDs.get(key);
    try { operation ||= sessionStorage.getItem(key) || undefined; } catch { /* In-memory retries remain available. */ }
    operation ||= this.composer.newID();
    this.sendIDs.set(key,operation);
    try { sessionStorage.setItem(key,operation); } catch { /* Storage may be disabled. */ }
    try {
      await this.mutate(workspace,session,{ action:"send_now", id:item.id, operation_id:operation, body_version:version, expected_turn_id:previous });
    } catch (error) {
      this.report(error,workspace,session);
      if (workspace === this.workspace && session === this.session) await this.refresh();
      return;
    }
    this.sendIDs.delete(key);
    try { sessionStorage.removeItem(key); } catch { /* Optional storage. */ }
  }

  private url(workspace: string, session: string, id = ""): string {
    return endpoint + "?" + new URLSearchParams({ workspace_id: workspace, session_id: session, id });
  }

  afterTurn(): void {
    if (this.hasWork && !document.hidden && !this.composer.turn()) void this.refresh();
  }

  private schedulePoll(): void {
    window.clearTimeout(this.poll);
    // Idle tabs have no SSE reader for another tab's Resume or withdrawal.
    if (this.hasWork && !document.hidden && !this.composer.turn()) {
      this.poll = window.setTimeout(() => { if (!document.hidden) void this.refresh(); }, 5000);
    }
  }

  async refresh(): Promise<void> {
    const workspace = this.workspace, session = this.session;
    if (!session) return;
    const pending = this.unresolved(workspace).filter(op => !op.session || op.session === session);
    try {
      const ids = pending.length ? pending.map(op => op.id) : [""];
      const views = await Promise.all(ids.map(id => this.api.json<CommandQueueSnapshot>(this.url(workspace, session, id))));
      for (const view of views) this.apply(workspace, view);
      const latest = views.reduce((a, b) => a.revision > b.revision ? a : b);
      if (workspace === this.workspace && session === this.session) this.schedulePoll();
      if (latest.turn_id && latest.revision === this.revision && workspace === this.workspace && session === this.session && !this.composer.turn()) {
        this.composer.watch(latest.turn_id);
      }
    } catch (error) {
      this.report(error, workspace, session);
      if (workspace === this.workspace && session === this.session && !isCancellation(error)) this.schedulePoll();
    }
  }

  // Recover a lost acknowledgement before Send can turn the same draft into
  // a fresh direct /compact after the parent finishes.
  async reconcileDraft(): Promise<boolean> {
    const workspace = this.workspace, session = this.session, draft = this.composer.draft();
    const op = this.unresolved(workspace).find(op => (!op.session || op.session === session) && op.draft === draft);
    if (!op) return true;
    let checkedSession = false;
    try {
      if (session) {
        const view = await this.api.json<CommandQueueSnapshot>(this.url(workspace, session, op.id));
        this.apply(workspace, view);
        if (view.receipt) return false;
        checkedSession = true;
      }
      if (this.workspace !== workspace || this.session !== session || this.composer.draft() !== draft) return false;
      // An empty snapshot can race the original POST. Replay that operation
      // ID to establish whether it was accepted before issuing a fresh send.
      const view = await this.post(workspace, op);
      this.apply(workspace, view);
      return !!session && view.session_id !== session && this.session === session && this.workspace === workspace && this.composer.draft() === draft;
    } catch (error) {
      const status = (error as { status?: number }).status;
      if ((checkedSession || !session) && (status === 404 || status === 410)) {
        if (op.mediaIDs?.length && !this.composer.mediaIDs?.().length && this.workspace === workspace && this.session === session && this.composer.draft() === draft) {
          const saved = await this.uploads.get(JSON.stringify([workspace, op.id]));
          if (!saved || !this.composer.restoreMedia) {
            this.composer.notify("这条消息未入队，本地附件已不可用。请重新添加附件后发送。");
            this.forget(workspace, op.id);
            return false;
          }
          if (this.workspace !== workspace || this.session !== session || this.composer.draft() !== draft) return false;
          if (!this.composer.mediaIDs?.().length) {
            try { this.composer.restoreMedia(saved); }
            catch (error) { this.report(error, workspace, session); return false; }
          }
        }
        this.forget(workspace, op.id);
        return this.workspace === workspace && this.session === session && this.composer.draft() === draft;
      }
      this.report(error, workspace, session);
      return false;
    }
  }

  async enqueue(agent: string, text: string, options?: Pick<Submission, "agent_model" | "agent_effort" | "agent_service_tier" | "agent_interaction_mode" | "tier_inherited">): Promise<void> {
    const parent = this.composer.turn(), draft = this.composer.draft();
    const workspace = this.workspace, session = this.session;
    if (!parent) return;
    const ids = this.composer.mediaIDs?.() || [];
    let media: SentAttachments | undefined;
    if (ids.length) {
      try {
        media = this.composer.captureMedia?.();
        if (!media || media.images.length + media.files.length !== ids.length) throw new Error("附件尚未读取完成。");
      } catch (error) { this.report(error, workspace, session); return; }
    }
    let op = this.unresolved(workspace).find(row => row.parent === parent && row.agent === agent && row.text === text && row.draft === draft &&
      JSON.stringify(row.mediaIDs || []) === JSON.stringify(ids));
    if (!op) {
      op = { id: this.composer.newID(), parent, agent, text, draft, session, ...(options && { kind: "message", ...options }), ...(ids.length ? { mediaIDs: ids } : {}) };
      this.unresolved(workspace).push(op);
      this.store(workspace);
    }
    if (this.inFlight.has(op.id)) return;
    this.inFlight.add(op.id);
    try {
      if (media) await this.uploads.put(JSON.stringify([workspace,op.id]), media);
      if (!this.unresolved(workspace).includes(op)) return;
      const view = await this.post(workspace, op);
      this.apply(workspace, view);
    } catch (error) {
      const status = (error as { status?: number }).status;
      if (status === 400 || status === 404 || status === 410 || status === 413) this.forget(workspace, op.id);
      if (this.workspace === workspace && (this.composer.turn() === parent || (!!session && this.session === session)) && !isCancellation(error)) this.composer.notify(errorMessage(error));
    } finally { this.inFlight.delete(op.id); }
  }

  private async post(workspace: string, op: Submission): Promise<CommandQueueSnapshot> {
    let media: SentAttachments | undefined;
    if (op.mediaIDs?.length) {
      media = await this.uploads.get(JSON.stringify([workspace,op.id]));
      if (!media && this.workspace === workspace && (this.session === op.session || this.composer.turn() === op.parent) &&
        this.composer.draft() === op.draft && this.composer.mediaIDs?.().length) media = this.composer.captureMedia?.();
      if (!media || !(media.images.length + media.files.length)) throw new Error("待确认的消息包含附件，请重新添加附件后发送。");
    }
    return this.api.json<CommandQueueSnapshot>(endpoint, {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ action: "enqueue", workspace_id: workspace, session_id: op.session, parent_turn_id: op.parent,
        id: op.id, agent: op.agent, text: op.text, kind: op.kind, agent_model: op.agent_model, agent_effort: op.agent_effort,
        agent_service_tier: op.agent_service_tier, agent_interaction_mode: op.agent_interaction_mode, tier_inherited: op.tier_inherited, ...media }),
    });
  }

  private async withdraw(row: Entry): Promise<void> {
    const workspace = this.workspace, session = this.session;
    try {
      const view = await this.api.json<CommandQueueSnapshot>(this.url(workspace, session, row.id) + "&body_version=" + (row.body_version || 0), { method: "DELETE" });
      this.apply(workspace, view);
      if (this.workspace === workspace && this.session === session && row.state === "waiting") {
        if (view.receipt?.state === "withdrawn" && view.receipt.attachments?.length) await this.takeBack(view.receipt);
        else this.restoreDraft(row);
      }
    } catch (error) { this.report(error, workspace, session); await this.refresh(); }
  }

  private async releaseAttachments(row: Entry): Promise<void> {
    const workspace = this.workspace, session = this.session;
    try { await this.mutate(workspace, session, { action: "release_attachments", id: row.id, body_version: row.body_version || 0 }); }
    catch (error) { this.report(error, workspace, session); }
  }

  private async takeBack(row: Entry): Promise<void> {
    const workspace = this.workspace, session = this.session;
    const occupied = () => !!this.composer.draft().trim() || !!this.composer.mediaIDs?.().length;
    if (occupied()) { this.composer.notify("输入框已有草稿。撤回的附件仍可在队列中取回。"); return; }
    try {
      const payload = await this.api.json<Partial<SentAttachments>>(this.url(workspace,session,row.id) + "&view=attachments");
      if (this.workspace !== workspace || this.session !== session || occupied() || !this.composer.restoreMedia) return;
      this.composer.restoreMedia(payload);
      this.restoreDraft(row, true);
      await this.releaseAttachments(row);
    } catch (error) { this.report(error, workspace, session); if (this.workspace === workspace && this.session === session) await this.refresh(); }
  }

  private restoreDraft(row: Pick<Entry, "agent" | "text" | "selection">, restoringMedia = false): boolean {
    if (this.composer.draft().trim() || (!restoringMedia && this.composer.mediaIDs?.().length)) return false;
    if (row.agent === builtinAgent) this.composer.chooseBuiltin?.();
    this.composer.setDraft(row.agent === builtinAgent ? row.text : addressedText(row));
    if (row.selection) this.composer.restoreSelection?.(row.agent, row.selection);
    return true;
  }

  private async resume(): Promise<void> {
    const workspace = this.workspace, session = this.session;
    try {
      await this.mutate(workspace, session, { action: "resume" });
    } catch (error) { this.report(error, workspace, session); }
  }

  private async mutate(workspace: string, session: string, fields: Record<string, unknown>): Promise<void> {
    const view = await this.api.json<CommandQueueSnapshot>(endpoint, {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ ...fields, workspace_id: workspace, session_id: session }),
    });
    this.apply(workspace, view);
    if (view.turn_id && view.revision === this.revision && workspace === this.workspace && session === this.session && !this.composer.turn()) this.composer.watch(view.turn_id);
  }

  private async move(id: string, anchor: string, position: "before" | "after"): Promise<void> {
    const workspace = this.workspace, session = this.session;
    const key = JSON.stringify([workspace, session, id, anchor, position]);
    const operation = this.moveIDs.get(key) || this.composer.newID();
    this.moveIDs.set(key, operation);
    try {
      await this.mutate(workspace, session, { action: "move", id, anchor_id: anchor, position, operation_id: operation });
      this.moveIDs.delete(key);
    } catch (error) {
      const status = (error as { status?: number }).status;
      if (status && status < 500) this.moveIDs.delete(key);
      this.report(error, workspace, session);
      if (workspace === this.workspace && session === this.session) await this.refresh();
    }
  }
  reset(): void {
    this.uploads.clear();
    this.pending.clear();
    this.sendIDs.clear();
    this.show("", "");
  }

  private report(error: unknown, workspace: string, session: string): void {
    if (!isCancellation(error) && workspace === this.workspace && session === this.session) this.composer.notify(errorMessage(error));
  }
}
