import { type ComposerMention, latestTurnId } from "@evener/appwire-client";
import {
  awaitingFirstFrameSend,
  createMutationProjectionFence,
  createPendingTurnsStore,
  createSubmissionRunner,
  type MutationPersistencePort,
  type MutationPersistenceSnapshot,
  type MutationProjectionRefresh,
  outboxEntriesByState,
  ownPendingTurnInput,
  type PendingTurnsDraftPort,
  type PendingTurnsThreadsPort,
  recoveryEntries,
  wireMutationCommitFeed,
} from "@evener/appwire-client/state/mutation";
import { useCallback, useEffect, useMemo, useRef, useSyncExternalStore } from "react";
import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import { isOwnMutationRecord } from "../../../../stores/mutationClientIdentity";
import type {
  MutationAttachment,
  MutationOutboxRecord,
  MutationRecoveryRecord,
} from "../../../../stores/mutationOutbox";
import { clearProjectionWorkForTests, trackProjectionWork } from "../../../../stores/projectionWork";
import {
  type ComposerMutationRoute,
  discardRecoveryMutation,
  type InputAttachment,
  installResumeOnlyProjection,
  installRetryPendingProjection,
  notifyReadyForMutationDispatch,
  readMutationPersistence,
  resendRecoveryMutation,
  retryBlockedMutation,
  subscribeMutationPersistence,
  threadsStore,
  transientRefusalRetryPending,
  updateRecoveryMutation,
  useThreadsStore,
} from "../../../../stores/threads";
import { clearDraft, readComposerDraft, readDraftRevision } from "../draft";
import type { PendingMethod, PendingTurnEntry } from "./pendingReconcile";

export type { PendingMethod, PendingTurnEntry } from "./pendingReconcile";

// The client-swap safety, reconciliation and submission bookkeeping now live
// in the package's pending-turns projection store
// (`@evener/appwire-client/state/mutation`); this module is the web's one
// instance, bound to the browser's thread store, composer-draft storage and
// client identity through the three ports the core takes. Every durable-read
// op below (the generation fencing and IndexedDB reads) stays here: it is
// genuinely web-specific, not part of what moved. The work they start is
// counted by the stores' projection work tracker (stores/projectionWork.ts).
const threadsPort: PendingTurnsThreadsPort = {
  getThreadModel: (ref) => threadsStore.getState().threads.get(ref),
};
const draftPort: PendingTurnsDraftPort = {
  readDraftRevision,
  readComposerDraft,
  clearDraft,
};
const pendingTurnsStore = createPendingTurnsStore<MutationAttachment>({
  threads: threadsPort,
  draft: draftPort,
  identity: { isOwnMutationRecord },
});

// A ref's pending state is UNKNOWN until its own durable read answers: the
// projection's maps are empty for a ref whether or not rows exist, so an empty
// answer is only evidence once this store has read and published it. The
// framework-free projection store deliberately holds records, not readiness -
// the web's durable-read ownership is this module's - so the reactive view of
// `loadedRefs` lives here, beside the entries it qualifies.
interface PendingReadinessState {
  loaded: ReadonlySet<string>;
}

const pendingReadinessStore = createStore<PendingReadinessState>(() => ({ loaded: new Set() }));

function publishPendingReadiness(): void {
  pendingReadinessStore.setState({ loaded: new Set(loadedRefs) });
}

export function usePendingTurnReadiness(ref: string): boolean {
  return useStore(pendingReadinessStore, (state) => state.loaded.has(ref));
}

// Retry's owner-scoped in-flight admission, reactive so every mounted view
// disables the action at the press rather than only after the durable row is
// visible. It is page-lifetime bookkeeping, not durable state: the pending
// projection owns the block once the record exists.
interface RetryAdmissionState {
  refs: ReadonlySet<string>;
}

const retryAdmissionStore = createStore<RetryAdmissionState>(() => ({ refs: new Set() }));
const retryAdmissions = new Set<string>();

function publishRetryAdmissions(): void {
  retryAdmissionStore.setState({ refs: new Set(retryAdmissions) });
}

