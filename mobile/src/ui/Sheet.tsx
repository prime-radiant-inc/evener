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

// ---------------------------------------------------------------------------
// Sheet history coordinator
// ---------------------------------------------------------------------------

/**
 * Namespaced plain-string key marking a same-document history entry owned by a
 * sheet. Real browsers structured-clone `history.state` on `pushState`,
 * discarding symbol-keyed properties and breaking object identity, so the
 * marker is a plain enumerable string key with a unique string token per
 * sheet-open lease. Comparison is always by token value, never by object
 * identity.
 */
const SHEET_KEY = "__evener_sheet";

interface SheetMarker {
  readonly __evener_sheet: true;
  readonly token: string;
}

function isSheetMarker(value: unknown): value is SheetMarker {
  return (
    typeof value === "object" &&
    value !== null &&
    (value as Record<string, unknown>)[SHEET_KEY] === true
  );
}

interface SheetOwner {
  readonly token: string;
  onClose: () => void;
  dismissed: boolean;
}

interface PendingBack {
  readonly token: string;
  readonly generation: number;
}

interface SheetCoordinator {
  owner: SheetOwner | null;
  retiredTokens: Set<string>;
  pendingBack: PendingBack | null;
  generation: number;
  listenerInstalled: boolean;
  settledResolvers: Set<() => void>;
}

const coordinator: SheetCoordinator = {
  owner: null,
  retiredTokens: new Set(),
  pendingBack: null,
  generation: 0,
  listenerInstalled: false,
  settledResolvers: new Set(),
};

function newToken(): string {
  return `s${Date.now().toString(36)}${Math.random().toString(36).slice(2, 8)}`;
}

function notifySettled(): void {
  if (coordinator.pendingBack !== null) return;
  const resolvers = coordinator.settledResolvers;
  coordinator.settledResolvers = new Set();
  for (const resolve of resolvers) resolve();
}

function onPopState(): void {
  if (coordinator.pendingBack !== null) {
    // This popstate is from a traversal WE initiated (unwind or inert-skip).
    coordinator.pendingBack = null;
    // After our own back landed, check whether we are now on an inert sentinel
    // that should be skipped, chaining the skip deterministically.
    const state = history.state;
    if (isSheetMarker(state) && coordinator.retiredTokens.has(state.token)) {
      coordinator.generation += 1;
      coordinator.pendingBack = {
        token: state.token,
        generation: coordinator.generation,
      };
      history.back();
    } else {
      notifySettled();
    }
    return;
  }

  const state = history.state;

  // An inert (retired) sentinel was exposed by user/system navigation. Skip
  // past it so the dead marker cannot surface or remain usable.
  if (isSheetMarker(state) && coordinator.retiredTokens.has(state.token)) {
    coordinator.generation += 1;
    coordinator.pendingBack = {
      token: state.token,
      generation: coordinator.generation,
    };
    history.back();
    return;
  }

  const owner = coordinator.owner;
  if (owner === null || owner.dismissed) return;

  // We are still on our own sentinel (e.g., the app pushed over it then
  // navigated back to it) — not a close.
  if (isSheetMarker(state) && state.token === owner.token) return;

  // A foreign popstate whose state is not a sheet marker and not our sentinel
  // is the app's own navigation — ignore it.
  if (!isSheetMarker(state) && state !== null && state !== undefined) return;

  // System Back navigated back over our sentinel: close once, without another
  // back.
  requestClose(owner.token, true);
}

function ensureListener(): void {
  if (coordinator.listenerInstalled) return;
  coordinator.listenerInstalled = true;
  window.addEventListener("popstate", onPopState);
}

function removeListener(): void {
  if (!coordinator.listenerInstalled) return;
  coordinator.listenerInstalled = false;
  window.removeEventListener("popstate", onPopState);
}

/**
 * Acquire sheet history ownership. Returns a token (the first/only owner) or
 * `null` if another sheet already owns the history — a second concurrent sheet
 * does not push another sentinel.
 */
function acquire(onClose: () => void): string | null {
  ensureListener();
  if (coordinator.owner !== null) return null;
  const token = newToken();
  coordinator.owner = { token, onClose, dismissed: false };
  return token;
}

/** Update the close handler for an existing owner (survives re-renders). */
function setCloseHandler(token: string, onClose: () => void): void {
  if (coordinator.owner?.token === token) {
    coordinator.owner.onClose = onClose;
  }
}

/** Retire a token so its sentinel becomes inert (skip on traversal). */
function retireToken(token: string): void {
  coordinator.retiredTokens.add(token);
}

/** Initiate an unwind back for a token if its sentinel is the current top. */
function unwindIfCurrent(token: string): void {
  retireToken(token);
  const state = history.state;
  if (isSheetMarker(state) && state.token === token) {
    // Our sentinel is the current top — back over it.
    coordinator.generation += 1;
    coordinator.pendingBack = { token, generation: coordinator.generation };
    history.back();
  }
  // If buried under an unrelated entry, we cannot pop it (never pop unrelated
  // history). The token is retired; the coordinator listener skips the inert
  // sentinel when later traversal exposes it.
}

/**
 * One idempotent dismissal path. Notifies the parent exactly once. For
 * user-initiated dismissals (not popstate), unwinds the sentinel.
 */
