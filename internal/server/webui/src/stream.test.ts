import { describe, expect, it } from "vitest";
import { SSEBuffer } from "./stream";

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
