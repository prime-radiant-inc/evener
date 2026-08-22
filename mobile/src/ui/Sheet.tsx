import {
  type JSX,
  type KeyboardEvent,
  type ReactNode,
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
} from "react";
import { createPortal } from "react-dom";
import { IconButton } from "./IconButton";

export interface SheetProps {
  readonly open: boolean;
  readonly onClose: () => void;
  readonly title: string;
  readonly children: ReactNode;
  /** When true, Escape is captured (default true). Back-button JS calls onClose. */
  readonly dismissOnEscape?: boolean;
}

/** Unique internal marker tagging a same-document history entry this sheet owns. */
const SHEET_SENTINEL = Symbol.for("evener.sheet.sentinel");

interface SheetSentinel {
  readonly [SHEET_SENTINEL]: true;
}

function isSheetSentinel(value: unknown): value is SheetSentinel {
  return (
    typeof value === "object" &&
    value !== null &&
    (value as Record<symbol, unknown>)[SHEET_SENTINEL] === true
  );
}

/**
 * Detent bottom sheet in a portal with a focus trap, Escape dismissal, and a
 * deterministic upward motion (removed under reduced motion). One sheet at a
 * time — never stacked. The grabber + Done button give explicit close
 * affordances.
 *
 * While open, exactly one tagged same-document history sentinel is pushed so
 * that the browser/Android system Back button closes the sheet (via
 * `popstate`) instead of navigating the app away. All dismissal paths funnel
 * through one idempotent path and unwind only this sheet's sentinel; an
 * external parent closure or unmount removes the sentinel safely. No
 * unrelated history is ever popped.
 */
