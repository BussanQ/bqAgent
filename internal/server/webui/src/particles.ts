import { byId } from "./dom";
import type { Particle, ParticlePulse, ParticleTrailPoint } from "./types";

const particleCanvas = byId<HTMLCanvasElement>("particle-field");

var PARTICLE_MAX_COUNT = 80;
  var PARTICLE_TARGET_FPS = 30;
  var PARTICLE_FRAME_INTERVAL = 1000 / PARTICLE_TARGET_FPS;
  var PARTICLE_LINK_DISTANCE = 100;
  var PARTICLE_MOUSE_RADIUS = 140;
  var PARTICLE_TRAIL_MAX_POINTS = 8;
  var PARTICLE_TRAIL_LIFETIME = 360;
  var PARTICLE_TRAIL_MIN_DISTANCE = 16;
  var PARTICLE_TRAIL_SAMPLE_INTERVAL = 40;
  var PARTICLE_PULSE_MAX_COUNT = 3;
  var PARTICLE_PULSE_LIFETIME = 520;
  var PARTICLE_PULSE_MAX_RADIUS = 96;
  var particleContext: CanvasRenderingContext2D | null = null;
  var particles: Particle[] = [];
  var particleBuckets: Record<string, number[]> = Object.create(null) as Record<string, number[]>;
  var particleTrailPoints: ParticleTrailPoint[] = [];
  var particlePulses: ParticlePulse[] = [];
  var particleTrailLast = { x: 0, y: 0, time: 0 };
  var particleWidth = 0;
  var particleHeight = 0;
  var particleDpr = 1;
  var particleFrame = 0;
  var particleResizeFrame = 0;
  var particleLastFrameTime = 0;
  var particleRunning = false;
  var particlePalette = { dot: "#dff7ff", link: "91, 199, 255", staticDot: "rgba(53, 184, 255, .20)", strength: 1 };
  var particleMouse = { x: 0, y: 0, active: false };
  var finePointerQuery = window.matchMedia ? window.matchMedia("(hover: hover) and (pointer: fine)") : null;
  var reducedMotionQuery = window.matchMedia ? window.matchMedia("(prefers-reduced-motion: reduce)") : null;

  function listenMediaQuery(query: MediaQueryList | null, handler: (event: MediaQueryListEvent) => void): void {
    if (!query) return;
    if (typeof query.addEventListener === "function") query.addEventListener("change", handler);
    else if (typeof query.addListener === "function") query.addListener(handler);
  }

  function randomBetween(min: number, max: number): number {
    return min + Math.random() * (max - min);
  }

  export function refreshParticlePalette(): void {
    if (!particleCanvas) return;
    var styles = getComputedStyle(document.documentElement);
    particlePalette.dot = styles.getPropertyValue("--particle-dot").trim() || "#dff7ff";
    particlePalette.link = styles.getPropertyValue("--particle-link").trim() || "91, 199, 255";
    particlePalette.staticDot = styles.getPropertyValue("--particle-static").trim() || "rgba(53, 184, 255, .20)";
    var strength = parseFloat(styles.getPropertyValue("--particle-strength"));
    particlePalette.strength = isFinite(strength) && strength > 0 ? strength : 1;
  }

  function particleAlpha(base: number): number {
    return Math.min(1, base * particlePalette.strength);
  }

  function targetParticleCount(): number {
    return Math.min(PARTICLE_MAX_COUNT, Math.max(36, Math.min(72,
      Math.round((particleWidth * particleHeight) / 12000))));
  }

  function createParticle(): Particle {
    var angle = Math.random() * Math.PI * 2;
    var speed = randomBetween(7, 18);
    var vx = Math.cos(angle) * speed;
    var vy = Math.sin(angle) * speed;
    return {
      x: Math.random() * particleWidth,
      y: Math.random() * particleHeight,
      vx: vx,
      vy: vy,
      baseVx: vx,
      baseVy: vy,
      radius: randomBetween(1, 1.8)
    };
  }

  function syncParticleCount(): void {
    var target = targetParticleCount();
    while (particles.length < target) particles.push(createParticle());
    if (particles.length > target) particles.length = target;
  }

  function resizeParticleField(): void {
    if (!particleCanvas || !particleContext) return;
    var previousWidth = particleWidth || window.innerWidth;
    var previousHeight = particleHeight || window.innerHeight;
    particleWidth = Math.max(1, window.innerWidth);
    particleHeight = Math.max(1, window.innerHeight);
    particleDpr = Math.min(window.devicePixelRatio || 1, 1.5);
    particleCanvas.width = Math.max(1, Math.floor(particleWidth * particleDpr));
    particleCanvas.height = Math.max(1, Math.floor(particleHeight * particleDpr));
    particleContext!.setTransform(particleDpr, 0, 0, particleDpr, 0, 0);
    particles.forEach(function (particle) {
      particle.x = particle.x / previousWidth * particleWidth;
      particle.y = particle.y / previousHeight * particleHeight;
    });
    syncParticleCount();
    resetParticleInteractions();
    drawParticleField(0, false);
  }

  function buildParticleBuckets(): void {
    particleBuckets = Object.create(null);
    particles.forEach(function (particle, index) {
      var cellX = Math.floor(particle.x / PARTICLE_LINK_DISTANCE);
      var cellY = Math.floor(particle.y / PARTICLE_LINK_DISTANCE);
      var key = cellX + ":" + cellY;
      if (!particleBuckets[key]) particleBuckets[key] = [];
      particleBuckets[key].push(index);
    });
  }

  function updateParticle(particle: Particle, dt: number): void {
    particle.vx += (particle.baseVx - particle.vx) * Math.min(1, dt * .7);
    particle.vy += (particle.baseVy - particle.vy) * Math.min(1, dt * .7);
    if (particleMouse.active) {
      var dx = particleMouse.x - particle.x;
      var dy = particleMouse.y - particle.y;
      var distanceSquared = dx * dx + dy * dy;
      if (distanceSquared > 1 && distanceSquared < PARTICLE_MOUSE_RADIUS * PARTICLE_MOUSE_RADIUS) {
        var distance = Math.sqrt(distanceSquared);
        var force = (1 - distance / PARTICLE_MOUSE_RADIUS) * 18;
        particle.vx += dx / distance * force * dt;
        particle.vy += dy / distance * force * dt;
      }
    }
    var speed = Math.sqrt(particle.vx * particle.vx + particle.vy * particle.vy);
    if (speed > 28) {
      particle.vx = particle.vx / speed * 28;
      particle.vy = particle.vy / speed * 28;
    }
    particle.x += particle.vx * dt;
    particle.y += particle.vy * dt;
    if (particle.x < -2) particle.x = particleWidth + 2;
    else if (particle.x > particleWidth + 2) particle.x = -2;
    if (particle.y < -2) particle.y = particleHeight + 2;
    else if (particle.y > particleHeight + 2) particle.y = -2;
  }

  function drawParticleLinks(): void {
    var maxDistanceSquared = PARTICLE_LINK_DISTANCE * PARTICLE_LINK_DISTANCE;
    particles.forEach(function (particle, index) {
      var cellX = Math.floor(particle.x / PARTICLE_LINK_DISTANCE);
      var cellY = Math.floor(particle.y / PARTICLE_LINK_DISTANCE);
      for (var offsetY = -1; offsetY <= 1; offsetY++) {
        for (var offsetX = -1; offsetX <= 1; offsetX++) {
          var bucket = particleBuckets[(cellX + offsetX) + ":" + (cellY + offsetY)] || [];
          bucket.forEach(function (otherIndex) {
            if (otherIndex <= index) return;
            var other = particles[otherIndex];
            var dx = other.x - particle.x;
            var dy = other.y - particle.y;
            var distanceSquared = dx * dx + dy * dy;
            if (distanceSquared >= maxDistanceSquared) return;
            var alpha = particleAlpha((1 - distanceSquared / maxDistanceSquared) * .20);
            particleContext!.beginPath();
            particleContext!.moveTo(particle.x, particle.y);
            particleContext!.lineTo(other.x, other.y);
            particleContext!.strokeStyle = "rgba(" + particlePalette.link + "," + alpha + ")";
            particleContext!.lineWidth = .65;
            particleContext!.stroke();
          });
        }
      }
    });
  }

  function resetParticleInteractions(): void {
    particleTrailPoints.length = 0;
    particlePulses.length = 0;
    particleTrailLast.x = 0;
    particleTrailLast.y = 0;
    particleTrailLast.time = 0;
  }

  function addParticleTrailPoint(x: number, y: number, now: number): void {
    var dx = x - particleTrailLast.x;
    var dy = y - particleTrailLast.y;
    var elapsed = now - particleTrailLast.time;
    if (particleTrailLast.time && dx * dx + dy * dy < PARTICLE_TRAIL_MIN_DISTANCE * PARTICLE_TRAIL_MIN_DISTANCE &&
        elapsed < PARTICLE_TRAIL_SAMPLE_INTERVAL) return;
    particleTrailPoints.push({ x: x, y: y, createdAt: now });
    if (particleTrailPoints.length > PARTICLE_TRAIL_MAX_POINTS) particleTrailPoints.shift();
    particleTrailLast.x = x;
    particleTrailLast.y = y;
    particleTrailLast.time = now;
  }

  function addParticlePulse(x: number, y: number, now: number): void {
    particlePulses.push({ x: x, y: y, createdAt: now });
    if (particlePulses.length > PARTICLE_PULSE_MAX_COUNT) particlePulses.shift();
  }

  function drawParticleInteractionFeedback(now: number): void {
    particleTrailPoints = particleTrailPoints.filter(function (point) {
      return now - point.createdAt < PARTICLE_TRAIL_LIFETIME;
    });
    for (var index = 1; index < particleTrailPoints.length; index++) {
      var previous = particleTrailPoints[index - 1];
      var current = particleTrailPoints[index];
      var progress = Math.min(1, (now - previous.createdAt) / PARTICLE_TRAIL_LIFETIME);
      var alpha = particleAlpha(Math.pow(1 - progress, 2) * .34);
      particleContext!.beginPath();
      particleContext!.moveTo(previous.x, previous.y);
      particleContext!.lineTo(current.x, current.y);
      particleContext!.strokeStyle = "rgba(" + particlePalette.link + "," + alpha + ")";
      particleContext!.lineWidth = .7 + (1 - progress) * .8;
      particleContext!.stroke();
    }

    particlePulses = particlePulses.filter(function (pulse) {
      return now - pulse.createdAt < PARTICLE_PULSE_LIFETIME;
    });
    particlePulses.forEach(function (pulse) {
      var progress = Math.min(1, (now - pulse.createdAt) / PARTICLE_PULSE_LIFETIME);
      var eased = 1 - Math.pow(1 - progress, 3);
      var alpha = particleAlpha(Math.pow(1 - progress, 2) * .48);
      var radius = 6 + PARTICLE_PULSE_MAX_RADIUS * eased;
      particleContext!.beginPath();
      particleContext!.arc(pulse.x, pulse.y, radius, 0, Math.PI * 2);
      particleContext!.strokeStyle = "rgba(" + particlePalette.link + "," + alpha + ")";
      particleContext!.lineWidth = 1 + (1 - progress) * 1.4;
      particleContext!.stroke();
      if (progress < .2) {
        particleContext!.beginPath();
        particleContext!.arc(pulse.x, pulse.y, 2.6 + progress * 16, 0, Math.PI * 2);
        particleContext!.fillStyle = "rgba(" + particlePalette.link + "," + (1 - progress / .2) * .7 + ")";
        particleContext!.fill();
      }
    });
  }

