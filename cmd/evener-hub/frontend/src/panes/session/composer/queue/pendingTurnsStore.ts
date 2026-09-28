import {
  awaitingFirstFrameSend,
  createMutationProjectionFence,
  createPendingTurnsStore,
  createSubmissionRunner,
  type MutationPersistencePort,
  outboxEntriesByState,
  type PendingTurnsDraftPort,
  type PendingTurnsThreadsPort,
  recoveryEntries,
  wireMutationCommitFeed,
} from "@evener/appwire-client/state/mutation";
import { useCallback, useEffect, useMemo, useRef, useSyncExternalStore } from "react";
import { useStore } from "zustand";
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
  readMutationPersistence,
  resendRecoveryMutation,
  retryBlockedMutation,
  subscribeMutationPersistence,
  threadsStore,
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

// Publish this projection's synchronous delivery-uncertain read into the
// threads store's shared resume-only predicate (installResumeOnlyProjection):
// the predicate the press, enqueue and dispatch paths share cannot import this
// module (it imports threads.ts), so it reaches the blockedUnknown rows through
// this registered read instead. Reading the live store state on every call
// keeps the predicate current with the last durable read this projection
// published, exactly as the composer's own useBlockedMutationEntries does.
installResumeOnlyProjection({
  hasBlockedUnknown: (ref) =>
    outboxEntriesByState(pendingTurnsStore.getState().outbox, ref, "blockedUnknown").length > 0,
  // A queued, not-yet-attempted non-turn/start row (still "submitting", not yet
  // handed to the wire) parks at the target's FIFO: a turn/start queued behind
  // it never dispatches, so a ref holding one is not the foldable shape. A
  // turn/start row is the send itself and does not block the carve-out, and a
  // settled row (blockedUnknown/canceled, or attempted) is not "queued" here.
  hasQueuedNonSend: (ref) =>
    outboxEntriesByState(pendingTurnsStore.getState().outbox, ref, "submitting").some(
      (record) => record.method !== "turn/start" && record.attempted !== true,
    ),
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

async function readProjectionIntoStore(ref?: string): Promise<boolean> {
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
  pendingTurnsStore.projectSnapshot(accepted.apply(), snapshot);
  return true;
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
): Promise<boolean> {
  // Composer serializes edits before resending. A committed edit must release
  // that chain even when the recovery tray cannot refresh yet.
  return trackProjectionWork(
    (async () => {
      const updated = await updateRecoveryMutation(clientMutationId, ref, text, attachments, skillNames);
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
): Promise<boolean> {
  // Resend publishes its committed handoff directly. Reading the recovery
  // tray again cannot hold up a submission that already has a durable owner.
  return trackProjectionWork(
    (async () => {
      const record = await resendRecoveryMutation(clientMutationId, ref, route, text, attachments, skillNames);
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
  pendingTurnsStore.setState({
    outbox: new Map(),
    optimistic: new Map(),
    recovery: new Map(),
    submittingRefs: new Set(),
    submittedHere: new Map(),
  });
}

// Keep the singleton projection warm when an authoritative pendingMutations
// snapshot changes even if no pending component is currently mounted.
threadsStore.subscribe((state, previous) => {
  if (state.threads === previous.threads) return;
  for (const ref of state.threads.keys()) {
    if (state.threads.get(ref) !== previous.threads.get(ref)) void refreshPendingTurnsProjection(ref);
  }
});
