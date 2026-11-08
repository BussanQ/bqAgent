import { expect, it, vi } from "vitest";
import { clearChatViewData, followTurn, readChatDraft, rememberTurn, saveChatDraft, turnBookmark, TurnViewer } from "./turn-replay";

const frame = (id: number, event: string, value: string) => "id: " + id + "\nevent: " + event + "\ndata: " + JSON.stringify(value) + "\n\n";

it("reconnects a partial frame from the last delivered event without resending the prompt or tools", async () => {
  const start = vi.fn(async () => new Response(frame(1, "tool_start", "tool") + 'id: 2\nevent: message\ndata: "cut'));
  const subscribe = vi.fn(async () => new Response(frame(1, "tool_start", "tool") + frame(2, "message", "完整") + frame(3, "done", "finished")));
  const reconnect = vi.fn();
  const events = [];
  for await (const e of followTurn(start, subscribe, new AbortController().signal, reconnect, async () => {})) events.push(e);
  expect(events.map(e => [e.id, e.event])).toEqual([[1,"tool_start"], [2,"message"], [3,"done"]]);
  expect(start).toHaveBeenCalledTimes(1);
  expect(subscribe).toHaveBeenCalledWith(1);
  expect(reconnect).toHaveBeenCalledOnce();
});

it("recovers an initial lost response with GET at zero and stops on a definitive unknown turn", async () => {
  const start = vi.fn(async () => { throw new TypeError("network"); });
  const subscribe = vi.fn(async () => new Response(frame(1,"stopped","done")));
  const events = [];
  for await (const e of followTurn(start,subscribe,new AbortController().signal,()=>{},async()=>{})) events.push(e);
  expect(subscribe).toHaveBeenCalledWith(0);
  expect(start).toHaveBeenCalledOnce();
  expect(events[0].event).toBe("stopped");
  const missing = followTurn(undefined, async () => new Response("",{status:404}),new AbortController().signal,()=>{},async()=>{});
  await expect(missing.next()).rejects.toMatchObject({status:404});
});

it("keeps tab turn bookmarks independent for each workspace", () => {
  rememberTurn("left","a"); rememberTurn("right","b");
  rememberTurn("left","");
  expect(turnBookmark("left")).toBe("");
  expect(turnBookmark("right")).toBe("b");
  rememberTurn("right","");
});

it("detaches the visible reader before another chat takes the composer", async () => {
  const viewer = new TurnViewer();
  const released: string[] = [];
  const original = viewer.observe("old", async controller => {
    await new Promise<void>(resolve => controller.signal.addEventListener("abort", () => resolve(), { once: true }));
    expect(viewer.leaving("old")).toBe(true);
    released.push("old");
    return "must-not-follow";
  });
  await viewer.detach();
  expect(await original).toBeUndefined();
  expect(await viewer.observe("new", async () => { released.push("new"); return "successor"; })).toBe("successor");
  await viewer.observe("successor", async () => { released.push("successor"); });
  expect(released).toEqual(["old", "new", "successor"]);
});

it("restores drafts only in the same workspace and chat, and clears them on logout", () => {
  saveChatDraft("w","s","未发的补充");
  expect(readChatDraft("w","other")).toBe("");
  expect(readChatDraft("other","s")).toBe("");
  expect(readChatDraft("w","s")).toBe("未发的补充");
  rememberTurn("w","turn");
  clearChatViewData();
  expect(readChatDraft("w","s")).toBe("");
  expect(turnBookmark("w")).toBe("");
});
