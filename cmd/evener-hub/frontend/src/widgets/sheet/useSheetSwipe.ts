import {
  type PointerEvent as ReactPointerEvent,
  type RefObject,
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
} from "react";
import { ownerDocumentOf, ownerWindowOf } from "../internal/documentRealm";

const VELOCITY_WINDOW_MS = 100;
const HOLD_CUTOFF_MS = 80;
const PROJECTION_MS = 200;
const LOCK_DISTANCE_PX = 8;
const SCROLL_SETTLE_MS = 180;
const DISMISS_RATIO = 0.35;
const MAX_DISMISS_THRESHOLD_PX = 160;
const FALLBACK_DISMISS_THRESHOLD_PX = 120;
const EXPANDABLE_DRAG_PX = 50;
const EXPANDABLE_TAP_SLOP_PX = 10;
const DIRECTION_REVERSAL_SLOP_PX = 2;

const INTERACTIVE_EXCLUDE_SELECTOR = 'input, textarea, select, [contenteditable]:not([contenteditable="false"])';
const HEADER_INTERACTIVE_EXCLUDE_SELECTOR = `${INTERACTIVE_EXCLUDE_SELECTOR}, button, a[href]`;

export interface UseSheetSwipeOptions {
  open: boolean;
  side: "right" | "bottom" | "left";
  onClose: () => void;
  panelRef: RefObject<HTMLDivElement | null>;
  bodyRef: RefObject<HTMLDivElement | null>;
  scrimRef: RefObject<HTMLDivElement | null>;
  expandable?: {
    geometry: "peek" | "full";
    onGeometryChange: (next: "peek" | "full" | ((prev: "peek" | "full") => "peek" | "full")) => void;
  };
}

export interface UseSheetSwipeResult {
  onHandlePointerDown: (e: ReactPointerEvent) => void;
  onHeaderPointerDown: (e: ReactPointerEvent) => void;
}

interface VelocitySample {
  y: number;
  time: number;
}

type GestureState =
  | { kind: "idle" }
  | {
      kind: "pendingBody";
      touchId: number;
      startX: number;
      startY: number;
      startTime: number;
      target: EventTarget | null;
    }
  | { kind: "scrollOwned"; touchId: number }
  | {
      kind: "touchDrag";
      touchId: number;
      startY: number;
      offsetY: number;
      panelHeight: number;
      samples: VelocitySample[];
    }
  | {
      kind: "pointerDrag";
      pointerId: number;
      startX: number;
      startY: number;
      axis: "pending" | "vertical" | "horizontal";
      offsetY: number;
      panelHeight: number;
      samples: VelocitySample[];
      hasTranslated: boolean;
      expandableMode: "peek" | "full" | undefined;
      maxAbsDelta: number;
      switchedGeometry: boolean;
      needsResetOnRelease: boolean;
    };

interface SwipeController {
  gesture: GestureState;
  lastScrollAt: number | null;
  cancelPointer: (() => void) | null;
  cancelDeclinedGuard: (() => void) | null;
}

function clearCallback(ctrl: SwipeController, key: "cancelPointer" | "cancelDeclinedGuard"): void {
  ctrl[key]?.();
  ctrl[key] = null;
}

function cancelAll(ctrl: SwipeController): void {
  clearCallback(ctrl, "cancelDeclinedGuard");
  clearCallback(ctrl, "cancelPointer");
}

function targetElement(target: EventTarget | null): Element | null {
  const node = target as Node | null;
  if (node === null) return null;
  return node.nodeType === 1 ? (node as Element) : node.parentElement;
}

function isExcludedTarget(target: EventTarget | null, inHeader: boolean): boolean {
  const el = targetElement(target);
  if (el === null) return false;
  return el.closest(inHeader ? HEADER_INTERACTIVE_EXCLUDE_SELECTOR : INTERACTIVE_EXCLUDE_SELECTOR) !== null;
}

function hasScrolledAncestor(target: EventTarget | null, panel: HTMLElement): boolean {
  let current: Node | null = target as Node | null;
  while (current !== null) {
    if (current.nodeType === 1 && (current as HTMLElement).scrollTop > 1) return true;
    if (current === panel) break;
    current = current.parentNode;
  }
  return false;
}

