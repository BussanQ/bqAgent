import { errorMessage } from "./api";
import { isCancellation } from "./api-client";
import { questionGroup, questionSummary } from "./question-group";
import type { ACPPermissionPayload, ACPPermissionToolCall, HistoricalPrompt, QuestionAnswer } from "./types";

export function agentDialogField(dialog: ACPPermissionPayload["dialog"]): HTMLInputElement | HTMLTextAreaElement | undefined {
  if (!dialog || (dialog.method !== "input" && dialog.method !== "editor" && !(dialog.method === "question" && dialog.free_text))) return;
  const field = dialog.method === "editor" || dialog.method === "question" && !dialog.secret ? document.createElement("textarea") : document.createElement("input");
  field.className = "agent-dialog-field";
  field.setAttribute("aria-label", dialog.title || "回复 Agent");
  field.placeholder = dialog.placeholder || (dialog.method === "question" ? "填写回答" : "");
  if (dialog.secret && field instanceof HTMLInputElement) {
    field.type = "password";
    field.autocomplete = "off";
    field.spellcheck = false;
  } else field.value = dialog.prefill || "";
  return field;
}

export function promptChoice(payload: ACPPermissionPayload, selected?: string): string | undefined {
  if (!selected || payload.expired) return;
  if (payload.dialog?.method === "question") {
    if (selected === "cancel") return;
    if (payload.dialog.secret || selected === "submit") return "已回答";
  }
  return payload.options.find(option => option.option_id === selected)?.name || selected;
}

export function hasBlockingPrompt(pending: Record<string, HTMLElement>): boolean {
  return Object.values(pending).some(card => card.dataset.blocking !== "false");
}

function answeredLabel(question: boolean, choice?: string): string {
  if (!question) return "已选择：" + (choice || "已提交");
  return !choice || choice === "已回答" ? "已回答" : "已回答：" + choice;
}

function mcpIdentity(card: HTMLElement, identity: string): void {
  if (!identity) return;
  const line = document.createElement("code");
  line.className = "mcp-approval-identity";
  line.textContent = identity;
  card.append(line);
}

function appendPromptDetail(card: HTMLElement, text: string): void {
  if (!text) return;
  const preview = document.createElement("pre");
  preview.className = "acp-permission-input";
  preview.textContent = text;
  card.append(preview);
}

export function acpPermissionTitle(toolCall: ACPPermissionToolCall | null | undefined): string {
  if (!toolCall) return "外部 Agent 请求执行操作";
  if (typeof toolCall.title === "string" && toolCall.title.trim()) return toolCall.title.trim();
  if (typeof toolCall.kind === "string" && toolCall.kind.trim()) return "请求执行 " + toolCall.kind.trim() + " 操作";
  return "外部 Agent 请求执行操作";
}

export function isRejectPermissionOption(kind: string | undefined): boolean {
  return typeof kind === "string" && kind.indexOf("reject_") === 0;
}

export function formatACPPermissionInput(rawInput: unknown): string {
  if (rawInput == null) return "";
  if (typeof rawInput === "string") return rawInput;
  try {
    return JSON.stringify(rawInput, null, 2);
  } catch (_) {
    return String(rawInput);
  }
}

export function settleAgentPrompt(pending: Record<string, HTMLElement>, id: string, choice?: string): void {
  const card = pending[id];
  if (!card) return;
  card.querySelectorAll<HTMLInputElement | HTMLTextAreaElement | HTMLButtonElement>("button,input,textarea")
    .forEach(control => { control.disabled = true; });
  const status = card.querySelector<HTMLElement>(".acp-permission-status");
  if (card.classList.contains("agent-question-card")) {
    card.querySelectorAll<HTMLInputElement | HTMLTextAreaElement>("input,textarea").forEach(field => {
      if (field instanceof HTMLInputElement && (field.type === "checkbox" || field.type === "radio")) field.checked = false;
      else field.value = "";
    });
  }
  if (status) {
    status.classList.remove("error");
    status.textContent = choice ? answeredLabel(card.classList.contains("agent-question-card"), choice) : "请求已取消或失效";
  }
  card.classList.add(choice ? "resolved" : "expired");
  delete pending[id];
}

export function renderHistoricalPrompt(card: HTMLElement, sender: string, prompt: HistoricalPrompt): void {
  card.classList.add("rendered", "acp-permission-card");
  card.classList.toggle("agent-question-card", prompt.method === "question");
  card.classList.toggle("resolved", prompt.state === "selected");
  card.classList.toggle("expired", prompt.state !== "selected");
  const title = document.createElement("div");
  title.className = "acp-permission-title";
  title.textContent = prompt.title || (sender + " 请求输入");
  const result = document.createElement("span");
  result.className = "acp-permission-status";
  result.textContent = prompt.state === "selected" ? answeredLabel(prompt.method === "question", prompt.choice)
    : prompt.state === "cancelled" ? "请求已取消" : "处理结果未知";
  card.replaceChildren();
  if (prompt.method === "mcp_approval" || prompt.header && prompt.header !== prompt.title) {
    const header = document.createElement("strong");
    header.className = "acp-permission-heading";
    header.textContent = prompt.method === "mcp_approval" ? "MCP 工具授权" : sender + " · " + prompt.header;
    card.append(header);
  }
  card.append(title);
  if (prompt.method === "mcp_approval") mcpIdentity(card, prompt.header || "");
  appendPromptDetail(card, prompt.detail || "");
  if (prompt.questions?.length) card.append(questionSummary(prompt.questions, prompt.state === "selected"));
  card.append(result);
  if (prompt.options?.length) {
    const details = document.createElement("details"), summary = document.createElement("summary");
    summary.textContent = "查看选项";
    const list = document.createElement("ul");
    for (const option of prompt.options) {
      const row = document.createElement("li");
      row.textContent = option.description ? option.label + " · " + option.description : option.label;
      list.append(row);
    }
    details.append(summary, list);
    card.append(details);
  }
}

