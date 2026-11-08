import { expect, it, vi } from "vitest";
import { APIClient } from "./api-client";
import { SingleAgentPicker } from "./single-agent";
import { busyCommandContext, createSlashCommands, type ChatCommand } from "./slash-commands";

async function commandUI(transport: typeof fetch, busy = false) {
  document.body.innerHTML = '<select></select><textarea></textarea><div id="commands" hidden></div>';
  const select = document.querySelector("select")!, input = document.querySelector("textarea")!, menu = document.querySelector("div")!;
  const agent = new SingleAgentPicker(select, () => controller.contextChanged());
  await agent.loadWorkspace(async () => ({ scheduler:"bqagent", participants:["codex","pi"].map(id => ({id,name:id,kind:"external",available:true})) }));
  const controller = createSlashCommands(new APIClient(transport), input, menu, {
    url: () => "/builtin", disabled: () => false, onComplete: () => {},
    context: () => {
      const view = agent.commandContext(input.value);
      if (busy) return busyCommandContext(view, view.agent === "pi");
      return {...view, nativeURL:view.agent && "/" + view.agent};
    },
  });
  function pick(name: string) {
    select.value = name;
    select.dispatchEvent(new Event("change"));
  }
  async function type(value: string) {
    input.value = value;
    input.setSelectionRange(value.length,value.length);
    await controller.update();
  }
  return { input,menu,controller,agent,pick,type };
}

const reply = (commands: ChatCommand[]) => new Response(JSON.stringify({commands}));

it("completes a colliding native command with its owner and resolves explicitly routed Codex skills", async () => {
  const transport = vi.fn<typeof fetch>(async url => {
    switch (String(url)) {
      case "/pi": return reply([{name:"/skill",description:"<img src=x>",agent:"pi",insert:"/pi /skill",hint:"[args]"}]);
      case "/codex": return reply([{name:"/review",description:"Review code",agent:"codex",insert:"/codex $review"}]);
      default: return reply([{name:"/skill",description:"workspace skill"}]);
    }
  });
  const ui=await commandUI(transport);
  ui.pick("pi");
  await ui.type("/sk");
  expect(ui.menu.querySelectorAll("button")).toHaveLength(2);
  expect(ui.menu.querySelector("img")).toBeNull();
  expect(ui.menu.textContent).toContain("[args]");
  ui.menu.querySelector<HTMLButtonElement>("button")!.click();
  expect(ui.input.value).toBe("/pi /skill ");
  expect(ui.menu.hidden).toBe(true);
  expect(ui.agent.externalResponder(ui.input.value)).toBe("pi");
  await ui.type("/codex /rev");
  expect(ui.menu.querySelectorAll("button")).toHaveLength(1);
  ui.menu.querySelector<HTMLButtonElement>("button")!.click();
  expect(ui.input.value).toBe("/codex $review ");
  expect(ui.agent.externalResponder(ui.input.value)).toBe("codex");
  expect(transport.mock.calls.filter(([url]) => String(url) === "/builtin")).toHaveLength(1);
  expect(ui.agent.externalResponder("/skill:review source")).toBe("pi");
  expect(ui.agent.externalResponder("/skill local")).toBeUndefined();
});

it("offers only supported queued compaction during a turn without fetching skill catalogs", async () => {
  const transport = vi.fn<typeof fetch>();
  const ui = await commandUI(transport, true);
  ui.pick("codex");
  await ui.type("/");
  expect(ui.menu.hidden).toBe(true);
  await ui.type("/pi /com");
  expect(ui.menu.querySelectorAll("button")).toHaveLength(1);
  const choose = new KeyboardEvent("keydown", { key: "Enter", cancelable: true });
  ui.input.dispatchEvent(choose);
  expect(choose.defaultPrevented).toBe(true);
  expect(ui.input.value).toBe("/pi /compact ");
  await ui.type("/pi /compact 保留测试");
  expect(ui.menu.hidden).toBe(true);
  const submit = new KeyboardEvent("keydown", { key: "Enter", cancelable: true });
  ui.input.dispatchEvent(submit);
  expect(submit.defaultPrevented).toBe(false);
  ui.pick("pi");
  await ui.type("/c");
  expect(ui.menu.textContent).toContain("/compact · pi");
  expect(transport).not.toHaveBeenCalled();
});

it("keeps builtin completion available during a slow probe and aborts it when the selected agent changes", async () => {
  let finish!: (r:Response) => void, signal:AbortSignal|undefined;
  const ui=await commandUI(async (url,init) => {
    if (String(url) === "/pi") {
      signal = init?.signal ?? undefined;
      return new Promise(resolve => {finish=resolve;});
    }
    if (String(url) === "/codex") throw new Error("no skills/list");
    return reply([{name:"/fast",description:"builtin"}]);
  });
  ui.pick("pi");
  const first=ui.type("/");
  await vi.waitFor(() => expect(ui.menu.textContent).toContain("/fast"));
  ui.pick("codex");
  expect(signal?.aborted).toBe(true);
  finish(reply([{name:"/removed",description:"old agent",agent:"pi",insert:"/pi /removed"}]));
  await first;
  await vi.waitFor(() => expect(ui.menu.textContent).toContain("codex 命令读取失败"));
  expect(ui.menu.textContent).not.toContain("old agent");
  await ui.type("/codex /manual");
  expect(ui.menu.textContent).toContain("读取失败");
  const enter = new KeyboardEvent("keydown",{key:"Enter",bubbles:true,cancelable:true});
  ui.input.dispatchEvent(enter);
  expect(enter.defaultPrevented).toBe(false);
});

it("keeps native compaction selectable when the rest of the skill report is unavailable", async () => {
  const ui = await commandUI(async url => String(url) === "/pi"
    ? new Response(JSON.stringify({commands:[{agent:"pi",kind:"control",name:"/compact",insert:"/pi /compact",hint:"[希望保留的信息]"}],error:"get_commands unavailable"}))
    : reply([]));
  ui.pick("codex");
  await ui.type("/pi /com");
  expect(ui.menu.textContent).toContain("get_commands unavailable");
  expect(ui.menu.querySelectorAll("button")).toHaveLength(1);
  ui.menu.querySelector<HTMLButtonElement>("button")!.click();
  expect(ui.input.value).toBe("/pi /compact ");
  expect(ui.agent.commandContext(ui.input.value + "保留结论")).toEqual({query:"/compact 保留结论",agent:"pi",qualified:true});
});
