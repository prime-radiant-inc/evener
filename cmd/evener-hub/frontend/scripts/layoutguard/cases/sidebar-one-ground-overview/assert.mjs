// sidebar-one-ground-overview: the Overview sidebar sits on the page's
// --surface-0 in both themes; the split is the 1px --edge hairline plus the
// left-cast --shadow-overview, and --z-raised keeps the cast above the
// following sibling's paint. The mobile variant (.sidebarMobile) carries
// neither hairline nor shadow.
// Spec: docs/superpowers/specs/2026-11-28-sidebar-one-ground-design.md.
const PAGE_DARK = "rgb(17, 18, 20)";
const PAGE_LIGHT = "rgb(246, 247, 249)";
const EDGE_DARK = "rgb(51, 56, 66)";
const EDGE_LIGHT = "rgb(213, 217, 224)";

function shadowOffsetX(shadow) {
  const m = /rgba?\([^)]*\) (-?\d+(?:\.\d+)?)px 0px 24px -12px/.exec(shadow);
  return m ? Number(m[1]) : null;
}

function shadowAlpha(shadow) {
  const m = /rgba\([^,]+,[^,]+,[^,]+,\s*([\d.]+)\)/.exec(shadow);
  return m ? Number(m[1]) : 1;
}

export default function assert(measurement) {
  const failures = [];
  if (measurement.ovDarkBg !== PAGE_DARK) {
    failures.push(`ovDarkBg resolved to ${measurement.ovDarkBg}, want page ground ${PAGE_DARK}`);
  }
  if (measurement.ovLightBg !== PAGE_LIGHT) {
    failures.push(`ovLightBg resolved to ${measurement.ovLightBg}, want page ground ${PAGE_LIGHT}`);
  }
  if (measurement.ovMobileBg !== PAGE_DARK) {
    failures.push(`ovMobileBg resolved to ${measurement.ovMobileBg}, want page ground ${PAGE_DARK}`);
  }
  if (measurement.ovDarkBorderLeftWidth !== "1px" || measurement.ovDarkBorderLeftStyle !== "solid") {
    failures.push(`overview dark hairline resolved to ${measurement.ovDarkBorderLeftWidth} ${measurement.ovDarkBorderLeftStyle}`);
  }
  if (measurement.ovDarkBorderLeftColor !== EDGE_DARK || measurement.ovLightBorderLeftColor !== EDGE_LIGHT) {
    failures.push(`overview hairline colors resolved to ${measurement.ovDarkBorderLeftColor}/${measurement.ovLightBorderLeftColor}`);
  }
  if (measurement.ovMobileBorderLeftWidth !== "0px") {
    failures.push(`mobile overview hairline resolved to ${measurement.ovMobileBorderLeftWidth}, want 0px`);
  }
  if (shadowOffsetX(measurement.ovDarkShadow) !== -8 || shadowOffsetX(measurement.ovLightShadow) !== -8) {
    failures.push(`overview shadows resolved to ${measurement.ovDarkShadow} / ${measurement.ovLightShadow}, want -8px 0 24px -12px`);
  }
  if (shadowAlpha(measurement.ovLightShadow) >= shadowAlpha(measurement.ovDarkShadow)) {
    failures.push(`light overview shadow (alpha ${shadowAlpha(measurement.ovLightShadow)}) is not fainter than dark's (${shadowAlpha(measurement.ovDarkShadow)})`);
  }
  if (measurement.ovMobileShadow !== "none") {
    failures.push(`mobile overview shadow resolved to ${measurement.ovMobileShadow}, want none`);
  }
  if (measurement.ovDarkZ !== "1") {
    failures.push(`overview z-index resolved to ${measurement.ovDarkZ}, want --z-raised (1)`);
  }
  return failures.length === 0
    ? { pass: true, reason: "the Overview sidebar shares the page ground in both themes, with the hairline, the left cast, and the raised z-index; the mobile variant carries neither" }
    : { pass: false, reason: failures.join("; ") };
}
