import { expect, it, vi } from "vitest";
import { readFileSync } from "node:fs";
import { AgentModelPicker, type AgentModelCatalog } from "./agent-models";
import { SingleAgentPicker } from "./single-agent";
import { busyInputActions } from "./turn-input";
import { AgentPreferenceWrites, readAgentDraft, recordFirstAgentTurn, saveModelDraft, saveResponderDraft } from "./agent-drafts";
import { clearChatViewData } from "./turn-replay";

it("selects Codex plan mode independently of its model, restores drafts and queues mode changes", async () => {
  const scope = "interaction-mode-test";
  const catalog: AgentModelCatalog = {models:[{id:"one",name:"One"},{id:"two",name:"Two"}], selected:"one", supports_interaction_mode:true, interaction_mode:"default"};
  function create() {
    const model = document.createElement("select"), mode = document.createElement("select");
    const picker = new AgentModelPicker(document.createElement("div"),model,document.createElement("input"),document.createElement("button"),
      document.createElement("select"),async agent => agent === "codex" ? catalog : {models:[]},undefined,undefined,vi.fn(),undefined,mode);
    return {picker,model,mode};
  }
  clearChatViewData();
  const original = create();
  original.picker.configure(scope,"codex",false);
  await original.picker.readyForSend("codex");
  expect(original.mode.value).toBe("default");
  original.mode.value = "plan";
  original.mode.dispatchEvent(new Event("change"));
  original.model.value = "two";
  original.model.dispatchEvent(new Event("change"));
  const {picker,mode} = create();
  picker.configure(scope,"codex",true);
  expect(await picker.readyForSend("codex")).toBe(true);
  expect(picker.turnOptions("codex","codex")).toMatchObject({agent_model:"two",agent_interaction_mode:"plan"});
  expect(picker.turnOptions("codex","codex",true)).toEqual({agent:"codex"});
  expect(picker.queueSelection("codex")).toMatchObject({agent_interaction_mode:"plan"});
  expect(picker.hasNextTurnSelection("codex",undefined,{agent:"codex",agent_model:"two",agent_interaction_mode:"default"})).toBe(true);
  picker.configure(scope,"pi",false);
  await picker.readyForSend("pi");
  expect(mode.hidden).toBe(true);
  expect(picker.turnOptions("pi","pi").agent_interaction_mode).toBeUndefined();
  picker.configure(scope,"codex",false);
  expect(mode.value).toBe("plan");
  const revision = picker.draftRevision("codex");
  picker.submitted("codex","submitted");
  mode.value = "default";
  mode.dispatchEvent(new Event("change"));
  picker.confirm("codex","two",undefined,undefined,revision,"submitted",undefined,undefined,"plan");
  expect(mode.value).toBe("default");
  expect(picker.queueSelection("codex").agent_interaction_mode).toBe("default");
  clearChatViewData();
});

it("does not silently send a restored plan draft through a transport without plan support", async () => {
  const scope="acp-mode-test", mode=document.createElement("select");
  sessionStorage.setItem("bqagent.web-agent-draft."+scope,JSON.stringify({models:{codex:{draftMode:"plan"}}}));
  const picker = new AgentModelPicker(document.createElement("div"),document.createElement("select"),document.createElement("input"),
    document.createElement("button"),document.createElement("select"),async () => ({models:[]}),undefined,undefined,vi.fn(),undefined,mode);
  picker.configure(scope,"codex",false);
  expect(await picker.readyForSend("codex")).toBe(false);
  expect(mode.hidden).toBe(false);
  expect(mode.validationMessage).toContain("未确认支持");
  mode.value="";
  mode.dispatchEvent(new Event("change"));
  expect(await picker.readyForSend("codex")).toBe(true);
  expect(mode.hidden).toBe(true);
  clearChatViewData();
});

it("uses the exact Codex /plan command locally only after the same chat confirms support", async () => {
  const mode=document.createElement("select");
  let metadata!: (catalog:AgentModelCatalog) => void;
  const picker = new AgentModelPicker(document.createElement("div"),document.createElement("select"),document.createElement("input"),
    document.createElement("button"),document.createElement("select"),async agent => {
      if (agent === "codex") return new Promise<AgentModelCatalog>(resolve => {metadata=resolve;});
      return {models:[]};
    },undefined,undefined,undefined,undefined,mode);
  picker.configure("first","codex",false);
  let current=true;
  const old=picker.pickPlanCommand("codex","/plan",() => current);
  current=false;
  metadata({models:[],supports_interaction_mode:true});
  expect(await old).toBe("stale");
  expect(mode.value).toBe("");
  expect(await picker.pickPlanCommand("codex","/plan explain",() => true)).toBeUndefined();
  expect(await picker.pickPlanCommand("codex","/default",() => true)).toBeUndefined();
  expect(await picker.pickPlanCommand("codex","/plan ",() => true)).toBe("applied");
  expect(picker.turnOptions("codex","codex").agent_interaction_mode).toBe("plan");
  picker.configure("second","codex",false);
  const native=picker.pickPlanCommand("codex","/plan",() => true);
  metadata({models:[],allow_custom:false});
  expect(await native).toBeUndefined();
  expect(mode.hidden).toBe(true);
  picker.configure("partial","codex",false);
  const unavailable=picker.pickPlanCommand("codex","/plan",() => true);
  metadata({models:[],supports_interaction_mode:true,interaction_modes:["default"]});
  expect(await unavailable).toBe("unavailable");
  expect(Array.from(mode.options,o => o.value)).toEqual(["","default"]);
});

it("keeps Claude planning choices on its CLI session, independently of model, effort and Codex", async () => {
  const model=document.createElement("select"), mode=document.createElement("select"), effort=document.createElement("select");
  const catalog:AgentModelCatalog={models:[{id:"sonnet",name:"Sonnet"}],selected:"sonnet",
    supports_interaction_mode:true,interaction_modes:["default","plan"],efforts:{sonnet:{options:[{id:"high",name:"High"}]}}};
  const picker=new AgentModelPicker(document.createElement("div"),model,document.createElement("input"),document.createElement("button"),effort,
    async agent => agent==="claude" ? catalog : {models:[]},undefined,undefined,undefined,undefined,mode);
  picker.configure("chat","claude",false);
  expect(await picker.pickPlanCommand("claude","/plan",()=>true)).toBe("applied");
  effort.value="high";
  effort.dispatchEvent(new Event("change"));
  expect(picker.turnOptions("claude","claude")).toMatchObject({agent:"claude",agent_model:"sonnet",agent_effort:"high",agent_interaction_mode:"plan"});
  expect(picker.queueSelection("claude").agent_interaction_mode).toBe("plan");
  expect(picker.turnOptions("claude","claude",true)).toEqual({agent:"claude"});
  picker.confirm("claude","sonnet","high",undefined,picker.draftRevision("claude"),"turn",undefined,undefined,"default");
  expect(mode.value).toBe("default");
  picker.configure("chat","codex",false);
  await picker.readyForSend("codex");
  expect(mode.hidden).toBe(true);
  expect(picker.turnOptions("codex","codex").agent_interaction_mode).toBeUndefined();
  picker.configure("chat","claude",false);
  expect(mode.value).toBe("default");
});

