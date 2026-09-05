import { createClickFeedback } from "./click-feedback";
import { advanceAttraction, createPointerTrail, foregroundPosition } from "./pointer-feedback";
import {
  advanceInertia, wrapCamera, createGalaxyScene, FRAME_PIXEL_BUDGET,
  isGalaxyDragSurface, moveGalaxyDrag, returnCamera, RETURN_DURATION,
} from "./galaxy";
import type { Camera, GalaxyDrag, GalaxyScene, Point } from "./galaxy";
import { createGalaxyLayers, renderGalaxy } from "./galaxy-renderer";
import type { GalaxyLayers } from "./galaxy-renderer";

const FRAME_INTERVAL = 1000 / 30;
let canvas: HTMLCanvasElement | null = null;
let main: HTMLElement | null = null;
let context: CanvasRenderingContext2D | null = null;
let scene: GalaxyScene | null = null;
let layers: GalaxyLayers | null = null;
let width = 0;
let height = 0;
let light = false;
let camera: Camera = { x: 0, y: 0, vx: 0, vy: 0 };
let drag: GalaxyDrag | null = null;
let returning: { origin: Point; elapsed: number } | null = null;
let elapsed = 0;
let frame = 0;
let lastFrame: number | null = null;
let motionPending = false;
let dirty = false;
let resizePending = false;
let focused = true;
let finePointer: MediaQueryList | null = null;
let reducedMotion: MediaQueryList | null = null;
let cleanup: (() => void) | null = null;
let clickFeedback: ReturnType<typeof createClickFeedback> | null = null;
let pointerTrail: ReturnType<typeof createPointerTrail> | null = null;
let pointerPosition: Point | null = null;
let attraction: Point[] = [];

function clearPointerFeedback(): void {
  pointerPosition = null;
  attraction = [];
  pointerTrail?.clear();
}

function visible(): boolean {
  return !document.hidden && document.body.classList.contains("auth-ready");
}
function interactive(): boolean {
  return !!context && !!layers && visible() && focused && !!finePointer?.matches && window.innerWidth > 720;
}
function animated(): boolean { return interactive() && !reducedMotion?.matches; }

function draw(): void {
  if (context && scene && layers && visible()) renderGalaxy(context, layers, scene, width, height, camera, elapsed, attraction);
}

function rebuild(): void {
  if (!canvas || !context || !scene) return;
  const previousWidth = width || window.innerWidth;
  const previousHeight = height || window.innerHeight;
  width = Math.max(1, window.innerWidth);
  height = Math.max(1, window.innerHeight);
  camera = wrapCamera({ x: camera.x * width / previousWidth, y: camera.y * height / previousHeight, vx: 0, vy: 0 }, width, height);
  const dpr = Math.min(1.5, window.devicePixelRatio || 1, Math.sqrt(FRAME_PIXEL_BUDGET / (width * height)));
  canvas.width = Math.max(1, Math.floor(width * dpr));
  canvas.height = Math.max(1, Math.floor(height * dpr));
  context.setTransform(canvas.width / width, 0, 0, canvas.height / height, 0, 0);
  const rect = main?.getBoundingClientRect();
  const center = { x: rect?.width ? rect.left + rect.width * .65 : width * .62, y: rect?.height ? rect.top + rect.height * .27 : height * .32 };
  // Release the previous backing stores before allocating the new pair.
  if (layers) { layers.far.canvas.width = 1; layers.galaxy.canvas.width = 1; }
  layers = createGalaxyLayers(scene, width, height, window.devicePixelRatio || 1, light, center);
}

function schedule(): void {
  if (!frame && visible() && context) frame = requestAnimationFrame(tick);
}
function requestDraw(): void { dirty = true; schedule(); }

