import type {
  AnyNotification,
  OverlayItem,
  ThreadItem,
  ThreadReadResponse,
  ThreadTurnsListResponse,
} from "@evener/appwire-client";
import { currentRead } from "@evener/appwire-client/testing/currentTranscript";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { afterEach, expect, test, vi } from "vitest";
import { connectionStore } from "./connection";
import { entryHistoryKey } from "./sessionCacheSchema";
import { SessionEntryCache } from "./sessionEntryCache";
import { createEntryCacheFixture } from "./testing/entryCacheFixture";
import { observeProductionEntryForTests } from "./testing/entryProductionHarness";
import { holdNextTransaction } from "./testing/stalledIndexedDB";
import {
  acquireBackgroundThread,
  backgroundTranscriptSnapshotForTests,
  resetThreadsStoreForTests,
  threadsStore,
} from "./threads";
import { TranscriptRepository, type TranscriptRepositoryBinding } from "./transcriptRepository";

const ref = "local:retirement";
const threadId = `thr_${ref}`;
const identity = { clientId: location.origin, hostId: "local", sessionId: `sess_${ref}`, incarnation: "inc-1" };
const historyKey = entryHistoryKey(identity);
const fixtures: ReturnType<typeof createEntryCacheFixture>[] = [];
const repos: TranscriptRepository[] = [];

function fixture() {
  const value = createEntryCacheFixture();
  fixtures.push(value);
  return value;
}
function repoOf(cache: ReturnType<typeof fixture>["cache"], ports: Partial<TranscriptRepositoryBinding> = {}) {
  const repo = new TranscriptRepository(cache, {
    ref,
    threadId,
    identity,
    historyKey,
    clearEpoch: 0,
    lifetime: 1,
    ...ports,
  });
  repos.push(repo);
  return repo;
}
function recorded(text = "recorded-stream"): ThreadItem {
  return {
    type: "agentMessage",
    id: "recorded",
    turnId: "turn_1",
    transcriptKey: "recorded",
    position: { entry: 41, item: 0 },
    version: 41,
    roundId: "r1",
    text,
  };
}
function readOf(items: ThreadItem[] = [], length = 40): ThreadReadResponse {
  const base = currentRead(ref);
  return {
    ...base,
    snapshot: { incarnation: "inc-1", length },
    thread: {
      ...base.thread,
      turns: [{ id: "turn_1", status: "completed", itemsView: "full", version: length, items }],
    },
  };
}
const end: AnyNotification = { method: "overlay/end", params: { ref, threadId, roundId: "r1" } };
function history(items: ThreadItem[], length = 41): AnyNotification {
  return {
    method: "history/updated",
    params: { ref, threadId, bootGeneration: "1", epoch: 1, snapshot: { incarnation: "inc-1", length }, items },
  };
}
const tool: AnyNotification = {
  method: "overlay/upserted",
  params: {
    ref,
    threadId,
    item: {
      key: "tool:old",
      kind: "tool",
      roundId: "r1",
      historyKey: "old-tool",
      turnId: "turn_1",
      anchor: { entry: 2, item: 0 },
      item: {
        id: "old-tool",
        type: "commandExecution",
        turnId: "turn_1",
        status: "inProgress",
        output: "provisional-tool",
      },
    },
  },
};
const writeStores = ["transcriptItems", "transcriptTurns", "transcriptCoverage", "transcriptMeta"];

afterEach(async () => {
  for (const repo of repos.splice(0)) repo.dispose();
  resetThreadsStoreForTests();
  vi.restoreAllMocks();
  for (const value of fixtures.splice(0)) await value.dispose();
});

test("end retires only covered slots in the real entry store publication", async () => {
  const value = fixture();
  resetThreadsStoreForTests();
  const route = observeProductionEntryForTests(value.cache, ref);
  const client = new FakeClient("ready");
  client.on("thread/read", () => readOf());
  client.on("thread/unsubscribe", () => ({}));
  connectionStore.getState().connect(client);
  await threadsStore.getState().ensureThread(ref);
  client.emitNotification(tool);
  client.emitNotification(end);
  expect(Object.keys(threadsStore.getState().threads.get(ref)?.overlay ?? {})).toEqual([
    "stream:s1:agentMessage",
    "tool:old",
  ]);
  route.dispose();
});

