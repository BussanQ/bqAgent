import { describe, expect, it, vi } from "vitest";
import { StreamRenderQueue } from "./stream-render";

function fixture(top = 800) {
  const scroller = document.createElement("div");
  let height = 1000;
  Object.defineProperty(scroller, "scrollHeight", { get: () => height });
  Object.defineProperty(scroller, "clientHeight", { value: 200 });
  scroller.scrollTop = top;
  const frames = new Map<number, FrameRequestCallback>();
  let next = 0;
  const request = vi.fn((callback: FrameRequestCallback) => { frames.set(++next, callback); return next; });
  const cancel = vi.fn((id: number) => frames.delete(id));
  const queue = new StreamRenderQueue(scroller, request, cancel);
  return { scroller, queue, request, frames, setHeight: (n: number) => { height = n; }, tick: () => {
    const ready = Array.from(frames.values()); frames.clear(); ready.forEach(callback => callback(0));
  } };
}

describe("stream frame rendering", () => {
  it("calls browser frame APIs with window as their receiver", () => {
    const frames = new Map<number, FrameRequestCallback>();
    let next = 0;
    const request = vi.spyOn(window, "requestAnimationFrame").mockImplementation(function (this: Window, callback) {
      if (this !== window) throw new TypeError("Illegal invocation");
      frames.set(++next, callback);
      return next;
    });
    const cancel = vi.spyOn(window, "cancelAnimationFrame").mockImplementation(function (this: Window, id) {
      if (this !== window) throw new TypeError("Illegal invocation");
      frames.delete(id);
    });
    try {
      const queue = new StreamRenderQueue(document.createElement("div"));
      const render = vi.fn();
      queue.schedule({}, render);
      expect(request).toHaveBeenCalledTimes(1);
      queue.flush();
      expect(render).toHaveBeenCalledTimes(1);
      expect(cancel).toHaveBeenCalledWith(1);
      queue.schedule({}, render);
      queue.dispose();
      expect(cancel).toHaveBeenCalledWith(2);
      expect(frames.size).toBe(0);
    } finally {
      request.mockRestore();
      cancel.mockRestore();
    }
  });
  it("coalesces updates to the latest text once per frame", () => {
    const f = fixture(); const bubble = document.createElement("div"); const render = vi.fn();
    for (let i = 0; i < 100; i++) f.queue.schedule(bubble, () => { bubble.textContent = String(i); render(); });
    expect(f.request).toHaveBeenCalledTimes(1);
    expect(render).not.toHaveBeenCalled();
    f.tick();
    expect(render).toHaveBeenCalledTimes(1);
    expect(bubble.textContent).toBe("99");
    f.queue.dispose();
  });
  it("follows near the bottom and preserves a reader's position", () => {
    const f = fixture();
    f.queue.schedule({}, () => f.setHeight(1200)); f.tick();
    expect(f.scroller.scrollTop).toBe(1200);
    f.scroller.scrollTop = 100; f.scroller.dispatchEvent(new Event("scroll"));
    f.queue.schedule({}, () => f.setHeight(1400)); f.tick();
    expect(f.scroller.scrollTop).toBe(100);
    f.scroller.scrollTop = 1150; f.scroller.dispatchEvent(new Event("scroll"));
    f.queue.schedule({}, () => f.setHeight(1500)); f.tick();
    expect(f.scroller.scrollTop).toBe(1500);
    f.queue.dispose();
  });
  it("starts without following when the reader is far from the bottom", () => {
    const f = fixture(0); f.queue.schedule({}, () => f.setHeight(1200)); f.tick();
    expect(f.scroller.scrollTop).toBe(0); f.queue.dispose();
  });
  it("flushes before terminal rendering so an old frame cannot overwrite it", () => {
    const f = fixture(); const bubble = document.createElement("div");
    f.queue.schedule(bubble, () => { bubble.textContent = "partial"; });
    f.queue.flush(); bubble.textContent = "final"; f.tick();
    expect(bubble.textContent).toBe("final"); expect(f.frames.size).toBe(0);
    f.queue.dispose();
  });
  it("discards pending work on disposal", () => {
    const f = fixture(); const render = vi.fn();
    f.queue.schedule({}, render); f.queue.dispose(); f.tick();
    f.queue.schedule({}, render); f.queue.flush();
    expect(render).not.toHaveBeenCalled(); expect(f.frames.size).toBe(0);
  });
});
