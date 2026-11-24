import { type PointerEvent as ReactPointerEvent, type RefObject, useCallback, useEffect, useRef } from "react";
import { motionDurationMs } from "../../motion/tokens";
import { ownerDocumentOf, ownerWindowOf } from "../internal/documentRealm";
import { requireClass } from "../internal/requireClass";
import styles from "./sheet.module.css";

const SNAP_BACK_CLASS = requireClass(styles.snapBack, "sheet.module.css", "snapBack");

const VELOCITY_WINDOW_MS = 100;
const HOLD_CUTOFF_MS = 80;
const PROJECTION_MS = 200;
const LOCK_DISTANCE_PX = 8;
const SCROLL_SETTLE_MS = 180;
const DISMISS_RATIO = 0.35;
const MAX_DISMISS_THRESHOLD_PX = 160;
const FALLBACK_DISMISS_THRESHOLD_PX = 120;
const SNAP_BACK_FALLBACK_SLACK_MS = 50;
const EXPANDABLE_DRAG_PX = 50;
const EXPANDABLE_TAP_SLOP_PX = 10;
const DIRECTION_REVERSAL_SLOP_PX = 2;

const INTERACTIVE_EXCLUDE_SELECTOR =
  'input[type="range"], textarea, select, [contenteditable]:not([contenteditable="false"])';
const HEADER_INTERACTIVE_EXCLUDE_SELECTOR = `${INTERACTIVE_EXCLUDE_SELECTOR}, button, a[href], input`;

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
      kind: "dragging";
      source: "touch" | "pointer";
      id: number;
      startY: number;
      offsetY: number;
      hasTranslated: boolean;
      samples: VelocitySample[];
      expandableMode: "peek" | "full" | undefined;
      maxAbsDelta: number;
      switchedGeometry: boolean;
      hadOrphanedDrag?: boolean;
    };

function targetElement(target: EventTarget | null): Element | null {
  const node = target as Node | null;
  if (node === null) return null;
  return node.nodeType === 1 ? (node as Element) : node.parentElement;
}

function isHandleOrHeaderTarget(target: EventTarget | null, panel: HTMLElement): boolean {
  const el = targetElement(target);
  if (el === null) return false;
  if (el.closest('[data-testid="sheet-handle"], [data-testid="sheet-grab-pill"]') !== null) return true;
  const header = el.closest("header");
  return header !== null && header.parentElement === panel;
}

function isExcludedTarget(target: EventTarget | null, body: HTMLElement | null, inHeader: boolean): boolean {
  const el = targetElement(target);
  if (el === null) return false;
  if (inHeader) return el.closest(HEADER_INTERACTIVE_EXCLUDE_SELECTOR) !== null;
  if (el.closest(INTERACTIVE_EXCLUDE_SELECTOR) !== null) return true;
  if (el.closest('button[aria-label="Close"]') !== null) return true;
  return body !== null && !body.contains(el) && el.closest("button") !== null;
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
  for (let i = 0; i < list.length; i += 1) {
    const touch = list[i];
    if (touch && (touch.identifier ?? 0) === id) return touch;
  }
  return undefined;
}

function dismissThreshold(panelHeight: number): number {
  return panelHeight > 0
    ? Math.min(panelHeight * DISMISS_RATIO, MAX_DISMISS_THRESHOLD_PX)
    : FALLBACK_DISMISS_THRESHOLD_PX;
}

function pushSample(samples: VelocitySample[], y: number, time: number): void {
  const prev = samples[samples.length - 1];
  const oldest = samples[0];
  if (prev !== undefined && oldest !== undefined) {
    const step = y - prev.y;
    // Discard pre-reversal samples so pulling back up before lift produces negative velocity.
    if (step * (prev.y - oldest.y) < 0 && Math.abs(step) > DIRECTION_REVERSAL_SLOP_PX) {
      samples.splice(0, samples.length, prev);
    }
  }
  samples.push({ y, time });
  while (samples[0] !== undefined && time - samples[0].time > VELOCITY_WINDOW_MS) {
    samples.shift();
  }
}

function computeVelocityY(samples: VelocitySample[], releaseTime: number): number {
  const last = samples[samples.length - 1];
  const first = samples[0];
  if (last === undefined || first === undefined || samples.length < 2) return 0;
  if (releaseTime - last.time > HOLD_CUTOFF_MS) return 0;
  const dt = last.time - first.time;
  return dt >= 1 ? (last.y - first.y) / dt : 0;
}

function clearInlineStyles(panel: HTMLElement | null, scrim: HTMLElement | null): void {
  if (panel !== null) panel.style.transform = "";
  if (scrim !== null) scrim.style.opacity = "";
}

