import { expect, it, vi } from "vitest";
import { hasBlockingPrompt, renderHistoricalPrompt, showAgentPrompt } from "./acp-permission";
import type { ACPPermissionPayload, PromptQuestion } from "./types";

const rows: PromptQuestion[] = [
  {id:"q1", title:"Pick a mode", header:"Mode", options:[{id:"a",label:"Brief",description:"<img src=x>"}]},
  {id:"q2", title:"Include which changes?", multi_select:true, options:[{id:"b",label:"Tests"},{id:"c",label:"Docs"}]},
  {id:"q3", title:"Anything else?"},
];
const payload: ACPPermissionPayload = {request_id:"ask",session_id:"chat",external_session_id:"native",agent:"claude",tool_call:{},
  dialog:{method:"question",title:"Claude 提问",header:"子 Agent：worker",questions:rows},
  options:[{option_id:"submit",kind:"",name:"提交回答"},{option_id:"cancel",kind:"reject_once",name:"停止本轮"}]};

it("submits a revisable Claude question group once and renders only confirmed choices", async () => {
  const card = document.createElement("div"), pending: Record<string,HTMLElement> = {};
  let resolve!: () => void;
  const post = vi.fn(() => new Promise<void>(done => {resolve = done;}));
  document.body.append(card);
  showAgentPrompt(payload,pending,() => card,post);
  showAgentPrompt(payload,pending,() => { throw new Error("duplicate card"); },post);
  expect(card.querySelectorAll("fieldset")).toHaveLength(3);
  expect(card.querySelector("img")).toBeNull();
  const [first,second,last] = Array.from(card.querySelectorAll("fieldset"));
  const submit = card.querySelector("button")!;
  submit.click();
  expect(post).not.toHaveBeenCalled();
  expect(card.textContent).toContain("请回答「Pick a mode」");
  const radio = first.querySelector("input")!, text = first.querySelector("textarea")!;
  radio.click();
  text.value = "draft";
  text.dispatchEvent(new Event("input"));
  expect(radio.checked).toBe(false);
  radio.click();
  expect(text.value).toBe("");
  second.querySelectorAll("input").forEach(input => input.click());
  second.querySelector("textarea")!.value = " extra ";
  last.querySelector("textarea")!.value = " private note ";
  submit.click();
  expect(post).toHaveBeenCalledOnce();
  expect(post).toHaveBeenCalledWith("submit",undefined,[
    {question_id:"q1",option_ids:["a"]},{question_id:"q2",option_ids:["b","c"],other:"extra"},
    {question_id:"q3",option_ids:[],other:"private note"},
  ]);
  expect(hasBlockingPrompt(pending)).toBe(true);
  expect(radio.disabled).toBe(true);
  // A different tab's selection can win before our HTTP reply arrives.
  const confirmed = structuredClone(rows);
  confirmed[1].options![1].selected = true;
  showAgentPrompt({...payload,selected:"submit",dialog:{...payload.dialog!,questions:confirmed}},pending,() => card,post);
  resolve();
  await Promise.resolve();
  expect(card.querySelector("button:not(:disabled),input,textarea")).toBeNull();
  expect(card.textContent).toContain("已选：Docs");
  expect(card.textContent).not.toMatch(/已选：Brief|已选：Tests|private note|等待确认/);
  expect(hasBlockingPrompt(pending)).toBe(false);
  renderHistoricalPrompt(card,"claude",{title:"Claude 提问",method:"question",state:"selected",questions:confirmed});
  expect(card.querySelectorAll(".prompt-question")).toHaveLength(3);
  expect(card.querySelector("button,input,textarea,img")).toBeNull();
  expect(card.textContent).toContain("已选：Docs");
  card.remove();
});

it("withdraws a Claude question group without submitting its drafts and retries a rejected answer", async () => {
  const pending: Record<string,HTMLElement> = {}, card = document.createElement("div");
  const post = vi.fn().mockRejectedValueOnce(new Error("表单已更新")).mockResolvedValue(undefined);
  showAgentPrompt(payload,pending,() => card,post);
  const [submit,stop] = Array.from(card.querySelectorAll("button"));
  card.querySelectorAll("textarea").forEach(el => {el.value = "draft";});
  submit.click();
  await Promise.resolve();
  expect(card.textContent).toContain("表单已更新");
  expect(submit.disabled).toBe(false);
  expect(card.querySelector("textarea")?.value).toBe("draft");
  stop.click();
  expect(post).toHaveBeenLastCalledWith("cancel",undefined);
  showAgentPrompt({...payload,expired:true},pending,() => card,post);
  await Promise.resolve();
  expect(card.classList.contains("expired")).toBe(true);
  expect(card.querySelector("textarea,input")).toBeNull();
  expect(card.textContent).not.toMatch(/draft|已选：|等待确认/);
});

it("keeps the winning Claude group resolution when another tab already submitted", async () => {
  const pending: Record<string,HTMLElement> = {}, card = document.createElement("div");
  const post = vi.fn().mockRejectedValue(Object.assign(new Error("handled elsewhere"),{status:410}));
  showAgentPrompt(payload,pending,() => card,post);
  card.querySelectorAll("textarea").forEach(el => {el.value = "local draft";});
  card.querySelector("button")!.click();
  await Promise.resolve();
  expect(card.textContent).toContain("等待 Agent 确认处理结果");
  expect(card.querySelector("textarea")).toBeNull();
  const confirmed = structuredClone(rows);
  confirmed[0].options![0].selected = true;
  showAgentPrompt({...payload,selected:"submit",dialog:{...payload.dialog!,questions:confirmed}},pending,() => card,post);
  expect(card.textContent).toContain("已选：Brief");
  expect(card.textContent).not.toMatch(/local draft|等待 Agent|失效/);
  expect(hasBlockingPrompt(pending)).toBe(false);
});