test.each(["end-first", "history-first"])(
  "%s recorded publication retires the covered slot while storage is held",
  async (order) => {
    const value = fixture();
    const repo = repoOf(value.cache);
    const reader = repo.attachReader();
    repo.acceptRead(readOf(), { lifetime: 1, readId: "initial", boundary: "initial" });
    await repo.settled();
    repo.accept(tool, { lifetime: 1 });
    const held = holdNextTransaction(writeStores, "readwrite");
    try {
      if (order === "end-first") repo.accept(end, { lifetime: 1 });
      repo.accept(history([recorded()]), { lifetime: 1 });
      await held.reached;
      if (order === "history-first") repo.accept(end, { lifetime: 1 });
      expect(Object.keys(repo.getSnapshot()?.overlay ?? {})).toEqual(["tool:old"]);
      expect(
        reader
          .getSnapshot()
          ?.turns.flatMap((turn) => turn.items)
          .filter((item) => item.text === "recorded-stream"),
      ).toHaveLength(1);
    } finally {
      held.release();
      await repo.settled();
      reader.dispose();
    }
  },
);

test("repeated ends share a fresh disposition and fixed off-window page coverage", async () => {
  const value = fixture();
  const client = new FakeClient("ready");
  const response = deferred<ThreadReadResponse>();
  client.on("thread/read", () => response.promise);
  client.on("thread/turns/list", () => ({
    bootGeneration: "1",
    epoch: 1,
    snapshot: { incarnation: "inc-1", length: 50 },
    data: [
      {
        id: "turn_1",
        status: "completed",
        itemsView: "full",
        version: 50,
        items: [
          {
            id: "old-tool",
            type: "commandExecution",
            turnId: "turn_1",
            transcriptKey: "old-tool",
            position: { entry: 2, item: 0 },
            version: 50,
            completedAtEntry: 50,
            status: "completed",
            output: "completed-tool",
          },
        ],
      },
    ],
  }));
  const repo = repoOf(value.cache, {
    readHistory: () => client.request("thread/read", { ref, includeTurns: true }),
    readHistoryPage: ({ cursor, before }) => client.request("thread/turns/list", { ref, cursor, before }),
  });
  const reader = repo.attachReader();
  repo.acceptRead(readOf(), { lifetime: 1, readId: "initial", boundary: "initial" });
  await repo.settled();
  repo.accept(tool, { lifetime: 1 });
  repo.accept(end, { lifetime: 1 });
  repo.accept(end, { lifetime: 1 });
  const work = repo.settled();
  response.resolve({
    ...readOf([{ ...recorded(), position: { entry: 49, item: 0 }, version: 50 }], 50),
    overlay: [],
    olderCursor: "fixed-50",
    requestGeneration: 2,
  });
  await work;
  expect(client.calls.filter((call) => call.method === "thread/read")).toHaveLength(1);
  expect(client.calls.filter((call) => call.method === "thread/turns/list").map((call) => call.params)).toEqual([
    { ref, cursor: "fixed-50", before: { entry: 49, item: 0 } },
  ]);
  expect(repo.getSnapshot()?.overlay).toEqual({});
  expect(
    (await value.cache.getLatest(historyKey, 100)).find((row) => row.transcriptKey === "old-tool")?.item,
  ).toMatchObject({ output: "completed-tool" });
  repo.accept(end, { lifetime: 1 });
  await repo.settled();
  expect(client.calls.filter((call) => call.method === "thread/read")).toHaveLength(1);
  reader.dispose();
});

