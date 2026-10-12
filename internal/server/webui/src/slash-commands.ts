import { APIClient, isCancellation } from "./api-client";

export interface CommandArgument { value: string; label?: string; description: string }
export interface ChatCommand { name: string; description: string; arguments?: CommandArgument[] }
interface Suggestion { value: string; label: string; description: string; expand: boolean }

export function commandSuggestions(value: string, commands: ChatCommand[]): Suggestion[] {
  if (value.includes("\n")) return [];
  const match = /^\/([^\s]*)(?:[ \t]+([^\s]*))?$/.exec(value);
  if (!match) return [];
  const name = "/" + match[1].toLowerCase();
  if (match[2] === undefined) {
    return commands.filter(command => command.name.startsWith(name)).map(command => ({
      value: command.name, label: command.name, description: command.description,
      expand: Boolean(command.arguments?.length) || command.name === "/skill",
    }));
  }
  const command = commands.find(command => command.name === name);
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
  disabled(): boolean;
  onComplete(): void;
}) {
  let commands: ChatCommand[] | null = null;
  let scope = "";
  let loading = false;
  let sequence = 0;
  let selected = 0;
  let suggestions: Suggestion[] = [];
  let dismissed = "";

  function close(): void {
    sequence++;
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
    else close();
    input.focus();
  }

  function render(): void {
    suggestions = commandSuggestions(input.value, commands || []);
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
    menu.hidden = suggestions.length === 0;
    input.setAttribute("aria-expanded", String(!menu.hidden));
    if (!menu.hidden) {
      input.setAttribute("aria-activedescendant", "slash-option-" + selected);
      menu.querySelector<HTMLElement>(".active")?.scrollIntoView?.({ block: "nearest" });
    } else input.removeAttribute("aria-activedescendant");
  }

  async function update(): Promise<void> {
    if (options.disabled() || input.selectionStart !== input.value.length || !/^\/[^\n]*$/.test(input.value)) { close(); return; }
    if (input.value === dismissed) return;
    const url = options.url();
    if (scope !== url) { close(); dismissed = ""; scope = url; }
    selected = 0;
    if (commands) { render(); return; }
    if (loading) return;
    loading = true;
    const current = ++sequence;
    suggestions = [];
    menu.textContent = "正在读取命令…";
    menu.hidden = false;
    try {
      const result = await api.json<{ commands: ChatCommand[] }>(url);
      if (current !== sequence || options.url() !== url || options.disabled()) return;
      commands = result.commands;
      render();
    } catch (error) {
      if (current === sequence) {
        if (isCancellation(error)) close();
        else { menu.textContent = "读取命令失败，请重新输入 / 重试"; menu.hidden = false; }
      }
    } finally { if (current === sequence) loading = false; }
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
    if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      event.preventDefault();
      if (suggestions.length) { selected = (selected + (event.key === "ArrowDown" ? 1 : -1) + suggestions.length) % suggestions.length; render(); }
    } else if ((event.key === "Enter" || event.key === "Tab") && !event.shiftKey) {
      // Do not submit the chat while the user is selecting or loading commands.
      event.preventDefault();
      if (suggestions.length && !loading) choose(selected);
    }
  });
  document.addEventListener("click", event => {
    if (event.target !== input && !menu.contains(event.target as Node)) close();
  });
  return { close, update };
}
