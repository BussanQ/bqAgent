import { expect, it } from "vitest";
import { normalizeHistoryMessages } from "./chat-rendering";
import { renderQueueDelivery } from "./queue-delivery";
import type { ConversationMessage } from "./types";

it("preserves the unconfirmed user boundary, its files and status through chat history projection", () => {
  const message: ConversationMessage = { role:"user",kind:"queue_delivery",content:"<img onerror=bad()>",
    delivery:{id:"card-1",state:"unknown",attachments:[{kind:"file",name:"input.txt"},{kind:"image"}]} };
  const rows = normalizeHistoryMessages([{role:"assistant",content:"earlier"},message,{role:"assistant",content:"later"}]);
  expect(rows.map(row=>row.content)).toEqual(["earlier",message.content,"later"]);
  const bubble=document.createElement("div");
  const link=(id:string,index:number)=>"/attachments/" + id + "/" + index;
  renderQueueDelivery(bubble,rows[1],link);
  expect(bubble.dataset.queueDelivery).toBe("card-1");
  expect(bubble.textContent).toContain(message.content);
  expect(bubble.querySelectorAll("img")).toHaveLength(1);
  expect(bubble.querySelector("img")?.src).toContain("/card-1/1");
  expect(bubble.querySelector("a")?.href).toContain("/card-1/0");
  expect(bubble.textContent).toContain("执行结果未确认");
  renderQueueDelivery(bubble,{...rows[1],delivery:{...message.delivery!,state:"returned"}},link);
  expect(bubble.textContent).not.toContain("执行结果未确认");
  expect(bubble.textContent).toContain("Agent 已退回");
});
