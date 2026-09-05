import { afterEach, describe, expect, it, vi } from "vitest";
import { conversationTopInset, initConversationLayout } from "./conversation-layout";
import { readFileSync } from "node:fs";

afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals(); document.body.style.cssText = ""; });

describe("conversation header space", () => {
  it.each([240, 266.24, 336])("aligns header dividers with both sidebars at workspace width %s", workspaceWidth => {
    document.body.innerHTML = '<header><div class="brand">bqagent</div><div class="actions"><button class="icon-button"></button><div class="status">已恢复历史会话，这是一条很长的状态信息</div><button class="icon-button"></button><button class="icon-button"></button><div class="account-controls"><button class="icon-button"></button></div></div></header><div class="app-layout"><aside class="conversation-sidebar"></aside><aside class="workspace-sidebar"></aside></div>';
    const source = document.createElement("style");
    const desktop = document.createElement("style");
    try {
      source.textContent = readFileSync("src/styles.css", "utf8")
        .replaceAll("var(--workspace-width)", `${workspaceWidth}px`);
      document.head.append(source);
      // jsdom does not evaluate viewport media queries; activate the desktop rules.
      const rule = Array.from(source.sheet!.cssRules).find(rule =>
        rule.type === CSSRule.MEDIA_RULE && (rule as CSSMediaRule).conditionText === "(min-width: 901px)",
      ) as CSSMediaRule;
      const palette = getComputedStyle(document.documentElement);
      // Resolve border colors because jsdom cannot expand var() inside border shorthands.
      const resolveBorders = (css: string) => css.replace(/var\((--(?:signal-rgb|line))\)/g, (_match, name: string) => palette.getPropertyValue(name));
      desktop.textContent = resolveBorders(Array.from(rule.cssRules).map(rule => rule.cssText).join("\n"));
      source.textContent = resolveBorders(source.textContent);
      document.head.append(desktop);
      const brand = getComputedStyle(document.querySelector(".brand")!);
      expect(brand.borderRightWidth).toBe("1px");
      expect(brand.borderRightStyle).toBe("solid");
      const sidebar = getComputedStyle(document.querySelector(".conversation-sidebar")!);
      expect(sidebar.paddingTop).toBe("4px");
      expect(sidebar.paddingLeft).toBe("12px");
      expect(sidebar.paddingBottom).toBe("64px");
      expect(sidebar.borderRightWidth).toBe(brand.borderRightWidth);
      expect(sidebar.borderRightStyle).toBe(brand.borderRightStyle);
      expect(sidebar.borderRightColor).toBe(brand.borderRightColor);
      expect(brand.borderRightColor).toBe(getComputedStyle(document.querySelector(".actions")!).borderBottomColor);
      expect(parseFloat(brand.borderBottomWidth) || 0).toBe(0);
      const actions = getComputedStyle(document.querySelector(".actions")!);
      const workspace = getComputedStyle(document.querySelector(".workspace-sidebar")!);
      expect(actions.borderBottomWidth).toBe("1px");
      expect(actions.width).toBe(`${workspaceWidth}px`);
      expect(actions.width).toBe(workspace.width);
      expect(actions.flexBasis).toBe(workspace.flexBasis);
      expect(actions.flexGrow).toBe("0");
      expect(actions.flexShrink).toBe("0");
      expect(actions.minWidth).toBe("0px");
      expect(actions.borderLeftWidth).toBe(workspace.borderLeftWidth);
      expect(actions.borderLeftColor).toBe(workspace.borderLeftColor);
      const status = getComputedStyle(document.querySelector(".status")!);
      expect(status.minWidth).toBe("0px");
      expect(status.overflow).toBe("hidden");
      expect(status.textOverflow).toBe("ellipsis");
      for (const button of document.querySelectorAll(".actions > .icon-button, .actions > .account-controls")) {
        expect(getComputedStyle(button).flexShrink).toBe("0");
      }
    } finally {
      source.remove(); desktop.remove();
    }
  });

  it("reclaims the desktop center while leaving the two occupied header areas alone", () => {
    expect(conversationTopInset(1920, 62, 280, 1584, 280, 1584)).toBe(0);
    expect(conversationTopInset(1280, 62, 230, 1014, 230, 1014)).toBe(0);
    expect(conversationTopInset(1440, 62, 259, 1140, 259.2, 1139.8)).toBe(0);
  });

  it("protects messages when long status text or a collapsed sidebar puts controls over the chat", () => {
    expect(conversationTopInset(1280, 62, 230, 1014, 230, 950)).toBe(62);
    expect(conversationTopInset(1920, 62, 280, 1920, 280, 1584)).toBe(62);
    expect(conversationTopInset(1000, 70, 220, 760, 230, 760)).toBe(70);
    expect(conversationTopInset(900, 62, 0, 900, 150, 680)).toBe(0);
    expect(conversationTopInset(1440, 0, 0, 0, 0, 0)).toBe(0);
  });

  it("responds to auth visibility, toolbar changes and workspace resizing without changing sidebars", () => {
    document.body.innerHTML = '<header><div class="brand"></div><div class="actions"></div></header><div class="app-layout"><aside class="conversation-sidebar"></aside><div class="conversation-column"></div><aside class="workspace-sidebar"></aside></div>';
    vi.spyOn(window, "innerWidth", "get").mockReturnValue(1440);
    let height = 0, actionsLeft = 1140, chatRight = 1140;
    const rect = (left: number, right: number, h = height) => ({ left, right, height: h } as DOMRect);
    vi.spyOn(document.querySelector("header")!, "getBoundingClientRect").mockImplementation(() => rect(0, 1440));
    vi.spyOn(document.querySelector(".brand")!, "getBoundingClientRect").mockImplementation(() => rect(0, 259));
    vi.spyOn(document.querySelector(".actions")!, "getBoundingClientRect").mockImplementation(() => rect(actionsLeft, 1440));
    const column = document.querySelector<HTMLElement>(".conversation-column")!;
    vi.spyOn(column, "getBoundingClientRect").mockImplementation(() => rect(259, chatRight, 900));
    let notify = () => {};
    const disconnect = vi.fn();
    const observe = vi.fn();
    vi.stubGlobal("ResizeObserver", class {
      constructor(callback: () => void) { notify = callback; }
      observe = observe;
      disconnect = disconnect;
    });
    const dispose = initConversationLayout();
    expect(observe).toHaveBeenCalledTimes(4);
    height = 62; notify();
    expect(document.body.style.getPropertyValue("--shell-header-height")).toBe("62px");
    expect(column.style.getPropertyValue("--conversation-top-inset")).toBe("0px");
    actionsLeft = 1080; notify();
    expect(column.style.getPropertyValue("--conversation-top-inset")).toBe("62px");
    actionsLeft = 1140; notify();
    expect(column.style.getPropertyValue("--conversation-top-inset")).toBe("0px");
    chatRight = 1440; notify();
    expect(column.style.getPropertyValue("--conversation-top-inset")).toBe("62px");
    for (const sidebar of document.querySelectorAll("aside")) expect(sidebar.getAttribute("style")).toBeNull();
    dispose();
    expect(disconnect).toHaveBeenCalledOnce();
    expect(column.style.getPropertyValue("--conversation-top-inset")).toBe("");
  });
});
