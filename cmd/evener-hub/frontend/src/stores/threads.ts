import {
  type ComposerMention,
  canWriteSharedNotes,
  isTurnError,
  retryEligibility,
  TURN_CONTINUATION_TEXT,
} from "@evener/appwire-client";
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
  CurrentTranscriptAdmission,
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
  canonicalSkillNames,
  collectAuthoritativeMutationIds,
  comparePositions,
  encodeTranscriptItem,
  errorText,
  hydrateThread,
  type InputAttachment,
  invalidateHistory,
  isStaleCursorError,
  issueLatestWindowRead,
  isTranscriptHistoryFailedError,
  isTranscriptIndexRebuildingError,
  mergeOlderItemPage,
  mergeTailTurns,
  mutationErrorData,
  notificationRoutingKey,
  pruneBackgroundHistoryBodies,
  readDisposition,
  readWindowBounds,
  resolvePendingEscalation,
  SHUT_DOWN_STATUSES,
  type ThreadSubscriptionLease,
  threadModelFromCache,
  WireError,
} from "@evener/appwire-client";
import {
  COMPOSER_METHODS,
  canEditQueuedMessage,
  canRemoveQueuedMessage,
  DIRECT_FALLBACK_METHODS,
  MutationDispatcher,
  notificationMutationIdentities,
  type PendingTurnEntry,
} from "@evener/appwire-client/state/mutation";
import { useEffect, useMemo, useSyncExternalStore } from "react";
import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import { releaseSubagentRows } from "../panes/session/transcript/tools/subagentModuleStore";
import { resetActivityPanelStoreForTests } from "./activityPanel";
import { backgroundTranscriptOptions } from "./backgroundTranscriptOptions";
import { connectedClientPort, connectionStore } from "./connection";
import { entryWriteAdmissionFor } from "./entryWriteAdmission";
import { acknowledgeHumanNote, resetHumanNoteDrafts } from "./humanNoteDrafts";
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
  type MutationRecoveryResendResult,
  type RetargetedMutations,
} from "./mutationOutboxIndexedDB";
import { trackProjectionWork } from "./projectionWork";
import { createReadyGenerationCallback } from "./readyGenerationCallback";
import { isRecoveryFenced, isResumeOnly, isSendResumes, type ResumeOnlySignals } from "./recoveryFence";
import { createSecureUUID } from "./secureUUID";
import {
  entryHistoryKey,
  SESSION_CACHE_DATABASE_NAME,
  SESSION_CACHE_LOOKUP_DEADLINE_MS,
  type TranscriptResolvedIdentity,
} from "./sessionCacheSchema";
import { SessionEntryCache } from "./sessionEntryCache";
import { resetTasksPanelStoreForTests } from "./tasksPanel";
import { appendFrameTime, isStorageFailure, isStorageTimeout, mapConflict, StopCanceledError } from "./threadsErrors";
import { encodedPayloadBytes } from "./transcriptPayloadBudget";
import {
  nextTranscriptReadIdentity,
  type TranscriptReader,
  TranscriptRepository,
  type TranscriptRetirementOutcome,
} from "./transcriptRepository";
import {
  createTranscriptResidency,
  type ReceivedTranscriptUpdate,
  type TranscriptResidency,
  type TranscriptResidencySnapshot,
} from "./transcriptResidency";
import { createVersionedChannel, makeSourceId, type VersionedChannelMessage } from "./versionedChannel";

export type { InputAttachment } from "@evener/appwire-client";

// InputAttachment is this store's real-attachment shape: base64 bytes, not a
// hosted URL. The wire's InputItem (appwire/types_turn.go) supports EITHER
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

// ForkFromTurnOptions mirrors ThreadForkParams verbatim (appwire/types_turn.go)
// minus ref (a separate positional argument, like every other
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
  // them. Recorded at the reconcile's rejection, or classified earlier by the
  // send fallback's own storage-timeout observation while the reconcile is
  // still pending. Cleared by a successful reconciliation; retried by every
  // discovery pass.
  mutationReconciliationStorageBlocked: ReadonlySet<string>;
  restartBlockingObligations: ReadonlyMap<string, symbol>;
  mutationAuthorityRefs: ReadonlySet<string>;
  // #4342: per session ref, the row the hub last refused in band as
  // transient backpressure (connectionBackpressure: notAccepted plus an
  // automatic retry disposition) and the consecutive refusal count pacing
  // its retry. Every pending-row surface reads it the same way: the row
  // whose id equals this entry's clientMutationId shows the hub has not
  // accepted it yet (Retrying / Waiting for the hub) instead of an ordinary
  // in-flight row. The entry leaves with settlement
  // (pruneSettledTransientRefusals) and a fresh refusal replaces it at the
  // next pace step; one bypassed by a direct row removal (no dispatcher
  // notification) lingers invisibly until the ref's next refusal replaces
  // it.
  transientRefusalPaces: ReadonlyMap<string, { clientMutationId: string; attempt: number }>;
  // Refs whose durable pending/outbox projection has published a read. The
  // pending-turns projection owns the answer; it is published here so a
  // consumer React-renders on the loaded/unloaded transition instead of
  // reading a plain Set it does not subscribe to. Retry fails closed for a ref
  // outside this set.
  pendingProjectionLoadedRefs: ReadonlySet<string>;
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
  // stopCanceled marks the failure a user's Stop caused (StopCanceledError).
  resumeFailures: ReadonlyMap<string, { seq: number; message: string; stopCanceled: boolean }>;
  clearResumedIdentity(ref: string): void;
  // Refs with a Force stop RPC this page started still in flight. The hub holds
  // Stopping > 0 for exactly that window and refuses even turn/start for the
  // drain (cmd/evener-hub's sessionActionRecoveryError), so a notLoaded snapshot
  // taken mid-drain must not be read as the merely-resumable shape
  // (isResumeOnly). Cleared when the RPC settles. A stop another client
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
  // Per-ref token of the in-flight entry-fixture local opening (the reader
  // restoring recorded bodies from disk). Present means "the pane is showing
  // the background-evicted first paint, whose rows react-virtual measures at
  // zero"; paging callers hold while it is present. The token makes a newer
  // opening supersede an older one, so an old opening settling cannot clear a
  // newer hold. Store state (not a module ref) so consumers re-render when it
  // settles.
  localOpenings: Map<string, number>;
  // Lean child subscriptions are independent from real pane ownership.
  watchedThreads: Map<string, ThreadModel>;
  // Refs whose hydration has been durably rejected by the hub's own
  // deletion fence (data.mutationOutcome === "targetDeleted" - see
  // readThreadHydration's catch below and cmd/evener-hub/app_sources.go's
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
  /** Refs whose latest latest-window read answered the hub's
   * transcriptIndexRebuilding discriminant (#4343): one owned repair runs
   * off the read path, the store's paced hydration retry keeps re-reading,
   * and the loading surface shows "Repairing session history" for these
   * refs. Set when the latest read failed with the code, cleared by the
   * next read that does not — success or any other failure, the
   * budget-exhausted history-failed answer included — and dropped with the
   * ref's model state on release. Unlike `deletedRefs` this is not terminal:
   * it follows the ref's reads. */
  repairingRefs: Set<string>;
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
  releaseThreadAndWait(ref: string): Promise<TranscriptRetirementOutcome | undefined>;
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
  // Re-issues the failed turn's continuation. `expectedTurnId` is the failure
  // the rendered action belonged to: activation rechecks the canonical latest
  // turn and the live outbox projection before submitting, so a callback
  // captured before the model advanced submits nothing.
  continueTurn(ref: string, expectedTurnId: string): Promise<void>;
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

export interface BackgroundThreadLease {
  readonly ready: Promise<void>;
  release(): Promise<undefined | TranscriptRetirementOutcome>;
}

// Module-private bookkeeping the locked interface doesn't expose: pane
// refcounts per ref, the hydrate promise currently in flight for a ref (so
// two panes racing to ensureThread() the same ref share one thread/read
// instead of sending two), and which client this store has already wired
// its notification/ready handlers onto (plus that wiring's own unsubscribe
// functions - see rewireClient below).
const refCounts = new Map<string, number>();
// Background claims keep canonical thread hydration/routing alive without
// borrowing a pane's cache or ensure-generation lifetime.
const backgroundRefCounts = new Map<string, number>();
const historyHoldCounts = new Map<string, number>();
const canonicalThreadOwnerGenerations = new Map<string, number>();
// The last background lease owns removal while its bounded final cache drain
// is in flight. Receipt/pin cleanup may still reconcile mutations, but cannot
// remove the model out from under that awaited drain.
const finalReleaseOwners = new Map<string, number>();
const backgroundLeaseSequences = new Map<string, number>();
// Per-ref "was a foreground/history/pin holder present" across owner changes,
// so a clear's suppression can tell a pre-clear holder's release from a fresh
// explicit foreground acquisition. Background membership is deliberately not a
// holder here: it never lifts suppression.
const cacheLifetimeHeld = new Map<string, boolean>();
// A generation changes whenever the last real pane releases and a new pane
// claims the ref. An ensure that fails after its pane lifecycle was retired
// must not roll back a replacement lifecycle's claim.
const ensureGenerations = new Map<string, number>();
// A per-ref counter of retirement events (an entry-route delete or same-tab
// Clear) that invalidate a hydration's claim to adopt newly observed durable
// cache authorities. `acceptEntryTranscriptHydration` awaits durable reads
// after its response published; a hydration that began before a retirement
// must not be relabelled fresh by authorities read after it, so it captures
// this counter at `beginThreadHydration` and refuses when it moved.
const entryRouteRetirements = new Map<string, number>();
function noteEntryRouteRetirement(ref: string): void {
  entryRouteRetirements.set(ref, (entryRouteRetirements.get(ref) ?? 0) + 1);
}
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

function canonicalThreadOwnerActive(ref: string): boolean {
  return (
    (refCounts.get(ref) ?? 0) > 0 ||
    (backgroundRefCounts.get(ref) ?? 0) > 0 ||
    (historyHoldCounts.get(ref) ?? 0) > 0 ||
    pinnedMutationRefs.has(ref)
  );
}

function beginCanonicalThreadClaim(ref: string, supersedePinnedOnly = false): void {
  const fixture = entryTranscriptRoute(ref);
  if (fixture?.ref === ref && fixture.retirement) {
    if (fixture.retirementEpoch === readyEpoch) {
      // Same connection, the wire membership and page authority never ended.
      fixture.repository?.cancelRetirement();
    } else {
      // A replacement client or reconnect cannot inherit the outgoing model
      // through the already-hydrated fast path.
      retireEntryTranscriptRoute(fixture);
      if (productionEntryRoutes.get(ref) === fixture) productionEntryRoutes.delete(ref);
      fixture.lifetime++;
      noteEntryRouteRetirement(ref);
      removeThreadModel(ref);
    }
    fixture.retirement = undefined;
    fixture.retirementEpoch = undefined;
    finalReleaseOwners.delete(ref);
  }
  const pinnedOnly =
    (refCounts.get(ref) ?? 0) === 0 && (backgroundRefCounts.get(ref) ?? 0) === 0 && pinnedMutationRefs.has(ref);
  if (canonicalThreadOwnerActive(ref) && !(supersedePinnedOnly && pinnedOnly)) return;
  canonicalThreadOwnerGenerations.set(ref, (canonicalThreadOwnerGenerations.get(ref) ?? 0) + 1);
}

function retireCanonicalThreadIfUnowned(ref: string): void {
  if (canonicalThreadOwnerActive(ref)) return;
  retireOwnedHydration("thread", ref);
  // The final cache drain retains the model, subscription and pending payload,
  // not the departed owner's repair verdict. Fence that verdict separately so
  // even a reacquisition cannot let this read publish a late repairing status.
  const pending = pendingThreadHydrations.get(ref);
  if (pending) pending.repairingRetired = true;
  clearThreadRepairingIfUnowned(ref);
}

function beginHistoryHold(ref: string): void {
  beginCanonicalThreadClaim(ref);
  historyHoldCounts.set(ref, (historyHoldCounts.get(ref) ?? 0) + 1);
  syncTranscriptResidencyOwners(ref);
  syncThreadSubscription(ref);
}

async function endHistoryHold(ref: string): Promise<void> {
  const remaining = (historyHoldCounts.get(ref) ?? 0) - 1;
  if (remaining > 0) {
    historyHoldCounts.set(ref, remaining);
    return;
  }
  historyHoldCounts.delete(ref);
  syncTranscriptResidencyOwners(ref);
  retireCanonicalThreadIfUnowned(ref);
  const retirement = retireEntryTranscriptIfUnowned(ref);
  if (retirement) {
    await retirement;
    return;
  }
  syncThreadSubscription(ref);
  if (!canonicalThreadOwnerActive(ref)) dropUnpinnedModel(ref);
}