function requestClose(token: string | null, fromPopstate: boolean): void {
  if (token === null) return;
  const owner = coordinator.owner;
  if (owner === null || owner.token !== token) return;
  if (owner.dismissed) return;
  owner.dismissed = true;
  owner.onClose();
  if (fromPopstate) {
    // The browser already navigated back over the sentinel.
    retireToken(token);
  } else {
    unwindIfCurrent(token);
  }
}

/**
 * Release ownership without notifying the parent (external close/unmount).
 * Retires the token and unwinds if the sentinel is current top.
 */
function release(token: string | null): void {
  if (token === null) return;
  const owner = coordinator.owner;
  if (owner?.token === token) {
    coordinator.owner = null;
  }
  if (!owner?.dismissed) {
    unwindIfCurrent(token);
  }
}

/** Await all pending coordinator traversals (unwinds, inert skips). */
function settled(): Promise<void> {
  // Schedule a microtask so that any release microtasks deferred by component
  // cleanup (StrictMode-aware) have run before we check pendingBack. This is
  // the coordinator's own completion signal, not a test sleep.
  return new Promise((resolve) => {
    queueMicrotask(() => {
      if (coordinator.pendingBack === null) {
        resolve();
      } else {
        coordinator.settledResolvers.add(resolve);
      }
    });
  });
}

/** Test-only: reset coordinator state between tests. */
function resetCoordinator(): void {
  removeListener();
  coordinator.owner = null;
  coordinator.retiredTokens = new Set();
  coordinator.pendingBack = null;
  coordinator.generation = 0;
  coordinator.settledResolvers = new Set();
}

/** @internal Exported for tests only. */
export function __sheetHistorySettled(): Promise<void> {
  return settled();
}

/** @internal Exported for tests only. */
export function __resetSheetHistory(): void {
  resetCoordinator();
}

// ---------------------------------------------------------------------------
// Sheet component
// ---------------------------------------------------------------------------

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
 * unrelated history is ever popped. The marker is a structured-clone-safe
 * plain-string key with a unique token per lease. A module-level coordinator
 * retains a single `popstate` listener, tracks pending unwinds by
 * token/generation (preventing a late back from a closed sheet from popping a
 * newer entry), enforces one global owner, and retires/inertly skips buried
 * sentinels.
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
  const tokenRef = useRef<string | null>(null);
  const activeRef = useRef(false);
  const openRef = useRef(open);
  openRef.current = open;
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;

  // Capture the element that had focus before opening; restore on close.
  useLayoutEffect(() => {
    if (!open) return;
    previouslyFocused.current =
      (document.activeElement as HTMLElement | null) ?? null;
    return () => {
      previouslyFocused.current?.focus?.();
    };
  }, [open]);

  // History sentinel + browser/Android Back (`popstate`) handling.
  useEffect(() => {
    if (!open) return;
    activeRef.current = true;
    // StrictMode remount: if we still own the same token from the prior
    // invoke (and open never went false), reuse it without re-acquiring or
    // re-pushing. Otherwise acquire a fresh lease. A second concurrent sheet
    // gets null (no sentinel).
    if (coordinator.owner?.token === tokenRef.current) {
      // StrictMode replay — reuse the existing lease.
    } else {
      const token = acquire(onCloseRef.current);
      tokenRef.current = token;
      if (token !== null) {
        // Pushing a new sentinel cancels any pending back from a prior
        // lease (pushState truncates the pending navigation), so clear it.
        coordinator.pendingBack = null;
        notifySettled();
        history.pushState({ [SHEET_KEY]: true, token } as SheetMarker, "");
      }
    }
    return () => {
      // Distinguish a real close (open went false) from a StrictMode replay
      // (open stayed true). For a real close, release synchronously so the
      // unwind is initiated before any reopen. For StrictMode/unmount, defer
      // to a microtask so a StrictMode remount (which resets `activeRef`
      // synchronously) cancels it; for unmount, `activeRef` stays false and
      // the sentinel is unwound safely.
      activeRef.current = false;
      const token = tokenRef.current;
      if (!openRef.current) {
        // Real close (open→false): release now.
        release(token);
        tokenRef.current = null;
      } else {
        // StrictMode replay or unmount: defer to microtask.
        queueMicrotask(() => {
          if (activeRef.current) return;
          release(token);
          tokenRef.current = null;
        });
      }
    };
  }, [open]);

  // Keep the coordinator's close handler current across re-renders.
  useEffect(() => {
    if (tokenRef.current !== null) {
      setCloseHandler(tokenRef.current, onCloseRef.current);
    }
  });

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

  // Document-level Escape handler so the key reaches the sheet regardless of
  // which portal child currently holds focus.
  useEffect(() => {
    if (!open || !dismissOnEscape) return;
    const onKey = (e: globalThis.KeyboardEvent) => {
      if (e.key === "Escape") {
        e.stopPropagation();
        requestClose(tokenRef.current, false);
      }
    };
    document.addEventListener("keydown", onKey, true);
    return () => document.removeEventListener("keydown", onKey, true);
  }, [open, dismissOnEscape]);

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
        onClick={() => requestClose(tokenRef.current, false)}
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
          <IconButton
            aria-label="Done"
            onClick={() => requestClose(tokenRef.current, false)}
          >
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
