import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Camera } from "./galaxy";
import { foregroundPosition } from "./pointer-feedback";

const renderer = vi.hoisted(() => ({ create: vi.fn(), draw: vi.fn() }));
vi.mock("./galaxy-renderer", () => ({ createGalaxyLayers: renderer.create, renderGalaxy: renderer.draw }));
let particles: typeof import("./particles");
let callbacks: Map<number, FrameRequestCallback>;
let reduced: boolean;
let fine: boolean;
let hidden: boolean;
let now: number;
let main: HTMLElement;
let capture: number | null;
let context: CanvasRenderingContext2D;
let mediaListeners: Map<string, () => void>;

beforeEach(async () => {
  vi.resetModules();
  document.body.className = "auth-ready";
  document.body.innerHTML = '<canvas id="particle-field"></canvas><main id="main"><div id="thread"><div class="empty"><p>Welcome</p><p class="galaxy-hint">Hint</p><div class="prompts"><button>Run</button></div></div><div class="msg"><div class="bubble">Message</div></div></div></main><aside><button>Side</button></aside><textarea></textarea>';
  document.documentElement.style.cssText = "--galaxy-light: 0";
  main = document.querySelector("main")!;
  vi.spyOn(window, "innerWidth", "get").mockReturnValue(1440);
  vi.spyOn(window, "innerHeight", "get").mockReturnValue(900);
  vi.spyOn(window, "devicePixelRatio", "get").mockReturnValue(3);
  reduced = false; fine = true; hidden = false; now = 0; capture = null;
  vi.spyOn(performance, "now").mockImplementation(() => now);
  vi.spyOn(document, "hidden", "get").mockImplementation(() => hidden);
  mediaListeners = new Map();
  vi.stubGlobal("matchMedia", (query: string) => ({
    get matches() { return query.includes("reduced-motion") ? reduced : fine; },
    addEventListener: (_event: string, handler: () => void) => mediaListeners.set(query, handler),
    removeEventListener: () => mediaListeners.delete(query),
  }));
  callbacks = new Map();
  let id = 0;
  vi.stubGlobal("requestAnimationFrame", vi.fn((callback: FrameRequestCallback) => { callbacks.set(++id, callback); return id; }));
  vi.stubGlobal("cancelAnimationFrame", vi.fn((frame: number) => callbacks.delete(frame)));
  context = { setTransform: vi.fn(), clearRect: vi.fn() } as unknown as CanvasRenderingContext2D;
  vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockReturnValue(context);
  main.setPointerCapture = vi.fn(pointerId => { capture = pointerId; });
  main.hasPointerCapture = vi.fn(pointerId => capture === pointerId);
  main.releasePointerCapture = vi.fn(() => { capture = null; });
  renderer.create.mockReset().mockImplementation(() => ({ far: { canvas: document.createElement("canvas") }, galaxy: { canvas: document.createElement("canvas") } }));
  renderer.draw.mockReset();
  particles = await import("./particles");
});
afterEach(() => {
  particles.disposeParticleField();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  document.body.className = "";
  document.documentElement.style.cssText = "";
});

function frame(delta = 34) {
  now += delta;
  const pending = [...callbacks];
  callbacks.clear();
  pending.forEach(([, callback]) => callback(now));
}
function pointer(type: string, target: EventTarget, x: number, y: number, options: { buttons?: number; pointerId?: number; pointerType?: string; button?: number } = {}) {
  const event = new MouseEvent(type, { bubbles: true, composed: true, cancelable: true, clientX: x, clientY: y, buttons: type === "pointerup" ? 0 : 1, ...options });
  Object.defineProperties(event, { pointerId: { value: options.pointerId ?? 1 }, pointerType: { value: options.pointerType ?? "mouse" } });
  target.dispatchEvent(event);
  return event;
}
function camera(): Camera { return renderer.draw.mock.lastCall![5]; }
function initialize() { particles.initParticleField(); frame(); }
function settle() {
  for (let i = 0; i < 60 && callbacks.size; i++) frame();
  expect(callbacks.size).toBe(0);
}
function drag() {
  pointer("pointerdown", main, 100, 100);
  now += 20;
  pointer("pointermove", main, 200, 160);
  frame();
}

