import "fake-indexeddb/auto";
import {
  type AnyNotification,
  currentTranscriptState,
  hydrateThread,
  type ThreadModel,
  type ThreadReadResponse,
} from "@evener/appwire-client";
import { currentRead, textDelta } from "@evener/appwire-client/testing/currentTranscript";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { installBackgroundTranscriptOptionsForTests } from "./backgroundTranscriptOptions";
import { connectionStore } from "./connection";
import { type CurrentSessionRecord, SessionCacheIndexedDB } from "./sessionCacheIndexedDB";
import {
  acquireBackgroundThread,
  backgroundTranscriptSnapshotForTests,
  resetThreadsStoreForTests,
  setSessionCacheAdapterForTests,
  settleBackgroundTranscriptsForTests,
  threadsStore,
} from "./threads";

// Independent measurement: the same UTF-8 JSON size the app's own encoder is
// asserted against, computed without reusing product code.
function independentJsonBytes(value: unknown): number {
  const encoded = JSON.stringify(value);
  if (encoded === undefined) return Number.POSITIVE_INFINITY;
  return new TextEncoder().encode(encoded).byteLength;
}

function bodyBytes(model: ThreadModel | undefined): number {
  if (model === undefined) return 0;
  return independentJsonBytes(currentTranscriptState(model));
}

function readWithText(ref: string, text: string): ThreadReadResponse {
  const response = currentRead(ref);
  response.overlay![0]!.item.text = text;
  return response;
}

const databaseName = `background-accounting-${crypto.randomUUID()}`;
let cache: SessionCacheIndexedDB;

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  cache = new SessionCacheIndexedDB({ indexedDB: new IDBFactory(), databaseName });
  setSessionCacheAdapterForTests(cache);
});

afterEach(() => {
  resetThreadsStoreForTests();
  cache.close();
});