function findTouchById(list: TouchList, id: number): Touch | undefined {
  return Array.from(list).find((touch) => (touch.identifier ?? 0) === id);
}

function isTouchActive(event: TouchEvent, touchId: number): boolean {
  return (
    findTouchById(event.touches, touchId) !== undefined && findTouchById(event.changedTouches, touchId) === undefined
  );
}

function dismissThreshold(panelHeight: number): number {
  return panelHeight > 0
    ? Math.min(panelHeight * DISMISS_RATIO, MAX_DISMISS_THRESHOLD_PX)
    : FALLBACK_DISMISS_THRESHOLD_PX;
}

function pushSample(samples: VelocitySample[], y: number, time: number): void {
  while (samples[0] !== undefined && time - samples[0].time > VELOCITY_WINDOW_MS) {
    samples.shift();
  }
  const first = samples[0];
  if (first !== undefined && samples.length >= 2) {
    let peak = first;
    let trough = first;
    let peakFromDown = false;
    let troughFromUp = false;
    for (const s of samples) {
      if (s.y >= peak.y) {
        peakFromDown = trough.y < s.y;
        peak = s;
      }
      if (s.y <= trough.y) {
        troughFromUp = peak.y > s.y;
        trough = s;
      }
    }
    // Reset window to the extremum once cumulative reversal exceeds slop (handles 120 Hz small frames).
    if (peakFromDown && peak.y - y > DIRECTION_REVERSAL_SLOP_PX) samples.splice(0, samples.length, peak);
    else if (troughFromUp && y - trough.y > DIRECTION_REVERSAL_SLOP_PX) samples.splice(0, samples.length, trough);
  }
  samples.push({ y, time });
}

function computeVelocityY(samples: VelocitySample[], releaseTime: number): number {
  const last = samples[samples.length - 1];
  const first = samples[0];
  if (last === undefined || first === undefined || samples.length < 2) return 0;
  if (releaseTime - last.time > HOLD_CUTOFF_MS) return 0;
  const dt = last.time - first.time;
  return dt >= 1 ? (last.y - first.y) / dt : 0;
}

function clearDragState(panel: HTMLElement | null, scrim: HTMLElement | null): void {
  if (panel !== null) {
    delete panel.dataset.swiping;
    panel.style.transform = "";
  }
  if (scrim !== null) scrim.style.opacity = "";
}

function applyDragStyles(panel: HTMLElement, scrim: HTMLElement | null, offsetY: number, panelHeight: number): void {
  panel.dataset.swiping = "true";
  panel.style.transform = `translateY(${offsetY}px)`;
  if (scrim !== null) {
    const ratio = offsetY / Math.max(panelHeight, 1);
    scrim.style.opacity = String(Math.max(0.4, 1 - ratio * 0.6));
  }
}

function settleSwipeDrag(
  ctrl: SwipeController,
  latestRef: RefObject<{ open: boolean; onClose: () => void }>,
  state: { offsetY: number; panelHeight: number; samples: VelocitySample[] },
  panel: HTMLElement,
  scrimRef: RefObject<HTMLDivElement | null>,
  cancelled: boolean,
): void {
  const scrim = scrimRef.current;
  if (!cancelled && state.offsetY > 0) {
    const threshold = dismissThreshold(state.panelHeight);
    const projectedY = state.offsetY + computeVelocityY(state.samples, performance.now()) * PROJECTION_MS;
    if (projectedY >= threshold) {
      latestRef.current.onClose();
      clearCallback(ctrl, "cancelDeclinedGuard");
      const win = ownerWindowOf(panel);
      const rafId = win.requestAnimationFrame(() => {
        ctrl.cancelDeclinedGuard = null;
        if (latestRef.current.open && ctrl.gesture.kind !== "touchDrag" && ctrl.gesture.kind !== "pointerDrag") {
          clearDragState(panel, scrimRef.current);
        }
      });
      ctrl.cancelDeclinedGuard = () => win.cancelAnimationFrame(rafId);
      return;
    }
  }
  clearDragState(panel, scrim);
}

