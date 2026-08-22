import {
  type JSX,
  type KeyboardEvent,
  type ReactNode,
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
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

/** Structured-clone-safe marker keys stored in browser History state. */
const SHEET_KEY = "__evener_sheet";
const SHEET_BASE_KEY = "__evener_sheet_base";

interface SheetMarker {
  readonly __evener_sheet: true;
  readonly token: string;
}

interface SheetBaseMarker {
  readonly __evener_sheet_base: {
    readonly token: string;
    readonly previous: unknown;
  };
}

function isSheetMarker(value: unknown): value is SheetMarker {
  return (
    typeof value === "object" &&
    value !== null &&
    (value as Record<string, unknown>)[SHEET_KEY] === true &&
    typeof (value as Record<string, unknown>).token === "string" &&
    ((value as Record<string, unknown>).token as string).length > 0
  );
}

function isSheetBaseMarker(value: unknown): value is SheetBaseMarker {
  if (typeof value !== "object" || value === null) return false;
  const meta = (value as Record<string, unknown>)[SHEET_BASE_KEY];
  return (
    typeof meta === "object" &&
    meta !== null &&
    typeof (meta as Record<string, unknown>).token === "string" &&
    ((meta as Record<string, unknown>).token as string).length > 0
  );
}

type Grant = (token: string) => void;
type Revoke = (token: string) => void;

interface SheetOwner {
  readonly instanceId: string;
  readonly token: string;
  onClose: () => void;
  grant: Grant;
  revoke: Revoke;
  dismissed: boolean;
  closing: boolean;
}

interface SheetWaiter {
  readonly instanceId: string;
  onClose: () => void;
  grant: Grant;
  revoke: Revoke;
}

interface PendingBack {
  readonly kind: "owner" | "dead";
  readonly token: string;
  readonly instanceId: string | null;
}

interface SheetCoordinator {
  owner: SheetOwner | null;
  queue: SheetWaiter[];
  pendingBack: PendingBack | null;
  buriedCount: number;
  listenerInstalled: boolean;
  settledResolvers: Set<() => void>;
}

const coordinator: SheetCoordinator = {
  owner: null,
  queue: [],
  pendingBack: null,
  buriedCount: 0,
  listenerInstalled: false,
  settledResolvers: new Set(),
};

let tokenSequence = 0;

function newToken(prefix: "owner" | "instance"): string {
  tokenSequence += 1;
  const randomUUID = globalThis.crypto?.randomUUID?.();
  return `${prefix}-${randomUUID ?? `${Date.now().toString(36)}-${tokenSequence}`}-${tokenSequence}`;
}

function restoreBaseMarker(state: SheetBaseMarker): void {
  history.replaceState(state.__evener_sheet_base.previous, "");
}

function notifySettled(): void {
  if (coordinator.pendingBack !== null) return;
  const resolvers = coordinator.settledResolvers;
  coordinator.settledResolvers = new Set();
  for (const resolve of resolvers) resolve();
}

function removeListenerIfIdle(): void {
  if (
    !coordinator.listenerInstalled ||
    coordinator.owner !== null ||
    coordinator.pendingBack !== null ||
    coordinator.queue.length > 0 ||
    coordinator.buriedCount > 0
  ) {
    return;
  }
  coordinator.listenerInstalled = false;
  window.removeEventListener("popstate", onPopState);
}

function ensureListener(): void {
  if (coordinator.listenerInstalled) return;
  coordinator.listenerInstalled = true;
  window.addEventListener("popstate", onPopState);
}

function startBack(pending: PendingBack): void {
  if (coordinator.pendingBack !== null) return;
  coordinator.pendingBack = pending;
  history.back();
}

function grant(waiter: SheetWaiter): void {
  ensureListener();
  const token = newToken("owner");
  const previous = history.state;
  const preservedFields =
    typeof previous === "object" &&
    previous !== null &&
    !Array.isArray(previous)
      ? (previous as Record<string, unknown>)
      : {};
  history.replaceState(
    {
      ...preservedFields,
      [SHEET_BASE_KEY]: { token, previous },
    } as SheetBaseMarker,
    "",
  );
  history.pushState({ [SHEET_KEY]: true, token } as SheetMarker, "");
  coordinator.owner = {
    ...waiter,
    token,
    dismissed: false,
    closing: false,
  };
  waiter.grant(token);
}

function pumpQueue(): void {
  if (coordinator.owner !== null || coordinator.pendingBack !== null) return;
  const waiter = coordinator.queue.shift();
  if (waiter !== undefined) grant(waiter);
  notifySettled();
  removeListenerIfIdle();
}

function requestOwnership(
  instanceId: string,
  onClose: () => void,
  grantToken: Grant,
  revokeToken: Revoke,
): () => void {
  ensureListener();
  const owner = coordinator.owner;
  if (owner?.instanceId === instanceId && !owner.closing) {
    owner.onClose = onClose;
    owner.grant = grantToken;
    owner.revoke = revokeToken;
    grantToken(owner.token);
  } else {
    coordinator.queue = coordinator.queue.filter(
      (waiter) => waiter.instanceId !== instanceId,
    );
    coordinator.queue.push({
      instanceId,
      onClose,
      grant: grantToken,
      revoke: revokeToken,
    });
    pumpQueue();
  }
  return () => {
    coordinator.queue = coordinator.queue.filter(
      (waiter) => waiter.instanceId !== instanceId,
    );
    removeListenerIfIdle();
  };
}

function updateHandlers(
  instanceId: string,
  onClose: () => void,
  grantToken: Grant,
  revokeToken: Revoke,
): void {
  if (coordinator.owner?.instanceId === instanceId) {
    coordinator.owner.onClose = onClose;
    coordinator.owner.grant = grantToken;
    coordinator.owner.revoke = revokeToken;
  }
  const waiter = coordinator.queue.find(
    (candidate) => candidate.instanceId === instanceId,
  );
  if (waiter !== undefined) {
    waiter.onClose = onClose;
    waiter.grant = grantToken;
    waiter.revoke = revokeToken;
  }
}

function finishOwnerFromBack(token: string): void {
  const owner = coordinator.owner;
  if (owner?.token === token) {
    coordinator.owner = null;
    owner.revoke(token);
  }
}

function onPopState(): void {
  const state = history.state;
  const pending = coordinator.pendingBack;
  if (pending !== null) {
    coordinator.pendingBack = null;
    if (
      isSheetBaseMarker(state) &&
      state.__evener_sheet_base.token === pending.token
    ) {
      restoreBaseMarker(state);
    }
    if (pending.kind === "owner") {
      finishOwnerFromBack(pending.token);
    } else {
      coordinator.buriedCount = Math.max(0, coordinator.buriedCount - 1);
    }
    // A dead marker may be immediately below another dead marker.
    const current = history.state;
    if (isSheetMarker(current) && coordinator.owner?.token !== current.token) {
      startBack({ kind: "dead", token: current.token, instanceId: null });
      return;
    }
    pumpQueue();
    return;
  }

  const owner = coordinator.owner;
  if (owner !== null) {
    if (isSheetMarker(state) && state.token === owner.token) {
      // An unrelated entry above the sentinel was popped; the sheet still owns
      // the now-current sentinel and remains open.
      return;
    }
    if (
      isSheetBaseMarker(state) &&
      state.__evener_sheet_base.token === owner.token
    ) {
      restoreBaseMarker(state);
      coordinator.owner = null;
      owner.revoke(owner.token);
      if (!owner.dismissed) {
        owner.dismissed = true;
        owner.onClose();
      }
      pumpQueue();
      return;
    }
    // Any other app/router state is unrelated navigation while our sentinel is
    // buried. Never close or pop it.
    return;
  }

  if (isSheetMarker(state)) {
    // A retired sentinel was exposed by Back/Forward. Skip it once; no Sheet is
    // notified because there is no live owner.
    startBack({ kind: "dead", token: state.token, instanceId: null });
    return;
  }
  if (isSheetBaseMarker(state)) {
    restoreBaseMarker(state);
    coordinator.buriedCount = Math.max(0, coordinator.buriedCount - 1);
  }
  pumpQueue();
}

/** Close the current owner once. Explicit closes notify immediately for UI
 * responsiveness, then serialize the history unwind before another owner is
 * granted. */
function requestClose(token: string | null): void {
  if (token === null) return;
  const owner = coordinator.owner;
  if (owner === null || owner.token !== token || owner.dismissed) return;
  owner.dismissed = true;
  owner.revoke(token);
  owner.onClose();
  const state = history.state;
  if (isSheetMarker(state) && state.token === token) {
    owner.closing = true;
    startBack({ kind: "owner", token, instanceId: owner.instanceId });
  } else {
    // An unrelated app entry is above the sentinel. Do not pop it. The old
    // sentinel is inert and will be skipped if later traversal exposes it.
    coordinator.owner = null;
    coordinator.buriedCount += 1;
    pumpQueue();
  }
}

/** Release ownership without notifying the parent (external close/unmount). */
function release(instanceId: string, token: string | null): void {
  if (token === null) return;
  const owner = coordinator.owner;
  if (
    owner === null ||
    owner.instanceId !== instanceId ||
    owner.token !== token ||
    owner.closing
  ) {
    return;
  }
  owner.revoke(token);
  const state = history.state;
  if (isSheetMarker(state) && state.token === token) {
    owner.closing = true;
    startBack({ kind: "owner", token, instanceId });
  } else {
    coordinator.owner = null;
    coordinator.buriedCount += 1;
    pumpQueue();
  }
}

/** Await all coordinator-owned traversals and queued lease grants. */
function settled(): Promise<void> {
  return new Promise((resolve) => {
    queueMicrotask(() => {
      if (coordinator.pendingBack === null) resolve();
      else coordinator.settledResolvers.add(resolve);
    });
  });
}

/** Test-only reset after all pending traversals have settled. */
function resetCoordinator(): void {
  if (coordinator.listenerInstalled) {
    window.removeEventListener("popstate", onPopState);
  }
  coordinator.owner = null;
  coordinator.queue = [];
  coordinator.pendingBack = null;
  coordinator.buriedCount = 0;
  coordinator.listenerInstalled = false;
  coordinator.settledResolvers = new Set();
}

/** @internal Exported for deterministic tests. */
export function __sheetHistorySettled(): Promise<void> {
  return settled();
}

/** @internal Exported for deterministic tests. */
export function __resetSheetHistory(): void {
  resetCoordinator();
}

/** @internal Bounded coordinator state for leak/ownership assertions. */
export function __sheetHistoryDebug(): {
  readonly hasOwner: boolean;
  readonly queueLength: number;
  readonly pendingBack: boolean;
  readonly buriedCount: number;
  readonly listenerInstalled: boolean;
} {
  return {
    hasOwner: coordinator.owner !== null,
    queueLength: coordinator.queue.length,
    pendingBack: coordinator.pendingBack !== null,
    buriedCount: coordinator.buriedCount,
    listenerInstalled: coordinator.listenerInstalled,
  };
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
 * retains a single `popstate` listener while needed, serializes pending unwinds
 * before granting the next lease, enforces one visible global owner, and skips
 * buried dead sentinels only when navigation later exposes them.
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
  const instanceRef = useRef<string | null>(null);
  if (instanceRef.current === null) instanceRef.current = newToken("instance");
  const [historyReady, setHistoryReady] = useState(false);
  const activeRef = useRef(false);
  const openRef = useRef(open);
  openRef.current = open;
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;

  const grantToken = useCallback((token: string) => {
    tokenRef.current = token;
    if (activeRef.current && openRef.current) setHistoryReady(true);
  }, []);

  const revokeToken = useCallback((token: string) => {
    if (tokenRef.current !== token) return;
    tokenRef.current = null;
    if (activeRef.current) setHistoryReady(false);
  }, []);

  // Capture the element that had focus before opening; restore on close.
  useLayoutEffect(() => {
    if (!open || !historyReady) return;
    previouslyFocused.current =
      (document.activeElement as HTMLElement | null) ?? null;
    return () => {
      previouslyFocused.current?.focus?.();
    };
  }, [open, historyReady]);

  // History sentinel + browser/Android Back (`popstate`) handling.
  useEffect(() => {
    if (!open) {
      setHistoryReady(false);
      return;
    }
    activeRef.current = true;
    const instanceId = instanceRef.current as string;
    const cancelRequest = requestOwnership(
      instanceId,
      onCloseRef.current,
      grantToken,
      revokeToken,
    );
    return () => {
      // Distinguish a real close (open went false) from a StrictMode replay
      // (open stayed true). For a real close, release synchronously so the
      // unwind is initiated before any reopen. For StrictMode/unmount, defer
      // to a microtask so a StrictMode remount (which resets `activeRef`
      // synchronously) cancels it; for unmount, `activeRef` stays false and
      // the sentinel is unwound safely.
      activeRef.current = false;
      cancelRequest();
      const token = tokenRef.current;
      if (!openRef.current) {
        // Real close (open→false): release now.
        release(instanceId, token);
      } else {
        // StrictMode replay or unmount: defer to microtask.
        queueMicrotask(() => {
          if (activeRef.current) return;
          release(instanceId, token);
        });
      }
    };
  }, [open, grantToken, revokeToken]);

  // Keep the coordinator's close handler current across re-renders.
  useEffect(() => {
    updateHandlers(
      instanceRef.current as string,
      onCloseRef.current,
      grantToken,
      revokeToken,
    );
  });

  // Focus the first focusable element when the sheet opens.
  useEffect(() => {
    if (!open || !historyReady) return;
    const dialog = dialogRef.current;
    if (dialog === null) return;
    // Defer one frame so portal content is painted.
    const id = window.requestAnimationFrame(() => {
      const focusable = queryFocusable(dialog);
      focusable[0]?.focus();
    });
    return () => window.cancelAnimationFrame(id);
  }, [open, historyReady]);

  // Document-level Escape handler so the key reaches the sheet regardless of
  // which portal child currently holds focus.
  useEffect(() => {
    if (!open || !historyReady || !dismissOnEscape) return;
    const onKey = (e: globalThis.KeyboardEvent) => {
      if (e.key === "Escape") {
        e.stopPropagation();
        requestClose(tokenRef.current);
      }
    };
    document.addEventListener("keydown", onKey, true);
    return () => document.removeEventListener("keydown", onKey, true);
  }, [open, historyReady, dismissOnEscape]);

  const handleKeyDown = useCallback((event: KeyboardEvent<HTMLDivElement>) => {
    if (event.key === "Tab") {
      trapTab(event, dialogRef.current);
    }
  }, []);

  if (!open || !historyReady) return null;

  return createPortal(
    <>
      <div
        className="evener-sheet-overlay"
        onClick={() => requestClose(tokenRef.current)}
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
            onClick={() => requestClose(tokenRef.current)}
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
