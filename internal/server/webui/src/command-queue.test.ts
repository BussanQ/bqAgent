import { beforeEach, expect, it, vi } from "vitest";
import { APIClient } from "./api-client";
import { CommandQueue, type CommandQueueSnapshot } from "./command-queue";
import type { SentAttachments } from "./attachments";
import { QueueUploadCache } from "./queue-upload-cache";

beforeEach(() => sessionStorage.clear());

const row = { id:"op", parent:"active", agent:"pi", text:"/compact 保留测试", state:"waiting" };
const snapshot = (revision:number, changes:Partial<CommandQueueSnapshot> = {}):CommandQueueSnapshot => ({
  session_id:"chat", revision, paused:false, items:[], ...changes,
});
const response = (view:CommandQueueSnapshot) => new Response(JSON.stringify(view));

function view(transport:typeof fetch, makeID = () => "op") {
  const root=document.createElement("div");
  const draft={ text:"/pi /compact 保留测试" };
  const notify=vi.fn(), watch=vi.fn(), turn={ id:"active" };
  const queue=new CommandQueue(root,new APIClient(transport),{
    turn:()=>turn.id, draft:()=>draft.text, setDraft:s=>{draft.text=s;}, newID:makeID, notify, watch,
  });
  return {root,draft,notify,queue,watch,turn};
}

function queueButton(root: HTMLElement, label: string): HTMLButtonElement {
  const button = Array.from(root.querySelectorAll("button")).find(node => node.textContent === label && !node.closest("[hidden]"));
  if (!button) throw new Error("Missing queue action " + label);
  return button;
}

it("posts the chosen Codex interaction mode and shows it on the acknowledged queue card", async () => {
  const card={id:"op",parent:"active",agent:"codex",text:"review",state:"waiting",kind:"message" as const,
    selection:{model:"m",interaction_mode:"plan"}};
  const transport=vi.fn<typeof fetch>(async (_url,init) => init?.method==="POST"
    ? response(snapshot(1,{items:[card],receipt:card})) : response(snapshot(0)));
  const ui=view(transport);
  ui.queue.show("ws","chat");
  ui.draft.text=card.text;
  await ui.queue.enqueue("codex",card.text,{agent_interaction_mode:"plan",agent_model:"m"});
  const post=transport.mock.calls.find(([,init])=>init?.method==="POST");
  expect(JSON.parse(String(post?.[1]?.body))).toMatchObject({kind:"message",agent:"codex",agent_interaction_mode:"plan",agent_model:"m"});
  expect(ui.root.querySelector(".queued-state")?.textContent).toContain("规划 · m");
});

it("accepts a compaction once, handles SSE before the POST and retains newer composer text", async () => {
  let finish!: (response:Response)=>void;
  const transport=vi.fn<typeof fetch>(async (_url,init)=>init?.method==="POST" ? new Promise(resolve=>{finish=resolve;}) : response(snapshot(0)));
  const ui=view(transport);
  ui.queue.show("ws","chat");
  const submitted=ui.queue.enqueue("pi",row.text);
  await ui.queue.enqueue("pi",row.text);
  const posts=transport.mock.calls.filter(([,init])=>init?.method==="POST");
  expect(posts).toHaveLength(1);
  expect(JSON.parse(String(posts[0][1]!.body))).toMatchObject({id:"op",session_id:"chat",workspace_id:"ws",parent_turn_id:"active",agent:"pi"});
  const queued=snapshot(2,{items:[row]});
  ui.queue.apply("ws",queued);
  expect(ui.draft.text).toBe("");
  ui.draft.text="新草稿";
  finish(response({...queued,receipt:row}));
  await submitted;
  ui.queue.apply("ws",snapshot(1));
  expect(ui.draft.text).toBe("新草稿");
  expect(ui.root.textContent).toContain("已排队");
  expect(queueButton(ui.root, "撤回")).toBeTruthy();
  expect(queueButton(ui.root, "编辑")).toBeTruthy();
});