function beginRetryAdmission(ref: string): boolean {
  if (retryAdmissions.has(ref)) return false;
  retryAdmissions.add(ref);
  publishRetryAdmissions();
  return true;
}

function endRetryAdmission(ref: string): void {
  if (!retryAdmissions.delete(ref)) return;
  publishRetryAdmissions();
}

export function useRetryAdmissionInFlight(ref: string): boolean {
  return useStore(retryAdmissionStore, (state) => state.refs.has(ref));
}

// An accepted Retry whose result the client has not observed yet. A
// projectionState:"reflected" receipt settles the durable row, but the
// conversation only proves the accepted turn when its own model advances (or
// an authoritative read says otherwise). Until then the old failure is not
// retryable: server-side reflection is not proof this client knows the tail.
// The marker is page-lifetime bookkeeping tied to the existing mutation and
// recovery owners, never a durable Retry record.
interface RetryReflection {
  failedTurnId: string;
  // Recovery rows already present when the Retry was submitted: a new one is
  // the existing explicit refusal path, which restores a usable Retry.
  recoveryBaseline: ReadonlySet<string>;
  refreshStarted: boolean;
  // The ref's authoritative-publication counter when this marker's own
  // refresh started. Only a publication newer than it can settle the marker:
  // a refresh promise can resolve after its read was discarded for superseded
  // hydration ownership, and that completion is not state evidence.
  publishedHydrations: number | undefined;
  // The marker's own read resolved without publishing (it lost ownership).
  // The winning read owns the next answer, so reconciliation waits for its
  // publication instead of starting another read over it.
  handedOff: boolean;
}

const retryReflections = new Map<string, RetryReflection>();
const retryReflectionStore = createStore<{ byRef: ReadonlyMap<string, string> }>(() => ({ byRef: new Map() }));

function publishRetryReflections(): void {
  retryReflectionStore.setState({
    byRef: new Map([...retryReflections].map(([ref, marker]) => [ref, marker.failedTurnId])),
  });
}

function clearRetryReflection(ref: string): void {
  if (!retryReflections.delete(ref)) return;
  publishRetryReflections();
}

function hasNewRecovery(ref: string, marker: RetryReflection): boolean {
  return recoveryEntries(pendingTurnsStore.getState().recovery, ref).some(
    (record) => !marker.recoveryBaseline.has(record.clientMutationId),
  );
}

function markRetrySubmitted(ref: string, failedTurnId: string): void {
  retryReflections.set(ref, {
    failedTurnId,
    recoveryBaseline: new Set(
      recoveryEntries(pendingTurnsStore.getState().recovery, ref).map((r) => r.clientMutationId),
    ),
    refreshStarted: false,
    publishedHydrations: undefined,
    handedOff: false,
  });
  publishRetryReflections();
  reconcileRetryReflection(ref);
}

function waitForRetryReflection(ref: string, marker: RetryReflection): void {
  if (marker.refreshStarted) return;
  marker.refreshStarted = true;
  // Snapshot the publication count before the read starts. A completion at
  // this count or below published nothing for this read (it was superseded,
  // joined an existing read, or never began), so it cannot settle the marker.
  const publishedHydrations = threadsStore.getState().hydrations.get(ref) ?? 0;
  marker.publishedHydrations = publishedHydrations;
  void threadsStore
    .getState()
    .refreshThread(ref)
    .then(() => {
      if (retryReflections.get(ref) !== marker) return;
      marker.refreshStarted = false;
      if ((threadsStore.getState().hydrations.get(ref) ?? 0) > publishedHydrations) {
        // This read published the current-owner state: current-conversation
        // evidence, so the ordinary eligibility rule may act again.
        clearRetryReflection(ref);
        return;
      }
      // The promise resolved but its response was discarded for superseded
      // hydration ownership. Hand the wait to the winning read: its
      // authoritative publication is what the store subscription below sees.
      marker.handedOff = true;
    })
    .catch(() => {
      // A failed read proves nothing, so the marker stays; a later
      // notification, reconnect or remount drives another attempt.
      if (retryReflections.get(ref) !== marker) return;
      marker.refreshStarted = false;
      // The owner's hydration-retry lifecycle owns the next read after a
      // transport failure; its publication settles this marker.
      marker.handedOff = true;
    });
}