it("routes Codex service tiers with the chosen model and restores them across draft reloads and agent switches", async () => {
  const scope = "codex-tier-chat";
  const rows = ["quick", "normal"].map(id => ({ id, name: id }));
  const catalog: AgentModelCatalog = { models: rows, selected: "quick", supports_service_tier: true,
    selected_service_tier: "default", service_tiers: { quick: [{ id: "priority", name: "Fast", description: "Priority processing" }], normal: [] } };
  const create = () => {
    const model = document.createElement("select"), tier = document.createElement("select");
    const picker = new AgentModelPicker(document.createElement("div"), model, document.createElement("input"), document.createElement("button"),
      document.createElement("select"), async agent => agent === "codex" ? catalog : { models: [{ id: "p/one", name: "Pi" }] },
      undefined, undefined, vi.fn(), tier);
    return { picker, model, tier };
  };
  clearChatViewData();
  const original = create();
  original.picker.configure(scope, "codex", false);
  await original.picker.readyForSend("codex");
  expect(original.tier.hidden).toBe(false);
  expect(original.tier.value).toBe("default");
  expect(original.tier.options[1].text).toBe("Fast");
  expect(original.tier.options[1].title).toBe("Priority processing");
  original.tier.value = "priority";
  original.tier.dispatchEvent(new Event("change"));
  const { picker, model, tier } = create();
  picker.configure(scope, "codex", true);
  await picker.readyForSend("codex");
  expect(tier.value).toBe("priority");
  expect(picker.hasNextTurnSelection("codex", undefined, { agent: "codex", agent_model: "quick", agent_service_tier: "default" })).toBe(true);
  expect(picker.queueSelection("codex")).toMatchObject({ agent_model: "quick", agent_service_tier: "priority" });
  expect(picker.turnOptions("codex", "codex", true)).toEqual({ agent: "codex" });
  picker.configure(scope, "pi", false);
  await picker.readyForSend("pi");
  expect(tier.hidden).toBe(true);
  expect(picker.turnOptions("pi", "pi").agent_service_tier).toBeUndefined();
  picker.configure(scope, "codex", false);
  expect(picker.turnOptions("codex", "codex")).toMatchObject({ agent_model: "quick", agent_service_tier: "priority" });
  picker.confirm("codex", "quick", undefined, undefined, picker.draftRevision("codex"), "turn", "priority", "priority");
  model.value = "normal";
  model.dispatchEvent(new Event("change"));
  expect(tier.hidden).toBe(true);
  expect(picker.queueSelection("codex")).toMatchObject({ agent_model: "normal", agent_service_tier: "default", tier_inherited: true });
  picker.confirm("codex", "normal", undefined, undefined, picker.draftRevision("codex"), "next", "default", "priority");
  model.value = "quick";
  model.dispatchEvent(new Event("change"));
  expect(tier.value).toBe("priority");
  tier.value = "default";
  tier.dispatchEvent(new Event("change"));
  expect(picker.turnOptions("codex", "codex")).toMatchObject({ agent_model: "quick", agent_service_tier: "default" });
  clearChatViewData();
});

it("keeps a configured unlisted Codex tier selectable and flags a draft removed by a catalog refresh", async () => {
  const model = document.createElement("select"), tier = document.createElement("select"), refresh = document.createElement("button");
  const catalog: AgentModelCatalog = { models: [{ id: "active", name: "Active" }], selected: "active", supports_service_tier: true,
    selected_service_tier: "flex", service_tiers: { active: [{ id: "priority", name: "Fast" }] } };
  const load = vi.fn<() => Promise<AgentModelCatalog>>().mockResolvedValueOnce(catalog)
    .mockResolvedValue({ ...catalog, service_tiers: { active: [] } });
  const picker = new AgentModelPicker(document.createElement("div"), model, document.createElement("input"), refresh,
    document.createElement("select"), load, undefined, undefined, undefined, tier);
  picker.configure("reported-tier", "codex", false);
  await picker.readyForSend("codex");
  expect(tier.value).toBe("flex");
  tier.value = "default";
  tier.dispatchEvent(new Event("change"));
  expect(Array.from(tier.options, option => option.value)).toContain("flex");
  tier.value = "priority";
  tier.dispatchEvent(new Event("change"));
  refresh.click();
  await vi.waitFor(() => expect(tier.textContent).toContain("已不可用"));
  expect(await picker.readyForSend("codex")).toBe(false);
  tier.value = "default";
  tier.dispatchEvent(new Event("change"));
  expect(await picker.readyForSend("codex")).toBe(true);
  expect(picker.turnOptions("codex","codex").agent_service_tier).toBe("default");
});

