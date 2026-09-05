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
