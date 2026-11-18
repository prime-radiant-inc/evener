// The flip-then-clamp overlay placement math, extracted verbatim from
// widgets/menu so Menu, the model combobox (Task 12), and the directory
// popover (Task 13) all share one implementation of §3.4's "float, never
// reflow" positioning. This is a pure move: the math is unchanged from Menu's
// original computeMenuPosition/resolveAxis.

export interface PopoverPosition {
  top: number;
  left: number;
}

// Clear space the popup always keeps from the viewport's own edge (matches
// --space-2 - a plain number here, not a custom-property read, since this
// feeds arithmetic against getBoundingClientRect() values, not a CSS
// declaration).
export const EDGE_MARGIN = 8;
// Gap between the trigger and the popup when it opens below/above it -
// matches the pre-portal popup's own `top: calc(100% + var(--space-1))`.
export const TRIGGER_GAP = 4;

// Slide `preferred` along one axis until a `size`-long box starting there sits
// inside [EDGE_MARGIN, viewportSize - EDGE_MARGIN] - the "shift" half of
// flip-then-clamp, and the whole of the collision handling for an overlay that
// has no meaningful flipped alternative (a centered tooltip bubble: mirroring a
// centered box lands it right back where it started).
//
// When the box is wider than the space between the two margins it cannot honor
// both, so the near edge wins: Math.max runs last, which pins the box to
// EDGE_MARGIN and lets it overhang the far margin rather than pushing its
// leading edge off-screen. Nothing is lost that way - the overhang eats into
// clear space the layout reserved, not into the viewport.
export function clampIntoViewport(preferred: number, size: number, viewportSize: number): number {
  return Math.max(EDGE_MARGIN, Math.min(preferred, viewportSize - size - EDGE_MARGIN));
}

// A candidate is usable only when it is inside the viewport on BOTH edges: its
// near edge clears EDGE_MARGIN, and its far edge does not pass the edge itself.
// The near margin is required because there is clear space reserved there.
//
// The far edge is where the two candidates differ, and only in one direction:
//
//   * `primary` - where the popup wants to be - must also keep the full
//     trailing margin, or a popup that merely grazes the edge would be accepted
//     where the caller has always flipped to keep the same clear space.
//   * `flipped` may use the trailing margin when the popup still fits inside
//     the viewport. It is the fallback that exists because `primary` did not
//     fit; refusing it for the margin alone would clamp a popup that fits.
//
// Testing the far edge of `primary` but only the near edge of `flipped` was the
// bug: it accepted a flip at y=576 in a 500px-tall viewport, because the flip's
// TOP cleared the margin while the whole popup - and the keyboard focus inside
// it - sat below the window. Such a trigger is ordinary, not pathological: a
// rail row keeps its own y while the window shrinks over it, with no scroll to
// move it.
function clearsViewport(position: number, size: number, viewportSize: number, trailingMargin: number): boolean {
  return position >= EDGE_MARGIN && position + size <= viewportSize - trailingMargin;
}

// One axis (horizontal or vertical) of the flip-then-clamp placement: try
// `primary` (the popup's default, unflipped offset - left-aligned to the
// trigger, or opening below it); if the popup crosses either edge there, try
// `flipped` instead (the other side - right-aligned to the trigger, or opening
// above it); if NEITHER fits, clamp `primary` into [EDGE_MARGIN,
// viewportSize - EDGE_MARGIN] so the popup stays as fully within the viewport
// as the available space allows, rather than settling for whichever of the two
// overflows less.
export function resolveAxis(primary: number, flipped: number, size: number, viewportSize: number): number {
  if (clearsViewport(primary, size, viewportSize, EDGE_MARGIN)) return primary;
  if (clearsViewport(flipped, size, viewportSize, 0)) return flipped;
  return clampIntoViewport(primary, size, viewportSize);
}

// The popup's position:fixed viewport coordinates, computed fresh every
// time it opens from the trigger's and popup's OWN measured rects (never
// assumed) - see the measuring useLayoutEffect in each consumer for why the
// popup has to already be in the DOM, rendered at some placeholder position,
// before this can run.
export function computePopoverPosition(
  triggerRect: DOMRect,
  popupSize: { width: number; height: number },
  viewport: { width: number; height: number } = { width: window.innerWidth, height: window.innerHeight },
): PopoverPosition {
  return {
    left: resolveAxis(triggerRect.left, triggerRect.right - popupSize.width, popupSize.width, viewport.width),
    top: resolveAxis(
      triggerRect.bottom + TRIGGER_GAP,
      triggerRect.top - TRIGGER_GAP - popupSize.height,
      popupSize.height,
      viewport.height,
    ),
  };
}

// The corner a placed popup grows out of: the side of the trigger it opened
// on (below or, flipped, above) and the edge it aligned to (the trigger's left
// or, flipped, its right). A Float layer scales from here, so it reads as
// coming out of its trigger whichever way the placement resolved.
export function transformOriginFor(triggerRect: { top: number; left: number }, placed: PopoverPosition): string {
  const vertical = placed.top < triggerRect.top ? "bottom" : "top";
  const horizontal = placed.left < triggerRect.left ? "right" : "left";
  return `${horizontal} ${vertical}`;
}
