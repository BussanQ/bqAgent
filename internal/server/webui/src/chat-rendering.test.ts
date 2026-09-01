import { describe, expect, it } from "vitest";
import { groupHistoryMessages, historyAssistantView, normalizeHistoryMessages, parseCompletedToolActivity } from "./chat-rendering";

const folded = [
  "Completed tool activity (retain this evidence for later reasoning):",
  "Assistant note: 先记下位置",
  `Calls: [{"id":"call-1","type":"function","function":{"name":"memory","arguments":"{\\"action\\":\\"add\\",\\"content\\":\\"用户在北京\\",\\"target\\":\\"global\\"}"}}]`,
  "Result call-1:",
  `{"target":"global","path":"/Users/haomi/.agent/memory/entries.jsonl"}`,
].join("\n");

describe("历史工具摘要解析", () => {
  it("把 compact 工具记录还原成卡片数据", () => {
    const view = parseCompletedToolActivity(folded);
    expect(view.content).toBe("先记下位置");
    expect(view.tools).toHaveLength(1);
    expect(view.tools[0]).toMatchObject({
      id: "call-1",
      name: "memory",
      status: "succeeded",
      arguments: { action: "add", content: "用户在北京", target: "global" },
    });
    expect(view.tools[0].preview).toContain("entries.jsonl");
  });

  it("对齐包含换行的 tool call id", () => {
    const id = "call-1\nfc_abc";
    const view = parseCompletedToolActivity([
      "Completed tool activity (retain this evidence for later reasoning):",
      `Calls: [{"id":${JSON.stringify(id)},"function":{"name":"glob","arguments":"{\\"pattern\\":\\"*\\"}"}}]`,
      `Result ${id}:`,
      ".DS_Store",
      "build.sh",
    ].join("\n"));
    expect(view.tools[0]).toMatchObject({ name: "glob", preview: ".DS_Store\nbuild.sh" });
  });

  it("普通回复保持原样", () => {
    expect(parseCompletedToolActivity("已经写入全局记忆。")).toEqual({
      content: "已经写入全局记忆。",
      tools: [],
    });
  });

  it("合并连续的工具记录和最终回复", () => {
    const grouped = groupHistoryMessages([
      { role: "user", content: "写入全局记忆，我在北京" },
      { role: "assistant", content: "", tools: [{ id: "a", name: "memory", result: "saved" }] },
      { role: "assistant", content: "", tools: [{ id: "b", name: "execute_bash", result: "ok" }] },
      { role: "assistant", content: "已经写入全局记忆。" },
    ]);
    expect(grouped).toHaveLength(2);
    expect(grouped[1].tools).toHaveLength(2);
    expect(grouped[1].content).toBe("已经写入全局记忆。");
  });

  it("先解析再合并 compact 工具记录", () => {
    const toolOnly = [
      "Completed tool activity (retain this evidence for later reasoning):",
      `Calls: [{"id":"call-1","function":{"name":"memory","arguments":"{}"}}]`,
      "Result call-1:",
      "saved",
    ].join("\n");
    const grouped = normalizeHistoryMessages([
      { role: "user", content: "分析当前项目" },
      { role: "assistant", content: toolOnly },
      { role: "assistant", content: toolOnly.replaceAll("memory", "execute_bash").replaceAll("call-1", "call-2") },
      { role: "assistant", content: "已经看完项目结构。" },
    ]);
    expect(grouped).toHaveLength(2);
    expect(grouped[1].tools).toHaveLength(2);
    expect(grouped[1].content).toBe("已经看完项目结构。");
  });

  it("优先使用接口返回的 tools", () => {
    const view = historyAssistantView({
      role: "assistant",
      content: "",
      tools: [{ id: "call-1", name: "memory", arguments: { target: "global" }, result: "saved", status: "succeeded" }],
    });
    expect(view.tools[0]).toMatchObject({ id: "call-1", name: "memory", preview: "saved" });
  });

  it("保留历史用户消息的文件附件", () => {
    const messages = normalizeHistoryMessages([{
      role: "user",
      content: "查看",
      files: [{ name: "TODO.md", path: "docs/TODO.md" }],
    }]);

    expect(messages).toEqual([{
      role: "user",
      content: "查看",
      tools: [],
      files: [{ name: "TODO.md", path: "docs/TODO.md" }],
    }]);
  });

  it("不合并不同群聊成员的连续回复", () => {
    const messages = normalizeHistoryMessages([
      { role: "assistant", sender: "codex", content: "Codex 结论" },
      { role: "assistant", sender: "opencode", content: "OpenCode 结论" },
      { role: "assistant", sender: "bqagent", content: "汇总" },
    ]);
    expect(messages.map((message) => message.sender)).toEqual(["codex", "opencode", "bqagent"]);
  });
});