// Re-decide one marker from the owners as they are now. Advances clear it, a
// new refusal recovery clears it, and an empty pending projection with no
// conversation change asks the thread owner for the authoritative answer once.
function reconcileRetryReflection(ref: string): void {
  const marker = retryReflections.get(ref);
  if (!marker) return;
  const model = threadsStore.getState().threads.get(ref);
  if (!model || latestTurnId(model) !== marker.failedTurnId) {
    clearRetryReflection(ref);
    return;
  }
  if (hasNewRecovery(ref, marker)) {
    clearRetryReflection(ref);
    return;
  }
  if (ownPendingTurnInput(pendingTurnsStore.pendingTurnEntries(ref))) return;
  // A publication newer than this marker's own read is the authoritative
  // current-owner answer, whichever refresh produced it (including the read
  // that superseded ours).
  if (
    marker.publishedHydrations !== undefined &&
    (threadsStore.getState().hydrations.get(ref) ?? 0) > marker.publishedHydrations
  ) {
    clearRetryReflection(ref);
    return;
  }
  // A handed-off marker waits for the winning read's publication. Starting a
  // read here would supersede that winner and discard the only answer in
  // flight; remounting the action explicitly asks again below.
  if (marker.handedOff) return;
  waitForRetryReflection(ref, marker);
}

export function useRetryAwaitingReflection(ref: string, turnId: string): boolean {
  return useStore(retryReflectionStore, (state) => state.byRef.get(ref) === turnId);
}

// A mounted action asks the owner to re-decide a still-open marker, so a
// remount after a failed reconciliation read gets another authoritative
// attempt instead of leaving the action permanently inactive.
export function ensureRetryReflectionResolved(ref: string): void {
  const marker = retryReflections.get(ref);
  // A remount is an explicit new probe: past a lost or failed read it asks
  // the owner again rather than leaving the action inactive.
  if (marker) marker.handedOff = false;
  reconcileRetryReflection(ref);
}

// The refs whose durable outbox this projection has actually read and
// published. The resume-only predicate (threads.ts's hasBlockedUnknown /
// hasQueuedNonSend) reads this through the registered isLoaded below to fail
// closed until a ref's own durable read resolves: before that the outbox map is
// empty for the ref whether or not durable rows exist, so "no uncertainty"
// there is not yet evidence. A ref is un-loaded when a refresh that could
// replace its rows starts, and re-loaded only once that refresh still owns it
// when it resolves (markProjectionLoaded): a ref read once is not "loaded"
// while a newer durable read that may publish blocked or queued rows is in
// flight, so the stale projection cannot answer for it. Cleared by the test
// reset hook too.
const loadedRefs = new Set<string>();

// Publish this projection's synchronous delivery-uncertain read into the
// threads store's shared resume-only predicate (installResumeOnlyProjection):
// the predicate the press, enqueue and dispatch paths share cannot import this
// module (it imports threads.ts), so it reaches the blockedUnknown rows through
// this registered read instead. Reading the live store state on every call
// keeps the predicate current with the last durable read this projection
// published, exactly as the composer's own useBlockedMutationEntries does.
installResumeOnlyProjection({
  isLoaded: (ref) => loadedRefs.has(ref),
  hasBlockedUnknown: (ref) =>
    outboxEntriesByState(pendingTurnsStore.getState().outbox, ref, "blockedUnknown").length > 0,
  // A queued non-turn/start row (still "submitting") parks at the target's
  // FIFO: a turn/start queued behind it never dispatches, so a ref holding one
  // is not the foldable shape. An ATTEMPTED row counts too: the method-aware
  // dispatcher parks any non-turn/start head of a resume-only ref before
  // markAttempted, and a Stop keeps attempted rows, so a row can be attempted
  // and still "submitting" - a new send enqueued behind it would never
  // dispatch while the resume-only session already has no Resume affordance. A
  // turn/start row is the send itself and does not block the carve-out; a row
  // that has left "submitting" (blockedUnknown, canceled) is settled, not
  // queued.
  hasQueuedNonSend: (ref) =>
    outboxEntriesByState(pendingTurnsStore.getState().outbox, ref, "submitting").some(
      (record) => record.method !== "turn/start",
    ),
});

