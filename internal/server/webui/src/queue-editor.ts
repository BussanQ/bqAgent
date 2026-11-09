import { errorMessage } from "./api";
import { APIError, isCancellation } from "./api-client";
import type { QueueEntry } from "./command-queue";
import { editMedia, QueueEditorMedia, type EditMedia } from "./queue-editor-media";
import type { QueueUploadCache } from "./queue-upload-cache";
import type { SentAttachments } from "./attachments";

type EditAction = "hold" | "renew" | "release" | "edit";
type Phase = "opening" | "held" | "lost" | "saving" | "uncertain";

interface Draft {
  id: string;
  agent: string;
  token: string;
  version: number;
  text: string;
  operation?: string;
  selection?: QueueEntry["selection"];
  media?: EditMedia;
}

interface EditorHost {
  newID(): string;
  request(workspace: string, session: string, fields: Record<string, unknown>): Promise<void>;
  refresh(): Promise<void>;
  changed(): void;
  restore(draft: Pick<Draft, "agent" | "text" | "selection">, added?: SentAttachments): boolean;
  confirmed(workspace:string, session:string, id:string, operation:string): Promise<boolean>;
}

const storageKey = (workspace: string, session: string) => "bqagent.web-command.edit." + JSON.stringify([workspace, session]);

function storedDraft(key: string): Draft | undefined {
  try {
    const draft = JSON.parse(sessionStorage.getItem(key) || "null");
    if (draft && typeof draft.id === "string" && typeof draft.token === "string" && typeof draft.agent === "string" &&
      Number.isSafeInteger(draft.version) && typeof draft.text === "string") return draft;
  } catch { /* The open editor still works without browser storage. */ }
}

// Keep the textarea mounted across queue snapshots so focus, selection and
// unsaved text survive SSE updates, including edits from another tab.
export class QueueEditor {
  readonly root = document.createElement("div");
  private readonly label = document.createElement("label");
  private readonly input = document.createElement("textarea");
  private readonly status = document.createElement("div");
  private readonly saveButton = document.createElement("button");
  private readonly retryButton = document.createElement("button");
  private readonly cancelButton = document.createElement("button");
  private readonly restoreButton = document.createElement("button");
  private readonly media: QueueEditorMedia;
  private workspace = "";
  private session = "";
  private draft?: Draft;
  private latest?: QueueEntry;
  private phase: Phase = "lost";
  private recoverHold = false;
  private snapshotSeen = false;
  private renewTimer?: number;

  constructor(private readonly host: EditorHost, private readonly cache: QueueUploadCache) {
    this.root.className = "queue-editor";
    this.root.hidden = true;
    this.input.id = "queue-edit-text";
    this.label.htmlFor = this.input.id;
    this.input.rows = 3;
    this.status.setAttribute("role", "status");
    const actions = document.createElement("div");
    actions.className = "queued-actions";
    for (const button of [this.saveButton, this.retryButton, this.restoreButton, this.cancelButton]) {
      button.type = "button";
      button.className = "turn-input-btn";
      actions.appendChild(button);
    }
    this.cancelButton.textContent = "取消";
    this.retryButton.textContent = "按最新版本继续编辑";
    this.restoreButton.textContent = "放回输入框";
    this.media = new QueueEditorMedia(cache, () => host.newID(), () => {
      this.persist();
      this.buttons();
    }, message => { this.status.textContent = message; });
    this.root.append(this.label, this.input, this.media.root, this.status, actions);
    this.input.addEventListener("input", () => {
      if (this.draft) { this.draft.text = this.input.value; this.persist(); }
    });
    this.input.addEventListener("keydown", event => {
      if (event.isComposing) return;
      if (event.key === "Escape" && !this.cancelButton.disabled) { event.preventDefault(); this.close(); }
      if (event.key === "Enter" && (event.ctrlKey || event.metaKey) && !this.saveButton.disabled) {
        event.preventDefault(); void this.save();
      }
    });
    this.saveButton.onclick = () => { void this.save(); };
    this.retryButton.onclick = () => {
      if (this.draft && this.latest?.state === "waiting") {
        this.reacquire(this.draft, this.latest.body_version || 0);
      }
    };
    this.restoreButton.onclick = async () => {
      const draft = this.draft;
      if (!draft) return;
      try {
        const added = draft.media?.added.length ? await this.cache.get(draft.media.key) : undefined;
        if (this.draft !== draft) return;
        if (draft.media?.added.length && !added) { this.status.textContent = "本地新增附件无法读取。文字仍可复制。"; return; }
        if (this.host.restore(draft, added)) this.close();
        else this.status.textContent = "输入框里已有草稿。可以复制此处的文字，或清空输入框后放回。";
      } catch (error) { if (this.draft === draft) this.status.textContent = errorMessage(error); }
    };
    this.cancelButton.onclick = () => this.close();
  }

