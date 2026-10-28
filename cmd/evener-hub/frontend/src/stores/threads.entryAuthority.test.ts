import type { AnyNotification, ThreadItem, ThreadReadResponse } from "@evener/appwire-client";
import { currentRead } from "@evener/appwire-client/testing/currentTranscript";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { IDBFactory, IDBKeyRange } from "fake-indexeddb";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { DEFAULT_BACKGROUND_TRANSCRIPT_OPTIONS } from "./backgroundTranscriptOptions";
import { SESSION_CACHE_DATABASE_NAME } from "./sessionCacheSchema";
import { SessionEntryCache } from "./sessionEntryCache";

const ref = "local:identity-regression";
const tabs: Array<{ threads: typeof import("./threads"); connection: typeof import("./connection") }> = [];
const caches: SessionEntryCache[] = [];
let factory: IDBFactory;

beforeEach(() => {
  factory = new IDBFactory();
  vi.stubGlobal("indexedDB", factory);
  vi.stubGlobal("IDBKeyRange", IDBKeyRange);
});
afterEach(() => {
  for (const tab of tabs.splice(0)) {
    tab.threads.resetThreadsStoreForTests();
    tab.connection.connectionStore.setState({ client: null, state: "idle" });
  }
  for (const cache of caches.splice(0)) cache.close();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  vi.resetModules();
});

function item(entry: number): ThreadItem {
  return {
    id: `row-${entry}`,
    transcriptKey: `row-${entry}`,
    turnId: "turn_1",
    type: "agentMessage",
    text: `body-${entry}`,
    position: { entry, item: 0 },
    version: entry,
  };
}

function read(length = 1): ThreadReadResponse {
  const response = currentRead(ref);
  return {
    ...response,
    olderCursor: undefined,
    overlay: [],
    snapshot: { incarnation: "inc-1", length },
    thread: {
      ...response.thread,
      turns: [{ id: "turn_1", status: "completed", itemsView: "full", version: length, items: [item(length)] }],
    },
  };
}

function suffix(entry: number): AnyNotification {
  return {
    method: "history/updated",
    params: {
      ref,
      threadId: read().thread.id,
      bootGeneration: "1",
      epoch: 1,
      snapshot: { incarnation: "inc-1", length: entry },
      items: [item(entry)],
    },
  };
}

async function openTab(hydrate = true) {
  // Separate module graphs model separate browser lifetimes, not the isolated
  // fixture route. Both ordinary stores use the same IndexedDB factory/name.
  vi.resetModules();
  const threads = await import("./threads");
  const connection = await import("./connection");
  const { setCacheChannelFactoryForTests } = await import("./cacheLifetime");
  threads.resetThreadsStoreForTests();
  setCacheChannelFactoryForTests(() => null);
  const cache = new SessionEntryCache({
    databaseName: SESSION_CACHE_DATABASE_NAME,
    indexedDB: factory,
    keyRange: IDBKeyRange,
  });
  caches.push(cache);
  const commits = vi.spyOn(cache, "commitBatch");
  threads.setProductionEntryCacheForTests(cache);
  const client = new FakeClient("ready");
  client.on("thread/read", (params) => ({ ...read(), requestGeneration: params.requestGeneration }));
  client.on("thread/unsubscribe", () => ({}));
  connection.connectionStore.getState().connect(client);
  const tab = { threads, connection, client, cache, commits };
  tabs.push(tab);
  if (hydrate) {
    await threads.threadsStore.getState().ensureThread(ref);
    await threads.settleEntryTranscriptForTests(ref);
  }
  return tab;
}

