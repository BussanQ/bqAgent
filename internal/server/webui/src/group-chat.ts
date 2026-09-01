import type { ConversationType, GroupInfo, GroupParticipant } from "./types";

export function normalizeConversationType(value: unknown): ConversationType {
  return String(value || "").toLowerCase() === "group" ? "group" : "default";
}

export function shouldRenderFinalReply(conversationType: unknown, replyKind: unknown): boolean {
  return normalizeConversationType(conversationType) !== "group" || String(replyKind || "") !== "participant_results";
}

export function shouldCloseCoordinatorSegment(groupEventKind: unknown): boolean {
  return String(groupEventKind || "") === "participant_start";
}

export function addableGroupParticipants(available: GroupInfo, current: GroupInfo | null): GroupParticipant[] {
  const joined = new Set((current?.participants || []).map(function (participant) { return participant.id; }));
  return (available?.participants || []).filter(function (participant) {
    return participant.available && participant.id !== available.scheduler && !joined.has(participant.id);
  });
}

export function canRemoveGroupParticipant(group: GroupInfo | null, participant: GroupParticipant): boolean {
  return Boolean(group && participant && participant.id !== group.scheduler);
}

export interface MentionQuery {
  start: number;
  end: number;
  query: string;
}

export function groupMentionQuery(text: string, cursor: number): MentionQuery | null {
  const prefix = String(text || "").slice(0, Math.max(0, cursor));
  const match = prefix.match(/(?:^|\s)@([A-Za-z0-9_-]*)$/);
  if (!match || match.index == null) return null;
  const at = prefix.lastIndexOf("@", match.index + match[0].length);
  return { start: at, end: cursor, query: match[1].toLowerCase() };
}

export function matchingGroupParticipants(participants: GroupParticipant[], query: string): GroupParticipant[] {
  const needle = String(query || "").toLowerCase();
  return (participants || []).filter(function (participant) {
    return participant.available && participant.id.toLowerCase().startsWith(needle);
  });
}

export function replaceGroupMention(text: string, mention: MentionQuery, participant: string): { text: string; cursor: number } {
  const replacement = "@" + participant + " ";
  const updated = text.slice(0, mention.start) + replacement + text.slice(mention.end);
  return { text: updated, cursor: mention.start + replacement.length };
}
