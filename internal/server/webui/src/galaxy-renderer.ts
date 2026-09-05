import { cacheLayout, layerOffset, PARALLAX, tiledPositions, wrap } from "./galaxy";
import { foregroundPosition } from "./pointer-feedback";
import type { GalaxyScene, Point, Star, StarChart } from "./galaxy";

interface GalaxyLayer {
  canvas: HTMLCanvasElement;
  width: number;
  height: number;
  depth: number;
}
export interface GalaxyLayers {
  far: GalaxyLayer;
  galaxy: GalaxyLayer;
  light: boolean;
}

function glow(context: CanvasRenderingContext2D, x: number, y: number, radius: number, color: string, alpha: number): void {
  const gradient = context.createRadialGradient(x, y, 0, x, y, radius);
  gradient.addColorStop(0, `rgba(${color},${alpha})`);
  gradient.addColorStop(.3, `rgba(${color},${alpha * .45})`);
  gradient.addColorStop(1, `rgba(${color},0)`);
  context.fillStyle = gradient;
  context.fillRect(x - radius, y - radius, radius * 2, radius * 2);
}

function dot(context: CanvasRenderingContext2D, star: Star, x: number, y: number, light: boolean, alpha = star.alpha): void {
  context.globalAlpha = alpha;
  context.fillStyle = light ? "#516a88" : star.warm ? "#ffe2b9" : "#cee2ff";
  context.beginPath();
  context.arc(x, y, star.radius, 0, Math.PI * 2);
  context.fill();
  context.globalAlpha = 1;
}

/** Draw copies intersecting an edge into the opposite edge of the texture too. */
function periodicCopies(x: number, y: number, margin: number, width: number, height: number, paint: (x: number, y: number) => void): void {
  for (const px of tiledPositions(x - margin, width, width + margin * 2)) {
    for (const py of tiledPositions(y - margin, height, height + margin * 2)) {
      const cx = px + margin, cy = py + margin;
      if (cx + margin >= 0 && cx - margin <= width && cy + margin >= 0 && cy - margin <= height) paint(cx, cy);
    }
  }
}

function drawChart(context: CanvasRenderingContext2D, chart: StarChart, radius: number, light: boolean): void {
  const project = (point: Point): Point => ({
    x: point.x * radius * Math.cos(chart.angle) - point.y * radius * chart.flatten * Math.sin(chart.angle),
    y: point.x * radius * Math.sin(chart.angle) + point.y * radius * chart.flatten * Math.cos(chart.angle),
  });
  const color = light ? "92,119,155" : chart.color;
  const flattenedGlow = (point: Point, size: number, tint: string, alpha: number) => {
    const p = project(point);
    context.save(); context.translate(p.x, p.y); context.rotate(chart.angle); context.scale(1, chart.flatten);
    glow(context, 0, 0, radius * size, tint, alpha);
    context.restore();
  };
  if (chart.kind !== "ring") flattenedGlow({ x: 0, y: 0 }, 1, color, light ? .07 : .3);
  for (const cloud of chart.clouds) {
    if (cloud.dust) continue;
    const p = project(cloud);
    glow(context, p.x, p.y, cloud.radius * radius, color, cloud.alpha * (light ? .4 : 1.7));
  }
  for (const star of chart.stars) {
    const p = project(star);
    const distance = Math.hypot(star.x, star.y);
    dot(context, star, p.x, p.y, light, star.alpha * Math.max(.2, 1 - distance * .45) * (light ? .24 : .85));
  }
  for (const cloud of chart.clouds) {
    if (!cloud.dust) continue;
    const p = project(cloud);
    glow(context, p.x, p.y, cloud.radius * radius * .65, light ? "235,240,247" : "3,8,20", cloud.alpha * 2);
  }
  const cores = chart.kind === "binary" ? [{ x: -.42, y: .13 }, { x: .42, y: -.13 }] : [{ x: 0, y: 0 }];
  if (chart.kind !== "ring") {
    for (const core of cores) {
      flattenedGlow(core, chart.kind === "binary" ? .2 : .3, light ? "130,145,171" : "201,203,220", light ? .12 : .5);
      flattenedGlow(core, .11, light ? "103,127,158" : "255,235,203", light ? .16 : .85);
      flattenedGlow(core, .038, light ? "103,127,158" : "255,245,228", light ? .16 : .9);
    }
  }

  // Orbital geometry and three deliberately sparse navigation markers.
  context.lineWidth = .65;
  context.strokeStyle = `rgba(${color},${light ? .25 : .4})`;
  for (const orbit of [1.13, 1.31]) {
    context.beginPath();
    context.ellipse(0, 0, radius * orbit, radius * chart.flatten * orbit, chart.angle, 0, Math.PI * 2);
    context.stroke();
  }
  context.setLineDash([2, 7]);
  context.beginPath();
  context.ellipse(0, 0, radius * 1.02, radius * chart.flatten * 1.02, chart.angle, -.3, Math.PI * 1.3);
  context.stroke();
  context.setLineDash([]);
  const fontSize = Math.max(9, Math.min(12, radius * .03));
  for (const [index, angle] of [.5, 2.6, 4.5].entries()) {
    const p = project({ x: Math.cos(angle) * 1.13, y: Math.sin(angle) * 1.13 });
    glow(context, p.x, p.y, 12, color, light ? .15 : .4);
    context.fillStyle = light ? "#42698c" : "#d0ecff";
    context.beginPath(); context.arc(p.x, p.y, 1.8, 0, Math.PI * 2); context.fill();
    context.beginPath(); context.ellipse(p.x, p.y, 10, 3.2, chart.angle, 0, Math.PI * 2); context.stroke();
    context.beginPath(); context.moveTo(p.x, p.y - 6); context.lineTo(p.x + 19, p.y - 32); context.lineTo(p.x + 29, p.y - 32); context.stroke();
    context.font = `${fontSize}px ui-monospace, SFMono-Regular, monospace`;
    context.fillStyle = light ? "rgba(52,81,115,.78)" : "rgba(185,215,243,.8)";
    context.fillText(index === 0 ? chart.name : index === 1 ? chart.designation.split(" /")[0] : "DEEP FIELD", p.x + 34, p.y - 29);
  }
  context.font = `${fontSize - 1}px ui-monospace, SFMono-Regular, monospace`;
  context.fillStyle = light ? "rgba(52,81,115,.6)" : "rgba(134,174,215,.55)";
  context.fillText(chart.designation, -radius * .6, radius * chart.flatten + 40);
  context.font = `${fontSize + 1}px ui-monospace, SFMono-Regular, monospace`;
  context.fillStyle = light ? "rgba(52,81,115,.8)" : "rgba(167,212,246,.86)";
  context.fillText(chart.name, -radius * .8, -radius * chart.flatten * .4 - 24);
}

