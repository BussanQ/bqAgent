import type { SSEEvent } from "./types";

export function parseSSE(raw: string): SSEEvent {
  let event = "message";
  const dataLines: string[] = [];
  raw.replace(/\r\n/g, "\n").split("\n").forEach((line) => {
    if (line.startsWith("event:")) {
      event = line.slice(6).trim();
    } else if (line.startsWith("data:")) {
      dataLines.push(line.slice(5).replace(/^ /, ""));
    }
  });
  return { event, data: dataLines.join("\n") };
}

export function narrowJSONObject<T extends object>(value: unknown, context = "JSON response"): T {
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    throw new Error(`${context} must be an object`);
  }
  return value as T;
}

export async function responseJSON<T extends object>(response: Response): Promise<T> {
  const value: unknown = await response.json();
  return narrowJSONObject<T>(value);
}

export function parseJSONEvent<T extends object>(data: string): T {
  const value: unknown = JSON.parse(data);
  return narrowJSONObject<T>(value, "SSE data");
}

export function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}
