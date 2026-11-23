// A pointer hold: movement past slopPx cancels it, and a completed hold's trailing click is consumed.
import type { MouseEvent as ReactMouseEvent, PointerEvent as ReactPointerEvent } from "react";
import { useCallback, useEffect, useRef } from "react";
import { POINTER_CLICK_WINDOW_MS } from "../hovercard/useFloatingLabel";
import { watchViewport } from "./viewportWatch";

/** How long a finger must rest before the hold fires. */
export const LONG_PRESS_MS = 500;

export interface LongPressOptions {
  onLongPress: () => void;
  holdMs?: number;
  slopPx?: number;
  enabled?: boolean;
  cancelOnVisibilityHidden?: boolean;
  /** End the press when the pointer leaves its trigger, as a lift would. */
  cancelOnPointerLeave?: boolean;
  /** End the press on a scroll or resize, pending or completed. */
  cancelOnViewportChange?: boolean;
  /** Capture the pointer at press start. A caller that ends on leave does not. */
  capturePointer?: boolean;
  /** The mounted card's current document, so an active gesture follows a move. */
  ownerDocument?: Document;
}

export interface LongPress {
  handlers: {
    onPointerDown: (event: ReactPointerEvent<HTMLElement>) => void;
    onPointerCancel: (event: ReactPointerEvent<HTMLElement>) => void;
    onLostPointerCapture: (event: ReactPointerEvent<HTMLElement>) => void;
    onPointerLeave: (event: ReactPointerEvent<HTMLElement>) => void;
  };
  cancel: () => void;
  clearClickSuppression: () => void;
  consumeClick: (event: ReactMouseEvent) => boolean;
}

