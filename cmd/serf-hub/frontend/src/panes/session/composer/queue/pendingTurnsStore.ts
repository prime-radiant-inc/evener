// Store-owned optimistic-pending state for composer submissions
// (send/steer/queue/drain), applied uniformly across all four methods (the
// wave's own beyond-parity decision - see the wave plan's Binding
// constraints - the legacy plain-send asymmetry, cmd/serf-hub/assets/
// appwire.js:513-518, is deliberately NOT carried forward).
//
// This module wires itself to the threads store's own model (wire truth) at
// load time - it never talks to an AppwireClient directly, per this wave's
// binding constraint. Reconciliation is a pure diff (pendingReconcile.ts)
// over consecutive ThreadModel snapshots; this file is only the impure glue:
// a zustand store for the pending entries themselves, the 10s timeout
// reaper, and submitWithPendingTracking as the one public entry point
// callers use instead of registering/resolving/failing an entry by hand.
import { useMemo } from "react";
import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import type { ThreadModel } from "../../../../protocol/model";
import type { InputAttachment } from "../../../../stores/threads";
import { threadsStore } from "../../../../stores/threads";
import { SYSTEM_PRELUDE_TURN_ID } from "../../transcript/transcriptVisibility";
import { collectItemIds, computeReconciledIds, type PendingMethod, type PendingTurnEntry } from "./pendingReconcile";

export type { PendingMethod, PendingTurnEntry } from "./pendingReconcile";

export const PENDING_TIMEOUT_MS = 10_000;

export class PendingConfirmationTimeoutError extends Error {
  constructor() {
    super("The server accepted this message, but the view didn't update.");
    this.name = "PendingConfirmationTimeoutError";
  }
}

export function isPendingConfirmationTimeoutError(error: unknown): error is PendingConfirmationTimeoutError {
  return error instanceof PendingConfirmationTimeoutError;
}

interface PendingTurnsStoreState {
  entries: Map<string, PendingTurnEntry>;
  // A successful send remains here after its user echo reconciles the
  // pending entry, until the first authoritative assistant/tool frame or a
  // terminal turn state arrives.
  awaitingFirstFrame: Map<string, string>;
}

const pendingTurnsStore = createStore<PendingTurnsStoreState>(() => ({
  entries: new Map(),
  awaitingFirstFrame: new Map(),
}));

// Module-private bookkeeping, deliberately not part of the store's own
// reactive state - mirrors stores/threads.ts's own refCounts/inflightHydrates
// split (public reactive state vs. private plumbing the store's actions
// close over).
let nextId = 0;
const timeoutHandles = new Map<string, ReturnType<typeof setTimeout>>();
const failureCallbacks = new Map<string, (error: unknown) => void>();
const settledPerformIds = new Set<string>();
// lastSeenModels is the reconciliation diff baseline: the last ThreadModel
// this module has actually scanned for each ref. It must advance on EVERY
// threads-store change (not just when something currently has a pending
// entry), or a later-registered entry could wrongly match an item that
// arrived before it was ever registered - see reconcileAll's own comment.
const lastSeenModels = new Map<string, ThreadModel>();

const THREAD_TERMINAL_STATUSES = new Set(["closed", "systemError"]);
const TURN_TERMINAL_STATUSES = new Set(["cancelled", "canceled", "completed", "error", "failed", "interrupted"]);

function isThreadTerminalStatus(status: string): boolean {
  return THREAD_TERMINAL_STATUSES.has(status);
}

function isTurnTerminalStatus(status: string): boolean {
  return TURN_TERMINAL_STATUSES.has(status.toLowerCase());
}

function hasTerminalRealTurn(model: ThreadModel): boolean {
  return model.turns.some((turn) => turn.id !== SYSTEM_PRELUDE_TURN_ID && isTurnTerminalStatus(turn.status));
}

function hasAuthoritativeFrame(model: ThreadModel): boolean {
  return model.turns.some((turn) =>
    turn.items.some((item) => item.type !== "userMessage" && item.type !== "systemMessage"),
  );
}

