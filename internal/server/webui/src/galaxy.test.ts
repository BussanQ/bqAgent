import { describe, expect, it } from "vitest";
import { advanceInertia, cacheLayout, CACHE_PIXEL_BUDGET, wrapCamera, tiledPositions, worldSize, wrap, createGalaxyScene, isGalaxyDragSurface, layerOffset, moveGalaxyDrag, PARALLAX, returnCamera } from "./galaxy";
import type { GalaxyDrag } from "./galaxy";

describe("galaxy scene and camera", () => {
  it("creates four stable, structurally different chart sectors", () => {
    const scene = createGalaxyScene();
    expect(createGalaxyScene()).toEqual(scene);
    expect(createGalaxyScene(7).charts[0].stars[0]).not.toEqual(scene.charts[0].stars[0]);
    expect(scene.charts.map(chart => chart.name)).toEqual(["ORION", "LYRA", "CYGNUS", "DRACO"]);
    expect(new Set(scene.charts.map(chart => chart.kind)).size).toBe(4);
    expect(new Set(scene.charts.map(chart => `${chart.column}:${chart.row}`)).size).toBe(4);
    const meanRadius = (index: number) => scene.charts[index].stars.reduce((sum, star) => sum + Math.hypot(star.x, star.y), 0) / scene.charts[index].stars.length;
    expect(meanRadius(1)).toBeGreaterThan(.65);
    expect(meanRadius(3)).toBeLessThan(.35);
    expect(scene.charts[2].stars.filter(star => star.x > .2).length).toBeGreaterThan(1800);
    expect(scene.charts[2].stars.filter(star => star.x < -.2).length).toBeGreaterThan(1800);
    expect(scene.near.length).toBeLessThan(200);
    expect([...scene.charts.flatMap(chart => chart.stars), ...scene.near, ...scene.far].every(star => Number.isFinite(star.x + star.y + star.radius) && star.radius > 0 && star.alpha >= 0 && star.alpha <= 1)).toBe(true);
  });

  it("moves all three planes in the same direction with different depths", () => {
    expect(layerOffset({ x: 100, y: -40 }, PARALLAX.far)).toEqual({ x: 20, y: -8 });
    expect(layerOffset({ x: 100, y: -40 }, PARALLAX.galaxy).x).toBeCloseTo(55);
    expect(layerOffset({ x: 100, y: -40 }, PARALLAX.galaxy).y).toBe(-22);
    expect(layerOffset({ x: 100, y: -40 }, PARALLAX.near)).toEqual({ x: 100, y: -40 });
  });

  it("waits beyond four pixels before dragging and preserves the initial offset", () => {
    const drag: GalaxyDrag = { pointerId: 1, start: { x: 10, y: 10 }, origin: { x: 20, y: 30 }, last: { x: 10, y: 10 }, lastTime: 0, active: false };
    expect(moveGalaxyDrag(drag, { x: 14, y: 10 }, 10, 1000, 800)).toBeNull();
    expect(drag.active).toBe(false);
    expect(moveGalaxyDrag(drag, { x: 20, y: 5 }, 20, 1000, 800)).toMatchObject({ x: 30, y: 25 });
    expect(drag.active).toBe(true);
    expect(moveGalaxyDrag(drag, { x: 10000, y: -10000 }, 21, 1000, 800)).toEqual({ x: 10010, y: -9980, vx: 900, vy: -900 });
  });

  it("decays inertia consistently across frame rates and settles within half a second", () => {
    const initial = { x: 0, y: 0, vx: 900, vy: -300 };
    const single = advanceInertia(initial, .2, 1440, 900);
    let many = initial;
    for (let index = 0; index < 20; index++) many = advanceInertia(many, .01, 1440, 900);
    expect(many.x).toBeCloseTo(single.x, 8);
    expect(many.y).toBeCloseTo(single.y, 8);
    expect(advanceInertia(initial, .5, 1440, 900)).toMatchObject({ vx: 0, vy: 0 });
    const crossed = advanceInertia({ x: 19999, y: 0, vx: 900, vy: 0 }, .05, 1000, 800);
    expect(crossed.x).toBeLessThan(-19900);
    expect(crossed.vx).toBeGreaterThan(0);
  });

  it("returns smoothly to the origin in 450ms without overshooting", () => {
    const origin = { x: -300, y: 100 };
    expect(returnCamera(origin, 0)).toEqual({ ...origin, vx: 0, vy: 0 });
    expect(returnCamera(origin, 225)).toMatchObject({ x: -37.5, y: 12.5 });
    expect(returnCamera(origin, 450).x).toBeCloseTo(0);
    expect(returnCamera(origin, 2000).y).toBe(0);
  });

  it.each([[390, 844, 3], [1440, 900, 2], [7680, 4320, 3]])("bounds cache pixels for the complete four-sector atlas at %sx%s", (width, height, dpr) => {
    const layout = cacheLayout(width, height, dpr);
    const pixels = [layout.far, layout.galaxy].reduce((sum, layer) => sum + Math.floor(layer.width * layout.dpr) * Math.floor(layer.height * layout.dpr), 0);
    expect(pixels).toBeLessThanOrEqual(CACHE_PIXEL_BUDGET);
    expect(layout.dpr).toBeLessThanOrEqual(1.5);
    expect(layout.galaxy).toEqual({ width: width * 2, height: height * 2 });
    expect(layout.far).toEqual({ width, height });
  });
  it("rebases arbitrarily long travel without changing any layer's visible phase or velocity", () => {
    const width = 1440, height = 900;
    const world = worldSize(width, height);
    for (const laps of [-1001, -2, 0, 2, 1001]) {
      const next = wrapCamera({ x: 127 + world.x * 20 * laps, y: -59 + world.y * 20 * laps, vx: 100, vy: -20 }, width, height);
      expect(next).toEqual({ x: 127, y: -59, vx: 100, vy: -20 });
      for (const depth of Object.values(PARALLAX)) {
        expect(wrap(next.x * depth, world.x)).toBeCloseTo(wrap(127 * depth, world.x));
      }
    }
  });

  it("tiles both axes continuously for positive, negative and repeated boundary crossings", () => {
    for (const offset of [-100001, -1000, -.01, 0, .01, 999.99, 1000, 100001]) {
      const positions = tiledPositions(offset, 1000, 1440);
      expect(positions[0]).toBeLessThanOrEqual(0);
      expect(positions.at(-1)! + 1000).toBeGreaterThanOrEqual(1440);
      for (let i = 1; i < positions.length; i++) expect(positions[i] - positions[i - 1]).toBeCloseTo(1000);
      expect(positions.length).toBeLessThanOrEqual(3);
    }
    expect(tiledPositions(-1, 1000, 1000)).toEqual([-1, 999]);
    expect(tiledPositions(1, 1000, 1000)).toEqual([-999, 1]);
  });

});

