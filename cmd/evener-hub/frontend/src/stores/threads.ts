import type { ComposerMention } from "@evener/appwire-client";
// threads.ts tracks the ThreadModel for every ref currently open in a pane,
// refcounted across panes sharing the same ref, and routes live wire
// notifications into the reducer for whichever tracked model(s) they target.
// It rides the single AppwireClientLike connection.ts wires via
// connectionStore.getState().connect(client) — this store has no
// connect() of its own — and reactively re-attaches its onNotification/onReady
// handlers to whatever client connectionStore currently holds, via a
// connectionStore.subscribe() wired at module load (see rewireClient).

import type {
  AnyNotification,
  AppwireClientLike,
  CachedSessionRecord,
  GoalSetResponse,
  JobActivityJob,
  JobOutputPage,
  ModelListResponse,
  SnapshotIdentity,
  TaskListResponse,
  ThreadClearResponse,
  ThreadForkResponse,
  ThreadItemPosition,
  ThreadModel,
  ThreadReadResponse,
  ThreadTurnsListResponse,
  TurnModel,
  UrlsRemoveResponse,
} from "@evener/appwire-client";
import {
  acquireThreadSubscription,
  applyHistoryReadFailure,
  applyNotification,
  applyReadResponse,
  buildComposerInput,
  buildInput,
  ClientNotReadyError,
  cachedSessionRecord,
  canonicalSkillNames,
  collectAuthoritativeMutationIds,
  comparePositions,
  errorText,
  hydrateThread,
  type InputAttachment,
  invalidateHistory,
  isStaleCursorError,
  issueLatestWindowRead,
  isTranscriptHistoryFailedError,
  mergeOlderItemPage,
  mergeTailTurns,
  mutationErrorData,
  notificationRoutingKey,
  readDisposition,
  readWindowBounds,
  resolvePendingEscalation,
  SHUT_DOWN_STATUSES,
  type ThreadSubscriptionLease,
  threadModelFromCache,
  WireError,
} from "@evener/appwire-client";
import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import { releaseSubagentRows } from "../panes/session/transcript/tools/subagentModuleStore";
import { resetActivityPanelStoreForTests } from "./activityPanel";
import { connectedClientPort, connectionStore } from "./connection";
import { acknowledgeHumanNote, canWriteHumanNote, resetHumanNoteDrafts } from "./humanNoteDrafts";
import { MutationDispatcher, validConsumedClientMutationIds } from "./mutationDispatcher";
import {
  type MutationAttachment,
  type MutationCommit,
  type MutationIntent,
  type MutationOptimisticRecord,
  MutationOutbox,
  type MutationOutboxOptions,
  type MutationOutboxRecord,
  type MutationRecoveryRecord,
  type MutationStopBarrier,
} from "./mutationOutbox";
import {
  MutationOutboxIndexedDB,
  MutationStorageTimeoutError,
  type RetargetedMutations,
} from "./mutationOutboxIndexedDB";
import { createReadyGenerationCallback } from "./readyGenerationCallback";
import { createSecureUUID } from "./secureUUID";
import { SESSION_CACHE_LOOKUP_DEADLINE_MS, SessionCacheIndexedDB } from "./sessionCacheIndexedDB";
import { resetTasksPanelStoreForTests } from "./tasksPanel";
import { createVersionedChannel, makeSourceId, type VersionedChannelMessage } from "./versionedChannel";

export type { InputAttachment } from "@evener/appwire-client";

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
export type ComposerMutationRoute = "send" | "queue" | "steer" | "drain";

