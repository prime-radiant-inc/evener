// A pointer hold: movement past slopPx cancels it, and a completed hold's trailing click is consumed.
import type { MouseEvent as ReactMouseEvent, PointerEvent as ReactPointerEvent } from "react";
import { useCallback, useEffect, useRef } from "react";
import { LONG_PRESS_MS, POINTER_CLICK_WINDOW_MS } from "../hovercard";

export interface LongPressOptions {
  onLongPress: () => void;
  slopPx?: number;
  enabled?: boolean;
  cancelOnVisibilityHidden?: boolean;
}
export interface LongPress {
  handlers: {
    onPointerDown: (event: ReactPointerEvent<HTMLElement>) => void;
    onPointerCancel: (event: ReactPointerEvent<HTMLElement>) => void;
    onLostPointerCapture: (event: ReactPointerEvent<HTMLElement>) => void;
  };
  cancel: () => void;
  consumeClick: (event: ReactMouseEvent) => boolean;
}

export function useLongPress({
  onLongPress,
  slopPx = 10,
  enabled = true,
  cancelOnVisibilityHidden = false,
}: LongPressOptions): LongPress {
  const holdTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const backstopTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const press = useRef<{ x: number; y: number; pointerId: number } | null>(null);
  const capture = useRef<{ element: Element; pointerId: number } | null>(null);
  const completed = useRef(false);
  const suppressClick = useRef(false);
  const gestureCleanup = useRef<(() => void) | null>(null);
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
      gestureCleanup.current = () => {
        doc.removeEventListener("pointermove", onMove);
        doc.removeEventListener("pointerup", onUp);
        doc.removeEventListener("pointercancel", onCancel);
      };
    },
    [abort, backstop, cancelHold, slopPx],
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
      try {
        event.currentTarget.setPointerCapture(event.pointerId);
        capture.current = { element: event.currentTarget, pointerId: event.pointerId };
      } catch {
        // jsdom has no pointer capture; the document listeners own the gesture.
      }
      holdTimer.current = setTimeout(() => {
        clearHoldTimer();
        completed.current = true;
        suppressClick.current = true;
        onLongPressRef.current();
      }, LONG_PRESS_MS);
      if (!cancelOnVisibilityHidden) return;
      // A hidden page loses the pointer, so the pending hold ends like an early release.
      const onVisibilityChange = () => {
        if (doc.visibilityState !== "hidden") return;
        abort();
      };
      doc.addEventListener("visibilitychange", onVisibilityChange);
      visibilityCleanup.current = () => doc.removeEventListener("visibilitychange", onVisibilityChange);
    },
    [abort, armGesture, backstop, cancelOnVisibilityHidden, clearHoldTimer, enabled],
  );

  const handleLostPointerCapture = useCallback(() => {
    if (holdTimer.current !== undefined) return abort();
    // A completed hold whose pointer never releases needs its window once the press ends.
    if (suppressClick.current && press.current === null) backstop(true);
  }, [abort, backstop]);

  const cancel = useCallback(() => {
    suppressClick.current = false;
    backstop(false);
    abort();
  }, [abort, backstop]);

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
      onPointerCancel: abort,
      onLostPointerCapture: handleLostPointerCapture,
    },
    cancel,
    consumeClick,
  };
}
