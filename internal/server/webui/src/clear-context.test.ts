import { expect, it, vi } from "vitest";
import { APIClient } from "./api-client";
import { clearDivider, clearRefusal, ClearContextRequests, parseClear } from "./clear-context";
import { normalizeHistoryMessages } from "./chat-rendering";

it("recognizes only the host clear command and rejects busy, group and attachment requests", () => {
  expect(parseClear(" /Pi /CLEAR ")).toEqual({ agent: "pi", args: "" });
  expect(parseClear("/clear extra")?.args).toBe("extra");
  for (const text of ["/clearer", "/pi discuss /clear", "explain /clear", "/unknown /clear"]) expect(parseClear(text)).toBeUndefined();
  expect(clearRefusal("", "run", "default", false, false)).toBeUndefined();
  expect(clearRefusal("", "run", "default", false, true)).toContain("停止");
  expect(clearRefusal("", "run", "default", true, false)).toContain("附件");
  expect(clearRefusal("extra", "run", "default", false, false)).toContain("参数");
  expect(clearRefusal("", "run", "group", false, false)).toContain("单聊");
  expect(clearRefusal("", "ask", "default", false, false)).toContain("Run");
});

it("retries a lost clear response across reload using one operation per chat and target",async () => {
  const posts: {turn_id: string; agent: string; session_id: string}[] = [];
  let n = 0;
  const transport = vi.fn<typeof fetch>(async (_url, init) => {
    posts.push(JSON.parse(String(init?.body)));
    if (posts.length === 1) throw new Error("lost");
    return new Response("{}", {status:200});
  });
  const requests = () => new ClearContextRequests(new APIClient(transport), () => "op-" + ++n);
  await expect(requests().clear("ws", "chat", "/pi /clear", "pi")).rejects.toThrow("lost");
  const finishOther = await requests().clear("ws","another","/clear","pi"); finishOther();
  const finish = await requests().clear("ws","chat","/pi /clear","pi"); finish();
  const finishNew = await requests().clear("ws","chat","/clear","pi"); finishNew();
  expect(posts.map(row => row.turn_id)).toEqual(["op-1","op-2","op-1","op-3"]);
});

it("renders a context divider between old and new assistant messages", () => {
  const rows = normalizeHistoryMessages([{role:"assistant",content:"old"}, {role:"assistant",content:"上下文已清空",kind:"context_clear"}, {role:"assistant",content:"new"}]);
  expect(rows.map(row => row.content)).toEqual(["old","上下文已清空","new"]);
  const divider = clearDivider(rows[1].content);
  expect(divider.getAttribute("role")).toBe("separator");
  expect(divider.getAttribute("aria-label")).toBe("上下文已清空");
});
