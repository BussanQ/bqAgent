import { APIClient, isCancellation } from "./api-client";

export interface CommandArgument { value: string; label?: string; description: string }
export interface ChatCommand { name: string; description: string; arguments?: CommandArgument[]; agent?: string; insert?: string; hint?: string; kind?: string }
interface Suggestion { value: string; label: string; description: string; expand: boolean }
interface CommandContext { query: string; agent?: string; qualified?: boolean; nativeURL?: string; available?: ChatCommand[] }

export function busyCommandContext(view: CommandContext, supportsCompact: boolean, supportsPlan = false): CommandContext {
  const agent = view.agent;
  const available: ChatCommand[] = [];
  if (agent && supportsPlan) available.push({name:"/plan", agent, insert:"/" + agent + " /plan", description:"将下一条消息切换到规划模式"});
  if (agent && supportsCompact) available.push({name:"/compact", agent, insert:"/" + agent + " /compact", description:"当前回复结束后压缩此会话"});
  return { ...view, available };
}

export function commandSuggestions(value: string, commands: ChatCommand[]): Suggestion[] {
  if (value.includes("\n")) return [];
  const match = /^\/([^\s]*)(?:[ \t]+([^\s]*))?$/.exec(value);
  if (!match) return [];
  const name = "/" + match[1].toLowerCase();
  if (match[2] === undefined) {
    return commands.filter(command => command.name.toLowerCase().startsWith(name)).map(command => ({
      value: command.insert || command.name, label: command.agent ? command.name + " · " + command.agent : command.name,
      description: [command.kind === "skill" ? "Skill" : command.kind === "extension" ? "扩展命令" : command.kind === "prompt" ? "提示模板" : "", command.description, command.hint].filter(Boolean).join(" · "),
      expand: !command.agent && (Boolean(command.arguments?.length) || command.name === "/skill"),
    }));
  }
  const command = commands.find(command => !command.agent && command.name === name);
  const query = match[2].toLowerCase();
  const seen = new Set<string>();
  return (command?.arguments || []).filter(argument => {
    const key = argument.value.toLowerCase();
    if (seen.has(key) || ![key, argument.label || "", argument.description].some(text => text.toLowerCase().includes(query))) return false;
    seen.add(key);
    return true;
  }).map(argument => ({
    value: name + " " + argument.value,
    label: name + " " + (argument.label || argument.value),
    description: argument.description, expand: false,
  }));
}