it("replaces history invalidated by a newer epoch after the response cut", async () => {
  const tab = await openTab(false);
  const authority = deferred<number>();
  const entered = deferred<void>();
  vi.spyOn(tab.cache, "getHistoryGeneration").mockImplementationOnce(() => {
    entered.resolve();
    return authority.promise;
  });
  let reads = 0;
  tab.client.on("thread/read", (params) => ({
    ...read(++reads === 1 ? 1 : 2),
    epoch: reads === 1 ? 1 : 2,
    requestGeneration: params.requestGeneration,
  }));
  const opening = tab.threads.threadsStore.getState().ensureThread(ref);
  await entered.promise;
  try {
    expect(tab.threads.productionEntryTranscriptForTests(ref).repository).toBeUndefined();
    const update = suffix(2);
    if (update.method !== "history/updated") throw new Error("expected recorded update");
    update.params.epoch = 2;
    tab.client.emitNotification(update);
    authority.resolve(0);
    await opening;
    await vi.waitFor(() => expect(reads).toBe(2));
    await tab.threads.settleEntryTranscriptForTests(ref);
    const repository = tab.threads.productionEntryTranscriptForTests(ref).repository!;
    expect(repository.getSnapshot()?.history?.invalidatedAtGeneration).toBeUndefined();
    expect(repository.getSnapshot()?.history?.epoch).toBe(2);
    const subsequent = suffix(3);
    if (subsequent.method !== "history/updated") throw new Error("expected recorded update");
    subsequent.params.epoch = 2;
    tab.client.emitNotification(subsequent);
    expect(
      repository
        .getSnapshot()
        ?.history?.turns.flatMap((turn) => turn.items)
        .map((row) => row.text),
    ).toContain("body-3");
    expect(tab.threads.threadsStore.getState().threads.get(ref)?.history?.epoch).toBe(2);
  } finally {
    authority.resolve(0);
    await opening;
  }
});

it("recovers an overlay notice after content Clear without a recorded update", async () => {
  const tab = await openTab();
  const fresh = read(0);
  fresh.thread.id = "cleared-thread";
  fresh.thread.evener.instanceId = "cleared-thread";
  fresh.thread.turns = [];
  fresh.snapshot = { incarnation: "cleared-incarnation", length: 0 };
  tab.client.on("thread/clear", (params) => ({
    thread: fresh.thread,
    ref,
    receipt: {
      clientMutationId: params.clientMutationId,
      disposition: "applied",
      threadId: fresh.thread.id,
      instanceId: fresh.thread.id,
      projectionState: "reflected",
    },
  }));
  await tab.threads.threadsStore.getState().clearThread(ref);
  await (await import("./projectionWork")).settleProjectionWorkForTests();
  expect(tab.threads.productionEntryTranscriptForTests(ref).repository).toBeUndefined();
  expect(tab.threads.threadsStore.getState().threads.get(ref)?.history).toBeUndefined();
  const reads = tab.client.calls.filter((call) => call.method === "thread/read").length;
  tab.client.on("thread/read", (params) => ({ ...fresh, requestGeneration: params.requestGeneration }));
  const notice = {
    key: "notice:after-clear",
    kind: "notice" as const,
    item: { id: "after-clear", type: "agentMessage" as const, text: "unrecorded error after Clear" },
  };
  tab.client.emitNotification({
    method: "overlay/upserted",
    params: { ref, threadId: fresh.thread.id, item: notice },
  });
  await vi.waitFor(() =>
    expect(tab.client.calls.filter((call) => call.method === "thread/read")).toHaveLength(reads + 1),
  );
  await tab.threads.settleEntryTranscriptForTests(ref);
  const repository = tab.threads.productionEntryTranscriptForTests(ref).repository!;
  expect(repository.getSnapshot()?.overlay?.[notice.key]?.item.text).toBe(notice.item.text);
  expect(tab.threads.threadsStore.getState().threads.get(ref)?.overlay?.[notice.key]?.item.text).toBe(notice.item.text);
});

