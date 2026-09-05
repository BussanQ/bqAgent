import { responseJSON } from "./api";

export class APIError extends Error {
  constructor(public readonly status: number, message: string) { super(message); this.name = "APIError"; }
}
export function isCancellation(error: unknown): boolean {
  return typeof error === "object" && error !== null && "name" in error && error.name === "AbortError";
}

// An epoch owns all protected requests, including response bodies still streaming.
// Aborting an epoch does not retry mutations or touch the browser's global fetch.
export class APIClient {
  private epoch = new AbortController();
  private expired = false;
  private signals = new WeakMap<Response, AbortSignal>();
  onUnauthorized: () => void = () => {};
  constructor(private readonly transport: typeof fetch = globalThis.fetch.bind(globalThis)) {}
  abort(): void { this.epoch.abort(); this.epoch = new AbortController(); }
  authenticated(): void { this.abort(); this.expired = false; }
  suspend(): void { this.expired = true; this.abort(); }
  get suspended(): boolean { return this.expired; }
  async request(input: RequestInfo | URL, init: RequestInit = {}): Promise<Response> {
    const url = new URL(typeof input === "string" ? input : input instanceof URL ? input.href : input.url, globalThis.location?.href || "http://localhost");
    const auth = url.pathname === "/api/v1/webui/auth";
    if (!auth && this.expired) throw new DOMException("Authentication expired", "AbortError");
    const epoch = this.epoch.signal;
    const signals: AbortSignal[] = [];
    if (!auth) signals.push(epoch);
    if (input instanceof Request) signals.push(input.signal);
    if (init.signal) signals.push(init.signal);
    const signal = signals.length ? AbortSignal.any(signals) : undefined;
    const response = await this.transport(input, { credentials: "same-origin", ...init, signal });
    if (signal?.aborted) throw new DOMException("Request cancelled", "AbortError");
    if (response.status === 401 && !auth) {
      if (!this.expired) { this.expired = true; this.abort(); this.onUnauthorized(); }
      throw new DOMException("Authentication expired", "AbortError");
    }
    if (!response.ok) {
      let message = `HTTP ${response.status}`;
      try { const payload = await responseJSON<{ error?: string }>(response); if (payload.error) message = payload.error; } catch { /* Keep the HTTP status for non-JSON errors. */ }
      throw new APIError(response.status, message);
    }
    if (signal) this.signals.set(response, signal);
    return response;
  }
  async readJSON<T extends object>(response: Response): Promise<T> {
    const value = await responseJSON<T>(response);
    if (this.signals.get(response)?.aborted) throw new DOMException("Stale response", "AbortError");
    return value;
  }
  async json<T extends object>(url: string, init?: RequestInit): Promise<T> { return this.readJSON<T>(await this.request(url, init)); }
}

export class RequestScope {
  private controller = new AbortController();
  private revision = 0;
  constructor(private readonly api: APIClient) {}
  readJSON<T extends object>(response: Response): Promise<T> { return this.api.readJSON<T>(response); }
  cancel(): void { this.controller.abort(); this.controller = new AbortController(); this.revision++; }
  async request(url: string, init: RequestInit = {}): Promise<Response> {
    const revision = this.revision;
    const signal = init.signal ? AbortSignal.any([init.signal, this.controller.signal]) : this.controller.signal;
    const response = await this.api.request(url, { ...init, signal });
    if (revision !== this.revision) throw new DOMException("Stale request", "AbortError");
    return response;
  }
  async json<T extends object>(url: string, init?: RequestInit): Promise<T> {
    const revision = this.revision;
    const payload = await this.readJSON<T>(await this.request(url, init));
    if (revision !== this.revision) throw new DOMException("Stale response", "AbortError");
    return payload;
  }
}
