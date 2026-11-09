import { expect, it, vi } from "vitest";
import { APIClient } from "./api-client";
import { normalizeHistoryMessages } from "./chat-rendering";
import { requestRewind, rewindButton } from "./rewind";

it("addresses repeated user prompts by native-backed target and retries the same operation", async () => {
  const actions = ["first", "second"].map(target => ({ target, expected_context: 3 }));
  const rows = normalizeHistoryMessages(actions.map(rewind => ({ role: "user", content: "same", rewind })));
  expect(rows.map(row => row.rewind)).toEqual(actions);
  const calls: { target: string; operation: string; replay: boolean; session_id: string }[] = [];
  const client = new APIClient(vi.fn<typeof fetch>(async (_url, init) => {
    calls.push(JSON.parse(String(init?.body)));
    if (calls.length === 1) throw new Error("lost");
    return new Response(JSON.stringify({ text: "same", files: [{ name: "saved", data_base64: "YQ==" }], context_number: 4 }));
  }));
  const submit = () => requestRewind(client, "workspace", "chat", rows[1].rewind!, false);
  await expect(submit()).rejects.toThrow("lost");
  expect((await submit()).files?.[0].data_base64).toBe("YQ==");
  const receipt = { ...actions[1], operation: calls[0].operation };
  await requestRewind(client, "workspace", "chat", receipt, true);
  expect(calls.map(x => x.operation)).toEqual(Array(3).fill("rewind-3-second"));
  expect(calls.map(x => x.replay)).toEqual([false, false, true]);
  expect(calls.every(x => x.session_id === "chat" && x.target === "second")).toBe(true);
  const click = vi.fn();
  const button = rewindButton(receipt, true, click);
  expect(button.textContent).toBe("取回回退草稿");
  button.click();
  expect(click).toHaveBeenCalledOnce();
});

it("restores the Claude responder for a recovered fork draft", async () => {
  const action = { agent: "claude" as const, target: "prior", expected_context: 1, operation: "confirmed" };
  const [row] = normalizeHistoryMessages([{ role: "user", content: "again", rewind: action }]);
  expect(row.rewind?.agent).toBe("claude");
  expect(rewindButton(action, true, () => {}).textContent).toBe("取回回退草稿");
  const client = new APIClient(vi.fn<typeof fetch>(async (_url, init) => {
    expect(JSON.parse(String(init?.body))).toMatchObject({ target: "prior", replay: true, operation: "confirmed" });
    return new Response(JSON.stringify({ agent: "claude", text: "again", context_number: 2 }));
  }));
  const draft = await requestRewind(client, "ws", "chat", action, true);
  expect(draft.agent).toBe("claude");
});

it("offers rewind only on recorded roots and keeps earlier tool output out of the restored prompt", () => {
  const action = { target: "root", expected_context: 0 };
  const rows = normalizeHistoryMessages([
    { role:"user", content:"review", rewind:action },
    { role:"assistant", sender:"codex", content:"read file" },
    { role:"user", content:"steer" },
  ]);
  expect(rows.map(row => row.rewind)).toEqual([action, undefined, undefined]);
  expect(rewindButton(action, false, () => {}).textContent).toBe("回退到这里");
  expect(rewindButton({ ...action, operation:"prior" }, false, () => {}).textContent).toBe("重试回退");
});
