import { readFileSync } from "node:fs";
import { beforeEach, describe, expect, it } from "vitest";
import { MessageSnapshots, SSEBuffer } from "./stream";
import { TaskProgress, renderTaskPanel } from "./task-progress";
import type { MessageSnapshot } from "./types";

const task = (over: Record<string, unknown> = {}) => ({
  goal: "修复登录",
  status: "active",
  criteria: [{ description: "单测通过", done: true, evidence: "go test ok" }, { description: "文档更新", done: false }],
  remaining: ["更新文档"],
  verification: [],
  reason: "",
  updated_at: "2026-10-17T00:00:00Z",
  ...over,
});
const snap = (seq: number, state: unknown, turn = "t1"): MessageSnapshot => ({
  turn_id: turn, item_id: turn + "/task", seq, kind: "task.updated", phase: "status", text: state === null ? "null" : JSON.stringify(state),
});

let panel: HTMLElement;
beforeEach(() => {
  const html = readFileSync("index.html", "utf8");
  document.body.innerHTML = html.slice(html.indexOf("<body"), html.indexOf("</body>") + 7);
  panel = document.getElementById("task-panel")!;
});

describe("任务面板渲染", () => {
  it("无任务时隐藏", () => {
    renderTaskPanel(panel, null);
    expect(panel.hidden).toBe(true);
    expect(panel.textContent).toBe("");
  });

  it("展示目标、状态、验收计数与项、剩余和原因", () => {
    const tasks = new TaskProgress();
    tasks.restore(task({ status: "blocked", reason: "缺少权限" }));
    renderTaskPanel(panel, tasks.state);
    expect(panel.hidden).toBe(false);
    expect(panel.querySelector(".task-panel-goal")!.textContent).toBe("修复登录");
    expect(panel.querySelector(".task-panel-status")!.textContent).toBe("已阻塞");
    expect(panel.querySelector(".task-panel-count")!.textContent).toBe("验收 1/2");
    expect(panel.querySelectorAll(".task-panel-criterion.done")).toHaveLength(1);
    expect(panel.querySelectorAll(".task-panel-criterion")).toHaveLength(2);
    expect(panel.querySelector(".task-panel-remaining")!.textContent).toContain("更新文档");
    expect(panel.querySelector(".task-panel-reason")!.textContent).toContain("缺少权限");
  });

  it("验收项全部完成但状态仍是 active 时不显示任务完成", () => {
    const tasks = new TaskProgress();
    tasks.restore(task({ criteria: [{ description: "a", done: true }], remaining: [] }));
    renderTaskPanel(panel, tasks.state);
    expect(panel.querySelector(".task-panel-count")!.textContent).toBe("验收 1/1");
    expect(panel.querySelector(".task-panel-status")!.textContent).toBe("进行中");
    expect(panel.textContent).not.toContain("已完成");
  });

  it("以文本渲染内容，不解析 HTML", () => {
    const tasks = new TaskProgress();
    tasks.restore(task({ goal: "<img src=x onerror=alert(1)>" }));
    renderTaskPanel(panel, tasks.state);
    expect(panel.querySelector("img")).toBeNull();
  });
});

describe("任务快照事件", () => {
  it("从 SSE message_snapshot 的 task.updated 更新，且不影响助手消息内容", () => {
    const sse = new SSEBuffer();
    const messages = new MessageSnapshots("t1");
    const tasks = new TaskProgress();
    const frame = (s: MessageSnapshot) => "event: message_snapshot\ndata: " + JSON.stringify(s) + "\n\n";
    const message: MessageSnapshot = { turn_id: "t1", item_id: "a", seq: 1, kind: "message.updated", phase: "draft", text: "回复" };
    const events = sse.push(frame(snap(5, task())) + frame(message) + frame(snap(2, task({ goal: "旧" }))));
    for (const event of events) {
      const data = JSON.parse(event.data) as MessageSnapshot;
      if (data.kind === "task.updated") tasks.apply(data); else messages.apply(data);
    }
    expect(tasks.state!.goal).toBe("修复登录");
    expect(messages.latest!.text).toBe("回复");
    expect(messages.items.size).toBe(1);
  });

  it("重复和乱序快照不倒退，null 清除任务", () => {
    const tasks = new TaskProgress();
    expect(tasks.apply(snap(3, task()))).toBe(true);
    expect(tasks.apply(snap(3, task({ goal: "重复" })))).toBe(false);
    expect(tasks.apply(snap(2, task({ goal: "乱序" })))).toBe(false);
    expect(tasks.state!.goal).toBe("修复登录");
    expect(tasks.apply(snap(4, null))).toBe(true);
    expect(tasks.state).toBeNull();
    renderTaskPanel(panel, tasks.state);
    expect(panel.hidden).toBe(true);
  });
});