// Retry admission's owner-scoped pending facts: the same projection and the
// same readiness this module renders from. Admission asks these live at the
// press, after the owner lock is taken, so a stale view cannot act on an
// unloaded or already-pending ref.
installRetryPendingProjection({
  isLoaded: (ref) => loadedRefs.has(ref),
  hasOwnPendingTurnInput: (ref) => ownPendingTurnInput(pendingTurnsStore.pendingTurnEntries(ref)),
  isSubmitting: (ref) => pendingTurnsStore.getState().submittingRefs.has(ref),
  load: (ref) => void refreshPendingTurnsProjection(ref),
  begin: beginRetryAdmission,
  end: endRetryAdmission,
  isInFlight: (ref) => retryAdmissions.has(ref),
  markSubmitted: markRetrySubmitted,
  awaitingReflection: (ref, failedTurnId) => retryReflections.get(ref)?.failedTurnId === failedTurnId,
});

// Settlement notifications and model changes are the two facts that can
// resolve a reflection marker. Both subscriptions re-decide from current
// owner state, so no marker outlives what proves it.
pendingTurnsStore.subscribe(() => {
  for (const ref of [...retryReflections.keys()]) reconcileRetryReflection(ref);
});

// The durable-read fence a refresh's targets are decided through, and the
// port it reads them from: readMutationPersistence is already shaped to
// MutationPersistencePort, so no adapter object is needed beyond naming it.
const projectionFence = createMutationProjectionFence<MutationAttachment>();
const persistencePort: MutationPersistencePort<MutationAttachment> = { read: readMutationPersistence };

// The fire-and-forget refresh port both the commit feed and the submission
// runner take, bound once here rather than written inline at each site.
const refreshTarget: (ref?: string) => void = (ref) => void refreshPendingTurnsProjection(ref);

export function refreshPendingTurnsProjection(ref?: string): Promise<boolean> {
  return trackProjectionWork(readProjectionIntoStore(ref));
}

// Un-loads the readiness of every ref a refresh will cover, BEFORE its read
// starts: while the read is in flight its rows are not published yet, so
// trusting the previous projection through that window is exactly the stale
// "loaded, no uncertainty" answer that can fold a resume ahead of a newly
// written blockedUnknown or queued non-send row. A specific refresh covers
// exactly its ref. An all-targets refresh covers every ref this page currently
// tracks - the same refs markProjectionLoaded marks, and every ref the
// resume-only predicate can be asked about (a ref with no tracked model answers
// false regardless of readiness). The resolved read re-establishes readiness
// for the refs it still owns.
function invalidateProjectionLoaded(ref?: string): void {
  if (ref !== undefined) {
    loadedRefs.delete(ref);
    publishPendingReadiness();
    return;
  }
  for (const target of threadsStore.getState().threads.keys()) loadedRefs.delete(target);
  publishPendingReadiness();
}