it("queues distinct text cards with their selected models before an earlier HTTP response completes", async () => {
  const replies: Array<(r: Response) => void> = [];
  const submitted: Record<string, string>[] = [];
  const ui = view(async (_url, init) => {
    if (init?.method !== "POST") return response(snapshot(0));
    submitted.push(JSON.parse(String(init.body)));
    return new Promise(resolve => replies.push(resolve));
  }, () => "msg-" + (submitted.length + 1));
  ui.queue.show("ws", "chat");
  ui.draft.text = "first";
  const first = ui.queue.enqueue("pi", "first", { agent_model: "p/m", agent_effort: "high" });
  await ui.queue.enqueue("pi", "first", { agent_model: "p/m", agent_effort: "high" });
  ui.draft.text = "/codex next";
  const next = ui.queue.enqueue("codex", "next", { agent_model: "another" });
  expect(submitted.map(s => [s.id, s.agent, s.text, s.kind, s.agent_model, s.agent_effort])).toEqual([
    ["msg-1", "pi", "first", "message", "p/m", "high"], ["msg-2", "codex", "next", "message", "another", undefined],
  ]);
  const one = { ...row, id: "msg-1", kind: "message" as const, text: "first", selection: { model: "p/m", effort: "high" } };
  const two = { ...one, id: "msg-2", agent: "codex", text: "next", selection: { model: "another" } };
  replies[1](response(snapshot(2, { items: [one, two], receipt: two })));
  await next;
  ui.draft.text = "later";
  replies[0](response(snapshot(1, { items: [one], receipt: one })));
  await first;
  expect(ui.draft.text).toBe("later");
  expect(ui.root.textContent).toContain("p/m · high");
  expect(ui.root.textContent).toContain("another");
});

it("reconciles an empty snapshot by replaying the original operation ID and picks", async () => {
  const posts: Record<string, string>[] = [];
  const ui = view(async (_url, init) => {
    if (init?.method !== "POST") return response(snapshot(0));
    posts.push(JSON.parse(String(init.body)));
    if (posts.length === 1) throw new TypeError("lost acknowledgement");
    return response(snapshot(4, { receipt: { ...row, kind: "message", text: "next", state: "done" } }));
  });
  ui.queue.show("ws", "chat");
  ui.draft.text = "next";
  await ui.queue.enqueue("pi", "next", { agent_model: "provider/model" });
  ui.turn.id = "";
  expect(await ui.queue.reconcileDraft()).toBe(false);
  expect(posts).toHaveLength(2);
  expect(posts[0]).toEqual(posts[1]);
  expect(ui.draft.text).toBe("");
});

it("follows a resumed child only for the currently visible queue revision", async () => {
  let resume!: (r: Response) => void;
  const ui = view(async (_url, init) => init?.method === "POST" ? new Promise(resolve => { resume = resolve; }) : response(snapshot(0)));
  ui.turn.id = "";
  ui.queue.show("ws", "chat");
  const held = snapshot(1, { paused: true, items: [row] });
  ui.queue.apply("ws", held);
  const button = queueButton(ui.root, "继续队列");
  button.click();
  ui.queue.apply("ws", snapshot(3)); // Another viewer has already observed completion.
  resume(response(snapshot(2, { turn_id: "stale", items: [{ ...row, state: "running" }] })));
  await vi.waitFor(() => expect(button.disabled).toBe(false));
  expect(ui.watch).not.toHaveBeenCalled();
  ui.queue.apply("ws", { ...held, revision: 4 });
  queueButton(ui.root, "继续队列").click();
  resume(response(snapshot(5, { turn_id: "actual", items: [{ ...row, state: "running" }] })));
  await vi.waitFor(() => expect(ui.watch).toHaveBeenCalledWith("actual"));
  ui.queue.reset();
});

it("recovers a lost POST receipt after reload without rerunning a finished command", async () => {
  const before=view(async (_url,init)=>{
    if(init?.method==="POST") throw new Error("disconnected");
    return response(snapshot(0));
  });
  before.queue.show("ws","chat");
  await before.queue.enqueue("pi",row.text);
  expect(before.draft.text).toContain("/compact");
  const requests=vi.fn<typeof fetch>(async url=>{
    expect(String(url)).toContain("id=op");
    return response(snapshot(5,{receipt:{...row,state:"done",turn_id:"finished"}}));
  });
  const after=view(requests);
  after.queue.show("ws","chat");
  await vi.waitFor(()=>expect(after.draft.text).toBe(""));
  expect(requests.mock.calls.every(([,init])=>!init?.method)).toBe(true);
  expect(after.root.hidden).toBe(true);
});

