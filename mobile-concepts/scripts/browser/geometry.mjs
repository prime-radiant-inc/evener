import { evaluate } from "./cdp.mjs";

const edges = ["bottom", "left", "right", "top"];

export function normalizeCssPixel(value, devicePixelRatio = 1) {
  const numeric = Number(value);
  const rasterized = Math.round(numeric * devicePixelRatio) / devicePixelRatio;
  return Math.abs(numeric - rasterized) <= 0.001 ? rasterized : numeric;
}

export function parseCssColor(value) {
  if (typeof value !== "string") return null;
  const normalized = value.trim().toLowerCase();
  if (normalized === "transparent") {
    return { red: 0, green: 0, blue: 0, alpha: 0 };
  }
  const srgb = normalized.match(/^color\(srgb\s+(.+)\)$/);
  if (srgb) {
    const parts = srgb[1].replace("/", " / ").trim().split(/\s+/);
    const slash = parts.indexOf("/");
    const channels = parts
      .slice(0, slash === -1 ? 3 : slash)
      .map((part) => Number.parseFloat(part) * 255);
    const alpha = slash === -1 ? 1 : Number.parseFloat(parts[slash + 1]);
    if (channels.length === 3 && [...channels, alpha].every(Number.isFinite)) {
      return { red: channels[0], green: channels[1], blue: channels[2], alpha };
    }
    return null;
  }
  const match = normalized.match(/^rgba?\((.*)\)$/);
  if (!match) return null;
  const parts = match[1]
    .replaceAll(",", " ")
    .replace("/", " / ")
    .trim()
    .split(/\s+/);
  const slash = parts.indexOf("/");
  const channels = (
    slash === -1 ? parts.slice(0, 3) : parts.slice(0, slash)
  ).map((part) =>
    part.endsWith("%")
      ? (Number.parseFloat(part) * 255) / 100
      : Number.parseFloat(part),
  );
  const alphaPart = slash === -1 ? parts[3] : parts[slash + 1];
  const alpha =
    alphaPart === undefined
      ? 1
      : alphaPart.endsWith("%")
        ? Number.parseFloat(alphaPart) / 100
        : Number.parseFloat(alphaPart);
  if (
    channels.length !== 3 ||
    [...channels, alpha].some((part) => !Number.isFinite(part))
  )
    return null;
  return { red: channels[0], green: channels[1], blue: channels[2], alpha };
}

export function compositeColor(foreground, background) {
  const alpha = foreground.alpha + background.alpha * (1 - foreground.alpha);
  if (alpha === 0) return { red: 0, green: 0, blue: 0, alpha: 0 };
  const channel = (name) =>
    (foreground[name] * foreground.alpha +
      background[name] * background.alpha * (1 - foreground.alpha)) /
    alpha;
  return {
    red: channel("red"),
    green: channel("green"),
    blue: channel("blue"),
    alpha,
  };
}

export function composePaintGroups(foregroundValue, layers) {
  let foreground = parseCssColor(foregroundValue);
  if (!foreground) return { unsupported: "unparseable-foreground" };
  let background = { red: 0, green: 0, blue: 0, alpha: 0 };
  for (const layer of layers ?? []) {
    if (layer.image && layer.image !== "none")
      return { unsupported: "background-image", layer };
    const paint = parseCssColor(layer.color);
    if (!paint) return { unsupported: "unparseable-background", layer };
    foreground = compositeColor(foreground, paint);
    background = compositeColor(background, paint);
    const opacity = Number(layer.opacity ?? 1);
    foreground = { ...foreground, alpha: foreground.alpha * opacity };
    background = { ...background, alpha: background.alpha * opacity };
  }
  if (foreground.alpha < 0.999 || background.alpha < 0.999)
    return { unsupported: "transparent-background" };
  return { foreground, background };
}

export function extractPaintStack(element, getStyle) {
  const layers = [];
  const pseudoPaint = [];
  let current = element;
  let index = 0;
  while (current) {
    const style = getStyle(current);
    const owner =
      current.id ||
      current.getAttribute?.("data-testid") ||
      current.tagName ||
      (index === 0 ? "candidate" : `ancestor-${index}`);
    layers.push({
      color: style.backgroundColor,
      image: style.backgroundImage,
      opacity: Number(style.opacity ?? 1),
      owner,
    });
    for (const pseudo of ["::before", "::after"]) {
      const pseudoStyle = getStyle(current, pseudo);
      if (
        pseudoStyle &&
        pseudoStyle.content !== "none" &&
        (pseudoStyle.backgroundImage !== "none" ||
          !["transparent", "rgba(0, 0, 0, 0)"].includes(
            pseudoStyle.backgroundColor,
          ))
      ) {
        pseudoPaint.push({
          owner,
          pseudo,
          color: pseudoStyle.backgroundColor,
          image: pseudoStyle.backgroundImage,
        });
      }
    }
    current = current.parentElement;
    index += 1;
  }
  return {
    layers,
    pseudoPaint,
    candidateOpacity: layers[0]?.opacity ?? 1,
    ancestorOpacities: layers.slice(1).map(({ opacity }) => opacity),
  };
}

export function selectFocusIndicator(before, after) {
  const paints = [];
  const outlineChanged =
    (after.outlineStyle !== before.outlineStyle ||
      after.outlineWidth !== before.outlineWidth ||
      after.outlineColor !== before.outlineColor ||
      after.outlineOffset !== before.outlineOffset) &&
    after.outlineStyle !== "none" &&
    Number.parseFloat(after.outlineWidth) > 0;
  if (outlineChanged) {
    paints.push({
      source: "outline",
      color: after.outlineColor,
      width: Number.parseFloat(after.outlineWidth),
      offset: Number.parseFloat(after.outlineOffset),
    });
  }
  if (after.boxShadow !== before.boxShadow && after.boxShadow !== "none") {
    for (const match of after.boxShadow.matchAll(/rgba?\([^)]*\)/g))
      paints.push({ source: "box-shadow", color: match[0] });
  }
  return { changed: paints.length > 0, paints };
}

