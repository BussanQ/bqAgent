// @vitest-environment jsdom
import { afterEach, describe, expect, it } from "vitest";
import { updateToolGroupSummary, updateToolTimeline, type ToolCards, type ToolTimeline } from "./tool-timeline";
import type { ToolSegment } from "./tool-status";
import type { ToolEventPayload } from "./types";

function segment() {
  const stack = document.createElement("div");
  const bubble = document.createElement("div");
  stack.appendChild(bubble);
  document.body.appendChild(stack);
  const cards: ToolCards = {};
  return { stack, bubble, cards };
}

function archive(part: ReturnType<typeof segment>): ToolSegment {
  const timeline = part.stack.querySelector<ToolTimeline>(".tool-timeline")!;
  return { cards: part.cards, refresh: () => updateToolGroupSummary(timeline, part.cards) };
}

function startBackground(part: ReturnType<typeof segment>) {
  updateToolTimeline(part.bubble, part.cards, "tool_start", { id: "start", name: "bash", arguments: { command: "sleep 1; printf done" } });
  updateToolTimeline(part.bubble, part.cards, "tool_result", {
    id: "start", name: "bash", job_id: "job", execution_status: "running", call_status: "success", result_delivered: false,
  });
}

function completeOrdinaryCalls(part: ReturnType<typeof segment>) {
  for (const id of ["read-1", "read-2"]) {
    updateToolTimeline(part.bubble, part.cards, "tool_start", { id, name: "read_file", arguments: { path: id } });
    updateToolTimeline(part.bubble, part.cards, "tool_result", { id, name: "read_file", status: "succeeded", preview: "text" });
  }
}

afterEach(() => { document.body.replaceChildren(); });

describe("跨消息段工具卡片渲染", () => {
  it.each([
    { execution_status: "completed", label: "完成", result_delivered: true },
    { execution_status: "failed", label: "失败", result_delivered: true },
    { execution_status: "killed", label: "已终止", result_delivered: false },
  ])("同一调用的 $execution_status 结果只更新原卡片", ({ execution_status, label, result_delivered }) => {
    const old = segment();
    startBackground(old);
    const root = old.cards.start.root;
    const args = root.querySelector(".tool-json")!.textContent;
    const toggle = root.querySelector<HTMLButtonElement>(".tool-toggle")!;
    toggle.click();
    const current = segment();
    const previous = [archive(old)];
    const final: ToolEventPayload = {
      id: "start", name: "bash", job_id: "job", execution_status,
      call_status: execution_status === "failed" ? "error" : "success", result_delivered, preview: "final output",
    };
    updateToolTimeline(current.bubble, current.cards, "tool_result", final, previous);
    // Replaying final delivery must not add another call either.
    updateToolTimeline(current.bubble, current.cards, "tool_result", final, previous);
    expect(document.querySelectorAll(".tool-card")).toHaveLength(1);
    expect(current.stack.querySelector(".tool-timeline")).toBeNull();
    expect(Object.keys(current.cards)).toHaveLength(0);
    expect(old.cards.start.root).toBe(root);
    expect(root.querySelector(".tool-json")!.textContent).toBe(args);
    expect(root.querySelector(".tool-preview")!.textContent).toBe("final output");
    expect(root.querySelector(".tool-status")!.textContent).toBe(label);
    expect(old.cards.start.status.title).toContain(result_delivered ? "最终结果：已交付" : "最终结果：待交付");
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    expect(root.querySelector(".tool-details")!.classList.contains("open")).toBe(true);
  });

  it("自动交付同步刷新旧段及当前段中同 job 的分组摘要", () => {
    const old = segment();
    startBackground(old);
    completeOrdinaryCalls(old);
    const current = segment();
    const previous = [archive(old)];
    updateToolTimeline(current.bubble, current.cards, "tool_start", { id: "wait", name: "bash_wait", arguments: { job_id: "job" } }, previous);
    updateToolTimeline(current.bubble, current.cards, "tool_result", { id: "wait", name: "bash_wait", job_id: "job", execution_status: "running" }, previous);
    completeOrdinaryCalls(current);
    expect(old.stack.querySelector(".tool-group-toggle .tool-status")!.textContent).toBe("3 次，1 运行中");
    expect(current.stack.querySelector(".tool-group-toggle .tool-status")!.textContent).toBe("3 次，1 运行中");
    updateToolTimeline(current.bubble, current.cards, "tool_result", {
      id: "start", name: "bash", job_id: "job", execution_status: "completed", result_delivered: true, preview: "done",
    }, previous);
    expect(document.querySelectorAll(".tool-card")).toHaveLength(6);
    for (const part of [old, current]) {
      expect(part.stack.querySelector(".tool-group-toggle .tool-status")!.textContent).toBe("3 次，全部完成");
      expect(part.stack.querySelector(".tool-group")!.classList.contains("succeeded")).toBe(true);
    }
  });

  it("旧段原调用的结果未带 job 元数据时也会刷新摘要", () => {
    const old = segment();
    updateToolTimeline(old.bubble, old.cards, "tool_start", { id: "pending", name: "read_file", arguments: { path: "pending.txt" } });
    completeOrdinaryCalls(old);
    const current = segment();
    updateToolTimeline(current.bubble, current.cards, "tool_result", {
      id: "pending", name: "read_file", status: "succeeded", preview: "content",
    }, [archive(old)]);
    expect(document.querySelectorAll(".tool-card")).toHaveLength(3);
    expect(current.stack.querySelector(".tool-timeline")).toBeNull();
    expect(old.stack.querySelector(".tool-group-toggle .tool-status")!.textContent).toBe("3 次，全部完成");
  });

  it("显式 bash_wait 保留独立卡片并完成旧段中的原调用", () => {
    const old = segment();
    startBackground(old);
    const current = segment();
    const previous = [archive(old)];
    updateToolTimeline(current.bubble, current.cards, "tool_start", { id: "wait", name: "bash_wait", arguments: { job_id: "job" } }, previous);
    updateToolTimeline(current.bubble, current.cards, "tool_result", {
      id: "wait", name: "bash_wait", job_id: "job", execution_status: "completed", exit_code: 0, result_delivered: true,
    }, previous);
    expect(document.querySelectorAll(".tool-card")).toHaveLength(2);
    expect(old.cards.start.state).toBe("succeeded");
    expect(current.cards.wait.state).toBe("succeeded");
    expect(JSON.parse(current.cards.wait.arguments.textContent!)).toEqual({ job_id: "job" });
  });

  it("没有本轮历史段时同名调用创建自己的卡片", () => {
    const old = segment();
    startBackground(old);
    const otherTurn = segment();
    updateToolTimeline(otherTurn.bubble, otherTurn.cards, "tool_result", {
      id: "start", name: "bash", job_id: "job", execution_status: "completed",
    });
    expect(document.querySelectorAll(".tool-card")).toHaveLength(2);
    expect(old.cards.start.state).toBe("running");
    expect(otherTurn.cards.start.state).toBe("succeeded");
  });
});