  get open(): boolean { return !!this.draft; }

  show(workspace: string, session: string): void {
    if (workspace === this.workspace && session === this.session) return;
    this.detach(false);
    this.workspace = workspace;
    this.session = session;
    this.snapshotSeen = false;
    this.recoverHold = false;
    const saved = session && storedDraft(storageKey(workspace, session));
    if (saved) {
      this.mount(saved);
      this.recoverHold = !saved.operation;
      this.setPhase(saved.operation ? "uncertain" : "lost", saved.operation ? "上次保存结果待确认，可以重试保存。" : "已恢复未保存的文字。");
      if (saved.operation) void this.save();
    }
  }

  begin(row: QueueEntry): void {
    if (this.draft) { this.input.focus(); return; }
    this.latest = row;
    this.snapshotSeen = true;
    const token = this.host.newID();
    const draft: Draft = { id: row.id, agent: row.agent, token, text: row.text, version: row.body_version || 0, selection: row.selection };
    if (row.kind === "message") draft.media = editMedia(row.attachments || [], JSON.stringify(["edit",this.workspace,this.session,row.id,token]));
    this.mount(draft);
    this.persist();
    void this.acquire(draft);
  }

  sync(rows: QueueEntry[]): void {
    const draft = this.draft;
    if (!draft) return;
    this.snapshotSeen = true;
    this.latest = rows.find(row => row.id === draft.id);
    if (this.recoverHold) {
      this.recoverHold = false;
      if (this.latest?.state === "waiting" && (this.latest.body_version || 0) === draft.version) {
        this.reacquire(draft, draft.version);
        return;
      }
    }
    if (this.phase === "saving" || this.phase === "uncertain" || this.phase === "opening") return;
    if (!this.latest || this.latest.state !== "waiting") {
      this.setPhase("lost", "这条消息已离开等待队列。可取回本地文字和新增附件。");
    } else if ((this.latest.body_version || 0) !== draft.version) {
      const assets = (this.latest.attachments || []).map(asset => asset.path || asset.name || asset.mime_type);
      this.setPhase("lost", "另一页面已保存修改。当前队列内容：" + this.latest.text +
        (assets.length ? "\n最新附件：" + assets.join("、") : "") +
        (draft.media ? "\n继续编辑会保留新版附件和本地新增附件。" : ""));
    } else if (this.phase === "held" && !this.latest.editing) {
      this.setPhase("lost", "编辑已过期。可按最新版本继续编辑。");
    } else {
      this.retryButton.hidden = this.phase !== "lost";
      this.restoreButton.hidden = true;
    }
  }

  private mount(draft: Draft): void {
    this.draft = draft;
    this.root.hidden = false;
    this.label.textContent = "编辑发给 " + draft.agent + " 的队列内容";
    this.input.value = draft.text;
    this.media.bind(draft.media);
    this.host.changed();
  }

  private persist(): void {
    if (!this.draft) return;
    try { sessionStorage.setItem(storageKey(this.workspace, this.session), JSON.stringify(this.draft)); } catch { /* In-memory draft remains available. */ }
  }

  private async request(action: EditAction, draft: Draft, workspace = this.workspace, session = this.session): Promise<void> {
    const fields: Record<string, unknown> = {
      action, id: draft.id, edit_id: draft.token,
      ...(action === "hold" || action === "edit" ? { body_version: draft.version } : {}),
      ...(action === "edit" ? { text: draft.text, operation_id: draft.operation } : {}),
    };
    if (action === "edit" && draft.media) {
      try { Object.assign(fields, await this.media.fields(draft.media)); }
      catch(error) { throw new APIError(400, errorMessage(error)); }
    }
    return this.host.request(workspace, session, fields);
  }

  private setPhase(phase: Phase, message = ""): void {
    this.phase = phase;
    this.status.textContent = message;
    this.input.readOnly = phase === "saving" || phase === "uncertain" || phase === "opening";
    this.saveButton.textContent = phase === "uncertain" ? "重试保存" : "保存";
    this.media.enable(phase === "held");
    this.buttons();
    this.cancelButton.textContent = phase === "uncertain" ? "关闭" : "取消";
    this.retryButton.hidden = phase !== "lost" || this.latest?.state !== "waiting";
    this.restoreButton.hidden = phase !== "lost" || !this.snapshotSeen || this.latest?.state === "waiting";
    if (phase !== "held") window.clearTimeout(this.renewTimer);
  }

