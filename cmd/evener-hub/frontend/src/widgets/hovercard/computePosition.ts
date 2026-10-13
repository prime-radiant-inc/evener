import { clampIntoViewport, EDGE_MARGIN } from "../popover/computePosition";
import { computeTooltipPosition, type Size, type TooltipPosition } from "../tooltip/computePosition";

/** The caller owns the row and side boundary, independently of the title trigger. */
export interface SideAnchor {
  rowRect: DOMRect;
  sideRight: number;
}

const SIDE_GAP = 12;

export function computeSideAnchorPosition(anchor: SideAnchor, bubbleSize: Size, viewport: Size): TooltipPosition {
  const left = anchor.sideRight + SIDE_GAP;
  if (left + bubbleSize.width > viewport.width - EDGE_MARGIN) {
    return computeTooltipPosition(anchor.rowRect, bubbleSize, viewport, SIDE_GAP);
  }
  return {
    left,
    top: clampIntoViewport(
      anchor.rowRect.top + anchor.rowRect.height / 2 - bubbleSize.height / 2,
      bubbleSize.height,
      viewport.height,
    ),
  };
}
