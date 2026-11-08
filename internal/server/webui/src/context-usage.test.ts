import { readFileSync } from "node:fs";
import { expect, it } from "vitest";
import { AgentContextMeter } from "./context-usage";

function meter() {
  const doc = new DOMParser().parseFromString(readFileSync("index.html","utf8"),"text/html");
  const details = doc.querySelector<HTMLDetailsElement>(".composer-meta #context-usage")!;
  return { details, meter:new AgentContextMeter(details) };
}

it("shows the selected agent's reported or estimated context in the composer and keeps percentages above 100", () => {
  const {details,meter: view} = meter();
  view.configure("chat","pi");
  expect(details.hidden).toBe(true);
  view.observe("chat","pi",{captured:1,used:1250,window:1000,estimated:true,model:"p/m"});
  expect(details.hidden).toBe(false);
  expect(details.querySelector("summary")!.textContent).toContain("125%");
  expect(details.classList.contains("critical")).toBe(true);
  expect(details.style.getPropertyValue("--context-fill")).toBe("100%");
  expect(details.textContent).toContain("估算已用 1,250 / 1,000 tokens");
  details.open = true;
  view.configure("chat","codex");
  expect(details.open).toBe(false); expect(details.hidden).toBe(true);
  view.observe("chat","codex",{captured:2,used:0,window:200});
  expect(details.hidden).toBe(false); expect(details.textContent).toContain("已用 0 / 200 tokens");
  view.configure("chat","pi"); expect(details.textContent).toContain("125%");
  view.configure("chat",""); expect(details.hidden).toBe(true);
});

it("ignores stale catalog reads after compaction and isolates contexts, branches and workspaces", () => {
  const {details,meter: view} = meter();
  view.configure("ws/source/0","codex");
  view.observe("ws/source/0","codex",{captured:3,used:80,window:100});
  view.observe("ws/source/0","codex",{captured:4,used:0,window:0});
  view.observe("ws/source/0","codex",{captured:3,used:80,window:100});
  expect(details.hidden).toBe(true);
  view.observe("ws/source/0","codex",{captured:5,used:20,window:100});
  expect(details.textContent).toContain("20%");
  for (const scope of ["ws/source/1","ws/child/0","other/source/0"]) {
    view.configure(scope,"codex");
    view.observe("ws/source/0","codex",{captured:8,used:80,window:100});
    expect(details.hidden).toBe(true);
  }
  view.configure("ws/source/1","codex",1);
  view.observe("ws/source/1","codex",{context:0,captured:10,used:88,window:100});
  expect(details.hidden).toBe(true);
});

it("displays native categories and thresholds and discards only an unchanged idle reading", () => {
  const {details,meter:view} = meter();
  view.configure("ws/chat","claude");
  view.observe("ws/chat","claude",{captured:1,used:20,window:100,percentage:25,compact_at:80,summary:true,
    categories:[{name:"Messages",tokens:15},{name:"<tools>",tokens:5,deferred:true}]});
  expect(details.querySelector("summary")!.textContent).toContain("25%");
  expect(details.textContent).toContain("自动压缩阈值 80 tokens");
  expect(details.textContent).toContain("摘要估算已用 20 / 100");
  expect(details.querySelector("ul")!.textContent).toContain("<tools>（按需加载） · 5");
  const first=view.captured("ws/chat","claude");
  view.observe("ws/chat","claude",{captured:2,used:27,window:100});
  view.reconcileIdle("ws/chat","claude",first);
  expect(details.hidden).toBe(false);
  expect(details.querySelector("ul")!.hidden).toBe(true);
  view.reconcileIdle("ws/chat","claude",2);
  expect(details.hidden).toBe(true);
});