  private buttons(): void {
    this.saveButton.disabled = (this.phase !== "held" && this.phase !== "uncertain") || this.media.loading;
    this.cancelButton.disabled = this.phase === "saving" || this.media.loading;
  }

  private reacquire(draft: Draft, version: number): void {
    const workspace = this.workspace, session = this.session, oldToken = draft.token;
    if (draft.media && draft.version !== version && this.latest) {
      // Previous indices refer to the old body. Start from the newly shown
      // attachments and preserve any local additions for explicit review.
      draft.media.originals = this.latest.attachments || [];
      draft.media.keep = draft.media.originals.map((_, i) => i);
      this.media.bind(draft.media);
    }
    draft.version = version;
    draft.token = this.host.newID();
    this.persist();
    void this.acquire(draft).finally(async () => {
      if (draft.token === oldToken) return;
      // Protect the head continuously while an old lease is replaced.
      try { await this.host.request(workspace, session, { action: "release", id: draft.id, edit_id: oldToken }); }
      catch { /* An unreachable old lease expires on the server. */ }
    });
  }

  private async acquire(draft: Draft): Promise<void> {
    const workspace = this.workspace, session = this.session, token = draft.token;
    this.setPhase("opening", "正在暂停这条消息的自动发送…");
    try {
      await this.request("hold", draft);
      if (this.draft !== draft || draft.token !== token) {
        await this.host.request(workspace, session, { action: "release", id: draft.id, edit_id: token });
        return;
      }
      this.setPhase("held");
      this.sync(this.latest ? [this.latest] : []);
      if (this.phase === "held") { this.scheduleRenew(draft); this.input.focus(); }
    } catch (error) { if (draft.token === token) this.failed(draft, error); }
  }

  private scheduleRenew(draft: Draft): void {
    window.clearTimeout(this.renewTimer);
    const token = draft.token;
    this.renewTimer = window.setTimeout(async () => {
      if (this.draft !== draft || this.phase !== "held") return;
      try {
        await this.request("renew", draft);
        if (this.draft === draft && this.phase === "held") this.scheduleRenew(draft);
      } catch (error) { if (this.phase === "held" && draft.token === token) this.failed(draft, error); }
    }, 45_000);
  }

  private async save(): Promise<void> {
    const draft = this.draft;
    if (!draft || this.media.loading || (this.phase !== "held" && this.phase !== "uncertain")) return;
    const retry = this.phase === "uncertain";
    draft.operation ||= this.host.newID();
    const workspace = this.workspace, session = this.session, key = storageKey(workspace, session);
    this.persist();
    this.setPhase("saving", "正在保存…");
    try {
      if (!retry || !await this.host.confirmed(workspace,session,draft.id,draft.operation)) await this.request("edit", draft, workspace, session);
      if (this.draft === draft) this.detach(true, false);
      else if (storedDraft(key)?.operation === draft.operation) {
        try { sessionStorage.removeItem(key); } catch { /* Recover from the receipt on return. */ }
        if (draft.media) this.cache.remove(draft.media.key);
      }
    } catch (error) {
      if (this.draft !== draft) return;
      const status = (error as { status?: number }).status;
      if (!status || status >= 500) {
        this.setPhase("uncertain", "保存结果尚未确认。重试会核对同一次修改。");
      } else {
        delete draft.operation;
        this.persist();
        if (status === 400) { this.setPhase("held", errorMessage(error)); this.scheduleRenew(draft); }
        else this.failed(draft, error);
      }
    }
  }

  private failed(draft: Draft, error: unknown): void {
    if (this.draft !== draft) return;
    this.setPhase("lost", isCancellation(error) ? "未确认编辑状态。" : errorMessage(error));
    void this.host.refresh();
  }

  private close(): void { this.detach(true); }

  private detach(discard: boolean, release = true): void {
    const draft = this.draft;
    if (!draft) return;
    window.clearTimeout(this.renewTimer);
    if (release && this.phase !== "saving") void this.request("release", draft).catch(() => { /* The server lease expires if the tab disconnects. */ });
    if (discard) {
      try { sessionStorage.removeItem(storageKey(this.workspace, this.session)); } catch { /* Storage may be disabled. */ }
      if (draft.media) this.cache.remove(draft.media.key);
    }
    this.draft = this.latest = undefined;
    this.media.bind(undefined);
    this.root.hidden = true;
    this.host.changed();
  }
}
