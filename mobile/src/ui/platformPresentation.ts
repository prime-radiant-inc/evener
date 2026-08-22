/**
 * Foundation 7C lane A — platform presentation preferences and the
 * visualViewport coordinator.
 *
 * Presentation preferences (theme, Dynamic Type content-size, reduced-motion)
 * are applied to `document.documentElement` — not only the `.evener-shell`
 * subtree — so body-level portals (sheets, dialogs) inherit the same tokens.
 * A snapshot of the pre-existing element state is captured before each
 * mutation and restored on cleanup, so StrictMode remounts and preference
 * changes never leak or clobber unrelated document state.
 *
 * The visualViewport coordinator writes two declarative CSS variables —
 * `--viewport-height` and `--keyboard-inset` — onto `document.documentElement`
 * from a robust overlay/resizes-content algorithm, and restores their prior
 * values when stopped. It listens to `visualViewport.resize`,
 * `visualViewport.scroll`, and a `window.resize` fallback; all listeners are
 * removed on stop. No Tauri imports; the coordinator is pure DOM.
 */

import { useEffect } from "react";
import type { ContentSizeCategory } from "../native/contract";

/** Appearance + sizing choices resolved onto the document root. */
export interface PlatformPresentation {
  /** `system` removes the explicit `data-theme`; light/dark sets it. */
  readonly theme: "system" | "light" | "dark";
  /** Always sets the exact native category as `data-content-size`. */
  readonly contentSize: ContentSizeCategory;
  /** `data-reduced-motion="true"` is present only when enabled. */
  readonly reducedMotion: boolean;
}

const DATA_THEME = "data-theme";
const DATA_CONTENT_SIZE = "data-content-size";
const DATA_REDUCED_MOTION = "data-reduced-motion";
const VIEWPORT_HEIGHT = "--viewport-height";
const KEYBOARD_INSET = "--keyboard-inset";

/** A captured, restorable snapshot of an attribute slot. */
interface AttrSnapshot {
  readonly had: boolean;
  readonly value: string | null;
}

/** Capture the current value of one managed attribute. */
function snapshotAttribute(el: Element, name: string): AttrSnapshot {
  return el.hasAttribute(name)
    ? { had: true, value: el.getAttribute(name) }
    : { had: false, value: null };
}

/** Restore a managed attribute to its captured value. */
function restoreAttribute(el: Element, name: string, snap: AttrSnapshot): void {
  if (snap.had) {
    el.setAttribute(name, snap.value ?? "");
  } else {
    el.removeAttribute(name);
  }
}

/** Capture the current inline value of one managed CSS variable. */
function snapshotCssVar(el: HTMLElement, name: string): string {
  return el.style.getPropertyValue(name);
}

/** Apply presentation preferences to `document.documentElement`.
 *
 * Returns a `stop` function that restores the prior `data-theme`,
 * `data-content-size`, and `data-reduced-motion` attributes exactly as they
 * were before this call (absent included). Calling `stop` more than once is a
 * no-op after the first call. */
export function applyPlatformPresentation(
  presentation: PlatformPresentation,
  doc: Document = document,
): () => void {
  const el = doc.documentElement;
  const themeSnap = snapshotAttribute(el, DATA_THEME);
  const sizeSnap = snapshotAttribute(el, DATA_CONTENT_SIZE);
  const motionSnap = snapshotAttribute(el, DATA_REDUCED_MOTION);

  if (presentation.theme === "system") {
    el.removeAttribute(DATA_THEME);
  } else {
    el.setAttribute(DATA_THEME, presentation.theme);
  }
  el.setAttribute(DATA_CONTENT_SIZE, presentation.contentSize);
  if (presentation.reducedMotion) {
    el.setAttribute(DATA_REDUCED_MOTION, "true");
  } else {
    el.removeAttribute(DATA_REDUCED_MOTION);
  }

  let stopped = false;
  return () => {
    if (stopped) return;
    stopped = true;
    restoreAttribute(el, DATA_THEME, themeSnap);
    restoreAttribute(el, DATA_CONTENT_SIZE, sizeSnap);
    restoreAttribute(el, DATA_REDUCED_MOTION, motionSnap);
  };
}

// ---------------------------------------------------------------------------
// visualViewport coordinator
// ---------------------------------------------------------------------------

/** A minimal view of `VisualViewport` the coordinator consumes. */
interface VisualViewportLike extends EventTarget {
  readonly height: number;
  readonly offsetTop: number;
}

/** Coordinator handle: `start` arms listeners; `stop` tears them down. */
export interface ViewportCoordinator {
  /** Recompute once and attach listeners. Idempotent: re-start is a no-op. */
  start(): void;
  /** Remove listeners and restore prior CSS variables. Idempotent. */
  stop(): void;
}

