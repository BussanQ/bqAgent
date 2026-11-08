import { expect, it } from "vitest";
import { normalizeHistoryMessages } from "./chat-rendering";
import { renderAgentPlan } from "./external-plan";
import { ExternalTimelineView } from "./external-timeline";
import type { AgentPlan } from "./types";

it("updates native steps in place across user boundaries and keeps collapsed and cleared plans in order", () => {
  const thread = document.createElement("main");
  const view = new ExternalTimelineView(() => {
    const row = document.createElement("section"), bubble = document.createElement("div");
    row.className = "msg";
    row.append(bubble);
    thread.append(row);
    return bubble;
  }, (bubble, text) => { bubble.textContent = text; });
  const update = (plan: AgentPlan) => view.plan({ id:"s/steps", agent:"codex", plan, text:"" });
  view.text({ id:"a", agent:"codex", text:"review" });
  update({ mode:"steps", steps:[
    { text:"first", status:"pending" }, { text:"<img src=x onerror=bad>", status:"in_progress", priority:"high" },
  ] });
  const original = view.planBubble("s/steps")!;
  const details = original.querySelector("details")!;
  expect(details.open).toBe(true);
  expect(details.querySelector("img")).toBeNull();
  expect(details.textContent).toContain("进行中");
  expect(details.textContent).toContain("高优先级");
  details.open = false;
  view.boundary();
  thread.append(document.createElement("aside"));
  view.text({ id:"b", agent:"codex", text:"next" });
  update({ mode:"steps", steps:[{ text:"second", status:"completed" }, { text:"first", status:"pending" }] });
  expect(Array.from(details.querySelectorAll(".agent-plan-step"), node => node.textContent)).toEqual(["second","first"]);
  expect(details.textContent).toContain("已完成 1/2");
  update({ mode:"steps" });
  expect(original.closest<HTMLElement>(".msg")!.hidden).toBe(true);
  update({ mode:"steps", steps:[{ text:"only", status:"pending" }] });
  expect(original.closest<HTMLElement>(".msg")!.hidden).toBe(false);
  expect(view.planBubble("s/steps")).toBe(original);
  expect(details.open).toBe(false);
  expect(thread.children).toHaveLength(4);
  expect(thread.children[1].contains(original)).toBe(true);
});

it("renders the plan-only reply once for both streaming and restored history", () => {
  const thread = document.createElement("main");
  const view = new ExternalTimelineView(() => {
    const bubble = document.createElement("div");
    thread.append(bubble);
    return bubble;
  }, () => {});
  const plan: AgentPlan = { mode:"proposal" };
  const item = { id:"s/proposal", agent:"codex", plan };
  view.plan({ ...item, text:"Old ", append:true });
  view.plan({ ...item, text:"proposal", append:true });
  view.plan({ ...item, text:"**New** <script>bad</script>" });
  const final = view.planBubble(item.id)!;
  expect(view.finish()).toBeUndefined();
  expect(thread.children).toHaveLength(1);
  expect(final.querySelector("script")).toBeNull();
  const text = "**New** <script>bad</script>";
  const rows = normalizeHistoryMessages([{ role:"assistant", sender:"codex", content:text, parts:[
    { kind:"text", id:"comment", text:"before" },
    { kind:"plan", id:"steps", plan:{mode:"steps"} },
    { kind:"plan", id:"proposal", plan, text },
  ] }]);
  expect(rows.map(row => row.content)).toEqual(["before",text]);
  const restored = document.createElement("div");
  renderAgentPlan(restored, rows[1].plan!, rows[1].content);
  expect(restored.innerHTML).toBe(final.innerHTML);
});
