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

function resolveBackground(backgrounds) {
  const relevant = [];
  for (const layer of backgrounds ?? []) {
    if (layer.image && layer.image !== "none") {
      return { unsupported: "background-image", rawLayers: backgrounds };
    }
    const color = parseCssColor(layer.color);
    if (!color)
      return { unsupported: "unparseable-background", rawLayers: backgrounds };
    const opacity = Number(layer.opacity ?? 1);
    relevant.push({ ...color, alpha: color.alpha * opacity });
    if (color.alpha * opacity >= 0.999) break;
  }
  let effective = { red: 0, green: 0, blue: 0, alpha: 0 };
  for (const layer of relevant.reverse())
    effective = compositeColor(layer, effective);
  if (effective.alpha < 0.999)
    return { unsupported: "transparent-background", rawLayers: backgrounds };
  return { color: effective, rawLayers: backgrounds };
}

export function buildContrastMeasurements(candidates) {
  return candidates.map((candidate) => {
    const raw = {
      foreground: candidate.foreground,
      backgrounds: candidate.backgrounds,
      before: candidate.before,
      after: candidate.after,
      source: candidate.source,
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
    const background = resolveBackground(candidate.backgrounds);
    if (background.unsupported) {
      return {
        id: candidate.id,
        kind: candidate.kind,
        minimum: candidate.minimum,
        changed: candidate.changed,
        raw,
        unsupported: background.unsupported,
      };
    }
    const foreground = parseCssColor(candidate.foreground);
    if (!foreground) {
      return {
        id: candidate.id,
        kind: candidate.kind,
        minimum: candidate.minimum,
        changed: candidate.changed,
        raw,
        unsupported: "unparseable-foreground",
      };
    }
    foreground.alpha *= Number(candidate.opacity ?? 1);
    const effectiveForeground = compositeColor(foreground, background.color);
    return {
      id: candidate.id,
      kind: candidate.kind,
      minimum: candidate.minimum,
      changed: candidate.changed,
      raw,
      effectiveForeground,
      effectiveBackground: background.color,
      ratio: ratio(effectiveForeground, background.color),
    };
  });
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
    const visible = element => { const style=getComputedStyle(element),box=element.getBoundingClientRect(); return style.display!=="none"&&style.visibility!=="hidden"&&Number(style.opacity)>0&&box.width>0&&box.height>0&&!element.closest('[inert],[aria-hidden="true"]'); };
    const transparent = color => color==='transparent'||color==='rgba(0, 0, 0, 0)';
    const name = (element,index) => element.id||element.getAttribute("aria-label")||element.getAttribute("data-testid")||element.textContent?.trim().slice(0,80)||element.tagName.toLowerCase()+"-"+index;
    const rect = element => { const box=element.getBoundingClientRect(); return {left:box.left,right:box.right,top:box.top,bottom:box.bottom,width:box.width,height:box.height}; };
    const backgrounds = element => { const result=[]; let current=element; while(current){const style=getComputedStyle(current);const before=getComputedStyle(current,'::before'),after=getComputedStyle(current,'::after');result.push({color:style.backgroundColor,image:style.backgroundImage,opacity:style.opacity,pseudo:(before.content!=="none"&&(before.backgroundImage!=="none"||before.backgroundColor!=="rgba(0, 0, 0, 0)"))||(after.content!=="none"&&(after.backgroundImage!=="none"||after.backgroundColor!=="rgba(0, 0, 0, 0)"))});current=current.parentElement;}return result;};
    const controls=[...document.querySelectorAll('button:not([disabled]),input:not([disabled]):not([type="hidden"]),select:not([disabled]),textarea:not([disabled]),a[href],[role="button"]')].filter(visible).map((element,index)=>{const target=element.matches('input[type="radio"],input[type="checkbox"]')?element.labels?.[0]??element:element;const box=rect(target);return{id:name(element,index),width:box.width,height:box.height,platform};});
    const ids=[...document.querySelectorAll('[id]')].map(element=>element.id);const duplicateIds=[...new Set(ids.filter((id,index)=>ids.indexOf(id)!==index))];
    const all=[...document.querySelectorAll('*')].filter(visible);
    const scrollers=all.filter(element=>{const style=getComputedStyle(element);return /auto|scroll/.test(style.overflowX+style.overflowY);}).map((element,index)=>{const style=getComputedStyle(element);return{id:name(element,index),primary:element.matches('main[data-route],[data-scroll-owner="primary"],.lab-controls'),overflowX:style.overflowX,overflowY:style.overflowY,scrollWidth:element.scrollWidth,clientWidth:element.clientWidth,scrollHeight:element.scrollHeight,clientHeight:element.clientHeight};});
    const primaryActions=[...document.querySelectorAll('[class*="primary-action"],[data-primary-action]')].filter(visible).map((element,index)=>{let owner=element.parentElement;while(owner){const style=getComputedStyle(owner);if(/auto|scroll/.test(style.overflowX+style.overflowY))break;owner=owner.parentElement;}return{id:name(element,index),rect:rect(element),scroller:owner?{...rect(owner),scrollTop:owner.scrollTop,scrollHeight:owner.scrollHeight,clientHeight:owner.clientHeight}:null};});
    const fixedBottom=[...document.querySelectorAll('nav,[class*="composer"],[data-fixed-bottom]')].filter(element=>{const style=getComputedStyle(element);return visible(element)&&(style.position==='fixed'||style.position==='sticky');}).map((element,index)=>({id:name(element,index),bottom:rect(element).bottom}));
    const rootStyle=getComputedStyle(document.documentElement);const root={};for(const edge of ['top','right','bottom','left'])root[edge]=rootStyle.getPropertyValue('--safe-area-'+edge).trim();
    const ownerElements=[...document.querySelectorAll('[data-safe-area-owner],[data-safe-area-owner-top],[data-safe-area-owner-right],[data-safe-area-owner-bottom],[data-safe-area-owner-left]')].filter(visible);const owners=ownerElements.map((element,index)=>{const style=getComputedStyle(element),tokens=(element.getAttribute('data-safe-area-owner')||'').split(/[\\s,]+/).filter(Boolean),owned=new Set(tokens);for(const edge of ['top','right','bottom','left'])if(element.hasAttribute('data-safe-area-owner-'+edge))owned.add(edge);const values={top:style.paddingTop,right:style.paddingRight,bottom:style.paddingBottom,left:style.paddingLeft};const result={};for(const edge of owned)result[edge]=values[edge];return{id:name(element,index),edges:result};});
    const text=[...document.querySelectorAll('h1,h2,h3,p,label,button:not([disabled]),a,input:not([disabled]),textarea:not([disabled]),select:not([disabled])')].filter(visible).map((element,index)=>{const style=getComputedStyle(element),layers=backgrounds(element);return{id:name(element,index),kind:'text',foreground:style.color,opacity:layers.reduce((value,layer)=>value*Number(layer.opacity),1),backgrounds:layers,minimum:parseFloat(style.fontSize)>=24||(parseFloat(style.fontSize)>=18.66&&Number(style.fontWeight)>=700)?3:4.5,pseudo:layers[0]?.pseudo};});
    const nontext=[...document.querySelectorAll('button:not([disabled]),input:not([disabled]),textarea:not([disabled]),select:not([disabled]),a[href]')].filter(visible).map((element,index)=>{const style=getComputedStyle(element),borderVisible=style.borderTopStyle!=='none'&&parseFloat(style.borderTopWidth)>0&&!transparent(style.borderTopColor),fillVisible=!transparent(style.backgroundColor),layers=backgrounds(element.parentElement);if(!borderVisible&&!fillVisible)return null;return{id:name(element,index),kind:'nontext',foreground:borderVisible?style.borderTopColor:style.backgroundColor,opacity:Number(style.opacity)*layers.reduce((value,layer)=>value*Number(layer.opacity),1),backgrounds:layers,minimum:3,source:borderVisible?'border':'fill'};}).filter(Boolean);
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