it("restores a withdrawn command only to an empty draft and isolates late queue responses by workspace", async () => {
  let release!: (r:Response)=>void;
  const transport=vi.fn<typeof fetch>(async (_url,init)=>{
    if(init?.method==="DELETE")return new Promise(resolve=>{release=resolve;});
    return response(snapshot(0));
  });
  const ui=view(transport);
  ui.queue.show("ws","chat");
  const waiting=snapshot(2,{paused:true,items:[row]});
  ui.queue.apply("ws",waiting);
  expect(ui.root.textContent).toContain("继续队列");
  ui.draft.text="";
  queueButton(ui.root, "撤回").click();
  release(response(snapshot(3,{receipt:{...row,state:"withdrawn"}})));
  await vi.waitFor(()=>expect(ui.draft.text).toBe("/pi /compact 保留测试"));
  ui.queue.apply("ws",{...waiting,revision:4});
  queueButton(ui.root, "撤回").click();
  ui.queue.show("another","chat");
  ui.draft.text="keep";
  release(response(snapshot(5,{receipt:{...row,state:"withdrawn"}})));
  await vi.waitFor(()=>expect(ui.root.hidden).toBe(true));
  expect(ui.draft.text).toBe("keep");
  ui.queue.apply("ws",{...waiting,revision:9});
  expect(ui.root.hidden).toBe(true);
});

it("shows uncertain handoffs without offering to execute them again", async () => {
  const ui=view(async()=>response(snapshot(0)));
  ui.queue.show("ws","chat");
  ui.queue.apply("ws",snapshot(3,{paused:true,items:[{...row,state:"unknown",text:"/compact <img src=x>"}]}));
  expect(ui.root.textContent).toContain("执行结果未确认");
  expect(ui.root.querySelector("img")).toBeNull();
  expect(ui.root.textContent).not.toContain("继续队列");
  expect(ui.root.querySelector("button")?.textContent).toBe("移除");
});

const editableRow = { ...row, text: "original", kind: "message" as const, selection: { model: "p/chosen", effort: "high" } };

function setEditorText(root: HTMLElement, text: string): HTMLTextAreaElement {
  const input = root.querySelector("textarea")!;
  input.value = text;
  input.dispatchEvent(new Event("input"));
  return input;
}

it("holds a queue entry while editing, preserves focus across SSE and reconciles a lost save after reload", async () => {
  let nextID = 0;
  const posts: Record<string, unknown>[] = [];
  let current = snapshot(1, { items: [editableRow] });
  let completed = false;
  const transport: typeof fetch = async (_url, init) => {
    if (init?.method !== "POST") return response(current);
    const req = JSON.parse(String(init.body));
    posts.push(req);
    if (req.action === "hold") current = snapshot(2, { items: [{ ...editableRow, editing: true }] });
    if (req.action === "edit" && !completed) {
      completed = true;
      current = snapshot(4);
      throw new TypeError("reply lost");
    }
    return response(current);
  };
  const ui = view(transport, () => "key-" + ++nextID);
  document.body.appendChild(ui.root);
  ui.queue.show("ws", "chat");
  await vi.waitFor(() => expect(queueButton(ui.root, "编辑")).toBeTruthy());
  queueButton(ui.root, "编辑").click();
  await vi.waitFor(() => expect(queueButton(ui.root, "保存").disabled).toBe(false));
  const input = setEditorText(ui.root, "my revised text");
  input.focus();
  input.setSelectionRange(3, 8);
  current = snapshot(3, { items: [{ ...editableRow, editing: true }, { ...editableRow, id: "other" }] });
  ui.queue.apply("ws", current);
  expect(document.activeElement).toBe(input);
  expect([input.selectionStart, input.selectionEnd]).toEqual([3, 8]);
  queueButton(ui.root, "保存").click();
  await vi.waitFor(() => expect(queueButton(ui.root, "重试保存").disabled).toBe(false));
  ui.queue.show("elsewhere", "");
  const after = view(transport, () => "unused");
  after.queue.show("ws", "chat");
  await vi.waitFor(() => expect(after.root.hidden).toBe(true));
  const writes = posts.filter(req => req.action === "edit");
  expect(writes).toHaveLength(2);
  expect(writes[0]).toMatchObject({ text: "my revised text", id: row.id, workspace_id: "ws", session_id: "chat", body_version: 0 });
  expect(writes[1]).toEqual(writes[0]);
  expect(after.draft.text).toBe("/pi /compact 保留测试");
  expect(sessionStorage.length).toBe(0);
  ui.root.remove();
});

