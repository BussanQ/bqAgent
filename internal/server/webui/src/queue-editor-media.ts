import { splitDroppedFiles, validateFileAttachment, validateImageAttachment, type SentAttachments } from "./attachments";
import type { QueueEntry } from "./command-queue";
import { errorMessage } from "./api";
import { QueueUploadCache } from "./queue-upload-cache";

type Asset = NonNullable<QueueEntry["attachments"]>[number];
export interface EditMedia {
  originals: Asset[];
  keep: number[];
  added: Asset[];
  key: string;
}

export function editMedia(assets: Asset[], key: string): EditMedia {
  return { originals: assets, keep: assets.map((_, index) => index), added: [], key };
}

function fileBase64(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result).split(",", 2)[1] || "");
    reader.onerror = () => reject(reader.error || new Error("读取附件失败"));
    reader.readAsDataURL(file);
  });
}

export class QueueEditorMedia {
  readonly root = document.createElement("div");
  private readonly tray = document.createElement("div");
  private readonly input = document.createElement("input");
  private readonly choose = document.createElement("button");
  private draft?: EditMedia;
  private enabled = false;
  private reading = false;

  constructor(private readonly cache: QueueUploadCache, private readonly newID: () => string, private readonly changed: () => void,
    private readonly notify: (message: string) => void) {
    this.root.className = "queue-edit-media";
    this.input.type = "file";
    this.input.multiple = true;
    this.input.hidden = true;
    this.choose.type = "button";
    this.choose.textContent = "添加附件";
    this.choose.className = "turn-input-btn";
    this.choose.onclick = () => this.input.click();
    this.input.onchange = () => {
      const files = Array.from(this.input.files || []);
      this.input.value = "";
      if (files.length) void this.add(files);
    };
    this.root.append(this.tray, this.input, this.choose);
    this.bind(undefined);
  }

  get loading(): boolean { return this.reading; }
  bind(draft?: EditMedia): void {
    this.draft = draft;
    this.reading = false;
    this.root.hidden = !draft;
    this.render();
  }
  enable(value: boolean): void { this.enabled = value; this.render(); }

  private render(): void {
    this.tray.replaceChildren();
    this.choose.disabled = !this.enabled || this.reading;
    const d = this.draft;
    if (!d) return;
    const chip = (asset: Asset, label: string, toggle: () => void) => {
      const row = document.createElement("span");
      row.className = "queue-edit-asset";
      const name = document.createElement("span");
      name.textContent = asset.name || asset.mime_type || "图片";
      const button = document.createElement("button");
      button.type = "button";
      button.className = "turn-input-btn";
      button.disabled = !this.enabled || this.reading;
      button.textContent = label;
      button.onclick = toggle;
      row.append(name, button);
      this.tray.appendChild(row);
    };
    d.originals.forEach((asset, i) => chip(asset, d.keep.includes(i) ? "移除附件" : "保留附件", () => {
      d.keep = d.keep.includes(i) ? d.keep.filter(index => index !== i) : [...d.keep, i].sort((a,b) => a-b);
      this.render(); this.changed();
    }));
    d.added.forEach((asset, i) => chip(asset, "移除新增附件", () => { void this.removeAdded(d,i); }));
    if (d.added.length > 1) {
      const clear = document.createElement("button");
      clear.type = "button";
      clear.className = "turn-input-btn";
      clear.textContent = "移除全部新增附件";
      clear.disabled = !this.enabled || this.reading;
      clear.onclick = () => { this.cache.remove(d.key); d.added = []; this.render(); this.changed(); };
      this.tray.appendChild(clear);
    }
  }

  async fields(draft: EditMedia): Promise<Record<string, unknown>> {
    if (draft.keep.length === draft.originals.length && !draft.added.length) return {};
    const payload = await this.payload(draft);
    return { keep_attachments: draft.keep, ...payload };
  }

  private async payload(d: EditMedia): Promise<SentAttachments> {
    if (!d.added.length) return { images: [], files: [] };
    const stored = await this.cache.get(d.key);
    if (!stored || stored.images.length + stored.files.length !== d.added.length) {
      throw new Error("新增附件在此浏览器中不可用，请移除后重新添加。");
    }
    return { images: [...stored.images], files: [...stored.files] };
  }

  private busy(value: boolean): void {
    this.reading = value;
    this.render();
    this.changed();
  }

  private async stage(d: EditMedia, payload: SentAttachments, added: Asset[]): Promise<void> {
    const prior = d.key;
    const key = "editor-upload-" + this.newID();
    await this.cache.put(key, payload);
    if (this.draft !== d || !this.enabled || d.key !== prior) { this.cache.remove(key); return; }
    d.key = key;
    d.added = added;
    this.changed();
    if (key !== prior) this.cache.remove(prior);
  }

  private async add(selected: File[]): Promise<void> {
    const d = this.draft;
    if (!d || !this.enabled || this.reading) return;
    this.busy(true);
    try {
      const payload = await this.payload(d);
      const current = [...d.keep.map(index => d.originals[index]), ...d.added];
      const files = splitDroppedFiles(selected);
      const additions = [...d.added];
      for (const [kind, group] of [["image", files.images], ["file", files.files]] as const) {
        for (const file of group) {
          const same = current.filter(asset => asset.kind === kind);
          const bytes = same.reduce((sum, asset) => sum + asset.size, 0);
          const error = kind === "image" ? validateImageAttachment(same.length, bytes, file) : validateFileAttachment(same.length, bytes, file.size);
          if (error) throw new Error(error);
          const data = await fileBase64(file);
          const asset = { kind, name: file.name, size: file.size, mime_type: file.type };
          current.push(asset);
          if (kind === "image") {
            additions.splice(payload.images.length, 0, asset);
            payload.images.push({ mime_type: file.type, data_base64: data });
          } else {
            additions.push(asset);
            payload.files.push({ name: file.name, data_base64: data });
          }
        }
      }
      if (this.draft !== d || !this.enabled) return;
      await this.stage(d, payload, additions);
    } catch (error) { if (this.draft === d) this.notify(errorMessage(error)); }
    finally { if (this.draft === d) this.busy(false); }
  }

  private async removeAdded(d: EditMedia, index: number): Promise<void> {
    if (this.reading || !this.enabled) return;
    this.busy(true);
    try {
      if (d.added.length === 1) {
        this.cache.remove(d.key);
        d.added = [];
      } else {
        const payload = await this.payload(d);
        if (this.draft !== d || !this.enabled) return;
        if (index < payload.images.length) payload.images.splice(index, 1);
        else payload.files.splice(index - payload.images.length, 1);
        await this.stage(d,payload,d.added.filter((_, i) => i !== index));
      }
    } catch (error) {
      if (this.draft === d) this.notify(errorMessage(error));
    } finally { if (this.draft === d) this.busy(false); }
  }
}
