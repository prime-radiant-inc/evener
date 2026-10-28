import "fake-indexeddb/auto";
import * as appwireClient from "@evener/appwire-client";
import {
  type AnyNotification,
  AppwireClient,
  acquireThreadSubscription,
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
import { IDBFactory, IDBKeyRange } from "fake-indexeddb";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { installBackgroundTranscriptOptionsForTests } from "./backgroundTranscriptOptions";
import { connectionStore } from "./connection";
import { productionEntryTranscriptForTests, setProductionEntryCacheForTests } from "./entryTranscriptRoutes";
import { SessionEntryCache } from "./sessionEntryCache";
import {
  acquireBackgroundThread,
  backgroundTranscriptSnapshotForTests,
  observePendingHydrationRetirementsForTests,
  pendingThreadHydrationBaseModelsForTests,
  pendingThreadHydrationUpdatesForTests,
  putThreadModel,
  resetThreadsStoreForTests,
  settleBackgroundTranscriptsForTests,
  settleEntryTranscriptForTests,
  threadsStore,
} from "./threads";
import * as residencyModule from "./transcriptResidency";

let cache: SessionEntryCache;
const activityLeases: ReturnType<typeof acquireThreadSubscription>[] = [];
beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  cache = new SessionEntryCache({
    indexedDB: new IDBFactory(),
    keyRange: IDBKeyRange,
    databaseName: `parallel-background-entry-${crypto.randomUUID()}`,
  });
  setProductionEntryCacheForTests(cache);
});
afterEach(() => {
  for (const lease of activityLeases.splice(0)) lease.release();
  resetThreadsStoreForTests();
  cache.close();
});

function connectCurrentClient(): FakeClient {
  const fake = new FakeClient("ready");
  fake.on("thread/read", (params) => ({
    ...currentRead(params.ref ?? "local:one"),
    requestGeneration: params.requestGeneration,
  }));
  fake.on("thread/unsubscribe", () => ({}));
  connectionStore.getState().connect(fake);
  return fake;
}

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

function positionedLatestRead(ref: string): ThreadReadResponse {
  const base = currentRead(ref);
  base.olderCursor = "cursor_latest";
  base.snapshot = { incarnation: "inc-1", length: 100 };
  base.thread.turns = [pageTurn("turn_latest", 2, 0, ["latest-recorded-item"])];
  return base;
}