test.each(["stream", "preview", "tool"] as const)(
  "unanchored %s waits for its off-window replacement",
  async (kind) => {
    const value = fixture();
    const page = deferred<ThreadTurnsListResponse>();
    const replacement: ThreadItem =
      kind === "tool"
        ? {
            id: "old-tool",
            type: "commandExecution",
            turnId: "turn_1",
            transcriptKey: "old-tool",
            position: { entry: 2, item: 0 },
            version: 50,
            completedAtEntry: 50,
            status: "completed",
            output: "completed-tool",
          }
        : {
            ...recorded("completed-message"),
            position: { entry: 2, item: 0 },
            version: 50,
            ...(kind === "preview" ? { callId: "communicate-1" } : {}),
          };
    const slot: OverlayItem =
      kind === "tool"
        ? { ...tool.params.item, anchor: undefined }
        : {
            key: `${kind}:old`,
            kind,
            roundId: "r1",
            turnId: "turn_1",
            ...(kind === "preview" ? { callId: "communicate-1" } : { streamId: "s1" }),
            item: { id: "provisional", type: "agentMessage", text: "provisional-message" },
          };
    const readPage = vi.fn(() => page.promise);
    const repo = repoOf(value.cache, {
      readHistory: async () => ({
        ...readOf(
          [
            {
              ...recorded("unrelated"),
              id: "unrelated",
              transcriptKey: "unrelated",
              roundId: "r2",
              position: { entry: 49, item: 0 },
              version: 50,
            },
          ],
          50,
        ),
        overlay: [],
        olderCursor: "fixed-50",
        requestGeneration: 2,
      }),
      readHistoryPage: readPage,
      retryScheduler: () => () => {},
    });
    const reader = repo.attachReader();
    repo.acceptRead({ ...readOf(), overlay: [] }, { lifetime: 1, readId: "initial", boundary: "initial" });
    await repo.settled();
    repo.accept({ method: "overlay/upserted", params: { ref, threadId, item: slot } }, { lifetime: 1 });
    const gaps: boolean[] = [];
    const unlisten = repo.subscribe(() => {
      const snapshot = reader.getSnapshot();
      gaps.push(
        !snapshot?.overlay?.[slot.key] &&
          !snapshot?.turns
            .flatMap((turn) => turn.items)
            .some((item) => item.output === "completed-tool" || item.text === "completed-message"),
      );
    });
    repo.accept(end, { lifetime: 1 });
    try {
      await vi.waitFor(() => expect(readPage).toHaveBeenCalledTimes(1));
      expect(readPage).toHaveBeenCalledWith(
        expect.objectContaining({
          snapshot: { incarnation: "inc-1", length: 50 },
          cursor: "fixed-50",
          before: { entry: 49, item: 0 },
        }),
      );
      expect(reader.getSnapshot()?.overlay?.[slot.key]).toBeDefined();
      page.resolve({
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 50 },
        data: [{ id: "turn_1", status: "completed", itemsView: "full", version: 50, items: [replacement] }],
      });
      await repo.settled();
      expect(reader.getSnapshot()?.overlay?.[slot.key]).toBeUndefined();
      expect(
        reader
          .getSnapshot()
          ?.turns.flatMap((turn) => turn.items)
          .some((item) => item.output === "completed-tool" || item.text === "completed-message"),
      ).toBe(true);
      expect(gaps.length).toBeGreaterThan(0);
      expect(gaps).not.toContain(true);
    } finally {
      unlisten();
      page.resolve({ bootGeneration: "1", epoch: 1, snapshot: { incarnation: "inc-1", length: 50 }, data: [] });
      reader.dispose();
    }
  },
);

test.each([false, true])("unestablished end coverage remains stale, foreground=%s", async (foreground) => {
  const value = fixture();
  const retries: Array<() => void> = [];
  const repo = repoOf(value.cache, {
    readHistory: async () => ({ ...readOf([], 50), overlay: [], requestGeneration: 2 }),
    retryScheduler: (_attempt, retry) => {
      retries.push(retry);
      return () => {};
    },
  });
  const reader = foreground ? repo.attachReader() : undefined;
  repo.acceptRead(readOf(), { lifetime: 1, readId: "initial", boundary: "initial" });
  await repo.settled();
  repo.accept(
    { ...tool, params: { ...tool.params, item: { ...tool.params.item, anchor: undefined } } },
    { lifetime: 1 },
  );
  repo.accept(end, { lifetime: 1 });
  await repo.settled();
  expect(repo.getSnapshot()?.history?.invalidatedAtGeneration).toBeDefined();
  expect(repo.getSnapshot()?.overlay?.["tool:old"] !== undefined).toBe(foreground);
  expect(repo.getSnapshot()?.overlay?.["stream:s1:agentMessage"] !== undefined).toBe(foreground);
  expect(retries).toHaveLength(1);
  reader?.dispose();
});

