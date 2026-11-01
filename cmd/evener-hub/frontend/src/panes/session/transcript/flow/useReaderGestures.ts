import { type RefObject, useCallback, useEffect, useRef } from "react";
import { isEditableTarget, isIMECompositionKeydown } from "../../../../keybindings/dispatcher";
import type { VirtualListHandle } from "../../../../widgets";
import type { ScrollMetrics } from "./scrollMetrics";
import { DRAG_BUTTON_BITS, MIDDLE_BUTTON_BIT, verticalInputCanMovePort } from "./scrollPredicates";
import type { PendingViewAnchor } from "./viewAnchor";

export interface UseReaderGesturesOptions {
  listRef: RefObject<VirtualListHandle | null>;
  measure: (el: HTMLElement) => ScrollMetrics;
  /** Frames the pending marker has left; the scroll listener reads it to classify an event. */
  gestureFramesLeftRef: RefObject<number>;
  readerGestureRef: RefObject<{ beforeOffset: number; admitted: boolean } | undefined>;
  readerCallbacksRef: RefObject<{ onReaderIntent?: () => void; onReaderMovement?: (beforeOffset: number) => void }>;
  /** The previous measurement's geometry; a wheel is attributed to the position the reader left. */
  lastScrollGeometryRef: RefObject<ScrollMetrics>;
  /** Cleared when the reader takes over: their gesture supersedes these pending requests. */
  pendingViewAnchorRef: RefObject<PendingViewAnchor | null>;
  pendingBottomJumpRef: RefObject<{ element: HTMLElement; scrollHeight: number; clientHeight: number } | null>;
  pendingInitialEndRef: RefObject<string | null>;
}

/**
 * The reader-gesture markers behind the bottom-hold correction: which wheel,
 * touch, key and pointer input counts as the reader moving the transcript, and
 * the handlers that record it. useTranscriptScroll attaches the returned
 * handlers to the scroll port and reads the returned state when it classifies
 * a scroll event.
 */
