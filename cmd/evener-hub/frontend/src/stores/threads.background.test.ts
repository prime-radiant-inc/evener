import "fake-indexeddb/auto";
import {
  type AnyNotification,
  acquireThreadSubscription,
  currentTranscriptState,
  type ThreadModel,
  type ThreadTurnsListResponse,
  WireError,
} from "@evener/appwire-client";
import { currentRead, textDelta } from "@evener/appwire-client/testing/currentTranscript";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { IDBDatabase, IDBFactory } from "fake-indexeddb";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { installBackgroundTranscriptOptionsForTests } from "./backgroundTranscriptOptions";
import { connectionStore } from "./connection";
import { MutationOutboxIndexedDB } from "./mutationOutboxIndexedDB";
import { SessionCacheIndexedDB } from "./sessionCacheIndexedDB";
import { holdIndexedDBEvent, holdNextWriteTransaction, nextRealTask } from "./testing/stalledIndexedDB";
import {
  acquireBackgroundThread,
  backgroundTranscriptSnapshotForTests,
  installHydrationRetrySchedulerForTests,
  resetThreadsStoreForTests,
  setMutationStorageForTests,
  setSessionCacheAdapterForTests,
  settleBackgroundTranscriptsForTests,
  threadsStore,
} from "./threads";
import { encodedPayloadBytes } from "./transcriptPayloadBudget";

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

function independentJsonBytes(value: unknown): number {
  const encoded = JSON.stringify(value);
  if (encoded === undefined) return Number.POSITIVE_INFINITY;
  return new TextEncoder().encode(encoded).byteLength;
}

