import { expect, it, vi } from "vitest";
import { hasBlockingPrompt, renderHistoricalPrompt, showAgentPrompt } from "./acp-permission";
import type { ACPPermissionPayload } from "./types";

const mcp: ACPPermissionPayload = {
  request_id: "approval", session_id: "chat", external_session_id: "thread", agent: "codex",
  tool_call: { source: "codex-app-server", kind: "mcp_tool_call", title: "Move this file?", server: "drive", tool: "move_file",
    detail: 'Destination: "<img src=x>"', rawInput: { hide: "private metadata" } },
  options: [{ option_id: "accept", name: "允许本次", kind: "allow_once" },
    { option_id: "acceptForSession", name: "本会话允许此工具", kind: "allow_always" },
    { option_id: "cancel", name: "拒绝本次调用", kind: "reject_once" }],
};

it("shows the Codex MCP tool, displayed arguments and offered scopes until its native resolution", async () => {
  const pending: Record<string, HTMLElement> = {}, card = document.createElement("div");
  const submit = vi.fn(async () => {});
  showAgentPrompt(mcp, pending, () => card, submit);
  expect(card.querySelector(".acp-permission-heading")?.textContent).toBe("MCP 工具授权");
  expect(card.querySelector("code")?.textContent).toBe("drive / move_file");
  expect(card.querySelector("pre")?.textContent).toBe(mcp.tool_call.detail);
  expect(card.textContent).not.toContain("private metadata");
  expect(card.querySelector("img")).toBeNull();
  const options = Array.from(card.querySelectorAll("button"));
  expect(options.map(option => option.textContent)).toEqual(["允许本次", "本会话允许此工具", "拒绝本次调用"]);
  options[1].click();
  await Promise.resolve();
  expect(submit).toHaveBeenCalledWith("acceptForSession", undefined);
  expect(hasBlockingPrompt(pending)).toBe(true);
  expect(card.textContent).toContain("等待确认");
  showAgentPrompt({ ...mcp, expired: true }, pending, () => card, submit);
  expect(card.classList.contains("expired")).toBe(true);
  expect(card.textContent).not.toContain("已选择：本会话允许此工具");
  expect(hasBlockingPrompt(pending)).toBe(false);
});

it("restores a Codex MCP approval with its public detail and the actual recorded decision", () => {
  const card = document.createElement("div");
  renderHistoricalPrompt(card, "codex", { method: "mcp_approval", title: "Move this file?", header: "drive / move_file",
    detail: mcp.tool_call.detail, state: "selected", choice: "拒绝本次调用",
    options: [{label: "允许本次"}, {label: "拒绝本次调用"}] });
  expect(card.querySelector("code")?.textContent).toBe("drive / move_file");
  expect(card.querySelector("pre")?.textContent).toContain("<img src=x>");
  expect(card.querySelector("img,button,input")).toBeNull();
  expect(card.textContent).toContain("已选择：拒绝本次调用");
  expect(Array.from(card.querySelectorAll("li"), node => node.textContent)).toEqual(["允许本次", "拒绝本次调用"]);
});