function hydrationIntentFor(ref: string): HydrationIntent {
  return (refCounts.get(ref) ?? 0) > 0 || pinnedMutationRefs.has(ref) ? "foreground" : "background";
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
// A ref's in-flight hydration read with the client and ready epoch it was
// issued under, so a waiter can tell a failure on the current connection from
// one that outlived it. includeTurns is meaningful for watched reads only: a
// rich caller cannot share a lean read in flight, a lean caller may share a
// rich one.
class InflightHydrations {
  readonly #entries = new Map<
    string,
    { promise: Promise<ThreadModel | null>; client: AppwireClientLike; epoch: number; includeTurns: boolean }
  >();

  get(ref: string): Promise<ThreadModel | null> | undefined {
    return this.#entries.get(ref)?.promise;
  }
  has(ref: string): boolean {
    return this.#entries.has(ref);
  }
  client(ref: string): AppwireClientLike | undefined {
    return this.#entries.get(ref)?.client;
  }
  epoch(ref: string): number | undefined {
    return this.#entries.get(ref)?.epoch;
  }
  includeTurns(ref: string): boolean {
    return this.#entries.get(ref)?.includeTurns ?? false;
  }
  // The entry leaves when its own read settles, unless a newer read has
  // replaced it since.
  start(
    ref: string,
    promise: Promise<ThreadModel | null>,
    client: AppwireClientLike,
    epoch: number,
    includeTurns = false,
  ): void {
    this.#entries.set(ref, { promise, client, epoch, includeTurns });
    void promise
      .finally(() => {
        if (this.#entries.get(ref)?.promise === promise) this.#entries.delete(ref);
      })
      .catch(() => {});
  }
  delete(ref: string): void {
    this.#entries.delete(ref);
  }
  clear(): void {
    this.#entries.clear();
  }
}
const inflightHydrates = new InflightHydrations();

// The entry cache is the tab's only transcript cache (6d removed the legacy
// adapter). Lazy so the ordinary browser captures its actual storage
// environment on first use.
let productionEntryCache: SessionEntryCache | undefined;
function currentEntryCache(): SessionEntryCache {
  productionEntryCache ??= new SessionEntryCache({ databaseName: SESSION_CACHE_DATABASE_NAME });
  return productionEntryCache;
}

/** @internal Replace only the ordinary route's storage/clock boundary before acquisition. */
export function setProductionEntryCacheForTests(cache: SessionEntryCache): void {
  productionEntryCache = cache;
}

/** @internal Observe an existing ordinary route, without creating or selecting it. */
export function productionEntryTranscriptForTests(ref: string) {
  const route = productionEntryRoutes.get(ref);
  return {
    ref,
    historyKey: route?.historyKey,
    model: route?.repository?.getSnapshot(),
    baseline: route?.repository?.getBaselineIdentity(),
    olderPageGeneration: olderPageGenerations.get(ref) ?? 0,
    readerRetention: route?.reader?.getRetentionSnapshotForTesting(),
    repository: route?.repository,
    reader: route?.reader,
    lastLocalOpenStatus: route?.lastLocalOpenStatus,
    lastLocalOpenUsable: route?.lastLocalOpenUsable,
  };
}

// This process-local token scopes current checkpoints to this browser instance.
// It is deliberately regenerated on reload and never persisted as identity.
const currentAppliedSequences = new Map<string, number>();
const currentThreadIds = new Map<string, string>();
const currentRefsByThreadId = new Map<string, Set<string>>();
function setCurrentThreadId(ref: string, threadId: string): void {
  const previous = currentThreadIds.get(ref);
  if (previous === threadId) return;
  if (previous !== undefined) {
    const refs = currentRefsByThreadId.get(previous);
    refs?.delete(ref);
    if (refs?.size === 0) currentRefsByThreadId.delete(previous);
  }
  currentThreadIds.set(ref, threadId);
  let refs = currentRefsByThreadId.get(threadId);
  if (!refs) {
    refs = new Set();
    currentRefsByThreadId.set(threadId, refs);
  }
  refs.add(ref);
}
interface EntryTranscriptRoute {
  ref: string;
  cache: SessionEntryCache;
  clientId: string;
  hostId: string;
  repository?: TranscriptRepository;
  reader?: TranscriptReader;
  unsubscribeRepository?: () => void;
  retirement?: Promise<TranscriptRetirementOutcome>;
  retirementEpoch?: number;
  lifetime: number;
  historyKey?: string;
  // True while an in-flight same-tab Clear has suppressed this route's
  // publication and persistence claims. An aborted Clear clears it, re-enabling
  // the same held route without a release/reacquire.
  clearInert?: boolean;
  // One scalar per owned route, never a replay buffer of suppressed frames.
  clearRefreshGeneration?: number;
  // The durable authorities the installed repository captured. A later read
  // that observes a newer Clear epoch or deletion generation retires it, so a
  // fresh explicit foreground acquisition begins caching under the current
  // authorities instead of carrying stale ones.
  clearEpoch?: number;
  historyGeneration?: number;
  identity?: TranscriptResolvedIdentity;
  cachedUnverified?: boolean;
  cachedOpening?: Promise<void>;
  // Local reader continuation, separate from the repository's opaque server cursor.
  localBefore?: ThreadItemPosition;
  // The status the last local open (`openEntryTranscript`) reported for this
  // route. Read-only test evidence for why a pane open did or did not need a
  // source read; never consulted by product logic.
  lastLocalOpenStatus?: string;
  // True while the view the last accepted local open (`openEntryTranscript`)
  // established is still the one to serve: cleared when that open is torn
  // down. A remount of the same route may serve this view without racing a
  // disk recertification against the lookup deadline.
  lastLocalOpenUsable?: boolean;
  // True when the most recent open's bounded lookup expired before the local
  // read answered (openEntryTranscript's deadline branch): the recorded window
  // was never served nor refused, so a held pane's display can still be
  // body-suppressed with no demand cursor. Distinguishes that strand from the
  // fast recovery-needed answers a repository without a usable record returns.
  localOpenDeadlineMissed?: boolean;
}

const productionEntryRoutes = new Map<string, EntryTranscriptRoute>();

function entryTranscriptRoute(ref: string): EntryTranscriptRoute | undefined {
  let route = productionEntryRoutes.get(ref);
  if (!route) {
    route = {
      ref,
      cache: currentEntryCache(),
      clientId: location.origin,
      hostId: "local",
      lifetime: 1,
    };
    productionEntryRoutes.set(ref, route);
  }
  return route;
}

function entryTranscriptRoutes(): EntryTranscriptRoute[] {
  return [...productionEntryRoutes.values()];
}

function isEntryTranscriptBackgroundOnly(ref: string): boolean {
  return (backgroundRefCounts.get(ref) ?? 0) > 0 && !heldCountsAtStore(ref);
}

// Opening tokens, monotonic across the tab. The pane paints the
// background-evicted recorded snapshot before its reader restores bodies from
// disk, and react-virtual measures those empty rows (at zero) as it paints
// them, so geometry cannot tell that first paint from a settled transcript
// that legitimately holds an empty turn. The store owns that opening fact:
// callers hold paging while a ref's opening is pending, then let ordinary
// geometry decide. Tokens make a newer opening supersede an older one, so an
// old opening settling cannot clear a newer hold.
let entryTranscriptOpeningSequence = 0;

// The unraced restore of an established view is bounded only by a genuine
// storage stall, not by load: CI already showed load pushing the 250ms lookup
// deadline around, which is exactly the race this restore must not lose.
let sessionCacheStalledOpenMs = 5000;

/** @internal Lets tests shorten the stall bound without waiting real seconds. */
export function setSessionCacheStalledOpenForTests(ms: number): void {
  sessionCacheStalledOpenMs = ms;
}

/** True while the entry fixture's reader is still restoring `ref` from disk. */
export function isEntryTranscriptLocalOpening(ref: string): boolean {
  return threadsStore.getState().localOpenings.has(ref);
}

// The newest opening that ever began for a ref, kept after it settles: a
// late restore result must not act once any newer opening exists, even after
// that newer opening finished.
const entryTranscriptLatestOpening = new Map<string, number>();

// Mark the opening pending BEFORE its first (pruned) publication, and publish
// the change through the store so subscribed consumers re-render. Returns the
// token the matching settle must present.
function beginEntryTranscriptLocalOpening(ref: string): number {
  entryTranscriptOpeningSequence += 1;
  const token = entryTranscriptOpeningSequence;
  entryTranscriptLatestOpening.set(ref, token);
  threadsStore.setState((state) => {
    const localOpenings = new Map(state.localOpenings);
    localOpenings.set(ref, token);
    return { localOpenings };
  });
  return token;
}

// Settle only the opening this token owns: a stale opening (a newer one took
// the ref) is a no-op, so it can never clear the newer hold or, by
// publishing, strand a port. Called on success, failure and no-model alike.
function settleEntryTranscriptLocalOpening(ref: string, token: number): void {
  threadsStore.setState((state) => {
    if (state.localOpenings.get(ref) !== token) return {};
    const localOpenings = new Map(state.localOpenings);
    localOpenings.delete(ref);
    return { localOpenings };
  });
}

// Drop any hold for a ref whose fixture route is gone (disposal): publish the
// change so a consumer showing the port resumes ordinarily rather than waiting
// for a frame that will never arrive.
function clearEntryTranscriptLocalOpening(ref: string): void {
  threadsStore.setState((state) => {
    if (!state.localOpenings.has(ref)) return {};
    const localOpenings = new Map(state.localOpenings);
    localOpenings.delete(ref);
    return { localOpenings };
  });
}

/** Scheduling identity for older demand, not an opaque wire cursor. */
export function olderTranscriptPageKey(ref: string): string | null | undefined {
  const model = threadsStore.getState().threads.get(ref);
  if (!model) return undefined;
  const route = entryTranscriptRoute(ref);
  const local = route?.repository?.localOlderPosition();
  // This is a scheduling identity only. The wire path reads model.olderCursor,
  // never this key. Tag both kinds so an opaque server cursor cannot collide.
  if (local)
    return JSON.stringify(["local", route?.repository?.getBaselineIdentity(), local.entry, local.item, local.sub ?? 0]);
  return model.olderCursor ? JSON.stringify(["server", model.olderCursor]) : null;
}

// Publish the repository's own model into the real store. Called while the
// fixture's reader is attached: the repository is the route's single owner of
// recorded ingestion, so its accepted updates (not a parallel legacy reduce)
// are what the renderer follows. The store model stays pruned only while the
// ref is genuinely background-only; an attached reader means a real holder.
function publishEntryTranscriptModel(ref: string): void {
  const fixture = entryTranscriptRoute(ref);
  if (!fixture || fixture.ref !== ref || fixture.clearInert) return;
  const snapshot = fixture?.repository?.getSnapshot();
  if (!snapshot) return;
  const displayed = fixture.localBefore
    ? { ...snapshot, olderCursor: snapshot.olderCursor ?? `entry-local:${JSON.stringify(fixture.localBefore)}` }
    : snapshot;
  putThreadModel(ref, isEntryTranscriptBackgroundOnly(ref) ? pruneBackgroundHistoryBodies(displayed) : displayed);
}

// The complete-requested-window standard a local open tolerates without a
// cache certificate: every foreground item the model expects is present with
// at least its version and a real body.
function holdsCompleteForegroundWindow(model: ThreadModel | undefined): boolean {
  const displayed = new Map(
    model?.turns.flatMap((turn) => turn.items.map((item) => [item.transcriptKey ?? item.id, item] as const)),
  );
  // Legacy reads keep their recorded bodies in turns, without versioned
  // history. Do not mistake that nonempty accepted window for an empty one.
  const requested = (model?.history?.turns ?? model?.turns)?.flatMap((turn) => turn.items).slice(-200) ?? [];
  return (
    requested.length > 0 &&
    requested.every((item) => {
      const restored = displayed.get(item.transcriptKey ?? item.id);
      return (
        restored !== undefined &&
        (restored.version ?? 0) >= (item.version ?? 0) &&
        !encodeTranscriptItem(restored).identityOnly
      );
    })
  );
}

async function openEntryTranscript(ref: string, recovery = false): Promise<boolean> {
  const fixture = entryTranscriptRoute(ref);
  if (!fixture || fixture.ref !== ref || !fixture.repository) return true;
  const repository = fixture.repository;
  const openingToken = beginEntryTranscriptLocalOpening(ref);
  fixture.localOpenDeadlineMissed = false;
  let timer: ReturnType<typeof setTimeout> | undefined;
  try {
    fixture.reader ??= fixture.repository.attachReader();
    // Ongoing repository publication: every accepted recorded update republishes
    // the repository model, so continued UI changes are repository-owned rather
    // than supplied by the parallel legacy reducer. The explicit openLocal result
    // below is one such publication; this is the subscription that outlives it.
    fixture.unsubscribeRepository ??= fixture.repository.subscribe(() => publishEntryTranscriptModel(ref));
    publishEntryTranscriptModel(ref);
    const reader = fixture.reader;
    // A remount over a route whose last accepted open still holds an
    // un-invalidated view trusts the local restore instead of racing it
    // against the lookup deadline: the repository kept that view's identities
    // and the restore republishes its bodies from the durable rows. Under
    // load the race can only lose — the expiry disposes this reader, the
    // background reclaim then drops the resident bodies, and the source
    // recovery replaces the loaded range with a latest window (#4939). The
    // unraced restore keeps the ordinary opening hold and result handling,
    // and only a genuinely stalled storage read settles the opening early.
    // Only the opening that still owns the route may record its verdict: a
    // superseded one (its pane released, its repository replaced, or a newer
    // open already begun) must not overwrite what a newer open established.
    const openingOwned = () =>
      entryTranscriptRoute(ref) === fixture &&
      fixture.repository === repository &&
      threadsStore.getState().localOpenings.get(ref) === openingToken;
    const established = fixture.lastLocalOpenUsable && repository.holdsEstablishedView();
    let stalled = false;
    const restore = established ? reader.openLocal(200) : undefined;
    const result = established
      ? await Promise.race([
          restore,
          new Promise<undefined>((resolve) => {
            timer = setTimeout(() => {
              stalled = true;
              resolve(undefined);
            }, sessionCacheStalledOpenMs);
          }),
        ])
      : await Promise.race([
          reader.openLocal(200),
          new Promise<undefined>((resolve) => {
            timer = setTimeout(
              () => {
                // A held pane keeps a reader through the swap: the repository
                // reclaims the bodies behind a displayed window the moment it
                // has none, and the next publication would then replace that
                // display with the body-suppressed model.
                if (fixture.reader === reader) {
                  fixture.reader = heldCountsAtStore(ref) ? repository.attachReader() : undefined;
                }
                reader.dispose();
                resolve(undefined);
                // A recovery attempt outlasts the lookup deadline on purpose:
                // a read that answers slower than the deadline but successfully
                // must be allowed to finish, or a consistently slow cache
                // strands the pane behind a race it can only keep losing.
              },
              recovery ? sessionCacheStalledOpenMs : SESSION_CACHE_LOOKUP_DEADLINE_MS,
            );
          }),
        ]);
    if (!result) {
      // A stalled restore keeps the established view standing only while that
      // view still holds its required bodies: a foreground release already
      // disposed the reader and reclaimed them, so an empty display must not
      // wait on storage that may never answer. The repository's own snapshot
      // is the truth here, not the possibly-pruned display.
      if (stalled && restore && repository.holdsResidentForegroundRange()) {
        // A stalled restore leaves the established view standing: the opening
        // settles so paging and placement resume, and the still-running
        // restore publishes through the repository subscription whenever
        // storage answers. Its late result still records the truth, but only
        // while this opening remains its owner: the route's current reader,
        // the newest opening generation and a live foreground pane all fence
        // a result that outlived a newer open, a release or a replacement.
        const stillOwnsLateResult = () =>
          entryTranscriptRoute(ref) === fixture &&
          fixture.repository === repository &&
          fixture.reader === reader &&
          entryTranscriptLatestOpening.get(ref) === openingToken &&
          (refCounts.get(ref) ?? 0) > 0 &&
          !threadsStore.getState().localOpenings.has(ref);
        void restore
          .then(async (settled) => {
            if (!stillOwnsLateResult()) return;
            fixture.lastLocalOpenStatus = settled.status;
            const usable =
              settled.retainedRangeComplete !== false &&
              (settled.status !== "recovery-needed" || holdsCompleteForegroundWindow(settled.model));
            if (usable) {
              fixture.lastLocalOpenUsable = true;
              return;
            }
            fixture.lastLocalOpenUsable = false;
            repository.beginFreshBaseline();
            publishEntryTranscriptModel(ref);
            const client = await requireReadyClient();
            const epoch = readyEpoch;
            if (!stillOwnsLateResult()) return;
            await refreshTrackedThread(client, epoch, ref, false, false, undefined, undefined, "foreground");
            // Mirror the raced recovery's full sequence: the read that
            // replaced the view is followed by the local re-open that
            // promotes its restored bodies into the display.
            if (!stillOwnsLateResult()) return;
            await openEntryTranscript(ref);
          })
          .catch(() => {
            // A failed recovery read keeps the ordinary owner-scoped retry.
          });
        return true;
      }
      fixture.lastLocalOpenStatus = "recovery-needed";
      // Only the bounded-lookup deadline miss is the stranding strand a
      // tracked re-open can heal; an established restore that stalled past
      // its own settled-opening window hands to source recovery instead
      // (the #4954 stalled-open contract), and must not arm this chain.
      if (!stalled) fixture.localOpenDeadlineMissed = true;
      if (openingOwned()) fixture.lastLocalOpenUsable = false;
      return false;
    }
    fixture.lastLocalOpenStatus = result.status;
    if (
      result.model &&
      entryTranscriptRoute(ref) === fixture &&
      fixture.repository === repository &&
      !fixture.clearInert &&
      (refCounts.get(ref) ?? 0) > 0 &&
      !threadsStore.getState().deletedRefs.has(ref)
    ) {
      fixture.localBefore = result.before;
      publishEntryTranscriptModel(ref);
    }
    // A previously loaded range can exceed the initial 200-body promotion: keep
    // its usable partial display, but do not certify it via a smaller tail.
    // Cache proof can also be unavailable while the live model already carries
    // the complete requested window. Recover missing bodies, not merely a
    // missing certificate, which would replace usable paged history with a
    // latest read.
    const usable =
      result.retainedRangeComplete !== false &&
      (result.status !== "recovery-needed" || holdsCompleteForegroundWindow(result.model));
    if (openingOwned()) fixture.lastLocalOpenUsable = usable;
    return usable;
  } finally {
    if (timer !== undefined) clearTimeout(timer);
    settleEntryTranscriptLocalOpening(ref, openingToken);
  }
}

async function openCachedEntryTranscript(ref: string, generation: number): Promise<void> {
  const route = entryTranscriptRoute(ref);
  if (!route || productionEntryRoutes.get(ref) !== route || route.repository) return;
  if (route.cachedOpening) return route.cachedOpening;
  const retirement = entryRouteRetirements.get(ref) ?? 0;
  let active = true;
  let installed: TranscriptRepository | undefined;
  let timer: ReturnType<typeof setTimeout> | undefined;
  const current = () => {
    const state = threadsStore.getState();
    return (
      active &&
      productionEntryRoutes.get(ref) === route &&
      (refCounts.get(ref) ?? 0) > 0 &&
      ensureGenerations.get(ref) === generation &&
      (entryRouteRetirements.get(ref) ?? 0) === retirement &&
      !state.deletedRefs.has(ref) &&
      !route.clearInert &&
      state.clearInFlight === undefined &&
      !state.cacheLifetimes.get(ref)?.suppressed
    );
  };
  const opening = Promise.race([
    (async () => {
      const metadata = await route.cache.getSessionForRequestedRef(route.clientId, ref);
      if (!current() || !metadata?.resolvedIdentity || route.repository || threadsStore.getState().threads.has(ref))
        return;
      const authorities = await readEntryRouteAuthorities(route, metadata.historyKey);
      if (
        !current() ||
        !authorities ||
        authorities.historyGeneration !== (metadata.generation ?? 0) ||
        route.repository ||
        threadsStore.getState().threads.has(ref)
      )
        return;
      const identity = metadata.resolvedIdentity;
      // Earlier v3 entries have identity but no display header. Their missing
      // wire instance stays explicitly unknown, not guessed from an opaque ref.
      // The first source success replaces this untrusted binding in full.
      const header = metadata.cachedHeader ?? {
        ref,
        threadId: "",
        name: "",
        modelProvider: "",
        model: "",
        imageSessionId: identity.sessionId,
        savedAt: metadata.savedAt ?? 0,
        history: {
          bootGeneration: "",
          epoch: 0,
          incarnation: identity.incarnation,
          length: 0,
          appliedGeneration: 0,
          issuedGeneration: 0,
        },
      };
      if (entryHistoryKey(identity) !== metadata.historyKey || header.history.incarnation !== identity.incarnation)
        return;
      // A first durable observation belongs to this unpublished acquisition.
      if (tabCacheEpoch === undefined) {
        threadsStore.setState((state) => ({
          cacheLifetimes: new Map(state.cacheLifetimes).set(ref, {
            ...state.cacheLifetimes.get(ref),
            leaseEpoch: authorities.clearEpoch,
          }),
        }));
      }
      onCacheEpochObserved(authorities.clearEpoch);
      if (!current()) return;
      route.identity = identity;
      route.hostId = identity.hostId;
      route.historyKey = metadata.historyKey;
      route.clearEpoch = authorities.clearEpoch;
      route.historyGeneration = authorities.historyGeneration;
      route.cachedUnverified = true;
      route.lifetime += 1;
      installed = createEntryTranscriptRepository(route, {
        threadId: header.threadId,
        historyKey: metadata.historyKey,
        identity,
        ...authorities,
        seedModel: threadModelFromCache(
          { ...header, ref, history: { ...header.history, turns: [] } },
          route.cache.now(),
        ),
      });
      route.repository = installed;
      route.reader = installed.attachReader();
      route.unsubscribeRepository = installed.subscribe(() => {
        if (current() && route.repository === installed) publishEntryTranscriptModel(ref);
      });
      const token = beginEntryTranscriptLocalOpening(ref);
      try {
        const cachedResult = await route.reader.openLocal(200);
        route.lastLocalOpenStatus = cachedResult.status;
      } finally {
        settleEntryTranscriptLocalOpening(ref, token);
      }
    })().catch(() => {
      // A storage failure is a miss. The ordinary source opening still runs.
    }),
    new Promise<void>((resolve) => {
      timer = setTimeout(() => {
        active = false;
        if (installed && route.repository === installed && route.cachedUnverified) {
          retireEntryTranscriptRoute(route);
          clearEntryTranscriptLocalOpening(ref);
        }
        resolve();
      }, SESSION_CACHE_LOOKUP_DEADLINE_MS);
    }),
  ]).finally(() => {
    if (timer !== undefined) clearTimeout(timer);
    if (route.cachedOpening === opening) route.cachedOpening = undefined;
  });
  route.cachedOpening = opening;
  await opening;
}

// A foreground acquisition whose bounded local opening missed its lookup
// deadline returns with the repository's recorded display standing in for the
// pane: body-suppressed and without a local demand cursor, because only a
// successful open promotes recorded bodies into the display — a fresh server
// read republishes the recorded projection and cannot restore them. One miss
// self-heals inside ensureThread (the authoritative read publishes, and the
// success arm's second open promotes the recorded window). Two misses in a
// row — the sustained-slow-cache case the 250ms lookup deadline makes
// possible — used to leave the pane that way permanently: nothing re-opened
// the record, so the thread store's documented bounded source recovery (S02:
// "the accepted baseline restores display, older paging and live updates")
// never ran. A tracked re-open closes the gap, and it must RE-ARM: its own
// open reads the same slow cache and can miss the same deadline again, so a
// fixed number of attempts just strands the pane one attempt later (the
// whole-suite third-miss failure this re-arm closes). Each attempt is bounded
// by the stalled-open window rather than the lookup deadline (a read that
// answers slowly but successfully must still finish), and only an attempt's
// completion decides the next one — an event-completion chain, not a timer
// loop — so it stops at the
// first successful open, when the foreground holder releases, or when the
// route retires, or once the storage chain has had its paced retry: a fast
// recovery-needed refusal, or a second missed stalled window, hands to
// source recovery once instead of re-arming against storage that may never
// answer. One tracked chain covers every attempt, deduplicated per live
// route, and is registered in trackedHydrationCompletions so a caller
// awaiting the acquisition's completion (including
// settleEntryTranscriptForTests) also awaits the display's convergence.
// The route a pending recovery belongs to, keyed by ref: a recovery opened for
// a retired route must never suppress a fresh acquisition's recovery, and a
// released-then-reacquired pane (same ref, new route) still gets its own.
const pendingOpeningRecoveries = new Map<string, EntryTranscriptRoute>();

// The recovery chain's whole-span registration per ref, so a concurrent
// hydration can compose with it instead of displacing it. Tracked separately
// from trackedHydrationCompletions because a plain hydration's registration
// may be superseded by a newer attempt, while the chain's span may not.
const openingRecoveryCompletions = new Map<string, Promise<void>>();

function scheduleEntryOpeningRecovery(ref: string): void {
  const route = entryTranscriptRoute(ref);
  if (!route || route.ref !== ref) return;
  if (pendingOpeningRecoveries.get(ref) === route) return;
  if ((refCounts.get(ref) ?? 0) <= 0) return; // no live foreground holder to recover for
  if (route.localOpenDeadlineMissed !== true) return; // the open refused fast or handed to another path: not this chain's strand
  // The chain exists to promote recorded bodies into a display that is still
  // body-suppressed; a display another path (main's source recovery, a newer
  // open) already healed needs no re-open churn against storage that may
  // never answer.
  if (holdsCompleteForegroundWindow(threadsStore.getState().threads.get(ref))) return;
  pendingOpeningRecoveries.set(ref, route);
  const recovery = (async () => {
    let stalledAttempts = 0;
    for (;;) {
      const opened = await openEntryTranscript(ref, true);
      if (opened) return;
      if (entryTranscriptRoute(ref) !== route) return; // a retired route's recovery must not outlive it
      if ((refCounts.get(ref) ?? 0) <= 0) return; // holder released: nothing left to recover for
      // Another path healed the display mid-chain only while the repository
      // still backs that display: a pending attempt whose timeout disposed the
      // shared reader lets the repository reclaim its bodies without
      // publishing, so the store's snapshot alone can be a stale complete view
      // the next publication would prune. Require the resident range too, and
      // a reclaimed repository re-opens instead of ending recovery on it.
      if (
        holdsCompleteForegroundWindow(threadsStore.getState().threads.get(ref)) &&
        route.repository?.holdsResidentForegroundRange()
      )
        return;
      if (route.localOpenDeadlineMissed === true && stalledAttempts < 1) {
        // The attempt missed the deadline again under sustained slowness; the
        // pane is still stranded and held, so give the storage chain one
        // more paced attempt before turning to the source.
        stalledAttempts += 1;
        continue;
      }
      // The attempt refused fast instead of missing the deadline — storage
      // answered a recovery-needed outright, and a transient repository error
      // takes openLocal's catch, which resolves without arming another
      // attempt — or it missed its second stalled window and the cache is not
      // answering at all. A pane that is still suppressed must not strand
      // behind a marker no later open will set, or behind storage that may
      // never answer: hand to source recovery once, the same sequence a
      // stalled restore hands to (#4954), and let the ordinary owner-scoped
      // paths own the rest.
      route.repository?.beginFreshBaseline();
      publishEntryTranscriptModel(ref);
      let sourceReadFailed = false;
      try {
        const client = await requireReadyClient();
        const epoch = readyEpoch;
        if (entryTranscriptRoute(ref) !== route) return;
        if ((refCounts.get(ref) ?? 0) <= 0) return;
        // The chain's own registration spans this read and the re-open after
        // it; the tracked read joins under it rather than displacing it, so
        // a consumer that starts waiting mid-read still captures a promise
        // covering the entire recovery.
        await refreshTrackedThread(client, epoch, ref, false, true, undefined, undefined, "foreground", false);
      } catch {
        // A failed source read keeps the ordinary owner-scoped retry — the
        // owned hydration lifecycle schedules it — and the re-open below
        // promotes the view a successful read restored, so a failed one has
        // nothing to promote.
        sourceReadFailed = true;
      }
      if (sourceReadFailed) {
        return;
      }
      if (entryTranscriptRoute(ref) !== route) return;
      if ((refCounts.get(ref) ?? 0) <= 0) return;
      // Mirror the raced recovery's full sequence: the read that replaced
      // the view is followed by the local re-open that promotes its
      // restored bodies into the display. A source response that already
      // healed the display needs no further cache read to converge.
      if (!holdsCompleteForegroundWindow(threadsStore.getState().threads.get(ref))) {
        await openEntryTranscript(ref, true);
      }
      return;
    }
  })().then(
    () => undefined,
    () => undefined,
  );
  // The chain's registration is the recovery's whole span; it is also
  // recorded in openingRecoveryCompletions so a concurrent refresh can compose
  // with it instead of displacing it. A superseded hydration's registration
  // may still be displaced by a newer hydration — the newer attempt owns the
  // ref's convergence — but the recovery chain's span is not a hydration and
  // must survive every one.
  openingRecoveryCompletions.set(ref, recovery);
  trackedHydrationCompletions.set(ref, recovery);
  void recovery.then(() => {
    if (pendingOpeningRecoveries.get(ref) === route) pendingOpeningRecoveries.delete(ref);
    if (openingRecoveryCompletions.get(ref) === recovery) openingRecoveryCompletions.delete(ref);
    if (trackedHydrationCompletions.get(ref) === recovery) trackedHydrationCompletions.delete(ref);
  });
}

function releaseEntryTranscriptReader(ref: string, releaseDisplay = false): void {
  if (heldCountsAtStore(ref)) return;
  const fixture = entryTranscriptRoute(ref);
  if (!fixture || fixture.ref !== ref) return;
  clearEntryTranscriptLocalOpening(ref);
  fixture.reader?.dispose();
  fixture.reader = undefined;
  const model = threadsStore.getState().threads.get(ref);
  if (releaseDisplay && model && isEntryTranscriptBackgroundOnly(ref)) {
    putThreadModel(ref, pruneBackgroundHistoryBodies(model));
  }
}

function retireEntryTranscriptIfUnowned(ref: string): Promise<TranscriptRetirementOutcome> | undefined {
  const fixture = entryTranscriptRoute(ref);
  const repository = fixture?.ref === ref ? fixture.repository : undefined;
  if (!fixture || !repository || canonicalThreadOwnerActive(ref)) return undefined;
  if (fixture.retirement) return fixture.retirement;
  const generation = (backgroundLeaseSequences.get(ref) ?? 0) + 1;
  backgroundLeaseSequences.set(ref, generation);
  finalReleaseOwners.set(ref, generation);
  syncThreadSubscription(ref);
  fixture.retirementEpoch = readyEpoch;
  const retirement: Promise<TranscriptRetirementOutcome> = repository.retire().then((outcome) => {
    if (fixture.retirement !== retirement || fixture.repository !== repository || canonicalThreadOwnerActive(ref))
      return { kind: "cancelled" };
    fixture.unsubscribeRepository?.();
    fixture.unsubscribeRepository = undefined;
    fixture.reader?.dispose();
    fixture.reader = undefined;
    repository.dispose();
    fixture.repository = undefined;
    fixture.retirement = undefined;
    fixture.retirementEpoch = undefined;
    if (finalReleaseOwners.get(ref) === generation) finalReleaseOwners.delete(ref);
    dropUnpinnedModel(ref, false);
    syncThreadSubscription(ref);
    return outcome;
  });
  fixture.retirement = retirement;
  return retirement;
}

async function runEntryHistoryRequest<T>(
  fixture: EntryTranscriptRoute,
  signal: AbortSignal | undefined,
  run: () => Promise<T>,
): Promise<T> {
  const repository = fixture.repository;
  const epoch = readyEpoch;
  const client = wiredClient;
  const current = () =>
    !signal?.aborted &&
    fixture.repository === repository &&
    !fixture.clearInert &&
    client === wiredClient &&
    client?.state === "ready" &&
    epoch === readyEpoch;
  const residency = transcriptResidency();
  residency.connection(epoch, client?.state === "ready");
  let ran = false;
  let result!: T;
  while (!ran) {
    if (!current()) throw new Error("entry history request was retired");
    let activated = false;
    await residency
      .scheduleHydration(
        fixture.ref,
        heldCountsAtStore(fixture.ref) ? "foreground" : "background",
        async (scheduledSignal) => {
          activated = true;
          if (!current() || scheduledSignal.aborted) throw new Error("entry history request was retired");
          ran = true;
          result = await run();
          if (!current() || scheduledSignal.aborted) throw new Error("entry history request was retired");
        },
      )
      .catch((error) => {
        // A cancelled predecessor can reject the shared slot before this
        // callback ran. Only its settlement was joined, so try our own request.
        if (activated || !current()) throw error;
      });
    // A previous same-ref read occupies this slot. Join its settlement, never
    // its response, then acquire a distinct request for the fresh authority.
  }
  return result;
}

function createEntryTranscriptRepository(
  fixture: EntryTranscriptRoute,
  binding: {
    threadId: string;
    historyKey: string;
    identity: TranscriptResolvedIdentity;
    clearEpoch: number;
    historyGeneration: number;
    seedModel?: ThreadModel;
  },
): TranscriptRepository {
  const { threadId, historyKey, identity, clearEpoch, historyGeneration, seedModel } = binding;
  const ref = fixture.ref;
  const productionRoute = productionEntryRoutes.get(ref) === fixture;
  return new TranscriptRepository(fixture.cache, {
    ref,
    threadId,
    historyKey,
    identity,
    clearEpoch,
    historyGeneration,
    resolvedEscalations: resolvedEscalationsFor(ref),
    lifetime: fixture.lifetime,
    seedModel,
    now: () => fixture.cache.now(),
    foldNotification: (model, notification, now) => foldCanonicalNotification(model, notification, now).model,
    admission: entryWriteAdmissionFor(fixture.cache),
    reconcile: () => {
      const client = wiredClient;
      if (client?.state !== "ready" || fixture.clearInert || !canonicalThreadOwnerActive(ref)) return;
      fixture.repository?.beginFreshBaseline();
      void refreshTrackedThread(
        client,
        readyEpoch,
        ref,
        true,
        false,
        undefined,
        undefined,
        hydrationIntentFor(ref),
      ).catch(() => {
        // The owned hydration lifecycle schedules transport recovery.
      });
    },
    isPreferred: () => heldCountsAtStore(ref) || (backgroundRefCounts.get(ref) ?? 0) > 0,
    suppressed: () => fixture.clearInert === true,
    mayPersist: () =>
      !fixture.cachedUnverified &&
      (!productionRoute || threadsStore.getState().cacheLifetimes.get(ref)?.suppressed !== true),
    readHistory: (held, signal) =>
      runEntryHistoryRequest(fixture, signal, () => {
        const client = wiredClient;
        const epoch = readyEpoch;
        if (client?.state !== "ready") return Promise.reject(new Error("entry history source is unavailable"));
        const lease = syncThreadSubscription(ref, client);
        if (!lease) return Promise.reject(new Error("entry history has no subscription owner"));
        const generation = (fixture.repository?.getSnapshot()?.history?.issuedGeneration ?? 0) + 1;
        return readThreadForgettingConfirmed(ref, lease, threadReadParams(true, generation, held)).then((result) => {
          if (client !== wiredClient || epoch !== readyEpoch) throw new Error("entry history source was retired");
          return result;
        });
      }),
    readHistoryPage: ({ cursor, before, signal }) =>
      runEntryHistoryRequest(fixture, signal, () => {
        const client = wiredClient;
        const epoch = readyEpoch;
        if (client?.state !== "ready") return Promise.reject(new Error("entry history source is unavailable"));
        return client.request("thread/turns/list", { ...olderItemsParams(ref, cursor), before }).then((result) => {
          if (client !== wiredClient || epoch !== readyEpoch) throw new Error("entry history source was retired");
          return result;
        });
      }),
  });
}

async function acceptEntryTranscriptHydration(
  ref: string,
  pending: PendingThreadHydration,
  hydration: ThreadHydration,
): Promise<boolean | undefined> {
  const fixture = entryTranscriptRoute(ref);
  if (!fixture || fixture.ref !== ref || pending.client !== wiredClient || pending.epoch !== readyEpoch) return;
  const response = hydration.response;
  const identity: TranscriptResolvedIdentity = {
    clientId: fixture.clientId,
    hostId: fixture.hostId,
    sessionId: response.thread.sessionId,
    incarnation: response.snapshot?.incarnation ?? "",
  };
  // Host qualification comes from the resolved response, never the opaque
  // requested ref. The same-origin hub is the durable client namespace.
  if (productionEntryRoutes.get(ref) === fixture) {
    const resolvedRef = response.thread.evener.ref;
    const separator = resolvedRef.indexOf(":");
    identity.hostId = separator < 0 ? "local" : resolvedRef.slice(0, separator);
    fixture.hostId = identity.hostId;
  }
  const historyKey = entryHistoryKey(identity);
  fixture.identity = identity;
  const historyChanged = fixture.historyKey !== undefined && fixture.historyKey !== historyKey;
  fixture.historyKey = historyKey;
  // The source response was issued before this point. Capture the acquisition's
  // retirement claim AND the route it currently owns, then read the durable
  // authorities with the lookup deadline: a delete or same-tab Clear that
  // retires this route while the read is in flight must not let the old
  // response adopt the new authorities, and must not dispossess a replacement
  // owner that installed itself meanwhile.
  const claimedRetirement = pending.entryRetirement ?? entryRouteRetirements.get(ref) ?? 0;
  const claimedRepository = fixture.repository;
  const authorities = await readEntryRouteAuthorities(fixture, historyKey);
  if (entryTranscriptRoute(ref) !== fixture || pending.client !== wiredClient || pending.epoch !== readyEpoch) {
    return;
  }
  if (
    productionEntryRoutes.get(ref) === fixture &&
    (pendingThreadHydrations.get(ref) !== pending || !canonicalThreadOwnerActive(ref))
  )
    return;
  const productionRoute = productionEntryRoutes.get(ref) === fixture;
  if (productionRoute && authorities) {
    // A first unpublished claim may capture the observed epoch. Existing
    // holders instead pass through the same suppression as a Clear broadcast.
    if (
      !claimedRepository &&
      !threadsStore.getState().threads.has(ref) &&
      !threadsStore.getState().cacheLifetimes.get(ref)?.suppressed
    ) {
      threadsStore.setState((state) => ({
        cacheLifetimes: new Map(state.cacheLifetimes).set(ref, {
          ...state.cacheLifetimes.get(ref),
          leaseEpoch: authorities.clearEpoch,
        }),
      }));
    }
    onCacheEpochObserved(authorities.clearEpoch);
  }
  const state = threadsStore.getState();
  const lifetime = state.cacheLifetimes.get(ref);
  const lifetimeSuppressed = lifetime?.suppressed === true;
  const retired =
    state.deletedRefs.has(ref) ||
    (entryRouteRetirements.get(ref) ?? 0) !== claimedRetirement ||
    (lifetimeSuppressed && !productionRoute);
  if (retired) {
    // The acquisition is no longer eligible. Only the route this acquisition
    // actually owns is retired: if a replacement has since installed its own
    // repository, that owner's reader, subscription and repository must not be
    // touched.
    if (fixture.repository === claimedRepository) retireEntryTranscriptRoute(fixture);
    return;
  }
  if (
    pending.entryHistoryKey === historyKey &&
    authorities &&
    pending.entryHistoryGeneration !== authorities.historyGeneration
  ) {
    // This response predates a durable deletion, even if its broadcast never
    // arrived. Retire only its binding, never grant this read the new fence.
    // A subsequent source read can establish a new repository and authority.
    if (fixture.repository === claimedRepository) retireEntryTranscriptRoute(fixture);
    return;
  }
  // A failed or held-out authority read keeps the installed repository rather
  // than adopting unread authorities; a brand-new route falls back to the
  // epoch-0/generation-0 baseline and lets the durable fence decide.
  const clearEpoch =
    productionRoute && lifetimeSuppressed && !lifetime?.freshForegroundLifetime
      ? (fixture.clearEpoch ?? 0)
      : (authorities?.clearEpoch ?? fixture.clearEpoch ?? 0);
  const historyGeneration = authorities?.historyGeneration ?? fixture.historyGeneration ?? 0;
  const threadChanged = fixture.repository?.getSnapshot()?.threadId !== response.thread.id;
  if (
    !fixture.repository ||
    hydration.discardHeldHistory ||
    threadChanged ||
    historyChanged ||
    fixture.clearEpoch !== clearEpoch ||
    fixture.historyGeneration !== historyGeneration
  ) {
    // Only rebuild the route this acquisition owns; a concurrent replacement is
    // not this response's to replace.
    if (fixture.repository !== claimedRepository) return;
    retireEntryTranscriptRoute(fixture);
    fixture.lifetime += 1;
    fixture.clearEpoch = clearEpoch;
    fixture.historyGeneration = historyGeneration;
    fixture.repository = createEntryTranscriptRepository(fixture, {
      threadId: response.thread.id,
      historyKey,
      identity,
      clearEpoch,
      historyGeneration,
    });
  }
  const repository = fixture.repository;
  fixture.cachedUnverified = false;
  fixture.unsubscribeRepository ??= repository.subscribe(() => publishEntryTranscriptModel(ref));
  if (productionRoute && heldCountsAtStore(ref) && !fixture.reader) fixture.reader = repository.attachReader();
  if (productionRoute && authorities && lifetime?.freshForegroundLifetime) settleCacheLifetimeAfterHydration(ref);
  const adoptedRead = repository.acceptRead(response, {
    lifetime: fixture.lifetime,
    ...nextTranscriptReadIdentity(),
  });
  repository.acceptBeforeReadCut(
    hydration.fixtureNotifications.map((update) => update.notification),
    { lifetime: fixture.lifetime },
  );
  return adoptedRead;
}

// Dispose the entry route's reader, publication subscription and repository
// together. `openEntryTranscript` uses `??=`, so leaving a disposed
// reader or a subscription bound to the old repository installed would let a
// later open reuse them.
function retireEntryTranscriptRoute(fixture: EntryTranscriptRoute): void {
  fixture.reader?.dispose();
  fixture.reader = undefined;
  fixture.localBefore = undefined;
  fixture.lastLocalOpenUsable = undefined;
  fixture.unsubscribeRepository?.();
  fixture.unsubscribeRepository = undefined;
  fixture.repository?.dispose();
  fixture.repository = undefined;
}

// Read the durable Clear epoch and deletion generation under the existing
// lookup deadline, with a storage-failure fallback: a held or failed durable
// read must not delay or reject an otherwise successful source hydration.
async function readEntryRouteAuthorities(
  fixture: EntryTranscriptRoute,
  historyKey: string,
): Promise<{ clearEpoch: number; historyGeneration: number } | undefined> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  try {
    return await Promise.race([
      Promise.all([fixture.cache.getClearEpoch(), fixture.cache.getHistoryGeneration(historyKey)]).then(
        ([clearEpoch, historyGeneration]) => ({ clearEpoch, historyGeneration }),
      ),
      new Promise<undefined>((resolve) => {
        timer = setTimeout(() => resolve(undefined), SESSION_CACHE_LOOKUP_DEADLINE_MS);
      }),
    ]);
  } catch {
    return undefined;
  } finally {
    if (timer !== undefined) clearTimeout(timer);
  }
}
let residencyIfCreated: TranscriptResidency | undefined;
const currentAdmissions = new Map<string, CurrentTranscriptAdmission>();
// The tab's in-memory view of the durable clear epoch: undefined until the
// first lookup observes it. Only durable observations advance it, monotonically.
// Optimistic same-tab clear arming lives separately in clearInFlight.
let tabCacheEpoch: number | undefined;

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
type PendingTranscriptUpdate = ReceivedTranscriptUpdate & {
  // Live acceptance already banked this arrival. The captured owner also
  // identifies an unchanged replay whose canonical snapshot remains current.
  acceptedEntry?: { repository: TranscriptRepository; lifetime: number };
};
type PendingThreadHydration = {
  client: AppwireClientLike;
  epoch: number;
  notifications: PendingTranscriptUpdate[];
  backgroundBytes: number;
  backgroundInvalidated?: boolean;
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
  // The entry-route retirement counter captured before this hydration's source
  // read was issued. A delete or same-tab Clear that lands before the durable
  // authority read resolves moves the counter, so the acquisition is refused
  // rather than granted the new authorities.
  entryRetirement?: number;
  // The installed binding's durable deletion claim when the source read began.
  // A later durable observation must not grant an older response a newer fence.
  entryHistoryKey?: string;
  entryHistoryGeneration?: number;
  // The per-ref read sequence history-requesting hydration reads stamp from
  // (threadReadSequences): pending-entry currency is per-kind, so only this
  // shared counter orders reads across the tracked and watched maps — an
  // older read of one kind cannot publish over a newer read of the other.
  // A lean watched read (no turns) neither advances it nor publishes a
  // repair-clearing verdict: it does not touch the transcript index, so it
  // has no cross-kind repair verdict to order.
  readSequence: number;
  repairingRetired?: boolean;
};
// A thread/read subscribes before it returns its snapshot. Notifications can
// therefore arrive in the gap between the source subscription and snapshot
// response. Keep the newest hydration's notifications out of the old model,
// then fold them onto the returned snapshot before publishing it.
const pendingThreadHydrations = new Map<string, PendingThreadHydration>();
// The per-ref read sequence history-requesting reads stamp their pendings
// from: each map keeps its own current entry, so pending-entry currency alone
// cannot order a tracked read against a watched one. Monotonic per ref, and
// advanced only by reads that request history — a lean watched read carries
// the current value instead, so it cannot fence out a tracked read's repair
// verdict.
const threadReadSequences = new Map<string, number>();
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
// Clear retires response ownership, not the mounted owner's need for a read.
const clearHydrationDemands = new Map<HydrationOwnerKind, Map<string, number>>();

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
  cacheLifetimeHeld.delete(ref);
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
    return {
      threads: nextThreads,
      frameTimes: nextFrameTimes,
      deletedRefs: nextDeletedRefs,
    };
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
type HydrationIntent = "foreground" | "background" | "final";

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

// #4342's paced re-drive for a mutation the hub refused in band as transient
// backpressure: the row stays queued, and this paces when the next attempt
// goes out so a saturated hub is not hammered and a freed hub is re-driven
// promptly. A scheduler, not a clock, mirroring the hydration retry's
// injectable seam: tests install a manual queue and invoke the retry
// directly. The returned function cancels the scheduled callback.
type BackpressureRetryScheduler = (attempt: number, retry: () => void) => () => void;

const BACKPRESSURE_RETRY_BASE_MS = 500;
const BACKPRESSURE_RETRY_MAX_MS = 15_000;

const backoffBackpressureRetryScheduler: BackpressureRetryScheduler = (attempt, retry) => {
  const delay = Math.min(BACKPRESSURE_RETRY_MAX_MS, BACKPRESSURE_RETRY_BASE_MS * 2 ** Math.max(0, attempt - 1));
  const timer = setTimeout(retry, delay);
  return () => clearTimeout(timer);
};

let backpressureRetryScheduler = backoffBackpressureRetryScheduler;

export function installBackpressureRetrySchedulerForTests(scheduler: BackpressureRetryScheduler): () => void {
  const previous = backpressureRetryScheduler;
  backpressureRetryScheduler = scheduler;
  return () => {
    backpressureRetryScheduler = previous;
  };
}

// Per ref, the cancel handle of the paced retry the ref's last in-band
// refusal armed. Serialized per-ref dispatch means at most one refused row
// per ref at a time (the FIFO head), so one handle per ref is exact. The
// refused-row fact itself (the row id and the pace count) lives in the
// store's transientRefusalPaces — one structure, not two kept in sync — so
// these handles are the only module-side state: an imperative side-channel to
// timers the reactive state cannot hold.
const transientRefusalCancels = new Map<string, () => void>();

// Records a hub backpressure refusal of one queued row: marks the row busy in
// the store and arms its paced retry, replacing any timer an earlier
// refusal for the row already armed. The retry re-drives the ref's ordinary
// dispatch, which every ready, discovery and lifecycle event also does —
// the pace only shortens the wait. The published entry leaves with
// settlement (pruneSettledTransientRefusals), not with the attempt: an
// in-flight attempt is still the refusal being worked.
function markTransientMutationRefusal(runtime: MutationRuntime, record: MutationOutboxRecord): void {
  const previous = threadsStore.getState().transientRefusalPaces.get(record.targetRef);
  const attempt = previous?.clientMutationId === record.clientMutationId ? previous.attempt + 1 : 1;
  // The refused row is the ref's one refused row (serialized dispatch), so
  // the retry an earlier refusal for it armed is superseded: cancel it
  // before arming the replacement, or refusals arriving faster than the
  // timer fires would stack one live timer per refusal.
  transientRefusalCancels.get(record.targetRef)?.();
  const cancelRetry = backpressureRetryScheduler(attempt, () => {
    // The handle is spent at its own fire: `transientRefusalRetryPending`
    // reads this map, and a fired timer must not keep suppressing the
    // projection hand-off once nothing is left to re-drive the ref - the
    // marker outlives the timer (it clears on settlement), so the map is
    // the one structure that tracks the timer's own lifetime.
    transientRefusalCancels.delete(record.targetRef);
    if (!isCurrentMutationRuntime(runtime)) return;
    scheduleMutationDispatch(runtime, [record.targetRef]);
  });
  transientRefusalCancels.set(record.targetRef, cancelRetry);
  threadsStore.setState((state) => ({
    transientRefusalPaces: new Map(state.transientRefusalPaces).set(record.targetRef, {
      clientMutationId: record.clientMutationId,
      attempt,
    }),
  }));
}

// The settlement half of the busy marker's contract: the marker clears when
// the refused record settles — a receipt, a recovery transfer, a blocked
// reclassification. Every settlement the dispatcher performs changes storage
// and notifies onStorageChange for the ref, so this prune rides that
// notification: it re-reads the refused row by its primary key and clears
// the entry when that row no longer sits submitting there. A refused retry
// does not disturb the ladder — the transient refusal path changes nothing a
// reader can see and notifies nothing.
async function pruneSettledTransientRefusals(runtime: MutationRuntime, targetRefs: Iterable<string>): Promise<void> {
  for (const targetRef of targetRefs) {
    const entry = threadsStore.getState().transientRefusalPaces.get(targetRef);
    if (!entry) continue;
    const cancelRetry = transientRefusalCancels.get(targetRef);
    // A failed read proves nothing, so it clears nothing: the marker and its
    // armed retry stay for a later recovery pass. The prune is fire-and-forget
    // at every call site, so containing the failure here is also what keeps a
    // storage error from escaping as an unhandled rejection.
    let row: MutationOutboxRecord | undefined;
    try {
      row = await runtime.storage.getOutbox(entry.clientMutationId);
    } catch {
      continue;
    }
    if (!isCurrentMutationRuntime(runtime)) return;
    if (row?.state === "submitting") continue;
    // The refused row settled: its entry, its busy marker, and any retry
    // still armed for it are spent. The identity checks keep a refusal that
    // replaced this entry between the read and the clear.
    cancelRetry?.();
    if (transientRefusalCancels.get(targetRef) === cancelRetry) transientRefusalCancels.delete(targetRef);
    threadsStore.setState((state) => {
      if (state.transientRefusalPaces.get(targetRef)?.clientMutationId !== entry.clientMutationId) return state;
      const next = new Map(state.transientRefusalPaces);
      next.delete(targetRef);
      return { transientRefusalPaces: next };
    });
  }
}

let wiredClient: AppwireClientLike | null = null;
let readyEpoch = 0;
const readyGenerationRetirementWaiters = new Map<number, Set<() => void>>();

function watchReadyGenerationRetirement(epoch: number, waiter: () => void): () => void {
  let waiters = readyGenerationRetirementWaiters.get(epoch);
  if (!waiters) {
    waiters = new Set();
    readyGenerationRetirementWaiters.set(epoch, waiters);
  }
  waiters.add(waiter);
  return () => {
    waiters?.delete(waiter);
    if (waiters?.size === 0) readyGenerationRetirementWaiters.delete(epoch);
  };
}

function retireReadyGeneration(epoch: number): void {
  const waiters = readyGenerationRetirementWaiters.get(epoch);
  if (!waiters) return;
  readyGenerationRetirementWaiters.delete(epoch);
  for (const waiter of waiters) waiter();
}

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
  const held = canonicalThreadOwnerActive(ref) || finalReleaseOwners.has(ref) || (watchRefCounts.get(ref) ?? 0) > 0;
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
  beginCanonicalThreadClaim(ref);
  pinnedMutationRefs.add(ref);
  syncTranscriptResidencyOwners(ref);
  syncThreadSubscription(ref);
}

