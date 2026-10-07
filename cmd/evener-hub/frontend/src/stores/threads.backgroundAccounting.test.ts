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
import { holdNextWriteTransaction } from "./testing/stalledIndexedDB";
import {
  acquireBackgroundThread,
  backgroundTranscriptSnapshotForTests,
  pendingThreadHydrationBaseModelsForTests,
  resetThreadsStoreForTests,
  setCacheWriteTimersForTests,
  setSessionCacheAdapterForTests,
  settleBackgroundTranscriptsForTests,
  threadsStore,
} from "./threads";
import { encodedPayloadBytes } from "./transcriptPayloadBudget";
import { observeWorkerCheckpointsForTests, type WorkerCheckpointObservation } from "./transcriptResidency";

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
      // Independent measurement vs the product encoder: the test-local UTF-8
      // JSON size must equal the app's own encodedPayloadBytes for the same
      // canonical state (an actual cross-check, not the same expression twice).
      expect(retained).toBe(encodedPayloadBytes(currentTranscriptState(model!)));
      // The literal replayed delta and the structured overlay item survive in
      // their domain fields, not merely as a total byte count.
      // Literal domain-field content of the authoritative replacement survives:
      // the non-ASCII stream text and the structured overlay output, not merely
      // a total byte count.
      expect(model?.overlay?.["stream:s1:agentMessage"]?.item?.text).toContain("工具".repeat(34_000));
      expect(model?.overlay?.["stream:accounting/toolOutput"]?.item?.output).toContain(
        '{"structured":{"output":["工具",7,{"retained":true}]}}',
      );
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

  test("does not let an older held write reset a newer authoritative charge", async () => {
    const ref = "local:held";
    const restoreOptions = installBackgroundTranscriptOptionsForTests({
      residentBytes: 4 << 20,
      pendingBytes: 4 << 20,
    });
    setCacheWriteTimersForTests({ debounceMs: 1, maxWaitMs: 1 });
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

    const writes: CurrentSessionRecord[] = [];
    const originalPut = cache.putCurrent.bind(cache);
    let heldRecord: CurrentSessionRecord | undefined;
    let transaction: ReturnType<typeof holdNextWriteTransaction> | undefined;
    const writeStarted = deferred<void>();
    const put = vi.spyOn(cache, "putCurrent").mockImplementation(async (record, now) => {
      if (!transaction && record.ref === ref) {
        transaction = holdNextWriteTransaction(["records", "meta", "current", "currentMeta"]);
        heldRecord = record;
        writeStarted.resolve();
      }
      const outcome = await originalPut(record, now);
      if (outcome.outcome === "written") writes.push(record);
      return outcome;
    });

    // The worker-boundary observer runs inside persist, whose rejection path
    // swallows a thrown assertion; record measurements out of band and assert
    // them at the test boundary instead.
    interface ObservedBoundary {
      observation: WorkerCheckpointObservation;
      charge: number;
      canonical: number;
      workerBytes: number;
      pending: number;
      inFlight: number;
    }
    const observed: ObservedBoundary[] = [];
    const observerFailures: unknown[] = [];
    const restoreObserver = observeWorkerCheckpointsForTests((observation) => {
      if (observation.ref !== ref) return;
      try {
        const snapshot = backgroundTranscriptSnapshotForTests();
        const entry = snapshot.refs.get(ref);
        observed.push({
          observation,
          charge: (entry?.residentBytes ?? 0) + (entry?.handoffBytes ?? 0),
          canonical: bodyBytes(threadsStore.getState().threads.get(ref)),
          workerBytes: snapshot.workerBytes,
          pending: entry?.pending.length ?? 0,
          inFlight: entry?.inFlight.length ?? 0,
        });
      } catch (error) {
        observerFailures.push(error);
      }
    });

    const lease = acquireBackgroundThread(ref);
    try {
      await lease.ready;
      await writeStarted.promise;
      await transaction?.reached;

      // Narrow held-boundary inventory: the worker observed the canonical base
      // by object identity, its batch was empty so its folded model IS that
      // same base, and the record it is writing is the held write record.
      const baseModel = threadsStore.getState().threads.get(ref);
      const base = observed.find((boundary) => boundary.observation.resident === baseModel);
      if (!base) throw new Error("worker did not observe the canonical base by identity");
      expect(base.observation.model).toBe(base.observation.resident);
      expect(base.observation.record).toBe(heldRecord);
      expect(base.workerBytes).toBe(independentJsonBytes(base.observation.record));
      expect(base.pending).toBe(0);
      expect(base.inFlight).toBe(0);
      expect(base.charge).toBeGreaterThanOrEqual(base.canonical);

      // Larger authoritative re-read while the base write is held; echo the
      // pending hydration base's real issued requestGeneration.
      holdRead = true;
      fake.emitNotification({
        method: "evener/thread/resync",
        params: { ref, threadId: `thr_${ref}` },
      } as AnyNotification);
      await readStarted.promise;
      await vi.waitFor(
        () => {
          if (!pendingThreadHydrationBaseModelsForTests().some((candidate) => candidate.ref === ref))
            throw new Error("pending hydration base not present");
        },
        { timeout: 4_000 },
      );
      const pendingBase = pendingThreadHydrationBaseModelsForTests().find((candidate) => candidate.ref === ref);
      const big = readWithText(ref, "R".repeat(100_000));
      big.requestGeneration = pendingBase?.history?.issuedGeneration ?? 1;
      nextRead = big;
      releaseRead.resolve();
      await vi.waitFor(
        () => {
          if (bodyBytes(threadsStore.getState().threads.get(ref)) < 50_000)
            throw new Error("authoritative re-read not published");
        },
        { timeout: 4_000 },
      );
      const afterReadEntry = backgroundTranscriptSnapshotForTests().refs.get(ref);
      expect((afterReadEntry?.residentBytes ?? 0) + (afterReadEntry?.handoffBytes ?? 0)).toBeGreaterThanOrEqual(
        bodyBytes(threadsStore.getState().threads.get(ref)),
      );

      // A later live delta while the write is still held.
      const marker = "-held-later";
      fake.emitNotification(textDelta(ref, marker));
      await vi.waitFor(
        () => {
          const model = threadsStore.getState().threads.get(ref);
          if (!model || !JSON.stringify(currentTranscriptState(model)).includes(marker))
            throw new Error("live delta not published");
        },
        { timeout: 4_000 },
      );
      const heldEntry = backgroundTranscriptSnapshotForTests().refs.get(ref);
      expect((heldEntry?.residentBytes ?? 0) + (heldEntry?.handoffBytes ?? 0)).toBeGreaterThanOrEqual(
        bodyBytes(threadsStore.getState().threads.get(ref)),
      );

      // Release the held write and drain. The charge must not be reset to the
      // write's captured pre-fold size at the next worker boundary.
      transaction?.release();
      await settleBackgroundTranscriptsForTests();
      const settledEntry = backgroundTranscriptSnapshotForTests().refs.get(ref);
      expect((settledEntry?.residentBytes ?? 0) + (settledEntry?.handoffBytes ?? 0)).toBeGreaterThanOrEqual(
        bodyBytes(threadsStore.getState().threads.get(ref)),
      );

      expect(observerFailures).toEqual([]);
      const postHeld = observed.filter((boundary) => boundary !== base);
      expect(postHeld.length, "a worker checkpoint runs after the held write completes").toBeGreaterThanOrEqual(1);
      for (const boundary of postHeld) {
        expect(
          boundary.charge,
          `post-held worker charge ${boundary.charge} must cover retained body ${boundary.canonical}`,
        ).toBeGreaterThanOrEqual(boundary.canonical);
      }
    } finally {
      holdRead = false;
      releaseRead.resolve();
      put.mockRestore();
      transaction?.release();
      restoreObserver();
      await settleBackgroundTranscriptsForTests().catch(() => undefined);
      await lease.release().catch(() => undefined);
      resetThreadsStoreForTests();
      setCacheWriteTimersForTests({ debounceMs: 1_000, maxWaitMs: 5_000 });
      restoreOptions();
    }
  });

  test("evicts an unopened target to disk while one pressure ref grows live under the diagnostic limits, then promotes it locally", async () => {
    // The unchanged strict browser limits: 64 KiB resident / 32 KiB pending.
    const restoreOptions = installBackgroundTranscriptOptionsForTests({
      residentBytes: 64 * 1024,
      pendingBytes: 32 * 1024,
    });
    const target = "local:grow-target";
    const pressure = "local:grow-pressure";
    const bodies: Record<string, ThreadReadResponse> = {
      [target]: readWithText(target, "t".repeat(27_250)),
      [pressure]: readWithText(pressure, "p".repeat(2_000)),
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
    const pressureLease = acquireBackgroundThread(pressure);
    try {
      await Promise.all([targetLease.ready, pressureLease.ready]);
      await settleBackgroundTranscriptsForTests();
      expect(threadsStore.getState().threads.has(target)).toBe(true);
      const targetBody = bodyBytes(threadsStore.getState().threads.get(target));
      expect(targetBody).toBeGreaterThan(26_000);
      expect(targetBody).toBeLessThan(30_000);

      // One pressure ref grows through a live stream; each delta is an ordinary
      // overlay text append, the same style the real browser journey sends.
      const transitions: Array<{
        step: number;
        pressureBody: number;
        pressureCharge: number;
        targetResident: boolean;
        targetResidentBytes: number;
        targetHandoffBytes: number;
        targetCharge: number;
        aggregateResident: number;
      }> = [];
      for (let step = 0; step < 3; step += 1) {
        fake.emitNotification(textDelta(pressure, "q".repeat(12_000)));
        const snapshot = backgroundTranscriptSnapshotForTests();
        transitions.push({
          step,
          pressureBody: bodyBytes(threadsStore.getState().threads.get(pressure)),
          pressureCharge:
            (snapshot.refs.get(pressure)?.residentBytes ?? 0) + (snapshot.refs.get(pressure)?.handoffBytes ?? 0),
          targetResident: snapshot.refs.get(target)?.resident ?? false,
          targetResidentBytes: snapshot.refs.get(target)?.residentBytes ?? 0,
          targetHandoffBytes: snapshot.refs.get(target)?.handoffBytes ?? 0,
          targetCharge:
            (snapshot.refs.get(target)?.residentBytes ?? 0) + (snapshot.refs.get(target)?.handoffBytes ?? 0),
          aggregateResident: snapshot.residentBytes,
        });
      }

      // The target must be genuinely disk-only with a committed current
      // checkpoint, evicted by the growing pressure ref.
      // The transfer moves the victim's whole body charge into pending (its
      // resident charge drops to zero) before its checkpoint/unload, so the
      // retained payload stays covered throughout.
      const staged = transitions.find(
        (transition) => transition.targetResidentBytes === 0 && transition.targetHandoffBytes > 0,
      );
      expect(staged, "target transferred to the handoff budget during the growing stream").toBeDefined();
      expect(staged!.targetHandoffBytes).toBeGreaterThanOrEqual(targetBody);
      await settleBackgroundTranscriptsForTests();
      const evicted = backgroundTranscriptSnapshotForTests();
      expect(evicted.refs.get(target)?.resident, JSON.stringify(transitions)).toBe(false);
      expect(threadsStore.getState().threads.has(target)).toBe(false);
      expect(evicted.residentBytes).toBeLessThanOrEqual(64 * 1024);
      const targetWrite = writes.filter((write) => write.ref === target).at(-1);
      if (!targetWrite) throw new Error("expected the evicted target to commit a checkpoint");
      const onDisk = await cache.getCurrent([targetWrite.ownerToken, target], Date.now());
      expect(onDisk?.record?.ref).toBe(target);
      expect(evicted.refs.get(target)?.current).toBe(true);

      // A literal post-eviction delta is applied exactly once on local promotion
      // with no new network read.
      const readsBefore = fake.calls.filter((call) => call.method === "thread/read").length;
      const marker = "-post-eviction";
      fake.emitNotification(textDelta(target, marker));
      await threadsStore.getState().ensureThread(target);
      const promoted = threadsStore.getState().threads.get(target);
      expect(promoted?.overlay?.["stream:s1:agentMessage"]?.item?.text).toContain(marker);
      const promotedText = JSON.stringify(promoted ? currentTranscriptState(promoted) : undefined);
      expect(promotedText.split(marker)).toHaveLength(2);
      expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(readsBefore);
      await settleBackgroundTranscriptsForTests();
      const finalWrite = writes.filter((write) => write.ref === target).at(-1);
      const finalDisk = await cache.getCurrent([finalWrite!.ownerToken, target], Date.now());
      expect(JSON.stringify(finalDisk?.record?.state).split(marker)).toHaveLength(2);
    } finally {
      put.mockRestore();
      await settleBackgroundTranscriptsForTests().catch(() => undefined);
      await Promise.all([targetLease.release(), pressureLease.release()]);
      resetThreadsStoreForTests();
      restoreOptions();
    }
  });

  test("keeps the bounded refusal path when the only evictable victim body cannot fit the pending budget", async () => {
    const restoreOptions = installBackgroundTranscriptOptionsForTests({
      residentBytes: 64 * 1024,
      pendingBytes: 32 * 1024,
    });
    const target = "local:large-victim";
    const pressure = "local:large-pressure";
    const bodies: Record<string, ThreadReadResponse> = {
      [target]: readWithText(target, "t".repeat(38_900)),
      [pressure]: readWithText(pressure, "p".repeat(2_000)),
    };
    const fake = new FakeClient("ready");
    fake.on("thread/read", ({ ref }) => bodies[ref ?? ""] ?? currentRead(ref ?? target));
    fake.on("thread/unsubscribe", () => ({}));
    connectionStore.getState().connect(fake);
    const targetLease = acquireBackgroundThread(target);
    const pressureLease = acquireBackgroundThread(pressure);
    try {
      await Promise.all([targetLease.ready, pressureLease.ready]);
      await settleBackgroundTranscriptsForTests();
      const targetBody = bodyBytes(threadsStore.getState().threads.get(target));
      // The only evictable victim's body is larger than the whole pending budget,
      // so even a small deficit cannot be staged.
      expect(targetBody).toBeGreaterThan(32 * 1024);
      expect(threadsStore.getState().threads.has(target)).toBe(true);

      // One live delta grows the pressure ref past the aggregate resident limit
      // and past the pending budget in the same step, so the candidate cannot be
      // handoff-staged and no victim can be moved into pending.
      fake.emitNotification(textDelta(pressure, "q".repeat(32_000)));
      const snapshot = backgroundTranscriptSnapshotForTests();
      // Bounded refusal: every victim is left unchanged and nothing leaks.
      expect(snapshot.refs.get(target)?.resident).toBe(true);
      expect(snapshot.refs.get(target)?.handoffBytes ?? 0).toBe(0);
      expect(
        (snapshot.refs.get(target)?.residentBytes ?? 0) + (snapshot.refs.get(target)?.handoffBytes ?? 0),
      ).toBeGreaterThanOrEqual(targetBody);
      expect(snapshot.pendingBytes).toBe(0);
      expect(snapshot.residentBytes).toBeLessThanOrEqual(64 * 1024);
      expect(snapshot.refs.get(pressure)?.persistence).toBe("repairing");
    } finally {
      await settleBackgroundTranscriptsForTests().catch(() => undefined);
      await Promise.all([targetLease.release(), pressureLease.release()]);
      resetThreadsStoreForTests();
      restoreOptions();
    }
  });

  test("does not transfer a victim whose worker is busy with an in-flight write", async () => {
    const restoreOptions = installBackgroundTranscriptOptionsForTests({
      residentBytes: 64 * 1024,
      pendingBytes: 32 * 1024,
    });
    setCacheWriteTimersForTests({ debounceMs: 1, maxWaitMs: 1 });
    const target = "local:busy-victim";
    const pressure = "local:busy-pressure";
    const bodies: Record<string, ThreadReadResponse> = {
      [target]: readWithText(target, "t".repeat(27_250)),
      [pressure]: readWithText(pressure, "p".repeat(2_000)),
    };
    const fake = new FakeClient("ready");
    fake.on("thread/read", ({ ref }) => bodies[ref ?? ""] ?? currentRead(ref ?? target));
    fake.on("thread/unsubscribe", () => ({}));
    connectionStore.getState().connect(fake);
    let transaction: ReturnType<typeof holdNextWriteTransaction> | undefined;
    const writeStarted = deferred<void>();
    const originalPut = cache.putCurrent.bind(cache);
    const put = vi.spyOn(cache, "putCurrent").mockImplementation(async (record, now) => {
      if (!transaction && record.ref === target) {
        transaction = holdNextWriteTransaction(["records", "meta", "current", "currentMeta"]);
        writeStarted.resolve();
      }
      return originalPut(record, now);
    });
    const targetLease = acquireBackgroundThread(target);
    const pressureLease = acquireBackgroundThread(pressure);
    try {
      await Promise.all([targetLease.ready, pressureLease.ready]);
      await writeStarted.promise;
      await transaction?.reached;
      expect(threadsStore.getState().threads.has(target)).toBe(true);
      const targetBody = bodyBytes(threadsStore.getState().threads.get(target));

      // The busy target is not an eligible victim, so even though its body fits
      // the pending budget the candidate takes the bounded refusal path.
      fake.emitNotification(textDelta(pressure, "q".repeat(32_000)));
      const snapshot = backgroundTranscriptSnapshotForTests();
      expect(snapshot.refs.get(target)?.resident).toBe(true);
      expect(snapshot.refs.get(target)?.handoffBytes ?? 0).toBe(0);
      expect(
        (snapshot.refs.get(target)?.residentBytes ?? 0) + (snapshot.refs.get(target)?.handoffBytes ?? 0),
      ).toBeGreaterThanOrEqual(targetBody);
      expect(snapshot.pendingBytes).toBe(0);
    } finally {
      put.mockRestore();
      transaction?.release();
      await settleBackgroundTranscriptsForTests().catch(() => undefined);
      await Promise.all([targetLease.release(), pressureLease.release()]);
      resetThreadsStoreForTests();
      setCacheWriteTimersForTests({ debounceMs: 1_000, maxWaitMs: 5_000 });
      restoreOptions();
    }
  });

  test("keeps a transferred victim's staged payload covered when its write fails, and releases it on ownership end", async () => {
    const restoreOptions = installBackgroundTranscriptOptionsForTests({
      residentBytes: 64 * 1024,
      pendingBytes: 32 * 1024,
    });
    const target = "local:fail-victim";
    const pressure = "local:fail-pressure";
    const bodies: Record<string, ThreadReadResponse> = {
      [target]: readWithText(target, "t".repeat(27_250)),
      [pressure]: readWithText(pressure, "p".repeat(2_000)),
    };
    const fake = new FakeClient("ready");
    fake.on("thread/read", ({ ref }) => bodies[ref ?? ""] ?? currentRead(ref ?? target));
    fake.on("thread/unsubscribe", () => ({}));
    connectionStore.getState().connect(fake);
    let failNextTargetWrite = false;
    const originalPut = cache.putCurrent.bind(cache);
    const put = vi.spyOn(cache, "putCurrent").mockImplementation(async (record, now) => {
      if (failNextTargetWrite && record.ref === target) {
        failNextTargetWrite = false;
        return { outcome: "failed" };
      }
      return originalPut(record, now);
    });
    const targetLease = acquireBackgroundThread(target);
    const pressureLease = acquireBackgroundThread(pressure);
    try {
      await Promise.all([targetLease.ready, pressureLease.ready]);
      await settleBackgroundTranscriptsForTests();
      const targetBody = bodyBytes(threadsStore.getState().threads.get(target));
      expect(targetBody).toBeGreaterThan(26_000);

      // Grow pressure so the target is transferred into the handoff budget; the
      // target's eviction write then fails, so its staged charge must still cover
      // the retained body until paced recovery reconciles it.
      failNextTargetWrite = true;
      for (let step = 0; step < 3; step += 1) fake.emitNotification(textDelta(pressure, "q".repeat(12_000)));
      const staged = backgroundTranscriptSnapshotForTests();
      // The victim's charge has moved from resident to the handoff budget (its
      // model may still be present until the failed write's unload path).
      expect(staged.refs.get(target)?.residentBytes ?? 0).toBe(0);
      expect(staged.refs.get(target)?.handoffBytes ?? 0).toBeGreaterThanOrEqual(targetBody);
      expect(staged.pendingBytes).toBeGreaterThanOrEqual(targetBody);

      // Ending the ownership releases the staged handoff charge: no leaked
      // reservation survives an ownership change.
      await targetLease.release();
      await settleBackgroundTranscriptsForTests();
      const finalSnapshot = backgroundTranscriptSnapshotForTests();
      expect(
        finalSnapshot.pendingBytes,
        JSON.stringify([...finalSnapshot.refs].map(([r, e]) => [r, e.residentBytes, e.handoffBytes, e.persistence])),
      ).toBe(0);
    } finally {
      put.mockRestore();
      await settleBackgroundTranscriptsForTests().catch(() => undefined);
      await Promise.all([targetLease.release(), pressureLease.release()]);
      resetThreadsStoreForTests();
      restoreOptions();
    }
  });
});
