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
    let ancestor = element.parentElement;
    while (ancestor) {
      const ancestorStyle =
        ancestor.ownerDocument?.defaultView?.getComputedStyle(ancestor);
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
        style?.display !== "none" &&
        style?.visibility !== "hidden" &&
        opacity > 0 &&
        intersection.width > 0 &&
        intersection.height > 0,
      opacity,
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
    const timing = animation.effect?.getComputedTiming?.() ?? {};
    const specified = animation.effect?.getTiming?.() ?? {};
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
    if (timing.endTime === Infinity || specified.iterations === Infinity) {
      let stage = "pause";
      let before;
      let duration;
      let current;
      try {
        animation.pause();
        stage = "before-snapshot";
        before = snapshotTarget(target);
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
          Number(snapshot.opacity) > 0 &&
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
        Number(snapshot.opacity) * 1e9 +
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
      const terminal = Number(timing.endTime);
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
  const foreground = parseCssColor(pair.raw?.foreground);
  if (!foreground)
    return { ...pair, unsupported: "unparseable-sampled-foreground" };
  const groupOpacities = [
    pair.raw?.candidateOpacity ?? 1,
    ...(pair.raw?.ancestorOpacities ?? []),
  ].map(Number);
  if (groupOpacities.some((opacity) => opacity !== 1)) {
    return {
      ...pair,
      ratio: undefined,
      unsupported: "rendered-group-opacity",
      raw: { ...pair.raw, renderedSamples },
    };
  }
  const distance = (color) =>
    Math.hypot(
      color.red - foreground.red,
      color.green - foreground.green,
      color.blue - foreground.blue,
    );
  const withoutForegroundPixels = (colors) => {
    if (colors.length <= 1) return colors;
    const maximum = Math.max(...colors.map(distance));
    return colors.filter((color) => distance(color) >= maximum * 0.5);
  };
  let surfaces =
    pair.kind === "focus"
      ? [
          ...withoutForegroundPixels(renderedSamples.inside ?? []),
          ...withoutForegroundPixels(renderedSamples.outside ?? []),
        ]
      : pair.kind === "nontext"
        ? (renderedSamples.outside ?? [])
        : (renderedSamples.inside ?? []);
  if (pair.kind === "text" && surfaces.length > 1) {
    surfaces = withoutForegroundPixels(surfaces);
  }
  const unique = [
    ...new Map(
      surfaces.map((color) => [
        [color.red, color.green, color.blue, color.alpha].join(":"),
        color,
      ]),
    ).values(),
  ];
  if (unique.length === 0)
    return { ...pair, unsupported: "missing-rendered-samples" };
  const sampledPairs = unique.map((background) => {
    const effectiveForeground = compositeColor(foreground, background);
    return {
      background,
      foreground: effectiveForeground,
      ratio: ratio(effectiveForeground, background),
    };
  });
  return {
    ...pair,
    unsupported: undefined,
    ratio: Math.min(...sampledPairs.map((sample) => sample.ratio)),
    sampledRatios: sampledPairs.map((sample) => sample.ratio),
    sampledPairs,
    raw: { ...pair.raw, renderedSamples },
    sampling: {
      method: "rendered-adjacent-pixels",
      logic:
        "Minimum contrast across sampled adjacent surfaces; WCAG 2.2 SC 1.4.3, 1.4.11, and focus SC 2.4.11 use 4.5:1/3:1 thresholds without averaging gradients.",
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
    const text=[...document.querySelectorAll('h1,h2,h3,p,label,button:not([disabled]),a,input:not([disabled]):not([type="radio"]):not([type="checkbox"]),textarea:not([disabled]),select:not([disabled])')].filter(visible).map((element,index)=>{const style=getComputedStyle(element),stack=backgrounds(element);return{id:name(element,index),kind:'text',foreground:style.color,backgrounds:stack.layers,minimum:parseFloat(style.fontSize)>=24||(parseFloat(style.fontSize)>=18.66&&Number(style.fontWeight)>=700)?3:4.5,pseudo:stack.pseudoPaint.length>0,candidateOpacity:stack.candidateOpacity,ancestorOpacities:stack.ancestorOpacities,pseudoPaint:stack.pseudoPaint,geometry:rect(element)};});
    const nontext=[...document.querySelectorAll('button:not([disabled]),input:not([disabled]),textarea:not([disabled]),select:not([disabled]),a[href]')].filter(visible).map((element,index)=>{const style=getComputedStyle(element),borderVisible=style.borderTopStyle!=='none'&&parseFloat(style.borderTopWidth)>0&&!transparent(style.borderTopColor),fillVisible=!transparent(style.backgroundColor),parentStack=backgrounds(element.parentElement);if(!borderVisible&&!fillVisible)return null;const layers=[{color:'transparent',image:'none',opacity:Number(style.opacity),owner:name(element,index)},...parentStack.layers];return{id:name(element,index),kind:'nontext',foreground:borderVisible?style.borderTopColor:style.backgroundColor,backgrounds:layers,minimum:3,source:borderVisible?'border':'fill',candidateOpacity:Number(style.opacity),ancestorOpacities:parentStack.layers.map(layer=>layer.opacity),pseudoPaint:backgrounds(element).pseudoPaint,geometry:rect(element)};}).filter(Boolean);
    return {devicePixelRatio,viewport:{width:innerWidth,height:innerHeight,visualWidth:visualViewport?.width??innerWidth,visualHeight:visualViewport?.height??innerHeight,visualTop:visualViewport?.offsetTop??0},document:{scrollWidth:document.documentElement.scrollWidth,clientWidth:document.documentElement.clientWidth},controls,duplicateIds,scrollers,primaryActions,fixedBottom,safe:{root,owners},contrastCandidates:[...text,...nontext]};
  })()`,
  );
  const geometry = classifyGeometrySnapshot(raw);
  const safeAreas = buildSafeAreaMeasurements(raw.safe);
  const contrastPairs = buildContrastMeasurements([
    ...raw.contrastCandidates,
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
