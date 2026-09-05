import { act, useEffect, useMemo } from "react";
import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import type {
  MutationOptimisticRecord,
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
import { clearDraft, readDraft, readDraftRevision } from "../draft";
import { type PendingMethod, type PendingTurnEntry, reconcilePendingEntries } from "./pendingReconcile";

export type { PendingMethod, PendingTurnEntry } from "./pendingReconcile";

interface PendingTurnsStoreState {
  outbox: Map<string, MutationOutboxRecord>;
  optimistic: Map<string, MutationOptimisticRecord>;
  recovery: Map<string, MutationRecoveryRecord>;
  submittingRefs: ReadonlySet<string>;
  // Every client mutation id this client's own durable projection has held, for
  // as long as this page lives. The durable records themselves are the primary
  // evidence of "this client submitted it", and they are deliberately short
  // lived: publishing an authoritative read settles every identity the daemon
  // reports back out of storage (threads.ts's reconcileIdentities), including
  // the still-unreflected sends it lists in pendingMutations. Provenance has to
  // outlive the record, because routing asks about it after the hydrate too -
  // see reconcilePendingEntries.
  //
  // Local commits publish their identity directly, before the composer can
  // accept another message. Projection reads discover identities from reloads
  // and other tabs; a stalled read cannot hide a commit made by this page.
  //
  // Ids only, one per mutation this page submits, never pruned - a page that
  // submits enough sends for that to matter has far larger records than these
  // in the durable stores it is reading from.
  submittedHere: ReadonlySet<string>;
}

const pendingTurnsStore = createStore<PendingTurnsStoreState>(() => ({
  outbox: new Map(),
  optimistic: new Map(),
  recovery: new Map(),
  submittingRefs: new Set(),
  submittedHere: new Set(),
}));

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

// Awaits whatever projection work is outstanding right now and reports how
// much that was. Callers repeat until it reports zero, flushing React in
// between: the components start this work from effects, so only a flush can
// reveal whether anything is left. The macrotask hop drains every pending
// microtask, so work chained onto an operation that just finished has already
// registered itself by the time the caller looks again.
export async function settlePendingTurnsProjectionForTests(): Promise<number> {
  const outstanding = [...inFlightProjectionWork];
  await Promise.allSettled(outstanding);
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

// The repeat loop every test wants, hoisted here because four test files had
// carried a byte-identical copy of it: run rounds until one of them finds
// nothing outstanding. The bound is a tripwire for a livelock - it throws
// rather than letting a test pass on a half-settled projection.
//
// What it can settle is exactly what registers with trackProjectionWork, so a
// round that found nothing is proof that nothing is left only while every
// durable path registers before it can be observed. Every path in THIS file
// does: the last one that did not was submitWithPendingTracking, which
// registered nothing until its own work had already finished, and a flush
// starting in that window declared the projection settled with a send still in
// flight (kata 3p22).
//
// That is a claim about this file, not about every caller. The round below
// snapshots synchronously, in the same tick as the call, so a caller that
// starts durable work which only registers a microtask later is invisible to
// it - but only if that same caller flushes without yielding first. Composer's
// queueRecoveryPersistence chains that way and every one of its call sites
// yields, which is why it was measured unreachable rather than fixed: zero
// occurrences in 1422 test executions under load (kata 5meh, closed as an
// audit).
//
// So the hazard is the pattern, not that instance. A new path that registers
// late reopens it as a load-sensitive false green rather than a failure.
// pendingTurnsStore's "a flush cannot settle while a submit is still in
// flight" pins the property for the paths here.
export async function flushPendingTurnsProjectionForTests(): Promise<void> {
  for (let round = 0; round < 10; round += 1) {
    let awaited = 0;
    await act(async () => {
      awaited = await settlePendingTurnsProjectionForTests();
    });
    if (awaited === 0) return;
  }
  throw new Error("pending-turns projection never settled");
}

export function refreshPendingTurnsProjection(ref?: string): Promise<boolean> {
  return trackProjectionWork(readProjectionIntoStore(ref));
}

function recordSubmittedHere(snapshot: {
  outbox: MutationOutboxRecord[];
  optimistic: MutationOptimisticRecord[];
}): void {
  const known = pendingTurnsStore.getState().submittedHere;
  const discovered = [...snapshot.outbox, ...snapshot.optimistic]
    .map((record) => record.clientMutationId)
    .filter((id) => !known.has(id));
  if (discovered.length === 0) return;
  pendingTurnsStore.setState((state) => ({ submittedHere: new Set([...state.submittedHere, ...discovered]) }));
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
    recordSubmittedHere(snapshot);
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
    recordSubmittedHere({ outbox: [record], optimistic: [] });
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
  recoveryId?: string;
  onFailure: (error: unknown) => void;
}

interface RecoverySubmissionCommit {
  clientMutationId: string;
  draftUnchanged: boolean;
  attachments: InputAttachment[];
}

type SubmissionCommittedListener = (ref: string, text: string, recovery?: RecoverySubmissionCommit) => void;
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
  if (pendingTurnsStore.getState().submittingRefs.has(opts.ref)) {
    return Promise.reject(new Error("A message submission is already pending for this task"));
  }
  const epoch = refreshEpoch;
  const draftRevision = readDraftRevision(opts.ref);
  pendingTurnsStore.setState((state) => ({ submittingRefs: new Set(state.submittingRefs).add(opts.ref) }));
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
        const draftUnchanged = readDraftRevision(opts.ref) === draftRevision;
        const clearStoredDraft = draftUnchanged && readDraft(opts.ref) === opts.text;
        if (epoch === refreshEpoch && (clearStoredDraft || opts.recoveryId)) {
          if (clearStoredDraft) clearDraft(opts.ref);
          for (const listener of submissionCommittedListeners) {
            try {
              listener(
                opts.ref,
                opts.text,
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
      } finally {
        if (epoch === refreshEpoch) {
          pendingTurnsStore.setState((state) => {
            const submittingRefs = new Set(state.submittingRefs);
            submittingRefs.delete(opts.ref);
            return { submittingRefs };
          });
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

export function usePendingTurnEntries(ref: string, method?: PendingMethod): PendingTurnEntry[] {
  const outbox = useStore(pendingTurnsStore, (state) => state.outbox);
  const optimistic = useStore(pendingTurnsStore, (state) => state.optimistic);
  const submittedHere = useStore(pendingTurnsStore, (state) => state.submittedHere);
  const model = useThreadsStore((state) => state.threads.get(ref));
  useEffect(() => {
    void refreshPendingTurnsProjection(ref);
  }, [ref]);
  return useMemo(() => {
    const matches = reconcilePendingEntries(
      ref,
      [...outbox.values(), ...optimistic.values()],
      model,
      submittedHere,
    ).filter((entry) => method === undefined || entry.method === method);
    return matches.length > 0 ? matches : NO_ENTRIES;
  }, [outbox, optimistic, submittedHere, model, ref, method]);
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

export function useBlockedMutationEntries(ref: string): MutationOutboxRecord[] {
  const outbox = useStore(pendingTurnsStore, (state) => state.outbox);
  useEffect(() => {
    void refreshPendingTurnsProjection(ref);
  }, [ref]);
  return useMemo(() => {
    const records = [...outbox.values()]
      .filter((record) => record.targetRef === ref && record.state === "blockedUnknown")
      .sort((left, right) => left.intentSequence - right.intentSequence);
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
): Promise<boolean> {
  // Composer serializes edits before resending. A committed edit must release
  // that chain even when the recovery tray cannot refresh yet.
  return trackProjectionWork(
    (async () => {
      const updated = await updateRecoveryMutation(clientMutationId, ref, text, attachments);
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
): Promise<boolean> {
  // Resend publishes its committed handoff directly. Reading the recovery
  // tray again cannot hold up a submission that already has a durable owner.
  return trackProjectionWork(
    (async () => {
      const record = await resendRecoveryMutation(clientMutationId, ref, route, text, attachments);
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