describe("历史恢复与会话隔离", () => {
  it("恢复历史立即显示 task，缺失或 null 时隐藏", () => {
    const tasks = new TaskProgress();
    tasks.restore(task(), "1");
    renderTaskPanel(panel, tasks.state);
    expect(panel.hidden).toBe(false);
    tasks.restore(undefined, "2");
    renderTaskPanel(panel, tasks.state);
    expect(panel.hidden).toBe(true);
  });

  it("会话切换后迟到的旧会话快照被丢弃，序号记忆也被清除", () => {
    const tasks = new TaskProgress();
    tasks.reset("1");
    expect(tasks.apply(snap(9, task({ goal: "A" })), "1")).toBe(true);
    tasks.restore(task({ goal: "B" }), "2");
    expect(tasks.apply(snap(10, task({ goal: "A2" })), "1")).toBe(false);
    expect(tasks.state!.goal).toBe("B");
    expect(tasks.apply(snap(1, task({ goal: "C" })), "2")).toBe(true);
    expect(tasks.state!.goal).toBe("C");
  });
});

describe("严格校验与作用域初始化", () => {
  it("以 main 的初始作用域 0 构造后，首次发送的快照可被接受", () => {
    const tasks = new TaskProgress("0");
    expect(tasks.apply(snap(1, task()), "0", "t1")).toBe(true);
    expect(tasks.state!.goal).toBe("修复登录");
  });

  it("非法快照不会清除有效状态也不推进 seq，仅 JSON null 清除", () => {
    const tasks = new TaskProgress("0");
    tasks.apply(snap(1, task()), "0");
    const bad = (seq: number, text: string): MessageSnapshot => ({ ...snap(seq, null), text });
    for (const text of ["{", "", JSON.stringify(task({ criteria: [] })), JSON.stringify(task({ remaining: "x" })), JSON.stringify(task({ status: "toString" })), JSON.stringify(task({ status: "__proto__" })), "[]", JSON.stringify(task({ status: "weird" })), JSON.stringify(task({ criteria: "x" })), JSON.stringify(task({ criteria: [{ description: "a" }] })), JSON.stringify(task({ goal: "" }))]) {
      expect(tasks.apply(bad(50, text), "0")).toBe(false);
    }
    expect(tasks.state!.goal).toBe("修复登录");
    // seq 50 was never consumed by the invalid events.
    expect(tasks.apply(snap(2, task({ goal: "新" })), "0")).toBe(true);
    expect(tasks.apply(snap(3, null), "0")).toBe(true);
    expect(tasks.state).toBeNull();
  });

  it("非法历史 task 视为无任务", () => {
    const tasks = new TaskProgress("0");
    tasks.restore(task({ status: "weird" }));
    expect(tasks.state).toBeNull();
  });

  it("同 turn 内以全局 seq 排序，不同 item_id 也不倒退", () => {
    const tasks = new TaskProgress("0");
    tasks.apply(snap(5, task()), "0");
    const other = { ...snap(4, task({ goal: "倒退" })), item_id: "t1/task" };
    expect(tasks.apply(other, "0")).toBe(false);
    expect(tasks.apply({ ...snap(6, task()), item_id: "t1/other" }, "0")).toBe(false);
    expect(tasks.state!.goal).toBe("修复登录");
  });

  it("拒绝同作用域下不同 turn 的快照", () => {
    const tasks = new TaskProgress("0");
    expect(tasks.apply(snap(1, task({ goal: "旧轮" }), "old"), "0", "t1")).toBe(false);
    expect(tasks.state).toBeNull();
    expect(tasks.apply(snap(1, task(), "t1"), "0", "t1")).toBe(true);
  });
});

describe("Go taskstate 序列化兼容", () => {
  // Shape produced by encoding/json for a completed State with nil Remaining.
  const goJSON = (status: string, extra = "") => '{"goal":"发布","status":"' + status + '","criteria":[{"description":"测试","done":true,"evidence":"ok"}],"remaining":null,"verification":[{"command":"go test","status":"passed","details":"ok"}]' + extra + ',"updated_at":"2026-10-17T00:00:00.123456+08:00"}';

  it.each(["completed", "active"])("接受 remaining:null 的 %s 快照并渲染", (status) => {
    const tasks = new TaskProgress("0");
    expect(tasks.apply({ ...snap(1, null), text: goJSON(status) }, "0")).toBe(true);
    expect(tasks.state!.remaining).toEqual([]);
    renderTaskPanel(panel, tasks.state);
    expect(panel.hidden).toBe(false);
    expect(panel.querySelector(".task-panel-remaining")).toBeNull();
    expect(panel.querySelector(".task-panel-count")!.textContent).toBe("验收 1/1");
  });

  it("历史 task 对象同样接受 remaining:null", () => {
    const tasks = new TaskProgress("0");
    tasks.restore(JSON.parse(goJSON("completed", ',"reason":""')));
    expect(tasks.state!.status).toBe("completed");
  });
});