it("waits for the live catalog to apply a host launch pick and its supported effort to the first message", async () => {
  const models = ["p/a", "p/b"].map(id => ({ id, name: id }));
  const preferences = { model:"p/b", efforts:{"p/b":"high"} };
  const effort = document.createElement("select"), select = document.createElement("select");
  let finish!: (catalog: AgentModelCatalog) => void;
  const picker = new AgentModelPicker(document.createElement("div"), select, document.createElement("input"),
    document.createElement("button"), effort, () => new Promise(resolve => { finish = resolve; }),
    async () => ({ models:null, allow_custom:true, preferences }));
  picker.configure("draft", "pi", false);
  let ready = false;
  const send = picker.readyForSend("pi").then(value => { ready = value; });
  await Promise.resolve();
  expect(ready).toBe(false);
  finish({ models, preferences, default_model:"p/a", efforts:{"p/b":{options:[{id:"high",name:"High"}]}} });
  await send;
  expect(ready).toBe(true);
  expect(select.value).toBe("p/b");
  expect(effort.value).toBe("high");
  expect(picker.turnOptions("pi","pi")).toEqual({ agent:"pi", agent_model:"p/b", agent_effort:"high" });
  picker.configure("removed","pi",false);
  finish({ models:models.slice(0,1), preferences, default_model:"p/a", allow_custom:true });
  await picker.readyForSend("pi");
  expect(select.value).toBe("p/a");
  expect(picker.selectionFor("pi")).toBeUndefined();
  picker.configure("custom","pi",false);
  finish({ models:[], allow_custom:true, preferences:{model:"p/private",custom:true} });
  await picker.readyForSend("pi");
  expect(picker.selectionFor("pi")).toBe("p/private");
  picker.configure("acp","cursor",false);
  finish({ models, preferences:{model:"p/private",custom:true} });
  await picker.readyForSend("cursor");
  expect(picker.selectionFor("cursor")).toBeUndefined();
  picker.configure("native-default","pi",false);
  finish({ models, default_model:"p/a", preferences:{new_session:true, efforts:{"p/a":"low"}},
    efforts:{"p/a":{options:[{id:"low",name:"Low"}]}} });
  await picker.readyForSend("pi");
  expect(picker.turnOptions("pi","pi")).toMatchObject({agent_model:"p/a", agent_effort:"low"});
});

it("restores unsent responder and model picks after reload and moves only the first chat's draft on binding", async () => {
  clearChatViewData();
  const options = {models:["p/a","p/b"].map(id=>({id,name:id})), selected:"p/a",
    efforts:{"p/b":{options:[{id:"high",name:"High"}]}}};
  const remember = vi.fn();
  const make = () => {
    const model = document.createElement("select"), effort = document.createElement("select");
    const picker = new AgentModelPicker(document.createElement("div"),model,document.createElement("input"),
      document.createElement("button"),effort,async()=>options,undefined,undefined,remember);
    return {model,effort,picker};
  };
  const original = make(), chat = "ws/chat/0", draft = "ws/new";
  original.picker.configure(draft,"pi",false);
  await vi.waitFor(() => expect(original.model.value).toBe("p/a"));
  original.model.value = "p/b"; original.model.dispatchEvent(new Event("change"));
  original.effort.value = "high"; original.effort.dispatchEvent(new Event("change"));
  expect(remember.mock.calls).toEqual([["pi",{model:"p/b"}],["pi",{model:"p/b",effort:"high"}]]);
  saveResponderDraft(draft,"pi"); recordFirstAgentTurn(draft,"turn");
  original.picker.submitted("pi", "turn");
  const reopened = make();
  reopened.picker.configure(draft,"pi",true);
  await reopened.picker.readyForSend("pi");
  expect(reopened.model.value).toBe("p/b"); expect(reopened.effort.value).toBe("high");
  reopened.picker.bindDraft(chat);
  expect(readAgentDraft(draft)).toEqual({});
  expect(readAgentDraft(chat).responder).toBe("pi");
  expect(readAgentDraft(chat).firstTurn).toBeUndefined();
  reopened.picker.configure("ws/other/0","pi",false);
  await reopened.picker.readyForSend("pi");
  expect(reopened.picker.selectionFor("pi")).toBeUndefined();
  reopened.picker.configure(chat,"pi",false);
  await reopened.picker.readyForSend("pi");
  expect(reopened.picker.turnOptions("pi","pi")).toMatchObject({agent_model:"p/b",agent_effort:"high"});
  reopened.picker.confirm("pi","p/a",undefined,undefined,undefined,"earlier-turn");
  expect(readAgentDraft(chat).models?.pi?.draft).toBe("p/b");
  reopened.picker.confirm("pi","p/b","high",undefined,undefined,"turn");
  expect(readAgentDraft(chat).models?.pi).toBeUndefined();
  clearChatViewData(); expect(readAgentDraft(chat)).toEqual({});
});

it("orders preference writes for one agent while another agent can save independently", async () => {
  const writes = new AgentPreferenceWrites(), calls: string[] = [];
  let done!: () => void;
  const first = writes.write("ws/pi", () => new Promise<void>(resolve => { calls.push("pi-1"); done=resolve; }));
  const second = writes.write("ws/pi", async () => { calls.push("pi-2"); });
  await writes.write("ws/codex", async () => { calls.push("codex"); });
  expect(calls).toEqual(["pi-1","codex"]);
  done(); await writes.settled("ws/pi"); await Promise.all([first,second]);
  expect(calls).toEqual(["pi-1","codex","pi-2"]);
});

it("selects a cached native model before the new chat's live catalog arrives", async () => {
  const select = document.createElement("select");
  const rows = ["p/one", "p/two"].map(id => ({ id, name: id }));
  let finish!: (catalog: AgentModelCatalog) => void;
  const picker = new AgentModelPicker(document.createElement("div"), select, document.createElement("input"),
    document.createElement("button"), document.createElement("select"),
    () => new Promise(resolve => { finish = resolve; }),
    async () => ({ models: rows, allow_custom: true }));
  picker.configure("draft", "pi", false, true);
  await vi.waitFor(() => expect(select.options[2]?.value).toBe("p/two"));
  expect(select.value).toBe("");
  expect(select.disabled).toBe(false);
  select.value = "p/two";
  select.dispatchEvent(new Event("change"));
  finish({ models: rows.slice(1), default_model: "p/two", allow_custom: true });
  await vi.waitFor(() => expect(select.textContent).not.toContain("p/one"));
  expect(picker.turnOptions("pi", "pi")).toMatchObject({ agent: "pi", agent_model: "p/two" });
  expect(picker.turnOptions("codex", "codex").agent_model).toBeUndefined();
});

it("sends the model picked after switching agents without waiting for another model's preferences", async () => {
  const select = document.createElement("select");
  const pi = { models: [{ id: "p/one", name: "One" }, { id: "p/two", name: "Two" }], allow_custom: true,
    preferences: { new_session: true, model: "p/one", efforts: { "p/one": "high" } } };
  const codex = { models: [{ id: "cx", name: "Codex" }] };
  let finish!: (catalog: AgentModelCatalog) => void;
  const picker = new AgentModelPicker(document.createElement("div"), select, document.createElement("input"),
    document.createElement("button"), document.createElement("select"),
    agent => agent === "pi" ? new Promise(resolve => { finish = resolve; }) : Promise.resolve(codex),
    async agent => agent === "pi" ? pi : codex);
  picker.configure("chat", "codex", false);
  await picker.readyForSend("codex");
  picker.configure("chat", "pi", true);
  await vi.waitFor(() => expect(select.textContent).toContain("Two"));
  select.value = "p/two";
  select.dispatchEvent(new Event("change"));
  let ready: boolean | undefined;
  const pending = picker.readyForSend("pi").then(result => { ready = result; });
  try {
    await vi.waitFor(() => expect(ready).toBe(true));
    expect(picker.turnOptions("pi", "pi")).toEqual({ agent: "pi", agent_model: "p/two", agent_effort: undefined });
  } finally {
    finish(pi);
    await pending;
  }
});

