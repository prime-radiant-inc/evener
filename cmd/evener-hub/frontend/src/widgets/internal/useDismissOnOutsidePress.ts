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
 *   own click handlers have already run - the ordering that lets the pressed
 *   control keep its click. A control whose own handler calls
 *   stopPropagation() never reaches that bubble listener, so a CAPTURE-phase
 *   listener beside it reports the same click and dismisses on a later task,
 *   after dispatch. Capture is the fallback, never the dismissal itself:
 *   dismissing inside dispatch would collapse the surface before the control's
 *   own handlers ran.
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
    // Bumped by every press. A deferred fallback carries the value it was
    // scheduled under, so it cannot dismiss for a press that is no longer the
    // current one.
    let pressSeq = 0;
    // Set when the bubble listener dismissed this click. The capture fallback
    // below then knows its own deferred check has nothing left to do.
    let dismissed = false;
    let fallbackTimer: ReturnType<typeof setTimeout> | null = null;
    let live = true;
    function cancelFallback(): void {
      if (fallbackTimer === null) return;
      clearTimeout(fallbackTimer);
      fallbackTimer = null;
    }
    /** Whether this click is a dismissal: it began outside and ended outside. */
    function isDismissal(event: MouseEvent): boolean {
      return pressStartedOutside && !latest.current.isInside(event.target);
    }
    function onMouseDown(event: MouseEvent) {
      // A new press owns the gesture: the previous one's fallback, if it is
      // still queued, must not dismiss for a click that is no longer current.
      cancelFallback();
      dismissed = false;
      pressSeq += 1;
      pressStartedOutside = !latest.current.isInside(event.target);
    }
    // The ordinary path: a click that reaches the document in the bubble phase
    // is dismissed synchronously, after the target's own handlers have run.
    function onClick(event: MouseEvent) {
      const dismiss = isDismissal(event);
      pressStartedOutside = false;
      // A drag that began inside the surface is not a dismissal, and a click
      // that somehow ends inside it is not either.
      if (!dismiss) return;
      dismissed = true;
      cancelFallback();
      latest.current.onDismiss();
    }
    // The fallback for a control whose own click handler calls
    // stopPropagation(): that event never reaches the bubble listener, so the
    // surface would stay open until some later click. It is read in the
    // CAPTURE phase, which still reaches the document, but it cannot dismiss
    // there: capture runs before the target's own handlers, and collapsing that
    // early is the reflow/ordering hazard this mode exists to avoid. So it only
    // schedules a check for a later task, by which point the target's handlers
    // have run and the bubble listener (when the click was not stopped) has
    // already dismissed synchronously and set `dismissed`.
    function onClickCapture(event: MouseEvent) {
      if (!isDismissal(event)) return;
      const seq = pressSeq;
      cancelFallback();
      fallbackTimer = setTimeout(() => {
        fallbackTimer = null;
        if (!live || dismissed || pressSeq !== seq) return;
        // The press is spent: a later click with no press of its own is not a
        // dismissal.
        pressStartedOutside = false;
        latest.current.onDismiss();
      }, 0);
    }
    ownerDocument.addEventListener("mousedown", onMouseDown);
    ownerDocument.addEventListener("click", onClick);
    ownerDocument.addEventListener("click", onClickCapture, true);
    return () => {
      live = false;
      cancelFallback();
      ownerDocument.removeEventListener("mousedown", onMouseDown);
      ownerDocument.removeEventListener("click", onClick);
      ownerDocument.removeEventListener("click", onClickCapture, true);
    };
  }, [enabled, ownerDocument, when]);
}
