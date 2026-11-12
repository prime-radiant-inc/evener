// Task 6 recovery: disk-backed current checkpoints must survive a storage
// fault, a sibling clear with no broadcast, and an expired promotion, without
// falsely claiming freshness or resurrecting cleared content. Every case uses
// the real store, the real reducer and a real IndexedDB adapter; the seams only
// hold real storage results at deterministic transaction boundaries.
import "fake-indexeddb/auto";
import type { AnyNotification } from "@evener/appwire-client";
import { currentRead } from "@evener/appwire-client/testing/currentTranscript";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { IDBFactory, IDBKeyRange } from "fake-indexeddb";
import { describe, expect, test, vi } from "vitest";
import { connectionStore, resetConnectionStoreForTests } from "./connection";
import { setProductionEntryCacheForTests } from "./entryTranscriptRoutes";
import { MutationOutboxIndexedDB } from "./mutationOutboxIndexedDB";
import { SessionEntryCache } from "./sessionEntryCache";
import { observeProductionEntryForTests } from "./testing/entryProductionHarness";
import {
  acquireBackgroundThread,
  installHydrationRetrySchedulerForTests,
  resetThreadsStoreForTests,
  setMutationStorageForTests,
  settleBackgroundTranscriptsForTests,
  threadsStore,
} from "./threads";

const ref = "local:one";

function _connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  fake.on("thread/read", ({ ref: requested }) => currentRead(requested ?? ref));
  fake.on("thread/unsubscribe", () => ({}));
  connectionStore.getState().connect(fake);
  return fake;
}

function _threadReads(fake: FakeClient): number {
  return fake.calls.filter((call) => call.method === "thread/read").length;
}

