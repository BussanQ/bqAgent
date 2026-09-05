import { APIClient, RequestScope, isCancellation } from "./api-client";
import { errorMessage } from "./api";
import { byId } from "./dom";
import "./account.css";

export function createAccountController(api: APIClient, onPasswordChanged: () => void) {
  const controls = byId<HTMLElement>("account-controls");
  const trigger = byId<HTMLButtonElement>("account-trigger");
  const menu = byId<HTMLElement>("account-menu");
  const change = byId<HTMLButtonElement>("change-password");
  const items = Array.from(menu.querySelectorAll<HTMLButtonElement>("[role=menuitem]"));
  const lifetime = new AbortController();
  const requests = new RequestScope(api);
  const dialog = document.createElement("dialog");
  dialog.className = "password-dialog";
  dialog.setAttribute("aria-labelledby", "password-title");
  dialog.innerHTML = `
    <form class="password-form">
      <header><div><h2 id="password-title">修改密码</h2><p>保护你的工作区与智能体会话</p></div>
        <button class="password-close" type="button" aria-label="关闭修改密码">×</button></header>
      <label for="password-current">当前密码</label>
      <input id="password-current" type="password" autocomplete="current-password" required>
      <label for="password-new">新密码</label>
      <input id="password-new" type="password" autocomplete="new-password" required aria-describedby="password-hint">
      <small id="password-hint">6–128 个字符，首尾不能有空格。</small>
      <label for="password-confirm">确认新密码</label>
      <input id="password-confirm" type="password" autocomplete="new-password" required>
      <p class="password-notice">保存后所有已登录的浏览器会话将失效，需要使用新密码重新登录。</p>
      <p class="password-error" role="alert" aria-live="polite"></p>
      <footer><button class="password-cancel" type="button">取消</button><button class="password-save" type="submit">保存新密码</button></footer>
    </form>`;
  document.body.append(dialog);
  const form = dialog.querySelector<HTMLFormElement>("form")!;
  const current = dialog.querySelector<HTMLInputElement>("#password-current")!;
  const next = dialog.querySelector<HTMLInputElement>("#password-new")!;
  const confirm = dialog.querySelector<HTMLInputElement>("#password-confirm")!;
  const error = dialog.querySelector<HTMLElement>(".password-error")!;
  const save = dialog.querySelector<HTMLButtonElement>(".password-save")!;
  const cancel = dialog.querySelector<HTMLButtonElement>(".password-cancel")!;
  const close = dialog.querySelector<HTMLButtonElement>(".password-close")!;
  let saving = false;
  let revision = 0;

  function closeMenu(focus = false) {
    menu.hidden = true;
    trigger.setAttribute("aria-expanded", "false");
    if (focus) trigger.focus();
  }
  function openMenu() {
    menu.hidden = false;
    trigger.setAttribute("aria-expanded", "true");
    items[0]?.focus();
  }
  function clearForm() {
    form.reset();
    error.textContent = "";
  }
  function setSaving(value: boolean) {
    saving = value;
    save.disabled = cancel.disabled = close.disabled = value;
    current.disabled = next.disabled = confirm.disabled = value;
    save.textContent = value ? "正在保存…" : "保存新密码";
  }
  function reset() {
    revision++;
    closeMenu();
    requests.cancel();
    if (dialog.open) dialog.close();
    clearForm();
    setSaving(false);
  }

  trigger.addEventListener("click", () => menu.hidden ? openMenu() : closeMenu(true), { signal: lifetime.signal });
  trigger.addEventListener("keydown", event => {
    if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      event.preventDefault(); openMenu();
      if (event.key === "ArrowUp") items.at(-1)?.focus();
    }
  }, { signal: lifetime.signal });
  menu.addEventListener("keydown", event => {
    if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      event.preventDefault();
      const index = items.indexOf(document.activeElement as HTMLButtonElement);
      items[(index + (event.key === "ArrowDown" ? 1 : items.length - 1)) % items.length]?.focus();
    }
  }, { signal: lifetime.signal });
  document.addEventListener("click", event => {
    if (event.target instanceof Node && !controls.contains(event.target)) closeMenu();
  }, { signal: lifetime.signal });
  document.addEventListener("keydown", event => {
    if (event.key === "Escape" && !menu.hidden) { event.preventDefault(); closeMenu(true); }
  }, { signal: lifetime.signal });
  controls.addEventListener("focusout", event => {
    if (!(event.relatedTarget instanceof Node) || !controls.contains(event.relatedTarget)) closeMenu();
  }, { signal: lifetime.signal });
  change.addEventListener("click", () => {
    reset(); dialog.showModal(); current.focus();
  }, { signal: lifetime.signal });
  cancel.addEventListener("click", () => dialog.close(), { signal: lifetime.signal });
  close.addEventListener("click", () => dialog.close(), { signal: lifetime.signal });
  dialog.addEventListener("cancel", event => { if (saving) event.preventDefault(); }, { signal: lifetime.signal });
  dialog.addEventListener("close", () => {
    requests.cancel(); clearForm();
    if (document.body.classList.contains("auth-ready")) trigger.focus();
  }, { signal: lifetime.signal });
  form.addEventListener("submit", async event => {
    event.preventDefault();
    if (saving) return;
    error.textContent = "";
    const length = Array.from(next.value).length;
    if (length < 6 || length > 128 || next.value.trim() !== next.value) {
      error.textContent = "新密码须为 6–128 个字符，且首尾不能有空格"; next.focus(); return;
    }
    if (next.value !== confirm.value) {
      error.textContent = "两次输入的新密码不一致"; confirm.focus(); return;
    }
    if (current.value === next.value) {
      error.textContent = "新密码不能与当前密码相同"; next.focus(); return;
    }
    const payload = { current_password: current.value, new_password: next.value, confirm_password: confirm.value };
    const requestID = ++revision;
    setSaving(true);
    try {
      await requests.json<{ changed: boolean }>("/api/v1/webui/password", {
        method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(payload),
      });
      if (requestID !== revision) return;
      reset();
      onPasswordChanged();
    } catch (reason) {
      if (requestID === revision && !isCancellation(reason)) error.textContent = errorMessage(reason);
    } finally { if (requestID === revision) setSaving(false); }
  }, { signal: lifetime.signal });

  return {
    setVisible(visible: boolean) { controls.hidden = !visible; if (!visible) reset(); },
    reset,
    dispose() { lifetime.abort(); requests.cancel(); dialog.remove(); },
  };
}