function tick(timestamp: number): void {
  frame = 0;
  if (!visible()) return;
  if (lastFrame !== null && timestamp - lastFrame < FRAME_INTERVAL - .1) { schedule(); return; }
  // Keep the frame-rate limit across idle periods without integrating the idle time.
  const dt = !motionPending || lastFrame === null ? FRAME_INTERVAL / 1000 : Math.min(.05, (timestamp - lastFrame) / 1000);
  lastFrame = timestamp;
  motionPending = false;
  if (resizePending) { resizePending = false; rebuild(); dirty = true; }
  if (animated()) {
    const previousCamera = camera;
    if (returning) {
      returning.elapsed += dt * 1000;
      camera = returnCamera(returning.origin, returning.elapsed);
      if (returning.elapsed >= RETURN_DURATION) returning = null;
    } else if (!drag) camera = advanceInertia(camera, dt, width, height);
    if (camera.x !== previousCamera.x || camera.y !== previousCamera.y) dirty = true;
    let attractionMoving = false;
    if (scene) attraction = scene.near.map((star, index) => {
      const previous = attraction[index] ?? { x: 0, y: 0 };
      const next = advanceAttraction(previous, foregroundPosition(star, camera, width, height), pointerPosition, dt);
      if (next.x !== previous.x || next.y !== previous.y) attractionMoving = true;
      return next;
    });
    dirty ||= attractionMoving;
    motionPending = !!returning || (!drag && (camera.vx !== 0 || camera.vy !== 0)) || attractionMoving;
    if (dirty) elapsed += dt;
  }
  if (dirty) draw();
  dirty = false;
  if (motionPending) schedule();
}

function endDrag(inertia: boolean): void {
  const previous = drag;
  drag = null;
  if (!inertia || !animated() || !previous?.active || performance.now() - previous.lastTime > 80) {
    camera.vx = 0;
    camera.vy = 0;
  }
  document.body.classList.remove("galaxy-dragging");
  if (previous && main?.hasPointerCapture?.(previous.pointerId)) main.releasePointerCapture(previous.pointerId);
}

export function stopParticleField(clear: boolean): void {
  clickFeedback?.clear();
  clearPointerFeedback();
  if (frame) cancelAnimationFrame(frame);
  frame = 0;
  lastFrame = null;
  motionPending = false;
  endDrag(false);
  returning = null;
  document.body.classList.remove("galaxy-interactive");
  if (clear) context?.clearRect(0, 0, width, height);
}

export function refreshParticleCapability(): void {
  if (!context) return;
  stopParticleField(!visible());
  document.body.classList.toggle("galaxy-interactive", interactive());
  document.body.classList.toggle("galaxy-enabled", !!layers && !!finePointer?.matches && window.innerWidth > 720);
  if (visible()) requestDraw();
}

export function refreshParticlePalette(): void {
  const nextLight = getComputedStyle(document.documentElement).getPropertyValue("--galaxy-light").trim() === "1";
  if (nextLight === light) return;
  light = nextLight;
  clearPointerFeedback();
  resizePending = true;
  endDrag(false);
  returning = null;
  requestDraw();
}

export function redrawParticleField(): void { requestDraw(); }

function onPointerDown(event: PointerEvent): void {
  if (animated()) clickFeedback?.pointerDown(event);
  if (!interactive() || drag || event.pointerType !== "mouse" || event.button !== 0 || !main || !isGalaxyDragSurface(event.composedPath(), main)) return;
  const rect = main.getBoundingClientRect();
  // Keep the native scrollbar interactive, including when it targets <main>.
  if (rect.width && event.clientX >= rect.left + main.clientWidth) return;
  returning = null;
  camera.vx = camera.vy = 0;
  const point = { x: event.clientX, y: event.clientY };
  drag = { pointerId: event.pointerId, start: point, last: point, origin: { ...camera }, lastTime: performance.now(), active: false };
}

function onPointerMove(event: PointerEvent): void {
  clickFeedback?.pointerMove(event);
  if (animated() && event.pointerType === "mouse") {
    const changed = pointerPosition?.x !== event.clientX || pointerPosition?.y !== event.clientY;
    pointerPosition = { x: event.clientX, y: event.clientY };
    pointerTrail?.move(pointerPosition, performance.now());
    if (changed) requestDraw();
  }
  if (!drag || event.pointerId !== drag.pointerId) return;
  if (!(event.buttons & 1)) { endDrag(false); return; }
  const wasActive = drag.active;
  const next = moveGalaxyDrag(drag, { x: event.clientX, y: event.clientY }, performance.now(), width, height);
  if (!next) return;
  if (!wasActive) {
    main?.setPointerCapture?.(event.pointerId);
    document.body.classList.add("galaxy-dragging", "galaxy-explored");
  }
  event.preventDefault();
  camera = next;
  requestDraw();
}

