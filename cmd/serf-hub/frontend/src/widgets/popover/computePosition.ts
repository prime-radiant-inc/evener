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

// One axis (horizontal or vertical) of the flip-then-clamp placement: try
// `primary` (the popup's default, unflipped offset - left-aligned to the
// trigger, or opening below it); if the popup would overflow the far edge
// there, try `flipped` instead (the other side - right-aligned to the
// trigger, or opening above it); if NEITHER fits, clamp `primary` into
// [0, viewportSize] so the popup stays as fully within the viewport as the
// available space allows, rather than settling for whichever of the two
// overflows less.
export function resolveAxis(primary: number, flipped: number, size: number, viewportSize: number): number {
  if (primary + size <= viewportSize - EDGE_MARGIN) return primary;
  if (flipped >= EDGE_MARGIN) return flipped;
  return Math.max(EDGE_MARGIN, Math.min(primary, viewportSize - size - EDGE_MARGIN));
}

// The popup's position:fixed viewport coordinates, computed fresh every
// time it opens from the trigger's and popup's OWN measured rects (never
// assumed) - see the measuring useLayoutEffect in each consumer for why the
// popup has to already be in the DOM, rendered at some placeholder position,
// before this can run.
export function computePopoverPosition(
  triggerRect: DOMRect,
  popupSize: { width: number; height: number },
): PopoverPosition {
  return {
    left: resolveAxis(triggerRect.left, triggerRect.right - popupSize.width, popupSize.width, window.innerWidth),
    top: resolveAxis(
      triggerRect.bottom + TRIGGER_GAP,
      triggerRect.top - TRIGGER_GAP - popupSize.height,
      popupSize.height,
      window.innerHeight,
    ),
  };
}
