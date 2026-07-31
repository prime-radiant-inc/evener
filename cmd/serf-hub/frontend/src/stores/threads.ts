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
  collectAuthoritativeMutationIds,
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
} from "../protocol/types.gen";
import { translateAttachmentMarkers } from "./attachmentMarkers";
import { connectionStore } from "./connection";
import { MutationDispatcher } from "./mutationDispatcher";
import {
  type MutationAttachment,
  type MutationIntent,
  type MutationOptimisticRecord,
  MutationOutbox,
  type MutationOutboxOptions,
  type MutationOutboxRecord,
  type MutationRecoveryRecord,
} from "./mutationOutbox";
import { MutationOutboxIndexedDB } from "./mutationOutboxIndexedDB";
import { createSecureUUID } from "./secureUUID";

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

export type ComposerMutationRoute = "send" | "queue" | "steer" | "drain";

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
  // Same expectedEntryId Conflict semantics as promoteQueuedAsSteer. The
  // authoritative removal result is owned by the asynchronous dispatcher;
  // this resolves once the intent itself is durably committed.
  cancelQueued(ref: string, index: number, expectedEntryId: string): Promise<void>;
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
  // either - unlike ThreadModel.capabilities, which thread/status/changed
  // now refreshes); pass refresh:true to bypass the cache and
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
  // Lists the session's jobs (serf/jobs/list) and fetches one job's output
  // tail (serf/jobs/output). Both Data fields are `any` on the wire catalog
  // (appwire/types.go) - these return the raw field verbatim, never wrapped,
  // so the store stays shape-agnostic; the caller owns interpreting it (the
  // chrome stream's parseJobListData / parseJobOutputData). Wire truth:
  // agent/jobs_panel.go's JobSummary / JobOutputTail.
  listJobs(ref: string): Promise<unknown>;
  jobOutput(ref: string, jobId: string): Promise<unknown>;
  // Answers one serf/sandbox/escalation/requested via serf/sandbox/
  // escalation/resolve. On success, removes the escalation from whichever
  // of threads/watchedThreads currently track `ref` (both, if both do -
  // see ThreadsStoreState's own doc comment on why they're independent
  // maps). On rejection, propagates unchanged - the caller (the
  // escalation rail) owns surfacing the failure.
  resolveEscalation(ref: string, escalationId: string, approve: boolean): Promise<void>;
}

// Module-private bookkeeping the locked interface doesn't expose: pane
// refcounts per ref, the hydrate promise currently in flight for a ref (so
// two panes racing to ensureThread() the same ref share one thread/read
// instead of sending two), and which client this store has already wired
// its notification/ready handlers onto (plus that wiring's own unsubscribe
// functions - see rewireClient below).
const refCounts = new Map<string, number>();
// A generation changes whenever the last real pane releases and a new pane
// claims the ref. An ensure that fails after its pane lifecycle was retired
// must not roll back a replacement lifecycle's claim.
const ensureGenerations = new Map<string, number>();
const inflightHydrates = new Map<string, Promise<ThreadModel | null>>();
const inflightHydrateClients = new Map<string, AppwireClientLike>();
const inflightHydrateEpochs = new Map<string, number>();
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
  epoch: number;
  notifications: AnyNotification[];
  routing: PendingHydrationRouting;
};
// A thread/read subscribes before it returns its snapshot. Notifications can
// therefore arrive in the gap between the source subscription and snapshot
// response. Keep the newest hydration's notifications out of the old model,
// then fold them onto the returned snapshot before publishing it.
const pendingThreadHydrations = new Map<string, PendingThreadHydration>();
const pendingWatchedHydrations = new Map<string, PendingThreadHydration>();
// One owned hydration lifecycle per (ref, owner kind, owner generation). It
// exists only while that owner still needs a first authoritative model and the
// newest attempt has failed: the attempt that failed schedules exactly one
// retry through it, and every owner of that generation awaits the one
// firstHydration promise instead of racing its own read.
//
// Backoff paces those retries and nothing else. Release, client identity, ready
// epoch, and owner generation are the correctness fences, and they are all
// enforced by one mechanism: each of them retires this record, and retiring a
// record cancels the retry it holds (closeOwnedHydration).
type HydrationOwnerKind = "thread" | "watched";

interface OwnedHydration {
  generation: number;
  retryAttempt: number;
  cancelRetry: (() => void) | null;
  // Settles with the model this lifecycle publishes, or null once the
  // lifecycle is retired (release, client swap, new ready generation) so a
  // waiting owner re-arms against the current generation instead of hanging.
  firstHydration: Promise<ThreadModel | null>;
  settle: (model: ThreadModel | null) => void;
}

const ownedThreadHydrations = new Map<string, OwnedHydration>();
const ownedWatchedHydrations = new Map<string, OwnedHydration>();

// A scheduler, not a clock: tests install a manual queue and invoke the retry
// callback directly, so no assertion in this store's suite depends on elapsed
// time. The returned function cancels the scheduled callback.
type HydrationRetryScheduler = (attempt: number, retry: () => void) => () => void;

const HYDRATION_RETRY_BASE_MS = 500;
const HYDRATION_RETRY_MAX_MS = 15_000;

const backoffHydrationRetryScheduler: HydrationRetryScheduler = (attempt, retry) => {
  const delay = Math.min(HYDRATION_RETRY_MAX_MS, HYDRATION_RETRY_BASE_MS * 2 ** Math.max(0, attempt - 1));
  const timer = setTimeout(retry, delay);
  return () => clearTimeout(timer);
};

let hydrationRetryScheduler: HydrationRetryScheduler = backoffHydrationRetryScheduler;

export function installHydrationRetrySchedulerForTests(scheduler: HydrationRetryScheduler): () => void {
  const previous = hydrationRetryScheduler;
  hydrationRetryScheduler = scheduler;
  return () => {
    hydrationRetryScheduler = previous;
  };
}

let wiredClient: AppwireClientLike | null = null;
let readyEpoch = 0;
let unwireNotification: (() => void) | null = null;
let unwireReady: (() => void) | null = null;
let resolveClientReadyOrRewired: (() => void) | null = null;
let clientReadyOrRewired: Promise<void> = Promise.resolve();
let dispatchReadyClient: AppwireClientLike | null = null;
let dispatchReadyEpoch = -1;
const pinnedMutationRefs = new Set<string>();
const dispatchableMutationRefs = new Set<string>();

interface MutationRuntime {
  storage: MutationOutboxIndexedDB;
  dispatcher: MutationDispatcher;
  outbox: MutationOutbox;
  start: Promise<void>;
  active: boolean;
}

let mutationRuntime: MutationRuntime | null = null;
let mutationStorageForTests: MutationOutboxIndexedDB | null = null;
let createMutationBroadcastChannelForTests: NonNullable<MutationOutboxOptions["createBroadcastChannel"]> | undefined;
const mutationPersistenceListeners = new Set<(targetRefs: string[]) => void>();

function notifyMutationPersistence(targetRefs: Iterable<string>): void {
  const refs = [...new Set(targetRefs)];
  for (const listener of mutationPersistenceListeners) listener(refs);
}

