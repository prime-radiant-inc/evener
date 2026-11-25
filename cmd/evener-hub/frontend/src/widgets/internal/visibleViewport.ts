/** The part of a window a reader can see: the VISUAL viewport where there is
 * one, the layout viewport where there is not (jsdom, and engines predating
 * it). Placement answers to this region, so a pinch-zoomed or keyboard-shrunk
 * page does not place a floating layer against area nobody can see. */
export interface VisibleViewportBounds {
  left: number;
  top: number;
  width: number;
  height: number;
}

export function visibleViewportBounds(ownerWindow: Window): VisibleViewportBounds {
  const viewport = ownerWindow.visualViewport;
  if (!viewport) return { left: 0, top: 0, width: ownerWindow.innerWidth, height: ownerWindow.innerHeight };
  return { left: viewport.offsetLeft, top: viewport.offsetTop, width: viewport.width, height: viewport.height };
}

/** A layout-viewport rect moved into `bounds`' coordinates, the frame the
 * flip-then-clamp math works in when the visible viewport is a panned
 * sub-rect of the layout viewport. */
export function rectInVisibleViewport(rect: DOMRect, bounds: VisibleViewportBounds): DOMRect {
  return {
    ...rect,
    left: rect.left - bounds.left,
    right: rect.right - bounds.left,
    top: rect.top - bounds.top,
    bottom: rect.bottom - bounds.top,
  } as DOMRect;
}

/** The inverse translation: a placement computed in `bounds`' coordinates,
 * back in the layout-viewport coordinates position:fixed uses. */
export function positionFromVisibleViewport(
  position: { left: number; top: number },
  bounds: VisibleViewportBounds,
): { left: number; top: number } {
  return { left: position.left + bounds.left, top: position.top + bounds.top };
}