async function readProjectionIntoStore(ref?: string): Promise<boolean> {
  invalidateProjectionLoaded(ref);
  const accepted = await projectionFence.refresh(persistencePort, ref);
  if (!accepted) return false;
  const { snapshot } = accepted;
  // Provenance is monotonic knowledge about ids rather than a view of the
  // records currently in storage, so it is published from every read the
  // fence accepted and in its own setState: a read a newer generation has
  // already superseded still saw a record of this client's, and the newer
  // read - taken later - may be looking at storage that record has since
  // been settled out of. Skipping it there would leave the id known to
  // nobody.
  pendingTurnsStore.recordSubmittedHere(snapshot);
  // apply() re-decides the accepted targets right here, not at refresh()'s
  // resolution: a live commit's advance() for one of them can land in
  // between, and it must still out-rank this snapshot for that target.
  const targets = accepted.apply();
  pendingTurnsStore.projectSnapshot(targets, snapshot);
  // Readiness follows the fence's own ownership check, at the same moment the
  // snapshot is projected: a read a newer read or a live commit has already
  // superseded is not this ref's current word, so it must not mark the ref
  // loaded - the still-outstanding winning read is the one that will publish
  // the rows, and marks it loaded when it does. Otherwise the predicate would
  // read "loaded, no uncertainty" from a projection the winner has not filled
  // in yet and fold a resume ahead of blocked or queued rows.
  const loaded = markProjectionLoaded(accepted, ref, targets, snapshot);
  if (loaded.length > 0) publishPendingReadiness();
  // A read that just established readiness may be exactly what a parked
  // dispatch was waiting for: a resume-only ref's head send parks while its
  // ref's durable outbox read is in flight (the fail-closed readiness the
  // predicate reads), and nothing else re-attempts it once the read resolves.
  // Hand the refs that (re)gained readiness to the store's own dispatch
  // scheduler - the same explicit hand-off publishAndReconcileThreadHydration
  // makes when its reconciliation opens the gate. A ref with no dispatchable
  // mutation is a no-op there.
  if (loaded.length > 0) {
    // #4342: a ref whose paced-retry timer is live has that timer owning
    // its refused row's re-drive - its fire schedules the same dispatch
    // this hand-off would - so the hand-off skips it rather than redeliver
    // the refused row immediately after its refusal (a reopened row's
    // refresh does run, making this gate live). The timer's fire consumes
    // its own handle, so the gate never outlives what backs it.
    const handoff = loaded.filter((ref) => !transientRefusalRetryPending(ref));
    if (handoff.length > 0) notifyReadyForMutationDispatch(handoff);
  }
  return true;
}

// Marks the refs a resolved durable read covered as loaded, but only the refs
// this read's generation still owns: a ref a newer in-flight read or a live
// commit's advance superseded stays unloaded so the winner marks it. A specific
// read names its ref. A global read covers every ref this page tracks (a
// tracked ref with no durable rows was read and found empty) plus every ref the
// snapshot named, each gated the same way. Returns the refs this call newly
// marked, so the caller can hand exactly those to the dispatch scheduler.
function markProjectionLoaded(
  refresh: MutationProjectionRefresh<MutationAttachment>,
  ref: string | undefined,
  targets: ReadonlySet<string>,
  snapshot: MutationPersistenceSnapshot<MutationAttachment>,
): string[] {
  const loaded: string[] = [];
  const mark = (target: string): void => {
    if (!refresh.stillOwns(target) || loadedRefs.has(target)) return;
    loadedRefs.add(target);
    loaded.push(target);
  };
  if (ref !== undefined) {
    mark(ref);
    return loaded;
  }
  for (const target of targets) {
    mark(target);
  }
  for (const target of threadsStore.getState().threads.keys()) {
    mark(target);
  }
  for (const record of [...snapshot.outbox, ...snapshot.optimistic, ...snapshot.recovery]) {
    mark(record.targetRef);
  }
  return loaded;
}

// The commit feed's fast path (advancing the fence and landing the record
// straight into the store, with no durable read at all) and the refresh it
// triggers for every other changed target both live in the package; this
// binds them to the browser's own durable-mutation feed and the refresh
// declared below.
wireMutationCommitFeed(pendingTurnsStore, projectionFence, { subscribe: subscribeMutationPersistence }, refreshTarget);

// The submission lifecycle's four singletons (store, fence, tracker, refresh),
// bound once here rather than repeated at every submitWithPendingTracking call.
const runSubmission = createSubmissionRunner(pendingTurnsStore, projectionFence, trackProjectionWork, refreshTarget);

export interface SubmitWithPendingTrackingOptions {
  ref: string;
  text: string;
  attachments?: InputAttachment[];
  // The canonical skill selections submitted with this text, for the
  // submitted snapshot: the stored draft clears only when both its text and
  // its selections still match what was sent.
  skillNames?: readonly string[];
  commandNames?: readonly string[];
  recoveryId?: string;
  onFailure: (error: unknown) => void;
}

interface RecoverySubmissionCommit {
  clientMutationId: string;
  draftUnchanged: boolean;
  attachments: InputAttachment[];
}

type SubmissionCommittedListener = (
  ref: string,
  text: string,
  skillNames: readonly string[],
  recovery?: RecoverySubmissionCommit,
  commandNames?: readonly string[],
) => void;
const submissionCommittedListeners = new Set<SubmissionCommittedListener>();

