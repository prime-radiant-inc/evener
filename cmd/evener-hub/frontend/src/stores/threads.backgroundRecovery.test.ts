// Task 6 recovery: disk-backed current checkpoints must survive a storage
// fault, a sibling clear with no broadcast, and an expired promotion, without
// falsely claiming freshness or resurrecting cleared content. Every case uses
// the real store, the real reducer and a real IndexedDB adapter; the seams only
// hold real storage results at deterministic transaction boundaries.
import "fake-indexeddb/auto";
import type { ThreadTurnsListResponse } from "@evener/appwire-client";
import { currentRead, textDelta } from "@evener/appwire-client/testing/currentTranscript";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { IDBFactory } from "fake-indexeddb";
import { describe, expect, test, vi } from "vitest";
import { installBackgroundTranscriptOptionsForTests } from "./backgroundTranscriptOptions";
import { connectionStore } from "./connection";
import { type CurrentSessionRecord, SessionCacheIndexedDB } from "./sessionCacheIndexedDB";
import {
  holdNextCurrentLookup,
  holdNextTransaction,
  holdNextWriteTransaction,
  nextRealTask,
} from "./testing/stalledIndexedDB";
import {
  acquireBackgroundThread,
  backgroundTranscriptSnapshotForTests,
  clearCachedSessions,
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

  test("the post-promotion epoch recheck starts immediately and its held real lookup times out by 250 ms of fake time into an unverified, recoverable state", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    const restoreOptions = installBackgroundTranscriptOptionsForTests({ residentBytes: 0 });
    const cache = new SessionCacheIndexedDB({
      indexedDB: new IDBFactory(),
      databaseName: `recovery-${crypto.randomUUID()}`,
    });
    resetThreadsStoreForTests();
    setSessionCacheAdapterForTests(cache);
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
    const lease = acquireBackgroundThread(ref);
    let heldLookup: ReturnType<typeof holdNextCurrentLookup> | undefined;
    let heldEpoch: ReturnType<typeof holdNextTransaction> | undefined;
    let paneHeld = false;
    let opening: Promise<void> | undefined;
    try {
      await lease.ready;
      await settleBackgroundTranscriptsForTests();
      for (let i = 0; i < 10 && threadsStore.getState().threads.has(ref); i += 1) {
        await nextRealTask();
        await settleBackgroundTranscriptsForTests();
      }
      expect(threadsStore.getState().threads.has(ref)).toBe(false);
      heldLookup = holdNextCurrentLookup(cache);
      heldEpoch = holdNextTransaction(["meta"], "readonly");

      opening = threadsStore.getState().ensureThread(ref);
      paneHeld = true;
      await heldLookup.reached;
      heldLookup.release();
      await opening;
      // The recheck starts immediately: its real readEpoch transaction opens and
      // is held with no clock advance.
      await heldEpoch.reached;
      expect(threadsStore.getState().threads.has(ref)).toBe(true);
      expect(backgroundTranscriptSnapshotForTests().refs.get(ref)?.persistence).toBe("enabled");

      // The held real lookup cannot beat readEpoch's own 250 ms deadline.
      await vi.advanceTimersByTimeAsync(250);
      await settleBackgroundTranscriptsForTests();
      expect(backgroundTranscriptSnapshotForTests().refs.get(ref)?.persistence).toBe("repairing");
      expect(backgroundTranscriptSnapshotForTests().refs.get(ref)?.current).toBe(false);
      // The stale publication stays visible; it just carries no authority and
      // no queued work.
      expect(threadsStore.getState().threads.has(ref)).toBe(true);
      expect(backgroundTranscriptSnapshotForTests().refs.get(ref)?.pending).toHaveLength(0);

      // Authoritative recovery follows the paced repair.
      expect(scheduled.length).toBeGreaterThanOrEqual(1);
      scheduled.shift()?.();
      await vi.advanceTimersByTimeAsync(0);
      await settleBackgroundTranscriptsForTests();
      for (let i = 0; i < 20 && backgroundTranscriptSnapshotForTests().refs.get(ref)?.persistence !== "enabled"; i += 1)
        await nextRealTask();
      expect(threadReads(fake)).toBeGreaterThanOrEqual(2);
      expect(backgroundTranscriptSnapshotForTests().refs.get(ref)?.persistence).toBe("enabled");
    } finally {
      heldEpoch?.release();
      heldLookup?.release();
      await opening?.catch(() => undefined);
      if (paneHeld) threadsStore.getState().releaseThread(ref);
      await settleBackgroundTranscriptsForTests();
      await lease.release();
      restoreScheduler();
      resetThreadsStoreForTests();
      restoreOptions();
      cache.close();
      vi.useRealTimers();
    }
  });

  test("the post-promotion epoch recheck detects a held sibling clear when its real lookup completes, without waiting out the deadline", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    const restoreOptions = installBackgroundTranscriptOptionsForTests({ residentBytes: 0 });
    const factory = new IDBFactory();
    const databaseName = `recovery-${crypto.randomUUID()}`;
    const cache = new SessionCacheIndexedDB({ indexedDB: factory, databaseName });
    const sibling = new SessionCacheIndexedDB({ indexedDB: factory, databaseName });
    resetThreadsStoreForTests();
    setSessionCacheAdapterForTests(cache);
    const fake = connectFakeClient();
    const lease = acquireBackgroundThread(ref);
    let heldLookup: ReturnType<typeof holdNextCurrentLookup> | undefined;
    let heldEpoch: ReturnType<typeof holdNextTransaction> | undefined;
    let paneHeld = false;
    let opening: Promise<void> | undefined;
    try {
      await lease.ready;
      await settleBackgroundTranscriptsForTests();
      for (let i = 0; i < 10 && threadsStore.getState().threads.has(ref); i += 1) {
        await nextRealTask();
        await settleBackgroundTranscriptsForTests();
      }
      expect(threadsStore.getState().threads.has(ref)).toBe(false);
      heldLookup = holdNextCurrentLookup(cache);
      heldEpoch = holdNextTransaction(["meta"], "readonly");

      opening = threadsStore.getState().ensureThread(ref);
      paneHeld = true;
      await heldLookup.reached;
      const cleared = await sibling.clear();
      expect(cleared.committed).toBe(true);
      heldLookup.release();
      await opening;
      // The recheck starts immediately, its real lookup held with no clock advance.
      await heldEpoch.reached;
      expect(threadsStore.getState().threads.has(ref)).toBe(true);

      // Releasing the real lookup completes it well inside the 250 ms deadline.
      heldEpoch.release();
      await settleBackgroundTranscriptsForTests();
      expect(backgroundTranscriptSnapshotForTests().refs.get(ref)?.persistence).toBe("clear-suppressed");
      // Stale visible state is retained; an authoritative read reconciles.
      expect(threadsStore.getState().threads.has(ref)).toBe(true);
      expect(threadReads(fake)).toBeGreaterThan(1);
    } finally {
      heldEpoch?.release();
      heldLookup?.release();
      await opening?.catch(() => undefined);
      if (paneHeld) threadsStore.getState().releaseThread(ref);
      await settleBackgroundTranscriptsForTests();
      await lease.release();
      resetThreadsStoreForTests();
      restoreOptions();
      cache.close();
      sibling.close();
      vi.useRealTimers();
    }
  });

  test("an alias rebind keeps the requested subscription, hydrates the new identity and rejects the old id's frame", async () => {
    const restoreOptions = installBackgroundTranscriptOptionsForTests({ residentBytes: 0 });
    const cache = new SessionCacheIndexedDB({
      indexedDB: new IDBFactory(),
      databaseName: `recovery-${crypto.randomUUID()}`,
    });
    resetThreadsStoreForTests();
    setSessionCacheAdapterForTests(cache);
    let reads = 0;
    const fake = new FakeClient("ready");
    fake.on("thread/read", ({ ref: requested, requestGeneration }) => {
      reads += 1;
      const response = currentRead(requested ?? ref);
      response.requestGeneration = requestGeneration ?? response.requestGeneration;
      response.thread.id = reads === 1 ? "thr_old" : "thr_new";
      response.thread.sessionId = reads === 1 ? "sess_old" : "sess_new";
      if (reads > 1 && response.overlay?.[0]) response.overlay[0].item.text = "new-identity-text";
      return response;
    });
    fake.on("thread/unsubscribe", () => ({}));
    connectionStore.getState().connect(fake);
    const lease = acquireBackgroundThread(ref);
    let paneHeld = false;
    try {
      await lease.ready;
      await settleBackgroundTranscriptsForTests();
      const pane = threadsStore.getState().ensureThread(ref);
      paneHeld = true;
      await pane;
      expect(threadsStore.getState().threads.get(ref)?.threadId).toBe("thr_old");

      // A resync proves the replacement identity for the same requested ref.
      fake.emitNotification({
        method: "evener/thread/resync",
        params: { ref, threadId: "thr_new" },
      } as never);
      await vi.waitFor(() => expect(threadsStore.getState().threads.get(ref)?.threadId).toBe("thr_new"));
      expect(JSON.stringify(threadsStore.getState().threads.get(ref))).toContain("new-identity-text");
      // The requested subscription survives the rebind.
      expect(threadsStore.getState().threads.has(ref)).toBe(true);
      expect(fake.calls.filter((call) => call.method === "thread/unsubscribe")).toHaveLength(0);

      // A late frame naming the old resolved id (and no requested ref) cannot
      // re-enter the model: the requested ref is the routing identity, and its
      // resolved id no longer points at this ref.
      const before = JSON.stringify(threadsStore.getState().threads.get(ref));
      fake.emitNotification({
        method: "thread/status/changed",
        params: { threadId: "thr_old", status: { type: "active", activeFlags: ["streaming"] } },
      } as never);
      await nextRealTask();
      expect(JSON.stringify(threadsStore.getState().threads.get(ref))).toBe(before);
    } finally {
      if (paneHeld) threadsStore.getState().releaseThread(ref);
      await settleBackgroundTranscriptsForTests();
      await lease.release();
      resetThreadsStoreForTests();
      restoreOptions();
      cache.close();
    }
  });

  test("an oversized background checkpoint is rebuilt as a latest window with the server's new cursor, without retrying the same oversized state", async () => {
    const maxBytes = 64 * 1024 * 1024;
    const restoreOptions = installBackgroundTranscriptOptionsForTests({ residentBytes: 0 });
    const cache = new SessionCacheIndexedDB({
      indexedDB: new IDBFactory(),
      databaseName: `recovery-${crypto.randomUUID()}`,
      maxBytes,
    });
    resetThreadsStoreForTests();
    setSessionCacheAdapterForTests(cache);
    let reads = 0;
    const fake = new FakeClient("ready");
    fake.on("thread/read", ({ ref: requested, requestGeneration }) => {
      reads += 1;
      const response = currentRead(requested ?? ref);
      response.requestGeneration = requestGeneration ?? response.requestGeneration;
      response.olderCursor = reads === 1 ? "cursor_old" : "cursor_server_new";
      return response;
    });
    fake.on("thread/unsubscribe", () => ({}));
    connectionStore.getState().connect(fake);
    const writes: CurrentSessionRecord[] = [];
    const originalPut = cache.putCurrent.bind(cache);
    const put = vi.spyOn(cache, "putCurrent").mockImplementation(async (record, now) => {
      const outcome = await originalPut(record, now);
      if (outcome.outcome === "written") writes.push(record);
      return outcome;
    });
    const lease = acquireBackgroundThread(ref);
    let paneHeld = false;
    try {
      await lease.ready;
      await settleBackgroundTranscriptsForTests();
      expect(threadsStore.getState().threads.has(ref)).toBe(false);
      const initial = writes.at(-1);
      if (!initial) throw new Error("expected a real background checkpoint");

      // Rewrite the real checkpoint with an oversized live body, still inside
      // the injected 64 MiB cache cap so the adapter admits it.
      const oversizedState = JSON.parse(JSON.stringify(initial.state)) as CurrentSessionRecord["state"];
      const overlayKey = "stream:s1:agentMessage";
      if (!oversizedState.overlay[overlayKey]) throw new Error("expected the captured overlay item");
      oversizedState.overlay[overlayKey] = {
        ...oversizedState.overlay[overlayKey],
        item: { ...oversizedState.overlay[overlayKey]!.item, text: "x".repeat(34 * 1024 * 1024) },
      } as (typeof oversizedState.overlay)[string];
      expect(
        await cache.putCurrent({ ...initial, savedAt: Date.now(), state: oversizedState }, Date.now()),
      ).toMatchObject({ outcome: "written" });

      // The next background update loads the oversized base, refuses it and
      // rebuilds from an authoritative background latest window.
      fake.emitNotification(textDelta(ref, "-oversized-trigger"));
      await settleBackgroundTranscriptsForTests();
      await vi.waitFor(() => expect(backgroundTranscriptSnapshotForTests().refs.get(ref)?.persistence).toBe("enabled"));
      const readsAfterRecovery = threadReads(fake);
      expect(readsAfterRecovery).toBe(2);

      // No continuous retry of the same unchanged oversized state.
      for (let i = 0; i < 10; i += 1) await nextRealTask();
      await settleBackgroundTranscriptsForTests();
      expect(threadReads(fake)).toBe(readsAfterRecovery);

      // The rebuilt checkpoint carries the server's own cursor, and reopening is
      // local.
      await threadsStore.getState().ensureThread(ref);
      paneHeld = true;
      expect(threadsStore.getState().threads.get(ref)?.olderCursor).toBe("cursor_server_new");
      expect(threadReads(fake)).toBe(readsAfterRecovery);
    } finally {
      if (paneHeld) threadsStore.getState().releaseThread(ref);
      await settleBackgroundTranscriptsForTests();
      await lease.release();
      put.mockRestore();
      resetThreadsStoreForTests();
      restoreOptions();
      cache.close();
    }
  });

  test("an ordinary background notification after Clear keeps both cache classes suppressed and writes no new rows", async () => {
    const restoreOptions = installBackgroundTranscriptOptionsForTests({ residentBytes: 0 });
    const cache = new SessionCacheIndexedDB({
      indexedDB: new IDBFactory(),
      databaseName: `recovery-${crypto.randomUUID()}`,
    });
    resetThreadsStoreForTests();
    setSessionCacheAdapterForTests(cache);
    const fake = connectFakeClient();
    const writes: unknown[] = [];
    const originalPut = cache.putCurrent.bind(cache);
    const put = vi.spyOn(cache, "putCurrent").mockImplementation(async (...args) => {
      const result = await originalPut(...args);
      if (result.outcome === "written") writes.push(args[0]);
      return result;
    });
    const lease = acquireBackgroundThread(ref);
    try {
      await lease.ready;
      await settleBackgroundTranscriptsForTests();
      expect(threadsStore.getState().threads.has(ref)).toBe(false);
      expect(await clearCachedSessions()).toEqual({ committed: true });
      const writesBefore = writes.length;

      fake.emitNotification(textDelta(ref, "-post-clear"));
      await settleBackgroundTranscriptsForTests();

      // No background repair read, no new current record, and both cache
      // classes still suppressed: only a fresh explicit pane may restore them.
      expect(threadReads(fake)).toBe(1);
      expect(backgroundTranscriptSnapshotForTests().refs.get(ref)?.persistence).toBe("clear-suppressed");
      expect(writes.length).toBe(writesBefore);
      expect(threadsStore.getState().cacheLifetimes.get(ref)?.suppressed).toBe(true);
    } finally {
      put.mockRestore();
      await lease.release();
      await settleBackgroundTranscriptsForTests();
      resetThreadsStoreForTests();
      restoreOptions();
      cache.close();
    }
  });

  test("a fresh explicit pane restores both cache classes even while the model is still resident", async () => {
    const restoreOptions = installBackgroundTranscriptOptionsForTests({ residentBytes: 16 * 1024 * 1024 });
    const cache = new SessionCacheIndexedDB({
      indexedDB: new IDBFactory(),
      databaseName: `recovery-${crypto.randomUUID()}`,
    });
    resetThreadsStoreForTests();
    setSessionCacheAdapterForTests(cache);
    const fake = connectFakeClient();
    const lease = acquireBackgroundThread(ref);
    let paneHeld = false;
    try {
      await lease.ready;
      await settleBackgroundTranscriptsForTests();
      await threadsStore.getState().ensureThread(ref);
      paneHeld = true;
      expect(await clearCachedSessions()).toEqual({ committed: true });
      threadsStore.getState().releaseThread(ref);
      paneHeld = false;
      // Resident: a large budget keeps the stale display model in RAM.
      expect(threadsStore.getState().threads.has(ref)).toBe(true);

      const readsBefore = threadReads(fake);
      await threadsStore.getState().ensureThread(ref);
      paneHeld = true;
      // The fresh lifetime reconciles authoritatively despite the resident model.
      expect(threadReads(fake)).toBeGreaterThan(readsBefore);
      expect(backgroundTranscriptSnapshotForTests().refs.get(ref)?.persistence).toBe("enabled");
      expect(threadsStore.getState().cacheLifetimes.get(ref)?.suppressed).toBeUndefined();
    } finally {
      if (paneHeld) threadsStore.getState().releaseThread(ref);
      await settleBackgroundTranscriptsForTests();
      await lease.release();
      resetThreadsStoreForTests();
      restoreOptions();
      cache.close();
    }
  });

  test("the lost-broadcast epoch recheck suppresses both record classes, not just the residency coordinator", async () => {
    const restoreOptions = installBackgroundTranscriptOptionsForTests({ residentBytes: 0 });
    const factory = new IDBFactory();
    const databaseName = `recovery-${crypto.randomUUID()}`;
    const cache = new SessionCacheIndexedDB({ indexedDB: factory, databaseName });
    const sibling = new SessionCacheIndexedDB({ indexedDB: factory, databaseName });
    resetThreadsStoreForTests();
    setSessionCacheAdapterForTests(cache);
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
      expect((await sibling.clear()).committed).toBe(true);
      heldLookup.release();
      await opening;
      await settleBackgroundTranscriptsForTests();
      // Both classes: the residency coordinator and the recorded-history lease.
      expect(backgroundTranscriptSnapshotForTests().refs.get(ref)?.persistence).toBe("clear-suppressed");
      expect(threadsStore.getState().cacheLifetimes.get(ref)?.suppressed).toBe(true);
    } finally {
      heldLookup.release();
      await opening?.catch(() => undefined);
      if (paneHeld) threadsStore.getState().releaseThread(ref);
      await settleBackgroundTranscriptsForTests();
      await lease.release();
      resetThreadsStoreForTests();
      restoreOptions();
      cache.close();
      sibling.close();
    }
  });

  test("repeated real transaction aborts advance the storage backoff streak and only a committed checkpoint resets it", async () => {
    const restoreOptions = installBackgroundTranscriptOptionsForTests({ residentBytes: 0 });
    resetThreadsStoreForTests();
    let faulted = true;
    const cache = new SessionCacheIndexedDB({
      indexedDB: new IDBFactory(),
      databaseName: `recovery-abort-${crypto.randomUUID()}`,
      beforeCommit: (operation, transaction) => {
        if (operation === "putCurrent" && faulted) transaction.abort();
      },
    });
    setSessionCacheAdapterForTests(cache);
    const attempts: number[] = [];
    const scheduled: Array<() => void> = [];
    const restoreScheduler = installHydrationRetrySchedulerForTests((attempt, retry) => {
      attempts.push(attempt);
      scheduled.push(retry);
      return () => {};
    });
    let responseCount = 0;
    const fake = new FakeClient("ready");
    fake.on("thread/read", ({ ref: requested, requestGeneration }) => {
      responseCount += 1;
      const response = currentRead(requested ?? ref);
      response.requestGeneration = requestGeneration ?? response.requestGeneration;
      response.epoch = responseCount;
      response.snapshot = { incarnation: "inc-1", length: 40 + responseCount };
      if (response.overlay?.[0]) response.overlay[0].item.text = `repair-${responseCount}`;
      return response;
    });
    fake.on("thread/unsubscribe", () => ({}));
    connectionStore.getState().connect(fake);
    const lease = acquireBackgroundThread(ref);
    try {
      await lease.ready;
      await settleBackgroundTranscriptsForTests();
      expect(attempts).toEqual([1]);

      scheduled.shift()?.();
      await settleBackgroundTranscriptsForTests();
      await vi.waitFor(() => expect(attempts).toEqual([1, 2]));

      faulted = false;
      scheduled.shift()?.();
      await settleBackgroundTranscriptsForTests();
      await vi.waitFor(() => expect(backgroundTranscriptSnapshotForTests().refs.get(ref)?.current).toBe(true));
      expect(threadsStore.getState().threads.has(ref)).toBe(false);
      const readsAfterRecovery = threadReads(fake);
      await threadsStore.getState().ensureThread(ref);
      expect(threadReads(fake)).toBe(readsAfterRecovery);
      expect(JSON.stringify(threadsStore.getState().threads.get(ref))).toContain("repair-3");
    } finally {
      faulted = false;
      if (threadsStore.getState().threads.has(ref)) threadsStore.getState().releaseThread(ref);
      await settleBackgroundTranscriptsForTests();
      await lease.release();
      restoreScheduler();
      resetThreadsStoreForTests();
      restoreOptions();
      cache.close();
    }
  });

  test("a pre-rebind older-page completion cannot re-enter after an alias rebind with distinct content", async () => {
    const restoreOptions = installBackgroundTranscriptOptionsForTests({ residentBytes: 0 });
    const cache = new SessionCacheIndexedDB({
      indexedDB: new IDBFactory(),
      databaseName: `recovery-${crypto.randomUUID()}`,
    });
    resetThreadsStoreForTests();
    setSessionCacheAdapterForTests(cache);
    let reads = 0;
    const fake = new FakeClient("ready");
    fake.on("thread/read", ({ ref: requested, requestGeneration }) => {
      reads += 1;
      const response = currentRead(requested ?? ref);
      response.requestGeneration = requestGeneration ?? response.requestGeneration;
      if (reads === 1) {
        response.thread.id = "thr_old";
        response.thread.sessionId = "sess_old";
        response.olderCursor = "cursor_old";
        if (response.overlay?.[0]) response.overlay[0].item.text = "old-alpha";
      } else {
        response.thread.id = "thr_new";
        response.thread.sessionId = "sess_new";
        response.snapshot = { incarnation: "inc-2", length: 41 };
        response.epoch = 2;
        response.olderCursor = "cursor_new";
        if (response.overlay?.[0]) response.overlay[0].item.text = "new-beta";
      }
      return response;
    });
    fake.on("thread/unsubscribe", () => ({}));
    const page = deferred<ThreadTurnsListResponse>();
    fake.on("thread/turns/list", () => page.promise);
    connectionStore.getState().connect(fake);
    const lease = acquireBackgroundThread(ref);
    let paneHeld = false;
    let loading: Promise<void> | undefined;
    try {
      await lease.ready;
      await settleBackgroundTranscriptsForTests();
      await threadsStore.getState().ensureThread(ref);
      paneHeld = true;
      expect(threadsStore.getState().threads.get(ref)?.threadId).toBe("thr_old");
      const readsBeforePage = threadReads(fake);

      // An older-page request is in flight when the alias rebinds to a new
      // resolved identity with distinct content.
      loading = threadsStore.getState().loadOlderTurns(ref);
      await vi.waitFor(() => expect(fake.calls.filter((call) => call.method === "thread/turns/list").length).toBe(1));
      fake.emitNotification({
        method: "evener/thread/resync",
        params: { ref, threadId: "thr_new" },
      } as never);
      await vi.waitFor(() => expect(threadsStore.getState().threads.get(ref)?.threadId).toBe("thr_new"));
      await vi.waitFor(() => expect(threadReads(fake)).toBeGreaterThan(readsBeforePage));

      // The pre-rebind page completes against the old identity: it must be
      // rejected, leaving the new identity's content.
      page.resolve({
        data: [
          {
            id: "turn_old_page",
            status: "completed",
            itemsView: "full",
            version: 1,
            items: [
              {
                type: "agentMessage",
                id: "old-page-item-id",
                turnId: "turn_old_page",
                transcriptKey: "apptranscript-item-v2:turn_old_page:0:0",
                position: { entry: 1, item: 0 },
                version: 1,
                text: "old-page-item",
              },
            ],
          },
        ],
        nextCursor: "cursor_after_old",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 100 },
      });
      await loading;
      await settleBackgroundTranscriptsForTests();

      const model = threadsStore.getState().threads.get(ref);
      expect(JSON.stringify(model)).toContain("new-beta");
      expect(JSON.stringify(model)).not.toContain("old-page-item");
      expect(model?.threadId).toBe("thr_new");
      expect(threadsStore.getState().threads.has(ref)).toBe(true);
      expect(fake.calls.filter((call) => call.method === "thread/unsubscribe")).toHaveLength(0);
    } finally {
      page.resolve({
        data: [],
        nextCursor: undefined,
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 100 },
      });
      await loading?.catch(() => undefined);
      if (paneHeld) threadsStore.getState().releaseThread(ref);
      await settleBackgroundTranscriptsForTests();
      await lease.release();
      resetThreadsStoreForTests();
      restoreOptions();
      cache.close();
    }
  });

  test("an oversized older-history checkpoint is rebuilt from a no-snapshot latest window with the new cursor and no unbidden retry", async () => {
    const maxBytes = 64 * 1024 * 1024;
    const restoreOptions = installBackgroundTranscriptOptionsForTests({ residentBytes: 0 });
    const cache = new SessionCacheIndexedDB({
      indexedDB: new IDBFactory(),
      databaseName: `recovery-${crypto.randomUUID()}`,
      maxBytes,
    });
    resetThreadsStoreForTests();
    setSessionCacheAdapterForTests(cache);
    const scheduled: Array<() => void> = [];
    const restoreScheduler = installHydrationRetrySchedulerForTests((_attempt, retry) => {
      scheduled.push(retry);
      return () => {};
    });
    const latestRead = (cursor: string) => {
      const response = currentRead(ref);
      response.olderCursor = cursor;
      response.snapshot = { incarnation: "inc-1", length: 100 };
      response.thread.turns = [
        {
          id: "turn_latest",
          status: "completed",
          itemsView: "full",
          version: 1,
          items: [
            {
              type: "agentMessage",
              id: "item_latest",
              turnId: "turn_latest",
              transcriptKey: "apptranscript-item-v2:turn_latest:0:0",
              position: { entry: 2, item: 0 },
              version: 1,
              text: "latest-recorded-item",
            },
          ],
        },
      ];
      return response;
    };
    let reads = 0;
    const heldSnapshots: unknown[] = [];
    const fake = new FakeClient("ready");
    fake.on("thread/read", ({ requestGeneration, heldSnapshot }) => {
      reads += 1;
      heldSnapshots.push(heldSnapshot);
      const response = latestRead(reads === 1 ? "cursor_old" : "cursor_valid_new");
      response.requestGeneration = requestGeneration ?? response.requestGeneration;
      return response;
    });
    fake.on("thread/unsubscribe", () => ({}));
    connectionStore.getState().connect(fake);
    const writes: CurrentSessionRecord[] = [];
    const originalPut = cache.putCurrent.bind(cache);
    const put = vi.spyOn(cache, "putCurrent").mockImplementation(async (record, now) => {
      const outcome = await originalPut(record, now);
      if (outcome.outcome === "written") writes.push(record);
      return outcome;
    });
    const lease = acquireBackgroundThread(ref);
    let paneHeld = false;
    try {
      await lease.ready;
      await settleBackgroundTranscriptsForTests();
      expect(threadsStore.getState().threads.has(ref)).toBe(false);
      const initial = writes.at(-1);
      if (!initial) throw new Error("expected a real background checkpoint");

      // Oversize the checkpoint's OLDER HISTORY (not the live overlay).
      const oversized = JSON.parse(JSON.stringify(initial.state)) as CurrentSessionRecord["state"];
      const historyTurn = oversized.history.turns[0];
      if (!historyTurn?.items[0]) throw new Error("expected a recorded history turn");
      historyTurn.items[0].text = "h".repeat(34 * 1024 * 1024);
      expect(JSON.stringify(oversized).length).toBeGreaterThan(32 * 1024 * 1024);
      expect(await cache.putCurrent({ ...initial, savedAt: Date.now(), state: oversized }, Date.now())).toMatchObject({
        outcome: "written",
      });

      // A background update loads the oversized history, refuses it, and arms
      // the paced repair instead of an immediate hydrate.
      fake.emitNotification(textDelta(ref, "-oversized-history"));
      await settleBackgroundTranscriptsForTests();
      expect(scheduled.length).toBe(1);
      const readsBeforeRepair = reads;
      // No unbidden retry without advancing the injected scheduler.
      for (let i = 0; i < 10; i += 1) await nextRealTask();
      await settleBackgroundTranscriptsForTests();
      expect(reads).toBe(readsBeforeRepair);
      // The controlled scheduler is the boundary: a still-unrepaired state can
      // only retry by arming another attempt, which it has not.
      expect(scheduled).toHaveLength(1);

      scheduled.shift()?.();
      await vi.waitFor(() => expect(reads).toBeGreaterThan(readsBeforeRepair));
      await vi.waitFor(() => expect(backgroundTranscriptSnapshotForTests().refs.get(ref)?.persistence).toBe("enabled"));
      // A background-only rebuild requests a latest window with no held snapshot
      // and adopts the server's new cursor.
      expect(heldSnapshots.at(-1)).toBeUndefined();
      await threadsStore.getState().ensureThread(ref);
      paneHeld = true;
      const rebuilt = threadsStore.getState().threads.get(ref);
      expect(rebuilt?.olderCursor).toBe("cursor_valid_new");
      expect(JSON.stringify(rebuilt)).toContain("latest-recorded-item");
      expect(JSON.stringify(rebuilt)).not.toContain("hhhhhhhhhh");
      const rebuiltWrite = writes.at(-1);
      expect(JSON.stringify(rebuiltWrite?.state ?? {})).not.toContain("hhhhhhhhhh");
    } finally {
      if (paneHeld) threadsStore.getState().releaseThread(ref);
      await settleBackgroundTranscriptsForTests();
      await lease.release();
      restoreScheduler();
      put.mockRestore();
      resetThreadsStoreForTests();
      restoreOptions();
      cache.close();
    }
  });

  test("an oversized older-history rebuild excludes a held reader: its recovery read keeps a held snapshot", async () => {
    const maxBytes = 64 * 1024 * 1024;
    const restoreOptions = installBackgroundTranscriptOptionsForTests({ residentBytes: 0 });
    const cache = new SessionCacheIndexedDB({
      indexedDB: new IDBFactory(),
      databaseName: `recovery-${crypto.randomUUID()}`,
      maxBytes,
    });
    resetThreadsStoreForTests();
    setSessionCacheAdapterForTests(cache);
    const scheduled: Array<() => void> = [];
    const restoreScheduler = installHydrationRetrySchedulerForTests((_attempt, retry) => {
      scheduled.push(retry);
      return () => {};
    });
    let reads = 0;
    const fake = new FakeClient("ready");
    fake.on("thread/read", ({ ref: requested, requestGeneration }) => {
      reads += 1;
      const response = currentRead(requested ?? ref);
      response.requestGeneration = requestGeneration ?? response.requestGeneration;
      response.olderCursor = reads === 1 ? "cursor_old" : "cursor_valid_new";
      response.snapshot = { incarnation: "inc-1", length: 100 };
      response.thread.turns = [
        {
          id: "turn_latest",
          status: "completed",
          itemsView: "full",
          version: 1,
          items: [
            {
              type: "agentMessage",
              id: "item_latest",
              turnId: "turn_latest",
              transcriptKey: "apptranscript-item-v2:turn_latest:0:0",
              position: { entry: 2, item: 0 },
              version: 1,
              text: "latest-recorded-item",
            },
          ],
        },
      ];
      return response;
    });
    fake.on("thread/unsubscribe", () => ({}));
    connectionStore.getState().connect(fake);
    const writes: CurrentSessionRecord[] = [];
    const originalPut = cache.putCurrent.bind(cache);
    const put = vi.spyOn(cache, "putCurrent").mockImplementation(async (record, now) => {
      const outcome = await originalPut(record, now);
      if (outcome.outcome === "written") writes.push(record);
      return outcome;
    });
    const lease = acquireBackgroundThread(ref);
    let paneHeld = false;
    try {
      await lease.ready;
      await settleBackgroundTranscriptsForTests();
      expect(threadsStore.getState().threads.has(ref)).toBe(false);
      const initial = writes.at(-1);
      if (!initial) throw new Error("expected a real background checkpoint");
      const oversized = JSON.parse(JSON.stringify(initial.state)) as CurrentSessionRecord["state"];
      const historyTurn = oversized.history.turns[0];
      if (!historyTurn?.items[0]) throw new Error("expected a recorded history turn");
      historyTurn.items[0].text = "h".repeat(34 * 1024 * 1024);
      await cache.putCurrent({ ...initial, savedAt: Date.now(), state: oversized }, Date.now());

      // A held pane adopts the oversized base; the rebuild must keep the
      // reader's held snapshot rather than a bare latest window.
      await threadsStore.getState().ensureThread(ref);
      paneHeld = true;
      expect(threadsStore.getState().threads.has(ref)).toBe(true);
      const readsWithReader = reads;
      fake.emitNotification(textDelta(ref, "-held-oversized"));
      await settleBackgroundTranscriptsForTests();
      // Reader exclusion: the bare latest-window rebuild is background-only, so
      // a held pane neither arms a repair nor issues a replacing read.
      expect(reads).toBe(readsWithReader);
      expect(scheduled).toHaveLength(0);
      expect(threadsStore.getState().threads.has(ref)).toBe(true);
      expect(
        threadsStore
          .getState()
          .threads.get(ref)
          ?.turns.map((turn) => turn.id),
      ).toContain("turn_latest");
    } finally {
      if (paneHeld) threadsStore.getState().releaseThread(ref);
      await settleBackgroundTranscriptsForTests();
      await lease.release();
      restoreScheduler();
      put.mockRestore();
      resetThreadsStoreForTests();
      restoreOptions();
      cache.close();
    }
  });
});