it.each(["capabilities first", "inventory first"])("keeps a newly selected agent's cached models after its first discovery fails (%s)", async order => {
  const select = document.createElement("select"), retry = document.createElement("button");
  const codex = { models: [{ id: "codex-model", name: "Codex model" }], allow_custom: true };
  const cached = { models: [{ id: "local/first", name: "First" }, { id: "local/second", name: "Second" }], allow_custom: true };
  const failed = { models: [], allow_custom: true, error: "Pi catalog offline" };
  let capabilities!: (menu: AgentModelCatalog) => void, listing!: (menu: AgentModelCatalog) => void;
  const picker = new AgentModelPicker(document.createElement("div"), select, document.createElement("input"), retry,
    document.createElement("select"), agent => agent === "pi" ? new Promise(resolve => { listing = resolve; }) : Promise.resolve(codex),
    agent => agent === "pi" ? new Promise(resolve => { capabilities = resolve; }) : Promise.resolve(codex));
  picker.configure("chat", "codex", false);
  await vi.waitFor(() => expect(select.textContent).toContain("Codex model"));
  picker.configure("chat", "pi", false);
  if (order === "inventory first") {
    listing(failed);
    await vi.waitFor(() => expect(select.title).toBe("Pi catalog offline"));
    capabilities(cached);
  } else {
    capabilities(cached);
    await vi.waitFor(() => expect(select.textContent).toContain("Second"));
    listing(failed);
  }
  await vi.waitFor(() => {
    expect(select.title).toBe("Pi catalog offline");
    expect(select.textContent).toContain("Second");
    expect(retry.disabled).toBe(false);
  });
  select.value = "local/second";
  select.dispatchEvent(new Event("change"));
  expect(picker.turnOptions("pi", "pi")).toMatchObject({ agent: "pi", agent_model: "local/second" });
  expect(select.textContent).not.toContain("Codex model");
  retry.click();
  listing({ models: [], allow_custom: true });
  await vi.waitFor(() => {
    expect(select.textContent).not.toContain("First");
    expect(select.textContent).toContain("Agent 未列出模型，可输入模型 ID");
  });
});

it("refreshes the selected agent's model list and keeps it usable after a temporary failure", async () => {
  const select = document.createElement("select"), retry = document.createElement("button");
  const initial = { models: [{ id: "p/one", name: "One" }, { id: "p/two", name: "Two" }], allow_custom: true };
  const load = vi.fn<() => Promise<AgentModelCatalog>>()
    .mockResolvedValueOnce(initial)
    .mockResolvedValueOnce({ models: null, allow_custom: true, error: "Pi offline" })
    .mockResolvedValueOnce({ models: [{ id: "p/new", name: "New" }], allow_custom: true })
    .mockResolvedValueOnce({ models: null, allow_custom: true, error: "Codex offline" });
  const picker = new AgentModelPicker(document.createElement("div"), select, document.createElement("input"),
    retry, document.createElement("select"), load);
  picker.configure("chat", "pi", false);
  await vi.waitFor(() => expect(select.options[1]?.value).toBe("p/one"));
  expect(retry.hidden).toBe(false);
  retry.click();
  await vi.waitFor(() => expect(select.title).toBe("Pi offline"));
  select.value = "p/two";
  select.dispatchEvent(new Event("change"));
  expect(picker.turnOptions("pi", "pi").agent_model).toBe("p/two");
  retry.click();
  await vi.waitFor(() => expect(Array.from(select.options, option => option.value)).toContain("p/new"));
  expect(select.value).toBe("p/two");
  expect(Array.from(select.options, option => option.value)).not.toContain("p/one");
  picker.configure("chat", "codex", false);
  await vi.waitFor(() => expect(select.title).toBe("Codex offline"));
  expect(select.textContent).not.toContain("Two");
});

it("refreshes the saved model without overwriting a turn that completes during the read", async () => {
  const select = document.createElement("select"), retry = document.createElement("button");
  let reply!: (menu: AgentModelCatalog) => void;
  const picker = new AgentModelPicker(document.createElement("div"), select, document.createElement("input"),
    retry, document.createElement("select"), () => new Promise(resolve => { reply = resolve; }));
  const catalog = (selected: string) => ({ models: ["one", "two"].map(id => ({ id, name: id })), selected });
  picker.configure("chat", "codex", false);
  reply(catalog("one"));
  await vi.waitFor(() => expect(select.value).toBe("one"));
  picker.confirm("codex", "one");
  retry.click();
  reply(catalog("two"));
  await vi.waitFor(() => expect(select.value).toBe("two"));
  retry.click();
  picker.confirm("codex", "one");
  reply(catalog("two"));
  await vi.waitFor(() => expect(retry.disabled).toBe(false));
  expect(select.value).toBe("one");
});

it("refreshes an expired catalog when switching agents and preserves a model picked during discovery", async () => {
  const now = vi.spyOn(Date, "now");
  const start = Date.now();
  try {
    now.mockReturnValue(start);
    const select = document.createElement("select");
    const requests: { agent: string; finish: (menu: AgentModelCatalog) => void }[] = [];
    const picker = new AgentModelPicker(document.createElement("div"), select, document.createElement("input"),
      document.createElement("button"), document.createElement("select"),
      (agent) => new Promise(finish => requests.push({ agent, finish })));
    const rows = ["p/first", "p/second"].map(id => ({ id, name: id }));
    picker.configure("chat", "pi", false);
    requests[0].finish({ models: rows });
    await picker.readyForSend("pi");
    picker.configure("chat", "codex", false);
    requests[1].finish({ models: [{ id: "codex-only", name: "Codex" }] });
    await picker.readyForSend("codex");
    picker.configure("chat", "pi", false);
    expect(requests.map(request => request.agent)).toEqual(["pi", "codex"]);
    picker.configure("chat", "codex", false);
    now.mockReturnValue(start + 5 * 60 * 1000);
    picker.configure("chat", "pi", false);
    expect(requests.map(request => request.agent)).toEqual(["pi", "codex", "pi"]);
    expect(select.disabled).toBe(false);
    select.value = "p/second";
    select.dispatchEvent(new Event("change"));
    requests[2].finish({ models: [...rows, { id: "p/new", name: "New" }] });
    await Promise.resolve();
    expect(select.options[3]?.value).toBe("p/new");
    expect(select.value).toBe("p/second");
    expect(picker.turnOptions("pi", "pi").agent_model).toBe("p/second");
  } finally {
    now.mockRestore();
  }
});

