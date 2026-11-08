import { describe, expect, it, vi } from "vitest";
import { acpPermissionTitle, agentDialogField, formatACPPermissionInput, isRejectPermissionOption, settleAgentPrompt, showAgentPrompt } from "./acp-permission";

describe("ACP permission presentation", () => {
  it("renders Pi editor and input defaults as plain text and leaves choices to buttons", () => {
    const editor = agentDialogField({method: "editor", title: "说明", prefill: "<b>原文</b>\n下一行"});
    expect(editor?.tagName).toBe("TEXTAREA");
    expect(editor?.value).toBe("<b>原文</b>\n下一行");
    const field = agentDialogField({method: "input", title: "名称", placeholder: "可留空"});
    expect(field?.tagName).toBe("INPUT");
    expect(field?.placeholder).toBe("可留空");
    expect(agentDialogField({method: "confirm", title: "继续？"})).toBeUndefined();
  });
  it("uses the tool title and formats structured input", () => {
    expect(acpPermissionTitle({ toolCallId: "tool-1", title: "运行测试", kind: "execute" })).toBe("运行测试");
    expect(formatACPPermissionInput({ command: "go test ./internal/extagent" })).toContain("go test ./internal/extagent");
  });

  it("identifies reject options", () => {
    expect(isRejectPermissionOption("reject_once")).toBe(true);
    expect(isRejectPermissionOption("allow_once")).toBe(false);
  });
});

it("disables a previously answered dialog when its resolution is replayed", () => {
  const card = document.createElement("div");
  card.innerHTML = '<textarea></textarea><button>提交</button><span class="acp-permission-status"></span>';
  const pending = { p: card };
  settleAgentPrompt(pending,"p","确认");
  expect(card.classList.contains("resolved")).toBe(true);
  expect(card.textContent).toContain("已选择：确认");
  expect(card.querySelector("textarea")?.disabled).toBe(true);
  expect(card.querySelector("button")?.disabled).toBe(true);
  expect(Object.keys(pending)).toEqual([]);
});

it("waits for the Claude CLI before resolving its tool approval", async () => {
  const card = document.createElement("div"), pending: Record<string, HTMLElement> = {};
  const prompt = {
    agent: "claude", request_id: "r", session_id: "chat", external_session_id: "session",
    tool_call: { source: "claude-cli", tool: "ExitPlanMode", title: "审阅计划", detail: "计划\n<step>" },
    options: [{ option_id: "allow", name: "批准计划", kind: "allow_once" }, { option_id: "deny", name: "继续规划", kind: "reject_once" }],
  };
  const submit = vi.fn(async () => {});
  showAgentPrompt(prompt, pending, () => card, submit);
  expect(card.textContent).toContain("Claude 工具授权 · ExitPlanMode");
  expect(card.querySelector("pre")?.textContent).toBe(prompt.tool_call.detail);
  expect(card.querySelector("step")).toBeNull();
  card.querySelector("button")!.click();
  await Promise.resolve();
  expect(submit).toHaveBeenCalledWith("allow", undefined);
  expect(card.textContent).toContain("等待确认");
  showAgentPrompt({ ...prompt, expired: true }, pending, () => card, submit);
  expect(card.classList.contains("expired")).toBe(true);
  expect(card.textContent).not.toContain("已选择：批准计划");
});