function currentDispatchClient(targetRef?: string): AppwireClientLike | null {
  if (wiredClient !== dispatchReadyClient || readyEpoch !== dispatchReadyEpoch) return null;
  if (targetRef && !dispatchableMutationRefs.has(targetRef)) return null;
  return wiredClient?.state === "ready" ? wiredClient : null;
}

function dropUnpinnedModel(ref: string): void {
  if (pinnedMutationRefs.has(ref) || (refCounts.get(ref) ?? 0) > 0) return;
  // Nothing owns this ref any more, so no scheduled retry may outlive it.
  retireOwnedHydration("thread", ref);
  threadsStore.setState((state) => {
    if (!state.threads.has(ref) && !state.frameTimes.has(ref)) return state;
    const threads = new Map(state.threads);
    threads.delete(ref);
    const frameTimes = new Map(state.frameTimes);
    frameTimes.delete(ref);
    return { threads, frameTimes };
  });
}

async function refreshMutationPins(runtime: MutationRuntime, targetRefs: Iterable<string>): Promise<void> {
  if (!runtime.active || mutationRuntime !== runtime) return;
  for (const targetRef of targetRefs) {
    if (!runtime.active || mutationRuntime !== runtime) return;
    const [outbox, optimistic] = await Promise.all([
      runtime.storage.listOutbox(targetRef),
      runtime.storage.listOptimistic(targetRef),
    ]);
    if (outbox.length > 0) {
      pinnedMutationRefs.add(targetRef);
      continue;
    }
    if (optimistic.length > 0) {
      pinnedMutationRefs.add(targetRef);
      dispatchableMutationRefs.delete(targetRef);
      continue;
    }
    pinnedMutationRefs.delete(targetRef);
    dispatchableMutationRefs.delete(targetRef);
    dropUnpinnedModel(targetRef);
  }
}

function scheduleMutationDispatch(runtime: MutationRuntime, targetRefs: Iterable<string>): void {
  if (!runtime.active) return;
  const refs = [...new Set(targetRefs)].filter((targetRef) => dispatchableMutationRefs.has(targetRef));
  if (refs.length === 0) return;
  void runtime.dispatcher
    .dispatchTargets(refs)
    .then(() => refreshMutationPins(runtime, refs))
    .catch(() => {
      // Durable records remain discoverable by the next ready/lifecycle scan.
    });
}

function handleDiscoveredMutations(runtime: MutationRuntime, targetRefs: Iterable<string>): void {
  const refs = [...new Set(targetRefs)];
  for (const targetRef of refs) pinnedMutationRefs.add(targetRef);
  notifyMutationPersistence(refs);
  scheduleMutationDispatch(runtime, refs);

  const client = currentDispatchClient();
  if (!client) return;
  const epoch = dispatchReadyEpoch;
  for (const targetRef of refs) {
    if (dispatchableMutationRefs.has(targetRef)) continue;
    const pending = pendingThreadHydrations.get(targetRef);
    if (pending?.client === client && pending.epoch === epoch) continue;
    void handleReady(client, epoch, targetRef);
  }
}

function getMutationRuntime(): MutationRuntime | null {
  if (mutationRuntime) return mutationRuntime;
  if (!globalThis.indexedDB) return null;

  const storage = mutationStorageForTests ?? new MutationOutboxIndexedDB();
  const dispatcher = new MutationDispatcher(storage, {
    getClient: currentDispatchClient,
    onStorageChange: notifyMutationPersistence,
  });
  let runtime: MutationRuntime;
  const outbox = new MutationOutbox(storage, {
    isReady: () => currentDispatchClient() !== null,
    onDiscover: (targetRefs) => {
      handleDiscoveredMutations(runtime, targetRefs);
    },
    createBroadcastChannel: createMutationBroadcastChannelForTests,
  });
  runtime = {
    storage,
    dispatcher,
    outbox,
    start: Promise.resolve(),
    active: true,
  };
  mutationRuntime = runtime;
  runtime.start = outbox.start();
  return runtime;
}

function requireMutationRuntime(): MutationRuntime {
  const runtime = getMutationRuntime();
  if (!runtime) throw new Error("threads store: IndexedDB is unavailable; mutation was not sent");
  return runtime;
}

export interface MutationPersistenceSnapshot {
  outbox: MutationOutboxRecord[];
  optimistic: MutationOptimisticRecord[];
  recovery: MutationRecoveryRecord[];
}

export function subscribeMutationPersistence(listener: (targetRefs: string[]) => void): () => void {
  mutationPersistenceListeners.add(listener);
  return () => mutationPersistenceListeners.delete(listener);
}

export async function readMutationPersistence(targetRef?: string): Promise<MutationPersistenceSnapshot> {
  const runtime = getMutationRuntime();
  if (!runtime) return { outbox: [], optimistic: [], recovery: [] };
  await runtime.start;
  const [outbox, optimistic, recovery] = await Promise.all([
    runtime.storage.listOutbox(targetRef),
    runtime.storage.listOptimistic(targetRef),
    runtime.storage.listRecovery(targetRef),
  ]);
  return { outbox, optimistic, recovery };
}

export async function retryBlockedMutation(clientMutationId: string): Promise<boolean> {
  const runtime = requireMutationRuntime();
  await runtime.start;
  const record = await runtime.storage.getOutbox(clientMutationId);
  if (record?.state !== "blockedUnknown") return false;
  await runtime.storage.markUnknown(clientMutationId, "submitting");
  notifyMutationPersistence([record.targetRef]);
  handleDiscoveredMutations(runtime, [record.targetRef]);
  return true;
}

export async function updateRecoveryMutation(
  clientMutationId: string,
  targetRef: string,
  text: string,
  attachments: InputAttachment[],
): Promise<boolean> {
  const runtime = requireMutationRuntime();
  await runtime.start;
  const record = await runtime.storage.updateRecoveryInput(
    clientMutationId,
    buildInput(text, attachments),
    durableAttachments(attachments),
  );
  if (!record) return false;
  notifyMutationPersistence([targetRef]);
  return true;
}

export async function discardRecoveryMutation(clientMutationId: string, targetRef: string): Promise<boolean> {
  const runtime = requireMutationRuntime();
  await runtime.start;
  const discarded = await runtime.storage.discardRecovery(clientMutationId);
  if (discarded) notifyMutationPersistence([targetRef]);
  return discarded;
}

export async function resendRecoveryMutation(
  clientMutationId: string,
  targetRef: string,
  route: ComposerMutationRoute,
  text: string,
  attachments: InputAttachment[],
): Promise<MutationOutboxRecord | undefined> {
  const runtime = requireMutationRuntime();
  await runtime.start;
  const intent = composerMutationIntent(targetRef, route, text, attachments);
  const record = await runtime.storage.resendRecovery(clientMutationId, intent);
  if (!record) return undefined;
  pinnedMutationRefs.add(targetRef);
  notifyMutationPersistence([targetRef]);
  handleDiscoveredMutations(runtime, [targetRef]);
  return record;
}

export function setMutationStorageForTests(storage: MutationOutboxIndexedDB): void {
  if (mutationRuntime) throw new Error("setMutationStorageForTests must run before the mutation runtime starts");
  mutationStorageForTests = storage;
}

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
const inflightWatchHydrateClients = new Map<string, AppwireClientLike>();
const inflightWatchHydrateEpochs = new Map<string, number>();
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