it("reuses each agent's in-flight catalog when switching back and cancels it on conversation changes", async () => {
  const select = document.createElement("select");
  const requests: { agent: string; signal: AbortSignal; finish: (menu: AgentModelCatalog) => void }[] = [];
  const picker = new AgentModelPicker(document.createElement("div"), select, document.createElement("input"),
    document.createElement("button"), document.createElement("select"),
    (agent, signal) => new Promise(finish => requests.push({ agent, signal, finish })));
  picker.configure("chat-1", "pi", false);
  picker.configure("chat-1", "codex", false);
  picker.configure("chat-1", "pi", false);
  expect(requests.map(req => req.agent)).toEqual(["pi", "codex"]);
  expect(requests[0].signal.aborted).toBe(false);
  requests[0].finish({ models: [{ id: "p/second", name: "Second" }] });
  await vi.waitFor(() => expect(select.disabled).toBe(false));
  select.value = "p/second";
  select.dispatchEvent(new Event("change"));
  expect(picker.turnOptions("pi", "pi").agent_model).toBe("p/second");
  picker.configure("chat-2", "codex", false);
  expect(requests[1].signal.aborted).toBe(true);
  requests[1].finish({ models: [{ id: "stale", name: "Stale" }] });
  requests[2].finish({ models: [{ id: "current", name: "Current" }] });
  await vi.waitFor(() => expect(Array.from(select.options, row => row.value)).toEqual(["", "current"]));
});

it("queues Enter with a newly picked model or effort instead of steering the running Pi model", async () => {
  const model = document.createElement("select"), effort = document.createElement("select");
  const picker = new AgentModelPicker(document.createElement("div"), model, document.createElement("input"),
    document.createElement("button"), effort, async () => ({
      models: ["p/one", "p/two"].map(id => ({ id, name: id })), selected: "p/one",
      efforts: { "p/one": { options: [{ id: "high", name: "High" }] } },
    }));
  picker.configure("chat", "pi", false);
  await vi.waitFor(() => expect(model.value).toBe("p/one"));
  model.dispatchEvent(new Event("change"));
  const sent = picker.draftRevision("pi");
  picker.configure("chat", "pi", true);
  const actions = (revision?: symbol) => busyInputActions("review", "pi", false, "pi", new Set(["steering", "follow_up"]),
    false, false, picker.hasNextTurnSelection("pi", revision));
  expect(actions(sent).steer).toBe(true);
  effort.value = "high";
  effort.dispatchEvent(new Event("change"));
  expect(actions(sent)).toEqual({ steer: false, followUp: false, queue: "message" });
  model.value = "p/two";
  model.dispatchEvent(new Event("change"));
  expect(actions(sent).steer).toBe(false);
  expect(actions(undefined).steer).toBe(false);
  expect(picker.queueSelection("pi")).toEqual({ agent_model: "p/two", agent_effort: undefined });
  // The queued child can accept native steering once its start event matches
  // the local choice. An unrelated agent's start never consumes that choice.
  expect(picker.hasNextTurnSelection("pi", undefined, { agent: "pi", agent_model: "p/two" })).toBe(false);
  expect(picker.hasNextTurnSelection("pi", undefined, { agent: "codex", agent_model: "p/two" })).toBe(true);
});

it("recovers an empty native catalog when returning to that agent", async () => {
  const select = document.createElement("select");
  const load = vi.fn<() => Promise<AgentModelCatalog>>()
    .mockResolvedValueOnce({ models: [] })
    .mockResolvedValueOnce({ models: [{ id: "installed", name: "Installed" }] });
  const picker = new AgentModelPicker(document.createElement("div"), select, document.createElement("input"),
    document.createElement("button"), document.createElement("select"), load);
  picker.configure("chat", "cursor", false);
  await vi.waitFor(() => expect(select.textContent).toContain("Agent 未提供可切换的模型"));
  picker.configure("chat", "", false);
  picker.configure("chat", "cursor", false);
  await vi.waitFor(() => expect(select.disabled).toBe(false));
  select.value = "installed";
  select.dispatchEvent(new Event("change"));
  expect(picker.selectionFor("cursor")).toBe("installed");
});

it("edits a resumed agent's model while discovery is pending using its bound transport", async () => {
  const select = document.createElement("select");
  const input = document.createElement("input");
  const catalogs = new Map<string, (menu: AgentModelCatalog) => void>();
  const metadata = new Map<string, (menu: AgentModelCatalog) => void>();
  const picker = new AgentModelPicker(document.createElement("div"), select, input,
    document.createElement("button"), document.createElement("select"),
    agent => new Promise(resolve => catalogs.set(agent, resolve)),
    agent => new Promise(resolve => metadata.set(agent, resolve)));

  picker.configure("existing", "codex", false);
  picker.configure("existing", "pi", false);
  metadata.get("pi")!({ models: null, selected: "p/old", allow_custom: true });
  metadata.get("codex")!({ models: null, selected: "wrong" });
  await vi.waitFor(() => expect(select.value).toBe("p/old"));
  select.selectedIndex = Array.from(select.options).findIndex(row => row.text === "输入模型 ID…");
  select.dispatchEvent(new Event("change"));
  input.value = "p/private";
  input.dispatchEvent(new Event("input"));
  expect(picker.turnOptions("pi", "pi").agent_model).toBe("p/private");
  catalogs.get("codex")!({ models: [{ id: "wrong", name: "Wrong" }] });
  catalogs.get("pi")!({ models: [], error: "offline", allow_custom: true });
  await vi.waitFor(() => expect(select.title).toBe("offline"));
  expect(input.value).toBe("p/private");
  expect(select.textContent).not.toContain("wrong");

  picker.configure("existing", "cursor", false);
  metadata.get("cursor")!({ models: null, selected: "acp/only" });
  await vi.waitFor(() => expect(select.value).toBe("acp/only"));
  expect(select.textContent).not.toContain("输入模型 ID");
  expect(input.hidden).toBe(true);
});