export function Sheet({
  open,
  onClose,
  title,
  children,
  dismissOnEscape = true,
}: SheetProps): JSX.Element | null {
  const dialogRef = useRef<HTMLDivElement>(null);
  const previouslyFocused = useRef<HTMLElement | null>(null);

  // Explicit state ownership. `ownsRef` is true while this sheet's sentinel is
  // the current top of the history stack; `markerRef` holds the exact sentinel
  // object we pushed so we can confirm `history.state` is still ours before
  // unwinding (a later sheet may have pushed over us). `dismissedRef` is true
  // once we have already notified the parent via `onClose` (idempotent guard).
  // `activeRef` distinguishes a real close/unmount from a StrictMode replay.
  const ownsRef = useRef(false);
  const markerRef = useRef<SheetSentinel | null>(null);
  const dismissedRef = useRef(false);
  const activeRef = useRef(false);
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;

  // Unwind this sheet's sentinel without notifying the parent. Safe to call
  // when we currently own the top sentinel (`history.state` is our exact
  // marker); a no-op otherwise — a later sheet may have pushed over us, and
  // backing then would pop the wrong entry. The resulting `popstate` is ignored
  // because `ownsRef` is cleared first and the listener is gone or guarded.
  const unwind = useCallback(() => {
    if (!ownsRef.current) return;
    if (markerRef.current === null || history.state !== markerRef.current) {
      ownsRef.current = false;
      return;
    }
    ownsRef.current = false;
    markerRef.current = null;
    history.back();
  }, []);

  // One idempotent dismissal path. Notifies the parent exactly once and, for
  // user-initiated dismissals (not a popstate), unwinds the sentinel so the
  // Back stack does not retain a dead entry.
  const dismiss = useCallback(
    (fromPopstate: boolean) => {
      if (dismissedRef.current) return;
      dismissedRef.current = true;
      onCloseRef.current();
      if (!fromPopstate) {
        unwind();
      } else {
        // The browser already navigated back over the sentinel.
        ownsRef.current = false;
        markerRef.current = null;
      }
    },
    [unwind],
  );

  // Capture the element that had focus before opening; restore on close.
  useLayoutEffect(() => {
    if (!open) return;
    previouslyFocused.current =
      (document.activeElement as HTMLElement | null) ?? null;
    return () => {
      previouslyFocused.current?.focus?.();
    };
  }, [open]);

  // Focus the first focusable element when the sheet opens.
  useEffect(() => {
    if (!open) return;
    const dialog = dialogRef.current;
    if (dialog === null) return;
    // Defer one frame so portal content is painted.
    const id = window.requestAnimationFrame(() => {
      const focusable = queryFocusable(dialog);
      focusable[0]?.focus();
    });
    return () => window.cancelAnimationFrame(id);
  }, [open]);

  // History sentinel + browser/Android Back (`popstate`) handling.
  useEffect(() => {
    if (!open) return;
    activeRef.current = true;
    // Push exactly one sentinel. Guard against StrictMode effect replay and
    // re-renders that re-run the effect while we already own the current entry:
    // if `history.state` is already OUR exact marker (same instance), reuse it.
    // If it is a stale sentinel left by a previous (unmounted) sheet, push a
    // fresh sentinel over it so the stale owner's later unwind is a no-op.
    // Do not assume `history.length` — compare `history.state`.
    if (markerRef.current !== null && history.state === markerRef.current) {
      // StrictMode remount / re-render: reuse our own sentinel.
    } else {
      const marker = { [SHEET_SENTINEL]: true } as SheetSentinel;
      history.pushState(marker, "");
      markerRef.current = marker;
    }
    ownsRef.current = true;
    dismissedRef.current = false;

    const onPopState = (event: PopStateEvent) => {
      // Ignore popstates for a foreign state (the app's own navigation) while
      // our sentinel is still on top: the sheet must not close on unrelated
      // history movement. A genuine system Back navigates back over our
      // sentinel, so `history.state` is no longer ours; a foreign popstate
      // dispatched while our sentinel remains current leaves `history.state`
      // unchanged and must be ignored.
      void event;
      if (!ownsRef.current || dismissedRef.current) return;
      if (isSheetSentinel(history.state)) return;
      // The user pressed system Back: the browser navigated back over our
      // sentinel. Close via the shared idempotent path.
      dismiss(true);
    };
    window.addEventListener("popstate", onPopState);

    return () => {
      activeRef.current = false;
      window.removeEventListener("popstate", onPopState);
      // Defer the unwind to a microtask so a StrictMode remount (which re-runs
      // this effect and resets `activeRef` synchronously) cancels it. For a
      // real close (open→false) or unmount, `activeRef` stays false and the
      // sentinel is unwound safely without notifying the parent.
      queueMicrotask(() => {
        if (activeRef.current) return;
        unwind();
      });
    };
  }, [open, dismiss, unwind]);

  // Document-level Escape handler so the key reaches the sheet regardless of
  // which portal child currently holds focus.
  useEffect(() => {
    if (!open || !dismissOnEscape) return;
    const onKey = (e: globalThis.KeyboardEvent) => {
      if (e.key === "Escape") {
        e.stopPropagation();
        dismiss(false);
      }
    };
    document.addEventListener("keydown", onKey, true);
    return () => document.removeEventListener("keydown", onKey, true);
  }, [open, dismissOnEscape, dismiss]);

  const handleKeyDown = useCallback((event: KeyboardEvent<HTMLDivElement>) => {
    if (event.key === "Tab") {
      trapTab(event, dialogRef.current);
    }
  }, []);

  if (!open) return null;

  return createPortal(
    <>
      <div
        className="evener-sheet-overlay"
        onClick={() => dismiss(false)}
        aria-hidden="true"
      />
      <div
        ref={dialogRef}
        className="evener-sheet"
        role="dialog"
        aria-modal="true"
        aria-label={title}
        onKeyDown={handleKeyDown}
      >
        <div className="evener-sheet__grabber" aria-hidden="true" />
        <div className="evener-sheet__header">
          <span className="evener-sheet__title">{title}</span>
          <IconButton aria-label="Done" onClick={() => dismiss(false)}>
            Done
          </IconButton>
        </div>
        <div className="evener-sheet__body">{children}</div>
      </div>
    </>,
    document.body,
  );
}

function queryFocusable(root: HTMLElement): HTMLElement[] {
  const selector =
    'button:not([disabled]), [href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';
  // jsdom reports offsetParent as null even for visible elements; we only
  // need the set of tabbable elements for focus management, not geometry.
  return [...root.querySelectorAll<HTMLElement>(selector)];
}

function trapTab(
  event: KeyboardEvent<HTMLDivElement>,
  dialog: HTMLElement | null,
): void {
  if (dialog === null) return;
  const focusable = queryFocusable(dialog);
  if (focusable.length === 0) return;
  const first = focusable[0];
  const last = focusable[focusable.length - 1];
  if (first === undefined || last === undefined) return;
  const active = document.activeElement as HTMLElement | null;
  if (event.shiftKey) {
    if (active === first || active === null) {
      event.preventDefault();
      last.focus();
    }
  } else {
    if (active === last) {
      event.preventDefault();
      first.focus();
    }
  }
}