function addAwaitingFirstFrame(id: string, ref: string): void {
  const model = threadsStore.getState().threads.get(ref);
  if (
    model &&
    (isThreadTerminalStatus(model.status.type) || hasTerminalRealTurn(model) || hasAuthoritativeFrame(model))
  ) {
    return;
  }

  pendingTurnsStore.setState((s) => {
    if (s.awaitingFirstFrame.has(id)) return s;
    const next = new Map(s.awaitingFirstFrame);
    next.set(id, ref);
    return { awaitingFirstFrame: next };
  });
}

function removeAwaitingFirstFrame(id: string): void {
  pendingTurnsStore.setState((s) => {
    if (!s.awaitingFirstFrame.has(id)) return s;
    const next = new Map(s.awaitingFirstFrame);
    next.delete(id);
    return { awaitingFirstFrame: next };
  });
}

function reconcileAwaitingFirstFrames(threads: Map<string, ThreadModel>): void {
  const awaiting = pendingTurnsStore.getState().awaitingFirstFrame;
  if (awaiting.size === 0) return;

  const next = new Map(awaiting);
  for (const [id, ref] of awaiting) {
    const model = threads.get(ref);
    if (
      !model ||
      isThreadTerminalStatus(model.status.type) ||
      hasTerminalRealTurn(model) ||
      hasAuthoritativeFrame(model)
    ) {
      next.delete(id);
      if (!pendingTurnsStore.getState().entries.has(id)) clearTimeoutHandle(id);
    }
  }
  if (next.size !== awaiting.size) pendingTurnsStore.setState({ awaitingFirstFrame: next });
}

// removeEntry drops one entry (its timer, its failure callback, and the
// store record) - used by both the single-entry failure path and (via
// resolveMany) the batched reconciliation path.
function clearBookkeeping(id: string): void {
  const timer = timeoutHandles.get(id);
  if (timer !== undefined) clearTimeout(timer);
  timeoutHandles.delete(id);
  failureCallbacks.delete(id);
  settledPerformIds.delete(id);
}

function clearTimeoutHandle(id: string): void {
  const timer = timeoutHandles.get(id);
  if (timer !== undefined) clearTimeout(timer);
  timeoutHandles.delete(id);
}

function removeEntry(id: string): void {
  clearBookkeeping(id);
  pendingTurnsStore.setState((s) => {
    if (!s.entries.has(id)) return s;
    const next = new Map(s.entries);
    next.delete(id);
    return { entries: next };
  });
}

// resolveMany removes every id in one batched store update, so a single
// reconcile pass that confirms several entries at once (e.g. two queued
// messages both echoed back in the same thread/queueChanged) triggers one
// re-render, not one per entry.
function resolveMany(ids: string[]): void {
  if (ids.length === 0) return;
  for (const id of ids) {
    const entry = pendingTurnsStore.getState().entries.get(id);
    const awaitingFirstFrame = pendingTurnsStore.getState().awaitingFirstFrame.has(id);
    const performSettled = settledPerformIds.delete(id);
    // Keep the timer for an unresolved perform: the wire echo only removed
    // the optimistic chip, not the send lifecycle. A send whose first
    // authoritative frame already arrived no longer needs that lifecycle;
    // queue/steer/drain entries still use the timer for their pending chip.
    if (performSettled || entry?.method !== "send" || !awaitingFirstFrame) {
      clearTimeoutHandle(id);
    }
    // If perform() already settled, no later failure can arrive. When it is
    // still unresolved, retain the callback so a late rejection remains
    // visible even though the wire echo already removed the pending chip.
    if (performSettled) failureCallbacks.delete(id);
  }
  pendingTurnsStore.setState((s) => {
    const next = new Map(s.entries);
    for (const id of ids) next.delete(id);
    return { entries: next };
  });
}

function failPendingTurn(id: string, error: unknown): void {
  const onFailure = failureCallbacks.get(id);
  removeEntry(id);
  // A send may already have had its pending chip reconciled by the user echo
  // while perform() is still unresolved. Remove its explicit lifecycle by id
  // on every failure so that late rejection and timeout paths cannot leave a
  // stale skeleton behind. Non-send entries simply have no matching lifecycle.
  removeAwaitingFirstFrame(id);
  onFailure?.(error);
}

