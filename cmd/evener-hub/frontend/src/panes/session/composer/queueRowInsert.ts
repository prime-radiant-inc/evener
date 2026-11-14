// queueRowInsert.ts: the pub/sub bridge that lets the transcript's pending
// rows (the live-edge trailing region, a sibling of Composer under
// ConversationBody) hand a queued entry's text back to that ref's Composer
// without either side depending on the other's internals - the pending rows
// never import Composer, and Composer never imports the pending stack.
// Mirrors quoteInsert.ts (the same vanilla-store-keyed-by-ref shape the rest
// of this pane's cross-component seams use), because the pending stack sits
// in the transcript tree while the restore target sits in the footer, exactly
// SelectionQuote's geometry. A request can also activate a recovery record's
// editing (the "Edit message" affordance a refused-recovery row carries): the
// record rides the same one-shot channel so the Composer's own recovery
// activation lease - which only it can hold - stays the single owner.

import type { ComposerMention } from "@evener/appwire-client";
import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import type { MutationOutboxRecord, MutationRecoveryRecord } from "../../../stores/mutationOutbox";

export interface QueueRowRestoreRequest {
  // Monotonic, not derived from content: two restores of the same entry must
  // read as two separate events, not one already handled (quoteInsert's same
  // rule for quoting the same line twice).
  id: number;
  text: string;
  // The entry's canonical skill/command selections, restored as chips the way
  // the queue strip's edit path restored them (useComposerActions'
  // restoreTextToComposer signature).
  skillNames?: readonly string[];
  commandNames?: readonly string[];
  // The entry's own mention offsets (command/skill markers at their authored
  // positions). Restoring without them re-derives mentions from every marker
  // occurrence in the text; with them the authored selection is the one that
  // survives the edit round-trip - the retired strip's own rule.
  mentions?: readonly ComposerMention[];
  // A refused-recovery row's Edit affordance: the record whose composition the
  // composer must re-open under its own recovery lease.
  recoveryRecord?: MutationRecoveryRecord;
  // A durable outbox row's Edit affordance: the row the composer must claim
  // (the encoding/lease gates run there, before the claim, exactly where
  // the retired strip's handleLocalAction ran them).
  outboxRecord?: MutationOutboxRecord;
  // The pane id of the seat the row belongs to, so one ref's two mounted
  // composers (an embedded cascade column) route the request the same way
  // quote-insert requests do.
  target: string | null;
}

interface QueueRowRestoreState {
  requests: Map<string, QueueRowRestoreRequest>;
  requestQueueRowRestore(ref: string, request: Omit<QueueRowRestoreRequest, "id">): void;
  consumeQueueRowRestore(ref: string): void;
}

let nextId = 1;

const store = createStore<QueueRowRestoreState>((set) => ({
  requests: new Map(),
  requestQueueRowRestore: (ref, request) =>
    set((state) => {
      const requests = new Map(state.requests);
      requests.set(ref, { id: nextId++, ...request });
      return { requests };
    }),
  consumeQueueRowRestore: (ref) =>
    set((state) => {
      if (!state.requests.has(ref)) return state;
      const requests = new Map(state.requests);
      requests.delete(ref);
      return { requests };
    }),
}));

/** The pending rows' write side: records a restore waiting for `ref`'s Composer. */
export function requestQueueRowRestore(ref: string, request: Omit<QueueRowRestoreRequest, "id">): void {
  store.getState().requestQueueRowRestore(ref, request);
}

/** Composer's write side: clears a request once its content has been merged. */
export function consumeQueueRowRestore(ref: string): void {
  store.getState().consumeQueueRowRestore(ref);
}

/** Test- and row-side read of whether `ref` still holds an unconsumed
 * request. A queued row's loser-safe edit cancels its queue entry only once
 * the composer has consumed the restore, so the row needs to observe that
 * moment without owning the composer's bookkeeping. */
export function hasQueueRowRestoreRequest(ref: string): boolean {
  return store.getState().requests.has(ref);
}

/** Bounded wait for `ref`'s pending restore request to be consumed by a
 * mounted composer. The queued row's loser-safe edit removes its queue entry
 * only after this resolves true; a seat with no composer resolves false at
 * the deadline and the row stays queued. The wait resolves on the store's
 * own subscription (a mounted composer consumes in a single effect tick),
 * with the deadline only as the composer-less seat's honest way out. */
