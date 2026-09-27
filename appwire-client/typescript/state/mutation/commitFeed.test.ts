// @vitest-environment node

import { describe, expect, test, vi } from "vitest";
import { type MutationCommit, type MutationCommitFeed, wireMutationCommitFeed } from "./commitFeed";
import type { MutationProjectionFence } from "./projection";
import { outboxRecord, recoveryRecord, testPendingTurnsStore } from "./testing";

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
    const store = testPendingTurnsStore();
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
    const store = testPendingTurnsStore();
    store.setState({ recovery: new Map([["recovery-1", recoveryRecord({ clientMutationId: "recovery-1" })]]) });
    const feed = fakeFeed();
    wireMutationCommitFeed(store, fakeFence(), feed, () => undefined);

    feed.emit([], { record: outboxRecord({ clientMutationId: "cmid-2" }), recoveryId: "recovery-1" });

    expect(store.getState().recovery.has("recovery-1")).toBe(false);
  });

  test("refreshes every named target when the feed reports specific refs", () => {
    const feed = fakeFeed();
    const refresh = vi.fn();
    wireMutationCommitFeed(testPendingTurnsStore(), fakeFence(), feed, refresh);

    feed.emit(["ref-a", "ref-b"]);

    expect(refresh).toHaveBeenCalledWith("ref-a");
    expect(refresh).toHaveBeenCalledWith("ref-b");
    expect(refresh).toHaveBeenCalledTimes(2);
  });

  test("a commit alongside named target refs both applies the fast path and refreshes those refs", () => {
    const store = testPendingTurnsStore();
    const fence = fakeFence();
    const feed = fakeFeed();
    const refresh = vi.fn();
    wireMutationCommitFeed(store, fence, feed, refresh);

    const record = outboxRecord();
    feed.emit(["ref-a"], { record });

    expect(store.getState().outbox.get(record.clientMutationId)).toEqual(record);
    expect(fence.advanceCalls).toEqual([record.targetRef]);
    expect(refresh).toHaveBeenCalledWith("ref-a");
  });

  test("unsubscribing stops the feed from reaching the store", () => {
    const store = testPendingTurnsStore();
    const feed = fakeFeed();
    const refresh = vi.fn();
    const unsubscribe = wireMutationCommitFeed(store, fakeFence(), feed, refresh);

    unsubscribe();
    feed.emit([], { record: outboxRecord() });

    expect(store.getState().outbox.size).toBe(0);
    expect(refresh).not.toHaveBeenCalled();
  });
});
