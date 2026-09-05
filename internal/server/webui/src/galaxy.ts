/** Scene coordinates are normalized so resizing never reshuffles the universe. */
export interface Point { x: number; y: number }
export interface Star extends Point { radius: number; alpha: number; phase: number; warm: boolean }
export interface Cloud extends Point { radius: number; alpha: number; dust: boolean }
export type ChartKind = "spiral" | "ring" | "binary" | "cluster";
export interface StarChart {
  name: string;
  designation: string;
  kind: ChartKind;
  column: number;
  row: number;
  angle: number;
  flatten: number;
  color: string;
  stars: Star[];
  clouds: Cloud[];
}
export interface GalaxyScene { far: Star[]; near: Star[]; charts: StarChart[] }
export interface Camera extends Point { vx: number; vy: number }
export interface GalaxyDrag {
  pointerId: number;
  start: Point;
  origin: Point;
  last: Point;
  lastTime: number;
  active: boolean;
}
export const PARALLAX = { far: .2, galaxy: .55, near: 1 } as const;
export const CACHE_PIXEL_BUDGET = 6_000_000;
export const FRAME_PIXEL_BUDGET = 4_000_000;
export const DRAG_THRESHOLD = 4;
export const RETURN_DURATION = 450;

export function seededRandom(seed = 41729): () => number {
  return () => {
    seed = (Math.imul(seed, 1664525) + 1013904223) >>> 0;
    return seed / 4294967296;
  };
}

export function createGalaxyScene(seed = 41729): GalaxyScene {
  const random = seededRandom(seed);
  const normal = () => (random() + random() + random() + random() - 2) * .5;
  const star = (x: number, y: number, radius: number): Star => ({
    x, y, radius, alpha: .25 + random() * .65, phase: random() * Math.PI * 2, warm: random() < .16,
  });
  const far = Array.from({ length: 500 }, () => star(random(), random(), .25 + random() * .65));
  const near = Array.from({ length: 140 }, () => star(random(), random(), .6 + random() * 1.05));
  const definitions = [
    { name: "ORION", designation: "SECTOR 07 / SPIRAL", kind: "spiral", column: 0, row: 0, angle: .24, flatten: .32, color: "105,185,244" },
    { name: "LYRA", designation: "SECTOR 12 / ANNULAR", kind: "ring", column: 1, row: 0, angle: -.35, flatten: .62, color: "154,148,244" },
    { name: "CYGNUS", designation: "SECTOR 21 / BINARY", kind: "binary", column: 0, row: 1, angle: -.2, flatten: .58, color: "91,207,216" },
    { name: "DRACO", designation: "SECTOR 34 / CLUSTER", kind: "cluster", column: 1, row: 1, angle: .12, flatten: .83, color: "213,189,134" },
  ] as const;
  const charts = definitions.map(definition => {
    const sample = (index: number): Point => {
      let radius = Math.pow(random(), .95);
      let angle = random() * Math.PI * 2;
      if (definition.kind === "spiral") {
        if (random() < .62) angle = index % 2 * Math.PI + Math.log(1 + radius * 10) * 2.8 + normal() * .8;
      } else if (definition.kind === "ring") {
        radius = .7 + normal() * .19;
      } else if (definition.kind === "binary") {
        radius = Math.pow(random(), 1.2) * .46;
        if (random() < .65) angle = Math.floor(index / 2) % 2 * Math.PI + Math.log(1 + radius * 16) * 2.5 + normal() * .7;
        return { x: Math.cos(angle) * radius + (index % 2 ? .42 : -.42), y: Math.sin(angle) * radius + (index % 2 ? -.13 : .13) };
      } else {
        radius = Math.pow(random(), 1.5) * .7;
      }
      return { x: Math.cos(angle) * radius + normal() * .014, y: Math.sin(angle) * radius + normal() * .014 };
    };
    const stars = Array.from({ length: 9500 }, (_, index) => {
      const point = sample(index);
      return star(point.x, point.y, .2 + random() * .65);
    });
    const clouds = Array.from({ length: 140 }, (_, index) => ({
      ...sample(index), radius: .025 + random() * .06, alpha: .025 + random() * .055, dust: index % 5 === 0,
    }));
    return { ...definition, stars, clouds };
  });
  return { far, near, charts };
}

