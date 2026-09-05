import { afterEach, describe, expect, it, vi } from "vitest";
import { conversationTopInset, initConversationLayout } from "./conversation-layout";

afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals(); document.body.style.cssText = ""; });

describe("conversation header space", () => {
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
