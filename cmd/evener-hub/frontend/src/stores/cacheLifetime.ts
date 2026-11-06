import type { ThreadItemPosition, ThreadModel, ThreadReadResponse, TurnModel } from "@evener/appwire-client";
import {
  applyReadResponse,
  comparePositions,
  hydrateThread,
  mergeTailTurns,
  readDisposition,
  readWindowBounds,
} from "@evener/appwire-client";
import { createVersionedChannel, makeSourceId, type VersionedChannelMessage } from "./versionedChannel";

// The session cache's tab-level lifetime state: the epoch this tab has
// observed, the cross-tab channel, and the live gap rule. This module never
// reads the thread store; threads.ts passes in what these need.

export interface CacheLifetime {
  /** The current model is an unverified cached shell: actions, paging and
   * writes wait for its first authoritative publish. */
  shell?: true;
  /** The newest recorded item position, fixed before any live shell merge. */
  anchor?: ThreadItemPosition;
  /** Clear/deletion write suppression, re-checked at write-fire time. */
  suppressed?: true;
  /** The owned ref's captured epoch, including misses and deadline fallbacks. */
  leaseEpoch?: number;
  /** Marks suppression armed by a cache clear (never a deletion): only this
   * class may be lifted by a fresh explicit foreground acquisition. */
  clearSuppressed?: true;
  /** True while a foreground/history/pin holder that existed when the clear
   * committed still holds the ref: no acquisition may capture a new epoch until
   * all of them release. */
  suppressionHoldsPreClearHolders?: true;
  /** Set when an explicit foreground acquisition begins only after those
   * pre-clear holders released; its authoritative hydration may capture the
   * current epoch and resume cached writes. */
  freshForegroundLifetime?: true;
}

// The tab's in-memory view of the durable clear epoch: undefined until the
// first lookup observes it. Only durable observations advance it, monotonically.
// Optimistic same-tab clear arming lives separately in clearInFlight. The
// missed-message backstop (spec, "Eviction, cap, and cross-tab"): the aborted
// write, a sibling's clear broadcast, or a durable observation is the tab's
// proof that a clear happened.
let tabCacheEpoch: number | undefined;

export function observedCacheEpoch(): number | undefined {
  return tabCacheEpoch;
}

/** Records an observed epoch; false when it is not newer than the one held. */
export function observeCacheEpoch(observed: number): boolean {
  if (tabCacheEpoch !== undefined && observed <= tabCacheEpoch) return false;
  tabCacheEpoch = observed;
  return true;
}

export function suppressCacheLifetimesBefore(
  lifetimes: Map<string, CacheLifetime>,
  epoch: number,
  isHeld: (ref: string) => boolean,
): Map<string, CacheLifetime> {
  const next = new Map(lifetimes);
  for (const [ref, lifetime] of lifetimes) {
    if (lifetime.leaseEpoch !== undefined && lifetime.leaseEpoch < epoch) {
      next.set(ref, {
        ...lifetime,
        suppressed: true,
        clearSuppressed: true,
        suppressionHoldsPreClearHolders: isHeld(ref) ? true : undefined,
      });
    }
  }
  return next;
}

/** The session cache's module state: the epoch view returns to unobserved so
 * the next test observes the fresh database it just deleted, and a test's
 * installed channel factory and its channel go with it, so the next test
 * attaches a fresh channel under the default factory. */
export function resetCacheLifetimeForTests(): void {
  tabCacheEpoch = undefined;
  setCacheChannelFactoryForTests(defaultCacheChannelFactory);
}

// Cross-tab propagation (spec, "The write seam" deletion bullet and the
// eviction section): one BroadcastChannel message per action, the same
// versioned-envelope discipline crossTabSync uses. A browser without
// BroadcastChannel degrades to single-tab: the durable epoch and the
// fire-time gates hold correctness without it.
const CACHE_CHANNEL_NAME = "evener.session-cache.v1";
const cacheSourceId = makeSourceId();
export type CacheChannelMessage = VersionedChannelMessage &
  ({ kind: "deletion"; refs: string[] } | { kind: "clear"; epoch: number });

type CacheChannel = ReturnType<typeof createVersionedChannel<CacheChannelMessage>>;

function isCacheChannelMessage(value: VersionedChannelMessage): value is CacheChannelMessage {
  const candidate = value as Partial<CacheChannelMessage>;
  return (
    (candidate.kind === "deletion" && Array.isArray(candidate.refs)) ||
    (candidate.kind === "clear" && typeof candidate.epoch === "number")
  );
}

