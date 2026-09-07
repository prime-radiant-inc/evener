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
import { releaseSubagentRows } from "../panes/session/transcript/tools/subagentModuleStore";
import { ClientNotReadyError, isStaleCursorError, mutationErrorData, WireError } from "../protocol/errors";
import type { ThreadModel } from "../protocol/model";
import {
  applyNotification,
  collectAuthoritativeMutationIds,
  hydrateThread,
  mergeOlderItemPage,
  notificationRoutingKey,
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
import { resetActivityPanelStoreForTests } from "./activityPanel";
import { resetActivitySummaryStoreForTests } from "./activitySummary";
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
import { resetTasksPanelStoreForTests } from "./tasksPanel";

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
//
// `marker` is the one field here that never reaches the wire: it is the
// composer marker number this attachment was staged under, carried so that
// every consumer downstream - the submit boundary's marker translation, the
// durable outbox record, the recovery draft that rebuilds a composer - pairs
// text and attachment by identity instead of re-deriving the pairing from
// array position. buildInput drops it when it assembles the wire input.
export interface InputAttachment {
  marker: number;
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
  mutationWriteStalled: boolean;
  mutationReconciliationFailures: ReadonlySet<string>;
  restartBlockingObligations: ReadonlyMap<string, symbol>;
  // Per-ref ring of live-notification arrival timestamps, for
  // widgets/cadence's Cadence trace - see appendFrameTime below. Deliberately
  // NOT part of ThreadModel/the reducer: it is display-liveness bookkeeping
  // the store layers on top, not wire-derived thread state, and (unlike
  // `threads`) it only grows from notifications actually applied live - a
  // hydrate/re-hydrate never seeds or resets it (see handleReady/rewireClient
  // below, which touch `threads` but not this map).
  frameTimes: Map<string, number[]>;
  // Per-ref count of full-snapshot publishes (initial hydration and every
  // rehydration - reconnect handleReady, targeted resync). A consumer that
  // retains derived state (ActivityPanel's freshness effect) watches this to
  // notice a WHOLESALE model replacement whose visible fields happen not to
  // change - e.g. jobsUpdatedAt is null on both sides of a resync, yet
  // activity retained across the gap may be stale. Like frameTimes, this is
  // store bookkeeping, not wire-derived thread state.
  hydrations: Map<string, number>;
  // Lean child subscriptions are independent from real pane ownership.
  watchedThreads: Map<string, ThreadModel>;
  // Refs whose hydration has been durably rejected by the hub's own
  // deletion fence (data.mutationOutcome === "targetDeleted" - see
  // hydrateAndSubscribe's catch below and cmd/evener-hub/app_sources.go's
  // deletionFenceError). That fence never clears once set, so once a ref
  // lands here it stays - unlike `threads`, this is not cleared on a
  // re-hydration attempt, because a deleted ref never gets one that
  // succeeds. A consumer (Session.tsx) reads this to tell "still loading" a
  // ref apart from "gone", which ensureThread's own retry-forever contract
  // (scheduleOwnedHydrationRetry has no terminal state - a rejection here is
  // always presumed transport, never a proof the ref itself is gone) cannot
  // otherwise distinguish: its returned promise never settles for a deleted
  // ref, since nothing about that loop treats this error as different from
  // an ordinary transient one.
  deletedRefs: Set<string>;
  ensureThread(ref: string): Promise<void>;
  refreshThread(ref: string): Promise<void>;
  releaseThread(ref: string): void;
  // Additive, leaner subscription to a child thread for a delegate card's
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
  setVisionModel(ref: string, visionModel: string): Promise<void>;
  // Sets or clears the session's /goal objective (an empty objective
  // clears it). Returns whether the goal loop started immediately (false
  // when cleared, or when a turn is already running and the goal picks up
  // after it). A successful response commits the known goal state locally;
  // the structured goal update push keeps every other client synchronized.
  setGoal(ref: string, objective: string): Promise<GoalSetResponse>;
  rename(ref: string, name: string): Promise<void>;
  compact(ref: string): Promise<void>;
  // Clears the thread's conversation through the durable mutation outbox. The
  // daemon's response carries the replacement snapshot; the dispatcher hands
  // it to applyClearResponse before settling the intent so both real and lean
  // views switch to the new instance together.
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
  // Lists the session's tasks (evener/tasks/list). TaskListResponse.Data is
  // `any` on the wire catalog (appwire/types.go:896-898) - this returns
  // that raw field verbatim, never wrapped, so the store stays shape-
  // agnostic; the caller owns interpreting it (the chrome stream's own
  // parseTaskListData). A source that omits the capability rejects this call
  // (appwire.Unavailable, "actionUnavailable") - that typed error
  // propagates unchanged, same as every other read-only action here; the
  // caller renders the empty/unsupported state for it.
  listTasks(ref: string): Promise<unknown>;
  // Lists the session's activity tree (evener/jobs/list) and fetches one job's
  // output tail (evener/jobs/output). Both Data fields are `any` on the wire
  // catalog (appwire/types.go) - these return the raw field verbatim, never
  // wrapped, so the store stays shape-agnostic; the caller owns interpreting
  // them (the chrome stream's parseActivityTree / parseJobOutputData).
  listJobs(ref: string, continuation?: string): Promise<unknown>;
  // beforeBytes > 0 pages backwards: the window ending at that lifetime
  // output offset instead of the tail (appwire.JobsOutputParams.BeforeBytes).
  // maxBytes > 0 bounds the window (appwire.JobsOutputParams.MaxBytes) - the
  // activity strip's preview uses it to fetch a couple hundred bytes instead
  // of the daemon's default tail.
  jobOutput(ref: string, jobId: string, beforeBytes?: number, maxBytes?: number): Promise<unknown>;
  // Answers one evener/sandbox/escalation/requested via evener/sandbox/
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
// A generation changes at every local goal request and every accepted goal
// authority (a matching notification or full hydration). A request response may
// publish its derived local state only while its generation is still current, so
// neither a later request nor accepted authoritative state that arrived during
// the await can be overwritten by that delayed response. This is independent of
// producer age: an older producer that sends no goal notification leaves the
// request generation current and keeps the existing immediate local commit
// behavior.
const goalUpdateGenerations = new Map<string, number>();

function invalidateGoalResponseFallback(ref: string): void {
  goalUpdateGenerations.set(ref, (goalUpdateGenerations.get(ref) ?? 0) + 1);
}
const inflightHydrates = new Map<string, Promise<ThreadModel | null>>();
const inflightHydrateClients = new Map<string, AppwireClientLike>();
const inflightHydrateEpochs = new Map<string, number>();
const trackedHydrationCompletions = new Map<string, Promise<void>>();
// The identity a pending hydration accepts frames for. Both facts come from an
// authority, never from the stream: the routing is seeded from the published
// model when the read starts and re-seeded from the authoritative snapshot at
// the response cut, which is what lets a ref-less (threadId-only) frame after
// the cut be judged against the thread the snapshot actually named.
//
// threadId is therefore absent only before the cut of a hydration that had no
// published model to seed from - and in exactly that window there is no model
// at this ref for a ref-less frame to reach, and the cut discards whatever the
// buffer took anyway (pinned by "frames before the response cut leave no
// trace" in threads.test.ts). Learning an id from a buffered frame there is
// unobservable, which is why nothing does (kata j4b0).
type PendingHydrationRouting = {
  ref: string;
  threadId?: string;
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
const pendingMutationReconciliations = new Map<string, Promise<void>>();
const pendingWatchedHydrations = new Map<string, PendingThreadHydration>();

// --- Notification routing index ---------------------------------------------
//
// handleNotification fires for EVERY notification on the socket. During a
// streaming turn that is dozens of delta frames per second, and its old shape
// ran notificationTargetsThread over EVERY entry of threads/watchedThreads for
// EVERY frame — O(tracked threads) per token. notificationRoutingKey
// (protocol/reducer.ts) is the single source of the routing precedence: a
// frame targets models by its own params.ref first, else by its
// params.threadId, else nothing. The models a frame can target are therefore
// fully determined by the frame's own keys, and only threadId needs an index:
// the ref route is the map itself (model.ref === its map key — every model
// enters a map through hydrateThread(resp, ref, ...), see the put/remove
// helpers below, which are the only membership paths).
//
// byThreadId: threadId -> Set<ref>  (several models may share a threadId: the
// same thread watched lean in watchedThreads while pane-owned in threads, or
// distinct refs the daemon maps to one id). A threadId-routed frame resolves
// each ref back through the map at route time, so it always folds onto the
// live model — a stale model object cannot linger, only a stale ref could,
// and one ref per slot is exactly what the map's own key invariant already
// guarantees.
//
// Index stability — why model identity changes never desynchronize it:
// applyNotification, prependOlderTurns and resolvePendingEscalation all build
// their results with `...model`, and hydrateThread is the ONLY function that
// ever sets a model's ref/threadId (protocol/reducer.ts). A model's routing
// keys are therefore stable for its lifetime in a map, so the index only
// needs maintenance on membership changes (add/replace/remove) — a reducer
// fold that produces a new model object under the same keys costs ZERO index
// work. The put helpers' ref invariant is the loud failure mode: a future
// code path that somehow violated it throws there rather than silently
// mis-routing.
type ThreadModelIndex = Map<string, Set<string>>;

function newThreadModelIndex(): ThreadModelIndex {
  return new Map();
}

// Record ref under model.threadId. `previous` is the model being replaced in
// the same map slot (or undefined for a pure add); a replace whose threadId
// moved is handled exactly — both memberships are updated.
function putThreadModelIndex(index: ThreadModelIndex, previous: ThreadModel | undefined, model: ThreadModel): void {
  if (previous && previous.threadId !== model.threadId) removeThreadModelIndex(index, previous);
  let refs = index.get(model.threadId);
  if (!refs) {
    refs = new Set();
    index.set(model.threadId, refs);
  }
  refs.add(model.ref);
}

function removeThreadModelIndex(index: ThreadModelIndex, model: ThreadModel): void {
  const refs = index.get(model.threadId);
  if (!refs) return;
  refs.delete(model.ref);
  if (refs.size === 0) index.delete(model.threadId);
}

// routeByNotificationKey selects the models a frame targets: the single
// model for a ref-routed frame (no wrapper array), the model list for a
// threadId-routed frame, or null when the frame routes nowhere. Routing
// equivalence with the pre-index scan lives on applyToMap — one-line version:
// ref route = map.get(ref), threadId route = byThreadId.get(threadId)
// resolved through the map. `skippedRefs` mirrors the scan's own exclusion
// set (a pending hydration owns the ref for this frame).
function routeByNotificationKey(
  map: Map<string, ThreadModel>,
  index: ThreadModelIndex,
  n: AnyNotification,
  skippedRefs: ReadonlySet<string> | undefined,
): ThreadModel | ThreadModel[] | null {
  const key = notificationRoutingKey(n);
  if (!key) return null;
  if ("ref" in key) {
    const model = map.get(key.ref);
    // model.ref === map key is the store's invariant (see the put helpers);
    // the check keeps this route exactly equivalent to the scan even for a
    // model that somehow violates it, instead of folding onto it.
    if (!model || model.ref !== key.ref) return null;
    return skippedRefs?.has(model.ref) ? null : model;
  }
  const refs = index.get(key.threadId);
  if (!refs) return null;
  const candidates: ThreadModel[] = [];
  for (const ref of refs) {
    if (skippedRefs?.has(ref)) continue;
    const model = map.get(ref);
    if (model) candidates.push(model);
  }
  return candidates.length > 0 ? candidates : null;
}

const threadsIndex = newThreadModelIndex();
const watchedThreadsIndex = newThreadModelIndex();

// Shared empty pending-ref set: handleNotification's steady state (no
// hydration in flight) allocates nothing per frame.
const EMPTY_PENDING_REFS: ReadonlySet<string> = new Set();

// The membership maintenance surface for threads/watchedThreads — the ONLY
// places a model enters or leaves either map, so no future mutation site can
// forget its index line. Each computes the next map at the call site and
// passes a plain patch to setState (matching the release paths' shape): a
// zustand updater must stay a pure compute-next-state function, not a home
// for module-level side effects an updater rerun would replay.
//
// putThreadModel is exported for the dev harness seeders
// (dev/surface-sections/composer.tsx, dev/overflowharness-entry.tsx), which
// seed fixture panes exactly the way production hydration publishes real
// ones — through the same membership path, index maintenance included, so
// dev-seeded models are routable by ref AND threadId like any other.
// assertModelRefMatchesKey guards BOTH membership paths — pure add and
// replace. The replace path used to be the only one that threw, but a pure
// add filed under a key its own ref contradicts breaks the same map key ===
// model.ref invariant every ref-routed frame's map.get (and the index) leans
// on, so it throws for the same reason: loudly, before it can mis-route.
function assertModelRefMatchesKey(
  ref: string,
  model: ThreadModel,
  watched: boolean,
  previous: ThreadModel | undefined,
): void {
  if (model.ref === ref) return;
  const mapName = watched ? "watched " : "";
  if (!previous) {
    throw new Error(
      `threads store: added ${mapName}model ref disagrees with map key (${ref} != ${model.ref}) — map key and model.ref must agree`,
    );
  }
  throw new Error(
    `threads store: replaced ${mapName}model ref moved (${previous.ref} -> ${model.ref}) — map key and model.ref must agree`,
  );
}

export function putThreadModel(ref: string, model: ThreadModel): void {
  putThreadModels(ref, model, undefined);
}

function putWatchedThreadModel(ref: string, model: ThreadModel): void {
  putThreadModels(ref, undefined, model);
}

// putThreadModels is the dual-map variant the combined actions use: the SAME
// model (or its two per-map resolutions) lands in threads and watchedThreads
// through ONE setState, so a synchronous subscriber between the two halves
// of the update — the split the sequential putThreadModel +
// putWatchedThreadModel pair introduced — cannot observe threads updated
// while watchedThreads still holds the stale model. `threadModel`/
// `watchedModel` are the models to file (the caller computes them first:
// hydrateThread for clearThread, resolvePendingEscalation for
// resolveEscalation); pass undefined for either to leave that map untouched,
// matching the old single-setState patch shape exactly. Both routing indexes
// are maintained in the same step, and both put helpers' ref invariant is
// re-checked here (the same assertModelRefMatchesKey) rather than trusted.
function putThreadModels(
  ref: string,
  threadModel: ThreadModel | undefined,
  watchedModel: ThreadModel | undefined,
): void {
  const state = threadsStore.getState();
  const previousThread = state.threads.get(ref);
  const previousWatched = state.watchedThreads.get(ref);
  if (threadModel) assertModelRefMatchesKey(ref, threadModel, false, previousThread);
  if (watchedModel) assertModelRefMatchesKey(ref, watchedModel, true, previousWatched);

  const patch: Partial<ThreadsStoreState> = {};
  if (threadModel) {
    putThreadModelIndex(threadsIndex, previousThread, threadModel);
    patch.threads = new Map(state.threads).set(ref, threadModel);
  }
  if (watchedModel) {
    putThreadModelIndex(watchedThreadsIndex, previousWatched, watchedModel);
    patch.watchedThreads = new Map(state.watchedThreads).set(ref, watchedModel);
  }
  if (!threadModel && !watchedModel) return;
  threadsStore.setState(patch);
}

function removeThreadModel(ref: string): void {
  const removed = threadsStore.getState().threads.get(ref);
  if (removed) removeThreadModelIndex(threadsIndex, removed);
  threadsStore.setState((s) => {
    if (!s.threads.has(ref) && !s.frameTimes.has(ref) && !s.deletedRefs.has(ref)) return s;
    const nextThreads = new Map(s.threads);
    nextThreads.delete(ref);
    const nextFrameTimes = new Map(s.frameTimes);
    nextFrameTimes.delete(ref);
    const nextDeletedRefs = new Set(s.deletedRefs);
    nextDeletedRefs.delete(ref);
    return { threads: nextThreads, frameTimes: nextFrameTimes, deletedRefs: nextDeletedRefs };
  });
}

function removeWatchedThreadModel(ref: string): void {
  const removed = threadsStore.getState().watchedThreads.get(ref);
  if (removed) removeThreadModelIndex(watchedThreadsIndex, removed);
  threadsStore.setState((s) => {
    if (!s.watchedThreads.has(ref)) return s;
    const nextWatchedThreads = new Map(s.watchedThreads);
    nextWatchedThreads.delete(ref);
    return { watchedThreads: nextWatchedThreads };
  });
}

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
let dispatchReadyClient: AppwireClientLike | null = null;
let dispatchReadyEpoch = -1;
const pinnedMutationRefs = new Set<string>();
const dispatchableMutationRefs = new Set<string>();
const olderPageGenerations = new Map<string, number>();

// Refs this connection generation holds a wire subscription for. thread/read
// with subscribe:true is how a subscription is created, and every re-read of
// a tracked ref (ensureThread retry, onReady resync, watchThread upgrade)
// used to send it again — additively and with a fresh capture cycle, because
// nothing recorded "already subscribed on THIS socket". A new connection
// carries no subscriptions, so rewireClient and the onReady path both clear
// the set; the next read of a still-tracked ref re-subscribes as before.
const wireSubscribedRefs = new Set<string>();

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
interface MutationCommit {
  record: MutationOutboxRecord;
  recoveryId?: string;
}

type MutationPersistenceListener = (targetRefs: string[], committed?: MutationCommit) => void;
const mutationPersistenceListeners = new Set<MutationPersistenceListener>();

function isCurrentMutationRuntime(runtime: MutationRuntime | null): runtime is MutationRuntime {
  return runtime?.active === true && mutationRuntime === runtime;
}

function notifyMutationPersistence(targetRefs: Iterable<string>, committed?: MutationCommit): void {
  const refs = [...new Set(targetRefs)];
  for (const listener of mutationPersistenceListeners) {
    try {
      listener(refs, committed);
    } catch (error) {
      // A projection listener cannot change the result of a durable write.
      console.error("Mutation persistence listener failed", error);
    }
  }
}

function applyClearResponse(targetRef: string, response: ThreadClearResponse): void {
  const now = Date.now();
  const model = hydrateThread({ thread: response.thread }, targetRef, now);
  // A clear response is a newer authoritative cut than any thread/read that
  // was already in flight for this ref. Retire those reads before publishing
  // the replacement so a late pre-clear snapshot cannot overwrite it.
  pendingThreadHydrations.delete(targetRef);
  pendingWatchedHydrations.delete(targetRef);
  // One setState for both maps (putThreadModels), so a synchronous subscriber
  // never sees threads cleared while watchedThreads still holds the old turns;
  // both routing indexes are maintained in the same step.
  const stateBefore = threadsStore.getState();
  putThreadModels(
    targetRef,
    stateBefore.threads.has(targetRef) ? model : undefined,
    stateBefore.watchedThreads.has(targetRef) ? model : undefined,
  );
  if (stateBefore.threads.has(targetRef)) {
    threadsStore.setState((state) => ({
      hydrations: new Map(state.hydrations).set(targetRef, (state.hydrations.get(targetRef) ?? 0) + 1),
    }));
  }
}

function currentDispatchClient(targetRef?: string): AppwireClientLike | null {
  if (wiredClient !== dispatchReadyClient || readyEpoch !== dispatchReadyEpoch) return null;
  if (targetRef && !dispatchableMutationRefs.has(targetRef)) return null;
  if (
    targetRef &&
    (pendingMutationReconciliations.has(targetRef) ||
      threadsStore.getState().restartBlockingObligations.has(targetRef) ||
      threadsStore.getState().mutationReconciliationFailures.has(targetRef))
  )
    return null;
  if (targetRef && threadsStore.getState().threads.get(targetRef)?.status.type === "restartRequired") return null;
  return wiredClient?.state === "ready" ? wiredClient : null;
}

function dropUnpinnedModel(ref: string): void {
  if (pinnedMutationRefs.has(ref) || (refCounts.get(ref) ?? 0) > 0) return;
  // Nothing owns this ref any more, so no scheduled retry may outlive it.
  retireOwnedHydration("thread", ref);
  // The model is leaving `threads` here, so its index membership leaves with
  // it (see putThreadModel/removeThreadModel — the membership paths).
  const dropped = threadsStore.getState().threads.get(ref);
  if (dropped) removeThreadModelIndex(threadsIndex, dropped);
  threadsStore.setState((state) => {
    if (!state.threads.has(ref) && !state.frameTimes.has(ref) && !state.hydrations.has(ref)) return state;
    const threads = new Map(state.threads);
    threads.delete(ref);
    const frameTimes = new Map(state.frameTimes);
    frameTimes.delete(ref);
    const hydrations = new Map(state.hydrations);
    hydrations.delete(ref);
    return { threads, frameTimes, hydrations };
  });
}

async function refreshMutationPins(runtime: MutationRuntime, targetRefs: Iterable<string>): Promise<void> {
  if (!isCurrentMutationRuntime(runtime)) return;
  for (const targetRef of targetRefs) {
    if (!isCurrentMutationRuntime(runtime)) return;
    const [outbox, optimistic] = await Promise.all([
      runtime.storage.listOutbox(targetRef),
      runtime.storage.listOptimistic(targetRef),
    ]);
    if (!isCurrentMutationRuntime(runtime)) return;
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
  if (!isCurrentMutationRuntime(runtime)) return;
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
  if (!isCurrentMutationRuntime(runtime)) return;
  const refs = [...new Set([...targetRefs, ...threadsStore.getState().mutationReconciliationFailures])];
  for (const targetRef of refs) pinnedMutationRefs.add(targetRef);
  notifyMutationPersistence(refs);
  scheduleMutationDispatch(runtime, refs);

  const client = currentDispatchClient();
  if (!client) return;
  const epoch = dispatchReadyEpoch;
  for (const targetRef of refs) {
    if (pendingMutationReconciliations.has(targetRef)) continue;
    if (
      dispatchableMutationRefs.has(targetRef) &&
      !threadsStore.getState().mutationReconciliationFailures.has(targetRef)
    )
      continue;
    const pending = pendingThreadHydrations.get(targetRef);
    if (pending?.client === client && pending.epoch === epoch) continue;
    void handleReady(client, epoch, targetRef);
  }
}

function getMutationRuntime(): MutationRuntime | null {
  if (mutationRuntime) return mutationRuntime;
  if (!globalThis.indexedDB) return null;

  let runtime: MutationRuntime | null = null;
  const storage =
    mutationStorageForTests ??
    new MutationOutboxIndexedDB({
      onWriteStalled: (waiting) => {
        if (isCurrentMutationRuntime(runtime)) threadsStore.setState({ mutationWriteStalled: waiting });
      },
    });
  const dispatcher = new MutationDispatcher(storage, {
    getClient: (targetRef) => (isCurrentMutationRuntime(runtime) ? currentDispatchClient(targetRef) : null),
    onStorageChange: (targetRefs) => {
      if (isCurrentMutationRuntime(runtime)) notifyMutationPersistence(targetRefs);
    },
    onClearResponse: applyClearResponse,
  });
  const outbox = new MutationOutbox(storage, {
    isReady: () => isCurrentMutationRuntime(runtime) && currentDispatchClient() !== null,
    onDiscover: (targetRefs) => {
      if (runtime) handleDiscoveredMutations(runtime, targetRefs);
    },
    createBroadcastChannel: createMutationBroadcastChannelForTests,
  });
  const initializedRuntime: MutationRuntime = {
    storage,
    dispatcher,
    outbox,
    start: Promise.resolve(),
    active: true,
  };
  runtime = initializedRuntime;
  mutationRuntime = initializedRuntime;
  initializedRuntime.start = outbox.start();
  return initializedRuntime;
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

export function subscribeMutationPersistence(listener: MutationPersistenceListener): () => void {
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
  const status = threadsStore.getState().threads.get(record.targetRef)?.status.type;
  if (!status || status === "restartRequired" || status === "notLoaded") return false;
  if (
    pendingMutationReconciliations.has(record.targetRef) ||
    threadsStore.getState().restartBlockingObligations.has(record.targetRef) ||
    threadsStore.getState().mutationReconciliationFailures.has(record.targetRef)
  )
    return false;
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
    text,
  );
  if (!record) return false;
  notifyMutationPersistence([targetRef]);
  return true;
}

export async function discardRecoveryMutation(
  clientMutationId: string,
  targetRef: string,
  shouldDiscard?: () => boolean,
): Promise<boolean> {
  const runtime = requireMutationRuntime();
  await runtime.start;
  const discarded = await runtime.storage.discardRecovery(clientMutationId, shouldDiscard);
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
  notifyMutationPersistence([targetRef], { record, recoveryId: clientMutationId });
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
// and the call that owns inflightModelsList clears it in a `finally` so a
// failed call never poisons the next one with a repeated rejection — only
// while the slot still holds its own request, because evener/auth/updated
// drops the slot and a newer call may have claimed it since.
let modelsCache: ModelListResponse | null = null;
// modelsEpoch advances on every evener/auth/updated: a credential change can
// make models discoverable (a stored Vertex credential JSON enables the
// publisher-model listing) or take them away, so a listing cached before it
// is stale, and a listing still in flight answers the old credentials and
// must not become the cache either.
let modelsEpoch = 0;
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

// Both hydrate paths (open-pane and watched) read a ref with exactly these
// params, differing only in includeTurns: replaceSubscription is always
// false — additive, layering onto whatever the daemon already tracks for this
// client rather than resetting it.
//
// subscribe is true only when this connection generation holds no wire
// subscription for the ref yet (see wireSubscribeDecision): a re-read of an
// already-subscribed ref sends subscribe:false so the server skips the
// buffered-capture cycle a second subscribe would run, and
// releaseThread's unsubscribe is what drops the entry again.
const TRANSCRIPT_ITEM_PAGE_SIZE = 40;

function threadReadParams(ref: string, includeTurns: boolean, subscribe: boolean) {
  return {
    ref,
    includeTurns,
    itemsView: "full",
    subscribe,
    replaceSubscription: false,
    itemLimit: TRANSCRIPT_ITEM_PAGE_SIZE,
  } as const;
}

interface ThreadHydration {
  model: ThreadModel;
  response: ThreadReadResponse;
}

// sendThreadUnsubscribe drops this client's wire subscription to a ref the
// last holder of just released. Fire-and-forget on purpose: the local release
// is already complete and cannot be rolled back, so a failed or racing
// unsubscribe must not block navigation — the hub's idle-relay teardown and
// the server's connection-close cleanup (RemoveConnection) are both
// idempotent backstops for a lost message.
function sendThreadUnsubscribe(ref: string): void {
  const client = wiredClient;
  if (client?.state !== "ready") return;
  void client.request("thread/unsubscribe", { ref }).catch(() => {
    // Swallow: see above. A dropped unsubscribe costs only a kept server-side
    // subscription until the connection or the relay's idle timer ends it.
  });
}

// The shared subscribe decision for both hydrate paths (open-pane and
// watched): a read subscribes only when this connection generation holds no
// wire subscription for the ref yet, and marks it held only after the read
// succeeds — a failed read's subscribe never took effect server-side, so its
// retry must send subscribe:true again.
//
// The membership set is NOT derivable from refCounts/watchRefCounts: those
// count local interest (incremented synchronously, before any wire call),
// while this records a fact about the wire (a subscribe that completed).
// A count>0 with no held entry is exactly the pending-hydration and
// failed-read-retry window, and deriving subscribe:false there would strand
// the ref unsubscribed.
//
// markSubscribed re-checks holders after the read resolves: a release that
// ran mid-flight left no holder, and that release saw the set WITHOUT this
// ref (so it sent no unsubscribe). Recording the entry now would leak the
// server-side subscription this read just created until connection close —
// so the zero-holder read sends its own unsubscribe instead. A pinned
// outbox ref is the deliberate exception: it holds no pane but must keep
// its subscription for the mutation replay.
function wireSubscribeDecision(ref: string): { subscribe: boolean; markSubscribed: () => void } {
  const subscribe = !wireSubscribedRefs.has(ref);
  return {
    subscribe,
    markSubscribed: () => {
      if (!subscribe) return;
      if ((refCounts.get(ref) ?? 0) <= 0 && (watchRefCounts.get(ref) ?? 0) <= 0 && !pinnedMutationRefs.has(ref)) {
        sendThreadUnsubscribe(ref);
        return;
      }
      wireSubscribedRefs.add(ref);
    },
  };
}

async function hydrateAndSubscribe(
  client: AppwireClientLike,
  ref: string,
  now: number,
  pending: PendingThreadHydration,
): Promise<ThreadHydration> {
  let response: ThreadReadResponse;
  const { subscribe, markSubscribed } = wireSubscribeDecision(ref);
  try {
    response = await client.request("thread/read", threadReadParams(ref, true, subscribe));
  } catch (err) {
    // thread/read is answered from the daemon's in-memory snapshot, so a
    // rejection here is a transport failure, not a slow file read and not a
    // lost claim. Ask this ref's owner generation to read again.
    markThreadDeletedIfFenced(ref, err);
    scheduleOwnedHydrationRetry("thread", ref, pending);
    throw err;
  }
  markSubscribed();
  const model = hydrateThread(response, ref, now);
  applyHydrationResponseCut(pending, ref, model);
  return { model, response };
}

// The one place a rejection is checked for the hub's durable deletion fence
// (data.mutationOutcome === "targetDeleted" - cmd/evener-hub/app_sources.go's
// deletionFenceError) and recorded into `deletedRefs`, which Session.tsx
// reads to tell a genuinely gone ref apart from one merely slow to hydrate.
// Purely additive: it changes no control flow here (the retry above still
// fires exactly as it always has, since retiring a deleted ref's retry loop
// is a separate concern this function does not take on), only what state a
// caller can observe once the retry loop is running.
function markThreadDeletedIfFenced(ref: string, err: unknown): void {
  if (mutationErrorData(err)?.mutationOutcome !== "targetDeleted") return;
  releaseSubagentRows(ref);
  threadsStore.setState((s) => {
    if (s.deletedRefs.has(ref)) return s;
    const deletedRefs = new Set(s.deletedRefs);
    deletedRefs.add(ref);
    return { deletedRefs };
  });
}

// Lean watches omit turns until an expanded card asks for them; the shared
// threadReadParams carries the rest (subscribe:false for a ref this
// connection generation already subscribes — the read still refreshes the
// snapshot).
async function hydrateAndSubscribeWatch(
  client: AppwireClientLike,
  ref: string,
  now: number,
  pending: PendingThreadHydration,
  includeTurns = false,
): Promise<ThreadModel> {
  let resp: ThreadReadResponse;
  const { subscribe, markSubscribed } = wireSubscribeDecision(ref);
  try {
    resp = await client.request("thread/read", threadReadParams(ref, includeTurns, subscribe));
  } catch (err) {
    markThreadDeletedIfFenced(ref, err);
    scheduleOwnedHydrationRetry("watched", ref, pending);
    throw err;
  }
  markSubscribed();
  const model = hydrateThread(resp, ref, now);
  applyHydrationResponseCut(pending, ref, model);
  return model;
}

function olderItemsParams(ref: string, cursor: string) {
  return { ref, cursor, itemsView: "full", itemLimit: TRANSCRIPT_ITEM_PAGE_SIZE } as const;
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
    const image: InputItem = { type: "image", mediaType: att.mediaType, data: att.data };
    if (att.name !== undefined) image.name = att.name;
    input.push(image);
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
    marker: attachment.marker,
    name: attachment.name ?? "attachment",
    mediaType: attachment.mediaType,
    blob: attachmentBlob(attachment),
  }));
}

function trackedThreadModel(ref: string): ThreadModel | undefined {
  const state = threadsStore.getState();
  return state.threads.get(ref) ?? state.watchedThreads.get(ref);
}

function threadInstanceID(model: ThreadModel | undefined): string | undefined {
  return model?.instanceId ?? model?.threadId;
}

function composerMutationIntent(
  ref: string,
  route: ComposerMutationRoute,
  text: string,
  attachments?: InputAttachment[],
): MutationIntent {
  const model = trackedThreadModel(ref);
  // Translated HERE, not inside buildInput: this is the submit boundary. The
  // untranslated text rides along as composerText so a record that fails and
  // lands in recovery can be restored into a composer with its marker anchors
  // intact - the tiles remove those anchors, and prose is not one.
  const input = buildInput(translateAttachmentMarkers(text, attachments), attachments);
  const expectedInstanceId = threadInstanceID(model);
  const base = {
    targetRef: ref,
    threadId: model?.threadId,
    attachments: durableAttachments(attachments),
    composerText: text,
  };
  if (route === "send") {
    return {
      ...base,
      method: "turn/start",
      payload: { ref, expectedInstanceId, input },
      optimisticDisplay: { method: "turn/start", input },
    };
  }
  if (route === "queue" || route === "steer") {
    const method = route === "queue" ? "turn/queue" : "turn/steer";
    return {
      ...base,
      method,
      payload: { ref, expectedInstanceId, input },
      optimisticDisplay: { method, input },
    };
  }
  // Drain's precondition is the queue revision it is swapping against, not a
  // turn: draining is destructive, so a queue that changed since the user saw
  // it must be rejected rather than swallowed into a steer they did not intend.
  const expectedQueueRevision = model?.queue?.revision ?? 0;
  return {
    ...base,
    method: "turn/drainAsSteer",
    payload: { ref, expectedInstanceId, expectedQueueRevision, input },
    optimisticDisplay: { method: "turn/drainAsSteer", input },
  };
}

async function enqueueMutationIntent(intent: MutationIntent): Promise<void> {
  const ref = intent.targetRef;
  const client = requireClient();
  if (client.state !== "ready") throw new Error(`threads store: cannot enqueue mutation while ${client.state}`);
  const runtime = requireMutationRuntime();
  await runtime.start;
  // Enqueue schedules discovery before returning; preserve the hydrated replay
  // gate now, but only a durable commit may pin this ref after its pane closes.
  const pending = pendingThreadHydrations.get(ref);
  if (pending?.client !== wiredClient || pending.epoch !== readyEpoch) {
    dispatchableMutationRefs.add(ref);
  }
  let record: MutationOutboxRecord;
  try {
    record = await runtime.outbox.enqueueIntent(intent);
  } catch (error) {
    if (!pinnedMutationRefs.has(ref)) dispatchableMutationRefs.delete(ref);
    throw error;
  }
  pinnedMutationRefs.add(ref);
  notifyMutationPersistence([ref], { record });
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
// data.evenerErrorInfo === "conflict" (appwire.Conflict(), appwire/errors.go).
// The discriminator is the evenerErrorInfo string, not the code alone — code
// -32013 is also used by appwire.QueuedDrainPartial with a different
// evenerErrorInfo, which must NOT map to ConflictError. Any other rejection
// (a different WireError, RequestTimeoutError, ConnectionClosedError, ...)
// passes through unchanged.
function mapConflict(err: unknown): Error {
  if (err instanceof WireError && err.evenerErrorInfo === "conflict") {
    return new ConflictError(err.message);
  }
  return err instanceof Error ? err : new Error(String(err));
}

function clearMutationIntent(ref: string): MutationIntent {
  const model = trackedThreadModel(ref);
  if (!model) throw new Error(`threads store: cannot clear unhydrated thread ${ref}`);
  const expectedInstanceId = threadInstanceID(model);
  if (!expectedInstanceId) throw new Error(`threads store: thread ${ref} has no instance identity`);
  return {
    targetRef: ref,
    threadId: model.threadId,
    method: "thread/clear",
    payload: { ref, expectedInstanceId },
    attachments: [],
    optimisticDisplay: { method: "thread/clear" },
  };
}

function expectedInstanceID(ref: string): string | undefined {
  return threadInstanceID(trackedThreadModel(ref));
}

function notificationRef(n: AnyNotification): string | undefined {
  const params = n.params as { ref?: unknown };
  return typeof params.ref === "string" ? params.ref : undefined;
}

function notificationThreadId(n: AnyNotification): string | undefined {
  const params = n.params as { threadId?: unknown };
  return typeof params.threadId === "string" ? params.threadId : undefined;
}

function notificationMutationIdentities(n: AnyNotification): string[] {
  if (n.method === "thread/queueChanged") return n.params.queue.clientMutationIds ?? [];
  if (n.method === "evener/steering/injected") {
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

// Buffering is decided by IDENTITY alone, the same rule applyToMap follows for
// live delivery: where a frame lands inside a model is the reducer's call, not
// this buffer's. turn/completed used to additionally need the routing's active
// turn to match, and the cost was the same as it was in the live router — a
// no-active-turn announcement carries a synthetic turn that is never anyone's
// active turn, so the gate dropped it, and a frame this buffer refuses is
// dropped outright: handleNotification also withholds it from the stale
// published model while the hydration is pending.
function targetsPendingHydration(n: AnyNotification, pending: PendingThreadHydration): boolean {
  const routing = pending.routing;
  const ref = notificationRef(n);
  const threadId = notificationThreadId(n);
  if (ref !== undefined) {
    if (ref !== routing.ref) return false;
    if (n.method === "evener/jobs/treeUpdated") return true;
    // A ref-targeted frame is authoritative for the requested subscription,
    // but once that subscription has also taught us its thread id, a
    // contradictory id is a different thread and must not enter this buffer.
    if (threadId !== undefined && routing.threadId !== undefined && threadId !== routing.threadId) return false;
    return true;
  }
  return threadId !== undefined && threadId === routing.threadId;
}

function pendingHydrationRouting(ref: string, model: ThreadModel | undefined): PendingHydrationRouting {
  return { ref, threadId: model?.threadId };
}

// Collects the refs a notification must skip in applyToMap because a pending
// hydration owns them for this frame — either the frame targets the pending
// record's own routing (so it is buffered for replay onto the eventual
// snapshot) or it is a contradictory ref-targeted frame: it belongs to this
// subscription's identity space, but its thread identity is unsafe to replay
// onto the stale model, so it is dropped. Only called when the pending map is
// non-empty (handleNotification guards), so the steady state allocates
// nothing for this.
function collectPendingRefs(
  pendingHydrations: Map<string, PendingThreadHydration>,
  n: AnyNotification,
  refs: Set<string>,
  targetedRefs?: Set<string>,
): void {
  const ref = notificationRef(n);
  for (const [pendingRef, pending] of pendingHydrations) {
    if (targetsPendingHydration(n, pending)) {
      bufferPendingNotification(pending, n);
      refs.add(pendingRef);
      targetedRefs?.add(pendingRef);
    } else if (ref === pendingRef) {
      refs.add(pendingRef);
    }
  }
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

function bufferPendingNotification(pending: PendingThreadHydration, notification: AnyNotification): void {
  pending.notifications.push(notification);
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

// A snapshot may publish only for the client and ready generation that own
// its hydration. Retired connections cannot overwrite replacement state.
function publishThreadHydration(ref: string, pending: PendingThreadHydration, model: ThreadModel): ThreadModel | null {
  if (pendingThreadHydrations.get(ref) !== pending) return null;
  if (readyEpoch !== pending.epoch || wiredClient !== pending.client) return null;
  if ((refCounts.get(ref) ?? 0) <= 0 && !pinnedMutationRefs.has(ref)) {
    pendingThreadHydrations.delete(ref);
    return null;
  }

  const { model: hydrated, appliedAt } = replayHydrationNotifications(model, pending.notifications);

  pendingThreadHydrations.delete(ref);
  putThreadModel(ref, hydrated);
  invalidateGoalResponseFallback(ref);
  threadsStore.setState((s) => {
    const hydrations = new Map(s.hydrations);
    hydrations.set(ref, (hydrations.get(ref) ?? 0) + 1);
    if (appliedAt.length === 0) return { hydrations };
    const nextFrameTimes = new Map(s.frameTimes);
    let times = nextFrameTimes.get(ref) ?? [];
    for (const now of appliedAt) times = appendFrameTime(times, now);
    nextFrameTimes.set(ref, times);
    return { frameTimes: nextFrameTimes, hydrations };
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
  if (published.status.type === "restartRequired") {
    threadsStore.setState((state) => ({
      restartBlockingObligations: new Map(state.restartBlockingObligations).set(ref, Symbol()),
    }));
  }
  const blockingObligation = threadsStore.getState().restartBlockingObligations.get(ref);
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
    // A newer snapshot may publish while an older storage transaction is in
    // flight. Serialize reconciliation and keep dispatch closed until the
    // latest snapshot has reconciled; older writes then cannot undo recovery.
    const previous = pendingMutationReconciliations.get(ref) ?? Promise.resolve();
    const reconciliation: Promise<void> = previous
      .catch(() => undefined)
      .then(async () => {
        // A published incompatibility remains a blocking obligation across
        // later saved snapshots and reconnects. Process it in order; only a
        // compatible snapshot afterward can prove that dispatch may resume.
        const current = () =>
          isCurrentMutationRuntime(runtime) &&
          (published.status.type === "restartRequired" ||
            (pending.epoch === readyEpoch && pending.client === wiredClient));
        if (!current()) return;
        const authoritativeIds = collectAuthoritativeMutationIds(hydration.response);
        await runtime.dispatcher.reconcileIdentities(authoritativeIds);
        if (!current()) return;
        // The same read that settles what the authority knows also proves what it
        // does not: a blockedUnknown record absent from every authoritative set
        // was never journaled, so it returns to dispatch here rather than parking
        // forever behind an outage that has since recovered (kata gwea).
        // Saved snapshots contain no authoritative daemon receipt history, even
        // after an incompatible daemon has been stopped. Persist uncertainty so
        // reopening that saved snapshot cannot release an already accepted send.
        if (published.status.type === "restartRequired" || published.status.type === "notLoaded") {
          for (const record of await runtime.storage.listOutbox(ref)) {
            if (!current()) return;
            if (record.state === "submitting")
              await runtime.storage.markUnknown(record.clientMutationId, "blockedUnknown");
          }
          notifyMutationPersistence([ref]);
        } else if (published.status.type !== "notLoaded") {
          await runtime.dispatcher.restoreProvenAbsent(ref, authoritativeIds);
        }
        if (!current()) return;
        await refreshMutationPins(runtime, [ref]);
        // A newer incompatible snapshot owns a different obligation. An older
        // successful reconciliation cannot clear that newer restriction.
        if (
          current() &&
          published.status.type !== "restartRequired" &&
          published.status.type !== "notLoaded" &&
          threadsStore.getState().restartBlockingObligations.get(ref) === blockingObligation
        ) {
          threadsStore.setState((state) => {
            const restartBlockingObligations = new Map(state.restartBlockingObligations);
            restartBlockingObligations.delete(ref);
            return { restartBlockingObligations };
          });
        }
      });
    pendingMutationReconciliations.set(ref, reconciliation);
    try {
      await reconciliation;
      if (
        pendingMutationReconciliations.get(ref) === reconciliation &&
        isCurrentMutationRuntime(runtime) &&
        pending.epoch === readyEpoch &&
        pending.client === wiredClient
      ) {
        threadsStore.setState((state) => {
          const mutationReconciliationFailures = new Set(state.mutationReconciliationFailures);
          mutationReconciliationFailures.delete(ref);
          return { mutationReconciliationFailures };
        });
      }
    } catch (error) {
      if (
        pendingMutationReconciliations.get(ref) === reconciliation &&
        isCurrentMutationRuntime(runtime) &&
        pending.epoch === readyEpoch &&
        pending.client === wiredClient
      ) {
        // Discovery retries the authoritative read after storage recovers.
        // Keep the failure visible and dispatch closed until that succeeds.
        threadsStore.setState((state) => ({
          mutationReconciliationFailures: new Set(state.mutationReconciliationFailures).add(ref),
        }));
      }
      throw error;
    } finally {
      if (pendingMutationReconciliations.get(ref) === reconciliation) pendingMutationReconciliations.delete(ref);
    }
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
  storeWatchedModel(ref, replayed.model, includeTurns, generation);
  settleOwnedHydration("watched", ref, replayed.model);
  return replayed.model;
}

// Fold one notification into matching models; real-pane updates also append
// the same timestamp to their liveness trace.
//
// Routing equivalence argument (why routeByNotificationKey is the scan):
// notificationTargetsThread (protocol/reducer.ts) targets a model by
// params.ref first, else by params.threadId, and both are read off the
// frame's own params — method-agnostic, so the equivalence holds for EVERY
// notification, in the generated catalog or not, and no per-method gate or
// fallback scan is needed:
//   - ref present: exactly the model with model.ref === params.ref, which is
//     map.get(params.ref) (model.ref === map key, hydrateThread
//     construction). A ref matching no map entry means the scan would select
//     nothing.
//   - ref absent, threadId present: exactly the models with
//     model.threadId === params.threadId, which is byThreadId.get(threadId)
//     resolved through the map. The reducer never rewrites threadId (only
//     hydrateThread sets it), so the index is authoritative.
//   - both absent: notificationTargetsThread returns false for every model —
//     the scan is a guaranteed no-op, and routeByNotificationKey's null
//     return produces the same result (no changedRefs, no frame-time writes).
// A reducer fold that produces a new model object keeps its ref/threadId
// (index-stability note above), so routing needs NO re-index here. For a
// ref-routed frame the router returns the single model directly (no wrapper
// array); changedRefs order differs from the scan's map-iteration order, but
// its only consumer (handleNotification's frameTimes loop) is
// order-insensitive, and applyToMap is module-private.
function applyToMap(
  map: Map<string, ThreadModel>,
  index: ThreadModelIndex,
  n: AnyNotification,
  now: number,
  skippedRefs?: ReadonlySet<string>,
): { next: Map<string, ThreadModel> | null; changedRefs: string[]; acceptedRefs: string[] } {
  let next: Map<string, ThreadModel> | null = null;
  const changedRefs: string[] = [];
  const acceptedRefs: string[] = [];
  const routed = routeByNotificationKey(map, index, n, skippedRefs);
  if (!routed) return { next, changedRefs, acceptedRefs };
  const accept = (model: ThreadModel): void => {
    acceptedRefs.push(model.ref);
  };
  if (Array.isArray(routed)) {
    for (const model of routed) {
      accept(model);
      const updated = applyNotification(model, n, now);
      if (updated === model) continue;
      next ??= new Map(map);
      next.set(model.ref, updated);
      changedRefs.push(model.ref);
    }
    return { next, changedRefs, acceptedRefs };
  }
  accept(routed);
  const updated = applyNotification(routed, n, now);
  if (updated !== routed) {
    next = new Map(map);
    next.set(routed.ref, updated);
    changedRefs.push(routed.ref);
  }
  return { next, changedRefs, acceptedRefs };
}

function handleNotification(n: AnyNotification): void {
  if (n.method === "evener/thread/resync") {
    if (wiredClient) void handleReady(wiredClient, readyEpoch, n.params.ref);
    return;
  }
  if (n.method === "evener/auth/updated") {
    modelsEpoch += 1;
    modelsCache = null;
    inflightModelsList = null;
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
  const now = Date.now();
  const { threads, frameTimes, watchedThreads } = threadsStore.getState();
  const acceptedGoalRefs = new Set<string>();
  // Pending-hydration routing: pendingThreadHydrations/pendingWatchedHydrations
  // are intentionally left as plain map iterations (NOT indexed). They are
  // usually tiny — at most one entry per in-flight thread/read (bounded by
  // concurrent pane mounts and reconnect fan-out), not per tracked thread —
  // and targetsPendingHydration's decision depends on the pending record's
  // own learned routing (ref/threadId), so an index would add maintenance
  // surface to every hydration begin/publish/release for no measurable win.
  // The hot path this store pays per delta is the threads/watchedThreads
  // fan-out, which IS indexed (see applyToMap).
  let pendingRefs: ReadonlySet<string> = EMPTY_PENDING_REFS;
  if (pendingThreadHydrations.size > 0) {
    const refs = new Set<string>();
    const targeted = n.method === "evener/goal/updated" ? new Set<string>() : undefined;
    collectPendingRefs(pendingThreadHydrations, n, refs, targeted);
    if (refs.size > 0) pendingRefs = refs;
    if (targeted) for (const ref of targeted) acceptedGoalRefs.add(ref);
  }
  let pendingWatchedRefs: ReadonlySet<string> = EMPTY_PENDING_REFS;
  if (pendingWatchedHydrations.size > 0) {
    const refs = new Set<string>();
    const targeted = n.method === "evener/goal/updated" ? new Set<string>() : undefined;
    collectPendingRefs(pendingWatchedHydrations, n, refs, targeted);
    if (refs.size > 0) pendingWatchedRefs = refs;
    if (targeted) for (const ref of targeted) acceptedGoalRefs.add(ref);
  }
  const {
    next: nextThreads,
    changedRefs: changedThreads,
    acceptedRefs: acceptedThreads,
  } = applyToMap(threads, threadsIndex, n, now, pendingRefs);
  const { next: nextWatchedThreads, acceptedRefs: acceptedWatchedThreads } = applyToMap(
    watchedThreads,
    watchedThreadsIndex,
    n,
    now,
    pendingWatchedRefs,
  );
  if (n.method === "evener/goal/updated") {
    for (const ref of acceptedThreads) acceptedGoalRefs.add(ref);
    for (const ref of acceptedWatchedThreads) acceptedGoalRefs.add(ref);
    for (const ref of acceptedGoalRefs) invalidateGoalResponseFallback(ref);
  }
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
  }
  threadsStore.setState(patch);
}

function storeWatchedModel(ref: string, model: ThreadModel, includeTurns: boolean, generation: number): void {
  if ((watchRefCounts.get(ref) ?? 0) <= 0) return;
  if ((watchGenerations.get(ref) ?? 0) !== generation) return;

  // A late lean reconnect snapshot cannot downgrade a rich snapshot that
  // already won an upgrade race in this same watch lifetime.
  const hydratedRich = watchHydratedIncludeTurns.get(ref) ?? false;
  if (!includeTurns && hydratedRich) return;
  watchHydratedIncludeTurns.set(ref, hydratedRich || includeTurns);
  invalidateGoalResponseFallback(ref);
  putWatchedThreadModel(ref, model);
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
  reportFailure = false,
): Promise<void> {
  if ((refCounts.get(ref) ?? 0) <= 0 && !pinnedMutationRefs.has(ref)) return;
  const previous = pendingThreadHydrations.get(ref);
  if (!targetedResync && previous?.client === client && previous.epoch === epoch) return;
  const pending = beginThreadHydration(ref, client, threadsStore.getState().threads.get(ref), epoch);
  // No pre-check here: pending.client is this `client` and pending.epoch is this
  // `epoch`, so publishThreadHydration re-decides exactly the same thing one
  // frame later, and returning null from there reconciles nothing either. The
  // gate lives in one place.
  const hydration = hydrateAndSubscribe(client, ref, Date.now(), pending).then((result) =>
    publishAndReconcileThreadHydration(ref, pending, result),
  );
  const completion = hydration.then(
    () => undefined,
    () => undefined,
  );
  trackedHydrationCompletions.set(ref, completion);
  void completion.then(() => {
    if (trackedHydrationCompletions.get(ref) === completion) trackedHydrationCompletions.delete(ref);
  });
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
  } catch (error) {
    if (reportFailure) throw error;
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

  if (!isCurrentMutationRuntime(runtime) || wiredClient !== client || readyEpoch !== epoch || client.state !== "ready")
    return;
  if (!targetedResync) {
    const alreadyHydrated = new Set(refs);
    const discovered = await discoveredPinnedRefs;
    // A pin is a fact about this runtime's storage, while rejoining is a fact
    // about this connection generation. This scan is a real IndexedDB read,
    // so reset or reconnect can land inside it; re-check both owners before
    // mutating the shared pin set or putting reads on the wire.
    if (!isCurrentMutationRuntime(runtime)) return;
    for (const ref of discovered) pinnedMutationRefs.add(ref);
    if (wiredClient !== client || readyEpoch !== epoch || client.state !== "ready") return;
    await Promise.all(
      discovered.filter((ref) => !alreadyHydrated.has(ref)).map((ref) => handleReady(client, epoch, ref)),
    );
    if (
      !isCurrentMutationRuntime(runtime) ||
      wiredClient !== client ||
      readyEpoch !== epoch ||
      client.state !== "ready"
    )
      return;
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
  // A different client is a different connection: every wire subscription
  // this generation tracked belongs to a socket that is gone, so drop the
  // whole set — handleReady's re-reads re-subscribe the still-tracked refs on
  // the new client.
  wireSubscribedRefs.clear();
  retireAllOwnedHydrations();
  dispatchReadyClient = null;
  dispatchReadyEpoch = -1;
  dispatchableMutationRefs.clear();
  unwireNotification?.();
  unwireReady?.();
  wiredClient = client;
  unwireNotification = client.onNotification(handleNotification);
  unwireReady = client.onReady(() => {
    readyEpoch += 1;
    // onReady is the SAME client reconnecting: its old connection's
    // subscriptions are server-side gone too, even though the client object
    // survives. handleReady re-subscribes the still-tracked refs.
    wireSubscribedRefs.clear();
    retireAllOwnedHydrations();
    dispatchReadyClient = null;
    dispatchReadyEpoch = -1;
    dispatchableMutationRefs.clear();
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

// waitForReadyOrRewire resolves once EITHER `client` itself fires its own
// onReady (the common case: the SAME client's automatic reconnect backoff
// lands) OR connectionStore's wired client identity changes out from under
// it (the rarer case: a manual retry - shell/ConnectionBanner.tsx - swaps in
// a genuinely different client while this one is still waiting), or rejects
// once `timeoutMs` elapses with neither. Always cleans up both subscriptions
// and the timer on whichever path settles first.
//
// Subscribes fresh to THIS client's own onReady on every call rather than
// sharing one module-level promise across callers: a single shared promise
// that only ever resolves once per client would need active re-arming every
// time the client leaves "ready" again, and a client that starts out ready
// (the common case - ConnectionBanner's manual retry awaits connect() before
// handing the client to connectionStore.connect()) gives that re-arming
// nothing to trigger off of. A fresh per-call subscription needs no such
// bookkeeping and is correct for every reconnect, not just the first.
function waitForReadyOrRewire(client: AppwireClientLike, timeoutMs: number): Promise<void> {
  return new Promise<void>((resolve, reject) => {
    const timer = setTimeout(() => {
      cleanup();
      reject(new ClientNotReadyError(`threads store: timed out waiting for a ready client after ${timeoutMs}ms`));
    }, timeoutMs);
    const unwireReady = client.onReady(() => {
      cleanup();
      resolve();
    });
    const unsubscribeSwap = connectionStore.subscribe((state) => {
      if (state.client && state.client !== client) {
        cleanup();
        resolve();
      }
    });
    function cleanup(): void {
      clearTimeout(timer);
      unwireReady();
      unsubscribeSwap();
    }
  });
}

// requireReadyClient waits out a reconnect rather than failing the caller
// with AppwireClient's synchronous "cannot call ... while reconnecting"
// rejection - the wait-and-retry shape that used to be hand-duplicated four
// times across ensureThread/watchThread's retry loops below, extracted once
// here and reused by both those call sites and the read-only actions
// (listJobs, listTasks, jobOutput, listModels, loadOlderTurns) that gate on
// it directly.
//
// Issue #195's RCA: transport-level queuing (in client.ts) was rejected as
// unsafe - a queued call retried blind across a reconnect could double-fire
// a non-idempotent mutation whose first attempt the server may already be
// executing. Read-only calls carry no such risk, so instead of queuing at
// the transport, callers that can safely blind-retry wait HERE for the
// client to become ready (or be rewired to one that already is), then issue
// one direct request() against a client already confirmed ready. Mutations
// (setModel, rename, compact, ..., and the outbox's own
// enqueueMutationIntent gate) deliberately do NOT call this - they keep
// AppwireClient's synchronous rejection, so a caller retrying a mutation
// whose first attempt may already be executing server-side can never have
// both attempts land.
//
// Loops rather than waiting once: a rewire mid-wait can land on a client
// that is ALSO not yet ready (a fresh client still mid-handshake), so this
// re-arms the wait on whatever client is current until one is actually
// ready or the shared deadline (not reset per iteration) elapses. Always
// returns the CURRENT client (a fresh requireClient() read) once ready,
// never one read before the wait.
//
// Bounded by timeoutMs so a genuinely-down hub cannot hang a caller forever:
// on timeout, throws ClientNotReadyError (protocol/errors.ts) rather than
// AppwireClient's own rejection text, so a caller can tell "gave up after
// waiting" apart from "rejected immediately" - and so friendlyErrorMessage/
// errorKind (protocol/errors.ts) still classify it as hub-unreachable for a
// caller that wants the same friendly message either way (see
// stores/activitySummary.ts's refreshRoot).
const REQUIRE_READY_TIMEOUT_MS = 15_000;

async function requireReadyClient(timeoutMs = REQUIRE_READY_TIMEOUT_MS): Promise<AppwireClientLike> {
  const deadline = Date.now() + timeoutMs;
  let client = requireClient();
  while (client.state !== "ready") {
    const remaining = deadline - Date.now();
    if (remaining <= 0) {
      throw new ClientNotReadyError(`threads store: timed out waiting for a ready client after ${timeoutMs}ms`);
    }
    await waitForReadyOrRewire(client, remaining);
    client = requireClient();
  }
  return client;
}

function replaceThread(
  models: Map<string, ThreadModel>,
  ref: string,
  update: (model: ThreadModel) => ThreadModel,
): Map<string, ThreadModel> {
  const current = models.get(ref);
  if (!current) return models;
  const next = new Map(models);
  next.set(ref, update(current));
  return next;
}

export const threadsStore = createStore<ThreadsStoreState>(() => ({
  threads: new Map(),
  mutationWriteStalled: false,
  mutationReconciliationFailures: new Set(),
  restartBlockingObligations: new Map(),
  frameTimes: new Map(),
  hydrations: new Map(),
  watchedThreads: new Map(),
  deletedRefs: new Set(),

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
          // Release is terminal even when the failed read belongs to an old
          // connection. A reconnect cannot re-arm a pane that no longer exists.
          if (!lifecycleActive) return;
          if (wiredClient !== inflightClient || readyEpoch !== inflightEpoch) {
            if (threadsStore.getState().threads.has(ref)) return;
            // requireReadyClient re-reads the CURRENT client on the way out,
            // so a client captured before the wait is never stamped onto a
            // hydration that outlived it. Same shape as the re-arm below and
            // as both of watchThread's.
            client = await requireReadyClient();
            if (ensureGenerations.get(ref) !== generation || (refCounts.get(ref) ?? 0) <= 0) return;
            inflight = inflightHydrates.get(ref) ?? startHydration(client);
            continue;
          }
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

        client = await requireReadyClient();
        if ((refCounts.get(ref) ?? 0) <= 0) return;
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
    releaseSubagentRows(ref);
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
    trackedHydrationCompletions.delete(ref);
    pendingThreadHydrations.delete(ref);
    // A watched lifecycle may still hold this ref (watchRefCounts), and its
    // model stays; only the pane's own tracking goes. Unsubscribe the wire
    // subscription when this was the last holder of either kind, so the hub
    // stops relaying a thread nobody renders and its relay can idle out.
    if (wireSubscribedRefs.has(ref) && (watchRefCounts.get(ref) ?? 0) <= 0) {
      wireSubscribedRefs.delete(ref);
      sendThreadUnsubscribe(ref);
    }
    // frameTimes is dropped in lockstep — an untracked ref has no business
    // holding onto a liveness trace a future ensureThread() of the same ref
    // should start fresh, the same way it re-reads a fresh model.
    removeThreadModel(ref);
  },

  // watchThread is the transcript/tools stream's own sanctioned addition:
  // an additive, leaner (includeTurns:false) subscription to a child
  // thread for a delegate card's live view, refcounted
  // independently of ensureThread's own counter and stored in watchedThreads.
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
          client = await requireReadyClient();
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

      client = await requireReadyClient();
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
    // The open-pane lifecycle may still hold this ref; only when it is gone
    // too does the wire subscription have no remaining holder.
    if (wireSubscribedRefs.has(ref) && (refCounts.get(ref) ?? 0) <= 0) {
      wireSubscribedRefs.delete(ref);
      sendThreadUnsubscribe(ref);
    }
    removeWatchedThreadModel(ref);
  },

  async refreshThread(ref): Promise<void> {
    await requireReadyClient();
    const client = requireClient();
    if (client.state !== "ready") return threadsStore.getState().refreshThread(ref);
    await refreshTrackedThread(client, readyEpoch, ref, true, true);
    const runtime = getMutationRuntime();
    if (runtime) scheduleMutationDispatch(runtime, [ref]);
  },

  async loadOlderTurns(ref) {
    // Read-only, so it waits out a reconnect (issue #195's RCA) instead of
    // failing with AppwireClient's synchronous "cannot call ... while
    // reconnecting" rejection - see requireReadyClient's own comment.
    await requireReadyClient();
    await trackedHydrationCompletions.get(ref);
    const client = await requireReadyClient();
    const capturedEpoch = readyEpoch;
    const model = threadsStore.getState().threads.get(ref);
    if (!model?.olderCursor) return; // untracked, or no more history to page in
    const capturedRef = model.ref;
    const capturedCursor = model.olderCursor;
    const capturedHydrations = threadsStore.getState().hydrations.get(ref) ?? 0;
    const capturedPageGeneration = olderPageGenerations.get(ref) ?? 0;
    let resp: ThreadTurnsListResponse;
    try {
      resp = await client.request("thread/turns/list", olderItemsParams(ref, capturedCursor));
    } catch (error) {
      if (isStaleCursorError(error)) {
        const current = threadsStore.getState().threads.get(ref);
        if (
          !current ||
          wiredClient !== client ||
          readyEpoch !== capturedEpoch ||
          current.ref !== capturedRef ||
          current.olderCursor !== capturedCursor ||
          (threadsStore.getState().hydrations.get(ref) ?? 0) !== capturedHydrations ||
          (olderPageGenerations.get(ref) ?? 0) !== capturedPageGeneration ||
          pendingThreadHydrations.has(ref)
        )
          return;
        await refreshTrackedThread(client, capturedEpoch, capturedRef, true);
        return;
      }
      throw error;
    }
    // A concurrent releaseThread() may have dropped this ref while the page
    // was in flight; don't resurrect it. Re-read (rather than reusing
    // `model`) so a live notification that arrived during the await isn't
    // clobbered by prepending onto a stale snapshot.
    const current = threadsStore.getState().threads.get(ref);
    if (
      !current ||
      wiredClient !== client ||
      readyEpoch !== capturedEpoch ||
      current.ref !== capturedRef ||
      current.olderCursor !== capturedCursor ||
      (threadsStore.getState().hydrations.get(ref) ?? 0) !== capturedHydrations ||
      (olderPageGenerations.get(ref) ?? 0) !== capturedPageGeneration ||
      pendingThreadHydrations.has(ref)
    )
      return;
    olderPageGenerations.set(ref, capturedPageGeneration + 1);
    putThreadModel(ref, mergeOlderItemPage(current, resp));
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
    // Stop is session-scoped, always. Naming a turn here could only ever make
    // Stop fail: the id is missing in the windows Stop matters most -- a turn
    // the session started for itself, a boundary between two turns of one
    // drain, a cold client -- and stale in the race where a turn rolls over
    // between the click and the request. Neither refusal is what the button
    // means. "Stop" means stop what you are doing.
    await enqueueMutation(
      ref,
      "turn/interrupt",
      { ref, expectedInstanceId: expectedInstanceID(ref) },
      { method: "turn/interrupt" },
    );
  },

  async drainAsSteer(ref, text, attachments) {
    await enqueueMutationIntent(composerMutationIntent(ref, "drain", text, attachments));
  },

  async promoteQueuedAsSteer(ref, index, expectedEntryId) {
    // The entry id is the precondition that matters: it names the message being
    // promoted, so a queue that shifted underneath is caught without needing a
    // turn id that would only add a second way to fail.
    await enqueueMutation(
      ref,
      "turn/promoteQueuedAsSteer",
      { ref, index, expectedInstanceId: expectedInstanceID(ref), expectedEntryId },
      { method: "turn/promoteQueuedAsSteer", index, expectedEntryId },
    );
  },

  async cancelQueued(ref, index, expectedEntryId) {
    await enqueueMutation(
      ref,
      "turn/cancelQueued",
      { ref, index, expectedInstanceId: expectedInstanceID(ref), expectedEntryId },
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

  async setVisionModel(ref, visionModel) {
    const client = requireClient();
    try {
      await client.request("thread/vision-model/set", { ref, visionModel });
    } catch (err) {
      throw mapConflict(err);
    }
  },

  async setGoal(ref, objective) {
    const client = requireClient();
    const generation = (goalUpdateGenerations.get(ref) ?? 0) + 1;
    goalUpdateGenerations.set(ref, generation);
    try {
      const response = await client.request("goal/set", { ref, objective });
      if (goalUpdateGenerations.get(ref) !== generation) return response;
      const goal = objective === "" ? null : { objective, status: "active", iterations: 0 };
      threadsStore.setState((state) => {
        const threads = replaceThread(state.threads, ref, (model) => ({ ...model, goal }));
        const watchedThreads = replaceThread(state.watchedThreads, ref, (model) => ({ ...model, goal }));
        if (threads === state.threads && watchedThreads === state.watchedThreads) return state;
        return { threads, watchedThreads };
      });
      return response;
    } catch (err) {
      throw mapConflict(err);
    }
  },

  async rename(ref, name) {
    const client = requireClient();
    try {
      await client.request("evener/thread/name/set", { ref, name });
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
    const runtime = requireMutationRuntime();
    await runtime.start;
    await enqueueMutationIntent(clearMutationIntent(ref));
    // A clear is fenced by the model's instance id, so it can dispatch while
    // an older resync read is in flight. Its response is the newer cut and
    // retires that read in applyClearResponse.
    dispatchableMutationRefs.add(ref);
    await runtime.dispatcher.dispatchTargets([ref]);
    await refreshMutationPins(runtime, [ref]);
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
    // Cache/inflight hits below need no wire call at all, so they must not
    // block on a reconnect that a warm cache makes irrelevant - check them
    // BEFORE waiting for a ready client, unlike the other read-only actions
    // here (which always need the wire, so the order doesn't matter).
    if (!refresh && modelsCache) return modelsCache;
    if (!refresh && inflightModelsList) return inflightModelsList;
    // The ready-wait (issue #195's RCA - read-only, so it waits out a
    // reconnect instead of failing with AppwireClient's synchronous "cannot
    // call ... while reconnecting" rejection; see requireReadyClient's own
    // comment) lives INSIDE this inner async call, not awaited directly
    // here, so the inflightModelsList assignment right below still runs
    // synchronously relative to a concurrent caller of this same method -
    // two callers racing listModels() must agree on one in-flight request
    // before either of them suspends, same as before this method waited on
    // anything.
    // No mapConflict here: model/list is a read-only listing with no
    // turn-CAS concept (verified against every server-side handler - see
    // this file's own describe block for the exact citations).
    const epoch = modelsEpoch;
    const request = (async () => {
      const client = await requireReadyClient();
      return client.request("model/list", {});
    })();
    if (!refresh) inflightModelsList = request;
    try {
      const resp = await request;
      if (epoch === modelsEpoch) modelsCache = resp;
      return resp;
    } finally {
      if (!refresh && inflightModelsList === request) inflightModelsList = null;
    }
  },

  async listTasks(ref) {
    // Read-only, so it waits out a reconnect (issue #195's RCA) instead of
    // failing with AppwireClient's synchronous "cannot call ... while
    // reconnecting" rejection - see requireReadyClient's own comment.
    const client = await requireReadyClient();
    // No mapConflict here either, same reasoning as listModels above.
    const resp = await client.request("evener/tasks/list", { ref });
    return resp.data;
  },

  async listJobs(ref, continuation) {
    // Read-only, so it waits out a reconnect (issue #195's RCA) instead of
    // failing with AppwireClient's synchronous "cannot call ... while
    // reconnecting" rejection - see requireReadyClient's own comment.
    const client = await requireReadyClient();
    // No mapConflict here either, same reasoning as listModels/listTasks above.
    const resp = await client.request("evener/jobs/list", { ref, ...(continuation ? { continuation } : {}) });
    return resp.data;
  },

  async jobOutput(ref, jobId, beforeBytes, maxBytes) {
    // Read-only, so it waits out a reconnect (issue #195's RCA) instead of
    // failing with AppwireClient's synchronous "cannot call ... while
    // reconnecting" rejection - see requireReadyClient's own comment.
    const client = await requireReadyClient();
    const resp = await client.request("evener/jobs/output", {
      ref,
      jobId,
      ...(beforeBytes !== undefined && beforeBytes > 0 ? { beforeBytes } : {}),
      ...(maxBytes !== undefined && maxBytes > 0 ? { maxBytes } : {}),
    });
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
      await client.request("evener/sandbox/escalation/resolve", { ref, escalationId, approve });
    } catch (err) {
      throw mapConflict(err);
    }
    // One setState for both maps (putThreadModels), same as clearThread:
    // two sequential puts would let a synchronous subscriber see the
    // escalation cleared in threads but not yet in watchedThreads. Both
    // resolutions are computed first, then filed together; each is dropped
    // when the resolver made no change (same-reference no-op), matching the
    // old single-setState patch shape exactly.
    const stateBefore = threadsStore.getState();
    const model = stateBefore.threads.get(ref);
    const resolvedModel = model ? resolvePendingEscalation(model, escalationId) : undefined;
    const watchedModel = stateBefore.watchedThreads.get(ref);
    const resolvedWatched = watchedModel ? resolvePendingEscalation(watchedModel, escalationId) : undefined;
    putThreadModels(
      ref,
      resolvedModel !== undefined && resolvedModel !== model ? resolvedModel : undefined,
      resolvedWatched !== undefined && resolvedWatched !== watchedModel ? resolvedWatched : undefined,
    );
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
  resetActivityPanelStoreForTests();
  resetActivitySummaryStoreForTests();
  resetTasksPanelStoreForTests();
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
  olderPageGenerations.clear();
  goalUpdateGenerations.clear();
  inflightHydrates.clear();
  inflightHydrateClients.clear();
  inflightHydrateEpochs.clear();
  trackedHydrationCompletions.clear();
  pendingThreadHydrations.clear();
  pendingMutationReconciliations.clear();
  watchRefCounts.clear();
  inflightWatchHydrates.clear();
  inflightWatchHydrateClients.clear();
  inflightWatchHydrateEpochs.clear();
  inflightWatchIncludeTurns.clear();
  pendingWatchedHydrations.clear();
  watchGenerations.clear();
  watchIncludeTurns.clear();
  watchHydratedIncludeTurns.clear();
  wireSubscribedRefs.clear();
  threadsIndex.clear();
  watchedThreadsIndex.clear();
  modelsCache = null;
  inflightModelsList = null;
  unwireNotification?.();
  unwireReady?.();
  unwireNotification = null;
  unwireReady = null;
  wiredClient = null;
  readyEpoch = 0;
  // replace:true, rebuilt from getInitialState() rather than a partial merge
  // onto whatever threadsStore currently holds: Zustand's default setState
  // does Object.assign({}, state, partial), which copies every OTHER
  // current property - including any action method a test has vi.spyOn'd,
  // like send/queue/steer - forward into the new state object unchanged. A
  // spy installed before any later setState call (e.g. this file's own
  // focusSession helper, called after vi.spyOn(threadsStore.getState(),
  // "send") in CommandPalette.test.tsx) therefore survives vi.restoreAllMocks()
  // forever: restoreAllMocks() only restores the ORIGINAL object it patched,
  // not the merged object that has since superseded it as threadsStore's
  // current state. getInitialState() returns Zustand's own pristine,
  // closure-captured-once state object, untouched by any setState call ever
  // made, so rebuilding from it guarantees no stale spy on any action method
  // can outlive this reset (kata ycet).
  threadsStore.setState(
    {
      ...threadsStore.getInitialState(),
      threads: new Map(),
      frameTimes: new Map(),
      hydrations: new Map(),
      watchedThreads: new Map(),
      deletedRefs: new Set(),
    },
    true,
  );
}

// Read-only snapshot of the thread-id routing indexes for the store's own
// tests: the differential test asserts key-set consistency with the maps
// after every notification — every tracked model's threadId is indexed under
// its ref, and nothing the maps dropped lingers in an index — which is what
// makes a stale index fail immediately instead of only when a random
// sequence happens to diverge. (The ref route needs no test-visible index:
// it IS the map.)
export function threadRoutingIndexesForTests(): {
  threadsByThreadId: ReadonlyMap<string, ReadonlySet<string>>;
  watchedByThreadId: ReadonlyMap<string, ReadonlySet<string>>;
} {
  return {
    threadsByThreadId: threadsIndex,
    watchedByThreadId: watchedThreadsIndex,
  };
}