function onPointerUp(event: PointerEvent): void {
  if (animated()) clickFeedback?.pointerUp(event);
  else clickFeedback?.clear();
  if (drag?.pointerId !== event.pointerId) return;
  const active = drag.active;
  endDrag(true);
  if (active) { event.preventDefault(); requestDraw(); }
}

function onCancel(event: PointerEvent): void {
  clickFeedback?.pointerCancel(event);
  clearPointerFeedback();
  if (drag?.pointerId === event.pointerId) { endDrag(false); returning = null; }
  requestDraw();
}

function onDoubleClick(event: MouseEvent): void {
  if (!interactive() || event.button !== 0 || !main || !isGalaxyDragSurface(event.composedPath(), main)) return;
  event.preventDefault();
  endDrag(false);
  if (reducedMotion?.matches) camera = returnCamera(camera, RETURN_DURATION);
  else returning = { origin: { ...camera }, elapsed: 0 };
  requestDraw();
}

/** Idempotent lifecycle also allows focused unit tests to release every listener. */
export function disposeParticleField(): void {
  stopParticleField(true);
  cleanup?.();
  cleanup = null;
  clickFeedback = null;
  pointerTrail = null;
  document.body.classList.remove("galaxy-enabled");
  if (layers) { layers.far.canvas.width = 1; layers.galaxy.canvas.width = 1; }
  layers = null;
  scene = null;
  context = null;
  canvas = null;
  main = null;
  width = height = elapsed = 0;
  camera = { x: 0, y: 0, vx: 0, vy: 0 };
  resizePending = dirty = false;
}

export function initParticleField(): void {
  if (cleanup) return;
  canvas = document.querySelector<HTMLCanvasElement>("#particle-field");
  main = document.querySelector<HTMLElement>("#main");
  if (!canvas || !main) return;
  try { context = canvas.getContext("2d"); } catch { context = null; }
  if (!context) return;
  finePointer = window.matchMedia?.("(hover: hover) and (pointer: fine)") ?? null;
  reducedMotion = window.matchMedia?.("(prefers-reduced-motion: reduce)") ?? null;
  focused = true;
  light = getComputedStyle(document.documentElement).getPropertyValue("--galaxy-light").trim() === "1";
  clickFeedback = createClickFeedback();
  pointerTrail = createPointerTrail();
  scene = createGalaxyScene();
  rebuild();
  const abort = new AbortController();
  const options = { signal: abort.signal };
  window.addEventListener("pointerdown", onPointerDown, options);
  window.addEventListener("pointermove", onPointerMove, options);
  window.addEventListener("pointerup", onPointerUp, options);
  window.addEventListener("pointercancel", onCancel, options);
  window.addEventListener("pointerout", event => {
    if (event.pointerType === "mouse" && !event.relatedTarget) {
      pointerPosition = null;
      pointerTrail?.clear();
      requestDraw();
    }
  }, options);
  main.addEventListener("lostpointercapture", onCancel, options);
  main.addEventListener("dblclick", onDoubleClick, options);
  window.addEventListener("blur", () => { focused = false; refreshParticleCapability(); }, options);
  window.addEventListener("focus", () => { focused = true; refreshParticleCapability(); }, options);
  window.addEventListener("resize", () => {
    endDrag(false);
    returning = null;
    resizePending = true;
    refreshParticleCapability();
  }, options);
  document.addEventListener("visibilitychange", refreshParticleCapability, options);
  const mediaCleanup: (() => void)[] = [];
  for (const query of [finePointer, reducedMotion]) {
    if (!query) continue;
    if (query.addEventListener) {
      query.addEventListener("change", refreshParticleCapability);
      mediaCleanup.push(() => query.removeEventListener("change", refreshParticleCapability));
    } else {
      query.addListener(refreshParticleCapability);
      mediaCleanup.push(() => query.removeListener(refreshParticleCapability));
    }
  }
  let wasReady = document.body.classList.contains("auth-ready");
  const authObserver = new MutationObserver(() => {
    const ready = document.body.classList.contains("auth-ready");
    if (wasReady === ready) return;
    wasReady = ready;
    refreshParticleCapability();
  });
  authObserver.observe(document.body, { attributes: true, attributeFilter: ["class"] });
  cleanup = () => { abort.abort(); authObserver.disconnect(); mediaCleanup.forEach(remove => remove()); };
  refreshParticleCapability();
}
