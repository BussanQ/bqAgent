import { describe, expect, it } from "vitest";
import { SSEBuffer, MessageSnapshots } from "./stream";

describe("SSEBuffer", () => {
  it("按任意分块顺序还原 SSE 事件并识别完成状态", () => {
    const stream = new SSEBuffer();
    expect(stream.push("event: pro")).toEqual([]);
    expect(stream.push("gress\r\ndata: {\"message\":\"处理中\"}\r\n\r")).toEqual([]);
    expect(stream.push("\nevent: done\ndata: {\"session_id\":\"s1\"}\n\n")).toEqual([
      { event: "progress", data: "{\"message\":\"处理中\"}" },
      { event: "done", data: "{\"session_id\":\"s1\"}" },
    ]);
    expect(stream.terminal).toBe(true);
    expect(stream.remainder()).toBe("");
  });

  it("没有 done、stopped 或 error 时保持未终止", () => {
    const stream = new SSEBuffer();
    expect(stream.push("data: {\"delta\":\"hello\"}\n\n")).toHaveLength(1);
    expect(stream.terminal).toBe(false);
    expect(stream.push("event: stopped\ndata: {}\n\n")).toHaveLength(1);
    expect(stream.terminal).toBe(true);
  });
});


describe("后台消息快照", () => {
  it("按 ID 替换草稿，重复和乱序事件不会累加", () => {
    const messages = new MessageSnapshots("t");
    const draft = { turn_id: "t", item_id: "a", seq: 1, kind: "message.updated" as const, phase: "draft", text: "旧草稿" };
    const final = { ...draft, seq: 2, kind: "message.completed" as const, phase: "final_answer", text: "主要用 Go" };
    expect(messages.apply(draft)).toBe(true);
    expect(messages.apply(final)).toBe(true);
    for (const event of [draft, final, { ...draft, seq: 3 }, { ...draft, turn_id: "other", seq: 4 }]) {
      expect(messages.apply(event)).toBe(false);
    }
    expect(messages.items.size).toBe(1);
    expect(messages.latest?.text).toBe("主要用 Go");
  });

  it("工具前的进度和最终回答使用不同记录，重复快照不新增记录", () => {
    const messages = new MessageSnapshots("t");
    const progress = { turn_id: "t", item_id: "progress", seq: 1, kind: "message.completed" as const, phase: "commentary", text: "正在检查" };
    const final = { ...progress, item_id: "answer", seq: 2, phase: "final_answer", text: "最终结论" };
    for (const event of [progress, progress, final, final, progress]) messages.apply(event);
    expect(messages.items.size).toBe(2);
    expect(messages.latest?.text).toBe("最终结论");
    expect(messages.items.get("progress")?.text).toBe("正在检查");
  });
});
