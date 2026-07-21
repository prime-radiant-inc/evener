// threads.ts tracks the ThreadModel for every ref currently open in a pane,
// refcounted across panes sharing the same ref, and routes live wire
// notifications into the reducer for whichever tracked model(s) they target.
// It rides the single AppwireClientLike connection.ts wires via
// useConnectionStore.getState().connect(client) — this store has no
// connect() of its own — and reactively re-attaches its onNotification/onReady
// handlers to whatever client connectionStore currently holds, via a
// connectionStore.subscribe() wired at module load (see rewireClient).
import { createStore } from "zustand/vanilla";
import { useStore } from "zustand";
import { connectionStore } from "./connection";
import type { AppwireClientLike } from "../protocol/testing/fakeClient";
import { applyNotification, hydrateThread, notificationTargetsThread, prependOlderTurns } from "../protocol/reducer";
import type { ThreadModel } from "../protocol/model";
import type { AnyNotification, InputItem, ThreadReadResponse, ThreadTurnsListResponse } from "../protocol/types.gen";
import { WireError } from "../protocol/errors";

export class ConflictError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "ConflictError";
  }
}

export interface ThreadsStoreState {
  threads: Map<string, ThreadModel>;
  // Per-ref ring of live-notification arrival timestamps, for
  // widgets/cadence's Cadence trace - see appendFrameTime below. Deliberately
  // NOT part of ThreadModel/the reducer: it is display-liveness bookkeeping
  // the store layers on top, not wire-derived thread state, and (unlike
  // `threads`) it only grows from notifications actually applied live - a
  // hydrate/re-hydrate never seeds or resets it (see handleReady/rewireClient
  // below, which touch `threads` but not this map).
  frameTimes: Map<string, number[]>;
  ensureThread(ref: string): Promise<void>;
  releaseThread(ref: string): void;
  loadOlderTurns(ref: string): Promise<void>;
  send(ref: string, text: string, images?: string[]): Promise<void>;
  steer(ref: string, text: string): Promise<void>;
  queue(ref: string, text: string): Promise<void>;
  interrupt(ref: string): Promise<void>;
}

// Module-private bookkeeping the locked interface doesn't expose: pane
// refcounts per ref, the hydrate promise currently in flight for a ref (so
// two panes racing to ensureThread() the same ref share one thread/read
// instead of sending two), and which client this store has already wired
// its notification/ready handlers onto (plus that wiring's own unsubscribe
// functions - see rewireClient below).
const refCounts = new Map<string, number>();
const inflightHydrates = new Map<string, Promise<ThreadModel>>();
let wiredClient: AppwireClientLike | null = null;
let unwireNotification: (() => void) | null = null;
let unwireReady: (() => void) | null = null;

// Every tracked ref gets exactly these params on both the first subscribe
// (ensureThread) and every re-subscribe (onReady after reconnect):
// replaceSubscription is always false — additive, layering onto whatever the
// daemon already tracks for this client rather than resetting it.
function readParams(ref: string) {
  return { ref, includeTurns: true, itemsView: "full", subscribe: true, replaceSubscription: false, turnLimit: 40 } as const;
}

async function hydrateAndSubscribe(client: AppwireClientLike, ref: string, now: number): Promise<ThreadModel> {
  const resp: ThreadReadResponse = await client.request("thread/read", readParams(ref));
  return hydrateThread(resp, ref, now);
}

// Older-turn paging (loadOlderTurns): same 30-turn page size as the legacy
// renderer's own OLDER_TURN_PAGE (cmd/serf-hub/assets/renderer.js, cited in
// docs/web-ui/parity/parity-m4-transcript.md §18) - not load-bearing for
// correctness, just a reasonable, parity-matching default a later wave can
// retune once it owns the scroll-triggered paging UX (T4).
const OLDER_TURNS_PAGE_SIZE = 30;

function olderTurnsParams(ref: string, cursor: string) {
  return { ref, cursor, itemsView: "full", limit: OLDER_TURNS_PAGE_SIZE } as const;
}

// FRAME_TIMES_WINDOW_MS matches widgets/cadence's own WINDOW_MS exactly
// (the trace it renders) so the ring never evicts a sample Cadence would
// still want to show; FRAME_TIMES_MAX_ENTRIES is an independent cap purely
// against runaway growth during a high-frequency notification burst within
// that same 60s window (a long-lived, mostly-idle thread's ring stays far
// under 64 on the window alone).
export const FRAME_TIMES_WINDOW_MS = 60_000;
export const FRAME_TIMES_MAX_ENTRIES = 64;

