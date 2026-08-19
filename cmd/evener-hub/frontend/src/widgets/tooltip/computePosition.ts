// Tooltip placement: centre-then-shift horizontally, flip-then-clamp
// vertically. The viewport-edge arithmetic itself is NOT re-derived here - both
// EDGE_MARGIN and the shift are imported from widgets/popover/computePosition,
// the module Menu and Popover already share, so the app has exactly one
// definition of "how much clear space an overlay keeps from the viewport edge"
// and one implementation of sliding a box back inside it.
//
// What is deliberately NOT shared is the placement POLICY, because the two
// widgets genuinely differ:
//
//   * Popover edge-ALIGNS its panel to the trigger and prefers opening below,
//     so resolveAxis only ever has to catch an overflow of the FAR edge.
//   * A tooltip CENTRES on its trigger and prefers opening above. Centring
//     makes a horizontal flip meaningless (mirroring a centred box lands it
//     back where it started - a shift is the only move available), and
//     preferring "above" makes the NEAR edge the one that overflows first,
//     which is the opposite of what resolveAxis tests.
//
// Widening resolveAxis to cover both preferences would make Popover's call
// site worse to serve a case it does not have. The three-line preference test
// below is the difference between the widgets, so it lives with the widget.
import { clampIntoViewport, EDGE_MARGIN } from "../popover/computePosition";

export interface TooltipPosition {
  top: number;
  left: number;
}

// Clear space between the trigger and the bubble - matches --space-2, and the
// pre-collision stylesheet's own `bottom: calc(100% + var(--space-2))`. A plain
// number, not a custom-property read, for the same reason EDGE_MARGIN is one:
// it feeds arithmetic against measured rects, not a CSS declaration.
export const TRIGGER_GAP = 8;

export interface Size {
  width: number;
  height: number;
}

/**
 * The bubble's position: fixed viewport coordinates for a trigger at
 * `triggerRect`, given the bubble's own untransformed size.
 *
 * Horizontally: centred on the trigger, then shifted just far enough to keep
 * EDGE_MARGIN of clear space at whichever viewport edge it would otherwise
 * cross. Vertically: above the trigger when the bubble fits there, below it
 * when it does not, and clamped to the viewport when neither side has room.
 */
export function computeTooltipPosition(triggerRect: DOMRect, bubbleSize: Size, viewport: Size): TooltipPosition {
  const centered = triggerRect.left + triggerRect.width / 2 - bubbleSize.width / 2;
  const above = triggerRect.top - TRIGGER_GAP - bubbleSize.height;
  const below = triggerRect.bottom + TRIGGER_GAP;
  return {
    left: clampIntoViewport(centered, bubbleSize.width, viewport.width),
    top:
      above >= EDGE_MARGIN
        ? above
        : below + bubbleSize.height <= viewport.height - EDGE_MARGIN
          ? below
          : clampIntoViewport(above, bubbleSize.height, viewport.height),
  };
}
