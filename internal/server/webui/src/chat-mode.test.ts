import { describe, expect, it } from "vitest";
import { chatModePlaceholder, normalizeChatMode } from "./chat-mode";

describe("chat mode", () => {
  it("defaults unknown and legacy agent values to run and accepts ask case-insensitively", () => {
    expect(normalizeChatMode(undefined)).toBe("run");
    expect(normalizeChatMode("invalid")).toBe("run");
    expect(normalizeChatMode("agent")).toBe("run");
    expect(normalizeChatMode("ASK")).toBe("ask");
  });

  it("describes the read-only boundary in ask mode", () => {
    expect(chatModePlaceholder("ask")).toContain("只读问答");
    expect(chatModePlaceholder("run")).toContain("完整工具能力");
  });
});
