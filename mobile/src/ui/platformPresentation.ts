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
 * declarations when stopped. It listens to `visualViewport.resize`,
 * `visualViewport.scroll`, and a `window.resize` fallback; all listeners are
 * removed on stop. No Tauri imports; the coordinator is pure DOM.
 *
 * Concurrency model: a single document-root owner at a time. An ownership
 * token prevents a stale coordinator's cleanup (e.g. a StrictMode remount's
 * first effect tear-down) from clobbering the declarations of a newer owner
 * while still letting a normal single-owner lifecycle restore prior state.
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
const STYLE_ATTR = "style";
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

// ---------------------------------------------------------------------------
// Inline CSS declaration snapshots (presence, value, !important priority)
// ---------------------------------------------------------------------------

interface CssDeclSnapshot {
  /** Whether the declaration was present before mutation. */
  readonly had: boolean;
  /** The pre-existing value (empty string if absent). */
  readonly value: string;
  /** The pre-existing priority (`""` or `"important"`). */
  readonly priority: string;
}

function snapshotCssDecl(el: HTMLElement, name: string): CssDeclSnapshot {
  const value = el.style.getPropertyValue(name);
  const priority = el.style.getPropertyPriority(name);
  return { had: value !== "", value, priority };
}

/**
 * Restore one managed inline CSS declaration to its captured state. Only the
 * managed property is touched; unrelated declarations added later are left in
 * place. If the declaration was absent, it is removed; if present, its value
 * and priority are restored exactly.
 */
function restoreCssDecl(
  el: HTMLElement,
  name: string,
  snap: CssDeclSnapshot,
): void {
  if (snap.had) {
    el.style.setProperty(name, snap.value, snap.priority);
  } else {
    el.style.removeProperty(name);
  }
}

/**
 * If the `style` attribute did not pre-exist and every declaration has been
 * removed by the restore pass (cssText is empty), drop the now-empty inline
 * `style` attribute so no artifact is left behind. Unrelated declarations
 * would keep cssText non-empty and preserve the attribute.
 */
function dropEmptyStyleIfAbsent(el: HTMLElement, hadStyle: boolean): void {
  if (!hadStyle && el.style.cssText === "") {
    el.removeAttribute(STYLE_ATTR);
  }
}

// ---------------------------------------------------------------------------
// Presentation application
// ---------------------------------------------------------------------------

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
  /** Remove listeners and restore prior CSS declarations. Idempotent. */
  stop(): void;
}

interface CoordinatorOptions {
  readonly document?: Document;
  /**
   * Test seam for the visualViewport. `null` forces the no-viewport fallback
   * even when `window.visualViewport` is present; `undefined` falls back to
   * `window.visualViewport`.
   */
  readonly visualViewport?: VisualViewportLike | null;
  /** Test seam; defaults to `window`. */
  readonly window?: Window & typeof globalThis;
}

/** A finite, nonnegative number; NaN/Infinity/negatives become 0. */
function finiteNonnegative(value: number): number {
  if (!Number.isFinite(value) || value < 0) return 0;
  return value;
}

/**
 * Compute the overlay/resizes-content viewport metrics.
 *
 * Layout height follows the current `window.innerHeight`, clamped to a finite
 * nonnegative number. The keyboard inset is the *final*
 * `max(0, layoutHeight - (offsetTop + height))` — the operands are used as-is
 * (finite negative offsetTop/height are NOT individually clamped before the
 * subtraction); only the final result is clamped to 0. A non-finite operand
 * (NaN/±Infinity) makes the intermediate or final result non-finite, which
 * yields 0.
 *
 * When `visualViewport` is absent/null, the viewport height is `innerHeight`
 * and the keyboard inset is 0.
 */
export function computeViewportMetrics(opts: {
  visualViewport?: VisualViewportLike | null;
  innerHeight?: number;
}): { viewportHeight: number; keyboardInset: number } {
  const viewportHeight = finiteNonnegative(opts.innerHeight ?? 0);
  const vp = opts.visualViewport;
  if (!vp) {
    return { viewportHeight, keyboardInset: 0 };
  }
  const offsetTop = vp.offsetTop;
  const height = vp.height;
  const sum = offsetTop + height;
  const inset = viewportHeight - sum;
  // Non-finite operand -> non-finite sum/inset; final clamp yields 0.
  return { viewportHeight, keyboardInset: finiteNonnegative(inset) };
}

/** Format a value for `setProperty`, normalizing non-finite to "0px". */
function pxDecl(value: number): string {
  return `${finiteNonnegative(value)}px`;
}

/** Per-document ownership token: the latest owner; stale owners skip restore. */
const owners = new WeakMap<Document, unknown>();
let nextToken = 1;

/** Create a testable visualViewport coordinator. */
export function createViewportCoordinator(
  options: CoordinatorOptions = {},
): ViewportCoordinator {
  const doc = options.document ?? document;
  const win = options.window ?? window;
  // Explicit `null` forces fallback; `undefined` falls through to the window.
  const vv =
    options.visualViewport === undefined
      ? ((win as { visualViewport?: VisualViewportLike | null })
          .visualViewport ?? null)
      : options.visualViewport;
  const el = doc.documentElement;

  let started = false;
  let stopped = false;
  let token: unknown = null;

  const priorViewportHeight = snapshotCssDecl(el, VIEWPORT_HEIGHT);
  const priorKeyboardInset = snapshotCssDecl(el, KEYBOARD_INSET);
  const hadStyle = el.hasAttribute(STYLE_ATTR);

  const onViewportEvent = (): void => {
    const metrics = computeViewportMetrics({
      visualViewport: vv,
      innerHeight: win.innerHeight,
    });
    el.style.setProperty(VIEWPORT_HEIGHT, pxDecl(metrics.viewportHeight));
    el.style.setProperty(KEYBOARD_INSET, pxDecl(metrics.keyboardInset));
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
      token = nextToken++;
      owners.set(doc, token);
      if (vv) {
        add(vv, "resize", onViewportEvent);
        add(vv, "scroll", onViewportEvent);
      }
      add(win, "resize", onViewportEvent);
      onViewportEvent();
    },
    stop() {
      if (stopped) return;
      stopped = true;
      for (const { target, type, handler } of listeners) {
        target.removeEventListener(type, handler);
      }
      listeners.length = 0;
      // Only the current owner restores prior declarations; a stale owner (e.g.
      // a StrictMode first-mount tear-down superseded by a second mount) leaves
      // the newer owner's values in place to avoid clobbering.
      if (owners.get(doc) === token) {
        owners.delete(doc);
        restoreCssDecl(el, VIEWPORT_HEIGHT, priorViewportHeight);
        restoreCssDecl(el, KEYBOARD_INSET, priorKeyboardInset);
        dropEmptyStyleIfAbsent(el, hadStyle);
      }
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
 * tears it down (restoring prior CSS declarations) on unmount.
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
