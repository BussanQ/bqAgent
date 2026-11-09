import type { SentAttachments } from "./attachments";

// Queue receipts and operation IDs stay in sessionStorage. Accepted files live
// on the server. Only uploads awaiting an acknowledgement use IndexedDB, whose
// quota is suitable for the existing attachment limits.
const databaseName = "bqagent-queued-uploads";
const uploads = "uploads";
type RequestValue = SentAttachments | undefined;

async function access(mode: IDBTransactionMode, run: (store: IDBObjectStore) => IDBRequest): Promise<RequestValue> {
  if (!globalThis.indexedDB) return;
  return new Promise((resolve, reject) => {
    const open = indexedDB.open(databaseName, 1);
    let blocked = false;
    open.onblocked = () => { blocked = true; reject(new Error("附件缓存正在其他页面中使用")); };
    open.onupgradeneeded = () => { if (!open.result.objectStoreNames.contains(uploads)) open.result.createObjectStore(uploads); };
    open.onerror = () => reject(open.error);
    open.onsuccess = () => {
      const db = open.result;
      if (blocked) { db.close(); return; }
      db.onversionchange = () => db.close();
      try {
        const tx = db.transaction(uploads, mode);
        const request = run(tx.objectStore(uploads));
        tx.oncomplete = () => { db.close(); resolve(mode === "readonly" ? request.result : undefined); };
        tx.onabort = () => { db.close(); reject(tx.error || new Error("附件缓存未写入")); };
      } catch (error) { db.close(); reject(error); }
    };
  });
}

export class QueueUploadCache {
  private memory = new Map<string, SentAttachments>();

  async put(key: string, payload: SentAttachments): Promise<void> {
    this.memory.set(key, payload);
    try { await access("readwrite", store => store.put(payload, key)); } catch { /* The originating tab can still submit and retry. */ }
  }

  async get(key: string): Promise<SentAttachments | undefined> {
    const known = this.memory.get(key);
    if (known) return known;
    try { return await access("readonly", store => store.get(key)); } catch { return; }
  }

  remove(key: string): void {
    this.memory.delete(key);
    void access("readwrite", store => store.delete(key)).catch(() => {});
  }

  clear(): void {
    this.memory.clear();
    void access("readwrite", store => store.clear()).catch(() => {});
  }
}
