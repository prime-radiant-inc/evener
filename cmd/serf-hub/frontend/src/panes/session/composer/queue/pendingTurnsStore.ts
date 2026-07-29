import { useEffect, useMemo } from "react";
import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import type { InputItem } from "../../../../protocol/types.gen";
import type {
  MutationOptimisticRecord,
  MutationOutboxRecord,
  MutationRecoveryRecord,
} from "../../../../stores/mutationOutbox";
import type { InputAttachment } from "../../../../stores/threads";
import {
  readMutationPersistence,
  resendRecoveryMutation,
  retryBlockedMutation,
  subscribeMutationPersistence,
  threadsStore,
  updateRecoveryMutation,
  useThreadsStore,
} from "../../../../stores/threads";
import { type PendingMethod, type PendingTurnEntry, reconcilePendingEntries } from "./pendingReconcile";

export type { PendingMethod, PendingTurnEntry } from "./pendingReconcile";

interface PendingTurnsStoreState {
  outbox: Map<string, MutationOutboxRecord>;
  optimistic: Map<string, MutationOptimisticRecord>;
  recovery: Map<string, MutationRecoveryRecord>;
}

const pendingTurnsStore = createStore<PendingTurnsStoreState>(() => ({
  outbox: new Map(),
  optimistic: new Map(),
  recovery: new Map(),
}));

const refreshGenerations = new Map<string, number>();
const appliedRefreshGenerations = new Map<string, number>();
let refreshEpoch = 0;

function replaceTargetRecords<T extends { clientMutationId: string; targetRef: string }>(
  current: Map<string, T>,
  targetRef: string | undefined,
  records: T[],
): Map<string, T> {
  if (targetRef === undefined) return new Map(records.map((record) => [record.clientMutationId, record]));
  const next = new Map(current);
  for (const [id, record] of next) {
    if (record.targetRef === targetRef) next.delete(id);
  }
  for (const record of records) next.set(record.clientMutationId, record);
  return next;
}

export async function refreshPendingTurnsProjection(ref?: string): Promise<void> {
  const epoch = refreshEpoch;
  const key = ref ?? "*";
  const generation = (refreshGenerations.get(key) ?? 0) + 1;
  refreshGenerations.set(key, generation);
  try {
    const snapshot = await readMutationPersistence(ref);
    if (refreshEpoch !== epoch || generation < (appliedRefreshGenerations.get(key) ?? 0)) return;
    appliedRefreshGenerations.set(key, generation);
    pendingTurnsStore.setState((state) => ({
      outbox: replaceTargetRecords(state.outbox, ref, snapshot.outbox),
      optimistic: replaceTargetRecords(state.optimistic, ref, snapshot.optimistic),
      recovery: replaceTargetRecords(state.recovery, ref, snapshot.recovery),
    }));
  } catch {
    // A read failure cannot discard the last durable projection. Lifecycle
    // discovery or the next explicit action retries the same IndexedDB read.
  }
}

subscribeMutationPersistence((targetRefs) => {
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
  onFailure: (error: unknown) => void;
}

// The action resolves at the local IndexedDB commit boundary. Durable state,
// not a component timer or text echo, is the only optimistic lifecycle.
export async function submitWithPendingTracking(
  opts: SubmitWithPendingTrackingOptions,
  perform: () => Promise<void>,
): Promise<void> {
  try {
    await perform();
  } catch (error) {
    opts.onFailure(error);
    throw error;
  } finally {
    await refreshPendingTurnsProjection(opts.ref);
  }
}

const NO_ENTRIES: PendingTurnEntry[] = [];
const NO_RECOVERY: MutationRecoveryRecord[] = [];
const NO_BLOCKED: MutationOutboxRecord[] = [];

export function usePendingTurnEntries(ref: string, method?: PendingMethod): PendingTurnEntry[] {
  const outbox = useStore(pendingTurnsStore, (state) => state.outbox);
  const optimistic = useStore(pendingTurnsStore, (state) => state.optimistic);
  const model = useThreadsStore((state) => state.threads.get(ref));
  useEffect(() => {
    void refreshPendingTurnsProjection(ref);
  }, [ref]);
  return useMemo(() => {
    const matches = reconcilePendingEntries(ref, [...outbox.values(), ...optimistic.values()], model).filter(
      (entry) => method === undefined || entry.method === method,
    );
    return matches.length > 0 ? matches : NO_ENTRIES;
  }, [outbox, optimistic, model, ref, method]);
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

export async function retryBlockedPendingTurn(clientMutationId: string, ref: string): Promise<boolean> {
  const retried = await retryBlockedMutation(clientMutationId);
  await refreshPendingTurnsProjection(ref);
  return retried;
}

export async function updateRecoveryText(record: MutationRecoveryRecord, text: string): Promise<boolean> {
  const input = Array.isArray(record.payload.input) ? (record.payload.input as InputItem[]) : [];
  const nextInput = input.filter((item) => item.type !== "text");
  if (text.trim()) nextInput.unshift({ type: "text", text });
  const payload = { ...record.payload, input: nextInput };
  const display =
    record.optimisticDisplay && typeof record.optimisticDisplay === "object"
      ? { ...record.optimisticDisplay, input: nextInput }
      : { method: record.method, input: nextInput };
  const updated = await updateRecoveryMutation(record.clientMutationId, payload, display);
  await refreshPendingTurnsProjection(record.targetRef);
  return updated;
}

export async function resendRecoveryPendingTurn(
  clientMutationId: string,
  ref: string,
  threadId?: string,
): Promise<boolean> {
  const resent = await resendRecoveryMutation(clientMutationId, ref, threadId);
  await refreshPendingTurnsProjection(ref);
  return resent !== undefined;
}

export function resetPendingTurnsStoreForTests(): void {
  refreshEpoch += 1;
  refreshGenerations.clear();
  appliedRefreshGenerations.clear();
  pendingTurnsStore.setState({ outbox: new Map(), optimistic: new Map(), recovery: new Map() });
}

// Keep the singleton projection warm when an authoritative pendingMutations
// snapshot changes even if no pending component is currently mounted.
threadsStore.subscribe((state, previous) => {
  if (state.threads === previous.threads) return;
  for (const ref of state.threads.keys()) {
    if (state.threads.get(ref) !== previous.threads.get(ref)) void refreshPendingTurnsProjection(ref);
  }
});