export function drawParticleField(dt: number, animate: boolean): void {
    if (!particleContext || !particleWidth || !particleHeight) return;
    particleContext!.clearRect(0, 0, particleWidth, particleHeight);
    if (animate) particles.forEach(function (particle) { updateParticle(particle, dt); });
    if (animate) {
      buildParticleBuckets();
      drawParticleLinks();
      drawParticleInteractionFeedback(performance.now());
      particleContext!.fillStyle = particlePalette.dot;
    } else {
      particleContext!.fillStyle = particlePalette.staticDot;
    }
    particles.forEach(function (particle) {
      particleContext!.beginPath();
      particleContext!.arc(particle.x, particle.y, particle.radius, 0, Math.PI * 2);
      particleContext!.fill();
    });
  }

export function redrawParticleField(): void {
  if (particleContext) drawParticleField(0, particleRunning);
}

  function particleAnimationLoop(timestamp: number): void {
    if (!particleRunning) return;
    particleFrame = requestAnimationFrame(particleAnimationLoop);
    if (particleLastFrameTime && timestamp - particleLastFrameTime < PARTICLE_FRAME_INTERVAL) return;
    var dt = particleLastFrameTime ? Math.min((timestamp - particleLastFrameTime) / 1000, .05) : 1 / PARTICLE_TARGET_FPS;
    particleLastFrameTime = timestamp;
    drawParticleField(dt, true);
  }

  function startParticleField(): void {
    if (particleRunning || !particleContext) return;
    particleRunning = true;
    particleLastFrameTime = 0;
    particleFrame = requestAnimationFrame(particleAnimationLoop);
  }

  export function stopParticleField(clear: boolean): void {
    particleRunning = false;
    particleLastFrameTime = 0;
    if (particleFrame) cancelAnimationFrame(particleFrame);
    particleFrame = 0;
    particleMouse.active = false;
    resetParticleInteractions();
    if (clear && particleContext) particleContext!.clearRect(0, 0, particleWidth, particleHeight);
  }

  function canAnimateParticleField(): boolean {
    return !!particleContext && !!finePointerQuery && finePointerQuery.matches &&
      !(reducedMotionQuery && reducedMotionQuery.matches) && window.innerWidth > 720 && !document.hidden;
  }

  export function refreshParticleCapability(): void {
    if (!particleContext) return;
    if (canAnimateParticleField()) {
      startParticleField();
      return;
    }
    stopParticleField(true);
    if (!document.hidden && window.innerWidth > 720 &&
        reducedMotionQuery && reducedMotionQuery.matches) drawParticleField(0, false);
  }

  function scheduleParticleResize(): void {
    if (particleResizeFrame) cancelAnimationFrame(particleResizeFrame);
    particleResizeFrame = requestAnimationFrame(function () {
      particleResizeFrame = 0;
      resizeParticleField();
      refreshParticleCapability();
    });
  }

  function handleParticlePointerMove(event: PointerEvent): void {
    if (!particleRunning || event.pointerType !== "mouse") return;
    particleMouse.x = event.clientX;
    particleMouse.y = event.clientY;
    particleMouse.active = true;
    addParticleTrailPoint(event.clientX, event.clientY, performance.now());
  }

  function handleParticlePointerDown(event: PointerEvent): void {
    if (!particleRunning || event.pointerType !== "mouse" || event.button !== 0) return;
    addParticlePulse(event.clientX, event.clientY, performance.now());
  }

  function clearParticlePointer(): void {
    particleMouse.active = false;
    particleTrailLast.time = 0;
  }

  export function initParticleField(): void {
    if (!particleCanvas || typeof particleCanvas.getContext !== "function") return;
    particleContext = particleCanvas.getContext("2d");
    if (!particleContext) return;
    refreshParticlePalette();
    resizeParticleField();
    window.addEventListener("pointermove", handleParticlePointerMove, { passive: true });
    window.addEventListener("pointerdown", handleParticlePointerDown, { passive: true });
    window.addEventListener("pointerout", function (event) {
      if (!event.relatedTarget) clearParticlePointer();
    });
    window.addEventListener("blur", clearParticlePointer);
    window.addEventListener("resize", scheduleParticleResize, { passive: true });
    document.addEventListener("visibilitychange", refreshParticleCapability);
    listenMediaQuery(finePointerQuery, refreshParticleCapability);
    listenMediaQuery(reducedMotionQuery, refreshParticleCapability);
    refreshParticleCapability();
  }
