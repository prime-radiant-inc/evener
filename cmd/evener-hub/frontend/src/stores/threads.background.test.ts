import "fake-indexeddb/auto";
import {
  type AnyNotification,
  AppwireClient,
  type acquireThreadSubscription,
  currentTranscriptState,
  hydrateThread,
  type ThreadModel,
  type ThreadReadResponse,
  type ThreadTurnsListResponse,
  type Turn,
} from "@evener/appwire-client";
import { currentRead, textDelta } from "@evener/appwire-client/testing/currentTranscript";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { FakeSocket } from "@evener/appwire-client/testing/fakeSocket";
import { IDBDatabase, IDBFactory } from "fake-indexeddb";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { focusedModel } from "../shell/palette/paletteContext";
import { findInSessionMatches } from "../shell/palette/search";
import { installBackgroundTranscriptOptionsForTests } from "./backgroundTranscriptOptions";
import { connectionStore } from "./connection";
import { MutationOutboxIndexedDB } from "./mutationOutboxIndexedDB";
import { type CurrentSessionRecord, SessionCacheIndexedDB } from "./sessionCacheIndexedDB";
import { holdIndexedDBEvent, holdNextWriteTransaction, nextRealTask } from "./testing/stalledIndexedDB";
import {
  acquireBackgroundThread,
  backgroundTranscriptSnapshotForTests,
  clearCachedSessions,
  pendingThreadHydrationBaseModelsForTests,
  pendingThreadHydrationUpdatesForTests,
  resetThreadsStoreForTests,
  setCacheWriteTimersForTests,
  setMutationStorageForTests,
  setSessionCacheAdapterForTests,
  settleBackgroundTranscriptsForTests,
  threadsStore,
} from "./threads";
import { encodedPayloadBytes } from "./transcriptPayloadBudget";
import { observeWorkerCheckpointsForTests } from "./transcriptResidency";

const databaseName = `background-thread-${crypto.randomUUID()}`;
let cache: SessionCacheIndexedDB;
const activityLeases: ReturnType<typeof acquireThreadSubscription>[] = [];
const mutationStorages: MutationOutboxIndexedDB[] = [];

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  cache = new SessionCacheIndexedDB({ indexedDB: new IDBFactory(), databaseName });
  setSessionCacheAdapterForTests(cache);
});

afterEach(() => {
  for (const lease of activityLeases.splice(0)) lease.release();
  resetThreadsStoreForTests();
  for (const storage of mutationStorages.splice(0)) storage.close();
  cache.close();
});

function mutationStorage(): MutationOutboxIndexedDB {
  const storage = new MutationOutboxIndexedDB({
    indexedDB: new IDBFactory(),
    databaseName: `background-outbox-${crypto.randomUUID()}`,
    createMutationId: () => `background-mutation-${crypto.randomUUID()}`,
  });
  mutationStorages.push(storage);
  setMutationStorageForTests(storage);
  return storage;
}

async function blockedOutboxRow(storage: MutationOutboxIndexedDB, ref: string) {
  const queued = await storage.enqueueIntent({
    targetRef: ref,
    method: "turn/queue",
    payload: { ref, expectedTurnId: "", input: [{ type: "text", text: "blocked queue" }] },
    attachments: [],
    optimisticDisplay: { text: "blocked queue" },
  });
  await storage.markAttempted(queued.clientMutationId);
  await storage.markUnknown(queued.clientMutationId, "blockedUnknown", { onlyAttempted: true });
  return storage.getOutbox(queued.clientMutationId);
}

function waitForHydration(ref: string, count: number): Promise<void> {
  if ((threadsStore.getState().hydrations.get(ref) ?? 0) >= count) return Promise.resolve();
  return new Promise((resolve) => {
    const unsubscribe = threadsStore.subscribe((state) => {
      if ((state.hydrations.get(ref) ?? 0) < count) return;
      unsubscribe();
      resolve();
    });
  });
}

function connectCurrentClient(): FakeClient {
  const fake = new FakeClient("ready");
  fake.on("thread/read", ({ ref }) => currentRead(ref ?? "local:one"));
  fake.on("thread/unsubscribe", () => ({}));
  connectionStore.getState().connect(fake);
  return fake;
}

function membershipReads(fake: FakeClient): number {
  return fake.calls.filter(
    (call) => call.method === "thread/read" && (call.params as { subscribe?: boolean }).subscribe === true,
  ).length;
}

function unsubscriptions(fake: FakeClient): number {
  return fake.calls.filter((call) => call.method === "thread/unsubscribe").length;
}

function threadReadRequests(socket: FakeSocket): Array<{ id: number; ref: string }> {
  return socket.sent
    .map((frame) => JSON.parse(frame) as { id?: number; method?: string; params?: { ref?: string } })
    .filter(
      (frame): frame is { id: number; method: string; params: { ref: string } } =>
        frame.method === "thread/read" && typeof frame.id === "number" && typeof frame.params?.ref === "string",
    )
    .map(({ id, params }) => ({ id, ref: params.ref }));
}

async function connectRealClientWithSocket(): Promise<{ client: AppwireClient; socket: FakeSocket }> {
  const socket = new FakeSocket({ autoInitialize: true });
  const client = new AppwireClient({ url: "ws://test/rpc", socketFactory: () => socket });
  const ready = client.connect();
  socket.open();
  await ready;
  connectionStore.getState().connect(client);
  return { client, socket };
}

// A positioned versioned turn: its items carry explicit (entry, item) positions
// so the reducer's position-aware turn weave orders pages as genuinely older.
function pageTurn(turnId: string, entry: number, startItem: number, itemTexts: readonly string[]): Turn {
  return {
    id: turnId,
    itemsView: "full",
    status: "completed",
    version: 1,
    items: itemTexts.map((text, index) => ({
      type: "agentMessage",
      id: `${turnId}-item-${index}`,
      transcriptKey: `apptranscript-item-v2:${turnId}:0:${index}`,
      turnId,
      position: { entry, item: startItem + index },
      version: 1,
      text,
    })),
  };
}

// A genuine older-history page: one positioned versioned turn plus the cursor
// that names the next page down. `length` stays at the latest window's own
// length so the versioned merge accepts the page rather than reading it stale;
// the strictly descending (entry, item) boundary is what makes it older.
function olderPage(
  turnId: string,
  entry: number,
  startItem: number,
  itemTexts: readonly string[],
  nextCursor: string | undefined,
  length = 100,
): ThreadTurnsListResponse {
  return {
    data: [pageTurn(turnId, entry, startItem, itemTexts)],
    nextCursor,
    bootGeneration: "1",
    epoch: 1,
    snapshot: { incarnation: "inc-1", length },
    authoritative: false,
  };
}

// The recorded latest window: a positioned recorded turn in history plus the
// streaming overlay (from currentRead) and its older cursor.
function positionedLatestRead(ref: string): ThreadReadResponse {
  const base = currentRead(ref);
  base.olderCursor = "cursor_latest";
  base.snapshot = { incarnation: "inc-1", length: 100 };
  base.thread.turns = [pageTurn("turn_latest", 2, 0, ["latest-recorded-item"])];
  return base;
}

function independentJsonBytes(value: unknown): number {
  const encoded = JSON.stringify(value);
  if (encoded === undefined) return Number.POSITIVE_INFINITY;
  return new TextEncoder().encode(encoded).byteLength;
}

function occurrences(value: string, needle: string): number {
  return value.split(needle).length - 1;
}

// A payload-field signature compares only a model's non-transcript fields by
// value: a read may rebuild them. It is never evidence that two transcripts
// share a body.
function transcriptPayloadFieldsSignature(model: ThreadModel): string | undefined {
  const state = currentTranscriptState(model);
  if (!state) return undefined;
  return JSON.stringify(state.fields);
}

// Two models share one logical transcript body only when they are the same
// model, or carry the SAME turns array and overlay object (structural sharing)
// with unchanged payload fields. Equal serialized content alone is not sharing:
// independently retained equal-text bodies must not be deduplicated.
function sharesTranscriptBody(base: ThreadModel, canonical: ThreadModel): boolean {
  if (base === canonical) return true;
  if (base.history === undefined || canonical.history === undefined) return false;
  if (base.history.turns !== canonical.history.turns) return false;
  if (base.overlay !== canonical.overlay) return false;
  const baseFields = transcriptPayloadFieldsSignature(base);
  return baseFields !== undefined && baseFields === transcriptPayloadFieldsSignature(canonical);
}