export async function stabilizePagePaint(
  documentTarget,
  frame,
  snapshotTarget = (element) => {
    if (!element) return null;
    const style = element.ownerDocument?.defaultView?.getComputedStyle(element);
    const box = element.getBoundingClientRect();
    const opacity = Number(style?.opacity ?? 1);
    const rectangle = {
      left: box.left,
      top: box.top,
      right: box.right,
      bottom: box.bottom,
      width: box.width,
      height: box.height,
    };
    const intersect = (left, right) => {
      const result = {
        left: Math.max(left.left, right.left),
        top: Math.max(left.top, right.top),
        right: Math.min(left.right, right.right),
        bottom: Math.min(left.bottom, right.bottom),
      };
      result.width = Math.max(0, result.right - result.left);
      result.height = Math.max(0, result.bottom - result.top);
      return result;
    };
    const viewport = {
      left: 0,
      top: 0,
      right: element.ownerDocument?.defaultView?.innerWidth ?? 0,
      bottom: element.ownerDocument?.defaultView?.innerHeight ?? 0,
    };
    const viewportIntersection = intersect(rectangle, viewport);
    let clipIntersection = { ...rectangle };
    const clippingAncestors = [];
    const ancestorStyles = [];
    let effectiveOpacity = opacity;
    let effectiveVisible =
      style?.display !== "none" && style?.visibility !== "hidden";
    let ancestor = element.parentElement;
    while (ancestor) {
      const ancestorStyle =
        ancestor.ownerDocument?.defaultView?.getComputedStyle(ancestor);
      const ancestorOpacity = Number(ancestorStyle?.opacity ?? 1);
      effectiveOpacity *= ancestorOpacity;
      effectiveVisible =
        effectiveVisible &&
        ancestorStyle?.display !== "none" &&
        ancestorStyle?.visibility !== "hidden" &&
        ancestorOpacity > 0;
      ancestorStyles.push({
        element:
          ancestor.id ||
          ancestor.getAttribute?.("data-testid") ||
          ancestor.tagName,
        display: ancestorStyle?.display,
        visibility: ancestorStyle?.visibility,
        opacity: ancestorOpacity,
      });
      const clipsX = ancestorStyle?.overflowX !== "visible";
      const clipsY = ancestorStyle?.overflowY !== "visible";
      if (clipsX || clipsY) {
        const ancestorBox = ancestor.getBoundingClientRect();
        const bounds = {
          left: clipsX ? ancestorBox.left : -Infinity,
          right: clipsX ? ancestorBox.right : Infinity,
          top: clipsY ? ancestorBox.top : -Infinity,
          bottom: clipsY ? ancestorBox.bottom : Infinity,
        };
        clipIntersection = intersect(clipIntersection, bounds);
        clippingAncestors.push({
          element:
            ancestor.id ||
            ancestor.getAttribute?.("data-testid") ||
            ancestor.tagName,
          overflowX: ancestorStyle?.overflowX,
          overflowY: ancestorStyle?.overflowY,
          rect: {
            left: ancestorBox.left,
            top: ancestorBox.top,
            right: ancestorBox.right,
            bottom: ancestorBox.bottom,
            width: ancestorBox.width,
            height: ancestorBox.height,
          },
        });
      }
      ancestor = ancestor.parentElement;
    }
    const intersection = intersect(viewportIntersection, clipIntersection);
    return {
      element:
        element.id ||
        element.getAttribute?.("aria-label") ||
        element.getAttribute?.("data-testid") ||
        element.tagName,
      visible:
        effectiveVisible &&
        effectiveOpacity > 0 &&
        intersection.width > 0 &&
        intersection.height > 0,
      opacity,
      effectiveOpacity,
      effectiveVisible,
      ancestorStyles,
      rect: rectangle,
      viewportIntersection,
      clipIntersection,
      intersection,
      clippingAncestors,
    };
  },
) {
  const animations = documentTarget.getAnimations({ subtree: true });
  const finite = [];
  const finiteForced = [];
  const infiniteStabilized = [];
  const capabilityFailures = [];
  for (const [index, animation] of animations.entries()) {
    const target = animation.effect?.target;
    const identity =
      animation.id ||
      animation.animationName ||
      `${target?.tagName ?? "unknown"}-animation-${index}`;
    const affectedElement =
      target?.id ||
      target?.getAttribute?.("aria-label") ||
      target?.getAttribute?.("data-testid") ||
      target?.tagName ||
      "unknown";
    let stage = "get-computed-timing";
    let timing;
    let specified;
    let endTime;
    let iterations;
    try {
      timing = animation.effect?.getComputedTiming?.() ?? {};
      stage = "get-timing";
      specified = animation.effect?.getTiming?.() ?? {};
      stage = "end-time-read";
      endTime = timing.endTime;
      stage = "iterations-read";
      iterations = specified.iterations;
    } catch (error) {
      capabilityFailures.push({
        code: "animation-timing-evaluation-failed",
        identity,
        affectedElement,
        stage,
        error: error.message,
      });
      continue;
    }
    if (endTime === Infinity || iterations === Infinity) {
      stage = "pause";
      let before;
      let duration;
      let current;
      try {
        animation.pause();
        stage = "before-snapshot";
        before = snapshotTarget(target);
        stage = "duration-read";
        duration = Number(specified.duration);
        stage = "current-time-read";
        current =
          animation.currentTime === null || animation.currentTime === undefined
            ? Number.NaN
            : Number(animation.currentTime);
      } catch (error) {
        capabilityFailures.push({
          code: "infinite-animation-evaluation-failed",
          identity,
          affectedElement,
          stage,
          error: error.message,
        });
        continue;
      }
      const times = [
        ...(Number.isFinite(current) ? [current] : []),
        ...(Number.isFinite(duration) && duration > 0
          ? [0, duration * 0.25, duration * 0.5, duration * 0.75]
          : []),
      ].filter((value, position, values) => values.indexOf(value) === position);
      if (times.length === 0) {
        capabilityFailures.push({
          code: "infinite-animation-no-representative-time",
          identity,
          affectedElement,
          before,
        });
        continue;
      }
      const phases = [];
      let phaseFailure = false;
      for (const time of times) {
        try {
          stage = "phase-assignment";
          animation.currentTime = time;
          stage = "phase-frame";
          await frame();
          stage = "phase-snapshot";
          phases.push({ time, snapshot: snapshotTarget(target) });
        } catch (error) {
          capabilityFailures.push({
            code: "infinite-animation-evaluation-failed",
            identity,
            affectedElement,
            stage,
            error: error.message,
          });
          phaseFailure = true;
          break;
        }
      }
      if (phaseFailure) continue;
      const eligible = phases.filter(
        ({ snapshot }) =>
          snapshot?.visible &&
          Number(snapshot.effectiveOpacity ?? snapshot.opacity) > 0 &&
          Number(snapshot.intersection?.width) > 0 &&
          Number(snapshot.intersection?.height) > 0,
      );
      if (eligible.length === 0) {
        capabilityFailures.push({
          code: "infinite-animation-no-visible-representative",
          identity,
          affectedElement,
          before,
          sampledPhases: times,
        });
        continue;
      }
      const score = ({ snapshot }) =>
        Number(snapshot.effectiveOpacity ?? snapshot.opacity) * 1e9 +
        Number(snapshot.intersection.width) *
          Number(snapshot.intersection.height);
      const chosen = eligible.reduce(
        (best, phase) =>
          best === null || score(phase) > score(best) ? phase : best,
        null,
      );
      try {
        stage = "final-assignment";
        animation.currentTime = chosen.time;
        stage = "final-frame";
        await frame();
        stage = "final-snapshot";
        infiniteStabilized.push({
          identity,
          affectedElement,
          before,
          after: snapshotTarget(target),
          chosenTime: chosen.time,
        });
      } catch (error) {
        capabilityFailures.push({
          code: "infinite-animation-evaluation-failed",
          identity,
          affectedElement,
          stage,
          error: error.message,
        });
      }
    } else if (
      animation.playState === "paused" ||
      Number(animation.playbackRate) === 0
    ) {
      const before = snapshotTarget(target);
      const terminal = Number(endTime);
      if (!Number.isFinite(terminal)) {
        capabilityFailures.push({
          code: "finite-animation-no-terminal-time",
          identity,
          affectedElement,
          before,
        });
        continue;
      }
      try {
        animation.pause();
        animation.currentTime = terminal;
        await frame();
        finiteForced.push({
          identity,
          affectedElement,
          before,
          after: snapshotTarget(target),
          chosenTime: terminal,
        });
      } catch (error) {
        capabilityFailures.push({
          code: "finite-animation-terminal-state-failed",
          identity,
          affectedElement,
          before,
          error: error.message,
        });
      }
    } else if (animation.playState !== "finished") {
      finite.push(animation.finished.catch(() => undefined));
    }
  }
  await Promise.all(finite);
  await documentTarget.fonts?.ready;
  await frame();
  await frame();
  return {
    finiteAwaited: finite.length,
    finiteForced,
    infiniteStabilized,
    capabilityFailures,
  };
}