it("preserves conflicting local text and requires the latest queue version before replacing it", async () => {
  let nextID = 0;
  const posts: Record<string, unknown>[] = [];
  let revision = 1, version = 0, text = "original", held = false;
  const result = () => snapshot(revision, { items: [{ ...editableRow, text, editing: held, body_version: version }] });
  const ui = view(async (_url, init) => {
    if (init?.method === "POST") {
      const req = JSON.parse(String(init.body));
      posts.push(req);
      if (req.action === "hold") { held = true; revision++; }
      if (req.action === "edit") { version++; revision++; held = false; text = req.text; }
    }
    return response(result());
  }, () => "client-" + ++nextID);
  ui.queue.show("ws", "chat");
  await vi.waitFor(() => expect(queueButton(ui.root, "编辑")).toBeTruthy());
  queueButton(ui.root, "编辑").click();
  await vi.waitFor(() => expect(queueButton(ui.root, "保存").disabled).toBe(false));
  const input = setEditorText(ui.root, "my text");
  text = "other editor's text"; version++; revision++; held = false;
  ui.queue.apply("ws", result());
  expect(queueButton(ui.root, "保存").disabled).toBe(true);
  expect(input.value).toBe("my text");
  expect(ui.root.querySelector('[role="status"]')?.textContent).toContain(text);
  queueButton(ui.root, "按最新版本继续编辑").click();
  await vi.waitFor(() => expect(queueButton(ui.root, "保存").disabled).toBe(false));
  queueButton(ui.root, "保存").click();
  await vi.waitFor(() => expect(ui.root.querySelector<HTMLElement>(".queue-editor")!.hidden).toBe(true));
  expect(posts.filter(req => req.action === "hold").map(req => req.body_version)).toEqual([0, 1]);
  expect(posts.find(req => req.action === "edit")).toMatchObject({ body_version: 1, text: "my text" });
  expect(text).toBe("my text");
  expect(ui.draft.text).toBe("/pi /compact 保留测试");
  ui.queue.reset();
});

it("recovers an editor whose row left the queue without overwriting the composer draft", async () => {
  let revision = 0;
  const ui = view(async (_url, init) => response(snapshot(++revision, { items: [{ ...editableRow, editing: init?.method === "POST" }] })));
  ui.queue.show("ws", "chat");
  await vi.waitFor(() => expect(queueButton(ui.root, "编辑")).toBeTruthy());
  queueButton(ui.root, "编辑").click();
  await vi.waitFor(() => expect(queueButton(ui.root, "保存").disabled).toBe(false));
  const input = setEditorText(ui.root, "unsaved");
  ui.queue.apply("ws", snapshot(++revision));
  expect(input.value).toBe("unsaved");
  queueButton(ui.root, "放回输入框").click();
  expect(ui.draft.text).toBe("/pi /compact 保留测试");
  expect(ui.root.textContent).toContain("已有草稿");
  ui.draft.text = "";
  queueButton(ui.root, "放回输入框").click();
  expect(ui.draft.text).toBe("/pi unsaved");
  expect(ui.root.hidden).toBe(true);
  ui.queue.reset();
});

it("moves only waiting cards relative to their visible neighbor and retries the same operation", async () => {
  let nextID = 0;
  const starting = snapshot(1, { items: [{ ...row, id: "running", state: "running" }, { ...row, id: "a" },
    { ...row, id: "old", state: "unknown" }, { ...row, id: "b" }] });
  const moved = snapshot(2, { items: [starting.items[0], starting.items[3], starting.items[2], starting.items[1]] });
  const posts: Record<string, unknown>[] = [];
  const ui = view(async (_url, init) => {
    if (init?.method !== "POST") return response(starting);
    posts.push(JSON.parse(String(init.body)));
    if (posts.length === 1) throw new TypeError("disconnected");
    return response(moved);
  }, () => "move-" + ++nextID);
  ui.queue.show("ws", "chat");
  await vi.waitFor(() => expect(queueButton(ui.root, "下移")).toBeTruthy());
  let cards = ui.root.querySelectorAll<HTMLElement>(".turn-input-queued");
  expect(cards[0].querySelector("button")).toBeNull();
  expect(cards[2].querySelectorAll("button")).toHaveLength(1);
  const down = queueButton(cards[1], "下移");
  down.click();
  await vi.waitFor(() => expect(down.disabled).toBe(false));
  down.click();
  await vi.waitFor(() => expect(posts).toHaveLength(2));
  expect(posts[0]).toMatchObject({ action: "move", id: "a", anchor_id: "b", position: "after" });
  expect(posts[1]).toEqual(posts[0]);
  cards = ui.root.querySelectorAll<HTMLElement>(".turn-input-queued");
  expect(queueButton(cards[3], "上移")).toBeTruthy();
  ui.queue.reset();
});