describe("background accounting", () => {
  test("charges the exact retained body of an authoritative re-read that replaces an already-charged resident", async () => {
    const ref = "local:pressure";
    const restoreOptions = installBackgroundTranscriptOptionsForTests({
      residentBytes: 1 << 20,
      pendingBytes: 4 << 20,
    });
    const fake = new FakeClient("ready");
    let holdRead = false;
    let nextRead: ThreadReadResponse | undefined;
    const readStarted = deferred<void>();
    const releaseRead = deferred<void>();
    fake.on("thread/read", async ({ ref: requested }) => {
      if (holdRead && requested === ref) {
        readStarted.resolve();
        await releaseRead.promise;
      }
      return nextRead ?? currentRead(requested ?? ref);
    });
    fake.on("thread/unsubscribe", () => ({}));
    connectionStore.getState().connect(fake);
    const lease = acquireBackgroundThread(ref);
    try {
      await lease.ready;
      await settleBackgroundTranscriptsForTests();
      const initialEntry = backgroundTranscriptSnapshotForTests().refs.get(ref);
      const initialBody = bodyBytes(threadsStore.getState().threads.get(ref));
      expect(initialEntry?.residentBytes ?? 0).toBeGreaterThanOrEqual(initialBody);
      expect(initialBody).toBeGreaterThan(0);

      // A held authoritative re-read. While it is in flight, a live delta is
      // buffered for replay onto the returned snapshot (bufferPendingNotification
      // -> reservePending), so it never enters entry.pending and its wire bytes
      // are not available to the incremental charge formula.
      holdRead = true;
      fake.emitNotification({
        method: "evener/thread/resync",
        params: { ref, threadId: `thr_${ref}` },
      } as AnyNotification);
      await readStarted.promise;
      fake.emitNotification(textDelta(ref, "🙂".repeat(2_500)));

      // Non-ASCII text plus structured live overlay data in the replacement
      // body, the same classes the real diagnostic measured.
      const big = readWithText(ref, "工具".repeat(34_000));
      big.overlay!.push({
        key: "stream:accounting/toolOutput",
        kind: "stream",
        streamId: "accounting",
        roundId: "accounting/0",
        turnId: "turn_1",
        item: {
          id: "item_accounting_tool",
          turnId: "turn_1",
          type: "commandExecution",
          output: JSON.stringify({ structured: { output: ["工具", 7, { retained: true }] } }),
          status: "inProgress",
        },
      });
      const base = threadsStore.getState().threads.get(ref);
      big.requestGeneration = base?.history?.issuedGeneration ?? 1;
      nextRead = big;
      releaseRead.resolve();

      await vi.waitFor(
        () => {
          const entry = backgroundTranscriptSnapshotForTests().refs.get(ref);
          if (!entry || entry.appliedSequence < 1) throw new Error("not published yet");
        },
        { timeout: 4_000 },
      );

      const model = threadsStore.getState().threads.get(ref);
      const retained = bodyBytes(model);
      const entry = backgroundTranscriptSnapshotForTests().refs.get(ref);
      const charged = (entry?.residentBytes ?? 0) + (entry?.handoffBytes ?? 0);
      expect(retained).toBeGreaterThan(100_000);
      // The independent retained-byte measurement is exactly the app encoder's.
      expect(retained).toBe(independentJsonBytes(currentTranscriptState(model!)));
      // The charge follows the whole retained body, not the stale 1,139-byte base.
      expect(charged).toBeGreaterThanOrEqual(retained);
      expect(charged).toBe(retained);
    } finally {
      releaseRead.resolve();
      await settleBackgroundTranscriptsForTests();
      await lease.release();
      resetThreadsStoreForTests();
      restoreOptions();
    }
  });

  test("evicts the least-recently-used unopened background target to disk under the diagnostic limits, then promotes it locally", async () => {
    // The unchanged strict browser limits: 64 KiB resident / 32 KiB pending.
    const restoreOptions = installBackgroundTranscriptOptionsForTests({
      residentBytes: 64 * 1024,
      pendingBytes: 32 * 1024,
    });
    const target = "local:target";
    const pressureA = "local:pressureA";
    const pressureB = "local:pressureB";
    const bodies: Record<string, ThreadReadResponse> = {
      [target]: readWithText(target, "t".repeat(27_250)),
      [pressureA]: readWithText(pressureA, "a".repeat(18_900)),
      [pressureB]: readWithText(pressureB, "b".repeat(18_900)),
    };
    const fake = new FakeClient("ready");
    fake.on("thread/read", ({ ref }) => bodies[ref ?? ""] ?? currentRead(ref ?? target));
    fake.on("thread/unsubscribe", () => ({}));
    connectionStore.getState().connect(fake);
    const writes: CurrentSessionRecord[] = [];
    const originalPut = cache.putCurrent.bind(cache);
    const put = vi.spyOn(cache, "putCurrent").mockImplementation(async (record, now) => {
      const outcome = await originalPut(record, now);
      if (outcome.outcome === "written") writes.push(record);
      return outcome;
    });
    const targetLease = acquireBackgroundThread(target);
    const aLease = acquireBackgroundThread(pressureA);
    const bLease = acquireBackgroundThread(pressureB);
    try {
      await Promise.all([targetLease.ready, aLease.ready, bLease.ready]);
      await settleBackgroundTranscriptsForTests();

      const snapshot = backgroundTranscriptSnapshotForTests();
      const targetBody = bodyBytes(hydrateThread(bodies[target]!, target, 1));
      // A 28,390-byte-class unopened target.
      expect(targetBody).toBeGreaterThan(26_000);
      expect(targetBody).toBeLessThan(30_000);
      // The target was the least-recently-used resident: never foregrounded,
      // admitted first, and evicted once the two growing pressure bodies
      // overflowed the resident budget through an individually admissible
      // candidate (each <= the 32 KiB pending budget).
      expect(snapshot.refs.get(target)?.resident).toBe(false);
      expect(threadsStore.getState().threads.has(target)).toBe(false);
      expect(snapshot.refs.get(pressureA)?.resident).toBe(true);
      expect(snapshot.refs.get(pressureB)?.resident).toBe(true);
      expect(snapshot.residentBytes).toBeLessThanOrEqual(64 * 1024);

      // The evicted target has a committed, current checkpoint on disk before
      // any further update.
      const targetWrite = writes.filter((write) => write.ref === target).at(-1);
      if (!targetWrite) throw new Error("expected the evicted target to commit a checkpoint");
      const onDisk = await cache.getCurrent([targetWrite.ownerToken, target], Date.now());
      expect(onDisk?.record?.ref).toBe(target);
      expect(snapshot.refs.get(target)?.current).toBe(true);

      // A literal post-eviction delta is applied exactly once on local
      // promotion, with no new network read.
      const readsBefore = fake.calls.filter((call) => call.method === "thread/read").length;
      const marker = "-post-eviction";
      fake.emitNotification(textDelta(target, marker));
      await threadsStore.getState().ensureThread(target);
      const promoted = threadsStore.getState().threads.get(target);
      const promotedText = JSON.stringify(promoted ? currentTranscriptState(promoted) : undefined);
      expect(promotedText.split(marker)).toHaveLength(2);
      expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(readsBefore);
      await settleBackgroundTranscriptsForTests();
      const finalWrite = writes.filter((write) => write.ref === target).at(-1);
      const finalDisk = await cache.getCurrent([finalWrite!.ownerToken, target], Date.now());
      expect(JSON.stringify(finalDisk?.record?.state).split(marker)).toHaveLength(2);
    } finally {
      put.mockRestore();
      await settleBackgroundTranscriptsForTests();
      await Promise.all([targetLease.release(), aLease.release(), bLease.release()]);
      resetThreadsStoreForTests();
      restoreOptions();
    }
  });
});
