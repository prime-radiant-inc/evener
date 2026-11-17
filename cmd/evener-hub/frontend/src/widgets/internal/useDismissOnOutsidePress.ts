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
 * `pressInFlight` is written by "click" mode only: true from an outside press
 * until that press is spent (its click arrived, or the surface was disabled).
 * A caller whose own blur handler would collapse the surface needs it: the
 * browser moves focus as mousedown's DEFAULT action, after every mousedown
 * listener, so a press on a focusable control below the surface blurs the
 * surface before the release and a blur-driven collapse reflows the page at
 * the press - the same hazard "click" mode exists to avoid. Such a caller
 * holds its blur collapse while this is true and lets the completed click
 * dismiss instead. One source for the press, never a second listener.
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
  pressInFlight,
}: {
  /** The document the surface lives in; a Dockview popout has its own. */
  ownerDocument: Document | null;
  isInside(target: EventTarget | null): boolean;
  onDismiss(): void;
  enabled: boolean;
  /** When the dismissal runs; see the note above. Defaults to "press". */
  when?: "press" | "click";
  /**
   * Optional sink for whether an outside press is in flight; see the note
   * above. Written by "click" mode only, and reset to false on teardown.
   */
  pressInFlight?: { current: boolean };
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
    let releaseTimer: ReturnType<typeof setTimeout> | null = null;
    let live = true;
    function cancelFallback(): void {
      if (fallbackTimer === null) return;
      clearTimeout(fallbackTimer);
      fallbackTimer = null;
    }
    function cancelRelease(): void {
      if (releaseTimer === null) return;
      clearTimeout(releaseTimer);
      releaseTimer = null;
    }
    /** Drop a press whose gesture ended, publishing that it is no longer in flight. */
    function spendPress(): void {
      pressStartedOutside = false;
      setPressInFlight(false);
    }
    /** Publish the press state to a caller whose blur handler must hold. */
    function setPressInFlight(inFlight: boolean): void {
      if (pressInFlight !== undefined) pressInFlight.current = inFlight;
    }
    /** Whether this click is a dismissal: it began outside and ended outside. */
    function isDismissal(event: MouseEvent): boolean {
      return pressStartedOutside && !latest.current.isInside(event.target);
    }
    function onMouseDown(event: MouseEvent) {
      // A new press owns the gesture: the previous one's fallback, if it is
      // still queued, must not dismiss for a click that is no longer current.
      cancelFallback();
      cancelRelease();
      dismissed = false;
      pressSeq += 1;
      pressStartedOutside = !latest.current.isInside(event.target);
      // Published before the browser's focus move (mousedown's default action
      // runs after dispatch), so a caller's blur sees the press in flight.
      setPressInFlight(pressStartedOutside);
    }
    // A press that ends without a click (a text-selection drag, a scroll drag)
    // must not leave `pressInFlight` stuck true, or a caller holding its blur
    // collapse would ignore a later keyboard blur. A real press dispatches its
    // click synchronously after pointerup, so this later-task check runs only
    // when no click came. Registered only for a caller that asked for the
    // sink: surfaces that do not read it need no extra listener.
    function onPointerUpOrCancel(): void {
      if (pressInFlight === undefined || !pressStartedOutside) return;
      cancelRelease();
      releaseTimer = setTimeout(() => {
        releaseTimer = null;
        // A click that arrived in between spent the press already.
        if (!live || !pressStartedOutside) return;
        spendPress();
      }, 0);
    }
    // The ordinary path: a click that reaches the document in the bubble phase
    // is dismissed synchronously, after the target's own handlers have run.
    function onClick(event: MouseEvent) {
      const dismiss = isDismissal(event);
      cancelRelease();
      pressStartedOutside = false;
      // The press is spent whether or not it dismissed: a later blur with no
      // press of its own is not part of it.
      setPressInFlight(false);
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
        setPressInFlight(false);
        latest.current.onDismiss();
      }, 0);
    }
    ownerDocument.addEventListener("mousedown", onMouseDown);
    ownerDocument.addEventListener("click", onClick);
    ownerDocument.addEventListener("click", onClickCapture, true);
    if (pressInFlight !== undefined) {
      ownerDocument.addEventListener("pointerup", onPointerUpOrCancel, true);
      ownerDocument.addEventListener("pointercancel", onPointerUpOrCancel, true);
    }
    return () => {
      live = false;
      cancelFallback();
      cancelRelease();
      setPressInFlight(false);
      ownerDocument.removeEventListener("mousedown", onMouseDown);
      ownerDocument.removeEventListener("click", onClick);
      ownerDocument.removeEventListener("click", onClickCapture, true);
      ownerDocument.removeEventListener("pointerup", onPointerUpOrCancel, true);
      ownerDocument.removeEventListener("pointercancel", onPointerUpOrCancel, true);
    };
    // `pressInFlight` is a ref OBJECT, so a caller that keeps one ref for its
    // lifetime (the usual case) never re-subscribes here; a caller that swaps
    // sinks rebinds onto the new one.
  }, [enabled, ownerDocument, when, pressInFlight]);
}