// ForkFromTurnOptions mirrors ThreadForkParams verbatim (appwire/types.go:
// 692-711) minus ref (a separate positional argument, like every other
// action here). Fork and aside are the SAME wire method with mutually
// exclusive param sets (aside excludes sourceItemKey/editedInput/deferInput/
// label per that struct's own doc comment) - the Go type itself is one flat
// struct with no type-level split enforcing this, so this TS type mirrors
// that honestly rather than inventing a discriminated union the wire
// doesn't have; enforcing the exclusion is the caller's (T5's) job.
export interface ForkFromTurnOptions {
  sourceItemKey?: string;
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

// The ghost's display input (steering-ghost spec §3): the row's full text
// - or the daemon's own preview placeholder when the row is image-only -
// plus its canonical skill selections, so the optimistic promote record
// previews the message it will become instead of the action's wire
// parameters.
export interface PromoteDisplayInput {
  text: string;
  skillNames?: readonly string[];
  commandNames?: readonly string[];
}

interface CacheLifetime {
  /** The current model is an unverified cached shell: actions, paging and
   * writes wait for its first authoritative publish. */
  shell?: true;
  /** The newest recorded item position, fixed before any live shell merge. */
  anchor?: ThreadItemPosition;
  /** Clear/deletion write suppression, re-checked at write-fire time. */
  suppressed?: true;
  /** The owned ref's captured epoch, including misses and deadline fallbacks. */
  leaseEpoch?: number;
}

export interface ThreadsStoreState {
  threads: Map<string, ThreadModel>;
  mutationWriteStalled: boolean;
  mutationReconciliationFailures: ReadonlySet<string>;
  // Reconciliation failures this page attributes to unavailable storage rather
  // than a mutation-state conflict: the same wedge that fences the durable
  // write also fails the reconcile read, and treating that as a real failure
  // fences the direct-dispatch fallback too, re-breaking the promise that a
  // send survives a storage wedge. Storage-blocked refs stay fenced for the
  // durable dispatcher; the send fallback is the one caller that may admit
  // them. Cleared by a successful reconciliation; retried by every discovery
  // pass.
  mutationReconciliationStorageBlocked: ReadonlySet<string>;
  restartBlockingObligations: ReadonlyMap<string, symbol>;
  mutationAuthorityRefs: ReadonlySet<string>;
  // The resumed identity when a store-driven resume (resumeFencedForSend)
  // returned a different ref than it started with, keyed by the ref the pane
  // was showing. The pane reads this and follows the new identity (Session.tsx)
  // then clears it: a store cannot navigate, so this slice is the one seam
  // between the resume driver and the pane that started it. An entry that
  // survives (the pane closed before it could follow) is read by the next pane
  // opened on that ref, which then follows the identity itself, so a resume is
  // never lost to a closed pane.
  resumedIdentities: ReadonlyMap<string, string>;
  // The most recent store-driven resume failure per ref, for the composer to
  // toast (a store cannot push a toast). Keyed by ref with a monotonic seq so
  // the observer toasts each failure exactly once; cleared when a drive
  // succeeds, and the composer seeds its dedup from the seq on mount so a
  // remount never re-toasts a failure that predates it.
  resumeFailures: ReadonlyMap<string, { seq: number; message: string }>;
  clearResumedIdentity(ref: string): void;
  // Refs with a Force stop RPC this page started still in flight. The hub holds
  // Stopping > 0 for exactly that window and refuses even turn/start for the
  // drain (cmd/evener-hub's sessionActionRecoveryError), so a notLoaded snapshot
  // taken mid-drain must not be read as the merely-resumable shape
  // (isResumeOnlyLocal). Cleared when the RPC settles. A stop another client
  // started is not observable here (no wire signal carries Stopping), so that
  // rare window degrades to the same bounded refusal the normal fence covers.
  stoppingRefs: ReadonlySet<string>;
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
  // ref apart from "gone". It is also the terminal marker for that ref's
  // hydration lifecycle: scheduleOwnedHydrationRetry retires the lifecycle
  // (settling any awaiting owner, cancelling its retry) and arms no further
  // read once this is set, and ensureThread/watchThread return on it - see
  // markThreadDeletedIfFenced. An ordinary transient rejection is still
  // presumed transport and keeps its retry-forever contract.
  deletedRefs: Set<string>;
  /** Cache state follows the owned model through its final release, including
   * mutation pins. A sibling deletion can create a suppression-only record
   * before a claim captures its lease; that record must not block a cold read.
   * Verification clears only shell/anchor, never the lease or suppression. */
  cacheLifetimes: Map<string, CacheLifetime>;
  /** The epoch this tab's in-flight clear armed (spec, "The
   * clear-cached-sessions setting"): while set, the write seam refuses every
   * open lease whose captured epoch predates it. The clear's arm lives here,
   * never in the lifetime's suppression — the commit marks the old leases, the abort
   * just drops the marker — so no abort can touch another source's
   * suppression (a sibling deletion message, the aborted-write backstop)
   * that arrived while the clear was in flight. undefined when no clear is
   * in flight. */
  clearInFlight: number | undefined;
  ensureThread(ref: string): Promise<void>;
  // beforePublish, when given, is evaluated synchronously immediately before
  // this refresh publishes its snapshot. A throw cancels the read's result so
  // a canceled action never republishes over newer authoritative state.
  refreshThread(ref: string, beforePublish?: () => void): Promise<void>;
  // One entry point for the R09 resume sequence (resumeStopBaseline +
  // client.resumeThread + the identity fence + refreshThread), so every trigger
  // - the composer/palette/ask-dock send, the recovery resend, and the session
  // notice's own Resume button - runs the same copy. Resolves when the drive
  // settles; a failure is published to resumeFailures rather than thrown, and
  // the identity-follow effect carries the draft and navigates.
  resumeSession(ref: string): Promise<void>;
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
  // skillNames carries the composer's canonical skill selections for the
  // request. It is gated: a non-empty selection requires the target's
  // advertised capabilities.skillInput to be true, and a refusal throws
  // before anything durable is written (composerMutationIntent's own gate).
  send(
    ref: string,
    text: string,
    attachments?: InputAttachment[],
    skillNames?: readonly string[],
    commandNames?: readonly string[],
    mentions?: readonly ComposerMention[],
  ): Promise<void>;
  steer(
    ref: string,
    text: string,
    attachments?: InputAttachment[],
    skillNames?: readonly string[],
    commandNames?: readonly string[],
    mentions?: readonly ComposerMention[],
  ): Promise<void>;
  queue(
    ref: string,
    text: string,
    attachments?: InputAttachment[],
    skillNames?: readonly string[],
    commandNames?: readonly string[],
    mentions?: readonly ComposerMention[],
  ): Promise<void>;
  interrupt(ref: string): Promise<void>;
  // drainAsSteer atomically appends the composer's current text/attachments
  // (if any) to the input queue, then drains the whole queue into the
  // active turn as one steering message (turn/drainAsSteer, kata 0bq1 Path
  // B) - see this file's own describe block for why `text` is a required
  // param here, not the bare `drainAsSteer(ref)` the plan's terse pseudocode
  // showed.
  drainAsSteer(
    ref: string,
    text: string,
    attachments?: InputAttachment[],
    skillNames?: readonly string[],
    commandNames?: readonly string[],
    mentions?: readonly ComposerMention[],
  ): Promise<void>;
  // Removes one queued message by index and injects it as steering into the
  // in-flight turn (issue #22). expectedEntryId, when non-empty, must match
  // the id the daemon minted for that queue position (QueueState.IDs) - a
  // mismatch (the queue shifted under the caller's snapshot) is a Conflict,
  // never a wrong-message promote.
  promoteQueuedAsSteer(
    ref: string,
    index: number,
    expectedEntryId: string,
    display: PromoteDisplayInput,
  ): Promise<void>;
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
  // Durably enqueues the human shared-note (an empty note clears it). Returns
  // the committed outbox record, not the daemon acknowledgment. Receipt
  // responses and evener/notes/updated publish canonical note state. A note
  // save is never a composer send, so it never takes enqueueMutationIntent's
  // direct fallback (DIRECT_FALLBACK_METHODS) and always resolves with the
  // committed record; enqueueCommittedMutation asserts that a non-send method
  // never returns undefined rather than casting the type.
  setHumanNote(
    ref: string,
    note: string,
    expectedInstanceId?: string,
    onCommitted?: (record: MutationOutboxRecord) => void,
  ): Promise<MutationOutboxRecord>;
  // Removes one session URL list entry by id. The response carries no
  // state; the evener/urls/updated push is the authority. Local list edits
  // on success are the push's business, not this response's — unlike
  // setGoal/setHumanNote there is no response-derived local commit here.
  removeURL(ref: string, id: string): Promise<UrlsRemoveResponse>;
  rename(ref: string, name: string): Promise<void>;
  compact(ref: string): Promise<void>;
  // Clears the thread's conversation through the durable mutation outbox. The
  // daemon's response carries the replacement snapshot; the dispatcher hands
  // it to applyClearResponse before settling the intent so both real and lean
  // views switch to the new instance together.
  clearThread(ref: string): Promise<void>;
  shutdown(ref: string): Promise<void>;
  forceStop(ref: string): Promise<void>;
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
  // Lists the session's task rows. Null is unavailable; [] is an authoritative
  // empty list. The caller adapts the wire fields with parseTaskListData.
  listTasks(ref: string): Promise<TaskListResponse["data"]>;
  // Explicit retained tree reader for callers of evener/jobs/list. Browser
  // activity views use SessionActivityStore; this method returns the wire's
  // untyped data field unchanged and owns no background refresh or retry.
  listJobs(ref: string, continuation?: string): Promise<unknown>;
  // An explicit beforeBytes pages backwards, ending at that lifetime byte
  // offset. Omission selects the latest page; explicit zero stays zero.
  // maxBytes > 0 bounds the window (appwire.JobsOutputParams.MaxBytes) - the
  // activity strip's preview uses it to fetch a couple hundred bytes instead
  // of the daemon's default tail.
  jobOutput(
    ref: string,
    jobId: string,
    beforeBytes?: number,
    maxBytes?: number,
    isCurrent?: () => boolean,
  ): Promise<JobOutputPage>;
  // Reads one job's metadata (evener/jobs/get): the activity-job shape,
  // including the untruncated command.
  jobGet(ref: string, jobId: string, isCurrent?: () => boolean): Promise<JobActivityJob>;
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
// A generation changes at every local human-note request and every accepted
// notes authority (a matching evener/notes/updated or full hydration). Same
// contract as goalUpdateGenerations above: a notes/human/set response may
// publish its derived local state only while its generation is still current,
// so neither a later request nor accepted authoritative state that arrived
// during the await can be overwritten by that delayed response. Per-verb,
// not shared with urls: the two verbs write disjoint fields (humanNote vs
// sessionUrls), and a urls/updated push carries no note state — sharing one
// generation would drop a note commit on an unrelated URL removal.
// Durable ordering survives hydration: an older retry must not displace a
// newer queued note even when both share the current authority generation.
const notesLatestIntentSequences = new Map<string, number>();
const notesUpdateGenerations = new Map<string, number>();

function invalidateNotesResponseFallback(ref: string): void {
  notesUpdateGenerations.set(ref, (notesUpdateGenerations.get(ref) ?? 0) + 1);
}

// Pushes whose acceptance retires a response-derived local commit: the goal
// push retires setGoal's, the notes push retires setHumanNote's. The
// pending-hydration path (targeted sets below) and the steady-state path
// (the per-method blocks in handleNotification) must agree on exactly this
// set, so it lives here rather than inline in both.
function isFallbackInvalidatingPush(method: string): boolean {
  return method === "evener/goal/updated" || method === "evener/notes/updated";
}
const inflightHydrates = new Map<string, Promise<ThreadModel | null>>();
const inflightHydrateClients = new Map<string, AppwireClientLike>();
const inflightHydrateEpochs = new Map<string, number>();

// The session-history cache's singleton storage adapter (web session-history
// cache spec, "The load seam"). One per tab: the lookup, the write seam
// (Task 6) and the clear (Task 10) all ride currentSessionCache() so tests
// can swap the whole storage for a wedged or gated instance.
const sessionCacheAdapter = new SessionCacheIndexedDB();
let sessionCacheAdapterOverride: SessionCacheIndexedDB | undefined;
function currentSessionCache(): SessionCacheIndexedDB {
  return sessionCacheAdapterOverride ?? sessionCacheAdapter;
}
/** Tests swap the singleton for a wedged or gated instance; production never calls this. */
export function setSessionCacheAdapterForTests(adapter: SessionCacheIndexedDB | undefined): void {
  sessionCacheAdapterOverride = adapter;
}
type CacheLookup = { record?: CachedSessionRecord; epoch: number };
const inflightCacheLookups = new Map<string, Promise<CacheLookup | undefined>>();
// The tab's in-memory view of the durable clear epoch: undefined until the
// first lookup observes it. Only durable observations advance it, monotonically.
// Optimistic same-tab clear arming lives separately in clearInFlight.
let tabCacheEpoch: number | undefined;

// One shared bounded lookup per ref. The deadline is the lookup's own
// Promise.race against SESSION_CACHE_LOOKUP_DEADLINE_MS — never the storage
// timeout — and a lost race resolves the join as a miss, discarding whatever
// the adapter answers later.
function joinCacheLookup(ref: string): Promise<CacheLookup | undefined> {
  const existing = inflightCacheLookups.get(ref);
  if (existing) return existing;
  const race = (async () => {
    // The deadline's timer is cleared the moment the race settles: the losing
    // timer's resolve was a no-op by then, so this only keeps an armed timer
    // from outliving the lookup.
    let deadlineTimer: ReturnType<typeof setTimeout> | undefined;
    try {
      return await Promise.race([
        (async (): Promise<CacheLookup | undefined> => {
          const adapter = currentSessionCache();
          const found = await adapter.get(ref, Date.now());
          const epoch = found?.epoch ?? adapter.observedEpoch;
          return epoch === undefined ? undefined : { record: found?.record, epoch };
        })(),
        new Promise<undefined>((resolve) => {
          deadlineTimer = setTimeout(() => resolve(undefined), SESSION_CACHE_LOOKUP_DEADLINE_MS);
        }),
      ]);
    } catch {
      return undefined; // every failure is a miss
    } finally {
      clearTimeout(deadlineTimer);
    }
  })();
  // The cleanup is a reaction registered here — before any awaiter's — so it
  // still runs ahead of every joiner the settlement wakes, exactly as an
  // inline finally would. (It cannot be an inline finally: the IIFE's own
  // body would reference its promise before the assignment completes.)
  const forget = () => {
    if (inflightCacheLookups.get(ref) === race) inflightCacheLookups.delete(ref);
  };
  void race.then(forget, forget);
  inflightCacheLookups.set(ref, race);
  return race;
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
  // The tracked-hydration attempt that owns this pending entry (tracked
  // hydrations only): the per-ref sequence beginThreadHydration stamps from
  // trackedHydrationAttempts, which the Stop-cancellation unwind compares
  // against that map so only the ref's newest attempt may unwind.
  attempt?: number;
  // The model this hydration's latest-window read was issued against
  // (already carrying the bumped history.issuedGeneration - see
  // issuedGenerationFor), when the ref already held a v6 model. undefined for
  // a ref's very first read, or one whose held model predates v6: there is no
  // held history for issueLatestWindowRead/applyReadResponse to work from, so
  // the response goes through hydrateThread's plain-replace path instead.
  baseModel?: ThreadModel;
  // The request generation this hydration's thread/read carries
  // (ThreadReadParams.requestGeneration), echoed by the response and used to
  // discard a superseded ErrorTranscriptHistoryFailed rejection.
  requestGeneration?: number;
};
// A thread/read subscribes before it returns its snapshot. Notifications can
// therefore arrive in the gap between the source subscription and snapshot
// response. Keep the newest hydration's notifications out of the old model,
// then fold them onto the returned snapshot before publishing it.
const pendingThreadHydrations = new Map<string, PendingThreadHydration>();
// Tracked-hydration attempts: one monotonically increasing counter per ref,
// stamped onto each tracked pending and never reset. This is the currency the
// Stop-cancellation unwind requires: only the ref's NEWEST attempt may unwind
// the dispatch gate and recovery obligation, because only it can still speak
// for the state a Stop would cancel. A superseded refresh published nothing
// (publishThreadHydration refuses it), and an overtaken one's banking belongs
// to the newer attempt's fresh proof now, so a stale cancellation re-arming
// recovery would clobber a resume that already succeeded.
const trackedHydrationAttempts = new Map<string, number>();
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
  // stop-cancellation-outbox §6, the cross-tab half: a published model whose
  // thread id differs from the one it replaces is a clear that landed
  // somewhere else - another tab dispatched it, and its response and
  // best-effort removal never reach this tab. The publication is the one clear
  // signal every tab observes on its own (the BroadcastChannel wakeup is a
  // timing hint, never an authority), so whichever tab sees the transition
  // completes the removal, scoped to the instance the transition provably
  // replaced: the current instance's canceled rows keep their explicit-Retry
  // contract.
  const supersededInstances = new Set<string>();
  // The comparison is the fused identity the fence uses (instanceId ??
  // threadId), never threadId alone: a replacement can rotate the instance
  // while retaining the thread id, and RoboRev's fresh review caught exactly
  // that rotation passing a threadId-only check.
  const supersededThreadInstance = threadInstanceID(previousThread);
  const supersededWatchedInstance = threadInstanceID(previousWatched);
  if (
    threadModel &&
    supersededThreadInstance !== undefined &&
    supersededThreadInstance !== threadInstanceID(threadModel)
  )
    supersededInstances.add(supersededThreadInstance);
  if (
    watchedModel &&
    supersededWatchedInstance !== undefined &&
    supersededWatchedInstance !== threadInstanceID(watchedModel)
  )
    supersededInstances.add(supersededWatchedInstance);
  for (const instanceThreadId of supersededInstances) discardSupersededInstanceCanceled(ref, instanceThreadId);
}

// The observing half of §6's cross-tab removal. Observation only: a model
// publication must never be what first mints the mutation runtime (its startup
// scan would then run on the next hydration instead of the first send). The
// no-runtime return below is unreachable in a connected tab: the ready flow
// mints the runtime (rewireClient's direct handleReady call, whose opening
// getMutationRuntime) before any publication that could observe a replacement
// instance completes, and a tab without IndexedDB never mints one but also has
// no durable rows to remove - so the rows this guard could skip are exactly
// none (pinned by the earliest-publication test in threads.test.ts).
function discardSupersededInstanceCanceled(targetRef: string, supersededThreadId: string): void {
  const runtime = mutationRuntime;
  if (!runtime) return;
  void runtime.storage
    .discardCanceledOfInstance(targetRef, supersededThreadId)
    .then(() => {
      if (!isCurrentMutationRuntime(runtime)) return;
      // Rows left the outbox: in-flight reads for the ref are stale.
      noteMutationStateChange(targetRef);
      // Every successful cleanup notifies AND refreshes pins, zero included -
      // the local discard path's own rule. Zero says what THIS tab's write
      // removed, never what another tab removed from under this tab's cached
      // projection or in-memory pin: zero is exactly what another tab's
      // identical cleanup leaves this one, and that other tab's removal may
      // have taken the ref's last durable row without this tab observing it.
      // The notify refreshes the projection; the pin refresh lets a later
      // releaseThread drop a model whose rows are already gone - a stale pin
      // keeps it pinned, and the cleared model leaks. The pin-only variant,
      // never the full refresh: a removal-triggered cleanup has authority
      // over the pin and none over dispatchability - a full refresh can read
      // the stores empty while an enqueue is mid-chain and de-arm a dispatch
      // the chain already scheduled (the supersede-discard listener's rule).
      notifyMutationPersistence([targetRef]);
      void refreshMutationPinAfterRemoval(runtime, targetRef).catch(() => {});
    })
    .catch(() => {});
}

function removeThreadModel(ref: string): void {
  // The memo never outlives the model (spec, "Eviction, cap, and cross-tab"):
  // a memo that survived the release would skip an eligible session
  // indefinitely.
  clearOversizeMemo(ref);
  const removed = threadsStore.getState().threads.get(ref);
  if (removed) removeThreadModelIndex(threadsIndex, removed);
  // The shell-only cache metadata leaves with the model: the fields' whole
  // meaning is "this ref's CURRENT model is an unverified cached shell", so a
  // final release must not leave a ref named with no model behind (Tasks 6/7
  // read them). The clear's suppression and the lease that armed it end with
  // the same final release (spec, "The clear-cached-sessions setting"):
  // premature suppression decays with the final release of each open ref,
  // so the next open of the same ref starts with no lease and no
  // suppression. Even a verified or suppression-only lifetime leaves here.
  threadsStore.setState((s) => {
    if (!s.cacheLifetimes.has(ref)) return s;
    return { cacheLifetimes: releaseCacheLifetime(s.cacheLifetimes, ref) };
  });
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

// This store's own guard: keybindings.ts and transcriptDisplay.ts wire the
// same connectionStore client through their own instances, so the three never
// contend over one shared registration slot.
const readyGenerationCallback = createReadyGenerationCallback();
let dispatchReadyClient: AppwireClientLike | null = null;
let dispatchReadyEpoch = -1;
const pinnedMutationRefs = new Set<string>();
const dispatchableMutationRefs = new Set<string>();
// Per ref, the clientMutationIds of that ref's durably committed rows this tab
// has not yet proven handled. The fallback's ordering guard asks exactly one
// question - does this ref have a committed row that has not been delivered? -
// and this map answers it with the rows themselves rather than a boolean that
// has to be guessed at. Deliberately NOT pinnedMutationRefs: that is a
// retention pin, and a canceled row (which leaves only on an explicit Retry or
// the thread going away) keeps a ref pinned with nothing left to deliver.
//   - a commit whose row is not born-canceled adds that row's id (every writer
//     can name its row: the send funnel from the record, the recovery writes
//     from the record they create or the id they reopen);
//   - a settle that proves delivery (settleReceipt / settleApplied) removes it;
//   - a successful read replaces the ref's ids with the non-canceled rows it
//     observed - also how a persisted row this tab never committed (a reload,
//     another tab) enters the set;
//   - a cancel path that cannot name its rows leaves them until that next
//     successful read;
//   - a failed read changes nothing: guessing "nothing is undelivered" while
//     storage is unreadable is precisely the reorder this guard exists to
//     prevent.
const undeliveredMutationIds = new Map<string, Set<string>>();
// Whether the guard's question is answered yes for a ref, for the places that
// want a bool (the fallback admission, disarmQuiescedMutationArm).
function hasUndeliveredMutation(ref: string): boolean {
  return (undeliveredMutationIds.get(ref)?.size ?? 0) > 0;
}
function noteUndeliveredMutation(ref: string, clientMutationId: string): void {
  const ids = undeliveredMutationIds.get(ref) ?? new Set<string>();
  ids.add(clientMutationId);
  undeliveredMutationIds.set(ref, ids);
}
// A proven delivery, from the two settle writes (see the runtime's storage
// wiring). This is what clears a settled row - no read required, so a settle
// whose clearing refresh fails cannot leave the guard armed forever. Only the
// id removal lives here; the read invalidation is separate and unconditional
// (see noteMutationStateChange), because a settle for a row this tab never
// registered still changed the ref's outbox.
function noteHandledMutation(clientMutationId: string): void {
  for (const [ref, ids] of undeliveredMutationIds) {
    if (!ids.delete(clientMutationId)) continue;
    if (ids.size === 0) undeliveredMutationIds.delete(ref);
  }
}
// A successful read is the authoritative snapshot of the ref: its ids are
// exactly the rows that read observed as non-canceled work.
function replaceUndeliveredMutations(ref: string, clientMutationIds: Iterable<string>): void {
  const ids = new Set(clientMutationIds);
  if (ids.size === 0) undeliveredMutationIds.delete(ref);
  else undeliveredMutationIds.set(ref, ids);
}
// The rule, in one place: ANY change to a ref's durable outbox state advances
// that ref's generation of read invalidation, so a read that began before the
// change discards its snapshot instead of restoring what the change resolved
// (or dropping what it added). Whoever performs the change names the ref: a
// commit, a settle (the storage reports the settled record's own ref), a cancel
// or a removal. A read's own reconciliation does not advance: reads are what
// the generation is compared against, and advancing there would let a newer
// read's snapshot be discarded by an older one.
const mutationStateGenerations = new Map<string, number>();
function noteMutationStateChange(ref: string): void {
  mutationStateGenerations.set(ref, (mutationStateGenerations.get(ref) ?? 0) + 1);
}
// Per-ref count of durable enqueues whose write is still in flight (from the
// click until enqueueDurableMutation settles). dispatchableMutationRefs cannot
// serve this: a background pin refresh clears a ref's arm when the outbox reads
// empty, which it does while another enqueue's write is still uncommitted, so an
// arm is not proof that a concurrent enqueue is outstanding. This count is that
// proof, and only the fallback's ordering guard reads it.
const inflightDurableEnqueues = new Map<string, number>();
const olderPageGenerations = new Map<string, number>();

// Pane, watch and durable outbox holders share one local lease. Activity views
// hold their own leases on the same routing ref and actual client identity.
const threadSubscriptions = new Map<string, { client: AppwireClientLike; lease: ThreadSubscriptionLease }>();

function syncThreadSubscription(ref: string, client = wiredClient): ThreadSubscriptionLease | undefined {
  const previous = threadSubscriptions.get(ref);
  const held = (refCounts.get(ref) ?? 0) > 0 || (watchRefCounts.get(ref) ?? 0) > 0 || pinnedMutationRefs.has(ref);
  if (previous && (!held || previous.client !== client)) {
    previous.lease.release();
    threadSubscriptions.delete(ref);
  }
  if (!held || !client) return;
  const existing = threadSubscriptions.get(ref);
  if (existing) return existing.lease;
  const lease = acquireThreadSubscription(client, ref);
  threadSubscriptions.set(ref, { client, lease });
  return lease;
}

function releaseThreadSubscriptions(): void {
  for (const { lease } of threadSubscriptions.values()) lease.release();
  threadSubscriptions.clear();
}

function pinMutationRef(ref: string): void {
  pinnedMutationRefs.add(ref);
  syncThreadSubscription(ref);
}

function unpinMutationRef(ref: string): void {
  pinnedMutationRefs.delete(ref);
  syncThreadSubscription(ref);
}

interface MutationRuntime {
  storage: MutationOutboxIndexedDB;
  // Bound to the attachment shape this host stages, like the outbox above.
  dispatcher: MutationDispatcher<MutationAttachment>;
  // The web's attachments carry bytes, so its outbox is the shared class bound
  // to the record shape with a Blob in it.
  outbox: MutationOutbox<MutationAttachment>;
  start: Promise<void>;
  active: boolean;
}

let mutationRuntime: MutationRuntime | null = null;
let mutationStorageForTests: MutationOutboxIndexedDB | null = null;
let createMutationBroadcastChannelForTests: NonNullable<MutationOutboxOptions["createBroadcastChannel"]> | undefined;

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

async function applyClearResponse(targetRef: string, response: ThreadClearResponse): Promise<void> {
  invalidateGoalResponseFallback(targetRef);
  // The cleared model must never publish while the ref's canceled rows are
  // still durable: a Retry pressed against the published state, or a
  // discovery scan, would otherwise face a thread that claims to be cleared
  // but still holds live canceled rows in storage. The discard stays
  // best-effort - a failed write keeps the rows and publishes anyway - and
  // retryBlockedMutation's press-time refusal is what closes that residue.
  await discardCanceledMutations(targetRef);
  const now = Date.now();
  const model = hydrateThread({ thread: response.thread }, targetRef, now);
  // A clear replaces the history outright — the one mid-lifetime shrink that
  // clears the oversize memo (spec: "replacement is the only mid-lifetime
  // shrink"; a merge only adds items).
  clearOversizeMemo(targetRef);
  // The session-content clear's cache hook: the cleared model is a bare
  // hydrate with no history, so it would never match the write gates;
  // deleting the record in the same step is what keeps a cleared session's
  // next reload from painting pre-clear content from the shell.
  cancelCacheWrite(targetRef);
  void currentSessionCache().deleteRecords([targetRef]);
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
  threadsStore.setState((state) => {
    // This authoritative replacement retired the shell's pending read, so
    // publishThreadHydration will never clear its verification metadata.
    const cacheLifetimes = releaseCacheShell(state.cacheLifetimes, targetRef);
    const mutationAuthorityRefs = new Set(state.mutationAuthorityRefs);
    if (response.thread.evener.mutationStateAuthoritative === true) mutationAuthorityRefs.add(targetRef);
    else mutationAuthorityRefs.delete(targetRef);
    return {
      cacheLifetimes,
      mutationAuthorityRefs,
      hydrations: stateBefore.threads.has(targetRef)
        ? new Map(state.hydrations).set(targetRef, (state.hydrations.get(targetRef) ?? 0) + 1)
        : state.hydrations,
    };
  });
}

// currentDispatchClient is the dispatch admission: the wired client and its
// epoch must still be current, and the named ref must pass its fences. The
// `requireArmed` clause is the dispatcher's own precondition - a ref is
// dispatched only while it is in dispatchableMutationRefs, the set of refs with
// durable work waiting. enqueueMutationIntent's fallback asks the SAME question
// with requireArmed false: it has no durable row of its own, so it cannot be in
// that set for its own sake, and the clause's absence only means "the client is
// current and the ref is not fenced" - the admission the fallback needs. The
// refs that DO hold durable work are covered separately, by the fallback's own
// ordering guard: undeliveredMutationIds (the committed rows this tab has not
// proven delivered) plus inflightDurableEnqueues (a durable enqueue for the ref
// still in flight).
function currentDispatchClient(
  targetRef?: string,
  method?: string,
  requireArmed = true,
  options?: { storageBlockedAdmissible?: boolean },
): AppwireClientLike | null {
  if (wiredClient !== dispatchReadyClient || readyEpoch !== dispatchReadyEpoch) return null;
  if (targetRef && requireArmed && !dispatchableMutationRefs.has(targetRef)) return null;
  const state = threadsStore.getState();
  // A storage-blocked ref (its reconcile failed only because storage would not
  // answer) is fenced here like any reconcile failure: the durable dispatcher
  // waits for discovery's retry to reconcile before it can order anything.
  // The send fallback is the one caller that may admit it
  // ({ storageBlockedAdmissible }): a storage wedge must not strand a send,
  // and the fallback's own in-memory ordering guards carry the ordering
  // responsibility instead. Admitting the ref also waives the reconcile still
  // pending for it - that read sits on the same wedge, so waiting for it
  // cannot produce ordering facts anyway.
  const storageBlocked = targetRef !== undefined && state.mutationReconciliationStorageBlocked.has(targetRef);
  const storageBlockedWaived = storageBlocked && options?.storageBlockedAdmissible === true;
  if (
    targetRef &&
    (((storageBlocked || pendingMutationReconciliations.has(targetRef)) && !storageBlockedWaived) ||
      // A Force stop this page started is its own fence for its whole drain
      // window: the hub holds Stopping > 0 and refuses EVERY method there,
      // turn/start included (cmd/evener-hub's sessionActionRecoveryError). Read
      // independently of restartBlockingObligations, which a stale thread
      // refresh can clear while the RPC is still in flight.
      state.stoppingRefs.has(targetRef) ||
      // A merely-resumable session's send must dispatch: the hub folds its
      // resume into turn/start, so the obligation does not park the mutation.
      // Only turn/start is carved out of the hub's recovery admission, though,
      // so the exemption holds only for that method: the dispatcher names the
      // record it is about to send (dispatcher.ts's method-aware lookup), and a
      // queued turn/queue/turn/steer/turn/interrupt parks instead of meeting a
      // refusal. The ref-less and pre-record calls pass no method, where the
      // exemption is the readiness answer they always were. Every other
      // obligation (Stop drain, restartRequired) still blocks it.
      //
      // This gate reads resumeOnlyLocalDispatchable, NOT resumeOnlyLocalModel:
      // dispatch asks "is this exact head record admissible?". At the
      // head-of-loop lookup the named record may itself be the turn/start with
      // nothing ahead of it, while resumeOnlyLocalModel's hasQueuedNonSend
      // clause answers the ADMISSION question ("may a NEW send be minted behind
      // this queue?"), where a queued non-send row is genuinely ahead of the
      // candidate. Used here that clause refused the head turn/start before the
      // method-aware recheck could name it, so a ref holding [turn/start,
      // turn/queue] could neither resume nor drain its outbox. The method check
      // below still parks the tail non-send row.
      (state.restartBlockingObligations.has(targetRef) &&
        !(resumeOnlyLocalDispatchable(targetRef) && (method === undefined || method === "turn/start"))) ||
      state.mutationReconciliationFailures.has(targetRef))
  )
    return null;
  if (targetRef && state.threads.get(targetRef)?.status.type === "restartRequired") return null;
  return wiredClient?.state === "ready" ? wiredClient : null;
}

function dropUnpinnedModel(ref: string): void {
  if (pinnedMutationRefs.has(ref) || (refCounts.get(ref) ?? 0) > 0) return;
  // The pinned drain is the path a pinned ref's model finally leaves through
  // (releaseThread returns early while pinned), so the flush runs here too —
  // ordered before the model leaves the map, on the pre-removal snapshot —
  // and the memo dies with the model.
  flushCacheWrite(ref);
  clearOversizeMemo(ref);
  // Nothing owns this ref any more, so no scheduled retry may outlive it.
  retireOwnedHydration("thread", ref);
  // The model is leaving `threads` here, so its index membership leaves with
  // it (see putThreadModel/removeThreadModel — the membership paths).
  const dropped = threadsStore.getState().threads.get(ref);
  if (dropped) removeThreadModelIndex(threadsIndex, dropped);
  // The clear's suppression and its lease end with this same final release
  // (removeThreadModel's own rule): premature suppression decays here too,
  // so a ref the pinned drain retires never stays suppressed with no model.
  threadsStore.setState((state) => {
    if (
      !state.threads.has(ref) &&
      !state.frameTimes.has(ref) &&
      !state.hydrations.has(ref) &&
      !state.cacheLifetimes.has(ref)
    )
      return state;
    const threads = new Map(state.threads);
    threads.delete(ref);
    const frameTimes = new Map(state.frameTimes);
    frameTimes.delete(ref);
    const hydrations = new Map(state.hydrations);
    hydrations.delete(ref);
    const cacheLifetimes = releaseCacheLifetime(state.cacheLifetimes, ref);
    return { threads, frameTimes, hydrations, cacheLifetimes };
  });
}

async function refreshMutationPins(runtime: MutationRuntime, targetRefs: Iterable<string>): Promise<void> {
  if (!isCurrentMutationRuntime(runtime)) return;
  for (const targetRef of targetRefs) {
    if (!isCurrentMutationRuntime(runtime)) return;
    const generation = mutationStateGenerations.get(targetRef) ?? 0;
    // A failed read proves nothing, so it changes nothing: guessing "nothing is
    // undelivered" here is what let a later storage-timeout send dispatch ahead
    // of a committed row whose delivery no one had proven. The rejection still
    // propagates, so a caller that relied on it (the hydration reconciliation
    // aborts on it) keeps that flow.
    const [outbox, optimistic] = await Promise.all([
      runtime.storage.listOutbox(targetRef),
      runtime.storage.listOptimistic(targetRef),
    ]);
    if (!isCurrentMutationRuntime(runtime)) return;
    // A durable commit for this ref since the read began means this snapshot
    // predates it: discard rather than replace the commit's own ids.
    if ((mutationStateGenerations.get(targetRef) ?? 0) !== generation) continue;
    // A successful read is the authoritative snapshot: the ref's ids are
    // exactly the non-canceled rows this read observed.
    replaceUndeliveredMutations(
      targetRef,
      outbox.filter((record) => record.state !== "canceled").map((record) => record.clientMutationId),
    );
    if (outbox.length > 0) {
      pinMutationRef(targetRef);
      continue;
    }
    if (optimistic.length > 0) {
      pinMutationRef(targetRef);
      // An optimistic row is accepted (settled), never undelivered work - the
      // empty outbox above already says so in the ids.
      dispatchableMutationRefs.delete(targetRef);
      continue;
    }
    unpinMutationRef(targetRef);
    dispatchableMutationRefs.delete(targetRef);
    dropUnpinnedModel(targetRef);
  }
}

// The pin half of refreshMutationPins alone, for the supersede-discard
// listener below: its trigger — rows just left the store through a
// fire-and-forget cleanup — has authority over whether the ref still needs
// its model pinned, and NONE over whether the ref is dispatchable. An
// enqueue mid-chain (a parked save replaying against a deadline, exactly the
// note editor's gated-save staging) re-arms the ref BEFORE its durable write
// commits, so a full refresh here can read the stores empty and de-arm a
// dispatch the enqueue chain already scheduled — the pinned-save tests pin
// that ordering. A pin left armed with no rows costs one no-op dispatch
// turn on the next discovery; a dispatch killed here costs the user's save.
async function refreshMutationPinAfterRemoval(runtime: MutationRuntime, targetRef: string): Promise<void> {
  if (!isCurrentMutationRuntime(runtime)) return;
  const generation = mutationStateGenerations.get(targetRef) ?? 0;
  // As in refreshMutationPins: a failed read proves nothing and changes
  // nothing, so nothing here catches it.
  const [outbox, optimistic] = await Promise.all([
    runtime.storage.listOutbox(targetRef),
    runtime.storage.listOptimistic(targetRef),
  ]);
  if (!isCurrentMutationRuntime(runtime)) return;
  // As in refreshMutationPins: a commit since the read began outranks it.
  if ((mutationStateGenerations.get(targetRef) ?? 0) !== generation) return;
  replaceUndeliveredMutations(
    targetRef,
    outbox.filter((record) => record.state !== "canceled").map((record) => record.clientMutationId),
  );
  if (outbox.length > 0 || optimistic.length > 0) {
    pinMutationRef(targetRef);
    return;
  }
  unpinMutationRef(targetRef);
  dropUnpinnedModel(targetRef);
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

// The dispatch gate a durable projection read opens. A resume-only ref's head
// send parks while its ref's outbox read is in flight (the pending-turns
// projection fails readiness closed through that window, and the predicate
// reads it), so the read resolving is what unparks it. Nothing else re-attempts
// that dispatch, so the projection hands the refs that (re)gained readiness
// here - the same explicit send-aside publishAndReconcileThreadHydration makes
// when its reconciliation opens the gate. A ref with no dispatchable mutation
// is a no-op.
export function notifyReadyForMutationDispatch(refs: Iterable<string>): void {
  const runtime = getMutationRuntime();
  if (!runtime) return;
  scheduleMutationDispatch(runtime, refs);
}

function handleDiscoveredMutations(runtime: MutationRuntime, targetRefs: Iterable<string>): void {
  if (!isCurrentMutationRuntime(runtime)) return;
  const state = threadsStore.getState();
  // Storage-blocked refs ride the same retry: the wedge that failed their
  // reconcile lifts with storage itself, and this pass is what re-reads the
  // outbox and re-arms the ref once it does.
  const refs = [
    ...new Set([...targetRefs, ...state.mutationReconciliationFailures, ...state.mutationReconciliationStorageBlocked]),
  ];
  for (const targetRef of refs) pinMutationRef(targetRef);
  notifyMutationPersistence(refs);
  scheduleMutationDispatch(runtime, refs);

  const client = currentDispatchClient();
  if (!client) return;
  const epoch = dispatchReadyEpoch;
  for (const targetRef of refs) {
    if (pendingMutationReconciliations.has(targetRef)) continue;
    const refState = threadsStore.getState();
    if (
      dispatchableMutationRefs.has(targetRef) &&
      !refState.mutationReconciliationFailures.has(targetRef) &&
      // A storage-blocked ref must fall through to handleReady's reconcile
      // retry, not take the authority refresh: that refresh's outbox read
      // would wedge on the same storage, and only a successful reconcile
      // clears the storage-blocked fence.
      !refState.mutationReconciliationStorageBlocked.has(targetRef)
    ) {
      // Another tab can block a shared record after this tab's snapshot.
      // Cached authority cannot settle that newly uncertain send.
      void refreshUncertainMutationAuthority(runtime, client, epoch, targetRef).catch(() => {
        // The next discovery pass retries after storage or transport recovers.
      });
      continue;
    }
    const pending = pendingThreadHydrations.get(targetRef);
    if (pending?.client === client && pending.epoch === epoch) continue;
    void handleReady(client, epoch, targetRef);
  }
}

async function refreshUncertainMutationAuthority(
  runtime: MutationRuntime,
  client: AppwireClientLike,
  epoch: number,
  targetRef: string,
): Promise<void> {
  const records = await runtime.storage.listOutbox(targetRef);
  if (!records.some((record) => record.state === "blockedUnknown")) return;
  if (!isCurrentMutationRuntime(runtime) || currentDispatchClient() !== client || dispatchReadyEpoch !== epoch) return;
  if (pendingMutationReconciliations.has(targetRef) || pendingThreadHydrations.has(targetRef)) return;
  await handleReady(client, epoch, targetRef);
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
  // A proven delivery is what resolves a committed row's id: the two settle
  // writes are the only place this tab learns that a committed row reached the
  // daemon. `settleReceipt` is the dispatcher's receipt commit and
  // `settleApplied` the live-submission reconciliation. This is what clears a
  // settled row: no read has to succeed for the guard to stand down. The
  // invalidation is unconditional and comes with the settled record's own ref,
  // so a settle for a row this tab never registered still invalidates that
  // ref's in-flight reads (see noteMutationStateChange).
  const settleReceipt = storage.settleReceipt.bind(storage);
  storage.settleReceipt = async (clientMutationId, projectionState) => {
    const settled = await settleReceipt(clientMutationId, projectionState);
    if (settled) noteHandledMutation(clientMutationId);
    return settled;
  };
  const settleApplied = storage.settleApplied.bind(storage);
  storage.settleApplied = async (clientMutationId) => {
    const settled = await settleApplied(clientMutationId);
    if (settled) noteHandledMutation(clientMutationId);
    return settled;
  };
  storage.setSettledListener((targetRef) => noteMutationStateChange(targetRef));
  // §6's note-row supersede discard commits fire-and-forget AFTER the settle
  // that spawned it has already notified — the dispatcher's post-settlement
  // refresh may have completed before the cleanup's write does, and a
  // fire-and-forget write no other path observes leaves the removal invisible
  // to this runtime's projections and pins (the store's supersede-discard
  // test pins the notification; the pin refresh lets a releaseThread drop a
  // model whose row just left). Zero-deletion cleanups included: another tab
  // may have removed the rows, and zero is what leaves this tab's cached
  // projection and pin stale.
  storage.setSupersededDiscardListener((targetRef) => {
    if (!isCurrentMutationRuntime(runtime)) return;
    notifyMutationPersistence([targetRef]);
    void refreshMutationPinAfterRemoval(runtime, targetRef).catch(() => {});
  });
  // One client lookup for both halves of the mutation runtime: the dispatcher
  // asks it per target ref (with the record's method, for the resume-only
  // carve-out), and the outbox asks it ref-less for "is any client ready right
  // now". Wiring them from one function is what keeps the dispatcher's
  // readiness and the outbox's from drifting apart; the method is optional so
  // the same function satisfies the outbox's ref-only lookup too.
  const getClient = (targetRef?: string, method?: string): AppwireClientLike | null =>
    isCurrentMutationRuntime(runtime) ? currentDispatchClient(targetRef, method) : null;
  const dispatcher = new MutationDispatcher(storage, {
    getClient,
    onStorageChange: (targetRefs) => {
      // The dispatcher's own state changes - a settle, a recovery transfer, a
      // blocked reclassification - invalidate in-flight reads for every ref it
      // names, on the rule noteMutationStateChange states.
      for (const targetRef of targetRefs) noteMutationStateChange(targetRef);
      if (isCurrentMutationRuntime(runtime)) notifyMutationPersistence(targetRefs);
    },
    onBlockedMutation: (targetRef, client) => {
      if (!isCurrentMutationRuntime(runtime) || currentDispatchClient() !== client) return;
      threadsStore.setState((state) => {
        const mutationAuthorityRefs = new Set(state.mutationAuthorityRefs);
        mutationAuthorityRefs.delete(targetRef);
        // The durable blocking write may have failed, leaving a submitting
        // record. New enqueues must wait for successful reconciliation too.
        return {
          mutationAuthorityRefs,
          mutationReconciliationFailures: new Set(state.mutationReconciliationFailures).add(targetRef),
        };
      });
      // Let the periodic discovery pass refresh and reconcile. An immediate
      // read can prove absence while journal writes still fail, creating a
      // read/retry loop without giving persistence time to recover.
      dispatchableMutationRefs.delete(targetRef);
    },
    onClearResponse: applyClearResponse,
    onHumanNoteReconciled: (record) => {
      if (!isCurrentMutationRuntime(runtime)) return;
      const model = trackedThreadModel(record.targetRef);
      if (model) acknowledgeHumanNote(record, model.humanNote);
    },
    prepareHumanNoteResponse: (record) => {
      const ref = record.targetRef;
      const generation = notesUpdateGenerations.get(ref);
      notesLatestIntentSequences.set(ref, Math.max(notesLatestIntentSequences.get(ref) ?? 0, record.intentSequence));
      return (response) => {
        if (!isCurrentMutationRuntime(runtime)) return;
        const current =
          notesUpdateGenerations.get(ref) === generation &&
          notesLatestIntentSequences.get(ref) === record.intentSequence;
        if (current) {
          threadsStore.setState((state) => ({
            threads: replaceThread(state.threads, ref, (model) => ({ ...model, humanNote: response.note })),
            watchedThreads: replaceThread(state.watchedThreads, ref, (model) => ({
              ...model,
              humanNote: response.note,
            })),
          }));
        }
        acknowledgeHumanNote(record, current ? response.note : (trackedThreadModel(ref)?.humanNote ?? response.note));
      };
    },
  });
  const outbox = new MutationOutbox(storage, {
    getClient,
    onDiscover: (targetRefs) => {
      if (runtime) handleDiscoveredMutations(runtime, targetRefs);
    },
    // The browser's own discovery capabilities, named here rather than reached
    // for inside the shared class: sibling tabs over BroadcastChannel, the
    // window's online/focus, the document's visibility, and a timer. A host
    // without them passes nothing and the class does nothing with them.
    createBroadcastChannel: createMutationBroadcastChannelForTests ?? ((name) => new BroadcastChannel(name)),
    lifecycleWindow: typeof window === "undefined" ? undefined : window,
    lifecycleDocument: typeof document === "undefined" ? undefined : document,
    setInterval: (callback, milliseconds) => globalThis.setInterval(callback, milliseconds),
    clearInterval: (intervalId) => globalThis.clearInterval(intervalId),
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

const userIntentStopGenerations = new Map<string, number>();
// Stop generations come from one page-wide sequence, never a per-ref counter,
// and an entry once written persists for the page session - releaseThread
// deliberately does NOT prune. Deleting the only cancellation evidence let a
// fence miss a real Stop (baseline 0, Stop nonzero, release deletes, fence
// reads 0); tombstones with a clearing schedule need a fence-lifetime
// registry that does not exist, and one that never clears is this. Growth is
// bounded by the distinct refs a user actually Stops in the page's lifetime.
// Sequence values are forever-unique and start at 1, so an absent entry's
// `?? 0` stays below every real generation, an increased entry is exactly a
// landed Stop, and a Stop after a re-ensure never recycles an older value.
let userIntentStopSequence = 0;

function cancelPendingUserIntents(ref: string): void {
  userIntentStopGenerations.set(ref, ++userIntentStopSequence);
}

// Marks/clears a Force stop this page started as in flight. A fresh Set each
// change (never mutating the initial state's Set) so a subscriber selects on
// the boolean and re-renders when the drain begins and ends.
function markStopping(ref: string, stopping: boolean): void {
  threadsStore.setState((state) => {
    if (state.stoppingRefs.has(ref) === stopping) return state;
    const stoppingRefs = new Set(state.stoppingRefs);
    if (stopping) stoppingRefs.add(ref);
    else stoppingRefs.delete(ref);
    return { stoppingRefs };
  });
}

// The refcounted group of Force stops this module has in flight for a ref.
// markStopping is a boolean Set with no ownership: two overlapping stops would
// clear the fence when the FIRST completed, while the second was still draining,
// and a stale thread/read refresh could then clear the restart obligation too -
// reopening mutation dispatch for the rest of the stop window. A bare count
// fixes that early clear but not the shared recovery obligation: each stop
// capturing the map's value and restoring it on its own abort can clobber a
// fence a sibling still owns, or write back a sibling's arming symbol. So the
// group records the pre-group obligation once, on the 0 -> 1 transition, and
// whether any member signalled a daemon, and settles both on the 1 -> 0
// transition.
interface StopGroup {
  count: number;
  // The obligation the ref held before the group's first stop armed its own
  // fence. endStop restores it only when no member of the group reached a
  // daemon.
  previousObligation: symbol | undefined;
  // The exact symbol this group armed on its 0 -> 1 transition. endStop
  // restores/deletes the ref's obligation only while the ref still holds THIS
  // symbol: a concurrent hydration can arm a fresh fence during an aborting
  // stop's cancellation await, and only a still-owned entry is the group's to
  // settle. Otherwise the group leaves the newer fence in place.
  ownObligation: symbol;
  // Whether any member reached client.forceStop, i.e. signalled a daemon. A
  // group that did keeps its armed fence (a later snapshot clears it); one that
  // never did restores previousObligation exactly.
  signalled: boolean;
}
const activeStops = new Map<string, StopGroup>();

// Arm the stopping fence and the recovery obligation on the 0 -> 1 transition
// only: every overlapping stop shares the one group, and endStop settles it once
// the count returns to 0. Capturing the pre-group obligation here - not per
// call - is what keeps an aborting stop from restoring a value another stop
// still owns.
function beginStop(ref: string): void {
  const existing = activeStops.get(ref);
  if (existing) {
    existing.count += 1;
    return;
  }
  const previousObligation = threadsStore.getState().restartBlockingObligations.get(ref);
  const ownObligation = Symbol();
  activeStops.set(ref, { count: 1, previousObligation, ownObligation, signalled: false });
  markStopping(ref, true);
  threadsStore.setState((state) => ({
    restartBlockingObligations: new Map(state.restartBlockingObligations).set(ref, ownObligation),
  }));
}

// Settle one member's return. `signalled` is true when the member reached
// client.forceStop (even if the RPC then rejected: the signal may still have
// reached the daemon). On the 1 -> 0 transition restore the pre-group obligation
// only when no member of the group ever signalled; a group that signalled keeps
// the fence armed until a later snapshot clears it, and no aborting member
// writes back its own captured value over an entry a sibling still owns. The
// ownership test is the group's own arming symbol, not merely "no sibling": a
// hydration can arm a fresh fence during the aborting member's cancellation
// await, and that newer fence must survive the abort untouched.
function endStop(ref: string, signalled: boolean): void {
  const group = activeStops.get(ref);
  if (!group) return;
  if (signalled) group.signalled = true;
  group.count -= 1;
  if (group.count > 0) return;
  activeStops.delete(ref);
  markStopping(ref, false);
  if (group.signalled) return;
  threadsStore.setState((state) => {
    // A hydration (publishAndReconcileThreadHydration) can arm a fresh
    // obligation during this aborting stop's cancellation await. Only a ref
    // still holding this group's own fence is the group's to restore; a newer
    // fence is left exactly as the hydration wrote it, rather than overwritten
    // with a stale captured value or deleted outright.
    if (state.restartBlockingObligations.get(ref) !== group.ownObligation) return {};
    const restartBlockingObligations = new Map(state.restartBlockingObligations);
    if (group.previousObligation === undefined) restartBlockingObligations.delete(ref);
    else restartBlockingObligations.set(ref, group.previousObligation);
    return { restartBlockingObligations };
  });
}

// The explicit Resume action is the one user intent that still starts a daemon
// directly, and a Stop acknowledged while its reconnect or post-resume
// hydration is in flight must cancel it. Production fences that action through
// resumeStopBaseline below -- resume can return a NEW identity a per-ref fence
// captured beforehand cannot name -- so this per-ref form now serves the
// single-ref checks that remain (the tests pinning refreshThread's
// beforePublish window among them) and delegates to the baseline's per-ref
// compare rather than re-implementing it.
export function resumeStopFence(ref: string): () => void {
  const baseline = resumeStopBaseline();
  return () => baseline(ref);
}

// Resume can return a NEW identity, and that identity can be named by a Stop
// before resumeThread resolves: any surface already tracking the resumed ref
// (a prior load, a list row hydrated from hub state) records its Stop during
// the reconnect window. A fence captured after the resolve would take that
// Stop as its baseline and never fire. Snapshot every generation before the
// resume starts instead; the returned check fences any ref -- the old one the
// pane still shows and the new one the resume returns -- against its
// pre-resume baseline.
//
// Called with NO ref, the check fences globally: any generation that has
// increased since the snapshot throws. beforeRequest runs before the resumed
// identity is knowable, so a per-ref fence cannot name it -- any ref's
// acknowledged Stop in the reconnect window suppresses the resume RPC. Values
// only grow (the page-wide sequence), so an increased entry is exactly a
// landed Stop; a released ref's retained entry compares equal to its
// snapshot, which is correct -- release is not a Stop.
export function resumeStopBaseline(): (ref?: string) => void {
  const generations = new Map(userIntentStopGenerations);
  return (ref?: string) => {
    if (ref === undefined) {
      for (const [stoppedRef, generation] of userIntentStopGenerations) {
        if (generation > (generations.get(stoppedRef) ?? 0))
          throw new Error("Stop canceled this pending action; send again when ready.");
      }
      return;
    }
    if ((userIntentStopGenerations.get(ref) ?? 0) !== (generations.get(ref) ?? 0))
      throw new Error("Stop canceled this pending action; send again when ready.");
  };
}

// The store-driven resume for the Send-resumes face (R09). A send on a fenced
// ref parks in the durable outbox (enqueueMutationIntent admits its turn/start),
// and this driver runs the same sequence the standalone Resume button ran
// (Session.tsx's RestartRequiredNotice): snapshot every Stop generation before
// the resume starts, launch the resume with that global fence as beforeRequest,
// then re-hydrate the resumed identity behind a fence over BOTH refs - the old
// one the pane still shows and the new one the resume returned, which can be
// named by a Stop while the resume RPC or its hydration is in flight. The
// hydration's reconciliation clears the obligation, and the dispatcher's tails
// drain the parked row exactly once.
//
// Idempotent per ref: one resumeThread at a time (AppwireClient itself rejects a
// second), so a second press while a drive is running parks its row and rides
// the first drive's reconciliation. A failed drive publishes its reason for the
// press surface to toast, and is re-drivable by the next press.
let resumeFailureSeq = 0;
// Per ref, the ONE in-flight drive. Concurrent callers (a second send, the
// notice's Resume press) await the same promise, so they ride the same retarget,
// hydration, and publication instead of starting a second resumeThread.
const inFlightResumes = new Map<string, Promise<void>>();

async function resumeFencedForSend(ref: string): Promise<void> {
  const existing = inFlightResumes.get(ref);
  if (existing !== undefined) return existing;
  const drive = runResumeFencedForSend(ref);
  inFlightResumes.set(ref, drive);
  try {
    await drive;
  } finally {
    if (inFlightResumes.get(ref) === drive) inFlightResumes.delete(ref);
  }
}

async function runResumeFencedForSend(ref: string): Promise<void> {
  // Hoisted for the catch: which identity this drive reached, what its first
  // retarget moved, whether a Stop won the identity fence, and the token that
  // identifies this drive's swap registration.
  let resumedRef: string | undefined;
  let movedFirst: RetargetedMutations | null = null;
  let stopWon = false;
  let swapToken: symbol | undefined;
  try {
    const client = wiredClient;
    if (client === null || client.state !== "ready")
      throw new Error("Connect to the hub before resuming this session.");
    const stopBaseline = resumeStopBaseline();
    const { thread } = await client.resumeThread(ref, { beforeRequest: stopBaseline });
    resumedRef = thread.evener.ref;
    // The token the daemon's instance fence compares (appwire_runtime's
    // expectedInstanceID vs appThreadID), read the same fused way a fresh send
    // derives it (instanceId ?? threadId). Taken off the resume RESPONSE: the
    // resumed ref is usually not tracked yet, so the store has no model for it.
    const resumedIdentity = { threadId: thread.id, instanceId: thread.evener.instanceId };
    // The fence over BOTH refs runs before and after the hydration, exactly as
    // the button's own identityFence did: a Stop against either ref cancels the
    // publish. The pane only follows the new identity once the hydration has
    // survived that fence, so a canceled resume leaves it where it was. It is
    // also the drive's Stop signal: whichever evaluation throws - here, or the
    // one refreshThread runs before and after publication - records that a Stop
    // won, so the catch distinguishes a Stop from a transport failure without
    // matching error text.
    const identityFence = () => {
      try {
        stopBaseline(ref);
        stopBaseline(resumedRef);
      } catch (error) {
        stopWon = true;
        throw error;
      }
    };
    identityFence();
    // The old identity is superseded: move every row still parked under it to
    // the resumed ref BEFORE the hydrated read, so that read reconciles the rows
    // and the dispatch tails drain the pressed send exactly once under the new
    // identity. The swap is registered BEFORE the move, so a Stop landing inside
    // the move's window still reaches the resumed ref through it.
    if (resumedRef !== ref) {
      swapToken = registerResumeSwap(ref, resumedRef);
      movedFirst = await retargetParkedMutations(ref, resumedRef, resumedIdentity);
    }
    await threadsStore.getState().refreshThread(resumedRef, identityFence);
    identityFence();
    if (resumedRef !== ref) {
      // A send enqueued after the first snapshot while this drive was in flight
      // rode this same promise and parked under the old ref. Move those late
      // rows too, draining until a pass moves nothing.
      //
      // The old ref's restart obligation is NOT cleared by this drive:
      // publishThreadHydration clears only the HYDRATED ref's obligation
      // (threads.ts's reconciliation tail), and the hydration here is the
      // resumed ref. So a concurrent send on the old ref can still be admitted
      // and park while we drain. The inline loop stops at the first pass that
      // moves nothing, but a row committed inside THAT pass's own read window is
      // not in its snapshot, so the trailing pass below is scheduled
      // unconditionally - not only when the loop reaches its cap.
      for (let pass = 0; pass < RETARGET_DRAIN_PASSES; pass += 1) {
        if ((await retargetParkedMutations(ref, resumedRef, resumedIdentity)) === null) break;
      }
      // scheduleRetargetDrain queues a microtask, and this drive's remaining
      // synchronous work (the resumedIdentities publication just below, then the
      // return) completes before the microtask can run - so the trailing pass
      // fires after this drive has published the identity, and it is the last
      // drain this drive performs (it re-schedules itself only while rows keep
      // moving). Navigation is a later React effect, so the trailing pass can
      // still precede the pane swap; a press that lands after it starts a fresh
      // drive once this one has settled, so its row is still moved.
      if (swapToken !== undefined) scheduleRetargetDrain(ref, resumedRef, resumedIdentity, swapToken);
      const swapTo = resumedRef;
      threadsStore.setState((state) => ({
        resumedIdentities: new Map(state.resumedIdentities).set(ref, swapTo),
      }));
    }
    // The drive succeeded: drop any earlier failure for this ref so a later
    // composer mount does not re-toast a superseded one.
    threadsStore.setState((state) => {
      if (!state.resumeFailures.has(ref)) return {};
      const resumeFailures = new Map(state.resumeFailures);
      resumeFailures.delete(ref);
      return { resumeFailures };
    });
  } catch (error) {
    // A Stop that won after the first retarget had already moved rows: the Stop's
    // own cancel named the old ref, so without this the moved rows would still
    // deliver while the toast says canceled. Cancel exactly that work under the
    // resumed ref so the UI and the wire agree. A NON-stop failure is
    // deliberately different: the moved rows are preserved work queued under the
    // resumed identity, and they deliver when it reconnects - canceling them
    // there would lose the user's message.
    if (stopWon && movedFirst !== null && resumedRef !== undefined) {
      await cancelUnattemptedMutations(resumedRef).catch(() => {});
    }
    if (swapToken !== undefined) settleResumeSwap(ref, swapToken);
    const message = error instanceof Error ? error.message : String(error);
    resumeFailureSeq += 1;
    threadsStore.setState((state) => ({
      resumeFailures: new Map(state.resumeFailures).set(ref, { seq: resumeFailureSeq, message }),
    }));
  }
}

// The late-row drain's in-line bound: enough passes for the common case. The
// scheduled trailing pass runs regardless, so the bound never exits silently.
const RETARGET_DRAIN_PASSES = 3;

// A resume that changed identity is mid-swap: the rows it moved to the resumed
// ref are still the user's to cancel with a Stop pressed on the ref they are
// looking at, but a Stop's own durable cancel names only that ref. This map
// (old ref -> resumed ref plus the registering drive's token) is the
// registration that closes the gap, live from the moment the resume response
// names the differing ref until the retarget
// machinery goes quiet - the drain pass that moves nothing - so a Stop racing
// either the pre-hydration window or the trailing drain still cancels the moved
// rows (the Stop call sites consult it). It is NOT cleared with the drive: the
// trailing drain runs after the drive's own bookkeeping, so a Stop during that
// pass must still find the target.
const resumeSwapTargets = new Map<string, { toRef: string; token: symbol }>();

// The token identifies THIS drive's registration. Two drives can target the
// same (fromRef, toRef) pair - inFlightResumes drops the first when it resolves
// while its trailing drain still awaits storage, and a second press then starts
// a second drive that registers the same pair - so matching only the pair would
// let the first drive's drain clear the second drive's live registration.
function registerResumeSwap(fromRef: string, toRef: string): symbol {
  const token = Symbol("resumeSwap");
  resumeSwapTargets.set(fromRef, { toRef, token });
  return token;
}

// Idempotent per drive: both the drive and its drain may settle, and only the
// registration carrying THIS token is cleared - an earlier swap's settle can
// never clear a later swap's registration.
function settleResumeSwap(fromRef: string, token: symbol): void {
  if (resumeSwapTargets.get(fromRef)?.token === token) resumeSwapTargets.delete(fromRef);
}

// The Stop call sites' second cancel: while `ref` is mid-swap, a Stop pressed on
// it must also cancel the unattempted rows the drive moved to the resumed ref.
// A Stop after the swap settles is about the resumed ref's own rows and is not
// this registration's to cancel.
async function cancelResumeSwapTarget(ref: string): Promise<void> {
  const entry = resumeSwapTargets.get(ref);
  if (entry === undefined) return;
  await cancelUnattemptedMutations(entry.toRef);
}

// One more drain pass, on a later task, re-scheduling itself while passes keep
// moving rows. The old ref's restart obligation is not cleared by the drive (see
// the drain loop's comment), so a concurrent send can park during the drain;
// this converges on a clean snapshot without blocking the event loop. The pass
// that moves nothing is the swap's convergence: it settles the Stop
// registration, so a Stop no longer needs to chase the resumed ref after the
// rows have stopped moving.
function scheduleRetargetDrain(
  fromRef: string,
  toRef: string,
  identity: { threadId?: string; instanceId?: string },
  token: symbol,
): void {
  queueMicrotask(() => {
    void retargetParkedMutations(fromRef, toRef, identity)
      .then((moved) => {
        if (moved !== null) scheduleRetargetDrain(fromRef, toRef, identity, token);
        else settleResumeSwap(fromRef, token);
      })
      // Silence is deliberate, like the store's other fire-and-forget tails: the
      // resume itself already succeeded, so a resume-failure toast would be
      // wrong. A storage failure here just leaves the straggler parked under the
      // old ref - the pre-move behavior - where a later drive or a fresh
      // discovery retries it. The swap is settled either way: no further moves
      // happen, so a Stop has nothing left to chase.
      .catch(() => settleResumeSwap(fromRef, token));
  });
}

// Move the rows a resume left parked under its superseded ref onto the resumed
// ref, through the storage's retarget transaction, rewriting the rows' identity
// fields to `identity` so the daemon's instance fence accepts them under the
// resumed identity. The outbox bookkeeping the store keeps per ref moves with
// them: the old ref's undelivered ids are resolved and re-registered under the
// new ref, the new ref is pinned and armed for dispatch, and the old ref's arm
// is dropped if it is now idle. Returns what moved (null when nothing did) - an
// optimistic-only or recovery-only identity change still needs the new ref
// pinned and armed, and the drive needs the moved set to cancel that work if a
// Stop wins the swap.
async function retargetParkedMutations(
  fromRef: string,
  toRef: string,
  identity: { threadId?: string; instanceId?: string },
): Promise<RetargetedMutations | null> {
  const runtime = getMutationRuntime();
  if (!runtime) return null;
  const moved = await runtime.storage.retargetOutbox(fromRef, toRef, identity);
  if (moved.outbox.length === 0 && moved.optimistic.length === 0 && moved.recovery.length === 0) return null;
  for (const record of moved.outbox) {
    noteHandledMutation(record.clientMutationId);
    noteUndeliveredMutation(toRef, record.clientMutationId);
  }
  noteMutationStateChange(fromRef);
  noteMutationStateChange(toRef);
  disarmQuiescedMutationArm(fromRef);
  pinMutationRef(toRef);
  if (dispatchReplayGateOpen(toRef)) dispatchableMutationRefs.add(toRef);
  // BOTH refs: the destination's projection must show the moved rows, and the
  // superseded ref's projection still holds them (retargetOutbox removed them
  // from storage), so it must re-read and clear to the honest state. Dispatch is
  // scheduled for the destination only - the old ref has no rows left, so a
  // schedule there would be a no-op drain.
  notifyMutationPersistence([fromRef, toRef]);
  scheduleMutationDispatch(runtime, [toRef]);
  return moved;
}

// The snapshot-level refusals of retryBlockedMutation: a target the store
// cannot act on regardless of what the retry would find -- no live status, a
// restartRequired or notLoaded snapshot, no mutation authority, or a
// restart-blocking obligation. Shared with QueueStrip's Retry button, which
// disables on exactly these so a fenced row never offers a Retry that always
// fails; the remaining refusals (an in-flight reconciliation or hydration, a
// reconciliation failure, a missing dispatch client) stay retry-time checks.
export function retryBlockedBySnapshot(
  statusType: string | undefined,
  hasMutationAuthority: boolean,
  restartObligated: boolean,
): boolean {
  return (
    statusType === undefined ||
    statusType === "restartRequired" ||
    statusType === "notLoaded" ||
    !hasMutationAuthority ||
    restartObligated
  );
}

export async function retryBlockedMutation(
  clientMutationId: string,
  mode: "user" | "backgroundNote" = "user",
): Promise<boolean> {
  const runtime = requireMutationRuntime();
  // §4's stop barrier on the release path, the click-time capture: REQUESTED
  // here, inside the click's own synchronous prefix, before `await
  // runtime.start` and every other wait in the retry chain — the enqueue
  // barrier's own rule. The ref is unknown until the row is read, so the
  // capture rides that read: one readonly transaction carries the row AND the
  // ref's stop epoch, making it the click's first storage observation (on a
  // cold connection the request itself issues the open). A Stop whose cancel
  // transaction commits after that snapshot bumps the epoch past the capture,
  // and the release below refuses — a newer Stop outranks an earlier Retry,
  // the same commit-order comparison the enqueue barrier carries. A Stop
  // committed before the capture read's transaction can exist is the bounded
  // residual §4 states for the enqueue, and for the same reason: the
  // deliberate post-Stop Retry §9 item 7 protects is indistinguishable from
  // it in the database.
  const captureRead = runtime.storage.getOutboxWithStopEpoch(clientMutationId);
  await runtime.start;
  const { record, stopEpoch } = await captureRead;
  if (record?.state !== "blockedUnknown" && record?.state !== "canceled") return false;
  // Only an explicit user Retry releases a canceled row (the one reversal the
  // design allows); background retry modes exist for delivery-uncertain rows
  // and must never resurrect a user's Stop.
  if (record.state === "canceled" && mode !== "user") return false;
  // A background note save keeps its blocked draft when Stop wins; it must
  // neither resume a session nor retry another kind of mutation.
  if (mode === "backgroundNote" && record.method !== "notes/human/set") return false;
  if (record.method === "notes/human/set" && !canWriteHumanNote(trackedThreadModel(record.targetRef))) return false;
  const state = threadsStore.getState();
  // A Retry is a press-time verdict on a ref this tab can still vouch for.
  // The hub's deletion fence (deletedRefs) and a clear's replacement model are
  // the two states that say the world the row knew is gone: the discard that
  // should have removed the row is best-effort and may have failed, so the
  // press itself must refuse rather than release a row whose instance the
  // daemon would fence as stale anyway.
  if (state.deletedRefs.has(record.targetRef)) return false;
  const pressModel = state.threads.get(record.targetRef) ?? state.watchedThreads.get(record.targetRef);
  // The same fused identity check replacement detection uses (instanceId ??
  // threadId): the row's enqueue-time instance against the press model's.
  // A replacement that rotated the instance while retaining the thread id
  // must refuse here too — the daemon would fence the stale send anyway.
  const rowInstance = record.instanceId ?? record.threadId;
  if (rowInstance !== undefined && threadInstanceID(pressModel) !== rowInstance) return false;
  if (
    retryBlockedBySnapshot(
      state.threads.get(record.targetRef)?.status.type,
      state.mutationAuthorityRefs.has(record.targetRef),
      state.restartBlockingObligations.has(record.targetRef),
    )
  )
    return false;
  if (
    pendingMutationReconciliations.has(record.targetRef) ||
    pendingThreadHydrations.has(record.targetRef) ||
    state.mutationReconciliationFailures.has(record.targetRef) ||
    // A storage-blocked ref cannot answer the ordering questions a Retry
    // press asks either: its reconcile sits on the same wedge, and
    // discovery's retry is what clears this fence.
    state.mutationReconciliationStorageBlocked.has(record.targetRef)
  )
    return false;
  const client = currentDispatchClient();
  if (!client) return false;
  const epoch = dispatchReadyEpoch;
  if (record.state === "canceled") {
    // The release is durable before any dispatch. A canceled row provably
    // never reached the daemon, so it needs no reconciliation to prove
    // absence — release, then let the same fenced resync-and-dispatch tail
    // every user retry uses carry it out. The barrier is the click-time
    // capture above: a Stop that landed between the capture and this release
    // outranks the Retry, and the refused release leaves the row canceled
    // for it — the release returning false is this press's refusal.
    // Registered around the write: the reopened row is undelivered work, and a
    // fallback send must see it before the async refresh can.
    const released = await trackOutboxWrite(
      record.targetRef,
      () => runtime.storage.releaseCanceled(clientMutationId, { stopEpoch }),
      // The released row keeps the id it is reopened under, and is undelivered
      // work: it is reopened for a fresh dispatch.
      (didRelease) => (didRelease ? { clientMutationId, undelivered: true } : undefined),
    );
    if (!released) return false;
    notifyMutationPersistence([record.targetRef]);
  }
  // Shared storage can become blocked after this tab's authoritative snapshot.
  // Only fresh reconciliation may settle it or restore it for dispatch.
  await handleReady(
    client,
    epoch,
    record.targetRef,
    // A background note save never dispatches (its own resend belongs to the
    // outbox's lifecycle scan) and reopens only the record it retries; a user
    // retry dispatches and reconciles the whole target.
    mode === "backgroundNote"
      ? { suppressTargetDispatch: () => false, reopenOnlyClientMutationId: clientMutationId }
      : undefined,
  );
  if (!isCurrentMutationRuntime(runtime) || currentDispatchClient() !== client || dispatchReadyEpoch !== epoch)
    return false;
  const current = await runtime.storage.getOutbox(clientMutationId);
  return current?.state !== "blockedUnknown" && current?.state !== "canceled";
}

export async function updateRecoveryMutation(
  clientMutationId: string,
  targetRef: string,
  text: string,
  attachments: InputAttachment[],
  skillNames?: readonly string[],
  commandNames?: readonly string[],
  mentions?: readonly ComposerMention[],
): Promise<boolean> {
  const runtime = requireMutationRuntime();
  await runtime.start;
  const record = await runtime.storage.updateRecoveryInput(
    clientMutationId,
    buildInput(text, attachments, skillNames, commandNames, mentions),
    durableAttachments(attachments),
    text,
    mentions,
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
  if (discarded) {
    // A row left the outbox: in-flight reads for the ref are stale.
    noteMutationStateChange(targetRef);
    // The removed row's id is resolved: a later settle of a replacement, or
    // unreadable storage, must not keep blocking fallback sends behind it.
    noteHandledMutation(clientMutationId);
    notifyMutationPersistence([targetRef]);
  }
  return discarded;
}

export async function resendRecoveryMutation(
  clientMutationId: string,
  targetRef: string,
  route: ComposerMutationRoute,
  text: string,
  attachments: InputAttachment[],
  skillNames?: readonly string[],
  commandNames?: readonly string[],
  mentions?: readonly ComposerMention[],
): Promise<MutationOutboxRecord | undefined> {
  const runtime = requireMutationRuntime();
  await runtime.start;
  const intent = composerMutationIntent(targetRef, route, text, attachments, skillNames, commandNames, mentions);
  // Registered around the write: the resent row is undelivered work, and a
  // fallback send must see it before the async refresh can.
  const record = await trackOutboxWrite(
    targetRef,
    () => runtime.storage.resendRecovery(clientMutationId, intent),
    // A resend mints its own row, so the id comes from the record it returns.
    (resent) =>
      resent === undefined
        ? undefined
        : { clientMutationId: resent.clientMutationId, undelivered: resent.state !== "canceled" },
  );
  if (!record) return undefined;
  // The resend mints a new row and removes the recovery row it came from, so
  // the old id is resolved: keep the new one (trackOutboxWrite registered it)
  // and drop the old, so a later settle of the new row or unreadable storage
  // does not strand the guard on a row that no longer exists.
  noteHandledMutation(clientMutationId);
  pinMutationRef(targetRef);
  notifyMutationPersistence([targetRef], { record, recoveryId: clientMutationId });
  handleDiscoveredMutations(runtime, [targetRef]);
  // A resent row on the Send-resumes face parks like an ordinary send: drive the
  // resume it needs. Read after the write, which cannot have changed the face.
  if (sendResumesLocalModel(targetRef)) void resumeFencedForSend(targetRef);
  return record;
}

export function setMutationStorageForTests(storage: MutationOutboxIndexedDB): void {
  if (mutationRuntime) throw new Error("setMutationStorageForTests must run before the mutation runtime starts");
  mutationStorageForTests = storage;
}

// listModels' own session-lifetime cache (models are not per-ref, so this
// is a single slot, not a Map): modelsCache holds the last successful
// response; inflightModelsList de-dupes concurrent non-refresh callers the
// same way inflightHydrates does for ensureThread, and always holds the
// NEWEST request - a refresh supersedes whatever older non-refresh request
// was waiting there, so a concurrent caller joins the newest request rather
// than receiving the list the refresh was issued to replace.
// inflightModelsListIsRefresh records whether the slot's request is a
// refresh, so a non-refresh caller with a warm cache can tell "the slot is a
// read I may skip in favor of the cache" from "the slot is a refresh whose
// answer is newer than the cache I hold" - the latter must join the refresh
// rather than hand back the listing the refresh was issued to replace (two
// concurrent model pickers otherwise receive different listings). A
// rejection is never written to modelsCache (so a prior good cache survives a
// later failed refresh, and a first-ever failure leaves nothing stale to keep
// serving), and the call that owns inflightModelsList clears it in a
// `finally` so a failed call never poisons the next one with a repeated
// rejection — only while the slot still holds its own request, because
// evener/auth/updated drops the slot and a newer call may have claimed it
// since.
let modelsCache: ModelListResponse | null = null;
// modelsEpoch advances on every evener/auth/updated: a credential change can
// make models discoverable (a stored Vertex credential JSON enables the
// publisher-model listing) or take them away, so a listing cached before it
// is stale, and a listing still in flight answers the old credentials and
// must not become the cache either.
let modelsEpoch = 0;
// modelsListGeneration advances on every new model/list request, and a
// response becomes the cache only while it is still the NEWEST request:
// refresh:true deliberately does not cancel an older in-flight listing, and
// when no evener/auth/updated intervenes (a keyless connection, a
// config-only save) the epoch guard alone cannot stop the older answer from
// landing last and overwriting the fresher one. Every caller still receives
// its own response; only the cache write is gated.
let modelsListGeneration = 0;
let inflightModelsList: Promise<ModelListResponse> | null = null;
// True exactly while inflightModelsList holds a refresh:true request. Cleared
// with the slot so the flag is never read for a request it does not describe.
let inflightModelsListIsRefresh = false;

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

// Both hydrate paths retain transcript window and response-cut parameters;
// the shared lease owns only wire membership and additive subscribe ordering.
const TRANSCRIPT_ITEM_PAGE_SIZE = 40;

// heldSnapshot lets the server answer with HistoryChanges instead of a full
// re-read when nothing outside the client's window changed (spec "Reads":
// "Later completions of held items"). Sent whenever the request carries a
// held v6 model's identity - the server decides whether the read is
// daemon-served or daemonless; the client sends what it holds either way.
function threadReadParams(includeTurns: boolean, requestGeneration?: number, heldSnapshot?: SnapshotIdentity) {
  return {
    includeTurns,
    itemsView: "full",
    itemLimit: TRANSCRIPT_ITEM_PAGE_SIZE,
    ...(requestGeneration !== undefined ? { requestGeneration } : {}),
    ...(heldSnapshot ? { heldSnapshot } : {}),
  } as const;
}

// The heldSnapshot a pending hydration's thread/read carries: the base
// model's held history identity, when it has one with a recorded
// incarnation (a v6 model that has completed at least one latest-window
// read).
function heldSnapshotFor(baseModel: ThreadModel | undefined): SnapshotIdentity | undefined {
  const history = baseModel?.history;
  return history?.incarnation === undefined ? undefined : { incarnation: history.incarnation, length: history.length };
}

// A held snapshot whose length the kept update log no longer reaches (spec's
// "Below-floor update-log requests" follow-up) cannot be answered with the
// window alone - held items outside it may have changed with no way to tell
// - so the hub rejects it with TranscriptItemCursorStale rather than
// silently merging a bare window. shouldRetryWithoutHeldSnapshot, called
// from a thread/read catch block that already has `held` and `err` in
// scope, reports whether that is what happened and the caller should retry
// once with no held snapshot (for a full latest-window replacement). Both
// call sites inline that retry in their own nested try/catch (rather than
// wrapping transcript history recovery in the membership helper). The
// hydration owner retains all history and response-cut ordering authority.
function shouldRetryWithoutHeldSnapshot(held: SnapshotIdentity | undefined, err: unknown): boolean {
  return held !== undefined && isStaleCursorError(err);
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
  let discardHeldHistory = false;
  const lease = syncThreadSubscription(ref, client);
  if (!lease) throw new Error("Thread hydration has no subscription holder");
  const held = heldSnapshotFor(pending.baseModel);
  try {
    try {
      response = await lease.read(threadReadParams(true, pending.requestGeneration, held));
    } catch (err) {
      if (!shouldRetryWithoutHeldSnapshot(held, err)) throw err;
      response = await lease.read(threadReadParams(true, pending.requestGeneration, undefined));
      discardHeldHistory = true;
    }
  } catch (err) {
    // thread/read is answered from the daemon's in-memory snapshot, so a
    // rejection here is a transport failure, not a slow file read and not a
    // lost claim. Ask this ref's owner generation to read again.
    markThreadDeletedIfFenced(ref, err);
    // A structured "history failed" response is not a transport hiccup: the
    // spec says the client shows one visible diagnostic and waits for a later
    // read to succeed, never auto-retrying in a loop. Applied only against
    // this hydration's own base model (superseded generations are ignored by
    // applyHistoryReadFailure itself).
    if (isTranscriptHistoryFailedError(err) && pending.baseModel) {
      putThreadModel(ref, applyHistoryReadFailure(pending.baseModel, errorText(err), pending.requestGeneration ?? 0));
    } else {
      scheduleOwnedHydrationRetry("thread", ref, pending);
    }
    throw err;
  }
  if (discardHeldHistory) clearOversizeMemo(ref); // the stale-snapshot retry's replacement is a mid-lifetime shrink
  // A retry that dropped the held snapshot asked for, and must be treated
  // as, a full latest-window replacement: applyReadResponse would still
  // merge it (same incarnation/epoch, length >= held's), leaving every held
  // item outside the fresh window exactly as stale as it was -- precisely
  // the items TranscriptItemCursorStale exists to protect, since the window
  // alone cannot answer for them. hydrateThread discards the held history
  // instead, matching the hub's intent.
  const model =
    !discardHeldHistory && pending.baseModel?.history !== undefined
      ? applyCacheGapRule(ref, pending.baseModel, response, now)
      : hydrateThread(response, ref, now);
  applyHydrationResponseCut(pending, ref, model);
  return { model, response };
}

// The one place a rejection is checked for the hub's durable deletion fence
// (data.mutationOutcome === "targetDeleted" - cmd/evener-hub/app_sources.go's
// deletionFenceError) and recorded into `deletedRefs`, which Session.tsx
// reads to tell a genuinely gone ref apart from one merely slow to hydrate.
// The fence never clears, so this rejection is terminal for the ref's
// hydration lifecycle: scheduleOwnedHydrationRetry below reads the flag this
// sets and retires the lifecycle (settling any owner awaiting a first model
// and cancelling its retry) instead of arming another read, and
// ensureThread/watchThread's loops return on it. Recording the flag here is
// what lets all three paths agree on one terminal deleted state. The cache
// work the fence owes (spec, "The write seam") is delegated, never
// duplicated: markCacheSessionsDeleted is exactly the effect set a
// read-proven deletion owes the cache — join deletedRefs, cancel the
// pending write, delete the record, propagate — and its idempotence IS the
// retry contract: a later firing retries the delete a storage fault
// aborted, instead of this hook growing a second copy of those lines.
function markThreadDeletedIfFenced(ref: string, err: unknown): void {
  if (mutationErrorData(err)?.mutationOutcome !== "targetDeleted") return;
  releaseSubagentRows(ref);
  discardCanceledMutations(ref);
  markCacheSessionsDeleted([ref]);
}

// The removal half of a canceled row's lifecycle
// (docs/design/stop-cancellation-outbox.md §6): when the thread is cleared or
// the hub proves it deleted, its canceled rows leave with it. Best-effort —
// a storage failure keeps the rows visible for an explicit Retry, and the
// next clear/delete retries the removal.
function discardCanceledMutations(targetRef: string): Promise<void> {
  const runtime = getMutationRuntime();
  if (!runtime) return Promise.resolve();
  return runtime.storage
    .discardCanceled(targetRef)
    .then(() => {
      if (!isCurrentMutationRuntime(runtime)) return;
      // Rows left the outbox: in-flight reads for the ref are stale, whether or
      // not any of their ids was registered here.
      noteMutationStateChange(targetRef);
      // Every successful discard notifies, a zero count included: zero says
      // what THIS tab's write removed, never what another tab may have
      // removed from under this tab's cached projection - and the notify is
      // what refreshes that projection. The pin refresh follows for the same
      // reason: the discard may have removed the ref's last durable row, and
      // a stale pin keeps releaseThread from dropping the model.
      notifyMutationPersistence([targetRef]);
      // Fired, not awaited - the clear's publication must not wait on it -
      // but its rejection stays inside this best-effort envelope: the reads
      // it performs can fail (a timeout, a VersionError, a retired
      // connection) after the discard already committed.
      void refreshMutationPins(runtime, [targetRef]).catch(() => {});
    })
    .catch(() => {});
}

// The cancellation write every Stop path makes before its stop action
// (stop-cancellation-outbox §4): forceStop/shutdown carry no interrupt
// record, so this standalone write is their cancel moment. A failure of a
// real store's write throws, aborting the stop before the daemon is touched.
// No store at all (IndexedDB unavailable to this tab) is different: there
// are no durable rows to cancel and this tab can neither resurrect nor
// reopen any, so the rule has nothing to protect and the stop proceeds.
async function cancelUnattemptedMutations(ref: string): Promise<void> {
  const runtime = getMutationRuntime();
  if (!runtime) return;
  await runtime.start;
  const canceled = await runtime.storage.cancelUnattempted(ref);
  // Rows were canceled in the outbox: in-flight reads for the ref are stale,
  // whether or not their ids were registered here.
  noteMutationStateChange(ref);
  // The write named the ids it canceled, so they are provably undelivered no
  // more: leaving them would fail a later send closed for a row that never
  // reached the daemon. Only a cancel path that cannot name its rows may leave
  // them for the next successful read.
  for (const clientMutationId of canceled) noteHandledMutation(clientMutationId);
  // Notify on every successful write, zero canceled rows included: zero is
  // exactly what this tab sees when a sibling tab's Stop already canceled the
  // rows, and cancelUnattempted is a raw storage write — it announces nothing
  // over the BroadcastChannel and schedules no discovery — so this notify is
  // the only thing that refreshes this tab's projection and pins now rather
  // than at the next discovery scan. The discard paths carry the same
  // zero-included rule.
  notifyMutationPersistence([ref]);
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
  let discardHeldHistory = false;
  const lease = syncThreadSubscription(ref, client);
  if (!lease) throw new Error("Thread hydration has no subscription holder");
  const held = heldSnapshotFor(pending.baseModel);
  try {
    try {
      resp = await lease.read(threadReadParams(includeTurns, pending.requestGeneration, held));
    } catch (err) {
      if (!shouldRetryWithoutHeldSnapshot(held, err)) throw err;
      resp = await lease.read(threadReadParams(includeTurns, pending.requestGeneration, undefined));
      discardHeldHistory = true;
    }
  } catch (err) {
    markThreadDeletedIfFenced(ref, err);
    if (isTranscriptHistoryFailedError(err) && pending.baseModel) {
      putWatchedThreadModel(
        ref,
        applyHistoryReadFailure(pending.baseModel, errorText(err), pending.requestGeneration ?? 0),
      );
    } else {
      scheduleOwnedHydrationRetry("watched", ref, pending);
    }
    throw err;
  }
  // See hydrateAndSubscribe's identical comment: a retry that dropped the
  // held snapshot must fully replace history, not merge into it.
  const model =
    !discardHeldHistory && pending.baseModel?.history !== undefined
      ? applyReadResponse(pending.baseModel, resp, now)
      : hydrateThread(resp, ref, now);
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

// The composer's routes and the wire methods they build, in ONE place: both
// composerMutationIntent and DIRECT_FALLBACK_METHODS read this map, so a route
// added here (or a method changed) cannot leave the fallback set silently
// behind. The Record<ComposerMutationRoute, string> annotation makes the map
// exhaustive over the route union at compile time.
const COMPOSER_ROUTE_METHODS: Record<ComposerMutationRoute, string> = {
  send: "turn/start",
  queue: "turn/queue",
  steer: "turn/steer",
  drain: "turn/drainAsSteer",
};

function composerMutationIntent(
  ref: string,
  route: ComposerMutationRoute,
  text: string,
  attachments?: InputAttachment[],
  skillNames?: readonly string[],
  commandNames?: readonly string[],
  mentions?: readonly ComposerMention[],
): MutationIntent {
  const model = trackedThreadModel(ref);
  // The store-side half of the skillInput gate (Task 12 gates the hub's
  // forwarding the same way): a target that does not advertise the
  // capability never receives a request carrying a selection, so no queue
  // entry, journal payload, or turn is created for it. An absent capability
  // reads exactly like a false one - an older daemon never sends it.
  // The gate measures what the request will actually carry: buildComposerInput
  // canonicalizes (trims, drops empties and duplicates), so a whitespace-only
  // or empty-name list would send zero skill items and must not be refused.
  if (canonicalSkillNames(skillNames).length > 0 && model?.capabilities?.skillInput !== true) {
    throw new Error("skill selections are not supported on this target");
  }
  if (canonicalSkillNames(commandNames).length > 0 && model?.capabilities?.commandInput !== true) {
    throw new Error("command selections are not supported on this target");
  }
  // Translated HERE, not inside buildInput: this is the submit boundary. The
  // untranslated text rides along as composerText so a record that fails and
  // lands in recovery can be restored into a composer with its marker anchors
  // intact - the tiles remove those anchors, and prose is not one.
  const input = buildComposerInput(text, attachments, skillNames, commandNames, mentions);
  const expectedInstanceId = threadInstanceID(model);
  const base = {
    targetRef: ref,
    threadId: model?.threadId,
    // The enqueue-time instance identity, persisted beside the thread id so
    // cleanup and Retry can compare the fused identity (instanceId ??
    // threadId) the fence uses — see discardCanceledOfInstance.
    instanceId: model?.instanceId,
    attachments: durableAttachments(attachments),
    composerText: text,
    composerMentions: mentions,
  };
  if (route === "send") {
    return {
      ...base,
      method: COMPOSER_ROUTE_METHODS.send,
      payload: { ref, expectedInstanceId, input },
      optimisticDisplay: { method: COMPOSER_ROUTE_METHODS.send, input },
    };
  }
  if (route === "queue" || route === "steer") {
    const method = COMPOSER_ROUTE_METHODS[route];
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
    method: COMPOSER_ROUTE_METHODS.drain,
    payload: { ref, expectedInstanceId, expectedQueueRevision, input },
    optimisticDisplay: { method: COMPOSER_ROUTE_METHODS.drain, input },
  };
}

// The local recovery fence. A LOCAL session carrying a restart-blocking
// obligation (a Stop in flight, or a snapshot the daemon reports as
// restartRequired/resumeRequired) admits no session action at all while the
// obligation stands: the hub's recovery admission refuses turn/steer,
// turn/queue and every other fenced mutation for exactly that window
// (cmd/evener-hub's sessionActionRecoveryError reads the resume locks, never
// the projected status), with turn/start carved out only for the merely-
// resumable shape isResumeOnlyLocal names, so even a still-ACTIVE snapshot is
// fenced while
// a Stop drains - a live read relays the daemon's active status with
// resumeRequired overlaid beside it (applyThreadResumeRequirement), and the
// store arms the obligation on that very hydration. An offered press in that
// window could only mint durable intent that parks until the explicit Resume
// action clears the fence. The predicate lives here - beside the obligation
// state it reads, and where the store's own mutation admission enforces it
// (enqueueMutationIntent) without an import cycle - and liveControls
// re-exports it for the surfaces.
export function isLocalRecoveryFenced(ref: string, restartObligated: boolean): boolean {
  return ref.startsWith("local:") && restartObligated;
}

// A LOCAL session the hub admits a folded send for. The authority is the hub's
// OWN answer, not a shape this client infers: ResumeOnlyFoldable (whom
// applyThreadResumeRequirement stamps) is true exactly when a turn/start on the
// current connection would be admitted under the resume-only carve-out
// (cmd/evener-hub's sessionAdmitsResumeRequired, read without a request: the
// requirement is set, no Stop is draining, the exit is confirmed, and no
// connection fence applies). The client no longer reads resumeRequired plus a
// cleared send capability as this shape - that overlay (applyThreadResumeRequirement)
// sets both for a Stop drain, an unconfirmed force-stop exit, the
// connection-recovery fence, and an incompatible-protocol daemon too, four
// shapes whose turn/start the hub still refuses, so inferring from them offered
// Send where the hub would refuse it.
//
// Deliberately narrow - it is NOT the whole recovery fence. Every clause below
// excludes a shape the hub would refuse or one whose send would starve:
//
//   - the hub bit must be true; an older or non-local snapshot that never
//     carried it is not this shape.
//   - no status clause: the hub's admission is status-independent
//     (sessionActionRecoveryError / resumeOnlyFoldable read no status), so a
//     live snapshot the hub legitimately stamped foldable - a daemon another
//     controller started under a confirmed force-stop obligation - is admitted
//     and folds. A status clause here would hide the folded send from a
//     snapshot the hub would happily admit, silently falling back to the old
//     explicit-Resume step the fold exists to remove. The stale bit a shut-down
//     snapshot leaves behind is defended against where it is written, not here:
//     the off-shut-down transition clears it (settleResumeOnlyOffShutdown) and
//     a connection-generation change invalidates it
//     (invalidateHeldHistoriesForReconnect), so a bit whose shut-down snapshot
//     has ended cannot mis-route a send.
//   - signals.uncertainMessages must be false: while delivery-uncertain rows
//     exist the hub's explicit Resume still runs reconciliation, so the send is
//     not the whole story and the Resume affordance stays.
//   - signals.stopInFlight must be false: a snapshot taken before this page's
//     Stop was answered can still carry the bit the hub stamped before the
//     Stop's drain began, so the local in-flight Stop keeps the fence.
//   - signals.queuedNonSend must be false: a queued non-turn/start durable row
//     (still "submitting", attempted or not) parks at the target's FIFO ahead
//     of a turn/start, so folding a send behind it would silently never
//     deliver it.
//
// A Stop in flight / active drain, a restartRequired daemon that needs the
// older daemon stopped first, and a stale-connection snapshot all leave the hub
// bit false; all keep the fence.
export interface ResumeOnlySignals {
  // The store's delivery-uncertain rows (blockedUnknown).
  uncertainMessages?: boolean;
  // A Force stop this page started is still draining (stoppingRefs).
  stopInFlight?: boolean;
  // A queued non-turn/start durable row (still "submitting", attempted or not)
  // ahead of the send (the pending-turns projection's hasQueuedNonSend).
  queuedNonSend?: boolean;
  // The store's mutation-recency reading (mutationAuthorityRefs), read only by
  // the Send-resumes face below: a local snapshot whose parent still owns its
  // uncertain rows (Session.tsx's recoveryOwnerRef) is not Send-driven.
  mutationStateAuthoritative?: boolean;
}

export function isResumeOnlyLocal(
  ref: string,
  model: Pick<ThreadModel, "resumeOnlyFoldable">,
  signals: ResumeOnlySignals = {},
): boolean {
  return (
    ref.startsWith("local:") &&
    model.resumeOnlyFoldable === true &&
    signals.uncertainMessages !== true &&
    signals.stopInFlight !== true &&
    signals.queuedNonSend !== true
  );
}

// The pending-turns projection's synchronous view of a ref's durable outbox,
// published by pendingTurnsStore.ts at its own module load. Deliberately NOT
// read through the projection module: threads.ts cannot import it (it imports
// this one), so the projection registers its read here instead. The composer's
// blockedMutations selector and this read see the same durable rows; routing
// the store-wide predicate through it is what gives the press, enqueue and
// dispatch paths the delivery-uncertain signal the render already had.
//
// Failing closed: until a projection is installed the read answers "uncertain",
// so the carve-out never folds a resume ahead of rows nobody has reconciled.
// The projection is installed wherever a session pane is (Session/Composer
// import it), and every caller of the predicate acts on a loaded session.
interface ResumeOnlyProjection {
  // Whether `ref`'s durable outbox has been read and published into the
  // projection. An installed projection whose first durable read for the ref
  // has not resolved yet still holds an EMPTY outbox for it - the same map a
  // ref with no rows holds - so a predicate that read only hasBlockedUnknown/
  // hasQueuedNonSend would report "no uncertainty" through exactly the window
  // between hydration and the refresh completing, where an unreconciled
  // blockedUnknown row can sit. Both reads below therefore fail closed until
  // this is true, per ref.
  isLoaded(ref: string): boolean;
  hasBlockedUnknown(ref: string): boolean;
  // Queued non-turn/start rows (still "submitting", attempted or not): a
  // turn/start queued behind one starves at the target's FIFO, so a ref holding
  // one is not foldable.
  hasQueuedNonSend(ref: string): boolean;
}
let resumeOnlyProjection: ResumeOnlyProjection | null = null;

export function installResumeOnlyProjection(projection: ResumeOnlyProjection): void {
  resumeOnlyProjection = projection;
}

// Whether ref's durable outbox holds delivery-uncertain (blockedUnknown) rows,
// as the pending-turns projection last published them. True when no projection
// is installed OR the ref's own durable outbox has not loaded yet (failing
// closed - an installed-but-unrefreshed projection holds an empty outbox for
// every ref). Exported so a surface that reasons about the fence can ask the
// same question the store-wide predicate does.
export function hasBlockedUnknown(ref: string): boolean {
  return (
    resumeOnlyProjection === null || !resumeOnlyProjection.isLoaded(ref) || resumeOnlyProjection.hasBlockedUnknown(ref)
  );
}

// Whether ref's durable outbox holds a queued non-turn/start row (still
// "submitting", attempted or not), as the pending-turns projection last
// published them. True when no projection is installed OR the ref's own
// durable outbox has not loaded yet (failing closed). Exported so a surface
// that reasons about the fence can ask the same question the store-wide
// predicate does.
export function hasQueuedNonSend(ref: string): boolean {
  return (
    resumeOnlyProjection === null || !resumeOnlyProjection.isLoaded(ref) || resumeOnlyProjection.hasQueuedNonSend(ref)
  );
}

// isResumeOnlyLocal over the store's current model for ref: the ADMISSION
// predicate the press (liveControls) and enqueue (enqueueMutationIntent) paths
// share. Unlike isResumeOnlyLocal it reads the delivery-uncertain signal
// itself, from the pending-turns projection (hasBlockedUnknown), and the
// queued-non-send signal (hasQueuedNonSend), so a direct caller that bypassed
// the composer's own reads - the palette's slash fallthrough, the ask dock's
// batch send, a failed turn's Retry - still keeps the fence while blockedUnknown
// rows or a queued non-send row stand. It can also read the store's own
// in-flight Stop (stoppingRefs). Dispatch reads its sibling instead:
// resumeOnlyLocalDispatchable omits the queued-non-send clause because at
// dispatch the named head record may itself be the send, with no non-send row
// ahead of it (currentDispatchClient).
export function resumeOnlyLocalModel(ref: string): boolean {
  const model = threadsStore.getState().threads.get(ref);
  return model !== undefined && isResumeOnlyLocal(ref, model, localFenceSignals(ref));
}

// The store's client-side fence signals, read once from the current state: the
// delivery-uncertain rows, the in-flight Stop, the queued non-send row, and the
// mutation-recency mark. resumeOnlyLocalModel, sendResumesLocalModel and
// sendDrivenLocalModel all share this one literal.
function localFenceSignals(ref: string): ResumeOnlySignals {
  const state = threadsStore.getState();
  return {
    stopInFlight: state.stoppingRefs.has(ref),
    uncertainMessages: hasBlockedUnknown(ref),
    queuedNonSend: hasQueuedNonSend(ref),
    mutationStateAuthoritative: state.mutationAuthorityRefs.has(ref),
  };
}

// A local snapshot the pane shows as retained by its owning session: its
// uncertain rows are the owner's to reconcile, and the pane offers the owner's
// link and a Refresh - never a resume. The ONE definition, consumed by the
// Send-resumes face here, Session.tsx's notice, and SessionChrome.tsx's
// force-stop eligibility.
export function ownerRetainedRef(
  model: Pick<ThreadModel, "status" | "parentRef">,
  mutationStateAuthoritative: boolean,
): string | undefined {
  return !mutationStateAuthoritative &&
    model.status.type !== "notLoaded" &&
    model.status.type !== "restartRequired" &&
    model.parentRef?.startsWith("local:") === true
    ? model.parentRef
    : undefined;
}

// The recovery fence's Send-driven face (R09): a LOCAL session carrying a
// restart-blocking obligation whose only recovery action used to be the
// standalone Resume button. Broad by design - it is everything the merely-
// resumable fold is NOT, minus the two shapes that keep their own controls:
//   - a restartRequired daemon still needs its older process stopped first,
//     which Send cannot do;
//   - an owner-retained snapshot's rows belong to its owner;
//   - a draining Stop is still the wire's own fence (the hub holds Stopping > 0
//     and refuses even turn/start).
// Everything else - delivery-uncertain rows, a queued non-send row, an
// unconfirmed exit, the connection fence - offers Send, and the store's driver
// resumes before the parked send dispatches.
export function isSendResumesLocal(
  ref: string,
  model: Pick<ThreadModel, "resumeOnlyFoldable" | "status" | "parentRef">,
  restartObligated: boolean,
  signals: ResumeOnlySignals = {},
): boolean {
  if (!ref.startsWith("local:")) return false;
  if (!restartObligated) return false;
  if (model.status.type === "restartRequired") return false;
  if (signals.stopInFlight === true) return false;
  if (ownerRetainedRef(model, signals.mutationStateAuthoritative === true) !== undefined) return false;
  return !isResumeOnlyLocal(ref, model, signals);
}

// isSendResumesLocal over the store's current model for ref: the ADMISSION
// predicate enqueueMutationIntent and the press (liveControls) share, read from
// the same store-wide signals resumeOnlyLocalModel reads.
export function sendResumesLocalModel(ref: string): boolean {
  const model = threadsStore.getState().threads.get(ref);
  return (
    model !== undefined &&
    isSendResumesLocal(ref, model, threadsStore.getState().restartBlockingObligations.has(ref), localFenceSignals(ref))
  );
}

// The two faces whose only recovery action is a Send: the merely-resumable fold
// and the Send-resumes face. The admission (enqueueMutationIntent) and the press
// (liveControls' pressRefusal) both admit turn/start for exactly this union, so
// they read this one helper rather than recombining the pair.
export function sendDrivenLocalModel(ref: string): boolean {
  return resumeOnlyLocalModel(ref) || sendResumesLocalModel(ref);
}

// The DISPATCH reading of the resume-only carve-out, used by
// currentDispatchClient when it names the exact head record it is about to
// send. It is deliberately NOT resumeOnlyLocalModel: the two callers ask
// genuinely different questions. resumeOnlyLocalModel answers ADMISSION - "may
// a new send be minted behind this queue?" - where any queued non-send row is
// genuinely ahead of the candidate, so hasQueuedNonSend belongs. Dispatch asks
// "is this exact head record admissible?", and the head record is the next
// thing the wire sees: a queued non-send row BEHIND a head turn/start is not
// ahead of it, so it must not park the head. currentDispatchClient still
// exempts only turn/start (and the ref-less/pre-record calls), so the gate
// names a queued non-send method itself and parks it; this reading only stops
// a tail non-send row from refusing the head send. The delivery-uncertain and
// in-flight-Stop signals stay, because both fence the head record too.
export function resumeOnlyLocalDispatchable(ref: string): boolean {
  const state = threadsStore.getState();
  const model = state.threads.get(ref);
  return (
    model !== undefined &&
    isResumeOnlyLocal(ref, model, {
      stopInFlight: state.stoppingRefs.has(ref),
      uncertainMessages: hasBlockedUnknown(ref),
    })
  );
}

// The verbs the shared admission fences, each mapped to the refusal its own
// surface already renders (Composer's Send/Steer sentences, QueueStrip's
// queue-actions sentence, the typed /clear idiom): the hub's recovery
// admission refuses exactly these methods for an obligation's whole window -
// turn/start, turn/steer, turn/queue, turn/drainAsSteer,
// turn/promoteQueuedAsSteer and turn/cancelQueued via
// withDeletionTargetOwnership, thread/clear through clearThreadWithResume's
// durable leg, notes/human/set through relayWithResume's - so an enqueue for
// a fenced ref could only mint durable intent that parks until the explicit
// Resume action clears the fence. turn/interrupt is the deliberate exemption:
// Stop is how the fenced window ends, the Stop button never disables for the
// fence, and the typed /interrupt agrees with the button
// (shell/palette/commands.ts's own carve-out, pinned there), so interrupt
// still enqueues and settles after Resume rather than refusing here. Any
// method absent from this table is therefore not fenced at admission - the
// table is the whole policy. turn/start is carved out for BOTH Send-driven
// shapes (sendDrivenLocalModel): the merely-resumable fold
// (isResumeOnlyLocal), whose resume the hub folds into turn/start, and the
// Send-resumes face (isSendResumesLocal), whose send parks and drives the
// store's resume. A live Stop drain or a restartRequired daemon is neither and
// still refuses it.
const RECOVERY_FENCE_REFUSALS: Record<string, string> = {
  "turn/start": "Send isn't available until this session is resumed",
  "turn/steer": "Steer isn't available until this session is resumed",
  "turn/queue": "Queue isn't available until this session is resumed",
  "turn/drainAsSteer": "Drain isn't available until this session is resumed",
  "turn/promoteQueuedAsSteer": "Queue actions aren't available until this session is resumed",
  "turn/cancelQueued": "Queue actions aren't available until this session is resumed",
  "thread/clear": "Clear isn't available until this session is resumed",
  "notes/human/set": "Notes aren't available until this session is resumed",
};

// The composer's four send verbs, DERIVED from COMPOSER_ROUTE_METHODS so the
// builder and this set cannot drift. These are the only mutations the direct
// fallback answers when the durable outbox cannot be written: each has no
// response-side local commit beyond the wire call itself, so a plain RPC looks
// to the rest of the store exactly like the dispatched row would have. Every
// other durable write keeps its own contract and stays fail-closed - see the
// catch in enqueueMutationIntent.
const DIRECT_FALLBACK_METHODS: ReadonlySet<string> = new Set(Object.values(COMPOSER_ROUTE_METHODS));

// Decrement a ref's in-flight durable-enqueue count, dropping the entry at
// zero. Paired exactly with the increment in enqueueMutationIntent and
// trackOutboxWrite, on both the settle and the throw path. A release with no
// matching acquire is a bookkeeping bug, not a normal case, so it throws rather
// than defaulting the missing entry to 1 - that default would also hide a
// double release or a mid-flight reset.
function releaseInflightDurableEnqueue(ref: string): void {
  const current = inflightDurableEnqueues.get(ref);
  if (current === undefined) {
    throw new Error(`threads store: released an in-flight durable enqueue for ${ref} that was never acquired`);
  }
  if (current > 1) inflightDurableEnqueues.set(ref, current - 1);
  else inflightDurableEnqueues.delete(ref);
}

// Register an outbox write that can create or reopen a row this tab now owes
// delivery (resendRecovery, releaseCanceled), not only the send funnel. It is
// counted in flight for the fallback's concurrent-enqueue guard, and the ref is
// marked as holding undelivered work when the write commits a row that is not
// canceled. Without this the guard's in-memory state stays stale-empty until an
// async refresh reads the row back - and if storage wedges first, a fallback
// send jumps the row that was just written. Every caller names the row it
// wrote: a reopened recovery row keeps its id and a resend returns the new
// record, so the settle that proves delivery can resolve it. (Writes that only
// REMOVE or cancel rows - discardRecovery, updateRecoveryInput, discardCanceled,
// discardCanceledOfInstance, cancelUnattempted - need no registration: they
// never create undelivered work, and the next refresh/hydration recomputes the
// ids from the records when it can read them.)
async function trackOutboxWrite<T>(
  ref: string,
  write: () => Promise<T>,
  committedRow: (result: T) => { clientMutationId: string; undelivered: boolean } | undefined,
): Promise<T> {
  inflightDurableEnqueues.set(ref, (inflightDurableEnqueues.get(ref) ?? 0) + 1);
  try {
    const result = await write();
    const committed = committedRow(result);
    if (committed !== undefined) {
      // The write changed this ref's outbox, so in-flight reads are stale
      // whether or not the row it touched is undelivered work.
      noteMutationStateChange(ref);
      if (committed.undelivered) noteUndeliveredMutation(ref, committed.clientMutationId);
    }
    return result;
  } finally {
    releaseInflightDurableEnqueue(ref);
  }
}

// Remove a ref's dispatch arm once the ref is genuinely idle: it has no
// undelivered durable work (a non-canceled outbox row) and no durable enqueue
// still in flight. Ownership is deliberately NOT consulted - the arm is shared
// state, and with ownership two clicks can each decline to remove it (A's
// because B was still in flight at A's failure, B's because B never added it),
// leaving a stale arm with no durable row behind it. Quiescence is exactly the
// state in which an arm has nothing left to dispatch, so a stale one should go.
// pinnedMutationRefs is NOT read here either: it is a retention pin, and a ref
// whose only row is canceled (which leaves only on an explicit Retry or the
// thread going away) stays pinned forever.
function disarmQuiescedMutationArm(ref: string): void {
  if (!hasUndeliveredMutation(ref) && (inflightDurableEnqueues.get(ref) ?? 0) === 0) {
    dispatchableMutationRefs.delete(ref);
  }
}

// The hydrated-replay gate: a matching pending hydration for the ref keeps
// dispatch closed while its replay is in flight. Read at the moment it is
// consulted, never from a value captured earlier - the click's arming and the
// commit's re-arm both ask NOW, so a hydration that starts or finishes across
// the write is seen either way. (A captured snapshot would re-arm during a
// replay that started mid-write, or skip the re-arm for a hydration that
// finished mid-write, leaving the committed row unarmed until the next
// discovery.)
function dispatchReplayGateOpen(ref: string): boolean {
  const pending = pendingThreadHydrations.get(ref);
  return pending?.client !== wiredClient || pending.epoch !== readyEpoch;
}

async function enqueueMutationIntent(
  intent: MutationIntent,
  onCommitted?: (record: MutationOutboxRecord) => void,
  durableWrite: "enqueue" | "interruptAndCancel" = "enqueue",
): Promise<MutationOutboxRecord | undefined> {
  const ref = intent.targetRef;
  // The recovery fence, enforced at the one funnel every durable action
  // passes through: a fenced local session's durable intent could only park
  // (the hub refuses these methods for the obligation's whole window), so it
  // is refused here - before any durable write, so every caller shape hears
  // the same admission refusal.
  const fenceRefusal = RECOVERY_FENCE_REFUSALS[intent.method];
  const admissionState = threadsStore.getState();
  if (
    fenceRefusal !== undefined &&
    // The obligation, OR an in-flight Stop of our own: a stale thread refresh
    // can clear restartBlockingObligations while the forceStop RPC still
    // drains, and the hub holds Stopping > 0 (refusing even turn/start) for
    // that whole window - so stopInFlight is an independent fence here, not
    // only a blocker of the resume-only carve-out below.
    isLocalRecoveryFenced(
      ref,
      admissionState.restartBlockingObligations.has(ref) || admissionState.stoppingRefs.has(ref),
    ) &&
    // Two shapes admit turn/start at this funnel. A merely-resumable session
    // folds its resume into the send - the hub admits it (cmd/evener-hub's
    // sessionActionRecoveryError turn/start carve-out). The Send-resumes face
    // (isSendResumesLocal) is the broad complement: its only recovery action
    // used to be the standalone Resume button, and a send now drives that
    // resume in the store (resumeFencedForSend) with the button's protections,
    // so its intent must be admitted to park rather than refused. turn/start on
    // a live Stop drain or a restartRequired daemon is neither; every other
    // fenced verb keeps the refusal unchanged.
    !(intent.method === "turn/start" && sendDrivenLocalModel(ref))
  )
    throw new Error(fenceRefusal);
  const client = requireClient();
  if (client.state !== "ready") throw new Error(`threads store: cannot enqueue mutation while ${client.state}`);
  const runtime = requireMutationRuntime();
  // §4's stop barrier, the click-time half: the capture's read transaction is
  // REQUESTED here, at true click time — inside the click's own synchronous
  // prefix, before `await runtime.start` and every other wait in the enqueue
  // chain — so nothing can take the queue position ahead of it. On a warm
  // connection the read's transaction is the click's first; on a cold one the
  // request itself issues the open, and the capture is the first transaction
  // the reopened connection creates. A Stop whose durable write is created
  // after this request lands either before the enqueue's own write — the
  // comparison reads its bump, and the row commits born-"canceled" — or after
  // it, where the Stop's cancel scan cancels the row directly. The one
  // unfenceable order is a Stop whose write is created before the capture's
  // transaction can exist at all (§4's honest boundary). The Stop's own
  // interrupt record passes no barrier (it IS the click the epoch records).
  const barrierRead = durableWrite === "enqueue" ? runtime.storage.readStopEpoch(ref) : undefined;
  await runtime.start;
  // Enqueue schedules discovery before returning; preserve the hydrated replay
  // gate now, but only a durable commit may pin this ref after its pane closes.
  // The click arms the ref for dispatch; a matching pending hydration keeps the
  // replay gate closed instead. Whether this click added the value or found it
  // already set no longer matters - the disarm is quiescence-based, not
  // ownership-based (see disarmQuiescedMutationArm).
  if (dispatchReplayGateOpen(ref)) {
    dispatchableMutationRefs.add(ref);
  }
  // Register this click's durable write as in flight for the ordering guard
  // below. It stays counted until the write settles, so a concurrent enqueue
  // the fallback must not jump stays visible even though it has not committed
  // (and so has not pinned, and its arm a refresh can clear).
  inflightDurableEnqueues.set(ref, (inflightDurableEnqueues.get(ref) ?? 0) + 1);
  let record: MutationOutboxRecord;
  try {
    // The click-time capture, awaited at the write it fences, and the durable
    // enqueue behind it, retried once on a storage timeout (see
    // enqueueDurableMutation).
    record = await enqueueDurableMutation(runtime, intent, onCommitted, durableWrite, barrierRead);
  } catch (error) {
    // This click's write has settled; only OTHER enqueues remain counted.
    releaseInflightDurableEnqueue(ref);
    // The direct fallback answers ONLY a durable enqueue whose method is one of
    // the composer's four send verbs (DIRECT_FALLBACK_METHODS). Every other
    // durable write keeps its own contract and fails closed here:
    // turn/interrupt is Stop - its enqueue is enqueueInterruptAndCancel, the
    // one transaction in which the ref's cancelable rows turn "canceled" AND
    // the interrupt record is written, both land or neither does, so falling
    // back would dispatch the interrupt while writing no cancels and leave the
    // queued messages the click was cancelling still queued for a later
    // dispatch. thread/clear needs its response applied locally
    // (applyClearResponse) and notes/human/set needs its onCommitted to mark
    // the draft saved; turn/promoteQueuedAsSteer is a queue action, not a send.
    // A storage-unavailable failure on any of those propagates exactly as every
    // other submission failure: the caller sees the refusal. This click's own
    // write has settled, so drop the ref's arm if it is now idle - see
    // disarmQuiescedMutationArm. (A Stop therefore lands here, not in the
    // fallback below, so its fail-closed decision lives with its invariant.)
    if (durableWrite !== "enqueue" || !DIRECT_FALLBACK_METHODS.has(intent.method) || !isStorageUnavailable(error)) {
      disarmQuiescedMutationArm(ref);
      throw error;
    }
    // Persistent storage failure on a composer send: the durable write could
    // not be made, so send it as a plain RPC right now, exactly as the
    // non-durable operations do (setModel, rename, compact, ...). The outbox
    // row, the dispatcher, the receipt/settle machinery and the recovery list
    // are all skipped - there is nothing durable to settle or replay.
    //
    // It must still re-earn the admission the dispatcher would grant, because
    // it is NOT the ref's durable FIFO head. currentDispatchClient is that
    // exact admission (the dispatcher asks it per ref and method), so re-run
    // it: it re-checks the wired client and readyEpoch - both may have changed
    // across the watchdogs - the client's ready state, and the ref's fences (a
    // pending reconciliation, a reconciliation failure, a Stop drain via
    // stoppingRefs, the restart obligation with turn/start's resume-only
    // carve-out, and restartRequired). requireArmed is off: the fallback's own
    // ordering guard is not the dispatcher's arm but the two checks below.
    // A reconcile the same storage wedge failed is admissible here and nowhere
    // else: storage-blocked is the one fence whose cause cannot be waited out
    // inside the wedge, so the fallback waives it (and the reconcile still
    // pending on that wedge) rather than strand the send.
    //
    // A fallback send must not jump an earlier durable send for the ref, and
    // two in-memory facts cover what this tab can see. undeliveredMutationIds
    // holds the ref's committed rows this tab has not proven delivered. (Not
    // pinnedMutationRefs: that is a retention pin, and a canceled row keeps a
    // ref pinned with nothing left to deliver, so reading it would refuse the
    // fallback for a ref whose only row a Stop already canceled.) And
    // inflightDurableEnqueues counts durable enqueues whose write has not
    // settled yet - the CONCURRENT case a committed row cannot show: this
    // click's own was just released, so a nonzero count is another enqueue for
    // this ref still in flight, one that has not committed (so not undelivered
    // work yet) and whose dispatch arm a background refresh can clear. The
    // precise answer - whether the ref's nextDispatchable head is this send -
    // lives in storage, which is unavailable here; these two are what the
    // in-memory state proves, and the fallback refuses on either rather than
    // reorder.
    const dispatchClient = currentDispatchClient(ref, intent.method, false, { storageBlockedAdmissible: true });
    const concurrentEnqueueOutstanding = (inflightDurableEnqueues.get(ref) ?? 0) > 0;
    if (dispatchClient === null || hasUndeliveredMutation(ref) || concurrentEnqueueOutstanding) {
      disarmQuiescedMutationArm(ref);
      throw error;
    }
    // The admission above proved the ref has no undelivered work and no enqueue
    // in flight, so it is idle: drop the arm - see disarmQuiescedMutationArm.
    disarmQuiescedMutationArm(ref);
    //
    // The forfeited cross-tab Stop fence: this send carries no click-time stop
    // epoch - whether the capture read timed out, or the capture succeeded and
    // only the enqueue timed out. The epoch is a commit-order comparison
    // against the durable row the enqueue would have written, not a wire
    // parameter (the dispatcher sends record.payload alone), so a send with no
    // row carries none either way. A Stop landing in another tab during that
    // window therefore cannot be honoured for it; the composer's draft is
    // untouched (it lives in localStorage), and a send is retryable by hand -
    // the trade the fallback makes against failing closed.
    await dispatchMutationDirectly(dispatchClient, intent);
    return undefined;
  }
  // The success path releases exactly once, here, BEFORE the post-commit
  // bookkeeping and its listeners run: a listener that consults
  // inflightDurableEnqueues sees this click already decremented, which is the
  // ordering the previous structure had. Keeping the bookkeeping OUTSIDE the
  // try means a throw from it can no longer re-enter a release (the
  // double-release bug), and this release is unreachable from an error path
  // because the only other exit from the try is the catch above.
  releaseInflightDurableEnqueue(ref);
  // Every commit bumps the generation refreshes check against: a born-canceled
  // row's pin must survive a stale refresh just as an undelivered row's markers
  // must.
  noteMutationStateChange(ref);
  pinMutationRef(ref);
  // A committed row is undelivered work unless the stop barrier committed it
  // born-canceled.
  const committedUndelivered = record.state !== "canceled";
  if (committedUndelivered) noteUndeliveredMutation(ref, record.clientMutationId);
  // A background pin refresh can clear the ref's arm while this write was in
  // flight (it reads the outbox empty before the commit), so re-arm on a
  // successful non-canceled commit rather than wait for the next hydration's
  // re-arm in publishAndReconcileThreadHydration. Gated by the same replay-gate
  // predicate as the click's own arming: a pending hydration still keeps the
  // gate closed.
  if (committedUndelivered && dispatchReplayGateOpen(ref)) {
    dispatchableMutationRefs.add(ref);
  }
  notifyMutationPersistence([ref], { record });
  return record;
}

// A durable write that timed out may mean only that storage is slow to
// answer, so try it a second time before giving up. The click-time capture is
// awaited ONCE here, before the ladder, and every attempt reuses that same
// observation: re-reading it would let a Stop landing between attempts become
// the retry's own baseline (the fence the capture exists to hold). A rejecting
// capture (MutationStorageTimeoutError, VersionError, a retired connection)
// propagates, and enqueueMutationIntent decides between the fallback and a
// hard failure.
const MUTATION_DURABLE_WRITE_ATTEMPTS = 2;
const MUTATION_DURABLE_WRITE_RETRY_DELAY_MS = 50;

async function enqueueDurableMutation(
  runtime: MutationRuntime,
  intent: MutationIntent,
  onCommitted: ((record: MutationOutboxRecord) => void) | undefined,
  durableWrite: "enqueue" | "interruptAndCancel",
  barrierRead: Promise<number> | undefined,
): Promise<MutationOutboxRecord> {
  const barrier: MutationStopBarrier | undefined =
    barrierRead === undefined ? undefined : { stopEpoch: await barrierRead };
  for (let attempt = 1; ; attempt += 1) {
    try {
      return durableWrite === "interruptAndCancel"
        ? await runtime.outbox.enqueueInterruptAndCancel(intent, onCommitted)
        : await runtime.outbox.enqueueIntent(intent, onCommitted, barrier);
    } catch (error) {
      if (!isStorageUnavailable(error) || attempt >= MUTATION_DURABLE_WRITE_ATTEMPTS) throw error;
      await new Promise((resolve) => setTimeout(resolve, MUTATION_DURABLE_WRITE_RETRY_DELAY_MS));
    }
  }
}

// Whether an error is the storage-unavailable signal (the IndexedDB watchdog
// gave up on an open or a transaction): the one failure the direct fallback
// answers, because it does not prove the mutation's shape was rejected.
function isStorageUnavailable(error: unknown): boolean {
  return error instanceof MutationStorageTimeoutError;
}

// The fallback send's RPC is its only transport, so a failure on the wire is
// retried rather than surfaced. Only a transport failure earns the second
// attempt: a settled WireError is the daemon's own answer (a conflict or a
// malformed request cannot succeed on an identical retry), while a timeout or a
// dropped connection leaves the outcome unknown. The clientMutationId is minted
// ONCE, before the ladder, and every attempt reuses it - the daemon dedups by
// clientMutationId, so a reply lost after the mutation applied replays as a
// no-op on the retry instead of applying twice.
const MUTATION_FALLBACK_SEND_ATTEMPTS = 2;
// A dropped connection leaves the client "reconnecting", where a retry fired
// now only meets the client's own synchronous not-ready rejection. Wait for a
// ready client before the second attempt, bounded so a wedged hub cannot hang
// the send: 10s is the durable-write watchdog's own scale, which the composer
// already tolerates. The read paths never wait like this for a mutation because
// a blind retry could land twice - but every attempt here reuses one
// clientMutationId, so the daemon dedups them.
const MUTATION_FALLBACK_SEND_READY_WAIT_MS = 10_000;

// The imperative transport for a mutation whose durable write could not be
// made: the same plain RPC the non-durable operations issue. Delivery here is
// at-least-once, not deduplicated: the id is freshly minted and NOTHING durable
// holds it, so if this RPC lands but its response is lost, a retry of the same
// action mints a different id and the daemon cannot recognise the duplicate
// (the same gap Jesse's ruling documents in issue #3313). A rejection maps a
// conflict the same way the non-durable operations do.
async function dispatchMutationDirectly(client: AppwireClientLike, intent: MutationIntent): Promise<void> {
  const clientMutationId = createSecureUUID();
  let target = client;
  for (let attempt = 1; ; attempt += 1) {
    try {
      // Resolve the transport from the CURRENT target: a retry may run against
      // a rewired or waited-ready client, so the first client's request must
      // not be reused.
      const request = target.request as unknown as (
        method: string,
        params: Record<string, unknown>,
      ) => Promise<unknown>;
      await request.call(target, intent.method, { ...intent.payload, clientMutationId });
      return;
    } catch (err) {
      if (err instanceof WireError || attempt >= MUTATION_FALLBACK_SEND_ATTEMPTS) throw mapConflict(err);
      // A failed client that has closed for good can never become ready again,
      // but a manual retry (ConnectionBanner) may already have wired a
      // replacement - which can itself still be connecting. Retry against it
      // once it is ready (bounded); surface at once only when no replacement
      // has been wired at all, rather than waiting out the bound for a failed
      // client that can never recover. requireClient() is rewire-aware, so it
      // reads the current client, not the failed one.
      if (target.state === "closed" || target.terminalReason !== null) {
        let replacement: AppwireClientLike | null = null;
        try {
          const current = requireClient();
          if (current !== target) replacement = current;
        } catch {
          replacement = null;
        }
        if (replacement === null) throw mapConflict(err);
        if (replacement.state !== "ready") {
          try {
            replacement = await requireReadyClient(MUTATION_FALLBACK_SEND_READY_WAIT_MS);
          } catch {
            throw mapConflict(err);
          }
        }
        target = replacement;
        continue;
      }
      try {
        target = await requireReadyClient(MUTATION_FALLBACK_SEND_READY_WAIT_MS);
      } catch {
        throw mapConflict(err);
      }
    }
  }
}

// A non-send durable write (thread/clear, notes/human/set,
// turn/promoteQueuedAsSteer) can never take the direct fallback, so
// enqueueMutationIntent always resolves with its committed record. This asserts
// that contract instead of casting it: a cast would hand the caller an
// undefined record its type called present, where a violation here throws. The
// union stays enqueueMutationIntent's own return; this is the honest narrow for
// a caller whose method is never one of the composer send verbs.
async function enqueueCommittedMutation(
  intent: MutationIntent,
  onCommitted?: (record: MutationOutboxRecord) => void,
  durableWrite: "enqueue" | "interruptAndCancel" = "enqueue",
): Promise<MutationOutboxRecord> {
  const record = await enqueueMutationIntent(intent, onCommitted, durableWrite);
  if (record === undefined) {
    throw new Error(`threads store: ${intent.method} unexpectedly took the direct send fallback`);
  }
  return record;
}

async function enqueueMutation(
  ref: string,
  method: MutationIntent["method"],
  payload: Record<string, unknown>,
  optimisticDisplay: unknown,
  attachments?: InputAttachment[],
  durableWrite: "enqueue" | "interruptAndCancel" = "enqueue",
): Promise<void> {
  await enqueueCommittedMutation(
    {
      targetRef: ref,
      threadId: threadsStore.getState().threads.get(ref)?.threadId,
      instanceId: threadsStore.getState().threads.get(ref)?.instanceId,
      method,
      payload,
      attachments: durableAttachments(attachments),
      optimisticDisplay,
    },
    undefined,
    durableWrite,
  );
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
    instanceId: model.instanceId,
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
  if (n.method === "thread/queueChanged") {
    // consumedClientMutationIds names entries THIS push's own transition (a
    // drain) just took out of the queue (issue #1704): the daemon knows
    // exactly which ids it consumed, so those settle by the same positive-
    // evidence rule as the remaining, still-queued ids below. Validated the
    // same way the receipt path validates it: anything not an array of
    // non-empty strings settles nothing, never guessed at by spreading it.
    return [
      ...(n.params.queue.clientMutationIds ?? []),
      ...validConsumedClientMutationIds(n.params.consumedClientMutationIds),
    ];
  }
  // history/updated is the read-model replacement for evener/steering/injected,
  // item/started, item/completed, turn/started and turn/completed alike: it
  // carries the recorded form of every item a change affected (a steering
  // item included), so one pass over its items covers what all five used to
  // carry across their own separate shapes.
  if (n.method === "history/updated") {
    return (n.params.items ?? [])
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

function preserveLiveActiveTurn(snapshot: ThreadModel, live: ThreadModel | undefined): ThreadModel {
  const activeTurnId = snapshot.activeTurnId;
  if (
    activeTurnId === undefined ||
    live?.threadId !== snapshot.threadId ||
    threadInstanceID(live) !== threadInstanceID(snapshot) ||
    live?.activeTurnId !== activeTurnId ||
    snapshot.turns.some((turn) => turn.id === activeTurnId)
  )
    return snapshot;
  const liveTurn = live.turns.find((turn) => turn.id === activeTurnId);
  if (!liveTurn) return snapshot;
  return { ...snapshot, turns: [...snapshot.turns, liveTurn] };
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

// Records a new latest-window read against a v6 model's held history
// (issueLatestWindowRead), synchronously - before the request goes out - so
// two reads issued back to back (a targeted resync racing an initial read,
// say) get strictly increasing generations and the held model reflects the
// newer one immediately. A model with no history (not yet v6, or the ref's
// very first read) gets no generation: applyReadResponse has nothing to
// discard against, and hydrateThread's plain-replace path is what applies
// its response.
function issuedGenerationFor(model: ThreadModel | undefined): {
  baseModel: ThreadModel | undefined;
  requestGeneration: number | undefined;
} {
  if (!model?.history) return { baseModel: model, requestGeneration: undefined };
  const { model: bumped, requestGeneration } = issueLatestWindowRead(model);
  return { baseModel: bumped, requestGeneration };
}

// applyReadResponse (reducer.ts) discarded a hydration's response - it
// answered a request generation a later one already superseded (readDisposition
// "discard") - by returning the base model itself. The held model already
// reflects the newer read; publishing this response, or reconciling mutations
// against it, would overwrite that with older state.
function isDiscardedReadResult(pending: PendingThreadHydration, model: ThreadModel): boolean {
  return pending.baseModel !== undefined && model === pending.baseModel;
}

function ensureCacheLease(ref: string): void {
  threadsStore.setState((state) =>
    state.cacheLifetimes.get(ref)?.leaseEpoch !== undefined
      ? state
      : {
          cacheLifetimes: new Map(state.cacheLifetimes).set(ref, {
            ...state.cacheLifetimes.get(ref),
            leaseEpoch: tabCacheEpoch ?? 0,
          }),
        },
  );
}

// Final release removes every part of the lifetime, but only after the
// release flush has checked the still-armed shell/suppression gates.
function releaseCacheLifetime(lifetimes: Map<string, CacheLifetime>, ref: string): Map<string, CacheLifetime> {
  const next = new Map(lifetimes);
  next.delete(ref);
  return next;
}

// An authoritative publish ends verification, not ownership. Keep even epoch
// zero and suppression without a lease; neither may disappear with the shell.
function releaseCacheShell(lifetimes: Map<string, CacheLifetime>, ref: string): Map<string, CacheLifetime> {
  const lifetime = lifetimes.get(ref);
  if (!lifetime?.shell && lifetime?.anchor === undefined) return lifetimes;
  if (lifetime.leaseEpoch === undefined && !lifetime.suppressed) return releaseCacheLifetime(lifetimes, ref);
  return new Map(lifetimes).set(ref, { leaseEpoch: lifetime.leaseEpoch, suppressed: lifetime.suppressed });
}

function beginThreadHydration(
  ref: string,
  client: AppwireClientLike,
  model: ThreadModel | undefined,
  epoch: number,
): PendingThreadHydration {
  // Restored mutations hydrate pinned refs without an ensureThread claim.
  // Capture their lifetime before any base publishes or read starts, so a
  // clear during hydration suppresses them through their final release too.
  ensureCacheLease(ref);
  const attempt = (trackedHydrationAttempts.get(ref) ?? 0) + 1;
  trackedHydrationAttempts.set(ref, attempt);
  const { baseModel, requestGeneration } = issuedGenerationFor(model);
  if (baseModel && baseModel !== model) putThreadModel(ref, baseModel);
  const pending = {
    client,
    epoch,
    notifications: [],
    routing: pendingHydrationRouting(ref, baseModel ?? model),
    attempt,
    baseModel,
    requestGeneration,
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
  const { baseModel, requestGeneration } = issuedGenerationFor(model);
  if (baseModel && baseModel !== model) putWatchedThreadModel(ref, baseModel);
  const pending = {
    client,
    epoch,
    notifications: [],
    routing: pendingHydrationRouting(ref, baseModel ?? model),
    baseModel,
    requestGeneration,
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

  const live = threadsStore.getState().threads.get(ref);
  const { model: hydrated, appliedAt } = replayHydrationNotifications(
    preserveLiveActiveTurn(model, live),
    pending.notifications,
  );

  pendingThreadHydrations.delete(ref);
  putThreadModel(ref, hydrated);
  // The first authoritative publish ends the cached-shell window: the flag
  // and the gap rule's anchor live only from the shell's arming until this
  // read's publish (spec, "The load seam"); the lease outlives
  // the window — until the ref's final release — for the clear's suppression
  // (Task 10).
  threadsStore.setState((s) => {
    const cacheLifetimes = releaseCacheShell(s.cacheLifetimes, ref);
    return cacheLifetimes === s.cacheLifetimes ? s : { cacheLifetimes };
  });
  invalidateGoalResponseFallback(ref);
  invalidateNotesResponseFallback(ref);
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
  // A background note retry proves absence only for the record it carries and
  // must not reopen an unrelated blocked row of the same ref (#1716); every
  // other caller leaves this undefined for the ordinary target-wide reopen.
  reopenOnlyClientMutationId?: string,
): Promise<ThreadModel | null> {
  const published = publishThreadHydration(ref, pending, hydration.model);
  if (!published) return null;
  threadsStore.setState((state) => {
    const mutationAuthorityRefs = new Set(state.mutationAuthorityRefs);
    if (hydration.response.thread.evener.mutationStateAuthoritative === true) mutationAuthorityRefs.add(ref);
    else mutationAuthorityRefs.delete(ref);
    return { mutationAuthorityRefs };
  });
  if (published.status.type === "restartRequired" || hydration.response.thread.evener.resumeRequired === true) {
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
        const mutationStateAuthoritative = hydration.response.thread.evener.mutationStateAuthoritative === true;
        await runtime.dispatcher.reconcileIdentities(authoritativeIds);
        if (!current()) return;
        // A bounded transcript or a clear can omit an accepted mutation from
        // the live projection. Retry unresolved records with their original
        // mutation ID and payload: the daemon journal replays accepted work,
        // and the original instance fence rejects a retry after a clear.
        // Saved snapshots contain no authoritative daemon receipt history, even
        // after an incompatible daemon has been stopped. Persist uncertainty so
        // reopening that saved snapshot cannot release an already accepted send.
        if (
          !mutationStateAuthoritative ||
          published.status.type === "restartRequired" ||
          published.status.type === "notLoaded"
        ) {
          for (const record of await runtime.storage.listOutbox(ref)) {
            if (!current()) return;
            if (record.state === "submitting")
              await runtime.storage.markUnknown(record.clientMutationId, "blockedUnknown", { onlyAttempted: true });
          }
          notifyMutationPersistence([ref]);
        } else {
          // restoreProvenAbsent reopens EVERY blockedUnknown row of the ref
          // that this read did not name. A scoped caller must reopen only its
          // own record: every other blocked row joins the set this reopen
          // excludes, so it stays blockedUnknown for a later target-wide
          // reconciliation. An unscoped caller keeps that whole-target reopen.
          let reopenExcludedIds = authoritativeIds;
          if (reopenOnlyClientMutationId !== undefined) {
            reopenExcludedIds = new Set(authoritativeIds);
            for (const record of await runtime.storage.listOutbox(ref)) {
              if (record.state === "blockedUnknown" && record.clientMutationId !== reopenOnlyClientMutationId)
                reopenExcludedIds.add(record.clientMutationId);
            }
            if (!current()) return;
          }
          await runtime.dispatcher.restoreProvenAbsent(ref, reopenExcludedIds);
        }
        if (!current()) return;
        await refreshMutationPins(runtime, [ref]);
        // Descendant reads cannot prove an uncertain mutation absent. Explicitly
        // never-attempted intents need no receipt authority before first delivery.
        const mutationsReconciled =
          mutationStateAuthoritative ||
          (await runtime.storage.listOutbox(ref)).every((record) => record.attempted === false);
        // A newer incompatible snapshot owns a different obligation. An older
        // successful reconciliation cannot clear that newer restriction.
        if (
          current() &&
          mutationsReconciled &&
          published.status.type !== "restartRequired" &&
          hydration.response.thread.evener.resumeRequired !== true &&
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
          // Returning the same reference skips the merge and the listener
          // pass (zustand/vanilla drops identical states), and a healthy ref
          // settles neither fence on most reconciles.
          if (!state.mutationReconciliationFailures.has(ref) && !state.mutationReconciliationStorageBlocked.has(ref))
            return state;
          const mutationReconciliationFailures = new Set(state.mutationReconciliationFailures);
          mutationReconciliationFailures.delete(ref);
          const mutationReconciliationStorageBlocked = new Set(state.mutationReconciliationStorageBlocked);
          mutationReconciliationStorageBlocked.delete(ref);
          return { mutationReconciliationFailures, mutationReconciliationStorageBlocked };
        });
      }
    } catch (error) {
      if (
        pendingMutationReconciliations.get(ref) === reconciliation &&
        isCurrentMutationRuntime(runtime) &&
        pending.epoch === readyEpoch &&
        pending.client === wiredClient
      ) {
        // A storage-unavailable reconcile is not a mutation-state fact: the
        // same wedge that fences the durable write failed this read, so record
        // the ref aside as storage-blocked - the slice the send fallback may
        // admit - and let discovery retry the read on every pass. A genuine
        // failure recorded earlier (a real reconcile conflict, a blocked
        // shared record) must survive the timeout: erasing it would unfence
        // the fallback through the waivable slice, so the timeout only adds
        // the storage-blocked record. Any non-timeout failure keeps its old
        // meaning and supersedes the storage-blocked state, being the
        // stricter fence: record it in failures and clear the storage-blocked
        // record. A successful reconcile clears both.
        const storageBlocked = isStorageUnavailable(error);
        threadsStore.setState((state) => {
          const mutationReconciliationFailures = new Set(state.mutationReconciliationFailures);
          const mutationReconciliationStorageBlocked = new Set(state.mutationReconciliationStorageBlocked);
          if (storageBlocked) {
            mutationReconciliationStorageBlocked.add(ref);
          } else {
            mutationReconciliationFailures.add(ref);
            mutationReconciliationStorageBlocked.delete(ref);
          }
          return { mutationReconciliationFailures, mutationReconciliationStorageBlocked };
        });
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

  const live = threadsStore.getState().watchedThreads.get(ref);
  const replayed = replayHydrationNotifications(preserveLiveActiveTurn(model, live), pending.notifications);
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
): {
  next: Map<string, ThreadModel> | null;
  changedRefs: string[];
  acceptedRefs: string[];
  // Refs whose thread/status/changed fold moved them OFF the shut-down set: the
  // merely-resumable window those refs' snapshots described has ended, so their
  // recovery obligation is the caller's to clear (see handleNotification).
  endedResumeOnlyRefs: string[];
} {
  let next: Map<string, ThreadModel> | null = null;
  const changedRefs: string[] = [];
  const acceptedRefs: string[] = [];
  const endedResumeOnlyRefs: string[] = [];
  const routed = routeByNotificationKey(map, index, n, skippedRefs);
  if (!routed) return { next, changedRefs, acceptedRefs, endedResumeOnlyRefs };
  const accept = (model: ThreadModel): void => {
    acceptedRefs.push(model.ref);
  };
  if (Array.isArray(routed)) {
    for (const model of routed) {
      accept(model);
      const updated = applyNotification(model, n, now);
      if (updated === model) continue;
      next ??= new Map(map);
      next.set(model.ref, settleResumeOnlyOffShutdown(model, updated, endedResumeOnlyRefs));
      changedRefs.push(model.ref);
    }
    return { next, changedRefs, acceptedRefs, endedResumeOnlyRefs };
  }
  accept(routed);
  const updated = applyNotification(routed, n, now);
  if (updated !== routed) {
    next = new Map(map);
    next.set(routed.ref, settleResumeOnlyOffShutdown(routed, updated, endedResumeOnlyRefs));
    changedRefs.push(routed.ref);
  }
  return { next, changedRefs, acceptedRefs, endedResumeOnlyRefs };
}

// A folded notification that moved a ref OFF the shut-down set ends the
// merely-resumable shape that ref's snapshot described: the resume the hub bit
// was stamped for has started, so the snapshot's resumeOnlyFoldable bit must
// not outlive the status that carried it. The bit is written ONLY by a snapshot
// hydration (reducer.ts's threadFields), while the thread/status/changed branch
// keeps ...model, so without this a stale bit would ride a live status. The
// predicate reads no status of its own, so this is where a bit whose shut-down
// snapshot has ended is cleared - the bit belongs to the snapshot, not to every
// later frame. The ref is reported so the caller clears the recovery obligation
// the same snapshot armed. Only the off-set direction counts: a fold onto the
// set (or within it) keeps the window, and the reverse transition is a shutdown,
// not a resume.
function settleResumeOnlyOffShutdown(previous: ThreadModel, updated: ThreadModel, endedRefs: string[]): ThreadModel {
  if (!SHUT_DOWN_STATUSES.has(previous.status.type) || SHUT_DOWN_STATUSES.has(updated.status.type)) {
    return updated;
  }
  // Only a model that genuinely carried the snapshot's resume-only shape ends
  // here. A ref that was never resume-only - a restart-required daemon, an
  // unconfirmed force-stop exit, connection recovery - keeps both its bit
  // (already false) and its recovery obligation: an off-shut-down status fold
  // alone is not evidence the hub resumed it, so clearing that ref's fence
  // would admit mutations the hub still rejects.
  if (previous.resumeOnlyFoldable !== true) {
    return updated;
  }
  // A Stop this page started still owns the ref's obligation until its RPC
  // settles (the hub holds Stopping > 0, refusing even turn/start, for that
  // whole window), so the ref is not reported for the obligation clear. Its bit
  // still clears with the snapshot.
  if (!activeStops.has(updated.ref)) endedRefs.push(updated.ref);
  return invalidateResumeOnlyForReconnect(updated);
}

function handleNotification(n: AnyNotification): void {
  if (n.method === "evener/thread/resync") {
    // A resync always re-reads the named ref, legacy and v6 threads alike:
    // legacy has no history.invalidatedAtGeneration for the generic
    // invalidation trigger below to notice, and a v6 model's re-read
    // naturally replaces (rather than merges) on this response, since a
    // resync's epoch/bootGeneration is newer by construction - readDisposition
    // takes that from the response's own identity, with no separate
    // invalidation step needed. Short-circuits before the generic fold so the
    // invalidation trigger below never double-issues a second read for it.
    if (wiredClient) void handleReady(wiredClient, readyEpoch, n.params.ref);
    return;
  }
  if (n.method === "evener/auth/updated") {
    modelsEpoch += 1;
    modelsCache = null;
    inflightModelsList = null;
    inflightModelsListIsRefresh = false;
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
  // Accepted fallback-invalidating refs, one set per family: goal pushes
  // invalidate the goal fallback, notes pushes the notes fallback. A push never invalidates another family's
  // fallback (a urls/updated carries no note state, so it must not retire
  // a setHumanNote response commit).
  const acceptedGoalRefs = new Set<string>();
  const acceptedNotesRefs = new Set<string>();
  let acceptedPendingRefs: Set<string> | undefined;
  if (isFallbackInvalidatingPush(n.method)) {
    acceptedPendingRefs = n.method === "evener/goal/updated" ? acceptedGoalRefs : acceptedNotesRefs;
  }
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
    collectPendingRefs(pendingThreadHydrations, n, refs, acceptedPendingRefs);
    if (refs.size > 0) pendingRefs = refs;
  }
  let pendingWatchedRefs: ReadonlySet<string> = EMPTY_PENDING_REFS;
  if (pendingWatchedHydrations.size > 0) {
    const refs = new Set<string>();
    collectPendingRefs(pendingWatchedHydrations, n, refs, acceptedPendingRefs);
    if (refs.size > 0) pendingWatchedRefs = refs;
  }
  const {
    next: nextThreads,
    changedRefs: changedThreads,
    acceptedRefs: acceptedThreads,
    endedResumeOnlyRefs: endedThreadsResumeOnly,
  } = applyToMap(threads, threadsIndex, n, now, pendingRefs);
  const {
    next: nextWatchedThreads,
    changedRefs: changedWatchedThreads,
    acceptedRefs: acceptedWatchedThreads,
    endedResumeOnlyRefs: endedWatchedResumeOnly,
  } = applyToMap(watchedThreads, watchedThreadsIndex, n, now, pendingWatchedRefs);
  if (n.method === "evener/goal/updated") {
    for (const ref of acceptedThreads) acceptedGoalRefs.add(ref);
    for (const ref of acceptedWatchedThreads) acceptedGoalRefs.add(ref);
    for (const ref of acceptedGoalRefs) invalidateGoalResponseFallback(ref);
  }
  if (n.method === "evener/notes/updated") {
    for (const ref of acceptedThreads) acceptedNotesRefs.add(ref);
    for (const ref of acceptedWatchedThreads) acceptedNotesRefs.add(ref);
    for (const ref of acceptedNotesRefs) invalidateNotesResponseFallback(ref);
  }
  if (!nextThreads && !nextWatchedThreads) return;

  // history.invalidatedAtGeneration becoming set - or moving to a newer
  // signal, on re-invalidation - is the store's cue to issue a fresh
  // latest-window read (task-15-report.md's store contract). It covers
  // evener/thread/resync, and equally a history/updated or backfill page that
  // names another boot generation, a newer epoch, or an unseen incarnation
  // (reducer.ts's classifySignal): the reducer marks the thread invalid,
  // whatever notification carried the signal, and this is the one place that
  // reacts to it, so a re-read is never missed nor duplicated per method.
  const invalidatedThreadRefs = newlyInvalidatedHistoryRefs(threads, nextThreads, changedThreads);
  const invalidatedWatchedRefs = newlyInvalidatedHistoryRefs(watchedThreads, nextWatchedThreads, changedWatchedThreads);

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

  // A status frame that moved a ref off the shut-down set ends the
  // merely-resumable window: the recovery obligation the same shut-down
  // snapshot armed is stale now, and leaving it armed would fence a session the
  // hub has resumed (stillFenced keeps Send and Queue off until a fresh read
  // clears it). Clear it here, in the same step the model's stale
  // resumeOnlyFoldable bit was cleared, so the composer routes normally. The
  // fold withholds a ref a Stop this page started still owns (see
  // settleResumeOnlyOffShutdown), so every reported ref is this clear's to make.
  if (endedThreadsResumeOnly.length > 0 || endedWatchedResumeOnly.length > 0) {
    const endedRefs = new Set([...endedThreadsResumeOnly, ...endedWatchedResumeOnly]);
    threadsStore.setState((state) => {
      const restartBlockingObligations = new Map(state.restartBlockingObligations);
      for (const ref of endedRefs) restartBlockingObligations.delete(ref);
      return { restartBlockingObligations };
    });
  }

  if (invalidatedThreadRefs.length > 0 || invalidatedWatchedRefs.length > 0) {
    const client = wiredClient;
    const epoch = readyEpoch;
    if (client) {
      // Fire-and-forget, the same idiom the old direct resync handling used:
      // refreshTrackedThread/refreshWatchedThread own their own currency
      // checks (client/epoch/refCount), so a race with a release or a
      // reconnect that lands before this read resolves is already handled.
      for (const ref of invalidatedThreadRefs) void refreshTrackedThread(client, epoch, ref, true);
      for (const ref of invalidatedWatchedRefs) void refreshWatchedThread(client, epoch, ref, true);
    }
  }
}

// Refs among `candidates` whose history.invalidatedAtGeneration is set in
// `next` and differs from what it was in `prev` - a fresh invalidation
// (first set, or re-armed at a newer signal; reducer.ts's classifySignal).
// Not every accepted notification invalidates, so this scans only the refs
// applyToMap actually changed, not the whole map.
function newlyInvalidatedHistoryRefs(
  prev: Map<string, ThreadModel>,
  next: Map<string, ThreadModel> | null,
  candidates: readonly string[],
): string[] {
  if (!next) return [];
  const refs: string[] = [];
  for (const ref of candidates) {
    const before = prev.get(ref)?.history?.invalidatedAtGeneration;
    const after = next.get(ref)?.history?.invalidatedAtGeneration;
    if (after !== undefined && after !== before) refs.push(ref);
  }
  return refs;
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
  invalidateNotesResponseFallback(ref);
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
  // A durably deleted target is terminal (see the `deletedRefs` field doc):
  // retire the lifecycle — cancelling its retry and settling any owner — and
  // arm nothing. markThreadDeletedIfFenced recorded the flag on the way here.
  if (threadsStore.getState().deletedRefs.has(ref)) {
    retireOwnedHydration(kind, ref);
    return;
  }
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

// Both of a fenced refreshThread's fence evaluations owe the ref they cancel
// the same unwind: a Stop-canceled refresh must not leave standing what its
// session banked on the way - the dispatch gate the enqueue path or an
// earlier refresh opened, and the restart-blocking obligation a proven
// snapshot cleared - or the outbox's own later discovery scan would dispatch
// queued mutations on the strength of both despite the acknowledged Stop. The
// ref leaves the dispatchable set, and the obligation re-arms (the forceStop
// tail's own retention rule) until a fresh snapshot proves it can clear.
//
// But only the ref's NEWEST tracked-hydration attempt may unwind (the attempt
// is the caller's own beginThreadHydration stamp): a superseded refresh
// published nothing to bank, and an overtaken one's banking belongs to the
// newer attempt's fresh proof, so a stale cancellation re-arming recovery
// here would clobber a resume that already succeeded - including closing the
// newer attempt's reconciliation out of its own clear, whose captured
// obligation symbol would no longer match. A refresh that never began
// (no attempt) banked nothing either and does not unwind.
function unwindStopCanceledRefresh(ref: string, attempt?: number): void {
  if (attempt === undefined || trackedHydrationAttempts.get(ref) !== attempt) return;
  dispatchableMutationRefs.delete(ref);
  threadsStore.setState((state) => ({
    restartBlockingObligations: new Map(state.restartBlockingObligations).set(ref, Symbol()),
  }));
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
  beforePublish?: () => void,
  reopenOnlyClientMutationId?: string,
): Promise<number | undefined> {
  // Returns the tracked-hydration attempt this refresh began, or undefined
  // when it never began one (an unowned ref, or an in-flight non-targeted
  // predecessor): refreshThread's tail recheck passes that attempt to
  // unwindStopCanceledRefresh, whose currency check needs to know which
  // attempt a cancellation belongs to.
  if ((refCounts.get(ref) ?? 0) <= 0 && !pinnedMutationRefs.has(ref)) return undefined;
  const previous = pendingThreadHydrations.get(ref);
  if (!targetedResync && previous?.client === client && previous.epoch === epoch) return undefined;
  const pending = beginThreadHydration(ref, client, threadsStore.getState().threads.get(ref), epoch);
  // No pre-check here: pending.client is this `client` and pending.epoch is this
  // `epoch`, so publishThreadHydration re-decides exactly the same thing one
  // frame later, and returning null from there reconciles nothing either. The
  // gate lives in one place.
  const hydration = hydrateAndSubscribe(client, ref, Date.now(), pending).then((result) => {
    // Evaluated synchronously immediately before publication, with no await in
    // between: a canceled read never reaches putThreadModel, the
    // mutation-authority publication, or capture of the current Stop obligation.
    try {
      beforePublish?.();
    } catch (error) {
      // A Stop that lands while the refreshed read is still in flight cancels
      // HERE, before publication: the rejection propagates out of this
      // refresh's await and back through refreshThread's, so it never reaches
      // the tail recheck whose catch carried the unwind before. This
      // evaluation must unwind the canceled refresh itself, or the state
      // recovery banked before it - the open dispatch gate, the cleared
      // restart-blocking obligation - stays standing for the outbox's own
      // later discovery scan to dispatch against despite the acknowledged
      // Stop. The unwind carries this attempt so only the ref's newest
      // hydration performs it: a superseded read's late cancellation is
      // still its caller's honest rejection, but it banked nothing, and the
      // newer attempt owns the ref's convergence from fresh state.
      unwindStopCanceledRefresh(ref, pending.attempt);
      throw error;
    }
    if (isDiscardedReadResult(pending, result.model)) return pending.baseModel ?? null;
    return publishAndReconcileThreadHydration(ref, pending, result, reopenOnlyClientMutationId);
  });
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
  return pending.attempt;
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
    isDiscardedReadResult(pending, model)
      ? (pending.baseModel ?? null)
      : publishWatchedHydration(ref, pending, model, includeTurns, generation),
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
async function handleReady(
  client: AppwireClientLike,
  epoch: number,
  targetRef?: string,
  // The targeted tail dispatches every dispatchable record of the target in
  // FIFO order, which is right when the caller is user intent on that session.
  // A caller that must not do that - a background note save, which must not
  // submit other pending mutations for the session - passes
  // `suppressTargetDispatch`; `false` suppresses the schedule. Evaluated
  // synchronously at the scheduling point, after every await.
  // `reopenOnlyClientMutationId` narrows the reconciliation's reopen to the
  // one record for the same caller: a background note retry must not return an
  // unrelated blocked row to submitting (#1716).
  options?: { suppressTargetDispatch?: () => boolean; reopenOnlyClientMutationId?: string },
): Promise<void> {
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
    ...Array.from(refs, (ref) =>
      refreshTrackedThread(client, epoch, ref, targetedResync, false, undefined, options?.reopenOnlyClientMutationId),
    ),
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
    for (const ref of discovered) pinMutationRef(ref);
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
  } else if (targetRef && dispatchableMutationRefs.has(targetRef) && (options?.suppressTargetDispatch?.() ?? true)) {
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
// teardownWiring retires the current connection generation: its handlers are
// unsubscribed and its generation-scoped bookkeeping (epochs, wire
// subscriptions, owned hydrations, dispatch state) is cleared. rewireClient
// calls it before wiring the next client; detachClient calls it when
// connectionStore drops its client entirely. Both leave wiredClient not yet
// updated by the caller, so a stale firing between the two calls still sees
// the outgoing client's identity (never a half-swapped one).
function teardownWiring(): void {
  readyEpoch += 1;
  threadsStore.setState({ mutationAuthorityRefs: new Set() });
  // Old leases belong to the outgoing client. The next hydration joins the
  // replacement client's membership without affecting activity owners.
  releaseThreadSubscriptions();
  retireAllOwnedHydrations();
  dispatchReadyClient = null;
  dispatchReadyEpoch = -1;
  dispatchableMutationRefs.clear();
  unwireNotification?.();
  unwireNotification = null;
  unwireReady?.();
  unwireReady = null;
}

function rewireClient(client: AppwireClientLike): void {
  if (client === wiredClient) return;
  teardownWiring();
  wiredClient = client;
  unwireNotification = client.onNotification(handleNotification);
  unwireReady = client.onReady(
    readyGenerationCallback(
      client,
      () => wiredClient,
      () => {
        readyEpoch += 1;
        threadsStore.setState({ mutationAuthorityRefs: new Set() });
        // onReady is the SAME client reconnecting: its old connection's
        // subscriptions are server-side gone too, even though the client object
        // survives. handleReady re-subscribes the still-tracked refs.

        retireAllOwnedHydrations();
        dispatchReadyClient = null;
        dispatchReadyEpoch = -1;
        dispatchableMutationRefs.clear();
        invalidateHeldHistoriesForReconnect();
        void handleReady(client, readyEpoch);
      },
    ),
  );
  // onReady only fires on a FUTURE transition into "ready" (AppwireClient/
  // FakeClient both dispatch it from within setState/emitStateChange) — it
  // does NOT fire retroactively for a client that is already ready by the
  // time we subscribe. A manual retry's fresh client is typically already
  // ready at this point (ConnectionBanner awaits the new client's own
  // connect() before ever handing it to connectionStore.connect()), so
  // without this, swapping to an already-ready client would never
  // re-subscribe/re-hydrate this store's tracked refs at all.
  if (client.state === "ready") {
    invalidateHeldHistoriesForReconnect();
    void handleReady(client, readyEpoch);
  }
}

// detachClient tears the wiring down when connectionStore loses its client
// (state.client === null — a disconnect or reset). Without it wiredClient
// keeps pointing at the last client and its onNotification/onReady handlers
// stay registered, so a detached client that later reaches "ready" still
// passes readyGenerationCallback's currentClient() guard (which reads
// wiredClient) and runs handleReady for a connection this store no longer
// holds (issue #1749).
function detachClient(): void {
  teardownWiring();
  wiredClient = null;
}

// A new connection (a reconnect, or a swapped-in client that is already
// ready) cannot be trusted to tell a live merge from a replace by identity
// alone: the daemon it lands on can report the very same boot generation,
// epoch and incarnation the client already held, even though entries were
// missed over the gap (task-15-report.md's "Live-merge gap" concern). Every
// v6 model this store still holds is therefore marked invalid before the
// reconnect's re-read goes out, so that read always replaces whole history
// rather than merging by version. A no-op the first time a client ever
// connects: nothing is tracked yet.
//
// The same connection change also invalidates the resumeOnlyFoldable bit: it
// is the PREVIOUS connection's answer (applyThreadResumeRequirement stamped it
// on a snapshot that connection produced), and the hub's connection-recovery
// fence can refuse a folded turn/start on the new connection. Clearing it here
// - the store's single connection-generation hook - is what keeps a send from
// folding from a bit the current connection has not re-stamped; a fresh read
// on the current connection restores folding.
function invalidateHeldHistoriesForReconnect(): void {
  const { threads, watchedThreads } = threadsStore.getState();
  for (const [ref, model] of threads) {
    const next = invalidateResumeOnlyForReconnect(model.history ? invalidateHistory(model) : model);
    if (next !== model) putThreadModel(ref, next);
  }
  for (const [ref, model] of watchedThreads) {
    const next = invalidateResumeOnlyForReconnect(model.history ? invalidateHistory(model) : model);
    if (next !== model) putWatchedThreadModel(ref, next);
  }
}

// Clears the retained resumeOnlyFoldable bit: the outgoing connection's answer
// on a reconnect, and the shut-down snapshot a status fold ended. A model with
// an absent or already-false bit is returned in place, so neither caller writes
// for the sessions that were not foldable.
function invalidateResumeOnlyForReconnect(model: ThreadModel): ThreadModel {
  return model.resumeOnlyFoldable === true ? { ...model, resumeOnlyFoldable: false } : model;
}

// The single reactive trigger for rewireClient: every connectionStore
// change is checked for a (possibly new) client, and rewireClient itself
// no-ops unless the reference actually changed — so this fires harmlessly
// on state-only changes (e.g. a client's own onStateChange mirroring) too.
// Registered once, at module load, same lifetime as this module's other
// singleton bookkeeping (refCounts, wiredClient, ...).
connectionStore.subscribe((state) => {
  if (state.client?.state !== "ready" && threadsStore.getState().mutationAuthorityRefs.size > 0) {
    threadsStore.setState({ mutationAuthorityRefs: new Set() });
  }
  if (state.client) rewireClient(state.client);
  else if (wiredClient !== null) detachClient();
});

// requireClient reads the client connection.ts wired via
// connectionStore.getState().connect(client) — threads.ts has no
// connect() of its own in the locked interface, so it rides connection.ts's
// single wiring point. The one thing that is threads-specific is the
// defensive rewireClient() call (see above), so it takes only the shared
// resolver and routes every client read through this rewire-aware wrapper -
// nothing here hands out a bare port whose request/onNotification would
// silently skip the rewire.
const { requireClient: resolveCurrentClient } = connectedClientPort("threads");
function requireClient(): AppwireClientLike {
  const client = resolveCurrentClient();
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
// both attempts land. The one mutation that does wait here is
// enqueueMutationIntent's storage-unavailable fallback send
// (dispatchMutationDirectly): it reuses ONE clientMutationId across its
// attempts, so the daemon dedups a replay and the double-land reason above
// does not hold.
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
  mutationReconciliationStorageBlocked: new Set(),
  restartBlockingObligations: new Map(),
  mutationAuthorityRefs: new Set(),
  resumedIdentities: new Map(),
  resumeFailures: new Map(),
  stoppingRefs: new Set(),
  frameTimes: new Map(),
  hydrations: new Map(),
  watchedThreads: new Map(),
  deletedRefs: new Set(),
  cacheLifetimes: new Map(),
  clearInFlight: undefined,

  clearResumedIdentity(ref) {
    threadsStore.setState((state) => {
      if (!state.resumedIdentities.has(ref)) return {};
      const resumedIdentities = new Map(state.resumedIdentities);
      resumedIdentities.delete(ref);
      return { resumedIdentities };
    });
  },

  async ensureThread(ref) {
    let client = requireClient();
    const count = refCounts.get(ref) ?? 0;
    if (count === 0) {
      ensureGenerations.set(ref, (ensureGenerations.get(ref) ?? 0) + 1);
      // A pinned model keeps its lifetime after the last pane releases it.
      ensureCacheLease(ref);
    }
    const generation = ensureGenerations.get(ref) ?? 0;
    refCounts.set(ref, count + 1);
    syncThreadSubscription(ref, client);
    if (threadsStore.getState().threads.has(ref)) return; // already hydrated: no re-read

    // The claim's hydration epoch, observed when the pane claimed the ref —
    // BEFORE the serial cache lookup, because the lookup widens the
    // claim-to-arming window by milliseconds and a ready transition inside
    // it must meet the same contract as one during the claim's read. A
    // lookup MISS arms at the observed epoch, exactly as the pre-lookup
    // claim did: the epoch-current replacement then comes from the same
    // machinery as before the seam (handleReady's refresh of a pending
    // hydration, or the re-arm loop after the stale epoch's publish is
    // refused), so only a matching client and epoch may share the pending
    // hydration — Session's deferred-until-ready handshake pins exactly this
    // claim-then-replace pair (Session.test.tsx). A lookup HIT publishes a
    // model — the shell — and the arming that follows serves that model, so
    // it commits to the CURRENT epoch: the ready pass has already gone by
    // and nothing would ever replace a stale arming, which would strand the
    // pane on an unverified shell with no live read behind it.
    const claimEpoch = readyEpoch;

    // The cached-shell lookup (spec, "The load seam"): serial by necessity —
    // the held identity must exist before issueLatestWindowRead runs — and
    // bounded by its own 250 ms deadline. The creator alone publishes and
    // arms; a joiner sees the shell (threads.has) or the hydration
    // (inflightHydrates) and joins that instead.
    let cachedBase: ThreadModel | undefined;
    if (!threadsStore.getState().threads.has(ref) && !inflightHydrates.has(ref)) {
      const found = await joinCacheLookup(ref);
      const state = threadsStore.getState();
      // The creator rechecks after the await: a pane that released while the
      // lookup was in flight, or a ref the deletion fence durably rejected,
      // publishes nothing and arms nothing — a closed pane must not send a
      // cold read in its own name. (A concurrent holder that published while
      // we looked keeps its model; the guard below just declines to arm the
      // shell over it.)
      if ((refCounts.get(ref) ?? 0) === 0 || ensureGenerations.get(ref) !== generation || state.deletedRefs.has(ref))
        return;
      if (found !== undefined) {
        const captured = found.epoch;
        // The first durable observation establishes this still-unpublished
        // claim's epoch. Already-published deadline fallbacks keep their old
        // lease, and later observations suppress rather than rebind them.
        if (tabCacheEpoch === undefined && state.clearInFlight === undefined && !state.threads.has(ref)) {
          threadsStore.setState((s) => ({
            cacheLifetimes: new Map(s.cacheLifetimes).set(ref, {
              ...s.cacheLifetimes.get(ref),
              leaseEpoch: captured,
            }),
          }));
        }
        onCacheEpochObserved(captured);
        const epochMatch =
          captured === tabCacheEpoch && (state.clearInFlight === undefined || captured >= state.clearInFlight);
        if (epochMatch && found.record !== undefined && !state.threads.has(ref)) {
          const shell = threadModelFromCache(found.record, Date.now());
          const anchor = newestItemPosition(found.record.history.turns);
          threadsStore.setState((s) => ({
            cacheLifetimes: new Map(s.cacheLifetimes).set(ref, {
              ...s.cacheLifetimes.get(ref),
              shell: true,
              ...(anchor === undefined ? {} : { anchor }),
            }),
          }));
          cachedBase = shell;
        }
      }
    }

    const startHydration = (
      hydrationClient: AppwireClientLike,
      hydrationEpoch: number,
    ): Promise<ThreadModel | null> => {
      const pending = beginThreadHydration(
        ref,
        hydrationClient,
        cachedBase ?? threadsStore.getState().threads.get(ref),
        hydrationEpoch,
      );
      const hydration = hydrateAndSubscribe(hydrationClient, ref, Date.now(), pending)
        .then(async (result) => {
          const model = await publishAndReconcileThreadHydration(ref, pending, result);
          // A message queued while this read was in flight found the ref not
          // yet dispatchable, so its enqueue discovery left it waiting.
          // Reconciliation has now opened the gate; deliver it here rather
          // than on the outbox's next periodic scan.
          const runtime = getMutationRuntime();
          if (model && runtime) scheduleMutationDispatch(runtime, [ref]);
          return model;
        })
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
    if (!inflight) inflight = startHydration(client, cachedBase !== undefined ? readyEpoch : claimEpoch);
    cachedBase = undefined; // the claim's arming consumed the shell; every re-arm reads the current model
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
          // Durable deletion is the other terminal: return the result of the
          // lifecycle scheduleOwnedHydrationRetry already retired (see the
          // `deletedRefs` field doc) instead of re-arming another read.
          if (threadsStore.getState().deletedRefs.has(ref)) return;
          if (wiredClient !== inflightClient || readyEpoch !== inflightEpoch) {
            if (threadsStore.getState().threads.has(ref)) return;
            // requireReadyClient re-reads the CURRENT client on the way out,
            // so a client captured before the wait is never stamped onto a
            // hydration that outlived it. Same shape as the re-arm below and
            // as both of watchThread's.
            client = await requireReadyClient();
            if (ensureGenerations.get(ref) !== generation || (refCounts.get(ref) ?? 0) <= 0) return;
            inflight = inflightHydrates.get(ref) ?? startHydration(client, readyEpoch);
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
        if (threadsStore.getState().deletedRefs.has(ref)) return;

        client = await requireReadyClient();
        if ((refCounts.get(ref) ?? 0) <= 0) return;
        // Recheck after the possibly-long ready wait: a deletion fence can
        // land while this loop is waiting out a reconnect, and a ref known
        // deleted must not start another read on becoming ready.
        if (threadsStore.getState().deletedRefs.has(ref)) return;
        inflight = inflightHydrates.get(ref);
        if (!inflight) inflight = startHydration(client, readyEpoch);
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
    syncThreadSubscription(ref);
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
    // The Stop generation is the one per-ref structure release must NOT tear
    // down: it is the only cancellation evidence fences captured elsewhere
    // read, and those fences have no registered lifetime to prune against.
    // Retention is what lets a fence captured before a Stop keep firing after
    // the ref's last holder lets go (see the map's own comment above).
    // A watched lifecycle may still hold this ref (watchRefCounts), and its
    // model stays; only the pane's own tracking goes. Unsubscribe the wire
    // subscription when this was the last holder of either kind, so the hub
    // stops relaying a thread nobody renders and its relay can idle out.

    // frameTimes is dropped in lockstep — an untracked ref has no business
    // holding onto a liveness trace a future ensureThread() of the same ref
    // should start fresh, the same way it re-reads a fresh model.
    flushCacheWrite(ref); // the tail commits before the model leaves the map
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
    syncThreadSubscription(ref, client);
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
        // Durable deletion is terminal for this watcher (see the `deletedRefs`
        // field doc), and it is checked here - before the client/epoch branch
        // below, exactly as ensureThread does - so a fenced rejection that
        // coincides with a rewire returns instead of starting another read
        // against a ref already known deleted.
        if (threadsStore.getState().deletedRefs.has(ref)) return;
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
      if (threadsStore.getState().deletedRefs.has(ref)) return;

      client = await requireReadyClient();
      // Recheck after the possibly-long ready wait: a deletion fence can land
      // while this loop waits out a reconnect, and a ref known deleted must
      // not start another read on becoming ready.
      if (threadsStore.getState().deletedRefs.has(ref)) return;
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
    syncThreadSubscription(ref);
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

    removeWatchedThreadModel(ref);
  },

  async refreshThread(ref, beforePublish): Promise<void> {
    const deadline = Date.now() + REQUIRE_READY_TIMEOUT_MS;
    let client: AppwireClientLike;
    do {
      const remaining = deadline - Date.now();
      if (remaining <= 0) {
        throw new ClientNotReadyError("threads store: timed out waiting for a ready client");
      }
      await requireReadyClient(remaining);
      client = requireClient();
    } while (client.state !== "ready");
    const attempt = await refreshTrackedThread(client, readyEpoch, ref, true, true, beforePublish);
    // beforePublish was evaluated before publication, but reconciliation above
    // runs asynchronously after it. A Stop acknowledged in that window must
    // cancel the dispatch this refresh earned too, exactly as handleReady's
    // targeted tail rechecks its own fence at the scheduling point. The
    // rejection lands only after publication, so what the refresh earned on
    // the way is unwound with it: publication opened the ref's dispatch gate
    // and reconciliation may have cleared its recovery-blocked obligation,
    // and a later outbox discovery would dispatch queued mutations on the
    // strength of both if the fence left them banked.
    try {
      beforePublish?.();
    } catch (error) {
      // Stop canceled this refresh, so its earned dispatchability goes with
      // it: close the gate and re-arm recovery (the forceStop tail's own
      // retention rule) until a fresh snapshot proves it can clear - but only
      // while this refresh is still the ref's newest hydration attempt. An
      // overtaken refresh's cancellation is its caller's honest rejection,
      // while the newer attempt's fresh proof owns the ref now; unwinding
      // here would re-arm recovery over it, closing the newer attempt's
      // reconciliation out of its own clear.
      unwindStopCanceledRefresh(ref, attempt);
      throw error;
    }
    const runtime = getMutationRuntime();
    if (runtime) scheduleMutationDispatch(runtime, [ref]);
  },

  resumeSession(ref) {
    return resumeFencedForSend(ref);
  },

  async loadOlderTurns(ref) {
    // A shell's cursor belongs to whichever window the reconcile settles on;
    // paging below a shell the gap rule is about to replace races that
    // replacement (spec, "Scroll-back"). Scroll-back waits for the read.
    if (threadsStore.getState().cacheLifetimes.get(ref)?.shell) return;
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

  async send(ref, text, attachments, skillNames, commandNames, mentions) {
    // The recovery fence is enforced at the shared admission every durable
    // action funnels through (enqueueMutationIntent's central check), so the
    // alternate send paths - the palette's slash fallthrough, the ask dock's
    // batch send, a failed turn's Retry - hear the same refusal the surfaces
    // render, with nothing parked behind it.
    //
    // The Send-resumes face is the one shape the fence admits: its send parks
    // (no RPC leaves while the obligation stands). After the durable write the
    // store runs the resume the standalone button used to, and the deferred
    // dispatch tails drain the parked row once the resume reconciles. The face
    // is read AFTER the write: a hydration that landed while the write was in
    // flight is the snapshot the admission saw, and re-reading here keeps the
    // drive from being skipped (or spuriously started) across that window.
    await enqueueMutationIntent(
      composerMutationIntent(ref, "send", text, attachments, skillNames, commandNames, mentions),
    );
    if (sendResumesLocalModel(ref)) void resumeFencedForSend(ref);
  },

  async steer(ref, text, attachments, skillNames, commandNames, mentions) {
    await enqueueMutationIntent(
      composerMutationIntent(ref, "steer", text, attachments, skillNames, commandNames, mentions),
    );
  },

  async queue(ref, text, attachments, skillNames, commandNames, mentions) {
    await enqueueMutationIntent(
      composerMutationIntent(ref, "queue", text, attachments, skillNames, commandNames, mentions),
    );
  },

  async interrupt(ref) {
    cancelPendingUserIntents(ref);
    // Same mid-swap reach as forceStop's cancel above: a Stop pressed on the
    // old ref while a resume is moving its rows to the resumed ref must cancel
    // the moved rows too, or the message delivers under a Stop.
    await cancelResumeSwapTarget(ref);
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
      undefined,
      // The click is the cancel moment (stop-cancellation-outbox §4): the
      // ref's non-attempted rows turn "canceled" in the same transaction that
      // enqueues the interrupt, so both are durable or neither is.
      "interruptAndCancel",
    );
  },

  async drainAsSteer(ref, text, attachments, skillNames, commandNames, mentions) {
    await enqueueMutationIntent(
      composerMutationIntent(ref, "drain", text, attachments, skillNames, commandNames, mentions),
    );
  },

  async promoteQueuedAsSteer(ref, index, expectedEntryId, display) {
    // The entry id is the precondition that matters: it names the message being
    // promoted, so a queue that shifted underneath is caught without needing a
    // turn id that would only add a second way to fail.
    await enqueueMutation(
      ref,
      "turn/promoteQueuedAsSteer",
      { ref, index, expectedInstanceId: expectedInstanceID(ref), expectedEntryId },
      {
        method: "turn/promoteQueuedAsSteer",
        input: [
          ...(display.text !== "" ? [{ type: "text", text: display.text }] : []),
          ...(display.skillNames ?? []).map((name) => ({ type: "skill", name })),
          ...(display.commandNames ?? []).map((name) => ({ type: "command", name })),
        ],
      },
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

  async setHumanNote(ref, note, expectedInstanceId, onCommitted) {
    const model = trackedThreadModel(ref);
    if (!canWriteHumanNote(model)) throw new Error("Session cannot accept notes");
    const instanceId = threadInstanceID(model) ?? "";
    if (expectedInstanceId !== undefined && expectedInstanceId !== instanceId)
      throw new Error("Session instance changed");
    const generation = (notesUpdateGenerations.get(ref) ?? 0) + 1;
    notesUpdateGenerations.set(ref, generation);
    return enqueueCommittedMutation(
      {
        targetRef: ref,
        threadId: model?.threadId,
        instanceId: model?.instanceId,
        method: "notes/human/set",
        payload: { ref, expectedInstanceId: expectedInstanceId ?? instanceId, note },
        attachments: [],
        optimisticDisplay: null,
      },
      (record) => {
        notesLatestIntentSequences.set(ref, Math.max(notesLatestIntentSequences.get(ref) ?? 0, record.intentSequence));
        onCommitted?.(record);
      },
    );
  },

  async removeURL(ref, id) {
    const model = trackedThreadModel(ref);
    if (!canWriteHumanNote(model)) throw new Error("Session cannot accept notes");
    const client = requireClient();
    try {
      const response = await client.request("urls/remove", {
        ref,
        clientMutationId: createSecureUUID(),
        expectedInstanceId: threadInstanceID(model) ?? "",
        id,
      });
      // The response carries no state; evener/urls/updated is authoritative.
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
    await enqueueCommittedMutation(clearMutationIntent(ref));
    // A clear is fenced by the model's instance id, so it can dispatch while
    // an older resync read is in flight. Its response is the newer cut and
    // retires that read in applyClearResponse.
    dispatchableMutationRefs.add(ref);
    await runtime.dispatcher.dispatchTargets([ref]);
    await refreshMutationPins(runtime, [ref]);
  },

  async forceStop(ref) {
    cancelPendingUserIntents(ref);
    // Arm the stopping fence and the recovery obligation synchronously with the
    // drain, BEFORE the first await: the hub holds Stopping > 0 for the whole
    // window and refuses even turn/start there (cmd/evener-hub's
    // sessionActionRecoveryError), so the store's own admission -
    // currentDispatchClient, enqueueMutationIntent, and every control surface
    // that reads this obligation - must fence that window too, not only the
    // restartRequired state the drain leaves behind. The in-flight Stop is not
    // itself a fence, so arming after the cancellation write's await would leave
    // that write's window unfenced. beginStop arms the group's shared fence and
    // captures the pre-group obligation on the 0 -> 1 transition; endStop
    // settles both when the last overlapping stop returns.
    beginStop(ref);
    // Whether this stop reached a daemon signal. A Stop that proceeds keeps the
    // fence; only a group in which no stop ever signalled restores the captured
    // obligation, on its last return.
    let signalled = false;
    try {
      // Write-first (stop-cancellation-outbox §4): the cancellation lands
      // durably before the stop RPC, so a storage failure aborts the stop here
      // with the daemon untouched and the user free to retry.
      await cancelUnattemptedMutations(ref);
      // A Stop pressed on the old ref while a resume is mid-swap must also
      // cancel the rows the drive already moved to the resumed ref.
      await cancelResumeSwapTarget(ref);
      // Resolve the client in its own step, so a lookup failure is
      // distinguishable by construction from a signal failure: no client
      // means no signal reached any daemon, so - exactly like the
      // cancellation-storage abort above - the obligation is restored on the
      // group's last return, and refreshThread must NOT be kicked (while
      // offline it cannot clear the fence and only leaves a live session's
      // recovery fenced).
      const client = requireClient();
      // Reaching the call counts as signalling even if it rejects below: the
      // signal may have reached the daemon despite failed exit confirmation.
      signalled = true;
      await client.forceStop(ref);
    } catch (error) {
      if (signalled) {
        // The signal may have succeeded despite failed exit confirmation.
        // The fence armed by beginStop is retained until a fresh snapshot
        // proves it can clear. Reconcile the hub's recovery requirement without
        // delaying this error.
        void threadsStore
          .getState()
          .refreshThread(ref)
          .catch(() => {});
      }
      throw error;
    } finally {
      endStop(ref, signalled);
    }
  },

  async shutdown(ref) {
    cancelPendingUserIntents(ref);
    await cancelUnattemptedMutations(ref);
    // Same mid-swap reach as forceStop's cancel above.
    await cancelResumeSwapTarget(ref);
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
      return await client.request("thread/fork", { ...opts, ref });
    } catch (err) {
      throw mapConflict(err);
    }
  },

  async listModels(refresh) {
    // Cache/inflight hits below need no wire call at all, so they must not
    // block on a reconnect that a warm cache makes irrelevant - check them
    // BEFORE waiting for a ready client, unlike the other read-only actions
    // here (which always need the wire, so the order doesn't matter).
    if (!refresh && modelsCache && !inflightModelsListIsRefresh) return modelsCache;
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
    const generation = ++modelsListGeneration;
    const request = (async () => {
      const client = await requireReadyClient();
      return client.request("model/list", {});
    })();
    // Supersede the shared in-flight slot with the newest request either way.
    // A refresh must not leave an older non-refresh request there: a
    // concurrent non-refresh caller would await that pre-refresh request and
    // receive the list the refresh was issued to replace. Every caller still
    // receives a response; de-dupe joins the newest one.
    inflightModelsList = request;
    inflightModelsListIsRefresh = refresh === true;
    try {
      const resp = await request;
      if (epoch === modelsEpoch && generation === modelsListGeneration) modelsCache = resp;
      return resp;
    } finally {
      if (inflightModelsList === request) {
        inflightModelsList = null;
        inflightModelsListIsRefresh = false;
      }
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

  async jobOutput(ref, jobId, beforeBytes, maxBytes, isCurrent) {
    // Read-only, so it waits out a reconnect (issue #195's RCA) instead of
    // failing with AppwireClient's synchronous "cannot call ... while
    // reconnecting" rejection - see requireReadyClient's own comment.
    const client = await requireReadyClient();
    if (isCurrent !== undefined && !isCurrent()) throw new Error("job read is no longer current");
    const resp = await client.request("evener/jobs/output", {
      ref,
      jobId,
      ...(beforeBytes !== undefined ? { beforeBytes } : {}),
      ...(maxBytes !== undefined && maxBytes > 0 ? { maxBytes } : {}),
    });
    return resp.data;
  },

  async jobGet(ref, jobId, isCurrent) {
    // Read-only, so it waits out a reconnect (issue #195's RCA) instead of
    // failing with AppwireClient's synchronous "cannot call ... while
    // reconnecting" rejection - see requireReadyClient's own comment.
    const client = await requireReadyClient();
    if (isCurrent !== undefined && !isCurrent()) throw new Error("job read is no longer current");
    const resp = await client.request("evener/jobs/get", { ref, jobId });
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

// The write seam (spec, "The write seam"): a subscription to the threads map,
// not a funnel. Publications flow through putThreadModel(s) and the
// notification handler's own setState; a subscription sees them all.
// The debounce and starvation bounds (spec, "The write seam"): a burst's
// trailing write fires 1 s after its last publication, and a streaming burst
// never starves past 5 s.
const DEFAULT_CACHE_WRITE_TIMERS = { debounceMs: 1_000, maxWaitMs: 5_000 };
let cacheWriteTimers = DEFAULT_CACHE_WRITE_TIMERS;

/** Tests inject cadence before arming writes; existing schedules keep their timers. */
export function setCacheWriteTimersForTests(timers: typeof DEFAULT_CACHE_WRITE_TIMERS): void {
  cacheWriteTimers = timers;
}

interface CacheWriteSchedule {
  trailing: ReturnType<typeof setTimeout>;
  maxWait: ReturnType<typeof setTimeout>;
}
const cacheWriteSchedules = new Map<string, CacheWriteSchedule>();
const oversizeMemo = new Set<string>();
const cacheHistoryLifetimes = new Map<string, symbol>();

function clearOversizeMemo(ref: string): void {
  oversizeMemo.delete(ref);
  cacheHistoryLifetimes.delete(ref);
}

// cancelCacheWrite ends the ref's schedule: both of the CURRENT entry's
// timers cleared and the entry removed, so no live timer for the ref ever
// survives outside the map and resetThreadsStoreForTests' walk always names
// every armed timer. The entry resolves AT CALL TIME, never through a
// closure's captured schedule object: the max-wait handle is inherited
// across reschedules, so a firing timer's closure can name an older object
// than the entry that now owns the handle — and the map's current entry is
// always the owner of every live timer for the ref, so ending it is the one
// correct fire action for either timer. (An identity-guarded retire was the
// round-1 shape: a stale-closure max-wait wrote at its boundary but left the
// newer entry holding its dead handle, inherited by every later reschedule —
// a stream of sub-trailing publications then never wrote again.)
function cancelCacheWrite(ref: string): void {
  const current = cacheWriteSchedules.get(ref);
  if (current === undefined) return;
  clearTimeout(current.trailing);
  clearTimeout(current.maxWait);
  cacheWriteSchedules.delete(ref);
}

function scheduleCacheWrite(ref: string): void {
  const existing = cacheWriteSchedules.get(ref);
  if (existing) clearTimeout(existing.trailing); // trailing debounce: reschedule
  // Whichever timer fires, it ends the burst by cancelling the schedule the
  // map holds at fire time and writing once. The next publication then arms a
  // FRESH max-wait, so a burst of sub-trailing publications keeps its
  // once-per-max-wait write even after a max-wait has fired once.
  const fire = () => {
    cancelCacheWrite(ref);
    const model = threadsStore.getState().threads.get(ref);
    if (model !== undefined) writeCacheRecord(ref, model);
  };
  const schedule: CacheWriteSchedule = {
    trailing: setTimeout(fire, cacheWriteTimers.debounceMs),
    maxWait: existing?.maxWait ?? setTimeout(fire, cacheWriteTimers.maxWaitMs), // max-wait: a streaming session never starves
  };
  cacheWriteSchedules.set(ref, schedule);
}

/** The flush: fires a pending write NOW, its gates evaluated on the current
 * (pre-removal) model. Ordered before the model leaves the map (spec, "The
 * flush is load-bearing"): a flush after removal would read an empty map and
 * drop the tail on every graceful close. */
function flushCacheWrite(ref: string): void {
  cancelCacheWrite(ref);
  const model = threadsStore.getState().threads.get(ref);
  if (model !== undefined) writeCacheRecord(ref, model);
}

function cacheWriteGatesPass(ref: string, model: ThreadModel): boolean {
  const state = threadsStore.getState();
  const lifetime = state.cacheLifetimes.get(ref);
  // The in-flight clear's arm (clearInFlight): every open lease captured
  // below the armed epoch predates the clear, so its writes wait for the
  // outcome — the same lifetimes the commit suppresses.
  const inFlight = state.clearInFlight;
  if (inFlight !== undefined) {
    const captured = lifetime?.leaseEpoch;
    if (captured !== undefined && captured < inFlight) return false;
  }
  return (
    model.history?.incarnation !== undefined && // a completed v6 content-bearing read
    !lifetime?.shell && // the shell skip: lineage state, not object identity
    !state.deletedRefs.has(ref) && // the deletion fence, re-checked at fire time
    !lifetime?.suppressed && // the clear suppression, re-checked at fire time
    model.history.invalidatedAtGeneration === undefined && // the one liveness marker
    model.history.failed === undefined &&
    currentSessionCache().isOpen() // a still-opening connection skips; the next publication retries
  );
}

function writeCacheRecord(ref: string, model: ThreadModel): void {
  if (!cacheWriteGatesPass(ref, model)) return;
  if (oversizeMemo.has(ref)) return; // memoized per ref, scoped to the model's lifetime
  const record = cachedSessionRecord(model, Date.now()); // the synchronous snapshot
  if (record === undefined) return;
  const lifetime = cacheHistoryLifetimes.get(ref) ?? Symbol();
  cacheHistoryLifetimes.set(ref, lifetime);
  const scheduledEpoch = tabCacheEpoch ?? 0; // the epoch this write was scheduled under
  void currentSessionCache()
    .put(record, scheduledEpoch, Date.now())
    .then((result) => {
      if (result.outcome === "oversize" && cacheHistoryLifetimes.get(ref) === lifetime) oversizeMemo.add(ref);
      if (result.outcome === "aborted") onCacheEpochObserved(result.observedEpoch);
    })
    .catch(() => {}); // every failure is a dropped write; the next debounced window retries
}

// The missed-message backstop (spec, "Eviction, cap, and cross-tab"): the
// aborted write is itself the tab's proof that a clear happened.
function onCacheEpochObserved(observed: number): void {
  if (tabCacheEpoch !== undefined && observed <= tabCacheEpoch) return;
  tabCacheEpoch = observed;
  threadsStore.setState((s) => ({ cacheLifetimes: suppressCacheLifetimesBefore(s.cacheLifetimes, observed) }));
}

function suppressCacheLifetimesBefore(
  lifetimes: Map<string, CacheLifetime>,
  epoch: number,
): Map<string, CacheLifetime> {
  const next = new Map(lifetimes);
  for (const [ref, lifetime] of lifetimes) {
    if (lifetime.leaseEpoch !== undefined && lifetime.leaseEpoch < epoch) {
      next.set(ref, { ...lifetime, suppressed: true });
    }
  }
  return next;
}

// Cross-tab propagation (spec, "The write seam" deletion bullet and the
// eviction section): one BroadcastChannel message per action, the same
// versioned-envelope discipline crossTabSync uses. A browser without
// BroadcastChannel degrades to single-tab: the durable epoch and the
// fire-time gates hold correctness without it.
const CACHE_CHANNEL_NAME = "evener.session-cache.v1";
const cacheSourceId = makeSourceId();
type CacheChannelMessage = VersionedChannelMessage &
  ({ kind: "deletion"; refs: string[] } | { kind: "clear"; epoch: number });

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
const cacheChannel = createVersionedChannel<CacheChannelMessage>({
  name: CACHE_CHANNEL_NAME,
  getSourceId: () => cacheSourceId,
  isMessage: isCacheChannelMessage,
  onMessage: onCacheChannelMessage,
  createChannel: (name) => createCacheChannel(name),
});
export function setCacheChannelFactoryForTests(factory: (name: string) => BroadcastChannel | null): void {
  cacheChannel.close();
  createCacheChannel = factory;
  // The installed factory's channel is attached at once: a peer's post must
  // reach this tab's handler even before this tab ever broadcasts, which is
  // the receiving half the channel exists for.
  cacheChannel.connect();
}

function onCacheChannelMessage(message: CacheChannelMessage): void {
  if (message.kind === "deletion") {
    // Two things, not one, in one step (spec): arm the suppression and heal
    // the storage. The heal cannot be lost: IndexedDB serializes this delete
    // transaction after any in-flight write's, so it always runs after the
    // record it must remove.
    threadsStore.setState((s) => {
      const cacheLifetimes = new Map(s.cacheLifetimes);
      for (const ref of message.refs) cacheLifetimes.set(ref, { ...cacheLifetimes.get(ref), suppressed: true });
      return { cacheLifetimes };
    });
    void currentSessionCache().deleteRecords(message.refs);
  } else {
    onCacheEpochObserved(message.epoch); // Task 10's clear arm: the same backstop the aborted write uses
  }
}

// The tab listens from the moment the store loads, not from its first send:
// a sibling's deletion must reach this tab even when this tab never deletes
// anything itself, which is the common tab. A browser without
// BroadcastChannel stays single-tab (the factory answers null); a failure to
// attach stays single-tab too.
cacheChannel.connect();

/** The deletion response's cache hook (spec, "The write seam"): keyed on the
 * response, not the caller — any deletion response that reports removed
 * thread ids reaches here through markDeletedSessionCaches, whichever
 * action produced it (session delete from the Rail or the chrome menu,
 * project delete). Joins deletedRefs (the immediate arm), cancels each
 * pending write, deletes each record, and propagates one message per action. */
export function markCacheSessionsDeleted(refs: string[]): void {
  if (refs.length === 0) return;
  threadsStore.setState((s) => {
    const deletedRefs = new Set(s.deletedRefs);
    for (const ref of refs) deletedRefs.add(ref);
    return { deletedRefs };
  });
  for (const ref of refs) cancelCacheWrite(ref);
  void currentSessionCache().deleteRecords(refs);
  cacheChannel.connect();
  cacheChannel.postMessage({ kind: "deletion", refs });
}

/** Clear cached session content (spec, "The clear-cached-sessions setting").
 * Synchronous in-memory step first — the only order that works, since
 * in-memory timer state cannot commit transactionally — then one
 * read-write transaction. The in-flight arm lives in clearInFlight, never
 * in the lifetimes: the write gate refuses open leases below the armed
 * epoch, the commit suppresses exactly those lifetimes, and the
 * abort drops the marker without touching their suppression at all — so an
 * abort is incapable of disarming another source's suppression (a sibling
 * deletion message, the aborted-write backstop) that arrived mid-flight.
 * The broadcast is commit-gated: a sibling never arms suppression for a
 * clear that did not happen. A clear that never reaches a definite commit
 * reverts its own in-memory effects — an earlier committed clear's
 * suppression stands — so open refs resume caching at their next
 * publication. */
export async function clearCachedSessions(): Promise<{ committed: boolean }> {
  const armed = Math.max(tabCacheEpoch ?? 0, threadsStore.getState().clearInFlight ?? 0) + 1;
  threadsStore.setState({ clearInFlight: armed });
  for (const ref of [...cacheWriteSchedules.keys()]) cancelCacheWrite(ref);
  const result = await currentSessionCache().clear();
  if (!result.committed) {
    // The abort drops only its own marker — lifetime suppression is never touched,
    // so another source's mid-flight arming stands. Durable observations
    // never need reverting: optimistic arming did not change their epoch.
    threadsStore.setState((s) => (s.clearInFlight === armed ? { clearInFlight: undefined } : s));
    return { committed: false };
  }
  onCacheEpochObserved(result.epoch);
  // Every open lease whose captured epoch predates the committed epoch: the
  // same set the marker refused during the flight, re-derived at commit so a
  // lease captured mid-flight is covered too.
  threadsStore.setState((s) => {
    const cacheLifetimes = suppressCacheLifetimesBefore(s.cacheLifetimes, result.epoch);
    return s.clearInFlight === armed ? { cacheLifetimes, clearInFlight: undefined } : { cacheLifetimes };
  });
  cacheChannel.connect();
  cacheChannel.postMessage({ kind: "clear", epoch: result.epoch });
  return { committed: true };
}

/** The settings row's reader (spec, "The clear-cached-sessions setting"):
 * the records store's row count through the same singleton seam the lookup,
 * the write and the clear ride. The adapter's failure discipline holds:
 * undefined means unavailable (a failed open or transaction), never a throw
 * and never a zero that would let the row claim a remedy ran. */
export async function countCachedSessions(): Promise<number | undefined> {
  return currentSessionCache().count();
}

/** Same-tab committed writes invalidate a mounted settings row immediately.
 * Sibling writes still use its existing per-render count. */
export function subscribeCacheWrites(listener: () => void): () => void {
  return currentSessionCache().subscribeWrites(listener);
}

// The live gap rule (spec, "The two serving paths, the live gap, and its
// rule"): a cached-shell reconciling read that carries no changes and whose
// fresh window starts above the shell's captured anchor replaces instead of
// merging. The anchor is captured at shell-build from pure record data, so
// a live fold cannot move it. A response carrying changes merges as usual;
// so does an empty record (the ordinary cold merge) and any disposition the
// identity rules already answer (replace/discard are theirs).
function applyCacheGapRule(ref: string, base: ThreadModel, response: ThreadReadResponse, now: number): ThreadModel {
  const ordinary = () => {
    if (base.history !== undefined && readDisposition(base.history, response) === "replace") clearOversizeMemo(ref);
    return applyReadResponse(base, response, now);
  };
  const state = threadsStore.getState();
  const lifetime = state.cacheLifetimes.get(ref);
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
  const current = state.threads.get(ref) ?? base;
  const newest = newestItemPosition(current.history?.turns ?? held.turns);
  const replaced = hydrateThread(response, ref, now);
  clearOversizeMemo(ref); // a replacement is the one mid-lifetime shrink
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

threadsStore.subscribe((state, previous) => {
  if (state.threads === previous.threads) return; // watched-only publications never touch this map
  for (const ref of state.threads.keys()) {
    if (state.threads.get(ref) === previous.threads.get(ref)) continue;
    if (state.threads.get(ref)?.history?.incarnation === undefined) continue; // a model with no recorded history can never produce a cache record, so the seam arms nothing for it — a pointless pending timer is the whole cost, but a test suite's fake clock pays it in shifted delivery; the publication that first gives the model a recorded history schedules it, and every write gate still applies at fire time
    scheduleCacheWrite(ref);
  }
});

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
  userIntentStopGenerations.clear();
  userIntentStopSequence = 0;
  activeStops.clear();
  inFlightResumes.clear();
  resumeSwapTargets.clear();
  resetHumanNoteDrafts();
  notesLatestIntentSequences.clear();
  resetActivityPanelStoreForTests();
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
  inflightDurableEnqueues.clear();
  undeliveredMutationIds.clear();
  mutationStateGenerations.clear();
  dispatchableMutationRefs.clear();
  dispatchReadyClient = null;
  dispatchReadyEpoch = -1;
  refCounts.clear();
  ensureGenerations.clear();
  olderPageGenerations.clear();
  goalUpdateGenerations.clear();
  notesUpdateGenerations.clear();
  inflightHydrates.clear();
  inflightHydrateClients.clear();
  inflightHydrateEpochs.clear();
  // The session cache's module state: a lookup still in flight must not
  // publish a shell into the fresh state (the map is dropped, not awaited —
  // its race's own guards recheck everything), the epoch view returns to
  // unobserved, and the singleton adapter drops its connection so the next
  // test opens afresh against a database its beforeEach just deleted. An
  // override a test installed goes with it; tests close their own adapters.
  inflightCacheLookups.clear();
  tabCacheEpoch = undefined;
  setSessionCacheAdapterForTests(undefined);
  sessionCacheAdapter.close();
  // The write seam's module state: a pending debounced write must not fire
  // into the fresh state, and the oversize memo dies with the models it
  // memoized.
  for (const schedule of cacheWriteSchedules.values()) {
    clearTimeout(schedule.trailing);
    clearTimeout(schedule.maxWait);
  }
  cacheWriteSchedules.clear();
  cacheWriteTimers = DEFAULT_CACHE_WRITE_TIMERS;
  oversizeMemo.clear();
  cacheHistoryLifetimes.clear();
  // The cache channel's module state: a test's installed factory and its
  // channel go with the reset, so the next test attaches a fresh channel
  // under the default factory (the seam's own close-and-reattach).
  setCacheChannelFactoryForTests(defaultCacheChannelFactory);
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
  releaseThreadSubscriptions();
  threadsIndex.clear();
  watchedThreadsIndex.clear();
  modelsCache = null;
  // A request already in flight when the store resets must not repopulate the
  // fresh cache: advancing the epoch and generation makes its late answer lose
  // both guards, exactly as an auth change or a newer request would.
  modelsEpoch += 1;
  modelsListGeneration += 1;
  inflightModelsList = null;
  inflightModelsListIsRefresh = false;
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
      cacheLifetimes: new Map(),
      clearInFlight: undefined,
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
