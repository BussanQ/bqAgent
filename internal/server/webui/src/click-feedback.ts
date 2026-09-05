import { DRAG_THRESHOLD } from "./galaxy";

const LIFETIME = 520;
const MAX_PULSES = 3;

/** Viewport-space feedback stays under the pointer while the star chart moves. */
export function createClickFeedback() {
  let press: { pointerId: number; x: number; y: number } | null = null;
  let layer: HTMLDivElement | null = null;
  const pulses = new Map<HTMLElement, ReturnType<typeof setTimeout>>();

  function remove(pulse: HTMLElement): void {
    clearTimeout(pulses.get(pulse));
    pulses.delete(pulse);
    pulse.remove();
  }

  function clear(): void {
    press = null;
    for (const pulse of pulses.keys()) remove(pulse);
    layer?.remove();
    layer = null;
  }

  return {
    clear,
    pointerDown(event: PointerEvent): void {
      if (event.pointerType !== "mouse" || event.button !== 0 || press) return;
      press = { pointerId: event.pointerId, x: event.clientX, y: event.clientY };
    },
    pointerMove(event: PointerEvent): void {
      if (press?.pointerId !== event.pointerId) return;
      if (!(event.buttons & 1) || Math.hypot(event.clientX - press.x, event.clientY - press.y) > DRAG_THRESHOLD) press = null;
    },
    pointerCancel(event: PointerEvent): void {
      if (press?.pointerId === event.pointerId) press = null;
    },
    pointerUp(event: PointerEvent): void {
      if (press?.pointerId !== event.pointerId) return;
      const origin = press;
      press = null;
      if (event.pointerType !== "mouse" || event.button !== 0 || Math.hypot(event.clientX - origin.x, event.clientY - origin.y) > DRAG_THRESHOLD) return;
      if (!layer) {
        layer = document.createElement("div");
        layer.className = "galaxy-click-layer";
        layer.setAttribute("aria-hidden", "true");
        document.body.append(layer);
      }
      if (pulses.size >= MAX_PULSES) remove(pulses.keys().next().value!);
      const pulse = document.createElement("span");
      pulse.className = "galaxy-click-pulse";
      pulse.style.left = `${event.clientX}px`;
      pulse.style.top = `${event.clientY}px`;
      layer.append(pulse);
      pulses.set(pulse, setTimeout(() => {
        remove(pulse);
        if (!pulses.size) { layer?.remove(); layer = null; }
      }, LIFETIME));
    },
  };
}