test("multi-page end disposition retains distinct connected page certificates", async () => {
  const value = fixture();
  const client = new FakeClient("ready");
  client.on("thread/read", () => ({
    ...readOf([{ ...recorded("latest"), position: { entry: 49, item: 0 }, version: 50 }], 50),
    overlay: [],
    olderCursor: "page-one",
    requestGeneration: 2,
  }));
  client.on("thread/turns/list", ({ cursor }) => ({
    bootGeneration: "1",
    epoch: 1,
    snapshot: { incarnation: "inc-1", length: 50 },
    data: [
      {
        id: "turn_1",
        status: "completed",
        itemsView: "full",
        version: 50,
        items: [
          {
            ...recorded(cursor === "page-one" ? "middle body" : "oldest body"),
            id: cursor === "page-one" ? "middle" : "oldest",
            transcriptKey: cursor === "page-one" ? "middle" : "oldest",
            position: { entry: cursor === "page-one" ? 20 : 2, item: 0 },
            version: 50,
          },
        ],
      },
    ],
    ...(cursor === "page-one" ? { nextCursor: "page-two" } : {}),
  }));
  const repo = repoOf(value.cache, {
    readHistory: () => client.request("thread/read", { ref, includeTurns: true }),
    readHistoryPage: ({ cursor, before }) => client.request("thread/turns/list", { ref, cursor, before }),
  });
  const reader = repo.attachReader();
  repo.acceptRead(readOf(), { lifetime: 1, readId: "initial", boundary: "initial" });
  await repo.settled();
  repo.accept(tool, { lifetime: 1 });
  repo.accept(end, { lifetime: 1 });
  await repo.settled();
  expect(client.calls.filter((call) => call.method === "thread/turns/list").map((call) => call.params)).toEqual([
    { ref, cursor: "page-one", before: { entry: 49, item: 0 } },
    { ref, cursor: "page-two", before: { entry: 20, item: 0 } },
  ]);
  const calls = client.calls.length;
  const window = await value.cache.getOlderWindow(historyKey, { entry: 49, item: 0 }, 100, repo.getBaselineIdentity());
  const pages = window.coverage.filter((certificate) => certificate.rangeId.startsWith("page:"));
  expect(pages).toHaveLength(2);
  expect(new Set(pages.map((certificate) => certificate.boundary)).size).toBe(2);
  expect(
    pages
      .map(({ requestCursor, serverCursor }) => ({ requestCursor, serverCursor }))
      .sort((a, b) => (a.requestCursor ?? "").localeCompare(b.requestCursor ?? "")),
  ).toEqual([
    { requestCursor: "page-one", serverCursor: "page-two" },
    { requestCursor: "page-two", serverCursor: undefined },
  ]);
  expect(window).toMatchObject({ complete: true, stale: false });
  expect(window.rows.map((row) => (row.item as ThreadItem).text)).toEqual(["oldest body", "middle body"]);
  expect(client.calls).toHaveLength(calls);
  reader.dispose();
});

test.each([false, true])(
  "failed end disposition leaves stale authority and paced recovery, foreground=%s",
  async (foreground) => {
    const value = fixture();
    const client = new FakeClient("ready");
    client.on("thread/read", () => {
      throw new Error("source unavailable");
    });
    const retries: Array<() => void> = [];
    const repo = repoOf(value.cache, {
      readHistory: () => client.request("thread/read", { ref, includeTurns: true }),
      retryScheduler: (_attempt, retry) => {
        retries.push(retry);
        return () => {};
      },
    });
    const reader = foreground ? repo.attachReader() : undefined;
    repo.acceptRead(readOf(), { lifetime: 1, readId: "initial", boundary: "initial" });
    await repo.settled();
    repo.accept(tool, { lifetime: 1 });
    repo.accept(
      {
        method: "overlay/upserted",
        params: {
          ref,
          threadId,
          item: {
            key: "notice",
            kind: "notice",
            roundId: "r1",
            turnId: "turn_1",
            item: { id: "notice", type: "agentMessage", text: "separate-notice" },
          },
        },
      },
      { lifetime: 1 },
    );
    repo.accept(end, { lifetime: 1 });
    await repo.settled();
    expect(repo.getSnapshot()?.history?.invalidatedAtGeneration).toBeDefined();
    expect(repo.getSnapshot()?.overlay?.["tool:old"] !== undefined).toBe(foreground);
    expect(
      repo
        .getSnapshot()
        ?.turns.flatMap((turn) => turn.items)
        .some((item) => item.output === "provisional-tool"),
    ).toBe(foreground);
    expect(repo.getSnapshot()?.overlay?.notice).toBeDefined();
    expect(client.calls.filter((call) => call.method === "thread/read")).toHaveLength(1);
    expect(retries).toHaveLength(1);
    reader?.dispose();
  },
);