export function subscribeComposerSubmissionCommitted(listener: SubmissionCommittedListener): () => void {
  submissionCommittedListeners.add(listener);
  return () => submissionCommittedListeners.delete(listener);
}

export function useComposerSubmitting(ref: string): boolean {
  return useStore(pendingTurnsStore, (state) => state.submittingRefs.has(ref));
}

// The action resolves at the local IndexedDB commit boundary. Durable state,
// not a component timer or text echo, is the only optimistic lifecycle. The
// begin/epoch-guard/settle-draft/end/refresh sequencing lives in the
// package's submission lifecycle (state/mutation/submission.ts); this binds
// it to the browser's projection tracker and fence, and turns what it
// decided into this file's own composer-submission notification (the
// recovery tray, the draft UI) - neither of which the package names.
export function submitWithPendingTracking(
  opts: SubmitWithPendingTrackingOptions,
  perform: () => Promise<void>,
): Promise<void> {
  const skillNames = [...(opts.skillNames ?? [])];
  return runSubmission(
    {
      ref: opts.ref,
      draftRevisionAtStart: readDraftRevision(opts.ref),
      text: opts.text,
      skillNames,
      commandNames: opts.commandNames,
      onFailure: opts.onFailure,
    },
    perform,
    ({ cleared, draftUnchanged }) => {
      if (!cleared && !opts.recoveryId) return;
      for (const listener of submissionCommittedListeners) {
        try {
          listener(
            opts.ref,
            opts.text,
            skillNames,
            opts.recoveryId
              ? { clientMutationId: opts.recoveryId, draftUnchanged, attachments: opts.attachments ?? [] }
              : undefined,
            opts.commandNames,
          );
        } catch (error) {
          console.error("Composer submission listener failed", error);
        }
      }
    },
  );
}

const NO_ENTRIES: PendingTurnEntry[] = [];
const NO_RECOVERY: MutationRecoveryRecord[] = [];
const NO_BLOCKED: MutationOutboxRecord[] = [];

type ThreadsPortModel = ReturnType<typeof threadsPort.getThreadModel>;

// pendingTurnEntries() reads both pendingTurnsStore's own state and, through
// the threads port, the live thread model - a turn moving from queued to
// started arrives over the wire into threadsStore, not into an outbox
// record, so a subscription to pendingTurnsStore alone would leave a
// component showing a turn as still pending after the hub already started
// it. useSyncExternalStore only re-runs getSnapshot on a subscribe
// notification (or a render for an unrelated reason), so it has to hear from
// both stores; and since pendingTurnEntries() builds a fresh array on every
// call, getSnapshot caches the last one and only replaces it when the
// inputs it actually reads - outbox, optimistic, submittedHere, the thread
// model, and which ref/method were asked for - have themselves changed
// (never the whole pendingTurnsStore state object, which also changes on
// writes this read does not depend on, like beginSubmission/endSubmission's
// submittingRefs or the recovery map), which is what keeps this from
// tearing into an infinite render loop without over-invalidating on those
// unrelated writes.
export function usePendingTurnEntries(ref: string, method?: PendingMethod): PendingTurnEntry[] {
  useEffect(() => {
    void refreshPendingTurnsProjection(ref);
  }, [ref]);

  const subscribe = useCallback(
    (onStoreChange: () => void) => {
      const unsubscribePending = pendingTurnsStore.subscribe(onStoreChange);
      const unsubscribeThreads = threadsStore.subscribe((state, previous) => {
        if (state.threads.get(ref) !== previous.threads.get(ref)) onStoreChange();
      });
      return () => {
        unsubscribePending();
        unsubscribeThreads();
      };
    },
    [ref],
  );

  const cacheRef = useRef<{
    outbox: ReturnType<typeof pendingTurnsStore.getState>["outbox"];
    optimistic: ReturnType<typeof pendingTurnsStore.getState>["optimistic"];
    submittedHere: ReturnType<typeof pendingTurnsStore.getState>["submittedHere"];
    model: ThreadsPortModel;
    ref: string;
    method: PendingMethod | undefined;
    entries: PendingTurnEntry[];
  } | null>(null);
  const getSnapshot = useCallback((): PendingTurnEntry[] => {
    const { outbox, optimistic, submittedHere } = pendingTurnsStore.getState();
    const model = threadsPort.getThreadModel(ref);
    const cached = cacheRef.current;
    if (
      cached &&
      cached.outbox === outbox &&
      cached.optimistic === optimistic &&
      cached.submittedHere === submittedHere &&
      cached.model === model &&
      cached.ref === ref &&
      cached.method === method
    ) {
      return cached.entries;
    }
    const entries = pendingTurnsStore.pendingTurnEntries(ref, method);
    const result = entries.length > 0 ? entries : NO_ENTRIES;
    cacheRef.current = { outbox, optimistic, submittedHere, model, ref, method, entries: result };
    return result;
  }, [ref, method]);

  return useSyncExternalStore(subscribe, getSnapshot);
}

