import { expect, it } from "vitest";
import { normalizeHistoryMessages, historyAssistantView } from "./chat-rendering";
import { TurnFileEdits } from "./file-edits";
import { updateToolTimeline, type ToolCards } from "./tool-timeline";
import type { EditFile, ToolEventPayload } from "./types";

function file(path:string, old_path?:string): EditFile {
  return { path, old_path, kind:old_path?"renamed":"edited",added:1,removed:1,lines:[
    {kind:"del",old:20,text:"bad"}, {kind:"add",new:21,text:"  <script>literal</script>"},
    {kind:"gap"}, {kind:"context",old:30,new:31,text:"unchanged"},
  ]};
}
function open(details:HTMLDetailsElement) {
  details.open=true; details.dispatchEvent(new Event("toggle"));
}

it("renders a landed file edit on the original live card and restores its lines from history", () => {
  const stack=document.createElement("div"), bubble=document.createElement("div");
  stack.append(bubble);
  const cards:ToolCards=Object.create(null);
  const payload:ToolEventPayload={turn_id:"turn",id:"edit",source:"external",name:"Edit",status:"running",edits:{files:[file("app.go")]}};
  updateToolTimeline(bubble,cards,"tool_start",payload);
  expect(stack.querySelector(".edit-file")).toBeNull();
  updateToolTimeline(bubble,cards,"tool_result",{...payload,status:"failed"});
  expect(stack.querySelector(".edit-file")).toBeNull();
  const done={...payload,id:"applied",status:"succeeded"};
  updateToolTimeline(bubble,cards,"tool_result",done);
  const details=stack.querySelector<HTMLDetailsElement>(".edit-file")!;
  expect(details.querySelector(".edit-lines")).toBeNull();
  open(details);
  expect(details.querySelector(".edit-line.add .edit-text")!.textContent).toBe("  <script>literal</script>");
  expect(details.querySelector("script")).toBeNull();
  expect(details.querySelector(".edit-line.del .edit-number")!.textContent).toBe("20");
  const rows=normalizeHistoryMessages([{role:"assistant",content:"done",sender:"claude",external_turn:"turn",
    parts:[{kind:"tool",id:"applied"},{kind:"text",id:"reply",text:"done"}],tools:[{...done,name:"Edit"}]}]);
  expect(rows.map(row=>row.external_turn)).toEqual(["turn","turn"]);
  const [restored]=historyAssistantView(rows[0]).tools;
  expect(restored.edits).toEqual(done.edits);expect(restored.turn_id).toBe("turn");
  const next=document.createElement("div"); stack.append(next);
  updateToolTimeline(next,{}, "tool_result",restored,[{cards}]);
  expect(stack.querySelectorAll(".edit-file")).toHaveLength(1);
  expect(stack.querySelector<HTMLDetailsElement>(".edit-file")!.open).toBe(true);
});

it("rolls up edits in call order across renames, final replays, failures and reused call IDs", () => {
  const rollup=new TurnFileEdits(), stack=document.createElement("div");
  const call=(id:string,status:string,change?:EditFile,turn_id="one"):ToolEventPayload=>({
    id,turn_id,status,source:"external",edits:change && {files:[change]},
  });
  rollup.observe(call("1","running"));rollup.observe(call("2","running"));rollup.observe(call("bad","running"));
  rollup.observe(call("2","succeeded",file("moved.go","old.go")));
  rollup.observe(call("bad","failed",file("wrong")));
  rollup.observe(call("1","succeeded",file("old.go")));
  rollup.observe(call("1","succeeded",file("old.go"))); // terminal replay
  rollup.observe(call("1","succeeded",file("unrelated"),"two"));
  rollup.render("one",stack);rollup.render("one",stack);
  const summary=stack.querySelector<HTMLDetailsElement>(".turn-file-edits")!;
  expect(stack.children).toHaveLength(1);
  expect(summary.textContent).toContain("本轮修改 1 个文件+2−2");
  open(summary);
  const row=summary.querySelector<HTMLDetailsElement>(".edit-rollup-file")!;
  expect(row.querySelector(".edit-path")!.textContent).toBe("moved.go");
  open(row);
  expect(row.querySelectorAll(".edit-file")).toHaveLength(2);
  expect(row.textContent).not.toMatch(/unrelated|wrong/);
});