describe("background thread claims", () => {
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
      await threadsStore.getState().ensureThread("local:one");
      releasePane = () => threadsStore.getState().releaseThread("local:one");
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
        restoreOptions();
      }
    }
  });

  test("promotes once across a held current lookup and applies notifications on both sides", async () => {
    const restoreOptions = installBackgroundTranscriptOptionsForTests({ residentBytes: 0, pendingBytes: 4096 });
    resetThreadsStoreForTests();
    setSessionCacheAdapterForTests(cache);
    const fake = connectCurrentClient();
    const background = acquireBackgroundThread("local:one");
    let paneHeld = false;
    let heldLookup: ReturnType<typeof holdNextWriteTransaction> | undefined;
    let promotedAtPublication: ThreadModel | undefined;
    const observePromotion = threadsStore.subscribe((state) => {
      const model = state.threads.get("local:one");
      if (model?.turns.flatMap((turn) => turn.items).some((item) => item.text?.includes("-during-lookup"))) {
        promotedAtPublication = model;
      }
    });
    try {
      await background.ready;
      await settleBackgroundTranscriptsForTests();
      expect(threadsStore.getState().threads.has("local:one")).toBe(false);

      const readCount = fake.calls.filter((call) => call.method === "thread/read").length;
      heldLookup = holdNextWriteTransaction(["records", "meta", "current", "currentMeta"]);
      const promotion = threadsStore.getState().ensureThread("local:one");
      await heldLookup.reached;
      const toolKey = "stream:round_tool/0:toolOutput";
      fake.emitNotification({
        method: "overlay/upserted",
        params: {
          threadId: "thr_local:one",
          ref: "local:one",
          item: {
            key: toolKey,
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
      fake.emitNotification({
        method: "overlay/delta",
        params: { threadId: "thr_local:one", ref: "local:one", key: toolKey, field: "output", delta: "工具🙂" },
      } as AnyNotification);
      fake.emitNotification(textDelta("local:one", "-during-lookup"));
      expect(backgroundTranscriptSnapshotForTests().pendingBytes).toBeGreaterThan(0);
      heldLookup.release();
      await promotion;
      paneHeld = true;

      const adopted = threadsStore.getState().threads.get("local:one");
      expect(adopted).toBeDefined();
      expect(adopted).toBe(promotedAtPublication);
      fake.emitNotification({
        method: "overlay/delta",
        params: { threadId: "thr_local:one", ref: "local:one", key: toolKey, field: "output", delta: ":later" },
      } as AnyNotification);
      fake.emitNotification(textDelta("local:one", "-after-publication"));
      const text =
        threadsStore
          .getState()
          .threads.get("local:one")
          ?.turns.flatMap((turn) => turn.items)
          .map((item) => item.text)
          .join("") ?? "";
      expect(text).toContain("alpha-during-lookup-after-publication");
      expect(text.split("-during-lookup")).toHaveLength(2);
      expect(text.split("-after-publication")).toHaveLength(2);
      const toolItems = adopted?.turns.flatMap((turn) => turn.items).filter((item) => item.id === "item_tool") ?? [];
      expect(toolItems).toHaveLength(1);
      expect(toolItems[0]?.output).toBe("工具🙂");
      const afterNotifications = threadsStore.getState().threads.get("local:one");
      expect(afterNotifications?.overlay?.[toolKey]?.item.output).toBe("工具🙂:later");
      expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(readCount);
      expect(fake.calls.filter((call) => ["turn/start", "turn/steer", "turn/queue"].includes(call.method))).toEqual([]);
    } finally {
      heldLookup?.release();
      observePromotion();
      if (paneHeld) threadsStore.getState().releaseThread("local:one");
      await background.release();
      resetThreadsStoreForTests();
      restoreOptions();
    }
  });

  test("accounts two completed hydrations and deltas while the real checkpoint transaction is held", async () => {
    const restoreOptions = installBackgroundTranscriptOptionsForTests({
      residentBytes: 1024 * 1024,
      pendingBytes: 4 * 1024 * 1024,
    });
    resetThreadsStoreForTests();
    setSessionCacheAdapterForTests(cache);
    const one = deferred<ReturnType<typeof currentRead>>();
    const two = deferred<ReturnType<typeof currentRead>>();
    const fake = new FakeClient("ready");
    fake.on("thread/read", ({ ref }) => (ref === "local:one" ? one.promise : two.promise));
    fake.on("thread/unsubscribe", () => ({}));
    connectionStore.getState().connect(fake);
    const first = acquireBackgroundThread("local:one");
    const second = acquireBackgroundThread("local:two");
    const acceptedFrames: AnyNotification[] = [];
    const writePayloads: Array<{ state: unknown; bytes: number; recordBytes: number }> = [];
    const putCurrentStarted = deferred<void>();
    let transaction: ReturnType<typeof holdNextWriteTransaction> | undefined;
    const publicationAccounting: Array<{ retainedBytes: number; chargedBytes: number }> = [];
    const observeAccounting = threadsStore.subscribe((state) => {
      const retainedBytes = [...state.threads.values()].reduce(
        (sum, model) => sum + independentJsonBytes(currentTranscriptState(model)),
        0,
      );
      const snapshot = backgroundTranscriptSnapshotForTests();
      publicationAccounting.push({
        retainedBytes,
        chargedBytes: snapshot.residentBytes + snapshot.pendingBytes + snapshot.workerBytes,
      });
    });
    const originalPutCurrent = cache.putCurrent.bind(cache);
    const putCurrent = vi.spyOn(cache, "putCurrent").mockImplementation(async (record, now) => {
      if (!transaction) {
        writePayloads.push({
          state: record.state,
          bytes: independentJsonBytes(record),
          recordBytes: independentJsonBytes(record),
        });
        transaction = holdNextWriteTransaction(["records", "meta", "current", "currentMeta"]);
        putCurrentStarted.resolve();
      }
      return originalPutCurrent(record, now);
    });
    const payload = (ref: string) => {
      const response = currentRead(ref);
      response.overlay![0]!.item.text = "x".repeat(256 * 1024);
      return response;
    };
    try {
      await vi.waitFor(() => expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(2));
      const firstResponse = payload("local:one");
      one.resolve(firstResponse);
      await vi.waitFor(() => expect(threadsStore.getState().hydrations.get("local:one")).toBeGreaterThan(0), {
        timeout: 1500,
      });
      const secondResponse = payload("local:two");
      two.resolve(secondResponse);
      await vi.waitFor(() => expect(threadsStore.getState().hydrations.get("local:two")).toBeGreaterThan(0), {
        timeout: 1500,
      });
      expect(threadsStore.getState().threads.has("local:two")).toBe(true);

      const firstFrame = textDelta("local:one", "🙂".repeat(1024));
      acceptedFrames.push(firstFrame);
      fake.emitNotification(firstFrame);
      await vi.waitFor(() => expect(writePayloads.length).toBeGreaterThan(0), { timeout: 1500 });
      await putCurrentStarted.promise;
      await transaction?.reached;

      // The first write payload is retained inside the real cache put call while
      // its four-store IndexedDB transaction is held. The second decoded model
      // remains in the real store. Measure those concrete objects independently.
      const firstWrite = writePayloads[0];
      expect(firstWrite).toBeDefined();
      const heldSnapshot = backgroundTranscriptSnapshotForTests();
      const firstBodyBytes = independentJsonBytes(firstWrite?.state);
      const secondModel = threadsStore.getState().threads.get("local:two");
      expect(secondModel).toBeDefined();
      const secondStateBytes = independentJsonBytes(currentTranscriptState(secondModel!));
      expect(firstBodyBytes).toBeGreaterThan(1024);
      expect(secondStateBytes).toBeGreaterThan(1024);
      expect(heldSnapshot.workerBytes).toBeGreaterThanOrEqual(firstBodyBytes);
      expect(firstWrite?.recordBytes).toBeGreaterThan(firstBodyBytes);
      expect(heldSnapshot.residentBytes + heldSnapshot.pendingBytes + heldSnapshot.workerBytes).toBeGreaterThanOrEqual(
        firstBodyBytes + secondStateBytes,
      );

      for (let i = 0; i < 15; i += 1) {
        const frame = textDelta("local:one", "🙂".repeat(1024));
        acceptedFrames.push(frame);
        fake.emitNotification(frame);
        const snapshot = backgroundTranscriptSnapshotForTests();
        expect(snapshot.residentBytes).toBeLessThanOrEqual(1024 * 1024);
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
      acceptedFrames.push(toolUpsert, toolOutput);
      fake.emitNotification(toolUpsert);
      fake.emitNotification(toolOutput);
      expect(threadsStore.getState().threads.get("local:one")?.overlay?.[toolKey]?.item.output).toContain(
        '"retained":true',
      );
      expect(publicationAccounting.length).toBeGreaterThan(0);
      expect(publicationAccounting.every(({ retainedBytes, chargedBytes }) => chargedBytes >= retainedBytes)).toBe(
        true,
      );
      expect(acceptedFrames.every((frame) => independentJsonBytes(frame) > 0)).toBe(true);
      const retainedBytes = [...threadsStore.getState().threads.values()].reduce(
        (sum, model) => sum + independentJsonBytes(currentTranscriptState(model)),
        0,
      );
      const acceptedFrameBytes = acceptedFrames.reduce((sum, frame) => sum + independentJsonBytes(frame), 0);
      const chargedBytes = backgroundTranscriptSnapshotForTests();
      expect(chargedBytes.residentBytes + chargedBytes.pendingBytes + chargedBytes.workerBytes).toBeGreaterThanOrEqual(
        retainedBytes + acceptedFrameBytes,
      );
      expect(encodedPayloadBytes({ text: "🙂", raw: { output: ["工具", 7] } })).toBe(
        new TextEncoder().encode(JSON.stringify({ text: "🙂", raw: { output: ["工具", 7] } })).byteLength,
      );
      expect(backgroundTranscriptSnapshotForTests().workerBytes).toBeGreaterThan(0);
      transaction?.release();
      await vi.waitFor(() => expect(backgroundTranscriptSnapshotForTests().diskWorkers).toBe(0), { timeout: 1000 });
      await settleBackgroundTranscriptsForTests();
      const completed = backgroundTranscriptSnapshotForTests();
      expect(completed.diskWorkers).toBe(0);
      expect(completed.refs.get("local:one")?.current, JSON.stringify(completed.refs.get("local:one"))).toBe(true);
      expect(completed.refs.get("local:two")?.current).toBe(true);
      const readsBeforePromotion = fake.calls.filter((call) => call.method === "thread/read").length;
      await threadsStore.getState().ensureThread("local:one");
      const promoted = threadsStore.getState().threads.get("local:one");
      const text =
        promoted?.turns
          .flatMap((turn) => turn.items)
          .map((item) => item.text)
          .join("") ?? "";
      expect(text.match(/🙂/gu)).toHaveLength(1024 * 16);
      expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(readsBeforePromotion);
    } finally {
      transaction?.release();
      putCurrent.mockRestore();
      observeAccounting();
      threadsStore.getState().releaseThread("local:one");
      await Promise.all([first.release(), second.release()]);
      await settleBackgroundTranscriptsForTests();
      restoreOptions();
    }
  });

  test("sizes each resident text delta without serializing the whole retained transcript", async () => {
    const fake = new FakeClient("ready");
    fake.on("thread/read", ({ ref }) => {
      const response = currentRead(ref ?? "local:one");
      response.overlay![0]!.item.text = "x".repeat(256 * 1024);
      return response;
    });
    fake.on("thread/unsubscribe", () => ({}));
    connectionStore.getState().connect(fake);
    const background = acquireBackgroundThread("local:one");
    const originalStringify = JSON.stringify.bind(JSON);
    const stringify = vi.spyOn(JSON, "stringify");
    try {
      await background.ready;
      await settleBackgroundTranscriptsForTests();
      const model = threadsStore.getState().threads.get("local:one");
      expect(model).toBeDefined();
      const retainedStateBytes = new TextEncoder().encode(originalStringify(currentTranscriptState(model!))).byteLength;
      expect(retainedStateBytes).toBeGreaterThan(128 * 1024);
      stringify.mockClear();

      for (let index = 0; index < 8; index += 1) {
        fake.emitNotification(textDelta("local:one", `🙂${index}`));
      }

      const serializedArguments = stringify.mock.calls.map(([value]) => value);
      const serializedSizes = serializedArguments.map((value) => {
        const encoded = originalStringify(value);
        return encoded === undefined ? Number.POSITIVE_INFINITY : new TextEncoder().encode(encoded).byteLength;
      });
      expect(serializedSizes).toHaveLength(8);
      expect(Math.max(...serializedSizes)).toBeLessThan(retainedStateBytes / 2);
      expect(threadsStore.getState().threads.get("local:one")).not.toBe(model);
      expect(
        threadsStore
          .getState()
          .threads.get("local:one")
          ?.turns.flatMap((turn) => turn.items)
          .map((item) => item.text)
          .join(""),
      ).toContain("🙂0🙂1🙂2🙂3🙂4🙂5🙂6🙂7");
    } finally {
      stringify.mockRestore();
      await background.release();
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
      await threadsStore.getState().ensureThread("local:one");
      paneHeld = true;
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
      restoreOptions();
    }
  });

  test("background-only hydration leaves durable mutations untouched and sends no mutation RPC", async () => {
    const storage = mutationStorage();
    await storage.enqueueIntent({
      targetRef: "local:one",
      method: "turn/queue",
      payload: { ref: "local:one", expectedTurnId: "", input: [{ type: "text", text: "saved queue" }] },
      attachments: [],
      optimisticDisplay: { text: "saved queue" },
    });
    const before = await storage.listOutbox("local:one");
    const fake = connectCurrentClient();

    const background = acquireBackgroundThread("local:one");
    await background.ready;
    const after = await storage.listOutbox("local:one");
    expect(after).toEqual(before);
    expect(
      fake.calls.filter((call) => ["turn/start", "turn/steer", "turn/queue", "thread/resume"].includes(call.method)),
    ).toEqual([]);
    await background.release();
  });

  test("an explicit foreground send dispatches while a background claim remains active", async () => {
    mutationStorage();
    const fake = connectCurrentClient();
    const dispatched = deferred<{ input?: Array<{ type: string; text?: string }> }>();
    fake.on("turn/start", (params) => {
      dispatched.resolve(params);
      return {
        turn: { id: "turn_1", status: "inProgress", itemsView: "" },
        receipt: {
          clientMutationId: params.clientMutationId,
          disposition: "applied",
          threadId: "thr_local:one",
          projectionState: "reflected",
        },
      };
    });

    const background = acquireBackgroundThread("local:one");
    await background.ready;
    await threadsStore.getState().send("local:one", "foreground send", [], [], [], []);
    const params = await dispatched.promise;
    expect(params.input).toContainEqual({ type: "text", text: "foreground send" });
    await background.release();
  });

  test("pane adopts a background-hydrated model and releases only its own claim", async () => {
    const fake = connectCurrentClient();
    const activity = acquireThreadSubscription(fake, "local:one");
    activityLeases.push(activity);
    await activity.ensure();

    const first = acquireBackgroundThread("local:one");
    const second = acquireBackgroundThread("local:one");
    await Promise.all([first.ready, second.ready]);
    const resident = threadsStore.getState().threads.get("local:one");
    expect(
      resident?.turns
        .flatMap((turn) => turn.items)
        .map((item) => item.text)
        .join(""),
    ).toContain("alpha");
    const subscribedReads = membershipReads(fake);
    expect(subscribedReads).toBe(1);

    await Promise.all([
      threadsStore.getState().ensureThread("local:one"),
      threadsStore.getState().ensureThread("local:one"),
    ]);
    expect(threadsStore.getState().threads.get("local:one")).toBe(resident);
    expect(membershipReads(fake)).toBe(subscribedReads);

    await first.release();
    await second.release();
    fake.emitNotification(textDelta("local:one", "-after-background-close"));
    expect(
      threadsStore
        .getState()
        .threads.get("local:one")
        ?.turns.flatMap((turn) => turn.items)
        .map((item) => item.text)
        .join(""),
    ).toContain("alpha-after-background-close");
    expect(unsubscriptions(fake)).toBe(0);

    threadsStore.getState().releaseThread("local:one");
    expect(unsubscriptions(fake)).toBe(0);
    const unsubscribed = deferred<void>();
    fake.on("thread/unsubscribe", () => {
      unsubscribed.resolve();
      return {};
    });
    threadsStore.getState().releaseThread("local:one");
    activity.release();
    await unsubscribed.promise;
    expect(unsubscriptions(fake)).toBe(1);
  });

  test("foreground release leaves background routing alive until its final claim", async () => {
    const fake = connectCurrentClient();
    const pane = threadsStore.getState().ensureThread("local:one");
    await pane;
    const background = acquireBackgroundThread("local:one");
    await background.ready;
    const reads = fake.calls.filter((call) => call.method === "thread/read").length;

    threadsStore.getState().releaseThread("local:one");
    fake.emitNotification(textDelta("local:one", "-live"));
    expect(
      threadsStore
        .getState()
        .threads.get("local:one")
        ?.turns.flatMap((turn) => turn.items)
        .map((item) => item.text)
        .join(""),
    ).toContain("alpha-live");
    expect(unsubscriptions(fake)).toBe(0);
    expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(reads);

    await background.release();
    expect(unsubscriptions(fake)).toBe(1);
    expect(threadsStore.getState().threads.has("local:one")).toBe(false);
  });

  test("releasing the last background claim retires a held read without publishing it", async () => {
    const fake = connectCurrentClient();
    const entered = deferred<void>();
    const held = deferred<ReturnType<typeof currentRead>>();
    let readCount = 0;
    fake.on("thread/read", () => {
      readCount += 1;
      if (readCount === 1) {
        entered.resolve();
        return held.promise;
      }
      return currentRead("local:one");
    });
    const background = acquireBackgroundThread("local:one");
    try {
      await entered.promise;
      const readsBeforeRelease = membershipReads(fake);
      await background.release();
      await background.ready;
      held.resolve(currentRead("local:one"));
      await nextRealTask();
      expect(threadsStore.getState().threads.has("local:one")).toBe(false);
      expect(membershipReads(fake)).toBe(readsBeforeRelease);
      expect(unsubscriptions(fake)).toBe(1);
    } finally {
      held.resolve(currentRead("local:one"));
      await background.release();
    }
  });

  test("a failed first attempt settles ready and leaves paced recovery armed", async () => {
    const scheduled = deferred<() => void>();
    const restore = installHydrationRetrySchedulerForTests((_attempt, retry) => {
      scheduled.resolve(retry);
      return () => {};
    });
    try {
      const fake = connectCurrentClient();
      let attempts = 0;
      fake.on("thread/read", () => {
        attempts += 1;
        if (attempts === 1) throw new Error("temporary read failure");
        return currentRead("local:one");
      });
      const background = acquireBackgroundThread("local:one");
      await background.ready;
      expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(1);
      const retry = await scheduled.promise;
      expect(threadsStore.getState().threads.has("local:one")).toBe(false);
      const hydrated = deferred<void>();
      const unsubscribe = threadsStore.subscribe((state) => {
        if (state.threads.has("local:one")) hydrated.resolve();
      });
      try {
        retry();
        await hydrated.promise;
      } finally {
        unsubscribe();
      }
      expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(2);
      await background.release();
    } finally {
      restore();
    }
  });

  test("background retry does not reconcile or dispatch an uncertain durable row", async () => {
    const storage = mutationStorage();
    const retryScheduled = deferred<() => void>();
    const restore = installHydrationRetrySchedulerForTests((_attempt, retry) => {
      retryScheduled.resolve(retry);
      return () => {};
    });
    const restoreSpy = vi.spyOn(storage, "restoreProvenAbsent");
    const initialScans = deferred<void>();
    const listTargetRefs = storage.listTargetRefs.bind(storage);
    let scanCount = 0;
    const scanSpy = vi.spyOn(storage, "listTargetRefs").mockImplementation(async () => {
      const refs = await listTargetRefs();
      scanCount += 1;
      if (scanCount === 3) initialScans.resolve();
      return refs;
    });
    try {
      const fake = connectCurrentClient();
      await initialScans.promise;
      await nextRealTask();
      const blocked = await blockedOutboxRow(storage, "local:one");
      let reads = 0;
      fake.on("thread/read", () => {
        reads += 1;
        if (reads === 1) throw new Error("temporary read failure");
        const response = currentRead("local:one");
        return {
          ...response,
          thread: {
            ...response.thread,
            evener: { ...response.thread.evener, mutationStateAuthoritative: true },
          },
        };
      });
      const background = acquireBackgroundThread("local:one");
      try {
        await background.ready;
        const hydration = waitForHydration("local:one", 1);
        const retry = await retryScheduled.promise;
        retry();
        await hydration;
        await nextRealTask();
        expect(restoreSpy).not.toHaveBeenCalled();
        await expect(storage.getOutbox(blocked?.clientMutationId ?? "missing")).resolves.toEqual(blocked);
        expect(fake.calls.filter((call) => call.method.startsWith("turn/") || call.method === "thread/resume")).toEqual(
          [],
        );
      } finally {
        await background.release();
      }
    } finally {
      scanSpy.mockRestore();
      restoreSpy.mockRestore();
      restore();
    }
  });

  test("background history invalidation does not reconcile an uncertain durable row", async () => {
    const storage = mutationStorage();
    const restoreSpy = vi.spyOn(storage, "restoreProvenAbsent");
    const initialScans = deferred<void>();
    const listTargetRefs = storage.listTargetRefs.bind(storage);
    let scanCount = 0;
    const scanSpy = vi.spyOn(storage, "listTargetRefs").mockImplementation(async () => {
      const refs = await listTargetRefs();
      scanCount += 1;
      if (scanCount === 3) initialScans.resolve();
      return refs;
    });
    try {
      const fake = connectCurrentClient();
      await initialScans.promise;
      await nextRealTask();
      const blocked = await blockedOutboxRow(storage, "local:one");
      let reads = 0;
      fake.on("thread/read", (params) => {
        reads += 1;
        const response = currentRead("local:one");
        return {
          ...response,
          epoch: reads === 1 ? 2 : 3,
          requestGeneration: params.requestGeneration ?? response.requestGeneration,
          thread: {
            ...response.thread,
            evener: { ...response.thread.evener, mutationStateAuthoritative: true },
          },
        };
      });
      const background = acquireBackgroundThread("local:one");
      try {
        await background.ready;
        const invalidatedHydration = waitForHydration("local:one", 2);
        fake.emitNotification({
          method: "history/updated",
          params: {
            ref: "local:one",
            threadId: "thr_local:one",
            bootGeneration: "1",
            epoch: 3,
            snapshot: { incarnation: "inc-1", length: 41 },
            items: [],
          },
        } as AnyNotification);
        expect(threadsStore.getState().threads.get("local:one")?.history?.invalidatedAtGeneration).toBeDefined();
        await invalidatedHydration;
        await nextRealTask();
        expect(restoreSpy).not.toHaveBeenCalled();
        await expect(storage.getOutbox(blocked?.clientMutationId ?? "missing")).resolves.toEqual(blocked);
        expect(fake.calls.filter((call) => call.method.startsWith("turn/") || call.method === "thread/resume")).toEqual(
          [],
        );
      } finally {
        await background.release();
      }
    } finally {
      scanSpy.mockRestore();
      restoreSpy.mockRestore();
    }
  });

  test("repeated background release shares one final drain", async () => {
    const fake = connectCurrentClient();
    const background = acquireBackgroundThread("local:one");
    await background.ready;
    const first = background.release();
    const second = background.release();
    expect(second).toBe(first);
    await Promise.all([first, second]);
    expect(threadsStore.getState().threads.has("local:one")).toBe(false);
    expect(unsubscriptions(fake)).toBe(1);
  });

  test("a replacement client hydrates while the old client's held response is fenced", async () => {
    const oldClient = connectCurrentClient();
    const oldEntered = deferred<void>();
    const oldResponse = deferred<ReturnType<typeof currentRead>>();
    let oldReadCount = 0;
    oldClient.on("thread/read", () => {
      oldReadCount += 1;
      if (oldReadCount === 1) {
        oldEntered.resolve();
        return oldResponse.promise;
      }
      return currentRead("local:one");
    });
    const background = acquireBackgroundThread("local:one");
    await oldEntered.promise;
    const replacement = new FakeClient("ready");
    replacement.on("thread/read", ({ ref }) => currentRead(ref ?? "local:one"));
    replacement.on("thread/unsubscribe", () => ({}));
    const hydrated = deferred<void>();
    const unsubscribe = threadsStore.subscribe((state) => {
      if (state.threads.get("local:one")?.turns.length) hydrated.resolve();
    });
    let readySettled = false;
    void background.ready.then(() => {
      readySettled = true;
    });
    try {
      connectionStore.getState().connect(replacement);
      await hydrated.promise;
      const replacementModel = threadsStore.getState().threads.get("local:one");
      expect(replacement.calls.some((call) => call.method === "thread/read")).toBe(true);
      await nextRealTask();
      expect(readySettled).toBe(true);
      oldResponse.resolve(currentRead("local:one"));
      await nextRealTask();
      expect(threadsStore.getState().threads.get("local:one")).toBe(replacementModel);
    } finally {
      oldResponse.resolve(currentRead("local:one"));
      unsubscribe();
      await background.release();
    }
  });

  test("a deletion response settles the background attempt and records the deleted ref", async () => {
    const fake = connectCurrentClient();
    fake.on("thread/read", () => {
      throw new WireError("target has been deleted: local:one", -32001, {
        evenerErrorInfo: "actionUnavailable",
        mutationOutcome: "targetDeleted",
        retryDisposition: "none",
      });
    });
    const background = acquireBackgroundThread("local:one");
    await background.ready;
    expect(threadsStore.getState().deletedRefs.has("local:one")).toBe(true);
    expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(1);
    await background.release();
  });

  test("a threadId-only notification routes to the requested background alias", async () => {
    const fake = connectCurrentClient();
    const background = acquireBackgroundThread("local:one");
    await background.ready;
    const notification = {
      method: "overlay/delta",
      params: {
        threadId: "thr_local:one",
        key: "stream:s1:agentMessage",
        field: "text",
        delta: "-via-id",
      },
    } as AnyNotification;
    fake.emitNotification(notification);
    expect(
      threadsStore
        .getState()
        .threads.get("local:one")
        ?.turns.flatMap((turn) => turn.items)
        .map((item) => item.text)
        .join(""),
    ).toContain("alpha-via-id");
    await background.release();
  });

  test("a mutation pin keeps a background-owned model until the foreground send settles", async () => {
    const storage = mutationStorage();
    const fake = connectCurrentClient();
    const entered = deferred<{ clientMutationId: string }>();
    const response = deferred<{
      turn: { id: string; status: "inProgress"; itemsView: string };
      receipt: {
        clientMutationId: string;
        disposition: "applied";
        threadId: string;
        projectionState: "reflected";
      };
    }>();
    fake.on("turn/start", (params) => {
      entered.resolve(params);
      return response.promise;
    });
    await threadsStore.getState().ensureThread("local:one");
    const sending = threadsStore.getState().send("local:one", "pinned send", [], [], [], []);
    const { clientMutationId } = await entered.promise;
    const background = acquireBackgroundThread("local:one");
    await background.ready;
    threadsStore.getState().releaseThread("local:one");
    await background.release();
    expect(threadsStore.getState().threads.has("local:one")).toBe(true);
    response.resolve({
      turn: { id: "turn_pinned", status: "inProgress", itemsView: "" },
      receipt: {
        clientMutationId,
        disposition: "applied",
        threadId: "thr_local:one",
        projectionState: "reflected",
      },
    });
    await sending;
    expect(await storage.getOutbox(clientMutationId)).toBeDefined();
    const retired = deferred<void>();
    const unsubscribe = threadsStore.subscribe((state) => {
      if (!state.threads.has("local:one")) retired.resolve();
    });
    try {
      await storage.settleReceipt(clientMutationId, "reflected");
      await retired.promise;
    } finally {
      unsubscribe();
    }
    await expect(storage.getOutbox(clientMutationId)).resolves.toBeUndefined();
    expect(threadsStore.getState().threads.has("local:one")).toBe(false);
  });

  test("a new canonical claim during held final persistence keeps the model resident", async () => {
    const fake = connectCurrentClient();
    await cache.get("local:one", Date.now());
    const background = acquireBackgroundThread("local:one");
    await background.ready;
    fake.emitNotification(textDelta("local:one", "-persist-me"));
    const transaction = holdNextWriteTransaction(["records", "meta", "current", "currentMeta"]);
    try {
      const release = background.release();
      await transaction.reached;
      const replacement = acquireBackgroundThread("local:one");
      await replacement.ready;
      expect(unsubscriptions(fake)).toBe(0);
      transaction.release();
      await release;
      expect(threadsStore.getState().threads.has("local:one")).toBe(true);
      fake.emitNotification(textDelta("local:one", "-after-held-save"));
      expect(
        threadsStore
          .getState()
          .threads.get("local:one")
          ?.turns.flatMap((turn) => turn.items)
          .map((item) => item.text)
          .join(""),
      ).toContain("persist-me-after-held-save");
      await replacement.release();
    } finally {
      transaction.release();
    }
  });

  test("a pane claim during held final persistence keeps actual wire membership", async () => {
    const fake = connectCurrentClient();
    await cache.get("local:one", Date.now());
    const background = acquireBackgroundThread("local:one");
    await background.ready;
    const transaction = holdNextWriteTransaction(["records", "meta", "current", "currentMeta"]);
    try {
      const release = background.release();
      await transaction.reached;
      await threadsStore.getState().ensureThread("local:one");
      expect(unsubscriptions(fake)).toBe(0);
      transaction.release();
      await release;
      expect(unsubscriptions(fake)).toBe(0);
    } finally {
      transaction.release();
      await background.release();
      threadsStore.getState().releaseThread("local:one");
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

  test("keeps the model owned until an in-flight history page finishes", async () => {
    const fake = connectCurrentClient();
    await threadsStore.getState().ensureThread("local:one");
    const entered = deferred<void>();
    const page = deferred<ThreadTurnsListResponse>();
    fake.on("thread/turns/list", () => {
      entered.resolve();
      return page.promise;
    });

    const loading = threadsStore.getState().loadOlderTurns("local:one");
    await entered.promise;
    threadsStore.getState().releaseThread("local:one");
    expect(threadsStore.getState().threads.has("local:one")).toBe(true);

    page.resolve({ data: [], bootGeneration: "1", epoch: 1, snapshot: { incarnation: "inc-1", length: 40 } });
    await loading;
    expect(threadsStore.getState().threads.has("local:one")).toBe(false);
  });
});