it("reopens an unsaved edit only in its own chat and cancels without sending it", async () => {
  let nextID = 0, rev = 1;
  const requests: Record<string, unknown>[] = [];
  const holders = new Set<string>();
  const transport: typeof fetch = async (url, init) => {
    if (init?.method === "POST") {
      const request = JSON.parse(String(init.body));
      requests.push(request);
      if (request.action === "hold") holders.add(request.edit_id);
      if (request.action === "release") holders.delete(request.edit_id);
      rev++;
    } else if (String(url).includes("session_id=another")) {
      return response({ ...snapshot(0), session_id: "another" });
    }
    return response(snapshot(rev, { items: [{ ...editableRow, editing: holders.size > 0 }] }));
  };
  const ids = () => "editor-" + ++nextID;
  const first = view(transport, ids);
  first.queue.show("ws", "chat");
  await vi.waitFor(() => expect(queueButton(first.root, "编辑")).toBeTruthy());
  queueButton(first.root, "编辑").click();
  await vi.waitFor(() => expect(queueButton(first.root, "保存").disabled).toBe(false));
  setEditorText(first.root, "draft in this chat");
  first.queue.show("ws", "another");
  expect(first.root.querySelector<HTMLElement>(".queue-editor")!.hidden).toBe(true);
  const restored = view(transport, ids);
  restored.queue.show("ws", "chat");
  await vi.waitFor(() => expect(queueButton(restored.root, "保存").disabled).toBe(false));
  expect(restored.root.querySelector("textarea")!.value).toBe("draft in this chat");
  const opening = requests.filter(req => req.action === "hold");
  expect(opening).toHaveLength(2);
  expect(opening[0].edit_id).not.toBe(opening[1].edit_id);
  restored.root.querySelector("textarea")!.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
  await vi.waitFor(() => expect(holders.size).toBe(0));
  expect(requests.some(req => req.action === "edit")).toBe(false);
  expect(restored.root.querySelector<HTMLElement>(".queue-editor")!.hidden).toBe(true);
  expect(sessionStorage.length).toBe(0);
  first.queue.reset();
  restored.queue.reset();
});

const testMedia: SentAttachments = { images: [{ mime_type: "image/png", data_base64: "aW1hZ2U=" }],
  files: [{ name: "review.txt", data_base64: "Y29udGVudA==" }] };
const mediaCard = { ...editableRow, text: "", attachments: [
  {kind:"image" as const, mime_type:"image/png", size:5}, {kind:"file" as const, name:"review.txt", size:7},
] };

function mediaView(transport: typeof fetch, cache = new QueueUploadCache()) {
  const root = document.createElement("div"), notify = vi.fn();
  const state = { text:"", turn:"active", ids:["image","file"], payload:testMedia };
  const restoreMedia = vi.fn((payload: Partial<SentAttachments>) => {
    state.payload = { images: payload.images || [], files: payload.files || [] };
    state.ids = [...state.payload.images,...state.payload.files].map((_,i) => "restored-" + i);
  });
  let next = 0;
  const queue = new CommandQueue(root, new APIClient(transport), {
    turn: () => state.turn, draft: () => state.text, setDraft: text => { state.text = text; }, newID: () => ++next === 1 ? "op" : "key-" + next,
    notify, watch: () => {}, mediaIDs: () => [...state.ids], captureMedia: () => state.payload,
    removeMedia: ids => { state.ids = state.ids.filter(id => !ids.includes(id)); }, restoreMedia,
  }, cache);
  return { root, state, queue, notify, restoreMedia };
}

