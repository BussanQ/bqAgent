import { readFileSync } from "node:fs";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { APIClient } from "./api-client";
import { createAuthController } from "./auth-controller";

beforeEach(() => {
  const html = readFileSync("index.html", "utf8");
  document.body.innerHTML = html.slice(html.indexOf("<body"), html.indexOf("</body>") + 7);
  Object.defineProperty(HTMLDialogElement.prototype, "showModal", { configurable: true, value: function(this: HTMLDialogElement) { this.open = true; } });
  Object.defineProperty(HTMLDialogElement.prototype, "close", { configurable: true, value: function(this: HTMLDialogElement) { this.open = false; this.dispatchEvent(new Event("close")); } });
});

function click(id: string) { document.getElementById(id)!.click(); }
function fill(current = "admin123", next = "new-secret", confirmation = next) {
  (document.getElementById("password-current") as HTMLInputElement).value = current;
  (document.getElementById("password-new") as HTMLInputElement).value = next;
  (document.getElementById("password-confirm") as HTMLInputElement).value = confirmation;
}
function submit() { document.querySelector(".password-form")!.dispatchEvent(new Event("submit", { cancelable:true })); }

describe("account menu and password changes", () => {
  it("offers both actions and closes on outside click or Escape", async () => {
    const api = new APIClient(vi.fn(async () => new Response('{"required":true,"authenticated":true}')));
    const auth = createAuthController(api, async () => {}, () => {});
    await auth.initialize(); click("account-trigger");
    expect(document.getElementById("account-menu")!.hidden).toBe(false);
    expect(document.getElementById("account-menu")!.textContent).toContain("修改密码");
    expect(document.getElementById("account-menu")!.textContent).toContain("退出登录");
    document.body.click(); expect(document.getElementById("account-menu")!.hidden).toBe(true);
    click("account-trigger"); document.dispatchEvent(new KeyboardEvent("keydown", { key:"Escape" }));
    expect(document.activeElement?.id).toBe("account-trigger");
    expect(document.getElementById("account-menu")!.hidden).toBe(true);
    auth.dispose();
  });

  it("validates confirmation locally and clears passwords on cancel", async () => {
    const transport = vi.fn(async () => new Response('{"required":true,"authenticated":true}'));
    const auth = createAuthController(new APIClient(transport), async () => {}, () => {});
    await auth.initialize(); click("account-trigger"); click("change-password"); fill("admin123", "new-secret", "different"); submit();
    expect(document.querySelector(".password-error")!.textContent).toContain("不一致");
    expect(transport).toHaveBeenCalledTimes(1);
    (document.querySelector(".password-cancel") as HTMLButtonElement).click();
    expect((document.getElementById("password-new") as HTMLInputElement).value).toBe("");
    expect((document.querySelector("dialog") as HTMLDialogElement).open).toBe(false);
    auth.dispose();
  });

  it("saves the password once and returns to login on success", async () => {
    let sent: Record<string,string> | undefined;
    const transport = vi.fn(async (url: RequestInfo | URL, init?: RequestInit) => {
      if (String(url).endsWith("/password")) { sent = JSON.parse(String(init?.body)); return new Response('{"changed":true}'); }
      return new Response('{"required":true,"authenticated":true}');
    });
    const clear = vi.fn();
    const auth = createAuthController(new APIClient(transport), async () => {}, clear);
    await auth.initialize(); click("account-trigger"); click("change-password"); fill(); submit(); submit();
    await vi.waitFor(() => expect(document.body.classList.contains("auth-login")).toBe(true));
    expect(sent).toEqual({ current_password:"admin123", new_password:"new-secret", confirm_password:"new-secret" });
    expect(transport).toHaveBeenCalledTimes(2); expect(clear).toHaveBeenCalledTimes(1);
    expect(document.getElementById("login-error")!.textContent).toContain("密码已修改");
    expect((document.getElementById("password-current") as HTMLInputElement).value).toBe("");
    auth.dispose();
  });

  it("keeps the dialog and login session on a wrong current password", async () => {
    const api = new APIClient(vi.fn(async url => String(url).endsWith("/password")
      ? new Response('{"error":"当前密码错误"}', { status:403 })
      : new Response('{"required":true,"authenticated":true}')));
    const clear = vi.fn(); const auth = createAuthController(api, async () => {}, clear);
    await auth.initialize(); click("change-password"); fill("wrong-password"); submit();
    await vi.waitFor(() => expect(document.querySelector(".password-error")!.textContent).toContain("当前密码错误"));
    expect(document.body.classList.contains("auth-ready")).toBe(true);
    expect((document.querySelector("dialog") as HTMLDialogElement).open).toBe(true);
    expect(clear).not.toHaveBeenCalled(); auth.dispose();
  });
});
