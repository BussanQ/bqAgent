import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { CACHE_PIXEL_BUDGET, createGalaxyScene } from "./galaxy";
import { createGalaxyLayers, renderGalaxy } from "./galaxy-renderer";

function mockContext() {
  const colors: string[] = [];
  const context = {
    globalAlpha: 1, fillStyle: "",
    setTransform: vi.fn(), translate: vi.fn(), rotate: vi.fn(), scale: vi.fn(), save: vi.fn(), restore: vi.fn(),
    beginPath: vi.fn(), arc: vi.fn(), ellipse: vi.fn(), stroke: vi.fn(), moveTo: vi.fn(), lineTo: vi.fn(), setLineDash: vi.fn(), fillText: vi.fn(), fill: vi.fn(), fillRect: vi.fn(), clearRect: vi.fn(), drawImage: vi.fn(),
    createRadialGradient: vi.fn(() => ({ addColorStop: (_stop: number, color: string) => colors.push(color) })),
  };
  return { context, colors, canvasContext: context as unknown as CanvasRenderingContext2D };
}
const scene = createGalaxyScene();
let caches: ReturnType<typeof mockContext>[];
beforeEach(() => {
  caches = [];
  vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockImplementation(() => {
    const cache = mockContext(); caches.push(cache); return cache.canvasContext;
  });
});
afterEach(() => vi.restoreAllMocks());

describe("galaxy cached rendering", () => {
  it("applies attraction to foreground stars while leaving cached chart positions fixed", () => {
    const smallScene = { ...scene, near: [{ x: .1, y: .1, radius: 1, alpha: .7, phase: 0, warm: false }] };
    const layers = createGalaxyLayers(smallScene, 1000, 600, 1, false, { x: 600, y: 200 })!;
    const output = mockContext();
    renderGalaxy(output.canvasContext, layers, smallScene, 1000, 600, { x: 0, y: 0 }, 0);
    const tiles = [...output.context.drawImage.mock.calls];
    expect(output.context.arc).toHaveBeenLastCalledWith(200, 120, 1, 0, Math.PI * 2);
    output.context.drawImage.mockClear();
    renderGalaxy(output.canvasContext, layers, smallScene, 1000, 600, { x: 0, y: 0 }, 0, [{ x: 20, y: -5 }]);
    expect(output.context.arc).toHaveBeenLastCalledWith(220, 115, 1, 0, Math.PI * 2);
    expect(output.context.drawImage.mock.calls).toEqual(tiles);
    expect(smallScene.near[0]).toMatchObject({ x: .1, y: .1 });
    expect(caches).toHaveLength(2);
  });

  it("renders four distinct navigation charts into a bounded pair of caches", () => {
    const layers = createGalaxyLayers(scene, 7680, 4320, 3, false, { x: 4800, y: 1500 })!;
    expect(caches).toHaveLength(2);
    expect(layers.far.canvas.width * layers.far.canvas.height + layers.galaxy.canvas.width * layers.galaxy.canvas.height).toBeLessThanOrEqual(CACHE_PIXEL_BUDGET);
    expect(caches[1].colors.some(color => color.includes("255,245,228"))).toBe(true);
    expect(caches[1].colors.some(color => color.includes("3,8,20"))).toBe(true);
    expect(caches[1].context.arc.mock.calls.length).toBeGreaterThan(26000);
    const texts = caches[1].context.fillText.mock.calls as unknown as [string, number, number][];
    for (const chart of scene.charts) expect(texts.some(call => call[0] === chart.name)).toBe(true);
    expect(caches[1].context.ellipse).toHaveBeenCalled();
    expect(caches[1].context.setLineDash).toHaveBeenCalledWith([2, 7]);
  });

  it("composites a seamless periodic atlas after many loops without rebuilding", () => {
    const width = 1440, height = 900;
    const layers = createGalaxyLayers(scene, width, height, 2, false, { x: 900, y: 300 })!;
    const output = mockContext();
    for (const x of [-100000, -1440, -.01, 0, .01, 1440, 100000]) {
      output.context.drawImage.mockClear();
      renderGalaxy(output.canvasContext, layers, scene, width, height, { x, y: x * .6 }, 0);
      const calls = output.context.drawImage.mock.calls as unknown as [HTMLCanvasElement, number, number, number, number][];
      expect(calls.length).toBeLessThanOrEqual(8);
      for (const layer of [layers.far, layers.galaxy]) {
        const tiles = calls.filter(call => call[0] === layer.canvas);
        for (const px of [0, 360, 720, 1439]) for (const py of [0, 450, 899]) {
          expect(tiles.some(([, tx, ty, tw, th]) => px >= tx && px < tx + tw && py >= ty && py < ty + th)).toBe(true);
        }
      }
      expect(output.context.globalAlpha).toBe(1);
    }
    expect(output.context.createRadialGradient).not.toHaveBeenCalled();
    expect(caches).toHaveLength(2);
  });

  it("repeats the same chart and foreground stars after a common full revolution", () => {
    const layers = createGalaxyLayers(scene, 1440, 900, 1, false, { x: 900, y: 300 })!;
    const output = mockContext();
    renderGalaxy(output.canvasContext, layers, scene, 1440, 900, { x: 0, y: 0 }, 1);
    const tiles = [...output.context.drawImage.mock.calls];
    const stars = [...output.context.arc.mock.calls];
    output.context.drawImage.mockClear(); output.context.arc.mockClear();
    renderGalaxy(output.canvasContext, layers, scene, 1440, 900, { x: 1440 * 40, y: 900 * 40 }, 1);
    expect(output.context.drawImage.mock.calls).toEqual(tiles);
    expect(output.context.arc.mock.calls).toEqual(stars);
  });

  it("uses a muted blue-gray palette in light mode", () => {
    const layers = createGalaxyLayers(scene, 1440, 900, 1, true, { x: 900, y: 300 })!;
    expect(layers.light).toBe(true);
    expect(caches[1].colors.some(color => color.includes("255,245,228"))).toBe(false);
    expect(caches[1].colors.some(color => color.includes("103,127,158"))).toBe(true);
    const output = mockContext();
    renderGalaxy(output.canvasContext, layers, scene, 1440, 900, { x: 0, y: 0 }, 0);
    expect(output.context.fillStyle).toBe("#516a88");
  });

  it("falls back cleanly if an offscreen canvas context is unavailable", () => {
    vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockReturnValue(null);
    expect(createGalaxyLayers(scene, 1440, 900, 1, false, { x: 900, y: 300 })).toBeNull();
  });
});