it("queues attachment-only input, clears only acknowledged files and keeps newer composer text", async () => {
  let finish!: (response: Response) => void;
  const posts: Record<string, unknown>[] = [];
  const ui = mediaView(async (_url, init) => {
    if (init?.method !== "POST") return response(snapshot(0));
    posts.push(JSON.parse(String(init.body)));
    return new Promise(resolve => { finish=resolve; });
  });
  ui.queue.show("ws","chat");
  const submit = ui.queue.enqueue("pi", "", { agent_model: "p/m" });
  await vi.waitFor(() => expect(posts).toHaveLength(1));
  expect(posts[0]).toMatchObject({ kind:"message", id:"op", text:"", agent_model:"p/m", ...testMedia });
  expect(sessionStorage.getItem("bqagent.web-command.ws")).not.toContain("aW1hZ2U=");
  ui.state.ids.push("new-file"); ui.state.text="new question";
  ui.queue.apply("ws", snapshot(2,{items:[mediaCard]}));
  expect(ui.state.ids).toEqual(["new-file"]);
  const links = Array.from(ui.root.querySelectorAll("a"));
  expect(links.map(link => link.textContent)).toEqual(["图片 1","review.txt"]);
  expect(links[0].href).toContain("workspace_id=ws&session_id=chat&id=op&view=attachment&index=0");
  expect(ui.root.textContent).not.toContain("aW1hZ2U=");
  finish(response(snapshot(2,{items:[mediaCard],receipt:mediaCard})));
  await submit;
  expect(ui.state.text).toBe("new question");
  ui.queue.reset();
});

it("recovers original queued bytes after a lost response and restores them if the parent already ended", async () => {
  const cache = new QueueUploadCache();
  const posts: Record<string,unknown>[] = [];
  const before = mediaView(async (_url, init) => {
    if (init?.method === "POST") { posts.push(JSON.parse(String(init.body))); throw new TypeError("lost"); }
    return response(snapshot(0));
  },cache);
  before.queue.show("ws","chat");
  before.state.text="review";
  await before.queue.enqueue("pi","review",{agent_model:"p/m"});
  const after = mediaView(async (_url, init) => {
    if (init?.method !== "POST") return response(snapshot(0));
    posts.push(JSON.parse(String(init.body)));
    return new Response('{"error":"parent ended"}',{status:410});
  },cache);
  after.state.ids=[]; after.state.turn=""; after.state.text="review";
  after.queue.show("ws","chat");
  expect(await after.queue.reconcileDraft()).toBe(true);
  expect(posts[1]).toEqual(posts[0]);
  expect(after.restoreMedia).toHaveBeenCalledWith(testMedia);
  expect(after.state.payload).toEqual(testMedia);
  expect(sessionStorage.getItem("bqagent.web-command.ws")).toBeNull();
  after.queue.reset();
});

it("never retries an unresolved upload as a text-only message when local bytes are unavailable", async () => {
  sessionStorage.setItem("bqagent.web-command.ws", JSON.stringify([{ id:"old", agent:"pi", parent:"parent", session:"chat",
    draft:"message", text:"message", kind:"message", mediaIDs:["from-old-tab"] }]));
  const request = vi.fn<typeof fetch>(async () => response(snapshot(0)));
  const ui = mediaView(request);
  ui.state.ids=[]; ui.state.text="message"; ui.state.turn="";
  ui.queue.show("ws","chat");
  expect(await ui.queue.reconcileDraft()).toBe(false);
  expect(request.mock.calls.every(([,init]) => init?.method !== "POST")).toBe(true);
  expect(ui.notify.mock.calls.flat().join("")).toContain("重新添加附件");
  ui.queue.reset();
});

