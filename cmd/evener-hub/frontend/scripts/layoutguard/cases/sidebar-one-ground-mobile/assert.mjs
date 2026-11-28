// sidebar-one-ground-mobile: at <=899px the rail renders inside StackHost's
// drawer Sheet, which already frames the panel — so the rail keeps the page
// ground but carries no border and no shadow of its own (spec 2026-11-28).
import { PAGE_DARK } from "../sidebar-one-ground/shared-assert.mjs";

export default function assert(measurement) {
  const failures = [];
  if (measurement.railBg !== PAGE_DARK) {
    failures.push(`rail background resolved to ${measurement.railBg}, want page ground ${PAGE_DARK}`);
  }
  if (measurement.railBorderRightWidth !== "0px") {
    failures.push(`rail hairline resolved to ${measurement.railBorderRightWidth}, want 0px inside the drawer`);
  }
  if (measurement.railShadow !== "none") {
    failures.push(`rail shadow resolved to ${measurement.railShadow}, want none inside the drawer`);
  }
  return failures.length === 0
    ? { pass: true, reason: "drawer-hosted rail keeps the page ground with no border and no shadow" }
    : { pass: false, reason: failures.join("; ") };
}
