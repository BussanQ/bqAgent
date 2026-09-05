import { APIClient, isCancellation } from "./api-client";
import { errorMessage } from "./api";
import { byId } from "./dom";
import { createAccountController } from "./account-controller";

interface AuthStatus { required: boolean; authenticated: boolean }

export function createAuthController(api: APIClient, initialize: () => Promise<void>, clear: () => void) {
  const view = byId<HTMLElement>("login-view");
  const form = byId<HTMLFormElement>("login-form");
  const password = byId<HTMLInputElement>("login-password");
  const toggle = byId<HTMLButtonElement>("login-password-toggle");
  const submit = byId<HTMLButtonElement>("login-submit");
  const error = byId<HTMLElement>("login-error");
  const logout = byId<HTMLButtonElement>("logout");
  const retryLogout = document.createElement("button");
  retryLogout.type = "button";
  retryLogout.textContent = "重试退出登录";
  retryLogout.hidden = true;
  error.after(retryLogout);

  const lifetime = new AbortController();
  let generation = 0;
  let starting: Promise<void> | null = null;

  function showLogin(message = "") {
    generation++;
    starting = null;
    api.suspend();
    account.reset();
    clear();
    document.body.classList.remove("auth-pending", "auth-ready");
    document.body.classList.add("auth-login");
    view.hidden = false;
    error.textContent = message;
    password.focus();
  }

  async function start(required: boolean) {
    if (starting) return starting;
    const current = ++generation;
    api.authenticated();
    account.setVisible(required);
    view.hidden = true;
    retryLogout.hidden = true;
    document.body.classList.remove("auth-pending", "auth-login");
    document.body.classList.add("auth-ready");
    starting = initialize();
    try {
      await starting;
    } catch (reason) {
      if (current === generation && !isCancellation(reason)) {
        showLogin("初始化失败，可重新登录重试：" + errorMessage(reason));
      }
    } finally {
      if (current === generation) starting = null;
    }
  }

  api.onUnauthorized = () => showLogin("登录已失效，请重新输入密码。");
  form.addEventListener("submit", async event => {
    event.preventDefault();
    if (submit.disabled) return;
    const current = ++generation;
    submit.disabled = true;
    submit.classList.add("loading");
    error.textContent = "";
    try {
      const status = await api.json<AuthStatus>("/api/v1/webui/auth", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ password: password.value }),
        signal: lifetime.signal,
      });
      if (current !== generation) return;
      password.value = "";
      await start(status.required);
    } catch (reason) {
      if (current === generation && !isCancellation(reason)) {
        error.textContent = errorMessage(reason);
        password.select();
      }
    } finally {
      submit.disabled = false;
      submit.classList.remove("loading");
    }
  }, { signal: lifetime.signal });

  toggle.addEventListener("click", () => {
    const visible = password.type === "text";
    password.type = visible ? "password" : "text";
    toggle.setAttribute("aria-pressed", String(!visible));
    toggle.setAttribute("aria-label", visible ? "显示密码" : "隐藏密码");
    toggle.querySelector("use")?.setAttribute("href", visible ? "#icon-eye" : "#icon-eye-off");
    password.focus();
  }, { signal: lifetime.signal });

  async function signOut() {
    if (logout.disabled) return;
    showLogin();
    const current = generation;
    logout.disabled = true;
    submit.disabled = true;
    retryLogout.disabled = true;
    try {
      await api.request("/api/v1/webui/auth", { method: "DELETE", signal: lifetime.signal });
      if (current === generation) retryLogout.hidden = true;
    } catch (reason) {
      if (current === generation && !isCancellation(reason)) {
        error.textContent = "退出请求失败，服务端会话可能仍有效；请恢复连接后重试退出。";
        retryLogout.hidden = false;
      }
    } finally {
      logout.disabled = false;
      submit.disabled = false;
      retryLogout.disabled = false;
    }
  }

  logout.addEventListener("click", () => void signOut(), { signal: lifetime.signal });
  retryLogout.addEventListener("click", () => void signOut(), { signal: lifetime.signal });

  const account = createAccountController(api, () => showLogin("密码已修改，请使用新密码重新登录。"));

  return {
    async initialize() {
      const current = generation;
      try {
        const status = await api.json<AuthStatus>("/api/v1/webui/auth", { signal: lifetime.signal });
        if (current !== generation) return;
        if (status.required && !status.authenticated) showLogin();
        else await start(status.required);
      } catch (reason) {
        if (current === generation && !isCancellation(reason)) {
          showLogin("无法连接到 bqagent，请确认服务仍在运行。");
        }
      }
    },
    dispose() {
      generation++;
      lifetime.abort();
      account.dispose();
      api.onUnauthorized = () => {};
      api.suspend();
      retryLogout.remove();
      clear();
    },
  };
}