describe("background transcript recovery", () => {
  test("foreground retry survives a completed background-only ownership lifetime", async () => {
    resetThreadsStoreForTests();
    const cache = new SessionEntryCache({
      indexedDB: new IDBFactory(),
      keyRange: IDBKeyRange,
      databaseName: "foreground-retry-generation",
    });
    setProductionEntryCacheForTests(cache);
    const retries: Array<() => void> = [];
    const restoreScheduler = installHydrationRetrySchedulerForTests((_attempt, retry) => {
      let active = true;
      retries.push(() => {
        if (active) retry();
      });
      return () => {
        active = false;
      };
    });
    try {
      const fake = _connectFakeClient();
      const background = acquireBackgroundThread(ref);
      await background.ready;
      await background.release();
      expect(threadsStore.getState().threads.has(ref)).toBe(false);
      // No cached seed may hide the foreground transport failure.
      await cache.clear();
      let reads = 0;
      fake.on("thread/read", (params) => {
        if (++reads === 1) throw new Error("transient foreground read failure");
        return { ...currentRead(ref), requestGeneration: params.requestGeneration };
      });
      const opening = threadsStore
        .getState()
        .ensureThread(ref)
        .then(
          () => ({ ok: true }),
          (error: unknown) => ({ ok: false, error }),
        );
      await vi.waitFor(() => expect(retries).toHaveLength(1));
      retries[0]!();
      await expect(opening).resolves.toEqual({ ok: true });
      expect(reads).toBe(2);
      expect(
        threadsStore
          .getState()
          .threads.get(ref)
          ?.turns.flatMap((turn) => turn.items)
          .map((item) => item.text),
      ).toContain("alpha");
    } finally {
      restoreScheduler();
      resetThreadsStoreForTests();
      resetConnectionStoreForTests();
      cache.close();
    }
  });

  test("a sibling Clear of the entry cache lets a fresh explicit open re-cache under the new epoch", async () => {
    const selected = "local:entry-sibling-clear";
    const factory = new IDBFactory();
    const databaseName = `entry-sibling-clear-${crypto.randomUUID()}`;
    const entryCache = new SessionEntryCache({ indexedDB: factory, keyRange: IDBKeyRange, databaseName });
    const sibling = new SessionEntryCache({ indexedDB: factory, keyRange: IDBKeyRange, databaseName });
    resetThreadsStoreForTests();
    const fixture = observeProductionEntryForTests(entryCache, selected);
    try {
      // A real recorded item so the entry repository has something to cache.
      const fake = new FakeClient("ready");
      fake.on("thread/read", (params) => {
        const response = currentRead(params.ref ?? selected);
        response.snapshot = { incarnation: "inc-1", length: 1 };
        response.thread.turns = [
          {
            id: "turn_1",
            itemsView: "full",
            status: "completed",
            version: 1,
            items: [
              {
                type: "agentMessage",
                id: "item-1",
                transcriptKey: "item-1",
                turnId: "turn_1",
                position: { entry: 1, item: 0 },
                version: 1,
                text: "ENTRY_CLEAR_BODY",
              },
            ],
          },
        ];
        return { ...response, requestGeneration: params.requestGeneration };
      });
      fake.on("thread/unsubscribe", () => ({}));
      connectionStore.getState().connect(fake);
      await threadsStore.getState().ensureThread(selected);
      await fixture.settled();
      const historyKey = fixture.inspect().historyKey;
      if (!historyKey) throw new Error("expected the fixture's resolved history key");
      await expect(entryCache.getLatest(historyKey, 10)).resolves.toHaveLength(1);

      // Release the pane so the next explicit open is a fresh acquisition that
      // must re-hydrate.
      threadsStore.getState().releaseThread(selected);
      await settleBackgroundTranscriptsForTests();

      // A sibling Clear on the same database with no broadcast.
      await expect(sibling.clear()).resolves.toEqual({ committed: true, epoch: 1 });
      await expect(entryCache.getLatest(historyKey, 10)).resolves.toEqual([]);

      // The fresh explicit foreground acquisition reads the new epoch, so it
      // caches again instead of carrying the pre-clear authority.
      await threadsStore.getState().ensureThread(selected);
      await fixture.settled();
      await expect(entryCache.getLatest(historyKey, 10)).resolves.toHaveLength(1);
      expect(await entryCache.getHistoryGeneration(historyKey)).toBe(0);
    } finally {
      threadsStore.getState().releaseThread(selected);
      await settleBackgroundTranscriptsForTests();
      fixture.dispose();
      entryCache.close();
      sibling.close();
      resetThreadsStoreForTests();
      await new Promise<void>((resolve) => {
        const request = factory.deleteDatabase(databaseName);
        request.onsuccess = () => resolve();
        request.onerror = () => resolve();
      });
    }
  });

  test("a sibling Clear leaves an alive foreground holder uncached and does not restart caching", async () => {
    const selected = "local:entry-sibling-holder";
    const factory = new IDBFactory();
    const databaseName = `entry-sibling-holder-${crypto.randomUUID()}`;
    const entryCache = new SessionEntryCache({ indexedDB: factory, keyRange: IDBKeyRange, databaseName });
    const sibling = new SessionEntryCache({ indexedDB: factory, keyRange: IDBKeyRange, databaseName });
    resetThreadsStoreForTests();
    const fixture = observeProductionEntryForTests(entryCache, selected);
    let commitSpy: ReturnType<typeof vi.spyOn> | undefined;
    try {
      const fake = new FakeClient("ready");
      fake.on("thread/read", (params) => {
        const response = currentRead(params.ref ?? selected);
        response.snapshot = { incarnation: "inc-1", length: 1 };
        response.thread.turns = [
          {
            id: "turn_1",
            itemsView: "full",
            status: "completed",
            version: 1,
            items: [
              {
                type: "agentMessage",
                id: "item-1",
                transcriptKey: "item-1",
                turnId: "turn_1",
                position: { entry: 1, item: 0 },
                version: 1,
                text: "SIBLING_HOLDER_BODY",
              },
            ],
          },
        ];
        return { ...response, requestGeneration: params.requestGeneration };
      });
      fake.on("thread/unsubscribe", () => ({}));
      connectionStore.getState().connect(fake);
      await threadsStore.getState().ensureThread(selected);
      await fixture.settled();
      const historyKey = fixture.inspect().historyKey;
      if (!historyKey) throw new Error("expected the fixture's resolved history key");
      await expect(entryCache.getLatest(historyKey, 10)).resolves.toHaveLength(1);

      // A sibling Clear with no broadcast while the foreground holder stays alive.
      await expect(sibling.clear()).resolves.toEqual({ committed: true, epoch: 1 });
      await expect(entryCache.getLatest(historyKey, 10)).resolves.toEqual([]);

      // Re-entering with the holder alive neither hydrates nor restarts caching.
      await threadsStore.getState().ensureThread(selected);
      await settleBackgroundTranscriptsForTests();
      await expect(entryCache.getLatest(historyKey, 10)).resolves.toEqual([]);
      expect(threadsStore.getState().threads.get(selected)).toBeDefined();

      // A real persistence attempt under the old (pre-sibling-Clear) epoch is
      // refused: the sibling Clear's suppression stands without a new
      // acquisition, while the readable model is retained. Record the batch
      // outcomes so an empty cache is proved a refused write, not a write never
      // attempted.
      const threadId = threadsStore.getState().threads.get(selected)?.threadId;
      if (!threadId) throw new Error("expected the held model");
      const outcomes: string[] = [];
      const realCommit = entryCache.commitBatch.bind(entryCache);
      commitSpy = vi.spyOn(entryCache, "commitBatch").mockImplementation(async (batch) => {
        const outcome = await realCommit(batch);
        outcomes.push(outcome.outcome === "refused" ? `refused:${outcome.reason}` : outcome.outcome);
        return outcome;
      });
      fake.emitNotification({
        method: "history/updated",
        params: {
          ref: selected,
          threadId,
          bootGeneration: "1",
          epoch: 1,
          snapshot: { incarnation: "inc-1", length: 2 },
          turns: [{ id: "holder-live", status: "completed", itemsView: "full", version: 2 }],
          items: [
            {
              type: "agentMessage",
              id: "holder-live-item",
              transcriptKey: "holder-live-item",
              turnId: "holder-live",
              version: 2,
              text: "HOLDER_LIVE_BODY",
              position: { entry: 2, item: 0 },
            },
          ],
        },
      } as AnyNotification);
      await fixture.settled();
      await settleBackgroundTranscriptsForTests();
      // The notification was genuinely accepted (the held display advanced) and
      // its write reached the cache and was refused by the durable clear-epoch
      // fence, so an empty cache here is a refused persistence attempt, not an
      // upstream discard.
      expect(JSON.stringify(threadsStore.getState().threads.get(selected) ?? null)).toContain("HOLDER_LIVE_BODY");
      expect(outcomes).toContain("refused:clear-epoch");
      await expect(entryCache.getLatest(historyKey, 10)).resolves.toEqual([]);
      await expect(entryCache.getByteTotals(historyKey)).resolves.toMatchObject({ historyBytes: 0 });
      expect(threadsStore.getState().threads.get(selected)).toBeDefined();
    } finally {
      commitSpy?.mockRestore();
      threadsStore.getState().releaseThread(selected);
      await settleBackgroundTranscriptsForTests();
      fixture.dispose();
      entryCache.close();
      sibling.close();
      resetThreadsStoreForTests();
      await new Promise<void>((resolve) => {
        const request = factory.deleteDatabase(databaseName);
        request.onsuccess = () => resolve();
        request.onerror = () => resolve();
      });
    }
  });
});