test("an interrupted end retries automatically after pacing and clears truly unrecorded slots", async () => {
  const value = fixture();
  const read = vi
    .fn()
    .mockRejectedValueOnce(new Error("interrupted"))
    // Amendment 1: exhaust the interval. The old cursor without a page source
    // could not establish that these slots were truly unrecorded.
    .mockResolvedValue({ ...readOf([], 50), overlay: [], olderCursor: undefined, requestGeneration: 2 });
  const retries: Array<() => void> = [];
  const repo = repoOf(value.cache, {
    readHistory: read,
    retryScheduler: (_attempt, retry) => {
      retries.push(retry);
      return () => {};
    },
  });
  const reader = repo.attachReader();
  repo.acceptRead(readOf(), { lifetime: 1, readId: "initial", boundary: "initial" });
  await repo.settled();
  repo.accept(tool, { lifetime: 1 });
  repo.accept(end, { lifetime: 1 });
  await vi.waitFor(() => expect(retries).toHaveLength(1));
  expect(repo.getSnapshot()?.overlay?.["tool:old"]).toBeDefined();
  retries[0]!();
  // Observe the production retry callback before using settlement to join IDB.
  await vi.waitFor(() => expect(repo.getSnapshot()?.overlay).toEqual({}));
  expect(read).toHaveBeenCalledTimes(2);
  expect(repo.getSnapshot()?.history?.invalidatedAtGeneration).toBeUndefined();
  await repo.settled();
  reader.dispose();
});

test("later traffic cannot extend the end page boundary", async () => {
  const value = fixture();
  const page = deferred<import("@evener/appwire-client").ThreadTurnsListResponse>();
  const retries: Array<() => void> = [];
  const repo = repoOf(value.cache, {
    readHistory: async () => ({ ...readOf([recorded()], 50), overlay: [], olderCursor: "fixed", requestGeneration: 2 }),
    readHistoryPage: () => page.promise,
    retryScheduler: (_attempt, retry) => {
      retries.push(retry);
      return () => {};
    },
  });
  repo.acceptRead(readOf(), { lifetime: 1, readId: "initial", boundary: "initial" });
  await repo.settled();
  repo.accept(tool, { lifetime: 1 });
  repo.accept(end, { lifetime: 1 });
  await vi.waitFor(() => expect(repo.getSnapshot()?.history?.length).toBe(50));
  page.resolve({ bootGeneration: "1", epoch: 1, snapshot: { incarnation: "inc-1", length: 51 }, data: [] });
  await repo.settled();
  expect(repo.getSnapshot()?.history?.length).toBe(50);
  expect(repo.getSnapshot()?.history?.invalidatedAtGeneration).toBeDefined();
  expect(retries).toHaveLength(1);
});

test.each(["background", "pane"])(
  "final %s release issues a fresh read and holds the wire until its recorded write finishes",
  async (ownerKind) => {
    const value = fixture();
    resetThreadsStoreForTests();
    const route = observeProductionEntryForTests(value.cache, ref);
    const client = new FakeClient("ready");
    const final = deferred<ThreadReadResponse>();
    let reads = 0;
    client.on("thread/read", () => (++reads === 1 ? readOf([], 40) : final.promise));
    client.on("thread/unsubscribe", () => ({}));
    connectionStore.getState().connect(client);
    const owner =
      ownerKind === "background"
        ? acquireBackgroundThread(ref)
        : {
            ready: threadsStore.getState().ensureThread(ref),
            release: () => threadsStore.getState().releaseThreadAndWait(ref),
          };
    await owner.ready;
    await route.settled();
    const releasing = owner.release();
    await vi.waitFor(() => expect(reads).toBe(2));
    expect(backgroundTranscriptSnapshotForTests().networkHydrations).toBe(1);
    expect(client.calls.filter((call) => call.method === "thread/unsubscribe")).toHaveLength(0);
    const held = holdNextTransaction(writeStores, "readwrite");
    try {
      final.resolve({ ...readOf([recorded("final-read")], 41), overlay: [], requestGeneration: 2 });
      await held.reached;
      expect(client.calls.filter((call) => call.method === "thread/unsubscribe")).toHaveLength(0);
    } finally {
      held.release();
    }
    expect(await releasing).toEqual({ kind: "saved" });
    expect(client.calls.filter((call) => call.method === "thread/unsubscribe")).toHaveLength(1);
    expect(
      (await value.cache.getLatest(historyKey, 100)).find((row) => row.transcriptKey === "recorded")?.item,
    ).toMatchObject({ text: "final-read" });
    route.dispose();
  },
);

