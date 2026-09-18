import {
  createPendingTurnsStore,
  type PendingTurnsDraftPort,
  type PendingTurnsThreadsPort,
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

let refreshGeneration = 0;
let allTargetsRefreshGeneration = 0;
const refreshGenerations = new Map<string, number>();
let refreshEpoch = 0;

function replaceTargetRecords<T extends { clientMutationId: string; targetRef: string }>(
  current: Map<string, T>,
  targets: ReadonlySet<string>,
  records: T[],
): Map<string, T> {
  const next = new Map(current);
  for (const [id, record] of next) {
    if (targets.has(record.targetRef)) next.delete(id);
  }
  for (const record of records) {
    if (targets.has(record.targetRef)) next.set(record.clientMutationId, record);
  }
  return next;
}

// Every durable projection operation below is registered here while it runs.
// The work is the mutation runtime's start plus real IndexedDB reads and
// writes, so its wall time scales with machine load - a mount-to-activation
// latency of 124-1246ms was measured for the Composer's own path (kata 3c7t).
// That leaves a test with nothing to await but the operation itself: polling
// its side effects against a fixed window is a race, not an assertion.
const inFlightProjectionWork = new Set<Promise<unknown>>();

function trackProjectionWork<T>(work: Promise<T>): Promise<T> {
  inFlightProjectionWork.add(work);
  return work.finally(() => {
    inFlightProjectionWork.delete(work);
  });
}

// A round waits on real durable work, so its wall time scales with machine
// load - but no amount of load turns work that has no completion left into
// work that finishes. A test that stalls storage and then flushes without
// releasing it parks HERE, inside the act() below, until vitest abandons the
// whole test at its own timeout - and an abandoned act() leaves React's act
// queue open for the rest of the FILE, so every later render produces nothing
// and one hang becomes dozens of failures (issue #1187). This bound exists to
// make that one named failure in the test that caused it, nothing else: it is
// a tripwire for a stall, never pacing. The slowest round measured across the
// whole web suite (10373 tests) under 32-way CPU contention was 165ms.
const SETTLE_STALL_TRIPWIRE_MS = 4_000;

async function awaitOutstandingProjectionWork(outstanding: Promise<unknown>[]): Promise<void> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  const tripwire = new Promise<never>((_resolve, reject) => {
    timer = setTimeout(
      () =>
        reject(
          new Error(
            `pending-turns projection work stalled: ${outstanding.length} operation(s) still unsettled after ${SETTLE_STALL_TRIPWIRE_MS}ms - release whatever storage or transport this test is holding before flushing`,
          ),
        ),
      SETTLE_STALL_TRIPWIRE_MS,
    );
  });
  try {
    await Promise.race([Promise.allSettled(outstanding), tripwire]);
  } finally {
    clearTimeout(timer);
  }
}

// Awaits whatever projection work is outstanding right now and reports how
// much that was. Callers repeat until it reports zero, flushing React in
// between: the components start this work from effects, so only a flush can
// reveal whether anything is left. The macrotask hop drains every pending
// microtask, so work chained onto an operation that just finished has already
// registered itself by the time the caller looks again.
export async function settlePendingTurnsProjectionForTests(): Promise<number> {
  const outstanding = [...inFlightProjectionWork];
  await awaitOutstandingProjectionWork(outstanding);
  await new Promise<void>((resolve) => {
    const hop = new MessageChannel();
    hop.port1.onmessage = () => {
      hop.port1.close();
      resolve();
    };
    hop.port2.postMessage(undefined);
  });
  return outstanding.length;
}

export function refreshPendingTurnsProjection(ref?: string): Promise<boolean> {
  return trackProjectionWork(readProjectionIntoStore(ref));
}

