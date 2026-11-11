import { useEffect, useRef } from "react";

/**
 * Dismiss a floating surface when a press lands outside it.
 *
 * The gesture is read at the PRESS (mousedown), not the release: a reader
 * selecting text and dragging the pointer out of the surface must not
 * dismiss it mid-selection. Matches the overlay family's press-start rule
 * (widgets/dialog/OverlayPanel.tsx).
 *
 * `isInside` reports whether a press target belongs to the surface. Each
 * caller has its own notion of "inside": a portaled panel finishes outside
 * its trigger's DOM subtree, and a disclosure whose conversation can be shown
 * by two panels at once must treat the other panel as inside too.
 *
 * The predicate and the dismissal ride refs, so a caller may pass fresh
 * closures on every render without re-subscribing the document listener.
 *
 * Nothing is observed while `enabled` is false - a closed surface has nothing
 * to dismiss, and a mounted-but-closed widget should not listen at all.
 */
export function useDismissOnOutsidePress({
  ownerDocument,
  isInside,
  onDismiss,
  enabled,
}: {
  /** The document the surface lives in; a Dockview popout has its own. */
  ownerDocument: Document | null;
  isInside(target: EventTarget | null): boolean;
  onDismiss(): void;
  enabled: boolean;
}): void {
  const latest = useRef({ isInside, onDismiss });
  latest.current = { isInside, onDismiss };
  useEffect(() => {
    if (!enabled || ownerDocument === null) return;
    function onMouseDown(event: MouseEvent) {
      if (latest.current.isInside(event.target)) return;
      latest.current.onDismiss();
    }
    ownerDocument.addEventListener("mousedown", onMouseDown);
    return () => ownerDocument.removeEventListener("mousedown", onMouseDown);
  }, [enabled, ownerDocument]);
}