it.each([true, false])("checks a restored custom model against the receiving agent's native list before sending (listed: %s)", async listed => {
  const scope = "restored-custom-" + listed, id = "p/private";
  saveModelDraft(scope, "pi", { custom: true, draft: id });
  const select = document.createElement("select"), input = document.createElement("input"), remember = vi.fn();
  let finish!: (catalog: AgentModelCatalog) => void;
  const picker = new AgentModelPicker(document.createElement("div"), select, input, document.createElement("button"),
    document.createElement("select"), agent => agent === "pi" ? new Promise(resolve => { finish = resolve; })
      : Promise.resolve({ models: [{ id: "codex-only", name: "Codex" }] }),
    async () => ({ models: null, allow_custom: false }), undefined, remember);
  picker.configure(scope, "codex", false);
  await picker.readyForSend("codex");
  picker.configure(scope, "pi", false);
  let ready: boolean | undefined;
  const sent = picker.readyForSend("pi").then(result => { ready = result; });
  await new Promise(resolve => setTimeout(resolve, 0));
  expect(ready).toBeUndefined();
  expect(remember).not.toHaveBeenCalled();
  const ids = listed ? ["p/listed", id] : ["p/listed"];
  finish({ models: ids.map(id => ({ id, name: id })), selected: "p/listed" });
  await sent;
  expect(ready).toBe(listed);
  expect(input.hidden).toBe(true);
  expect(readAgentDraft(scope).models?.pi?.draft).toBe(id);
  expect(select.selectedOptions[0].textContent).toContain(id);
  expect(picker.turnOptions("pi", "pi").agent_model).toBe(id);
  expect(picker.turnOptions("codex", "codex").agent_model).toBeUndefined();
  if (!listed) {
    expect(select.validationMessage).toContain("重新选择");
    select.value = "p/listed";
    select.dispatchEvent(new Event("change"));
    expect(await picker.readyForSend("pi")).toBe(true);
    expect(picker.selectionFor("pi")).toBe("p/listed");
  }
  expect(remember).not.toHaveBeenCalledWith("pi", { model: id, custom: true });
});

it("keeps the next model and effort chosen during generation after the previous turn completes or replays", async () => {
  const select = document.createElement("select");
  const effort = document.createElement("select");
  const picker = new AgentModelPicker(document.createElement("div"), select, document.createElement("input"),
    document.createElement("button"), effort, async () => ({
      models: ["first", "second"].map(id => ({ id, name: id })), selected: "first",
      efforts: { second: { options: [{ id: "high", name: "High" }] } },
    }));
  picker.configure("draft", "pi", false);
  await vi.waitFor(() => expect(select.value).toBe("first"));
  const sent = picker.draftRevision("pi");
  picker.configure("draft", "pi", true);
  expect(select.disabled).toBe(false);
  select.value = "second";
  select.dispatchEvent(new Event("change"));
  effort.value = "high";
  effort.dispatchEvent(new Event("change"));
  picker.bindDraft("saved");
  picker.confirm("pi", "first", undefined, undefined, sent);
  picker.configure("saved", "pi", false);
  picker.confirm("pi", "first"); // History replay did not submit this draft.
  expect(select.value).toBe("second");
  expect(effort.value).toBe("high");
  expect(picker.turnOptions("pi", "pi")).toEqual({ agent: "pi", agent_model: "second", agent_effort: "high" });
  picker.confirm("pi", "second", "high", undefined, picker.draftRevision("pi"));
  expect(picker.selectionFor("pi")).toBeUndefined();
  expect(select.value).toBe("second");
});

it("captures displayed queue settings and restores a withdrawn card without replacing newer picks", async () => {
  const model = document.createElement("select");
  const picker = new AgentModelPicker(document.createElement("div"), model, document.createElement("input"),
    document.createElement("button"), document.createElement("select"), async () => ({
      models: ["active", "queued"].map(id => ({ id, name: id })), selected: "active", selected_effort: "high",
      efforts: { active: { options: [{ id: "high", name: "high" }] }, queued: { options: [{ id: "low", name: "low" }] } },
    }));
  picker.configure("saved", "pi", true);
  await vi.waitFor(() => expect(model.value).toBe("active"));
  expect(picker.queueSelection("pi")).toEqual({ agent_model: "active", agent_effort: "high" });
  expect(picker.queueSelection("codex")).toEqual({});
  picker.restoreQueuedSelection("pi", { model: "queued", effort: "low" });
  expect(picker.queueSelection("pi")).toEqual({ agent_model: "queued", agent_effort: "low" });
  model.value = "active";
  model.dispatchEvent(new Event("change"));
  picker.restoreQueuedSelection("pi", { model: "queued", effort: "low" });
  expect(picker.queueSelection("pi").agent_model).toBe("active");
});

it("keeps Codex and Claude settings in a collapsed model picker and updates its visible name", async () => {
  const page = new DOMParser().parseFromString(readFileSync("index.html", "utf8"), "text/html");
  const controls = page.querySelector<HTMLDetailsElement>("details#agent-model-controls")!;
  const name = controls.querySelector("summary .agent-model-name")!;
  const model = controls.querySelector<HTMLSelectElement>("#agent-model-select")!;
  const input = controls.querySelector("input")!;
  const picker = new AgentModelPicker(controls, model, input, controls.querySelector("button")!,
    controls.querySelector<HTMLSelectElement>("#agent-effort-select")!, async agent => ({
      models: [{ id: "one", name: agent === "codex" ? "GPT-6-Astra" : "workbuddy/deepseek-v4.1-flash" }],
      selected: "one", allow_custom: true,
    }));

  picker.configure("draft", "codex", false);
  await vi.waitFor(() => expect(name.textContent).toBe("GPT-6-Astra"));
  expect(controls.open).toBe(false);
  controls.open = true;
  picker.configure("draft", "claude", false);
  await vi.waitFor(() => expect(name.textContent).toBe("workbuddy/deepseek-v4.1-flash"));
  expect(controls.open).toBe(false);

  controls.open = true;
  model.value = Array.from(model.options).find(option => option.text === "输入模型 ID…")!.value;
  model.dispatchEvent(new Event("change"));
  input.value = "custom/picked";
  input.dispatchEvent(new Event("input"));
  expect(name.textContent).toBe("custom/picked");
  input.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
  expect(controls.open).toBe(false);
  input.value = "";
  input.dispatchEvent(new Event("input"));
  expect(picker.validFor("claude")).toBe(false);
  expect(controls.open).toBe(true);
  page.querySelector("#input")!.dispatchEvent(new Event("pointerdown", { bubbles: true }));
  expect(controls.open).toBe(false);
});