it("recovers a withdrawn bundle on demand and acknowledges only after restoring it in the right chat", async () => {
  const returned = { ...mediaCard, state:"withdrawn" };
  let current = snapshot(1,{items:[mediaCard]});
  const writes: Record<string,unknown>[] = [];
  const reads: string[] = [];
  const ui = mediaView(async (url,init) => {
    if (init?.method==="DELETE") {
      expect(String(url)).toContain("body_version=0");
      current=snapshot(2,{items:[returned],receipt:returned});
    } else if (String(url).includes("view=attachments")) {
      reads.push(String(url));
      return new Response(JSON.stringify(testMedia));
    } else if (init?.method==="POST") {
      writes.push(JSON.parse(String(init.body)));
      current=snapshot(3,{receipt:{...returned,attachments:[]}});
    }
    return response(current);
  });
  ui.state.ids=[]; ui.state.text="another draft"; ui.queue.show("ws","chat");
  await vi.waitFor(() => expect(queueButton(ui.root,"撤回")).toBeTruthy());
  queueButton(ui.root,"撤回").click();
  await vi.waitFor(() => expect(queueButton(ui.root,"放回输入框")).toBeTruthy());
  expect(reads).toHaveLength(0);
  expect(ui.state.text).toBe("another draft");
  ui.state.text="";
  queueButton(ui.root,"放回输入框").click();
  await vi.waitFor(() => expect(writes[0]).toMatchObject({action:"release_attachments", id:"op",session_id:"chat"}));
  expect(ui.restoreMedia).toHaveBeenCalledWith(testMedia);
  expect(ui.state.text).toBe("/pi ");
  expect(ui.root.hidden).toBe(true);
  ui.queue.reset();
});

it("edits queued attachments by index and adds a local image without changing the composer", async () => {
  const posts: Record<string,unknown>[] = [];
  let current = snapshot(1,{items:[mediaCard]});
  const ui = mediaView(async (_url,init) => {
    if (init?.method==="POST") {
      const req=JSON.parse(String(init.body)); posts.push(req);
      if(req.action==="hold") current=snapshot(2,{items:[{...mediaCard,editing:true}]});
      if(req.action==="edit") current=snapshot(3,{items:[{...mediaCard,body_version:1}]});
    }
    return response(current);
  });
  ui.queue.show("ws","chat");
  await vi.waitFor(() => expect(queueButton(ui.root,"编辑")).toBeTruthy());
  queueButton(ui.root,"编辑").click();
  await vi.waitFor(() => expect(queueButton(ui.root,"保存").disabled).toBe(false));
  queueButton(ui.root,"移除附件").click();
  const picker=ui.root.querySelector<HTMLInputElement>('.queue-edit-media input[type=file]')!;
  Object.defineProperty(picker,"files",{value:[new File(["new image"],"new.png",{type:"image/png"})],configurable:true});
  picker.dispatchEvent(new Event("change"));
  await vi.waitFor(() => expect(ui.root.querySelector(".queue-edit-media")?.textContent).toContain("new.png"));
  queueButton(ui.root,"保存").click();
  await vi.waitFor(() => expect(posts.find(req=>req.action==="edit")).toMatchObject({
    keep_attachments:[1], images:[{mime_type:"image/png",data_base64:btoa("new image")}], body_version:0, text:"",
  }));
  expect(ui.state.ids).toEqual(["image","file"]);
  ui.queue.reset();
});

it("confirms a completed edit on reload even when its added files are no longer in browser storage", async () => {
  sessionStorage.setItem('bqagent.web-command.edit.["ws","chat"]',JSON.stringify({
    id:"op", agent:"pi", token:"edit-token", version:0, text:"saved wording", operation:"save-once",
    media:{originals:[],keep:[],added:[{kind:"file",name:"local.md",size:3}],key:"missing-media"},
  }));
  const requests: { url:string; method?:string }[] = [];
  const ui = view(async (url,init) => {
    requests.push({url:String(url),method:init?.method});
    return response(snapshot(8,{ operation_committed:String(url).includes("operation_id=save-once") }));
  });
  ui.queue.show("ws","chat");
  await vi.waitFor(() => expect(sessionStorage.length).toBe(0));
  expect(requests.some(req=>req.url.includes("operation_id=save-once"))).toBe(true);
  expect(requests.every(req=>!req.method)).toBe(true);
  expect(ui.root.hidden).toBe(true);
  ui.queue.reset();
});

it("does not restore attachment bytes into a different workspace while a download is in flight", async () => {
  let finish!: (r:Response)=>void;
  const released:Record<string,unknown>[]=[];
  const ui = mediaView(async (url,init) => {
    if (String(url).includes("view=attachments")) return new Promise(resolve=>{finish=resolve;});
    if (init?.method==="POST") released.push(JSON.parse(String(init.body)));
    return response(snapshot(2,{ items:[{...mediaCard,state:"withdrawn"}] }));
  });
  ui.state.ids = []; ui.queue.show("ws","chat");
  await vi.waitFor(()=>expect(queueButton(ui.root,"放回输入框")).toBeTruthy());
  const button = queueButton(ui.root,"放回输入框");
  button.click();
  ui.queue.show("other","chat"); ui.state.text="keep";
  finish(new Response(JSON.stringify(testMedia)));
  await vi.waitFor(()=>expect(button.disabled).toBe(false));
  expect(ui.restoreMedia).not.toHaveBeenCalled();
  expect(released).toHaveLength(0);
  expect(ui.state.text).toBe("keep");
  ui.queue.reset();
});

