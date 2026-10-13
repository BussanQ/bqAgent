import { readFileSync } from "node:fs";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { APIClient } from "./api-client";
import { createProviderController } from "./provider-controller";
import { createWorkspaceController, type WorkspaceDependencies } from "./workspace-controller";
import { renderDoctorReport } from "./doctor-controller";

beforeEach(()=>{
  const html=readFileSync("index.html","utf8");document.body.innerHTML=html.slice(html.indexOf("<body"),html.indexOf("</body>")+7);localStorage.clear();
  Object.assign(document.getElementById("model-select")!,{handleDefaultSlotChange:()=>{},selectedOptions:[]});
});
describe("extracted controllers",()=>{
  it("drops late provider settings after cancellation",async()=>{
    let finish!:(response:Response)=>void;
    const transport=vi.fn(()=>new Promise<Response>(resolve=>{finish=resolve}));
    const controller=createProviderController(new APIClient(transport),{statusEl:document.getElementById("status") as HTMLDivElement,sessionId:"",busy:false,workspaceScopedURL:url=>url,setChatMode:()=>{}});
    const pending=controller.loadRuntimeModel();controller.cancel();finish(new Response('{"active_provider":"old","providers":[]}'));
    // loadRuntimeModel is best-effort, but must not issue status requests after cancellation.
    await pending;expect(transport).toHaveBeenCalledTimes(1);controller.dispose();
    expect(controller.providerSettingsState.active_provider).toBe("");
  });
  it("keeps the newest directory picker response",async()=>{
    const pending:Array<(response:Response)=>void>=[];
    const api=new APIClient(vi.fn(async(input:RequestInfo|URL)=>{
      const url=String(input);
      if(url.includes("/directories"))return new Promise<Response>(resolve=>pending.push(resolve));
      if(url==="/api/v1/webui/workspaces")return new Response(JSON.stringify({roots:[{id:"r",name:"root",path:"/root"},{id:"s",name:"second",path:"/second"}],default:{id:"w",root_id:"r",name:"workspace",path:"/root",relative_path:""}}));
      return new Response('{"entries":[],"next_offset":null}');
    }));
    const deps:WorkspaceDependencies={onSwitch:()=>{},thread:document.getElementById("thread") as HTMLDivElement,input:document.getElementById("input") as HTMLTextAreaElement,sendBtn:document.getElementById("send") as HTMLButtonElement,statusEl:document.getElementById("status") as HTMLDivElement,sessionId:"",busy:false,pendingFiles:[],MAX_PENDING_FILES:5,MAX_PENDING_TOTAL_FILE_BYTES:6000000,setConversationType:()=>{},refreshConversations:async()=>{},loadRuntimeModel:async()=>{},formatBytes:String,pathBaseName:value=>value,clearPendingAttachments:()=>{},addWorkspacePath:()=>true,setAttachmentMenu:()=>{},setChatMode:()=>{},emptyMarkup:()=>""};
    const controller=createWorkspaceController(api,deps);await controller.initializeWorkspaceSelection();
    document.getElementById("workspace-select")!.click();const select=document.getElementById("workspace-picker-root") as HTMLSelectElement;select.value="s";select.dispatchEvent(new Event("change"));
    pending[1](new Response('{"path":"","absolute_path":"/new","directories":[],"next_offset":null}'));
    await vi.waitFor(()=>expect(document.getElementById("workspace-picker-path")!.textContent).toBe("/new"));
    pending[0](new Response('{"path":"","absolute_path":"/old","directories":[],"next_offset":null}'));
    await new Promise(resolve=>setTimeout(resolve,0));expect(document.getElementById("workspace-picker-path")!.textContent).toBe("/new");
    controller.dispose();
  });
  it("renders diagnostic causes as text and distinguishes disabled components",()=>{
    const container=document.createElement("div");renderDoctorReport(container,{ready:true,status:"degraded",mode:"snapshot",checked_at:"2026-09-05T00:00:00Z",checks:[{id:"<script>unsafe</script>",group:"mcp",state:"disabled",reason:"not_configured",source:"local",checked_at:"2026-09-05T00:00:00Z",required:false}]});
    expect(container.querySelector("script")).toBeNull();expect(container.textContent).toContain("未启用");expect(container.textContent).toContain("服务就绪");
  });
});