export function layerOffset(point: Point, depth: number): Point {
  return { x: point.x * depth, y: point.y * depth };
}

/** Centered modulo works for negative drags as well as repeated full revolutions. */
export function wrap(value: number, period: number): number {
  return ((value + period / 2) % period + period) % period - period / 2;
}

export function worldSize(width: number, height: number): Point {
  return { x: width * 2, y: height * 2 };
}

export function wrapCamera(camera: Camera, width: number, height: number): Camera {
  const world = worldSize(width, height);
  // .2, .55 and 1 all repeat after 20 atlas widths. Rebase at that common
  // period so no depth layer jumps, even after thousands of revolutions.
  return { ...camera, x: wrap(camera.x, world.x * 20), y: wrap(camera.y, world.y * 20) };
}

export function tiledPositions(offset: number, tileSize: number, viewportSize: number): number[] {
  const start = ((offset % tileSize) + tileSize) % tileSize;
  const positions: number[] = [];
  for (let position = start === 0 ? 0 : start - tileSize; position < viewportSize; position += tileSize) positions.push(position);
  return positions;
}

export function advanceInertia(camera: Camera, seconds: number, width: number, height: number): Camera {
  // Integrate exponential damping analytically; the travel is frame-rate independent.
  const decay = Math.exp(-12 * seconds);
  return wrapCamera({
    x: camera.x + camera.vx * (1 - decay) / 12,
    y: camera.y + camera.vy * (1 - decay) / 12,
    vx: Math.abs(camera.vx * decay) < 3 ? 0 : camera.vx * decay,
    vy: Math.abs(camera.vy * decay) < 3 ? 0 : camera.vy * decay,
  }, width, height);
}

export function moveGalaxyDrag(drag: GalaxyDrag, point: Point, now: number, width: number, height: number): Camera | null {
  if (!drag.active && Math.hypot(point.x - drag.start.x, point.y - drag.start.y) <= DRAG_THRESHOLD) return null;
  drag.active = true;
  const seconds = Math.max(.008, (now - drag.lastTime) / 1000);
  const camera = wrapCamera({
    x: drag.origin.x + point.x - drag.start.x,
    y: drag.origin.y + point.y - drag.start.y,
    vx: Math.max(-900, Math.min(900, (point.x - drag.last.x) / seconds)),
    vy: Math.max(-900, Math.min(900, (point.y - drag.last.y) / seconds)),
  }, width, height);
  drag.origin = { x: camera.x, y: camera.y };
  drag.start = point;
  drag.last = point;
  drag.lastTime = now;
  return camera;
}

export function returnCamera(origin: Point, elapsed: number): Camera {
  const remaining = Math.pow(1 - Math.max(0, Math.min(1, elapsed / RETURN_DURATION)), 3);
  return { x: origin.x * remaining, y: origin.y * remaining, vx: 0, vy: 0 };
}

export function cacheLayout(width: number, height: number, devicePixelRatio: number) {
  // One periodic star tile plus a 2 × 2 atlas of distinct charts; memory is
  // bounded regardless of how far the user travels.
  const far = { width, height };
  const world = worldSize(width, height);
  const galaxy = { width: world.x, height: world.y };
  const dpr = Math.min(1.5, devicePixelRatio || 1, Math.sqrt(CACHE_PIXEL_BUDGET / (far.width * far.height + galaxy.width * galaxy.height)));
  return { far, galaxy, dpr };
}

const BLANK_SURFACES = "#main, #thread, .empty, .prompts, .msg";
const CONTENT = "button, a, input, textarea, select, label, [contenteditable]:not([contenteditable='false']), [role], .bubble, .message-stack, .avatar, .big, .empty-mark, .galaxy-hint";

export function isGalaxyDragSurface(path: EventTarget[], main: HTMLElement): boolean {
  const elements = path.filter((item): item is Element => item instanceof Element);
  const mainIndex = elements.indexOf(main);
  if (mainIndex < 0 || !elements[0]?.matches(BLANK_SURFACES)) return false;
  // Inspect the composed path, including nodes inside Web Component shadow roots.
  return !elements.slice(0, mainIndex + 1).some(element => element.matches(CONTENT) || element.localName.includes("-"));
}
