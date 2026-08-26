import { evaluate } from "./cdp.mjs";

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
    if (item.bottom > item.keyboardTop) {
      violations.push({ code: "keyboard-occlusion", subject: item.id });
    }
  }
  for (const id of measurements.duplicateIds)
    violations.push({ code: "duplicate-id", subject: id });
  for (const id of measurements.clippedPrimary)
    violations.push({ code: "clipped-primary", subject: id });
  for (const area of measurements.safeAreas) {
    if (
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
    if (pair.ratio < pair.minimum)
      violations.push({ code: "insufficient-contrast", subject: pair.id });
  }
  return violations.sort((left, right) =>
    `${left.code}:${left.subject}`.localeCompare(
      `${right.code}:${right.subject}`,
    ),
  );
}
export async function measurePage(client, options = {}) {
  const platform = options.platform ?? "ios";
  const expectedTop = options.safeArea?.top ?? 0;
  const expectedBottom = options.safeArea?.bottom ?? 0;
  const keyboardTop = options.keyboardTop ?? options.viewport?.height ?? 100000;
  const focusOrder = options.focusOrder ?? [];
  return evaluate(
    client,
    `(() => {
    const platform = ${JSON.stringify(platform)};
    const expectedTop = ${expectedTop};
    const expectedBottom = ${expectedBottom};
    const keyboardTop = ${keyboardTop};
    const focusOrder = ${JSON.stringify(focusOrder)};
    const visible = (element) => {
      const style = getComputedStyle(element); const box = element.getBoundingClientRect();
      return style.display !== "none" && style.visibility !== "hidden" && Number(style.opacity) > 0 && box.width > 0 && box.height > 0;
    };
    const name = (element, index) => element.id || element.getAttribute("aria-label") || element.getAttribute("data-testid") || element.textContent?.trim().slice(0, 80) || element.tagName.toLowerCase() + "-" + index;
    const controls = [...document.querySelectorAll('button:not([disabled]), input:not([disabled]):not([type="hidden"]), select:not([disabled]), textarea:not([disabled]), a[href], [role="button"]')]
      .filter(visible).map((element, index) => { const target = element.matches('input[type="radio"],input[type="checkbox"]') ? element.labels?.[0] ?? element : element; const box = target.getBoundingClientRect(); return { id: name(element, index), width: box.width, height: box.height, platform }; });
    const ids = [...document.querySelectorAll("[id]")].map((element) => element.id);
    const duplicateIds = [...new Set(ids.filter((id, index) => ids.indexOf(id) !== index))];
    const primary = [...document.querySelectorAll('[class*="primary-action"], [data-primary-action]')].filter(visible);
    const clippedPrimary = primary.filter((element) => { const box = element.getBoundingClientRect(); let ancestor=element.parentElement,sawScrollable=false; while(ancestor){const style=getComputedStyle(ancestor);if(/auto|scroll/.test(style.overflow+style.overflowY+style.overflowX))sawScrollable=true;if(!sawScrollable&&/hidden|clip/.test(style.overflow+style.overflowY+style.overflowX)){const owner=ancestor.getBoundingClientRect();return box.left<owner.left||box.right>owner.right||box.top<owner.top||box.bottom>owner.bottom}ancestor=ancestor.parentElement}return false; }).map(name);
    const scrollOwners = [...document.querySelectorAll('main[data-route], [data-scroll-owner]')].filter(element => { const style=getComputedStyle(element); return visible(element) && /auto|scroll/.test(style.overflowY); }).map((element, index) => ({ id: name(element, index), overflowY: getComputedStyle(element).overflowY }));
    const fixedBottom = [...document.querySelectorAll('nav, [class*="composer"], [data-fixed-bottom]')].filter((element) => { const style = getComputedStyle(element); return visible(element) && (style.position === "fixed" || style.position === "sticky"); }).map((element, index) => ({ id: name(element, index), bottom: element.getBoundingClientRect().bottom, keyboardTop }));
    const candidates = [...document.querySelectorAll('[data-safe-area-owner]')].filter(visible);
    const topOwners = candidates.filter((element) => parseFloat(getComputedStyle(element).paddingTop) >= expectedTop && expectedTop > 0);
    const bottomOwners = candidates.filter((element) => parseFloat(getComputedStyle(element).paddingBottom) >= expectedBottom && expectedBottom > 0);
    const safeAreas = expectedTop || expectedBottom ? [
      { id: "safe-area-top", ownerCount: topOwners.length, top: topOwners.length === 1 ? expectedTop : -1, bottom: expectedBottom, expectedTop, expectedBottom },
      { id: "safe-area-bottom", ownerCount: bottomOwners.length, top: expectedTop, bottom: bottomOwners.length === 1 ? expectedBottom : -1, expectedTop, expectedBottom },
    ] : [{ id: "safe-area-zero", ownerCount: 1, top: 0, bottom: 0, expectedTop: 0, expectedBottom: 0 }];
    const parse = (value) => { const match = value.match(/[\\d.]+/g); return match ? match.slice(0, 3).map(Number) : null; };
    const luminance = (rgb) => { const values = rgb.map((value) => { const x = value / 255; return x <= .04045 ? x / 12.92 : ((x + .055) / 1.055) ** 2.4; }); return .2126 * values[0] + .7152 * values[1] + .0722 * values[2]; };
    const contrastPairs = [...document.querySelectorAll('h1,h2,h3,p,label,button:not([disabled]),a,input:not([disabled]),textarea:not([disabled]),select:not([disabled])')].filter(visible).map((element, index) => {
      const style = getComputedStyle(element); let parent = element; let background = null;
      while (parent && !background) { const color = getComputedStyle(parent).backgroundColor; if (!color.endsWith(', 0)') && color !== 'rgba(0, 0, 0, 0)') background = parse(color); parent = parent.parentElement; }
      const foreground = parse(style.color); const first = luminance(foreground ?? [0,0,0]); const second = luminance(background ?? [255,255,255]);
      return { id: name(element, index), ratio: (Math.max(first, second) + .05) / (Math.min(first, second) + .05), minimum: parseFloat(style.fontSize) >= 24 || (parseFloat(style.fontSize) >= 18.66 && Number(style.fontWeight) >= 700) ? 3 : 4.5 };
    });
    return { viewport: { width: innerWidth, height: innerHeight }, document: { scrollWidth: document.documentElement.scrollWidth, clientWidth: document.documentElement.clientWidth }, scrollOwners, controls, fixedBottom, duplicateIds, clippedPrimary, safeAreas, focusOrder, contrastPairs };
  })()`,
  );
}