function timeoutPendingTurn(id: string): void {
  const state = pendingTurnsStore.getState();
  if (!state.entries.has(id) && !state.awaitingFirstFrame.has(id)) return; // already resolved or failed
  failPendingTurn(id, new PendingConfirmationTimeoutError());
}

function retireReleasedPendingTurns(threads: Map<string, ThreadModel>): void {
  const state = pendingTurnsStore.getState();
  const retiredIds = new Set<string>();
  for (const [id, entry] of state.entries) {
    if (!threads.has(entry.ref)) retiredIds.add(id);
  }
  for (const [id, ref] of state.awaitingFirstFrame) {
    if (!threads.has(ref)) retiredIds.add(id);
  }
  if (retiredIds.size === 0) return;

  for (const id of retiredIds) clearBookkeeping(id);
  pendingTurnsStore.setState((s) => {
    const entries = new Map(s.entries);
    const awaitingFirstFrame = new Map(s.awaitingFirstFrame);
    for (const id of retiredIds) {
      entries.delete(id);
      awaitingFirstFrame.delete(id);
    }
    return { entries, awaitingFirstFrame };
  });
}

// reconcileAll runs on every threads-store change, diffing each ref's
// CURRENT model against the last one this module observed for that ref, and
// resolving whichever pending entries computeReconciledIds says the diff
// confirms. A ref whose model reference is unchanged since last time is
// skipped entirely (same no-op idiom the threads store itself uses), and
// the diff baseline is advanced for every ref actually present, whether or
// not it currently has any pending entries - see lastSeenModels's own
// comment for why that unconditional advance matters.
function reconcileAll(threads: Map<string, ThreadModel>): void {
  const entries = pendingTurnsStore.getState().entries;
  const toResolve: string[] = [];

  for (const [ref, model] of threads) {
    const prev = lastSeenModels.get(ref);
    if (prev === model) continue;
    const priorItemIds = collectItemIds(prev);
    lastSeenModels.set(ref, model);

    const refEntries = Array.from(entries.values()).filter((e) => e.ref === ref);
    if (refEntries.length === 0) continue;

    toResolve.push(...computeReconciledIds(refEntries, model, priorItemIds));
  }

  resolveMany(toResolve);
  retireReleasedPendingTurns(threads);
  reconcileAwaitingFirstFrames(threads);

  // Forget any ref the threads store no longer tracks at all (pane closed) -
  // a snapshot of the keys since delete() mutates lastSeenModels mid-loop.
  for (const ref of Array.from(lastSeenModels.keys())) {
    if (!threads.has(ref)) lastSeenModels.delete(ref);
  }
}

threadsStore.subscribe((state) => {
  reconcileAll(state.threads);
});

export interface SubmitWithPendingTrackingOptions {
  ref: string;
  method: PendingMethod;
  text: string;
  attachments?: InputAttachment[];
  // Called with the raw failure (the perform() rejection value, or a
  // synthesized Error for the 10s timeout reaper) - deliberately the raw
  // error, not a pre-formatted string, so a caller with method-specific
  // knowledge (e.g. the queue strip's own drain action distinguishing a
  // WireError with serfErrorInfo "queuedDrainPartial" from any other
  // failure - parity-m5-composer.md §A) can format its own message; a
  // caller with no such nuance can still do the simple thing
  // (`err instanceof Error ? err.message : String(err)`) itself. The wave's
  // own failure-feedback convention (Session.tsx's loadOlder catch is the
  // reference implementation) has the caller push the resulting message via
  // useToasts(). Kept as an explicit callback (rather than this module
  // importing the toast widget directly) so this store stays free of any
  // widget-layer dependency, consistent with pendingReconcile.ts/
  // queueDisplay.ts being pure and this file's own scope being store
  // plumbing, not UI.
  onFailure: (error: unknown) => void;
}

