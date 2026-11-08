import { expect, it } from "vitest";
import { ExternalTimelineView } from "./external-timeline";
import { renderHistoricalPrompt } from "./acp-permission";
import { normalizeHistoryMessages } from "./chat-rendering";

it("keeps streamed external text, concurrent calls, and late revisions in their first positions", () => {
  const thread = document.createElement("main");
  const view = new ExternalTimelineView(sender => {
    const row = document.createElement("div"), stack = document.createElement("div"), bubble = document.createElement("div");
    row.className = "msg"; row.dataset.sender = sender;
    row.append(stack); stack.append(bubble); thread.append(row);
    return bubble;
  }, (bubble, text) => { bubble.textContent = text; });
  const tool = {id:"a", source:"external", name:"read", status:"running"};
  view.text({id:"intro",agent:"pi",text:"inspect",append:true});
  view.tool("tool_start",tool,"pi");
  view.tool("tool_start",{...tool,id:"b",name:"second"},"pi");
  view.text({id:"after",agent:"pi",text:"answer",append:true});
  const card = thread.querySelector(".tool-card");
  view.text({id:"intro",agent:"pi",text:"inspect first"});
  view.tool("tool_result",{...tool,status:"succeeded",preview:"late output"},"pi");
  expect(view.finish()?.text).toBe("answer");
  expect(thread.children.length).toBe(3);
  expect(thread.children[0].textContent).toBe("inspect first");
  expect(thread.children[1].querySelectorAll(".tool-card")).toHaveLength(2);
  expect(thread.children[1].querySelector(".tool-card")).toBe(card);
  expect(card?.textContent).toContain("late output");
  expect(thread.children[2].textContent).toBe("answer");
});

it("restores ordered native history without merging commentary into tools or repeating the aggregate reply", () => {
  const rows = normalizeHistoryMessages([{role:"assistant",sender:"pi",content:"leadanswer",
    parts:[{kind:"text",id:"1",text:"lead"},{kind:"tool",id:"a"},{kind:"tool",id:"b"},{kind:"text",id:"2",text:"answer"}],
    tools:[{id:"a",name:"read",status:"succeeded",result:"done"},{id:"b",name:"edit",status:"failed"}]}]);
  expect(rows.map(r=>[r.content, r.tools?.map(t=>t.id)])).toEqual([["lead",[]],["",["a","b"]],["answer",[]]]);
  expect(rows.every(r=>r.sender==="pi")).toBe(true);
});

it("restores a historical decision in order without reopening its controls", () => {
  const rows = normalizeHistoryMessages([{role:"assistant",sender:"pi",content:"answer",
    parts:[{kind:"text",id:"a",text:"comment"},{kind:"prompt",id:"p",prompt:{method:"confirm",title:"Change file?",state:"selected",choice:"拒绝"}},{kind:"text",id:"b",text:"answer"}]}]);
  expect(rows.map(r=>r.prompt?.title || r.content)).toEqual(["comment","Change file?","answer"]);
  const card = document.createElement("div");
  renderHistoricalPrompt(card,"pi",rows[1].prompt!);
  expect(card.textContent).toContain("已选择：拒绝");
  expect(card.querySelector("button,input,textarea")).toBeNull();
});
