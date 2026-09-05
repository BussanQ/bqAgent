import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createClickFeedback } from "./click-feedback";

let feedback: ReturnType<typeof createClickFeedback>;
const pointer = (x: number, y: number, extra: Partial<PointerEvent> = {}) => ({
  pointerId: 1, pointerType: "mouse", button: 0, buttons: 1, clientX: x, clientY: y, ...extra,
}) as PointerEvent;
function click(x = 100, y = 200) {
  feedback.pointerDown(pointer(x, y));
  feedback.pointerUp(pointer(x, y, { buttons: 0 }));
}
function pulses() { return [...document.querySelectorAll<HTMLElement>(".galaxy-click-pulse")]; }
beforeEach(() => { document.body.innerHTML = ""; vi.useFakeTimers(); feedback = createClickFeedback(); });
afterEach(() => { feedback.clear(); vi.useRealTimers(); });

describe("mouse click feedback", () => {
  it("places the effect at the click position and removes it after 520ms", () => {
    click(240, 180);
    expect(pulses()).toHaveLength(1);
    expect(pulses()[0].style.left).toBe("240px");
    expect(pulses()[0].style.top).toBe("180px");
    expect(document.querySelector(".galaxy-click-layer")!.getAttribute("aria-hidden")).toBe("true");
    vi.advanceTimersByTime(519); expect(pulses()).toHaveLength(1);
    vi.advanceTimersByTime(1); expect(document.querySelector(".galaxy-click-layer")).toBeNull();
    expect(vi.getTimerCount()).toBe(0);
  });

  it("keeps only the three newest pulses, each with its own lifetime", () => {
    for (let index = 0; index < 4; index++) { click(index, 100); vi.advanceTimersByTime(100); }
    expect(pulses().map(pulse => pulse.style.left)).toEqual(["1px", "2px", "3px"]);
    expect(vi.getTimerCount()).toBe(3);
    vi.advanceTimersByTime(220);
    expect(pulses().map(pulse => pulse.style.left)).toEqual(["2px", "3px"]);
    vi.advanceTimersByTime(200);
    expect(pulses()).toHaveLength(0);
  });

  it("suppresses drags even when the pointer comes back to its starting position", () => {
    feedback.pointerDown(pointer(100, 100));
    feedback.pointerMove(pointer(106, 100));
    feedback.pointerMove(pointer(100, 100));
    feedback.pointerUp(pointer(100, 100));
    expect(pulses()).toHaveLength(0);
    feedback.pointerDown(pointer(100, 100));
    feedback.pointerUp(pointer(160, 100));
    expect(pulses()).toHaveLength(0);
    feedback.pointerDown(pointer(100, 100));
    feedback.pointerMove(pointer(104, 100));
    feedback.pointerUp(pointer(104, 100));
    expect(pulses()).toHaveLength(1);
  });

  it("ignores touch, right clicks, unmatched releases and cancelled presses", () => {
    for (const extra of [{ pointerType: "touch" }, { button: 2 }]) {
      feedback.pointerDown(pointer(100, 100, extra));
      feedback.pointerUp(pointer(100, 100, extra));
    }
    feedback.pointerUp(pointer(100, 100));
    expect(pulses()).toHaveLength(0);
    feedback.pointerDown(pointer(100, 100));
    feedback.pointerUp(pointer(100, 100, { pointerId: 2 }));
    expect(pulses()).toHaveLength(0);
    feedback.pointerCancel(pointer(100, 100));
    feedback.pointerUp(pointer(100, 100));
    expect(pulses()).toHaveLength(0);
  });

  it("clears pending presses, timers and the overlay when the page stops", () => {
    click(); feedback.pointerDown(pointer(100, 100));
    feedback.clear(); feedback.pointerUp(pointer(100, 100));
    expect(document.querySelector(".galaxy-click-layer")).toBeNull();
    expect(vi.getTimerCount()).toBe(0);
    click(); expect(pulses()).toHaveLength(1);
  });
});
