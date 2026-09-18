import {
  awaitingFirstFrameSend,
  blockedEntries,
  createMutationProjectionFence,
  createMutationProjectionWorkTracker,
  createPendingTurnsStore,
  type MutationPersistencePort,
  type PendingTurnsDraftPort,
  type PendingTurnsThreadsPort,
  recoveryEntries,
  replaceTargetRecords,
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
import {
  type ComposerMutationRoute,
  discardRecoveryMutation,
  type InputAttachment,
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
// op below (the generation fencing, IndexedDB reads, settle tracking) stays
// here: it is genuinely web-specific, not part of what moved.
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

// The durable-read fence a refresh's targets are decided through, and the
// port it reads them from: readMutationPersistence is already shaped to
// MutationPersistencePort, so no adapter object is needed beyond naming it.
const projectionFence = createMutationProjectionFence<MutationAttachment>();
const persistencePort: MutationPersistencePort<MutationAttachment> = { read: readMutationPersistence };

// Every durable projection operation below is registered with this tracker
// while it runs. The work is the mutation runtime's start plus real IndexedDB
// reads and writes, so its wall time scales with machine load - a
// mount-to-activation latency of 124-1246ms was measured for the Composer's
// own path (kata 3c7t). That leaves a test with nothing to await but the
// operation itself: polling its side effects against a fixed window is a
// race, not an assertion. The stall tripwire and the macrotask yield the
// tracker settles through are the package's; this binds them to the
// browser's own timers and a MessageChannel hop.
const projectionWorkTracker = createMutationProjectionWorkTracker({
  setTimeout: (callback, milliseconds) => setTimeout(callback, milliseconds),
  clearTimeout: (timerId) => clearTimeout(timerId as ReturnType<typeof setTimeout>),
  yieldMacrotask: () =>
    new Promise<void>((resolve) => {
      const hop = new MessageChannel();
      hop.port1.onmessage = () => {
        hop.port1.close();
        resolve();
      };
      hop.port2.postMessage(undefined);
    }),
});

function trackProjectionWork<T>(work: Promise<T>): Promise<T> {
  return projectionWorkTracker.track(work);
}

// Awaits whatever projection work is outstanding right now and reports how
// much that was. Callers repeat until it reports zero, flushing React in
// between: the components start this work from effects, so only a flush can
// reveal whether anything is left.
export async function settlePendingTurnsProjectionForTests(): Promise<number> {
  return projectionWorkTracker.settle();
}

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
  const targets = accepted.apply();
  pendingTurnsStore.setState((state) => ({
    outbox: replaceTargetRecords(state.outbox, targets, snapshot.outbox),
    optimistic: replaceTargetRecords(state.optimistic, targets, snapshot.optimistic),
    recovery: replaceTargetRecords(state.recovery, targets, snapshot.recovery),
  }));
  return true;
}

// The commit feed's fast path (advancing the fence and landing the record
// straight into the store, with no durable read at all) and the refresh it
// triggers for every other changed target both live in the package; this
// binds them to the browser's own durable-mutation feed and the refresh
// declared below.
wireMutationCommitFeed(pendingTurnsStore, projectionFence, { subscribe: subscribeMutationPersistence }, (ref) => {
  void refreshPendingTurnsProjection(ref);
});

export interface SubmitWithPendingTrackingOptions {
  ref: string;
  method: PendingMethod;
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
// not a component timer or text echo, is the only optimistic lifecycle.
//
// Registered with trackProjectionWork for its whole duration, not just for the
// refresh it ends with. Every other durable path here is tracked from the call
// that starts it; this one used to register nothing until `perform` had already
// resolved, which left a window where a settle round found the set empty and
// reported the projection settled while a send was still in flight (kata 3p22).
// Tracking from the click is what makes one zero round genuine proof.
export function submitWithPendingTracking(
  opts: SubmitWithPendingTrackingOptions,
  perform: () => Promise<void>,
): Promise<void> {
  if (!pendingTurnsStore.beginSubmission(opts.ref)) {
    return Promise.reject(new Error("A message submission is already pending for this task"));
  }
  const epoch = projectionFence.epoch();
  const draftRevisionAtStart = readDraftRevision(opts.ref);
  return trackProjectionWork(
    (async () => {
      try {
        try {
          await perform();
        } catch (error) {
          opts.onFailure(error);
          throw error;
        }
        // Submission ownership outlives a mounted composer. A retired mount
        // must not clear a newer draft written after a tab switch.
        if (epoch === projectionFence.epoch()) {
          const skillNames = [...(opts.skillNames ?? [])];
          const { cleared: clearStoredDraft, draftUnchanged } = pendingTurnsStore.settleSubmittedDraft(opts.ref, {
            draftRevisionAtStart,
            text: opts.text,
            skillNames,
          });
          if (clearStoredDraft || opts.recoveryId) {
            for (const listener of submissionCommittedListeners) {
              try {
                listener(
                  opts.ref,
                  opts.text,
                  skillNames,
                  opts.recoveryId
                    ? {
                        clientMutationId: opts.recoveryId,
                        draftUnchanged,
                        attachments: opts.attachments ?? [],
                      }
                    : undefined,
                );
              } catch (error) {
                console.error("Composer submission listener failed", error);
              }
            }
          }
        }
      } finally {
        if (epoch === projectionFence.epoch()) {
          pendingTurnsStore.endSubmission(opts.ref);
        }
        // Projection reads own their tracking, but cannot delay or change the
        // result of a submission whose durable outcome is already known.
        void refreshPendingTurnsProjection(opts.ref);
      }
    })(),
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

export function useBlockedMutationEntries(ref: string): MutationOutboxRecord[] {
  const outbox = useStore(pendingTurnsStore, (state) => state.outbox);
  useEffect(() => {
    void refreshPendingTurnsProjection(ref);
  }, [ref]);
  return useMemo(() => {
    const records = blockedEntries(outbox, ref);
    return records.length > 0 ? records : NO_BLOCKED;
  }, [outbox, ref]);
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

export function retryBlockedPendingTurn(clientMutationId: string, ref: string): Promise<boolean> {
  return mutateThenRefresh(ref, () => retryBlockedMutation(clientMutationId));
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
  projectionWorkTracker.clear();
  pendingTurnsStore.setState({
    outbox: new Map(),
    optimistic: new Map(),
    recovery: new Map(),
    submittingRefs: new Set(),
    submittedHere: new Set(),
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
