// Task 6 recovery: disk-backed current checkpoints must survive a storage
// fault, a sibling clear with no broadcast, and an expired promotion, without
// falsely claiming freshness or resurrecting cleared content. Every case uses
// the real store, the real reducer and a real IndexedDB adapter; the seams only
// hold real storage results at deterministic transaction boundaries.
import "fake-indexeddb/auto";
import { currentRead, textDelta } from "@evener/appwire-client/testing/currentTranscript";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { IDBFactory } from "fake-indexeddb";
import { describe, expect, test, vi } from "vitest";
import { installBackgroundTranscriptOptionsForTests } from "./backgroundTranscriptOptions";
import { connectionStore } from "./connection";
import { type CurrentSessionRecord, SessionCacheIndexedDB } from "./sessionCacheIndexedDB";
import { holdNextCurrentLookup, holdNextWriteTransaction } from "./testing/stalledIndexedDB";
import {
  acquireBackgroundThread,
  backgroundTranscriptSnapshotForTests,
  installHydrationRetrySchedulerForTests,
  resetThreadsStoreForTests,
  setSessionCacheAdapterForTests,
  settleBackgroundTranscriptsForTests,
  threadsStore,
} from "./threads";

const ref = "local:one";

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  fake.on("thread/read", ({ ref: requested }) => currentRead(requested ?? ref));
  fake.on("thread/unsubscribe", () => ({}));
  connectionStore.getState().connect(fake);
  return fake;
}

function threadReads(fake: FakeClient): number {
  return fake.calls.filter((call) => call.method === "thread/read").length;
}