it("routes the displayed model with the receiving agent and keeps other picks when the new chat binds", async () => {
  const page = new DOMParser().parseFromString(readFileSync("index.html", "utf8"), "text/html");
  const agentSelect = page.querySelector<HTMLSelectElement>("#chat-agent")!;
  const controls = page.querySelector<HTMLElement>("#agent-model-controls")!;
  const modelSelect = controls.querySelector<HTMLSelectElement>("#agent-model-select")!;
  expect(controls.parentElement).toBe(page.querySelector(".composer"));
  expect(page.querySelector("#model-select")!.closest(".composer")).toBe(controls.parentElement);
  const agents = new SingleAgentPicker(agentSelect, () => {});
  await agents.loadWorkspace(async () => ({scheduler:"bqagent", participants:["codex","pi"].map(id => ({
    id, name:id, kind:"external", available:true, transport:id === "pi" ? "cli" : "app-server",
  }))}));
  const load = vi.fn(async (agent: string) => ({
    models: ["first", "second"].map(id => ({id:agent + "/" + id, name:id})),
    default_model: agent + "/first",
  }));
  const picker = new AgentModelPicker(controls, modelSelect, controls.querySelector("input")!,
    controls.querySelector("button")!, controls.querySelector<HTMLSelectElement>("#agent-effort-select")!, load);
  const route = (scope: string, text: string, busy = false) => {
    const responder = agents.externalResponder(text);
    picker.configure(scope, responder || "", busy);
    return picker.turnOptions(agents.requestAgent, responder);
  };
  agentSelect.value = "codex";
  agentSelect.dispatchEvent(new Event("change"));
  for (const text of ["review", "/pi revise"]) {
    const responder = agents.externalResponder(text)!;
    route("draft", text);
    await vi.waitFor(() => expect(modelSelect.value).toBe(responder + "/first"));
    modelSelect.value = responder + "/second";
    modelSelect.dispatchEvent(new Event("change"));
    expect(route("draft", text)).toMatchObject({agent:responder, agent_model:responder + "/second"});
  }
  expect(agentSelect.value).toBe("codex");
  route("draft", "/pi revise", true);
  const sentRevision = picker.draftRevision("pi");
  picker.bindDraft("assigned");
  picker.confirm("pi", "pi/second", undefined, undefined, sentRevision);
  route("assigned", "/pi next");
  await vi.waitFor(() => expect(load).toHaveBeenCalledTimes(3));
  expect(route("assigned", "review")).toMatchObject({agent:"codex", agent_model:"codex/second"});
  await vi.waitFor(() => expect(modelSelect.value).toBe("codex/second"));
  expect(route("another-chat", "review").agent_model).toBeUndefined();
  await vi.waitFor(() => expect(modelSelect.value).toBe("codex/first"));
});

it("allows a CLI model ID during discovery and rejects a blank custom ID", async () => {
  const select = document.createElement("select"), input = document.createElement("input");
  let finish!: (catalog: AgentModelCatalog) => void;
  const picker = new AgentModelPicker(document.createElement("div"), select, input, document.createElement("button"),
    document.createElement("select"), () => new Promise(resolve => { finish = resolve; }));
  picker.configure("new", "pi", false, true);
  expect(select.disabled).toBe(false);
  select.selectedIndex = 1;
  select.dispatchEvent(new Event("change"));
  expect(picker.validFor("pi")).toBe(false);
  input.value = "p/private";
  input.dispatchEvent(new Event("input"));
  finish({models:[], allow_custom:true, error:"catalog unavailable"});
  await vi.waitFor(() => expect(select.title).toBe("catalog unavailable"));
  expect(picker.validFor("pi")).toBe(true);
  expect(picker.turnOptions("pi", "pi").agent_model).toBe("p/private");
  picker.configure("new", "cursor", false);
  expect(select.disabled).toBe(true);
  expect(input.hidden).toBe(true);
});

it("submits the native effort for the displayed model and clears it when switching to a model without reasoning", async () => {
  const model = document.createElement("select");
  const effort = document.createElement("select");
  const picker = new AgentModelPicker(document.createElement("div"), model, document.createElement("input"), document.createElement("button"), effort,
    async () => ({models: [{id:"p/reason",name:"Reason"},{id:"p/plain",name:"Plain"}], default_model:"p/reason", selected_effort:"low",
      efforts: {"p/reason":{options:[{id:"low",name:"Low"},{id:"xhigh",name:"Extra high"}]},"p/plain":{options:[]}}}));
  picker.configure("chat", "pi", false);
  await vi.waitFor(() => expect(effort.value).toBe("low"));
  effort.value = "xhigh";
  effort.dispatchEvent(new Event("change"));
  expect(picker.selectionFor("pi")).toBe("p/reason");
  expect(picker.effortFor("pi")).toBe("xhigh");
  expect(picker.effortFor("codex")).toBeUndefined();
  picker.configure("chat", "pi", true);
  expect(effort.disabled).toBe(false);
  picker.confirm("pi", "p/reason", "xhigh", undefined, picker.draftRevision("pi"));
  expect(effort.value).toBe("xhigh");
  picker.configure("chat", "pi", false);
  model.value = "p/plain";
  model.dispatchEvent(new Event("change"));
  expect(effort.hidden).toBe(true);
  expect(picker.effortFor("pi")).toBeUndefined();
});

it("previews ACP effort choices without letting a late model response replace the latest selection", async () => {
  const model = document.createElement("select");
  const effort = document.createElement("select");
  const pending = new Map<string, (result: AgentModelCatalog) => void>();
  const picker = new AgentModelPicker(document.createElement("div"), model, document.createElement("input"), document.createElement("button"), effort,
    async (_agent,_signal,preview) => preview ? new Promise(resolve => pending.set(preview,resolve))
      : {models:["a","b","c"].map(id => ({id,name:id})), selected:"a",session_scoped_effort:true,efforts:{a:{options:[]}}});
  picker.configure("chat","cursor",false);
  await vi.waitFor(() => expect(model.value).toBe("a"));
  for (const id of ["b","c"]) {
    model.value=id;
    model.dispatchEvent(new Event("change"));
  }
  pending.get("c")!({models:[],efforts:{c:{options:[{id:"vendor-deep",name:"Deep"}]}},selected_effort:"vendor-deep"});
  pending.get("b")!({models:[],efforts:{b:{options:[{id:"stale",name:"Wrong"}]}}});
  await vi.waitFor(() => expect(effort.value).toBe("vendor-deep"));
  expect(effort.textContent).not.toContain("Wrong");
});