it("keeps replayed overlay deltas exactly once when live history preserves the read snapshot length", async () => {
  const tab = await openTab();
  const slot = {
    key: "stream:live:agentMessage",
    kind: "stream" as const,
    streamId: "live",
    roundId: "round_live",
    turnId: "turn_live",
    item: { id: "item_live", turnId: "turn_live", type: "agentMessage" as const, text: "start" },
  };
  tab.client.emitNotification({ method: "overlay/upserted", params: { ref, threadId: read().thread.id, item: slot } });
  const authority = deferred<number>();
  const entered = deferred<void>();
  vi.spyOn(tab.cache, "getHistoryGeneration").mockImplementationOnce(() => {
    entered.resolve();
    return authority.promise;
  });
  const response = read();
  response.overlay = [slot];
  tab.client.on("thread/read", (params) => ({ ...response, requestGeneration: params.requestGeneration }));
  const hydrations = tab.threads.threadsStore.getState().hydrations.get(ref)!;
  tab.client.emitNotification({ method: "evener/thread/resync", params: { ref, threadId: response.thread.id } });
  await entered.promise;
  tab.client.emitNotification({
    method: "overlay/delta",
    params: { ref, threadId: response.thread.id, key: slot.key, field: "text", delta: " tail" },
  });
  tab.client.emitNotification(suffix(2));
  const repository = tab.threads.productionEntryTranscriptForTests(ref).repository!;
  expect(repository.getSnapshot()?.overlay?.[slot.key]?.item.text).toBe("start tail");
  // Live recorded input merges bodies but does not advance the read cursor's
  // snapshot length. This is an invariant, not a rejected-read regression.
  expect(repository.getSnapshot()?.history?.length).toBe(1);
  expect(
    repository
      .getSnapshot()
      ?.history?.turns.flatMap((turn) => turn.items)
      .some((item) => item.text === "body-2"),
  ).toBe(true);
  const frames = tab.threads.threadsStore.getState().frameTimes.get(ref)?.length;
  authority.resolve(0);
  await vi.waitFor(() => expect(tab.threads.threadsStore.getState().hydrations.get(ref)).toBe(hydrations + 1));
  expect(tab.threads.productionEntryTranscriptForTests(ref).repository).toBe(repository);
  expect(repository.getSnapshot()?.overlay?.[slot.key]?.item.text).toBe("start tail");
  expect(tab.threads.threadsStore.getState().threads.get(ref)?.overlay?.[slot.key]?.item.text).toBe("start tail");
  expect(tab.threads.threadsStore.getState().frameTimes.get(ref)?.length).toBe(frames);
});

it("does not replay an accepted delta after an end read advances history during hydration authorities", async () => {
  const tab = await openTab();
  const slot = {
    key: "stream:live:agentMessage",
    kind: "stream" as const,
    streamId: "live",
    roundId: "round_live",
    turnId: "turn_live",
    item: { id: "item_live", turnId: "turn_live", type: "agentMessage" as const, text: "start" },
  };
  const ending = {
    ...slot,
    key: "stream:ending:agentMessage",
    streamId: "ending",
    roundId: "round_ending",
    turnId: "turn_ending",
    item: { ...slot.item, id: "item_ending", turnId: "turn_ending", text: "ending" },
  };
  for (const item of [slot, ending]) {
    tab.client.emitNotification({ method: "overlay/upserted", params: { ref, threadId: read().thread.id, item } });
  }
  const authority = deferred<number>();
  const entered = deferred<void>();
  vi.spyOn(tab.cache, "getHistoryGeneration").mockImplementationOnce(() => {
    entered.resolve();
    return authority.promise;
  });
  let reads = 0;
  tab.client.on("thread/read", (params) => {
    const response = read(++reads === 1 ? 1 : 2);
    response.overlay = reads === 1 ? [slot, ending] : [{ ...slot, item: { ...slot.item, text: "start tail" } }];
    return { ...response, requestGeneration: params.requestGeneration };
  });
  const repository = tab.threads.productionEntryTranscriptForTests(ref).repository!;
  const accepts = vi.spyOn(repository, "acceptRead");
  const hydrations = tab.threads.threadsStore.getState().hydrations.get(ref)!;
  tab.client.emitNotification({ method: "evener/thread/resync", params: { ref, threadId: read().thread.id } });
  await entered.promise;
  try {
    tab.client.emitNotification({
      method: "overlay/delta",
      params: { ref, threadId: read().thread.id, key: slot.key, field: "text", delta: " tail" },
    });
    tab.client.emitNotification({
      method: "overlay/end",
      params: { ref, threadId: read().thread.id, roundId: ending.roundId },
    });
    await vi.waitFor(() => expect(repository.getSnapshot()?.history?.length).toBe(2));
    expect(reads).toBe(2);
    expect(accepts.mock.results.map((result) => result.value)).toEqual([true]);
    expect(repository.getSnapshot()?.overlay?.[slot.key]?.item.text).toBe("start tail");
    const frames = tab.threads.threadsStore.getState().frameTimes.get(ref)?.length;
    authority.resolve(0);
    await vi.waitFor(() => expect(tab.threads.threadsStore.getState().hydrations.get(ref)).toBe(hydrations + 1));
    expect(accepts.mock.results.map((result) => result.value)).toEqual([true, false]);
    expect(tab.threads.productionEntryTranscriptForTests(ref).repository).toBe(repository);
    expect(repository.getSnapshot()?.overlay?.[slot.key]?.item.text).toBe("start tail");
    expect(tab.threads.threadsStore.getState().threads.get(ref)?.overlay?.[slot.key]?.item.text).toBe("start tail");
    expect(tab.threads.threadsStore.getState().frameTimes.get(ref)?.length).toBe(frames);
  } finally {
    authority.resolve(0);
  }
});