export function createSlashCommands(api: APIClient, input: HTMLTextAreaElement, menu: HTMLDivElement, options: {
  url(): string;
  context?(): CommandContext;
  disabled(): boolean;
  onComplete(): void;
}) {
  let commands: ChatCommand[] | null = null;
  let scope = "";
  let loading = false;
  let sequence = 0;
  let selected = 0;
  let navigated = false;
  let suggestions: Suggestion[] = [];
  let dismissed = "";
  let request: AbortController | undefined;
  let catalogError = "";

  function context(): CommandContext {
    return options.context?.() || { query: input.value };
  }

  function catalogScope(): string {
    const view = context();
    return [options.url(), view.nativeURL, view.agent, view.qualified, view.available?.map(row => row.name).join(",") ?? "remote"].join("|");
  }

  function close(): void {
    sequence++;
    request?.abort();
    catalogError = "";
    commands = null;
    suggestions = [];
    loading = false;
    dismissed = input.value;
    menu.hidden = true;
    menu.replaceChildren();
    input.setAttribute("aria-expanded", "false");
    input.removeAttribute("aria-activedescendant");
  }

  function choose(index: number): void {
    const suggestion = suggestions[index];
    if (!suggestion) return;
    input.value = suggestion.value + " ";
    input.setSelectionRange(input.value.length, input.value.length);
    options.onComplete();
    if (suggestion.expand) { selected = 0; render(); }
    else { close(); scope = catalogScope(); }
    input.focus();
  }

  function render(): void {
    const view = context();
    suggestions = commandSuggestions(view.query, commands || []);
    selected = Math.min(selected, Math.max(0, suggestions.length - 1));
    menu.replaceChildren();
    const hint = document.createElement("div");
    hint.className = "slash-command-hint";
    hint.textContent = "↑↓ 选择 · Enter / Tab 补全 · Esc 关闭";
    menu.appendChild(hint);
    suggestions.forEach((suggestion, index) => {
      const button = document.createElement("button");
      button.type = "button";
      button.id = "slash-option-" + index;
      button.setAttribute("role", "option");
      button.setAttribute("aria-selected", String(index === selected));
      button.classList.toggle("active", index === selected);
      const label = document.createElement("strong");
      label.textContent = suggestion.label;
      const description = document.createElement("span");
      description.textContent = suggestion.description;
      button.title = suggestion.description;
      button.append(label, description);
      button.addEventListener("mousedown", event => event.preventDefault());
      button.addEventListener("click", event => { event.stopPropagation(); choose(index); });
      menu.appendChild(button);
    });
    const emptyCatalog = view.available === undefined && view.qualified && view.query === "/" && !loading && !commands?.length;
    if ((catalogError && !/\s/.test(view.query)) || emptyCatalog) {
      const status = document.createElement("div");
      status.className = "slash-command-hint";
      status.setAttribute("role", "status");
      status.textContent = catalogError || view.agent + " 当前没有公布命令";
      menu.appendChild(status);
    }
    menu.hidden = suggestions.length === 0 && menu.children.length === 1;
    input.setAttribute("aria-expanded", String(!menu.hidden));
    if (suggestions.length) {
      input.setAttribute("aria-activedescendant", "slash-option-" + selected);
      menu.querySelector<HTMLElement>(".active")?.scrollIntoView?.({ block: "nearest" });
    } else input.removeAttribute("aria-activedescendant");
  }

  async function update(): Promise<void> {
    const view = context();
    if (options.disabled() || input.selectionStart !== input.value.length || !/^\/[^\n]*$/.test(input.value) || !/^\/\S*(?:[ \t]+\S*)?$/.test(view.query)) { close(); return; }
    const url = options.url();
    const nextScope = catalogScope();
    if (scope !== nextScope) { close(); dismissed = ""; scope = nextScope; }
    if (input.value === dismissed) return;
    selected = 0;
    navigated = false;
    if (view.available !== undefined) { commands = view.available; render(); return; }
    if (commands) { render(); return; }
    if (loading) return;
    loading = true;
    const current = ++sequence;
    suggestions = [];
    menu.textContent = "正在读取命令…";
    menu.hidden = false;
    request = new AbortController();
    const sources = [...(view.nativeURL ? [view.nativeURL] : []), ...(view.qualified ? [] : [url])];
    const signal = request.signal;
    const catalogs: ChatCommand[][] = sources.map(() => []);
    let remaining = sources.length;
    await Promise.all(sources.map(async (source, index) => {
      let failure = "";
      try {
        const result = await api.json<{ commands: ChatCommand[]; error?: string }>(source, { signal });
        catalogs[index] = result.commands;
        if (result.error) failure = view.agent + " 的其他命令暂时无法读取：" + result.error;
      } catch (error) {
        if (isCancellation(error)) { if (current === sequence) close(); return; }
        failure = source === view.nativeURL ? view.agent + " 命令读取失败，请重新输入 / 重试" : "读取命令失败，请重新输入 / 重试";
      }
      if (current !== sequence || catalogScope() !== scope || options.disabled()) return;
      const previous = navigated ? suggestions[selected]?.value : undefined;
      catalogError = failure || catalogError;
      loading = --remaining > 0;
      commands = catalogs.flat();
      if (!commands.length && loading && !catalogError) return;
      // Preserve the highlighted action when the second catalog arrives.
      if (previous) selected = Math.max(0, commandSuggestions(context().query, commands).findIndex(item => item.value === previous));
      render();
    }));
  }

  input.setAttribute("aria-controls", menu.id);
  input.setAttribute("aria-autocomplete", "list");
  input.addEventListener("input", () => { dismissed = ""; void update(); });
  input.addEventListener("focus", () => { void update(); });
  input.addEventListener("click", () => { void update(); });
  input.addEventListener("keydown", event => {
    if (event.isComposing || menu.hidden) return;
    if (["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) { close(); return; }
    if (event.key === "Escape") { event.preventDefault(); close(); return; }
    if (!loading && !suggestions.length) return;
    if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      event.preventDefault();
      if (suggestions.length) { navigated = true; selected = (selected + (event.key === "ArrowDown" ? 1 : -1) + suggestions.length) % suggestions.length; render(); }
    } else if ((event.key === "Enter" || event.key === "Tab") && !event.shiftKey) {
      // Do not submit the chat while the user is selecting or loading commands.
      event.preventDefault();
      if (suggestions.length) choose(selected);
    }
  });
  document.addEventListener("click", event => {
    if (event.target !== input && !menu.contains(event.target as Node)) close();
  });
  function contextChanged(): void {
    if (!menu.hidden && catalogScope() !== scope) void update();
  }
  return { close, update, contextChanged };
}