test("off-window completion accepted before final response is saved, while later traffic cannot extend the cut", async () => {
  const value = fixture();
  const response = deferred<ThreadReadResponse>();
  const read = vi.fn(() => response.promise);
  const repo = repoOf(value.cache, { readHistory: read });
  const reader = repo.attachReader();
  repo.acceptRead(readOf(), { lifetime: 1, readId: "initial", boundary: "initial" });
  await repo.settled();
  const completion: ThreadItem = {
    id: "old-tool",
    type: "commandExecution",
    turnId: "turn_1",
    transcriptKey: "old-tool",
    position: { entry: 2, item: 0 },
    version: 42,
    completedAtEntry: 42,
    status: "completed",
    output: "release-completion",
  };
  const retiring = repo.retire();
  expect(read).toHaveBeenCalledTimes(1);
  const held = holdNextTransaction(writeStores, "readwrite");
  try {
    expect(repo.accept(history([completion], 42), { lifetime: 1 })).toBe(true);
    await held.reached;
    response.resolve({
      ...readOf([{ ...recorded("finite-cut"), position: { entry: 43, item: 0 }, version: 43 }], 43),
      overlay: [],
      requestGeneration: 2,
    });
    await vi.waitFor(() => expect(repo.getSnapshot()?.history?.length).toBe(43));
    expect(
      repo.accept(history([{ ...recorded("beyond-cut"), position: { entry: 44, item: 0 }, version: 44 }], 44), {
        lifetime: 1,
      }),
    ).toBe(false);
    expect(repo.getSnapshot()?.history?.length).toBe(43);
  } finally {
    held.release();
  }
  expect(await retiring).toEqual({ kind: "saved" });
  const rows = await value.cache.getLatest(historyKey, 100);
  expect(rows.find((row) => row.transcriptKey === "old-tool")?.item).toMatchObject({
    output: "release-completion",
    completedAtEntry: 42,
  });
  expect(rows.some((row) => (row.item as ThreadItem).text === "beyond-cut")).toBe(false);
  reader.dispose();
});

test.each(["read", "write"])("final %s failure finishes stale without claiming saved", async (failure) => {
  const value = fixture();
  const repo = repoOf(value.cache, {
    readHistory: async () => {
      if (failure === "read") throw new Error("unreachable");
      return { ...readOf([{ ...recorded("not-saved"), version: 42 }], 42), overlay: [], requestGeneration: 2 };
    },
    retryScheduler: () => () => {},
  });
  const reader = repo.attachReader();
  repo.acceptRead(readOf([recorded("cached-before")], 41), { lifetime: 1, readId: "initial", boundary: "initial" });
  await repo.settled();
  if (failure === "write")
    vi.spyOn(value.cache, "commitBatch").mockResolvedValue({ outcome: "failed", error: new Error("write failed") });
  expect(await repo.retire()).toEqual({ kind: "stale" });
  if (failure === "write") expect(value.cache.commitBatch).toHaveBeenCalled();
  expect(repo.getSnapshot()?.history?.invalidatedAtGeneration).toBeDefined();
  expect(
    (await value.cache.getLatest(historyKey, 100)).find((row) => row.transcriptKey === "recorded")?.item,
  ).toMatchObject({ text: "cached-before" });
  reader.dispose();
});

test("reacquisition cancels final retirement and its old response cannot publish into the renewed lifetime", async () => {
  const value = fixture();
  resetThreadsStoreForTests();
  const route = observeProductionEntryForTests(value.cache, ref);
  const client = new FakeClient("ready");
  const old = deferred<ThreadReadResponse>();
  let reads = 0;
  client.on("thread/read", () => {
    reads++;
    return reads === 2 ? old.promise : { ...readOf([recorded(reads === 1 ? "first" : "renewed")], 41), overlay: [] };
  });
  client.on("thread/unsubscribe", () => ({}));
  connectionStore.getState().connect(client);
  const first = acquireBackgroundThread(ref);
  await first.ready;
  await route.settled();
  const releasing = first.release();
  await vi.waitFor(() => expect(reads).toBe(2));
  const renewed = acquireBackgroundThread(ref);
  await renewed.ready;
  expect(await releasing).toEqual({ kind: "cancelled" });
  old.resolve({ ...readOf([recorded("obsolete-final")], 41), overlay: [], requestGeneration: 2 });
  await route.settled();
  expect(
    threadsStore
      .getState()
      .threads.get(ref)
      ?.turns.flatMap((turn) => turn.items)
      .some((item) => item.text === "obsolete-final"),
  ).toBe(false);
  expect(client.calls.filter((call) => call.method === "thread/unsubscribe")).toHaveLength(0);
  expect(await renewed.release()).toEqual({ kind: "saved" });
  route.dispose();
});

