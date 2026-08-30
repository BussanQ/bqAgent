import type { ConversationHistoryTool, ConversationMessage, ToolEventPayload } from "./types";

const COMPLETED_TOOL_ACTIVITY = "Completed tool activity (retain this evidence for later reasoning):";
const COMPLETED_TOOL_RESULT = "Completed tool result:";
const ASSISTANT_NOTE = "Assistant note: ";
const CALLS_LEAD = "Calls: ";

export interface HistoryAssistantView {
  content: string;
  tools: ToolEventPayload[];
}

export function complexTaskNotice(text: string): string {
  const match = String(text || "").match(/^Agent stopped: reached maximum of (\d+) iterations without completing\.$/);
  if (!match) return "";
  return [
    "复杂任务还没有完成。",
    "",
    `本轮已触达失控保险上限（${match[1]} 次迭代）。默认已开启自动压缩续跑，正常很难碰到这个上限；你可以继续发送“继续”，我会沿当前会话接着处理。如确实需要更长的单轮，可在 \`.env\` 中调大 \`AGENT_MAX_ITERATIONS\` 后重启服务。`,
  ].join("\n");
}

export function normalizeHistoryMessages(messages: ConversationMessage[]): ConversationMessage[] {
  return groupHistoryMessages((messages || []).map(function (message) {
    if (message.role !== "assistant") {
      return { role: message.role, content: message.content || "", tools: [] };
    }
    const view = historyAssistantView(message);
    return {
      role: "assistant",
      content: view.content,
      tools: (view.tools || []).map(function (tool) {
        return {
          id: tool.id,
          name: tool.name || "tool",
          arguments: tool.arguments,
          result: tool.preview,
          status: tool.status,
          truncated: tool.truncated,
        };
      }),
    };
  }));
}

export function groupHistoryMessages(messages: ConversationMessage[]): ConversationMessage[] {
  const grouped: ConversationMessage[] = [];
  (messages || []).forEach(function (message) {
    const current: ConversationMessage = {
      role: message.role,
      content: message.content || "",
      tools: message.tools ? message.tools.slice() : [],
    };
    const previous = grouped[grouped.length - 1];
    if (!previous || previous.role !== "assistant" || current.role !== "assistant") {
      grouped.push(current);
      return;
    }
    const previousToolsOnly = (previous.tools || []).length > 0 && !String(previous.content || "").trim();
    const currentToolsOnly = (current.tools || []).length > 0 && !String(current.content || "").trim();
    if (currentToolsOnly || previousToolsOnly) {
      previous.tools = (previous.tools || []).concat(current.tools || []);
      if (String(current.content || "").trim()) previous.content = current.content;
      return;
    }
    grouped.push(current);
  });
  return grouped;
}

export function historyAssistantView(message: ConversationMessage): HistoryAssistantView {
  if (message.tools && message.tools.length) {
    return { content: String(message.content || "").trim(), tools: message.tools.map(historyToolEvent) };
  }
  return parseCompletedToolActivity(String(message.content || ""));
}