// appendFrameTime is a pure ring-buffer step: append `now`, evict anything
// older than the trace window (mirroring Cadence's own ticksFor exclusion,
// `age > WINDOW_MS`, so the two boundaries agree exactly), then cap at
// FRAME_TIMES_MAX_ENTRIES, keeping the most recent. `times` need not be
// sorted (Cadence's own frameTimes prop doc says the same) - this never
// re-sorts, only filters and slices.
export function appendFrameTime(times: number[], now: number): number[] {
  const kept = times.filter((t) => now - t <= FRAME_TIMES_WINDOW_MS);
  const next = [...kept, now];
  return next.length > FRAME_TIMES_MAX_ENTRIES ? next.slice(next.length - FRAME_TIMES_MAX_ENTRIES) : next;
}

function buildInput(text: string, images?: string[]): InputItem[] {
  const input: InputItem[] = [];
  if (text.trim()) input.push({ type: "text", text });
  for (const url of images ?? []) input.push({ type: "image", url });
  return input;
}

// mapConflict is the one WireError shape the daemon uses for turn CAS losers
// (turn/start, turn/steer, turn/queue, turn/interrupt): code -32013 with
// data.serfErrorInfo === "conflict" (appwire.Conflict(), appwire/errors.go).
// The discriminator is the serfErrorInfo string, not the code alone — code
// -32013 is also used by appwire.QueuedDrainPartial with a different
// serfErrorInfo, which must NOT map to ConflictError. Any other rejection
// (a different WireError, RequestTimeoutError, ConnectionClosedError, ...)
// passes through unchanged.
function mapConflict(err: unknown): Error {
  if (err instanceof WireError && err.serfErrorInfo === "conflict") {
    return new ConflictError(err.message);
  }
  return err instanceof Error ? err : new Error(String(err));
}

// targetsNotification decides whether one live notification belongs to
// `model`. turn/completed is the one exception: its payload carries no
// ref/threadId at all, and turn ids are per-thread sequential, so the same
// turnId can legitimately be the active turn on at most one tracked model at
// a time — activeTurnId match is therefore both necessary and (in practice)
// sufficient to route it correctly. Every other notification carries
// ref/threadId and is routed by notificationTargetsThread, same as the
// reducer's own tests exercise it.
function targetsNotification(n: AnyNotification, model: ThreadModel): boolean {
  if (n.method === "turn/completed") {
    const turnId = n.params.turnId || n.params.turn.id;
    return model.activeTurnId === turnId;
  }
  return notificationTargetsThread(n, model);
}

// handleNotification routes one live notification to whichever tracked
// model(s) it targets, folding it through the reducer. A notification for a
// ref this store isn't tracking (or that targets no tracked model) finds no
// match below and the threads map is left as the exact same reference — no
// setState call at all. Every ref whose model actually changed (a real
// applied frame, not a same-reference reducer no-op) also gets `now`
// appended to its frameTimes ring, reusing this same `now` rather than
// reading a second Date.now() for it.
function handleNotification(n: AnyNotification): void {
  const now = Date.now();
  const { threads, frameTimes } = threadsStore.getState();
  let nextThreads: Map<string, ThreadModel> | null = null;
  let nextFrameTimes: Map<string, number[]> | null = null;
  for (const [ref, model] of threads) {
    if (!targetsNotification(n, model)) continue;
    const updated = applyNotification(model, n, now);
    if (updated === model) continue;
    nextThreads ??= new Map(threads);
    nextThreads.set(ref, updated);
    nextFrameTimes ??= new Map(frameTimes);
    nextFrameTimes.set(ref, appendFrameTime(frameTimes.get(ref) ?? [], now));
  }
  if (nextThreads && nextFrameTimes) threadsStore.setState({ threads: nextThreads, frameTimes: nextFrameTimes });
}

// handleReady re-subscribes every currently-tracked ref, additively, and
// replaces its model wholesale from the fresh snapshot (hydrateThread) —
// snapshot recovery, since notifications published while the socket was down
// were missed. Fires on every client.onReady transition into "ready",
// including the very first — a no-op in practice, since nothing is tracked
// yet that early in the app's lifecycle — and every reconnect after it. Also
// called directly (not via onReady) from rewireClient below, for the case
// where a client swap lands on a client that is ALREADY ready — onReady only
// fires on a FUTURE transition, never retroactively for a client that
// reached "ready" before this store ever subscribed to it (see
// rewireClient's own comment).
async function handleReady(client: AppwireClientLike): Promise<void> {
  const refs = Array.from(threadsStore.getState().threads.keys());
  await Promise.all(
    refs.map(async (ref) => {
      try {
        const model = await hydrateAndSubscribe(client, ref, Date.now());
        // A concurrent releaseThread() may have dropped this ref while the
        // re-subscribe was in flight; don't resurrect it.
        if (!threadsStore.getState().threads.has(ref)) return;
        threadsStore.setState((s) => {
          const next = new Map(s.threads);
          next.set(ref, model);
          return { threads: next };
        });
      } catch {
        // Best-effort: a failed re-subscribe leaves the stale model in place
        // rather than losing it; the next onReady (another reconnect) or a
        // fresh ensureThread() from a remounting pane will retry.
      }
    }),
  );
}

