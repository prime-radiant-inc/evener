import type { AnyNotification, ThreadItem, ThreadReadResponse } from "@evener/appwire-client";
import { currentRead } from "@evener/appwire-client/testing/currentTranscript";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { IDBFactory, IDBKeyRange } from "fake-indexeddb";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
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

async function openTab() {
  // Separate module graphs model separate browser lifetimes, not the isolated
  // fixture route. Both ordinary stores use the same IndexedDB factory/name.
  vi.resetModules();
  const threads = await import("./threads");
  const connection = await import("./connection");
  threads.resetThreadsStoreForTests();
  threads.setCacheChannelFactoryForTests(() => null);
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
  await threads.threadsStore.getState().ensureThread(ref);
  await threads.settleEntryTranscriptForTests(ref);
  return tab;
}

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
