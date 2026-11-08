import { copyText } from "./clipboard";
import type { EditFile, EditLine, FileEdits, ToolEventPayload } from "./types";

const labels = { added:"新增", deleted:"删除", renamed:"重命名", edited:"修改", written:"写入" };
const sign = { add:"+", del:"−", context:" ", content:" ", gap:"…" };

function span(className:string, text:string): HTMLSpanElement {
  const node = document.createElement("span");
  node.className = className;
  node.textContent = text;
  return node;
}

function counts(added:number, removed:number): HTMLElement {
  const node = span("edit-counts","");
  node.append(span("edit-add","+" + added),span("edit-del","−" + removed));
  return node;
}

function onExpand(details:HTMLDetailsElement, render:()=>void): void {
  const open = () => {
    if (!details.open) return;
    details.removeEventListener("toggle",open);
    render();
  };
  details.addEventListener("toggle",open);
}

function lineNode(row:EditLine): HTMLElement {
  const node = document.createElement("div");
  node.className = "edit-line " + row.kind;
  if (row.kind === "gap") { node.textContent="…"; return node; }
  node.append(span("edit-number", row.old ? String(row.old) : ""), span("edit-number", row.new ? String(row.new) : ""),
    span("edit-sign",sign[row.kind]), span("edit-text",row.text || ""));
  return node;
}

function editFile(file:EditFile): HTMLDetailsElement {
  const details = document.createElement("details");
  details.className = "edit-file";
  const header = document.createElement("summary");
  header.append(span("edit-kind",labels[file.kind]),span("edit-path", file.old_path ? file.old_path + " → " + file.path : file.path));
  if (file.kind !== "written") header.append(counts(file.added,file.removed));
  details.append(header);
  onExpand(details,() => {
    const lines = file.lines || [], info = document.createElement("div");
    info.className = "edit-actions";
    if (file.partial) info.append(span("edit-note","部分差异"));
    if (file.snippet) info.append(span("edit-note","编辑片段"));
    if (file.kind === "written") info.append(span("edit-note","写入内容，原文未知"));
    if (lines.length) {
      const copy = document.createElement("button");
      copy.type = "button"; copy.textContent = "复制所示内容";
      copy.addEventListener("click",async () => {
        const text = lines.map(line => line.kind === "gap" ? "…" :
          (line.kind === "content" ? "" : line.kind === "del" ? "-" : sign[line.kind]) + (line.text || "")).join("\n");
        try { await copyText(text); copy.textContent="已复制"; }
        catch { copy.textContent="复制失败，重试"; }
      });
      info.append(copy);
    }
    details.append(info);
    if (lines.length) {
      const body = document.createElement("div"); body.className="edit-lines";
      body.tabIndex=0; body.setAttribute("aria-label",file.path+" 文件差异");
      body.append(...lines.map(lineNode));
      details.append(body);
    }
  });
  return details;
}

export function updateEditCard(container:HTMLElement, payload:ToolEventPayload): void {
  const openPaths = new Set([...container.querySelectorAll<HTMLDetailsElement>(":scope > details[open]")].map(file=>file.dataset.path));
  container.replaceChildren();
  const edits = payload.edits;
  if (payload.source !== "external" || payload.status !== "succeeded" || !edits?.files?.length) {container.hidden=true;return;}
  container.hidden=false;
  for (const file of edits.files) {
    const details=editFile(file);
    details.dataset.path=file.path;
    container.append(details);
    if (openPaths.has(file.path)) details.open=true;
  }
  if (edits.partial) container.append(span("edit-note","部分文件未显示"));
}

interface FileSummary { path:string; edits: { file:EditFile; order:number }[] }

// Count edit operations in transcript order. A rename transfers earlier edits
// to the destination; a later creation at the old path is a separate file.
function groupFiles(calls:Map<string,FileEdits | undefined>): FileSummary[] {
  const files = new Map<string,FileSummary>();
  let order=0;
  for (const changes of calls.values()) for (const file of changes?.files || []) {
    const prior = file.old_path && file.old_path !== file.path ? files.get(file.old_path) : undefined;
    const dest = files.get(file.path);
    const group = dest || prior || {path:file.path,edits:[]};
    if (prior) {
      files.delete(prior.path);
      if (dest && dest !== prior) {
        group.edits.push(...prior.edits);
        group.edits.sort((a,b)=>a.order-b.order);
      }
    }
    group.path=file.path;
    group.edits.push({ file, order:order++ });
    files.set(file.path,group);
  }
  return [...files.values()];
}

export class TurnFileEdits {
  private turns = new Map<string,Map<string,FileEdits | undefined>>();

  observe(tool:ToolEventPayload): void {
    if (!tool.turn_id || !tool.id || tool.source !== "external") return;
    let calls = this.turns.get(tool.turn_id);
    if (!calls) { calls=new Map();this.turns.set(tool.turn_id,calls); }
    if (!calls.has(tool.id) || tool.status === "succeeded") calls.set(tool.id,tool.status === "succeeded" ? tool.edits : undefined);
  }

  render(turn:string, stack:HTMLElement): void {
    const calls=this.turns.get(turn);
    if (!calls?.size) return;
    const files=groupFiles(calls);
    if (!files.length) return;
    const rollup = document.createElement("details");
    rollup.className="turn-file-edits";
    rollup.dataset.editTurn=turn;
    const title=document.createElement("summary");
    let added=0, removed=0, partial=false, writes=false;
    for (const call of calls.values()) {
      if (!call) continue;
      partial ||= !!call.partial;
      for (const file of call.files || []) {
        added+=file.added; removed+=file.removed; partial ||= !!(file.partial || file.snippet);
        writes ||= file.kind === "written";
      }
    }
    title.append(span("edit-summary-label","本轮修改 "+files.length+" 个文件"));
    if (!writes || added || removed) title.append(counts(added,removed));
    if (partial || writes) title.append(span("edit-note","部分计数"));
    rollup.append(title);
    onExpand(rollup,() => {
      for (const group of files) {
        const row=document.createElement("details"), label=document.createElement("summary");
        label.append(span("edit-path",group.path));
        if (group.edits.some(e=>e.file.kind!=="written")) {
          label.append(counts(group.edits.reduce((sum,e)=>sum+e.file.added,0),group.edits.reduce((sum,e)=>sum+e.file.removed,0)));
        }
        row.append(label); row.className="edit-rollup-file";
        onExpand(row,()=>row.append(...group.edits.map(e=>editFile(e.file))));
        rollup.append(row);
      }
    });
    const previous=[...stack.querySelectorAll<HTMLDetailsElement>(":scope > .turn-file-edits")].find(node=>node.dataset.editTurn===turn);
    if (previous) { rollup.open=previous.open; previous.replaceWith(rollup); }
    else stack.append(rollup);
  }
}
