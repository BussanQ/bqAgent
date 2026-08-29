import { afterEach, describe, expect, it, vi } from "vitest";
import { showTemporaryError, validateFileAttachment, validateImageAttachment } from "./attachments";

describe("附件限制", () => {
  afterEach(() => vi.useRealTimers());

  it("限制图片类型、单张大小、数量和合计大小", () => {
    expect(validateImageAttachment(4, 0, { type: "image/png", size: 1 })).toContain("4 张");
    expect(validateImageAttachment(0, 0, { type: "image/webp", size: 1 })).toContain("PNG");
    expect(validateImageAttachment(0, 0, { type: "image/png", size: 3 * 1024 * 1024 + 1 })).toContain("3 MiB");
    expect(validateImageAttachment(1, 8 * 1024 * 1024, { type: "image/png", size: 1 })).toContain("8 MiB");
  });

  it("限制文件数量、单个大小和合计大小", () => {
    expect(validateFileAttachment(5, 0, 1)).toContain("5 个");
    expect(validateFileAttachment(0, 0, 2 * 1024 * 1024 + 1)).toContain("2 MiB");
    expect(validateFileAttachment(1, 6 * 1024 * 1024, 1)).toContain("6 MiB");
    expect(validateFileAttachment(0, 0, 1)).toBe("");
  });

  it("附件错误在超时后自动隐藏", () => {
    vi.useFakeTimers();
    const element = document.createElement("div");
    let expired = false;
    showTemporaryError(element, "读取失败", 4000, () => { expired = true; });
    expect(element.textContent).toBe("读取失败");
    vi.advanceTimersByTime(3999);
    expect(element.textContent).toBe("读取失败");
    vi.advanceTimersByTime(1);
    expect(element.textContent).toBe("");
    expect(expired).toBe(true);
  });
});