function applyDragStyles(panel: HTMLElement, scrim: HTMLElement | null, offsetY: number, panelHeight: number): void {
  panel.style.transform = `translateY(${offsetY}px)`;
  if (scrim !== null) {
    const ratio = offsetY / Math.max(panelHeight, 1);
    scrim.style.opacity = String(Math.max(0.4, 1 - ratio * 0.6));
  }
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

  const ctrlRef = useRef<{
    gesture: GestureState;
    lastScrollAt: number | null;
    cancelPointer: (() => void) | null;
    cancelSnapBack: (() => void) | null;
    cancelDeclinedGuard: (() => void) | null;
  }>({
    gesture: { kind: "idle" },
    lastScrollAt: null,
    cancelPointer: null,
    cancelSnapBack: null,
    cancelDeclinedGuard: null,
  });

  const cancelAllTimersAndListeners = useCallback(() => {
    const ctrl = ctrlRef.current;
    ctrl.cancelDeclinedGuard?.();
    ctrl.cancelDeclinedGuard = null;
    ctrl.cancelPointer?.();
    ctrl.cancelPointer = null;
    ctrl.cancelSnapBack?.();
    ctrl.cancelSnapBack = null;
  }, []);

  const snapBack = useCallback((panel: HTMLElement, scrim: HTMLElement | null) => {
    const ctrl = ctrlRef.current;
    ctrl.cancelSnapBack?.();
    ctrl.cancelSnapBack = null;
    if (scrim !== null) scrim.style.opacity = "";

    const win = ownerWindowOf(panel);
    if (win.matchMedia?.("(prefers-reduced-motion: reduce)").matches) {
      panel.classList.remove(SNAP_BACK_CLASS);
      panel.style.transform = "";
      return;
    }

    panel.classList.add(SNAP_BACK_CLASS);
    panel.style.transform = "translateY(0)";

    let settled = false;
    const finish = (clearTransform: boolean) => {
      if (settled) return;
      settled = true;
      panel.removeEventListener("transitionend", onTransitionEnd);
      win.clearTimeout(timer);
      panel.classList.remove(SNAP_BACK_CLASS);
      if (clearTransform) panel.style.transform = "";
      ctrl.cancelSnapBack = null;
    };
    const onTransitionEnd = (event: TransitionEvent) => {
      if (event.target !== panel) return;
      if (event.propertyName && event.propertyName !== "transform") return;
      finish(true);
    };
    const delay = motionDurationMs("--motion-duration-spatial-exit", panel) + SNAP_BACK_FALLBACK_SLACK_MS;
    const timer = win.setTimeout(() => finish(true), delay);
    panel.addEventListener("transitionend", onTransitionEnd);
    ctrl.cancelSnapBack = () => finish(false);
  }, []);

  const settleSwipeDrag = useCallback(
    (state: Extract<GestureState, { kind: "dragging" }>, panel: HTMLElement, cancelled: boolean) => {
      const scrim = scrimRef.current;
      if (cancelled) {
        if (state.offsetY > 0) snapBack(panel, scrim);
        else clearInlineStyles(panel, scrim);
        return;
      }

      const threshold = dismissThreshold(panel.getBoundingClientRect().height);
      const velocityY = computeVelocityY(state.samples, performance.now());
      const projectedY = state.offsetY + velocityY * PROJECTION_MS;

      if (state.offsetY > 0 && projectedY >= threshold) {
        latestRef.current.onClose();
        const ctrl = ctrlRef.current;
        ctrl.cancelDeclinedGuard?.();
        const win = ownerWindowOf(panel);
        const rafId = win.requestAnimationFrame(() => {
          ctrl.cancelDeclinedGuard = null;
          if (latestRef.current.open && ctrl.gesture.kind === "idle") {
            snapBack(panel, scrimRef.current);
          }
        });
        ctrl.cancelDeclinedGuard = () => win.cancelAnimationFrame(rafId);
        return;
      }

      if (state.offsetY > 0) snapBack(panel, scrim);
      else clearInlineStyles(panel, scrim);
    },
    [scrimRef, snapBack],
  );

  const prevOpenRef = useRef(open);
  useEffect(() => {
    const wasOpen = prevOpenRef.current;
    prevOpenRef.current = open;
    if (open && !wasOpen) {
      cancelAllTimersAndListeners();
      const panel = panelRef.current;
      panel?.classList.remove(SNAP_BACK_CLASS);
      clearInlineStyles(panel, scrimRef.current);
      ctrlRef.current.gesture = { kind: "idle" };
    } else if (!open) {
      cancelAllTimersAndListeners();
      ctrlRef.current.gesture = { kind: "idle" };
    }
  }, [open, panelRef, scrimRef, cancelAllTimersAndListeners]);

  useEffect(() => cancelAllTimersAndListeners, [cancelAllTimersAndListeners]);

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
        if (ctrl.gesture.kind === "dragging" && ctrl.gesture.source === "pointer") return;
        const prevTouchId = ctrl.gesture.kind === "dragging" ? ctrl.gesture.id : ctrl.gesture.touchId;
        // Recover if the tracked touch disappeared from active touches (e.g. detached child).
        if (findTouchById(event.touches, prevTouchId) !== undefined) return;
        if (ctrl.gesture.kind === "dragging" && ctrl.gesture.offsetY > 0) snapBack(panel, scrimRef.current);
        ctrl.gesture = { kind: "idle" };
      }
      if (event.touches.length !== 1) return;
      if (isHandleOrHeaderTarget(event.target, panel)) return;
      if (isExcludedTarget(event.target, bodyRef.current, false)) return;

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
      const state = ctrl.gesture;
      if (state.kind === "idle" || state.kind === "scrollOwned") return;
      if (state.kind === "dragging" && state.source === "pointer") {
        if (event.cancelable) event.preventDefault();
        return;
      }

      const touchId = state.kind === "pendingBody" ? state.touchId : state.id;
      const touch = findTouchById(event.touches, touchId) ?? findTouchById(event.changedTouches, touchId);
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

        ctrl.cancelSnapBack?.();
        ctrl.cancelSnapBack = null;
        ctrl.cancelDeclinedGuard?.();
        ctrl.cancelDeclinedGuard = null;
        const samples: VelocitySample[] = [];
        pushSample(samples, state.startY, state.startTime);
        pushSample(samples, touch.clientY, performance.now());
        const offsetY = Math.max(0, dy);
        ctrl.gesture = {
          kind: "dragging",
          source: "touch",
          id: state.touchId,
          startY: state.startY,
          offsetY,
          hasTranslated: true,
          samples,
          expandableMode: undefined,
          maxAbsDelta: Math.abs(dy),
          switchedGeometry: false,
        };
        if (event.cancelable) event.preventDefault();
        applyDragStyles(panel, scrimRef.current, offsetY, panel.getBoundingClientRect().height);
        return;
      }

      if (event.cancelable) event.preventDefault();
      const offsetY = Math.max(0, touch.clientY - state.startY);
      state.offsetY = offsetY;
      state.hasTranslated = true;
      pushSample(state.samples, touch.clientY, performance.now());
      applyDragStyles(panel, scrimRef.current, offsetY, panel.getBoundingClientRect().height);
    };

    const finishTouch = (event: TouchEvent, cancelled: boolean) => {
      const state = ctrl.gesture;
      if (state.kind === "idle" || (state.kind === "dragging" && state.source === "pointer")) return;
      const touchId = state.kind === "dragging" ? state.id : state.touchId;
      const stillActive =
        findTouchById(event.touches, touchId) !== undefined &&
        findTouchById(event.changedTouches, touchId) === undefined;
      if (stillActive) return;

      ctrl.gesture = { kind: "idle" };
      if (state.kind === "dragging") settleSwipeDrag(state, panel, cancelled);
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
  }, [open, side, panelRef, bodyRef, scrimRef, settleSwipeDrag, snapBack]);

  const startPointerDrag = useCallback(
    (e: ReactPointerEvent, origin: "handle" | "header") => {
      const ctrl = ctrlRef.current;
      const { open: isOpen, side: currentSide, expandable: exp } = latestRef.current;
      if (typeof e.button === "number" && e.button > 0) return;
      let hadOrphanedDrag = false;
      if (ctrl.gesture.kind !== "idle") {
        if (ctrl.gesture.kind === "dragging" && ctrl.gesture.source === "pointer") return;
        if (e.isPrimary === false) return;
        // A primary pointerdown implies no primary touch remains active; clear orphaned touch state.
        hadOrphanedDrag = ctrl.gesture.kind === "dragging" && ctrl.gesture.offsetY > 0;
        ctrl.gesture = { kind: "idle" };
        if (hadOrphanedDrag && panelRef.current !== null) snapBack(panelRef.current, scrimRef.current);
      }
      if (
        origin === "header"
          ? !isOpen || currentSide !== "bottom" || exp?.geometry === "full"
          : !exp && (!isOpen || currentSide !== "bottom")
      ) {
        return;
      }
      if (isExcludedTarget(e.target, bodyRef.current, origin === "header")) return;

      const win = ownerWindowOf(panelRef.current ?? e.currentTarget);
      ctrl.cancelSnapBack?.();
      ctrl.cancelSnapBack = null;
      ctrl.cancelDeclinedGuard?.();
      ctrl.cancelDeclinedGuard = null;

      ctrl.gesture = {
        kind: "dragging",
        source: "pointer",
        id: e.pointerId ?? 0,
        startY: e.clientY,
        offsetY: 0,
        hasTranslated: false,
        samples: [{ y: e.clientY, time: performance.now() }],
        expandableMode: origin === "handle" && exp !== undefined ? exp.geometry : undefined,
        maxAbsDelta: 0,
        switchedGeometry: false,
        hadOrphanedDrag,
      };

      const onPointerMove = (moveEvent: PointerEvent) => {
        const state = ctrl.gesture;
        if (state.kind !== "dragging" || state.source !== "pointer") return;
        if ((moveEvent.pointerId ?? 0) !== state.id) return;

        const delta = moveEvent.clientY - state.startY;
        state.maxAbsDelta = Math.max(state.maxAbsDelta, Math.abs(delta));
        const panel = panelRef.current;
        const activeSide = latestRef.current.side;

        if (state.expandableMode === "full") {
          if (!state.switchedGeometry && delta > EXPANDABLE_DRAG_PX) {
            state.switchedGeometry = true;
            latestRef.current.expandable?.onGeometryChange("peek");
          }
          return;
        }

        if (state.expandableMode === "peek") {
          if (state.switchedGeometry) return;
          if (delta < -EXPANDABLE_DRAG_PX) {
            state.switchedGeometry = true;
            if (state.hasTranslated) {
              clearInlineStyles(panel, scrimRef.current);
              state.offsetY = 0;
              state.hasTranslated = false;
            }
            latestRef.current.expandable?.onGeometryChange("full");
            return;
          }
          if (activeSide !== "bottom" || (!state.hasTranslated && delta <= EXPANDABLE_TAP_SLOP_PX)) return;
        }

        if (activeSide !== "bottom" || panel === null) return;
        const offsetY = Math.max(0, delta);
        if (offsetY > 0 || state.hasTranslated) {
          state.hasTranslated = true;
          state.offsetY = offsetY;
          applyDragStyles(panel, scrimRef.current, offsetY, panel.getBoundingClientRect().height);
        }
        pushSample(state.samples, moveEvent.clientY, performance.now());
      };

      const finishPointer = (endEvent: PointerEvent, cancelled: boolean) => {
        const state = ctrl.gesture;
        if (state.kind !== "dragging" || state.source !== "pointer") return;
        if ((endEvent.pointerId ?? 0) !== state.id) return;

        ctrl.cancelPointer?.();
        ctrl.cancelPointer = null;
        ctrl.gesture = { kind: "idle" };
        const panel = panelRef.current;

        if (state.expandableMode !== undefined && !state.hasTranslated) {
          if (!cancelled && !state.switchedGeometry && state.maxAbsDelta <= EXPANDABLE_TAP_SLOP_PX) {
            latestRef.current.expandable?.onGeometryChange((g) => (g === "peek" ? "full" : "peek"));
          }
          if (state.hadOrphanedDrag && panel !== null) snapBack(panel, scrimRef.current);
          return;
        }
        if (panel !== null && state.hasTranslated) settleSwipeDrag(state, panel, cancelled);
        else if (state.hadOrphanedDrag && panel !== null) snapBack(panel, scrimRef.current);
      };

      const onPointerUp = (upEvent: PointerEvent) => finishPointer(upEvent, false);
      const onPointerCancel = (cancelEvent: PointerEvent) => finishPointer(cancelEvent, true);

      ctrl.cancelPointer?.();
      ctrl.cancelPointer = () => {
        win.removeEventListener("pointermove", onPointerMove);
        win.removeEventListener("pointerup", onPointerUp);
        win.removeEventListener("pointercancel", onPointerCancel);
      };
      win.addEventListener("pointermove", onPointerMove);
      win.addEventListener("pointerup", onPointerUp);
      win.addEventListener("pointercancel", onPointerCancel);
    },
    [panelRef, bodyRef, scrimRef, settleSwipeDrag, snapBack],
  );

  const onHandlePointerDown = useCallback((e: ReactPointerEvent) => startPointerDrag(e, "handle"), [startPointerDrag]);
  const onHeaderPointerDown = useCallback((e: ReactPointerEvent) => startPointerDrag(e, "header"), [startPointerDrag]);

  return { onHandlePointerDown, onHeaderPointerDown };
}
