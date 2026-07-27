// threads.ts tracks the ThreadModel for every ref currently open in a pane,
// refcounted across panes sharing the same ref, and routes live wire
// notifications into the reducer for whichever tracked model(s) they target.
// It rides the single AppwireClientLike connection.ts wires via
// useConnectionStore.getState().connect(client) — this store has no
// connect() of its own — and reactively re-attaches its onNotification/onReady
// handlers to whatever client connectionStore currently holds, via a
// connectionStore.subscribe() wired at module load (see rewireClient).

import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import { applySerfJobFinished, applySerfJobStarted } from "../panes/session/transcript/tools/subagentModuleStore";
import { WireError } from "../protocol/errors";
import type { ThreadModel } from "../protocol/model";
import {
  applyNotification,
  hydrateThread,
  notificationTargetsThread,
  prependOlderTurns,
  resolvePendingEscalation,
} from "../protocol/reducer";
import type { AppwireClientLike } from "../protocol/testing/fakeClient";
import type {
  AnyNotification,
  GoalSetResponse,
  InputItem,
  ModelListResponse,
  ThreadClearResponse,
  ThreadForkResponse,
  ThreadReadResponse,
  ThreadTurnsListResponse,
  TurnCancelQueuedResponse,
} from "../protocol/types.gen";
import { connectionStore } from "./connection";

// InputAttachment is this store's real-attachment shape: base64 bytes, not a
// hosted URL. The wire's InputItem (appwire/types.go:561-570) supports EITHER
// a Data+MediaType+Name triple OR a URL string (both fields are independently
// optional on the same struct), but nothing in this codebase ever constructs
// a url-based InputItem (verified: no caller of send/steer/queue/drainAsSteer
// exists yet outside this store's own tests) - a pasted/dropped/picked image
// is always bytes, never a pre-hosted URL, so that half of InputItem's shape
// is left unexercised here rather than invented into this store's public
// surface. A future caller that genuinely has a hosted URL can still reach
// it at the wire layer; it just isn't this parameter.
export interface InputAttachment {
  mediaType: string;
  data: string; // base64-encoded bytes (wire InputItem.data)
  name?: string;
}