// rewireClient is the single place this store's onNotification/onReady
// handlers move to a new client. It is idempotent (a no-op once `client` is
// already the wired one) and is triggered two ways:
//   - reactively, by the connectionStore.subscribe() call below, the moment
//     connectionStore's own client reference changes — this is what fixes
//     the bug this whole describe block in threads.test.ts is named after:
//     a manual retry (shell/ConnectionBanner.tsx) that swaps in a fresh
//     AppwireClient used to leave this store's handlers attached to the now-
//     dead client until some pane happened to call an action, silently
//     starving every already-open pane of live deltas in the meantime.
//   - defensively, from requireClient() below, for the (never exercised in
//     practice, since this module's own top-level subscribe() call below
//     runs at import time, before any action can possibly run) case where
//     an action reaches requireClient() before that subscription has taken
//     effect.
function rewireClient(client: AppwireClientLike): void {
  if (client === wiredClient) return;
  unwireNotification?.();
  unwireReady?.();
  wiredClient = client;
  unwireNotification = client.onNotification(handleNotification);
  unwireReady = client.onReady(() => {
    void handleReady(client);
  });
  // onReady only fires on a FUTURE transition into "ready" (AppwireClient/
  // FakeClient both dispatch it from within setState/emitStateChange) — it
  // does NOT fire retroactively for a client that is already ready by the
  // time we subscribe. A manual retry's fresh client is typically already
  // ready at this point (ConnectionBanner awaits the new client's own
  // connect() before ever handing it to connectionStore.connect()), so
  // without this, swapping to an already-ready client would never
  // re-subscribe/re-hydrate this store's tracked refs at all.
  if (client.state === "ready") void handleReady(client);
}

// The single reactive trigger for rewireClient: every connectionStore
// change is checked for a (possibly new) client, and rewireClient itself
// no-ops unless the reference actually changed — so this fires harmlessly
// on state-only changes (e.g. a client's own onStateChange mirroring) too.
// Registered once, at module load, same lifetime as this module's other
// singleton bookkeeping (refCounts, wiredClient, ...).
connectionStore.subscribe((state) => {
  if (state.client) rewireClient(state.client);
});

// requireClient reads the client connection.ts wired via
// useConnectionStore.getState().connect(client) — threads.ts has no
// connect() of its own in the locked interface, so it rides connection.ts's
// single wiring point.
function requireClient(): AppwireClientLike {
  const client = connectionStore.getState().client;
  if (!client) {
    throw new Error("threads store: no client connected; call useConnectionStore.getState().connect(client) first");
  }
  rewireClient(client);
  return client;
}

