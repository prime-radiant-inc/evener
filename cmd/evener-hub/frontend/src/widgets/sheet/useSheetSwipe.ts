import {
  type PointerEvent as ReactPointerEvent,
  type RefObject,
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
} from "react";
import { motionDurationMs } from "../../motion/tokens";
import { ownerDocumentOf, ownerWindowOf } from "../internal/documentRealm";
import { requireClass } from "../internal/requireClass";
import styles from "./sheet.module.css";

const SNAP_BACK_CLASS = requireClass(styles.snapBack, "sheet.module.css", "snapBack");
const HANDLE_CLASS = requireClass(styles.handle, "sheet.module.css", "handle");
const GRAB_HANDLE_CLASS = requireClass(styles.grabHandle, "sheet.module.css", "grabHandle");

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
      kind: "touchDrag";
      touchId: number;
      startY: number;
      offsetY: number;
      samples: VelocitySample[];
    }
  | {
      kind: "pointerDrag";
      pointerId: number;
      startY: number;
      offsetY: number;
      samples: VelocitySample[];
      hasTranslated: boolean;
      expandableMode: "peek" | "full" | undefined;
      maxAbsDelta: number;
      switchedGeometry: boolean;
      recoveryOffsetY: number | undefined;
    };

interface SwipeController {
  gesture: GestureState;
  lastScrollAt: number | null;
  cancelPointer: (() => void) | null;
  cancelSnapBack: (() => void) | null;
  cancelDeclinedGuard: (() => void) | null;
}

function clearCallback(ctrl: SwipeController, key: "cancelPointer" | "cancelSnapBack" | "cancelDeclinedGuard"): void {
  ctrl[key]?.();
  ctrl[key] = null;
}

function cancelAll(ctrl: SwipeController): void {
  clearCallback(ctrl, "cancelDeclinedGuard");
  clearCallback(ctrl, "cancelPointer");
  clearCallback(ctrl, "cancelSnapBack");
}

function targetElement(target: EventTarget | null): Element | null {
  const node = target as Node | null;
  if (node === null) return null;
  return node.nodeType === 1 ? (node as Element) : node.parentElement;
}

