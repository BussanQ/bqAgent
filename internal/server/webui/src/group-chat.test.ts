import { describe, expect, it } from "vitest";
import { groupMentionQuery, matchingGroupParticipants, normalizeConversationType, replaceGroupMention } from "./group-chat";

describe("群聊输入", () => {
  const participants = [
    { id: "bqagent", name: "bqagent", kind: "builtin", available: true },
    { id: "codex", name: "codex", kind: "external", available: true },
    { id: "opencode", name: "opencode", kind: "external", available: false },
  ];

  it("规范化会话类型", () => {
    expect(normalizeConversationType("group")).toBe("group");
    expect(normalizeConversationType("unknown")).toBe("default");
  });

  it("识别光标前的 @ 查询并替换", () => {
    const text = "请 @co";
    const query = groupMentionQuery(text, text.length);
    expect(query).toEqual({ start: 2, end: 5, query: "co" });
    expect(matchingGroupParticipants(participants, query!.query).map((item) => item.id)).toEqual(["codex"]);
    expect(replaceGroupMention(text, query!, "codex")).toEqual({ text: "请 @codex ", cursor: 9 });
  });

  it("不提示不可用成员或邮箱片段", () => {
    expect(matchingGroupParticipants(participants, "open")).toEqual([]);
    expect(groupMentionQuery("mail@example.com", 16)).toBeNull();
  });
});