describe("background transcript recovery", () => {
  test("an epoch lookup failure at promotion is a storage fault, not a clear, and recovers through the paced scheduler", async () => {
    const restoreOptions = installBackgroundTranscriptOptionsForTests({ residentBytes: 0 });
    const cache = new SessionCacheIndexedDB({
      indexedDB: new IDBFactory(),
      databaseName: `recovery-${crypto.randomUUID()}`,
    });
    resetThreadsStoreForTests();
    setSessionCacheAdapterForTests(cache);
    // A recovery read that carries a newer window publishes; the first read is
    // the healthy background hydration.
    let readCount = 0;
    const fake = new FakeClient("ready");
    fake.on("thread/read", ({ ref: requested, requestGeneration }) => {
      readCount += 1;
      const response = currentRead(requested ?? ref);
      response.requestGeneration = requestGeneration ?? response.requestGeneration;
      if (readCount > 1) {
        response.snapshot = { incarnation: "inc-1", length: 41 };
        response.epoch = 2;
        if (response.overlay?.[0]) response.overlay[0].item.text = "alpha-recovered";
      }
      return response;
    });
    fake.on("thread/unsubscribe", () => ({}));
    connectionStore.getState().connect(fake);
    const scheduled: Array<() => void> = [];
    const restoreScheduler = installHydrationRetrySchedulerForTests((_attempt, retry) => {
      scheduled.push(retry);
      return () => {};
    });
    const epochSpy = vi.spyOn(cache, "readEpoch").mockResolvedValue(undefined);
    const lease = acquireBackgroundThread(ref);
    const heldLookup = holdNextCurrentLookup(cache);
    let paneHeld = false;
    let opening: Promise<void> | undefined;
    try {
      await lease.ready;
      await settleBackgroundTranscriptsForTests();
      expect(threadsStore.getState().threads.has(ref)).toBe(false);

      opening = threadsStore.getState().ensureThread(ref);
      paneHeld = true;
      await heldLookup.reached;
      heldLookup.release();
      await opening;
      await settleBackgroundTranscriptsForTests();
      // A failed epoch lookup revokes as a storage fault; it never claims a
      // clear it cannot prove.
      expect(backgroundTranscriptSnapshotForTests().refs.get(ref)?.persistence).toBe("repairing");

      epochSpy.mockRestore();
      while (scheduled.length > 0) {
        scheduled.shift()?.();
        await settleBackgroundTranscriptsForTests();
      }
      await vi.waitFor(() => expect(backgroundTranscriptSnapshotForTests().refs.get(ref)?.persistence).toBe("enabled"));
    } finally {
      heldLookup.release();
      epochSpy.mockRestore();
      await opening?.catch(() => undefined);
      if (paneHeld) threadsStore.getState().releaseThread(ref);
      await settleBackgroundTranscriptsForTests();
      await lease.release();
      restoreScheduler();
      resetThreadsStoreForTests();
      restoreOptions();
      cache.close();
    }
  });

  test("a replacement client before the delayed epoch result cannot invalidate the newer lifetime", async () => {
    const restoreOptions = installBackgroundTranscriptOptionsForTests({ residentBytes: 0 });
    const cache = new SessionCacheIndexedDB({
      indexedDB: new IDBFactory(),
      databaseName: `recovery-${crypto.randomUUID()}`,
    });
    resetThreadsStoreForTests();
    setSessionCacheAdapterForTests(cache);
    connectFakeClient();
    const epochGate = deferred<number | undefined>();
    const epochSpy = vi.spyOn(cache, "readEpoch").mockImplementation(() => epochGate.promise);
    const lease = acquireBackgroundThread(ref);
    const heldLookup = holdNextCurrentLookup(cache);
    let paneHeld = false;
    let opening: Promise<void> | undefined;
    try {
      await lease.ready;
      await settleBackgroundTranscriptsForTests();
      expect(threadsStore.getState().threads.has(ref)).toBe(false);
      // Installation calls (background hydration) also read the epoch; arm the
      // gate only now so only the promotion's recheck is held.

      opening = threadsStore.getState().ensureThread(ref);
      paneHeld = true;
      await heldLookup.reached;
      heldLookup.release();
      await opening;
      // A replacement client (new ready generation) lands before the delayed
      // epoch result arrives.
      connectFakeClient();
      epochGate.resolve(99);
      await settleBackgroundTranscriptsForTests();
      // The stale result is fenced by the generation guard: it cannot suppress
      // the newer lifetime with an epoch from a retired client generation.
      expect(backgroundTranscriptSnapshotForTests().refs.get(ref)?.persistence).not.toBe("clear-suppressed");
    } finally {
      heldLookup.release();
      epochGate.resolve(undefined);
      epochSpy.mockRestore();
      await opening?.catch(() => undefined);
      if (paneHeld) threadsStore.getState().releaseThread(ref);
      await settleBackgroundTranscriptsForTests();
      await lease.release();
      resetThreadsStoreForTests();
      restoreOptions();
      cache.close();
    }
  });

  test("a sibling clear with no broadcast is caught by the post-promotion epoch recheck", async () => {
    const restoreOptions = installBackgroundTranscriptOptionsForTests({ residentBytes: 0 });
    const factory = new IDBFactory();
    const databaseName = `recovery-${crypto.randomUUID()}`;
    const cache = new SessionCacheIndexedDB({ indexedDB: factory, databaseName });
    const sibling = new SessionCacheIndexedDB({ indexedDB: factory, databaseName });
    resetThreadsStoreForTests();
    setSessionCacheAdapterForTests(cache);
    const fake = connectFakeClient();

    const writes: CurrentSessionRecord[] = [];
    const originalPut = cache.putCurrent.bind(cache);
    const put = vi.spyOn(cache, "putCurrent").mockImplementation(async (record, now) => {
      const outcome = await originalPut(record, now);
      if (outcome.outcome === "written") writes.push(record);
      return outcome;
    });

    const lease = acquireBackgroundThread(ref);
    const heldLookup = holdNextCurrentLookup(cache);
    let paneHeld = false;
    let opening: Promise<void> | undefined;
    try {
      await lease.ready;
      await settleBackgroundTranscriptsForTests();
      // residentBytes 0 evicted the healthy checkpoint to disk without a read.
      expect(threadsStore.getState().threads.has(ref)).toBe(false);
      const preClearRecord = writes.at(-1);
      if (!preClearRecord) throw new Error("expected a real pre-clear checkpoint");

      // Start the promotion, hold its real lookup result, then clear from a
      // sibling adapter on the same factory WITHOUT emitting a broadcast.
      const readsBefore = threadReads(fake);
      opening = threadsStore.getState().ensureThread(ref);
      paneHeld = true;
      await heldLookup.reached;
      const cleared = await sibling.clear();
      expect(cleared.committed).toBe(true);
      heldLookup.release();
      await opening;
      await settleBackgroundTranscriptsForTests();

      // The delayed/lost broadcast did not hide the clear: the post-promotion
      // epoch recheck suppressed persistence, and the pre-clear checkpoint
      // cannot be written back under the old epoch.
      expect(backgroundTranscriptSnapshotForTests().refs.get(ref)?.persistence).toBe("clear-suppressed");
      expect((await cache.putCurrent(preClearRecord, 3)).outcome).toBe("aborted");

      // The recheck reconciled authoritatively: a wire read follows detection.
      expect(threadReads(fake)).toBeGreaterThan(readsBefore);
      await settleBackgroundTranscriptsForTests();
      expect(backgroundTranscriptSnapshotForTests().refs.get(ref)?.persistence).toBe("clear-suppressed");
    } finally {
      heldLookup.release();
      await opening?.catch(() => undefined);
      if (paneHeld) threadsStore.getState().releaseThread(ref);
      await settleBackgroundTranscriptsForTests();
      await lease.release();
      put.mockRestore();
      resetThreadsStoreForTests();
      restoreOptions();
      cache.close();
      sibling.close();
    }
  });

  test("a storage fault leaves the ref unverified and recovers through the paced scheduler to a local no-read open", async () => {
    const restoreOptions = installBackgroundTranscriptOptionsForTests({ residentBytes: 0, pendingBytes: 2048 });
    const factory = new IDBFactory();
    const cache = new SessionCacheIndexedDB({ indexedDB: factory, databaseName: `recovery-${crypto.randomUUID()}` });
    resetThreadsStoreForTests();
    setSessionCacheAdapterForTests(cache);
    const fake = connectFakeClient();

    const scheduled: Array<() => void> = [];
    const restoreScheduler = installHydrationRetrySchedulerForTests((_attempt, retry) => {
      scheduled.push(retry);
      return () => {};
    });
    let faulted = false;
    let transaction: ReturnType<typeof holdNextWriteTransaction> | undefined;
    const writeHeld = deferred<void>();
    const originalPut = cache.putCurrent.bind(cache);
    let holdSpy: ReturnType<typeof vi.spyOn> | undefined;

    const lease = acquireBackgroundThread(ref);
    let paneHeld = false;
    try {
      // The initial hydration writes one healthy checkpoint and the model
      // evicts; arm the write seam only now so the next write is the faulted one.
      await lease.ready;
      await settleBackgroundTranscriptsForTests();
      expect(threadsStore.getState().threads.has(ref)).toBe(false);
      holdSpy = vi.spyOn(cache, "putCurrent").mockImplementation(async (record, now) => {
        if (!transaction) {
          transaction = holdNextWriteTransaction(["records", "meta", "current", "currentMeta"]);
          writeHeld.resolve();
        }
        if (faulted) return { outcome: "aborted", observedEpoch: record.clearEpoch };
        return originalPut(record, now);
      });

      // Block a real checkpoint write, then stream until the bounded pending
      // admission refuses another update: the ref enters repairing.
      fake.emitNotification(textDelta(ref, "-faulted"));
      await writeHeld.promise;
      await transaction?.reached;
      for (let i = 0; i < 80; i += 1) {
        fake.emitNotification(textDelta(ref, `-overload-${i}`));
      }
      const repairing = backgroundTranscriptSnapshotForTests();
      expect(repairing.refs.get(ref)?.persistence).toBe("repairing");
      expect(repairing.refs.get(ref)?.current).toBe(false);
      expect(repairing.pendingBytes).toBeLessThanOrEqual(2048);
      expect(repairing.refs.get(ref)?.residentBytes).toBe(0);
      // Pacing held: no un-paced hydration storm while the fault stands.
      expect(threadReads(fake)).toBe(1);
      expect(scheduled.length).toBeGreaterThanOrEqual(1);
      transaction?.release();
      await settleBackgroundTranscriptsForTests();

      // The stale checkpoint cannot certify currentness: an explicit open issues
      // an authoritative hydration.
      const readsBefore = threadReads(fake);
      faulted = true;
      await threadsStore.getState().ensureThread(ref);
      paneHeld = true;
      expect(threadReads(fake)).toBeGreaterThan(readsBefore);

      // Remove the fault and let the paced retry run: a successful checkpoint
      // commits, the model evicts to disk, and a later open is local.
      faulted = false;
      while (scheduled.length > 0) {
        scheduled.shift()?.();
        await settleBackgroundTranscriptsForTests();
      }
      await settleBackgroundTranscriptsForTests();
      const recovered = backgroundTranscriptSnapshotForTests();
      expect(recovered.refs.get(ref)?.persistence).toBe("enabled");

      threadsStore.getState().releaseThread(ref);
      paneHeld = false;
      await settleBackgroundTranscriptsForTests();
      expect(threadsStore.getState().threads.has(ref)).toBe(false);
      expect(backgroundTranscriptSnapshotForTests().refs.get(ref)?.current).toBe(true);
      const readsAfterRecovery = threadReads(fake);
      await threadsStore.getState().ensureThread(ref);
      paneHeld = true;
      expect(threadReads(fake)).toBe(readsAfterRecovery);
    } finally {
      faulted = false;
      transaction?.release();
      if (paneHeld) threadsStore.getState().releaseThread(ref);
      await settleBackgroundTranscriptsForTests();
      await lease.release();
      restoreScheduler();
      holdSpy?.mockRestore();
      resetThreadsStoreForTests();
      restoreOptions();
      cache.close();
    }
  });
});
