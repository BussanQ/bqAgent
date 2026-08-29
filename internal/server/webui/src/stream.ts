import { parseSSE } from "./api";
import type { SSEEvent } from "./types";

const TERMINAL_EVENTS = new Set(["done", "stopped", "error"]);

export class SSEBuffer {
  private buffer = "";
  terminal = false;

  push(chunk: string): SSEEvent[] {
    this.buffer += chunk;
    const events: SSEEvent[] = [];
    let boundary = this.findBoundary();
    while (boundary) {
      const raw = this.buffer.slice(0, boundary.index);
      this.buffer = this.buffer.slice(boundary.index + boundary.length);
      if (raw.trim()) {
        const event = parseSSE(raw);
        events.push(event);
        if (TERMINAL_EVENTS.has(event.event)) this.terminal = true;
      }
      boundary = this.findBoundary();
    }
    return events;
  }

  remainder(): string {
    return this.buffer;
  }

  private findBoundary(): { index: number; length: number } | null {
    const lf = this.buffer.indexOf("\n\n");
    const crlf = this.buffer.indexOf("\r\n\r\n");
    if (lf < 0 && crlf < 0) return null;
    if (crlf >= 0 && (lf < 0 || crlf < lf)) return { index: crlf, length: 4 };
    return { index: lf, length: 2 };
  }
}
