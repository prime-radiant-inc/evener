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
import { holdNextWriteTransaction, nextRealTask } from "./testing/stalledIndexedDB";
import {
  acquireBackgroundThread,
  backgroundTranscriptSnapshotForTests,
  installHydrationRetrySchedulerForTests,
  pendingThreadHydrationBaseModelsForTests,
  pendingThreadHydrationUpdatesForTests,
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

      // Required proof A: simultaneous retained-payload inventory at this
      // suspended boundary (the write is still held, AFTER the 100,000-char
      // authoritative replacement and the later accepted delta). Distinct
      // retained payloads are accounted once against the correct class:
      //  - ordinary resident class: the newer canonical body in the store;
      //  - worker class: the held write's captured OLD base body and record,
      //    which is the separately bounded single cache-sized worker.
      const canonicalAfterDelta = threadsStore.getState().threads.get(ref);
      if (!canonicalAfterDelta) throw new Error("expected the newer canonical body at the held boundary");
      const canonicalBytes = bodyBytes(canonicalAfterDelta);
      expect(canonicalBytes).toBeGreaterThan(100_000);
      // The authoritative replacement and the literal accepted delta survive in
      // their domain field (the stream text), not merely as a total byte count.
      const canonicalText = canonicalAfterDelta.overlay?.["stream:s1:agentMessage"]?.item?.text;
      expect(canonicalText).toContain("R".repeat(100_000));
      expect(canonicalText).toContain(marker);

      // The worker's folded model IS the base it entered with (its batch was
      // empty, so it folded nothing), and that base is a genuinely distinct
      // retained OBJECT from the newer canonical body: real object identity, not
      // a fresh codec wrapper or equal JSON.
      expect(base.observation.batch).toHaveLength(0);
      expect(base.observation.model).toBe(base.observation.resident);
      expect(base.observation.resident).toBe(baseModel);
      expect(base.observation.resident).not.toBe(canonicalAfterDelta);
      if (!baseModel) throw new Error("expected the old base model at the held boundary");

      const liveBoundary = backgroundTranscriptSnapshotForTests();
      const liveWorkerCharge = liveBoundary.workerBytes;
      const liveHeldRecord = base.observation.record;
      const oldBaseModel = base.observation.resident;
      if (!oldBaseModel) throw new Error("expected the retained old base model at the held boundary");

      // P1 (approved scalar oracle): classify the held record's body as the one
      // worker-class encoded payload by SOURCE CONSTRUCTION plus RUNTIME SHAPE,
      // not by an equal byte count. currentTranscriptState
      // (appwire-client/typescript/reducer.ts) builds the record's state by:
      //   - 2198-2200: `fields[key] = model[key]`  direct assignment, no copy;
      //   - 2204-2206: a history wrapper, then encodeCachedItem per item;
      //   - 2208-2210: overlay `{...item, item: {...item.item}}` spread, whose
      //     leaves are the base item's own values;
      //   - 1345-1354: encodeCachedItem returns `{...item}` and only COPIES a
      //     payload array when pendingText / reasoningSummaries is defined.
      // Equal JSON size does not establish sharing, and a fresh wrapper does not
      // either. The runtime shape below shows this fixture takes none of the
      // copying branches, so the record's state holds no independently
      // materialized payload: it is the old base model's content reflected
      // through fresh, non-payload wrappers.
      const overlayKey = "stream:s1:agentMessage";
      const stateFields = liveHeldRecord.state.fields as unknown as Record<string, unknown>;
      const stateOverlay = liveHeldRecord.state.overlay[overlayKey];
      const baseOverlay = oldBaseModel.overlay?.[overlayKey];
      if (!stateOverlay || !baseOverlay) throw new Error("expected the overlay item at the held boundary");
      const stateOverlayItem = stateOverlay.item as unknown as Record<string, unknown>;
      const baseOverlayItem = baseOverlay.item as unknown as Record<string, unknown>;

      // (2) Text-bearing item shape and values in the old model versus the held
      // state: the same scalar leaf set, the same primitive types, and the same
      // literal value. The payload text is a primitive, so no allocation
      // identity is claimed for it, only the real value.
      const baseText = baseOverlayItem.text;
      expect(typeof baseText).toBe("string");
      expect(baseText).toBe(baseModel.overlay?.[overlayKey]?.item?.text);
      expect(Object.keys(stateOverlayItem).sort()).toEqual(Object.keys(baseOverlayItem).sort());
      for (const key of Object.keys(stateOverlayItem)) {
        expect(typeof stateOverlayItem[key], `state item leaf ${key}`).toBe(typeof baseOverlayItem[key]);
      }
      expect(stateOverlayItem.text).toBe(baseText);
      // Distinct payload inventory: the text-bearing overlay item carries no
      // object-valued leaf, so there is no independently materialized payload
      // body inside it; the fixture takes neither independently materializing
      // encodeCachedItem branch (no pendingText / reasoningSummaries array).
      expect(Object.values(stateOverlayItem).every((leaf) => leaf === null || typeof leaf !== "object")).toBe(true);
      expect("pendingText" in baseOverlayItem).toBe(false);
      expect("reasoningSummaries" in baseOverlayItem).toBe(false);
      expect("pendingText" in stateOverlayItem).toBe(false);
      expect("reasoningSummaries" in stateOverlayItem).toBe(false);
      // New wrappers neither establish nor refute sharing: the state's overlay
      // wrapper and item are fresh objects, distinct from the base model's.
      expect(stateOverlay).not.toBe(baseOverlay);
      expect(stateOverlayItem).not.toBe(baseOverlayItem);
      // (2b) Metadata versus payload: every non-null object-valued field leaf in
      // the held state is the SAME object as the old base model's field
      // (construction `fields[key] = model[key]`), so those metadata/empty-array
      // leaves are shared by real reference identity, not materialized copies.
      const sharedObjectFieldKeys: string[] = [];
      for (const key of Object.keys(stateFields)) {
        const leaf = stateFields[key];
        if (leaf !== null && typeof leaf === "object") {
          expect(leaf, `field ${key} must be the base model's own object`).toBe(
            (oldBaseModel as unknown as Record<string, unknown>)[key],
          );
          sharedObjectFieldKeys.push(key);
        }
      }
      expect(sharedObjectFieldKeys).toContain("status");
      // The history carries no items at this boundary, so encodeCachedItem
      // materializes no payload item here either.
      expect(liveHeldRecord.state.history.turns).toHaveLength(0);
      expect(oldBaseModel.history?.turns ?? []).toHaveLength(0);

      // The held write record still retains the OLD base content and none of the
      // replacement: distinct content from the canonical body, not the same
      // bytes counted twice.
      const heldRecordText = JSON.stringify(liveHeldRecord.state);
      expect(heldRecordText).toContain(baseText as string);
      expect(heldRecordText).not.toContain("R".repeat(100_000));

      // (3) Pair the shape/provenance inventory with the LIVE reservations: the
      // worker's live reservation covers its WHOLE held record (state plus record
      // metadata) and is unchanged since the held write began; the ordinary class
      // separately covers the newer canonical body. No genuinely distinct
      // non-exempt body is present, so no second worker-class body needs coverage.
      const heldRecordPayload = independentJsonBytes(liveHeldRecord);
      const recordStatePayload = independentJsonBytes(liveHeldRecord.state);
      expect(liveWorkerCharge).toBe(heldRecordPayload);
      expect(liveWorkerCharge).toBeGreaterThanOrEqual(recordStatePayload);
      expect(recordStatePayload).not.toBe(canonicalBytes);
      expect(liveWorkerCharge).toBe(base.workerBytes); // unchanged since the held write began
      expect(liveWorkerCharge).toBeGreaterThan(0);
      expect(liveWorkerCharge).toBeLessThan(32 * 1024 * 1024);
      expect(liveWorkerCharge).not.toBe(canonicalBytes);
      // The ordinary resident charge covers the newer canonical body exactly
      // once, while the worker's distinct record is charged separately.
      expect((heldEntry?.residentBytes ?? 0) + (heldEntry?.handoffBytes ?? 0)).toBeGreaterThanOrEqual(canonicalBytes);
      // Current pending/in-flight are empty at THIS boundary: the accepted delta
      // folded into the canonical body and the held worker's batch was consumed.
      // This is the live post-replacement state, not the initial emptiness above.
      expect(heldEntry?.pending).toHaveLength(0);
      expect(heldEntry?.inFlight).toHaveLength(0);
      // The exposed hydration inventory retains nothing: neither a base model nor
      // a buffered notification update survives the published re-read.
      expect(pendingBase).toBeDefined();
      expect(pendingThreadHydrationBaseModelsForTests().some((candidate) => candidate.ref === ref)).toBe(false);
      expect(pendingThreadHydrationUpdatesForTests()).toHaveLength(0);

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

      // Each ordinary packet is within the 32 KiB pending budget including its
      // envelope, so the candidate is never refused for being oversized.
      const firstDelta = textDelta(pressure, "q".repeat(32_000));
      expect(independentJsonBytes(firstDelta)).toBeLessThanOrEqual(32 * 1024);
      fake.emitNotification(firstDelta);
      const first = backgroundTranscriptSnapshotForTests();
      // The exact original single-delta fixture stays below the resident limit:
      // this measured aggregate is 63,701 <= 65,536, so it does not yet reach
      // the transfer/refusal branch. The extended second delta below does.
      expect(first.refs.get(target)?.resident).toBe(true);
      expect(first.refs.get(target)?.handoffBytes ?? 0).toBe(0);
      expect(
        (first.refs.get(target)?.residentBytes ?? 0) + (first.refs.get(target)?.handoffBytes ?? 0),
      ).toBeGreaterThanOrEqual(targetBody);
      expect(first.residentBytes).toBeLessThanOrEqual(64 * 1024);
      expect(first.refs.get(pressure)?.persistence, JSON.stringify(first.refs.get(pressure))).toBe("enabled");

      const pressureChargeBefore =
        (first.refs.get(pressure)?.residentBytes ?? 0) + (first.refs.get(pressure)?.handoffBytes ?? 0);
      const pressureBodyBefore = bodyBytes(threadsStore.getState().threads.get(pressure));

      // A second ordinary delta in the SAME held-write scenario pushes the
      // aggregate past the resident limit. The candidate individually fits the
      // resident limit, but the only other resident (the busy target) is not an
      // eligible victim, so the branch takes the bounded refusal path.
      const refusedMarker = "-busy-refused";
      const refusedDelta = textDelta(pressure, refusedMarker + "q".repeat(16_000 - refusedMarker.length));
      expect(independentJsonBytes(refusedDelta)).toBeLessThanOrEqual(32 * 1024);
      expect(pressureChargeBefore + independentJsonBytes(refusedDelta)).toBeLessThanOrEqual(64 * 1024);
      fake.emitNotification(refusedDelta);
      const refused = backgroundTranscriptSnapshotForTests();
      const refusedTarget = refused.refs.get(target);
      const refusedPressure = refused.refs.get(pressure);
      // Real bounded refusal: the busy target is never transferred, the
      // overflowing pressure ref is refused into repairing, no budget is
      // exceeded, and no retained body is left undercharged.
      expect(refusedTarget?.resident, JSON.stringify([...refused.refs])).toBe(true);
      expect(refusedTarget?.handoffBytes ?? 0).toBe(0);
      expect((refusedTarget?.residentBytes ?? 0) + (refusedTarget?.handoffBytes ?? 0)).toBeGreaterThanOrEqual(
        targetBody,
      );
      expect(refusedPressure?.persistence).toBe("repairing");
      expect(refused.residentBytes).toBeLessThanOrEqual(64 * 1024);
      expect(refused.pendingBytes).toBeLessThanOrEqual(32 * 1024);
      expect(refused.pendingBytes).toBe(0);
      const refusedPressureModel = threadsStore.getState().threads.get(pressure);
      // The refused candidate reverted to its prior body/charge: the canonical
      // body is unchanged and the literal refused delta was never published.
      const refusedPressureBody = bodyBytes(refusedPressureModel);
      expect(refusedPressureBody).toBe(pressureBodyBefore);
      expect((refusedPressure?.residentBytes ?? 0) + (refusedPressure?.handoffBytes ?? 0)).toBeGreaterThanOrEqual(
        refusedPressureBody,
      );
      expect(
        JSON.stringify(refusedPressureModel ? currentTranscriptState(refusedPressureModel) : undefined),
      ).not.toContain(refusedMarker);
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

  test("keeps a transferred victim's staged payload covered while its write fails, then recovers it under a continuing owner", async () => {
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
    let targetRecoveryBody: ThreadReadResponse | undefined;
    fake.on("thread/read", ({ ref: requested, requestGeneration }) => {
      const base =
        requested === target && targetRecoveryBody
          ? targetRecoveryBody
          : (bodies[requested ?? ""] ?? currentRead(requested ?? target));
      // Echo the real issued request generation so the store does not discard
      // the recovery read as older than the held history.
      return { ...base, requestGeneration: requestGeneration ?? base.requestGeneration };
    });
    fake.on("thread/unsubscribe", () => ({}));
    connectionStore.getState().connect(fake);
    const scheduled: Array<() => void> = [];
    const restoreScheduler = installHydrationRetrySchedulerForTests((_attempt, retry) => {
      scheduled.push(retry);
      return () => {};
    });
    // A real cache fault seam: beforeCommit aborts the REAL putCurrent
    // transaction, so the victim eviction write CALLS THROUGH to the adapter and
    // fails durably with an abort, not a fabricated outcome.
    let faultArmed = false;
    const faultCache = new SessionCacheIndexedDB({
      indexedDB: new IDBFactory(),
      databaseName: `background-fail-${crypto.randomUUID()}`,
      beforeCommit: (operation, tx) => {
        if (operation === "putCurrent" && faultArmed) {
          faultArmed = false;
          tx.abort();
        }
      },
    });
    resetThreadsStoreForTests();
    setSessionCacheAdapterForTests(faultCache);
    let failNextTargetWrite = false;
    const writes: CurrentSessionRecord[] = [];
    const originalPut = faultCache.putCurrent.bind(faultCache);
    const put = vi.spyOn(faultCache, "putCurrent").mockImplementation(async (record, now) => {
      if (failNextTargetWrite && record.ref === target) {
        failNextTargetWrite = false;
        faultArmed = true;
      }
      const outcome = await originalPut(record, now);
      if (outcome.outcome === "written") writes.push(record);
      return outcome;
    });
    const targetLease = acquireBackgroundThread(target);
    const pressureLease = acquireBackgroundThread(pressure);
    try {
      await Promise.all([targetLease.ready, pressureLease.ready]);
      await settleBackgroundTranscriptsForTests();
      const targetBody = bodyBytes(threadsStore.getState().threads.get(target));
      expect(targetBody).toBeGreaterThan(26_000);

      // Baseline the target reads and committed writes BEFORE the operation, so
      // the later recovery read and checkpoint are distinguished from any
      // intervening read or write.
      const targetReadsBeforeFault = fake.calls.filter(
        (call) => call.method === "thread/read" && (call.params as { ref?: string })?.ref === target,
      ).length;
      const targetWritesBeforeFault = writes.filter((write) => write.ref === target).length;

      // Grow pressure so the target is transferred into the handoff budget; its
      // real eviction write then aborts while the target's owner stays alive.
      failNextTargetWrite = true;
      for (let step = 0; step < 3; step += 1) fake.emitNotification(textDelta(pressure, "q".repeat(12_000)));
      const transferred = backgroundTranscriptSnapshotForTests();
      expect(transferred.refs.get(target)?.residentBytes ?? 0).toBe(0);
      expect(transferred.refs.get(target)?.handoffBytes ?? 0).toBeGreaterThanOrEqual(targetBody);
      expect(transferred.pendingBytes).toBeGreaterThanOrEqual(targetBody);

      // Await the ACTUAL one-shot failure processing (the async failed return is
      // processed after persist's await) with the owner still alive.
      await vi.waitFor(
        () => {
          if (backgroundTranscriptSnapshotForTests().refs.get(target)?.persistence !== "repairing")
            throw new Error("failed write not processed yet");
        },
        { timeout: 4_000 },
      );
      const failed = backgroundTranscriptSnapshotForTests();
      const failedEntry = failed.refs.get(target);
      // Currentness/repairing and the exact body/charge at the failed boundary:
      // the target is not current, its model is still retained, and the staged
      // handoff charge covers that retained body.
      expect(failedEntry?.persistence).toBe("repairing");
      expect(failedEntry?.current).toBe(false);
      expect(failedEntry?.residentBytes ?? -1).toBe(0);
      expect(failedEntry?.handoffBytes ?? 0).toBeGreaterThanOrEqual(targetBody);
      expect(failed.pendingBytes).toBeGreaterThanOrEqual(targetBody);
      expect(threadsStore.getState().threads.has(target)).toBe(true);
      expect(bodyBytes(threadsStore.getState().threads.get(target))).toBe(targetBody);

      // The real aborted eviction write left no committed target record.
      expect(writes.filter((write) => write.ref === target).length).toBe(targetWritesBeforeFault);

      // A scheduled useful recovery read is attributed to THIS operation; the
      // pre-fault baseline distinguishes it from any intervening read.
      expect(scheduled.length).toBeGreaterThanOrEqual(1);
      targetRecoveryBody = readWithText(target, `${"t".repeat(27_250)}-recovered`);
      for (const retry of scheduled.splice(0)) retry();
      await vi.waitFor(
        () => {
          const reads = fake.calls.filter(
            (call) => call.method === "thread/read" && (call.params as { ref?: string })?.ref === target,
          ).length;
          if (reads <= targetReadsBeforeFault) throw new Error("recovery read not issued");
        },
        { timeout: 4_000 },
      );
      expect(
        fake.calls.filter((call) => call.method === "thread/read" && (call.params as { ref?: string })?.ref === target)
          .length,
      ).toBe(targetReadsBeforeFault + 1);
      await settleBackgroundTranscriptsForTests();

      // Recovery republished through the real reducer/cache, committed a
      // checkpoint carrying the recovered content, and released the staging.
      const recovered = backgroundTranscriptSnapshotForTests();
      const recoveredEntry = recovered.refs.get(target);
      expect(recoveredEntry?.persistence).toBe("enabled");
      expect(recoveredEntry?.current).toBe(true);
      expect((recoveredEntry?.residentBytes ?? 0) + (recoveredEntry?.handoffBytes ?? 0)).toBe(0);
      expect(recovered.pendingBytes).toBe(0);
      const targetWrite = writes.filter((write) => write.ref === target).at(-1);
      if (!targetWrite) throw new Error("expected a recovered checkpoint for the target");
      const onDisk = await faultCache.getCurrent([targetWrite.ownerToken, target], Date.now());
      expect(onDisk?.record?.ref).toBe(target);
      expect(JSON.stringify(onDisk?.record?.state)).toContain("-recovered");
    } finally {
      restoreScheduler();
      put.mockRestore();
      await settleBackgroundTranscriptsForTests().catch(() => undefined);
      await Promise.all([targetLease.release(), pressureLease.release()]);
      resetThreadsStoreForTests();
      faultCache.close();
      restoreOptions();
    }
  });

  test("releases a transferred victim's live staged reservation on ownership end", async () => {
    const restoreOptions = installBackgroundTranscriptOptionsForTests({
      residentBytes: 64 * 1024,
      pendingBytes: 32 * 1024,
    });
    const target = "local:cleanup-victim";
    const pressure = "local:cleanup-pressure";
    const bodies: Record<string, ThreadReadResponse> = {
      [target]: readWithText(target, "t".repeat(27_250)),
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
      expect(targetBody).toBeGreaterThan(26_000);

      // Grow pressure so the target is transferred into the handoff budget. The
      // staged reservation is live and nonzero immediately before release.
      for (let step = 0; step < 3; step += 1) fake.emitNotification(textDelta(pressure, "q".repeat(12_000)));
      const staged = backgroundTranscriptSnapshotForTests();
      const stagedEntry = staged.refs.get(target);
      expect(stagedEntry?.residentBytes ?? -1).toBe(0);
      expect(stagedEntry?.handoffBytes ?? 0).toBeGreaterThan(0);
      expect(stagedEntry?.handoffBytes ?? 0).toBeGreaterThanOrEqual(targetBody);
      expect(staged.pendingBytes).toBeGreaterThan(0);

      // Ending ownership must release that live staged reservation through real
      // cleanup.
      await targetLease.release();
      await settleBackgroundTranscriptsForTests();
      const final = backgroundTranscriptSnapshotForTests();
      expect(
        final.pendingBytes,
        JSON.stringify([...final.refs].map(([r, e]) => [r, e.residentBytes, e.handoffBytes, e.persistence])),
      ).toBe(0);
      expect((final.refs.get(target)?.residentBytes ?? 0) + (final.refs.get(target)?.handoffBytes ?? 0)).toBe(0);
    } finally {
      await settleBackgroundTranscriptsForTests().catch(() => undefined);
      await Promise.all([targetLease.release(), pressureLease.release()]);
      resetThreadsStoreForTests();
      restoreOptions();
    }
  });

  test("preserves a later victim delta exactly once through a held eviction checkpoint and unload", async () => {
    const restoreOptions = installBackgroundTranscriptOptionsForTests({
      residentBytes: 64 * 1024,
      pendingBytes: 32 * 1024,
    });
    const target = "local:transfer-target";
    const pressure = "local:transfer-pressure";
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
    let holdNext = false;
    let transaction: ReturnType<typeof holdNextWriteTransaction> | undefined;
    let heldRecord: CurrentSessionRecord | undefined;
    const writeStarted = deferred<void>();
    const put = vi.spyOn(cache, "putCurrent").mockImplementation(async (record, now) => {
      if (holdNext && !transaction && record.ref === target) {
        transaction = holdNextWriteTransaction(["records", "meta", "current", "currentMeta"]);
        heldRecord = record;
        writeStarted.resolve();
      }
      const outcome = await originalPut(record, now);
      if (outcome.outcome === "written") writes.push(record);
      return outcome;
    });
    const targetLease = acquireBackgroundThread(target);
    const pressureLease = acquireBackgroundThread(pressure);
    try {
      await Promise.all([targetLease.ready, pressureLease.ready]);
      await settleBackgroundTranscriptsForTests();
      const targetBody = bodyBytes(threadsStore.getState().threads.get(target));
      expect(targetBody).toBeGreaterThan(26_000);

      // Hold only the target's EVICTION checkpoint (the first target write after
      // the initial hydration settles), not its initial checkpoint.
      holdNext = true;
      let transferred = false;
      for (let step = 0; step < 3 && !transferred; step += 1) {
        fake.emitNotification(textDelta(pressure, "q".repeat(12_000)));
        const snapshot = backgroundTranscriptSnapshotForTests();
        transferred =
          (snapshot.refs.get(target)?.residentBytes ?? 1) === 0 && (snapshot.refs.get(target)?.handoffBytes ?? 0) > 0;
      }
      expect(transferred, "target transferred into pending under the growing stream").toBe(true);
      await writeStarted.promise;
      await transaction?.reached;
      const transfer = backgroundTranscriptSnapshotForTests();
      expect(transfer.refs.get(target)?.residentBytes ?? -1).toBe(0);
      expect(transfer.refs.get(target)?.handoffBytes ?? 0).toBeGreaterThanOrEqual(targetBody);
      expect(transfer.pendingBytes).toBeGreaterThanOrEqual(targetBody);
      // The captured eviction checkpoint predates the later delta.
      if (!heldRecord) throw new Error("expected the held eviction checkpoint record");
      const heldRecordText = JSON.stringify(heldRecord.state);
      expect(heldRecordText).not.toContain("-transfer-later");
      // The victim's model is still retained until the held checkpoint unloads
      // it, so a later delta can still fold into the surviving model.
      expect(threadsStore.getState().threads.has(target)).toBe(true);

      // A later target delta arrives BEFORE the eviction checkpoint finishes.
      const laterMarker = "-transfer-later";
      fake.emitNotification(textDelta(target, laterMarker));
      await vi.waitFor(
        () => {
          const model = threadsStore.getState().threads.get(target);
          if (!model || !JSON.stringify(currentTranscriptState(model)).includes(laterMarker))
            throw new Error("later delta not applied");
        },
        { timeout: 4_000 },
      );
      const live = backgroundTranscriptSnapshotForTests().refs.get(target);
      const liveBody = bodyBytes(threadsStore.getState().threads.get(target));
      // Retained content and charge are covered while the hold is live.
      expect((live?.residentBytes ?? 0) + (live?.handoffBytes ?? 0)).toBeGreaterThanOrEqual(liveBody);
      expect(liveBody).toBeGreaterThan(targetBody);

      // Let the held checkpoint finish. The finite checkpoint/unload/replay must
      // preserve the literal delta exactly once.
      transaction?.release();
      await settleBackgroundTranscriptsForTests();
      const final = backgroundTranscriptSnapshotForTests();
      expect(final.refs.get(target)?.resident).toBe(false);
      expect(final.refs.get(target)?.current).toBe(true);
      expect((final.refs.get(target)?.residentBytes ?? 0) + (final.refs.get(target)?.handoffBytes ?? 0)).toBe(0);
      expect(final.pendingBytes).toBe(0);
      const targetWrite = writes.filter((write) => write.ref === target).at(-1);
      if (!targetWrite) throw new Error("expected a final checkpoint for the target");
      const onDisk = await cache.getCurrent([targetWrite.ownerToken, target], Date.now());
      expect(onDisk?.record?.ref).toBe(target);
      expect(JSON.stringify(onDisk?.record?.state).split(laterMarker)).toHaveLength(2);
    } finally {
      put.mockRestore();
      transaction?.release();
      await settleBackgroundTranscriptsForTests().catch(() => undefined);
      await Promise.all([targetLease.release(), pressureLease.release()]);
      resetThreadsStoreForTests();
      restoreOptions();
    }
  });

  test("does not let an obsolete victim checkpoint certify or erase a successor after an ownership change", async () => {
    const restoreOptions = installBackgroundTranscriptOptionsForTests({
      residentBytes: 64 * 1024,
      pendingBytes: 32 * 1024,
    });
    const target = "local:transfer-owner";
    const pressure = "local:transfer-owner-pressure";
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
    let holdNext = false;
    let transaction: ReturnType<typeof holdNextWriteTransaction> | undefined;
    let heldRecord: CurrentSessionRecord | undefined;
    const writeStarted = deferred<void>();
    const put = vi.spyOn(cache, "putCurrent").mockImplementation(async (record, now) => {
      if (holdNext && !transaction && record.ref === target) {
        transaction = holdNextWriteTransaction(["records", "meta", "current", "currentMeta"]);
        heldRecord = record;
        writeStarted.resolve();
      }
      const outcome = await originalPut(record, now);
      if (outcome.outcome === "written") writes.push(record);
      return outcome;
    });
    // Observe the held (obsolete) worker's captured ownership generation.
    const observedGenerations: number[] = [];
    const restoreObserver = observeWorkerCheckpointsForTests((observation) => {
      if (observation.ref === target) observedGenerations.push(observation.record.ownershipGeneration);
    });
    const targetLease = acquireBackgroundThread(target);
    const pressureLease = acquireBackgroundThread(pressure);
    let secondLease: ReturnType<typeof acquireBackgroundThread> | undefined;
    let thirdLease: ReturnType<typeof acquireBackgroundThread> | undefined;
    try {
      await Promise.all([targetLease.ready, pressureLease.ready]);
      await settleBackgroundTranscriptsForTests();
      const targetBody = bodyBytes(threadsStore.getState().threads.get(target));
      expect(targetBody).toBeGreaterThan(26_000);

      holdNext = true;
      let transferred = false;
      for (let step = 0; step < 3 && !transferred; step += 1) {
        fake.emitNotification(textDelta(pressure, "q".repeat(12_000)));
        const snapshot = backgroundTranscriptSnapshotForTests();
        transferred =
          (snapshot.refs.get(target)?.residentBytes ?? 1) === 0 && (snapshot.refs.get(target)?.handoffBytes ?? 0) > 0;
      }
      expect(transferred).toBe(true);
      await writeStarted.promise;
      await transaction?.reached;
      expect(observedGenerations.length).toBeGreaterThanOrEqual(1);
      const obsoleteGeneration = observedGenerations.at(-1)!;
      if (!heldRecord) throw new Error("expected the held eviction checkpoint record");

      // NO later delta here: the held record's captured sequence IS the entry's
      // applied sequence, so only the ownership fence can stop this completion
      // from certifying. Change ownership (a second live background owner) while
      // the eviction checkpoint is still held.
      secondLease = acquireBackgroundThread(target);
      await secondLease.ready;
      const beforeRelease = backgroundTranscriptSnapshotForTests().refs.get(target);
      const bodyBeforeRelease = bodyBytes(threadsStore.getState().threads.get(target));
      expect(beforeRelease?.current).toBe(true);
      expect(bodyBeforeRelease).toBe(targetBody);
      expect((beforeRelease?.residentBytes ?? 0) + (beforeRelease?.handoffBytes ?? 0)).toBeGreaterThanOrEqual(
        bodyBeforeRelease,
      );

      // Let the obsolete write complete. Its record commits to disk, but it MUST
      // NOT certify: the ownership fence rejects it, with no stale sequence to
      // mask that rejection, before any successor can heal the entry.
      transaction?.release();
      await vi.waitFor(
        () => {
          if (backgroundTranscriptSnapshotForTests().refs.get(target)?.current !== false)
            throw new Error("obsolete completion not yet rejected");
        },
        { timeout: 4_000 },
      );
      const obsoleteDone = backgroundTranscriptSnapshotForTests().refs.get(target);
      const obsoleteWrite = writes.filter((write) => write.ref === target).at(-1);
      if (!obsoleteWrite) throw new Error("expected the obsolete checkpoint on disk");
      const onDiskObsolete = await cache.getCurrent([obsoleteWrite.ownerToken, target], Date.now());
      // The obsolete checkpoint landed at its captured generation; the entry is
      // not current, its model is still retained (no unload), and its charge
      // still covers the body: the fence stopped certification, not the sequence.
      expect(obsoleteWrite.ownershipGeneration).toBe(obsoleteGeneration);
      expect(onDiskObsolete?.record?.ownershipGeneration).toBe(obsoleteGeneration);
      expect(obsoleteDone?.current).toBe(false);
      expect(threadsStore.getState().threads.has(target)).toBe(true);
      expect(bodyBytes(threadsStore.getState().threads.get(target))).toBe(targetBody);
      expect((obsoleteDone?.residentBytes ?? 0) + (obsoleteDone?.handoffBytes ?? 0)).toBeGreaterThanOrEqual(targetBody);

      // A successor owner's write now heals: same literal content, newer
      // generation, released staging.
      await nextRealTask();
      thirdLease = acquireBackgroundThread(target);
      await thirdLease.ready;
      await settleBackgroundTranscriptsForTests();
      const final = backgroundTranscriptSnapshotForTests();
      expect(final.refs.get(target)?.current).toBe(true);
      expect((final.refs.get(target)?.residentBytes ?? 0) + (final.refs.get(target)?.handoffBytes ?? 0)).toBe(0);
      expect(final.pendingBytes).toBe(0);
      const successorWrite = writes.filter((write) => write.ref === target).at(-1);
      if (!successorWrite) throw new Error("expected a successor checkpoint for the target");
      expect(successorWrite.ownershipGeneration).not.toBe(obsoleteGeneration);
      const onDiskFinal = await cache.getCurrent([successorWrite.ownerToken, target], Date.now());
      expect(onDiskFinal?.record?.ownershipGeneration).not.toBe(obsoleteGeneration);
      expect(JSON.stringify(onDiskFinal?.record?.state)).toContain("t".repeat(27_250));
    } finally {
      restoreObserver();
      put.mockRestore();
      transaction?.release();
      await settleBackgroundTranscriptsForTests().catch(() => undefined);
      await Promise.all([
        targetLease.release(),
        pressureLease.release(),
        secondLease?.release(),
        thirdLease?.release(),
      ]);
      resetThreadsStoreForTests();
      restoreOptions();
    }
  });
});