it("unsubscribes after background release and pane close before initial hydration settles", async () => {
  const tab = await openTab(false);
  const held = deferred<number>();
  const entered = deferred<void>();
  // Wire acquisition must finish before unsubscribe can run. Hold the initial
  // hydration at its authority lookup, still before repository creation.
  vi.spyOn(tab.cache, "getHistoryGeneration").mockImplementationOnce(() => {
    entered.resolve();
    return held.promise;
  });
  const pane = tab.threads.threadsStore.getState().ensureThread(ref);
  await entered.promise;
  const background = tab.threads.acquireBackgroundThread(ref);
  try {
    expect(tab.threads.productionEntryTranscriptForTests(ref).repository).toBeUndefined();
    await background.release();
    expect(tab.client.calls.filter((call) => call.method === "thread/unsubscribe")).toHaveLength(0);
    tab.threads.threadsStore.getState().releaseThread(ref);
    expect(tab.client.calls.filter((call) => call.method === "thread/unsubscribe")).toHaveLength(1);
  } finally {
    held.resolve(0);
    await pane;
    await background.ready;
  }
});

it("reacquisition cancels real-route retirement without leaving a phantom owner or clearing a newer release", async () => {
  const tab = await openTab();
  const repository = tab.threads.productionEntryTranscriptForTests(ref).repository!;
  const cancel = vi.spyOn(repository, "cancelRetirement");
  const held = [deferred<ThreadReadResponse>(), deferred<ThreadReadResponse>()];
  const entered = [deferred<void>(), deferred<void>()];
  let reads = 0;
  tab.client.on("thread/read", async (params) => {
    const index = reads++;
    entered[index]!.resolve();
    return { ...(await held[index]!.promise), requestGeneration: params.requestGeneration };
  });
  const firstRelease = tab.threads.threadsStore.getState().releaseThreadAndWait(ref);
  await entered[0]!.promise;
  try {
    const replacement = tab.threads.acquireBackgroundThread(ref);
    await replacement.ready;
    expect(cancel).toHaveBeenCalledTimes(1);
    expect(tab.threads.productionEntryTranscriptForTests(ref).repository).toBe(repository);
    const secondRelease = replacement.release();
    held[0]!.resolve(read());
    expect(await firstRelease).toEqual({ kind: "cancelled" });
    await entered[1]!.promise;
    expect(tab.client.calls.filter((call) => call.method === "thread/unsubscribe")).toHaveLength(0);
    held[1]!.resolve(read());
    await secondRelease;
    expect(tab.client.calls.filter((call) => call.method === "thread/unsubscribe")).toHaveLength(1);
    expect(tab.threads.productionEntryTranscriptForTests(ref).repository).toBeUndefined();
  } finally {
    for (const gate of held) gate.resolve(read());
  }
});