test("one deadline bounds end disposition and late responses cannot publish", async () => {
  const value = fixture();
  const response = deferred<ThreadReadResponse>();
  const retries: Array<() => void> = [];
  const repo = repoOf(value.cache, {
    readHistory: () => response.promise,
    retryScheduler: (_attempt, retry) => {
      retries.push(retry);
      return () => {};
    },
  });
  repo.acceptRead(readOf(), { lifetime: 1, readId: "initial", boundary: "initial" });
  await repo.settled();
  vi.useFakeTimers();
  try {
    repo.accept(end, { lifetime: 1 });
    await vi.advanceTimersByTimeAsync(30_000);
    expect(retries).toHaveLength(1);
    response.resolve({ ...readOf([recorded("too-late")], 41), overlay: [], requestGeneration: 2 });
    await vi.advanceTimersByTimeAsync(0);
    expect(repo.getSnapshot()?.history?.length).toBe(40);
    expect(repo.getSnapshot()?.history?.invalidatedAtGeneration).toBeDefined();
  } finally {
    vi.useRealTimers();
  }
  await repo.settled();
});

test("a held earlier read does not substitute for the fresh final read", async () => {
  const value = fixture();
  resetThreadsStoreForTests();
  const route = observeProductionEntryForTests(value.cache, ref);
  const client = new FakeClient("ready");
  const earlier = deferred<ThreadReadResponse>();
  const final = deferred<ThreadReadResponse>();
  let reads = 0;
  client.on("thread/read", () => (++reads === 1 ? readOf([], 40) : reads === 2 ? earlier.promise : final.promise));
  client.on("thread/unsubscribe", () => ({}));
  connectionStore.getState().connect(client);
  const owner = acquireBackgroundThread(ref);
  await owner.ready;
  await route.settled();
  const refreshing = threadsStore.getState().refreshThread(ref);
  await vi.waitFor(() => expect(reads).toBe(2));
  const releasing = owner.release();
  await vi.waitFor(() => expect(reads).toBe(3));
  final.resolve({ ...readOf([recorded("fresh-final")], 41), overlay: [], requestGeneration: 2 });
  expect(await releasing).toEqual({ kind: "saved" });
  earlier.resolve({ ...readOf([recorded("earlier-read")], 41), overlay: [], requestGeneration: 2 });
  await refreshing;
  expect(threadsStore.getState().threads.has(ref)).toBe(false);
  expect(
    (await value.cache.getLatest(historyKey, 100)).find((row) => row.transcriptKey === "recorded")?.item,
  ).toMatchObject({ text: "fresh-final" });
  route.dispose();
});

test("a hung final read finishes stale at the read deadline and its late result is inert", async () => {
  const value = fixture();
  const response = deferred<ThreadReadResponse>();
  const repo = repoOf(value.cache, { readHistory: () => response.promise });
  repo.acceptRead(readOf(), { lifetime: 1, readId: "initial", boundary: "initial" });
  await repo.settled();
  vi.useFakeTimers();
  try {
    const retiring = repo.retire();
    await vi.advanceTimersByTimeAsync(30_000);
    expect(await retiring).toEqual({ kind: "stale" });
    response.resolve({ ...readOf([recorded("late-final")], 41), requestGeneration: 2 });
    await vi.advanceTimersByTimeAsync(0);
    expect(repo.getSnapshot()?.history?.length).toBe(40);
    expect(repo.getSnapshot()?.history?.invalidatedAtGeneration).toBeDefined();
  } finally {
    vi.useRealTimers();
  }
  await repo.settled();
});

test.each(["background", "pane"])("offline final %s release finishes stale without a fresh read", async (kind) => {
  const value = fixture();
  const route = observeProductionEntryForTests(value.cache, ref);
  const client = new FakeClient("ready");
  client.on("thread/read", () => readOf());
  client.on("thread/unsubscribe", () => ({}));
  connectionStore.getState().connect(client);
  const owner =
    kind === "background"
      ? acquireBackgroundThread(ref)
      : {
          ready: threadsStore.getState().ensureThread(ref),
          release: () => threadsStore.getState().releaseThreadAndWait(ref),
        };
  await owner.ready;
  await route.settled();
  client.emitStateChange("reconnecting");
  expect(await owner.release()).toEqual({ kind: "stale" });
  expect(client.calls.filter((call) => call.method === "thread/read")).toHaveLength(1);
  expect(threadsStore.getState().threads.has(ref)).toBe(false);
  client.emitReady();
  await new Promise((resolve) => setTimeout(resolve, 0));
  expect(client.calls.filter((call) => call.method === "thread/read")).toHaveLength(1);
  route.dispose();
});