export function parseCompletedToolActivity(content: string): HistoryAssistantView {
  const text = String(content || "").trim();
  if (!text) return { content: "", tools: [] };
  if (text.startsWith(COMPLETED_TOOL_RESULT)) {
    const result = text.slice(COMPLETED_TOOL_RESULT.length).trim();
    return { content: "", tools: [{ name: "tool", status: historyToolStatus(result), preview: result }] };
  }
  if (!text.startsWith(COMPLETED_TOOL_ACTIVITY)) {
    return { content: text, tools: [] };
  }
  let body = text.slice(COMPLETED_TOOL_ACTIVITY.length).trim();
  let note = "";
  if (body.startsWith(ASSISTANT_NOTE)) {
    const rest = body.slice(ASSISTANT_NOTE.length);
    const callsAt = rest.indexOf("\n" + CALLS_LEAD);
    const resultAt = rest.indexOf("\nResult ");
    if (callsAt >= 0) {
      note = rest.slice(0, callsAt).trim();
      body = rest.slice(callsAt + 1).trim();
    } else if (resultAt >= 0) {
      note = rest.slice(0, resultAt).trim();
      body = rest.slice(resultAt + 1).trim();
    } else {
      note = rest.trim();
      body = "";
    }
  }
  let calls: Array<{ id?: string; function?: { name?: string; arguments?: string | Record<string, unknown> } }> = [];
  if (body.startsWith(CALLS_LEAD)) {
    const payload = body.slice(CALLS_LEAD.length);
    const lineEnd = payload.indexOf("\n");
    const jsonText = lineEnd >= 0 ? payload.slice(0, lineEnd) : payload;
    try {
      const parsed = JSON.parse(jsonText);
      if (Array.isArray(parsed)) {
        calls = parsed;
        body = lineEnd >= 0 ? payload.slice(lineEnd + 1).trim() : "";
      }
    } catch (_) {}
  }
  const results = extractCompletedToolResults(body, calls.map(function (call) { return call && call.id ? String(call.id) : ""; }));
  const tools: ToolEventPayload[] = [];
  const seen: Record<string, boolean> = Object.create(null);
  calls.forEach(function (call) {
    const id = call && call.id ? String(call.id) : "";
    const result = results[id] || "";
    tools.push({
      id: id || undefined,
      name: call && call.function && call.function.name ? String(call.function.name) : "tool",
      arguments: parseToolArguments(call && call.function ? call.function.arguments : undefined),
      status: historyToolStatus(result),
      preview: result,
    });
    if (id) seen[id] = true;
  });
  Object.keys(results).forEach(function (id) {
    if (seen[id]) return;
    tools.push({ id: id || undefined, name: "tool", status: historyToolStatus(results[id]), preview: results[id] });
  });
  return { content: note, tools: tools };
}

function historyToolEvent(tool: ConversationHistoryTool): ToolEventPayload {
  return {
    id: tool.id,
    name: tool.name,
    arguments: tool.arguments,
    status: tool.status || historyToolStatus(tool.result || ""),
    preview: tool.result,
    truncated: tool.truncated,
  };
}

function extractCompletedToolResults(body: string, ids: string[]): Record<string, string> {
  const results: Record<string, string> = Object.create(null);
  ids.forEach(function (id, index) {
    if (!id) return;
    const header = "Result " + id + ":\n";
    const startAt = body.indexOf(header);
    if (startAt < 0) return;
    let end = body.length;
    for (let next = index + 1; next < ids.length; next++) {
      if (!ids[next]) continue;
      const nextAt = body.indexOf("Result " + ids[next] + ":\n", startAt + header.length);
      if (nextAt >= 0) {
        end = nextAt;
        break;
      }
    }
    results[id] = body.slice(startAt + header.length, end).replace(/\n+$/, "");
  });
  if (Object.keys(results).length) return results;
  return parseCompletedToolResults(body);
}

function parseCompletedToolResults(body: string): Record<string, string> {
  const results: Record<string, string> = Object.create(null);
  const header = /^Result ([^\n:]*):\n/gm;
  const matches: Array<{ id: string; start: number; headerAt: number }> = [];
  let match: RegExpExecArray | null;
  while ((match = header.exec(body))) {
    matches.push({ id: match[1], start: match.index + match[0].length, headerAt: match.index });
  }
  matches.forEach(function (item, index) {
    const end = index + 1 < matches.length ? matches[index + 1].headerAt : body.length;
    results[item.id] = body.slice(item.start, end).replace(/\n+$/, "");
  });
  return results;
}

function parseToolArguments(value: string | Record<string, unknown> | undefined): Record<string, unknown> | undefined {
  if (!value) return undefined;
  if (typeof value === "object") return value;
  try {
    const parsed = JSON.parse(value);
    return parsed && typeof parsed === "object" ? parsed as Record<string, unknown> : { raw: value };
  } catch (_) {
    return { raw: value };
  }
}

function historyToolStatus(result: string): string {
  return String(result || "").trim().startsWith("Error:") ? "failed" : "succeeded";
}
