import { describe, expect, it } from "vitest";
import { addableGroupParticipants, groupMentionQuery, matchingGroupParticipants, normalizeConversationType, replaceGroupMention, shouldRenderFinalReply } from "./group-chat";

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

  it("外部成员直达结果不再渲染 bqagent 最终气泡", () => {
    expect(shouldRenderFinalReply("group", "participant_results")).toBe(false);
    expect(shouldRenderFinalReply("group", "coordinator")).toBe(true);
    expect(shouldRenderFinalReply("default", "participant_results")).toBe(true);
  });

  it("只列出当前可用且尚未加入的外部成员", () => {
    const candidates = addableGroupParticipants(
      { scheduler: "bqagent", participants: participants },
      { scheduler: "bqagent", participants: participants.slice(0, 2) },
    );
    expect(candidates).toEqual([]);
    expect(addableGroupParticipants(
      { scheduler: "bqagent", participants: participants.map(function (participant) { return participant.id === "opencode" ? { ...participant, available: true } : participant; }) },
      { scheduler: "bqagent", participants: participants.slice(0, 2) },
    ).map(function (participant) { return participant.id; })).toEqual(["opencode"]);
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
