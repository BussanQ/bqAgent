import { afterEach, describe, expect, it, vi } from "vitest";
import { advanceAttraction, createPointerTrail, foregroundPosition, MAX_TRAIL_SEGMENTS, TRAIL_LIFETIME } from "./pointer-feedback";

afterEach(() => { vi.useRealTimers(); document.body.innerHTML = ""; });

describe("mouse attraction and trails", () => {
  it("reaches an exact resting position in bounded time with and without a pointer", () => {
    const position = { x: 100, y: 100 };
    let offset = { x: 0, y: 0 };
    for (let i = 0; i < 30; i++) offset = advanceAttraction(offset, position, { x: 180, y: 100 }, 1 / 30);
    expect(offset).toEqual({ x: 30, y: 0 });
    expect(advanceAttraction(offset, position, { x: 180, y: 100 }, 1 / 30)).toEqual(offset);
    for (let i = 0; i < 30; i++) offset = advanceAttraction(offset, position, null, 1 / 30);
    expect(offset).toEqual({ x: 0, y: 0 });
  });

  it("pulls nearby stars toward the pointer with bounded, frame-rate independent motion", () => {
    const position = { x: 100, y: 100 }, pointer = { x: 180, y: 100 };
    const offset = advanceAttraction({ x: 0, y: 0 }, position, pointer, .1);
    expect(offset.x).toBeGreaterThan(0);
    expect(offset.x).toBeLessThan(30);
    expect(offset.y).toBe(0);
    let subdivided = { x: 0, y: 0 };
    for (let i = 0; i < 10; i++) subdivided = advanceAttraction(subdivided, position, pointer, .01);
    expect(subdivided.x).toBeCloseTo(offset.x, 10);
    expect(advanceAttraction(offset, position, pointer, 100).x).toBeLessThanOrEqual(30);
    expect(advanceAttraction({ x: 0, y: 0 }, position, { x: 500, y: 500 }, 1)).toEqual({ x: 0, y: 0 });
  });

  it("smoothly restores the star when the mouse leaves or moves away", () => {
    for (const pointer of [null, { x: 900, y: 900 }]) {
      const next = advanceAttraction({ x: 30, y: -10 }, { x: 100, y: 100 }, pointer, .05);
      expect(next.x).toBeGreaterThan(0);
      expect(next.x).toBeLessThan(30);
      const home = advanceAttraction(next, { x: 100, y: 100 }, pointer, 1);
      expect(Math.hypot(home.x, home.y)).toBeLessThan(.002);
    }
  });

  it("projects the same foreground position after either direction of wrapping", () => {
    const star = { x: .9, y: .9 };
    const position = foregroundPosition(star, { x: 250, y: 200 }, 1000, 600);
    expect(position).toEqual({ x: 50, y: 80 });
    for (const loops of [-100, -1, 1, 100]) {
      expect(foregroundPosition(star, { x: 250 + loops * 2000, y: 200 + loops * 1200 }, 1000, 600)).toEqual(position);
    }
  });

  it("samples hover trails, caps DOM nodes, fades them and avoids connecting stale positions", () => {
    vi.useFakeTimers();
    const trail = createPointerTrail();
    trail.move({ x: 100, y: 100 }, 0);
    trail.move({ x: 102, y: 100 }, 10);
    expect(document.querySelector(".galaxy-trail-layer")).toBeNull();
    trail.move({ x: 120, y: 100 }, 20);
    const segment = document.querySelector<HTMLElement>(".galaxy-trail-segment")!;
    expect(segment.style.left).toBe("100px");
    expect(segment.style.width).toBe("20px");
    expect(segment.style.transform).toBe("rotate(0rad)");
    for (let i = 1; i <= 100; i++) trail.move({ x: 120 + i * 20, y: 100 }, 20 + i);
    expect(document.querySelectorAll(".galaxy-trail-segment")).toHaveLength(MAX_TRAIL_SEGMENTS);
    vi.advanceTimersByTime(TRAIL_LIFETIME);
    expect(document.querySelector(".galaxy-trail-layer")).toBeNull();
    trail.move({ x: 1, y: 1 }, 1000);
    expect(document.querySelector(".galaxy-trail-layer")).toBeNull();
    trail.move({ x: 21, y: 1 }, 1020);
    expect(document.querySelectorAll(".galaxy-trail-segment")).toHaveLength(1);
    trail.clear();
    expect(vi.getTimerCount()).toBe(0);
    expect(document.querySelector(".galaxy-trail-layer")).toBeNull();
    trail.move({ x: 500, y: 500 }, 1030);
    expect(document.querySelector(".galaxy-trail-layer")).toBeNull();
  });
});
