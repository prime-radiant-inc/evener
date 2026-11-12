import { useEffect, useRef } from "react";

/**
 * Dismiss a floating surface when a press lands outside it.
 *
 * The gesture is read at the PRESS, not the release: a reader selecting text
 * and dragging the pointer out of the surface must not dismiss it
 * mid-selection. Matches the overlay family's press-start rule
 * (widgets/dialog/OverlayPanel.tsx).
 *
 * `when` decides when the dismissal actually runs:
 *
 * - `"press"` dismisses on mousedown. Right for a floating layer that is out
 *   of flow (a portaled menu or popover): closing it changes no layout, so the
 *   control being pressed stays under the pointer and still receives its click.
 * - `"click"` records where the press started and dismisses on the completed
 *   click. Right for an IN-FLOW surface: collapsing it on mousedown reflows the
 *   page before mouseup, so a control below it moves out from under the pointer
 *   and its click is lost - the user has to click a second time. A click event
 *   only fires when press and release share a target, and by then that target's
 *   own click handlers have already run.
 *
 * `isInside` reports whether a target belongs to the surface. Each caller has
 * its own notion of "inside": a portaled panel finishes outside its trigger's
 * DOM subtree, and a disclosure whose conversation can be shown by two panels
 * at once must treat the other panel as inside too.
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
  when = "press",
}: {
  /** The document the surface lives in; a Dockview popout has its own. */
  ownerDocument: Document | null;
  isInside(target: EventTarget | null): boolean;
  onDismiss(): void;
  enabled: boolean;
  /** When the dismissal runs; see the note above. Defaults to "press". */
  when?: "press" | "click";
}): void {
  const latest = useRef({ isInside, onDismiss });
  latest.current = { isInside, onDismiss };
  useEffect(() => {
    if (!enabled || ownerDocument === null) return;
    if (when === "press") {
      function onMouseDown(event: MouseEvent) {
        if (latest.current.isInside(event.target)) return;
        latest.current.onDismiss();
      }
      ownerDocument.addEventListener("mousedown", onMouseDown);
      return () => ownerDocument.removeEventListener("mousedown", onMouseDown);
    }
    // "click": the press origin decides, and the dismissal waits for the
    // completed click so an in-flow collapse cannot swallow it.
    let pressStartedOutside = false;
    function onMouseDown(event: MouseEvent) {
      pressStartedOutside = !latest.current.isInside(event.target);
    }
    function onClick(event: MouseEvent) {
      const startedOutside = pressStartedOutside;
      pressStartedOutside = false;
      // A drag that began inside the surface is not a dismissal, and a click
      // that somehow ends inside it is not either.
      if (!startedOutside || latest.current.isInside(event.target)) return;
      latest.current.onDismiss();
    }
    ownerDocument.addEventListener("mousedown", onMouseDown);
    ownerDocument.addEventListener("click", onClick);
    return () => {
      ownerDocument.removeEventListener("mousedown", onMouseDown);
      ownerDocument.removeEventListener("click", onClick);
    };
  }, [enabled, ownerDocument, when]);
}