export function mergeStabilizationEvidence(stabilization) {
  const failures = [
    ...(stabilization.preCollection?.capabilityFailures ?? []),
    ...(stabilization.focusSteps ?? []).flatMap(
      (evidence) => evidence.capabilityFailures ?? [],
    ),
    ...(stabilization.postFocus?.capabilityFailures ?? []),
  ];
  const seen = new Set();
  return {
    stabilization,
    capabilityFailures: failures.filter((failure) => {
      const identity = JSON.stringify(failure);
      if (seen.has(identity)) return false;
      seen.add(identity);
      return true;
    }),
  };
}

function luminance(color) {
  const channel = (value) => {
    const normalized = value / 255;
    return normalized <= 0.04045
      ? normalized / 12.92
      : ((normalized + 0.055) / 1.055) ** 2.4;
  };
  return (
    0.2126 * channel(color.red) +
    0.7152 * channel(color.green) +
    0.0722 * channel(color.blue)
  );
}

function ratio(left, right) {
  const first = luminance(left);
  const second = luminance(right);
  return (Math.max(first, second) + 0.05) / (Math.min(first, second) + 0.05);
}

export function analyzeDifferentialCapture(candidate, capture) {
  const relativeLuminance = (color) => {
    const channel = (value) => {
      const normalized = value / 255;
      return normalized <= 0.04045
        ? normalized / 12.92
        : ((normalized + 0.055) / 1.055) ** 2.4;
    };
    return (
      0.2126 * channel(color.red) +
      0.7152 * channel(color.green) +
      0.0722 * channel(color.blue)
    );
  };
  const contrastRatio = (left, right) => {
    const first = relativeLuminance(left);
    const second = relativeLuminance(right);
    return (Math.max(first, second) + 0.05) / (Math.min(first, second) + 0.05);
  };
  const { images, clip, scroll = { x: 0, y: 0 } } = capture;
  const imageNames = ["original", "hidden", "black", "white"];
  const first = images?.original;
  const timing = capture.timing ?? {
    sequence: [
      "stabilized",
      "geometry",
      "original",
      "hidden",
      "black-probe",
      "white-probe",
    ],
    eventDriven: true,
  };
  timing.geometryCollectedAt ??= candidate.raw?.geometryCollectedAt;
  const emptyMask = { pixelCount: 0, bounds: null, runs: [] };
  const geometry = candidate.geometry ?? candidate.raw?.geometry;
  const candidateDocument = geometry
    ? {
        left: geometry.left + scroll.x,
        top: geometry.top + scroll.y,
        right: geometry.right + scroll.x,
        bottom: geometry.bottom + scroll.y,
      }
    : null;
  const mapping = {
    coordinateSystem: capture.coordinateSystem,
    cssGeometry: geometry,
    candidateDocument,
    screenshotGeometry: first
      ? { width: first.width, height: first.height }
      : null,
    devicePixelRatio: capture.devicePixelRatio,
    scroll,
    clip,
    scaleX: first && clip ? first.width / clip.width : null,
    scaleY: first && clip ? first.height / clip.height : null,
  };
  const workState = capture.work ?? { limit: 25_000_000, operations: 0 };
  workState.limit = Number(workState.limit ?? 25_000_000);
  workState.operations = Number(workState.operations ?? 0);
  const workStart = workState.operations;
  const workSnapshot = () => ({
    limit: workState.limit,
    operations: workState.operations - workStart,
    caseOperations: workState.operations,
  });
  const unresolved = (reason) => ({
    unresolved: reason,
    mapping,
    timing,
    mask: emptyMask,
    surfacePalette: { behind: [], inside: [], outside: [] },
    surfaceVerdicts: {},
    work: workSnapshot(),
  });
  const reserveWork = (stage, requested) => {
    if (
      !Number.isSafeInteger(requested) ||
      requested < 0 ||
      workState.operations + requested > workState.limit
    ) {
      const capabilityFailure = {
        code: "contrast-analyzer-work-budget-exceeded",
        candidateId: candidate.id,
        kind: candidate.kind,
        stage,
        operations: workState.operations,
        requested,
        limit: workState.limit,
      };
      return {
        ...unresolved("analyzer-work-budget-exceeded"),
        capabilityFailure,
      };
    }
    workState.operations += requested;
    return null;
  };
  if (!geometry || !clip || !first)
    return unresolved("missing-capture-geometry");
  if (
    imageNames.some(
      (name) =>
        !images[name] ||
        images[name].width !== first.width ||
        images[name].height !== first.height ||
        images[name].data?.length !== first.width * first.height * 4,
    )
  )
    return unresolved("inconsistent-differential-capture");
  const clipRight = clip.x + clip.width;
  const clipBottom = clip.y + clip.height;
  if (
    candidate.kind !== "focus" &&
    (candidateDocument.right <= clip.x ||
      candidateDocument.left >= clipRight ||
      candidateDocument.bottom <= clip.y ||
      candidateDocument.top >= clipBottom)
  )
    return unresolved("candidate-outside-capture");

  const scaleX = mapping.scaleX;
  const scaleY = mapping.scaleY;
  const read = (image, x, y) => {
    const offset = (y * image.width + x) * 4;
    return {
      red: image.data[offset],
      green: image.data[offset + 1],
      blue: image.data[offset + 2],
      alpha: image.data[offset + 3] / 255,
    };
  };
  const documentPoint = (x, y) => ({
    x: clip.x + (x + 0.5) / scaleX,
    y: clip.y + (y + 0.5) / scaleY,
  });
  const inCandidate = (point) =>
    point.x >= candidateDocument.left &&
    point.x < candidateDocument.right &&
    point.y >= candidateDocument.top &&
    point.y < candidateDocument.bottom;
  const imageBounds =
    candidate.kind === "focus"
      ? { left: 0, top: 0, right: first.width, bottom: first.height }
      : {
          left: Math.max(
            0,
            Math.ceil((candidateDocument.left - clip.x) * scaleX - 0.5),
          ),
          top: Math.max(
            0,
            Math.ceil((candidateDocument.top - clip.y) * scaleY - 0.5),
          ),
          right: Math.min(
            first.width,
            Math.ceil((candidateDocument.right - clip.x) * scaleX - 0.5),
          ),
          bottom: Math.min(
            first.height,
            Math.ceil((candidateDocument.bottom - clip.y) * scaleY - 0.5),
          ),
        };
  const pixels = [];
  const maskKeys = new Set();
  const maskBounds = {
    left: Infinity,
    top: Infinity,
    right: -Infinity,
    bottom: -Infinity,
  };
  const maskScanWork =
    (imageBounds.right - imageBounds.left) *
    (imageBounds.bottom - imageBounds.top);
  const maskScanFailure = reserveWork("mask-scan", maskScanWork);
  if (maskScanFailure) return maskScanFailure;
  for (let y = imageBounds.top; y < imageBounds.bottom; y += 1) {
    for (let x = imageBounds.left; x < imageBounds.right; x += 1) {
      const black = read(images.black, x, y);
      const white = read(images.white, x, y);
      const coverage =
        (Math.abs(white.red - black.red) +
          Math.abs(white.green - black.green) +
          Math.abs(white.blue - black.blue)) /
        (3 * 255);
      if (coverage <= 1 / 255) continue;
      const point = documentPoint(x, y);
      if (candidate.kind !== "focus" && !inCandidate(point)) continue;
      const original = read(images.original, x, y);
      const background = read(images.hidden, x, y);
      const channel = (name) =>
        Math.max(
          0,
          Math.min(
            255,
            (original[name] - (1 - coverage) * background[name]) / coverage,
          ),
        );
      pixels.push({
        x,
        y,
        point,
        coverage,
        original,
        background,
        foreground: {
          red: channel("red"),
          green: channel("green"),
          blue: channel("blue"),
          alpha: 1,
        },
        probes: { black, white },
      });
      maskKeys.add(`${x}:${y}`);
      if (x < maskBounds.left) maskBounds.left = x;
      if (y < maskBounds.top) maskBounds.top = y;
      if (x + 1 > maskBounds.right) maskBounds.right = x + 1;
      if (y + 1 > maskBounds.bottom) maskBounds.bottom = y + 1;
    }
  }
  if (pixels.length === 0) return unresolved("missing-differential-mask");
  const maskIndexFailure = reserveWork("mask-index", pixels.length * 4);
  if (maskIndexFailure) return maskIndexFailure;

  const runs = [];
  const rows = new Map();
  for (const pixel of pixels) {
    if (!rows.has(pixel.y)) rows.set(pixel.y, []);
    rows.get(pixel.y).push(pixel.x);
  }
  for (const [y, values] of [...rows].sort((left, right) => left[0] - right[0])) {
    const sorted = [...new Set(values)].sort((left, right) => left - right);
    let startX = sorted[0];
    let endX = sorted[0];
    for (const x of sorted.slice(1)) {
      if (x === endX + 1) {
        endX = x;
      } else {
        runs.push({ y, startX, endX });
        startX = x;
        endX = x;
      }
    }
    runs.push({ y, startX, endX });
  }
  const mask = {
    pixelCount: pixels.length,
    bounds: maskBounds,
    runs,
  };
  const reliablePixels = pixels.filter(({ coverage }) => coverage >= 0.1);
  mask.reliablePixelCount = reliablePixels.length;
  if (reliablePixels.length === 0)
    return {
      ...unresolved("ambiguous-low-coverage-mask"),
      mask,
    };
  const colorKey = (color) =>
    [color.red, color.green, color.blue, color.alpha].join(":");
  const palette = (entries) => {
    const values = new Map();
    for (const entry of entries) {
      const key = colorKey(entry.color);
      const current = values.get(key);
      if (current) current.count += 1;
      else values.set(key, { color: entry.color, count: 1 });
    }
    return [...values.values()];
  };
  const coordinate = (pixel) => ({
    image: { x: pixel.x, y: pixel.y },
    document: pixel.point,
    viewport: {
      x: pixel.point.x - scroll.x,
      y: pixel.point.y - scroll.y,
    },
  });
  const nearestSourceMap = (region, source) => {
    const width = region.right - region.left;
    const height = region.bottom - region.top;
    const area = width * height;
    const verticalDistance = new Float64Array(area);
    verticalDistance.fill(Infinity);
    const verticalSource = new Int32Array(area);
    verticalSource.fill(-1);
    const nearest = new Int32Array(area);
    nearest.fill(-1);
    const distanceSquared = new Float64Array(area);
    distanceSquared.fill(Infinity);
    const maximum = Math.max(width, height);
    const input = new Float64Array(maximum);
    const output = new Float64Array(maximum);
    const argument = new Int32Array(maximum);
    const vertices = new Int32Array(maximum);
    const boundaries = new Float64Array(maximum + 1);
    const transform = (length) => {
      let envelope = -1;
      for (let position = 0; position < length; position += 1) {
        if (!Number.isFinite(input[position])) continue;
        let intersection = -Infinity;
        while (envelope >= 0) {
          const previous = vertices[envelope];
          intersection =
            (input[position] + position * position -
              (input[previous] + previous * previous)) /
            (2 * (position - previous));
          if (intersection > boundaries[envelope]) break;
          envelope -= 1;
        }
        envelope += 1;
        vertices[envelope] = position;
        boundaries[envelope] =
          envelope === 0 ? -Infinity : intersection;
        boundaries[envelope + 1] = Infinity;
      }
      if (envelope < 0) {
        for (let position = 0; position < length; position += 1) {
          output[position] = Infinity;
          argument[position] = -1;
        }
        return;
      }
      let active = 0;
      for (let position = 0; position < length; position += 1) {
        while (
          active < envelope &&
          boundaries[active + 1] < position
        )
          active += 1;
        const selected = vertices[active];
        output[position] =
          (position - selected) * (position - selected) + input[selected];
        argument[position] = selected;
      }
    };

    for (let x = 0; x < width; x += 1) {
      for (let y = 0; y < height; y += 1) {
        input[y] = source(region.left + x, region.top + y) ? 0 : Infinity;
      }
      transform(height);
      for (let y = 0; y < height; y += 1) {
        const index = y * width + x;
        verticalDistance[index] = output[y];
        verticalSource[index] = argument[y];
      }
    }
    for (let y = 0; y < height; y += 1) {
      for (let x = 0; x < width; x += 1)
        input[x] = verticalDistance[y * width + x];
      transform(width);
      for (let x = 0; x < width; x += 1) {
        const selectedX = argument[x];
        if (selectedX < 0) continue;
        const selectedY = verticalSource[y * width + selectedX];
        if (selectedY < 0) continue;
        const index = y * width + x;
        nearest[index] =
          (region.top + selectedY) * first.width + region.left + selectedX;
        distanceSquared[index] = output[x];
      }
    }
    return { region, width, nearest, distanceSquared };
  };
  const nearestAt = (map, pixel) => {
    const localX = pixel.x - map.region.left;
    const localY = pixel.y - map.region.top;
    if (
      localX < 0 ||
      localY < 0 ||
      localX >= map.width ||
      localY >= map.region.bottom - map.region.top
    )
      return null;
    const localIndex = localY * map.width + localX;
    const sourceIndex = map.nearest[localIndex];
    if (sourceIndex < 0) return null;
    const x = sourceIndex % first.width;
    const y = Math.floor(sourceIndex / first.width);
    const point = documentPoint(x, y);
    return {
      color: read(images.hidden, x, y),
      coordinate: {
        image: { x, y },
        document: point,
        viewport: { x: point.x - scroll.x, y: point.y - scroll.y },
      },
      distanceSquared: map.distanceSquared[localIndex],
    };
  };
  const evidence = (foregroundPixel, backgroundEntry, value) => ({
    ratio: value,
    coordinate: coordinate(foregroundPixel),
    backgroundCoordinate: backgroundEntry.coordinate,
    coverage: foregroundPixel.coverage,
    foreground: foregroundPixel.foreground,
    background: backgroundEntry.color,
    original: foregroundPixel.original,
    probes: foregroundPixel.probes,
    adjacentDistanceSquared: backgroundEntry.distanceSquared ?? 0,
  });
  const pairedVerdict = (foregrounds, backgrounds) => {
    let minimum = null;
    for (let index = 0; index < foregrounds.length; index += 1) {
      const foregroundPixel = foregrounds[index];
      const backgroundEntry = backgrounds[index];
      const value = contrastRatio(
        foregroundPixel.foreground,
        backgroundEntry.color,
      );
      if (minimum === null || value < minimum.ratio)
        minimum = evidence(foregroundPixel, backgroundEntry, value);
    }
    return minimum
      ? { ratio: minimum.ratio, sampleCount: backgrounds.length, minimumEvidence: minimum }
      : null;
  };

  const behind = pixels.map((pixel) => ({
    color: pixel.background,
    coordinate: coordinate(pixel),
  }));
  let inside = [];
  let outside = [];
  let pairedForegrounds = pixels;
  let adjacency;
  if (candidate.kind === "focus") {
    const candidateImageBounds = {
      left: Math.max(
        0,
        Math.ceil((candidateDocument.left - clip.x) * scaleX - 0.5),
      ),
      top: Math.max(
        0,
        Math.ceil((candidateDocument.top - clip.y) * scaleY - 0.5),
      ),
      right: Math.min(
        first.width,
        Math.ceil((candidateDocument.right - clip.x) * scaleX - 0.5),
      ),
      bottom: Math.min(
        first.height,
        Math.ceil((candidateDocument.bottom - clip.y) * scaleY - 0.5),
      ),
    };
    const region = {
      left: Math.max(
        0,
        Math.min(maskBounds.left, candidateImageBounds.left) - 1,
      ),
      top: Math.max(
        0,
        Math.min(maskBounds.top, candidateImageBounds.top) - 1,
      ),
      right: Math.min(
        first.width,
        Math.max(maskBounds.right, candidateImageBounds.right) + 1,
      ),
      bottom: Math.min(
        first.height,
        Math.max(maskBounds.bottom, candidateImageBounds.bottom) + 1,
      ),
    };
    const regionArea =
      (region.right - region.left) * (region.bottom - region.top);
    const mapFailure = reserveWork("focus-distance-maps", regionArea * 24);
    if (mapFailure) return { ...mapFailure, mask };
    const availableSurface = (x, y) => !maskKeys.has(`${x}:${y}`);
    const insideMap = nearestSourceMap(
      region,
      (x, y) => availableSurface(x, y) && inCandidate(documentPoint(x, y)),
    );
    const outsideMap = nearestSourceMap(
      region,
      (x, y) => availableSurface(x, y) && !inCandidate(documentPoint(x, y)),
    );
    if (reliablePixels.length !== pixels.length) {
      const foregroundMapFailure = reserveWork(
        "focus-foreground-distance-map",
        regionArea * 12,
      );
      if (foregroundMapFailure) return { ...foregroundMapFailure, mask };
      const reliableByIndex = new Map(
        reliablePixels.map((pixel) => [
          pixel.y * first.width + pixel.x,
          pixel,
        ]),
      );
      const foregroundMap = nearestSourceMap(
        region,
        (x, y) => reliableByIndex.has(y * first.width + x),
      );
      pairedForegrounds = pixels.map((pixel) => {
        if (pixel.coverage >= 0.1) return pixel;
        const nearest = nearestAt(foregroundMap, pixel);
        return reliableByIndex.get(
          nearest.coordinate.image.y * first.width + nearest.coordinate.image.x,
        );
      });
    }
    const pairingFailure = reserveWork(
      "focus-surface-pairing",
      pixels.length * 4,
    );
    if (pairingFailure) return { ...pairingFailure, mask };
    for (const pixel of pixels) {
      const insideEntry = nearestAt(insideMap, pixel);
      if (!insideEntry)
        return { ...unresolved("missing-focus-inside-surface"), mask };
      const outsideEntry = nearestAt(outsideMap, pixel);
      if (!outsideEntry)
        return { ...unresolved("missing-focus-outside-surface"), mask };
      inside.push(insideEntry);
      outside.push(outsideEntry);
    }
    adjacency = {
      method: "exact-euclidean-distance-map",
      matchedPixelCount: pixels.length,
      region,
    };
  } else if (reliablePixels.length !== pixels.length) {
    const regionArea =
      (imageBounds.right - imageBounds.left) *
      (imageBounds.bottom - imageBounds.top);
    const mapFailure = reserveWork(
      "foreground-distance-map",
      regionArea * 12,
    );
    if (mapFailure) return { ...mapFailure, mask };
    const reliableByIndex = new Map(
      reliablePixels.map((pixel) => [pixel.y * first.width + pixel.x, pixel]),
    );
    const foregroundMap = nearestSourceMap(
      imageBounds,
      (x, y) => reliableByIndex.has(y * first.width + x),
    );
    pairedForegrounds = pixels.map((pixel) => {
      if (pixel.coverage >= 0.1) return pixel;
      const nearest = nearestAt(foregroundMap, pixel);
      return reliableByIndex.get(
        nearest.coordinate.image.y * first.width + nearest.coordinate.image.x,
      );
    });
  }
  const surfacePairingFailure = reserveWork(
    "surface-verdicts",
    pairedForegrounds.length * (candidate.kind === "focus" ? 4 : 2),
  );
  if (surfacePairingFailure) return { ...surfacePairingFailure, mask };
  const surfaceVerdicts =
    candidate.kind === "focus"
      ? {
          inside: pairedVerdict(pairedForegrounds, inside),
          outside: pairedVerdict(pairedForegrounds, outside),
        }
      : {
          behind: pairedVerdict(pairedForegrounds, behind),
        };
  const minimumEvidence = Object.values(surfaceVerdicts)
    .filter(Boolean)
    .map((value) => value.minimumEvidence)
    .reduce(
      (minimum, value) =>
        minimum === null || value.ratio < minimum.ratio ? value : minimum,
      null,
    );
  return {
    mapping,
    timing,
    mask,
    surfacePalette: {
      behind: palette(behind),
      inside: palette(inside),
      outside: palette(outside),
    },
    surfaceVerdicts,
    minimumEvidence,
    adjacency,
    work: workSnapshot(),
    ratio: minimumEvidence.ratio,
  };
}