describe("background thread claims", () => {
  test("sharesTranscriptBody requires shared identity, not equal serialized content", () => {
    const model = hydrateThread(currentRead("local:identity"), "local:identity", 1);
    const history = model.history;
    if (!history) throw new Error("expected the fixture to carry a recorded history");
    const sameContentClone: ThreadModel = {
      ...model,
      history: { ...history, turns: [...history.turns] },
      overlay: { ...model.overlay },
    };
    // Identical serialized content, but independent turns/overlay containers.
    expect(currentTranscriptState(sameContentClone)).toEqual(currentTranscriptState(model));
    expect(sameContentClone).not.toBe(model);
    expect(sameContentClone.history?.turns).not.toBe(history.turns);
    expect(sameContentClone.overlay).not.toBe(model.overlay);
    // Only the same model, or a genuinely shared turns array and overlay, share.
    expect(sharesTranscriptBody(model, model)).toBe(true);
    expect(sharesTranscriptBody({ ...model, history }, model)).toBe(true);
    expect(sharesTranscriptBody(sameContentClone, model)).toBe(false);
  });

  test("a protocol-valid notification routes to the requested background alias", async () => {
    const fake = connectCurrentClient();
    const background = acquireBackgroundThread("local:one");
    await background.ready;
    const notification = {
      method: "overlay/delta",
      params: {
        threadId: "thr_local:one",
        ref: "local:one",
        key: "stream:s1:agentMessage",
        field: "text",
        delta: "-via-id",
      },
    } satisfies AnyNotification;
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

  test("retirement prevents a stale-cursor fallback read after its first response rejects", async () => {
    const { client, socket } = await connectRealClientWithSocket();
    const ref = "local:retired-fallback";
    const lease = acquireBackgroundThread(ref);
    try {
      putThreadModel(ref, hydrateThread(currentRead(ref), ref, Date.now()));
      await vi.waitFor(() => expect(threadReadRequests(socket)).toHaveLength(1));
      const first = threadReadRequests(socket)[0]!;
      const release = lease.release();
      socket.receive({
        id: first.id,
        error: {
          code: -32000,
          message: "stale cursor",
          data: { evenerErrorInfo: "transcriptItemCursorStale" },
        },
      });
      await release;
      await vi.waitFor(() => expect(backgroundTranscriptSnapshotForTests().networkHydrations).toBe(0), {
        timeout: 1_000,
      });
      expect(threadReadRequests(socket)).toHaveLength(1);
    } finally {
      for (const request of threadReadRequests(socket)) {
        socket.receive({ id: request.id, result: currentRead(request.ref) });
      }
      await lease.release();
      client.close();
    }
  });
  test("a released read waiting on shared membership cannot issue after that transition", async () => {
    const { client, socket } = await connectRealClientWithSocket();
    const ref = "local:membership-retire";
    const activity = acquireThreadSubscription(client, ref);
    activityLeases.push(activity);
    const membershipRead = activity.read({ includeTurns: false, itemsView: "full", itemLimit: 80 });
    const background = acquireBackgroundThread(ref);
    try {
      await vi.waitFor(() => expect(threadReadRequests(socket)).toHaveLength(1));
      await vi.waitFor(() => expect(backgroundTranscriptSnapshotForTests().networkHydrations).toBe(1));
      await background.release();
      const first = threadReadRequests(socket)[0]!;
      socket.receive({ id: first.id, result: currentRead(ref) });
      await membershipRead;
      await vi.waitFor(() => expect(backgroundTranscriptSnapshotForTests().networkHydrations).toBe(0));
      expect(threadReadRequests(socket)).toHaveLength(1);
    } finally {
      socket.closeFromServer(1000);
      await background.release();
      client.close();
    }
  });

  test("last-owner retirement releases the pending charge and detaches frames and base model", async () => {
    const { client, socket } = await connectRealClientWithSocket();
    const ref = "local:retired-buffer";
    const background = acquireBackgroundThread(ref);
    const retirements: Array<{
      ref: string;
      retainedNotificationCount: number;
      hadBaseModel: boolean;
      notificationCount: number;
      hasBaseModel: boolean;
    }> = [];
    let restoreObserver: (() => void) | undefined;
    try {
      await vi.waitFor(() => expect(threadReadRequests(socket)).toHaveLength(1));
      const initial = threadReadRequests(socket)[0]!;
      socket.receive({ id: initial.id, result: currentRead(ref) });
      await background.ready;

      restoreObserver = observePendingHydrationRetirementsForTests((retirement) => retirements.push(retirement));
      socket.receive({
        method: "evener/thread/resync",
        params: { ref, threadId: `thr_${ref}` },
      });
      await vi.waitFor(() => expect(threadReadRequests(socket)).toHaveLength(2));
      const retired = threadReadRequests(socket)[1]!;
      const update = textDelta(ref, "buffered while snapshot waits");
      socket.receive({ method: update.method, params: update.params });
      await vi.waitFor(() => expect(backgroundTranscriptSnapshotForTests().pendingBytes).toBeGreaterThan(0));

      // R54: held resync leaves retirement work unresolved, so driven and real
      // clocks reach the same repository stale deadline, no assertion measures time.
      // Ordinary release is bounded, unlike legacy immediate release. This is
      // the retirement race's deadline, not the issued RPC's client timeout.
      // Keep the real-timer resync held until its unchanged late response.
      vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
      try {
        const releasing = background.release();
        await vi.advanceTimersByTimeAsync(30_000);
        await releasing;
      } finally {
        vi.useRealTimers();
      }
      expect(backgroundTranscriptSnapshotForTests().pendingBytes).toBe(0);
      expect(retirements).toEqual([
        { ref, retainedNotificationCount: 1, hadBaseModel: true, notificationCount: 0, hasBaseModel: false },
      ]);

      expect(backgroundTranscriptSnapshotForTests().networkHydrations).toBe(1);
      socket.receive({ id: retired.id, result: currentRead(ref) });
      await vi.waitFor(() => expect(backgroundTranscriptSnapshotForTests().networkHydrations).toBe(0));
      expect(threadsStore.getState().threads.has(ref)).toBe(false);
    } finally {
      restoreObserver?.();
      for (const request of threadReadRequests(socket)) {
        socket.receive({ id: request.id, result: currentRead(request.ref) });
      }
      await background.release();
      client.close();
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
      // The guarantee: live deltas never serialize the whole retained
      // transcript. Before 6d the store constructed one sized update per delta
      // to feed the legacy residency; that construction is retired, so the
      // entry route may serialize each delta's own item (or nothing). What must
      // still hold is that no call reaches the whole-transcript size.
      expect(serializedSizes.every((size) => size < retainedStateBytes / 2)).toBe(true);
      expect(Math.max(0, ...serializedSizes)).toBeLessThan(retainedStateBytes / 2);
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
  test("resync control does not issue fallback during a held final write", async () => {
    const path = "resync control";
    let joiningRetirement = false;
    let executingEntryHistory = false;
    const readProducers: Array<{ ref: string; kind: "entry-history" | "hydration"; params: unknown }> = [];
    const restoreReadObservers: Array<() => void> = [];
    const acquireRealSubscription = appwireClient.acquireThreadSubscription;
    const subscriptionSpy = vi.spyOn(appwireClient, "acquireThreadSubscription").mockImplementation((...args) => {
      const subscription = acquireRealSubscription(...args);
      const read = subscription.read.bind(subscription);
      const observer = vi.spyOn(subscription, "read").mockImplementation((params) => {
        readProducers.push({ ref: args[1], kind: executingEntryHistory ? "entry-history" : "hydration", params });
        return read(params);
      });
      restoreReadObservers.push(() => observer.mockRestore());
      return subscription;
    });
    const barrierReached = deferred<void>();
    const createRealResidency = residencyModule.createTranscriptResidency;
    const factorySpy = vi.spyOn(residencyModule, "createTranscriptResidency").mockImplementation((...args) => {
      const residency = createRealResidency(...args);
      const schedule = residency.scheduleHydration.bind(residency);
      residency.scheduleHydration = (...request) => {
        // Identify the actual named ordinary history caller, then observe
        // its synchronous read producer inside the real scheduled callback.
        // A stale-cursor fallback resumes outside that callback and cannot
        // acquire the entry-history kind even with identical wire fields.
        const entryHistoryCaller = new Error().stack?.includes("runEntryHistoryRequest") === true;
        const run = request[2];
        const observedRun = run
          ? (signal: AbortSignal) => {
              const previous = executingEntryHistory;
              executingEntryHistory = entryHistoryCaller;
              try {
                return run(signal);
              } finally {
                executingEntryHistory = previous;
              }
            }
          : undefined;
        const result = schedule(request[0], request[1], observedRun);
        if (joiningRetirement && request[0] === "local:recovery-retirement" && request[2]) {
          if (!entryHistoryCaller) throw new Error("retirement did not join the actual ordinary history caller");
          barrierReached.resolve();
        }
        return result;
      };
      return residency;
    });
    const restoreOptions = installBackgroundTranscriptOptionsForTests({ pendingBytes: 1 });
    const { client, socket } = await connectRealClientWithSocket();
    const ref = "local:recovery-retirement";
    const lease = acquireBackgroundThread(ref);
    let restoreRetireObserver: (() => void) | undefined;
    let release: ReturnType<ReturnType<typeof acquireBackgroundThread>["release"]> | undefined;
    try {
      await cache.getClearEpoch();
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
      const repository = productionEntryTranscriptForTests(ref).repository;
      if (!repository) throw new Error("expected ordinary repository");
      const retire = repository.retire.bind(repository);
      const retireSpy = vi.spyOn(repository, "retire").mockImplementation(() => {
        joiningRetirement = true;
        try {
          return retire();
        } finally {
          joiningRetirement = false;
        }
      });
      restoreRetireObserver = () => retireSpy.mockRestore();
      const finalGeneration = (repository.getSnapshot()?.history?.issuedGeneration ?? 0) + 1;
      const receiveObserver = vi.spyOn(socket, "receive");
      release = lease.release();
      let watchdog: ReturnType<typeof setTimeout> | undefined;
      try {
        await Promise.race([
          barrierReached.promise,
          new Promise<never>((_, reject) => {
            watchdog = setTimeout(
              () => reject(new Error("R58 ordinary retire read did not join its held request within 1000ms")),
              1000,
            );
          }),
        ]);
      } finally {
        if (watchdog) clearTimeout(watchdog);
      }
      expect(backgroundTranscriptSnapshotForTests().networkHydrations).toBe(1);
      expect(threadReadRequests(socket)).toHaveLength(2);
      const recovery = threadReadRequests(socket)[1]!;
      socket.receive({
        id: recovery.id,
        error: {
          code: -32000,
          message: "stale cursor",
          data: { evenerErrorInfo: "transcriptItemCursorStale" },
        },
      });
      expect(receiveObserver).toHaveBeenCalledWith({
        id: recovery.id,
        error: { code: -32000, message: "stale cursor", data: { evenerErrorInfo: "transcriptItemCursorStale" } },
      });
      await vi.waitFor(() =>
        expect(
          backgroundTranscriptSnapshotForTests().networkHydrations === 0 || threadReadRequests(socket).length > 2,
        ).toBe(true),
      );
      expect(readProducers.filter((read) => read.ref === ref && read.kind === "hydration")).toHaveLength(2);
      const wireReads = socket.sent
        .map((frame) => JSON.parse(frame) as { id?: number; method?: string; params?: Record<string, unknown> })
        .filter((frame) => frame.method === "thread/read");
      expect(readProducers.filter((read) => read.ref === ref)).toStrictEqual([
        { ref, kind: "hydration", params: { includeTurns: true, itemsView: "full", itemLimit: 40 } },
        {
          ref,
          kind: "hydration",
          params: {
            includeTurns: true,
            itemsView: "full",
            itemLimit: 40,
            requestGeneration: 2,
            heldSnapshot: { incarnation: "inc-1", length: 40 },
          },
        },
        {
          ref,
          kind: "entry-history",
          params: { includeTurns: true, itemsView: "full", itemLimit: 40, requestGeneration: finalGeneration },
        },
      ]);
      expect(wireReads).toStrictEqual([
        {
          id: threadReadRequests(socket)[0]!.id,
          method: "thread/read",
          params: {
            includeTurns: true,
            itemsView: "full",
            itemLimit: 40,
            ref,
            subscribe: true,
            replaceSubscription: false,
          },
        },
        {
          id: recovery.id,
          method: "thread/read",
          params: {
            includeTurns: true,
            itemsView: "full",
            itemLimit: 40,
            requestGeneration: 2,
            heldSnapshot: { incarnation: "inc-1", length: 40 },
            ref,
            subscribe: false,
            replaceSubscription: false,
          },
        },
        {
          id: threadReadRequests(socket)[2]?.id,
          method: "thread/read",
          params: {
            includeTurns: true,
            itemsView: "full",
            itemLimit: 40,
            requestGeneration: finalGeneration,
            ref,
            subscribe: false,
            replaceSubscription: false,
          },
        },
      ]);
    } finally {
      restoreRetireObserver?.();
      for (const restore of restoreReadObservers) restore();
      subscriptionSpy.mockRestore();
      factorySpy.mockRestore();
      client.close();
      await (release ?? lease.release());
      resetThreadsStoreForTests();
      restoreOptions();
    }
  });
  test("round-trips multiple older pages, a nonresident update and further paging through disk eviction and reopen", async () => {
    const restoreOptions = installBackgroundTranscriptOptionsForTests({ residentBytes: 0, pendingBytes: 64 * 1024 });
    resetThreadsStoreForTests();
    setProductionEntryCacheForTests(cache);
    const fake = new FakeClient("ready");
    fake.on("thread/read", ({ ref }) => positionedLatestRead(ref ?? "local:one"));
    fake.on("thread/unsubscribe", () => ({}));
    const listCursors: string[] = [];
    fake.on("thread/turns/list", ({ cursor }) => {
      listCursors.push(cursor ?? "");
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
    const historyShape = (model: ThreadModel | undefined) =>
      (model?.history?.turns ?? []).map((turn) => ({ id: turn.id, texts: turn.items.map((item) => item.text) }));
    const loadedShape = [
      { id: "turn_page2", texts: ["page-two-item", "unique-later-item🙂"] },
      { id: "turn_page1", texts: ["page-one-item"] },
      { id: "turn_latest", texts: ["latest-recorded-item"] },
    ];
    try {
      await background.ready;
      await settleEntryTranscriptForTests("local:one");
      expect(threadsStore.getState().threads.has("local:one")).toBe(true);
      const projection = threadsStore.getState().threads.get("local:one");
      const repository = productionEntryTranscriptForTests("local:one").repository;
      if (!projection || !repository) throw new Error("expected ordinary pruned background owner");
      const recordedIdentities = [
        {
          id: "turn_latest-item-0",
          turnId: "turn_latest",
          type: "agentMessage",
          text: "",
          transcriptKey: "apptranscript-item-v2:turn_latest:0:0",
          position: { entry: 2, item: 0 },
          version: 1,
        },
      ];
      expect(projection.history?.turns.flatMap((turn) => turn.items)).toStrictEqual(recordedIdentities);
      expect(JSON.stringify(projection)).not.toContain("latest-recorded-item");
      expect(repository.getSnapshot()?.history?.turns.flatMap((turn) => turn.items)).toStrictEqual(recordedIdentities);
      expect(JSON.stringify(repository.getSnapshot())).not.toContain("latest-recorded-item");
      expect(repository.retainedInventoryForTesting()).toStrictEqual({
        pendingRows: [],
        inFlightBodies: [],
        retryBodies: [],
        backgroundAcceptBodies: [],
        handoffOverlayBodies: [],
      });
      expect(repository.getRetentionSnapshotForTesting()).toEqual({ bindingHasSeedModel: false });

      const pane = threadsStore.getState().ensureThread("local:one");
      paneHeld = true;
      await pane;
      await threadsStore.getState().loadOlderTurns("local:one");
      await threadsStore.getState().loadOlderTurns("local:one");
      const loaded = threadsStore.getState().threads.get("local:one");
      if (!loaded) throw new Error("expected the paged model");
      // The strictly descending (entry, item) boundaries place both pages, in
      // order, ahead of the positioned recorded latest window.
      expect(historyShape(loaded)).toEqual(loadedShape);
      expect(loaded.turns.flatMap((turn) => turn.items).map((item) => item.text)).toContain("unique-later-item🙂");
      expect(loaded.olderCursor).toBe("cursor_p2");
      const readsBeforeEviction = fake.calls.filter((call) => call.method === "thread/read").length;

      // Close the last pane while background demand remains; force disk eviction.
      threadsStore.getState().releaseThread("local:one");
      paneHeld = false;
      await settleEntryTranscriptForTests("local:one");
      // R59: releasing the pane retains a pruned background projection,
      // not the legacy disk-only whole-model absence.
      const releasedProjection = threadsStore.getState().threads.get("local:one");
      if (!releasedProjection) throw new Error("expected the released pruned projection");
      expect(
        releasedProjection.history?.turns
          .flatMap((turn) => turn.items)
          .map(({ id, text, position }) => ({ id, text, position })),
      ).toStrictEqual([
        { id: "turn_page2-item-0", text: "", position: { entry: 1, item: 0 } },
        { id: "turn_page2-item-1", text: "", position: { entry: 1, item: 1 } },
        { id: "turn_page1-item-0", text: "", position: { entry: 1, item: 2 } },
        { id: "turn_latest-item-0", text: "", position: { entry: 2, item: 0 } },
      ]);
      expect(JSON.stringify(releasedProjection)).not.toContain("unique-later-item🙂");
      expect(JSON.stringify(releasedProjection)).not.toContain("page-one-item");
      expect(JSON.stringify(releasedProjection)).not.toContain("latest-recorded-item");

      // Deliver a live update while the model is nonresident.
      fake.emitNotification(textDelta("local:one", "-live-while-nonresident"));
      await settleEntryTranscriptForTests("local:one");

      // Reopen locally: promotion from disk, no additional thread/read.
      const reopen = threadsStore.getState().ensureThread("local:one");
      paneHeld = true;
      await reopen;
      expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(readsBeforeEviction);
      const reopened = threadsStore.getState().threads.get("local:one");
      if (!reopened) throw new Error("expected the reopened model");
      expect(historyShape(reopened)).toEqual(loadedShape);
      const reopenedText = reopened.turns.flatMap((turn) => turn.items).map((item) => item.text);
      expect(reopenedText).toContain("unique-later-item🙂");
      expect(reopenedText.join("")).toContain("alpha-live-while-nonresident");
      expect(reopened.olderCursor).toBe("cursor_p2");

      // Page further with the preserved cursor and reach below the prior oldest boundary.
      await threadsStore.getState().loadOlderTurns("local:one");
      expect(listCursors).toEqual(["cursor_latest", "cursor_p1", "cursor_p2"]);
      const after = threadsStore.getState().threads.get("local:one");
      expect(historyShape(after)).toEqual([{ id: "turn_page3", texts: ["beyond-oldest-item"] }, ...loadedShape]);
    } finally {
      if (paneHeld) threadsStore.getState().releaseThread("local:one");
      try {
        await background.release();
      } finally {
        resetThreadsStoreForTests();
        restoreOptions();
      }
    }
  });
});

describe("background thread claims", () => {
  test("last pane release detaches its pending frames and base model", async () => {
    const { client, socket } = await connectRealClientWithSocket();
    const ref = "local:retired-pane-buffer";
    const retirements: Array<{
      ref: string;
      retainedNotificationCount: number;
      hadBaseModel: boolean;
      notificationCount: number;
      hasBaseModel: boolean;
    }> = [];
    let restoreObserver: (() => void) | undefined;
    try {
      const initial = threadsStore.getState().ensureThread(ref);
      await vi.waitFor(() => expect(threadReadRequests(socket)).toHaveLength(1));
      const first = threadReadRequests(socket)[0]!;
      socket.receive({ id: first.id, result: currentRead(ref) });
      await initial;

      restoreObserver = observePendingHydrationRetirementsForTests((retirement) => retirements.push(retirement));
      socket.receive({
        method: "evener/thread/resync",
        params: { ref, threadId: `thr_${ref}` },
      });
      await vi.waitFor(() => expect(threadReadRequests(socket)).toHaveLength(2));
      const retired = threadReadRequests(socket)[1]!;
      const update = textDelta(ref, "buffered while pane snapshot waits");
      socket.receive({ method: update.method, params: update.params });

      expect(pendingThreadHydrationUpdatesForTests()).toHaveLength(1);
      expect(pendingThreadHydrationBaseModelsForTests()).toHaveLength(1);
      const repository = productionEntryTranscriptForTests(ref)?.repository;
      if (!repository) throw new Error("expected the ordinary pane repository");
      const retire = vi.spyOn(repository, "retire");
      threadsStore.getState().releaseThread(ref);
      expect(retirements).toEqual([]);
      expect(pendingThreadHydrationUpdatesForTests()).toHaveLength(1);
      expect(pendingThreadHydrationBaseModelsForTests()).toHaveLength(1);
      expect(retire).toHaveBeenCalledTimes(1);
      socket.receive({ id: retired.id, result: currentRead(ref) });
      await vi.waitFor(() => expect(threadReadRequests(socket)).toHaveLength(3));
      const fresh = threadReadRequests(socket)[2]!;
      const freshWire = socket.sent.map((frame) => JSON.parse(frame)).find((frame) => frame.id === fresh.id);
      socket.receive({
        id: fresh.id,
        result: { ...currentRead(ref), requestGeneration: freshWire.params.requestGeneration },
      });
      await vi.waitFor(() => expect(backgroundTranscriptSnapshotForTests().networkHydrations).toBe(0));
      await retire.mock.results[0]!.value;
      expect(pendingThreadHydrationUpdatesForTests()).toHaveLength(0);
      expect(pendingThreadHydrationBaseModelsForTests()).toHaveLength(0);
      expect(threadsStore.getState().threads.has(ref)).toBe(false);
    } finally {
      restoreObserver?.();
      for (const request of threadReadRequests(socket)) {
        socket.receive({ id: request.id, result: currentRead(request.ref) });
      }
      threadsStore.getState().releaseThread(ref);
      client.close();
    }
  });
});
