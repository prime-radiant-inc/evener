// sidebar-one-ground: the rail sits on the page's --surface-0 in both
// themes; the split is the 1px --edge hairline plus the one-sided
// --shadow-rail cast, and --z-raised keeps the cast above the following
// sibling's paint. Spec: docs/superpowers/specs/2026-11-28-sidebar-one-ground-design.md.
import { EDGE_DARK, EDGE_LIGHT, PAGE_DARK, PAGE_LIGHT, shadowAlpha, shadowOffsetX } from "./shared-assert.mjs";

export default function assert(measurement) {
  const failures = [];
  if (measurement.railDarkBg !== PAGE_DARK) {
    failures.push(`railDarkBg resolved to ${measurement.railDarkBg}, want page ground ${PAGE_DARK}`);
  }
  if (measurement.railLightBg !== PAGE_LIGHT) {
    failures.push(`railLightBg resolved to ${measurement.railLightBg}, want page ground ${PAGE_LIGHT}`);
  }
  if (measurement.railDarkBorderRightWidth !== "1px" || measurement.railDarkBorderRightStyle !== "solid") {
    failures.push(`rail dark hairline resolved to ${measurement.railDarkBorderRightWidth} ${measurement.railDarkBorderRightStyle}`);
  }
  if (measurement.railDarkBorderRightColor !== EDGE_DARK || measurement.railLightBorderRightColor !== EDGE_LIGHT) {
    failures.push(`rail hairline colors resolved to ${measurement.railDarkBorderRightColor}/${measurement.railLightBorderRightColor}`);
  }
  if (shadowOffsetX(measurement.railDarkShadow) !== 8 || shadowOffsetX(measurement.railLightShadow) !== 8) {
    failures.push(`rail shadows resolved to ${measurement.railDarkShadow} / ${measurement.railLightShadow}, want 8px 0 24px -12px`);
  }
  if (shadowAlpha(measurement.railLightShadow) >= shadowAlpha(measurement.railDarkShadow)) {
    failures.push(`light rail shadow (alpha ${shadowAlpha(measurement.railLightShadow)}) is not fainter than dark's (${shadowAlpha(measurement.railDarkShadow)})`);
  }
  if (measurement.railDarkZ !== "1") {
    failures.push(`rail z-index resolved to ${measurement.railDarkZ}, want --z-raised (1)`);
  }
  return failures.length === 0
    ? { pass: true, reason: "the rail shares the page ground in both themes, with the hairline, the cast, and the raised z-index" }
    : { pass: false, reason: failures.join("; ") };
}
