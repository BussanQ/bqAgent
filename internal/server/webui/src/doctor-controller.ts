import { APIClient, RequestScope, isCancellation } from "./api-client";
import { errorMessage } from "./api";
import { byId } from "./dom";
import "./doctor.css";

interface Check { id: string; group: string; state: string; reason?: string; hint?: string; source: string; checked_at: string; required: boolean }
export interface DoctorReport { ready: boolean; status: string; checked_at: string; mode: string; checks: Check[] }
const labels: Record<string,string> = { healthy: "状态良好", degraded: "服务就绪 · 部分能力待处理", not_ready: "服务未就绪", available: "可用", error: "异常", disabled: "未启用", detecting: "检测中", unverified: "未验证", config: "全局配置", storage: "存储", external_agents: "外部 Agent", mcp: "MCP 服务", channels: "对话渠道", runtime: "运行时快照", local: "本地检查", active: "主动检测" };

export function renderDoctorReport(container: HTMLElement, report: DoctorReport): void {
  container.replaceChildren();
  const summary = document.createElement("div"); summary.className = "doctor-summary"; summary.dataset.state = report.status;
  const title = document.createElement("strong"); title.textContent = labels[report.status] || report.status;
  const time = document.createElement("p"); time.textContent = `${report.ready ? "Ready" : "Not ready"} · ${report.mode === "active" ? "主动检测" : "状态快照"} · ${new Date(report.checked_at).toLocaleString()}`;
  summary.append(title,time); container.append(summary);
  for (const group of ["config", "storage", "external_agents", "mcp", "channels"]) {
    const section = document.createElement("section"); const heading = document.createElement("h3"); heading.textContent = labels[group]; section.append(heading);
    for (const check of report.checks.filter(item => item.group === group)) {
      const row = document.createElement("article"); row.className = "doctor-check"; row.dataset.state = check.state;
      const name = document.createElement("strong"); name.textContent = check.id;
      const state = document.createElement("span"); state.className = "doctor-badge"; state.textContent = labels[check.state] || check.state;
      const reason = document.createElement("p"); reason.textContent = check.reason || "—";
      const hint = document.createElement("small"); hint.textContent = check.hint || "";
      const source = document.createElement("small"); source.className = "doctor-source"; source.textContent = `${labels[check.source] || check.source} · ${new Date(check.checked_at).toLocaleString()}${check.required ? " · 核心检查" : ""}`;
      row.append(name,state,reason,hint,source); section.append(row);
    }
    container.append(section);
  }
}

export function createDoctorController(api: APIClient, scopedURL: (url:string)=>string) {
  const trigger = byId<HTMLButtonElement>("doctor-trigger");
  const requests = new RequestScope(api); const lifetime = new AbortController();
  const dialog = document.createElement("dialog"); dialog.className = "doctor-dialog"; dialog.setAttribute("aria-labelledby", "doctor-title");
  dialog.innerHTML = '<header><div><h2 id="doctor-title">系统诊断</h2><p>看见运行状态，定位失败原因。</p></div><button type="button" data-close aria-label="关闭系统诊断">×</button></header><div class="doctor-actions"><button type="button" data-refresh>刷新状态</button><button type="button" data-active>主动检测</button><span>不会发送模型请求或渠道消息</span></div><div class="doctor-content" aria-live="polite"></div>';
  document.body.append(dialog);
  const content = dialog.querySelector<HTMLElement>(".doctor-content")!;
  const refresh = dialog.querySelector<HTMLButtonElement>("[data-refresh]")!;
  const active = dialog.querySelector<HTMLButtonElement>("[data-active]")!;
  async function load(probe: boolean) {
    requests.cancel(); refresh.disabled = true; active.disabled = true; content.textContent = probe ? "正在检测，最多等待 15 秒…" : "正在读取状态…";
    try { renderDoctorReport(content,await requests.json<DoctorReport>(scopedURL("/api/v1/webui/doctor"), { method: probe ? "POST" : "GET" })); }
    catch (error) { if (!isCancellation(error)) content.textContent = "诊断失败：" + errorMessage(error); }
    finally { refresh.disabled = false; active.disabled = false; }
  }
  trigger.addEventListener("click", () => { dialog.showModal(); void load(false); }, { signal:lifetime.signal });
  refresh.addEventListener("click", () => void load(false), { signal:lifetime.signal });
  active.addEventListener("click", () => void load(true), { signal:lifetime.signal });
  dialog.querySelector("[data-close]")!.addEventListener("click", () => dialog.close(), { signal:lifetime.signal });
  dialog.addEventListener("close", () => { requests.cancel(); trigger.focus(); }, { signal:lifetime.signal });
  return { cancel() { requests.cancel(); if(dialog.open)dialog.close(); content.replaceChildren(); }, dispose() { lifetime.abort(); requests.cancel(); dialog.remove(); } };
}