// In tests the default factory answers null: the jsdom window has no
// BroadcastChannel, so the visible one is Node's own — which crosses worker
// threads and keeps an open event loop alive, so a real channel would let one
// test file's broadcasts reach another concurrently-running file's handler
// (and stall its worker at exit). Tests that need a channel install one
// through the seam; DEFAULT_OPEN_DIAGNOSTIC's MODE gate is the precedent.
const defaultCacheChannelFactory = (name: string): BroadcastChannel | null =>
  typeof BroadcastChannel !== "undefined" && import.meta.env.MODE !== "test" ? new BroadcastChannel(name) : null;
let createCacheChannel: (name: string) => BroadcastChannel | null = defaultCacheChannelFactory;
let cacheChannel: CacheChannel | undefined;

/** Creates the tab's cache channel and attaches it at once. The tab listens
 * from the moment the store loads, not from its first send: a sibling's
 * deletion must reach this tab even when this tab never deletes anything
 * itself, which is the common tab. A browser without BroadcastChannel stays
 * single-tab (the factory answers null); a failure to attach stays single-tab
 * too. */
export function attachCacheChannel(onMessage: (message: CacheChannelMessage) => void): void {
  cacheChannel = createVersionedChannel<CacheChannelMessage>({
    name: CACHE_CHANNEL_NAME,
    getSourceId: () => cacheSourceId,
    isMessage: isCacheChannelMessage,
    onMessage,
    createChannel: (name) => createCacheChannel(name),
  });
  cacheChannel?.connect();
}

/** Connects first because a failed earlier attach is retried before sends. */
export function postCacheChannelMessage(payload: Parameters<CacheChannel["postMessage"]>[0]): void {
  cacheChannel?.connect();
  cacheChannel?.postMessage(payload);
}

export function setCacheChannelFactoryForTests(factory: (name: string) => BroadcastChannel | null): void {
  cacheChannel?.close();
  createCacheChannel = factory;
  // The installed factory's channel is attached at once: a peer's post must
  // reach this tab's handler even before this tab ever broadcasts, which is
  // the receiving half the channel exists for.
  cacheChannel?.connect();
}

// The gap rule's anchor: the newest item position the record holds, fixed at
// shell-build from pure record data before any live merge, ordered by
// comparePositions (turns can interleave, so the newest positioned item wins
// rather than the last turn's). undefined for a record with no positioned
// item, which Task 7's predicate treats as the ordinary cold merge.
function newestItemPosition(turns: TurnModel[]): ThreadItemPosition | undefined {
  let newest: ThreadItemPosition | undefined;
  for (const turn of turns) {
    for (const item of turn.items) {
      if (item.position === undefined) continue;
      if (newest === undefined || comparePositions(item.position, newest) > 0) newest = item.position;
    }
  }
  return newest;
}

// The live gap rule (spec, "The two serving paths, the live gap, and its
// rule"): a cached-shell reconciling read that carries no changes and whose
// fresh window starts above the shell's captured anchor replaces instead of
// merging. The anchor is captured at shell-build from pure record data, so
// a live fold cannot move it. A response carrying changes merges as usual;
// so does an empty record (the ordinary cold merge) and any disposition the
// identity rules already answer (replace/discard are theirs).
export function applyCacheGapRule(
  ref: string,
  base: ThreadModel,
  response: ThreadReadResponse,
  now: number,
  lifetime: CacheLifetime | undefined,
  live: ThreadModel | undefined,
): ThreadModel {
  const ordinary = () => {
    return applyReadResponse(base, response, now);
  };
  const anchor = lifetime?.anchor;
  if (anchor === undefined || !lifetime?.shell || response.changes !== undefined) return ordinary();
  const held = base.history;
  if (held === undefined || held.turns.length === 0) return ordinary();
  if (readDisposition(held, response) !== "merge") return ordinary();
  const bounds = readWindowBounds(response);
  if (bounds.start === undefined || comparePositions(bounds.start, anchor) <= 0) return ordinary();
  // The window starts above the anchor: replace. Pages drop, the response's
  // cursor is taken. The abut case (start at the anchor's successor) lands
  // here too: the position model has no predecessor function, and the cached
  // pages re-fetch rather than risk a hole (the spec's accepted cost).
  const current = live ?? base;
  const newest = newestItemPosition(current.history?.turns ?? held.turns);
  const replaced = hydrateThread(response, ref, now);
  if (newest === undefined || bounds.end === undefined || comparePositions(newest, bounds.end) <= 0) {
    return replaced; // the response covers everything folded so far
  }
  // The fold is newer than the response: replace, then replay the tail —
  // the model's items above the window's end. They cannot be cached pages,
  // since the anchor sits below the window's start. The result carries the
  // same hole today's live path carries when a notification was dropped;
  // the next authoritative read's merge fills it.
  const end = bounds.end;
  const tail = (current.history?.turns ?? []).flatMap((turn) => {
    const items = turn.items.filter((item) => item.position !== undefined && comparePositions(item.position, end) > 0);
    return items.length === 0 ? [] : [{ ...turn, items }];
  });
  return mergeTailTurns(replaced, tail);
}