// The same projection read from the stores as they are now, not as a component
// rendered them: for the press handlers that re-derive their verdict at the
// press (stores/liveControls.ts is the rule; this is its pending-send input).
export function pendingTurnEntries(ref: string, method?: PendingMethod): PendingTurnEntry[] {
  const entries = pendingTurnsStore.pendingTurnEntries(ref, method);
  return entries.length > 0 ? entries : NO_ENTRIES;
}

export function useAwaitingFirstFrameSend(ref: string): boolean {
  const model = useThreadsStore((state) => state.threads.get(ref));
  return useMemo(() => awaitingFirstFrameSend(model), [model]);
}

export function useRecoveryEntries(ref: string): MutationRecoveryRecord[] {
  const recovery = useStore(pendingTurnsStore, (state) => state.recovery);
  useEffect(() => {
    void refreshPendingTurnsProjection(ref);
  }, [ref]);
  return useMemo(() => {
    const records = recoveryEntries(recovery, ref);
    return records.length > 0 ? records : NO_RECOVERY;
  }, [recovery, ref]);
}

// The shared body of the two outbox-state selectors below: one stable snapshot
// per state, sorted by intent sequence the way the durable rows render.
function useOutboxRecordsByState(ref: string, state: "blockedUnknown" | "canceled"): MutationOutboxRecord[] {
  const outbox = useStore(pendingTurnsStore, (state) => state.outbox);
  useEffect(() => {
    void refreshPendingTurnsProjection(ref);
  }, [ref]);
  return useMemo(() => {
    const records = outboxEntriesByState(outbox, ref, state);
    return records.length > 0 ? records : NO_BLOCKED;
  }, [outbox, ref, state]);
}

// Delivery-uncertain rows only. Session's restart notice reads exactly this
// selector: a canceled row is a settled fact (provably never sent), not a
// recovery obligation, so it must stay out of that notice.
export function useBlockedMutationEntries(ref: string): MutationOutboxRecord[] {
  return useOutboxRecordsByState(ref, "blockedUnknown");
}

// Stop-canceled rows (stop-cancellation-outbox §6): QueueStrip surfaces them
// in the same durable-rows slot as blocked ones, where their explicit Retry
// affordance lives.
export function useCanceledMutationEntries(ref: string): MutationOutboxRecord[] {
  return useOutboxRecordsByState(ref, "canceled");
}

// A durable mutation and the projection refresh that publishes it are one
// operation: nothing has observed the mutation until the refresh has run.
function mutateThenRefresh<T>(ref: string, mutate: () => Promise<T>): Promise<T> {
  return trackProjectionWork(
    (async () => {
      const result = await mutate();
      await refreshPendingTurnsProjection(ref);
      return result;
    })(),
  );
}

export type BlockedRetryOutcome = { released: true } | { released: false; current: MutationOutboxRecord | undefined };

// A retry that did not release the row reports the record as it stands after
// the refresh (undefined once it left the outbox), so the caller decides on
// the state that refresh observed rather than a pre-refresh snapshot another
// tab may have made moot. The decision read belongs to the same tracked
// operation: nothing about the press is settled until it has run.
export function retryBlockedPendingTurn(clientMutationId: string, ref: string): Promise<BlockedRetryOutcome> {
  return trackProjectionWork(
    (async (): Promise<BlockedRetryOutcome> => {
      if (await mutateThenRefresh(ref, () => retryBlockedMutation(clientMutationId))) return { released: true };
      const { outbox } = await readMutationPersistence(ref);
      return { released: false, current: outbox.find((entry) => entry.clientMutationId === clientMutationId) };
    })(),
  );
}

