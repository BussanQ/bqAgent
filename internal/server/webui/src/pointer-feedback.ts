import { worldSize, wrap } from "./galaxy";
import type { Point } from "./galaxy";

export const ATTRACTION_RADIUS = 160;
export const TRAIL_LIFETIME = 360;
export const MAX_TRAIL_SEGMENTS = 8;

/** Shared projection keeps hover attraction aligned with the looping foreground. */
export function foregroundPosition(star: Point, camera: Point, width: number, height: number): Point {
  const world = worldSize(width, height);
  return {
    x: wrap(star.x * world.x + wrap(camera.x, world.x) - width / 2, world.x) + width / 2,
    y: wrap(star.y * world.y + wrap(camera.y, world.y) - height / 2, world.y) + height / 2,
  };
}

/** A bounded displacement toward the pointer, easing home without changing the atlas. */
export function advanceAttraction(offset: Point, position: Point, pointer: Point | null, seconds: number): Point {
  const dx = pointer ? pointer.x - position.x : 0;
  const dy = pointer ? pointer.y - position.y : 0;
  const distance = Math.hypot(dx, dy);
  const pull = pointer ? Math.max(0, 1 - distance / ATTRACTION_RADIUS) * .75 : 0;
  const blend = 1 - Math.exp(-10 * Math.max(0, seconds));
  const target = { x: dx * pull, y: dy * pull };
  const next = { x: offset.x + (target.x - offset.x) * blend, y: offset.y + (target.y - offset.y) * blend };
  // Exponential easing never reaches its target exactly; settle below a visible pixel.
  return Math.hypot(next.x - target.x, next.y - target.y) < .05 ? target : next;
}

/** Small, fading screen-space strokes remain visible over the reading veil. */
export function createPointerTrail() {
  let previous: (Point & { time: number }) | null = null;
  let layer: HTMLDivElement | null = null;
  const segments = new Map<HTMLElement, ReturnType<typeof setTimeout>>();
  function remove(segment: HTMLElement): void {
    clearTimeout(segments.get(segment));
    segments.delete(segment);
    segment.remove();
    if (!segments.size) { layer?.remove(); layer = null; }
  }
  function clear(): void {
    previous = null;
    for (const segment of segments.keys()) remove(segment);
  }
  function move(point: Point, time: number): void {
    if (!previous || time - previous.time > TRAIL_LIFETIME) { previous = { ...point, time }; return; }
    const dx = point.x - previous.x, dy = point.y - previous.y;
    const distance = Math.hypot(dx, dy);
    if (distance < 1 || (distance < 16 && time - previous.time < 40)) return;
    while (segments.size >= MAX_TRAIL_SEGMENTS) remove(segments.keys().next().value!);
    if (!layer) {
      layer = document.createElement("div");
      layer.className = "galaxy-trail-layer";
      layer.setAttribute("aria-hidden", "true");
      document.body.append(layer);
    }
    const segment = document.createElement("span");
    segment.className = "galaxy-trail-segment";
    segment.style.left = `${previous.x}px`;
    segment.style.top = `${previous.y}px`;
    segment.style.width = `${distance}px`;
    segment.style.transform = `rotate(${Math.atan2(dy, dx)}rad)`;
    layer.append(segment);
    segments.set(segment, setTimeout(() => remove(segment), TRAIL_LIFETIME));
    previous = { ...point, time };
  }
  return { move, clear };
}
