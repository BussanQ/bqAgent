import { describe, expect, it } from "vitest";
import { escapeHtml, renderMarkdown, safeHref } from "./markdown";

describe("Markdown 安全渲染", () => {
  it("转义原始 HTML", () => {
    expect(escapeHtml('<script>alert("x")</script>')).toBe("&lt;script&gt;alert(&quot;x&quot;)&lt;/script&gt;");
    expect(renderMarkdown("<img src=x onerror=alert(1)>")).not.toContain("<img");
  });

  it("只允许安全链接协议", () => {
    expect(safeHref("https://example.com/a?q=1")).toBe("https://example.com/a?q=1");
    expect(safeHref("mailto:test@example.com")).toBe("mailto:test@example.com");
    expect(safeHref("javascript:alert(1)")).toBe("");
    expect(renderMarkdown("[危险](javascript:alert(1))")).not.toContain("href=");
  });
});