// ForkFromTurnOptions mirrors ThreadForkParams verbatim (appwire/types.go:
// 692-711) minus ref (a separate positional argument, like every other
// action here). Fork and aside are the SAME wire method with mutually
// exclusive param sets (aside excludes sourceTurnId/editedInput/deferInput/
// label per that struct's own doc comment) - the Go type itself is one flat
// struct with no type-level split enforcing this, so this TS type mirrors
// that honestly rather than inventing a discriminated union the wire
// doesn't have; enforcing the exclusion is the caller's (T5's) job.
export interface ForkFromTurnOptions {
  sourceTurnId?: string;
  editedInput?: string;
  label?: string;
  modelProvider?: string;
  model?: string;
  deferInput?: boolean;
  aside?: boolean;
}

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
  // watchedThreads/watchedFrameTimes: the transcript/tools stream's own
  // sanctioned extension (watched-child subagent-module rows - a leaner,
  // additive subscription alongside a real ensureThread'd pane, never
  // replacing it). Deliberately SEPARATE maps from threads/frameTimes,
  // not a shared one keyed the same way: a ref can legitimately be both
  // ensureThread'd (a real pane open on it) and watchThread'd (a parent
  // session's subagent row watching it) at once, and releasing one must
  // never disturb the other's tracked data or lifecycle.
  watchedThreads: Map<string, ThreadModel>;
  watchedFrameTimes: Map<string, number[]>;
  // Per-ref scroll offset (transcript/flow's own coordinate - see that
  // module), persisted independently of the ensureThread/releaseThread
  // refcount lifecycle: unlike `threads`/`frameTimes` (cheaply re-derived
  // from a fresh thread/read on the next mount), a scroll position has no
  // server-side source of truth to recover it from, so it must survive a
  // pane unmount (dockview unmounts an inactive pane's whole tree - see
  // Session.tsx's own comment) even once its refcount hits zero. Grows for
  // the lifetime of the store (one entry per ref ever visited this session)
  // - no eviction; unbounded growth across a single browser session visiting
  // many distinct refs is not a problem this wave needs to solve.
  scrollPositions: Map<string, number>;
  ensureThread(ref: string): Promise<void>;
  releaseThread(ref: string): void;
  // Additive, leaner subscription to a child thread for a subagent-module
  // row's live view (see this file's own doc comment). opts.includeTurns
  // upgrades the read to carry the child's turn history for the expanded
  // card's Activity feed (yd16 §4.2); it is MONOTONIC per ref — once any
  // watcher asks for turns they stay until the last watcher releases. The
  // default (no opts) is the lean includeTurns:false read.
  watchThread(ref: string, opts?: { includeTurns?: boolean }): Promise<void>;
  releaseWatchedThread(ref: string): void;
  loadOlderTurns(ref: string): Promise<void>;
  send(ref: string, text: string, attachments?: InputAttachment[]): Promise<void>;
  steer(ref: string, text: string, attachments?: InputAttachment[]): Promise<void>;
  queue(ref: string, text: string, attachments?: InputAttachment[]): Promise<void>;
  interrupt(ref: string): Promise<void>;
  // drainAsSteer atomically appends the composer's current text/attachments
  // (if any) to the input queue, then drains the whole queue into the
  // active turn as one steering message (turn/drainAsSteer, kata 0bq1 Path
  // B) - see this file's own describe block for why `text` is a required
  // param here, not the bare `drainAsSteer(ref)` the plan's terse pseudocode
  // showed.
  drainAsSteer(ref: string, text: string, attachments?: InputAttachment[]): Promise<void>;
  // Removes one queued message by index and injects it as steering into the
  // in-flight turn (issue #22). expectedEntryId, when non-empty, must match
  // the id the daemon minted for that queue position (QueueState.IDs) - a
  // mismatch (the queue shifted under the caller's snapshot) is a Conflict,
  // never a wrong-message promote.
  promoteQueuedAsSteer(ref: string, index: number, expectedEntryId: string): Promise<void>;
  // Removes the queued follow-up at index so it is never consumed (issue
  // #23; also the removal half of the composer's edit-and-recompose flow).
  // Same expectedEntryId Conflict semantics as promoteQueuedAsSteer. Returns
  // the wire's own echo of what was removed (RemovedText/RemovedImages) so
  // the caller can restore the full untruncated text and warn about any
  // image attachments that were on the entry and are not restored.
  cancelQueued(ref: string, index: number, expectedEntryId: string): Promise<TurnCancelQueuedResponse>;
  setModel(ref: string, modelProvider: string, model: string): Promise<void>;
  setReasoningEffort(ref: string, level: string): Promise<void>;
  // Sets or clears the session's /goal objective (an empty objective
  // clears it). Returns whether the goal loop started immediately (false
  // when cleared, or when a turn is already running and the goal picks up
  // after it) - the goal is set either way. No live push exists for goal
  // state (appwire/protocol.go's Notifications catalog has no goal-changed
  // entry): reflecting this locally is left to the caller (T5 owns that
  // "snapshot + optimistic local update" per the wave plan), not this
  // store, which stays a plain fire-and-report wire call like setModel/
  // setReasoningEffort/rename/compact/shutdown above.
  setGoal(ref: string, objective: string): Promise<GoalSetResponse>;
  rename(ref: string, name: string): Promise<void>;
  compact(ref: string): Promise<void>;
  // Clears the thread's conversation. Unlike the actions above, thread/clear
  // has no corresponding live notification, so its response's fresh Thread
  // snapshot is the only signal the transcript is now empty; this action
  // applies that snapshot to whichever of threads/watchedThreads track
  // `ref` (mirroring resolveEscalation's own dual-map update below) so the
  // pane doesn't keep showing stale turns until some unrelated future
  // notification or reconnect.
  clearThread(ref: string): Promise<void>;
  shutdown(ref: string): Promise<void>;
  // Forks a thread from a source turn, or - with opts.aside - forks the
  // session at its current tip into a side thread (same wire method,
  // mutually exclusive param sets - see ForkFromTurnOptions). The response
  // describes a DIFFERENT ref (the new child thread), so this never touches
  // the parent's own tracked model; the caller (T5) opens the child as its
  // own pane via ensureThread on the returned ref.
  forkFromTurn(ref: string, opts: ForkFromTurnOptions): Promise<ThreadForkResponse>;
  // Lists available models (model/list) with launch diagnostics, feeding
  // the chrome stream's model-switch picker. Session-lifetime cached
  // (models don't change mid-session, and no live push exists for them
  // either - same "no capabilities-changed entry" reasoning as
  // ThreadModel.capabilities); pass refresh:true to bypass the cache and
  // force a fresh request. A failed request never poisons the cache with a
  // rejected promise - the next call (with or without refresh) retries.
  listModels(refresh?: boolean): Promise<ModelListResponse>;
  // Lists the session's tasks (serf/tasks/list). TaskListResponse.Data is
  // `any` on the wire catalog (appwire/types.go:896-898) - this returns
  // that raw field verbatim, never wrapped, so the store stays shape-
  // agnostic; the caller owns interpreting it (the chrome stream's own
  // parseTaskListData). A Codex-source thread rejects this call
  // (appwire.Unavailable, "actionUnavailable") - that typed error
  // propagates unchanged, same as every other read-only action here; the
  // caller renders the empty/unsupported state for it.
  listTasks(ref: string): Promise<unknown>;
  // Answers one serf/sandbox/escalation/requested via serf/sandbox/
  // escalation/resolve. On success, removes the escalation from whichever
  // of threads/watchedThreads currently track `ref` (both, if both do -
  // see ThreadsStoreState's own doc comment on why they're independent
  // maps). On rejection, propagates unchanged - the caller (the
  // escalation rail) owns surfacing the failure.
  resolveEscalation(ref: string, escalationId: string, approve: boolean): Promise<void>;
  // The one synchronous, no-network action on this store: flow/'s scroll
  // hook calls it directly off a real scroll event, not through
  // requireClient() - there is nothing to request, just client-side UI
  // state to remember for the next mount.
  setScrollPosition(ref: string, position: number): void;
}