it("sends the chosen queue card by ID and revision without consuming the composer or following a stale chat", async () => {
  const requests: Record<string, unknown>[] = [];
  let finish!: (result: Response) => void;
  const ui = view(async (_url, init) => {
    if (init?.method !== "POST") return response(snapshot(0));
    requests.push(JSON.parse(String(init.body)));
    return new Promise(resolve => { finish = resolve; });
  }, () => "press");
  ui.turn.id = "";
  ui.queue.show("ws","chat");
  const card = { ...row, id: "chosen", kind: "message" as const, text: "saved", body_version: 3, editing: true };
  ui.queue.apply("ws",snapshot(4,{paused:true, items:[row,card]}));
  const send = ui.root.querySelectorAll<HTMLButtonElement>("button[data-send-card]")[1];
  expect(send.disabled).toBe(false);
  send.click(); send.click();
  expect(requests).toEqual([{action:"send_now", id:"chosen", body_version:3, expected_turn_id:"", operation_id:"press", workspace_id:"ws",session_id:"chat"}]);
  ui.draft.text = "newer composer text";
  ui.queue.show("other","another");
  finish(response(snapshot(5,{turn_id:"old-child",items:[{...card,state:"running"}]})));
  await vi.waitFor(() => expect(send.disabled).toBe(false));
  expect(ui.draft.text).toBe("newer composer text");
  expect(ui.watch).not.toHaveBeenCalled();
  ui.queue.reset();
});

it("blocks command cards while working and all sends at a prompt; an uncertain handoff has no resend button", () => {
  const ui=view(async()=>response(snapshot(0)));
  ui.queue.show("ws","chat");
  const message={...row,id:"text",text:"message",kind:"message" as const};
  ui.queue.apply("ws",snapshot(1,{items:[row,message,{...message,id:"returned",state:"returned",turn_id:"first"}, {...message,id:"unknown",state:"unknown"}]}));
  const buttons=[...ui.root.querySelectorAll<HTMLButtonElement>("button[data-send-card]")];
  expect(buttons.map(b=>b.textContent)).toEqual(["立即发送","立即发送","重新发送"]);
  ui.queue.setSendActivity(true,false);
  expect(buttons.map(b=>b.disabled)).toEqual([true,false,false]);
  ui.queue.setSendActivity(true,true);
  expect(buttons.every(b=>b.disabled)).toBe(true);
  ui.queue.setSendActivity(false,false);
  expect(buttons.every(b=>!b.disabled)).toBe(true);
  ui.queue.reset();
});

it("retries a lost send-now reply after reload with the original operation and expected handoff", async () => {
  const requests: Record<string,unknown>[]=[];
  const card={...row,state:"returned",kind:"message" as const,turn_id:"prior"};
  const held=snapshot(1,{items:[card]});
  const before=view(async (_url,init)=>{
    if (init?.method==="POST"){requests.push(JSON.parse(String(init.body)));throw new Error("lost");}
    return response(held);
  },()=>"first-press");
  before.queue.show("ws","chat");before.queue.apply("ws",held);
  queueButton(before.root,"重新发送").click();
  await vi.waitFor(()=>expect(before.notify).toHaveBeenCalled());
  before.queue.reset();
  const after=view(async (_url,init)=>{
    if (init?.method==="POST"){requests.push(JSON.parse(String(init.body)));return response(snapshot(2,{receipt:{...card,state:"done"}}));}
    return response(held);
  },()=>"unused");
  after.queue.show("ws","chat");after.queue.apply("ws",held);
  queueButton(after.root,"重新发送").click();
  await vi.waitFor(()=>expect(requests).toHaveLength(2));
  expect(requests[1]).toEqual(requests[0]);
  expect(requests[1]).toMatchObject({operation_id:"first-press",expected_turn_id:"prior"});
  after.queue.reset();
});
