import type { ToolEventPayload } from "./types";

export type ToolState = "running" | "succeeded" | "failed" | "killed";

export interface ToolResultCard {
  root: HTMLElement;
  status: HTMLElement;
  result: HTMLElement;
  state: ToolState;
  jobID?: string;
}

export function normalizeToolResult(payload: ToolEventPayload): ToolEventPayload {
  const result = { ...payload };
  const text = (payload.preview || "").trim();
  const failed = text.startsWith("Error:");
  if (["bash", "bash_wait", "bash_kill"].includes(payload.name || "") && !payload.execution_status) {
    const output = failed ? text.split("\n\nOutput before failure:\n").slice(1).join("\n\nOutput before failure:\n") : text;
    try {
      const job = JSON.parse(output);
      if (job && typeof job.job_id === "string" && job.job_id && ["running", "completed", "failed", "killed"].includes(job.status)) {
        result.job_id = job.job_id;
        result.execution_status = job.status;
        if (Number.isInteger(job.exit_code)) result.exit_code = job.exit_code;
        result.result_delivered = job.status !== "running";
      }
    } catch (_) {}
  }
  result.status = result.execution_status === "completed" ? "succeeded" : result.execution_status || result.status || "succeeded";
  if ((failed || result.call_status === "error") && result.execution_status !== "killed") result.status = "failed";
  return result;
}

// restoredToolPayload keeps restored execution metadata so the card is not
// re-inferred from its output (a cleaned-up job stays undelivered).
export function restoredToolPayload(tool: ToolEventPayload, index: number): ToolEventPayload {
  return { ...tool, id: tool.id || "history-tool-" + (index + 1) };
}

export function applyToolResult(cards: Record<string, ToolResultCard>, current: ToolResultCard, payload: ToolEventPayload): void {
  const result = normalizeToolResult(payload);
  if (result.job_id) current.jobID = result.job_id;
  renderToolResult(current, result);
  if (result.job_id && result.execution_status && result.execution_status !== "running") {
    for (const card of Object.values(cards)) {
      if (card !== current && card.jobID === result.job_id && card.state === "running") renderToolResult(card, result);
    }
  }
}

function renderToolResult(card: ToolResultCard, payload: ToolEventPayload): void {
  const state: ToolState = payload.status === "running" ? "running" : payload.status === "killed" ? "killed" : payload.status === "failed" || payload.status === "error" ? "failed" : "succeeded";
  card.state = state;
  card.root.classList.remove("succeeded", "failed");
  if (state !== "running") card.root.classList.add(state === "succeeded" ? "succeeded" : "failed");
  const label = { running: "后台运行中", succeeded: "完成", failed: "失败", killed: "已终止" }[state];
  const duration = payload.duration_ms ? "，" + payload.duration_ms + "ms" : "";
  const truncated = payload.truncated ? "，已截断" : "";
  card.status.textContent = label + duration + truncated;
  const details: string[] = [];
  if (payload.call_status) details.push("工具调用：" + (payload.call_status === "error" ? "失败" : "成功"));
  if (payload.job_id) details.push("任务 ID：" + payload.job_id);
  if (payload.execution_status) details.push("命令状态：" + payload.execution_status);
  if (payload.exit_code !== undefined) details.push("退出码：" + payload.exit_code);
  if (payload.result_delivered !== undefined) details.push("最终结果：" + (payload.result_delivered ? "已交付" : "待交付"));
  card.status.title = details.join("\n");
  card.result.textContent = payload.preview || (state === "running" ? "等待最终执行结果…" : state === "failed" ? "工具执行失败" : "（无输出）");
}
