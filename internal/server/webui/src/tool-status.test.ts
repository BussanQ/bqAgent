// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import { applyToolResult, normalizeToolResult, restoredToolPayload, type ToolResultCard } from "./tool-status";
import { historyAssistantView, normalizeHistoryMessages, parseCompletedToolActivity } from "./chat-rendering";

function card(): ToolResultCard {
  return { root: document.createElement("div"), status: document.createElement("span"), result: document.createElement("pre"), state: "running" };
}

const running = JSON.stringify({ job_id: "job-1", status: "running", output: "" });

describe("后台命令状态", () => {
  it("调用成功仍显示后台运行和结果待交付", () => {
    const current = card();
    applyToolResult({ start: current }, current, {
      name: "bash", status: "running", call_status: "success", execution_status: "running", job_id: "job-1", result_delivered: false,
    });
    expect(current.state).toBe("running");
    expect(current.status.textContent).toBe("后台运行中");
    expect(current.root.classList.contains("succeeded")).toBe(false);
    expect(current.status.title).toContain("工具调用：成功");
    expect(current.status.title).toContain("最终结果：待交付");
  });

  it.each(["completed", "failed", "killed"])("终态 %s 更新同一 job 的运行卡片", (state) => {
    const start = card();
    const wait = card();
    const other = card();
    const cards = { start, wait, other };
    applyToolResult(cards, start, { name: "bash", preview: running });
    applyToolResult(cards, other, { name: "bash", job_id: "other", execution_status: "running" });
    applyToolResult(cards, wait, {
      name: "bash_wait", job_id: "job-1", execution_status: state, call_status: state === "failed" ? "error" : "success",
      exit_code: state === "completed" ? 0 : 1, result_delivered: true,
    });
    expect(start.state).toBe(state === "completed" ? "succeeded" : state);
    expect(wait.state).toBe(start.state);
    expect(other.state).toBe("running");
    expect(start.status.title).toContain("最终结果：已交付");
    expect(start.status.title).toContain("退出码：" + (state === "completed" ? 0 : 1));
    if (state === "killed") {
      expect(start.status.textContent).toBe("已终止");
      expect(start.root.classList.contains("succeeded")).toBe(false);
    }
  });

  it("取消调用有错误仍保留明确的 killed 执行状态", () => {
    const current = card();
    applyToolResult({ current }, current, { name: "bash", execution_status: "killed", call_status: "error", job_id: "job-1", preview: "Error: context canceled" });
    expect(current.state).toBe("killed");
    expect(current.status.textContent).toBe("已终止");
    expect(current.status.title).toContain("工具调用：失败");
  });

  it("从 Error 输出解析出的 killed 不被错误前缀覆盖", () => {
    const preview = "Error: context canceled\n\nOutput before failure:\n" + JSON.stringify({ job_id: "job-1", status: "killed" });
    expect(normalizeToolResult({ name: "bash_kill", preview })).toMatchObject({ status: "killed", execution_status: "killed", job_id: "job-1" });
  });

  it("普通工具保持成功兼容且不会把文件内容当作后台结果", () => {
    const current = card();
    applyToolResult({ current }, current, { name: "read_file", preview: running });
    expect(current.state).toBe("succeeded");
    expect(current.jobID).toBeUndefined();
  });

  it("Error 前缀优先且保留失败 job 的关联信息", () => {
    expect(normalizeToolResult({ name: "bash_wait", status: "succeeded", preview: "Error: failed\n\nOutput before failure:\n" + JSON.stringify({ job_id: "job-1", status: "failed", exit_code: 2 }) })).toMatchObject({ status: "failed", job_id: "job-1", execution_status: "failed", exit_code: 2 });
    expect(normalizeToolResult({ name: "bash", execution_status: "completed", preview: "Error: failure" }).status).toBe("failed");
  });
});

describe("历史后台命令状态", () => {
  it("保留接口返回的执行元数据", () => {
    const messages = normalizeHistoryMessages([{ role: "assistant", content: "", tools: [{ name: "bash_wait", result: "done", job_id: "job-1", execution_status: "completed", call_status: "success", exit_code: 0, result_delivered: true }] }]);
    expect(historyAssistantView(messages[0]).tools[0]).toMatchObject({ status: "succeeded", job_id: "job-1", execution_status: "completed", call_status: "success", exit_code: 0, result_delivered: true });
  });

  it.each(["bash", "bash_wait", "bash_kill"])("恢复 %s 的结构化状态并覆盖旧的 succeeded", (name) => {
    const messages = normalizeHistoryMessages([{ role: "assistant", content: "", tools: [{ id: "start", name, status: "succeeded", result: running }] }]);
    expect(historyAssistantView(messages[0]).tools[0]).toMatchObject({ status: "running", job_id: "job-1", execution_status: "running", result_delivered: false });
  });

  it("从 compact 工具记录恢复后台状态", () => {
    const view = parseCompletedToolActivity([
      "Completed tool activity (retain this evidence for later reasoning):",
      'Calls: [{"id":"start","function":{"name":"bash","arguments":"{}"}}]',
      "Result start:", running,
    ].join("\n"));
    expect(view.tools[0]).toMatchObject({ status: "running", job_id: "job-1" });
  });

  it("恢复渲染保留清理任务的未交付状态", () => {
    const tools = historyAssistantView({ role: "assistant", content: "", tools: [
      { id: "start", name: "bash", status: "killed", result: JSON.stringify({ job_id: "job-1", status: "killed" }), job_id: "job-1", execution_status: "killed", call_status: "success", result_delivered: false },
    ] }).tools;
    const current = card();
    applyToolResult({ current }, current, restoredToolPayload(tools[0], 0));
    expect(current.state).toBe("killed");
    expect(current.status.title).toContain("最终结果：待交付");
    expect(restoredToolPayload({ name: "bash" }, 2).id).toBe("history-tool-3");
  });

  it("普通历史文件读取不会误判，错误前缀不被旧状态掩盖", () => {
    const tools = historyAssistantView({ role: "assistant", content: "", tools: [
      { name: "read_file", result: running },
      { name: "bash", status: "succeeded", result: "Error: command failed" },
    ] }).tools;
    expect(tools[0].status).toBe("succeeded");
    expect(tools[0].job_id).toBeUndefined();
    expect(tools[1].status).toBe("failed");
  });
});