describe("galaxy background lifecycle and interaction", () => {
  it("draws once at startup and schedules no frames while the mouse is stationary", () => {
    initialize();
    expect(callbacks.size).toBe(0);
    const scheduled = vi.mocked(requestAnimationFrame).mock.calls.length;
    for (let i = 0; i < 300; i++) frame();
    expect(renderer.draw).toHaveBeenCalledTimes(1);
    expect(vi.mocked(requestAnimationFrame).mock.calls.length).toBe(scheduled);
    pointer("pointerdown", document.querySelector("button")!, 200, 100);
    pointer("pointerup", document.querySelector("button")!, 200, 100);
    expect(document.querySelector(".galaxy-click-pulse")).not.toBeNull();
    expect(callbacks.size).toBe(0);
    expect(renderer.draw).toHaveBeenCalledTimes(1);
  });

  it("settles hover attraction, sleeps with the pointer present and wakes after a long idle", () => {
    initialize();
    pointer("pointermove", main, 300, 200, { buttons: 0 });
    expect(callbacks.size).toBe(1);
    settle();
    const count = renderer.draw.mock.calls.length;
    expect(count).toBeGreaterThan(1);
    const previousOffsets = renderer.draw.mock.lastCall![7];
    const elapsed = renderer.draw.mock.lastCall![6];
    frame(60000);
    pointer("pointermove", main, 300, 200, { buttons: 0 });
    expect(callbacks.size).toBe(0);
    expect(renderer.draw).toHaveBeenCalledTimes(count);
    pointer("pointermove", main, 380, 240, { buttons: 0 });
    frame();
    expect(renderer.draw.mock.lastCall![7]).not.toEqual(previousOffsets);
    expect(renderer.draw.mock.lastCall![6] - elapsed).toBeLessThan(.051);
    settle();
    pointer("pointerout", main, 380, 240, { buttons: 0 });
    settle();
    expect(renderer.draw.mock.lastCall![7].every((p: { x: number; y: number }) => p.x === 0 && p.y === 0)).toBe(true);
    expect(renderer.create).toHaveBeenCalledTimes(1);
  });

  it("sleeps during a stationary held drag and after release inertia and animated reset", () => {
    initialize(); drag();
    settle();
    expect(capture).toBe(1);
    const held = { ...camera() };
    frame(60000);
    expect(camera()).toEqual(held);
    pointer("pointermove", main, 240, 180);
    frame();
    expect(camera()).toMatchObject({ x: 140, y: 80 });
    pointer("pointerup", main, 240, 180);
    settle();
    expect(camera().x).toBeGreaterThan(140);
    expect(camera()).toMatchObject({ vx: 0, vy: 0 });
    frame(60000);
    main.dispatchEvent(new MouseEvent("dblclick", { bubbles: true }));
    frame();
    expect(camera().x).toBeGreaterThan(0);
    settle();
    expect(camera()).toMatchObject({ x: 0, y: 0, vx: 0, vy: 0 });
  });

  it("repaints once for theme, resize and visibility restoration then returns to idle", () => {
    initialize();
    const redrawOnce = (action: () => void) => {
      const count = renderer.draw.mock.calls.length;
      action(); frame();
      expect(renderer.draw).toHaveBeenCalledTimes(count + 1);
      expect(callbacks.size).toBe(0);
    };
    redrawOnce(() => {
      document.documentElement.style.setProperty("--galaxy-light", "1");
      particles.refreshParticlePalette();
    });
    redrawOnce(() => window.dispatchEvent(new Event("resize")));
    hidden = true; document.dispatchEvent(new Event("visibilitychange"));
    expect(callbacks.size).toBe(0);
    redrawOnce(() => { hidden = false; document.dispatchEvent(new Event("visibilitychange")); });
  });

  it("restores mouse hover attraction and trails over content without dragging or rebuilding", () => {
    initialize();
    const scene = renderer.draw.mock.lastCall![2];
    const index = scene.near.findIndex((star: { x: number; y: number }) => {
      const p = foregroundPosition(star, camera(), 1440, 900);
      return p.x > 50 && p.x < 1200 && p.y > 50 && p.y < 800;
    });
    expect(index).toBeGreaterThanOrEqual(0);
    const p = foregroundPosition(scene.near[index], camera(), 1440, 900);
    const host = document.createElement("test-content");
    const button = document.createElement("button");
    host.attachShadow({ mode: "open" }).append(button); main.append(host);
    pointer("pointermove", button, p.x + 60, p.y, { buttons: 0 });
    const move = pointer("pointermove", button, p.x + 80, p.y, { buttons: 0 });
    frame();
    expect(move.defaultPrevented).toBe(false);
    expect(capture).toBeNull();
    expect(camera()).toMatchObject({ x: 0, y: 0 });
    expect(renderer.draw.mock.lastCall![7][index].x).toBeGreaterThan(0);
    expect(document.querySelectorAll(".galaxy-trail-segment")).toHaveLength(1);
    expect(renderer.create).toHaveBeenCalledTimes(1);
    const displacement = renderer.draw.mock.lastCall![7][index].x;
    pointer("pointerout", button, p.x + 80, p.y, { buttons: 0 });
    frame();
    expect(renderer.draw.mock.lastCall![7][index].x).toBeLessThan(displacement);
    expect(document.querySelector(".galaxy-trail-layer")).toBeNull();
  });

  it("disables hover effects for touch, reduced motion and mobile, and restores them on desktop", () => {
    initialize();
    const hover = (pointerType = "mouse") => {
      pointer("pointermove", main, 100, 100, { buttons: 0, pointerType });
      pointer("pointermove", main, 200, 100, { buttons: 0, pointerType }); frame();
    };
    hover("touch");
    expect(document.querySelector(".galaxy-trail-layer")).toBeNull();
    reduced = true; particles.refreshParticleCapability(); hover();
    expect(renderer.draw.mock.lastCall![7]).toEqual([]);
    expect(document.querySelector(".galaxy-trail-layer")).toBeNull();
    reduced = false; fine = false; particles.refreshParticleCapability(); hover();
    expect(document.querySelector(".galaxy-trail-layer")).toBeNull();
    fine = true; particles.refreshParticleCapability(); hover();
    expect(document.querySelector(".galaxy-trail-layer")).not.toBeNull();
    particles.disposeParticleField();
    expect(document.querySelector(".galaxy-trail-layer")).toBeNull();
    hover();
    expect(document.querySelector(".galaxy-trail-layer")).toBeNull();
  });

  it("captures only after the threshold, moves the camera, hides the hint and reuses cached layers", () => {
    initialize();
    pointer("pointerdown", main, 100, 100);
    pointer("pointermove", main, 104, 100);
    expect(capture).toBeNull();
    expect(document.body.classList.contains("galaxy-explored")).toBe(false);
    now += 20;
    pointer("pointermove", main, 200, 140);
    frame();
    expect(camera()).toMatchObject({ x: 100, y: 40 });
    expect(capture).toBe(1);
    expect(document.body.classList.contains("galaxy-explored")).toBe(true);
    pointer("pointermove", document.querySelector("button")!, 220, 150);
    frame();
    expect(camera()).toMatchObject({ x: 120, y: 50 });
    expect(renderer.create).toHaveBeenCalledTimes(1);
    pointer("pointerup", main, 220, 150);
    expect(capture).toBeNull();
    frame();
    expect(camera().x).toBeGreaterThan(120);
    expect(document.body.classList.contains("galaxy-dragging")).toBe(false);
  });

  it("continues across many complete wraps without clamping or rebuilding textures", () => {
    reduced = true;
    initialize();
    pointer("pointerdown", main, 100, 100);
    now += 20;
    pointer("pointermove", main, 100 + 1440 * 40 * 100 + 130, 100 - 900 * 40 * 80 - 60);
    frame();
    expect(camera()).toMatchObject({ x: 130, y: -60 });
    now += 20;
    pointer("pointermove", main, 100 + 1440 * 40 * 100 + 170, 100 - 900 * 40 * 80 - 40);
    frame();
    expect(camera()).toMatchObject({ x: 170, y: -40 });
    pointer("pointerup", main, 200, 200); frame();
    pointer("pointerdown", main, 100, 100);
    now += 20;
    pointer("pointermove", main, 130, 120); frame();
    expect(camera()).toMatchObject({ x: 200, y: -20 });
    expect(renderer.create).toHaveBeenCalledTimes(1);
    expect(capture).toBe(1);
  });

  it("restores blank and control clicks without preventing their normal events", () => {
    initialize();
    for (const target of [main, document.querySelector("button")!, document.querySelector(".bubble")!]) {
      const down = pointer("pointerdown", target, 200, 140);
      const up = pointer("pointerup", target, 200, 140);
      expect(down.defaultPrevented).toBe(false);
      expect(up.defaultPrevented).toBe(false);
    }
    expect(document.querySelectorAll(".galaxy-click-pulse")).toHaveLength(3);
    frame();
    expect(camera()).toMatchObject({ x: 0, y: 0 });
    window.dispatchEvent(new Event("blur"));
    expect(document.querySelector(".galaxy-click-layer")).toBeNull();
  });

  it("keeps actual drags and reduced-motion interactions free of click pulses", () => {
    initialize(); drag(); pointer("pointerup", main, 200, 160);
    expect(document.querySelector(".galaxy-click-layer")).toBeNull();
    reduced = true; particles.refreshParticleCapability();
    pointer("pointerdown", main, 100, 100); pointer("pointerup", main, 100, 100);
    expect(document.querySelector(".galaxy-click-layer")).toBeNull();
  });

  it("removes visible click feedback on logout", async () => {
    initialize();
    pointer("pointerdown", main, 100, 100); pointer("pointerup", main, 100, 100);
    expect(document.querySelectorAll(".galaxy-click-pulse")).toHaveLength(1);
    document.body.classList.replace("auth-ready", "auth-login");
    await Promise.resolve();
    expect(document.querySelector(".galaxy-click-layer")).toBeNull();
  });

  it("ignores text, controls, sidebars, touch, right clicks and other pointers", () => {
    initialize();
    for (const selector of [".bubble", ".empty p", ".prompts button", "aside", "textarea"]) {
      const down = pointer("pointerdown", document.querySelector(selector)!, 100, 100);
      pointer("pointermove", main, 200, 100);
      pointer("pointerup", main, 200, 100);
      expect(down.defaultPrevented).toBe(false);
      expect(capture).toBeNull();
    }
    for (const options of [{ pointerType: "touch" }, { button: 2 }]) {
      pointer("pointerdown", main, 100, 100, options);
      pointer("pointermove", main, 200, 100, options);
      expect(capture).toBeNull();
    }
    pointer("pointerdown", main, 100, 100);
    pointer("pointermove", main, 200, 100, { pointerId: 2 });
    expect(capture).toBeNull();
    frame();
    expect(camera()).toMatchObject({ x: 0, y: 0 });
    const wheel = new WheelEvent("wheel", { cancelable: true, bubbles: true });
    main.dispatchEvent(wheel);
    expect(wheel.defaultPrevented).toBe(false);
  });

  it("returns to the origin on a blank double click and leaves content double clicks alone", () => {
    initialize(); drag(); pointer("pointerup", main, 200, 160);
    document.querySelector(".bubble")!.dispatchEvent(new MouseEvent("dblclick", { bubbles: true }));
    frame(); expect(camera().x).toBeGreaterThan(100);
    main.dispatchEvent(new MouseEvent("dblclick", { bubbles: true }));
    for (let i = 0; i < 14; i++) frame();
    expect(camera().x).toBeCloseTo(0);
    expect(camera().y).toBeCloseTo(0);
  });

  it("keeps reduced motion draggable with no idle frames or release inertia", () => {
    reduced = true;
    initialize();
    expect(callbacks.size).toBe(0);
    drag();
    expect(camera()).toMatchObject({ x: 100, y: 60 });
    pointer("pointerup", main, 200, 160); frame();
    expect(camera()).toMatchObject({ x: 100, y: 60, vx: 0, vy: 0 });
    expect(callbacks.size).toBe(0);
    main.dispatchEvent(new MouseEvent("dblclick", { bubbles: true })); frame();
    expect(camera()).toMatchObject({ x: 0, y: 0 });
    expect(callbacks.size).toBe(0);
  });

  it("throttles reduced-motion drag repaints and drops stale release velocity", () => {
    reduced = true;
    initialize();
    pointer("pointerdown", main, 100, 100);
    pointer("pointermove", main, 140, 100);
    frame(10); frame(10); frame(10);
    expect(renderer.draw).toHaveBeenCalledTimes(1);
    frame(4);
    expect(renderer.draw).toHaveBeenCalledTimes(2);
    expect(camera().x).toBe(40);
    pointer("pointerup", main, 140, 100); frame();
    reduced = false;
    particles.refreshParticleCapability(); frame();
    drag(); now += 100;
    pointer("pointerup", main, 200, 160); frame();
    expect(camera()).toMatchObject({ vx: 0, vy: 0 });
  });

  it("renders a static scene on mobile and enables dragging when capability changes", () => {
    vi.spyOn(window, "innerWidth", "get").mockReturnValue(390);
    fine = false; initialize();
    expect(renderer.draw).toHaveBeenCalledTimes(1);
    expect(callbacks.size).toBe(0);
    expect(document.body.classList.contains("galaxy-interactive")).toBe(false);
    pointer("pointerdown", main, 100, 100); pointer("pointermove", main, 200, 100);
    expect(capture).toBeNull();
    vi.spyOn(window, "innerWidth", "get").mockReturnValue(1440);
    fine = true;
    mediaListeners.get("(hover: hover) and (pointer: fine)")!();
    frame();
    expect(document.body.classList.contains("galaxy-interactive")).toBe(true);
  });

  it.each(["pointercancel", "lostpointercapture", "blur", "hidden", "logout"])("cancels movement on %s and resumes without a jump", async reason => {
    initialize();
    pointer("pointermove", main, 100, 100, { buttons: 0 });
    drag();
    expect(document.querySelector(".galaxy-trail-layer")).not.toBeNull();
    if (reason === "hidden") { hidden = true; document.dispatchEvent(new Event("visibilitychange")); }
    else if (reason === "logout") { document.body.classList.replace("auth-ready", "auth-login"); await Promise.resolve(); }
    else if (reason === "blur") window.dispatchEvent(new Event("blur"));
    else pointer(reason, main, 200, 160);
    expect(capture).toBeNull();
    expect(document.querySelector(".galaxy-trail-layer")).toBeNull();
    expect(document.body.classList.contains("galaxy-dragging")).toBe(false);
    now += 10000;
    if (reason === "hidden") { hidden = false; document.dispatchEvent(new Event("visibilitychange")); }
    if (reason === "logout") { document.body.classList.replace("auth-login", "auth-ready"); await Promise.resolve(); }
    if (reason === "blur") window.dispatchEvent(new Event("focus"));
    frame();
    expect(camera()).toMatchObject({ x: 100, y: 60, vx: 0, vy: 0 });
    expect(renderer.draw.mock.lastCall![7].every((p: { x: number; y: number }) => p.x === 0 && p.y === 0)).toBe(true);
    expect(document.body.classList.contains("galaxy-explored")).toBe(true);
  });

  it("rebuilds on theme and viewport changes, retains composition and caps framebuffer pixels", () => {
    initialize(); drag(); pointer("pointerup", main, 200, 160);
    document.documentElement.style.setProperty("--galaxy-light", "1");
    particles.refreshParticlePalette(); frame();
    expect(renderer.create.mock.lastCall![4]).toBe(true);
    expect(camera()).toMatchObject({ x: 100, y: 60 });
    const count = renderer.create.mock.calls.length;
    particles.refreshParticlePalette(); frame();
    expect(renderer.create).toHaveBeenCalledTimes(count);
    vi.spyOn(window, "innerWidth", "get").mockReturnValue(7680);
    vi.spyOn(window, "innerHeight", "get").mockReturnValue(4320);
    window.dispatchEvent(new Event("resize")); frame();
    expect(camera().x).toBeCloseTo(100 * 7680 / 1440);
    const canvas = document.querySelector("canvas")!;
    expect(canvas.width * canvas.height).toBeLessThanOrEqual(4_000_000);
    expect(renderer.create.mock.lastCall!.slice(1, 3)).toEqual([7680, 4320]);
  });

  it("limits drawing to 30fps and disposes all callbacks without duplicating initialization", () => {
    initialize(); particles.initParticleField();
    expect(renderer.create).toHaveBeenCalledTimes(1);
    pointer("pointermove", main, 300, 200, { buttons: 0 });
    frame(10); frame(10); frame(10);
    expect(renderer.draw).toHaveBeenCalledTimes(1);
    frame(4); expect(renderer.draw).toHaveBeenCalledTimes(2);
    particles.disposeParticleField();
    expect(callbacks.size).toBe(0);
    expect(mediaListeners.size).toBe(0);
    pointer("pointerdown", main, 100, 100); pointer("pointermove", main, 200, 100);
    expect(capture).toBeNull();
  });

  it("keeps the CSS fallback without installing interactions when Canvas is unavailable", () => {
    vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockReturnValue(null);
    particles.initParticleField();
    expect(renderer.create).not.toHaveBeenCalled();
    expect(callbacks.size).toBe(0);
    expect(document.body.classList.contains("galaxy-interactive")).toBe(false);
  });
});
