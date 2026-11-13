// Motion token reads for TypeScript. The tokens in styles/tokens.css are the
// single source of truth for every duration; code that needs one in
// milliseconds (a timeout bounded by an animation) reads it here at call time,
// from the element it concerns, so a popout window's own stylesheet and any
// later token change are honored. This module does not import the motion
// library, so a widget can read a duration without loading it.

export type MotionDurationToken =
  | "--motion-duration-press"
  | "--motion-duration-exit"
  | "--motion-duration-overlay"
  | "--motion-duration-hover"
  | "--motion-duration-spatial-exit"
  | "--motion-duration-attention"
  | "--motion-duration-spatial";

// Used only where the stylesheet is not loaded (a unit test's document): the
// token's shipped value, so a timeout still has its real bound.
const SHIPPED_MS: Record<MotionDurationToken, number> = {
  "--motion-duration-press": 90,
  "--motion-duration-exit": 80,
  "--motion-duration-overlay": 120,
  "--motion-duration-hover": 150,
  "--motion-duration-spatial-exit": 180,
  "--motion-duration-attention": 200,
  "--motion-duration-spatial": 240,
};

function parseMs(raw: string): number {
  const value = raw.trim();
  if (value.endsWith("ms")) return Number.parseFloat(value);
  if (value.endsWith("s")) return Number.parseFloat(value) * 1000;
  return Number.NaN;
}

/** The token's duration in milliseconds, as computed on `from` (the document
 * root of `from`'s window when omitted). */
export function motionDurationMs(token: MotionDurationToken, from?: Element | null): number {
  const element = from ?? document.documentElement;
  const view = element.ownerDocument.defaultView ?? window;
  const ms = parseMs(view.getComputedStyle(element).getPropertyValue(token));
  return Number.isFinite(ms) && ms >= 0 ? ms : SHIPPED_MS[token];
}