// submitWithPendingTracking is the one public entry point a caller (this
// stream's own QueueStrip, and - at the wave integration merge - T2's
// composer core for its own send/steer calls) uses instead of registering/
// resolving/failing an entry by hand: it registers a pending entry, calls
// `perform` (the caller's own threadsStore action invocation), and on a
// rejection fails the entry (removing it and reporting via onFailure)
// before rethrowing so the caller's OWN catch can still run its own
// non-toast bookkeeping (e.g. "don't clear the textarea on failure").
//
// A SUCCESSFUL perform() deliberately does not resolve the entry itself -
// mirrors the legacy registry's own "a successful RPC response does NOT
// itself reconcile the pending chip" rule (test-optimistic-rendering.js):
// the entry stays pending until a matching wire echo (or the timeout
// reaper) resolves it via reconcileAll above.
export async function submitWithPendingTracking(
  opts: SubmitWithPendingTrackingOptions,
  perform: () => Promise<void>,
): Promise<void> {
  nextId += 1;
  const id = `pending_${nextId}`;
  const entry: PendingTurnEntry = {
    id,
    ref: opts.ref,
    method: opts.method,
    text: opts.text,
    imageCount: opts.attachments?.length ?? 0,
    createdAt: Date.now(),
  };
  failureCallbacks.set(id, opts.onFailure);
  if (opts.method === "send") addAwaitingFirstFrame(id, opts.ref);
  pendingTurnsStore.setState((s) => {
    const next = new Map(s.entries);
    next.set(id, entry);
    return { entries: next };
  });

  try {
    await perform();
    settledPerformIds.add(id);
    if (pendingTurnsStore.getState().entries.has(id)) {
      timeoutHandles.set(
        id,
        setTimeout(() => timeoutPendingTurn(id), PENDING_TIMEOUT_MS),
      );
    } else {
      settledPerformIds.delete(id);
      failureCallbacks.delete(id);
    }
  } catch (err) {
    failPendingTurn(id, err);
    throw err;
  }
}

const NO_ENTRIES: PendingTurnEntry[] = [];

// usePendingTurnEntries selects the pending entries for one ref (optionally
// narrowed to one method), memoized against the store's own entries Map
// reference so a change to an UNRELATED ref's entries doesn't recompute (or
// re-render) this selector's callers - the store's Map reference only
// changes when SOMETHING in it changed (registerPendingTurn/removeEntry/
// resolveMany all follow the same copy-on-write idiom as stores/threads.ts),
// but filtering that Map into a fresh array on every call would otherwise
// defeat that stability.
export function usePendingTurnEntries(ref: string, method?: PendingMethod): PendingTurnEntry[] {
  const entries = useStore(pendingTurnsStore, (s) => s.entries);
  return useMemo(() => {
    const matches: PendingTurnEntry[] = [];
    for (const entry of entries.values()) {
      if (entry.ref !== ref) continue;
      if (method !== undefined && entry.method !== method) continue;
      matches.push(entry);
    }
    return matches.length > 0 ? matches : NO_ENTRIES;
  }, [entries, ref, method]);
}

export function useAwaitingFirstFrameSend(ref: string): boolean {
  const awaiting = useStore(pendingTurnsStore, (s) => s.awaitingFirstFrame);
  return useMemo(() => {
    for (const awaitingRef of awaiting.values()) {
      if (awaitingRef === ref) return true;
    }
    return false;
  }, [awaiting, ref]);
}

// resetPendingTurnsStoreForTests resets every module-private/store field to
// its initial state, mirroring stores/threads.ts's own
// resetThreadsStoreForTests - this module is a singleton (one Map, one id
// counter, one diff baseline) shared by the whole app, so tests must reset
// it between runs to stay isolated. No production code should ever call
// this. The top-level threadsStore.subscribe() above is never re-registered
// here (same rationale as threads.ts's own permanent connectionStore
// subscription): it always reads fresh state on its next fire, so it
// reflects whatever this reset just cleared without needing to resubscribe.
export function resetPendingTurnsStoreForTests(): void {
  for (const timer of timeoutHandles.values()) clearTimeout(timer);
  timeoutHandles.clear();
  failureCallbacks.clear();
  settledPerformIds.clear();
  lastSeenModels.clear();
  pendingTurnsStore.setState({ entries: new Map(), awaitingFirstFrame: new Map() });
}