async function readProjectionIntoStore(ref?: string): Promise<boolean> {
  const epoch = refreshEpoch;
  const generation = ++refreshGeneration;
  // Starting a newer read supersedes older snapshots even if that read fails.
  if (ref === undefined) allTargetsRefreshGeneration = generation;
  else refreshGenerations.set(ref, generation);
  try {
    const snapshot = await readMutationPersistence(ref);
    if (refreshEpoch !== epoch) return false;
    // Provenance is monotonic knowledge about ids rather than a view of the
    // records currently in storage, so it is published from every read of this
    // epoch and in its own setState: a read a newer generation has already
    // superseded still saw a record of this client's, and the newer read - taken
    // later - may be looking at storage that record has since been settled out
    // of. Skipping it there would leave the id known to nobody.
    pendingTurnsStore.recordSubmittedHere(snapshot);
    // Reads of all targets and reads of one target share the same ordering.
    // An old all-target snapshot must not erase a newer local commit.
    const targets = new Set(
      ref === undefined
        ? [
            ...refreshGenerations.keys(),
            ...snapshot.outbox.map((record) => record.targetRef),
            ...snapshot.optimistic.map((record) => record.targetRef),
            ...snapshot.recovery.map((record) => record.targetRef),
          ]
        : [ref],
    );
    for (const target of targets) {
      if (generation < Math.max(allTargetsRefreshGeneration, refreshGenerations.get(target) ?? 0))
        targets.delete(target);
      else refreshGenerations.set(target, generation);
    }
    pendingTurnsStore.setState((state) => ({
      outbox: replaceTargetRecords(state.outbox, targets, snapshot.outbox),
      optimistic: replaceTargetRecords(state.optimistic, targets, snapshot.optimistic),
      recovery: replaceTargetRecords(state.recovery, targets, snapshot.recovery),
    }));
    return true;
  } catch {
    // A read failure cannot discard the last durable projection. Lifecycle
    // discovery or the next explicit action retries the same IndexedDB read.
    return false;
  }
}

subscribeMutationPersistence((targetRefs, committed) => {
  if (committed) {
    const { record, recoveryId } = committed;
    refreshGenerations.set(record.targetRef, ++refreshGeneration);
    pendingTurnsStore.recordSubmittedHere({ outbox: [record], optimistic: [] });
    pendingTurnsStore.setState((state) => {
      const recovery = new Map(state.recovery);
      if (recoveryId) recovery.delete(recoveryId);
      return { outbox: new Map(state.outbox).set(record.clientMutationId, record), recovery };
    });
  }
  if (targetRefs.length === 0) {
    void refreshPendingTurnsProjection();
    return;
  }
  for (const ref of targetRefs) void refreshPendingTurnsProjection(ref);
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
  const epoch = refreshEpoch;
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
        if (epoch === refreshEpoch) {
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
        if (epoch === refreshEpoch) {
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
  return useMemo(() => {
    const activeTurn = model?.turns.find((turn) => turn.id === model.activeTurnId);
    if (!activeTurn) return false;
    let sawIdentifiedUserMessage = false;
    for (const item of activeTurn.items) {
      if (item.type === "userMessage" && item.clientMutationId) {
        sawIdentifiedUserMessage = true;
        continue;
      }
      if (sawIdentifiedUserMessage && item.type !== "systemMessage") return false;
    }
    return sawIdentifiedUserMessage;
  }, [model]);
}

export function useRecoveryEntries(ref: string): MutationRecoveryRecord[] {
  const recovery = useStore(pendingTurnsStore, (state) => state.recovery);
  useEffect(() => {
    void refreshPendingTurnsProjection(ref);
  }, [ref]);
  return useMemo(() => {
    const records = [...recovery.values()]
      .filter((record) => record.targetRef === ref)
      .sort((left, right) => left.intentSequence - right.intentSequence);
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
    const records = [...outbox.values()]
      .filter((record) => record.targetRef === ref && record.state === state)
      .sort((left, right) => left.intentSequence - right.intentSequence);
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
  refreshEpoch += 1;
  refreshGeneration = 0;
  allTargetsRefreshGeneration = 0;
  refreshGenerations.clear();
  // The epoch bump already voids anything still running against the previous
  // test's storage, so it is not this test's projection work to wait for.
  inFlightProjectionWork.clear();
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