describe("accepted-copy journal replay", () => {
  // RoboRev on 1973ead74e: the end-disposition read caches the replay key
  // BEFORE the journal ask resolves. A transient failure must not leave the
  // memo claimed, or every later read with the same identities skips the
  // journal forever and an off-line-delivered copy stays a Delivering row.
  test("a failed journal ask does not block the next end-disposition read's re-ask", { timeout: 30_000 }, async () => {
    resetThreadsStoreForTests();
    const selected = "local:replay-retry";
    const storage = new MutationOutboxIndexedDB({ createMutationId: () => "m1" });
    await storage.enqueueIntent({
      targetRef: selected,
      method: "turn/queue",
      payload: { ref: selected, input: [{ type: "text", text: "offline send" }] },
      attachments: [],
      optimisticDisplay: { method: "turn/queue", input: [{ type: "text", text: "offline send" }] },
    });
    await storage.markAttempted("m1");
    await storage.settleReceipt("m1", "pending");
    setMutationStorageForTests(storage);
    const cache = new SessionEntryCache({
      indexedDB: new IDBFactory(),
      keyRange: IDBKeyRange,
      databaseName: "replay-retry",
    });
    setProductionEntryCacheForTests(cache);
    const fixture = observeProductionEntryForTests(cache, selected);
    try {
      const fake = new FakeClient("ready");
      fake.on("thread/read", ({ ref: requested }) => {
        const response = currentRead(requested ?? selected);
        response.snapshot = { incarnation: "inc-1", length: 1 };
        response.thread.evener.mutationStateAuthoritative = true;
        response.thread.turns = [
          {
            id: "turn_recipient",
            itemsView: "full",
            status: "completed",
            version: 1,
            items: [
              {
                type: "userMessage",
                id: "item-other",
                turnId: "turn_recipient",
                clientMutationId: "m-other",
                version: 1,
                text: "delivered elsewhere",
              },
            ],
          },
        ];
        return { ...response };
      });
      fake.on("thread/unsubscribe", () => ({}));
      let queueCalls = 0;
      let heldThreadId = "";
      fake.on("turn/queue", () => {
        queueCalls += 1;
        // The tracked-hydration read may ask first; both it and the first
        // end-disposition ask fail under the transient backpressure, so the
        // failure the memo must survive lands on the memo-gated path.
        if (queueCalls <= 2) throw new Error("journal busy");
        return {
          receipt: {
            clientMutationId: "m1",
            disposition: "replayed",
            threadId: heldThreadId,
            projectionState: "reflected",
          },
        };
      });
      connectionStore.getState().connect(fake);
      await threadsStore.getState().ensureThread(selected);
      await fixture.settled();
      const threadId = threadsStore.getState().threads.get(selected)?.threadId;
      if (!threadId) throw new Error("expected the held model");
      heldThreadId = threadId;

      // The end disposition is the repository read that consults the replay
      // memo: its ask fails under transient backpressure, so the memo it
      // claimed must be reopened for the next read.
      fake.emitNotification({ method: "overlay/end", params: { ref: selected, threadId, roundId: "first" } });
      await vi.waitFor(() => expect(queueCalls).toBe(2));
      await fixture.settled();
      expect(await storage.getOptimistic("m1")).toBeDefined();

      // A second end disposition with the SAME authoritative identities must
      // ask the journal again, not skip on the memo the failed pass claimed.
      fake.emitNotification({ method: "overlay/end", params: { ref: selected, threadId, roundId: "second" } });
      await vi.waitFor(() => expect(queueCalls).toBe(3), { timeout: 10_000 });
      await vi.waitFor(
        async () => {
          expect(await storage.getOptimistic("m1")).toBeUndefined();
        },
        { timeout: 10_000 },
      );
    } finally {
      threadsStore.getState().releaseThread(selected);
      fixture.dispose();
      storage.close();
      cache.close();
      resetThreadsStoreForTests();
    }
  });
});
