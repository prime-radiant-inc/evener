// threads.ts tracks the ThreadModel for every ref currently open in a pane,
// refcounted across panes sharing the same ref, and routes live wire
// notifications into the reducer for whichever tracked model(s) they target.
// It rides the single AppwireClientLike connection.ts wires via
// useConnectionStore.getState().connect(client) — this store has no
// connect() of its own — and lazily attaches its own onNotification/onReady
// handlers to that client the first time any of its methods run.
import { createStore } from "zustand/vanilla";
import { useStore } from "zustand";
import { connectionStore } from "./connection";
import type { AppwireClientLike } from "../protocol/testing/fakeClient";
import { applyNotification, hydrateThread, notificationTargetsThread } from "../protocol/reducer";
import type { ThreadModel } from "../protocol/model";
import type { AnyNotification, InputItem, ThreadReadResponse } from "../protocol/types.gen";
import { WireError } from "../protocol/errors";

export class ConflictError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "ConflictError";
  }
}

export interface ThreadsStoreState {
  threads: Map<string, ThreadModel>;
  ensureThread(ref: string): Promise<void>;
  releaseThread(ref: string): void;
  send(ref: string, text: string, images?: string[]): Promise<void>;
  steer(ref: string, text: string): Promise<void>;
  queue(ref: string, text: string): Promise<void>;
  interrupt(ref: string): Promise<void>;
}

// Module-private bookkeeping the locked interface doesn't expose: pane
// refcounts per ref, the hydrate promise currently in flight for a ref (so
// two panes racing to ensureThread() the same ref share one thread/read
// instead of sending two), and which client this store has already wired
// its notification/ready handlers onto.
const refCounts = new Map<string, number>();
const inflightHydrates = new Map<string, Promise<ThreadModel>>();
let wiredClient: AppwireClientLike | null = null;

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
// setState call at all.
function handleNotification(n: AnyNotification): void {
  const now = Date.now();
  const { threads } = threadsStore.getState();
  let next: Map<string, ThreadModel> | null = null;
  for (const [ref, model] of threads) {
    if (!targetsNotification(n, model)) continue;
    const updated = applyNotification(model, n, now);
    if (updated === model) continue;
    if (!next) next = new Map(threads);
    next.set(ref, updated);
  }
  if (next) threadsStore.setState({ threads: next });
}

// handleReady re-subscribes every currently-tracked ref, additively, and
// replaces its model wholesale from the fresh snapshot (hydrateThread) —
// snapshot recovery, since notifications published while the socket was down
// were missed. Fires on every client.onReady transition into "ready",
// including the very first — a no-op in practice, since nothing is tracked
// yet that early in the app's lifecycle — and every reconnect after it.
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

// requireClient reads the client connection.ts wired via
// useConnectionStore.getState().connect(client) — threads.ts has no
// connect() of its own in the locked interface, so it rides connection.ts's
// single wiring point — and lazily (idempotently) attaches this store's own
// onNotification/onReady handlers to it the first time it's seen.
function requireClient(): AppwireClientLike {
  const client = connectionStore.getState().client;
  if (!client) {
    throw new Error("threads store: no client connected; call useConnectionStore.getState().connect(client) first");
  }
  if (client !== wiredClient) {
    wiredClient = client;
    client.onNotification(handleNotification);
    client.onReady(() => {
      void handleReady(client);
    });
  }
  return client;
}

export const threadsStore = createStore<ThreadsStoreState>(() => ({
  threads: new Map(),

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
    threadsStore.setState((s) => {
      if (!s.threads.has(ref)) return s;
      const next = new Map(s.threads);
      next.delete(ref);
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
// production code should ever call this.
export function resetThreadsStoreForTests(): void {
  refCounts.clear();
  inflightHydrates.clear();
  wiredClient = null;
  threadsStore.setState({ threads: new Map() });
}