export function buildContrastMeasurements(candidates) {
  return candidates.map((candidate) => {
    const raw = {
      foreground: candidate.foreground,
      backgrounds: candidate.backgrounds,
      insideBackgrounds: candidate.insideBackgrounds,
      before: candidate.before,
      after: candidate.after,
      source: candidate.source,
      candidateOpacity: candidate.candidateOpacity,
      ancestorOpacities: candidate.ancestorOpacities,
      pseudoPaint: candidate.pseudoPaint,
      geometry: candidate.geometry,
      adjacent: candidate.adjacent,
      paint: candidate.paint,
      oracleId: candidate.oracleId,
      geometryCollectedAt: candidate.geometryCollectedAt,
    };
    if (candidate.kind === "focus" && !candidate.changed) {
      return {
        id: candidate.id,
        kind: candidate.kind,
        minimum: candidate.minimum,
        changed: false,
        raw,
        unsupported: "unchanged-focus-indicator",
      };
    }
    if (candidate.pseudo) {
      return {
        id: candidate.id,
        kind: candidate.kind,
        minimum: candidate.minimum,
        changed: candidate.changed,
        raw,
        unsupported: "pseudo-paint",
      };
    }
    const paint = composePaintGroups(
      candidate.foreground,
      candidate.backgrounds,
    );
    if (paint.unsupported) {
      return {
        id: candidate.id,
        kind: candidate.kind,
        minimum: candidate.minimum,
        changed: candidate.changed,
        raw,
        unsupported: paint.unsupported,
      };
    }
    return {
      id: candidate.id,
      kind: candidate.kind,
      minimum: candidate.minimum,
      changed: candidate.changed,
      raw,
      effectiveForeground: paint.foreground,
      effectiveBackground: paint.background,
      ratio: ratio(paint.foreground, paint.background),
    };
  });
}