it("replaces an overflowed background hydration when its retry fires before the held response", async () => {
  const tab = await openTab(false);
  const held = deferred<ThreadReadResponse>();
  const retries: Array<() => void> = [];
  const restoreScheduler = tab.threads.installHydrationRetrySchedulerForTests((_attempt, retry) => {
    retries.push(retry);
    return () => {};
  });
  let reads = 0;
  let active = 0;
  let maximum = 0;
  tab.client.on("thread/read", async (params) => {
    maximum = Math.max(maximum, ++active);
    try {
      return { ...(++reads === 1 ? await held.promise : read(6)), requestGeneration: params.requestGeneration };
    } finally {
      active -= 1;
    }
  });
  const background = tab.threads.acquireBackgroundThread(ref);
  try {
    await vi.waitFor(() => expect(reads).toBe(1));
    expect(tab.threads.productionEntryTranscriptForTests(ref).repository).toBeUndefined();
    for (let entry = 1; entry <= 5; entry += 1) {
      const update = suffix(entry);
      if (update.method === "history/updated") update.params.items![0]!.text = "x".repeat(1024 * 1024);
      tab.client.emitNotification(update);
    }
    expect(tab.threads.pendingThreadHydrationUpdatesForTests()).toHaveLength(0);
    expect(retries).toHaveLength(1);
    expect(tab.threads.backgroundTranscriptSnapshotForTests().networkHydrations).toBe(1);
    retries[0]!();
    // Retry demand arrives while the retired response still owns its slot.
    expect(reads).toBe(1);
    held.resolve(read());
    await background.ready;
    await vi.waitFor(() => expect(reads).toBe(2));
    await tab.threads.settleEntryTranscriptForTests(ref);
    expect(tab.threads.threadsStore.getState().threads.has(ref)).toBe(true);
    const historyKey = tab.threads.productionEntryTranscriptForTests(ref).historyKey!;
    expect((await tab.cache.getLatest<{ text?: string }>(historyKey, 10)).map((row) => row.item.text)).toEqual([
      "body-6",
    ]);
    expect(retries).toHaveLength(1);
    expect(maximum).toBe(1);
    expect(maximum).toBeLessThanOrEqual(DEFAULT_BACKGROUND_TRANSCRIPT_OPTIONS.networkHydrations);
    // Opening uses the replacement's local bodies, with no extra activation read.
    await tab.threads.threadsStore.getState().ensureThread(ref);
    const model = tab.threads.threadsStore.getState().threads.get(ref);
    expect(model?.turns.flatMap((turn) => turn.items.map((entry) => entry.text))).toEqual(["body-6"]);
    expect(reads).toBe(2);
  } finally {
    held.resolve(read());
    restoreScheduler();
    await background.release();
  }
});

it("same-client reconnect replaces hydrations held at cache authorities and retires outgoing queued work", async () => {
  const tab = await openTab(false);
  const refs = [ref, "local:reconnect-two", "local:reconnect-queued"];
  const authorities = [deferred<number>(), deferred<number>()];
  const entered = deferred<void>();
  const getGeneration = tab.cache.getHistoryGeneration.bind(tab.cache);
  let authorityReads = 0;
  vi.spyOn(tab.cache, "getHistoryGeneration").mockImplementation((key) => {
    const gate = authorities[authorityReads++];
    if (authorityReads === 2) entered.resolve();
    return gate ? gate.promise : getGeneration(key);
  });
  const reads: string[] = [];
  let active = 0;
  let maximum = 0;
  tab.client.on("thread/read", async (params) => {
    const target = params.ref!;
    reads.push(target);
    maximum = Math.max(maximum, ++active);
    try {
      await Promise.resolve();
      const response = currentRead(target);
      return {
        ...response,
        olderCursor: undefined,
        overlay: [],
        snapshot: { incarnation: "inc-1", length: 6 },
        thread: { ...response.thread, turns: read(6).thread.turns },
        requestGeneration: params.requestGeneration,
      };
    } finally {
      active -= 1;
    }
  });
  const backgrounds = refs.map((target) => tab.threads.acquireBackgroundThread(target));
  try {
    await entered.promise;
    expect(reads).toEqual(refs.slice(0, 2));
    expect(tab.threads.backgroundTranscriptSnapshotForTests().networkHydrations).toBe(2);
    expect(tab.threads.backgroundTranscriptSnapshotForTests().queuedHydrationRefs).toEqual([refs[2]]);
    // Deliberately no disconnected residency transition, only this client's onReady.
    tab.client.emitStateChange("reconnecting");
    tab.client.emitReady();
    expect(tab.threads.backgroundTranscriptSnapshotForTests().networkHydrations).toBe(2);
    expect(reads).toEqual(refs.slice(0, 2));
    for (const authority of authorities) authority.resolve(0);
    await vi.waitFor(() => {
      for (const target of refs) expect(tab.threads.threadsStore.getState().threads.has(target)).toBe(true);
    });
    expect(reads).toEqual([...refs.slice(0, 2), ...refs]);
    expect(maximum).toBeLessThanOrEqual(DEFAULT_BACKGROUND_TRANSCRIPT_OPTIONS.networkHydrations);
    for (const target of refs) {
      await tab.threads.settleEntryTranscriptForTests(target);
      const historyKey = tab.threads.productionEntryTranscriptForTests(target).historyKey!;
      expect((await tab.cache.getLatest<{ text?: string }>(historyKey, 10)).map((row) => row.item.text)).toEqual([
        "body-6",
      ]);
    }
  } finally {
    for (const authority of authorities) authority.resolve(0);
    await Promise.all(backgrounds.map((background) => background.release()));
  }
});