// Module-private bookkeeping the locked interface doesn't expose: pane
// refcounts per ref, the hydrate promise currently in flight for a ref (so
// two panes racing to ensureThread() the same ref share one thread/read
// instead of sending two), and which client this store has already wired
// its notification/ready handlers onto (plus that wiring's own unsubscribe
// functions - see rewireClient below).
const refCounts = new Map<string, number>();
const inflightHydrates = new Map<string, Promise<ThreadModel | null>>();
// The published model is intentionally stale while a thread/read is pending.
// Keep only the routing facts that can evolve from accepted notifications, so
// a later ref-less or threadId-only frame is judged against the pending stream
// rather than that stale snapshot.
type PendingHydrationRouting = {
  ref: string;
  threadId?: string;
  activeTurnId?: string;
};
type PendingThreadHydration = {
  client: AppwireClientLike;
  notifications: AnyNotification[];
  routing: PendingHydrationRouting;
};
// A thread/read subscribes before it returns its snapshot. Notifications can
// therefore arrive in the gap between the source subscription and snapshot
// response. Keep the newest hydration's notifications out of the old model,
// then fold them onto the returned snapshot before publishing it.
const pendingThreadHydrations = new Map<string, PendingThreadHydration>();
const pendingWatchedHydrations = new Map<string, PendingThreadHydration>();
let wiredClient: AppwireClientLike | null = null;
let unwireNotification: (() => void) | null = null;
let unwireReady: (() => void) | null = null;

// listModels' own session-lifetime cache (models are not per-ref, so this
// is a single slot, not a Map): modelsCache holds the last successful
// response; inflightModelsList de-dupes concurrent non-refresh callers the
// same way inflightHydrates does for ensureThread. A rejection is never
// written to modelsCache (so a prior good cache survives a later failed
// refresh, and a first-ever failure leaves nothing stale to keep serving),
// and inflightModelsList is always cleared in a `finally` so a failed call
// never poisons the next one with a repeated rejection.
let modelsCache: ModelListResponse | null = null;
let inflightModelsList: Promise<ModelListResponse> | null = null;

// watchThread's own refcount/inflight bookkeeping - independent of
// refCounts/inflightHydrates above, so a watch and a real pane on the
// same ref never share (or fight over) one counter.
const watchRefCounts = new Map<string, number>();
const inflightWatchHydrates = new Map<string, Promise<ThreadModel | null>>();
const inflightWatchIncludeTurns = new Map<string, boolean>();
// A generation changes whenever the last watcher releases. Late responses
// from that retired lifetime must not populate a new one.
const watchGenerations = new Map<string, number>();
// Per-ref "does any watcher want turns" flag (yd16 §4.2). Monotonic across a
// ref's watch lifetime: set true the first time any watchThread call asks for
// turns, never flipped back to false while watched, cleared only when the last
// watcher releases. Drives both the includeTurns read param and the
// lean-then-rich upgrade re-read in watchThread.
const watchIncludeTurns = new Map<string, boolean>();
// Whether the model currently in watchedThreads came from a rich read. This
// prevents a slower lean read from replacing a rich model that won the race,
// while still allowing a lean read to populate the store if its rich sibling
// was released before either response arrived.
const watchHydratedIncludeTurns = new Map<string, boolean>();

// Every tracked ref gets exactly these params on both the first subscribe
// (ensureThread) and every re-subscribe (onReady after reconnect):
// replaceSubscription is always false — additive, layering onto whatever the
// daemon already tracks for this client rather than resetting it.
function readParams(ref: string) {
  return {
    ref,
    includeTurns: true,
    itemsView: "full",
    subscribe: true,
    replaceSubscription: false,
    turnLimit: 40,
  } as const;
}

async function hydrateAndSubscribe(client: AppwireClientLike, ref: string, now: number): Promise<ThreadModel> {
  const resp: ThreadReadResponse = await client.request("thread/read", readParams(ref));
  return hydrateThread(resp, ref, now);
}

// watchReadParams mirrors readParams but defaults includeTurns:false - a
// watched child's row dot only needs live status/liveness (Cadence reads
// watchedFrameTimes, not turn content), not its full turn/item history,
// which would be wasted fetch+storage for a subagent row most sessions
// never expand. The expanded card (yd16 §4.2) passes includeTurns:true to
// carry the child's turns for its Activity feed.
function watchReadParams(ref: string, includeTurns = false) {
  return {
    ref,
    includeTurns,
    itemsView: "full",
    subscribe: true,
    replaceSubscription: false,
    turnLimit: 40,
  } as const;
}

