import { renderMarkdown } from "./markdown";
import type { AgentPlan } from "./types";

const statuses: Record<string, string> = { pending: "待处理", in_progress: "进行中", completed: "已完成" };
const priorities: Record<string, string> = { high: "高优先级", medium: "中优先级", low: "低优先级" };

export function planVisible(plan: AgentPlan, text: string): boolean {
  return Boolean(text.trim() || plan.steps?.length);
}

export function renderAgentPlan(bubble: HTMLDivElement, plan: AgentPlan, text: string): void {
  let details = bubble.querySelector<HTMLDetailsElement>("details.agent-plan");
  if (!details) {
    details = document.createElement("details");
    details.className = "agent-plan";
    details.open = true;
    const summary = document.createElement("summary");
    const title = document.createElement("strong"), progress = document.createElement("span");
    summary.append(title, progress);
    const body = document.createElement("div"), list = document.createElement("ol");
    body.className = "agent-plan-body";
    details.append(summary,body,list);
    bubble.replaceChildren(details);
  }
  bubble.classList.add("rendered", "agent-plan-card");
  const row = bubble.closest<HTMLElement>(".msg");
  if (row) row.hidden = !planVisible(plan,text);
  const steps = plan.steps || [];
  const [summary,body,list] = details.children;
  summary.children[0].textContent = plan.mode === "proposal" ? "实施方案" : "工作计划";
  summary.children[1].textContent = steps.length ? "已完成 " + steps.filter(s => s.status === "completed").length + "/" + steps.length : "";
  if (text) body.innerHTML = renderMarkdown(text);
  else body.replaceChildren();
  list.replaceChildren(...steps.map(step => {
    const li = document.createElement("li");
    const content = document.createElement("span"), status = document.createElement("span");
    content.className = "agent-plan-step";
    content.textContent = step.text;
    status.className = "agent-plan-status";
    status.textContent = Object.hasOwn(statuses,step.status) ? statuses[step.status] : step.status || "状态未知";
    if (Object.hasOwn(statuses,step.status)) li.dataset.status = step.status;
    li.append(status,content);
    if (step.priority) {
      const priority = document.createElement("small");
      priority.textContent = Object.hasOwn(priorities,step.priority) ? priorities[step.priority] : step.priority;
      li.append(priority);
    }
    return li;
  }));
}