it("renders provider display names and saves and selects model IDs", async () => {
  let settings = { active_provider: "p", providers: [{ id: "p", name: "Provider", api_type: "openai", base_url: "", models: ["model-id", "legacy"], default_model: "model-id", model_display_names: { "model-id": "Friendly Model" } }] };
  let selection: unknown;
  let saved: typeof settings | undefined;
  const transport = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url.endsWith("/provider-models")) return new Response(JSON.stringify({ models: [{ id: "model-id", display_name: "Updated Name" }, { id: "new-id", display_name: "New Model" }] }));
    if (url.endsWith("/provider-selection")) {
      selection = JSON.parse(String(init?.body));
      return new Response(JSON.stringify({ api_type: "openai", provider_id: "p", model: "model-id", model_display_name: "Friendly Model" }));
    }
    if (url.endsWith("/providers")) {
      if (init?.method === "PUT") { saved = JSON.parse(String(init.body)); settings = saved!; }
      return new Response(JSON.stringify(settings));
    }
    return new Response(JSON.stringify({ llm: { api_type: "openai", provider_id: "p", model: "model-id", mode: "run" } }));
  });
  const controller = createProviderController(new APIClient(transport), { statusEl: document.getElementById("status") as HTMLDivElement, sessionId: "s", busy: false, workspaceScopedURL: url => url, setChatMode: () => {} });
  await controller.loadRuntimeModel();
  expect(controller.runtimeModelOptions().map(option => option.textContent)).toEqual(["Friendly Model", "legacy"]);
  const option = controller.runtimeModelOptions()[0];
  expect(option.dataset.model).toBe("model-id");
  expect(controller.modelSelect.title).toBe("p / Friendly Model");
  Object.assign(controller.modelSelect, { selectedOptions: [option] });
  controller.modelSelect.dispatchEvent(new Event("change"));
  await controller.runtimeModelSelectionPromise;
  expect(selection).toEqual({ provider_id: "p", model: "model-id", session_id: "s" });
  controller.providerSettingsTrigger.click();
  await vi.waitFor(() => expect(document.querySelector(".provider-model-row")?.textContent).toBe("Friendly Model"));
  document.getElementById("provider-fetch-models")!.click();
  await vi.waitFor(() => expect(document.querySelectorAll(".provider-model-row")).toHaveLength(3));
  expect(Array.from(document.querySelectorAll(".provider-model-row")).map(row => row.textContent)).toEqual(["Updated Name", "legacy", "New Model"]);
  const defaults = document.getElementById("provider-default-model") as HTMLSelectElement;
  expect(defaults.options[0].text).toBe("Updated Name");
  expect(defaults.value).toBe("model-id");
  document.getElementById("provider-settings-save")!.click();
  await vi.waitFor(() => expect(saved).toBeDefined());
  expect(saved!.providers[0].models).toEqual(["model-id", "legacy", "new-id"]);
  expect(saved!.providers[0].model_display_names).toEqual({ "model-id": "Updated Name", "new-id": "New Model" });
  await vi.waitFor(() => expect(controller.runtimeModelOptions()[0].textContent).toBe("Updated Name"));
  controller.dispose();
});

