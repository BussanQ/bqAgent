import { describe, expect, it, vi } from "vitest";
import { inputKindForKey, isInputAppliedEvent, postTurnInput, restoreDraft, TurnInputSession, TurnInputTracker, type TurnInputHost } from "./turn-input";
import { MessageSnapshots } from "./stream";

describe("忙时输入快捷键", () => {
  it("Enter 纠偏，Ctrl/Cmd/Alt+Enter 作为后续任务，Shift+Enter 与输入法组合不提交", () => {
    expect(inputKindForKey({ key: "Enter" })).toBe("steering");
    expect(inputKindForKey({ key: "Enter", ctrlKey: true })).toBe("follow_up");
    expect(inputKindForKey({ key: "Enter", metaKey: true })).toBe("follow_up");
    expect(inputKindForKey({ key: "Enter", altKey: true })).toBe("follow_up");
    expect(inputKindForKey({ key: "Enter", shiftKey: true })).toBeNull();
    expect(inputKindForKey({ key: "Enter", isComposing: true })).toBeNull();
    expect(inputKindForKey({ key: "a" })).toBeNull();
  });
});

describe("postTurnInput", () => {
  const payload = { turnId: "t1", workspaceId: "w", kind: "steering" as const, text: "改用 Go" };
  it("只发送文本字段并解析 accepted", async () => {
    const request = vi.fn<(url: string, init: RequestInit) => Promise<Response>>()
      .mockResolvedValue(new Response(JSON.stringify({ status: "accepted", id: "i1" })));
    expect(await postTurnInput(request, payload)).toEqual({ outcome: "accepted", id: "i1", error: "" });
    const init = request.mock.calls[0][1];
    expect(JSON.parse(String(init.body))).toEqual({ turn_id: "t1", workspace_id: "w", kind: "steering", text: "改用 Go" });
  });
  it("把 404/409/400 状态映射为 notfound/closed/rejected", async () => {
    for (const [status, outcome] of [[404, "notfound"], [409, "closed"], [400, "rejected"]] as const) {
      const request = vi.fn(async () => { throw Object.assign(new Error("x"), { status }); });
      expect((await postTurnInput(request, payload)).outcome).toBe(outcome);
    }
    const network = vi.fn(async () => { throw new Error("offline"); });
    await expect(postTurnInput(network, payload)).rejects.toThrow("offline");
  });
});

describe("TurnInputTracker", () => {
  it("排队后收到 input.applied 变为已应用，且应用事件早于响应也能匹配", () => {
    const tracker = new TurnInputTracker();
    const a = tracker.add("steering", "A");
    tracker.accepted(a, "ia");
    expect(a.state).toBe("queued");
    expect(tracker.applied("ia")).toBe(a);
    expect(a.state).toBe("applied");

    const b = tracker.add("follow_up", "B");
    expect(tracker.applied("ib")).toBeNull();
    tracker.accepted(b, "ib");
    expect(b.state).toBe("applied");
    expect(tracker.queued()).toEqual([]);
  });
  it("结束时未应用的输入标记为未消费并可恢复为草稿，失败项不恢复", () => {
    const tracker = new TurnInputTracker();
    const a = tracker.add("steering", "已应用");
    tracker.accepted(a, "ia");
    tracker.applied("ia");
    const b = tracker.add("steering", "未消费1");
    tracker.accepted(b, "ib");
    const c = tracker.add("follow_up", "未消费2");
    tracker.accepted(c, "ic");
    const d = tracker.add("steering", "失败");
    tracker.failed(d);
    const unconsumed = tracker.finish([{ id: "ib", kind: "steering", text: "未消费1" }]);
    expect(unconsumed.map((item) => item.text)).toEqual(["未消费1", "未消费2"]);
    expect(b.state).toBe("unconsumed");
    expect(restoreDraft("正在写", unconsumed)).toBe("未消费1\n\n未消费2\n\n正在写");
    expect(restoreDraft("", [])).toBe("");

    const late = tracker.add("steering", "迟到");
    tracker.accepted(late, "il");
    expect(late.state).toBe("unconsumed");
  });
});

