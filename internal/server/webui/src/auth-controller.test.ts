import { beforeEach, describe, expect, it, vi } from "vitest";
import { APIClient } from "./api-client";
import { createAuthController } from "./auth-controller";

beforeEach(() => {
  document.body.innerHTML = '<section id="login-view"><form id="login-form"><input id="login-password" type="password"><button id="login-password-toggle" type="button"></button><button id="login-submit"></button><div id="login-error"></div></form></section><div id="account-controls"><button id="account-trigger"></button><div id="account-menu" hidden><button id="change-password" role="menuitem"></button><button id="logout" role="menuitem"></button></div></div>';
});
describe("authentication lifecycle", () => {
  it("allows initialization to retry after failure without duplicate listeners", async () => {
    const transport=vi.fn(async()=>new Response('{"required":true,"authenticated":true}'));
    const initialize=vi.fn().mockRejectedValueOnce(new Error("workspace failed")).mockResolvedValue(undefined);
    const clear=vi.fn();const auth=createAuthController(new APIClient(transport),initialize,clear);
    await auth.initialize();expect(document.body.classList.contains("auth-login")).toBe(true);
    document.querySelector("form")!.dispatchEvent(new Event("submit",{cancelable:true}));
    await vi.waitFor(()=>expect(initialize).toHaveBeenCalledTimes(2));
    expect(document.body.classList.contains("auth-ready")).toBe(true);expect(transport).toHaveBeenCalledTimes(2);
    auth.dispose();document.querySelector("form")!.dispatchEvent(new Event("submit",{cancelable:true}));expect(transport).toHaveBeenCalledTimes(2);
  });
  it("clears protected content on logout failure and offers an explicit retry", async () => {
    const transport=vi.fn(async (_input: RequestInfo|URL,init?:RequestInit)=> init?.method==="DELETE" ? new Response("failure",{status:500}) : new Response('{"required":true,"authenticated":true}'));
    const clear=vi.fn();const auth=createAuthController(new APIClient(transport),async()=>{},clear);await auth.initialize();
    document.querySelector<HTMLButtonElement>("#logout")!.click();await vi.waitFor(()=>expect(document.querySelector("#login-error")!.textContent).toContain("服务端会话可能仍有效"));
    expect(clear).toHaveBeenCalled();expect(document.body.classList.contains("auth-login")).toBe(true);
    const retry=[...document.querySelectorAll("button")].find(button=>button.textContent==="重试退出登录")!;expect(retry.hidden).toBe(false);
    auth.dispose();
  });
});