export function createGalaxyLayers(scene: GalaxyScene, width: number, height: number, devicePixelRatio: number, light: boolean, center: Point): GalaxyLayers | null {
  const layout = cacheLayout(width, height, devicePixelRatio);
  const makeLayer = (size: typeof layout.far, depth: number): [GalaxyLayer, CanvasRenderingContext2D] | null => {
    const canvas = document.createElement("canvas");
    canvas.width = Math.max(1, Math.floor(size.width * layout.dpr));
    canvas.height = Math.max(1, Math.floor(size.height * layout.dpr));
    const context = canvas.getContext("2d");
    if (!context) return null;
    context.setTransform(canvas.width / size.width, 0, 0, canvas.height / size.height, 0, 0);
    return [{ canvas, ...size, depth }, context];
  };
  const far = makeLayer(layout.far, PARALLAX.far);
  const galaxy = makeLayer(layout.galaxy, PARALLAX.galaxy);
  if (!far || !galaxy) return null;
  const farContext = far[1];
  periodicCopies(width * .72, height * .32, width * .5, width, height, (x, y) => {
    glow(farContext, x, y, width * .5, light ? "131,151,184" : "25,57,103", light ? .09 : .25);
  });
  for (const star of scene.far) periodicCopies(star.x * width, star.y * height, star.radius + 1, width, height,
    (x, y) => dot(farContext, star, x, y, light, star.alpha * (light ? .3 : .62)));

  const context = galaxy[1];
  const radius = Math.min(width * .34, height * .56);
  for (const chart of scene.charts) {
    const cx = center.x + chart.column * width;
    const cy = center.y + chart.row * height + (chart.kind === "ring" || chart.kind === "cluster" ? height * .1 : 0);
    const chartRadius = radius * (chart.kind === "ring" ? .85 : chart.kind === "cluster" ? .8 : 1);
    // Include the full orbit/label bounds when crossing an atlas seam.
    periodicCopies(cx, cy, radius * 1.4 + 160, layout.galaxy.width, layout.galaxy.height, (x, y) => {
      context.save(); context.translate(x, y);
      drawChart(context, chart, chartRadius, light);
      context.restore();
    });
  }
  return { far: far[0], galaxy: galaxy[0], light };
}

function viewOffset(camera: Point, depth: number, width: number, height: number): Point {
  const offset = layerOffset(camera, depth);
  return { x: wrap(offset.x, width), y: wrap(offset.y, height) };
}

export function renderGalaxy(context: CanvasRenderingContext2D, layers: GalaxyLayers, scene: GalaxyScene, width: number, height: number, camera: Point, elapsed: number, attraction: readonly Point[] = []): void {
  context.clearRect(0, 0, width, height);
  for (const layer of [layers.far, layers.galaxy]) {
    const offset = viewOffset(camera, layer.depth, layer.width, layer.height);
    for (const x of tiledPositions(offset.x, layer.width, width)) {
      for (const y of tiledPositions(offset.y, layer.height, height)) context.drawImage(layer.canvas, x, y, layer.width, layer.height);
    }
  }
  for (const [index, star] of scene.near.entries()) {
    const position = foregroundPosition(star, camera, width, height);
    const x = position.x + (attraction[index]?.x ?? 0);
    const y = position.y + (attraction[index]?.y ?? 0);
    if (x < -12 || x > width + 12 || y < -12 || y > height + 12) continue;
    const twinkle = star.radius > 1.15 ? .86 + Math.sin(elapsed * .55 + star.phase) * .14 : 1;
    const alpha = star.alpha * twinkle * (layers.light ? .45 : .9);
    if (star.radius > 1.15) {
      context.fillStyle = layers.light ? "#7792b3" : "#8cbcff";
      for (let ring = 3; ring >= 1; ring--) {
        context.globalAlpha = alpha * .018 * (4 - ring);
        context.beginPath(); context.arc(x, y, star.radius * (ring * 1.6 + 1), 0, Math.PI * 2); context.fill();
      }
    }
    dot(context, star, x, y, layers.light, alpha);
  }
  context.globalAlpha = 1;
}
