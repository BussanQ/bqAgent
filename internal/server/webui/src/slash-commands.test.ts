import { beforeEach, describe, expect, it, vi } from "vitest";
import { APIClient } from "./api-client";
import { commandSuggestions, createSlashCommands, type ChatCommand } from "./slash-commands";

const commands: ChatCommand[] = [
  { name: "/skill", description: "使用 Skill", arguments: [{ value: "eli5", description: "简单解释 中文描述" }, { value: "explain", description: "alias → eli5" }] },
  { name: "/fast", description: "Fast 模式", arguments: [{ value: "on", description: "开启" }, { value: "off", description: "关闭" }] },
  { name: "/codex", description: "切换 Codex" },
];
beforeEach(() => { document.body.innerHTML = '<textarea id="input"></textarea><div id="commands" hidden></div>'; });

function setup(transport = vi.fn<typeof fetch>(async () => new Response(JSON.stringify({ commands })))) {
  const input = document.querySelector("textarea")!;
  const menu = document.querySelector("div")!;
  let scope = "one";
  let conversationType = "default";
  let disabled = false;
  const complete = vi.fn();
  const controller = createSlashCommands(new APIClient(transport), input, menu, {
    url: () => "/api/v1/webui/commands?workspace_id=" + scope + "&conversation_type=" + conversationType,
    disabled: () => disabled, onComplete: complete,
  });
  const type = async (value: string) => { input.value = value; input.setSelectionRange(value.length, value.length); input.dispatchEvent(new Event("input")); await vi.waitFor(() => expect(menu.textContent).not.toBe("正在读取命令…")); };
  const key = (key: string, extra = {}) => { const event = new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true, ...extra }); input.dispatchEvent(event); return event; };
  return { input, menu, type, key, complete, controller, transport,
    setConversationType: (value: string) => { conversationType = value; controller.close(); },
    setScope: (value: string) => { scope = value; controller.close(); }, disable: () => { disabled = true; controller.close(); } };
}

describe("slash command discovery", () => {
  it("filters command prefixes, skill names, aliases and descriptions without matching task text", () => {
    expect(commandSuggestions("/sk", commands).map(item => item.value)).toEqual(["/skill"]);
    expect(commandSuggestions("/skill 中文", commands)[0].value).toBe("/skill eli5");
    expect(commandSuggestions("/skill exp", commands)[0].value).toBe("/skill explain");
    expect(commandSuggestions("/skill eli5 explain sky", commands)).toEqual([]);
    expect(commandSuggestions("text /skill", commands)).toEqual([]);
    expect(commandSuggestions("/skill\n", commands)).toEqual([]);
  });

  it("opens arguments, completes a skill with description, and does not submit on selection", async () => {
    const ui = setup();
    await ui.type("/");
    expect(ui.menu.hidden).toBe(false);
    expect(ui.key("Enter").defaultPrevented).toBe(true);
    expect(ui.input.value).toBe("/skill ");
    expect(ui.menu.textContent).toContain("简单解释 中文描述");
    expect(ui.key("ArrowDown").defaultPrevented).toBe(true);
    expect(ui.input.getAttribute("aria-activedescendant")).toBe("slash-option-1");
    ui.key("Tab");
    expect(ui.input.value).toBe("/skill explain ");
    expect(ui.menu.hidden).toBe(true);
    expect(ui.key("Enter").defaultPrevented).toBe(false);
  });

  it("wraps selection, supports clicks and Escape, and leaves composition and Shift+Enter alone", async () => {
    const ui = setup();
    await ui.type("/");
    expect(ui.key("Enter", { isComposing: true }).defaultPrevented).toBe(false);
    expect(ui.key("Enter", { shiftKey: true }).defaultPrevented).toBe(false);
    ui.key("ArrowUp"); ui.key("Enter");
    expect(ui.input.value).toBe("/codex ");
    await ui.type("/fast");
    ui.menu.querySelector<HTMLButtonElement>("button")!.click();
    expect(ui.menu.hidden).toBe(false);
    expect(ui.input.value).toBe("/fast ");
    ui.menu.querySelector<HTMLButtonElement>("button")!.click();
    expect(ui.input.value).toBe("/fast on ");
    await ui.type("/"); ui.key("Escape");
    expect(ui.menu.hidden).toBe(true);
    expect(ui.input.getAttribute("aria-expanded")).toBe("false");
  });

  it("allows dismissing failures and retries after new input without fetching ordinary text", async () => {
    const transport = vi.fn<typeof fetch>().mockRejectedValueOnce(new Error("offline")).mockResolvedValue(new Response(JSON.stringify({ commands })));
    const ui = setup(transport);
    await ui.type("ordinary text");
    expect(transport).not.toHaveBeenCalled();
    await ui.type("/");
    expect(ui.menu.textContent).toContain("读取命令失败");
    ui.key("Escape");
    await ui.type("/sk");
    expect(ui.menu.textContent).toContain("/skill");
    expect(transport).toHaveBeenCalledTimes(2);
  });

  it("refreshes metadata on reopening and switching workspaces", async () => {
    const ui = setup();
    await ui.type("/"); ui.key("Escape");
    await ui.type("/skill ");
    expect(ui.transport).toHaveBeenCalledTimes(2);
    ui.setScope("two"); await ui.type("/");
    expect(ui.transport.mock.calls.at(-1)![0].toString()).toContain("workspace_id=two");
    ui.disable(); await ui.type("/");
    expect(ui.menu.hidden).toBe(true);
  });

  it("reloads commands when entering and leaving a group instead of reusing ordinary chat candidates", async () => {
    const transport = vi.fn<typeof fetch>(async url => new Response(JSON.stringify({
      commands: String(url).includes("conversation_type=group") ? commands.filter(command => command.name !== "/codex") : commands,
    })));
    const ui = setup(transport);
    await ui.type("/");
    expect(ui.menu.textContent).toContain("/codex");
    ui.setConversationType("group");
    expect(ui.menu.hidden).toBe(true);
    await ui.type("/");
    expect(ui.menu.textContent).not.toContain("/codex");
    expect(ui.menu.textContent).toContain("/skill");
    ui.setConversationType("default"); await ui.type("/");
    expect(ui.menu.textContent).toContain("/codex");
    expect(transport).toHaveBeenCalledTimes(3);
  });

  it("discards an old workspace response and never renders descriptions as HTML", async () => {
    let resolve!: (response: Response) => void;
    const transport = vi.fn<typeof fetch>().mockImplementationOnce(() => new Promise<Response>(r => { resolve = r; })).mockResolvedValue(new Response(JSON.stringify({ commands: [{ name: "/skill", description: "<img src=x onerror=alert(1)>" }] })));
    const ui = setup(transport);
    ui.input.value = "/"; ui.input.setSelectionRange(1, 1); const old = ui.controller.update();
    expect(ui.key("Enter").defaultPrevented).toBe(true);
    ui.setScope("two"); await ui.type("/");
    resolve(new Response(JSON.stringify({ commands })));
    await old;
    expect(ui.menu.textContent).toContain("<img src=x");
    expect(ui.menu.querySelector("img")).toBeNull();
    expect(ui.menu.textContent).not.toContain("Fast 模式");
  });
});