export function applyRenderedSamples(pair, renderedSamples) {
  if (renderedSamples?.unresolved || !Number.isFinite(renderedSamples?.ratio)) {
    return {
      ...pair,
      ratio: undefined,
      unsupported: renderedSamples?.unresolved ?? "missing-rendered-samples",
      raw: { ...pair.raw, renderedSamples },
    };
  }
  const verdicts = Object.values(renderedSamples.surfaceVerdicts ?? {}).filter(
    Boolean,
  );
  return {
    ...pair,
    unsupported: undefined,
    ratio: renderedSamples.ratio,
    sampledRatios: verdicts.map(({ ratio: value }) => value),
    sampledPairs: verdicts.map(({ minimumEvidence }) => minimumEvidence),
    raw: { ...pair.raw, renderedSamples },
    sampling: {
      method: "rendered-differential-mask",
      logic:
        "Minimum contrast across every differential-mask pixel and independently retained adjacent surface; WCAG 2.2 SC 1.4.3, 1.4.11, and focus SC 2.4.11 use 4.5:1/3:1 thresholds without averaging gradients.",
    },
  };
}

function pixel(value) {
  const parsed = Number.parseFloat(String(value));
  return Number.isFinite(parsed) ? parsed : null;
}

export function buildSafeAreaMeasurements(raw) {
  return edges.map((edge) => ({
    edge,
    rootValue: pixel(raw.root?.[edge]),
    owners: (raw.owners ?? [])
      .filter((owner) => Object.hasOwn(owner.edges ?? {}, edge))
      .map((owner) => ({
        id: owner.id,
        computedValue: pixel(owner.edges[edge]),
      })),
  }));
}

