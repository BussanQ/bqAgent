import { afterEach, expect, it, vi } from "vitest";
import { QueueUploadCache } from "./queue-upload-cache";

// Exercise the browser transaction boundary with an in-memory object store.
// Separate cache instances share only successfully completed transactions.
function mockDatabase() {
  const values = new Map<string, unknown>();
  vi.stubGlobal("indexedDB", {
    open() {
      const open: { result?: unknown; onsuccess?: () => void } = {};
      queueMicrotask(() => {
        open.result = {
          close() {},
          transaction() {
            const transaction = {
              oncomplete: undefined as (() => void) | undefined,
              objectStore: () => {
                function result(value?: unknown) {
                  queueMicrotask(() => transaction.oncomplete?.());
                  return { result: value };
                }
                return {
                  put(value: unknown, key: string) { values.set(key, structuredClone(value)); return result(); },
                  get(key: string) { return result(structuredClone(values.get(key))); },
                  delete(key: string) { values.delete(key); return result(); },
                  clear() { values.clear(); return result(); },
                };
              },
            };
            return transaction;
          },
        };
        open.onsuccess?.();
      });
      return open;
    },
  });
  return values;
}
afterEach(() => vi.unstubAllGlobals());

it("persists unresolved uploads across cache instances and releases bytes on receipt or logout", async () => {
  const stored = mockDatabase();
  const payload = { images: [], files: [{ name: "empty.md", data_base64: "" }] };
  await new QueueUploadCache().put("w:a",payload);
  const restored = new QueueUploadCache();
  expect(await restored.get("w:a")).toEqual(payload);
  expect(await restored.get("other:a")).toBeUndefined();
  restored.remove("w:a");
  await vi.waitFor(() => expect(stored.has("w:a")).toBe(false));
  await restored.put("w:b",payload);
  restored.clear();
  await vi.waitFor(() => expect(stored.size).toBe(0));
});

it("keeps the current tab's upload usable when durable browser storage is unavailable", async () => {
  vi.stubGlobal("indexedDB", { open() { throw new Error("storage disabled"); } });
  const cache = new QueueUploadCache();
  const payload = { images: [{ mime_type:"image/png", data_base64:"aW1n" }],files:[] };
  await cache.put("op",payload);
  expect(await cache.get("op")).toEqual(payload);
  expect(await new QueueUploadCache().get("op")).toBeUndefined();
  cache.remove("op");
  expect(await cache.get("op")).toBeUndefined();
});
