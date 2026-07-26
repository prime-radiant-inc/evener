// WCAG 2.x relative-luminance contrast ratio, shared by any layoutguard case
// that asserts colour rather than geometry. Takes the raw `rgb(r, g, b)` (or
// `rgba(...)`) strings a browser's getComputedStyle returns - no colour
// parsing happens in harness.html itself, so a case stays a thin geometry-vs-
// colour extraction and the math lives in one place.

function parseRgb(css) {
  const m = /rgba?\(\s*([\d.]+)\s*,\s*([\d.]+)\s*,\s*([\d.]+)/.exec(css);
  if (!m) throw new Error(`not an rgb()/rgba() color string: ${css}`);
  return [Number(m[1]), Number(m[2]), Number(m[3])];
}

function channelLuminance(c) {
  const s = c / 255;
  return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
}

function relativeLuminance([r, g, b]) {
  return 0.2126 * channelLuminance(r) + 0.7152 * channelLuminance(g) + 0.0722 * channelLuminance(b);
}

/** @param {string} fgCss @param {string} bgCss @returns {number} */
export function contrastRatio(fgCss, bgCss) {
  const lFg = relativeLuminance(parseRgb(fgCss));
  const lBg = relativeLuminance(parseRgb(bgCss));
  const lighter = Math.max(lFg, lBg);
  const darker = Math.min(lFg, lBg);
  return (lighter + 0.05) / (darker + 0.05);
}
