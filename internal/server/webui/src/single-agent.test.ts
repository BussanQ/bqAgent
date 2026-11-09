import { describe, expect, it, vi } from "vitest";
import { SingleAgentPicker } from "./single-agent";
import type { GroupInfo } from "./types";

const catalog = (...ids: string[]): GroupInfo => ({
  scheduler: "bqagent",
  participants: ids.map(id => ({ id, name: id, kind: "external", available: true })),
});

describe("single chat agent picker", () => {
  it("waits for workspace detection before resolving a typed agent on the first message", async () => {
    const picker = new SingleAgentPicker(document.createElement("select"), () => {});
    let finish!: (agents: GroupInfo) => void;
    const load = picker.loadWorkspace(() => new Promise(resolve => { finish = resolve; }));
    const route = picker.readyForRoute("/pi hello").then(() => picker.externalResponder("/pi hello"));
    finish(catalog("codex", "pi"));
    expect(await route).toBe("pi");
    await load;
  });

  it("restores a draft responder without replacing the agent that owns the running turn", async () => {
    const select = document.createElement("select");
    const picker = new SingleAgentPicker(select, () => {});
    await picker.loadWorkspace(async () => catalog("pi", "codex"));
    picker.restore("codex", "pi");
    picker.configure("default", "run", true);
    picker.observe("codex");
    expect(select.value).toBe("pi");
    expect(picker.reportedAgent).toBe("codex");
    expect(picker.busyTarget("codex")).toBe("pi");
    picker.restore("codex");
    expect(select.value).toBe("codex");
    expect(picker.pendingAgent).toBeUndefined();
  });

  it("selects the responder without altering the message and respects Ask and group chats", async () => {
    const select = document.createElement("select");
    const changed = vi.fn();
    const picker = new SingleAgentPicker(select, changed);
    const agents = catalog("pi", "codex", "cursor");
    agents.participants[2].available = false;
    await picker.loadWorkspace(async () => agents);
    expect(Array.from(select.options, option => option.value)).toEqual(["bqagent", "pi", "codex"]);
    select.value = "pi";
    select.dispatchEvent(new Event("change"));
    expect(changed).toHaveBeenCalledOnce();
    expect(picker.requestAgent).toBe("pi");
    expect(picker.externalResponder("/codex review")).toBe("codex");
    picker.configure("default", "run", true);
    expect(select.disabled).toBe(false);
    for (const [type, mode] of [["default", "ask"], ["group", "run"]] as const) {
      picker.configure(type, mode, false);
      expect(select.disabled).toBe(true);
      expect(select.value).toBe("bqagent");
      expect(picker.requestAgent).toBeUndefined();
    }
    picker.configure("default", "run", false);
    expect(select.value).toBe("pi");
    select.value = "bqagent";
    select.dispatchEvent(new Event("change"));
    expect(picker.requestAgent).toBe("bqagent");
  });

  it("retains a mid-turn agent choice over the old completion and a queued child's replay", async () => {
    const select = document.createElement("select");
    const picker = new SingleAgentPicker(select, () => {});
    await picker.loadWorkspace(async () => catalog("codex", "pi"));
    picker.restore("codex");
    picker.configure("default", "run", true);
    expect(picker.busyTarget("codex")).toBe("codex");
    select.value = "pi";
    select.dispatchEvent(new Event("change"));
    picker.observe("codex");
    expect(picker.reportedAgent).toBe("codex");
    expect(picker.busyTarget("codex")).toBe("pi");
    expect(picker.externalResponder("replayed", picker.reportedAgent)).toBe("codex");
    picker.configure("default", "run", false);
    expect(picker.externalResponder("next")).toBe("pi");
    picker.configure("default", "run", true);
    picker.observe("codex");
    expect(select.value).toBe("pi");
    picker.selectNext("bqagent");
    expect(picker.busyTarget("codex")).toBe("bqagent");
    picker.beginSend();
    picker.observe("bqagent");
    expect(picker.busyTarget(undefined)).toBeUndefined();
    picker.restore("codex");
    expect(select.value).toBe("codex");
  });

  it("restores history without picking agents from a previous workspace", async () => {
    const select = document.createElement("select");
    const picker = new SingleAgentPicker(select, () => {});
    let finish!: (result: GroupInfo) => void;
    const old = picker.loadWorkspace(() => new Promise(resolve => { finish = resolve; }));
    const current = picker.loadWorkspace(async () => catalog("pi"));
    picker.restore("codex");
    await current;
    finish(catalog("cursor", "codex"));
    await old;
    expect(select.value).toBe("codex");
    expect(select.selectedOptions[0].disabled).toBe(true);
    expect(Array.from(select.options, option => option.value)).toEqual(["bqagent", "pi", "codex"]);
    picker.restore();
    expect(select.value).toBe("bqagent");
    expect(picker.requestAgent).toBeUndefined();
  });

  it("routes a bare agent name with attachments to its own model picker in Run single chats", async () => {
    const picker = new SingleAgentPicker(document.createElement("select"), () => {});
    await picker.loadWorkspace(async () => catalog("codex", "pi"));
    picker.configure("default", "run", true);
    expect(picker.busyContext(" /pi", "codex", true)).toEqual({ agent:"pi", qualified:true, query:"" });
    expect(picker.busyContext("", undefined, true)).toEqual({ agent:"bqagent", query:"" });
    picker.selectNext("pi");
    expect(picker.busyContext("", "codex", true).agent).toBe("pi");
    picker.configure("default", "ask", true);
    expect(picker.busyContext("/pi", undefined, true).agent).toBeUndefined();
    picker.configure("group", "run", true);
    expect(picker.busyContext("", undefined, true).agent).toBeUndefined();
  });
});