/**
 * Swipe-down dismissal for `side="bottom"` Sheets, unified with expandable
 * `peek`/`full` handle dragging so every handle/header gesture uses one
 * `pointermove`/`pointerup` pair on `ownerWindowOf(panel)` while body touches
 * run on non-passive touch listeners on `panel`.
 */
export function useSheetSwipe({
  open,
  side,
  onClose,
  panelRef,
  bodyRef,
  scrimRef,
  expandable,
}: UseSheetSwipeOptions): UseSheetSwipeResult {
  const latestRef = useRef({ open, side, onClose, expandable });
  latestRef.current = { open, side, onClose, expandable };

  const ctrlRef = useRef<SwipeController>({
    gesture: { kind: "idle" },
    lastScrollAt: null,
    cancelPointer: null,
    cancelDeclinedGuard: null,
  });

  const prevStateRef = useRef({ open, side });
  useLayoutEffect(() => {
    const prev = prevStateRef.current;
    prevStateRef.current = { open, side };
    const shouldResetStyles = (open && !prev.open) || (open && prev.side === "bottom" && side !== "bottom");
    if (!open || shouldResetStyles) {
      cancelAll(ctrlRef.current);
      ctrlRef.current.gesture = { kind: "idle" };
    }
    if (shouldResetStyles) {
      clearDragState(panelRef.current, scrimRef.current);
    }
  }, [open, side, panelRef, scrimRef]);

  useEffect(() => () => cancelAll(ctrlRef.current), []);

  useEffect(() => {
    if (!open || side !== "bottom") return;
    const panel = panelRef.current;
    if (panel === null) return;
    const doc = ownerDocumentOf(panel);
    const ctrl = ctrlRef.current;

    const inCooldown = () => ctrl.lastScrollAt !== null && performance.now() - ctrl.lastScrollAt < SCROLL_SETTLE_MS;

    const onScroll = () => {
      ctrl.lastScrollAt = performance.now();
      if (ctrl.gesture.kind === "pendingBody") {
        ctrl.gesture = { kind: "scrollOwned", touchId: ctrl.gesture.touchId };
      }
    };

    const onTouchStart = (event: TouchEvent) => {
      const { open: isOpen, side: currentSide, expandable: exp } = latestRef.current;
      if (!isOpen || currentSide !== "bottom" || exp?.geometry === "full") return;
      if (ctrl.gesture.kind !== "idle") {
        if (ctrl.gesture.kind === "pointerDrag") return;
        if (event.touches.length > 1 && isTouchActive(event, ctrl.gesture.touchId)) return;
        if (ctrl.gesture.kind === "touchDrag") clearDragState(panel, scrimRef.current);
        ctrl.gesture = { kind: "idle" };
      }
      if (event.touches.length !== 1) return;
      const body = bodyRef.current;
      const targetEl = targetElement(event.target);
      if (body === null || targetEl === null || !body.contains(targetEl)) return;
      if (isExcludedTarget(event.target, false)) return;

      const touch = event.touches[0];
      if (touch === undefined) return;
      const touchId = touch.identifier ?? 0;

      if (hasScrolledAncestor(event.target, panel) || inCooldown()) {
        ctrl.gesture = { kind: "scrollOwned", touchId };
        return;
      }

      ctrl.gesture = {
        kind: "pendingBody",
        touchId,
        startX: touch.clientX,
        startY: touch.clientY,
        startTime: performance.now(),
        target: event.target,
      };
    };

    const onTouchMove = (event: TouchEvent) => {
      let state = ctrl.gesture;
      if (state.kind === "idle" || state.kind === "scrollOwned") return;
      if (state.kind === "pointerDrag") {
        if (event.cancelable) event.preventDefault();
        return;
      }

      const touch = findTouchById(event.touches, state.touchId) ?? findTouchById(event.changedTouches, state.touchId);
      if (touch === undefined) return;

      if (state.kind === "pendingBody") {
        const dx = touch.clientX - state.startX;
        const dy = touch.clientY - state.startY;
        if (hasScrolledAncestor(state.target, panel) || inCooldown()) {
          ctrl.gesture = { kind: "scrollOwned", touchId: state.touchId };
          return;
        }
        if (Math.abs(dx) <= LOCK_DISTANCE_PX && Math.abs(dy) <= LOCK_DISTANCE_PX) return;
        if (dy < 0 || Math.abs(dx) >= dy) {
          ctrl.gesture = { kind: "scrollOwned", touchId: state.touchId };
          return;
        }

        clearCallback(ctrl, "cancelDeclinedGuard");
        state = ctrl.gesture = {
          kind: "touchDrag",
          touchId: state.touchId,
          startY: state.startY,
          offsetY: 0,
          panelHeight: panel.getBoundingClientRect().height,
          samples: [{ y: state.startY, time: state.startTime }],
        };
      }

      if (event.cancelable) event.preventDefault();
      const offsetY = Math.max(0, touch.clientY - state.startY);
      state.offsetY = offsetY;
      pushSample(state.samples, touch.clientY, performance.now());
      applyDragStyles(panel, scrimRef.current, offsetY, state.panelHeight);
    };

    const finishTouch = (event: TouchEvent, cancelled: boolean) => {
      const state = ctrl.gesture;
      if (state.kind === "idle" || state.kind === "pointerDrag") return;
      if (isTouchActive(event, state.touchId)) return;

      ctrl.gesture = { kind: "idle" };
      if (state.kind === "touchDrag") settleSwipeDrag(ctrl, latestRef, state, panel, scrimRef, cancelled);
    };

    const onTouchEnd = (event: TouchEvent) => finishTouch(event, false);
    const onTouchCancel = (event: TouchEvent) => finishTouch(event, true);

    panel.addEventListener("touchstart", onTouchStart, { passive: false });
    panel.addEventListener("touchmove", onTouchMove, { passive: false });
    doc.addEventListener("touchend", onTouchEnd, { capture: true, passive: true });
    doc.addEventListener("touchcancel", onTouchCancel, { capture: true, passive: true });
    panel.addEventListener("scroll", onScroll, { capture: true, passive: true });

    return () => {
      panel.removeEventListener("touchstart", onTouchStart);
      panel.removeEventListener("touchmove", onTouchMove);
      doc.removeEventListener("touchend", onTouchEnd, { capture: true });
      doc.removeEventListener("touchcancel", onTouchCancel, { capture: true });
      panel.removeEventListener("scroll", onScroll, { capture: true });
    };
  }, [open, side, panelRef, bodyRef, scrimRef]);

  const startPointerDrag = useCallback(
    (e: ReactPointerEvent, origin: "handle" | "header") => {
      const ctrl = ctrlRef.current;
      const { open: isOpen, side: currentSide, expandable: exp } = latestRef.current;
      if (typeof e.button === "number" && e.button > 0) return;
      if (ctrl.gesture.kind === "pointerDrag") return;
      if (ctrl.gesture.kind !== "idle" && e.isPrimary === false) return;

      const orphanedTouch = ctrl.gesture.kind === "touchDrag" ? ctrl.gesture : null;
      if (ctrl.gesture.kind !== "idle") {
        // A primary pointerdown implies no primary touch remains active; clear orphaned touch state.
        ctrl.gesture = { kind: "idle" };
      }
      const originAllowsDrag =
        origin === "header"
          ? currentSide === "bottom" && exp?.geometry !== "full"
          : currentSide === "bottom" || Boolean(exp);
      if (!isOpen || !originAllowsDrag || isExcludedTarget(e.target, origin === "header")) {
        if (orphanedTouch !== null) clearDragState(panelRef.current, scrimRef.current);
        return;
      }

      const panelAtStart = panelRef.current;
      const win = ownerWindowOf(panelAtStart ?? e.currentTarget);
      const needsResetOnRelease = orphanedTouch !== null || ctrl.cancelDeclinedGuard !== null;
      clearCallback(ctrl, "cancelDeclinedGuard");

      ctrl.gesture = {
        kind: "pointerDrag",
        pointerId: e.pointerId ?? 0,
        startX: e.clientX,
        startY: e.clientY,
        axis: "pending",
        offsetY: 0,
        panelHeight: panelAtStart?.getBoundingClientRect().height ?? 0,
        hasTranslated: false,
        samples: [{ y: e.clientY, time: performance.now() }],
        expandableMode: origin === "handle" && exp !== undefined ? exp.geometry : undefined,
        maxAbsDelta: 0,
        switchedGeometry: false,
        needsResetOnRelease,
      };

      const onPointerMove = (moveEvent: PointerEvent) => {
        const state = ctrl.gesture;
        if (state.kind !== "pointerDrag" || (moveEvent.pointerId ?? 0) !== state.pointerId) return;

        const dx = moveEvent.clientX - state.startX;
        const dy = moveEvent.clientY - state.startY;
        state.maxAbsDelta = Math.max(state.maxAbsDelta, Math.abs(dx), Math.abs(dy));
        if (state.axis === "pending" && (Math.abs(dx) > LOCK_DISTANCE_PX || Math.abs(dy) > LOCK_DISTANCE_PX)) {
          state.axis = Math.abs(dx) >= Math.abs(dy) ? "horizontal" : "vertical";
        }
        if (state.axis === "horizontal") return;

        const panel = panelRef.current;
        const activeSide = latestRef.current.side;

        if (state.expandableMode === "full") {
          if (!state.switchedGeometry && dy > EXPANDABLE_DRAG_PX) {
            state.switchedGeometry = true;
            latestRef.current.expandable?.onGeometryChange("peek");
          }
          return;
        }

        if (state.expandableMode === "peek") {
          if (state.switchedGeometry) return;
          if (dy < -EXPANDABLE_DRAG_PX) {
            state.switchedGeometry = true;
            if (state.hasTranslated || state.needsResetOnRelease) {
              clearDragState(panel, scrimRef.current);
              state.offsetY = 0;
              state.hasTranslated = false;
              state.needsResetOnRelease = false;
            }
            latestRef.current.expandable?.onGeometryChange("full");
            return;
          }
          if (activeSide !== "bottom" || (!state.hasTranslated && dy <= EXPANDABLE_TAP_SLOP_PX)) return;
        }

        if (activeSide !== "bottom" || panel === null || state.axis !== "vertical") return;
        const offsetY = Math.max(0, dy);
        if (offsetY > 0 || state.hasTranslated) {
          state.hasTranslated = true;
          state.offsetY = offsetY;
          applyDragStyles(panel, scrimRef.current, offsetY, state.panelHeight);
        }
        pushSample(state.samples, moveEvent.clientY, performance.now());
      };

      const finishPointer = (endEvent: PointerEvent, cancelled: boolean) => {
        const state = ctrl.gesture;
        if (state.kind !== "pointerDrag" || (endEvent.pointerId ?? 0) !== state.pointerId) return;

        clearCallback(ctrl, "cancelPointer");
        ctrl.gesture = { kind: "idle" };
        const panel = panelRef.current;

        if (
          state.expandableMode !== undefined &&
          !state.hasTranslated &&
          !cancelled &&
          !state.switchedGeometry &&
          state.maxAbsDelta <= EXPANDABLE_TAP_SLOP_PX
        ) {
          latestRef.current.expandable?.onGeometryChange((g) => (g === "peek" ? "full" : "peek"));
        }
        if (panel !== null && state.hasTranslated) settleSwipeDrag(ctrl, latestRef, state, panel, scrimRef, cancelled);
        else if (state.needsResetOnRelease) clearDragState(panel, scrimRef.current);
      };

      const onPointerUp = (upEvent: PointerEvent) => finishPointer(upEvent, false);
      const onPointerCancel = (cancelEvent: PointerEvent) => finishPointer(cancelEvent, true);

      clearCallback(ctrl, "cancelPointer");
      ctrl.cancelPointer = () => {
        win.removeEventListener("pointermove", onPointerMove);
        win.removeEventListener("pointerup", onPointerUp);
        win.removeEventListener("pointercancel", onPointerCancel);
      };
      win.addEventListener("pointermove", onPointerMove);
      win.addEventListener("pointerup", onPointerUp);
      win.addEventListener("pointercancel", onPointerCancel);
    },
    [panelRef, scrimRef],
  );

  const onHandlePointerDown = useCallback((e: ReactPointerEvent) => startPointerDrag(e, "handle"), [startPointerDrag]);
  const onHeaderPointerDown = useCallback((e: ReactPointerEvent) => startPointerDrag(e, "header"), [startPointerDrag]);

  return { onHandlePointerDown, onHeaderPointerDown };
}