export function useLongPress({
  onLongPress,
  holdMs = LONG_PRESS_MS,
  slopPx = 10,
  enabled = true,
  cancelOnVisibilityHidden = false,
  cancelOnPointerLeave = false,
  cancelOnViewportChange = false,
  capturePointer = true,
  ownerDocument,
}: LongPressOptions): LongPress {
  const holdTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const backstopTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const press = useRef<{ x: number; y: number; pointerId: number } | null>(null);
  const capture = useRef<{ element: Element; pointerId: number } | null>(null);
  const completed = useRef(false);
  const suppressClick = useRef(false);
  const gestureCleanup = useRef<(() => void) | null>(null);
  const gestureDocument = useRef<Document | null>(null);
  const visibilityCleanup = useRef<(() => void) | null>(null);
  const onLongPressRef = useRef(onLongPress);
  onLongPressRef.current = onLongPress;
  const clearHoldTimer = useCallback(() => {
    clearTimeout(holdTimer.current);
    holdTimer.current = undefined;
    visibilityCleanup.current?.();
    visibilityCleanup.current = null;
  }, []);

  // arm=false clears the window, arm=true starts it at the release click.
  const backstop = useCallback((arm: boolean) => {
    clearTimeout(backstopTimer.current);
    backstopTimer.current = undefined;
    if (!arm) return;
    backstopTimer.current = setTimeout(() => {
      backstopTimer.current = undefined;
      suppressClick.current = false;
    }, POINTER_CLICK_WINDOW_MS);
  }, []);

  const cancelHold = useCallback(() => {
    clearHoldTimer();
    press.current = null;
    gestureCleanup.current?.();
    gestureCleanup.current = null;
    gestureDocument.current = null;
    const captured = capture.current;
    capture.current = null;
    if (captured !== null) {
      try {
        captured.element.releasePointerCapture(captured.pointerId);
      } catch {
        // The element can be gone, and the capture already revoked.
      }
    }
    // A pending hold ends without opening anything; a completed one keeps its
    // click suppression until the click or its backstop consumes it.
    if (completed.current) return;
    suppressClick.current = false;
    backstop(false);
  }, [backstop, clearHoldTimer]);

  const abort = useCallback(() => {
    completed.current = false;
    cancelHold();
  }, [cancelHold]);
  const armGesture = useCallback(
    (doc: Document) => {
      const owns = (event: PointerEvent) => press.current?.pointerId === event.pointerId;
      const onMove = (event: PointerEvent) => {
        if (!owns(event) || completed.current) return;
        const start = press.current;
        if (start && Math.hypot(event.clientX - start.x, event.clientY - start.y) > slopPx) cancelHold();
      };
      const onUp = (event: PointerEvent) => {
        if (!owns(event)) return;
        cancelHold();
        if (suppressClick.current) backstop(true);
      };
      const onCancel = (event: PointerEvent) => {
        if (owns(event)) abort();
      };
      doc.addEventListener("pointermove", onMove);
      doc.addEventListener("pointerup", onUp);
      doc.addEventListener("pointercancel", onCancel);
      const stopViewport = cancelOnViewportChange ? watchViewport(doc, abort) : null;
      gestureDocument.current = doc;
      gestureCleanup.current = () => {
        doc.removeEventListener("pointermove", onMove);
        doc.removeEventListener("pointerup", onUp);
        doc.removeEventListener("pointercancel", onCancel);
        stopViewport?.();
      };
    },
    [abort, backstop, cancelHold, cancelOnViewportChange, slopPx],
  );

  const handlePointerDown = useCallback(
    (event: ReactPointerEvent<HTMLElement>) => {
      if (!enabled || event.button !== 0 || press.current !== null) return;
      completed.current = false;
      suppressClick.current = false;
      backstop(false);
      press.current = { x: event.clientX, y: event.clientY, pointerId: event.pointerId };
      const doc = event.currentTarget.ownerDocument ?? document;
      armGesture(doc);
      if (capturePointer) {
        try {
          event.currentTarget.setPointerCapture(event.pointerId);
          capture.current = { element: event.currentTarget, pointerId: event.pointerId };
        } catch {
          // jsdom has no pointer capture; the document listeners own the gesture.
        }
      }
      holdTimer.current = setTimeout(() => {
        clearHoldTimer();
        completed.current = true;
        suppressClick.current = true;
        onLongPressRef.current();
      }, holdMs);
      if (!cancelOnVisibilityHidden) return;
      // A hidden page loses the pointer, so the pending hold ends like an early release.
      const onVisibilityChange = () => {
        if (doc.visibilityState !== "hidden") return;
        abort();
      };
      doc.addEventListener("visibilitychange", onVisibilityChange);
      visibilityCleanup.current = () => doc.removeEventListener("visibilitychange", onVisibilityChange);
    },
    [abort, armGesture, backstop, cancelOnVisibilityHidden, capturePointer, clearHoldTimer, enabled, holdMs],
  );

  const handleLostPointerCapture = useCallback(() => {
    if (!capturePointer) return;
    if (holdTimer.current !== undefined) return abort();
    // A completed hold whose pointer never releases needs its window once the press ends.
    if (suppressClick.current && press.current === null) backstop(true);
  }, [abort, backstop, capturePointer]);

  const handlePointerLeave = useCallback(
    (event: ReactPointerEvent<HTMLElement>) => {
      if (!cancelOnPointerLeave || press.current?.pointerId !== event.pointerId) return;
      cancelHold();
      if (suppressClick.current) backstop(true);
    },
    [backstop, cancelHold, cancelOnPointerLeave],
  );

  // A second pointer's cancel must not end the gesture the first one owns.
  const handlePointerCancel = useCallback(
    (event: ReactPointerEvent<HTMLElement>) => {
      if (press.current?.pointerId !== event.pointerId) return;
      abort();
    },
    [abort],
  );

  const cancel = useCallback(() => {
    suppressClick.current = false;
    backstop(false);
    abort();
  }, [abort, backstop]);

  const clearClickSuppression = useCallback(() => {
    suppressClick.current = false;
    backstop(false);
  }, [backstop]);

  const consumeClick = useCallback(
    (event: ReactMouseEvent) => {
      if (!suppressClick.current) return false;
      suppressClick.current = false;
      backstop(false);
      event.preventDefault();
      event.stopPropagation();
      return true;
    },
    [backstop],
  );

  // The card can move to another document while the press runs, so the
  // cancellation pair follows it or a scroll there would never end the press.
  useEffect(() => {
    if (ownerDocument === undefined || press.current === null) return;
    if (gestureDocument.current === ownerDocument) return;
    gestureCleanup.current?.();
    gestureCleanup.current = null;
    armGesture(ownerDocument);
  }, [armGesture, ownerDocument]);

  useEffect(
    () => () => {
      cancelHold();
      backstop(false);
    },
    [backstop, cancelHold],
  );

  return {
    handlers: {
      onPointerDown: handlePointerDown,
      onPointerCancel: handlePointerCancel,
      onLostPointerCapture: handleLostPointerCapture,
      onPointerLeave: handlePointerLeave,
    },
    cancel,
    clearClickSuppression,
    consumeClick,
  };
}