function unpinMutationRef(ref: string): void {
  pinnedMutationRefs.delete(ref);
  syncTranscriptResidencyOwners(ref);
  retireCanonicalThreadIfUnowned(ref);
  // Hand the last pin's existing wire lease to final retirement before syncing
  // membership. Starting the drain later would unsubscribe and re-subscribe.
  retireEntryTranscriptIfUnowned(ref);
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
  // The session-content clear's cache hook: the cleared model is a bare
  // hydrate with no history, so it would never match the write gates;
  // deleting the record in the same step is what keeps a cleared session's
  // next reload from painting pre-clear content from the shell.
  noteEntryRouteRetirement(targetRef);
  deleteCachedRefs([targetRef]);
  // A clear response is a newer authoritative cut than any thread/read that
  // was already in flight for this ref. Retire those reads before publishing
  // the replacement so a late pre-clear snapshot cannot overwrite it.
  const pendingThread = pendingThreadHydrations.get(targetRef);
  if (pendingThread) retirePendingHydration(targetRef, pendingThread, pendingThreadHydrations);
  const pendingWatched = pendingWatchedHydrations.get(targetRef);
  if (pendingWatched) retirePendingHydration(targetRef, pendingWatched, pendingWatchedHydrations);
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
  // The dispatch-ready generation is the reconciled one, and reconciliation
  // needs durable storage. With no runtime there is nothing to reconcile, so the
  // wired ready client IS the dispatch generation: a send must still reach the
  // daemon when the tab has no IndexedDB at all (#4194).
  if (getMutationRuntime() !== null && (wiredClient !== dispatchReadyClient || readyEpoch !== dispatchReadyEpoch))
    return null;
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
      // A graceful shutdown's own cancellation write is in flight: the write is
      // an await and is not itself a fence, so a dispatch already scheduled for
      // the ref could be admitted in that window and put the row the Stop is
      // cancelling on the wire (#4552). Read only for the dispatcher's armed
      // lookup (requireArmed): the Send faces and the storage-failure direct
      // fallback must stay exactly as they were (Jesse's ruling), so a Send
      // during the window is still admitted.
      (requireArmed && shutdownDispatchGates.has(targetRef)) ||
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
      // This gate reads resumeOnlyDispatchable, NOT resumeOnlyModel:
      // dispatch asks "is this exact head record admissible?". At the
      // head-of-loop lookup the named record may itself be the turn/start with
      // nothing ahead of it, while resumeOnlyModel's hasQueuedNonSend
      // clause answers the ADMISSION question ("may a NEW send be minted behind
      // this queue?"), where a queued non-send row is genuinely ahead of the
      // candidate. Used here that clause refused the head turn/start before the
      // method-aware recheck could name it, so a ref holding [turn/start,
      // turn/queue] could neither resume nor drain its outbox. The method check
      // below still parks the tail non-send row.
      (state.restartBlockingObligations.has(targetRef) &&
        !(resumeOnlyDispatchable(targetRef) && (method === undefined || method === "turn/start"))) ||
      state.mutationReconciliationFailures.has(targetRef))
  )
    return null;
  if (targetRef && state.threads.get(targetRef)?.status.type === "restartRequired") return null;
  return wiredClient?.state === "ready" ? wiredClient : null;
}

function dropUnpinnedModel(ref: string, flush = true): void {
  if (canonicalThreadOwnerActive(ref) || finalReleaseOwners.has(ref)) return;
  // The last mutation/history pin is a real final owner too. Keep the model
  // while the repository captures and saves its finite final read, just as a
  // pane/background last release does. The retirement callback drops with
  // flush=false, so this cannot start a second retirement.
  if (flush && retireEntryTranscriptIfUnowned(ref)) return;
  cacheLifetimeHeld.delete(ref);
  // Nothing owns this ref any more, so no scheduled retry may outlive it.
  retireOwnedHydration("thread", ref);
  // A read still on the wire when the pin left belongs to the lifetime this
  // drain just ended: retire its pending entry and its inflight bookkeeping
  // the way releaseThread does, so a late resolution finds the publication
  // fence closed — and a pane reopening here starts its own fresh
  // hydration instead of adopting a promise whose rejection can no longer
  // schedule a retry.
  pendingThreadHydrations.delete(ref);
  inflightHydrates.delete(ref);
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
  residencyIfCreated?.removed(ref);
  // With the last durable claim gone, the repair flag follows unless a
  // watcher still holds the ref.
  clearThreadRepairingIfUnowned(ref);
}

