import "fake-indexeddb/auto";
import {
  type AnyNotification,
  AppwireClient,
  acquireThreadSubscription,
  currentTranscriptState,
  hydrateThread,
  type ThreadModel,
  type ThreadTurnsListResponse,
} from "@evener/appwire-client";
import { currentRead, textDelta } from "@evener/appwire-client/testing/currentTranscript";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { FakeSocket } from "@evener/appwire-client/testing/fakeSocket";
import { IDBFactory, IDBKeyRange } from "fake-indexeddb";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { connectionStore } from "./connection";
import { SessionEntryCache } from "./sessionEntryCache";
import {
  acquireBackgroundThread,
  backgroundTranscriptSnapshotForTests,
  observePendingHydrationRetirementsForTests,
  putThreadModel,
  resetThreadsStoreForTests,
  setProductionEntryCacheForTests,
  threadsStore,
} from "./threads";

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
});