describe("background thread claims", () => {
  test.each(["overflow recovery"])("%s does not issue fallback during a held final write", async (path) => {
    const restoreOptions = installBackgroundTranscriptOptionsForTests({ pendingBytes: 1 });
    const { client, socket } = await connectRealClientWithSocket();
    const ref = "local:recovery-retirement";
    const lease = acquireBackgroundThread(ref);
    let hold: ReturnType<typeof holdNextWriteTransaction> | undefined;
    let release: ReturnType<ReturnType<typeof acquireBackgroundThread>["release"]> | undefined;
    try {
      await cache.readEpoch();
      await vi.waitFor(() => expect(threadReadRequests(socket)).toHaveLength(1));
      socket.receive({ id: threadReadRequests(socket)[0]!.id, result: currentRead(ref) });
      await lease.ready;
      await vi.waitFor(() => expect(backgroundTranscriptSnapshotForTests().networkHydrations).toBe(0));

      if (path === "resync control") {
        socket.receive({ method: "evener/thread/resync", params: { ref, threadId: `thr_${ref}` } });
      } else {
        const update = textDelta(ref, "overflow forces canonical residency recovery");
        socket.receive({ method: update.method, params: update.params });
      }
      await vi.waitFor(() => expect(threadReadRequests(socket)).toHaveLength(2));
      expect(threadReadRequests(socket)[1]?.ref).toBe(ref);
      hold = holdNextWriteTransaction(["records", "meta", "current", "currentMeta"]);
      release = lease.release();
      await hold.reached;
      const recovery = threadReadRequests(socket)[1]!;
      socket.receive({
        id: recovery.id,
        error: {
          code: -32000,
          message: "stale cursor",
          data: { evenerErrorInfo: "transcriptItemCursorStale" },
        },
      });
      await vi.waitFor(() =>
        expect(
          backgroundTranscriptSnapshotForTests().networkHydrations === 0 || threadReadRequests(socket).length > 2,
        ).toBe(true),
      );
      expect(threadReadRequests(socket)).toHaveLength(2);
    } finally {
      hold?.release();
      client.close();
      await (release ?? lease.release());
      resetThreadsStoreForTests();
      restoreOptions();
    }
  });

  test("retired overflow recovery does not dispatch after the ready continuation on reacquisition", async () => {
    const restoreOptions = installBackgroundTranscriptOptionsForTests({ pendingBytes: 1 });
    const { client, socket } = await connectRealClientWithSocket();
    const ref = "local:ready-continuation-retirement";
    const firstOwner = acquireBackgroundThread(ref);
    let replacement: ReturnType<typeof acquireBackgroundThread> | undefined;
    let heldWrite: ReturnType<typeof holdNextWriteTransaction> | undefined;
    let firstRelease: ReturnType<ReturnType<typeof acquireBackgroundThread>["release"]> | undefined;
    let replacementRelease: ReturnType<ReturnType<typeof acquireBackgroundThread>["release"]> | undefined;
    try {
      await cache.readEpoch();
      await vi.waitFor(() => expect(threadReadRequests(socket)).toHaveLength(1));
      socket.receive({ id: threadReadRequests(socket)[0]!.id, result: currentRead(ref) });
      await firstOwner.ready;
      await vi.waitFor(() => expect(backgroundTranscriptSnapshotForTests().networkHydrations).toBe(0));

      const update = textDelta(ref, "overflow crosses ready continuation");
      socket.receive({ method: update.method, params: update.params });
      heldWrite = holdNextWriteTransaction(["records", "meta", "current", "currentMeta"]);
      // The callback has called requireReadyClient and suspended at its await.
      // Releasing and reacquiring in this stack retires only the old signal;
      // its continuation must not claim the replacement owner's authority.
      firstRelease = firstOwner.release();
      replacement = acquireBackgroundThread(ref);
      await heldWrite.reached;
      await vi.waitFor(() =>
        expect(
          backgroundTranscriptSnapshotForTests().networkHydrations === 0 || threadReadRequests(socket).length > 1,
        ).toBe(true),
      );
      expect(threadReadRequests(socket)).toHaveLength(1);
      replacementRelease = replacement.release();
    } finally {
      heldWrite?.release();
      client.close();
      await Promise.all([firstRelease ?? firstOwner.release(), replacementRelease ?? replacement?.release()]);
      resetThreadsStoreForTests();
      restoreOptions();
    }
  });

  test("promotes a zero-resident current checkpoint without another thread/read", async () => {
    const restoreOptions = installBackgroundTranscriptOptionsForTests({ residentBytes: 0, pendingBytes: 4096 });
    resetThreadsStoreForTests();
    setSessionCacheAdapterForTests(cache);
    const fake = connectCurrentClient();
    const background = acquireBackgroundThread("local:one");
    let releasePane: (() => void) | undefined;
    try {
      await background.ready;
      await settleBackgroundTranscriptsForTests();
      expect(threadsStore.getState().threads.has("local:one")).toBe(false);
      expect(backgroundTranscriptSnapshotForTests().diskWorkers).toBe(0);
      fake.emitNotification(textDelta("local:one", "-disk"));
      expect(backgroundTranscriptSnapshotForTests().pendingBytes).toBeGreaterThan(0);
      const reads = fake.calls.filter((call) => call.method === "thread/read").length;
      // Register release before awaiting: ensureThread claims the pane refcount
      // synchronously, so a rejection must still release that claim.
      const pane = threadsStore.getState().ensureThread("local:one");
      releasePane = () => threadsStore.getState().releaseThread("local:one");
      await pane;
      expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(reads);
      expect(
        threadsStore
          .getState()
          .threads.get("local:one")
          ?.turns.flatMap((turn) => turn.items)
          .map((item) => item.text)
          .join(""),
      ).toContain("alpha-disk");
    } finally {
      releasePane?.();
      try {
        await background.release();
      } finally {
        resetThreadsStoreForTests();
        restoreOptions();
      }
    }
  });

  test.each(["settled checkpoint", "unfinished captured work"])(
    "promotes once across a held current lookup and applies notifications on both sides, %s",
    async (phase) => {
      const overlapCapturedWork = phase === "unfinished captured work";
      const restoreOptions = installBackgroundTranscriptOptionsForTests({ residentBytes: 0, pendingBytes: 4096 });
      resetThreadsStoreForTests();
      setSessionCacheAdapterForTests(cache);
      const storage = mutationStorage();
      const targetScans = deferred<void>();
      const originalListTargetRefs = storage.listTargetRefs.bind(storage);
      let targetScanCount = 0;
      const targetScanSpy = vi.spyOn(storage, "listTargetRefs").mockImplementation(async () => {
        const refs = await originalListTargetRefs();
        targetScanCount += 1;
        if (targetScanCount >= 3) targetScans.resolve();
        return refs;
      });
      const fake = connectCurrentClient();
      fake.on("thread/read", ({ ref }) => {
        const response = currentRead(ref ?? "local:one");
        response.thread.evener = { ...response.thread.evener, instanceId: "instance-canonical" };
        response.overlay?.push({
          key: "stream:pre-cut/toolOutput",
          kind: "stream",
          streamId: "pre-cut",
          roundId: "pre-cut-round",
          turnId: "turn_1",
          item: {
            id: "pre-cut-tool",
            turnId: "turn_1",
            type: "commandExecution",
            output: "pre-cut-tool🙂",
            status: "inProgress",
          },
        });
        return response;
      });
      const background = acquireBackgroundThread("local:one");
      const completionSource = acquireBackgroundThread("local:completion-source");
      let paneHeld = false;
      let heldLookup: ReturnType<typeof holdNextWriteTransaction> | undefined;
      let heldCapturedWrite: ReturnType<typeof holdNextWriteTransaction> | undefined;
      let capturedWriteCompleted = false;
      let lookupStarted = false;
      const phases: string[] = [];
      const promotionLookupReached = deferred<void>();
      const releasePromotionLookup = deferred<void>();
      const lookupTransactionReached = deferred<void>();
      const releaseLookupTransaction = deferred<void>();
      const lookupWorkCommitted = deferred<void>();
      const sourceWriteReached = deferred<void>();
      const releaseSourceWrite = deferred<void>();
      const postCutWriteReached = deferred<void>();
      const releasePostCutWrite = deferred<void>();
      let postCutWriteCompleted = false;
      let postCutWriteText: string | undefined;
      let armPromotionLookup = false;
      let getCurrentSpy: ReturnType<typeof vi.spyOn> | undefined;
      let promotedAtPublication: ThreadModel | undefined;
      let promotion: Promise<void> | undefined;
      let firstWriteReached: ReturnType<typeof deferred<void>> | undefined;
      let releaseFirstWrite: ReturnType<typeof deferred<void>> | undefined;
      let laterWriteReached: ReturnType<typeof deferred<void>> | undefined;
      let releaseLaterWrite: ReturnType<typeof deferred<void>> | undefined;
      let putCurrentSpy: ReturnType<typeof vi.spyOn> | undefined;
      const observePromotion = threadsStore.subscribe((state) => {
        const model = state.threads.get("local:one");
        if (model?.turns.flatMap((turn) => turn.items).some((item) => item.text?.includes("-during-lookup"))) {
          promotedAtPublication = model;
        }
      });
      try {
        await targetScans.promise;
        await background.ready;
        await completionSource.ready;
        await settleBackgroundTranscriptsForTests();
        expect(threadsStore.getState().threads.has("local:one")).toBe(false);

        const pending = await blockedOutboxRow(storage, "local:one");
        expect(pending?.state).toBe("blockedUnknown");
        expect(threadsStore.getState().mutationAuthorityRefs.has("local:one")).toBe(false);
        const outboxBeforePromotion = await storage.listOutbox("local:one");
        expect(outboxBeforePromotion).toEqual([pending]);

        const readCount = fake.calls.filter((call) => call.method === "thread/read").length;
        const originalPutCurrent = cache.putCurrent.bind(cache);
        firstWriteReached = deferred<void>();
        releaseFirstWrite = deferred<void>();
        laterWriteReached = deferred<void>();
        releaseLaterWrite = deferred<void>();
        let completionUpdates = 0;
        putCurrentSpy = vi.spyOn(cache, "putCurrent").mockImplementation(async (record, now) => {
          if (record.ref === "local:one" && record.appliedSequence === 1) {
            // The transaction commits, but its completion callback remains held.
            // The real adapter and coordinator still await that operation.
            heldCapturedWrite = holdNextWriteTransaction(["records", "meta", "current", "currentMeta"]);
            const write = originalPutCurrent(record, now);
            await heldCapturedWrite.reached;
            phases.push("captured callback held");
            firstWriteReached?.resolve();
            await releaseFirstWrite?.promise;
            heldCapturedWrite.release();
            const result = await write;
            capturedWriteCompleted = true;
            phases.push("captured write returned");
            for (const sequence of [2, 3, 4, 5]) {
              if (sequence === 2) {
                fake.emitNotification({
                  method: "overlay/upserted",
                  params: {
                    threadId: "thr_local:one",
                    ref: "local:one",
                    item: {
                      key: "stream:round_tool/0:toolOutput",
                      kind: "stream",
                      turnId: "turn_1",
                      roundId: "round_tool",
                      streamId: "round_tool/0",
                      callId: "call_tool",
                      item: {
                        type: "commandExecution",
                        id: "item_tool",
                        turnId: "turn_1",
                        roundId: "round_tool",
                        callId: "call_tool",
                        status: "inProgress",
                      },
                    },
                  },
                } as AnyNotification);
              } else if (sequence === 3) {
                fake.emitNotification({
                  method: "overlay/delta",
                  params: {
                    threadId: "thr_local:one",
                    ref: "local:one",
                    key: "stream:round_tool/0:toolOutput",
                    field: "output",
                    delta: "工具🙂",
                  },
                } as AnyNotification);
              } else {
                fake.emitNotification(textDelta("local:one", `-completion-${sequence}`));
              }
              completionUpdates += 1;
            }
            phases.push("completion updates emitted");
            return result;
          }
          if (record.ref === "local:one" && record.appliedSequence === 5) {
            laterWriteReached?.resolve();
            await releaseLaterWrite?.promise;
          }
          if (overlapCapturedWork && record.ref === "local:one" && record.appliedSequence > 5) {
            phases.push("later target write held");
            postCutWriteReached.resolve();
            await releasePostCutWrite.promise;
          }
          if (overlapCapturedWork && record.ref === "local:completion-source" && record.appliedSequence === 1) {
            sourceWriteReached.resolve();
            await releaseSourceWrite.promise;
          }
          const result = await originalPutCurrent(record, now);
          if (overlapCapturedWork && record.ref === "local:one" && record.appliedSequence === 5) {
            // The ownership retry completes the captured cut. Replenish target
            // work at this actual completion too, not just during local lookup.
            fake.emitNotification(textDelta("local:one", "-post-cut"));
            fake.emitNotification({
              method: "overlay/delta",
              params: {
                threadId: "thr_local:one",
                ref: "local:one",
                key: "stream:round_tool/0:toolOutput",
                field: "output",
                delta: ":post-cut",
              },
            } as AnyNotification);
            phases.push("cut retry returned and replenished target");
          }
          if (overlapCapturedWork && record.ref === "local:one" && record.appliedSequence > 5) {
            expect(result.outcome).toBe("written");
            postCutWriteCompleted = true;
            postCutWriteText = JSON.stringify(record.state);
          }
          if (record.ref === "local:completion-source" && record.appliedSequence === 1) {
            fake.emitNotification(textDelta("local:one", "-completion-in-lookup"));
            lookupWorkCommitted.resolve();
          }
          return result;
        });
        const originalGetCurrent = cache.getCurrent.bind(cache);
        let targetLookupCount = 0;
        getCurrentSpy = vi.spyOn(cache, "getCurrent").mockImplementation(async (...args) => {
          if (args[0][1] === "local:one") targetLookupCount += 1;
          if (armPromotionLookup && args[0][1] === "local:one") {
            armPromotionLookup = false;
            lookupStarted = true;
            phases.push("promotion lookup reached");
            promotionLookupReached.resolve();
            await releasePromotionLookup.promise;
            heldLookup = holdNextWriteTransaction(["records", "meta", "current", "currentMeta"]);
            const lookup = originalGetCurrent(...args);
            await heldLookup.reached;
            lookupTransactionReached.resolve();
            await releaseLookupTransaction.promise;
            return lookup;
          }
          return originalGetCurrent(...args);
        });
        fake.emitNotification(textDelta("local:one", "-captured"));
        await firstWriteReached.promise;
        if (overlapCapturedWork) {
          expect(capturedWriteCompleted, "ensure must start before the captured completion").toBe(false);
          expect(backgroundTranscriptSnapshotForTests().diskWorkers).toBe(1);
          expect(threadsStore.getState().threads.has("local:one")).toBe(false);
          const lookupsBeforeEnsure = targetLookupCount;
          phases.push("ensure started with captured work unfinished");
          promotion = threadsStore.getState().ensureThread("local:one");
          paneHeld = true;
          // ensureThread synchronously acquires the pane and enters promote's
          // cut wait. Complete an independent durable outbox read while the
          // target completion remains held, not a sleep or microtask flush.
          expect(await storage.listOutbox("local:one")).toEqual(outboxBeforePromotion);
          expect(targetLookupCount).toBe(lookupsBeforeEnsure);
          expect(capturedWriteCompleted).toBe(false);
          expect(lookupStarted).toBe(false);
          expect(promotedAtPublication).toBeUndefined();
          expect(threadsStore.getState().threads.has("local:one")).toBe(false);
          expect(threadsStore.getState().mutationAuthorityRefs.has("local:one")).toBe(false);
          expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(readCount);
          expect(
            fake.calls.filter((call) =>
              ["thread/resume", "turn/start", "turn/steer", "turn/queue", "turn/interrupt"].includes(call.method),
            ),
          ).toEqual([]);
        }
        releaseFirstWrite.resolve();
        await laterWriteReached.promise;
        // Foreground acquisition can require the already-issued write's real
        // ownership retry. Do not mistake that worker load for promotion lookup.
        if (overlapCapturedWork) {
          // Queue this ref before the retry completes. It occupies the single
          // worker while promotion starts its target lookup, without preemption
          // or an internal lookup-call ordinal deciding which read to gate.
          fake.emitNotification(textDelta("local:completion-source", "-source-write"));
        }
        armPromotionLookup = overlapCapturedWork;
        releaseLaterWrite.resolve();
        if (!overlapCapturedWork) {
          await settleBackgroundTranscriptsForTests();
          armPromotionLookup = true;
          promotion = threadsStore.getState().ensureThread("local:one");
          paneHeld = true;
        }
        await promotionLookupReached.promise;
        if (overlapCapturedWork) {
          expect(phases).toEqual([
            "captured callback held",
            "ensure started with captured work unfinished",
            "captured write returned",
            "completion updates emitted",
            "cut retry returned and replenished target",
            "promotion lookup reached",
          ]);
          await sourceWriteReached.promise;
          releaseSourceWrite.resolve();
        } else {
          fake.emitNotification(textDelta("local:completion-source", "-source-write"));
        }
        await lookupWorkCommitted.promise;
        if (overlapCapturedWork) await postCutWriteReached.promise;
        else await settleBackgroundTranscriptsForTests();
        releasePromotionLookup.resolve();
        await lookupTransactionReached.promise;
        const toolKey = "stream:round_tool/0:toolOutput";
        fake.emitNotification({
          method: "overlay/delta",
          params: {
            threadId: "thr_local:one",
            ref: "local:one",
            key: toolKey,
            field: "output",
            delta: ":during-lookup",
          },
        } as AnyNotification);
        fake.emitNotification(textDelta("local:one", "-during-lookup"));
        expect(backgroundTranscriptSnapshotForTests().pendingBytes).toBeGreaterThan(0);
        if (!heldLookup) throw new Error("promotion lookup transaction was not held");
        heldLookup.release();
        releaseLookupTransaction.resolve();
        await promotion;
        paneHeld = true;
        if (overlapCapturedWork) {
          phases.push("published with later target write unfinished");
          expect(postCutWriteCompleted).toBe(false);
          expect(backgroundTranscriptSnapshotForTests().diskWorkers).toBe(1);
          expect(phases.slice(-2)).toEqual(["later target write held", "published with later target write unfinished"]);
        }

        const adopted = threadsStore.getState().threads.get("local:one");
        expect(adopted).toBeDefined();
        expect(adopted).toBe(promotedAtPublication);
        expect(adopted?.capabilities.send).toBe(true);
        expect(adopted?.capabilities.queue).toBe(true);
        expect(adopted?.capabilitySource).toBe("read");
        expect(adopted?.instanceId).toBe("instance-canonical");
        expect(adopted?.turns.flatMap((turn) => turn.items).find((item) => item.id === "pre-cut-tool")?.output).toBe(
          "pre-cut-tool🙂",
        );
        expect(threadsStore.getState().mutationAuthorityRefs.has("local:one")).toBe(false);
        fake.emitNotification({
          method: "overlay/delta",
          params: {
            threadId: "thr_local:one",
            ref: "local:one",
            key: toolKey,
            field: "output",
            delta: ":after-publication",
          },
        } as AnyNotification);
        fake.emitNotification(textDelta("local:one", "-after-publication"));
        const text =
          threadsStore
            .getState()
            .threads.get("local:one")
            ?.turns.flatMap((turn) => turn.items)
            .map((item) => item.text)
            .join("") ?? "";
        expect(text).toContain(
          `alpha-captured-completion-4-completion-5${overlapCapturedWork ? "-post-cut" : ""}-completion-in-lookup-during-lookup-after-publication`,
        );
        expect(text.split("-captured"), "pre-cut update must be applied exactly once").toHaveLength(2);
        let previousIndex = text.indexOf("-captured");
        for (const marker of [
          "-completion-4",
          "-completion-5",
          ...(overlapCapturedWork ? ["-post-cut"] : []),
          "-completion-in-lookup",
          "-during-lookup",
          "-after-publication",
        ]) {
          expect(text.split(marker), marker).toHaveLength(2);
          const index = text.indexOf(marker);
          expect(index).toBeGreaterThan(previousIndex);
          previousIndex = index;
        }
        expect(completionUpdates).toBe(4);
        const toolItems = adopted?.turns.flatMap((turn) => turn.items).filter((item) => item.id === "item_tool") ?? [];
        expect(toolItems).toHaveLength(1);
        const completionToolOutput = `工具🙂${overlapCapturedWork ? ":post-cut" : ""}:during-lookup`;
        expect(toolItems[0]?.output).toBe(completionToolOutput);
        const afterNotifications = threadsStore.getState().threads.get("local:one");
        expect(
          afterNotifications?.turns.flatMap((turn) => turn.items).find((item) => item.id === "item_tool")?.output,
        ).toBe(`${completionToolOutput}:after-publication`);
        expect(afterNotifications?.overlay?.[toolKey]?.item.output).toBe(`${completionToolOutput}:after-publication`);
        expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(readCount);
        expect(await storage.listOutbox("local:one")).toEqual(outboxBeforePromotion);
        expect(
          fake.calls.filter((call) =>
            ["thread/resume", "turn/start", "turn/steer", "turn/queue", "turn/interrupt"].includes(call.method),
          ),
        ).toEqual([]);
        releasePostCutWrite.resolve();
        await settleBackgroundTranscriptsForTests();
        if (overlapCapturedWork) {
          expect(postCutWriteCompleted).toBe(true);
          expect(postCutWriteText).toContain("alpha-captured-completion-4-completion-5-post-cut-completion-in-lookup");
          expect(postCutWriteText?.split("-captured")).toHaveLength(2);
          expect(backgroundTranscriptSnapshotForTests().diskWorkers).toBe(0);
        }
        fake.emitNotification(textDelta("local:one", "-after-drain"));
        fake.emitNotification({
          method: "overlay/delta",
          params: { threadId: "thr_local:one", ref: "local:one", key: toolKey, field: "output", delta: ":after-drain" },
        } as AnyNotification);
        const afterDrain = threadsStore.getState().threads.get("local:one");
        expect(afterDrain?.turns.flatMap((turn) => turn.items).find((item) => item.id === "a1")?.text).toBe(
          `${text}-after-drain`,
        );
        expect(afterDrain?.turns.flatMap((turn) => turn.items).find((item) => item.id === "pre-cut-tool")?.output).toBe(
          "pre-cut-tool🙂",
        );
        expect(afterDrain?.turns.flatMap((turn) => turn.items).find((item) => item.id === "item_tool")?.output).toBe(
          `${completionToolOutput}:after-publication:after-drain`,
        );
        expect(afterDrain?.overlay?.[toolKey]?.item.output).toBe(
          `${completionToolOutput}:after-publication:after-drain`,
        );
        expect(await storage.listOutbox("local:one")).toEqual(outboxBeforePromotion);
        expect(threadsStore.getState().mutationAuthorityRefs.has("local:one")).toBe(false);
        expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(readCount);
        expect(
          fake.calls.filter((call) =>
            ["thread/resume", "turn/start", "turn/steer", "turn/queue", "turn/interrupt"].includes(call.method),
          ),
        ).toEqual([]);
      } finally {
        heldCapturedWrite?.release();
        heldLookup?.release();
        releaseLookupTransaction.resolve();
        releasePromotionLookup.resolve();
        releaseFirstWrite?.resolve();
        releaseLaterWrite?.resolve();
        releaseSourceWrite.resolve();
        releasePostCutWrite.resolve();
        getCurrentSpy?.mockRestore();
        putCurrentSpy?.mockRestore();
        targetScanSpy.mockRestore();
        await promotion?.catch(() => undefined);
        observePromotion();
        if (paneHeld) threadsStore.getState().releaseThread("local:one");
        await completionSource.release();
        await background.release();
        resetThreadsStoreForTests();
        restoreOptions();
      }
    },
  );

  test("accounts two completed hydrations and deltas while the real checkpoint transaction is held", async () => {
    const restoreOptions = installBackgroundTranscriptOptionsForTests({
      residentBytes: 1024,
      pendingBytes: 4 * 1024 * 1024,
    });
    resetThreadsStoreForTests();
    setSessionCacheAdapterForTests(cache);
    const one = deferred<ReturnType<typeof currentRead>>();
    const two = deferred<ReturnType<typeof currentRead>>();
    const writerResync = deferred<ReturnType<typeof currentRead>>();
    let writerReads = 0;
    const fake = new FakeClient("ready");
    fake.on("thread/read", ({ ref }) => {
      if (ref === "local:one") return one.promise;
      if (ref === "local:two") return two.promise;
      if (ref === "local:writer") {
        writerReads += 1;
        return writerReads === 1 ? currentRead("local:writer") : writerResync.promise;
      }
      return currentRead("local:writer");
    });
    fake.on("thread/unsubscribe", () => ({}));
    connectionStore.getState().connect(fake);
    const writer = acquireBackgroundThread("local:writer");
    const first = acquireBackgroundThread("local:one");
    const second = acquireBackgroundThread("local:two");
    let heldWrite: CurrentSessionRecord | undefined;
    const putCurrentStarted = deferred<void>();
    let transaction: ReturnType<typeof holdNextWriteTransaction> | undefined;
    let secondHoldArmed = false;
    let secondTransaction: ReturnType<typeof holdNextWriteTransaction> | undefined;
    const secondWriteStarted = deferred<void>();
    const writtenRecords: CurrentSessionRecord[] = [];
    const workerCheckpoints: Array<{ ref: string; batchLength: number }> = [];
    let foregroundPromotion = false;
    const publicationAccounting: Array<{ retainedBytes: number; chargedBytes: number }> = [];
    const coverageFailures: unknown[] = [];
    // Independent simultaneous inventory of every retained class, with shared
    // identity de-duplication and a paired live reservation per class:
    // - canonical store models -> that ref's resident+handoff charge
    // - coordinator pending/inFlight frames -> pending budget
    // - hydration notification buffers -> pending budget
    // - hydration base models -> the canonical body they share, never a global
    //   spare charge
    // The worker checkpoint body is covered separately at the call-through
    // putCurrent boundary, where the folded model and record are both visible.
    // Incoming decoding, JSON serialization temporaries and IndexedDB copies are
    // excluded; a base model shared with the store model is counted once by
    // identity, never as a second body.
    const accountRetainedInventory = (state: { threads: Map<string, ThreadModel> }) => {
      const snapshot = backgroundTranscriptSnapshotForTests();
      const seen = new Set<object>();
      let retainedBytes = 0;
      const claim = (identity: object, bytes: number, reservation: number, label: string) => {
        if (seen.has(identity)) return;
        seen.add(identity);
        retainedBytes += bytes;
        expect(reservation, `${label} covered by its live reservation`).toBeGreaterThanOrEqual(bytes);
      };
      for (const [ref, model] of state.threads) {
        const ownership = snapshot.refs.get(ref);
        claim(
          model,
          independentJsonBytes(currentTranscriptState(model)),
          (ownership?.residentBytes ?? 0) + (ownership?.handoffBytes ?? 0),
          `canonical model ${ref}`,
        );
      }
      let queuedBytes = 0;
      for (const entry of snapshot.refs.values()) {
        for (const update of [...entry.pending, ...entry.inFlight]) {
          const bytes = independentJsonBytes(update.notification);
          expect(bytes, `coordinator frame ${update.sequence}`).toBe(update.bytes);
          queuedBytes += bytes;
          claim(update.notification, bytes, snapshot.pendingBytes, `coordinator frame ${update.sequence}`);
        }
      }
      let hydrationBytes = 0;
      for (const update of pendingThreadHydrationUpdatesForTests()) {
        const bytes = independentJsonBytes(update.notification);
        expect(bytes).toBe(update.bytes);
        hydrationBytes += bytes;
        claim(update.notification, bytes, snapshot.pendingBytes, `hydration frame ${update.sequence}`);
      }
      const handoffTotal = [...snapshot.refs.values()].reduce((sum, entry) => sum + entry.handoffBytes, 0);
      // A hydration base is the canonical model or shares its body; it is never
      // a distinct uncharged transcript covered by unrelated spare charges.
      for (const base of pendingThreadHydrationBaseModelsForTests()) {
        const canonical = state.threads.get(base.ref);
        expect(
          canonical !== undefined && sharesTranscriptBody(base, canonical),
          `hydration base ${base.ref} shares its canonical body`,
        ).toBe(true);
      }
      // Bucket sum: the pending budget is exactly the handoff charges plus the
      // actual queued frames plus the hydration notification bytes.
      expect(snapshot.pendingBytes, JSON.stringify({ handoffTotal, queuedBytes, hydrationBytes })).toBe(
        handoffTotal + queuedBytes + hydrationBytes,
      );
      return { retainedBytes, chargedBytes: snapshot.residentBytes + snapshot.pendingBytes + snapshot.workerBytes };
    };
    // The subscriber's own failures are swallowed by the notification emitter,
    // so record them here and assert them empty at each visible check.
    const assertCoverage = (label: string) => {
      const measured = accountRetainedInventory(threadsStore.getState());
      expect(measured.chargedBytes, label).toBeGreaterThanOrEqual(measured.retainedBytes);
      expect(coverageFailures, `${label}: subscriber coverage`).toEqual([]);
      expect(workerObserverFailures, `${label}: worker observer coverage`).toEqual([]);
    };
    const observeAccounting = threadsStore.subscribe((state) => {
      if (foregroundPromotion) return;
      try {
        publicationAccounting.push(accountRetainedInventory(state));
      } catch (error) {
        coverageFailures.push(error);
      }
    });
    // The worker-boundary observer sees the folded worker model and the exact
    // record it is about to write, which the store and cache ports cannot. Its
    // checks must not throw: the callback runs inside persist, whose rejection
    // runWorker converts into a storage revoke, so a thrown failure would be
    // swallowed. Record failures out of band and assert them at the test
    // boundary, exactly as the subscriber does, and always record the
    // checkpoint so batch-shape checks still run.
    const workerObserverFailures: unknown[] = [];
    const restoreWorkerObserver = observeWorkerCheckpointsForTests((observation) => {
      const snapshot = backgroundTranscriptSnapshotForTests();
      try {
        const modelState = currentTranscriptState(observation.model);
        expect(modelState, `worker model ${observation.ref} has a recorded state`).toBeDefined();
        const stateBytes = independentJsonBytes(modelState);
        const recordBytes = independentJsonBytes(observation.record);
        expect(stateBytes).toBe(encodedPayloadBytes(modelState));
        expect(independentJsonBytes(observation.record.state)).toBe(stateBytes);
        expect(recordBytes).toBe(encodedPayloadBytes(observation.record));
        expect(recordBytes).toBeGreaterThanOrEqual(stateBytes);
        // The larger model-state/record encoding is charged exactly once.
        expect(snapshot.workerBytes).toBe(recordBytes);
        const ownership = snapshot.refs.get(observation.ref);
        if (observation.resident && observation.resident !== observation.model) {
          const residentBytes = independentJsonBytes(currentTranscriptState(observation.resident));
          expect(ownership?.residentBytes ?? 0, `worker starting body ${observation.ref}`).toBeGreaterThanOrEqual(
            residentBytes,
          );
        }
      } catch (error) {
        workerObserverFailures.push(error);
      }
      workerCheckpoints.push({ ref: observation.ref, batchLength: observation.batch.length });
    });
    const originalPutCurrent = cache.putCurrent.bind(cache);
    const putCurrent = vi.spyOn(cache, "putCurrent").mockImplementation(async (record, now) => {
      if (!transaction) {
        heldWrite = record;
        transaction = holdNextWriteTransaction(["records", "meta", "current", "currentMeta"]);
        putCurrentStarted.resolve();
      } else if (secondHoldArmed && !secondTransaction && record.ref === "local:one") {
        secondHoldArmed = false;
        secondTransaction = holdNextWriteTransaction(["records", "meta", "current", "currentMeta"]);
        secondWriteStarted.resolve();
      }
      const outcome = await originalPutCurrent(record, now);
      if (outcome.outcome === "written") writtenRecords.push(record);
      return outcome;
    });
    const payload = (ref: string) => {
      const response = currentRead(ref);
      response.overlay![0]!.item.text = "x".repeat(1024 * 1024);
      return response;
    };
    try {
      await vi.waitFor(() => expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(3));
      await putCurrentStarted.promise;
      await transaction?.reached;
      expect(threadsStore.getState().threads.has("local:writer")).toBe(true);
      assertCoverage("writer hydration published with its write held");
      fake.emitNotification(textDelta("local:one", "buffered-one"));
      fake.emitNotification(textDelta("local:two", "buffered-two"));
      const bufferedUpdates = pendingThreadHydrationUpdatesForTests();
      const independentlyMeasuredBufferedBytes = bufferedUpdates.reduce((sum, update) => {
        const bytes = independentJsonBytes(update.notification);
        expect(bytes).toBe(update.bytes);
        return sum + bytes;
      }, 0);
      expect(bufferedUpdates).toHaveLength(2);
      expect(backgroundTranscriptSnapshotForTests().pendingBytes).toBeGreaterThanOrEqual(
        independentlyMeasuredBufferedBytes,
      );
      one.resolve(payload("local:one"));
      two.resolve(payload("local:two"));
      await Promise.all([waitForHydration("local:one", 1), waitForHydration("local:two", 1)]);
      expect(threadsStore.getState().threads.has("local:one")).toBe(true);
      expect(threadsStore.getState().threads.has("local:two")).toBe(true);
      assertCoverage("both hydrations published while the write is held");

      // Phase 1 (still held): one deferred writer resync through the real
      // notification seam exposes a nonempty hydration base and buffer.
      fake.emitNotification({
        method: "evener/thread/resync",
        params: { ref: "local:writer", threadId: "thr_local:writer" },
      } as AnyNotification);
      await vi.waitFor(() => expect(writerReads).toBeGreaterThanOrEqual(2));
      await vi.waitFor(() => expect(pendingThreadHydrationBaseModelsForTests()).toHaveLength(1));
      const resyncBase = pendingThreadHydrationBaseModelsForTests()[0]!;
      expect(resyncBase.ref).toBe("local:writer");
      const resyncCanonical = threadsStore.getState().threads.get("local:writer");
      expect(resyncCanonical).toBe(resyncBase);
      expect(sharesTranscriptBody(resyncBase, resyncCanonical!)).toBe(true);
      expect(resyncBase.history?.turns).toBe(resyncCanonical!.history?.turns);
      expect(resyncBase.overlay).toBe(resyncCanonical!.overlay);
      const baseOwnership = backgroundTranscriptSnapshotForTests().refs.get("local:writer");
      expect(
        (baseOwnership?.residentBytes ?? 0) + (baseOwnership?.handoffBytes ?? 0),
        "resync base covered by its canonical reservation",
      ).toBeGreaterThanOrEqual(independentJsonBytes(currentTranscriptState(resyncBase)));
      fake.emitNotification(textDelta("local:writer", "-resync-buffered"));
      await vi.waitFor(() => expect(pendingThreadHydrationUpdatesForTests()).toHaveLength(1));
      assertCoverage("nonempty resync base and buffer while the write is held");
      const resyncResponse = currentRead("local:writer");
      resyncResponse.requestGeneration = resyncBase.history?.issuedGeneration ?? 1;
      writerResync.resolve(resyncResponse);
      await vi.waitFor(() => expect(pendingThreadHydrationBaseModelsForTests()).toHaveLength(0));
      await vi.waitFor(() => expect(pendingThreadHydrationUpdatesForTests()).toHaveLength(0));

      const firstFrame = textDelta("local:one", "🙂".repeat(1024));
      fake.emitNotification(firstFrame);
      const firstWrite = heldWrite;
      expect(firstWrite).toBeDefined();
      const heldSnapshot = backgroundTranscriptSnapshotForTests();
      const firstBodyBytes = independentJsonBytes(firstWrite?.state);
      const firstModel = threadsStore.getState().threads.get("local:one");
      const secondModel = threadsStore.getState().threads.get("local:two");
      expect(firstModel).toBeDefined();
      expect(secondModel).toBeDefined();
      const firstStateBytes = independentJsonBytes(currentTranscriptState(firstModel!));
      const secondStateBytes = independentJsonBytes(currentTranscriptState(secondModel!));
      expect(firstBodyBytes).toBeGreaterThan(0);
      const remainingResidentRoom = 1024 - heldSnapshot.residentBytes;
      expect(firstStateBytes).toBeGreaterThan(remainingResidentRoom);
      expect(secondStateBytes).toBeGreaterThan(remainingResidentRoom);
      expect(secondStateBytes).toBeGreaterThan(1024);
      expect(firstStateBytes).toBeLessThan(32 * 1024 * 1024);
      expect(secondStateBytes).toBeLessThan(32 * 1024 * 1024);
      // The held checkpoint write is the writer's; its captured batch was empty.
      expect(
        workerCheckpoints.some((checkpoint) => checkpoint.ref === "local:writer" && checkpoint.batchLength === 0),
      ).toBe(true);
      expect(heldSnapshot.workerBytes).toBeGreaterThanOrEqual(firstBodyBytes);
      expect(independentJsonBytes(firstWrite)).toBeGreaterThan(firstBodyBytes);
      assertCoverage("first emoji frame accepted");

      for (let i = 0; i < 15; i += 1) {
        const frame = textDelta("local:one", "🙂".repeat(1024));
        fake.emitNotification(frame);
        assertCoverage(`emoji frame ${i}`);
        const snapshot = backgroundTranscriptSnapshotForTests();
        expect(snapshot.residentBytes).toBeLessThanOrEqual(1024);
        expect(snapshot.pendingBytes).toBeLessThanOrEqual(4 * 1024 * 1024);
        expect(snapshot.diskWorkers).toBeLessThanOrEqual(1);
        expect(snapshot.networkHydrations).toBeLessThanOrEqual(2);
        expect(snapshot.workerBytes).toBeLessThanOrEqual(32 * 1024 * 1024);
      }
      const toolKey = "stream:accounting/0:toolOutput";
      const toolUpsert = {
        method: "overlay/upserted",
        params: {
          threadId: "thr_local:one",
          ref: "local:one",
          item: {
            key: toolKey,
            kind: "stream",
            turnId: "turn_1",
            roundId: "accounting",
            streamId: "accounting/0",
            callId: "call_accounting",
            item: {
              type: "commandExecution",
              id: "item_accounting_tool",
              turnId: "turn_1",
              roundId: "accounting",
              callId: "call_accounting",
              status: "inProgress",
            },
          },
        },
      } as AnyNotification;
      const toolOutput = {
        method: "overlay/delta",
        params: {
          threadId: "thr_local:one",
          ref: "local:one",
          key: toolKey,
          field: "output",
          delta: JSON.stringify({ structured: { output: ["工具", 7, { retained: true }] } }),
        },
      } as AnyNotification;
      fake.emitNotification(toolUpsert);
      fake.emitNotification(toolOutput);
      expect(threadsStore.getState().threads.get("local:one")?.overlay?.[toolKey]?.item.output).toContain(
        '"retained":true',
      );
      assertCoverage("structured tool notification accepted");
      expect(publicationAccounting.length).toBeGreaterThan(0);
      expect(publicationAccounting.every(({ retainedBytes, chargedBytes }) => chargedBytes >= retainedBytes)).toBe(
        true,
      );
      expect(encodedPayloadBytes({ text: "🙂", raw: { output: ["工具", 7] } })).toBe(
        new TextEncoder().encode(JSON.stringify({ text: "🙂", raw: { output: ["工具", 7] } })).byteLength,
      );
      expect(backgroundTranscriptSnapshotForTests().workerBytes).toBeGreaterThan(0);
      expect(backgroundTranscriptSnapshotForTests().pendingBytes).toBeLessThanOrEqual(4 * 1024 * 1024);

      transaction?.release();
      await vi.waitFor(() => expect(backgroundTranscriptSnapshotForTests().diskWorkers).toBe(0), { timeout: 1000 });
      await settleBackgroundTranscriptsForTests();
      const completed = backgroundTranscriptSnapshotForTests();
      expect(completed.diskWorkers).toBe(0);
      expect(completed.refs.get("local:one")?.current, JSON.stringify(completed.refs.get("local:one"))).toBe(true);
      expect(completed.refs.get("local:two")?.current).toBe(true);
      expect(pendingThreadHydrationUpdatesForTests()).toEqual([]);
      expect(pendingThreadHydrationBaseModelsForTests()).toEqual([]);
      expect([...completed.refs.values()].every((entry) => entry.inFlight.length === 0)).toBe(true);
      expect(completed.workerBytes).toBe(0);
      assertCoverage("initial draining quiescent");
      // Initial draining has made local:one disk-only.
      expect(threadsStore.getState().threads.has("local:one")).toBe(false);
      expect(completed.refs.get("local:one")?.resident).toBe(false);
      expect(completed.refs.get("local:one")?.residentBytes).toBe(0);
      expect(completed.refs.get("local:one")?.handoffBytes).toBe(0);

      // Phase 2: hold local:one's next real write, capture one ordinary batch,
      // then let a second delta queue behind it as a pending tail.
      secondHoldArmed = true;
      const capturedDelta = textDelta("local:one", "-captured-batch");
      fake.emitNotification(capturedDelta);
      assertCoverage("nonresident delta reserved");
      await secondWriteStarted.promise;
      await secondTransaction?.reached;
      const heldBatch = backgroundTranscriptSnapshotForTests().refs.get("local:one");
      expect(heldBatch?.inFlight).toHaveLength(1);
      expect(heldBatch?.inFlight[0]?.notification).toBe(capturedDelta);
      expect(heldBatch?.pending).toHaveLength(0);
      const tailDelta = textDelta("local:one", "-pending-tail");
      fake.emitNotification(tailDelta);
      assertCoverage("captured batch plus pending tail");
      const withTail = backgroundTranscriptSnapshotForTests().refs.get("local:one");
      expect(withTail?.inFlight).toHaveLength(1);
      expect(withTail?.inFlight[0]?.notification).toBe(capturedDelta);
      expect(withTail?.pending).toHaveLength(1);
      expect(withTail?.pending[0]?.notification).toBe(tailDelta);
      secondTransaction?.release();
      await vi.waitFor(() => expect(backgroundTranscriptSnapshotForTests().diskWorkers).toBe(0), { timeout: 1000 });
      await settleBackgroundTranscriptsForTests();
      assertCoverage("captured batch and pending tail committed");

      // Both chunks reach the real disk content exactly once.
      const localOneWrite = writtenRecords.filter((record) => record.ref === "local:one").at(-1);
      if (!localOneWrite) throw new Error("expected a real local:one checkpoint");
      const onDisk = await cache.getCurrent([localOneWrite.ownerToken, "local:one"], Date.now());
      const onDiskText = JSON.stringify(onDisk?.record?.state);
      expect(occurrences(onDiskText, "-captured-batch")).toBe(1);
      expect(occurrences(onDiskText, "-pending-tail")).toBe(1);

      // Local reopening serves that same disk content with no further read.
      const readsBeforePromotion = fake.calls.filter((call) => call.method === "thread/read").length;
      foregroundPromotion = true;
      await threadsStore.getState().ensureThread("local:one");
      const promoted = threadsStore.getState().threads.get("local:one");
      const text =
        promoted?.turns
          .flatMap((turn) => turn.items)
          .map((item) => item.text)
          .join("") ?? "";
      expect(text.match(/🙂/gu)).toHaveLength(1024 * 16);
      const reopenedText = JSON.stringify(promoted ? currentTranscriptState(promoted) : undefined);
      expect(occurrences(reopenedText, "-captured-batch")).toBe(1);
      expect(occurrences(reopenedText, "-pending-tail")).toBe(1);
      await threadsStore.getState().ensureThread("local:two");
      expect(
        threadsStore.getState().threads.get("local:two")?.overlay?.["stream:s1:agentMessage"]?.item.text,
      ).toContain("x".repeat(1024));
      expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(readsBeforePromotion);
      expect(coverageFailures, "subscriber coverage").toEqual([]);
      expect(workerObserverFailures, "worker observer coverage").toEqual([]);
    } finally {
      one.resolve(currentRead("local:one"));
      two.resolve(currentRead("local:two"));
      writerResync.resolve(currentRead("local:writer"));
      transaction?.release();
      secondTransaction?.release();
      putCurrent.mockRestore();
      restoreWorkerObserver();
      observeAccounting();
      threadsStore.getState().releaseThread("local:one");
      threadsStore.getState().releaseThread("local:two");
      await Promise.all([writer.release(), first.release(), second.release()]);
      await settleBackgroundTranscriptsForTests();
      resetThreadsStoreForTests();
      restoreOptions();
    }
  });

  test("refuses an oversized resident notification at the real publication gate while the write is held, then recovers", async () => {
    const ref = "local:refuse";
    const base = hydrateThread(currentRead(ref), ref, 1);
    const baseBytes = encodedPayloadBytes(currentTranscriptState(base));
    const marker = "-oversized-candidate";
    const oversizedFrame = textDelta(ref, marker.repeat(6000));
    const frameBytes = encodedPayloadBytes(oversizedFrame);
    const restoreOptions = installBackgroundTranscriptOptionsForTests({
      // The real hydrated store body is a few bytes larger than the fixture
      // model measured above, so allow that slack while still refusing the
      // 120 KiB candidate.
      residentBytes: baseBytes + 64,
      pendingBytes: frameBytes,
    });
    resetThreadsStoreForTests();
    setSessionCacheAdapterForTests(cache);
    const fake = new FakeClient("ready");
    fake.on("thread/read", ({ ref: requested }) => currentRead(requested ?? ref));
    fake.on("thread/unsubscribe", () => ({}));
    connectionStore.getState().connect(fake);
    let transaction: ReturnType<typeof holdNextWriteTransaction> | undefined;
    const writeStarted = deferred<void>();
    const originalPut = cache.putCurrent.bind(cache);
    const put = vi.spyOn(cache, "putCurrent").mockImplementation(async (record, now) => {
      if (!transaction && record.ref === ref) {
        transaction = holdNextWriteTransaction(["records", "meta", "current", "currentMeta"]);
        writeStarted.resolve();
      }
      return originalPut(record, now);
    });
    const lease = acquireBackgroundThread(ref);
    let reacquired: ReturnType<typeof acquireBackgroundThread> | undefined;
    try {
      await lease.ready;
      await writeStarted.promise;
      await transaction?.reached;
      expect(threadsStore.getState().threads.has(ref)).toBe(true);
      const preFrame = backgroundTranscriptSnapshotForTests();
      const baseCharged = preFrame.refs.get(ref)?.residentBytes;
      if (!baseCharged) throw new Error("expected the real base model to be admitted");
      expect(preFrame.refs.get(ref)?.handoffBytes).toBe(0);

      // The store folds the notification into the resident, asks the real
      // publication gate to admit it, and must revert when admission refuses.
      fake.emitNotification(oversizedFrame);
      const refusedModel = threadsStore.getState().threads.get(ref);
      if (!refusedModel) throw new Error("expected the base model to remain published");
      expect(JSON.stringify(currentTranscriptState(refusedModel))).not.toContain(marker);
      const refused = backgroundTranscriptSnapshotForTests();
      expect(refused.refs.get(ref)?.residentBytes).toBe(baseCharged);
      expect(refused.refs.get(ref)?.handoffBytes).toBe(0);
      expect(refused.pendingBytes).toBe(0);
      expect(refused.refs.get(ref)?.persistence).toBe("repairing");

      // Releasing the held write and the lease, then re-acquiring, forces a
      // fresh real background hydration through the authoritative publication
      // gate; the refused candidate never appears and the ref returns to
      // useful, current state.
      transaction?.release();
      await settleBackgroundTranscriptsForTests();
      await lease.release();
      reacquired = acquireBackgroundThread(ref);
      await reacquired.ready;
      await settleBackgroundTranscriptsForTests();
      const recovered = threadsStore.getState().threads.get(ref);
      if (!recovered) throw new Error("expected the ref to recover through the real publication gate");
      expect(JSON.stringify(currentTranscriptState(recovered))).not.toContain(marker);
      const recoveredEntry = backgroundTranscriptSnapshotForTests().refs.get(ref);
      expect(recoveredEntry?.persistence).toBe("enabled");
      expect(recoveredEntry?.current).toBe(true);
    } finally {
      transaction?.release();
      put.mockRestore();
      await settleBackgroundTranscriptsForTests();
      await lease.release();
      await reacquired?.release();
      resetThreadsStoreForTests();
      restoreOptions();
    }
  });

  test("recovers a background ref through a real cache miss without replaying the dropped frame", async () => {
    const ref = "local:miss";
    const restoreOptions = installBackgroundTranscriptOptionsForTests({
      residentBytes: encodedPayloadBytes(currentTranscriptState(hydrateThread(currentRead(ref), ref, 1))),
    });
    resetThreadsStoreForTests();
    setSessionCacheAdapterForTests(cache);
    const fake = new FakeClient("ready");
    fake.on("thread/read", ({ ref: requested }) => currentRead(requested ?? ref));
    fake.on("thread/unsubscribe", () => ({}));
    connectionStore.getState().connect(fake);
    const writes: Array<{ ref: string; state: unknown; ownerToken: string }> = [];
    const originalPut = cache.putCurrent.bind(cache);
    const put = vi.spyOn(cache, "putCurrent").mockImplementation(async (record, now) => {
      const outcome = await originalPut(record, now);
      if (outcome.outcome === "written") writes.push(record);
      return outcome;
    });
    const lease = acquireBackgroundThread(ref);
    const other = acquireBackgroundThread("local:other");
    try {
      await Promise.all([lease.ready, other.ready]);
      await settleBackgroundTranscriptsForTests();
      // The second resident evicted the target while keeping its background
      // ownership; deleting the real checkpoint makes the next write look up a
      // genuinely missing record.
      expect(threadsStore.getState().threads.has(ref)).toBe(false);
      expect(await cache.deleteRecords([ref])).toBe(true);

      const stale = textDelta(ref, "-stale-frame");
      fake.emitNotification(stale);
      await waitForHydration(ref, 2);
      await settleBackgroundTranscriptsForTests();
      const recoveredEntry = backgroundTranscriptSnapshotForTests().refs.get(ref);
      expect(recoveredEntry?.persistence).toBe("enabled");
      expect(recoveredEntry?.current).toBe(true);
      const recoveredWrite = writes.filter((write) => write.ref === ref).at(-1);
      if (!recoveredWrite) throw new Error("expected the recovered ref to reach the real cache");
      const onDisk = await cache.getCurrent([recoveredWrite.ownerToken, ref], Date.now());
      expect(JSON.stringify(onDisk?.record?.state)).not.toContain("-stale-frame");
      expect(JSON.stringify(onDisk?.record?.state)).toContain("alpha");
    } finally {
      await settleBackgroundTranscriptsForTests();
      put.mockRestore();
      await Promise.all([lease.release(), other.release()]);
      resetThreadsStoreForTests();
      restoreOptions();
    }
  });

  test("recovers a real oversized cache hit through the base guard and a same-lease authoritative repair", async () => {
    const ref = "local:oversized-base";
    const maxBytes = 64 * 1024 * 1024;
    const restoreOptions = installBackgroundTranscriptOptionsForTests({ residentBytes: 0 });
    const localCache = new SessionCacheIndexedDB({
      indexedDB: new IDBFactory(),
      databaseName: `background-oversized-${crypto.randomUUID()}`,
      maxBytes,
    });
    resetThreadsStoreForTests();
    setSessionCacheAdapterForTests(localCache);
    const fake = new FakeClient("ready");
    const readIssued = deferred<void>();
    const releaseRead = deferred<void>();
    let holdReads = false;
    fake.on("thread/read", async ({ ref: requested }) => {
      if (holdReads) {
        readIssued.resolve();
        await releaseRead.promise;
      }
      return currentRead(requested ?? ref);
    });
    fake.on("thread/unsubscribe", () => ({}));
    connectionStore.getState().connect(fake);

    // Call-through observers over the real test-local adapter: capture the
    // initial successful checkpoint's key/certificate, and record every real
    // getCurrent so an oversized hit is told apart from a cache miss.
    const writes: CurrentSessionRecord[] = [];
    const lookups: CurrentSessionRecord[] = [];
    const originalPut = localCache.putCurrent.bind(localCache);
    const put = vi.spyOn(localCache, "putCurrent").mockImplementation(async (record, now) => {
      const outcome = await originalPut(record, now);
      if (outcome.outcome === "written") writes.push(record);
      return outcome;
    });
    const originalGet = localCache.getCurrent.bind(localCache);
    const get = vi.spyOn(localCache, "getCurrent").mockImplementation(async (key, now) => {
      const found = await originalGet(key, now);
      if (found?.record) lookups.push(found.record);
      return found;
    });

    const lease = acquireBackgroundThread(ref);
    let paneHeld = false;
    try {
      await lease.ready;
      await settleBackgroundTranscriptsForTests();
      // residentBytes 0 settles the hydrated base to disk-only, so the next
      // real delta has to load whatever checkpoint the worker finds.
      expect(threadsStore.getState().threads.has(ref)).toBe(false);
      const initial = writes.filter((write) => write.ref === ref).at(-1);
      if (!initial) throw new Error("expected the real background hydration to write an initial checkpoint");
      expect(independentJsonBytes(initial.state)).toBeLessThan(32 * 1024 * 1024);

      // Replace the checkpoint's body through the real adapter, keeping its
      // captured key/certificate. The valid body independently measures over
      // the product's 32 MiB base bound while the whole record still fits the
      // injected 64 MiB cache cap, so the adapter admits the write itself.
      const oversizedState = JSON.parse(JSON.stringify(initial.state)) as CurrentSessionRecord["state"];
      const overlayKey = "stream:s1:agentMessage";
      const overlayEntry = oversizedState.overlay[overlayKey];
      if (!overlayEntry) throw new Error(`expected the captured overlay item ${overlayKey}`);
      oversizedState.overlay[overlayKey] = {
        ...overlayEntry,
        item: { ...overlayEntry.item, text: "z".repeat(34 * 1024 * 1024 + 4) } as typeof overlayEntry.item,
      };
      const oversizedRecord: CurrentSessionRecord = { ...initial, savedAt: Date.now(), state: oversizedState };
      expect(independentJsonBytes(oversizedState)).toBeGreaterThan(32 * 1024 * 1024);
      expect(independentJsonBytes(oversizedRecord)).toBeLessThan(maxBytes);
      const oversizedWrite = await localCache.putCurrent(oversizedRecord, Date.now());
      expect(oversizedWrite.outcome).toBe("written");

      // The uniquely marked stale delta sends the worker to the real cache hit
      // and parks the ensuing authoritative read in its handler. At the hold
      // the oversized base was rejected before folding: the retired frame's
      // queue entry and charge are gone, the ref stays repairing, and nothing
      // oversized or stale published.
      const staleMarker = "-stale-oversized";
      const readsBeforeStale = fake.calls.filter((call) => call.method === "thread/read").length;
      const hydrationsBeforeStale = threadsStore.getState().hydrations.get(ref) ?? 0;
      holdReads = true;
      fake.emitNotification(textDelta(ref, staleMarker));
      await readIssued.promise;
      expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(readsBeforeStale + 1);
      expect(lookups.some((record) => independentJsonBytes(record.state) > 32 * 1024 * 1024)).toBe(true);
      const held = backgroundTranscriptSnapshotForTests();
      const heldEntry = held.refs.get(ref);
      expect(heldEntry?.persistence).toBe("repairing");
      expect(held.pendingBytes).toBe(0);
      expect(heldEntry?.residentBytes).toBe(0);
      expect(heldEntry?.pending).toHaveLength(0);
      expect(heldEntry?.inFlight).toHaveLength(0);
      expect(threadsStore.getState().threads.has(ref)).toBe(false);
      expect(JSON.stringify(threadsStore.getState().threads.get(ref) ?? null)).not.toContain(staleMarker);

      // A small authoritative response, without releasing or reacquiring the
      // lease, republishes current state and restores a usable checkpoint.
      releaseRead.resolve();
      await waitForHydration(ref, hydrationsBeforeStale + 1);
      await settleBackgroundTranscriptsForTests();
      const recoveredEntry = backgroundTranscriptSnapshotForTests().refs.get(ref);
      expect(recoveredEntry?.persistence).toBe("enabled");
      expect(recoveredEntry?.current).toBe(true);
      const recovered = await localCache.getCurrent([initial.ownerToken, ref], Date.now());
      if (!recovered?.record) throw new Error("expected the repaired ref to have a current checkpoint");
      expect(independentJsonBytes(recovered.record.state)).toBeLessThan(32 * 1024 * 1024);
      expect(JSON.stringify(recovered.record.state)).toContain("alpha");
      expect(JSON.stringify(recovered.record.state)).not.toContain(staleMarker);

      // A fresh delta settles exactly once, and reopening locally repaints it
      // from the checkpoint with no activation read.
      const freshMarker = "-fresh-oversized";
      fake.emitNotification(textDelta(ref, freshMarker));
      await settleBackgroundTranscriptsForTests();
      const readsBeforeOpen = fake.calls.filter((call) => call.method === "thread/read").length;
      await threadsStore.getState().ensureThread(ref);
      paneHeld = true;
      const opened = threadsStore.getState().threads.get(ref);
      if (!opened) throw new Error("expected the recovered ref to reopen from its local checkpoint");
      const displayText = opened.turns
        .flatMap((turn) => turn.items)
        .map((item) => item.text ?? "")
        .join("");
      expect(displayText).toContain(freshMarker);
      expect(displayText.split(freshMarker)).toHaveLength(2);
      expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(readsBeforeOpen);
    } finally {
      releaseRead.resolve();
      holdReads = false;
      put.mockRestore();
      get.mockRestore();
      if (paneHeld) threadsStore.getState().releaseThread(ref);
      await settleBackgroundTranscriptsForTests();
      await lease.release();
      resetThreadsStoreForTests();
      restoreOptions();
      localCache.close();
    }
  });

  test("preserves a pane-held history window and cursor while another background ref is under pressure", async () => {
    const restoreOptions = installBackgroundTranscriptOptionsForTests({ residentBytes: 0, pendingBytes: 4096 });
    resetThreadsStoreForTests();
    setSessionCacheAdapterForTests(cache);
    const fake = connectCurrentClient();
    const first = acquireBackgroundThread("local:one");
    const second = acquireBackgroundThread("local:two");
    let paneHeld = false;
    try {
      await first.ready;
      await settleBackgroundTranscriptsForTests();
      expect(threadsStore.getState().threads.has("local:one")).toBe(false);
      // Register the cleanup before the await resolves: ensureThread claims the
      // pane refcount synchronously, so a rejection still leaves a claim that
      // the failure path must release.
      const pane = threadsStore.getState().ensureThread("local:one");
      paneHeld = true;
      await pane;
      const held = threadsStore.getState().threads.get("local:one");
      expect(held).toBeDefined();
      const cursor = held?.olderCursor;
      const heldHistory = held?.history;

      await second.ready;
      await settleBackgroundTranscriptsForTests();
      expect(backgroundTranscriptSnapshotForTests().residentBytes).toBe(0);
      expect(threadsStore.getState().threads.get("local:one")).toBe(held);
      expect(threadsStore.getState().threads.get("local:one")?.olderCursor).toBe(cursor);
      expect(threadsStore.getState().threads.get("local:one")?.history).toBe(heldHistory);
      expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(2);
    } finally {
      if (paneHeld) threadsStore.getState().releaseThread("local:one");
      await Promise.all([first.release(), second.release()]);
      resetThreadsStoreForTests();
      restoreOptions();
    }
  });

  test("a pane-held paged model and its Find match survive an oversized background completion, then evict on release", async () => {
    const restoreOptions = installBackgroundTranscriptOptionsForTests({
      residentBytes: 0,
      pendingBytes: 8 * 1024 * 1024,
    });
    resetThreadsStoreForTests();
    setSessionCacheAdapterForTests(cache);
    const pressureGate = deferred<void>();
    const pressureStarted = deferred<void>();
    const fake = new FakeClient("ready");
    fake.on("thread/read", async ({ ref }) => {
      if (ref === "local:pressure") {
        pressureStarted.resolve();
        await pressureGate.promise;
        const response = currentRead("local:pressure");
        response.overlay![0]!.item.text = "p".repeat(256 * 1024);
        return response;
      }
      return positionedLatestRead(ref ?? "local:one");
    });
    fake.on("thread/unsubscribe", () => ({}));
    fake.on("thread/turns/list", () =>
      olderPage("turn_page1", 1, 0, ["page-one-item", "unique-later-item🙂"], "cursor_p1"),
    );
    connectionStore.getState().connect(fake);
    const background = acquireBackgroundThread("local:one");
    const pressure = acquireBackgroundThread("local:pressure");
    let paneHeld = false;
    let foregroundHeld = false;
    try {
      await background.ready;
      const pane = threadsStore.getState().ensureThread("local:one");
      paneHeld = true;
      await pane;
      await threadsStore.getState().loadOlderTurns("local:one");
      const held = threadsStore.getState().threads.get("local:one");
      if (!held) throw new Error("expected the pane-held paged model");
      const readsBeforePressure = fake.calls.filter(
        (call) => call.method === "thread/read" && (call.params as { ref?: string }).ref === "local:one",
      ).length;

      // The pressure read is admitted, but its oversized response is gated until
      // the pane hold and its loaded history already exist.
      await pressureStarted.promise;
      expect(backgroundTranscriptSnapshotForTests().networkHydrations).toBeGreaterThan(0);
      // An unrelated foreground pane still hydrates while the protected pane and
      // the oversized pressure read are both alive.
      const foreground = threadsStore.getState().ensureThread("local:foreground");
      foregroundHeld = true;
      await foreground;
      expect(threadsStore.getState().threads.has("local:foreground")).toBe(true);

      pressureGate.resolve();
      await pressure.ready;
      await settleBackgroundTranscriptsForTests();

      expect(threadsStore.getState().threads.get("local:one")).toBe(held);
      expect(threadsStore.getState().threads.get("local:one")?.olderCursor).toBe("cursor_p1");
      expect(threadsStore.getState().threads.get("local:one")?.history).toBe(held.history);
      expect(threadsStore.getState().threads.has("local:pressure")).toBe(false);
      expect(
        fake.calls.filter(
          (call) => call.method === "thread/read" && (call.params as { ref?: string }).ref === "local:one",
        ),
      ).toHaveLength(readsBeforePressure);

      // The palette's in-session Find reads the live pane model, not a lease.
      const matches = findInSessionMatches(focusedModel("local:one")!, "unique-later-item");
      expect(matches.map((match) => match.itemId)).toContain("turn_page1-item-1");
      expect(focusedModel("local:one")).toBe(held);

      threadsStore.getState().releaseThread("local:one");
      paneHeld = false;
      await settleBackgroundTranscriptsForTests();
      expect(threadsStore.getState().threads.has("local:one")).toBe(false);
    } finally {
      pressureGate.resolve();
      if (foregroundHeld) threadsStore.getState().releaseThread("local:foreground");
      if (paneHeld) threadsStore.getState().releaseThread("local:one");
      await Promise.all([background.release(), pressure.release()]);
      resetThreadsStoreForTests();
      restoreOptions();
    }
  });

  test("a held older-page request keeps its history and cursor through an oversized background completion", async () => {
    const restoreOptions = installBackgroundTranscriptOptionsForTests({
      residentBytes: 16 * 1024,
      pendingBytes: 8 * 1024 * 1024,
    });
    resetThreadsStoreForTests();
    setSessionCacheAdapterForTests(cache);
    const pressureGate = deferred<void>();
    const pressureStarted = deferred<void>();
    const fake = new FakeClient("ready");
    fake.on("thread/read", async ({ ref }) => {
      if (ref === "local:pressure") {
        pressureStarted.resolve();
        await pressureGate.promise;
        const response = currentRead("local:pressure");
        response.overlay![0]!.item.text = "p".repeat(256 * 1024);
        return response;
      }
      return positionedLatestRead(ref ?? "local:one");
    });
    fake.on("thread/unsubscribe", () => ({}));
    const pageEntered = deferred<void>();
    const heldPage = deferred<ThreadTurnsListResponse>();
    fake.on("thread/turns/list", ({ cursor }) => {
      if (cursor === "cursor_latest")
        return olderPage("turn_page1", 1, 2, ["page-one-item", "unique-later-item🙂"], "cursor_p1");
      pageEntered.resolve();
      return heldPage.promise;
    });
    connectionStore.getState().connect(fake);
    let paneHeld = false;
    let foregroundHeld = false;
    const pressure = acquireBackgroundThread("local:pressure");
    let loading: Promise<void> | undefined;
    try {
      const pane = threadsStore.getState().ensureThread("local:one");
      paneHeld = true;
      await pane;
      await threadsStore.getState().loadOlderTurns("local:one");
      const held = threadsStore.getState().threads.get("local:one");
      if (!held) throw new Error("expected the paged model");
      const readsBeforePressure = fake.calls.filter(
        (call) => call.method === "thread/read" && (call.params as { ref?: string }).ref === "local:one",
      ).length;

      loading = threadsStore.getState().loadOlderTurns("local:one");
      await pageEntered.promise;
      // Release the pane while the page request is still in flight: the history
      // hold, not the pane, keeps the model for the duration.
      threadsStore.getState().releaseThread("local:one");
      paneHeld = false;
      expect(threadsStore.getState().threads.get("local:one")).toBe(held);

      // Establish the history hold before releasing the oversized response.
      await pressureStarted.promise;
      // An unrelated foreground pane still hydrates while the history-only hold
      // and the oversized pressure read are both alive.
      const foreground = threadsStore.getState().ensureThread("local:foreground");
      foregroundHeld = true;
      await foreground;
      expect(threadsStore.getState().threads.has("local:foreground")).toBe(true);
      pressureGate.resolve();
      await pressure.ready;
      await settleBackgroundTranscriptsForTests();
      expect(threadsStore.getState().threads.get("local:one")).toBe(held);
      expect(threadsStore.getState().threads.get("local:one")?.olderCursor).toBe("cursor_p1");
      expect(threadsStore.getState().threads.has("local:pressure")).toBe(false);
      expect(
        fake.calls.filter(
          (call) => call.method === "thread/read" && (call.params as { ref?: string }).ref === "local:one",
        ),
      ).toHaveLength(readsBeforePressure);

      // Reacquire the pane so the completed page can be observed after the
      // history hold ends; the in-flight page request continues underneath it.
      const reacquire = threadsStore.getState().ensureThread("local:one");
      paneHeld = true;
      await reacquire;
      expect(threadsStore.getState().threads.get("local:one")).toBe(held);
      heldPage.resolve(olderPage("turn_page2", 1, 0, ["later-page-two-item"], "cursor_p2"));
      await loading;
      loading = undefined;
      const merged = threadsStore.getState().threads.get("local:one");
      expect(merged?.turns.flatMap((turn) => turn.items).map((item) => item.text)).toContain("later-page-two-item");
      expect(merged?.olderCursor).toBe("cursor_p2");

      threadsStore.getState().releaseThread("local:one");
      paneHeld = false;
      await settleBackgroundTranscriptsForTests();
      expect(threadsStore.getState().threads.has("local:one")).toBe(false);
    } finally {
      heldPage.resolve(olderPage("turn_page2", 1, 0, ["later-page-two-item"], "cursor_p2"));
      pressureGate.resolve();
      await loading?.catch(() => undefined);
      if (foregroundHeld) threadsStore.getState().releaseThread("local:foreground");
      if (paneHeld) threadsStore.getState().releaseThread("local:one");
      await pressure.release();
      resetThreadsStoreForTests();
      restoreOptions();
    }
  });

  test("a durable mutation pin keeps its paged model through an oversized background completion until the receipt settles", async () => {
    const restoreOptions = installBackgroundTranscriptOptionsForTests({
      residentBytes: 16 * 1024,
      pendingBytes: 8 * 1024 * 1024,
    });
    resetThreadsStoreForTests();
    setSessionCacheAdapterForTests(cache);
    const storage = mutationStorage();
    const pressureGate = deferred<void>();
    const pressureStarted = deferred<void>();
    const fake = new FakeClient("ready");
    fake.on("thread/read", async ({ ref }) => {
      if (ref === "local:pressure") {
        pressureStarted.resolve();
        await pressureGate.promise;
        const response = currentRead("local:pressure");
        response.overlay![0]!.item.text = "p".repeat(256 * 1024);
        return response;
      }
      return positionedLatestRead(ref ?? "local:one");
    });
    fake.on("thread/unsubscribe", () => ({}));
    fake.on("thread/turns/list", () =>
      olderPage("turn_page1", 1, 0, ["page-one-item", "unique-later-item🙂"], "cursor_p1"),
    );
    const started = deferred<{ clientMutationId: string }>();
    const response = deferred<{
      turn: { id: string; status: "inProgress"; itemsView: string };
      receipt: { clientMutationId: string; disposition: "applied"; threadId: string; projectionState: "reflected" };
    }>();
    fake.on("turn/start", (params) => {
      started.resolve(params);
      return response.promise;
    });
    connectionStore.getState().connect(fake);
    let paneHeld = false;
    const pressure = acquireBackgroundThread("local:pressure");
    let sending: Promise<void> | undefined;
    try {
      const pane = threadsStore.getState().ensureThread("local:one");
      paneHeld = true;
      await pane;
      await threadsStore.getState().loadOlderTurns("local:one");
      const held = threadsStore.getState().threads.get("local:one");
      if (!held) throw new Error("expected the paged model");

      // A durable explicit send pins the ref. Its dispatch response is held, so
      // the outbox row (and the pin) stays durable across the pane release.
      sending = threadsStore.getState().send("local:one", "pinned send", [], [], [], []);
      const params = await started.promise;
      threadsStore.getState().releaseThread("local:one");
      paneHeld = false;
      expect(threadsStore.getState().threads.get("local:one")).toBe(held);
      const readsBeforePressure = fake.calls.filter(
        (call) => call.method === "thread/read" && (call.params as { ref?: string }).ref === "local:one",
      ).length;

      // Establish the mutation pin before releasing the oversized response.
      await pressureStarted.promise;
      pressureGate.resolve();
      await pressure.ready;
      await settleBackgroundTranscriptsForTests();
      expect(threadsStore.getState().threads.get("local:one")).toBe(held);
      expect(threadsStore.getState().threads.get("local:one")?.olderCursor).toBe("cursor_p1");
      expect(threadsStore.getState().threads.has("local:pressure")).toBe(false);
      expect(
        fake.calls.filter(
          (call) => call.method === "thread/read" && (call.params as { ref?: string }).ref === "local:one",
        ),
      ).toHaveLength(readsBeforePressure);
      expect(
        threadsStore
          .getState()
          .threads.get("local:one")
          ?.turns.flatMap((turn) => turn.items)
          .map((item) => item.text),
      ).toContain("unique-later-item🙂");

      response.resolve({
        turn: { id: "turn_pinned", status: "inProgress", itemsView: "" },
        receipt: {
          clientMutationId: params.clientMutationId,
          disposition: "applied",
          threadId: "thr_local:one",
          projectionState: "reflected",
        },
      });
      await sending;
      sending = undefined;
      await expect(storage.getOutbox(params.clientMutationId)).resolves.toBeDefined();

      const retired = deferred<void>();
      const unsubscribe = threadsStore.subscribe((state) => {
        if (!state.threads.has("local:one")) retired.resolve();
      });
      try {
        await storage.settleReceipt(params.clientMutationId, "reflected");
        await retired.promise;
      } finally {
        unsubscribe();
      }
      await settleBackgroundTranscriptsForTests();
      expect(threadsStore.getState().threads.has("local:one")).toBe(false);
    } finally {
      if (paneHeld) threadsStore.getState().releaseThread("local:one");
      response.resolve({
        turn: { id: "turn_pinned", status: "inProgress", itemsView: "" },
        receipt: {
          clientMutationId: "background-mutation-cleanup",
          disposition: "applied",
          threadId: "thr_local:one",
          projectionState: "reflected",
        },
      });
      await sending?.catch(() => undefined);
      pressureGate.resolve();
      await pressure.release();
      resetThreadsStoreForTests();
      restoreOptions();
    }
  });

  test.each(["background", "pane"] as const)(
    "a new %s claim during the final cut write keeps later notification progress",
    async (claimKind) => {
      const fake = connectCurrentClient();
      await cache.get("local:one", Date.now());
      const background = acquireBackgroundThread("local:one");
      await background.ready;
      fake.emitNotification(textDelta("local:one", "-before-cut"));
      const reached = [deferred<void>(), deferred<void>()] as const;
      const held: ReturnType<typeof holdIndexedDBEvent>[] = [];
      const stores = ["records", "meta", "current", "currentMeta"];
      const originalTransaction = IDBDatabase.prototype.transaction;
      let writeCount = 0;
      const transactionSpy = vi.spyOn(IDBDatabase.prototype, "transaction").mockImplementation(function (
        this: IDBDatabase,
        ...args: Parameters<IDBDatabase["transaction"]>
      ) {
        const transaction = originalTransaction.apply(this, args);
        if (
          transaction.mode === "readwrite" &&
          transaction.objectStoreNames.length === stores.length &&
          stores.every((store) => transaction.objectStoreNames.contains(store)) &&
          writeCount < 2
        ) {
          const index = writeCount++;
          const gate = holdIndexedDBEvent(transaction, "complete");
          held[index] = gate;
          const completion = reached[index];
          if (!completion) throw new Error(`unexpected cache write ${index + 1}`);
          void gate.reached.then(() => completion.resolve());
        }
        return transaction;
      });
      let replacement: ReturnType<typeof acquireBackgroundThread> | undefined;
      let paneClaimed = false;
      try {
        const startingReads = membershipReads(fake);
        const release = background.release();
        await reached[0].promise;
        fake.emitNotification({
          method: "history/updated",
          params: {
            ref: "local:one",
            threadId: "thr_local:one",
            bootGeneration: "1",
            epoch: 1,
            snapshot: { incarnation: "inc-1", length: 42 },
            turns: [{ id: "turn_cut", status: "completed", itemsView: "full", version: 2 }],
            items: [
              {
                type: "agentMessage",
                id: "item_cut",
                turnId: "turn_cut",
                text: "pre-cut-model",
                position: { entry: 42, item: 0 },
              },
            ],
          },
        } as AnyNotification);
        const firstWrite = held[0];
        if (!firstWrite) throw new Error("first cache write was not held");
        firstWrite.release();
        await reached[1].promise;
        if (claimKind === "background") {
          replacement = acquireBackgroundThread("local:one");
          await replacement.ready;
        } else {
          await threadsStore.getState().ensureThread("local:one");
          paneClaimed = true;
        }
        expect(membershipReads(fake)).toBe(startingReads);
        expect(unsubscriptions(fake)).toBe(0);
        fake.emitNotification(textDelta("local:one", "-during-cut"));
        const secondWrite = held[1];
        if (!secondWrite) throw new Error("second cache write was not held");
        secondWrite.release();
        await release;
        fake.emitNotification(textDelta("local:one", "-after-new-claim"));
        expect(
          threadsStore
            .getState()
            .threads.get("local:one")
            ?.turns.flatMap((turn) => turn.items)
            .map((item) => item.text)
            .join(""),
        ).toContain("pre-cut-modelalpha-before-cut-during-cut-after-new-claim");
        expect(threadsStore.getState().threads.has("local:one")).toBe(true);
        expect(unsubscriptions(fake)).toBe(0);
      } finally {
        transactionSpy.mockRestore();
        for (const gate of held) gate.release();
        await replacement?.release();
        if (paneClaimed) threadsStore.getState().releaseThread("local:one");
        await background.release();
      }
    },
  );

  test.each([true, false])("final release drains a recorded-history update, identity=%s", async (hasIdentity) => {
    const storage = mutationStorage();
    const fake = connectCurrentClient();
    await cache.get("local:one", Date.now());
    const background = acquireBackgroundThread("local:one");
    await background.ready;
    const reached = [deferred<void>(), deferred<void>()] as const;
    const held: ReturnType<typeof holdIndexedDBEvent>[] = [];
    const stores = ["records", "meta", "current", "currentMeta"];
    const receiptCleanupRead = deferred<void>();
    const originalListOptimistic = storage.listOptimistic.bind(storage);
    const listOptimistic = vi.spyOn(storage, "listOptimistic").mockImplementation(async (ref) => {
      const rows = await originalListOptimistic(ref);
      if (ref === "local:one") receiptCleanupRead.resolve();
      return rows;
    });
    const originalTransaction = IDBDatabase.prototype.transaction;
    let writeCount = 0;
    const transactionSpy = vi.spyOn(IDBDatabase.prototype, "transaction").mockImplementation(function (
      this: IDBDatabase,
      ...args: Parameters<IDBDatabase["transaction"]>
    ) {
      const transaction = originalTransaction.apply(this, args);
      if (
        transaction.mode === "readwrite" &&
        transaction.objectStoreNames.length === stores.length &&
        stores.every((store) => transaction.objectStoreNames.contains(store)) &&
        writeCount < 2
      ) {
        const index = writeCount++;
        const gate = holdIndexedDBEvent(transaction, "complete");
        held[index] = gate;
        if (index === 0) void gate.reached.then(() => reached[0].resolve());
        else void gate.reached.then(() => reached[1].resolve());
      }
      return transaction;
    });
    try {
      const release = background.release();
      let releaseSettled = false;
      void release.then(() => {
        releaseSettled = true;
      });
      await reached[0].promise;
      fake.emitNotification({
        method: "history/updated",
        params: {
          ref: "local:one",
          threadId: "thr_local:one",
          bootGeneration: "1",
          epoch: 1,
          snapshot: { incarnation: "inc-1", length: 41 },
          turns: [{ id: "turn_final", status: "completed", itemsView: "full", version: 2 }],
          items: [
            {
              type: "agentMessage",
              id: "item_final",
              turnId: "turn_final",
              ...(hasIdentity ? { clientMutationId: "receipt-from-another-client" } : {}),
              text: "accepted-during-final-write",
              position: { entry: 41, item: 0 },
            },
          ],
        },
      } as AnyNotification);
      if (hasIdentity) {
        await receiptCleanupRead.promise;
        await nextRealTask();
      }
      expect(threadsStore.getState().threads.has("local:one")).toBe(true);
      const firstWrite = held[0];
      if (!firstWrite) throw new Error("first cache write was not held");
      firstWrite.release();
      await reached[1].promise;
      expect(releaseSettled).toBe(false);
      const secondWrite = held[1];
      if (!secondWrite) throw new Error("second cache write was not held");
      secondWrite.release();
      await release;
      expect(threadsStore.getState().threads.has("local:one")).toBe(false);
      expect(unsubscriptions(fake)).toBe(1);
      const durable = JSON.stringify(await cache.get("local:one", Date.now()));
      expect(durable).toContain("accepted-during-final-write");
      expect(durable.split("accepted-during-final-write")).toHaveLength(2);
    } finally {
      transactionSpy.mockRestore();
      listOptimistic.mockRestore();
      for (const gate of held) gate.release();
      await background.release();
    }
  });

  test.each(["background", "pane"] as const)(
    "a %s replacement keeps wire membership through identity receipt cleanup",
    async (replacementKind) => {
      const storage = mutationStorage();
      const fake = connectCurrentClient();
      await cache.get("local:one", Date.now());
      const background = acquireBackgroundThread("local:one");
      await background.ready;
      const reached = [deferred<void>(), deferred<void>()] as const;
      const held: ReturnType<typeof holdIndexedDBEvent>[] = [];
      const stores = ["records", "meta", "current", "currentMeta"];
      const receiptCleanupRead = deferred<void>();
      const originalListOptimistic = storage.listOptimistic.bind(storage);
      const listOptimistic = vi.spyOn(storage, "listOptimistic").mockImplementation(async (ref) => {
        const rows = await originalListOptimistic(ref);
        if (ref === "local:one") receiptCleanupRead.resolve();
        return rows;
      });
      const originalTransaction = IDBDatabase.prototype.transaction;
      let writeCount = 0;
      const transactionSpy = vi.spyOn(IDBDatabase.prototype, "transaction").mockImplementation(function (
        this: IDBDatabase,
        ...args: Parameters<IDBDatabase["transaction"]>
      ) {
        const transaction = originalTransaction.apply(this, args);
        if (
          transaction.mode === "readwrite" &&
          transaction.objectStoreNames.length === stores.length &&
          stores.every((store) => transaction.objectStoreNames.contains(store)) &&
          writeCount < reached.length
        ) {
          const index = writeCount++;
          const gate = holdIndexedDBEvent(transaction, "complete");
          held[index] = gate;
          const completion = reached[index];
          if (!completion) throw new Error(`unexpected cache write ${index + 1}`);
          void gate.reached.then(() => completion.resolve());
        }
        return transaction;
      });
      let replacement: ReturnType<typeof acquireBackgroundThread> | undefined;
      let paneClaimed = false;
      try {
        const startingReads = membershipReads(fake);
        const release = background.release();
        await reached[0].promise;
        fake.emitNotification({
          method: "history/updated",
          params: {
            ref: "local:one",
            threadId: "thr_local:one",
            bootGeneration: "1",
            epoch: 1,
            snapshot: { incarnation: "inc-1", length: 41 },
            turns: [{ id: "turn_receipt_claim", status: "completed", itemsView: "full", version: 2 }],
            items: [
              {
                type: "agentMessage",
                id: "item_receipt_claim",
                turnId: "turn_receipt_claim",
                clientMutationId: "receipt-from-another-client",
                text: "receipt-and-replacement",
                position: { entry: 41, item: 0 },
              },
            ],
          },
        } as AnyNotification);
        await receiptCleanupRead.promise;
        await nextRealTask();

        if (replacementKind === "background") {
          replacement = acquireBackgroundThread("local:one");
          await replacement.ready;
        } else {
          await threadsStore.getState().ensureThread("local:one");
          paneClaimed = true;
        }
        expect(membershipReads(fake)).toBe(startingReads);
        expect(unsubscriptions(fake)).toBe(0);
        expect(threadsStore.getState().threads.has("local:one")).toBe(true);

        const firstWrite = held[0];
        if (!firstWrite) throw new Error("first cache write was not held");
        firstWrite.release();
        await reached[1].promise;
        expect(unsubscriptions(fake)).toBe(0);
        fake.emitNotification(textDelta("local:one", "-while-final-write-held"));
        expect(
          threadsStore
            .getState()
            .threads.get("local:one")
            ?.turns.flatMap((turn) => turn.items)
            .map((item) => item.text)
            .join(""),
        ).toContain("-while-final-write-held");
        const secondWrite = held[1];
        if (!secondWrite) throw new Error("final cache write was not held");
        secondWrite.release();
        await release;
        expect(unsubscriptions(fake)).toBe(0);
        fake.emitNotification(textDelta("local:one", "-after-release"));
        expect(
          threadsStore
            .getState()
            .threads.get("local:one")
            ?.turns.flatMap((turn) => turn.items)
            .map((item) => item.text)
            .join(""),
        ).toContain("-while-final-write-held-after-release");
      } finally {
        transactionSpy.mockRestore();
        listOptimistic.mockRestore();
        for (const gate of held) gate.release();
        await replacement?.release();
        if (paneClaimed) threadsStore.getState().releaseThread("local:one");
        await background.release();
      }
    },
  );

  test("final release cuts live updates at the bounded final write", async () => {
    const fake = connectCurrentClient();
    await cache.get("local:one", Date.now());
    const background = acquireBackgroundThread("local:one");
    await background.ready;
    const emitRecorded = (length: number, text: string) => {
      fake.emitNotification({
        method: "history/updated",
        params: {
          ref: "local:one",
          threadId: "thr_local:one",
          bootGeneration: "1",
          epoch: 1,
          snapshot: { incarnation: "inc-1", length },
          turns: [{ id: "turn_bounded", status: "completed", itemsView: "full", version: 2 }],
          items: [
            {
              type: "agentMessage",
              id: `item_bounded_${length}`,
              turnId: "turn_bounded",
              text,
              position: { entry: length, item: 0 },
            },
          ],
        },
      } as AnyNotification);
    };
    emitRecorded(40, "before-release");
    const gates: ReturnType<typeof holdIndexedDBEvent>[] = [];
    const stores = ["records", "meta", "current", "currentMeta"];
    const originalTransaction = IDBDatabase.prototype.transaction;
    let writeCount = 0;
    const transactionSpy = vi.spyOn(IDBDatabase.prototype, "transaction").mockImplementation(function (
      this: IDBDatabase,
      ...args: Parameters<IDBDatabase["transaction"]>
    ) {
      const transaction = originalTransaction.apply(this, args);
      if (
        transaction.mode === "readwrite" &&
        transaction.objectStoreNames.length === stores.length &&
        stores.every((store) => transaction.objectStoreNames.contains(store))
      ) {
        const index = writeCount++;
        const gate = holdIndexedDBEvent(transaction, "complete");
        gates[index] = gate;
        void gate.reached.then(() => {
          gate.release();
          if (index < 3) fake.emitNotification(textDelta("local:one", `-write-completion-${index + 1}`));
        });
      }
      return transaction;
    });
    try {
      const release = background.release();
      await release;
      expect(writeCount).toBe(2);
      expect(threadsStore.getState().threads.has("local:one")).toBe(false);
      expect(unsubscriptions(fake)).toBe(1);
      const durable = JSON.stringify(await cache.get("local:one", Date.now()));
      expect(durable).toContain("before-release");
      expect(durable.split("before-release")).toHaveLength(2);
    } finally {
      transactionSpy.mockRestore();
      for (const gate of gates) gate.release();
      await background.release();
    }
  });

  test("a bounded final cache failure does not block another ref release or claim a save", async () => {
    cache.close();
    let failNextPut = true;
    cache = new SessionCacheIndexedDB({
      indexedDB: new IDBFactory(),
      databaseName: `background-thread-failure-${crypto.randomUUID()}`,
      beforeCommit(operation) {
        if (operation === "put" && failNextPut) {
          failNextPut = false;
          throw new Error("injected final cache failure");
        }
      },
    });
    setSessionCacheAdapterForTests(cache);
    await Promise.all([cache.get("local:one", Date.now()), cache.get("local:two", Date.now())]);
    const fake = connectCurrentClient();
    const first = acquireBackgroundThread("local:one");
    const second = acquireBackgroundThread("local:two");
    await Promise.all([first.ready, second.ready]);
    for (const [ref, threadId, itemId] of [
      ["local:one", "thr_local:one", "item_failure_one"],
      ["local:two", "thr_local:two", "item_failure_two"],
    ]) {
      fake.emitNotification({
        method: "history/updated",
        params: {
          ref,
          threadId,
          bootGeneration: "1",
          epoch: 1,
          snapshot: { incarnation: "inc-1", length: 41 },
          turns: [{ id: `turn_${itemId}`, status: "completed", itemsView: "full", version: 2 }],
          items: [
            {
              type: "agentMessage",
              id: itemId,
              turnId: `turn_${itemId}`,
              text: itemId,
              position: { entry: 41, item: 0 },
            },
          ],
        },
      } as AnyNotification);
    }

    await first.release();
    expect(failNextPut).toBe(false);
    expect(await cache.get("local:one", Date.now())).toBeUndefined();
    expect(threadsStore.getState().threads.has("local:one")).toBe(false);

    await second.release();
    expect(threadsStore.getState().threads.has("local:two")).toBe(false);
    expect(await cache.get("local:two", Date.now())).toBeDefined();
    expect(unsubscriptions(fake)).toBe(2);
  });

  test("a reconnect replaces a nonresident desired ref's pages with the latest window, then pages back to the prior later-page item", async () => {
    const restoreOptions = installBackgroundTranscriptOptionsForTests({ residentBytes: 0, pendingBytes: 64 * 1024 });
    resetThreadsStoreForTests();
    setSessionCacheAdapterForTests(cache);
    const fake = new FakeClient("ready");
    fake.on("thread/read", ({ ref }) => positionedLatestRead(ref ?? "local:one"));
    fake.on("thread/unsubscribe", () => ({}));
    fake.on("thread/turns/list", ({ cursor }) => {
      switch (cursor) {
        case "cursor_latest":
          return olderPage("turn_page1", 1, 2, ["page-one-item"], "cursor_p1");
        case "cursor_p1":
          return olderPage("turn_page2", 1, 0, ["page-two-item", "unique-later-item🙂"], "cursor_p2");
        case "cursor_p2":
          return olderPage("turn_page3", 0, 4, ["beyond-oldest-item"], undefined);
        default:
          throw new Error(`unexpected older-page cursor ${cursor}`);
      }
    });
    connectionStore.getState().connect(fake);
    const background = acquireBackgroundThread("local:one");
    let paneHeld = false;
    const visibleText = (): string =>
      threadsStore
        .getState()
        .threads.get("local:one")
        ?.turns.flatMap((turn) => turn.items)
        .map((item) => item.text ?? "")
        .join("") ?? "";
    try {
      await background.ready;
      await settleBackgroundTranscriptsForTests();
      expect(threadsStore.getState().threads.has("local:one")).toBe(false);

      const pane = threadsStore.getState().ensureThread("local:one");
      paneHeld = true;
      await pane;
      await threadsStore.getState().loadOlderTurns("local:one");
      await threadsStore.getState().loadOlderTurns("local:one");
      expect(visibleText()).toContain("unique-later-item🙂");
      expect(threadsStore.getState().threads.get("local:one")?.olderCursor).toBe("cursor_p2");

      // Close the pane: the desired background ref goes nonresident (disk only).
      threadsStore.getState().releaseThread("local:one");
      paneHeld = false;
      await settleBackgroundTranscriptsForTests();
      expect(threadsStore.getState().threads.has("local:one")).toBe(false);

      // Reconnect: the authoritative latest window replaces held history; the
      // old checkpoint cannot certify the pre-reconnect pages.
      const hydrationsBefore = threadsStore.getState().hydrations.get("local:one") ?? 0;
      fake.emitStateChange("reconnecting");
      fake.emitReady();
      await waitForHydration("local:one", hydrationsBefore + 1);
      await settleBackgroundTranscriptsForTests();
      await nextRealTask();

      const reopen = threadsStore.getState().ensureThread("local:one");
      paneHeld = true;
      await reopen;
      const replacedTurns =
        threadsStore
          .getState()
          .threads.get("local:one")
          ?.turns.map((turn) => turn.id) ?? [];
      expect(replacedTurns).toContain("turn_latest");
      expect(replacedTurns).not.toContain("turn_page1");
      expect(replacedTurns).not.toContain("turn_page2");
      expect(visibleText()).toContain("latest-recorded-item");
      expect(visibleText()).not.toContain("unique-later-item🙂");
      // Do not assert retention of the pre-reconnect cursor; the response's own
      // cursor is what pages back.
      expect(threadsStore.getState().threads.get("local:one")?.olderCursor).toBe("cursor_latest");

      // Page back through the server cursor: the previously visible later-page
      // item appears exactly once, and further paging still advances.
      await threadsStore.getState().loadOlderTurns("local:one");
      await threadsStore.getState().loadOlderTurns("local:one");
      expect(occurrences(visibleText(), "unique-later-item🙂")).toBe(1);
      await threadsStore.getState().loadOlderTurns("local:one");
      expect(visibleText()).toContain("beyond-oldest-item");
      expect(occurrences(visibleText(), "unique-later-item🙂")).toBe(1);
    } finally {
      if (paneHeld) threadsStore.getState().releaseThread("local:one");
      await settleBackgroundTranscriptsForTests();
      await background.release();
      resetThreadsStoreForTests();
      restoreOptions();
    }
  });

  test("a continuously Live background claim does not keep a cleared ref's recorded-history writes suppressed after an explicit re-open", async () => {
    const ref = "local:one";
    const restoreOptions = installBackgroundTranscriptOptionsForTests({ residentBytes: 0 });
    resetThreadsStoreForTests();
    setSessionCacheAdapterForTests(cache);
    setCacheWriteTimersForTests({ debounceMs: 1, maxWaitMs: 1 });
    connectCurrentClient();
    const lease = acquireBackgroundThread(ref);
    let paneHeld = false;
    try {
      await lease.ready;
      await settleBackgroundTranscriptsForTests();
      await threadsStore.getState().ensureThread(ref);
      paneHeld = true;
      await vi.waitFor(async () => expect(await cache.get(ref, Date.now())).toBeDefined());

      expect(await clearCachedSessions()).toEqual({ committed: true });
      expect(await cache.get(ref, Date.now())).toBeUndefined();
      expect(threadsStore.getState().cacheLifetimes.get(ref)?.suppressed).toBe(true);

      threadsStore.getState().releaseThread(ref);
      paneHeld = false;
      await settleBackgroundTranscriptsForTests();
      // Background demand stays: the model is gone, the Live claim is not.
      expect(threadsStore.getState().threads.has(ref)).toBe(false);

      await threadsStore.getState().ensureThread(ref);
      paneHeld = true;
      await settleBackgroundTranscriptsForTests();
      // The explicit re-open hydrates authoritatively; recorded-history caching
      // must resume even though the ref never left Live.
      await vi.waitFor(async () => expect(await cache.get(ref, Date.now())).toBeDefined());
      expect(threadsStore.getState().cacheLifetimes.get(ref)?.suppressed).toBeUndefined();
    } finally {
      if (paneHeld) threadsStore.getState().releaseThread(ref);
      await settleBackgroundTranscriptsForTests();
      await lease.release();
      resetThreadsStoreForTests();
      restoreOptions();
    }
  });

  test("clear while continuously Live suppresses until an explicit re-open hydrates and restores caching", async () => {
    const ref = "local:one";
    const restoreOptions = installBackgroundTranscriptOptionsForTests({ residentBytes: 0 });
    resetThreadsStoreForTests();
    setSessionCacheAdapterForTests(cache);
    const fake = connectCurrentClient();
    const lease = acquireBackgroundThread(ref);
    let paneHeld = false;
    try {
      await lease.ready;
      await settleBackgroundTranscriptsForTests();
      // Both holders exist when clear commits: the background Live claim and the
      // explicitly opened pane.
      await threadsStore.getState().ensureThread(ref);
      paneHeld = true;
      expect(backgroundTranscriptSnapshotForTests().refs.get(ref)?.persistence).toBe("enabled");

      expect(await clearCachedSessions()).toEqual({ committed: true });
      expect(backgroundTranscriptSnapshotForTests().refs.get(ref)?.persistence).toBe("clear-suppressed");

      // Releasing the pane leaves background demand; suppression stays and the
      // unwritable model leaves RAM without a futile write.
      threadsStore.getState().releaseThread(ref);
      paneHeld = false;
      await settleBackgroundTranscriptsForTests();
      expect(threadsStore.getState().threads.has(ref)).toBe(false);
      expect(backgroundTranscriptSnapshotForTests().refs.get(ref)?.persistence).toBe("clear-suppressed");

      // The next explicit foreground acquisition hydrates authoritatively and
      // restores the persistence lifetime at the current epoch.
      const readsBefore = fake.calls.filter((call) => call.method === "thread/read").length;
      await threadsStore.getState().ensureThread(ref);
      paneHeld = true;
      expect(fake.calls.filter((call) => call.method === "thread/read").length).toBeGreaterThan(readsBefore);
      expect(backgroundTranscriptSnapshotForTests().refs.get(ref)?.persistence).toBe("enabled");

      // It committed a usable checkpoint, so the next re-open is local: no read.
      threadsStore.getState().releaseThread(ref);
      paneHeld = false;
      await settleBackgroundTranscriptsForTests();
      const afterRecovery = fake.calls.filter((call) => call.method === "thread/read").length;
      await threadsStore.getState().ensureThread(ref);
      paneHeld = true;
      expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(afterRecovery);
    } finally {
      if (paneHeld) threadsStore.getState().releaseThread(ref);
      await settleBackgroundTranscriptsForTests();
      await lease.release();
      resetThreadsStoreForTests();
      restoreOptions();
    }
  });

  test("background release and reacquire alone never lift a clear's suppression", async () => {
    const ref = "local:one";
    const restoreOptions = installBackgroundTranscriptOptionsForTests({ residentBytes: 0 });
    resetThreadsStoreForTests();
    setSessionCacheAdapterForTests(cache);
    connectCurrentClient();
    const lease = acquireBackgroundThread(ref);
    try {
      await lease.ready;
      await settleBackgroundTranscriptsForTests();
      expect(await clearCachedSessions()).toEqual({ committed: true });
      expect(backgroundTranscriptSnapshotForTests().refs.get(ref)?.persistence).toBe("clear-suppressed");

      await lease.release();
      await settleBackgroundTranscriptsForTests();
      const readAgain = acquireBackgroundThread(ref);
      try {
        await readAgain.ready;
        await settleBackgroundTranscriptsForTests();
        // Background churn cannot reset suppression: no explicit foreground
        // acquisition ever happened.
        expect(backgroundTranscriptSnapshotForTests().refs.get(ref)?.persistence).toBe("clear-suppressed");
      } finally {
        await readAgain.release();
      }
    } finally {
      await settleBackgroundTranscriptsForTests();
      resetThreadsStoreForTests();
      restoreOptions();
    }
  });

  test("same-tab Clear synchronously fences a pending background hydration before its outcome returns", async () => {
    const ref = "local:one";
    const restoreOptions = installBackgroundTranscriptOptionsForTests({ residentBytes: 16 * 1024 * 1024 });
    resetThreadsStoreForTests();
    setSessionCacheAdapterForTests(cache);
    await cache.readEpoch();
    const entered = deferred<void>();
    const response = deferred<ThreadReadResponse>();
    const fake = new FakeClient("ready");
    fake.on("thread/read", () => {
      entered.resolve();
      return response.promise;
    });
    fake.on("thread/unsubscribe", () => ({}));
    connectionStore.getState().connect(fake);
    const lease = acquireBackgroundThread(ref);
    const hold = holdNextWriteTransaction(["records", "meta", "current", "currentMeta"]);
    let published: ThreadModel | undefined;
    try {
      await entered.promise;
      const clearing = clearCachedSessions();
      await hold.reached;
      expect(threadsStore.getState().clearInFlight).toBeDefined();
      // The pre-Clear read returns while the clear arm is up.
      response.resolve(currentRead(ref));
      await lease.ready;
      published = threadsStore.getState().threads.get(ref);
      hold.release();
      expect(await clearing).toEqual({ committed: true });
      expect(published).toBeUndefined();
    } finally {
      response.resolve(currentRead(ref));
      hold.release();
      await lease.release();
      resetThreadsStoreForTests();
      restoreOptions();
    }
  });

  test("two pre-Clear panes keep both cache classes suppressed until a later fresh pane", async () => {
    const ref = "local:one";
    const restoreOptions = installBackgroundTranscriptOptionsForTests({ residentBytes: 16 * 1024 * 1024 });
    resetThreadsStoreForTests();
    setSessionCacheAdapterForTests(cache);
    const fake = connectCurrentClient();
    const lease = acquireBackgroundThread(ref);
    let panesHeld = 0;
    try {
      await lease.ready;
      await settleBackgroundTranscriptsForTests();
      await threadsStore.getState().ensureThread(ref);
      panesHeld += 1;
      await threadsStore.getState().ensureThread(ref);
      panesHeld += 1;

      expect(await clearCachedSessions()).toEqual({ committed: true });
      expect(backgroundTranscriptSnapshotForTests().refs.get(ref)?.persistence).toBe("clear-suppressed");
      expect(threadsStore.getState().cacheLifetimes.get(ref)?.suppressed).toBe(true);

      // Release one pre-Clear pane and open a new one while the other pre-Clear
      // pane still holds: neither class may be restored.
      const readsBefore = fake.calls.filter((call) => call.method === "thread/read").length;
      threadsStore.getState().releaseThread(ref);
      panesHeld -= 1;
      await threadsStore.getState().ensureThread(ref);
      panesHeld += 1;
      await settleBackgroundTranscriptsForTests();
      expect(threadsStore.getState().cacheLifetimes.get(ref)?.suppressed).toBe(true);
      expect(backgroundTranscriptSnapshotForTests().refs.get(ref)?.persistence).toBe("clear-suppressed");
      expect(fake.calls.filter((call) => call.method === "thread/read").length).toBe(readsBefore);

      // Release the remaining pre-Clear panes, then a fresh pane restores both.
      threadsStore.getState().releaseThread(ref);
      panesHeld -= 1;
      threadsStore.getState().releaseThread(ref);
      panesHeld -= 1;
      await settleBackgroundTranscriptsForTests();
      const beforeFresh = fake.calls.filter((call) => call.method === "thread/read").length;
      await threadsStore.getState().ensureThread(ref);
      panesHeld += 1;
      expect(fake.calls.filter((call) => call.method === "thread/read").length).toBeGreaterThan(beforeFresh);
      expect(backgroundTranscriptSnapshotForTests().refs.get(ref)?.persistence).toBe("enabled");
      expect(threadsStore.getState().cacheLifetimes.get(ref)?.suppressed).toBeUndefined();
    } finally {
      for (let i = 0; i < panesHeld; i += 1) threadsStore.getState().releaseThread(ref);
      await settleBackgroundTranscriptsForTests();
      await lease.release();
      resetThreadsStoreForTests();
      restoreOptions();
    }
  });

  test("a durable mutation pin keeps both cache classes suppressed through Clear until it releases and a fresh pane opens", async () => {
    const ref = "local:one";
    const restoreOptions = installBackgroundTranscriptOptionsForTests({ residentBytes: 16 * 1024 * 1024 });
    resetThreadsStoreForTests();
    setSessionCacheAdapterForTests(cache);
    const storage = mutationStorage();
    const started = deferred<{ clientMutationId: string }>();
    const response = deferred<{
      turn: { id: string; status: "inProgress"; itemsView: string };
      receipt: {
        clientMutationId: string;
        disposition: "applied";
        threadId: string;
        projectionState: "reflected";
      };
    }>();
    const fake = new FakeClient("ready");
    fake.on("thread/read", ({ ref: requested }) => currentRead(requested ?? ref));
    fake.on("thread/unsubscribe", () => ({}));
    fake.on("turn/start", (params) => {
      started.resolve(params);
      return response.promise;
    });
    connectionStore.getState().connect(fake);
    const lease = acquireBackgroundThread(ref);
    let paneHeld = false;
    let sending: Promise<void> | undefined;
    try {
      await lease.ready;
      await settleBackgroundTranscriptsForTests();
      await threadsStore.getState().ensureThread(ref);
      paneHeld = true;
      // A durable explicit send pins the ref; its dispatch response is held.
      sending = threadsStore.getState().send(ref, "pinned send", [], [], [], []);
      const params = await started.promise;
      threadsStore.getState().releaseThread(ref);
      paneHeld = false;
      expect(threadsStore.getState().threads.has(ref)).toBe(true);

      expect(await clearCachedSessions()).toEqual({ committed: true });
      expect(backgroundTranscriptSnapshotForTests().refs.get(ref)?.persistence).toBe("clear-suppressed");
      expect(threadsStore.getState().cacheLifetimes.get(ref)?.suppressed).toBe(true);

      // A new pane while the pre-Clear pin still holds cannot restore either
      // class: the pin is a pre-Clear holder.
      const readsBefore = fake.calls.filter((call) => call.method === "thread/read").length;
      await threadsStore.getState().ensureThread(ref);
      paneHeld = true;
      await settleBackgroundTranscriptsForTests();
      expect(backgroundTranscriptSnapshotForTests().refs.get(ref)?.persistence).toBe("clear-suppressed");
      expect(threadsStore.getState().cacheLifetimes.get(ref)?.suppressed).toBe(true);
      expect(fake.calls.filter((call) => call.method === "thread/read").length).toBe(readsBefore);

      // Settle the receipt: the pin releases.
      response.resolve({
        turn: { id: "turn_pinned", status: "inProgress", itemsView: "" },
        receipt: {
          clientMutationId: params.clientMutationId,
          disposition: "applied",
          threadId: "thr_local:one",
          projectionState: "reflected",
        },
      });
      await sending;
      sending = undefined;
      await storage.settleReceipt(params.clientMutationId, "reflected");
      await settleBackgroundTranscriptsForTests();
      for (let i = 0; i < 10; i += 1) {
        await nextRealTask();
        await settleBackgroundTranscriptsForTests();
      }

      // Release the pane, then a fresh explicit pane restores both classes.
      threadsStore.getState().releaseThread(ref);
      paneHeld = false;
      await settleBackgroundTranscriptsForTests();
      const beforeFresh = fake.calls.filter((call) => call.method === "thread/read").length;
      await threadsStore.getState().ensureThread(ref);
      paneHeld = true;
      expect(fake.calls.filter((call) => call.method === "thread/read").length).toBeGreaterThan(beforeFresh);
      expect(backgroundTranscriptSnapshotForTests().refs.get(ref)?.persistence).toBe("enabled");
      expect(threadsStore.getState().cacheLifetimes.get(ref)?.suppressed).toBeUndefined();
    } finally {
      response.resolve({
        turn: { id: "turn_pinned", status: "inProgress", itemsView: "" },
        receipt: {
          clientMutationId: "background-pin-cleanup",
          disposition: "applied",
          threadId: "thr_local:one",
          projectionState: "reflected",
        },
      });
      await sending?.catch(() => undefined);
      if (paneHeld) threadsStore.getState().releaseThread(ref);
      await settleBackgroundTranscriptsForTests();
      await lease.release();
      resetThreadsStoreForTests();
      restoreOptions();
    }
  });
});
