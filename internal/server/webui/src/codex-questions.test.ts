import { expect, it, vi } from "vitest";
import { hasBlockingPrompt, renderHistoricalPrompt, showAgentPrompt } from "./acp-permission";
import type { ACPPermissionPayload } from "./types";

const choice: ACPPermissionPayload = {
  request_id: "one", agent: "codex", session_id: "chat", external_session_id: "thread", tool_call: {},
  dialog: { method: "question", header: "Files", title: "Choose a folder" },
  options: [{ option_id: "choice-0", name: "src", description: "<img src=x> Source files", kind: "" },
    { option_id: "cancel", name: "取消本组问题", kind: "reject_once" }],
};

it("renders independent Codex questions and preserves the authoritative resolution over a pending HTTP reply", async () => {
  const pending: Record<string, HTMLElement> = {}, thread = document.createElement("main");
  const create = () => { const el = document.createElement("div"); thread.append(el); return el; };
  let finish!: () => void;
  const post = vi.fn(() => new Promise<void>(resolve => { finish = resolve; }));
  const text: ACPPermissionPayload = { ...choice, request_id: "two", dialog: { method: "question", title: "Add detail", free_text: true, nonblocking: true },
    options: [{ option_id: "submit", name: "提交填写的回答", kind: "" }] };
  showAgentPrompt(choice, pending, create, post);
  showAgentPrompt(text, pending, create, post);
  showAgentPrompt(choice, pending, create, post);
  expect(thread.children).toHaveLength(2);
  expect(pending.one.textContent).toContain("codex · Files");
  expect(pending.one.querySelector("button small")?.textContent).toContain("Source files");
  expect(pending.one.querySelector("img,textarea")).toBeNull();
  expect(hasBlockingPrompt(pending)).toBe(true);
  pending.one.querySelector("button")!.click();
  expect(post).toHaveBeenCalledWith("choice-0", undefined);
  showAgentPrompt({ ...choice, expired: true }, pending, create, post);
  finish();
  await Promise.resolve();
  expect(thread.children[0].textContent).toContain("请求已取消或失效");
  expect(thread.children[0].textContent).not.toContain("已回答：src");
  expect(hasBlockingPrompt(pending)).toBe(false);
  const field = pending.two.querySelector("textarea")!;
  pending.two.querySelector("button")!.click();
  expect(post).toHaveBeenCalledTimes(1);
  expect(pending.two.textContent).toContain("请先填写回答");
  field.value = "  a\nb  ";
  pending.two.querySelector("button")!.click();
  expect(post).toHaveBeenLastCalledWith("submit", "  a\nb  ");
  finish();
  await Promise.resolve();
  expect(pending.two.textContent).toContain("等待确认");
  showAgentPrompt({ ...text, selected: "submit" }, pending, create, post);
  expect(field.value).toBe("");
  expect(thread.children[1].querySelector(".acp-permission-status")?.textContent).toBe("已回答");
  expect(Object.keys(pending)).toEqual([]);
});

it("masks secret Codex input, clears it on expiry, and restores question options as read-only text", async () => {
  const pending: Record<string, HTMLElement> = {}, card = document.createElement("div");
  const secret: ACPPermissionPayload = { ...choice, dialog: { method: "question", title: "Enter credential", secret: true, free_text: true },
    options: [{ option_id: "submit", name: "提交填写的回答", kind: "" }] };
  const rejected = vi.fn(async () => { throw Object.assign(new Error("expired"), {status: 410}); });
  showAgentPrompt(secret, pending, () => card, rejected);
  const field = card.querySelector("input")!;
  expect(field.type).toBe("password");
  field.value = "sensitive input";
  card.querySelector("button")!.click();
  await Promise.resolve();
  expect(field.disabled).toBe(true);
  expect(field.value).toBe("");
  expect(card.textContent).not.toContain("sensitive input");
  renderHistoricalPrompt(card, "codex", {method: "question", title: "Choose a folder", header: "Files",
    options: [{label: "src", description: "<img src=x> Source files"}], state: "selected", choice: "src"});
  expect(card.textContent).toContain("已回答：src");
  expect(card.querySelector("button,input,img")).toBeNull();
  expect(card.classList.contains("expired")).toBe(false);
  expect(card.querySelector("details li")?.textContent).toContain("Source files");
});