async function refreshMutationPins(runtime: MutationRuntime, targetRefs: Iterable<string>): Promise<void> {
  if (!isCurrentMutationRuntime(runtime)) return;
  // Every dispatch tail - including the discovery-driven ones - re-reads the
  // ref's shared storage, so a refusal whose row another tab settled (this
  // tab's dispatcher never sees that settle) prunes here.
  void pruneSettledTransientRefusals(runtime, targetRefs);
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

// True while the ref's paced-retry timer is live. That timer owns the
// re-drive of the ref's refused row, so a projection refresh completing in
// its window must not hand readiness to the dispatcher: the refusal's own
// notification can trigger exactly that refresh (a reopened row's rollback
// changed the projected shape), and the hand-off would redeliver the refused
// row immediately instead of waiting its pace step. The handle is consumed
// at the timer's fire (markTransientMutationRefusal), so the suppression
// always has a live re-drive behind it and ends when the timer does - the
// fired timer itself schedules the dispatch the hand-off would have.
export function transientRefusalRetryPending(targetRef: string): boolean {
  return transientRefusalCancels.has(targetRef);
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
    onStorageChange: (targetRefs, cause) => {
      // The dispatcher's own state changes - a settle, a recovery transfer, a
      // blocked reclassification - invalidate in-flight reads for every ref it
      // names, on the rule noteMutationStateChange states.
      for (const targetRef of targetRefs) noteMutationStateChange(targetRef);
      if (isCurrentMutationRuntime(runtime)) {
        // #4342 (round-11 review): the transient refusal's rollback
        // reverted only attempt evidence - fields no queue projection
        // carries - so the browser's projection refresh would read an
        // identical queue, and its newly-loaded hand-off would immediately
        // redispatch the refused row (observed 222 requests in ~350ms for
        // a clean rollback, 169 in ~370ms with residual evidence). Skip the
        // refresh for the unchanged shape; the fence-held cancellation
        // still refreshes, every non-refusal notification refreshes as
        // before, and native's own consumer ignores the cause and re-reads
        // regardless.
        if (cause?.kind !== "transientRefusal" || !cause.projectedShapeUnchanged) {
          notifyMutationPersistence(targetRefs);
        }
        void pruneSettledTransientRefusals(runtime, targetRefs);
      }
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
    onTransientRefusal: (record) => {
      if (!isCurrentMutationRuntime(runtime)) return;
      markTransientMutationRefusal(runtime, record);
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
          entryTranscriptRoute(ref)?.repository?.acceptResponseFields({ humanNote: response.note });
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

// The refusal for a mutation that needed local persistence this tab does not
// have. Shared by requireMutationRuntime and the send funnel's no-IndexedDB
// branch so the two text cannot drift.
const INDEXEDDB_UNAVAILABLE = "threads store: IndexedDB is unavailable; mutation was not sent";

function requireMutationRuntime(): MutationRuntime {
  const runtime = getMutationRuntime();
  if (!runtime) throw new Error(INDEXEDDB_UNAVAILABLE);
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
  // A drain that just ended settles everything waiting on it. Reading the
  // store rather than `stopping` here is deliberate: a concurrent Stop for the
  // same ref can leave the flag set, and those waiters must keep waiting.
  if (!threadsStore.getState().stoppingRefs.has(ref)) settleDrainWaiters(ref);
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

// The dispatch-only gate a graceful shutdown arms for the refs its cancellation
// writes reach (#4552; Jesse's 2026-10-13 ruling). A Stop's cancellation write
// is an await and is not itself a fence, so a dispatch already scheduled for the
// ref can be admitted in that window and put the row the Stop is cancelling on
// the wire. forceStop closes the window with stoppingRefs; a graceful shutdown
// must NOT, because that fence also refuses a Send the user makes during the
// window (isRecoveryFenced and isSendResumes read it). This gate is
// read by currentDispatchClient ALONE, never by the Send faces, so only the
// cancelled row is kept off the wire. The named ref's arm is held for the whole
// shutdown; the resume-swap target's is released as soon as its own write
// settles. Refcounted: two overlapping shutdowns must not clear each other's
// arm.
const shutdownDispatchGates = new Map<string, number>();

function armShutdownDispatchGate(ref: string): () => void {
  shutdownDispatchGates.set(ref, (shutdownDispatchGates.get(ref) ?? 0) + 1);
  // The release is idempotent per call, so a call site can release an arm early
  // (the resume-swap target's arm covers only its own write) and still release
  // unconditionally in its outer finally without double-decrementing.
  let released = false;
  return () => {
    if (released) return;
    released = true;
    const count = shutdownDispatchGates.get(ref);
    if (count === undefined) return;
    if (count > 1) {
      shutdownDispatchGates.set(ref, count - 1);
      return;
    }
    shutdownDispatchGates.delete(ref);
    // The ref's dispatch was fenced for the gate's window, and a dispatch
    // scheduled inside it (a Send admitted during the shutdown, say) was
    // refused. Re-schedule on the way out rather than leave the row parked
    // until the next discovery scan finds it.
    const runtime = getMutationRuntime();
    if (runtime) scheduleMutationDispatch(runtime, [ref]);
  };
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
        if (generation > (generations.get(stoppedRef) ?? 0)) throw new StopCanceledError();
      }
      return;
    }
    if ((userIntentStopGenerations.get(ref) ?? 0) !== (generations.get(ref) ?? 0)) throw new StopCanceledError();
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

// Waiters parked on a ref's Stop drain, settled by markStopping when the drain
// ends. A resolver is not a store subscription: the only write that can end a
// drain is markStopping's, so the waiters belong to that writer instead of
// riding every unrelated store change for the drain's whole length.
const drainWaiters = new Map<string, Set<() => void>>();

function settleDrainWaiters(ref: string): void {
  const waiters = drainWaiters.get(ref);
  if (waiters === undefined) return;
  drainWaiters.delete(ref);
  for (const resolve of waiters) resolve();
}

// Resolves once no Stop this page started is still draining for ref. The
// caller re-reads the drain state rather than trusting a timer, because the
// hub's admission turns on the drain itself.
function waitForStopDrain(ref: string): Promise<void> {
  if (!threadsStore.getState().stoppingRefs.has(ref)) return Promise.resolve();
  return new Promise((resolve) => {
    const waiters = drainWaiters.get(ref);
    if (waiters === undefined) drainWaiters.set(ref, new Set([resolve]));
    else waiters.add(resolve);
  });
}

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
  // retarget moved, whether a Stop won the identity fence, and whether this
  // drive registered a swap (it only does when the resume changed the ref).
  let resumedRef: string | undefined;
  let movedFirst: RetargetedMutations | null = null;
  let stopWon = false;
  let swapRegistered = false;
  try {
    const client = wiredClient;
    if (client === null || client.state !== "ready")
      throw new Error("Connect to the hub before resuming this session.");
    // The baseline is taken BEFORE the drain wait: a Stop that lands during the
    // wait is a person canceling the message this drive would send, and a
    // baseline captured after it would treat that Stop as already accounted for
    // and resume anyway.
    const stopBaseline = resumeStopBaseline();
    // A Stop this page started holds Stopping > 0 and the hub refuses even
    // turn/start for that window, so wait the drain out before resuming. Send
    // stays the single recovery action: the press parks its row and this drive
    // runs once the drain settles, instead of the user meeting a Resume button.
    // Only a drain in progress defers the RPC; with none, the resume starts in
    // this same tick so a Send still issues it on the press's own turn.
    while (threadsStore.getState().stoppingRefs.has(ref)) await waitForStopDrain(ref);
    const { thread } = await client.resumeThread(ref, { beforeRequest: stopBaseline });
    resumedRef = thread.evener.ref;
    // The token the daemon's instance fence compares (server/appwire_thread_settings.go's
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
      registerResumeSwap(ref, resumedRef);
      swapRegistered = true;
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
      scheduleRetargetDrain(ref, resumedRef, resumedIdentity);
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
    if (swapRegistered) endResumeSwap(ref);
    const message = error instanceof Error ? error.message : String(error);
    resumeFailureSeq += 1;
    threadsStore.setState((state) => ({
      resumeFailures: new Map(state.resumeFailures).set(ref, {
        seq: resumeFailureSeq,
        message,
        stopCanceled: error instanceof StopCanceledError,
      }),
    }));
  }
}

// The late-row drain's in-line bound: enough passes for the common case. The
// scheduled trailing pass runs regardless, so the bound never exits silently.
const RETARGET_DRAIN_PASSES = 3;

// A resume that changed identity is mid-swap: the rows it moved to the resumed
// ref are still the user's to cancel with a Stop pressed on the ref they are
// looking at, but a Stop's own durable cancel names only that ref. This map
// (old ref -> resumed ref) is the registration that closes the gap, live from
// the moment the resume response names the differing ref until the pane follows
// the identity that drive published (Session.tsx's effect, which clears the
// resumed identity) or the drive fails.
//
// It is NOT ended when the retarget machinery goes quiet: an empty drain pass
// proves only that no rows remain parked under the OLD ref - not that the rows
// already moved to the resumed ref were delivered, and not that the pane has
// followed (the follow is a later React commit). With the resumed ref itself
// fenced, the moved rows sit unattempted under it, and a Stop on the
// still-displayed old ref in that pre-navigation sliver would find no
// registration and let the message dispatch when the resumed ref clears.
const resumeSwapTargets = new Map<string, string>();

function registerResumeSwap(fromRef: string, toRef: string): void {
  resumeSwapTargets.set(fromRef, toRef);
}

// The registration ends at the pane's follow (clearResumedIdentity: the user is
// now looking at the resumed ref, so a Stop names it directly) or at this
// drive's failure (a failed drive publishes no identity, so no pane will ever
// follow to clear it). Idempotent: deleting an absent entry is a no-op.
function endResumeSwap(fromRef: string): void {
  resumeSwapTargets.delete(fromRef);
}

// The target a Stop pressed on `ref` must also cancel, read SYNCHRONOUSLY at the
// press: the pane's follow effect ends the registration, and forceStop and
// shutdown await their own cancellation write before they reach the cancel - a
// Stop pressed while the pane still showed `ref` must still cancel the rows the
// drive moved, even if the follow lands inside that await.
function resumeSwapTargetFor(ref: string): string | undefined {
  return resumeSwapTargets.get(ref);
}

// The Stop call sites' second cancel: while `ref` is mid-swap (the captured
// target is defined), a Stop pressed on it must also cancel the unattempted rows
// the drive moved to the resumed ref. Once the pane has followed the identity,
// a Stop is about the resumed ref's own rows (a Stop there names it directly)
// and the registration is gone, so the captured target is undefined.
async function cancelResumeSwapTarget(toRef: string | undefined): Promise<void> {
  if (toRef === undefined) return;
  await cancelUnattemptedMutations(toRef);
}

// One more drain pass, on a later task, re-scheduling itself while passes keep
// moving rows. The old ref's restart obligation is not cleared by the drive (see
// the drain loop's comment), so a concurrent send can park during the drain;
// this converges on a clean snapshot without blocking the event loop. The pass
// that moves nothing does NOT settle the Stop registration: it proves only that
// no rows remain parked under the old ref, not that the rows already moved to
// the resumed ref were delivered or that the pane has followed. The
// registration ends with that follow (endResumeSwap, from clearResumedIdentity)
// or with this drive's failure.
function scheduleRetargetDrain(
  fromRef: string,
  toRef: string,
  identity: { threadId?: string; instanceId?: string },
): void {
  queueMicrotask(() => {
    void retargetParkedMutations(fromRef, toRef, identity)
      .then((moved) => {
        if (moved !== null) scheduleRetargetDrain(fromRef, toRef, identity);
      })
      // Silence is deliberate, like the store's other fire-and-forget tails: the
      // resume itself already succeeded, so a resume-failure toast would be
      // wrong. A storage failure here just leaves the straggler parked under the
      // old ref - the pre-move behavior - where a later drive or a fresh
      // discovery retries it. The registration is deliberately NOT settled here
      // either: rows an earlier pass already moved to the resumed ref are still
      // the old ref's Stop to cancel while the pane has not followed.
      .catch(() => {});
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
  if (record.method === "notes/human/set" && !canWriteSharedNotes(trackedThreadModel(record.targetRef))) return false;
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

function localMutationIdentityMatches(record: MutationOutboxRecord, targetRef: string): boolean {
  const state = threadsStore.getState();
  const model = state.threads.get(targetRef) ?? state.watchedThreads.get(targetRef);
  return (
    record.targetRef === targetRef &&
    !state.deletedRefs.has(targetRef) &&
    model !== undefined &&
    (record.instanceId ?? record.threadId) === threadInstanceID(model)
  );
}

export async function editQueuedMutation(
  clientMutationId: string,
  targetRef: string,
): Promise<MutationRecoveryRecord | undefined> {
  const runtime = requireMutationRuntime();
  const capture = runtime.storage.getOutboxWithStopEpoch(clientMutationId);
  await runtime.start;
  const { record, stopEpoch } = await capture;
  if (!record || !canEditQueuedMessage(record) || !localMutationIdentityMatches(record, targetRef)) return undefined;
  const recovery = await runtime.storage.editQueuedMessage(record, { stopEpoch });
  if (recovery) noteHandledMutation(clientMutationId);
  noteMutationStateChange(targetRef);
  notifyMutationPersistence([targetRef]);
  return recovery;
}

export async function removeQueuedMutation(clientMutationId: string, targetRef: string): Promise<boolean> {
  const runtime = requireMutationRuntime();
  const capture = runtime.storage.getOutboxWithStopEpoch(clientMutationId);
  await runtime.start;
  const { record, stopEpoch } = await capture;
  if (!record || !canRemoveQueuedMessage(record) || !localMutationIdentityMatches(record, targetRef)) return false;
  const removed = await runtime.storage.removeQueuedMessage(record, { stopEpoch });
  if (removed) noteHandledMutation(clientMutationId);
  noteMutationStateChange(targetRef);
  notifyMutationPersistence([targetRef]);
  return removed;
}

interface QueuedMutationProjection {
  subscribe(listener: () => void): () => void;
  getOutbox(): ReadonlyMap<string, MutationOutboxRecord>;
  refresh(ref: string): Promise<boolean>;
}
let queuedMutationProjection: QueuedMutationProjection | undefined;

// The browser projection owns the snapshot. Register its read port rather
// than creating a second asynchronously refreshed copy in this selector.
export function installQueuedMutationProjection(projection: QueuedMutationProjection): void {
  queuedMutationProjection = projection;
}

export function useEditableQueuedMutationEntries(ref: string): MutationOutboxRecord[] {
  const projection = queuedMutationProjection;
  if (!projection) throw new Error("Queued mutation projection is not installed");
  const outbox = useSyncExternalStore(projection.subscribe, projection.getOutbox, projection.getOutbox);
  useEffect(() => {
    void projection.refresh(ref);
  }, [ref]);
  // Include removable controls so the strip can offer Remove without Edit.
  return useMemo(
    () => [...outbox.values()].filter((record) => record.targetRef === ref && canRemoveQueuedMessage(record)),
    [outbox, ref],
  );
}

export function captureRecoveryStopBarrier(targetRef: string): Promise<MutationStopBarrier> {
  const barrier = requireMutationRuntime()
    .storage.readStopEpoch(targetRef)
    .then((stopEpoch) => ({ stopEpoch }));
  // The capture can fail while Composer is still awaiting its recovery write.
  // Keep that rejection for the sender without an unhandled interim promise.
  void barrier.catch(() => undefined);
  return barrier;
}

export async function updateRecoveryMutation(
  clientMutationId: string,
  targetRef: string,
  text: string,
  attachments: InputAttachment[],
  skillNames?: readonly string[],
  commandNames?: readonly string[],
  mentions?: readonly ComposerMention[],
  consumeRecoveryIds?: readonly string[],
  localDraftRevisionId?: string,
  expectedCompositionRevision?: number,
  expectedRecoveryRevisions?: Readonly<Record<string, number>>,
  compositionOwnerId?: string,
  composerText?: string,
): Promise<MutationRecoveryRecord | undefined> {
  const runtime = requireMutationRuntime();
  await runtime.start;
  const record = await runtime.storage.updateRecoveryInput(
    clientMutationId,
    buildInput(text, attachments, skillNames, commandNames, mentions),
    durableAttachments(attachments),
    composerText ?? text,
    mentions,
    consumeRecoveryIds,
    localDraftRevisionId,
    expectedCompositionRevision,
    expectedRecoveryRevisions,
    compositionOwnerId,
  );
  if (!record) return undefined;
  notifyMutationPersistence([targetRef]);
  return record;
}

// Preserve a stale pane's complete composition under a fresh recovery owner.
// The staled row is never written: the fresh owner reuses its method and
// target, and carries the local text, selections and attachment bytes.
export async function preserveRecoveryMutation(
  base: MutationRecoveryRecord,
  targetRef: string,
  text: string,
  attachments: InputAttachment[],
  skillNames?: readonly string[],
  commandNames?: readonly string[],
  mentions?: readonly ComposerMention[],
  compositionOwnerId?: string,
  localDraftRevisionId?: string,
): Promise<MutationRecoveryRecord | undefined> {
  const runtime = requireMutationRuntime();
  await runtime.start;
  const record = await runtime.storage.preserveRecoveryComposition(
    base,
    buildInput(text, attachments, skillNames, commandNames, mentions),
    durableAttachments(attachments),
    text,
    mentions,
    compositionOwnerId,
    localDraftRevisionId,
  );
  if (!record) return undefined;
  notifyMutationPersistence([targetRef]);
  return record;
}

export async function discardRecoveryMutation(
  clientMutationId: string,
  targetRef: string,
  shouldDiscard?: () => boolean,
  expectedCompositionRevision?: number,
): Promise<boolean> {
  const runtime = requireMutationRuntime();
  await runtime.start;
  const discarded = await runtime.storage.discardRecovery(clientMutationId, shouldDiscard, expectedCompositionRevision);
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
  barrierRead = captureRecoveryStopBarrier(targetRef),
  expectedRecoveryRevisions?: Readonly<Record<string, number>>,
  compositionOwnerId?: string,
): Promise<MutationRecoveryResendResult> {
  const runtime = requireMutationRuntime();
  await runtime.start;
  const intent = composerMutationIntent(targetRef, route, text, attachments, skillNames, commandNames, mentions);
  const barrier = await barrierRead;
  // Registered around the write: the resent row is undelivered work, and a
  // fallback send must see it before the async refresh can.
  const result = await trackOutboxWrite(
    targetRef,
    () =>
      runtime.storage.resendRecoveryResult(
        clientMutationId,
        intent,
        barrier,
        expectedRecoveryRevisions,
        compositionOwnerId,
      ),
    (outcome) =>
      outcome.status !== "sent"
        ? undefined
        : {
            clientMutationId: outcome.record.clientMutationId,
            undelivered: outcome.record.state !== "canceled",
          },
  );
  if (result.status !== "sent") return result;
  const { record } = result;
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
  if (sendResumesModel(targetRef)) void resumeFencedForSend(targetRef);
  return result;
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
const inflightWatchHydrates = new InflightHydrations();
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

// setThreadRepairing records the ref's latest latest-window read outcome for
// #4343's rebuilding status: true when the read answered the hub's
// transcriptIndexRebuilding discriminant, false when it answered anything
// else or succeeded. The rule is per-ref and per-read — the flag never
// outlives the read that set it — so the loading surface reading it stays
// honest through the whole repair-and-retry cycle, including the
// budget-exhausted transition to the history-failed diagnostic.
function setThreadRepairing(ref: string, repairing: boolean): void {
  threadsStore.setState((s) => {
    if (s.repairingRefs.has(ref) === repairing) return s;
    const next = new Set(s.repairingRefs);
    if (repairing) next.add(ref);
    else next.delete(ref);
    return { repairingRefs: next };
  });
}

// publishThreadRepairing is setThreadRepairing behind the same ownership fence
// publishThreadHydration and scheduleOwnedHydrationRetry use, plus what that
// fence cannot see across kinds: only the ref's CURRENT pending hydration,
// on the current client and ready epoch, whose read is the ref's newest of
// EITHER kind, may publish its repair outcome. A late resolution from an
// attempt a newer one superseded, from a lifetime its owner released (the
// releases retire its repair verdict or delete the pending entry), or from an
// older read of the other kind — still current in its own map — must not clear a flag a newer
// read set or recreate one a newer read already answered.
function publishThreadRepairing(
  kind: HydrationOwnerKind,
  ref: string,
  pending: PendingThreadHydration,
  repairing: boolean,
): void {
  const pendingHydrations = kind === "watched" ? pendingWatchedHydrations : pendingThreadHydrations;
  if (pending.repairingRetired) return;
  if (pendingHydrations.get(ref) !== pending) return;
  if (wiredClient !== pending.client || readyEpoch !== pending.epoch) return;
  if ((threadReadSequences.get(ref) ?? 0) !== pending.readSequence) return;
  setThreadRepairing(ref, repairing);
}

// The repair flag never outlives its owners: the release that drops the ref's
// last holder of either kind clears it, because no read that could answer it
// honestly is still in flight — this release already fenced the pending
// entry a late read would need to publish one — and no owner remains to
// re-open the ref. A remaining holder of the other kind keeps the flag: their
// pane or card still renders the same in-flight repair.
function clearThreadRepairingIfUnowned(ref: string): void {
  if (canonicalThreadOwnerActive(ref)) return;
  if ((watchRefCounts.get(ref) ?? 0) > 0) return;
  setThreadRepairing(ref, false);
}

interface ThreadHydration {
  model: ThreadModel;
  response: ThreadReadResponse;
  fixtureNotifications: ReceivedTranscriptUpdate[];
  discardHeldHistory?: true;
}

// The two hydration lifecycles read a ref the same way and differ in these
// explicit places. A tracked read (a pane or background owner) always asks for
// turns, may run under a background AbortSignal with its own subscription
// lease, starts from the entry route's cached-unverified fence, and records a
// history failure on the entry repository. A watched read asks for turns only
// when a watcher wants them (lean watches omit them until an expanded card
// asks), has no signal, and records a history failure on the watched model.
type HydrationRead = { kind: "thread"; signal?: AbortSignal } | { kind: "watched"; includeTurns: boolean };

async function readThreadHydration(
  client: AppwireClientLike,
  ref: string,
  now: number,
  pending: PendingThreadHydration,
  read: HydrationRead,
): Promise<ThreadHydration> {
  const tracked = read.kind === "thread";
  const signal = tracked ? read.signal : undefined;
  const includeTurns = tracked || read.includeTurns;
  let response: ThreadReadResponse;
  // Cached display is not a source baseline. Its generations, epoch and length
  // cannot fence the first successful read or ask the source for a delta.
  let discardHeldHistory = tracked && productionEntryRoutes.get(ref)?.cachedUnverified === true;
  const ownerLease = syncThreadSubscription(ref, client);
  if (!ownerLease) throw new Error("Thread hydration has no subscription holder");
  // A background attempt gets its own lease so retirement can invalidate a
  // read deferred behind another membership transition without dropping the
  // canonical owner that still protects the final-release cut.
  const lease = signal ? acquireThreadSubscription(client, ref) : ownerLease;
  const releaseLease = () => lease.release();
  signal?.addEventListener("abort", releaseLease, { once: true });
  const held = discardHeldHistory ? undefined : heldSnapshotFor(pending.baseModel);
  try {
    try {
      if (signal?.aborted) throw new Error("Thread hydration attempt was retired");
      response = await readThreadForgettingConfirmed(
        ref,
        lease,
        threadReadParams(includeTurns, pending.requestGeneration, held),
      );
    } catch (err) {
      if (signal?.aborted) throw err;
      if (!shouldRetryWithoutHeldSnapshot(held, err)) throw err;
      // Logical retirement removes this operation before its issued request's
      // physical slot settles. Its still-live signal cannot authorize a retry.
      if (signal?.aborted || (tracked && pendingThreadHydrations.get(ref) !== pending)) throw err;
      response = await readThreadForgettingConfirmed(
        ref,
        lease,
        threadReadParams(includeTurns, pending.requestGeneration, undefined),
      );
      discardHeldHistory = true;
    }
  } catch (err) {
    if (signal?.aborted) throw err;
    // thread/read is answered from the daemon's in-memory snapshot, so a
    // rejection here is a transport failure, not a slow file read and not a
    // lost claim. Ask this ref's owner generation to read again.
    markThreadDeletedIfFenced(ref, err);
    // #4343: a typed "index rebuilding" answer is a retryable state — record
    // it for the loading surface (set on this code, cleared by any other
    // outcome of this attempt's read, the success path below included; the
    // publishThreadRepairing fence drops a superseded attempt's late say).
    // A lean watched read publishes only an explicit rebuilding answer it
    // received itself: its ordinary rejection says nothing about the
    // transcript index, so it must not replace a tracked read's repair status
    // (the rejection-side twin of the success gate below).
    if (includeTurns || isTranscriptIndexRebuildingError(err)) {
      publishThreadRepairing(read.kind, ref, pending, isTranscriptIndexRebuildingError(err));
    }
    // A structured "history failed" response is not a transport hiccup: the
    // spec says the client shows one visible diagnostic and waits for a later
    // read to succeed, never auto-retrying in a loop. Applied only against
    // this hydration's own base model (superseded generations are ignored by
    // applyHistoryReadFailure itself).
    if (isTranscriptHistoryFailedError(err) && pending.baseModel) {
      const failedModel = applyHistoryReadFailure(pending.baseModel, errorText(err), pending.requestGeneration ?? 0);
      const entry = tracked ? productionEntryRoutes.get(ref) : undefined;
      if (!tracked) {
        putWatchedThreadModel(ref, failedModel);
      } else if (entry?.repository) {
        if (
          pending.client === wiredClient &&
          pending.epoch === readyEpoch &&
          pendingThreadHydrations.get(ref) === pending
        ) {
          entry.repository.acceptReadFailure(errorText(err), pending.requestGeneration ?? 0, {
            lifetime: entry.lifetime,
          });
        }
      } else {
        putThreadModel(ref, failedModel);
      }
    } else {
      scheduleOwnedHydrationRetry(read.kind, ref, pending);
    }
    throw err;
  } finally {
    signal?.removeEventListener("abort", releaseLease);
    if (lease !== ownerLease) lease.release();
  }
  if (signal?.aborted) throw new Error("Thread hydration attempt was retired");
  // The read succeeded: any repairing status from this attempt's earlier
  // read is over — a superseded attempt leaves the newer one's say standing.
  // Only a history-requesting read observed the index: a lean watch the hub
  // can answer without touching it saw nothing about the repair, so its
  // success must not clear a status a tracked read owns.
  if (includeTurns) publishThreadRepairing(read.kind, ref, pending, false);
  // A retry that dropped the held snapshot asked for, and must be treated
  // as, a full latest-window replacement: applyReadResponse would still
  // merge it (same incarnation/epoch, length >= held's), leaving every held
  // item outside the fresh window exactly as stale as it was -- precisely
  // the items TranscriptItemCursorStale exists to protect, since the window
  // alone cannot answer for them. hydrateThread discards the held history
  // instead, matching the hub's intent.
  const model =
    !discardHeldHistory && pending.baseModel?.history !== undefined
      ? tracked
        ? applyCacheGapRule(ref, pending.baseModel, response, now)
        : applyReadResponse(pending.baseModel, response, now)
      : hydrateThread(response, ref, now);
  // The response cut retires buffered input from the legacy hydrator because
  // its snapshot is authoritative for that route. The entry repository also
  // receives this input, but only comparable versions may supersede the read:
  // recorded item/turn versions can preserve newer content outside its window,
  // and a higher queue revision can supersede its queue. Missing overlay slots
  // can be enriched under recorded coverage, never replacing response-owned
  // overlay slots or replaying their already-represented deltas.
  const fixtureNotifications = pending.notifications.slice();
  const cutModel = withoutResolvedEscalations(ref, pending, model);
  applyHydrationResponseCut(pending, ref, cutModel);
  return {
    model: cutModel,
    response,
    fixtureNotifications,
    ...(discardHeldHistory ? { discardHeldHistory: true } : {}),
  };
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

function olderItemsParams(ref: string, cursor: string) {
  return { ref, cursor, itemsView: "full", itemLimit: TRANSCRIPT_ITEM_PAGE_SIZE } as const;
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

async function sendMutationIntent(intent: MutationIntent): Promise<void> {
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
  // Mark the composer's shared per-session submission guard for every ordinary
  // send path, not only the ones wrapped by submitWithPendingTracking: the
  // command palette's slash fallthrough and the ask dock call send() directly.
  // The marker must exist before the durable enqueue, because Retry can look
  // at an empty projection while that write is still in flight. A submission
  // the composer already marked owns the marker, so this call does not end it.
  const release = markInputSubmission(intent.targetRef);
  try {
    await enqueueMutationIntent(intent);
    if (sendResumesModel(intent.targetRef)) void resumeFencedForSend(intent.targetRef);
  } finally {
    release();
  }
}

// Returns the release for a submission marker this call owns, or a no-op when
// an enclosing submission (submitWithPendingTracking, or Retry's own begin)
// already holds the ref's shared guard.
function markInputSubmission(ref: string): () => void {
  if (!beginSharedSubmission(ref)) return () => {};
  return () => endSharedSubmission(ref);
}

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
      method: COMPOSER_METHODS.send,
      payload: { ref, expectedInstanceId, input },
      optimisticDisplay: { method: COMPOSER_METHODS.send, input },
    };
  }
  if (route === "queue" || route === "steer") {
    const method = COMPOSER_METHODS[route];
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
    method: COMPOSER_METHODS.drain,
    payload: { ref, expectedInstanceId, expectedQueueRevision, input },
    optimisticDisplay: { method: COMPOSER_METHODS.drain, input },
  };
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
  // Retry's pending-owner input, read at activation: the reconciled entries
  // for `ref`. The shared eligibility decision applies ownPendingTurnInput,
  // and threads.ts cannot import the pending-turns store back without a cycle,
  // so it registers the read here beside the fence's own. Readiness is the
  // threads store's own reactive `pendingProjectionLoadedRefs` copy.
  pendingTurnEntries(ref: string): readonly PendingTurnEntry[];
  // The same per-session submission guard ordinary Send/press uses
  // (submittingRefs through pendingTurnsStore). Retry begins and ends one
  // submission through it, so an ordinary send whose durable enqueue is still
  // in flight blocks Retry, and Retry's own window blocks a second send.
  beginSubmission(ref: string): boolean;
  endSubmission(ref: string): void;
}
let resumeOnlyProjection: ResumeOnlyProjection | null = null;

export function installResumeOnlyProjection(projection: ResumeOnlyProjection): void {
  resumeOnlyProjection = projection;
}

// The pending-turns projection publishes a ref's durable-read readiness here
// at the same moment it records it for the fence's own predicate: one
// authority, a reactive copy for consumers. A transition notifies subscribers,
// so a mounted Retry action re-renders unloaded -> loaded (or back) rather than
// reading a stale plain Set.
export function setPendingProjectionLoaded(ref: string, loaded: boolean): void {
  threadsStore.setState((state) => {
    if (state.pendingProjectionLoadedRefs.has(ref) === loaded) return {};
    const pendingProjectionLoadedRefs = new Set(state.pendingProjectionLoadedRefs);
    if (loaded) pendingProjectionLoadedRefs.add(ref);
    else pendingProjectionLoadedRefs.delete(ref);
    return { pendingProjectionLoadedRefs };
  });
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

// The pending owner's inputs for Retry's shared decision, read at activation
// rather than trusted from the render that disabled (or enabled) the button.
// A ref whose durable pending read has not resolved is UNKNOWN, not empty:
// activation fails closed through the gate's not-ready reason, exactly as the
// rendered action stays disabled. No installed projection is likewise unknown
// (and has no entries to offer).
function retryPendingInputNow(ref: string): {
  pendingMutations: readonly PendingTurnEntry[] | null;
  pendingMutationsReady: boolean;
} {
  const projection = resumeOnlyProjection;
  return {
    pendingMutations: projection?.pendingTurnEntries(ref) ?? null,
    // No installed projection is unknown, not empty, exactly as before the
    // shared decision.
    pendingMutationsReady: projection !== null && threadsStore.getState().pendingProjectionLoadedRefs.has(ref),
  };
}

// Retry's share of the composer's per-session submission guard. The pending
// store owns `submittingRefs`; no projection installed means no shared owner
// can certify the window, so Retry fails closed.
function beginSharedSubmission(ref: string): boolean {
  return resumeOnlyProjection?.beginSubmission(ref) ?? false;
}

function endSharedSubmission(ref: string): void {
  resumeOnlyProjection?.endSubmission(ref);
}

// isResumeOnly over the store's current model for ref: the ADMISSION
// predicate the press (liveControls) and enqueue (enqueueMutationIntent) paths
// share. Unlike isResumeOnly it reads the delivery-uncertain signal
// itself, from the pending-turns projection (hasBlockedUnknown), and the
// queued-non-send signal (hasQueuedNonSend), so a direct caller that bypassed
// the composer's own reads - the palette's slash fallthrough, the ask dock's
// batch send, a failed turn's Retry - still keeps the fence while blockedUnknown
// rows or a queued non-send row stand. It can also read the store's own
// in-flight Stop (stoppingRefs). Dispatch reads its sibling instead:
// resumeOnlyDispatchable omits the queued-non-send clause because at
// dispatch the named head record may itself be the send, with no non-send row
// ahead of it (currentDispatchClient).
export function resumeOnlyModel(ref: string): boolean {
  const model = threadsStore.getState().threads.get(ref);
  return model !== undefined && isResumeOnly(model, localFenceSignals(ref));
}

// The store's client-side fence signals, read once from the current state: the
// delivery-uncertain rows, the in-flight Stop, the queued non-send row, and the
// mutation-recency mark. resumeOnlyModel, sendResumesModel and
// sendDrivenModel all share this one literal.
function localFenceSignals(ref: string): ResumeOnlySignals {
  const state = threadsStore.getState();
  return {
    stopInFlight: state.stoppingRefs.has(ref),
    uncertainMessages: hasBlockedUnknown(ref),
    queuedNonSend: hasQueuedNonSend(ref),
    mutationStateAuthoritative: state.mutationAuthorityRefs.has(ref),
  };
}

// isSendResumes over the store's current model for ref: the ADMISSION
// predicate enqueueMutationIntent and the press (liveControls) share, read from
// the same store-wide signals resumeOnlyModel reads.
export function sendResumesModel(ref: string): boolean {
  const model = threadsStore.getState().threads.get(ref);
  return (
    model !== undefined &&
    isSendResumes(model, threadsStore.getState().restartBlockingObligations.has(ref), localFenceSignals(ref))
  );
}

// The two faces whose only recovery action is a Send: the merely-resumable fold
// and the Send-resumes face. The admission (enqueueMutationIntent) and the press
// (liveControls' pressRefusal) both admit turn/start for exactly this union, so
// they read this one helper rather than recombining the pair.
export function sendDrivenModel(ref: string): boolean {
  return resumeOnlyModel(ref) || sendResumesModel(ref);
}

// The DISPATCH reading of the resume-only carve-out, used by
// currentDispatchClient when it names the exact head record it is about to
// send. It is deliberately NOT resumeOnlyModel: the two callers ask
// genuinely different questions. resumeOnlyModel answers ADMISSION - "may
// a new send be minted behind this queue?" - where any queued non-send row is
// genuinely ahead of the candidate, so hasQueuedNonSend belongs. Dispatch asks
// "is this exact head record admissible?", and the head record is the next
// thing the wire sees: a queued non-send row BEHIND a head turn/start is not
// ahead of it, so it must not park the head. currentDispatchClient still
// exempts only turn/start (and the ref-less/pre-record calls), so the gate
// names a queued non-send method itself and parks it; this reading only stops
// a tail non-send row from refusing the head send. The delivery-uncertain and
// in-flight-Stop signals stay, because both fence the head record too.
export function resumeOnlyDispatchable(ref: string): boolean {
  const state = threadsStore.getState();
  const model = state.threads.get(ref);
  return (
    model !== undefined &&
    isResumeOnly(model, {
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
// shapes (sendDrivenModel): the merely-resumable fold
// (isResumeOnly), whose resume the hub folds into turn/start, and the
// Send-resumes face (isSendResumes), whose send parks and drives the
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

// DIRECT_FALLBACK_METHODS (the package's composer-verb map) holds the only
// mutations the direct fallback answers when storage fails their durable write
// (isStorageFailure): each has no response-side local commit beyond the wire
// call itself, so a plain RPC looks to the rest of the store exactly like the
// dispatched row would have. Every other durable write keeps its own contract
// and stays fail-closed - see the catch in enqueueMutationIntent.

// The one eligibility rule for a send that may take the direct path with no
// durable row: an ordinary enqueue of one of the four composer send verbs.
// turn/interrupt's cancel transaction and every non-send durable write keep
// their own contracts, so they never qualify.
function directSendEligible(method: string, durableWrite: "enqueue" | "interruptAndCancel"): boolean {
  return durableWrite === "enqueue" && DIRECT_FALLBACK_METHODS.has(method);
}

// The direct-dispatch admission for a composer send with no durable row behind
// it, shared by the two callers that may take that path: the storage-failure
// fallback in enqueueMutationIntent and its no-IndexedDB branch. It re-earns the
// admission the dispatcher would grant (currentDispatchClient, which re-checks
// the wired client and the ref's fences) and refuses to jump earlier durable
// work for the ref: undeliveredMutationIds holds the committed rows this tab has
// not proven delivered, and inflightDurableEnqueues counts durable enqueues
// whose write has not settled yet. Returns the client to dispatch against, or
// null when a fence or either ordering guard refuses it - the caller decides
// what a refusal raises.
function admitDirectSend(
  ref: string,
  intent: MutationIntent,
  options?: { storageBlockedAdmissible?: boolean },
): AppwireClientLike | null {
  const client = currentDispatchClient(ref, intent.method, false, options);
  if (client === null || hasUndeliveredMutation(ref) || (inflightDurableEnqueues.get(ref) ?? 0) > 0) return null;
  return client;
}

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
    isRecoveryFenced(admissionState.restartBlockingObligations.has(ref) || admissionState.stoppingRefs.has(ref)) &&
    // Two shapes admit turn/start at this funnel. A merely-resumable session
    // folds its resume into the send - the hub admits it (cmd/evener-hub's
    // sessionActionRecoveryError turn/start carve-out). The Send-resumes face
    // (isSendResumes) is the broad complement: its only recovery action
    // used to be the standalone Resume button, and a send now drives that
    // resume in the store (resumeFencedForSend) with the button's protections,
    // so its intent must be admitted to park rather than refused. turn/start on
    // a live Stop drain or a restartRequired daemon is neither; every other
    // fenced verb keeps the refusal unchanged.
    !(intent.method === "turn/start" && sendDrivenModel(ref))
  )
    throw new Error(fenceRefusal);
  const client = requireClient();
  if (client.state !== "ready") throw new Error(`threads store: cannot enqueue mutation while ${client.state}`);
  const runtime = getMutationRuntime();
  if (runtime === null) {
    // No IndexedDB at all: no durable row can be written, so the click-time
    // capture, the reconcile bookkeeping and the dispatcher do not exist either.
    // A composer send still goes out directly (see admitDirectSend); every other
    // durable write keeps its fail-closed contract. A ref the recovery fence
    // refuses to dispatch - a Send-resumes local session whose obligation still
    // stands, say - is refused here too, just as the storage-failure fallback
    // below refuses it.
    const dispatchClient = directSendEligible(intent.method, durableWrite) ? admitDirectSend(ref, intent) : null;
    if (dispatchClient === null) {
      throw new Error(INDEXEDDB_UNAVAILABLE);
    }
    await dispatchMutationDirectly(dispatchClient, intent);
    return undefined;
  }
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
    // A storage failure on any of those propagates exactly as every other
    // submission failure: the caller sees the refusal. This click's own write
    // has settled, so drop the ref's arm if it is now idle - see
    // disarmQuiescedMutationArm. (A Stop therefore lands here, not in the
    // fallback below, so its fail-closed decision lives with its invariant.)
    if (!directSendEligible(intent.method, durableWrite) || !isStorageFailure(error)) {
      disarmQuiescedMutationArm(ref);
      throw error;
    }
    // Storage failed a composer send's durable write - timed out or refused
    // outright (see isStorageFailure) - so send it as a plain
    // RPC right now, exactly as the non-durable operations do (setModel,
    // rename, compact, ...). The outbox row, the dispatcher, the
    // receipt/settle machinery and the recovery list are all skipped - there
    // is nothing durable to settle or replay.
    //
    // It must still re-earn the admission the dispatcher would grant, because
    // it is NOT the ref's durable FIFO head. currentDispatchClient is that
    // exact admission (the dispatcher asks it per ref and method), so re-run
    // it: it re-checks the wired client and readyEpoch - both may have changed
    // across the failed write - the client's ready state, and the ref's fences (a
    // pending reconciliation, a reconciliation failure, a Stop drain via
    // stoppingRefs, the restart obligation with turn/start's resume-only
    // carve-out, and restartRequired). requireArmed is off: the fallback's own
    // ordering guard is not the dispatcher's arm but the checks admitDirectSend
    // applies.
    // A reconcile the same storage wedge failed is admissible here and nowhere
    // else: storage-blocked is the one fence whose cause cannot be waited out
    // inside the wedge, so the fallback waives it (and the reconcile still
    // pending on that wedge) rather than strand the send. The pending record
    // is classified below when the fallback's own timeout observes the wedge,
    // so the waiver does not wait for the reconcile's own watchdog.
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
    // lives in storage, which just failed this send; these two are what the
    // in-memory state proves, and the fallback refuses on either rather than
    // reorder.
    // The storage that failed this write is the storage the reconcile reads,
    // so a reconcile still pending for the ref sits on the same failure:
    // classify it now rather than wait for it to reject. Without this, a send
    // whose fallback decision lands while the reconcile has not yet rejected
    // meets the pending fence and dies with the storage error - the exact
    // window the fallback exists to close. A reconcile that later succeeds
    // (a full origin's reads still answer) clears the record, so a read that
    // was healthy after all self-corrects.
    //
    // A full origin's QuotaExceededError refuses only writes, so its pending
    // reconcile may still finish and find an undelivered row this send then
    // jumps. That reorder is accepted on purpose (friction C13's per-ref
    // ordering gap). Keeping the pending fence for quota would fail the send
    // the person just made, in order to wait on a reconcile that usually finds
    // nothing; getting the send out wins.
    if (pendingMutationReconciliations.has(ref)) {
      threadsStore.setState((state) => ({
        mutationReconciliationStorageBlocked: new Set(state.mutationReconciliationStorageBlocked).add(ref),
      }));
    }
    const dispatchClient = admitDirectSend(ref, intent, { storageBlockedAdmissible: true });
    if (dispatchClient === null) {
      disarmQuiescedMutationArm(ref);
      throw error;
    }
    // The admission above proved the ref has no undelivered work and no enqueue
    // in flight, so it is idle: drop the arm - see disarmQuiescedMutationArm.
    disarmQuiescedMutationArm(ref);
    //
    // The forfeited cross-tab Stop fence: this send carries no click-time stop
    // epoch - whether the capture read failed, or the capture succeeded and only
    // the enqueue failed. The epoch is a commit-order comparison
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
// the retry's own baseline (the fence the capture exists to hold). Only a
// timeout is retried: a write the browser refused outright would be refused
// again. A rejecting
// capture, or a write that is not retried, propagates, and
// enqueueMutationIntent decides between the fallback and a hard failure.
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
      if (!isStorageTimeout(error) || attempt >= MUTATION_DURABLE_WRITE_ATTEMPTS) throw error;
      await new Promise((resolve) => setTimeout(resolve, MUTATION_DURABLE_WRITE_RETRY_DELAY_MS));
    }
  }
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

// Escalations this client resolved, per ref. A resolved escalation never
// returns, but the resolving client learns of it by its own acknowledgment,
// which the daemon's snapshot cut does not order: a read issued before the
// resolve, or one that starts while the approved tool is still rerunning, can
// carry the escalation. Every model this client builds from a read drops these.
const resolvedEscalationsByRef = new Map<string, Set<string>>();

function resolvedEscalationsFor(ref: string): Set<string> {
  let resolved = resolvedEscalationsByRef.get(ref);
  if (!resolved) {
    resolved = new Set();
    resolvedEscalationsByRef.set(ref, resolved);
  }
  return resolved;
}

// A live read issued after an escalation was resolved that no longer advertises
// it shows the daemon is done with it (it is dropped when the approved tool's
// rerun finishes), so that id can never come back and is forgotten. Ids resolved
// after the read was issued are kept: the read says nothing of them. Saved
// history is not a live snapshot and confirms nothing.
//
// Reads are answered out of order and adopted after they return, so a confirmed
// id is forgotten only while no read for the ref is in flight and no hydration
// is still adopting a response; each response is also stripped of resolved ids
// on arrival. The Set is trimmed in place; the transcript repository holds this
// same Set.
const threadReadsByRef = new Map<string, { inFlight: number; confirmed: Set<string> }>();

function forgetConfirmedEscalations(ref: string): void {
  const reads = threadReadsByRef.get(ref);
  if (!reads || reads.inFlight > 0 || pendingThreadHydrations.has(ref) || pendingWatchedHydrations.has(ref)) return;
  for (const escalationId of reads.confirmed) resolvedEscalationsByRef.get(ref)?.delete(escalationId);
  threadReadsByRef.delete(ref);
}

function readThreadForgettingConfirmed(
  ref: string,
  lease: ThreadSubscriptionLease,
  params: Parameters<ThreadSubscriptionLease["read"]>[0],
): ReturnType<ThreadSubscriptionLease["read"]> {
  const resolvedAtIssue = [...(resolvedEscalationsByRef.get(ref) ?? [])];
  const reads = threadReadsByRef.get(ref) ?? { inFlight: 0, confirmed: new Set<string>() };
  threadReadsByRef.set(ref, reads);
  reads.inFlight += 1;
  const settle = () => {
    reads.inFlight -= 1;
    forgetConfirmedEscalations(ref);
  };
  return lease.read(params).then(
    (response) => {
      const evener = response.thread.evener;
      const advertised = evener?.pendingEscalations ?? [];
      if (evener?.mutationStateAuthoritative === true) {
        const advertisedIds = new Set(advertised.map((e) => e.escalationId));
        for (const escalationId of resolvedAtIssue)
          if (!advertisedIds.has(escalationId)) reads.confirmed.add(escalationId);
      }
      const resolved = resolvedEscalationsByRef.get(ref);
      const kept = resolved ? advertised.filter((e) => !resolved.has(e.escalationId)) : advertised;
      settle();
      if (!evener || kept.length === advertised.length) return response;
      return { ...response, thread: { ...response.thread, evener: { ...evener, pendingEscalations: kept } } };
    },
    (error) => {
      settle();
      throw error;
    },
  );
}

function withoutResolvedEscalations(ref: string, pending: PendingThreadHydration, model: ThreadModel): ThreadModel {
  // A discarded response is the base model itself (isDiscardedReadResult).
  if (model === pending.baseModel) return model;
  let kept = model;
  for (const escalationId of resolvedEscalationsByRef.get(ref) ?? []) {
    kept = resolvePendingEscalation(kept, escalationId);
  }
  return kept;
}

function applyHydrationResponseCut(pending: PendingThreadHydration, ref: string, model: ThreadModel): void {
  // AppWire orders the matching response at the authoritative snapshot cut.
  // Every notification already buffered is at or before that cut and is
  // already represented by this model, except a resolved escalation (see
  // withoutResolvedEscalations). Notifications delivered after the
  // response enter the buffer later and remain ordered for replay.
  releasePendingHydrationBytes(ref, pending);
  pending.notifications = [];
  pending.routing = pendingHydrationRouting(ref, model);
}

function releasePendingHydrationBytes(ref: string, pending: PendingThreadHydration): void {
  if (pending.backgroundBytes <= 0) return;
  residencyIfCreated?.releasePending(ref, pending.backgroundBytes);
  pending.backgroundBytes = 0;
}

type PendingHydrationRetirementForTests = (retirement: {
  ref: string;
  retainedNotificationCount: number;
  hadBaseModel: boolean;
  notificationCount: number;
  hasBaseModel: boolean;
}) => void;

let pendingHydrationRetirementForTests: PendingHydrationRetirementForTests | undefined;

export function observePendingHydrationRetirementsForTests(
  observer: PendingHydrationRetirementForTests | undefined,
): () => void {
  const previous = pendingHydrationRetirementForTests;
  pendingHydrationRetirementForTests = observer;
  return () => {
    pendingHydrationRetirementForTests = previous;
  };
}

function retirePendingHydration(
  ref: string,
  pending: PendingThreadHydration,
  pendingHydrations: Map<string, PendingThreadHydration>,
): void {
  const isCurrent = pendingHydrations.get(ref) === pending;
  if (!isCurrent && pending.backgroundBytes <= 0 && pending.notifications.length === 0 && !pending.baseModel) return;
  const retainedNotificationCount = pending.notifications.length;
  const hadBaseModel = pending.baseModel !== undefined;
  pending.notifications = [];
  pending.baseModel = undefined;
  releasePendingHydrationBytes(ref, pending);
  if (isCurrent) pendingHydrations.delete(ref);
  forgetConfirmedEscalations(ref);
  pendingHydrationRetirementForTests?.({
    ref,
    retainedNotificationCount,
    hadBaseModel,
    notificationCount: pending.notifications.length,
    hasBaseModel: pending.baseModel !== undefined,
  });
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
  entryAcceptedRefs?: ReadonlySet<string>,
): void {
  const ref = notificationRef(n);
  for (const [pendingRef, pending] of pendingHydrations) {
    if (targetsPendingHydration(n, pending)) {
      const entry = entryAcceptedRefs?.has(pendingRef) ? entryTranscriptRoute(pendingRef) : undefined;
      bufferPendingNotification(
        pendingRef,
        pending,
        n,
        pendingHydrations,
        entry?.repository ? { repository: entry.repository, lifetime: entry.lifetime } : undefined,
      );
      // Admission may retire this read. It no longer retains the frame or
      // owns a future publication, so it must not withhold the store fold.
      if (pendingHydrations.get(pendingRef) !== pending) continue;
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

// A live foreground/history/pin holder. Background membership is not one: it
// must never be what lifts a clear's suppression.
function heldCountsAtStore(ref: string): boolean {
  return (refCounts.get(ref) ?? 0) > 0 || (historyHoldCounts.get(ref) ?? 0) > 0 || pinnedMutationRefs.has(ref);
}

// The store-level mirror of the residency's clear lifetime record, driven from
// the same owner-change call sites (syncTranscriptResidencyOwners). Once the
// pre-clear holders release, the next explicit foreground acquisition is fresh;
// background churn and reconnect never make one.
function syncCacheLifetimeForeground(ref: string): void {
  const nowHeld = heldCountsAtStore(ref);
  const wasHeld = cacheLifetimeHeld.get(ref) ?? false;
  cacheLifetimeHeld.set(ref, nowHeld);
  if (wasHeld === nowHeld) return;
  const lifetime = threadsStore.getState().cacheLifetimes.get(ref);
  if (!lifetime?.clearSuppressed) return;
  if (wasHeld && !nowHeld) {
    // The last pre-clear holder released: a later acquisition may be fresh.
    if (lifetime.suppressionHoldsPreClearHolders) {
      threadsStore.setState((state) => {
        const current = state.cacheLifetimes.get(ref);
        if (!current?.suppressionHoldsPreClearHolders) return state;
        const cacheLifetimes = new Map(state.cacheLifetimes);
        cacheLifetimes.set(ref, { ...current, suppressionHoldsPreClearHolders: undefined });
        return { cacheLifetimes };
      });
    }
  }
}

// Retention alone grants no permission: automatic outbox pins and history
// work can hold a ref after Clear without starting a writable cache lifetime.
function acquireCacheLifetimeForeground(ref: string): void {
  threadsStore.setState((state) => {
    const current = state.cacheLifetimes.get(ref);
    if (!current?.clearSuppressed || current.suppressionHoldsPreClearHolders) return state;
    if (current.freshForegroundLifetime) return state;
    const cacheLifetimes = new Map(state.cacheLifetimes);
    cacheLifetimes.set(ref, { ...current, freshForegroundLifetime: true });
    return { cacheLifetimes };
  });
}

// An authoritative hydration ends the cached-shell window. A clear suppression
// is lifted only for a qualifying fresh explicit foreground lifetime; otherwise
// the suppression stands (deletion suppression is never lifted here because it
// carries no clearSuppressed marker).
function settleCacheLifetimeAfterHydration(ref: string): void {
  threadsStore.setState((state) => {
    const lifetime = state.cacheLifetimes.get(ref);
    if (lifetime?.clearSuppressed && lifetime.freshForegroundLifetime && heldCountsAtStore(ref)) {
      const cacheLifetimes = new Map(state.cacheLifetimes);
      cacheLifetimes.set(ref, { leaseEpoch: tabCacheEpoch ?? 0 });
      return { cacheLifetimes };
    }
    const cacheLifetimes = releaseCacheShell(state.cacheLifetimes, ref);
    return cacheLifetimes === state.cacheLifetimes ? state : { cacheLifetimes };
  });
}

// A clear-suppressed ref whose pre-clear holders have released and whose new
// explicit foreground lifetime has begun: it must reconcile authoritatively
// even if a resident display model still exists. The ordinary healthy resident
// path is untouched (no clear suppression, no fresh lifetime).
function needsClearLifetimeReconciliation(ref: string): boolean {
  const lifetime = threadsStore.getState().cacheLifetimes.get(ref);
  return lifetime?.clearSuppressed === true && lifetime.freshForegroundLifetime === true && heldCountsAtStore(ref);
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
  const previousPending = pendingThreadHydrations.get(ref);
  if (previousPending) retirePendingHydration(ref, previousPending, pendingThreadHydrations);
  const readSequence = (threadReadSequences.get(ref) ?? 0) + 1;
  threadReadSequences.set(ref, readSequence);
  const { baseModel, requestGeneration } = issuedGenerationFor(model);
  if (baseModel && baseModel !== model) putThreadModel(ref, baseModel);
  const entryRoute = entryTranscriptRoute(ref);
  const pending = {
    client,
    epoch,
    notifications: [],
    backgroundBytes: 0,
    routing: pendingHydrationRouting(ref, baseModel ?? model),
    attempt,
    baseModel,
    requestGeneration,
    entryRetirement: entryRouteRetirements.get(ref) ?? 0,
    entryHistoryKey: entryRoute?.repository ? entryRoute.historyKey : undefined,
    entryHistoryGeneration: entryRoute?.repository ? (entryRoute.historyGeneration ?? 0) : undefined,
    readSequence,
  };
  pendingThreadHydrations.set(ref, pending);
  return pending;
}

function beginWatchedHydration(
  ref: string,
  client: AppwireClientLike,
  model: ThreadModel | undefined,
  epoch: number,
  includeTurns: boolean,
): PendingThreadHydration {
  const previousPending = pendingWatchedHydrations.get(ref);
  if (previousPending) retirePendingHydration(ref, previousPending, pendingWatchedHydrations);
  // Only a history-requesting read orders the repair-status sequence: a
  // lean watch the hub can answer without touching the transcript index
  // must not advance the shared counter out from under a tracked read's
  // still-pending publication — the fence would drop the tracked read's
  // own verdict. The lean read carries the current value instead, which
  // satisfies the fence for the one verdict it did observe itself: its
  // catch-path rebuilding answer.
  let readSequence: number;
  if (includeTurns) {
    readSequence = (threadReadSequences.get(ref) ?? 0) + 1;
    threadReadSequences.set(ref, readSequence);
  } else {
    readSequence = threadReadSequences.get(ref) ?? 0;
  }
  const { baseModel, requestGeneration } = issuedGenerationFor(model);
  if (baseModel && baseModel !== model) putWatchedThreadModel(ref, baseModel);
  const entryRoute = entryTranscriptRoute(ref);
  const pending = {
    client,
    epoch,
    notifications: [],
    backgroundBytes: 0,
    routing: pendingHydrationRouting(ref, baseModel ?? model),
    baseModel,
    requestGeneration,
    entryHistoryKey: entryRoute?.repository ? entryRoute.historyKey : undefined,
    entryHistoryGeneration: entryRoute?.repository ? (entryRoute.historyGeneration ?? 0) : undefined,
    readSequence,
  };
  pendingWatchedHydrations.set(ref, pending);
  return pending;
}

function bufferPendingNotification(
  ref: string,
  pending: PendingThreadHydration,
  notification: AnyNotification,
  pendingHydrations: Map<string, PendingThreadHydration>,
  acceptedEntry?: PendingTranscriptUpdate["acceptedEntry"],
): void {
  const sequence = (currentAppliedSequences.get(ref) ?? 0) + 1;
  const update: PendingTranscriptUpdate = {
    sequence,
    now: Date.now(),
    notification,
    bytes: encodedPayloadBytes(notification),
    ...(acceptedEntry ? { acceptedEntry } : {}),
  };
  if ((backgroundRefCounts.get(ref) ?? 0) > 0) {
    if (!transcriptResidency().reservePending(ref, update.bytes)) {
      // An established repository already owns accepted input, so only its
      // incomplete replay copy is retired. Initial hydration has no such
      // owner: arm paced replacement while the pending claim still exists.
      // Neither path frees the issued request's slot before it settles.
      if (acceptedEntry) retirePendingHydration(ref, pending, pendingHydrations);
      else
        scheduleOwnedHydrationRetry(
          pendingHydrations === pendingWatchedHydrations ? "watched" : "thread",
          ref,
          pending,
        );
      return;
    }
    pending.backgroundBytes += update.bytes;
  }
  currentAppliedSequences.set(ref, sequence);
  pending.notifications.push(update);
}

function replayHydrationNotifications(
  model: ThreadModel,
  notifications: ReceivedTranscriptUpdate[],
): { model: ThreadModel; appliedAt: number[] } {
  let hydrated = model;
  const appliedAt: number[] = [];
  for (const update of notifications) {
    const folded = foldCanonicalNotification(hydrated, update.notification, update.now);
    const updated = folded.model;
    if (updated === hydrated) continue;
    hydrated = updated;
    appliedAt.push(update.now);
  }
  return { model: hydrated, appliedAt };
}

function replayEntryHydrationNotifications(
  repository: TranscriptRepository,
  lifetime: number,
  model: ThreadModel,
  notifications: PendingTranscriptUpdate[],
  adoptedRead: boolean,
): { model: ThreadModel; appliedAt: number[] } {
  let hydrated = model;
  const appliedAt: number[] = [];
  for (const update of notifications) {
    const acceptedLive = update.acceptedEntry?.repository === repository && update.acceptedEntry.lifetime === lifetime;
    // A discarded read left this owner's live model intact. Only a read that
    // replaced it needs frames this same repository already accepted replayed.
    if (!adoptedRead && acceptedLive) continue;
    // The repository owns both recorded ingestion and later publications, so
    // the post-cut tail must join that owner, not a temporary store-only fold.
    const accepted = repository.accept(update.notification, { lifetime });
    // Refused recorded input can still invalidate authority. Preserve that
    // transition without counting the refused frame as accepted content.
    hydrated = repository.getSnapshot() ?? hydrated;
    if (!accepted && !acceptedLive) continue;
    // The live path already counted this physical arrival. Replay banks only
    // frames first accepted here, regardless of whether it accepts again.
    if (!update.acceptedEntry) appliedAt.push(update.now);
  }
  return { model: hydrated, appliedAt };
}

// A snapshot may publish only for the client and ready generation that own
// its hydration. Retired connections cannot overwrite replacement state.
function publishThreadHydration(
  ref: string,
  pending: PendingThreadHydration,
  model: ThreadModel,
  adoptedEntryRead: boolean,
): ThreadModel | null {
  if (pendingThreadHydrations.get(ref) !== pending) return null;
  if (readyEpoch !== pending.epoch || wiredClient !== pending.client) return null;
  if (pending.backgroundInvalidated) {
    retirePendingHydration(ref, pending, pendingThreadHydrations);
    return null;
  }
  if (!canonicalThreadOwnerActive(ref)) {
    retirePendingHydration(ref, pending, pendingThreadHydrations);
    return null;
  }

  const live = threadsStore.getState().threads.get(ref);
  const entry = productionEntryRoutes.get(ref);
  const repositoryModel = entry?.repository?.getSnapshot();
  const { model: hydrated, appliedAt } =
    entry?.repository && repositoryModel
      ? replayEntryHydrationNotifications(
          entry.repository,
          entry.lifetime,
          repositoryModel,
          pending.notifications,
          adoptedEntryRead,
        )
      : replayHydrationNotifications(preserveLiveActiveTurn(model, live), pending.notifications);
  const backgroundModel = isEntryTranscriptBackgroundOnly(ref) ? pruneBackgroundHistoryBodies(hydrated) : hydrated;
  const replayInvalidated =
    entry?.repository &&
    hydrated.history?.invalidatedAtGeneration !== undefined &&
    pending.notifications.some((update) => update.notification.method === "history/updated");
  retirePendingHydration(ref, pending, pendingThreadHydrations);
  if (replayInvalidated) {
    // Live recovery was gated by this pending read. Retire it before handing
    // the invalidated owner a replacement, and do not settle stale authority.
    putThreadModel(ref, backgroundModel);
    void refreshTrackedThread(
      pending.client,
      pending.epoch,
      ref,
      true,
      false,
      undefined,
      undefined,
      hydrationIntentFor(ref),
    ).catch(() => {});
    return null;
  }
  currentAdmissions.set(ref, {
    capabilities: hydrated.capabilities,
    capabilitySource: hydrated.capabilitySource,
    instanceId: hydrated.instanceId,
    resumeRequired: hydrated.resumeRequired,
    resumeOnlyFoldable: hydrated.resumeOnlyFoldable,
  });
  currentAppliedSequences.set(ref, currentAppliedSequences.get(ref) ?? 0);
  putThreadModel(ref, backgroundModel);
  setCurrentThreadId(ref, backgroundModel.threadId);
  // The first authoritative publish ends the cached-shell window: the flag
  // and the gap rule's anchor live only from the shell's arming until this
  // read's publish (spec, "The load seam"); the lease outlives
  // the window — until the ref's final release — for the clear's suppression
  // (Task 10).
  settleCacheLifetimeAfterHydration(ref);
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
  settleOwnedHydration("thread", ref, backgroundModel);
  return backgroundModel;
}

async function publishAndReconcileThreadHydration(
  ref: string,
  pending: PendingThreadHydration,
  hydration: ThreadHydration,
  // A background note retry proves absence only for the record it carries and
  // must not reopen an unrelated blocked row of the same ref (#1716); every
  // other caller leaves this undefined for the ordinary target-wide reopen.
  reopenOnlyClientMutationId?: string,
  intent: HydrationIntent = "foreground",
): Promise<ThreadModel | null> {
  if (pendingThreadHydrations.get(ref) !== pending || !canonicalThreadOwnerActive(ref)) return null;
  const adoptedEntryRead = (await acceptEntryTranscriptHydration(ref, pending, hydration)) === true;
  const published = publishThreadHydration(ref, pending, hydration.model, adoptedEntryRead);
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
  if (intent === "background" || intent === "final") return published;
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
        // A storage failure is not a mutation-state fact: the same storage
        // that fails the durable write failed this read, so record
        // the ref aside as storage-blocked - the slice the send fallback may
        // admit - and let discovery retry the read on every pass. A genuine
        // failure recorded earlier (a real reconcile conflict, a blocked
        // shared record) must survive it: erasing it would unfence the
        // fallback through the waivable slice, so a storage failure only adds
        // the storage-blocked record. Any other failure keeps its old
        // meaning and supersedes the storage-blocked state, being the
        // stricter fence: record it in failures and clear the storage-blocked
        // record. A successful reconcile clears both.
        const storageBlocked = isStorageFailure(error);
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
  retirePendingHydration(ref, pending, pendingWatchedHydrations);
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
  previousModels: ReadonlyMap<string, ThreadModel> = map,
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
      const folded = foldCanonicalNotification(model, n, now, previousModels.get(model.ref));
      const updated = folded.model;
      if (updated === model) continue;
      next ??= new Map(map);
      next.set(model.ref, updated);
      if (folded.endedResumeOnly) endedResumeOnlyRefs.push(model.ref);
      changedRefs.push(model.ref);
    }
    return { next, changedRefs, acceptedRefs, endedResumeOnlyRefs };
  }
  accept(routed);
  const folded = foldCanonicalNotification(routed, n, now, previousModels.get(routed.ref));
  const updated = folded.model;
  if (updated !== routed) {
    next = new Map(map);
    next.set(routed.ref, updated);
    if (folded.endedResumeOnly) endedResumeOnlyRefs.push(routed.ref);
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

function foldCanonicalNotification(
  model: ThreadModel,
  notification: AnyNotification,
  now: number,
  previous: ThreadModel = model,
): { model: ThreadModel; endedResumeOnly: boolean } {
  const endedRefs: string[] = [];
  const updated = settleResumeOnlyOffShutdown(previous, applyNotification(model, notification, now), endedRefs);
  return { model: updated, endedResumeOnly: endedRefs.includes(model.ref) };
}

// Every suppressed frame shares the ordinary pending-byte budget. Overflow
// drops the owner's frames and retains only a recovery obligation, never a
// closure whose captured notification can keep an unaccounted body alive.
interface ClearReplayOwner {
  fixture: EntryTranscriptRoute;
  repository: TranscriptRepository;
  lifetime: number;
  client: AppwireClientLike | null;
  epoch: number;
  frames: Array<{ notification: AnyNotification; bytes: number; fieldGeneration?: number }>;
  overflow: boolean;
}
const pendingClearReplay = new Map<EntryTranscriptRoute, ClearReplayOwner>();

function releaseClearReplayFrames(owner: ClearReplayOwner): void {
  entryWriteAdmissionFor(owner.fixture.cache).release(
    owner.frames.map(({ notification, bytes }) => ({ key: notification, bytes })),
    owner,
  );
  owner.frames = [];
}

function discardPendingClearReplay(): void {
  for (const owner of pendingClearReplay.values()) releaseClearReplayFrames(owner);
  pendingClearReplay.clear();
}

function retainClearReplay(
  fixture: EntryTranscriptRoute,
  repository: TranscriptRepository,
  notification: AnyNotification,
): void {
  let owner = pendingClearReplay.get(fixture);
  if (!owner) {
    owner = {
      fixture,
      repository,
      lifetime: fixture.lifetime,
      client: wiredClient,
      epoch: readyEpoch,
      frames: [],
      overflow: false,
    };
    pendingClearReplay.set(fixture, owner);
  }
  if (owner.overflow) return;
  const bytes = encodedPayloadBytes(notification);
  if (!entryWriteAdmissionFor(fixture.cache).retain([{ key: notification, bytes }], owner, "background")) {
    releaseClearReplayFrames(owner);
    owner.overflow = true;
    return;
  }
  owner.frames.push({ notification, bytes, fieldGeneration: clearReplayFieldGeneration(fixture.ref, notification) });
}

function clearReplayFieldGeneration(ref: string, notification: AnyNotification): number | undefined {
  if (notification.method === "evener/goal/updated") return goalUpdateGenerations.get(ref) ?? 0;
  if (notification.method === "evener/notes/updated") return notesUpdateGenerations.get(ref) ?? 0;
  return undefined;
}

function currentClearReplayOwner(owner: ClearReplayOwner): boolean {
  const { fixture, repository, lifetime, client, epoch } = owner;
  const ref = fixture.ref;
  return (
    entryTranscriptRoute(ref) === fixture &&
    fixture.repository === repository &&
    fixture.lifetime === lifetime &&
    !fixture.clearInert &&
    client === wiredClient &&
    epoch === readyEpoch &&
    canonicalThreadOwnerActive(ref) &&
    !threadsStore.getState().deletedRefs.has(ref) &&
    !threadsStore.getState().cacheLifetimes.get(ref)?.suppressed
  );
}

function replayPendingClearReplay(): void {
  const replay = [...pendingClearReplay.values()];
  pendingClearReplay.clear();
  for (const owner of replay) {
    if (!currentClearReplayOwner(owner)) {
      releaseClearReplayFrames(owner);
      continue;
    }
    if (owner.overflow) {
      const ref = owner.fixture.ref;
      owner.repository.beginFreshBaseline();
      publishEntryTranscriptModel(ref);
      if (owner.client?.state === "ready") {
        void refreshTrackedThread(
          owner.client,
          owner.epoch,
          ref,
          true,
          false,
          undefined,
          undefined,
          hydrationIntentFor(ref),
        ).catch(() => {});
      }
      continue;
    }
    // Release one frame immediately before passing it to repository admission.
    // The rest remain charged until their own replay or retirement.
    for (const frame of owner.frames) {
      entryWriteAdmissionFor(owner.fixture.cache).release([{ key: frame.notification, bytes: frame.bytes }], owner);
      if (
        currentClearReplayOwner(owner) &&
        frame.fieldGeneration === clearReplayFieldGeneration(owner.fixture.ref, frame.notification)
      )
        acceptEntryNotification(owner.fixture, owner.repository, frame.notification);
    }
    owner.frames = [];
  }
}

function acceptEntryNotification(
  fixture: EntryTranscriptRoute,
  repository: TranscriptRepository,
  n: AnyNotification,
): boolean {
  const ref = fixture.ref;
  const beforeInvalidation = repository.getSnapshot()?.history?.invalidatedAtGeneration;
  const accepted = repository.accept(n, { lifetime: fixture.lifetime });
  if (accepted) {
    if (!fixture.unsubscribeRepository) publishEntryTranscriptModel(ref);
  }
  const invalidation = repository.getSnapshot()?.history?.invalidatedAtGeneration;
  if (
    productionEntryRoutes.get(ref) === fixture &&
    invalidation !== undefined &&
    invalidation !== beforeInvalidation &&
    !pendingThreadHydrations.has(ref) &&
    canonicalThreadOwnerActive(ref) &&
    wiredClient?.state === "ready"
  ) {
    // The repository already published this transition. The later legacy
    // fold sees no diff, so the entry owner must wake its own recovery.
    void refreshTrackedThread(
      wiredClient,
      readyEpoch,
      ref,
      true,
      false,
      undefined,
      undefined,
      hydrationIntentFor(ref),
    ).catch(() => {});
  }
  return accepted;
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
  const route = notificationRoutingKey(n);
  const entryAcceptedRefs = new Set<string>();
  const clearReplayRoutes: Array<{ fixture: EntryTranscriptRoute; repository: TranscriptRepository }> = [];
  // Synchronous repository publication can replace the store model before its
  // transition fold runs. Keep the actual pre-accept model for state-dependent
  // settlement, while folding content from the repository's current model.
  const previousThreads = threadsStore.getState().threads;
  if (route) {
    const refs = "ref" in route ? [route.ref] : [...(currentRefsByThreadId.get(route.threadId) ?? [])];
    for (const ref of refs) {
      const fixture = entryTranscriptRoute(ref);
      if (fixture && threadsStore.getState().clearInFlight !== undefined && canonicalThreadOwnerActive(ref)) {
        fixture.clearRefreshGeneration = hydrationOwnerGeneration("thread", ref);
      }
      if (fixture?.ref === ref && fixture.repository && threadsStore.getState().clearInFlight === undefined) {
        if (acceptEntryNotification(fixture, fixture.repository, n)) entryAcceptedRefs.add(ref);
      } else if (fixture?.ref === ref && fixture.repository && threadsStore.getState().clearInFlight !== undefined) {
        // Preserve metadata in the same order as recorded and overlay input.
        // Otherwise content replay can publish the repository's stale status
        // over metadata the ordinary store accepted during suppression.
        clearReplayRoutes.push({ fixture, repository: fixture.repository });
      } else if (
        fixture &&
        productionEntryRoutes.get(ref) === fixture &&
        !fixture.clearInert &&
        (n.method === "history/updated" || n.method.startsWith("overlay/")) &&
        threadsStore.getState().threads.get(ref)?.threadId === notificationThreadId(n) &&
        !threadsStore.getState().deletedRefs.has(ref) &&
        !threadsStore.getState().cacheLifetimes.get(ref)?.suppressed &&
        !pendingThreadHydrations.has(ref) &&
        canonicalThreadOwnerActive(ref) &&
        wiredClient?.state === "ready"
      ) {
        // A content clear leaves a bare new thread and retires the old cache
        // binding. Recorded and overlay signals both need a replacement owner.
        // Pending hydration below retains the signal for repository replay,
        // rather than creating a second store-owned ingestion path.
        void refreshTrackedThread(
          wiredClient,
          readyEpoch,
          ref,
          true,
          false,
          undefined,
          undefined,
          hydrationIntentFor(ref),
        ).catch(() => {});
      }
    }
  }
  const { threads, frameTimes, watchedThreads } = threadsStore.getState();
  if (route) {
    const refs = "ref" in route ? [route.ref] : [...(currentRefsByThreadId.get(route.threadId) ?? [])];
    for (const ref of refs) {
      const pending = pendingThreadHydrations.get(ref);
      if (pending && targetsPendingHydration(n, pending)) continue;
      const sequence = (currentAppliedSequences.get(ref) ?? 0) + 1;
      currentAppliedSequences.set(ref, sequence);
    }
  }
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
    collectPendingRefs(pendingThreadHydrations, n, refs, acceptedPendingRefs, entryAcceptedRefs);
    if (refs.size > 0) pendingRefs = refs;
  }
  let pendingWatchedRefs: ReadonlySet<string> = EMPTY_PENDING_REFS;
  if (pendingWatchedHydrations.size > 0) {
    const refs = new Set<string>();
    collectPendingRefs(pendingWatchedHydrations, n, refs, acceptedPendingRefs);
    if (refs.size > 0) pendingWatchedRefs = refs;
  }
  // The selected entry fixture's repository is the single owner of its
  // recorded ingestion. `history/updated` is the only recorded-history
  // notification (the repository's own notificationRows/notificationTurns
  // accept no other method), and the frame has already been offered to that
  // repository above. Folding it into the store as well would make the store a
  // second owner able to publish recorded content the repository refused.
  // Overlay is repository-owned too: the ordinary end reducer would overwrite
  // the repository's uncovered-slot handoff. Other routes retain their existing
  // overlay semantics, and status/session metadata keep their ordinary fold.
  const fixtureRecordedRefs: ReadonlySet<string> =
    route && (n.method === "history/updated" || n.method.startsWith("overlay/"))
      ? new Set("ref" in route ? [route.ref] : [...(currentRefsByThreadId.get(route.threadId) ?? [])])
      : EMPTY_PENDING_REFS;
  const {
    next: reducedThreads,
    changedRefs: changedThreads,
    acceptedRefs: acceptedThreads,
    endedResumeOnlyRefs: endedThreadsResumeOnly,
  } = applyToMap(threads, threadsIndex, n, now, new Set([...pendingRefs, ...fixtureRecordedRefs]), previousThreads);
  const {
    next: reducedWatchedThreads,
    changedRefs: changedWatchedThreads,
    acceptedRefs: acceptedWatchedThreads,
    endedResumeOnlyRefs: endedWatchedResumeOnly,
  } = applyToMap(watchedThreads, watchedThreadsIndex, n, now, pendingWatchedRefs);
  let nextThreads = reducedThreads;
  if (nextThreads) {
    let projected: Map<string, ThreadModel> | undefined;
    for (const ref of changedThreads) {
      const model = nextThreads.get(ref);
      if (!model || !isEntryTranscriptBackgroundOnly(ref)) continue;
      projected ??= new Map(nextThreads);
      projected.set(ref, pruneBackgroundHistoryBodies(model));
    }
    if (projected) nextThreads = projected;
  }
  let nextWatchedThreads = reducedWatchedThreads;
  if (nextWatchedThreads) {
    let projected: Map<string, ThreadModel> | undefined;
    for (const ref of changedWatchedThreads) {
      const model = nextWatchedThreads.get(ref);
      if (!model || watchIncludeTurns.get(ref)) continue;
      projected ??= new Map(nextWatchedThreads);
      projected.set(ref, pruneBackgroundHistoryBodies(model));
    }
    if (projected) nextWatchedThreads = projected;
  }
  const publishableThreads = nextThreads;
  const publishableChangedThreads = changedThreads;
  const publishableEndedResumeOnly = endedThreadsResumeOnly;
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
  // Capture after this notification has advanced its field generation. Later
  // response-only mutations or pushes may supersede it before Clear aborts.
  for (const { fixture, repository } of clearReplayRoutes) retainClearReplay(fixture, repository, n);
  // Metadata may already be published by the repository's synchronous accept
  // subscription, leaving no diff for the store fold. Account those accepted
  // refs too, once each even when the store also publishes the same frame.
  // This includes recorded and overlay input whose store ingestion is skipped.
  // Acceptance banks now, independently of replay-copy admission or retirement.
  // A retained receipt prevents tail replay from counting the same arrival again.
  const acceptedFrameRefs = new Set(publishableChangedThreads);
  for (const ref of entryAcceptedRefs) acceptedFrameRefs.add(ref);
  if (!nextThreads && !nextWatchedThreads && acceptedFrameRefs.size === 0) return;

  // history.invalidatedAtGeneration becoming set - or moving to a newer
  // signal, on re-invalidation - is the store's cue to issue a fresh
  // latest-window read (task-15-report.md's store contract). It covers
  // evener/thread/resync, and equally a history/updated or backfill page that
  // names another boot generation, a newer epoch, or an unseen incarnation
  // (reducer.ts's classifySignal): the reducer marks the thread invalid,
  // whatever notification carried the signal, and this is the one place that
  // reacts to it, so a re-read is never missed nor duplicated per method.
  const invalidatedThreadRefs = newlyInvalidatedHistoryRefs(threads, publishableThreads, publishableChangedThreads);
  const invalidatedWatchedRefs = newlyInvalidatedHistoryRefs(watchedThreads, nextWatchedThreads, changedWatchedThreads);

  const patch: Partial<ThreadsStoreState> = {};
  if (publishableThreads) {
    patch.threads = publishableThreads;
  }
  if (publishableThreads || acceptedFrameRefs.size > 0) {
    const nextFrameTimes = new Map(frameTimes);
    for (const ref of acceptedFrameRefs) nextFrameTimes.set(ref, appendFrameTime(frameTimes.get(ref) ?? [], now));
    patch.frameTimes = nextFrameTimes;
  }
  if (nextWatchedThreads) {
    patch.watchedThreads = nextWatchedThreads;
  }
  threadsStore.setState(patch);
  // Every accepted model was accounted before the synchronous subscriber call.

  // A status frame that moved a ref off the shut-down set ends the
  // merely-resumable window: the recovery obligation the same shut-down
  // snapshot armed is stale now, and leaving it armed would fence a session the
  // hub has resumed (stillFenced keeps Send and Queue off until a fresh read
  // clears it). Clear it here, in the same step the model's stale
  // resumeOnlyFoldable bit was cleared, so the composer routes normally. The
  // fold withholds a ref a Stop this page started still owns (see
  // settleResumeOnlyOffShutdown), so every reported ref is this clear's to make.
  if (publishableEndedResumeOnly.length > 0 || endedWatchedResumeOnly.length > 0) {
    const endedRefs = new Set([...publishableEndedResumeOnly, ...endedWatchedResumeOnly]);
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
      for (const ref of invalidatedThreadRefs)
        void refreshTrackedThread(client, epoch, ref, true, false, undefined, undefined, hydrationIntentFor(ref));
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
  putWatchedThreadModel(ref, !includeTurns ? pruneBackgroundHistoryBodies(model) : model);
}

function ownedHydrationsFor(kind: HydrationOwnerKind): Map<string, OwnedHydration> {
  return kind === "watched" ? ownedWatchedHydrations : ownedThreadHydrations;
}

// A ref is owned while a pane holds a claim, a watcher holds a claim, or a
// durable mutation record pins it. Ownership is what makes convergence this
// store's job at all; with none left there is nothing to converge for.
function hydrationOwnerActive(kind: HydrationOwnerKind, ref: string): boolean {
  if (kind === "watched") return (watchRefCounts.get(ref) ?? 0) > 0;
  return canonicalThreadOwnerActive(ref);
}

function hydrationOwnerGeneration(kind: HydrationOwnerKind, ref: string): number {
  return kind === "watched" ? (watchGenerations.get(ref) ?? 0) : (canonicalThreadOwnerGenerations.get(ref) ?? 0);
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
  retirePendingHydration(ref, pending, pendingHydrations);
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
  await refreshTrackedThread(client, epoch, ref, true, false, undefined, undefined, hydrationIntentFor(ref));
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
  intent: HydrationIntent = "foreground",
  registerCompletion = true,
): Promise<number | undefined> {
  // The scheduler can join an existing repository end read. Apply the same
  // owner fence as refreshTrackedThreadNow before joining, so a released ref
  // cannot carry that retired read's rejection into the reconnect callback.
  if (!canonicalThreadOwnerActive(ref)) return undefined;
  if (intent !== "background")
    return refreshTrackedThreadNow(
      client,
      epoch,
      ref,
      targetedResync,
      reportFailure,
      beforePublish,
      reopenOnlyClientMutationId,
      intent,
      undefined,
      registerCompletion,
    );
  let attempt: number | undefined;
  const residency = transcriptResidency();
  residency.connection(readyEpoch, wiredClient?.state === "ready");
  await residency.scheduleHydration(
    ref,
    intent,
    async (signal) => {
      attempt = await refreshTrackedThreadNow(
        client,
        epoch,
        ref,
        targetedResync,
        reportFailure,
        beforePublish,
        reopenOnlyClientMutationId,
        intent,
        signal,
        registerCompletion,
      );
    },
    // Recovery needs a fresh read, even when its pending publication claim was
    // retired before the scheduler's issued predecessor has settled.
    targetedResync ? "replace" : "join",
  );
  return attempt;
}

async function refreshTrackedThreadNow(
  client: AppwireClientLike,
  epoch: number,
  ref: string,
  targetedResync: boolean,
  reportFailure = false,
  beforePublish?: () => void,
  reopenOnlyClientMutationId?: string,
  intent: HydrationIntent = "foreground",
  signal?: AbortSignal,
  registerCompletion = true,
): Promise<number | undefined> {
  // Returns the tracked-hydration attempt this refresh began, or undefined
  // when it never began one (an unowned ref, or an in-flight non-targeted
  // predecessor): refreshThread's tail recheck passes that attempt to
  // unwindStopCanceledRefresh, whose currency check needs to know which
  // attempt a cancellation belongs to.
  if (!canonicalThreadOwnerActive(ref)) return undefined;
  const previous = pendingThreadHydrations.get(ref);
  if (!targetedResync && previous?.client === client && previous.epoch === epoch) return undefined;
  const pending = beginThreadHydration(ref, client, threadsStore.getState().threads.get(ref), epoch);
  const retirePending = () => retirePendingHydration(ref, pending, pendingThreadHydrations);
  signal?.addEventListener("abort", retirePending, { once: true });
  // No pre-check here: pending.client is this `client` and pending.epoch is this
  // `epoch`, so publishThreadHydration re-decides exactly the same thing one
  // frame later, and returning null from there reconciles nothing either. The
  // gate lives in one place.
  const hydration = readThreadHydration(client, ref, Date.now(), pending, { kind: "thread", signal }).then((result) => {
    if (signal?.aborted) return null;
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
    return publishAndReconcileThreadHydration(ref, pending, result, reopenOnlyClientMutationId, intent);
  });
  // A tracked read normally publishes its own settlement in
  // trackedHydrationCompletions so waiting consumers await its convergence.
  // A caller that already holds a wider registration for this ref — the
  // opening-recovery chain registers its whole span — must keep that wider
  // promise as the one consumers capture mid-read, so this read joins under
  // it instead of displacing it.
  if (registerCompletion) {
    // Compose with the opening-recovery chain's registration when one is
    // outstanding rather than displacing it: the chain's span is wider than
    // this hydration, and a consumer that starts waiting while this read runs
    // must still span the recovery behind it. Hydration-only registrations
    // still displace each other — the newer attempt owns the ref's
    // convergence. The compose is allSettled on purpose: a rejected hydration
    // is the failed refresh's own concern and must not settle the composed
    // completion while the chain's span is still pending, or a waiting
    // consumer resumes before recovery establishes the display and cursor.
    const outstandingRecovery = openingRecoveryCompletions.get(ref);
    const completion = outstandingRecovery
      ? Promise.allSettled([hydration, outstandingRecovery]).then(() => undefined)
      : hydration.then(
          () => undefined,
          () => undefined,
        );
    trackedHydrationCompletions.set(ref, completion);
    void completion.then(() => {
      if (trackedHydrationCompletions.get(ref) === completion) trackedHydrationCompletions.delete(ref);
    });
  }
  const hasPublishedModel = threadsStore.getState().threads.has(ref);
  // A failed targeted predecessor may already have removed `previous`.
  // Keep the newest targeted read adoptable by the still-active initial
  // caller until a sufficient model has actually published.
  const trackForActiveLifecycle = !hasPublishedModel && (previous !== undefined || targetedResync);
  if (trackForActiveLifecycle) {
    inflightHydrates.start(ref, hydration, client, epoch);
  }
  try {
    const model = await hydration;
    if (model && intent === "foreground" && pinnedMutationRefs.has(ref)) dispatchableMutationRefs.add(ref);
  } catch (error) {
    if (reportFailure) throw error;
    // The stale model stays published. Convergence is the owned hydration
    // lifecycle's job now (scheduleOwnedHydrationRetry, above).
  } finally {
    signal?.removeEventListener("abort", retirePending);
    retirePendingHydration(ref, pending, pendingThreadHydrations);
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
  const includeTurns = watchIncludeTurns.get(ref) ?? false;
  const pending = beginWatchedHydration(
    ref,
    client,
    threadsStore.getState().watchedThreads.get(ref),
    epoch,
    includeTurns,
  );
  // Same as refreshTrackedThread: publishWatchedHydration re-decides this.
  const hydration = readThreadHydration(client, ref, Date.now(), pending, { kind: "watched", includeTurns }).then(
    ({ model }) =>
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
    inflightWatchHydrates.start(ref, hydration, client, epoch, includeTurns);
  }
  try {
    await hydration;
  } catch {
    // Same rationale as the real-pane path above.
  } finally {
    retirePendingHydration(ref, pending, pendingWatchedHydrations);
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
    : new Set([
        ...threadsStore.getState().threads.keys(),
        ...pendingThreadHydrations.keys(),
        ...backgroundRefCounts.keys(),
        ...pinnedMutationRefs,
      ]);
  const watchRefs = targetRef
    ? new Set([targetRef])
    : new Set([...threadsStore.getState().watchedThreads.keys(), ...pendingWatchedHydrations.keys()]);
  await Promise.all([
    ...Array.from(refs, (ref) =>
      refreshTrackedThread(
        client,
        epoch,
        ref,
        targetedResync,
        false,
        undefined,
        options?.reopenOnlyClientMutationId,
        hydrationIntentFor(ref),
      ),
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
  retireReadyGeneration(readyEpoch);
  readyEpoch += 1;
  residencyIfCreated?.connection(readyEpoch, false);
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
        retireReadyGeneration(readyEpoch);
        readyEpoch += 1;
        residencyIfCreated?.connection(readyEpoch, true);
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
    residencyIfCreated?.connection(readyEpoch, true);
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
  // The entry repository's model is not in these maps, so its baseline must be
  // retired explicitly at the same boundary; otherwise a same-thread reconnect
  // that reports the same source identifiers would keep the outgoing page
  // certificates eligible for the new connection.
  for (const route of entryTranscriptRoutes()) route.repository?.beginFreshBaseline();
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
// enqueueMutationIntent's storage-failure fallback send
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
  transientRefusalPaces: new Map(),
  pendingProjectionLoadedRefs: new Set(),
  resumedIdentities: new Map(),
  resumeFailures: new Map(),
  stoppingRefs: new Set(),
  frameTimes: new Map(),
  hydrations: new Map(),
  localOpenings: new Map(),
  watchedThreads: new Map(),
  deletedRefs: new Set(),
  repairingRefs: new Set(),
  cacheLifetimes: new Map(),
  clearInFlight: undefined,

  clearResumedIdentity(ref) {
    // The pane followed the identity: its follow effect calls this, so this is
    // where a mid-swap Stop registration ends (see resumeSwapTargets). The user
    // is now on the resumed ref, and a Stop there names it directly.
    endResumeSwap(ref);
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
      beginCanonicalThreadClaim(ref, true);
      // A pinned model keeps its lifetime after the last pane releases it.
      ensureCacheLease(ref);
    }
    const generation = ensureGenerations.get(ref) ?? 0;
    refCounts.set(ref, count + 1);
    syncTranscriptResidencyOwners(ref);
    if (count === 0) acquireCacheLifetimeForeground(ref);
    syncThreadSubscription(ref, client);
    // Already hydrated: no re-read. The exception is a clear-suppressed ref
    // whose new explicit foreground lifetime owes an authoritative hydration
    // even while its stale display model is still resident.
    if (threadsStore.getState().threads.has(ref) && !needsClearLifetimeReconciliation(ref)) {
      if (count > 0) return; // Another mounted reader already owns the live bodies.
      if (await openEntryTranscript(ref)) return;
      if (ensureGenerations.get(ref) !== generation || (refCounts.get(ref) ?? 0) <= 0) return;
      // Missing or unwritten backing rows cannot establish the captured view.
      // Retire the held source proof so recovery asks for a complete baseline.
      entryTranscriptRoute(ref)?.repository?.beginFreshBaseline();
      publishEntryTranscriptModel(ref);
    }

    const claimEpoch = readyEpoch;

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
    // The cached-shell lookup (spec, "The load seam"): serial by necessity —
    // the held identity must exist before issueLatestWindowRead runs — and
    // bounded by its own 250 ms deadline. The creator alone publishes and
    // arms; a joiner sees the shell (threads.has) or the hydration
    // (inflightHydrates) and joins that instead.
    if (!threadsStore.getState().threads.has(ref) && !inflightHydrates.has(ref)) {
      await openCachedEntryTranscript(ref, generation);
      if (
        (refCounts.get(ref) ?? 0) === 0 ||
        ensureGenerations.get(ref) !== generation ||
        threadsStore.getState().deletedRefs.has(ref)
      )
        return;
    }
    let cachedBase: ThreadModel | undefined = threadsStore.getState().threads.get(ref);

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
      const hydration = readThreadHydration(hydrationClient, ref, Date.now(), pending, { kind: "thread" })
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
          retirePendingHydration(ref, pending, pendingThreadHydrations);
        });
      // .finally() re-throws inflight's own rejection on ITS OWN returned
      // promise — a separate object from `inflight` — so without a catch
      // here a failed hydrate becomes an unhandled rejection on top of the
      // one every caller already observes via `await inflight` below.
      inflightHydrates.start(ref, hydration, hydrationClient, hydrationEpoch);
      return hydration;
    };

    let inflight = inflightHydrates.get(ref);
    if (!inflight) inflight = startHydration(client, cachedBase !== undefined ? readyEpoch : claimEpoch);
    cachedBase = undefined; // the claim's arming consumed the shell; every re-arm reads the current model
    try {
      for (;;) {
        const inflightClient = inflightHydrates.client(ref) ?? client;
        const inflightEpoch = inflightHydrates.epoch(ref) ?? readyEpoch;
        try {
          const model = await inflight;
          if (model) {
            if (!(await openEntryTranscript(ref))) scheduleEntryOpeningRecovery(ref);
            return;
          }
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
          if (owned?.generation !== hydrationOwnerGeneration("thread", ref)) throw err;
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

  async releaseThreadAndWait(ref) {
    threadsStore.getState().releaseThread(ref);
    return entryTranscriptRoute(ref)?.retirement;
  },

  releaseThread(ref) {
    const count = refCounts.get(ref) ?? 0;
    if (count <= 0) return; // never tracked, or already released
    if (count > 1) {
      refCounts.set(ref, count - 1);
      syncTranscriptResidencyOwners(ref);
      return;
    }
    refCounts.delete(ref);
    syncTranscriptResidencyOwners(ref);
    const retirement = retireEntryTranscriptIfUnowned(ref);
    if (retirement) {
      releaseSubagentRows(ref);
      releaseEntryTranscriptReader(ref, true);
      retireCanonicalThreadIfUnowned(ref);
      return retirement;
    }
    syncThreadSubscription(ref);
    releaseSubagentRows(ref);
    releaseEntryTranscriptReader(ref, true);
    if (canonicalThreadOwnerActive(ref)) return;
    // Release is terminal for this owner generation: cancel its scheduled
    // retry and wake anything still awaiting its first model.
    retireCanonicalThreadIfUnowned(ref);
    // A pending read belongs to this released pane lifecycle. Retire it
    // before a new ensureThread(ref) can claim the same ref; the old promise's
    // identity-guarded finally blocks must not remove a newer hydration.
    inflightHydrates.delete(ref);
    trackedHydrationCompletions.delete(ref);
    const pending = pendingThreadHydrations.get(ref);
    if (pending) retirePendingHydration(ref, pending, pendingThreadHydrations);
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
    removeThreadModel(ref);
    // With the last tracked holder gone, the repair flag follows unless a
    // watched lifecycle still holds the ref.
    clearThreadRepairingIfUnowned(ref);
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
        needTurns,
      );
      const hydration = readThreadHydration(hydrationClient, ref, Date.now(), pending, {
        kind: "watched",
        includeTurns: needTurns,
      })
        .then(({ model }) => publishWatchedHydration(ref, pending, model, needTurns, generation))
        .finally(() => {
          retirePendingHydration(ref, pending, pendingWatchedHydrations);
        });
      inflightWatchHydrates.start(ref, hydration, hydrationClient, hydrationEpoch, needTurns);
      return hydration;
    };

    let inflight = inflightWatchHydrates.get(ref);
    const inflightHasTurns = inflightWatchHydrates.includeTurns(ref);
    // A rich caller cannot share a lean request already in flight: the
    // response would be structurally missing the turns it requested. A
    // lean caller may share a rich request because the richer snapshot is
    // sufficient for both callers.
    if (!inflight || (needTurns && !inflightHasTurns)) inflight = startHydration(client);

    for (;;) {
      const inflightClient = inflightWatchHydrates.client(ref) ?? client;
      const inflightEpoch = inflightWatchHydrates.epoch(ref) ?? readyEpoch;
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
      const currentInflightHasTurns = inflightWatchHydrates.includeTurns(ref);
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
    const pending = pendingWatchedHydrations.get(ref);
    if (pending) retirePendingHydration(ref, pending, pendingWatchedHydrations);
    // Drop the monotonic turns flag with the last watcher so a future watch of
    // the same ref starts lean again (yd16 §4.2).
    watchIncludeTurns.delete(ref);
    watchHydratedIncludeTurns.delete(ref);
    // The open-pane lifecycle may still hold this ref; only when it is gone
    // too does the wire subscription have no remaining holder.

    removeWatchedThreadModel(ref);
    // With the last watcher gone, the repair flag follows unless a tracked
    // pane still holds the ref.
    clearThreadRepairingIfUnowned(ref);
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
    beginHistoryHold(ref);
    try {
      // A shell's cursor belongs to whichever window the reconcile settles on;
      // paging below a shell the gap rule is about to replace races that
      // replacement (spec, "Scroll-back"). Scroll-back waits for the read.
      if (threadsStore.getState().cacheLifetimes.get(ref)?.shell) return;
      if (entryTranscriptRoute(ref)?.cachedUnverified) return;
      // Read-only, so it waits out a reconnect (issue #195's RCA) instead of
      // failing with AppwireClient's synchronous "cannot call ... while
      // reconnecting" rejection - see requireReadyClient's own comment.
      await requireReadyClient();
      await trackedHydrationCompletions.get(ref);
      const client = await requireReadyClient();
      const capturedEpoch = readyEpoch;
      const model = threadsStore.getState().threads.get(ref);
      if (!model) return;
      const capturedRef = model.ref;
      const capturedCursor = model.olderCursor;
      const capturedHydrations = threadsStore.getState().hydrations.get(ref) ?? 0;
      const capturedPageGeneration = olderPageGenerations.get(ref) ?? 0;
      const fixture = entryTranscriptRoute(ref);
      const fixtureState = fixture?.ref === ref ? fixture : undefined;
      const fixtureRepository = fixtureState?.repository;
      // The captured request boundary and baseline authority travel with the
      // request so a reconciliation or item update during flight cannot be
      // mis-certified on completion.
      const capturedAuthority = fixtureRepository?.getBaselineIdentity();
      const capturedBefore = fixtureRepository?.oldestPosition();
      const capturedLocalBefore = fixtureState?.localBefore;
      // The same ownership/epoch/generation guard the server path uses, applied
      // before any local publication and before issuing a reconciliation.
      const stillOwned = (): boolean => {
        const current = threadsStore.getState().threads.get(ref);
        return (
          current !== undefined &&
          wiredClient === client &&
          readyEpoch === capturedEpoch &&
          current.ref === capturedRef &&
          current.olderCursor === capturedCursor &&
          fixtureState?.localBefore === capturedLocalBefore &&
          (threadsStore.getState().hydrations.get(ref) ?? 0) === capturedHydrations &&
          (olderPageGenerations.get(ref) ?? 0) === capturedPageGeneration &&
          !pendingThreadHydrations.has(ref) &&
          entryTranscriptRoute(ref) === fixtureState &&
          fixtureState?.repository === fixtureRepository &&
          !threadsStore.getState().deletedRefs.has(ref)
        );
      };
      // The entry fixture's repository owns recorded ingestion for its one
      // selected ref. Certified local coverage serves the page; an unverified
      // frontier runs the bounded reconciliation; no local certificate is an
      // ordinary server continuation.
      if (fixtureState && fixtureRepository) {
        const outcome = await fixtureRepository.loadOlderPage(
          TRANSCRIPT_ITEM_PAGE_SIZE,
          fixtureState.lifetime,
          stillOwned,
          () => {
            // Guarded synchronous commit: the generation advance and the store
            // handoff run in the same step as loadOlderPage's guarded
            // publication, before its promise can resolve. A newer accepted
            // update or hydration delivered in the continuation window
            // therefore cannot be overwritten by the snapshot this page
            // returned, and the background-only route (no attached reader
            // subscription) still receives the model here.
            olderPageGenerations.set(ref, capturedPageGeneration + 1);
            if (fixtureState.localBefore) fixtureState.localBefore = fixtureRepository.localOlderPosition();
            publishEntryTranscriptModel(ref);
          },
        );
        if (outcome.kind === "reconcile") {
          if (!stillOwned()) return;
          // The reconciliation is itself the new baseline boundary: the cache
          // reported a connected-but-unverified frontier, so the certificates
          // that made it ambiguous must be retired BEFORE the refresh. A live
          // read that reports the same identity and omits the wire
          // authoritative flag does not advance the source revision on its
          // own, and without this the same conflicting certificates would stay
          // eligible and every later action would repeat the refresh.
          fixtureRepository.beginFreshBaseline();
          await refreshTrackedThread(client, capturedEpoch, capturedRef, true);
          return;
        }
        // The local outcome already committed generation and publication
        // synchronously in onPublished; do not republish the returned model
        // after the await.
        if (outcome.kind === "local") return;
        if (outcome.kind === "exhausted") {
          if (!stillOwned()) return;
          fixtureState.localBefore = undefined;
          olderPageGenerations.set(ref, capturedPageGeneration + 1);
          putThreadModel(ref, outcome.model);
          return;
        }
        if (outcome.kind === "superseded") return;
        if (capturedLocalBefore && !fixtureRepository.getSnapshot()?.olderCursor) {
          // A local-only scheduling key is never an opaque source cursor.
          // If its cache lookup is unavailable, recover through the normal
          // authoritative read rather than sending the display key on the wire.
          if (!stillOwned()) return;
          fixtureState.localBefore = undefined;
          await refreshTrackedThread(client, capturedEpoch, capturedRef, true);
          return;
        }
      }
      // No server cursor does not imply no local bodies: try the repository
      // first, and never manufacture a wire cursor from its local boundary.
      if (!capturedCursor) return;
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
        threadsStore.getState().deletedRefs.has(ref) ||
        (fixtureRepository !== undefined &&
          (entryTranscriptRoute(ref) !== fixtureState || fixtureState?.repository !== fixtureRepository)) ||
        pendingThreadHydrations.has(ref)
      )
        return;
      olderPageGenerations.set(ref, capturedPageGeneration + 1);
      let accepted = false;
      if (fixtureState && fixtureRepository && capturedAuthority !== undefined) {
        accepted = fixtureRepository.acceptPage(resp, {
          lifetime: fixtureState.lifetime,
          ...nextTranscriptReadIdentity(),
          authority: capturedAuthority,
          ...(capturedBefore === undefined ? {} : { before: capturedBefore }),
          requestCursor: capturedCursor,
        });
      }
      if (accepted) {
        if (fixtureState) fixtureState.localBefore = undefined;
        const snapshot = fixture?.repository?.getSnapshot();
        if (snapshot) putThreadModel(ref, snapshot);
      } else if (!(fixtureState && fixtureRepository && capturedAuthority !== undefined)) {
        // Only a page no repository owns falls back to the ordinary merge. A
        // repository-owned page it refused (a disposed owner after a deletion
        // fence, a lost lifetime, an authority change) must not be published
        // here: the repository is the sole owner of its recorded content.
        putThreadModel(ref, mergeOlderItemPage(current, resp));
      }
    } finally {
      await endHistoryHold(ref);
    }
  },

  async send(ref, text, attachments, skillNames, commandNames, mentions) {
    await sendMutationIntent(
      composerMutationIntent(ref, "send", text, attachments, skillNames, commandNames, mentions),
    );
  },

  async continueTurn(ref, expectedTurnId) {
    // The one shared decision is re-read here, synchronously, at activation:
    // the render's captured button state is not authority. The canonical
    // latest turn, the clicked turn's current failure, the pending owner and
    // its readiness all enter the same gate the end-cap renders.
    const model = threadsStore.getState().threads.get(ref);
    if (!model) return;
    // Identity alone is not a failure. An authoritative refresh can keep the
    // same latest turn id and drop its error (or replace the turn), so the
    // activation resolves the current turn by that id and requires it to still
    // carry the failure the captured action belonged to.
    const currentTurn = model.turns.find((turn) => turn.id === expectedTurnId);
    const pending = retryPendingInputNow(ref);
    const decision = retryEligibility({
      clickedTurnId: expectedTurnId,
      thread: model,
      clickedTurnFailed: currentTurn !== undefined && isTurnError(currentTurn.error),
      pendingMutations: pending.pendingMutations,
      pendingMutationsReady: pending.pendingMutationsReady,
      // Browser Retry is durably routed: the enqueue funnel owns the live
      // client requirement and surfaces its refusal to the press. Moving that
      // check into eligibility would turn a reported failure into a silent
      // no-op, so the browser offers the action and the funnel answers.
      serviceReady: true,
    });
    if (!decision.allowed) return;
    // An ordinary Send/press marks the composer's shared submission guard
    // before its durable row is visible in the projection. Retry must join that
    // guard, not only its own read, or a press in that window would admit a
    // continuation that dispatches after the new message. The same guard
    // refuses a second Retry press while this one's enqueue is in flight.
    if (!beginSharedSubmission(ref)) return;
    try {
      await sendMutationIntent({
        ...composerMutationIntent(ref, "send", TURN_CONTINUATION_TEXT),
        preserveComposer: true,
      });
    } finally {
      endSharedSubmission(ref);
    }
  },

  async steer(ref, text, attachments, skillNames, commandNames, mentions) {
    await enqueueMutationIntent(
      composerMutationIntent(ref, "steer", text, attachments, skillNames, commandNames, mentions),
    );
  },

  async queue(ref, text, attachments, skillNames, commandNames, mentions) {
    // A queue is composer input too: mark the shared guard for the enqueue's
    // window so Retry cannot enqueue a continuation ahead of the queued row
    // becoming visible in the projection.
    const release = markInputSubmission(ref);
    try {
      await enqueueMutationIntent(
        composerMutationIntent(ref, "queue", text, attachments, skillNames, commandNames, mentions),
      );
    } finally {
      release();
    }
  },

  async interrupt(ref) {
    cancelPendingUserIntents(ref);
    // Same mid-swap reach as forceStop's cancel above: a Stop pressed on the
    // old ref while a resume is moving its rows to the resumed ref must cancel
    // the moved rows too, or the message delivers under a Stop.
    await cancelResumeSwapTarget(resumeSwapTargetFor(ref));
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
      COMPOSER_METHODS.promote,
      { ref, index, expectedInstanceId: expectedInstanceID(ref), expectedEntryId },
      {
        method: COMPOSER_METHODS.promote,
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
      entryTranscriptRoute(ref)?.repository?.acceptResponseFields({ goal });
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
    if (!canWriteSharedNotes(model)) throw new Error("Session cannot accept notes");
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
    if (!canWriteSharedNotes(model)) throw new Error("Session cannot accept notes");
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
    // Capture the mid-swap target before the first await: the pane's follow
    // effect can clear the registration while the cancellation write below is
    // in flight, and this Stop was pressed while the pane still showed `ref`.
    const swapTarget = resumeSwapTargetFor(ref);
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
      await cancelResumeSwapTarget(swapTarget);
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
    // Captured before the await, for forceStop's reason above.
    const swapTarget = resumeSwapTargetFor(ref);
    // The dispatch-only gate (#4552, Jesse's ruling): forceStop closes its
    // cancellation write's window with stoppingRefs, but a graceful shutdown
    // must not, because that fence also refuses a Send the user makes during the
    // window. This gate covers exactly the refs the Stop's cancellation writes
    // reach and is read by currentDispatchClient alone. Armed synchronously,
    // before the first await, so the whole write window is fenced.
    const releaseRef = armShutdownDispatchGate(ref);
    const releaseSwapTarget = swapTarget === undefined ? undefined : armShutdownDispatchGate(swapTarget);
    try {
      await cancelUnattemptedMutations(ref);
      if (swapTarget !== undefined) {
        // The swap target's own cancellation write is the only thing its arm
        // covers, so release it here rather than gating a live resumed ref for
        // the whole shutdown RPC.
        try {
          // Same mid-swap reach as forceStop's cancel above.
          await cancelResumeSwapTarget(swapTarget);
        } finally {
          releaseSwapTarget?.();
        }
      }
      const client = requireClient();
      try {
        await client.request("thread/shutdown", { ref });
      } catch (err) {
        throw mapConflict(err);
      }
    } finally {
      releaseSwapTarget?.();
      releaseRef();
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
      return client.request("model/list", refresh ? { refresh: true } : {});
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
    // appwire.Conflict() (server/appwire_turn_handlers.go's
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
    // The daemon broadcasts the resolved notification to every other client, so
    // this one records the resolve itself (see resolvedEscalationsByRef), then
    // folds it through the same routing a broadcast takes for the models and
    // hydrations that are live now.
    resolvedEscalationsFor(ref).add(escalationId);
    const resolvedAnywhere =
      (model !== undefined && resolvedModel !== model) ||
      (watchedModel !== undefined && resolvedWatched !== watchedModel);
    const threadId = (model ?? watchedModel)?.threadId;
    if (resolvedAnywhere && threadId !== undefined) {
      handleNotification({
        method: "evener/sandbox/escalation/resolved",
        params: { threadId, ref, escalationId },
      });
    }
  },
}));

// The missed-message backstop (spec, "Eviction, cap, and cross-tab"): the
// aborted write, a sibling's clear broadcast, or a durable observation is the
// tab's proof that a clear happened.
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
      next.set(ref, {
        ...lifetime,
        suppressed: true,
        clearSuppressed: true,
        suppressionHoldsPreClearHolders: heldCountsAtStore(ref) ? true : undefined,
      });
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
    for (const ref of message.refs) noteEntryRouteRetirement(ref);
    deleteCachedRefs(message.refs);
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
  for (const ref of refs) {
    residencyIfCreated?.revoke(ref, "deleted");
    noteEntryRouteRetirement(ref);
  }
  deleteCachedRefs(refs);
  cacheChannel.connect();
  cacheChannel.postMessage({ kind: "deletion", refs });
}

// Retirement is synchronous. Durable healing then discovers disk-only histories
// by requested-ref metadata and uses the accepted per-history deletion fence.
// Captured identities are included even when their first write has not landed.
function deleteCachedRefs(refs: string[]): void {
  const byCache = new Map<SessionEntryCache, Set<string>>();
  const routes = entryTranscriptRoutes().filter((route) => refs.includes(route.ref));
  for (const route of routes) {
    const keys = byCache.get(route.cache) ?? new Set<string>();
    if (route.historyKey) keys.add(route.historyKey);
    byCache.set(route.cache, keys);
    clearEntryTranscriptLocalOpening(route.ref);
    retireEntryTranscriptRoute(route);
  }
  // The legacy stores are gone (6d), so there is nothing left to source-heal;
  // entry histories are deleted durably through the cache's own per-history
  // fence below.
  if (!byCache.has(currentEntryCache())) byCache.set(currentEntryCache(), new Set());
  const histories = Promise.allSettled(
    [...byCache].flatMap(([cache, captured]) => [
      ...[...captured].map((historyKey) => cache.deleteHistory(historyKey)),
      // Discovery adds disk-only histories. It cannot gate already-known keys,
      // and must not delete them twice (each deletion advances their fence).
      cache
        .historyKeysForRequestedRefs(refs)
        .then((keys) =>
          Promise.allSettled(keys.filter((key) => !captured.has(key)).map((key) => cache.deleteHistory(key))),
        ),
    ]),
  );
  void trackProjectionWork(histories).catch(() => {
    // Durable storage is best-effort, but retired claims never resume writes.
  });
}

function reconcileClearInput(): void {
  if (threadsStore.getState().clearInFlight !== undefined) return;
  const tracked = clearHydrationDemands.get("thread") ?? new Map<string, number>();
  clearHydrationDemands.set("thread", tracked);
  for (const route of entryTranscriptRoutes()) {
    const generation = route.clearRefreshGeneration;
    route.clearRefreshGeneration = undefined;
    if (generation !== undefined) tracked.set(route.ref, generation);
  }
  const demands = [...clearHydrationDemands];
  clearHydrationDemands.clear();
  for (const [kind, refs] of demands) {
    for (const [ref, generation] of refs) {
      if (
        generation !== hydrationOwnerGeneration(kind, ref) ||
        !hydrationOwnerActive(kind, ref) ||
        threadsStore.getState().deletedRefs.has(ref) ||
        wiredClient?.state !== "ready"
      )
        continue;
      // Ordinary refresh preserves publication fences and the held cache
      // lifetime's suppression. Reconnect owns a non-ready client.
      const retry = kind === "watched" ? retryWatchedHydration : retryTrackedHydration;
      void retry(wiredClient, readyEpoch, ref).catch(() => {});
    }
  }
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
  // The separately installed entry route retires its publication/persistence
  // claims NOW, synchronously, before either clear transaction resolves: an
  // already-installed reader, subscription or repository must not publish or
  // persist into the clear window, and a hydration that began before it must not
  // adopt the post-Clear epoch. Readable content stays in the store model.
  const entryRoutes = entryTranscriptRoutes();
  for (const route of entryRoutes) {
    noteEntryRouteRetirement(route.ref);
    // Retire the route's publication and persistence claims immediately by
    // suppressing it, WITHOUT disposing it: an aborted Clear (nothing changed)
    // re-enables the same held reader/subscription/repository, so a still-held
    // foreground model resumes ordinary ingestion without a release/reacquire.
    route.clearInert = true;
  }
  // Synchronous logical retirement, before the transaction resolves: a
  // pre-Clear hydration must not publish into the same-tab clear window. Issued
  // reads keep their capacity charge; only their claim to publish is retired.
  for (const kind of ["thread", "watched"] as const) {
    const pendingHydrations = kind === "thread" ? pendingThreadHydrations : pendingWatchedHydrations;
    const demands = clearHydrationDemands.get(kind) ?? new Map<string, number>();
    clearHydrationDemands.set(kind, demands);
    for (const [ref, pending] of [...pendingHydrations]) {
      demands.set(ref, hydrationOwnerGeneration(kind, ref));
      retirePendingHydration(ref, pending, pendingHydrations);
    }
  }
  residencyIfCreated?.retireClaims();
  // The entry cache is the tab's only durable Clear boundary.
  const result = await currentEntryCache().clear();
  if (!result.committed) {
    // The abort drops only its own marker — lifetime suppression is never touched,
    // so another source's mid-flight arming stands. Durable observations
    // never need reverting: optimistic arming did not change their epoch.
    // Neither database changed, so the suppressed entry route is re-enabled: a
    // still-held foreground model resumes ordinary recorded ingestion
    // (publication and persistence) without a release/reacquire.
    // Re-enable BEFORE resuming the retired hydration work: a resumed task
    // re-checks its route, and one that runs while the route is still inert
    // rejects as retired.
    for (const route of entryRoutes) {
      route.clearInert = false;
      route.repository?.resumePersistence();
    }
    threadsStore.setState((s) => (s.clearInFlight === armed ? { clearInFlight: undefined } : s));
    residencyIfCreated?.releaseClearClaim();
    // Input the clear window suppressed must survive an aborted clear: replay
    // it now that the routes and the write gate are open again.
    replayPendingClearReplay();
    reconcileClearInput();
    return { committed: false };
  }
  // A held production route keeps displaying live input. Its captured old
  // epoch is unchanged, so all subsequent writes are durably refused until a
  // fresh explicit acquisition after the pre-Clear holders have released.
  for (const route of entryRoutes) {
    route.repository?.cancelPersistenceAfterClear();
    route.clearInert = false;
  }
  // The clear committed, so the content its window suppressed is gone: drop the
  // held replay rather than resurrecting it after the clear.
  discardPendingClearReplay();
  onCacheEpochObserved(result.epoch);
  residencyIfCreated?.clear(result.epoch);
  // Every open lease whose captured epoch predates the committed epoch: the
  // same set the marker refused during the flight, re-derived at commit so a
  // lease captured mid-flight is covered too.
  threadsStore.setState((s) => {
    const cacheLifetimes = suppressCacheLifetimesBefore(s.cacheLifetimes, result.epoch);
    return s.clearInFlight === armed ? { cacheLifetimes, clearInFlight: undefined } : { cacheLifetimes };
  });
  cacheChannel.connect();
  cacheChannel.postMessage({ kind: "clear", epoch: result.epoch });
  reconcileClearInput();
  return { committed: true };
}

/** The settings row's reader (spec, "The clear-cached-sessions setting"):
 * the entry cache's session count. The failure discipline holds: undefined
 * means unavailable (a failed open or transaction), never a throw and never a
 * zero that would let the row claim a remedy ran. */
export async function countCachedSessions(): Promise<number | undefined> {
  return currentEntryCache().count();
}

/** Same-tab committed writes invalidate a mounted settings row immediately.
 * Sibling writes still use its existing per-render count. */
export function subscribeCacheWrites(listener: () => void): () => void {
  return currentEntryCache().subscribeWrites(listener);
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

function transcriptResidency(): TranscriptResidency {
  if (residencyIfCreated) return residencyIfCreated;
  residencyIfCreated = createTranscriptResidency(
    {
      hydrate: async (ref, intent, signal) => {
        if (signal.aborted) return;
        const client = await requireReadyClient();
        if (signal.aborted || !canonicalThreadOwnerActive(ref)) return;
        const epoch = readyEpoch;
        await refreshTrackedThreadNow(client, epoch, ref, false, false, undefined, undefined, intent, signal);
      },
    },
    backgroundTranscriptOptions(),
  );
  return residencyIfCreated;
}

function syncTranscriptResidencyOwners(ref: string): void {
  residencyIfCreated?.connection(readyEpoch, wiredClient?.state === "ready");
  syncCacheLifetimeForeground(ref);
  const route = entryTranscriptRoute(ref);
  if (!route?.repository) return;
  // A history or mutation hold owns repository retention, not the lifetime of
  // the already-published display. Pane release drops its display separately.
  if (heldCountsAtStore(ref)) route.reader ??= route.repository.attachReader();
  else if (route.reader) releaseEntryTranscriptReader(ref);
}

export async function settleBackgroundTranscriptsForTests(): Promise<void> {
  // The reduced residency owns no disk work (its settled() is a no-op). The
  // transcript write and checkpoint work the pre-6d helper drained now lives in
  // each entry route's TranscriptRepository, so drain every route's repository.
  // Like the pre-6d residency, this must not await an issued network hydration:
  // a callers' read may legitimately never settle (see residency's own
  // settled()). A repository's settled() throws when recovery is genuinely
  // outstanding; that throw propagates, matching settleEntryTranscriptForTests,
  // so this drain never reports a clean settle while background work remains.
  await residencyIfCreated?.settled();
  await Promise.all(entryTranscriptRoutes().map((route) => route.repository?.settled(false)));
}

/** @internal Observe actual store hydration before the entry commit, without issuing a read. */
export async function settleEntryTranscriptForTests(ref: string): Promise<void> {
  await trackedHydrationCompletions.get(ref);
  await entryTranscriptRoute(ref)?.repository?.settled();
}

export function backgroundTranscriptSnapshotForTests(): TranscriptResidencySnapshot {
  return (
    residencyIfCreated?.snapshot() ?? {
      residentBytes: 0,
      pendingBytes: 0,
      workerBytes: 0,
      diskWorkers: 0,
      networkHydrations: 0,
      queuedHydrationRefs: [],
      committedWrites: 0,
      refs: new Map(),
    }
  );
}

export function pendingThreadHydrationUpdatesForTests(): readonly ReceivedTranscriptUpdate[] {
  return [...pendingThreadHydrations.values()].flatMap((pending) => pending.notifications);
}

export function pendingThreadHydrationBaseModelsForTests(): readonly ThreadModel[] {
  return [...pendingThreadHydrations.values()].flatMap((pending) => (pending.baseModel ? [pending.baseModel] : []));
}

export function acquireBackgroundThread(ref: string): BackgroundThreadLease {
  beginCanonicalThreadClaim(ref);
  backgroundRefCounts.set(ref, (backgroundRefCounts.get(ref) ?? 0) + 1);
  syncTranscriptResidencyOwners(ref);
  backgroundLeaseSequences.set(ref, (backgroundLeaseSequences.get(ref) ?? 0) + 1);
  const ownerGeneration = canonicalThreadOwnerGenerations.get(ref) ?? 0;
  syncThreadSubscription(ref);

  let settleRetired: (() => void) | undefined;
  let stopWatchingReadyGeneration: (() => void) | undefined;
  const retired = new Promise<void>((resolve) => {
    settleRetired = resolve;
  });
  const acquisition = (async () => {
    if (threadsStore.getState().deletedRefs.has(ref) || threadsStore.getState().threads.has(ref)) return;
    try {
      const client = await requireReadyClient();
      const epoch = readyEpoch;
      if (client !== wiredClient) return;
      stopWatchingReadyGeneration = watchReadyGenerationRetirement(epoch, () => settleRetired?.());
      if (canonicalThreadOwnerGenerations.get(ref) !== ownerGeneration || !canonicalThreadOwnerActive(ref)) return;
      const completion = trackedHydrationCompletions.get(ref);
      if (completion) {
        await completion;
        return;
      }
      const inflight = inflightHydrates.get(ref);
      if (inflight) {
        await inflight.catch(() => null);
        return;
      }
      await refreshTrackedThread(client, epoch, ref, false, false, undefined, undefined, "background");
    } catch {
      // `ready` settles after its first bounded attempt, not as a currentness
      // certificate. The existing owner-scoped retry scheduler owns recovery.
    }
  })();
  const ready = Promise.race([acquisition, retired]).finally(() => stopWatchingReadyGeneration?.());

  let releasePromise: Promise<undefined | TranscriptRetirementOutcome> | undefined;
  return {
    ready,
    release(): Promise<undefined | TranscriptRetirementOutcome> {
      if (releasePromise) return releasePromise;
      settleRetired?.();
      const remaining = (backgroundRefCounts.get(ref) ?? 0) - 1;
      if (remaining > 0) {
        backgroundRefCounts.set(ref, remaining);
        syncTranscriptResidencyOwners(ref);
        releasePromise = Promise.resolve(undefined);
        return releasePromise;
      }
      backgroundRefCounts.delete(ref);
      syncTranscriptResidencyOwners(ref);
      const entryRetirement = retireEntryTranscriptIfUnowned(ref);
      if (entryRetirement) {
        retireCanonicalThreadIfUnowned(ref);
        const pending = pendingThreadHydrations.get(ref);
        if (pending) retirePendingHydration(ref, pending, pendingThreadHydrations);
        inflightHydrates.delete(ref);
        trackedHydrationCompletions.delete(ref);
        releasePromise = entryRetirement;
        return releasePromise;
      }
      const releaseGeneration = (backgroundLeaseSequences.get(ref) ?? 0) + 1;
      backgroundLeaseSequences.set(ref, releaseGeneration);
      finalReleaseOwners.set(ref, releaseGeneration);
      if (!canonicalThreadOwnerActive(ref)) {
        // A held attempt cannot certify a released owner's final state. Retire
        // it synchronously so its late response cannot publish into a new life.
        retireCanonicalThreadIfUnowned(ref);
        const pending = pendingThreadHydrations.get(ref);
        if (pending) retirePendingHydration(ref, pending, pendingThreadHydrations);
        inflightHydrates.delete(ref);
        trackedHydrationCompletions.delete(ref);
      }
      releasePromise = (async () => {
        // Yield once so a publication routed in the same turn is observed
        // before the owner census decides whether to drop the model.
        await Promise.resolve();
        if (finalReleaseOwners.get(ref) === releaseGeneration) finalReleaseOwners.delete(ref);
        if (backgroundLeaseSequences.get(ref) !== releaseGeneration || canonicalThreadOwnerActive(ref)) return;
        dropUnpinnedModel(ref, false);
        syncThreadSubscription(ref);
      })();
      return releasePromise;
    },
  };
}

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
  resolvedEscalationsByRef.clear();
  threadReadsByRef.clear();
  for (const route of productionEntryRoutes.values()) retireEntryTranscriptRoute(route);
  productionEntryRoutes.clear();
  productionEntryCache?.close();
  productionEntryCache = undefined;
  residencyIfCreated?.dispose();
  residencyIfCreated = undefined;
  currentAdmissions.clear();
  currentAppliedSequences.clear();
  currentThreadIds.clear();
  currentRefsByThreadId.clear();
  userIntentStopGenerations.clear();
  userIntentStopSequence = 0;
  activeStops.clear();
  shutdownDispatchGates.clear();
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
  for (const cancelRetry of transientRefusalCancels.values()) cancelRetry();
  transientRefusalCancels.clear();
  dispatchReadyClient = null;
  dispatchReadyEpoch = -1;
  refCounts.clear();
  backgroundRefCounts.clear();
  historyHoldCounts.clear();
  cacheLifetimeHeld.clear();
  finalReleaseOwners.clear();
  canonicalThreadOwnerGenerations.clear();
  backgroundLeaseSequences.clear();
  // A pre-reset ensure may still await the cache. Do not reuse its generation
  // when the next test claims the same ref, or that old client can re-enter.
  for (const [ref, generation] of ensureGenerations) ensureGenerations.set(ref, generation + 1);
  entryRouteRetirements.clear();
  olderPageGenerations.clear();
  discardPendingClearReplay();
  goalUpdateGenerations.clear();
  notesUpdateGenerations.clear();
  inflightHydrates.clear();
  // The session cache's module state: the epoch view returns to unobserved so
  // the next test observes the fresh database it just deleted.
  tabCacheEpoch = undefined;
  // The cache channel's module state: a test's installed factory and its
  // channel go with the reset, so the next test attaches a fresh channel
  // under the default factory (the seam's own close-and-reattach).
  setCacheChannelFactoryForTests(defaultCacheChannelFactory);
  trackedHydrationCompletions.clear();
  openingRecoveryCompletions.clear();
  pendingThreadHydrations.clear();
  pendingMutationReconciliations.clear();
  watchRefCounts.clear();
  inflightWatchHydrates.clear();
  pendingWatchedHydrations.clear();
  clearHydrationDemands.clear();
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
      repairingRefs: new Set(),
      cacheLifetimes: new Map(),
      clearInFlight: undefined,
    },
    true,
  );
}