export function waitForQueueRowRestoreConsumed(ref: string, deadlineMs = 2000): Promise<boolean> {
  const state = store.getState();
  if (!state.requests.has(ref)) return Promise.resolve(true);
  return new Promise((resolve) => {
    const unsubscribe = store.subscribe((next) => {
      if (!next.requests.has(ref)) {
        unsubscribe();
        clearTimeout(timer);
        resolve(true);
      }
    });
    // Re-check synchronously in case consumption landed between the initial
    // read and the subscription, then arm the composer-less deadline.
    if (!store.getState().requests.has(ref)) {
      unsubscribe();
      return resolve(true);
    }
    const timer = setTimeout(() => {
      unsubscribe();
      resolve(!store.getState().requests.has(ref));
    }, deadlineMs);
  });
}

/** Composer's read side: the pending request for `ref` that belongs to this
 * seat, if any. Same routing rule as useQuoteInsertRequest: an unaddressed
 * request (target null) reads everywhere for the ref; a request naming a pane
 * id reads only under that pane. */
export function useQueueRowRestoreRequest(ref: string, paneId?: string): QueueRowRestoreRequest | undefined {
  return useStore(store, (s) => {
    const request = s.requests.get(ref);
    if (!request) return undefined;
    if (paneId === undefined || request.target === null || request.target === paneId) return request;
    return undefined;
  });
}

/** Test-only: clears every pending request, mirroring this pane's other
 * module-singleton stores' own resetXForTests (quoteInsert.ts, askDockStore.ts). */
export function resetQueueRowRestoreStoreForTests(): void {
  store.setState({ requests: new Map() });
  activeRecoveryStore.setState({ activeRecoveryIds: new Map() });
  composerBusyStore.setState({ busyRefs: new Map() });
}

// The composer's live recovery composition, published per ref: the pending
// rows exclude a recovery record whose composition the composer already
// holds (the retired strip's activeRecoveryId prop, moved to the one seam
// both surfaces already share). The publisher is Composer's own snapshot
// effect; a ref with no mounted composer holds no active recovery here,
// which is also the honest reading - nothing is being edited.
const activeRecoveryStore = createStore<{
  activeRecoveryIds: Map<string, string | null>;
  owners: Map<string, string>;
  publishActiveRecovery(ref: string, id: string | null, owner?: string): void;
}>((set) => ({
  activeRecoveryIds: new Map(),
  owners: new Map(),
  publishActiveRecovery: (ref, id, owner) =>
    set((state) => {
      if (state.activeRecoveryIds.get(ref) === id) return state;
      // A closing pane's cleanup clear must not wipe a sibling composer's
      // publication for the same ref (an embedded cascade column).
      if (id === null && owner !== undefined && state.owners.get(ref) !== owner) return state;
      const activeRecoveryIds = new Map(state.activeRecoveryIds);
      const owners = new Map(state.owners);
      if (id === null) activeRecoveryIds.delete(ref);
      else {
        activeRecoveryIds.set(ref, id);
        if (owner !== undefined) owners.set(ref, owner);
      }
      return { activeRecoveryIds, owners };
    }),
}));

export function publishActiveRecovery(ref: string, id: string | null, owner?: string): void {
  activeRecoveryStore.getState().publishActiveRecovery(ref, id, owner);
}

export function useActiveRecoveryId(ref: string): string | null {
  return useStore(activeRecoveryStore, (s) => s.activeRecoveryIds.get(ref) ?? null);
}

// The composer's shared busy gate (its own busyAction: an in-flight
// submit/steer/drain/interrupt), published per ref beside the active
// recovery id. A recovery row's Edit stays reachable-looking but
// aria-disabled while a send is in flight - the retired strip's busy prop,
// moved to the same seam so the pending rows read the composer's live gate
// without importing it.
const composerBusyStore = createStore<{
  busyRefs: Map<string, boolean>;
  busyOwners: Map<string, string>;
  publishComposerBusy(ref: string, busy: boolean, owner?: string): void;
}>((set) => ({
  busyRefs: new Map(),
  busyOwners: new Map(),
  publishComposerBusy: (ref, busy, owner) =>
    set((state) => {
      if (state.busyRefs.get(ref) === busy) return state;
      // Same sibling rule as the recovery publication above.
      if (!busy && owner !== undefined && state.busyOwners.get(ref) !== owner) return state;
      const busyRefs = new Map(state.busyRefs);
      const busyOwners = new Map(state.busyOwners);
      if (busy) {
        busyRefs.set(ref, true);
        if (owner !== undefined) busyOwners.set(ref, owner);
      } else {
        busyRefs.delete(ref);
        busyOwners.delete(ref);
      }
      return { busyRefs, busyOwners };
    }),
}));

export function publishComposerBusy(ref: string, busy: boolean, owner?: string): void {
  composerBusyStore.getState().publishComposerBusy(ref, busy, owner);
}

export function useComposerBusy(ref: string): boolean {
  return useStore(composerBusyStore, (s) => s.busyRefs.get(ref) === true);
}
