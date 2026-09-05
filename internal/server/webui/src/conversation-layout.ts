/** Reclaim the header's empty center only when the fixed controls fit beside it. */
export function conversationTopInset(viewportWidth: number, headerHeight: number, chatLeft: number, chatRight: number, brandRight: number, actionsLeft: number): number {
  if (viewportWidth <= 900 || headerHeight <= 0) return 0;
  return brandRight > chatLeft + 1 || actionsLeft < chatRight - 1 ? headerHeight : 0;
}

export function initConversationLayout(): () => void {
  const header = document.querySelector<HTMLElement>("body > header");
  const brand = header?.querySelector<HTMLElement>(".brand");
  const actions = header?.querySelector<HTMLElement>(".actions");
  const column = document.querySelector<HTMLElement>(".conversation-column");
  if (!header || !brand || !actions || !column) return () => {};
  const update = () => {
    const bounds = column.getBoundingClientRect();
    const height = header.getBoundingClientRect().height;
    const inset = conversationTopInset(window.innerWidth, height, bounds.left, bounds.right, brand.getBoundingClientRect().right, actions.getBoundingClientRect().left);
    const value = `${inset}px`;
    if (column.style.getPropertyValue("--conversation-top-inset") !== value) column.style.setProperty("--conversation-top-inset", value);
    if (height > 0) document.body.style.setProperty("--shell-header-height", `${height}px`);
  };
  const observer = typeof ResizeObserver === "undefined" ? null : new ResizeObserver(update);
  for (const element of [header, brand, actions, column]) observer?.observe(element);
  window.addEventListener("resize", update);
  update();
  return () => {
    observer?.disconnect();
    window.removeEventListener("resize", update);
    column.style.removeProperty("--conversation-top-inset");
    document.body.style.removeProperty("--shell-header-height");
  };
}