export function updateRecoveryPendingTurn(
  clientMutationId: string,
  ref: string,
  text: string,
  attachments: InputAttachment[],
  skillNames?: readonly string[],
  commandNames?: readonly string[],
  mentions?: readonly ComposerMention[],
): Promise<boolean> {
  // Composer serializes edits before resending. A committed edit must release
  // that chain even when the recovery tray cannot refresh yet.
  return trackProjectionWork(
    (async () => {
      const updated = await updateRecoveryMutation(
        clientMutationId,
        ref,
        text,
        attachments,
        skillNames,
        commandNames,
        mentions,
      );
      void refreshPendingTurnsProjection(ref);
      return updated;
    })(),
  );
}

export function discardRecoveryPendingTurn(
  clientMutationId: string,
  ref: string,
  shouldDiscard?: () => boolean,
): Promise<boolean> {
  return mutateThenRefresh(ref, () => discardRecoveryMutation(clientMutationId, ref, shouldDiscard));
}

export function resendRecoveryPendingTurn(
  clientMutationId: string,
  ref: string,
  route: ComposerMutationRoute,
  text: string,
  attachments: InputAttachment[],
  skillNames?: readonly string[],
  commandNames?: readonly string[],
  mentions?: readonly ComposerMention[],
): Promise<boolean> {
  // Resend publishes its committed handoff directly. Reading the recovery
  // tray again cannot hold up a submission that already has a durable owner.
  return trackProjectionWork(
    (async () => {
      const record = await resendRecoveryMutation(
        clientMutationId,
        ref,
        route,
        text,
        attachments,
        skillNames,
        commandNames,
        mentions,
      );
      void refreshPendingTurnsProjection(ref);
      return record !== undefined;
    })(),
  );
}

export function resetPendingTurnsStoreForTests(): void {
  projectionFence.reset();
  // The epoch bump already voids anything still running against the previous
  // test's storage, so it is not this test's projection work to wait for.
  clearProjectionWorkForTests();
  loadedRefs.clear();
  publishPendingReadiness();
  retryAdmissions.clear();
  publishRetryAdmissions();
  retryReflections.clear();
  publishRetryReflections();
  pendingTurnsStore.setState({
    outbox: new Map(),
    optimistic: new Map(),
    recovery: new Map(),
    submittingRefs: new Set(),
    submittedHere: new Map(),
  });
}

// Test-only: publish an already-durable outbox record into this projection
// store synchronously, in the same task as a press, so a test can stage the
// window between a render and a press - the render-time read a component holds
// cannot have caught up, while the live hasBlockedUnknown/readMutationPersistence
// reads have. Mirrors resetPendingTurnsStoreForTests.
export function publishOutboxRecordForTests(record: MutationOutboxRecord): void {
  pendingTurnsStore.setState((state) => ({
    outbox: new Map(state.outbox).set(record.clientMutationId, record),
  }));
}

// Keep the singleton projection warm when an authoritative pendingMutations
// snapshot changes even if no pending component is currently mounted.
threadsStore.subscribe((state, previous) => {
  // Publication is two store steps: the model, then the per-ref publication
  // count. Watch both, and only the refs whose model or hydration actually
  // moved - a streaming publication for one session must not reconcile every
  // open conversation's reflection marker or refresh every projection.
  if (state.threads === previous.threads && state.hydrations === previous.hydrations) return;
  for (const ref of new Set([...state.threads.keys(), ...retryReflections.keys()])) {
    const modelChanged = state.threads.get(ref) !== previous.threads.get(ref);
    const hydrationChanged = state.hydrations.get(ref) !== previous.hydrations.get(ref);
    if (modelChanged) void refreshPendingTurnsProjection(ref);
    if ((modelChanged || hydrationChanged) && retryReflections.has(ref)) reconcileRetryReflection(ref);
  }
});
