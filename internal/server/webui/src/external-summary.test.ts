import { expect, it } from "vitest";
import { normalizeHistoryMessages } from "./chat-rendering";
import { renderAgentSummary } from "./external-summary";
import { ExternalTimelineView } from "./external-timeline";

it("renders indexed Codex summaries in one disclosure across tool and user boundaries", () => {
  const thread = document.createElement("main");
  const timeline = new ExternalTimelineView(() => {
    const row = document.createElement("section"), bubble = document.createElement("div");
    row.className = "msg";
    row.append(bubble);
    thread.append(row);
    return bubble;
  }, (bubble, text) => { bubble.textContent = text; });
  const item = { agent: "codex", id: "old/summary/r" };
  timeline.summary({ ...item, index: 1, text: "old", append: true });
  const details = thread.querySelector("details")!;
  expect(details.open).toBe(false);
  expect(details.textContent).toBe("正在生成推理摘要");
  timeline.tool("tool_start", { id: "t", name: "read", status: "running" }, "codex");
  timeline.boundary();
  thread.append(document.createElement("aside"));
  details.open = true;
  timeline.summary({ ...item, index: 0, text: "**First** <script>bad</script>" });
  timeline.summary({ ...item, index: 1, text: "revised" });
  timeline.text({ agent: "codex", id: "answer", text: "answer" });
  timeline.summary({ ...item, index: 0, text: "", append: true, ended: true });
  expect(timeline.finish()?.text).toBe("answer");
  expect(thread.children).toHaveLength(4);
  expect(thread.querySelectorAll(".agent-summary")).toHaveLength(1);
  expect(thread.firstElementChild!.contains(details)).toBe(true);
  expect(details.open).toBe(true);
  expect(details.querySelector("summary")?.textContent).toBe("推理摘要");
  expect(details.querySelector("strong")?.textContent).toBe("First");
  expect(details.querySelector("script")).toBeNull();
  expect(details.querySelector(".agent-summary-body")!.textContent).toMatch(/First[\s\S]*revised/);
  expect(details.textContent).not.toContain("old");
});

it("restores the first summary position without duplicating the answer or exposing an empty summary", () => {
  const rows = normalizeHistoryMessages([{ role: "assistant", sender: "codex", content: "answer", parts: [
    { kind: "summary", id: "r/1", summary: { item: "r", index: 1, ended: true }, text: "two" },
    { kind: "tool", id: "read" },
    { kind: "summary", id: "r/0", summary: { item: "r", index: 0, truncated: true }, text: "one" },
    { kind: "summary", id: "empty/0", summary: { item: "empty", index: 0 }, text: "" },
    { kind: "text", id: "reply", text: "answer" },
  ], tools: [{ id: "read", name: "Read" }] }]);
  expect(rows).toHaveLength(3);
  expect(rows.map(row => row.content)).toEqual(["", "", "answer"]);
  expect(rows[1].tools?.[0]?.name).toBe("Read");
  const bubble = document.createElement("div");
  renderAgentSummary(bubble, rows[0].summary!, false);
  const details = bubble.querySelector("details")!;
  details.open = true;
  details.dispatchEvent(new Event("toggle"));
  expect(details.textContent).toMatch(/one[\s\S]*two[\s\S]*摘要已截断/);
  details.open = false;
  details.dispatchEvent(new Event("toggle"));
  expect(details.textContent).toBe("推理摘要");
});
