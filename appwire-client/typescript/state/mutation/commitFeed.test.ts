import { describe, expect, test, vi } from "vitest";
import { type MutationCommit, type MutationCommitFeed, wireMutationCommitFeed } from "./commitFeed";
import { createPendingTurnsStore } from "./pendingTurns";
import type { MutationProjectionFence } from "./projection";
import type { MutationOutboxRecord, MutationRecoveryRecord } from "./records";

function outboxRecord(overrides: Partial<MutationOutboxRecord> = {}): MutationOutboxRecord {
  return {
    version: 1,
    clientMutationId: "cmid-1",
    targetRef: "ref-a",
    method: "turn/start",
    payload: {},
    attachments: [],
    optimisticDisplay: null,
    intentSequence: 0,
    createdAt: 0,
    state: "submitting",
    ...overrides,
  };
}

function recoveryRecord(overrides: Partial<MutationRecoveryRecord> = {}): MutationRecoveryRecord {
  return { ...outboxRecord(), recoveryKind: "rejected", ...overrides };
}

function testStore() {
  return createPendingTurnsStore({
    threads: { getThreadModel: () => undefined },
    draft: {
      readDraftRevision: () => 0,
      readComposerDraft: () => ({ text: "", skillNames: [] }),
      clearDraft: () => undefined,
    },
    identity: { isOwnMutationRecord: () => true },
  });
}

function fakeFence(): MutationProjectionFence & { advanceCalls: string[] } {
  const advanceCalls: string[] = [];
  return {
    epoch: () => 0,
    refresh: async () => false,
    advance: (ref) => advanceCalls.push(ref),
    reset: () => undefined,
    advanceCalls,
  };
}

function fakeFeed(): MutationCommitFeed & { emit(targetRefs: string[], committed?: MutationCommit): void } {
  const listeners = new Set<(targetRefs: string[], committed?: MutationCommit) => void>();
  return {
    subscribe(listener) {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    emit(targetRefs, committed) {
      for (const listener of listeners) listener(targetRefs, committed);
    },
  };
}

describe("wireMutationCommitFeed", () => {
  test("a commit lands in the store and advances the fence synchronously, with no macrotask hop", () => {
    const store = testStore();
    const fence = fakeFence();
    const feed = fakeFeed();
    const refresh = vi.fn();
    wireMutationCommitFeed(store, fence, feed, refresh);

    const record = outboxRecord();
    feed.emit([], { record });

    expect(store.getState().outbox.get(record.clientMutationId)).toEqual(record);
    expect(fence.advanceCalls).toEqual([record.targetRef]);
    expect(refresh).toHaveBeenCalledWith();
  });

  test("a resend's commit retires the recovery entry it replaces", () => {
    const store = testStore();
    store.setState({ recovery: new Map([["recovery-1", recoveryRecord({ clientMutationId: "recovery-1" })]]) });
    const feed = fakeFeed();
    wireMutationCommitFeed(store, fakeFence(), feed, () => undefined);

    feed.emit([], { record: outboxRecord({ clientMutationId: "cmid-2" }), recoveryId: "recovery-1" });

    expect(store.getState().recovery.has("recovery-1")).toBe(false);
  });

  test("refreshes every named target when the feed reports specific refs", () => {
    const feed = fakeFeed();
    const refresh = vi.fn();
    wireMutationCommitFeed(testStore(), fakeFence(), feed, refresh);

    feed.emit(["ref-a", "ref-b"]);

    expect(refresh).toHaveBeenCalledWith("ref-a");
    expect(refresh).toHaveBeenCalledWith("ref-b");
    expect(refresh).toHaveBeenCalledTimes(2);
  });

  test("unsubscribing stops the feed from reaching the store", () => {
    const store = testStore();
    const feed = fakeFeed();
    const refresh = vi.fn();
    const unsubscribe = wireMutationCommitFeed(store, fakeFence(), feed, refresh);

    unsubscribe();
    feed.emit([], { record: outboxRecord() });

    expect(store.getState().outbox.size).toBe(0);
    expect(refresh).not.toHaveBeenCalled();
  });
});