function actionReachable(action, viewport) {
  const { rect, scroller } = action;
  if (rect.left < 0 || rect.right > viewport.width) return false;
  if (!scroller) return rect.top >= 0 && rect.bottom <= viewport.height;
  if (rect.left < scroller.left || rect.right > scroller.right) return false;
  const contentTop = rect.top - scroller.top + scroller.scrollTop;
  const contentBottom = rect.bottom - scroller.top + scroller.scrollTop;
  return contentTop >= 0 && contentBottom <= scroller.scrollHeight;
}

export function classifyGeometrySnapshot(raw) {
  const devicePixelRatio = raw.devicePixelRatio ?? 1;
  const controls = (raw.controls ?? []).map((control) => ({
    ...control,
    width: normalizeCssPixel(control.width, devicePixelRatio),
    height: normalizeCssPixel(control.height, devicePixelRatio),
  }));
  const scrollOwners = (raw.scrollers ?? []).filter(
    (scroller) => scroller.primary,
  );
  const nestedScrollers = (raw.scrollers ?? []).filter(
    (scroller) => !scroller.primary,
  );
  const clippedPrimary = (raw.primaryActions ?? [])
    .filter((action) => !actionReachable(action, raw.viewport))
    .map((action) => action.id);
  return { controls, scrollOwners, nestedScrollers, clippedPrimary };
}