async function hydrateAndSubscribeWatch(
  client: AppwireClientLike,
  ref: string,
  now: number,
  includeTurns = false,
): Promise<ThreadModel> {
  const resp: ThreadReadResponse = await client.request("thread/read", watchReadParams(ref, includeTurns));
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

// buildInput assembles the wire turn/start|steer|queue|drainAsSteer input
// array: an optional leading text item (queueText allows empty/whitespace-
// only text when attachments are present - parity finding §B, "image-only
// queue entries are valid" - so this only omits the text item, never
// rejects the call), then one image item per attachment.
function buildInput(text: string, attachments?: InputAttachment[]): InputItem[] {
  const input: InputItem[] = [];
  if (text.trim()) input.push({ type: "text", text });
  for (const att of attachments ?? []) {
    input.push({ type: "image", mediaType: att.mediaType, data: att.data, name: att.name });
  }
  return input;
}

// mapConflict recognizes the WireError shape the daemon uses for a lost turn
// CAS (turn/start, turn/steer, turn/queue, turn/interrupt) or a stale/raced
// escalation resolve: code -32013 with
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

function notificationRef(n: AnyNotification): string | undefined {
  const params = n.params as { ref?: unknown };
  return typeof params.ref === "string" ? params.ref : undefined;
}

function notificationThreadId(n: AnyNotification): string | undefined {
  const params = n.params as { threadId?: unknown };
  return typeof params.threadId === "string" ? params.threadId : undefined;
}

function notificationTurnId(n: AnyNotification): string | undefined {
  if (n.method === "turn/completed") return n.params.turnId || n.params.turn.id;
  if (n.method === "turn/started") return n.params.turn.id;
  if (n.method === "item/started" || n.method === "item/completed") {
    return n.params.turnId || n.params.item.turnId;
  }
  return undefined;
}

function targetsPendingHydration(n: AnyNotification, pending: PendingThreadHydration): boolean {
  const routing = pending.routing;
  const ref = notificationRef(n);
  const threadId = notificationThreadId(n);
  if (ref !== undefined) {
    if (ref !== routing.ref) return false;
    // A ref-targeted frame is authoritative for the requested subscription,
    // but once that subscription has also taught us its thread id, a
    // contradictory id is a different thread and must not enter this buffer.
    if (threadId !== undefined && routing.threadId !== undefined && threadId !== routing.threadId) return false;
    if (n.method === "turn/completed") return routing.activeTurnId === notificationTurnId(n);
    return true;
  }
  if (n.method === "turn/completed") return routing.activeTurnId === notificationTurnId(n);
  return threadId !== undefined && threadId === routing.threadId;
}

function advancePendingHydrationRouting(n: AnyNotification, pending: PendingThreadHydration): void {
  const threadId = notificationThreadId(n);
  if (pending.routing.threadId === undefined && threadId !== undefined) pending.routing.threadId = threadId;

  if (n.method === "turn/started") {
    pending.routing.activeTurnId = n.params.turn.id;
  } else if (n.method === "turn/completed" && pending.routing.activeTurnId === notificationTurnId(n)) {
    pending.routing.activeTurnId = undefined;
  } else if (
    (n.method === "item/started" || n.method === "item/completed") &&
    pending.routing.activeTurnId === undefined
  ) {
    // Initial hydration can begin with an item frame whose turn/started frame
    // was already durable in the snapshot. Learn that active turn from the
    // item so the following bare turn/completed frame remains in order.
    pending.routing.activeTurnId = notificationTurnId(n);
  }
}

function pendingHydrationRouting(ref: string, model: ThreadModel | undefined): PendingHydrationRouting {
  return { ref, threadId: model?.threadId, activeTurnId: model?.activeTurnId };
}

function beginThreadHydration(
  ref: string,
  client: AppwireClientLike,
  model: ThreadModel | undefined,
): PendingThreadHydration {
  const pending = {
    client,
    notifications: [],
    routing: pendingHydrationRouting(ref, model),
  };
  pendingThreadHydrations.set(ref, pending);
  return pending;
}

function beginWatchedHydration(
  ref: string,
  client: AppwireClientLike,
  model: ThreadModel | undefined,
): PendingThreadHydration {
  const pending = {
    client,
    notifications: [],
    routing: pendingHydrationRouting(ref, model),
  };
  pendingWatchedHydrations.set(ref, pending);
  return pending;
}

function replayHydrationNotifications(
  model: ThreadModel,
  notifications: AnyNotification[],
): { model: ThreadModel; appliedAt: number[] } {
  let hydrated = model;
  const appliedAt: number[] = [];
  for (const notification of notifications) {
    const now = Date.now();
    const updated = applyNotification(hydrated, notification, now);
    if (updated === hydrated) continue;
    hydrated = updated;
    appliedAt.push(now);
  }
  return { model: hydrated, appliedAt };
}

function publishThreadHydration(ref: string, pending: PendingThreadHydration, model: ThreadModel): ThreadModel | null {
  if (pendingThreadHydrations.get(ref) !== pending) return null;
  if (wiredClient !== pending.client) return null;
  if ((refCounts.get(ref) ?? 0) <= 0) {
    pendingThreadHydrations.delete(ref);
    return null;
  }

  const { model: hydrated, appliedAt } = replayHydrationNotifications(model, pending.notifications);

  pendingThreadHydrations.delete(ref);
  threadsStore.setState((s) => {
    if ((refCounts.get(ref) ?? 0) <= 0) return s;
    const nextThreads = new Map(s.threads);
    nextThreads.set(ref, hydrated);
    if (appliedAt.length === 0) return { threads: nextThreads };
    const nextFrameTimes = new Map(s.frameTimes);
    let times = nextFrameTimes.get(ref) ?? [];
    for (const now of appliedAt) times = appendFrameTime(times, now);
    nextFrameTimes.set(ref, times);
    return { threads: nextThreads, frameTimes: nextFrameTimes };
  });
  return hydrated;
}

function publishWatchedHydration(
  ref: string,
  pending: PendingThreadHydration,
  model: ThreadModel,
  includeTurns: boolean,
  generation: number,
): ThreadModel | null {
  if (pendingWatchedHydrations.get(ref) !== pending) return null;
  if (wiredClient !== pending.client) return null;
  if ((watchRefCounts.get(ref) ?? 0) <= 0 || (watchGenerations.get(ref) ?? 0) !== generation) {
    pendingWatchedHydrations.delete(ref);
    return null;
  }

  const replayed = replayHydrationNotifications(model, pending.notifications);
  pendingWatchedHydrations.delete(ref);
  storeWatchedModel(ref, replayed.model, includeTurns, generation, replayed.appliedAt);
  return replayed.model;
}

// handleNotification routes one live notification to whichever tracked
// model(s) it targets, folding it through the reducer. A notification for a
// ref this store isn't tracking (or that targets no tracked model) finds no
// match below and the threads map is left as the exact same reference — no
// setState call at all. Every ref whose model actually changed (a real
// applied frame, not a same-reference reducer no-op) also gets `now`
// appended to its frameTimes ring, reusing this same `now` rather than
// reading a second Date.now() for it.
// applyToMap folds `n` through every model in `map` that targets it,
// returning the replaced map (or null if nothing in this particular map
// changed) plus exactly the refs that actually changed - shared by both
// the threads/frameTimes pass and the watchedThreads/watchedFrameTimes
// pass below, since the fold-and-detect-a-real-change logic is identical
// for either map.
function applyToMap(
  map: Map<string, ThreadModel>,
  n: AnyNotification,
  now: number,
  skippedRefs?: ReadonlySet<string>,
): { next: Map<string, ThreadModel> | null; changedRefs: string[] } {
  let next: Map<string, ThreadModel> | null = null;
  const changedRefs: string[] = [];
  for (const [ref, model] of map) {
    if (skippedRefs?.has(ref)) continue;
    if (!targetsNotification(n, model)) continue;
    const updated = applyNotification(model, n, now);
    if (updated === model) continue;
    next ??= new Map(map);
    next.set(ref, updated);
    changedRefs.push(ref);
  }
  return { next, changedRefs };
}

// applySubagentJobSignal is the "Signal merging" step from
// docs/superpowers/specs/2026-06-25-subagent-run-rendering-design.md (dr7e):
// serf/job/started|finished carry (originTurnId, delegateId/jobId) linkage
// straight back to a subagentModuleStore row, independent of ThreadModel
// entirely (a SEPARATE store this one has no other reason to import - see
// applySerfJobStarted/applySerfJobFinished's own comments for why this
// supplements, not replaces, watchedChild.tsx's per-child watch). Runs
// unconditionally on every notification, same as applyToMap below; both
// functions already no-op silently when the job carries no originTurnId or
// matches no tracked row, so no pre-filtering is needed here.
//
// n.params.ref (kata 8525) is passed straight through as the owning
// session's ref: originTurnId alone is per-session, not globally unique, so
// applySerfJobStarted/Finished need it to scope the write correctly in this
// page-lifetime singleton store (same reasoning as every other call site -
// see subagentModuleStore.ts's own turnScopeKey comment).
function applySubagentJobSignal(n: AnyNotification): void {
  if (n.method === "serf/job/started") applySerfJobStarted(n.params.job, n.params.ref);
  else if (n.method === "serf/job/finished") applySerfJobFinished(n.params.job, n.params.ref);
}

function handleNotification(n: AnyNotification): void {
  applySubagentJobSignal(n);
  const now = Date.now();
  const { threads, frameTimes, watchedThreads, watchedFrameTimes } = threadsStore.getState();
  const pendingRefs = new Set<string>();
  for (const [ref, pending] of pendingThreadHydrations) {
    if (targetsPendingHydration(n, pending)) {
      pending.notifications.push(n);
      advancePendingHydrationRouting(n, pending);
      pendingRefs.add(ref);
    } else if (notificationRef(n) === ref) {
      // Do not let a contradictory ref-targeted frame mutate the stale model
      // through applyToMap. It belongs to this subscription's identity space,
      // but its thread identity is unsafe to replay here, so drop it.
      pendingRefs.add(ref);
    }
  }
  const pendingWatchedRefs = new Set<string>();
  for (const [ref, pending] of pendingWatchedHydrations) {
    if (targetsPendingHydration(n, pending)) {
      pending.notifications.push(n);
      advancePendingHydrationRouting(n, pending);
      pendingWatchedRefs.add(ref);
    } else if (notificationRef(n) === ref) {
      pendingWatchedRefs.add(ref);
    }
  }
  const { next: nextThreads, changedRefs: changedThreads } = applyToMap(threads, n, now, pendingRefs);
  const { next: nextWatchedThreads, changedRefs: changedWatched } = applyToMap(
    watchedThreads,
    n,
    now,
    pendingWatchedRefs,
  );
  if (!nextThreads && !nextWatchedThreads) return;

  const patch: Partial<ThreadsStoreState> = {};
  if (nextThreads) {
    patch.threads = nextThreads;
    const nextFrameTimes = new Map(frameTimes);
    for (const ref of changedThreads) nextFrameTimes.set(ref, appendFrameTime(frameTimes.get(ref) ?? [], now));
    patch.frameTimes = nextFrameTimes;
  }
  if (nextWatchedThreads) {
    patch.watchedThreads = nextWatchedThreads;
    const nextWatchedFrameTimes = new Map(watchedFrameTimes);
    for (const ref of changedWatched)
      nextWatchedFrameTimes.set(ref, appendFrameTime(watchedFrameTimes.get(ref) ?? [], now));
    patch.watchedFrameTimes = nextWatchedFrameTimes;
  }
  threadsStore.setState(patch);
}

function storeWatchedModel(
  ref: string,
  model: ThreadModel,
  includeTurns: boolean,
  generation: number,
  appliedAt: number[] = [],
): void {
  if ((watchRefCounts.get(ref) ?? 0) <= 0) return;
  if ((watchGenerations.get(ref) ?? 0) !== generation) return;

  // A late lean reconnect snapshot cannot downgrade a rich snapshot that
  // already won an upgrade race in this same watch lifetime.
  const hydratedRich = watchHydratedIncludeTurns.get(ref) ?? false;
  if (!includeTurns && hydratedRich) return;
  watchHydratedIncludeTurns.set(ref, hydratedRich || includeTurns);
  threadsStore.setState((s) => {
    const next = new Map(s.watchedThreads);
    next.set(ref, model);
    if (appliedAt.length === 0) return { watchedThreads: next };
    const nextFrameTimes = new Map(s.watchedFrameTimes);
    let times = nextFrameTimes.get(ref) ?? [];
    for (const now of appliedAt) times = appendFrameTime(times, now);
    nextFrameTimes.set(ref, times);
    return { watchedThreads: next, watchedFrameTimes: nextFrameTimes };
  });
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
  const watchRefs = Array.from(threadsStore.getState().watchedThreads.keys());
  await Promise.all([
    ...refs.map(async (ref) => {
      const pending = beginThreadHydration(ref, client, threadsStore.getState().threads.get(ref));
      try {
        const model = await hydrateAndSubscribe(client, ref, Date.now());
        // A newer hydration (for example, a fresh client replacing this one)
        // owns publication. Its pending notification buffer must not be
        // discarded by this older response.
        if (wiredClient === client && pendingThreadHydrations.get(ref) === pending) {
          publishThreadHydration(ref, pending, model);
        }
      } catch {
        // Best-effort: a failed re-subscribe leaves the stale model in place
        // rather than losing it; the next onReady (another reconnect) or a
        // fresh ensureThread() from a remounting pane will retry.
      } finally {
        if (pendingThreadHydrations.get(ref) === pending) pendingThreadHydrations.delete(ref);
      }
    }),
    ...watchRefs.map(async (ref) => {
      const generation = watchGenerations.get(ref) ?? 0;
      const pending = beginWatchedHydration(ref, client, threadsStore.getState().watchedThreads.get(ref));
      try {
        const includeTurns = watchIncludeTurns.get(ref) ?? false;
        const model = await hydrateAndSubscribeWatch(client, ref, Date.now(), includeTurns);
        if (wiredClient === client && pendingWatchedHydrations.get(ref) === pending) {
          publishWatchedHydration(ref, pending, model, includeTurns, generation);
        }
      } catch {
        // Best-effort, same rationale as the real-pane path above.
      } finally {
        if (pendingWatchedHydrations.get(ref) === pending) pendingWatchedHydrations.delete(ref);
      }
    }),
  ]);
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
  watchedThreads: new Map(),
  watchedFrameTimes: new Map(),
  scrollPositions: new Map(),

  async ensureThread(ref) {
    const client = requireClient();
    refCounts.set(ref, (refCounts.get(ref) ?? 0) + 1);
    if (threadsStore.getState().threads.has(ref)) return; // already hydrated: no re-read

    let inflight = inflightHydrates.get(ref);
    if (!inflight) {
      const pending = beginThreadHydration(ref, client, threadsStore.getState().threads.get(ref));
      inflight = hydrateAndSubscribe(client, ref, Date.now())
        .then((model) => publishThreadHydration(ref, pending, model))
        .finally(() => {
          if (pendingThreadHydrations.get(ref) === pending) pendingThreadHydrations.delete(ref);
        });
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
    let model: ThreadModel | null;
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
    // The hydration promise publishes its snapshot and buffered notifications
    // atomically. A newer hydration or a concurrent release may make this
    // response obsolete; in that case there is nothing for this caller to do.
    if (!model) return;
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

  // watchThread is the transcript/tools stream's own sanctioned addition:
  // an additive, leaner (includeTurns:false) subscription to a child
  // thread for a subagent-module row's live view, refcounted
  // independently of ensureThread's own counter (watchRefCounts, not
  // refCounts) and stored in watchedThreads/watchedFrameTimes, not
  // threads/frameTimes - see this file's own ThreadsStoreState doc
  // comment for why the two must stay fully independent. Otherwise a
  // structural mirror of ensureThread above.
  async watchThread(ref, opts) {
    const client = requireClient();
    const wantTurns = opts?.includeTurns ?? false;
    if ((watchRefCounts.get(ref) ?? 0) === 0) {
      watchGenerations.set(ref, (watchGenerations.get(ref) ?? 0) + 1);
    }
    const generation = watchGenerations.get(ref) ?? 0;
    watchRefCounts.set(ref, (watchRefCounts.get(ref) ?? 0) + 1);
    // Monotonic per-ref turns flag: once any watcher wants turns, keep them
    // for every watcher until the last release (yd16 §4.2).
    const hadTurns = watchIncludeTurns.get(ref) ?? false;
    const needTurns = hadTurns || wantTurns;
    watchIncludeTurns.set(ref, needTurns);
    const tracked = threadsStore.getState().watchedThreads.has(ref);
    // Upgrading: this ref is already tracked lean but this caller wants turns.
    // A fresh rich re-read is required because the .has(ref)/inflight-dedup
    // short-circuits below (which exist only to share ONE read across
    // concurrent first-mounts) would otherwise return the already-hydrated
    // lean model, which has no turns.
    const upgrading = tracked && wantTurns && !hadTurns;
    if (tracked && !upgrading) return; // already hydrated at the level we need

    let inflight = inflightWatchHydrates.get(ref);
    const inflightHasTurns = inflightWatchIncludeTurns.get(ref) ?? false;
    // A rich caller cannot share a lean request already in flight: the
    // response would be structurally missing the turns it requested. A
    // lean caller may share a rich request because the richer snapshot is
    // sufficient for both callers.
    if (!inflight || (needTurns && !inflightHasTurns)) {
      const pending = beginWatchedHydration(ref, client, threadsStore.getState().watchedThreads.get(ref));
      inflight = hydrateAndSubscribeWatch(client, ref, Date.now(), needTurns)
        .then((model) => publishWatchedHydration(ref, pending, model, needTurns, generation))
        .finally(() => {
          if (pendingWatchedHydrations.get(ref) === pending) pendingWatchedHydrations.delete(ref);
        });
      inflightWatchHydrates.set(ref, inflight);
      inflightWatchIncludeTurns.set(ref, needTurns);
      void inflight
        .finally(() => {
          if (inflightWatchHydrates.get(ref) === inflight) {
            inflightWatchHydrates.delete(ref);
            inflightWatchIncludeTurns.delete(ref);
          }
        })
        .catch(() => {});
    }
    const model = await inflight;
    if (!model) return;
  },

  releaseWatchedThread(ref) {
    const count = watchRefCounts.get(ref) ?? 0;
    if (count <= 0) return; // never tracked, or already released
    if (count > 1) {
      watchRefCounts.set(ref, count - 1);
      return;
    }
    watchRefCounts.delete(ref);
    watchGenerations.set(ref, (watchGenerations.get(ref) ?? 0) + 1);
    // A retired lifecycle must not lend its pending hydrate to a new watcher.
    // The old promise may still settle, but its generation check prevents it
    // from publishing into the new lifecycle.
    inflightWatchHydrates.delete(ref);
    inflightWatchIncludeTurns.delete(ref);
    pendingWatchedHydrations.delete(ref);
    // Drop the monotonic turns flag with the last watcher so a future watch of
    // the same ref starts lean again (yd16 §4.2).
    watchIncludeTurns.delete(ref);
    watchHydratedIncludeTurns.delete(ref);
    threadsStore.setState((s) => {
      if (!s.watchedThreads.has(ref) && !s.watchedFrameTimes.has(ref)) return s;
      const nextWatchedThreads = new Map(s.watchedThreads);
      nextWatchedThreads.delete(ref);
      const nextWatchedFrameTimes = new Map(s.watchedFrameTimes);
      nextWatchedFrameTimes.delete(ref);
      return { watchedThreads: nextWatchedThreads, watchedFrameTimes: nextWatchedFrameTimes };
    });
  },

  async loadOlderTurns(ref) {
    const client = requireClient();
    const model = threadsStore.getState().threads.get(ref);
    if (!model?.olderCursor) return; // untracked, or no more history to page in
    const resp: ThreadTurnsListResponse = await client.request(
      "thread/turns/list",
      olderTurnsParams(ref, model.olderCursor),
    );
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

  async send(ref, text, attachments) {
    const client = requireClient();
    try {
      await client.request("turn/start", { ref, input: buildInput(text, attachments) });
    } catch (err) {
      throw mapConflict(err);
    }
  },

  async steer(ref, text, attachments) {
    const client = requireClient();
    const expectedTurnId = threadsStore.getState().threads.get(ref)?.activeTurnId;
    try {
      await client.request("turn/steer", { ref, expectedTurnId, input: buildInput(text, attachments) });
    } catch (err) {
      throw mapConflict(err);
    }
  },

  async queue(ref, text, attachments) {
    const client = requireClient();
    try {
      await client.request("turn/queue", { ref, input: buildInput(text, attachments) });
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

  async drainAsSteer(ref, text, attachments) {
    const client = requireClient();
    try {
      await client.request("turn/drainAsSteer", { ref, input: buildInput(text, attachments) });
    } catch (err) {
      throw mapConflict(err);
    }
  },

  async promoteQueuedAsSteer(ref, index, expectedEntryId) {
    const client = requireClient();
    try {
      await client.request("turn/promoteQueuedAsSteer", { ref, index, expectedEntryId });
    } catch (err) {
      throw mapConflict(err);
    }
  },

  async cancelQueued(ref, index, expectedEntryId) {
    const client = requireClient();
    try {
      return await client.request("turn/cancelQueued", { ref, index, expectedEntryId });
    } catch (err) {
      throw mapConflict(err);
    }
  },

  async setModel(ref, modelProvider, model) {
    const client = requireClient();
    try {
      await client.request("thread/model/set", { ref, modelProvider, model });
    } catch (err) {
      throw mapConflict(err);
    }
  },

  async setReasoningEffort(ref, level) {
    const client = requireClient();
    try {
      await client.request("thread/reasoning-effort/set", { ref, reasoningEffort: level });
    } catch (err) {
      throw mapConflict(err);
    }
  },

  async setGoal(ref, objective) {
    const client = requireClient();
    try {
      return await client.request("goal/set", { ref, objective });
    } catch (err) {
      throw mapConflict(err);
    }
  },

  async rename(ref, name) {
    const client = requireClient();
    try {
      await client.request("serf/thread/name/set", { ref, name });
    } catch (err) {
      throw mapConflict(err);
    }
  },

  async compact(ref) {
    const client = requireClient();
    try {
      await client.request("thread/compact/start", { ref });
    } catch (err) {
      throw mapConflict(err);
    }
  },

  async clearThread(ref) {
    const client = requireClient();
    let resp: ThreadClearResponse;
    try {
      resp = await client.request("thread/clear", { ref });
    } catch (err) {
      throw mapConflict(err);
    }
    const now = Date.now();
    threadsStore.setState((s) => {
      const patch: Partial<ThreadsStoreState> = {};
      if (s.threads.has(ref)) {
        const next = new Map(s.threads);
        next.set(ref, hydrateThread({ thread: resp.thread }, ref, now));
        patch.threads = next;
      }
      if (s.watchedThreads.has(ref)) {
        const next = new Map(s.watchedThreads);
        next.set(ref, hydrateThread({ thread: resp.thread }, ref, now));
        patch.watchedThreads = next;
      }
      return Object.keys(patch).length > 0 ? patch : s;
    });
  },

  async shutdown(ref) {
    const client = requireClient();
    try {
      await client.request("thread/shutdown", { ref });
    } catch (err) {
      throw mapConflict(err);
    }
  },

  async forkFromTurn(ref, opts) {
    const client = requireClient();
    try {
      // ThreadForkParams.sourceTurnId has no `omitempty` on the wire
      // (appwire/types.go:694) - it is REQUIRED JSON, unlike every other
      // fork field - so an aside-mode caller that never set it (aside is
      // mutually exclusive with sourceTurnId) still gets a well-formed
      // request rather than an absent field.
      return await client.request("thread/fork", { ...opts, ref, sourceTurnId: opts.sourceTurnId ?? "" });
    } catch (err) {
      throw mapConflict(err);
    }
  },

  async listModels(refresh) {
    const client = requireClient();
    if (!refresh && modelsCache) return modelsCache;
    if (!refresh && inflightModelsList) return inflightModelsList;
    // No mapConflict here: model/list is a read-only listing with no
    // turn-CAS concept (verified against every server-side handler - see
    // this file's own describe block for the exact citations).
    const request = client.request("model/list", {});
    if (!refresh) inflightModelsList = request;
    try {
      const resp = await request;
      modelsCache = resp;
      return resp;
    } finally {
      if (!refresh) inflightModelsList = null;
    }
  },

  async listTasks(ref) {
    const client = requireClient();
    // No mapConflict here either, same reasoning as listModels above.
    const resp = await client.request("serf/tasks/list", { ref });
    return resp.data;
  },

  async resolveEscalation(ref, escalationId, approve) {
    const client = requireClient();
    // Map a daemon Conflict to ConflictError, same as every other mutating
    // action: the daemon surfaces a stale/double/raced resolve as
    // appwire.Conflict() (server/appwire_runtime.go's
    // handleAppSandboxEscalationResolve) precisely so the client drops the card
    // instead of retrying. mapConflict passes any non-conflict rejection
    // through unchanged, and the local clear below runs only on a resolve that
    // actually landed.
    try {
      await client.request("serf/sandbox/escalation/resolve", { ref, escalationId, approve });
    } catch (err) {
      throw mapConflict(err);
    }
    threadsStore.setState((s) => {
      const patch: Partial<ThreadsStoreState> = {};
      const model = s.threads.get(ref);
      if (model) {
        const resolved = resolvePendingEscalation(model, escalationId);
        if (resolved !== model) {
          const next = new Map(s.threads);
          next.set(ref, resolved);
          patch.threads = next;
        }
      }
      const watchedModel = s.watchedThreads.get(ref);
      if (watchedModel) {
        const resolved = resolvePendingEscalation(watchedModel, escalationId);
        if (resolved !== watchedModel) {
          const next = new Map(s.watchedThreads);
          next.set(ref, resolved);
          patch.watchedThreads = next;
        }
      }
      return Object.keys(patch).length > 0 ? patch : s;
    });
  },

  setScrollPosition(ref, position) {
    threadsStore.setState((s) => {
      if (s.scrollPositions.get(ref) === position) return s; // same reference: no-op, like handleNotification's own guard
      const next = new Map(s.scrollPositions);
      next.set(ref, position);
      return { scrollPositions: next };
    });
  },
}));

export function useThreadsStore(): ThreadsStoreState;
export function useThreadsStore<T>(selector: (state: ThreadsStoreState) => T): T;
export function useThreadsStore<T>(selector?: (state: ThreadsStoreState) => T): T | ThreadsStoreState {
  // Not a real conditional hook call - see stores/connection.ts's own
  // useConnectionStore for the full explanation (zustand's useStore has a
  // `selector = identity` JS default param, so both arms run identically).
  // biome-ignore lint/correctness/useHookAtTopLevel: same hook both arms, JS default param not a real conditional - see stores/connection.ts
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
  pendingThreadHydrations.clear();
  watchRefCounts.clear();
  inflightWatchHydrates.clear();
  inflightWatchIncludeTurns.clear();
  pendingWatchedHydrations.clear();
  watchGenerations.clear();
  watchIncludeTurns.clear();
  watchHydratedIncludeTurns.clear();
  modelsCache = null;
  inflightModelsList = null;
  unwireNotification?.();
  unwireReady?.();
  unwireNotification = null;
  unwireReady = null;
  wiredClient = null;
  threadsStore.setState({
    threads: new Map(),
    frameTimes: new Map(),
    watchedThreads: new Map(),
    watchedFrameTimes: new Map(),
    scrollPositions: new Map(),
  });
}