function isHandleOrHeaderTarget(target: EventTarget | null, panel: HTMLElement): boolean {
  const el = targetElement(target);
  if (el === null) return false;
  const match = el.closest(`.${HANDLE_CLASS}, .${GRAB_HANDLE_CLASS}, header`);
  return match !== null && match.parentElement === panel;
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

function clearInlineStyles(panel: HTMLElement | null, scrim: HTMLElement | null): void {
  if (panel !== null) panel.style.transform = "";
  if (scrim !== null) scrim.style.opacity = "";
}

function applyDragStyles(panel: HTMLElement, scrim: HTMLElement | null, offsetY: number): void {
  panel.style.transform = `translateY(${offsetY}px)`;
  if (scrim !== null) {
    const ratio = offsetY / Math.max(panel.getBoundingClientRect().height, 1);
    scrim.style.opacity = String(Math.max(0.4, 1 - ratio * 0.6));
  }
}

function snapBack(ctrl: SwipeController, panel: HTMLElement, scrim: HTMLElement | null): void {
  clearCallback(ctrl, "cancelSnapBack");
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
}

function settleOrClear(
  ctrl: SwipeController,
  panel: HTMLElement | null,
  scrim: HTMLElement | null,
  offsetY: number,
): void {
  if (panel !== null && offsetY > 0) snapBack(ctrl, panel, scrim);
  else clearInlineStyles(panel, scrim);
}

function settleSwipeDrag(
  ctrl: SwipeController,
  latestRef: RefObject<{ open: boolean; onClose: () => void }>,
  state: { offsetY: number; samples: VelocitySample[] },
  panel: HTMLElement,
  scrimRef: RefObject<HTMLDivElement | null>,
  cancelled: boolean,
): void {
  const scrim = scrimRef.current;
  if (!cancelled && state.offsetY > 0) {
    const threshold = dismissThreshold(panel.getBoundingClientRect().height);
    const projectedY = state.offsetY + computeVelocityY(state.samples, performance.now()) * PROJECTION_MS;
    if (projectedY >= threshold) {
      latestRef.current.onClose();
      clearCallback(ctrl, "cancelDeclinedGuard");
      const win = ownerWindowOf(panel);
      const rafId = win.requestAnimationFrame(() => {
        ctrl.cancelDeclinedGuard = null;
        if (latestRef.current.open && ctrl.gesture.kind !== "touchDrag" && ctrl.gesture.kind !== "pointerDrag") {
          snapBack(ctrl, panel, scrimRef.current);
        }
      });
      ctrl.cancelDeclinedGuard = () => win.cancelAnimationFrame(rafId);
      return;
    }
  }
  settleOrClear(ctrl, panel, scrim, state.offsetY);
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
    cancelSnapBack: null,
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
      panelRef.current?.classList.remove(SNAP_BACK_CLASS);
      clearInlineStyles(panelRef.current, scrimRef.current);
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
        if (ctrl.gesture.kind === "touchDrag") settleOrClear(ctrl, panel, scrimRef.current, ctrl.gesture.offsetY);
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

        clearCallback(ctrl, "cancelSnapBack");
        clearCallback(ctrl, "cancelDeclinedGuard");
        state = ctrl.gesture = {
          kind: "touchDrag",
          touchId: state.touchId,
          startY: state.startY,
          offsetY: 0,
          samples: [{ y: state.startY, time: state.startTime }],
        };
      }

      if (event.cancelable) event.preventDefault();
      const offsetY = Math.max(0, touch.clientY - state.startY);
      state.offsetY = offsetY;
      pushSample(state.samples, touch.clientY, performance.now());
      applyDragStyles(panel, scrimRef.current, offsetY);
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
      if (
        !isOpen ||
        (origin === "header"
          ? currentSide !== "bottom" || exp?.geometry === "full"
          : !exp && currentSide !== "bottom") ||
        isExcludedTarget(e.target, bodyRef.current, origin === "header")
      ) {
        if (orphanedTouch !== null) settleOrClear(ctrl, panelRef.current, scrimRef.current, orphanedTouch.offsetY);
        return;
      }

      const win = ownerWindowOf(panelRef.current ?? e.currentTarget);
      const recoveryOffsetY =
        orphanedTouch?.offsetY ??
        (ctrl.cancelDeclinedGuard !== null ? 1 : ctrl.cancelSnapBack !== null ? 0 : undefined);
      clearCallback(ctrl, "cancelSnapBack");
      clearCallback(ctrl, "cancelDeclinedGuard");

      ctrl.gesture = {
        kind: "pointerDrag",
        pointerId: e.pointerId ?? 0,
        startY: e.clientY,
        offsetY: 0,
        hasTranslated: false,
        samples: [{ y: e.clientY, time: performance.now() }],
        expandableMode: origin === "handle" && exp !== undefined ? exp.geometry : undefined,
        maxAbsDelta: 0,
        switchedGeometry: false,
        recoveryOffsetY,
      };

      const onPointerMove = (moveEvent: PointerEvent) => {
        const state = ctrl.gesture;
        if (state.kind !== "pointerDrag" || (moveEvent.pointerId ?? 0) !== state.pointerId) return;

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
            if (state.hasTranslated || state.recoveryOffsetY !== undefined) {
              clearInlineStyles(panel, scrimRef.current);
              state.offsetY = 0;
              state.hasTranslated = false;
              state.recoveryOffsetY = undefined;
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
          applyDragStyles(panel, scrimRef.current, offsetY);
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
        else if (state.recoveryOffsetY !== undefined)
          settleOrClear(ctrl, panel, scrimRef.current, state.recoveryOffsetY);
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
    [panelRef, bodyRef, scrimRef],
  );

  const onHandlePointerDown = useCallback((e: ReactPointerEvent) => startPointerDrag(e, "handle"), [startPointerDrag]);
  const onHeaderPointerDown = useCallback((e: ReactPointerEvent) => startPointerDrag(e, "header"), [startPointerDrag]);

  return { onHandlePointerDown, onHeaderPointerDown };
}