export function assertGeometry(measurements, definition) {
  const violations = [];
  if (measurements.document.scrollWidth > measurements.document.clientWidth) {
    violations.push({ code: "horizontal-overflow", subject: "document" });
  }
  if (measurements.scrollOwners.length !== 1) {
    violations.push({ code: "scroll-owner-count", subject: definition.route });
  }
  for (const control of measurements.controls) {
    const minimum = control.platform === "android" ? 48 : 44;
    if (control.width < minimum || control.height < minimum) {
      violations.push({ code: "undersized-control", subject: control.id });
    }
  }
  for (const item of measurements.fixedBottom) {
    if (item.bottom > item.keyboardTop)
      violations.push({ code: "keyboard-occlusion", subject: item.id });
  }
  for (const id of measurements.duplicateIds)
    violations.push({ code: "duplicate-id", subject: id });
  for (const id of measurements.clippedPrimary)
    violations.push({ code: "clipped-primary", subject: id });
  for (const area of measurements.safeAreas) {
    if (area.edge) {
      const expected = definition.safeArea?.[area.edge] ?? 0;
      const owner = area.owners[0];
      if (
        area.rootValue !== expected ||
        area.owners.length !== 1 ||
        owner?.computedValue === null ||
        owner?.computedValue < area.rootValue
      ) {
        violations.push({
          code: "safe-area-ownership",
          subject: `safe-area-${area.edge}`,
        });
      }
    } else if (
      area.ownerCount !== 1 ||
      area.top !== area.expectedTop ||
      area.bottom !== area.expectedBottom
    ) {
      violations.push({ code: "safe-area-ownership", subject: area.id });
    }
  }
  for (let index = 0; index < measurements.focusOrder.length; index += 1) {
    const focus = measurements.focusOrder[index];
    if (!focus.visible || focus.order !== index + 1) {
      violations.push({ code: "focus-order-or-visibility", subject: focus.id });
    }
  }
  for (const pair of measurements.contrastPairs) {
    if (pair.unsupported) {
      violations.push({
        code:
          pair.kind === "focus" &&
          pair.unsupported === "unchanged-focus-indicator"
            ? "missing-focus-indicator"
            : "unmeasurable-contrast",
        subject: pair.id,
      });
    } else if (pair.ratio < pair.minimum) {
      const code =
        pair.kind === "focus"
          ? "insufficient-focus-contrast"
          : pair.kind === "nontext"
            ? "insufficient-nontext-contrast"
            : "insufficient-contrast";
      violations.push({ code, subject: pair.id });
    }
  }
  return violations.sort((left, right) =>
    `${left.code}:${left.subject}`.localeCompare(
      `${right.code}:${right.subject}`,
    ),
  );
}