interface ThreadHydration {
  model: ThreadModel;
  response: ThreadReadResponse;
}

async function hydrateAndSubscribe(
  client: AppwireClientLike,
  ref: string,
  now: number,
  pending: PendingThreadHydration,
): Promise<ThreadHydration> {
  let response: ThreadReadResponse;
  try {
    response = await client.request("thread/read", readParams(ref));
  } catch (err) {
    // thread/read is answered from the daemon's in-memory snapshot, so a
    // rejection here is a transport failure, not a slow file read and not a
    // lost claim. Ask this ref's owner generation to read again.
    scheduleOwnedHydrationRetry("thread", ref, pending);
    throw err;
  }
  const model = hydrateThread(response, ref, now);
  applyHydrationResponseCut(pending, ref, model);
  return { model, response };
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
  pending: PendingThreadHydration,
  includeTurns = false,
): Promise<ThreadModel> {
  let resp: ThreadReadResponse;
  try {
    resp = await client.request("thread/read", watchReadParams(ref, includeTurns));
  } catch (err) {
    scheduleOwnedHydrationRetry("watched", ref, pending);
    throw err;
  }
  const model = hydrateThread(resp, ref, now);
  applyHydrationResponseCut(pending, ref, model);
  return model;
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
// rejects the call), then one image item per attachment. The text arrives
// verbatim: any new SUBMIT path through here owes it the same
// translateAttachmentMarkers pass composerMutationIntent applies.
function buildInput(text: string, attachments?: InputAttachment[]): InputItem[] {
  const input: InputItem[] = [];
  if (text.trim()) input.push({ type: "text", text });
  for (const att of attachments ?? []) {
    input.push({ type: "image", mediaType: att.mediaType, data: att.data, name: att.name });
  }
  return input;
}

function attachmentBlob(attachment: InputAttachment): Blob {
  const bytes = Uint8Array.from(atob(attachment.data), (character) => character.charCodeAt(0));
  return new Blob([bytes], { type: attachment.mediaType });
}

function durableAttachments(attachments?: InputAttachment[]): MutationAttachment[] {
  return (attachments ?? []).map((attachment) => ({
    presentationId: createSecureUUID(),
    name: attachment.name ?? "attachment",
    mediaType: attachment.mediaType,
    blob: attachmentBlob(attachment),
  }));
}

function composerMutationIntent(
  ref: string,
  route: ComposerMutationRoute,
  text: string,
  attachments?: InputAttachment[],
): MutationIntent {
  const model = threadsStore.getState().threads.get(ref);
  // Translated HERE, not inside buildInput: this is the submit boundary. The
  // other buildInput caller (updateRecoveryMutation) persists a composer DRAFT
  // that recoveryComposerDraft reads back into the textarea, where the raw
  // "[image N]" markers are still the chips' anchors and must survive.
  const input = buildInput(translateAttachmentMarkers(text, attachments), attachments);
  const base = {
    targetRef: ref,
    threadId: model?.threadId,
    attachments: durableAttachments(attachments),
  };
  if (route === "send") {
    return {
      ...base,
      method: "turn/start",
      payload: { ref, input },
      optimisticDisplay: { method: "turn/start", input },
    };
  }
  const expectedTurnId = model?.activeTurnId ?? "";
  if (route === "queue" || route === "steer") {
    const method = route === "queue" ? "turn/queue" : "turn/steer";
    return {
      ...base,
      method,
      payload: { ref, expectedTurnId, input },
      optimisticDisplay: { method, input },
    };
  }
  // Drain steers the ACTIVE turn by contract — the hub and the daemon both
  // reject an empty expectedTurnId (appwire InvalidParams, "drain: no active
  // turn to steer"). Minting a durable intent that violates the contract at
  // birth would poison the outbox head: the rejection names no
  // clientMutationId, so nothing can ever settle it (kata wr3s). Refuse
  // before anything durable exists; callers surface this like any submit
  // failure. Queue and steer intents are NOT gated here: the status-flip /
  // turn-started race window deliberately lets them race the daemon (see
  // deriveSendQueueAvailability), and a lost race now lands in recovery.
  if (!expectedTurnId) throw new Error("Drain failed: no active turn to steer");
  const expectedQueueRevision = model?.queue?.revision ?? 0;
  return {
    ...base,
    method: "turn/drainAsSteer",
    payload: { ref, expectedTurnId, expectedQueueRevision, input },
    optimisticDisplay: { method: "turn/drainAsSteer", input },
  };
}

async function enqueueMutationIntent(intent: MutationIntent): Promise<void> {
  const ref = intent.targetRef;
  const client = requireClient();
  if (client.state !== "ready") throw new Error(`threads store: cannot enqueue mutation while ${client.state}`);
  const runtime = requireMutationRuntime();
  await runtime.start;
  pinnedMutationRefs.add(ref);
  const pending = pendingThreadHydrations.get(ref);
  if (pending?.client !== wiredClient || pending.epoch !== readyEpoch) {
    dispatchableMutationRefs.add(ref);
  }
  await runtime.outbox.enqueueIntent(intent);
  notifyMutationPersistence([ref]);
}

async function enqueueMutation(
  ref: string,
  method: MutationIntent["method"],
  payload: Record<string, unknown>,
  optimisticDisplay: unknown,
  attachments?: InputAttachment[],
): Promise<void> {
  await enqueueMutationIntent({
    targetRef: ref,
    threadId: threadsStore.getState().threads.get(ref)?.threadId,
    method,
    payload,
    attachments: durableAttachments(attachments),
    optimisticDisplay,
  });
  notifyMutationPersistence([ref]);
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
// `model`. v2 carries authoritative ref/threadId on every thread-scoped
// notification. turn/completed additionally requires the matching active turn
// so a stale completion cannot settle a newer turn on the same thread.
function targetsNotification(n: AnyNotification, model: ThreadModel): boolean {
  if (!notificationTargetsThread(n, model)) return false;
  if (n.method === "turn/completed") {
    const turnId = n.params.turnId || n.params.turn.id;
    return model.activeTurnId === turnId;
  }
  return true;
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

function notificationMutationIdentities(n: AnyNotification): string[] {
  if (n.method === "thread/queueChanged") return n.params.queue.clientMutationIds ?? [];
  if (n.method === "serf/steering/injected") {
    return n.params.clientMutationId ? [n.params.clientMutationId] : [];
  }
  if (n.method === "item/started" || n.method === "item/completed") {
    return n.params.item.clientMutationId ? [n.params.item.clientMutationId] : [];
  }
  if (n.method === "turn/started" || n.method === "turn/completed") {
    return (n.params.turn.items ?? [])
      .map((item) => item.clientMutationId)
      .filter((clientMutationId): clientMutationId is string => Boolean(clientMutationId));
  }
  return [];
}

function applyHydrationResponseCut(pending: PendingThreadHydration, ref: string, model: ThreadModel): void {
  // AppWire orders the matching response at the authoritative snapshot cut.
  // Every notification already buffered is at or before that cut and is
  // already represented by this model. Notifications delivered after the
  // response enter the buffer later and remain ordered for replay.
  pending.notifications = [];
  pending.routing = pendingHydrationRouting(ref, model);
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
    // item so the following turn/completed frame remains in order.
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
  epoch: number,
): PendingThreadHydration {
  const pending = {
    client,
    epoch,
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
  epoch: number,
): PendingThreadHydration {
  const pending = {
    client,
    epoch,
    notifications: [],
    routing: pendingHydrationRouting(ref, model),
  };
  pendingWatchedHydrations.set(ref, pending);
  return pending;
}

function transferPendingHydration(previous: PendingThreadHydration | undefined, next: PendingThreadHydration): void {
  if (!previous) return;

  // Notifications are ordered events: equal payloads can be distinct
  // streaming chunks. The array copy transfers the existing buffer once
  // without inventing payload identity or changing event multiplicity.
  next.notifications = [...previous.notifications];
  next.routing = { ...previous.routing };
}

function bufferPendingNotification(pending: PendingThreadHydration, notification: AnyNotification): void {
  pending.notifications.push(notification);
  advancePendingHydrationRouting(notification, pending);
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

// A snapshot may only publish into the ready generation it was cut on, and the
// epoch alone says that for the client too: rewireClient bumps readyEpoch
// before it assigns wiredClient, the epoch only ever increases, and every
// hydration captures its client and its epoch in the same synchronous step —
// the one site that used to capture them either side of an await now re-reads
// the client, and a test pins it there. So a hydration under a superseded
// client necessarily carries a superseded epoch.
function publishThreadHydration(ref: string, pending: PendingThreadHydration, model: ThreadModel): ThreadModel | null {
  if (pendingThreadHydrations.get(ref) !== pending) return null;
  if (readyEpoch !== pending.epoch) return null;
  if ((refCounts.get(ref) ?? 0) <= 0 && !pinnedMutationRefs.has(ref)) {
    pendingThreadHydrations.delete(ref);
    return null;
  }

  const { model: hydrated, appliedAt } = replayHydrationNotifications(model, pending.notifications);

  pendingThreadHydrations.delete(ref);
  threadsStore.setState((s) => {
    const nextThreads = new Map(s.threads);
    nextThreads.set(ref, hydrated);
    if (appliedAt.length === 0) return { threads: nextThreads };
    const nextFrameTimes = new Map(s.frameTimes);
    let times = nextFrameTimes.get(ref) ?? [];
    for (const now of appliedAt) times = appendFrameTime(times, now);
    nextFrameTimes.set(ref, times);
    return { threads: nextThreads, frameTimes: nextFrameTimes };
  });
  settleOwnedHydration("thread", ref, hydrated);
  return hydrated;
}

async function publishAndReconcileThreadHydration(
  ref: string,
  pending: PendingThreadHydration,
  hydration: ThreadHydration,
): Promise<ThreadModel | null> {
  const published = publishThreadHydration(ref, pending, hydration.model);
  if (!published) return null;
  // The authoritative read has succeeded, so the replay gate opens HERE — in
  // the same synchronous step publishThreadHydration deleted the ref's
  // pending-hydration entry — not after the storage hygiene below. Between
  // that delete and the end of these awaits, a lifecycle discovery scan sees
  // "no hydration in flight, not dispatchable" and mints a redundant targeted
  // resync; opening the gate first routes that scan to dispatch instead
  // (a no-op drain when nothing is dispatchable). The add in
  // refreshTrackedThread stays as the gate for its own await-completion path.
  if (pinnedMutationRefs.has(ref)) dispatchableMutationRefs.add(ref);
  const runtime = getMutationRuntime();
  if (runtime) {
    const authoritativeIds = collectAuthoritativeMutationIds(hydration.response);
    await runtime.dispatcher.reconcileIdentities(authoritativeIds);
    // The same read that settles what the authority knows also proves what it
    // does not: a blockedUnknown record absent from every authoritative set
    // was never journaled, so it returns to dispatch here rather than parking
    // forever behind an outage that has since recovered (kata gwea).
    await runtime.dispatcher.restoreProvenAbsent(ref, authoritativeIds);
    await refreshMutationPins(runtime, [ref]);
  }
  return published;
}

function publishWatchedHydration(
  ref: string,
  pending: PendingThreadHydration,
  model: ThreadModel,
  includeTurns: boolean,
  generation: number,
): ThreadModel | null {
  // Same ready-generation gate as publishThreadHydration, for the same reason.
  // No owner check beside it: unlike a pinned thread ref, a watched ref cannot
  // outlive its claim while holding a pending entry — releaseWatchedThread is
  // the only decrementer and deletes the pending entry in the same block, and
  // the generation only advances while the count is zero, i.e. while no pending
  // entry exists. storeWatchedModel re-decides both a call later regardless.
  if (pendingWatchedHydrations.get(ref) !== pending) return null;
  if (readyEpoch !== pending.epoch) return null;

  const replayed = replayHydrationNotifications(model, pending.notifications);
  pendingWatchedHydrations.delete(ref);
  storeWatchedModel(ref, replayed.model, includeTurns, generation, replayed.appliedAt);
  settleOwnedHydration("watched", ref, replayed.model);
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
  if (n.method === "serf/thread/resync") {
    if (wiredClient) void handleReady(wiredClient, readyEpoch, n.params.ref);
    return;
  }
  const mutationIdentities = notificationMutationIdentities(n);
  if (mutationIdentities.length > 0) {
    const runtime = getMutationRuntime();
    if (runtime) {
      void runtime.dispatcher
        .reconcileIdentities(mutationIdentities)
        .then(() => {
          const ref = notificationRef(n);
          return ref ? refreshMutationPins(runtime, [ref]) : undefined;
        })
        .catch(() => {
          // A later snapshot or receipt retries the same identity settlement.
        });
    }
  }
  applySubagentJobSignal(n);
  const now = Date.now();
  const { threads, frameTimes, watchedThreads, watchedFrameTimes } = threadsStore.getState();
  const pendingRefs = new Set<string>();
  for (const [ref, pending] of pendingThreadHydrations) {
    if (targetsPendingHydration(n, pending)) {
      bufferPendingNotification(pending, n);
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
      bufferPendingNotification(pending, n);
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

function ownedHydrationsFor(kind: HydrationOwnerKind): Map<string, OwnedHydration> {
  return kind === "watched" ? ownedWatchedHydrations : ownedThreadHydrations;
}

// A ref is owned while a pane holds a claim, a watcher holds a claim, or a
// durable mutation record pins it. Ownership is what makes convergence this
// store's job at all; with none left there is nothing to converge for.
function hydrationOwnerActive(kind: HydrationOwnerKind, ref: string): boolean {
  if (kind === "watched") return (watchRefCounts.get(ref) ?? 0) > 0;
  return (refCounts.get(ref) ?? 0) > 0 || pinnedMutationRefs.has(ref);
}

function hydrationOwnerGeneration(kind: HydrationOwnerKind, ref: string): number {
  return kind === "watched" ? (watchGenerations.get(ref) ?? 0) : (ensureGenerations.get(ref) ?? 0);
}

function openOwnedHydration(kind: HydrationOwnerKind, ref: string): OwnedHydration {
  const lifecycles = ownedHydrationsFor(kind);
  const generation = hydrationOwnerGeneration(kind, ref);
  const existing = lifecycles.get(ref);
  if (existing?.generation === generation) return existing;
  if (existing) retireOwnedHydration(kind, ref);
  let settle: (model: ThreadModel | null) => void = () => {};
  const firstHydration = new Promise<ThreadModel | null>((resolve) => {
    settle = resolve;
  });
  const owned: OwnedHydration = { generation, retryAttempt: 0, cancelRetry: null, firstHydration, settle };
  lifecycles.set(ref, owned);
  return owned;
}

// Retirement is total, and it is the only fence the retry path needs. Closing a
// lifecycle removes its record AND cancels its scheduled callback in the same
// step, so a retired lifecycle cannot reach the wire: the production scheduler
// is clearTimeout, and a fired callback that somehow outruns its cancel finds
// its own record gone from the map and returns. Every state change that would
// invalidate a pending retry — client swap, ready-epoch bump, released claim,
// dropped pin, superseded owner generation — runs through here first, which is
// why none of them needs its own check inside the callback. Do not re-add one.
function closeOwnedHydration(kind: HydrationOwnerKind, ref: string, model: ThreadModel | null): void {
  const lifecycles = ownedHydrationsFor(kind);
  const owned = lifecycles.get(ref);
  if (!owned) return;
  lifecycles.delete(ref);
  owned.cancelRetry?.();
  owned.cancelRetry = null;
  owned.settle(model);
}

// A published authoritative model retires the lifecycle that was waiting for
// one, whichever attempt produced it — this owner's own retry, a reconnect, or
// a targeted resync. Settling at the single publish point (rather than on the
// retry's own promise) is what keeps an owner from waiting on a lifecycle some
// other attempt already satisfied, and resets the retry attempt with it.
function settleOwnedHydration(kind: HydrationOwnerKind, ref: string, model: ThreadModel): void {
  closeOwnedHydration(kind, ref, model);
}

function retireOwnedHydration(kind: HydrationOwnerKind, ref: string): void {
  closeOwnedHydration(kind, ref, null);
}

// A new client or a new ready epoch owns convergence for every ref: cancel the
// retries the retired generation scheduled and wake its owners so they re-arm
// against the current one.
function retireAllOwnedHydrations(): void {
  for (const ref of [...ownedThreadHydrations.keys()]) retireOwnedHydration("thread", ref);
  for (const ref of [...ownedWatchedHydrations.keys()]) retireOwnedHydration("watched", ref);
}

// scheduleOwnedHydrationRetry is the self-heal itself: the attempt that just
// failed in transport asks its owner generation to read again. At most one
// retry is outstanding per lifecycle — concurrent owners share it — and only
// while this attempt is still the newest one on the current client and ready
// epoch. A newer client, a newer ready generation, and a released claim each
// own convergence themselves, so none of them gets a retry from here.
//
// Every check below decides whether a retry is worth ARMING. Nothing re-checks
// them when it fires, because arming is guarded by a lifecycle record and
// retiring that record cancels the retry with it (closeOwnedHydration).
function scheduleOwnedHydrationRetry(kind: HydrationOwnerKind, ref: string, pending: PendingThreadHydration): void {
  const pendingHydrations = kind === "watched" ? pendingWatchedHydrations : pendingThreadHydrations;
  // A rejection removes only this attempt's own response-cut buffer, and it
  // removes it now rather than a microtask later: the retry scheduled below
  // must be able to see that no attempt is in flight for this ref. A newer
  // attempt already owns the entry, so leave that one — and its retry — alone.
  if (pendingHydrations.get(ref) !== pending) return;
  pendingHydrations.delete(ref);
  const client = pending.client;
  const epoch = pending.epoch;
  if (wiredClient !== client || readyEpoch !== epoch) return;
  if (!hydrationOwnerActive(kind, ref)) return;
  const owned = openOwnedHydration(kind, ref);
  if (owned.cancelRetry) return;
  // Not ready is not this lifecycle's to pace: that client generation's next
  // ready trigger re-reads what it tracks and retires this record either way.
  if (client.state !== "ready") return;
  owned.retryAttempt += 1;
  owned.cancelRetry = hydrationRetryScheduler(owned.retryAttempt, () => {
    // The whole fire-time fence: this callback belongs to one lifecycle record,
    // and it acts only while that record is still the live one for this ref.
    // See closeOwnedHydration for why nothing else has to be re-checked here.
    if (ownedHydrationsFor(kind).get(ref) !== owned) return;
    owned.cancelRetry = null;
    // Another attempt reached the wire while this retry waited; it owns the
    // next outcome, including scheduling the retry after it. Retirement says
    // nothing about a concurrent attempt, so this one is its own check.
    if (pendingHydrations.has(ref)) return;
    const retried =
      kind === "watched" ? retryWatchedHydration(client, epoch, ref) : retryTrackedHydration(client, epoch, ref);
    void retried.catch(() => {
      // A failed retry schedules the next one through this same path.
    });
  });
}

// The retry action for a real pane or a pinned outbox ref: one targeted
// authoritative refresh, then the same replay gate a resync opens — mutation
// replay stays closed until an authoritative read actually succeeds.
async function retryTrackedHydration(client: AppwireClientLike, epoch: number, ref: string): Promise<void> {
  dispatchableMutationRefs.delete(ref);
  await refreshTrackedThread(client, epoch, ref, true);
  const runtime = getMutationRuntime();
  if (!runtime || wiredClient !== client || readyEpoch !== epoch || client.state !== "ready") return;
  if (dispatchableMutationRefs.has(ref)) scheduleMutationDispatch(runtime, [ref]);
}

async function retryWatchedHydration(client: AppwireClientLike, epoch: number, ref: string): Promise<void> {
  await refreshWatchedThread(client, epoch, ref, true);
}

// refreshTrackedThread re-subscribes one real-pane/pinned ref and replaces its
// model wholesale from the fresh snapshot (hydrateThread) — snapshot recovery
// for notifications the old relay missed. A rejection keeps the last published
// model and leaves the next read to this ref's owned retry lifecycle.
async function refreshTrackedThread(
  client: AppwireClientLike,
  epoch: number,
  ref: string,
  targetedResync: boolean,
): Promise<void> {
  if ((refCounts.get(ref) ?? 0) <= 0 && !pinnedMutationRefs.has(ref)) return;
  const previous = pendingThreadHydrations.get(ref);
  if (!targetedResync && previous?.client === client && previous.epoch === epoch) return;
  const pending = beginThreadHydration(ref, client, threadsStore.getState().threads.get(ref), epoch);
  transferPendingHydration(previous, pending);
  // No pre-check here: pending.client is this `client` and pending.epoch is this
  // `epoch`, so publishThreadHydration re-decides exactly the same thing one
  // frame later, and returning null from there reconciles nothing either. The
  // gate lives in one place.
  const hydration = hydrateAndSubscribe(client, ref, Date.now(), pending).then((result) =>
    publishAndReconcileThreadHydration(ref, pending, result),
  );
  const hasPublishedModel = threadsStore.getState().threads.has(ref);
  // A failed targeted predecessor may already have removed `previous`.
  // Keep the newest targeted read adoptable by the still-active initial
  // caller until a sufficient model has actually published.
  const trackForActiveLifecycle = !hasPublishedModel && (previous !== undefined || targetedResync);
  if (trackForActiveLifecycle) {
    inflightHydrates.set(ref, hydration);
    inflightHydrateClients.set(ref, client);
    inflightHydrateEpochs.set(ref, epoch);
    void hydration
      .finally(() => {
        if (inflightHydrates.get(ref) === hydration) {
          inflightHydrates.delete(ref);
          inflightHydrateClients.delete(ref);
          inflightHydrateEpochs.delete(ref);
        }
      })
      .catch(() => {});
  }
  try {
    const model = await hydration;
    if (model && pinnedMutationRefs.has(ref)) dispatchableMutationRefs.add(ref);
  } catch {
    // The stale model stays published. Convergence is the owned hydration
    // lifecycle's job now (scheduleOwnedHydrationRetry, above).
  } finally {
    if (pendingThreadHydrations.get(ref) === pending) pendingThreadHydrations.delete(ref);
  }
}

// refreshWatchedThread is the watched-owner mirror of refreshTrackedThread.
async function refreshWatchedThread(
  client: AppwireClientLike,
  epoch: number,
  ref: string,
  targetedResync: boolean,
): Promise<void> {
  if ((watchRefCounts.get(ref) ?? 0) <= 0) return;
  const generation = watchGenerations.get(ref) ?? 0;
  const previous = pendingWatchedHydrations.get(ref);
  if (!targetedResync && previous?.client === client && previous.epoch === epoch) return;
  const pending = beginWatchedHydration(ref, client, threadsStore.getState().watchedThreads.get(ref), epoch);
  transferPendingHydration(previous, pending);
  const includeTurns = watchIncludeTurns.get(ref) ?? false;
  // Same as refreshTrackedThread: publishWatchedHydration re-decides this.
  const hydration = hydrateAndSubscribeWatch(client, ref, Date.now(), pending, includeTurns).then((model) =>
    publishWatchedHydration(ref, pending, model, includeTurns, generation),
  );
  const hasPublishedModel = threadsStore.getState().watchedThreads.has(ref);
  const hasSufficientPublishedModel =
    hasPublishedModel && (!includeTurns || (watchHydratedIncludeTurns.get(ref) ?? false));
  // Rich watched callers need the same adoption path as open callers,
  // and a published lean model is not sufficient for includeTurns.
  const trackForActiveLifecycle =
    (previous !== undefined && !hasPublishedModel) || (targetedResync && !hasSufficientPublishedModel);
  if (trackForActiveLifecycle) {
    inflightWatchHydrates.set(ref, hydration);
    inflightWatchHydrateClients.set(ref, client);
    inflightWatchHydrateEpochs.set(ref, epoch);
    inflightWatchIncludeTurns.set(ref, includeTurns);
    void hydration
      .finally(() => {
        if (inflightWatchHydrates.get(ref) === hydration) {
          inflightWatchHydrates.delete(ref);
          inflightWatchHydrateClients.delete(ref);
          inflightWatchHydrateEpochs.delete(ref);
          inflightWatchIncludeTurns.delete(ref);
        }
      })
      .catch(() => {});
  }
  try {
    await hydration;
  } catch {
    // Same rationale as the real-pane path above.
  } finally {
    if (pendingWatchedHydrations.get(ref) === pending) pendingWatchedHydrations.delete(ref);
  }
}

// handleReady re-subscribes every currently-tracked ref by default, or only
// targetRef when a relay-recovery hint names one thread. Either path subscribes
// additively and replaces its model wholesale from the fresh snapshot
// (hydrateThread) — snapshot recovery for notifications the old relay missed.
// The full-set path fires on every client.onReady transition into "ready",
// including the very first — a no-op in practice, since nothing is tracked
// yet that early in the app's lifecycle — and every reconnect after it. Also
// called directly (not via onReady) from rewireClient below, for the case
// where a client swap lands on a client that is ALREADY ready — onReady only
// fires on a FUTURE transition, never retroactively for a client that
// reached "ready" before this store ever subscribed to it (see
// rewireClient's own comment).
async function handleReady(client: AppwireClientLike, epoch: number, targetRef?: string): Promise<void> {
  const targetedResync = targetRef !== undefined;
  if (targetRef) dispatchableMutationRefs.delete(targetRef);
  const runtime = getMutationRuntime();
  const discoveredPinnedRefs =
    runtime && !targetedResync
      ? runtime.start.then(() => runtime.storage.listTargetRefs()).catch(() => [] as string[])
      : Promise.resolve<string[]>([]);
  const refs = targetRef
    ? new Set([targetRef])
    : new Set([...threadsStore.getState().threads.keys(), ...pendingThreadHydrations.keys(), ...pinnedMutationRefs]);
  const watchRefs = targetRef
    ? new Set([targetRef])
    : new Set([...threadsStore.getState().watchedThreads.keys(), ...pendingWatchedHydrations.keys()]);
  await Promise.all([
    ...Array.from(refs, (ref) => refreshTrackedThread(client, epoch, ref, targetedResync)),
    ...Array.from(watchRefs, (ref) => refreshWatchedThread(client, epoch, ref, targetedResync)),
  ]);

  if (!runtime || wiredClient !== client || readyEpoch !== epoch || client.state !== "ready") return;
  if (!targetedResync) {
    const alreadyHydrated = new Set(refs);
    const discovered = await discoveredPinnedRefs;
    // A pin is a fact about storage, so record it whatever generation we are
    // in. Rejoining is a fact about a connection: this scan is a real
    // IndexedDB read and a reconnect can land inside it, so re-check before
    // dispatching rather than letting a dead generation put reads on the wire
    // and relying on the publish gate to throw their snapshots away.
    for (const ref of discovered) pinnedMutationRefs.add(ref);
    if (wiredClient !== client || readyEpoch !== epoch || client.state !== "ready") return;
    await Promise.all(
      discovered.filter((ref) => !alreadyHydrated.has(ref)).map((ref) => handleReady(client, epoch, ref)),
    );
    if (wiredClient !== client || readyEpoch !== epoch || client.state !== "ready") return;
  }
  if (!targetedResync) {
    dispatchReadyClient = client;
    dispatchReadyEpoch = epoch;
    await runtime.outbox.connectionReady();
  } else if (targetRef && dispatchableMutationRefs.has(targetRef)) {
    scheduleMutationDispatch(runtime, [targetRef]);
  }
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
  readyEpoch += 1;
  retireAllOwnedHydrations();
  dispatchReadyClient = null;
  dispatchReadyEpoch = -1;
  dispatchableMutationRefs.clear();
  resolveClientReadyOrRewired?.();
  resolveClientReadyOrRewired = null;
  unwireNotification?.();
  unwireReady?.();
  wiredClient = client;
  if (client.state === "ready") {
    clientReadyOrRewired = Promise.resolve();
  } else {
    clientReadyOrRewired = new Promise<void>((resolve) => {
      resolveClientReadyOrRewired = resolve;
    });
  }
  unwireNotification = client.onNotification(handleNotification);
  unwireReady = client.onReady(() => {
    readyEpoch += 1;
    retireAllOwnedHydrations();
    dispatchReadyClient = null;
    dispatchReadyEpoch = -1;
    dispatchableMutationRefs.clear();
    resolveClientReadyOrRewired?.();
    resolveClientReadyOrRewired = null;
    void handleReady(client, readyEpoch);
  });
  // onReady only fires on a FUTURE transition into "ready" (AppwireClient/
  // FakeClient both dispatch it from within setState/emitStateChange) — it
  // does NOT fire retroactively for a client that is already ready by the
  // time we subscribe. A manual retry's fresh client is typically already
  // ready at this point (ConnectionBanner awaits the new client's own
  // connect() before ever handing it to connectionStore.connect()), so
  // without this, swapping to an already-ready client would never
  // re-subscribe/re-hydrate this store's tracked refs at all.
  if (client.state === "ready") void handleReady(client, readyEpoch);
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

  async ensureThread(ref) {
    let client = requireClient();
    const count = refCounts.get(ref) ?? 0;
    if (count === 0) {
      ensureGenerations.set(ref, (ensureGenerations.get(ref) ?? 0) + 1);
    }
    const generation = ensureGenerations.get(ref) ?? 0;
    refCounts.set(ref, count + 1);
    if (threadsStore.getState().threads.has(ref)) return; // already hydrated: no re-read

    const startHydration = (hydrationClient: AppwireClientLike): Promise<ThreadModel | null> => {
      const hydrationEpoch = readyEpoch;
      const pending = beginThreadHydration(
        ref,
        hydrationClient,
        threadsStore.getState().threads.get(ref),
        hydrationEpoch,
      );
      const hydration = hydrateAndSubscribe(hydrationClient, ref, Date.now(), pending)
        .then((result) => publishAndReconcileThreadHydration(ref, pending, result))
        .finally(() => {
          if (pendingThreadHydrations.get(ref) === pending) pendingThreadHydrations.delete(ref);
        });
      inflightHydrates.set(ref, hydration);
      inflightHydrateClients.set(ref, hydrationClient);
      inflightHydrateEpochs.set(ref, hydrationEpoch);
      // .finally() re-throws inflight's own rejection on ITS OWN returned
      // promise — a separate object from `inflight` — so without a catch
      // here a failed hydrate becomes an unhandled rejection on top of the
      // one every caller already observes via `await inflight` below.
      void hydration
        .finally(() => {
          if (inflightHydrates.get(ref) === hydration) {
            inflightHydrates.delete(ref);
            inflightHydrateClients.delete(ref);
            inflightHydrateEpochs.delete(ref);
          }
        })
        .catch(() => {});
      return hydration;
    };

    let inflight = inflightHydrates.get(ref);
    if (!inflight) inflight = startHydration(client);
    try {
      for (;;) {
        const inflightClient = inflightHydrateClients.get(ref) ?? client;
        const inflightEpoch = inflightHydrateEpochs.get(ref) ?? readyEpoch;
        try {
          const model = await inflight;
          if (model) return;
        } catch (err) {
          const replacement = inflightHydrates.get(ref);
          const lifecycleActive = ensureGenerations.get(ref) === generation && (refCounts.get(ref) ?? 0) > 0;
          if (lifecycleActive && replacement && replacement !== inflight) {
            inflight = replacement;
            continue;
          }
          if (lifecycleActive && threadsStore.getState().threads.has(ref)) return;
          if (wiredClient !== inflightClient || readyEpoch !== inflightEpoch) {
            if (threadsStore.getState().threads.has(ref)) return;
            client = requireClient();
            // Waiting for readiness can span a further swap, and startHydration
            // stamps the hydration with the epoch it reads at capture time. A
            // client captured before this wait would therefore be labelled with
            // the live generation while pointing at a superseded connection, so
            // read it again on the way out. Same shape as the re-arm below and
            // as both of watchThread's.
            if (client.state !== "ready") {
              await clientReadyOrRewired;
              client = requireClient();
            }
            inflight = inflightHydrates.get(ref) ?? startHydration(client);
            continue;
          }
          // Release is terminal for this owner generation: this call's claim is
          // already gone, so there is nothing left to retry or to report.
          if (!lifecycleActive) return;
          // Same client, same ready epoch: the read failed in transport, not
          // because this pane lost the ref. The failed attempt owns one
          // scheduled retry for this owner generation, and every concurrent
          // owner waits on that one lifecycle rather than reading again here.
          const owned = ownedThreadHydrations.get(ref);
          if (owned?.generation !== generation) throw err;
          await owned.firstHydration;
          // Fall through to the shared re-arm below: it returns when the claim
          // is gone or a model published, and otherwise rejoins the newest
          // attempt on the current client.
        }

        if ((refCounts.get(ref) ?? 0) <= 0) return;
        if (threadsStore.getState().threads.has(ref)) return;

        client = requireClient();
        if (client.state !== "ready") {
          await clientReadyOrRewired;
          client = requireClient();
        }
        inflight = inflightHydrates.get(ref);
        if (!inflight) inflight = startHydration(client);
      }
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
      if (ensureGenerations.get(ref) === generation && (refCounts.get(ref) ?? 0) > 0) {
        threadsStore.getState().releaseThread(ref);
      }
      throw err;
    }
  },

  releaseThread(ref) {
    const count = refCounts.get(ref) ?? 0;
    if (count <= 0) return; // never tracked, or already released
    if (count > 1) {
      refCounts.set(ref, count - 1);
      return;
    }
    refCounts.delete(ref);
    if (pinnedMutationRefs.has(ref)) return;
    // Release is terminal for this owner generation: cancel its scheduled
    // retry and wake anything still awaiting its first model.
    retireOwnedHydration("thread", ref);
    // A pending read belongs to this released pane lifecycle. Retire it
    // before a new ensureThread(ref) can claim the same ref; the old promise's
    // identity-guarded finally blocks must not remove a newer hydration.
    inflightHydrates.delete(ref);
    inflightHydrateClients.delete(ref);
    inflightHydrateEpochs.delete(ref);
    pendingThreadHydrations.delete(ref);
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
    let client = requireClient();
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

    const startHydration = (hydrationClient: AppwireClientLike): Promise<ThreadModel | null> => {
      const hydrationEpoch = readyEpoch;
      const pending = beginWatchedHydration(
        ref,
        hydrationClient,
        threadsStore.getState().watchedThreads.get(ref),
        hydrationEpoch,
      );
      const hydration = hydrateAndSubscribeWatch(hydrationClient, ref, Date.now(), pending, needTurns)
        .then((model) => publishWatchedHydration(ref, pending, model, needTurns, generation))
        .finally(() => {
          if (pendingWatchedHydrations.get(ref) === pending) pendingWatchedHydrations.delete(ref);
        });
      inflightWatchHydrates.set(ref, hydration);
      inflightWatchHydrateClients.set(ref, hydrationClient);
      inflightWatchHydrateEpochs.set(ref, hydrationEpoch);
      inflightWatchIncludeTurns.set(ref, needTurns);
      void hydration
        .finally(() => {
          if (inflightWatchHydrates.get(ref) === hydration) {
            inflightWatchHydrates.delete(ref);
            inflightWatchHydrateClients.delete(ref);
            inflightWatchHydrateEpochs.delete(ref);
            inflightWatchIncludeTurns.delete(ref);
          }
        })
        .catch(() => {});
      return hydration;
    };

    let inflight = inflightWatchHydrates.get(ref);
    const inflightHasTurns = inflightWatchIncludeTurns.get(ref) ?? false;
    // A rich caller cannot share a lean request already in flight: the
    // response would be structurally missing the turns it requested. A
    // lean caller may share a rich request because the richer snapshot is
    // sufficient for both callers.
    if (!inflight || (needTurns && !inflightHasTurns)) inflight = startHydration(client);

    for (;;) {
      const inflightClient = inflightWatchHydrateClients.get(ref) ?? client;
      const inflightEpoch = inflightWatchHydrateEpochs.get(ref) ?? readyEpoch;
      try {
        const model = await inflight;
        if (model) return;
      } catch (err) {
        const replacement = inflightWatchHydrates.get(ref);
        const lifecycleActive = (watchRefCounts.get(ref) ?? 0) > 0 && (watchGenerations.get(ref) ?? 0) === generation;
        if (lifecycleActive && replacement && replacement !== inflight) {
          inflight = replacement;
          continue;
        }
        const hydrated = threadsStore.getState().watchedThreads.get(ref);
        if (lifecycleActive && hydrated && (!needTurns || (watchHydratedIncludeTurns.get(ref) ?? false))) return;
        if (wiredClient !== inflightClient || readyEpoch !== inflightEpoch) {
          if ((watchRefCounts.get(ref) ?? 0) <= 0 || (watchGenerations.get(ref) ?? 0) !== generation) return;
          if (hydrated && (!needTurns || (watchHydratedIncludeTurns.get(ref) ?? false))) return;
          client = requireClient();
          if (client.state !== "ready") {
            await clientReadyOrRewired;
            client = requireClient();
          }
          inflight = inflightWatchHydrates.get(ref) ?? startHydration(client);
          continue;
        }
        // Release is terminal for this watcher generation, same as above.
        if (!lifecycleActive) return;
        // Same client, same ready epoch: the watcher still owns this ref, so
        // its own lifecycle reads again. Same contract as ensureThread above.
        const owned = ownedWatchedHydrations.get(ref);
        if (owned?.generation !== generation) throw err;
        await owned.firstHydration;
        // Fall through to the shared re-arm below, which re-checks the
        // rich/lean requirement a published model has to satisfy.
      }

      if ((watchRefCounts.get(ref) ?? 0) <= 0 || (watchGenerations.get(ref) ?? 0) !== generation) return;
      const hydrated = threadsStore.getState().watchedThreads.get(ref);
      if (hydrated && (!needTurns || (watchHydratedIncludeTurns.get(ref) ?? false))) return;

      client = requireClient();
      if (client.state !== "ready") {
        await clientReadyOrRewired;
        client = requireClient();
      }
      inflight = inflightWatchHydrates.get(ref);
      const currentInflightHasTurns = inflightWatchIncludeTurns.get(ref) ?? false;
      if (!inflight || (needTurns && !currentInflightHasTurns)) inflight = startHydration(client);
    }
  },

  releaseWatchedThread(ref) {
    const count = watchRefCounts.get(ref) ?? 0;
    if (count <= 0) return; // never tracked, or already released
    if (count > 1) {
      watchRefCounts.set(ref, count - 1);
      return;
    }
    watchRefCounts.delete(ref);
    retireOwnedHydration("watched", ref);
    watchGenerations.set(ref, (watchGenerations.get(ref) ?? 0) + 1);
    // A retired lifecycle must not lend its pending hydrate to a new watcher.
    // The old promise may still settle, but its generation check prevents it
    // from publishing into the new lifecycle.
    inflightWatchHydrates.delete(ref);
    inflightWatchHydrateClients.delete(ref);
    inflightWatchHydrateEpochs.delete(ref);
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
    await enqueueMutationIntent(composerMutationIntent(ref, "send", text, attachments));
  },

  async steer(ref, text, attachments) {
    await enqueueMutationIntent(composerMutationIntent(ref, "steer", text, attachments));
  },

  async queue(ref, text, attachments) {
    await enqueueMutationIntent(composerMutationIntent(ref, "queue", text, attachments));
  },

  async interrupt(ref) {
    const expectedTurnId = threadsStore.getState().threads.get(ref)?.activeTurnId ?? "";
    await enqueueMutation(ref, "turn/interrupt", { ref, expectedTurnId }, { method: "turn/interrupt" });
  },

  async drainAsSteer(ref, text, attachments) {
    await enqueueMutationIntent(composerMutationIntent(ref, "drain", text, attachments));
  },

  async promoteQueuedAsSteer(ref, index, expectedEntryId) {
    const expectedTurnId = threadsStore.getState().threads.get(ref)?.activeTurnId ?? "";
    await enqueueMutation(
      ref,
      "turn/promoteQueuedAsSteer",
      { ref, index, expectedTurnId, expectedEntryId },
      { method: "turn/promoteQueuedAsSteer", index, expectedEntryId },
    );
  },

  async cancelQueued(ref, index, expectedEntryId) {
    await enqueueMutation(
      ref,
      "turn/cancelQueued",
      { ref, index, expectedEntryId },
      { method: "turn/cancelQueued", index, expectedEntryId },
    );
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

  async listJobs(ref) {
    const client = requireClient();
    // No mapConflict here either, same reasoning as listModels/listTasks above.
    const resp = await client.request("serf/jobs/list", { ref });
    return resp.data;
  },

  async jobOutput(ref, jobId) {
    const client = requireClient();
    const resp = await client.request("serf/jobs/output", { ref, jobId });
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
  if (mutationRuntime) {
    mutationRuntime.active = false;
    void mutationRuntime.outbox.stop();
    mutationRuntime.storage.close();
    mutationRuntime = null;
  }
  mutationStorageForTests = null;
  createMutationBroadcastChannelForTests = () => {
    const channel = new EventTarget();
    return Object.assign(channel, {
      postMessage() {},
      close() {},
    });
  };
  retireAllOwnedHydrations();
  pinnedMutationRefs.clear();
  dispatchableMutationRefs.clear();
  dispatchReadyClient = null;
  dispatchReadyEpoch = -1;
  refCounts.clear();
  ensureGenerations.clear();
  inflightHydrates.clear();
  inflightHydrateClients.clear();
  inflightHydrateEpochs.clear();
  pendingThreadHydrations.clear();
  watchRefCounts.clear();
  inflightWatchHydrates.clear();
  inflightWatchHydrateClients.clear();
  inflightWatchHydrateEpochs.clear();
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
  resolveClientReadyOrRewired?.();
  resolveClientReadyOrRewired = null;
  clientReadyOrRewired = Promise.resolve();
  wiredClient = null;
  readyEpoch = 0;
  threadsStore.setState({
    threads: new Map(),
    frameTimes: new Map(),
    watchedThreads: new Map(),
    watchedFrameTimes: new Map(),
  });
}