export function showAgentPrompt(payload: ACPPermissionPayload, pending: Record<string, HTMLElement>,
  create: () => HTMLElement, submit: (choice: string, value?: string, answers?: QuestionAnswer[]) => Promise<void>): void {
  if (payload.selected || payload.expired) {
    const card = pending[payload.request_id];
    const choice = promptChoice(payload, payload.selected);
    settleAgentPrompt(pending, payload.request_id, choice);
    if (card && payload.dialog?.questions?.length) {
      card.querySelector(".prompt-question-group")?.replaceWith(questionSummary(payload.dialog.questions, !!choice));
    }
    return;
  }
  if (pending[payload.request_id]) return;
  const bubble = create();
  bubble.classList.add("rendered", "acp-permission-card");
  const question = payload.dialog?.method === "question";
  const codexApproval = payload.agent === "codex" && payload.tool_call?.source === "codex-app-server";
  const claudeApproval = payload.agent === "claude" && payload.tool_call?.source === "claude-cli";
  const mcp = codexApproval && payload.tool_call.kind === "mcp_tool_call";
  bubble.classList.toggle("agent-question-card", question);
  bubble.dataset.blocking = String(!payload.dialog?.nonblocking);

  const heading = document.createElement("strong");
  heading.className = "acp-permission-heading";
  heading.textContent = payload.dialog ? payload.agent + " 需要你的输入" : mcp ? "MCP 工具授权" : "需要你的权限";
  if (claudeApproval) heading.textContent = "Claude 工具授权 · " + payload.tool_call.tool;
  if (question && payload.dialog?.header && payload.dialog.header !== payload.dialog.title) heading.textContent = payload.agent + " · " + payload.dialog.header;
  bubble.appendChild(heading);

  const title = document.createElement("div");
  title.className = "acp-permission-title";
  title.textContent = payload.dialog?.title || acpPermissionTitle(payload.tool_call);
  bubble.appendChild(title);

  if (mcp) mcpIdentity(bubble, [payload.tool_call.server, payload.tool_call.tool].filter(Boolean).join(" / "));
  const inputText = mcp || claudeApproval ? payload.tool_call.detail : payload.dialog?.message || formatACPPermissionInput(payload.tool_call?.rawInput);
  appendPromptDetail(bubble, inputText || "");

  const field = agentDialogField(payload.dialog);
  if (field) bubble.appendChild(field);
  const group = payload.dialog?.questions?.length ? questionGroup(payload.dialog.questions, payload.request_id) : undefined;
  if (group) bubble.append(group.element);
  const actions = document.createElement("div");
  actions.className = "acp-permission-actions";
  const status = document.createElement("span");
  status.className = "acp-permission-status";
  status.setAttribute("role", "status");
  payload.options.forEach(function (option) {
    const button = document.createElement("button");
    button.type = "button";
    button.className = "ui-btn ui-btn-sm " + (isRejectPermissionOption(option.kind) ? "ui-btn-danger" : "ui-btn-primary");
    button.textContent = option.name;
    if (option.description) {
      const description = document.createElement("small");
      description.textContent = option.description;
      button.append(description);
    }
    button.addEventListener("click", async function () {
      if (question && option.option_id === "submit" && field && !field.value.trim()) {
        status.textContent = "请先填写回答";
        field.focus();
        return;
      }
      let answers: QuestionAnswer[] | undefined;
      if (group && option.option_id === "submit") {
        try { answers = group.answers(); }
        catch (error) { status.textContent = errorMessage(error); return; }
      }
      const controls = bubble.querySelectorAll<HTMLButtonElement | HTMLInputElement | HTMLTextAreaElement>("button,input,textarea");
      controls.forEach(item => { item.disabled = true; });
      status.classList.remove("error");
      status.textContent = "正在提交…";
      try {
        if (answers) await submit(option.option_id, undefined, answers);
        else await submit(option.option_id, option.option_id === "submit" ? field?.value : undefined);
        if (question || codexApproval || claudeApproval) {
          if (pending[payload.request_id]) {
            if (payload.dialog?.secret && field) field.value = "";
            status.textContent = "已提交，等待确认";
          }
        } else settleAgentPrompt(pending, payload.request_id, promptChoice(payload, option.option_id));
      } catch (error) {
        if (isCancellation(error) || !pending[payload.request_id]) return;
        if ((error as {status?: number}).status === 410) {
          if (group && payload.dialog?.questions) {
            // Another tab may already have claimed this group. The SSE
            // receipt or turn end will settle it with the actual disposition.
            group.element.replaceWith(questionSummary(payload.dialog.questions, false));
            status.textContent = "等待 Agent 确认处理结果";
            return;
          }
          settleAgentPrompt(pending, payload.request_id);
          return;
        }
        controls.forEach(item => { item.disabled = false; });
        status.textContent = errorMessage(error);
        status.classList.add("error");
      }
    });
    actions.appendChild(button);
  });
  bubble.appendChild(actions);
  bubble.appendChild(status);
  pending[payload.request_id] = bubble;
}