export async function measurePage(client, options = {}) {
  const platform = options.platform ?? "ios";
  const focusOrder = options.focusOrder ?? [];
  const focusCandidates = options.focusCandidates ?? [];
  const raw = await evaluate(
    client,
    `(() => {
    const platform = ${JSON.stringify(platform)};
    const extractPaintStack = ${extractPaintStack.toString()};
    const visible = element => { const style=getComputedStyle(element),box=element.getBoundingClientRect(); return style.display!=="none"&&style.visibility!=="hidden"&&Number(style.opacity)>0&&box.width>0&&box.height>0&&!element.closest('[inert],[aria-hidden="true"]'); };
    const transparent = color => color==='transparent'||color==='rgba(0, 0, 0, 0)';
    const name = (element,index) => element.id||element.getAttribute("aria-label")||element.getAttribute("data-testid")||element.textContent?.trim().slice(0,80)||element.tagName.toLowerCase()+"-"+index;
    const rect = element => { const box=element.getBoundingClientRect(); return {left:box.left,right:box.right,top:box.top,bottom:box.bottom,width:box.width,height:box.height}; };
    const backgrounds = element => extractPaintStack(element, (target,pseudo) => getComputedStyle(target,pseudo));
    const controls=[...document.querySelectorAll('button:not([disabled]),input:not([disabled]):not([type="hidden"]),select:not([disabled]),textarea:not([disabled]),a[href],[role="button"]')].filter(visible).map((element,index)=>{const target=element.matches('input[type="radio"],input[type="checkbox"]')?element.labels?.[0]??element:element;const box=rect(target);return{id:name(element,index),width:box.width,height:box.height,platform};});
    const ids=[...document.querySelectorAll('[id]')].map(element=>element.id);const duplicateIds=[...new Set(ids.filter((id,index)=>ids.indexOf(id)!==index))];
    const all=[...document.querySelectorAll('*')].filter(visible);
    const scrollers=all.filter(element=>{const style=getComputedStyle(element);return /auto|scroll/.test(style.overflowX+style.overflowY);}).map((element,index)=>{const style=getComputedStyle(element);return{id:name(element,index),primary:element.matches('main[data-route],[data-scroll-owner="primary"],.lab-controls'),overflowX:style.overflowX,overflowY:style.overflowY,scrollWidth:element.scrollWidth,clientWidth:element.clientWidth,scrollHeight:element.scrollHeight,clientHeight:element.clientHeight};});
    const primaryActions=[...document.querySelectorAll('[class*="primary-action"],[data-primary-action]')].filter(visible).map((element,index)=>{let owner=element.parentElement;while(owner){const style=getComputedStyle(owner);if(/auto|scroll/.test(style.overflowX+style.overflowY))break;owner=owner.parentElement;}return{id:name(element,index),rect:rect(element),scroller:owner?{...rect(owner),scrollTop:owner.scrollTop,scrollHeight:owner.scrollHeight,clientHeight:owner.clientHeight}:null};});
    const fixedBottom=[...document.querySelectorAll('nav,[class*="composer"],[data-fixed-bottom]')].filter(element=>{const style=getComputedStyle(element);return visible(element)&&(style.position==='fixed'||style.position==='sticky');}).map((element,index)=>({id:name(element,index),bottom:rect(element).bottom}));
    const rootStyle=getComputedStyle(document.documentElement);const root={};for(const edge of ['top','right','bottom','left'])root[edge]=rootStyle.getPropertyValue('--safe-area-'+edge).trim();
    const ownerElements=[...document.querySelectorAll('[data-safe-area-owner],[data-safe-area-owner-top],[data-safe-area-owner-right],[data-safe-area-owner-bottom],[data-safe-area-owner-left]')].filter(visible);const owners=ownerElements.map((element,index)=>{const style=getComputedStyle(element),tokens=(element.getAttribute('data-safe-area-owner')||'').split(/[\\s,]+/).filter(Boolean),owned=new Set(tokens);for(const edge of ['top','right','bottom','left'])if(element.hasAttribute('data-safe-area-owner-'+edge))owned.add(edge);const values={top:style.paddingTop,right:style.paddingRight,bottom:style.paddingBottom,left:style.paddingLeft};const result={};for(const edge of owned)result[edge]=values[edge];return{id:name(element,index),edges:result};});
    const text=[...document.querySelectorAll('h1,h2,h3,p,label,button:not([disabled]),a,input:not([disabled]):not([type="radio"]):not([type="checkbox"]),textarea:not([disabled]),select:not([disabled])')].filter(visible).map((element,index)=>{const style=getComputedStyle(element),stack=backgrounds(element),oracleId='text-'+index;element.setAttribute('data-browser-text-candidate',oracleId);return{id:name(element,index),kind:'text',foreground:style.color,backgrounds:stack.layers,minimum:parseFloat(style.fontSize)>=24||(parseFloat(style.fontSize)>=18.66&&Number(style.fontWeight)>=700)?3:4.5,pseudo:stack.pseudoPaint.length>0,candidateOpacity:stack.candidateOpacity,ancestorOpacities:stack.ancestorOpacities,pseudoPaint:stack.pseudoPaint,geometry:rect(element),oracleId};});
    const nontext=[...document.querySelectorAll('button:not([disabled]),input:not([disabled]),textarea:not([disabled]),select:not([disabled]),a[href]')].filter(visible).map((element,index)=>{const style=getComputedStyle(element),borderVisible=style.borderTopStyle!=='none'&&parseFloat(style.borderTopWidth)>0&&!transparent(style.borderTopColor),fillVisible=!transparent(style.backgroundColor),parentStack=backgrounds(element.parentElement);if(!borderVisible&&!fillVisible)return null;const source=borderVisible?'border':'fill',oracleId='nontext-'+index;element.setAttribute('data-browser-nontext-candidate',oracleId);element.setAttribute('data-browser-nontext-source',source);const layers=[{color:'transparent',image:'none',opacity:Number(style.opacity),owner:name(element,index)},...parentStack.layers];return{id:name(element,index),kind:'nontext',foreground:borderVisible?style.borderTopColor:style.backgroundColor,backgrounds:layers,minimum:3,source,candidateOpacity:Number(style.opacity),ancestorOpacities:parentStack.layers.map(layer=>layer.opacity),pseudoPaint:backgrounds(element).pseudoPaint,geometry:rect(element),oracleId};}).filter(Boolean);
    return {collectionTime:performance.now(),devicePixelRatio,viewport:{width:innerWidth,height:innerHeight,visualWidth:visualViewport?.width??innerWidth,visualHeight:visualViewport?.height??innerHeight,visualTop:visualViewport?.offsetTop??0},document:{scrollWidth:document.documentElement.scrollWidth,clientWidth:document.documentElement.clientWidth},controls,duplicateIds,scrollers,primaryActions,fixedBottom,safe:{root,owners},contrastCandidates:[...text,...nontext]};
  })()`,
  );
  const geometry = classifyGeometrySnapshot(raw);
  const safeAreas = buildSafeAreaMeasurements(raw.safe);
  const contrastPairs = buildContrastMeasurements([
    ...raw.contrastCandidates.map((candidate) => ({
      ...candidate,
      geometryCollectedAt: raw.collectionTime,
    })),
    ...focusCandidates,
  ]);
  const keyboardTop = raw.viewport.visualTop + raw.viewport.visualHeight;
  return {
    viewport: raw.viewport,
    document: raw.document,
    controls: geometry.controls,
    scrollOwners: geometry.scrollOwners,
    nestedScrollers: geometry.nestedScrollers,
    fixedBottom: raw.fixedBottom.map((item) => ({ ...item, keyboardTop })),
    duplicateIds: raw.duplicateIds,
    clippedPrimary: geometry.clippedPrimary,
    safeAreas,
    focusOrder,
    contrastPairs,
  };
}