export const threadsStore = createStore<ThreadsStoreState>(() => ({
  threads: new Map(),
  frameTimes: new Map(),

  async ensureThread(ref) {
    const client = requireClient();
    refCounts.set(ref, (refCounts.get(ref) ?? 0) + 1);
    if (threadsStore.getState().threads.has(ref)) return; // already hydrated: no re-read

    let inflight = inflightHydrates.get(ref);
    if (!inflight) {
      inflight = hydrateAndSubscribe(client, ref, Date.now());
      inflightHydrates.set(ref, inflight);
      // .finally() re-throws inflight's own rejection on ITS OWN returned
      // promise — a separate object from `inflight` — so without a catch
      // here a failed hydrate becomes an unhandled rejection on top of the
      // one every caller already observes via `await inflight` below.
      void inflight
        .finally(() => {
          if (inflightHydrates.get(ref) === inflight) inflightHydrates.delete(ref);
        })
        .catch(() => {});
    }
    let model: ThreadModel;
    try {
      model = await inflight;
    } catch (err) {
      // This call's own claim (the increment above) never landed: undo it
      // via the same releaseThread() a caller would otherwise use, so a
      // caller that retries ensureThread() after a failure and then
      // releases exactly once (the normal mount/retry/unmount lifecycle)
      // doesn't strand a phantom refcount that keeps a never-hydrated ref
      // "tracked" forever (scanned by handleNotification on every
      // notification, with no pane left to ever release it). Reusing
      // releaseThread() rather than hand-rolling the decrement also means
      // its own <=0 guard already makes this safe if a concurrent
      // releaseThread() consumed this exact claim first.
      threadsStore.getState().releaseThread(ref);
      throw err;
    }
    // A concurrent releaseThread() may have dropped this ref to zero panes
    // while the hydrate was in flight; don't resurrect it.
    if ((refCounts.get(ref) ?? 0) <= 0) return;
    threadsStore.setState((s) => {
      const next = new Map(s.threads);
      next.set(ref, model);
      return { threads: next };
    });
  },

  releaseThread(ref) {
    const count = refCounts.get(ref) ?? 0;
    if (count <= 0) return; // never tracked, or already released
    if (count > 1) {
      refCounts.set(ref, count - 1);
      return;
    }
    refCounts.delete(ref);
    // No wire call exists for "stop pushing me updates for this ref" (no
    // thread/read subscribe:false, no unsubscribe method) — the daemon keeps
    // sending; removing it from `threads` just stops handleNotification's
    // per-model scan from matching it, so nothing routes here anymore.
    // frameTimes is dropped in lockstep — an untracked ref has no business
    // holding onto a liveness trace a future ensureThread() of the same ref
    // should start fresh, the same way it re-reads a fresh model.
    threadsStore.setState((s) => {
      if (!s.threads.has(ref) && !s.frameTimes.has(ref)) return s;
      const nextThreads = new Map(s.threads);
      nextThreads.delete(ref);
      const nextFrameTimes = new Map(s.frameTimes);
      nextFrameTimes.delete(ref);
      return { threads: nextThreads, frameTimes: nextFrameTimes };
    });
  },

  async loadOlderTurns(ref) {
    const client = requireClient();
    const model = threadsStore.getState().threads.get(ref);
    if (!model?.olderCursor) return; // untracked, or no more history to page in
    const resp: ThreadTurnsListResponse = await client.request("thread/turns/list", olderTurnsParams(ref, model.olderCursor));
    // A concurrent releaseThread() may have dropped this ref while the page
    // was in flight; don't resurrect it. Re-read (rather than reusing
    // `model`) so a live notification that arrived during the await isn't
    // clobbered by prepending onto a stale snapshot.
    const current = threadsStore.getState().threads.get(ref);
    if (!current) return;
    threadsStore.setState((s) => {
      const next = new Map(s.threads);
      next.set(ref, prependOlderTurns(current, resp));
      return { threads: next };
    });
  },

  async send(ref, text, images) {
    const client = requireClient();
    try {
      await client.request("turn/start", { ref, input: buildInput(text, images) });
    } catch (err) {
      throw mapConflict(err);
    }
  },

  async steer(ref, text) {
    const client = requireClient();
    const expectedTurnId = threadsStore.getState().threads.get(ref)?.activeTurnId;
    try {
      await client.request("turn/steer", { ref, expectedTurnId, input: buildInput(text) });
    } catch (err) {
      throw mapConflict(err);
    }
  },

  async queue(ref, text) {
    const client = requireClient();
    try {
      await client.request("turn/queue", { ref, input: buildInput(text) });
    } catch (err) {
      throw mapConflict(err);
    }
  },

  async interrupt(ref) {
    const client = requireClient();
    const expectedTurnId = threadsStore.getState().threads.get(ref)?.activeTurnId;
    try {
      await client.request("turn/interrupt", { ref, expectedTurnId });
    } catch (err) {
      throw mapConflict(err);
    }
  },
}));

export function useThreadsStore(): ThreadsStoreState;
export function useThreadsStore<T>(selector: (state: ThreadsStoreState) => T): T;
export function useThreadsStore<T>(selector?: (state: ThreadsStoreState) => T): T | ThreadsStoreState {
  return selector ? useStore(threadsStore, selector) : useStore(threadsStore);
}

// resetThreadsStoreForTests resets every module-private/store field to its
// initial state. threads.ts is a singleton store (one Map, one refcount
// table, one wired-client marker) shared by the whole app, so
// threads.test.ts must reset it between tests to keep them isolated — no
// production code should ever call this. Calls the previous wiring's own
// unwire functions (rather than just dropping the references) so the next
// test's first rewireClient() call never fires a stale unwire closure from
// an unrelated, already-discarded FakeClient.
export function resetThreadsStoreForTests(): void {
  refCounts.clear();
  inflightHydrates.clear();
  unwireNotification?.();
  unwireReady?.();
  unwireNotification = null;
  unwireReady = null;
  wiredClient = null;
  threadsStore.setState({ threads: new Map(), frameTimes: new Map() });
}