it.each(["save", "delete"])("shows managed Magpie and excludes it from ordinary provider %s", async (action) => {
  const magpie = { id: "magpie", name: "Magpie", managed_by: "magpie", api_type: "openai-response", base_url: "http://localhost:12345/v1", api_key_configured: true, models: ["p/first", "p/second"], default_model: "p/first", model_display_names: { "p/second": "Second Model" } };
  const ordinary = { id: "saved", name: "Saved", api_type: "openai", base_url: "", models: ["original"], default_model: "original" };
  const settings = { active_provider: "magpie", providers: [magpie, ordinary] };
  let written: { active_provider: string; providers: typeof ordinary[] } | undefined;
  let selection: unknown;
  const transport = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url.endsWith("/provider-selection")) {
      selection = JSON.parse(String(init?.body));
      return new Response(JSON.stringify({ provider_id: "magpie", api_type: "openai-response", model: "p/second", model_display_name: "Second Model" }));
    }
    if (url.endsWith("/providers")) {
      if (init?.method === "PUT") {
        written = JSON.parse(String(init.body));
        return new Response(JSON.stringify({ active_provider: written!.active_provider, providers: [magpie, ...written!.providers] }));
      }
      return new Response(JSON.stringify(settings));
    }
    return new Response(JSON.stringify({ llm: { provider_id: "magpie", api_type: "openai-response", model: "p/first", mode: "run" } }));
  });
  const controller = createProviderController(new APIClient(transport), { statusEl: document.getElementById("status") as HTMLDivElement, sessionId: "s", busy: false, workspaceScopedURL: url => url, setChatMode: () => {} });
  await controller.loadRuntimeModel();
  expect(controller.runtimeModelOptions().map(option => option.textContent)).toEqual(["p/first", "Second Model", "original"]);
  Object.assign(controller.modelSelect, { selectedOptions: [controller.runtimeModelOptions()[1]] });
  controller.modelSelect.dispatchEvent(new Event("change"));
  await controller.runtimeModelSelectionPromise;
  expect(selection).toEqual({ provider_id: "magpie", model: "p/second", session_id: "s" });
  expect(controller.modelSelect.title).toBe("magpie / Second Model");

  controller.providerSettingsTrigger.click();
  await vi.waitFor(() => expect(controller.providerSettingsBackdrop.hidden).toBe(false));
  expect(document.getElementById("provider-managed-notice")!.hidden).toBe(false);
  expect(document.getElementById("provider-local-notice")!.hidden).toBe(true);
  expect(document.getElementById("provider-api-key-status")!.textContent).toBe("由 Magpie 管理");
  expect((document.getElementById("provider-api-type") as HTMLSelectElement).value).toBe("openai-response");
  expect((document.getElementById("provider-api-key") as HTMLInputElement).value).toBe("");
  const controls = ["provider-name", "provider-id", "provider-api-type", "provider-base-url", "provider-api-key", "provider-api-key-toggle", "provider-fetch-models", "provider-model-add", "provider-model-manual", "provider-default-model", "provider-settings-save", "provider-settings-delete"];
  controls.forEach(id => expect((document.getElementById(id) as HTMLInputElement).disabled, id).toBe(true));
  expect(Array.from(document.querySelectorAll<HTMLButtonElement>(".provider-model-row")).every(row => row.disabled)).toBe(true);
  document.getElementById("provider-settings-save")!.click();
  document.getElementById("provider-settings-delete")!.click();
  expect(written).toBeUndefined();

  document.querySelector<HTMLButtonElement>('.provider-settings-item[data-index="1"]')!.click();
  controls.forEach(id => expect((document.getElementById(id) as HTMLInputElement).disabled, id).toBe(false));
  expect(document.getElementById("provider-managed-notice")!.hidden).toBe(true);
  if (action === "save") {
    (document.getElementById("provider-name") as HTMLInputElement).value = "Renamed";
    document.getElementById("provider-settings-save")!.click();
  } else {
    document.getElementById("provider-settings-delete")!.click();
    document.getElementById("provider-settings-delete")!.click();
  }
  await vi.waitFor(() => expect(written).toBeDefined());
  expect(written!.active_provider).toBe(action === "save" ? "saved" : "magpie");
  expect(written!.providers.map(provider => provider.id)).toEqual(action === "save" ? ["saved"] : []);
  if (action === "save") expect(written!.providers[0].name).toBe("Renamed");
  await vi.waitFor(() => expect(controller.providerSettingsState.providers).toHaveLength(action === "save" ? 2 : 1));
  expect(controller.runtimeModelOptions().some(option => option.dataset.providerId === "magpie")).toBe(true);
  controller.dispose();
});

it("renders background MCP connecting status", () => {
  const container = document.createElement("div");
  renderDoctorReport(container, { ready: true, status: "degraded", mode: "snapshot", checked_at: "2026-10-13T00:00:00Z",
    checks: [{ id: "search", group: "mcp", state: "connecting", source: "runtime", checked_at: "2026-10-13T00:00:00Z", required: false }] });
  expect(container.querySelector(".doctor-badge")?.textContent).toBe("连接中");
});