describe("input.applied 快照", () => {
  it("识别输入应用事件，使其不会进入助手消息快照", () => {
    const messages = new MessageSnapshots("t");
    const event = { turn_id: "t", item_id: "i1", seq: 3, kind: "input.applied", phase: "steering", text: "改用 Go" };
    expect(isInputAppliedEvent(event)).toBe(true);
    expect(isInputAppliedEvent({ kind: "message.updated" })).toBe(false);
    expect(messages.apply(event as never)).toBe(false);
    expect(messages.latest).toBeNull();
  });
});

function fakeHost(overrides: Partial<TurnInputHost> = {}) {
  const state = { draft: "", scope: "w:1", notices: [] as string[], applied: [] as string[][], queue: [] as string[][] };
  const host: TurnInputHost = {
    request: async () => new Response(JSON.stringify({ status: "accepted", id: "i1" })),
    scope: () => state.scope,
    takeDraft: () => { const text = state.draft; state.draft = ""; return text; },
    getDraft: () => state.draft,
    setDraft: (text) => { state.draft = text; },
    renderQueue: (items) => { state.queue.push(items.map((item) => item.state)); },
    renderApplied: (kind, text) => { state.applied.push([kind, text]); },
    notify: (message) => { state.notices.push(message); },
    ...overrides,
  };
  return { state, host };
}

describe("TurnInputSession 接入状态", () => {
  it("input.applied 先于 accepted 到达：渲染一次，之后既不排队也不恢复草稿", async () => {
    let release!: () => void;
    const gate = new Promise<void>((resolve) => { release = resolve; });
    const { state, host } = fakeHost({ request: async () => { await gate; return new Response(JSON.stringify({ status: "accepted", id: "i1" })); } });
    state.draft = "改用 Go";
    const session = new TurnInputSession("t", "w", host);
    const submitted = session.submit("steering");
    session.onApplied({ item_id: "i1", phase: "steering", text: "改用 Go" });
    expect(state.applied).toEqual([["steering", "改用 Go"]]);
    expect(session.tracker.queued()).toEqual([]);
    release();
    await submitted;
    session.finish([]);
    expect(session.tracker.items[0].state).toBe("applied");
    expect(state.draft).toBe("");
    expect(state.notices).toEqual([]);
  });

  it("请求期间会话切换后失败，不把草稿恢复到另一会话", async () => {
    const { state, host } = fakeHost({ request: async () => { state.scope = "w:2"; throw Object.assign(new Error("gone"), { status: 409 }); } });
    state.draft = "纠偏";
    const session = new TurnInputSession("t", "w", host);
    await session.submit("steering");
    expect(state.draft).toBe("");
    expect(state.notices[0]).toContain("未恢复");
  });

  it("同一会话内 closed 失败恢复草稿；turn 结束后迟到的 accepted 也恢复", async () => {
    const closed = fakeHost({ request: async () => { throw Object.assign(new Error("closed"), { status: 409 }); } });
    closed.state.draft = "A";
    await new TurnInputSession("t", "w", closed.host).submit("follow_up");
    expect(closed.state.draft).toBe("A");

    let release!: () => void;
    const gate = new Promise<void>((resolve) => { release = resolve; });
    const late = fakeHost({ request: async () => { await gate; return new Response(JSON.stringify({ status: "accepted", id: "i9" })); } });
    late.state.draft = "B";
    const session = new TurnInputSession("t", "w", late.host);
    const submitted = session.submit("steering");
    session.finish([]);
    release();
    await submitted;
    expect(late.state.draft).toBe("B");
  });

  it("结束时服务端返回的未消费输入恢复为草稿，重复 finish 不重复恢复", async () => {
    const { state, host } = fakeHost();
    state.draft = "排队";
    const session = new TurnInputSession("t", "w", host);
    await session.submit("follow_up");
    session.finish([{ id: "i1", kind: "follow_up", text: "排队" }]);
    session.finish([{ id: "i1", kind: "follow_up", text: "排队" }]);
    expect(state.draft).toBe("排队");
  });
});