interface CoordinatorOptions {
  readonly document?: Document;
  /** Test seam; defaults to `window.visualViewport`. */
  readonly visualViewport?: VisualViewportLike | null;
  /** Test seam; defaults to `window`. */
  readonly window?: Window & typeof globalThis;
}

/** Clamp to a finite, nonnegative number; NaN/Infinity/negatives become 0. */
function finiteNonnegative(value: number): number {
  if (!Number.isFinite(value) || value < 0) return 0;
  return value;
}

/** Format a pixel value as a finite nonnegative `${n}px` string. */
function px(value: number): string {
  return `${finiteNonnegative(value)}px`;
}

/**
 * Compute the overlay/resizes-content viewport metrics.
 *
 * Layout height follows the current `window.innerHeight`. The keyboard inset
 * is `max(0, layoutHeight - (offsetTop + height))`. When `visualViewport` is
 * absent, the viewport height is `innerHeight` and the keyboard inset is 0.
 */
export function computeViewportMetrics(opts: {
  visualViewport?: VisualViewportLike | null;
  innerHeight?: number;
}): { viewportHeight: number; keyboardInset: number } {
  const innerHeight = finiteNonnegative(opts.innerHeight ?? 0);
  const vp = opts.visualViewport;
  if (!vp) {
    return { viewportHeight: innerHeight, keyboardInset: 0 };
  }
  const offsetTop = finiteNonnegative(vp.offsetTop);
  const height = finiteNonnegative(vp.height);
  const inset = innerHeight - (offsetTop + height);
  return {
    viewportHeight: innerHeight,
    keyboardInset: finiteNonnegative(inset),
  };
}

/** Create a testable visualViewport coordinator. */
export function createViewportCoordinator(
  options: CoordinatorOptions = {},
): ViewportCoordinator {
  const doc = options.document ?? document;
  const win = options.window ?? window;
  const vv =
    options.visualViewport ??
    (win as { visualViewport?: VisualViewportLike }).visualViewport ??
    null;
  const el = doc.documentElement;

  let started = false;
  let stopped = false;
  const priorViewportHeight = snapshotCssVar(el, VIEWPORT_HEIGHT);
  const priorKeyboardInset = snapshotCssVar(el, KEYBOARD_INSET);

  const onViewportEvent = (): void => {
    const metrics = computeViewportMetrics({
      visualViewport: vv,
      innerHeight: win.innerHeight,
    });
    el.style.setProperty(VIEWPORT_HEIGHT, px(metrics.viewportHeight));
    el.style.setProperty(KEYBOARD_INSET, px(metrics.keyboardInset));
  };

  const onWindowResize = (): void => {
    onViewportEvent();
  };

  const listeners: Array<{
    target: EventTarget;
    type: string;
    handler: EventListener;
  }> = [];

  function add(
    target: EventTarget,
    type: string,
    handler: EventListener,
  ): void {
    target.addEventListener(type, handler);
    listeners.push({ target, type, handler });
  }

  return {
    start() {
      if (started || stopped) return;
      started = true;
      if (vv) {
        add(vv, "resize", onViewportEvent);
        add(vv, "scroll", onViewportEvent);
      }
      add(win, "resize", onWindowResize);
      onViewportEvent();
    },
    stop() {
      if (stopped) return;
      stopped = true;
      for (const { target, type, handler } of listeners) {
        target.removeEventListener(type, handler);
      }
      listeners.length = 0;
      el.style.setProperty(VIEWPORT_HEIGHT, priorViewportHeight);
      el.style.setProperty(KEYBOARD_INSET, priorKeyboardInset);
    },
  };
}

// ---------------------------------------------------------------------------
// React bindings
// ---------------------------------------------------------------------------

/** Minimal store surface the presentation hook consumes. */
export interface PresentationStore {
  readonly theme: PlatformPresentation["theme"];
  readonly contentSize: ContentSizeCategory;
  readonly reducedMotion: boolean;
}

/**
 * React hook: subscribes to presentation preferences and applies them to
 * `document.documentElement` for the lifetime of the component, restoring the
 * prior state on unmount. Re-applies on any preference change.
 */
export function usePlatformPresentation(store: PresentationStore): void {
  const { theme, contentSize, reducedMotion } = store;
  useEffect(() => {
    return applyPlatformPresentation({ theme, contentSize, reducedMotion });
  }, [theme, contentSize, reducedMotion]);
}

/**
 * React hook: installs the visualViewport coordinator once on mount and
 * tears it down (restoring prior CSS variables) on unmount.
 */
export function useViewportCoordinator(): void {
  useEffect(() => {
    const coord = createViewportCoordinator();
    coord.start();
    return () => {
      coord.stop();
    };
  }, []);
}