it.each([false, true])(
  "namespaces read and suffix identities across production lifetimes, overlap=%s",
  async (overlap) => {
    const first = await openTab();
    const firstRoute = first.threads.productionEntryTranscriptForTests(ref);
    const historyKey = firstRoute.historyKey!;
    first.client.emitNotification(suffix(2));
    await first.threads.settleEntryTranscriptForTests(ref);
    const firstRead = first.commits.mock.calls.map(([batch]) => batch.coverage).find((row) => row?.kind === "read")!;
    const firstSuffix = (await first.cache.getCoverage(historyKey, `suffix:${firstRead.readId}`))!;
    expect(firstSuffix.representedSnapshot).toBe(2);
    if (!overlap) {
      first.threads.resetThreadsStoreForTests();
      first.connection.connectionStore.setState({ client: null, state: "idle" });
    }

    const second = await openTab();
    expect(second.threads.productionEntryTranscriptForTests(ref).historyKey).toBe(historyKey);
    second.client.emitNotification(suffix(3));
    await second.threads.settleEntryTranscriptForTests(ref);
    const secondRead = second.commits.mock.calls.map(([batch]) => batch.coverage).find((row) => row?.kind === "read")!;
    expect(secondRead.readId).not.toBe(firstRead.readId);
    expect(secondRead.boundary).not.toBe(firstRead.boundary);
    const secondSuffix = await second.cache.getCoverage(historyKey, `suffix:${secondRead.readId}`);
    expect((await second.cache.getCoverage(historyKey, `read:${secondRead.readId}`))?.suffixBlocked).not.toBe(true);
    expect(secondSuffix?.representedSnapshot).toBe(3);
    expect(secondSuffix?.continuityId).not.toBe(firstSuffix.continuityId);
    if (overlap) {
      first.client.emitNotification(suffix(4));
      await first.threads.settleEntryTranscriptForTests(ref);
      expect((await first.cache.getCoverage(historyKey, `suffix:${firstRead.readId}`))?.representedSnapshot).toBe(4);
      expect((await first.cache.getCoverage(historyKey, `read:${firstRead.readId}`))?.suffixBlocked).not.toBe(true);
    }
  },
);

it("rejects a pre-deletion hydration and repopulates only from a subsequent source read", async () => {
  const tab = await openTab();
  const historyKey = tab.threads.productionEntryTranscriptForTests(ref).historyKey!;
  expect((await tab.cache.getLatest(historyKey, 10)).map((row) => row.transcriptKey)).toEqual(["row-1"]);
  const reply = deferred<ThreadReadResponse>();
  const entered = deferred<void>();
  tab.client.on("thread/read", async (params) => {
    entered.resolve();
    return { ...(await reply.promise), requestGeneration: params.requestGeneration };
  });
  tab.client.emitNotification({ method: "evener/thread/resync", params: { ref, threadId: read().thread.id } });
  await entered.promise;
  const sibling = new SessionEntryCache({
    databaseName: SESSION_CACHE_DATABASE_NAME,
    indexedDB: factory,
    keyRange: IDBKeyRange,
  });
  caches.push(sibling);
  expect(await sibling.deleteHistory(historyKey)).toBe(true);
  expect(await sibling.getHistoryGeneration(historyKey)).toBe(1);
  expect(await tab.cache.getLatest(historyKey, 10)).toEqual([]);
  tab.commits.mockClear();

  reply.resolve(read(2));
  await tab.threads.settleEntryTranscriptForTests(ref);
  expect(await sibling.getLatest(historyKey, 10)).toEqual([]);
  expect(tab.commits).not.toHaveBeenCalled();

  const readsBefore = tab.client.calls.filter((call) => call.method === "thread/read").length;
  tab.client.on("thread/read", (params) => ({ ...read(3), requestGeneration: params.requestGeneration }));
  tab.client.emitNotification({ method: "evener/thread/resync", params: { ref, threadId: read().thread.id } });
  await tab.threads.settleEntryTranscriptForTests(ref);
  expect(tab.client.calls.filter((call) => call.method === "thread/read")).toHaveLength(readsBefore + 1);
  expect((await sibling.getLatest(historyKey, 10)).map((row) => row.transcriptKey)).toEqual(["row-3"]);
  expect(tab.commits.mock.calls.every(([batch]) => batch.historyGeneration === 1)).toBe(true);
});