it("keeps the selected model with its agent and drops stale catalogs and selections from other conversations", async () => {
  const controls = document.createElement("div");
  const select = document.createElement("select");
  const pending: Record<string, (menu: AgentModelCatalog) => void> = {};
  const picker = new AgentModelPicker(controls, select, document.createElement("input"), document.createElement("button"),
    document.createElement("select"),
    name => new Promise(resolve => { pending[name] = resolve; }));
  picker.configure("chat1", "codex", false);
  picker.configure("chat1", "pi", false);
  pending.pi({ models: [{ id: "p/model", name: "Pi model" }] });
  pending.codex({ models: [{ id: "wrong", name: "stale" }] });
  await vi.waitFor(() => expect(select.options[1].text).toBe("Pi model"));
  select.value = "p/model";
  select.dispatchEvent(new Event("change"));
  expect(picker.selectionFor("pi")).toBe("p/model");
  expect(picker.selectionFor("codex")).toBeUndefined();
  picker.configure("chat1", "", false);
  expect(controls.hidden).toBe(true);
  picker.configure("chat1", "pi", true);
  expect(select.disabled).toBe(false);
  expect(select.value).toBe("p/model");
  picker.confirm("pi", "p/model", undefined, undefined, picker.draftRevision("pi"));
  expect(picker.selectionFor("pi")).toBeUndefined();
  expect(select.value).toBe("p/model");
  picker.configure("chat2", "pi", false);
  pending.pi({ models: [{ id: "other", name: "Restored" }], selected: "other" });
  await vi.waitFor(() => expect(select.value).toBe("other"));
  expect(picker.selectionFor("pi")).toBeUndefined();
});

it("allows retrying model discovery and uses a typed ID when a CLI cannot list models", async () => {
  const select = document.createElement("select"), input = document.createElement("input"), retry = document.createElement("button");
  const load = vi.fn<() => Promise<AgentModelCatalog>>().mockResolvedValueOnce({models: [], allow_custom: true, error: "unsupported"}).mockResolvedValue({models: [{id:"custom",name:"Custom"}], allow_custom: true});
  const picker = new AgentModelPicker(document.createElement("div"), select, input, retry, document.createElement("select"), load);
  picker.configure("chat", "pi", false);
  await vi.waitFor(() => expect(select.title).toBe("unsupported"));
  select.selectedIndex = 1;
  select.dispatchEvent(new Event("change"));
  input.value = "custom";
  input.dispatchEvent(new Event("input"));
  expect(picker.selectionFor("pi")).toBe("custom");
  retry.click();
  await vi.waitFor(() => expect(retry.textContent).toBe("刷新"));
  expect(Array.from(select.options, option => option.text)).toContain("Custom");
  expect(picker.selectionFor("pi")).toBe("custom");
});

it("retries an empty agent catalog without reopening the conversation", async () => {
  const select = document.createElement("select"), retry = document.createElement("button");
  const load = vi.fn<() => Promise<AgentModelCatalog>>()
    .mockResolvedValueOnce({ models: [] }).mockResolvedValue({ models: [{ id: "native", name: "Native" }] });
  const picker = new AgentModelPicker(document.createElement("div"), select, document.createElement("input"),
    retry, document.createElement("select"), load);
  picker.configure("saved", "opencode", false);
  await vi.waitFor(() => expect(select.textContent).toContain("未提供可切换的模型"));
  expect(retry.hidden).toBe(false);
  retry.click();
  await vi.waitFor(() => expect(select.disabled).toBe(false));
  select.value = "native";
  select.dispatchEvent(new Event("change"));
  expect(picker.turnOptions("opencode", "opencode").agent_model).toBe("native");
});

it("shows the native default and allows choosing a custom ID alongside discovered models", async () => {
  const select = document.createElement("select");
  const input = document.createElement("input");
  const picker = new AgentModelPicker(document.createElement("div"), select, input,
    document.createElement("button"), document.createElement("select"), async () => ({
      default_model: "p/default", models: [{ id: "p/default", name: "Default model" }], allow_custom: true,
    }));
  picker.configure("chat", "pi", false);
  await vi.waitFor(() => expect(select.value).toBe("p/default"));
  expect(picker.selectionFor("pi")).toBeUndefined();
  select.selectedIndex = Array.from(select.options).findIndex(option => option.text === "输入模型 ID…");
  select.dispatchEvent(new Event("change"));
  input.value = "provider/private-model";
  input.dispatchEvent(new Event("input"));
  expect(picker.selectionFor("pi")).toBe("provider/private-model");
});

it("keeps a pending model and reasoning choice across native compaction and honors the bound transport", async () => {
  const model = document.createElement("select"), effort = document.createElement("select");
  const picker = new AgentModelPicker(document.createElement("div"), model, document.createElement("input"),
    document.createElement("button"), effort, async agent => ({
      supports_compact:agent === "pi", models:["old","next"].map(id=>({id,name:id})),selected:"old",
      efforts:{ next:{options:[{id:"high",name:"High"}]} },
    }));
  picker.configure("chat","pi",false);
  await vi.waitFor(() => expect(model.value).toBe("old"));
  model.value="next";
  model.dispatchEvent(new Event("change"));
  effort.value="high";
  effort.dispatchEvent(new Event("change"));
  const compact=picker.isCompactCommand("pi", "/compact 保留测试", false);
  expect(compact).toBe(true);
  expect(picker.turnOptions("codex","pi",compact)).toEqual({agent:"pi"});
  picker.confirm("pi","old",undefined,"compact");
  expect(picker.turnOptions("pi","pi")).toEqual({agent:"pi",agent_model:"next",agent_effort:"high"});
  expect(model.value).toBe("next");
  expect(picker.isCompactCommand("pi","/compaction",true)).toBe(false);
  picker.configure("chat","codex",false);
  await vi.waitFor(() => expect(model.value).toBe("old"));
  expect(picker.isCompactCommand("codex","/compact",true)).toBe(false);
});