test("a real Clear epoch refusal makes final retirement stale rather than saved", async () => {
  const value = fixture();
  const sibling = new SessionEntryCache({ indexedDB: value.indexedDB, databaseName: value.name });
  const repo = repoOf(value.cache, {
    readHistory: async () => ({ ...readOf([recorded("refused-final")], 42), overlay: [], requestGeneration: 2 }),
  });
  const reader = repo.attachReader();
  repo.acceptRead(readOf([recorded("before-clear")], 41), { lifetime: 1, readId: "initial", boundary: "initial" });
  await repo.settled();
  expect(await value.cache.getLatest(historyKey, 100)).toHaveLength(1);
  try {
    expect(await sibling.clear()).toEqual({ committed: true, epoch: 1 });
    const writes = vi.spyOn(value.cache, "commitBatch");
    const outcome = await repo.retire();
    expect(writes).toHaveBeenCalledTimes(1);
    expect(await writes.mock.results[0]!.value).toMatchObject({ outcome: "refused", reason: "clear-epoch" });
    expect(await value.cache.getLatest(historyKey, 100)).toEqual([]);
    expect(outcome).toEqual({ kind: "stale" });
  } finally {
    sibling.close();
    reader.dispose();
  }
});

test("pending end and final release finish stale with a committed finite cut and ownership cleanup", async () => {
  const value = fixture();
  const route = observeProductionEntryForTests(value.cache, ref);
  const client = new FakeClient("ready");
  const pendingEnd = deferred<ThreadReadResponse>();
  let reads = 0;
  client.on("thread/read", () => {
    reads++;
    if (reads === 2) return pendingEnd.promise;
    return reads === 1 ? readOf() : { ...readOf([recorded("finite-final")], 42), overlay: [], requestGeneration: 3 };
  });
  client.on("thread/unsubscribe", () => ({}));
  connectionStore.getState().connect(client);
  const owner = acquireBackgroundThread(ref);
  await owner.ready;
  await route.settled();
  client.emitNotification(tool);
  client.emitNotification(end);
  await vi.waitFor(() => expect(reads).toBe(2));
  const writes = vi.spyOn(value.cache, "commitBatch");
  const releasing = owner.release();
  expect(client.calls.filter((call) => call.method === "thread/unsubscribe")).toHaveLength(0);
  pendingEnd.resolve({ ...readOf([], 41), overlay: [], requestGeneration: 2 });
  expect(await releasing).toEqual({ kind: "stale" });
  expect(reads).toBe(3);
  expect(writes).toHaveBeenCalledTimes(1);
  expect(await writes.mock.results[0]!.value).toMatchObject({ outcome: "committed" });
  expect((await value.cache.getLatest(historyKey, 100)).map((row) => row.item)).toEqual([
    expect.objectContaining({ text: "finite-final" }),
  ]);
  expect(threadsStore.getState().threads.has(ref)).toBe(false);
  expect(backgroundTranscriptSnapshotForTests().networkHydrations).toBe(0);
  expect(client.calls.filter((call) => call.method === "thread/unsubscribe")).toHaveLength(1);
  route.dispose();
});

test.each(["reconnect", "replace"])(
  "a final waiter cannot issue after client %s changes its lifetime",
  async (change) => {
    const value = fixture();
    const route = observeProductionEntryForTests(value.cache, ref);
    const client = new FakeClient("ready");
    const pendingEnd = deferred<ThreadReadResponse>();
    let reads = 0;
    client.on("thread/read", () => (++reads === 2 ? pendingEnd.promise : readOf()));
    client.on("thread/unsubscribe", () => ({}));
    connectionStore.getState().connect(client);
    const owner = acquireBackgroundThread(ref);
    await owner.ready;
    await route.settled();
    client.emitNotification(tool);
    client.emitNotification(end);
    await vi.waitFor(() => expect(reads).toBe(2));
    const releasing = owner.release();
    const next = change === "replace" ? new FakeClient("ready") : client;
    if (change === "replace") {
      next.on("thread/read", () => readOf());
      next.on("thread/unsubscribe", () => ({}));
      connectionStore.getState().connect(next);
    } else {
      client.emitStateChange("reconnecting");
      client.emitReady();
    }
    pendingEnd.resolve({ ...readOf([recorded("obsolete-end")], 41), overlay: [], requestGeneration: 2 });
    expect(await releasing).toEqual({ kind: "stale" });
    expect(reads).toBe(2);
    expect(await value.cache.getLatest(historyKey, 100)).toEqual([]);
    const renewed = acquireBackgroundThread(ref);
    await renewed.ready;
    expect(next.calls.filter((call) => call.method === "thread/read")).toHaveLength(change === "replace" ? 1 : 3);
    expect(await renewed.release()).toEqual({ kind: "saved" });
    route.dispose();
  },
);
