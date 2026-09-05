import { describe, expect, it, vi } from "vitest";
import { APIClient, APIError, RequestScope } from "./api-client";

describe("unified API client", () => {
  it("coalesces concurrent 401s without replacing fetch or replaying writes", async () => {
    const original = globalThis.fetch;
    const transport = vi.fn(async () => new Response("{}", { status: 401 }));
    const api = new APIClient(transport); api.onUnauthorized = vi.fn();
    const results = await Promise.allSettled([api.request("/api/v1/chat", { method: "POST" }), api.request("/api/v1/status")]);
    expect(results.every(result => result.status === "rejected")).toBe(true);
    expect(api.onUnauthorized).toHaveBeenCalledTimes(1); expect(transport).toHaveBeenCalledTimes(2); expect(globalThis.fetch).toBe(original);
  });
  it("preserves 403 errors and does not expire authentication", async () => {
    const api = new APIClient(vi.fn(async () => new Response('{"error":"forbidden"}', { status:403 }))); api.onUnauthorized = vi.fn();
    await expect(api.json("/api/v1/webui/doctor")).rejects.toMatchObject({ status:403, message:"forbidden" }); expect(api.onUnauthorized).not.toHaveBeenCalled();
    expect(new APIError(403,"forbidden")).toBeInstanceOf(Error);
  });
  it("leaves SSE response bodies unread", async () => {
    const response = new Response("event: done\ndata: {}\n\n", { headers:{"Content-Type":"text/event-stream"} });
    const api = new APIClient(vi.fn(async () => response));
    const stream = await api.request("/api/v1/chat"); expect(stream).toBe(response); expect(stream.bodyUsed).toBe(false); expect(await stream.text()).toContain("event: done");
  });
  it("drops JSON completing after its workspace scope is cancelled", async () => {
    let resolve!: (value:object)=>void;
    const response = new Response("{}"); response.json = () => new Promise(done => { resolve = done; });
    const api = new APIClient(vi.fn(async () => response)); const scope = new RequestScope(api);
    const pending = scope.json("/api/v1/webui/workspace");
    await vi.waitFor(() => expect(resolve).toBeTypeOf("function")); scope.cancel(); resolve({ path:"old-workspace" });
    await expect(pending).rejects.toMatchObject({ name:"AbortError" });
  });
  it("suspends protected calls but permits login and then resumes", async () => {
    const transport = vi.fn(async () => new Response("{}"));const api = new APIClient(transport);
    api.suspend();await expect(api.request("/api/v1/status")).rejects.toMatchObject({name:"AbortError"});
    await api.request("/api/v1/webui/auth",{method:"POST"});api.authenticated();await api.json("/api/v1/status");expect(transport).toHaveBeenCalledTimes(2);
  });
});
