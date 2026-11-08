import { expect, it, vi } from "vitest";
import { APIClient } from "./api-client";
import { normalizeHistoryMessages } from "./chat-rendering";
import { ForkRequests, forkSourceMarker, replyAnchor } from "./fork";

it("retries a lost branch with one operation and opens a fresh branch on the next explicit click", async () => {
  const ops: string[] = [];
  const client = new APIClient(vi.fn<typeof fetch>(async (_url,init) => {
    const body = JSON.parse(String(init?.body)); ops.push(body.operation);
    expect(body).toMatchObject({workspace_id:"ws",session_id:"parent",target:"second",expected_context:2});
    if (ops.length === 1) throw new Error("lost");
    return new Response(JSON.stringify({session_id:"child-"+body.operation}));
  }));
  let n = 0;
  const requests = () => new ForkRequests(client, () => "op-"+ ++n);
  const action = {target:"second",expected_context:2};
  await expect(requests().create("ws","parent",action)).rejects.toThrow("lost");
  const restored = requests();
  const retry = restored.create("ws","parent",action);
  expect(restored.create("ws","parent",action)).toBe(retry);
  expect(await retry).toBe("child-op-1");
  expect(await restored.create("ws","parent",action)).toBe("child-op-2");
  expect(ops).toEqual(["op-1","op-1","op-2"]);
});

it("places the branch and source link at the selected assistant segment even with repeated text", () => {
  const action = { target:"segment-2", expected_context:1 };
  const rows = normalizeHistoryMessages([
    {role:"assistant",sender:"codex",content:"same",anchor:"segment-1",parts:[{kind:"text",id:"a",text:"same"}]},
    {role:"assistant",sender:"codex",content:"same",anchor:"segment-2",fork:action,parts:[{kind:"text",id:"a",text:"tool commentary"},{kind:"text",id:"b",text:"same"}]},
  ]);
  expect(rows.map(r=>r.anchor)).toEqual(["segment-1",undefined,"segment-2"]);
  expect(rows.filter(r=>r.fork)).toEqual([rows[2]]);
  const thread = document.createElement("div");
  for (const row of rows) {
    const msg = document.createElement("div"); msg.textContent=row.content;
    if (row.anchor) msg.dataset.replyAnchor=row.anchor;
    thread.append(msg);
  }
  const origin={session:"source",epoch:1,target:action.target}, open=vi.fn();
  const marker = forkSourceMarker(origin,open);
  replyAnchor(thread,origin.target)!.after(marker);
  expect(marker.previousElementSibling).toBe(thread.children[2]);
  marker.querySelector("button")!.click(); expect(open).toHaveBeenCalledWith(origin);
});
