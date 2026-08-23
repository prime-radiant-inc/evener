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
  /** Optional className appended to the dialog element (e.g. for detent sizing). */
  readonly dialogClassName?: string;
  /** Optional data attributes spread onto the dialog element (e.g. data-activity-detent). */
  readonly dialogDataAttrs?: Record<string, string>;
}

// ---------------------------------------------------------------------------
// Sheet history coordinator
// ---------------------------------------------------------------------------

/** Structured-clone-safe marker keys stored in browser History state. */
const SHEET_KEY = "__evener_sheet";
const SHEET_BASE_KEY = "__evener_sheet_base";
const SHEET_DEAD_KEY = "__evener_sheet_dead";

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

interface SheetDeadMarker {
  readonly __evener_sheet_dead: true;
  readonly token: string;
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

function isSheetDeadMarker(value: unknown): value is SheetDeadMarker {
  return (
    typeof value === "object" &&
    value !== null &&
    (value as Record<string, unknown>)[SHEET_DEAD_KEY] === true &&
    typeof (value as Record<string, unknown>).token === "string" &&
    ((value as Record<string, unknown>).token as string).length > 0
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
  notifyOnComplete: boolean;
}

interface SheetWaiter {
  readonly instanceId: string;
  onClose: () => void;
  grant: Grant;
  revoke: Revoke;
}

interface PendingBack {
  readonly kind: "owner" | "dead" | "recover";
  readonly token: string;
  readonly instanceId: string | null;
}

interface QueuedHistoryWrite {
  readonly kind: "push" | "replace";
  readonly data: unknown;
  readonly unused: string;
  readonly url: string | null;
}

interface SheetCoordinator {
  owner: SheetOwner | null;
  queue: SheetWaiter[];
  pendingBack: PendingBack | null;
  buriedCount: number;
  forwardDead: boolean;
  restorePushObserver: (() => void) | null;
  queuedHistoryWrites: QueuedHistoryWrite[];
  restoreHistoryGate: (() => void) | null;
  listenerInstalled: boolean;
  settledResolvers: Set<() => void>;
}

const coordinator: SheetCoordinator = {
  owner: null,
  queue: [],
  pendingBack: null,
  buriedCount: 0,
  forwardDead: false,
  restorePushObserver: null,
  queuedHistoryWrites: [],
  restoreHistoryGate: null,
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

function stopObservingPushState(): void {
  coordinator.restorePushObserver?.();
  coordinator.restorePushObserver = null;
}

/** A push from the restored base truncates its Forward branch. Observe exactly
 * that bounded period so the dead-forward listener can tear down even when the
 * next write comes from the app/router rather than another Sheet. */
function observeForwardTruncation(): void {
  if (coordinator.restorePushObserver !== null) return;
  const previousPushState = history.pushState;
  const observedPushState: History["pushState"] = function observed(
    data: unknown,
    unused: string,
    url?: string | URL | null,
  ): void {
    previousPushState.call(history, data, unused, url ?? null);
    if (coordinator.forwardDead) {
      setForwardDead(false);
      removeListenerIfIdle();
    }
  };
  history.pushState = observedPushState;
  coordinator.restorePushObserver = () => {
    if (history.pushState === observedPushState) {
      history.pushState = previousPushState;
    }
  };
}

function setForwardDead(value: boolean): void {
  coordinator.forwardDead = value;
  if (value) observeForwardTruncation();
  else stopObservingPushState();
}

function cloneHistoryData(data: unknown): unknown {
  return typeof structuredClone === "function" ? structuredClone(data) : data;
}

/** Queue app/router History writes during one coordinator-owned Back. This
 * preserves their order while preventing a synchronous push from racing and
 * being popped by the already-requested asynchronous traversal. */
function gateHistoryWrites(): void {
  if (coordinator.restoreHistoryGate !== null) return;
  const previousPushState = history.pushState;
  const previousReplaceState = history.replaceState;
  history.pushState = function queuedPush(
    data: unknown,
    unused: string,
    url?: string | URL | null,
  ): void {
    coordinator.queuedHistoryWrites.push({
      kind: "push",
      data: cloneHistoryData(data),
      unused,
      url: url?.toString() ?? null,
    });
  };
  history.replaceState = function queuedReplace(
    data: unknown,
    unused: string,
    url?: string | URL | null,
  ): void {
    coordinator.queuedHistoryWrites.push({
      kind: "replace",
      data: cloneHistoryData(data),
      unused,
      url: url?.toString() ?? null,
    });
  };
  coordinator.restoreHistoryGate = () => {
    history.pushState = previousPushState;
    history.replaceState = previousReplaceState;
  };
}

function takeQueuedHistoryWrites(): QueuedHistoryWrite[] {
  const writes = coordinator.queuedHistoryWrites;
  coordinator.queuedHistoryWrites = [];
  coordinator.restoreHistoryGate?.();
  coordinator.restoreHistoryGate = null;
  return writes;
}

function replayHistoryWrites(writes: readonly QueuedHistoryWrite[]): void {
  for (const write of writes) {
    const method =
      write.kind === "push" ? history.pushState : history.replaceState;
    method.call(history, write.data, write.unused, write.url);
  }
}

function removeListenerIfIdle(): void {
  if (
    !coordinator.listenerInstalled ||
    coordinator.owner !== null ||
    coordinator.pendingBack !== null ||
    coordinator.queue.length > 0 ||
    coordinator.buriedCount > 0 ||
    coordinator.forwardDead
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
  const state = history.state;
  if (
    (isSheetMarker(state) || isSheetDeadMarker(state)) &&
    state.token === pending.token
  ) {
    history.replaceState(
      pending.kind === "owner"
        ? ({ [SHEET_DEAD_KEY]: true, token: pending.token } as SheetDeadMarker)
        : null,
      "",
    );
  }
  coordinator.pendingBack = pending;
  gateHistoryWrites();
  history.back();
}

function grant(waiter: SheetWaiter): void {
  ensureListener();
  const token = newToken("owner");
  coordinator.owner = {
    ...waiter,
    token,
    dismissed: false,
    closing: false,
    notifyOnComplete: false,
  };
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
  setForwardDead(false);
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

function finishOwnerFromBack(token: string): (() => void) | null {
  const owner = coordinator.owner;
  if (owner?.token === token) {
    coordinator.owner = null;
    owner.revoke(token);
    if (owner.notifyOnComplete) {
      owner.notifyOnComplete = false;
      return owner.onClose;
    }
  }
  return null;
}

function onPopState(): void {
  const state = history.state;
  const pending = coordinator.pendingBack;
  if (pending !== null) {
    if (pending.kind === "recover") {
      coordinator.pendingBack = null;
      const queuedWrites = takeQueuedHistoryWrites();
      const owner = coordinator.owner;
      let notifyParent: (() => void) | null = null;
      if (owner?.token === pending.token) {
        coordinator.owner = null;
        coordinator.buriedCount += 1;
        owner.revoke(owner.token);
        if (owner.notifyOnComplete) {
          owner.notifyOnComplete = false;
          notifyParent = owner.onClose;
        }
      }
      setForwardDead(false);
      replayHistoryWrites(queuedWrites);
      notifyParent?.();
      pumpQueue();
      return;
    }

    const landedOnBase =
      isSheetBaseMarker(state) &&
      state.__evener_sheet_base.token === pending.token;
    if (pending.kind === "owner" && !landedOnBase) {
      // A newer unrelated entry was pushed after Back was requested, so the
      // late traversal popped that entry instead of our dead sentinel. Reverse
      // exactly that traversal before finishing the close; never pop farther.
      coordinator.pendingBack = {
        kind: "recover",
        token: pending.token,
        instanceId: pending.instanceId,
      };
      history.forward();
      return;
    }

    coordinator.pendingBack = null;
    const queuedWrites = takeQueuedHistoryWrites();
    if (landedOnBase) restoreBaseMarker(state);
    let notifyParent: (() => void) | null = null;
    if (pending.kind === "owner") {
      setForwardDead(true);
      notifyParent = finishOwnerFromBack(pending.token);
    } else {
      coordinator.buriedCount = Math.max(0, coordinator.buriedCount - 1);
      setForwardDead(false);
    }
    replayHistoryWrites(queuedWrites);
    notifyParent?.();
    // A dead marker may be immediately below another dead marker.
    const current = history.state;
    if (
      (isSheetMarker(current) || isSheetDeadMarker(current)) &&
      coordinator.owner?.token !== current.token
    ) {
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
      setForwardDead(true);
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

  if (isSheetMarker(state) || isSheetDeadMarker(state)) {
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

/** Close the current owner once. The portal is revoked immediately, while the
 * parent callback waits for the matching history completion so router effects
 * cannot race a still-pending Back. */
function requestClose(token: string | null): void {
  if (token === null) return;
  const owner = coordinator.owner;
  if (owner === null || owner.token !== token || owner.dismissed) return;
  owner.dismissed = true;
  owner.revoke(token);
  const state = history.state;
  if (isSheetMarker(state) && state.token === token) {
    owner.closing = true;
    owner.notifyOnComplete = true;
    startBack({ kind: "owner", token, instanceId: owner.instanceId });
  } else {
    // An unrelated app entry is above the sentinel. Do not pop it. The old
    // sentinel is inert and will be skipped if later traversal exposes it.
    coordinator.owner = null;
    coordinator.buriedCount += 1;
    owner.onClose();
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
  stopObservingPushState();
  coordinator.restoreHistoryGate?.();
  if (coordinator.listenerInstalled) {
    window.removeEventListener("popstate", onPopState);
  }
  coordinator.owner = null;
  coordinator.queue = [];
  coordinator.pendingBack = null;
  coordinator.buriedCount = 0;
  coordinator.forwardDead = false;
  coordinator.restorePushObserver = null;
  coordinator.queuedHistoryWrites = [];
  coordinator.restoreHistoryGate = null;
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
  readonly forwardDead: boolean;
  readonly listenerInstalled: boolean;
} {
  return {
    hasOwner: coordinator.owner !== null,
    queueLength: coordinator.queue.length,
    pendingBack: coordinator.pendingBack !== null,
    buriedCount: coordinator.buriedCount,
    forwardDead: coordinator.forwardDead,
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
  dialogClassName,
  dialogDataAttrs,
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
        className={`evener-sheet${dialogClassName ? ` ${dialogClassName}` : ""}`}
        role="dialog"
        aria-modal="true"
        aria-label={title}
        onKeyDown={handleKeyDown}
        {...dialogDataAttrs}
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