export function useReaderGestures({
  listRef,
  measure,
  gestureFramesLeftRef,
  readerGestureRef,
  readerCallbacksRef,
  lastScrollGeometryRef,
  pendingViewAnchorRef,
  pendingBottomJumpRef,
  pendingInitialEndRef,
}: UseReaderGesturesOptions) {
  const gestureClearFrameRef = useRef<number | null>(null);
  const pointerDraggingRef = useRef(false);
  const middleButtonHeldRef = useRef(false);
  const lastTouchYRef = useRef<number | null>(null);
  // Announce that the reader is moving the transcript right now. Cleared on the
  // next animation frame, which is exactly "this frame": a frame's scroll steps
  // run BEFORE its requestAnimationFrame callbacks, so every scroll event the
  // gesture can be responsible for is delivered while the flag is up, and none
  // of the next frame's corrections see it.
  //
  // That frame may never arrive - requestAnimationFrame does not run while the
  // tab is hidden, and the first frame after it becomes visible again can be
  // arbitrarily far away - so the scroll listener also CONSUMES the flag on the
  // event it explains (one whose content and viewport heights held), and the
  // document going hidden clears it outright. Between the three, a gesture
  // vetoes the event it caused and, within its frames, the measurement
  // corrections that land before that event - nothing later.
  //
  // Over-marking is the dangerous direction, not under-marking. A veto falls
  // through to the ordinary path below, which records the reader as away from
  // the bottom, so the at-bottom clause fails for every later correction until
  // the reader actually returns to the bottom: one false veto reinstates the
  // mount strand permanently, rather than costing a single frame.
  //
  // A native scrolling key's marker lasts three frames. The browser's smooth
  // scroll for the key starts a frame or two after it, so its first scroll
  // event arrives after one or two frame clears (Chrome: usually 20ms after
  // Shift-Space's keydown, #3880, and sometimes 37.8ms, #4065). An instant key
  // scroll consumes the marker in the first frame.
  // biome-ignore lint/correctness/useExhaustiveDependencies: the ref arguments are stable useRef objects owned by useTranscriptScroll
  const markGesture = useCallback(
    (readerIntent = true, beforeOffset?: number, frames = 1) => {
      const el = listRef.current?.getScrollElement();
      if (readerIntent && el) {
        pendingViewAnchorRef.current = null;
        pendingBottomJumpRef.current = null;
        readerCallbacksRef.current.onReaderIntent?.();
        if (beforeOffset !== undefined && Number.isFinite(beforeOffset) && el.scrollTop !== beforeOffset)
          readerCallbacksRef.current.onReaderMovement?.(beforeOffset);
        readerGestureRef.current = { beforeOffset: el.scrollTop, admitted: true };
      }
      pendingInitialEndRef.current = null;
      gestureFramesLeftRef.current = Math.max(gestureFramesLeftRef.current, frames);
      if (gestureClearFrameRef.current !== null) return;
      const clearOnFrame = () => {
        gestureClearFrameRef.current = requestAnimationFrame(() => {
          gestureFramesLeftRef.current -= 1;
          if (gestureFramesLeftRef.current > 0) return clearOnFrame();
          gestureFramesLeftRef.current = 0;
          gestureClearFrameRef.current = null;
          readerGestureRef.current = undefined;
        });
      };
      clearOnFrame();
    },
    [listRef],
  );
  // Drops a pending marker and its clearing frame, for when no scroll it could
  // explain is still to come: the document went hidden, or the session changed.
  // biome-ignore lint/correctness/useExhaustiveDependencies: the ref arguments are stable useRef objects owned by useTranscriptScroll
  const forgetPendingMarker = useCallback(() => {
    gestureFramesLeftRef.current = 0;
    readerGestureRef.current = undefined;
    if (gestureClearFrameRef.current !== null) {
      cancelAnimationFrame(gestureClearFrameRef.current);
      gestureClearFrameRef.current = null;
    }
  }, []);
  // How exact each marker is, since over-marking is the harmful direction:
  //
  //   scroll chords - EXACT. useTranscriptScrollKeys writes the offset itself
  //     and marks only when the write moved it (its scrollPortBy).
  //   wheel         - a sideways wheel scrolls nothing here and never marks,
  //     whether it says so with a zero deltaY or with shiftKey; nor does a zoom
  //     gesture (ctrl-wheel) or one a descendant has already claimed via
  //     preventDefault. A vertical one marks only when
  //     verticalInputCanMovePort says it can reach and move the port: not into a
  //     limit the port is already at, and not when a nested scroller will answer
  //     it instead.
  //   touch         - the same predicate, on real vertical movement only, so a
  //     sideways swipe and a swipe a nested scroller answers are both ignored.
  //   native keys   - the wheel's predicate, by key direction. The one marker
  //     that lasts three frames, since the key's smooth scroll starts a frame or
  //     two late, so a key whose scroll never starts can veto a correction in
  //     each of them.
  //   pointer drag  - the LEAST exact, and deliberately kept: a selection drag
  //     that moves without scrolling marks a gesture, which no handler can tell
  //     from a scrollbar drag that does scroll. The PRIMARY button marks only
  //     while the pointer moves, so a held one is never a standing veto; a held
  //     MIDDLE button is an autoscroll in progress and vetoes for as long as it
  //     is down, stationary pointer included. Ruled out are a finger (it has the
  //     touch path, which is more exact), a secondary button, a drag that is
  //     over (no button held), and one that has left the port.
  // biome-ignore lint/correctness/useExhaustiveDependencies: the ref arguments are stable useRef objects owned by useTranscriptScroll
  const startPointerDrag = useCallback((event: PointerEvent) => {
    // A finger produces BOTH event streams. The touch path knows about
    // direction and nested scrollers; adopting the same finger here as a drag
    // would mark every sideways swipe and undo that exactness.
    //
    // The primary button drags a scrollbar or a selection; the middle button is
    // native autoscroll, which scrolls the port continuously for as long as it
    // is held - so leaving it out costs a re-pin on every correction that lands
    // while it is moving, not a single frame. The secondary button opens a menu
    // and never scrolls. Where the middle button does nothing at all, this marks
    // a drag that moves nothing: the same trade the path already makes for a
    // selection drag.
    //
    // The two buttons are tracked differently, because their held states mean
    // opposite things.
    //
    // A mark comes from pointermove and lasts one frame, which is the whole
    // story for the PRIMARY button: a selection drag holds it for as long as the
    // reader is choosing text and scrolls nothing while they pause, so a held
    // primary button must never be a standing veto - that is the permanent false
    // veto this design treats as the harmful direction.
    //
    // A held MIDDLE button is the opposite: native autoscroll scrolls the port
    // continuously while the pointer sits still, producing a stream of scroll
    // events with no pointermove to mark any of them. So it is tracked as a
    // state rather than an event, and the scroll listener reads it. Being a
    // state, it is bounded by its clears rather than by any clock: the button
    // coming up, a move without it, the pointer leaving the port, the pane
    // switching session, and focus or visibility going away.
    //
    // Two trades come with it. On a platform where a middle-button hold does
    // nothing at all it is a standing veto over a transcript that is not moving,
    // which needs the reader to press and hold the middle button over the
    // transcript while content grows. And because pointerleave is one of the
    // clears, an autoscroll that carries on with the cursor outside the port
    // goes unmarked from then on - under-marking, the safe side, and the same
    // trade pointerleave already makes for a drag.
    if (event.pointerType === "touch") return;
    if ((event.button !== 0 && event.button !== 1) || !event.isPrimary) return;
    if (event.button === 1) middleButtonHeldRef.current = true;
    pointerDraggingRef.current = true;
    const port = event.currentTarget;
    if (port instanceof HTMLElement) readerGestureRef.current = { beforeOffset: port.scrollTop, admitted: false };
  }, []);
  // A mouse pointer gets no implicit capture, so a drag released outside the
  // port never delivers pointerup to it, and a drag that started elsewhere can
  // wander in with its button still down. Two answers, neither alone complete:
  // the held button ends a drag wherever it was released, and leaving the port
  // ends it too, so a foreign drag entering later is not adopted. The trade is
  // that a drag which leaves the port and returns - an edge auto-scroll - stops
  // marking; under-marking is the safe direction here.
  const continuePointerDrag = useCallback(
    (event: PointerEvent) => {
      // Ahead of both reading AND writing the flag: a finger must neither mark
      // a mouse drag nor end one.
      if (event.pointerType === "touch") return;
      // The middle bit going away ends the autoscroll even when another button
      // is still down, and even when the release never reaches this port.
      if ((event.buttons & MIDDLE_BUTTON_BIT) === 0) middleButtonHeldRef.current = false;
      // A move carrying neither button a drag is begun for ends the drag rather
      // than continuing it. Testing the mask, not just "some button is down",
      // is what stops a secondary drag inheriting a primary one the port never
      // saw end: the flag survives a blur, and every later right-button move
      // would otherwise mark from a button the drag path never accepted.
      if ((event.buttons & DRAG_BUTTON_BITS) === 0) {
        pointerDraggingRef.current = false;
        return;
      }
      if (pointerDraggingRef.current) markGesture(false);
    },
    [markGesture],
  );
  // Deliberately NOT filtered by pointerType, unlike the two above: ending a
  // drag is the under-marking direction, so a stray touch pointerup clearing a
  // mouse drag costs an unmarked scroll. Ignoring it would keep the drag marked
  // only until the mouse's own pointerup, its next button-free move, or a
  // pointerleave - a short window, not a latch, but still the over-marking side
  // of the trade.
  const endPointerDrag = useCallback(() => {
    pointerDraggingRef.current = false;
    middleButtonHeldRef.current = false;
  }, []);
  // The autoscroll state is the only gesture state with no clock and nothing to
  // consume it, so everything that ends it has to be named. A middle press over
  // the transcript can be released anywhere - once focus leaves, the port sees
  // no pointerup at all - and until something says otherwise this would keep
  // vetoing, with every vetoed correction disarming the re-pin. Losing focus and
  // going hidden are both "the release, wherever it happens, is not ours to
  // see". pointerDraggingRef needs no equivalent: it only acts on a later
  // pointermove, which carries its own buttons.
  const endAutoscrollOnFocusLoss = useCallback(() => {
    middleButtonHeldRef.current = false;
  }, []);
  // A hidden document stops the clearing frame as well as hiding the scroll, so
  // it bounds the pending MARKER too, not just the autoscroll: a drag marks
  // without producing any scroll event of its own, and that marker would
  // otherwise survive until the first scroll after return - which, if content
  // grew meanwhile, is the measurement correction it would then veto. Blur needs
  // no equivalent: a blurred window is still rendering, so the frame boundary is
  // still the marker's bound there.
  const forgetGesturesWhenHidden = useCallback(() => {
    if (document.visibilityState !== "hidden") return;
    middleButtonHeldRef.current = false;
    forgetPendingMarker();
  }, [forgetPendingMarker]);
  // biome-ignore lint/correctness/useExhaustiveDependencies: the ref arguments are stable useRef objects owned by useTranscriptScroll
  const markWheel = useCallback(
    (event: WheelEvent) => {
      // None of these move this port. ctrl-wheel is the browser's zoom gesture,
      // and a macOS trackpad pinch arrives as one. shift-wheel is horizontal,
      // and the port is overflow-x:clip (virtuallist.module.css) so it pans in
      // no engine - engines disagree on whether they zero deltaY or leave it set
      // while scrolling sideways, and reading shiftKey needs no such guess. A
      // wheel a descendant already claimed will not reach the port either.
      if (event.ctrlKey || event.shiftKey || event.defaultPrevented) return;
      if (event.deltaY === 0) return;
      const port = event.currentTarget;
      if (!(port instanceof HTMLElement)) return;
      if (!verticalInputCanMovePort(port, measure(port), event.target, event.deltaY > 0 ? 1 : -1)) return;
      markGesture(true, lastScrollGeometryRef.current.scrollTop);
    },
    [markGesture, measure],
  );
  const startTouch = useCallback((event: TouchEvent) => {
    lastTouchYRef.current = event.touches[0]?.clientY ?? null;
  }, []);
  const continueTouch = useCallback(
    (event: TouchEvent) => {
      const y = event.touches[0]?.clientY ?? null;
      const last = lastTouchYRef.current;
      lastTouchYRef.current = y;
      // A first move with no recorded start (the touch began outside the port)
      // is not marked; the next one with real movement is.
      if (y === null || last === null || y === last) return;
      const port = event.currentTarget;
      if (!(port instanceof HTMLElement)) return;
      // A finger moving UP - clientY decreasing - pushes content down.
      if (!verticalInputCanMovePort(port, measure(port), event.target, y < last ? 1 : -1)) return;
      markGesture();
    },
    [markGesture, measure],
  );
  // A finished touch has to clear the recorded Y, or the "no recorded start"
  // guard above is true exactly once per mount and every later outside-start
  // move compares against a stale value.
  const endTouch = useCallback(() => {
    lastTouchYRef.current = null;
  }, []);
  const markNativeKey = useCallback(
    (event: KeyboardEvent) => {
      const space = event.key === " ";
      if (
        event.defaultPrevented ||
        event.altKey ||
        event.ctrlKey ||
        event.metaKey ||
        (event.shiftKey && !space) ||
        isIMECompositionKeydown(event) ||
        isEditableTarget(event.target) ||
        (space && event.target instanceof Element && event.target.closest("button, summary"))
      )
        return;
      let direction: -1 | 1 | undefined;
      if (space) {
        direction = event.shiftKey ? -1 : 1;
      } else if (event.key === "ArrowUp" || event.key === "PageUp" || event.key === "Home") {
        direction = -1;
      } else if (event.key === "ArrowDown" || event.key === "PageDown" || event.key === "End") {
        direction = 1;
      }
      const port = event.currentTarget;
      if (
        direction === undefined ||
        !(port instanceof HTMLElement) ||
        !verticalInputCanMovePort(port, measure(port), event.target, direction)
      )
        return;
      markGesture(true, undefined, 3);
      // markGesture's reader intent supersedes the pending capture - and, with
      // it, the movement record - so the key's own backward direction has to be
      // armed AFTER it. A forward key keeps waiting for its event, as before.
      if (direction === -1 && listRef.current?.getScrollElement() === port) listRef.current.noteBackwardMovement?.();
    },
    [listRef, markGesture, measure],
  );
  useEffect(
    () => () => {
      if (gestureClearFrameRef.current !== null) cancelAnimationFrame(gestureClearFrameRef.current);
    },
    [],
  );

  return {
    pointerDraggingRef,
    middleButtonHeldRef,
    lastTouchYRef,
    markGesture,
    forgetPendingMarker,
    markNativeKey,
    markWheel,
    startTouch,
    continueTouch,
    endTouch,
    startPointerDrag,
    continuePointerDrag,
    endPointerDrag,
    endAutoscrollOnFocusLoss,
    forgetGesturesWhenHidden,
  };
}