describe("galaxy drag surfaces", () => {
  it("allows blank containers and message gaps while protecting text and controls", () => {
    document.body.innerHTML = '<aside id="side"></aside><main id="main"><div id="thread"><div class="empty"><p>Welcome</p><div class="prompts"><button>Run</button></div></div><div class="msg"><div class="message-stack"><div class="bubble"><p>Message</p><input><a>link</a><pre>code</pre><span contenteditable="">edit</span></div></div></div></div></main><footer><textarea></textarea></footer>';
    const main = document.querySelector<HTMLElement>("main")!;
    const path = (element: Element) => {
      const result: EventTarget[] = [];
      for (let current: Element | null = element; current; current = current.parentElement) result.push(current);
      return result;
    };
    for (const selector of ["#main", "#thread", ".empty", ".prompts", ".msg"]) expect(isGalaxyDragSurface(path(document.querySelector(selector)!), main)).toBe(true);
    for (const selector of ["aside", "button", "p", ".message-stack", ".bubble", "input", "a", "pre", "[contenteditable]", "textarea"]) expect(isGalaxyDragSurface(path(document.querySelector(selector)!), main)).toBe(false);
  });

  it("rejects Web Component internals even when they contain a blank-looking element", () => {
    document.body.innerHTML = '<main id="main"><wa-select></wa-select></main>';
    const main = document.querySelector<HTMLElement>("main")!;
    const host = main.firstElementChild!;
    const shadow = host.attachShadow({ mode: "open" });
    shadow.innerHTML = '<div class="empty">Option</div>';
    expect(isGalaxyDragSurface([shadow.firstElementChild!, shadow, host, main, document.body], main)).toBe(false);
    expect(isGalaxyDragSurface([host, main, document.body], main)).toBe(false);
  });
});
