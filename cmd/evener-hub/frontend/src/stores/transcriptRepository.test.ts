import {
  type AnyNotification,
  encodeTranscriptItem,
  hydrateThread,
  type ThreadItem,
  type ThreadModel,
  type ThreadReadResponse,
} from "@evener/appwire-client";
import { IDBObjectStore } from "fake-indexeddb";
import { afterEach, expect, it, vi } from "vitest";
import { SessionCacheIndexedDB } from "./sessionCacheIndexedDB";
import {
  ENTRY_COVERAGE_STORE,
  ENTRY_ITEM_STORE,
  entryHistoryKey,
  type TranscriptResolvedIdentity,
} from "./sessionCacheSchema";
import { createEntryCacheFixture } from "./testing/entryCacheFixture";
import { holdIndexedDBEvent, holdNextTransaction } from "./testing/stalledIndexedDB";
import { TranscriptRepository } from "./transcriptRepository";

const fixtures: ReturnType<typeof createEntryCacheFixture>[] = [];
const identity: TranscriptResolvedIdentity = {
  clientId: "client-a",
  hostId: "host-a",
  sessionId: "session-a",
  incarnation: "inc-a",
};
const ref = "ref-a";
const threadId = "thread-a";
const historyKey = entryHistoryKey(identity);
const CAPABILITIES = {
  send: true,
  steer: true,
  interrupt: true,
  compact: true,
  clear: true,
  forkFromTurn: true,
  shutdown: true,
  changeModel: true,
  changeVisionModel: true,
  queue: true,
  goal: true,
  sharedNotes: true,
  rename: true,
};

function read(label: string, length = 1, transcriptKey = `item-${label}`): ThreadReadResponse {
  return {
    thread: {
      id: threadId,
      sessionId: identity.sessionId,
      preview: "",
      ephemeral: false,
      modelProvider: "test/provider",
      createdAt: 1,
      updatedAt: 1,
      status: { type: "idle" },
      cwd: "/tmp",
      cliVersion: "test",
      source: "evener",
      turns: [
        {
          id: "turn_1",
          itemsView: "full",
          status: "completed",
          version: length,
          items: [
            {
              type: "agentMessage",
              id: transcriptKey,
              transcriptKey,
              turnId: "turn_1",
              position: { entry: length, item: 0 },
              version: length,
              text: label,
            },
          ],
        },
      ],
      evener: { ref, capabilities: CAPABILITIES, queue: { revision: 0 } },
    },
    bootGeneration: "1",
    epoch: 0,
    snapshot: { incarnation: identity.incarnation, length },
    overlay: [],
  };
}

function readMany(count: number): ThreadReadResponse {
  const response = read("row-1", count);
  response.thread.turns = [
    {
      id: "turn_1",
      itemsView: "full",
      status: "completed",
      version: count,
      items: Array.from(
        { length: count },
        (_, index): ThreadItem => ({
          type: "agentMessage",
          id: `item-${index + 1}`,
          transcriptKey: `item-${index + 1}`,
          turnId: "turn_1",
          position: { entry: index + 1, item: 0 },
          version: index + 1,
          text: `row-${index + 1}`,
        }),
      ),
    },
  ];
  response.snapshot = { incarnation: identity.incarnation, length: count };
  return response;
}

function repository(cache: ReturnType<typeof createEntryCacheFixture>["cache"], lifetime = 1, seedModel?: ThreadModel) {
  return new TranscriptRepository(cache, {
    ref,
    threadId,
    historyKey,
    identity,
    clearEpoch: 0,
    lifetime,
    seedModel,
    now: () => 100,
  });
}

function holdNextEntryPut(transcriptKey: string) {
  const put = IDBObjectStore.prototype.put;
  let transaction: IDBTransaction | undefined;
  let hold: ReturnType<typeof holdIndexedDBEvent> | undefined;
  let released = false;
  let markReached!: () => void;
  const reached = new Promise<void>((resolve) => {
    markReached = resolve;
  });
  const spy = vi.spyOn(IDBObjectStore.prototype, "put").mockImplementation(function (
    this: IDBObjectStore,
    ...args: Parameters<IDBObjectStore["put"]>
  ) {
    const request = put.apply(this, args);
    const value = args[0] as { transcriptKey?: string };
    if (!hold && this.name === ENTRY_ITEM_STORE && value.transcriptKey === transcriptKey) {
      transaction = this.transaction;
      request.addEventListener("success", markReached, { once: true });
      hold = holdIndexedDBEvent(request, "success");
      request.addEventListener("success", () => {}, { once: true });
      const pulse = () => {
        if (released) return;
        const keepAlive = this.count();
        keepAlive.addEventListener("success", pulse, { once: true });
      };
      pulse();
    }
    return request;
  });
  return {
    reached,
    commit() {
      released = true;
      spy.mockRestore();
      hold?.release();
    },
    abort() {
      transaction?.abort();
      released = true;
      spy.mockRestore();
      hold?.release();
    },
  };
}

afterEach(async () => {
  for (const fixture of fixtures.splice(0)) await fixture.dispose();
});

it("returns an awaitable completion for entry fixture database deletion", async () => {
  const fixture = createEntryCacheFixture();
  const disposed = fixture.dispose();
  expect(disposed).toBeInstanceOf(Promise);
  await expect(disposed).resolves.toBeUndefined();
});

it("accepts a complete wire read, publishes a real model, and commits keyed rows and coverage", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const owner = repository(fixture.cache);
  const updates: unknown[] = [];
  owner.subscribe(() => updates.push(owner.getSnapshot()));

  expect(owner.acceptRead(read("recorded"), { lifetime: 1, readId: "read-1", boundary: "read-boundary-1" })).toBe(true);
  expect(
    owner
      .getSnapshot()
      ?.turns.flatMap((turn) => turn.items)
      .map((item) => item.text),
  ).toEqual(["recorded"]);
  expect(updates).toHaveLength(1);
  await owner.settled();
  await expect(fixture.cache.getLatest(historyKey, 40)).resolves.toMatchObject([
    { transcriptKey: "item-recorded", version: 1, item: { text: "recorded" } },
  ]);
  await expect(fixture.cache.getCoverage(historyKey, "read:read-1")).resolves.toMatchObject({
    representedSnapshot: 1,
    boundary: "read-boundary-1",
  });
  owner.dispose();
});

it("commits all initial read rows and serves the latest bounded ascending window after bodies settle", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const owner = repository(fixture.cache);
  expect(owner.acceptRead(readMany(45), { lifetime: 1, readId: "read-many", boundary: "read-many-boundary" })).toBe(
    true,
  );
  await owner.settled();
  const rows = await fixture.cache.getLatest(historyKey, 100);
  const window = await fixture.cache.getLatestWindow(historyKey, 40, "read-many-boundary");
  const backgroundItems = owner.getSnapshot()?.history?.turns.flatMap((turn) => turn.items) ?? [];
  expect(rows).toHaveLength(45);
  expect(owner.getSnapshot()?.turns.flatMap((turn) => turn.items)).toHaveLength(0);
  expect(backgroundItems).toHaveLength(45);
  expect(backgroundItems.map(encodeTranscriptItem).every((item) => item.identityOnly && item.textOmitted)).toBe(true);
  expect(JSON.stringify(backgroundItems.map(encodeTranscriptItem))).not.toContain("row-");
  expect(window.rows.map((row) => row.transcriptKey)).toEqual(
    Array.from({ length: 40 }, (_, index) => `item-${index + 6}`),
  );
  expect(window.coverage[0]).toMatchObject({ representedSnapshot: 45, start: { entry: 1 }, end: { entry: 45 } });
  owner.dispose();
});

it("records explicit empty history coverage without inventing items", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const owner = repository(fixture.cache);
  const empty = read("empty", 0);
  empty.thread.turns = [];
  expect(owner.acceptRead(empty, { lifetime: 1, readId: "read-empty", boundary: "read-empty-boundary" })).toBe(true);
  await owner.settled();
  await expect(fixture.cache.getLatest(historyKey, 40)).resolves.toEqual([]);
  await expect(fixture.cache.getCoverage(historyKey, "read:read-empty")).resolves.toMatchObject({
    start: null,
    end: null,
    representedSnapshot: 0,
  });
  owner.dispose();
});

it("stores compatible read changes without claiming their gaps as latest-window coverage", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const owner = repository(fixture.cache);
  const response = read("latest", 10);
  response.changes = {
    items: [
      {
        type: "agentMessage",
        id: "old-item",
        transcriptKey: "item-old",
        turnId: "turn_1",
        position: { entry: 2, item: 0 },
        version: 2,
        text: "old-but-compatible",
      },
    ],
  };
  owner.acceptRead(response, { lifetime: 1, readId: "read-cut", boundary: "read-cut-boundary" });
  expect(
    owner
      .getSnapshot()
      ?.turns.flatMap((turn) => turn.items)
      .map((item) => item.text),
  ).toContain("old-but-compatible");
  await owner.settled();
  const coverage = await fixture.cache.getCoverage(historyKey, "read:read-cut");
  expect(coverage).toMatchObject({ start: { entry: 10 }, end: { entry: 10 }, representedSnapshot: 10 });
  await expect(fixture.cache.getLatest(historyKey, 40)).resolves.toMatchObject([
    { transcriptKey: "item-old" },
    { transcriptKey: "item-latest" },
  ]);
  owner.dispose();
});

it("keeps an accepted read visible after cache failure and refuses to call the local window current", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const owner = repository(fixture.cache);
  const reader = owner.attachReader();
  owner.acceptRead(read("before"), { lifetime: 1, readId: "read-before", boundary: "read-before-boundary" });
  await owner.settled();
  vi.spyOn(fixture.cache, "commitBatch").mockResolvedValue({
    outcome: "failed",
    error: new Error("transaction aborted"),
  });

  expect(
    owner.acceptRead(read("accepted", 2), { lifetime: 1, readId: "read-after", boundary: "read-after-boundary" }),
  ).toBe(true);
  await owner.settled();
  expect(
    owner
      .getSnapshot()
      ?.turns.flatMap((turn) => turn.items)
      .map((item) => item.text),
  ).toEqual(["before", "accepted"]);
  await expect(fixture.cache.getLatest(historyKey, 40)).resolves.toMatchObject([{ transcriptKey: "item-before" }]);
  expect((await reader.openLocal(40)).status).toBe("recovery-needed");
  reader.dispose();
  owner.dispose();
});

it("reopens committed nonresident history from bounded local rows without a server read", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const writer = repository(fixture.cache);
  writer.acceptRead(read("persisted"), { lifetime: 1, readId: "read-local", boundary: "read-local-boundary" });
  await writer.settled();
  await expect(fixture.cache.getLatestWindow(historyKey, 40)).resolves.toMatchObject({ complete: true, stale: false });
  writer.dispose();

  const summary = read("summary");
  summary.thread.status = { type: "active" };
  summary.thread.turns = [{ id: "turn_live", itemsView: "full", status: "inProgress", version: 1, items: [] }];
  summary.thread.evener.activeTurnId = "turn_live";
  summary.overlay = [
    {
      key: "stream:live/1:agentMessage",
      kind: "stream",
      turnId: "turn_live",
      roundId: "live",
      streamId: "live/1",
      item: {
        type: "agentMessage",
        id: "live-stream",
        turnId: "turn_live",
        roundId: "live",
        text: "live while opening local history",
        status: "inProgress",
      },
    },
  ];
  const seed = hydrateThread(summary, ref, 101);
  const localOnly = repository(fixture.cache, 2, seed);
  const reader = localOnly.attachReader();
  const acceptedWireReads = vi.spyOn(localOnly, "acceptRead");
  const opened = await reader.openLocal(40);
  expect(opened.status).toBe("current");
  expect(acceptedWireReads).not.toHaveBeenCalled();
  expect(opened.model?.turns.flatMap((turn) => turn.items).map((item) => item.text)).toEqual([
    "live while opening local history",
    "persisted",
  ]);
  expect(opened.model?.runningTurnId).toBe("turn_live");
  expect(opened.model?.overlay).toMatchObject({
    "stream:live/1:agentMessage": { item: { text: "live while opening local history" } },
  });
  expect(localOnly.getSnapshot()?.turns.flatMap((turn) => turn.items)).toHaveLength(2);
  reader.dispose();
  expect(
    localOnly
      .getSnapshot()
      ?.turns.flatMap((turn) => turn.items)
      .map((item) => item.text),
  ).toEqual(["live while opening local history"]);
  localOnly.dispose();
});

it("attaches a reader to an already-mounted model by reference without reconstructing history", () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const mounted = hydrateThread(read("mounted"), ref, 101);
  const owner = repository(fixture.cache, 2, mounted);
  const localQuery = vi.spyOn(fixture.cache, "getLatestWindow");
  const reader = owner.attachReader();

  expect(reader.getSnapshot()).toBe(mounted);
  expect(owner.getSnapshot()).toBe(mounted);
  expect(localQuery).not.toHaveBeenCalled();
  reader.dispose();
  owner.dispose();
});

it("attaches before its local query and keeps the reader subscribed to later accepted reads", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const owner = repository(fixture.cache);
  owner.acceptRead(read("first"), { lifetime: 1, readId: "read-1", boundary: "read-boundary-1" });
  await owner.settled();
  const reader = owner.attachReader();
  const opened = await reader.openLocal(40);
  expect(opened.status).toBe("current");
  expect(opened.model?.turns.flatMap((turn) => turn.items).map((item) => item.text)).toEqual(["first"]);

  owner.acceptRead(read("second", 2, "item-first"), { lifetime: 1, readId: "read-2", boundary: "read-boundary-2" });
  expect(
    reader
      .getSnapshot()
      ?.turns.flatMap((turn) => turn.items)
      .map((item) => item.text),
  ).toEqual(["second"]);
  expect(owner.acceptRead(read("wrong-ref"), { lifetime: 1, readId: "read-3", boundary: "read-boundary-3" })).toBe(
    false,
  );
  reader.dispose();
  owner.dispose();
});

it("merges an old local query with pending and later accepted versions without regression", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const owner = repository(fixture.cache);
  owner.acceptRead(read("A"), { lifetime: 1, readId: "read-1", boundary: "read-boundary-1" });
  await owner.settled();
  const queryHold = holdNextTransaction([ENTRY_ITEM_STORE, ENTRY_COVERAGE_STORE], "readonly");
  const reader = owner.attachReader();
  const opening = reader.openLocal(40);
  await queryHold.reached;
  owner.acceptRead(read("B", 2), { lifetime: 1, readId: "read-2", boundary: "read-boundary-2" });
  owner.acceptRead(read("C", 3), { lifetime: 1, readId: "read-3", boundary: "read-boundary-3" });
  await owner.settled();
  expect(
    owner
      .getSnapshot()
      ?.history?.turns.flatMap((turn) => turn.items)
      .filter((item) => item.transcriptKey === "item-A")
      .map(encodeTranscriptItem),
  ).toMatchObject([{ identityOnly: true, textOmitted: true }]);
  queryHold.release();
  const opened = await opening;
  expect(opened.model?.turns.flatMap((turn) => turn.items).map((item) => item.text)).toEqual(["A", "B", "C"]);
  expect(opened.status).toBe("current");

  owner.acceptRead(read("D", 4), { lifetime: 1, readId: "read-4", boundary: "read-boundary-4" });
  expect(
    reader
      .getSnapshot()
      ?.turns.flatMap((turn) => turn.items)
      .map((item) => item.text),
  ).toEqual(["A", "B", "C", "D"]);
  reader.dispose();
  owner.dispose();
});

it.each(["reader disposal", "binding invalidation"] as const)(
  "does not publish a held local query after %s",
  async (retirement) => {
    const fixture = createEntryCacheFixture();
    fixtures.push(fixture);
    const owner = repository(fixture.cache);
    owner.acceptRead(read("A"), { lifetime: 1, readId: "read-fence", boundary: "read-fence-boundary" });
    await owner.settled();
    const publications: ThreadModel[] = [];
    owner.subscribe(() => {
      const snapshot = owner.getSnapshot();
      if (snapshot) publications.push(snapshot);
    });
    const queryHold = holdNextTransaction([ENTRY_ITEM_STORE, ENTRY_COVERAGE_STORE], "readonly");
    const reader = owner.attachReader();
    const opening = reader.openLocal(40);
    await queryHold.reached;

    if (retirement === "reader disposal") reader.dispose();
    else owner.invalidate();
    const retiredSnapshot = owner.getSnapshot();
    queryHold.release();
    const result = await opening;

    expect(result.status).toBe("recovery-needed");
    expect(owner.getSnapshot()).toBe(retiredSnapshot);
    expect(publications).toEqual([]);
    if (retirement === "binding invalidation") reader.dispose();
    owner.dispose();
  },
);

it("rejects notification frames from an incompatible ref or lifetime", () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const owner = repository(fixture.cache);
  owner.acceptRead(read("base"), { lifetime: 1, readId: "read-1", boundary: "read-boundary-1" });
  const notification = {
    method: "overlay/upserted",
    params: {
      ref: "other-ref",
      threadId,
      item: { key: "stream-1", kind: "stream", item: { type: "agentMessage", id: "live", text: "live" } },
    },
  } as unknown as AnyNotification;
  expect(owner.accept(notification, { lifetime: 1 })).toBe(false);
  expect(owner.accept(notification, { lifetime: 2 })).toBe(false);
  owner.dispose();
});

it("keeps an ended-round overlay until the repository receives its recorded replacement", () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const owner = repository(fixture.cache);
  owner.acceptRead(read("base"), { lifetime: 1, readId: "read-end-order", boundary: "read-end-order-boundary" });
  const streamKey = "stream:round-1/1:agentMessage";
  owner.accept(
    {
      method: "overlay/upserted",
      params: {
        ref,
        threadId,
        item: {
          key: streamKey,
          kind: "stream",
          turnId: "turn_1",
          roundId: "round-1",
          streamId: "round-1/1",
          item: {
            type: "agentMessage",
            id: "live-handoff",
            turnId: "turn_1",
            roundId: "round-1",
            text: "pending handoff",
            status: "inProgress",
          },
        },
      },
    } as AnyNotification,
    { lifetime: 1 },
  );

  owner.accept({ method: "overlay/end", params: { ref, threadId, roundId: "round-1" } } as AnyNotification, {
    lifetime: 1,
  });
  expect(Object.keys(owner.getSnapshot()?.overlay ?? {})).toEqual([streamKey]);

  expect(
    owner.accept(
      {
        method: "history/updated",
        params: {
          ref,
          threadId,
          bootGeneration: "1",
          epoch: 0,
          snapshot: { incarnation: identity.incarnation, length: 2 },
          items: [
            {
              type: "agentMessage",
              id: "recorded-handoff",
              transcriptKey: "recorded-handoff",
              turnId: "turn_1",
              position: { entry: 2, item: 0 },
              version: 2,
              roundId: "round-1",
              text: "recorded handoff",
            },
          ],
        },
      } as AnyNotification,
      { lifetime: 1 },
    ),
  ).toBe(true);
  expect(owner.getSnapshot()?.overlay).toEqual({});
  expect(owner.getSnapshot()?.turns[0]?.items.map((item) => item.text)).toEqual(["base", "recorded handoff"]);
  owner.dispose();
});

it("keeps the repository's recorded replacement when history arrives before overlay/end", () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const owner = repository(fixture.cache);
  owner.acceptRead(read("base"), { lifetime: 1, readId: "read-end-first", boundary: "read-end-first-boundary" });
  const streamKey = "stream:round-1/1:agentMessage";
  owner.accept(
    {
      method: "overlay/upserted",
      params: {
        ref,
        threadId,
        item: {
          key: streamKey,
          kind: "stream",
          turnId: "turn_1",
          roundId: "round-1",
          streamId: "round-1/1",
          item: {
            type: "agentMessage",
            id: "live-before-history",
            turnId: "turn_1",
            roundId: "round-1",
            text: "pending text",
            status: "inProgress",
          },
        },
      },
    } as AnyNotification,
    { lifetime: 1 },
  );
  owner.accept(
    {
      method: "history/updated",
      params: {
        ref,
        threadId,
        bootGeneration: "1",
        epoch: 0,
        snapshot: { incarnation: identity.incarnation, length: 2 },
        items: [
          {
            type: "agentMessage",
            id: "recorded-before-end",
            transcriptKey: "recorded-before-end",
            turnId: "turn_1",
            position: { entry: 2, item: 0 },
            version: 2,
            roundId: "round-1",
            text: "recorded text",
          },
        ],
      },
    } as AnyNotification,
    { lifetime: 1 },
  );
  expect(owner.getSnapshot()?.overlay).toEqual({});
  expect(owner.getSnapshot()?.turns[0]?.items.map((item) => item.text)).toEqual(["base", "recorded text"]);

  owner.accept({ method: "overlay/end", params: { ref, threadId, roundId: "round-1" } } as AnyNotification, {
    lifetime: 1,
  });
  expect(owner.getSnapshot()?.overlay).toEqual({});
  expect(owner.getSnapshot()?.turns[0]?.items.map((item) => item.text)).toEqual(["base", "recorded text"]);
  owner.dispose();
});

it("publishes a recorded item and covered stream retirement before the real IDB transaction commits", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const owner = repository(fixture.cache);
  const snapshots: Array<{ text: string[]; overlay: string[] }> = [];
  owner.acceptRead(read("base"), { lifetime: 1, readId: "read-1", boundary: "read-boundary-1" });
  await owner.settled();
  const writeHold = holdNextEntryPut("recorded-item");
  const overlay: AnyNotification = {
    method: "overlay/upserted",
    params: {
      ref,
      threadId,
      item: {
        key: "stream:round-1/1:agentMessage",
        kind: "stream",
        turnId: "turn_1",
        roundId: "round-1",
        streamId: "round-1/1",
        item: {
          type: "agentMessage",
          id: "stream-item",
          turnId: "turn_1",
          roundId: "round-1",
          text: "live",
          status: "inProgress",
        },
      },
    },
  };
  owner.accept(overlay, { lifetime: 1 });
  owner.accept(
    {
      method: "overlay/delta",
      params: { ref, threadId, key: "stream:round-1/1:agentMessage", field: "text", delta: " chunk" },
    } as AnyNotification,
    { lifetime: 1 },
  );
  for (const item of [
    {
      key: "stream:round-1/2:reasoning",
      kind: "stream",
      turnId: "turn_1",
      roundId: "round-1",
      streamId: "round-1/2",
      item: { type: "reasoning", id: "reasoning-live", turnId: "turn_1", roundId: "round-1", text: "thinking" },
    },
    {
      key: "preview:preview-call",
      kind: "preview",
      turnId: "turn_1",
      roundId: "round-1",
      streamId: "round-1/2",
      callId: "preview-call",
      item: { type: "agentMessage", id: "preview-live", turnId: "turn_1", callId: "preview-call", text: "preview" },
    },
    {
      key: "tool:tool-history",
      kind: "tool",
      turnId: "turn_1",
      roundId: "round-1",
      callId: "tool-call",
      historyKey: "tool-history",
      item: {
        type: "commandExecution",
        id: "tool-live",
        turnId: "turn_1",
        roundId: "round-1",
        callId: "tool-call",
        output: "tool output",
        status: "inProgress",
      },
    },
  ]) {
    owner.accept({ method: "overlay/upserted", params: { ref, threadId, item } } as unknown as AnyNotification, {
      lifetime: 1,
    });
  }
  expect(
    owner
      .getSnapshot()
      ?.turns.flatMap((turn) => turn.items)
      .some((item) => item.text === "live chunk"),
  ).toBe(true);
  owner.subscribe(() => {
    const model = owner.getSnapshot();
    snapshots.push({
      text: model?.turns.flatMap((turn) => turn.items).map((item) => item.text) ?? [],
      overlay: Object.keys(model?.overlay ?? {}),
    });
  });
  const replacement: AnyNotification = {
    method: "history/updated",
    params: {
      ref,
      threadId,
      bootGeneration: "1",
      epoch: 0,
      snapshot: { incarnation: identity.incarnation, length: 2 },
      items: [
        {
          type: "agentMessage",
          id: "recorded-item",
          transcriptKey: "recorded-item",
          turnId: "turn_1",
          position: { entry: 2, item: 0 },
          version: 2,
          roundId: "round-1",
          text: "recorded",
        },
        {
          type: "reasoning",
          id: "reasoning-recorded",
          transcriptKey: "reasoning-recorded",
          turnId: "turn_1",
          position: { entry: 3, item: 0 },
          version: 2,
          roundId: "round-1",
          text: "reasoning recorded",
        },
        {
          type: "agentMessage",
          id: "preview-recorded",
          transcriptKey: "preview-recorded",
          turnId: "turn_1",
          position: { entry: 4, item: 0 },
          version: 2,
          callId: "preview-call",
          text: "preview recorded",
        },
        {
          type: "commandExecution",
          id: "tool-recorded",
          transcriptKey: "tool-history",
          turnId: "turn_1",
          position: { entry: 5, item: 0 },
          version: 2,
          callId: "tool-call",
          output: "tool recorded",
          status: "completed",
          completedAtEntry: 5,
        },
      ],
    },
  };
  expect(owner.accept(replacement, { lifetime: 1 })).toBe(true);
  expect(snapshots).toHaveLength(1);
  expect(snapshots[0]).toMatchObject({
    text: expect.arrayContaining(["recorded", "reasoning recorded", "preview recorded"]),
    overlay: [],
  });
  await writeHold.reached;
  let settled = false;
  void owner.settled().then(() => {
    settled = true;
  });
  await Promise.resolve();
  expect(settled).toBe(false);
  writeHold.commit();
  await owner.settled();
  await expect(fixture.cache.getLatest(historyKey, 40)).resolves.toMatchObject([
    { transcriptKey: "item-base" },
    { transcriptKey: "recorded-item", item: { text: "recorded" } },
    { transcriptKey: "reasoning-recorded", item: { text: "reasoning recorded" } },
    { transcriptKey: "preview-recorded", item: { text: "preview recorded" } },
    { transcriptKey: "tool-history", item: { output: "tool recorded" } },
  ]);
  await expect(fixture.cache.getCoverage(historyKey, "suffix:read-1")).resolves.toMatchObject({
    representedSnapshot: 2,
  });
  expect(snapshots).toHaveLength(1);
  owner.dispose();
});

it("keeps accepted publication after the real entry transaction aborts", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const owner = repository(fixture.cache);
  owner.acceptRead(read("base"), { lifetime: 1, readId: "read-abort", boundary: "read-abort-boundary" });
  await owner.settled();
  const reader = owner.attachReader();
  const snapshots: string[][] = [];
  owner.subscribe(() =>
    snapshots.push(
      owner
        .getSnapshot()
        ?.turns.flatMap((turn) => turn.items)
        .map((item) => item.text) ?? [],
    ),
  );
  const writeHold = holdNextEntryPut("aborted-item");
  owner.accept(
    {
      method: "history/updated",
      params: {
        ref,
        threadId,
        bootGeneration: "1",
        epoch: 0,
        snapshot: { incarnation: identity.incarnation, length: 2 },
        items: [
          {
            type: "agentMessage",
            id: "aborted-item",
            transcriptKey: "aborted-item",
            turnId: "turn_1",
            position: { entry: 2, item: 0 },
            version: 2,
            text: "visible but aborted",
          },
        ],
      },
    } as AnyNotification,
    { lifetime: 1 },
  );
  await writeHold.reached;
  expect(snapshots).toHaveLength(1);
  expect(snapshots[0]).toContain("visible but aborted");
  writeHold.abort();
  await owner.settled();

  expect(
    reader
      .getSnapshot()
      ?.turns.flatMap((turn) => turn.items)
      .map((item) => item.text),
  ).toContain("visible but aborted");
  await expect(fixture.cache.getLatest(historyKey, 40)).resolves.toMatchObject([{ transcriptKey: "item-base" }]);
  await expect(fixture.cache.getCoverage(historyKey, "read:read-abort")).resolves.toMatchObject({
    representedSnapshot: 1,
  });
  await expect(fixture.cache.getCoverage(historyKey, "suffix:read-abort")).resolves.toBeUndefined();
  expect(snapshots).toHaveLength(1);
  reader.dispose();
  owner.dispose();
});

it("refuses a stale-epoch entry write after real Clear and keeps mounted content current only for display", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const owner = repository(fixture.cache);
  owner.acceptRead(read("base"), { lifetime: 1, readId: "read-clear", boundary: "read-clear-boundary" });
  await owner.settled();
  const reader = owner.attachReader();
  const clearCache = new SessionCacheIndexedDB({
    indexedDB: fixture.indexedDB,
    databaseName: fixture.name,
    databaseVersion: 3,
  });
  expect(await clearCache.clear()).toEqual({ committed: true, epoch: 1 });
  clearCache.close();

  owner.accept(
    {
      method: "history/updated",
      params: {
        ref,
        threadId,
        bootGeneration: "1",
        epoch: 0,
        snapshot: { incarnation: identity.incarnation, length: 2 },
        items: [
          {
            type: "agentMessage",
            id: "after-clear",
            transcriptKey: "after-clear",
            turnId: "turn_1",
            position: { entry: 2, item: 0 },
            version: 2,
            text: "visible after clear",
          },
        ],
      },
    } as AnyNotification,
    { lifetime: 1 },
  );
  await owner.settled();

  expect(
    reader
      .getSnapshot()
      ?.turns.flatMap((turn) => turn.items)
      .map((item) => item.text),
  ).toContain("visible after clear");
  expect((await reader.openLocal(40)).status).toBe("recovery-needed");
  await expect(fixture.cache.getLatest(historyKey, 40)).resolves.toMatchObject([{ transcriptKey: "item-base" }]);
  await expect(fixture.cache.getCoverage(historyKey, "suffix:read-clear")).resolves.toBeUndefined();
  reader.dispose();
  owner.dispose();
});

it("extends producer-complete suffix coverage across an old update and distant new entry", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const owner = repository(fixture.cache);
  const baseline = readMany(50);
  baseline.snapshot = { incarnation: identity.incarnation, length: 8_004 };
  owner.acceptRead(baseline, { lifetime: 1, readId: "read-gap", boundary: "read-gap-boundary" });
  await owner.settled();
  const notification: AnyNotification = {
    method: "history/updated",
    params: {
      ref,
      threadId,
      bootGeneration: "1",
      epoch: 0,
      // ChangedSince(published) returns every item/turn changed since the
      // previous byte boundary; its length may jump by many transcript bytes.
      snapshot: { incarnation: identity.incarnation, length: 19_431 },
      items: [
        {
          type: "agentMessage",
          id: "old-item",
          transcriptKey: "item-2",
          turnId: "turn_1",
          position: { entry: 2, item: 0 },
          version: 52,
          text: "old replacement",
        },
        {
          type: "agentMessage",
          id: "new-item",
          transcriptKey: "item-51",
          turnId: "turn_1",
          position: { entry: 51, item: 0 },
          version: 53,
          text: "distant append",
        },
      ],
    },
  };
  owner.accept(notification, { lifetime: 1 });
  await owner.settled();
  await expect(fixture.cache.getCoverage(historyKey, "suffix:read-gap")).resolves.toMatchObject({
    representedSnapshot: 19_431,
    start: { entry: 1 },
    end: { entry: 51 },
  });
  const reader = owner.attachReader();
  expect((await reader.openLocal(40)).status).toBe("current");
  reader.dispose();
  owner.dispose();
});

it("does not certify isolated old-plus-distant input after history authority breaks", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const owner = repository(fixture.cache);
  const baseline = readMany(50);
  baseline.snapshot = { incarnation: identity.incarnation, length: 8_004 };
  owner.acceptRead(baseline, { lifetime: 1, readId: "read-broken", boundary: "read-broken-boundary" });
  await owner.settled();

  expect(
    owner.accept(
      {
        method: "history/updated",
        params: {
          ref,
          threadId,
          bootGeneration: "2",
          epoch: 0,
          snapshot: { incarnation: identity.incarnation, length: 12_000 },
          items: [],
        },
      } as AnyNotification,
      { lifetime: 1 },
    ),
  ).toBe(false);

  expect(
    owner.accept(
      {
        method: "history/updated",
        params: {
          ref,
          threadId,
          bootGeneration: "1",
          epoch: 0,
          snapshot: { incarnation: identity.incarnation, length: 19_431 },
          items: [
            {
              type: "agentMessage",
              id: "isolated-old",
              transcriptKey: "item-2",
              turnId: "turn_1",
              position: { entry: 2, item: 0 },
              version: 52,
              text: "isolated old update",
            },
            {
              type: "agentMessage",
              id: "isolated-distant",
              transcriptKey: "item-51",
              turnId: "turn_1",
              position: { entry: 51, item: 0 },
              version: 53,
              text: "isolated distant entry",
            },
          ],
        },
      } as AnyNotification,
      { lifetime: 1 },
    ),
  ).toBe(true);
  await owner.settled();

  await expect(fixture.cache.getCoverage(historyKey, "suffix:read-broken")).resolves.toBeUndefined();
  await expect(fixture.cache.getCoverage(historyKey, "read:read-broken")).resolves.toMatchObject({
    representedSnapshot: 8_004,
    end: { entry: 50 },
  });
  const rows = await fixture.cache.getLatest(historyKey, 100);
  expect(rows).toHaveLength(51);
  expect(rows[0]).toMatchObject({ transcriptKey: "item-1", item: { text: "row-1" } });
  expect(rows.find((row) => row.transcriptKey === "item-2")).toMatchObject({
    item: { text: "isolated old update" },
  });
  expect(rows.at(-1)).toMatchObject({ transcriptKey: "item-51", item: { text: "isolated distant entry" } });
  const reader = owner.attachReader();
  expect((await reader.openLocal(40)).status).toBe("recovery-needed");
  reader.dispose();
  owner.dispose();
});

it("extends coverage from accepted provenance for sparse positions and turn-only updates", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const owner = repository(fixture.cache);
  const baseline = readMany(2);
  baseline.thread.turns![0]!.items = [
    {
      type: "agentMessage",
      id: "baseline",
      transcriptKey: "baseline",
      turnId: "turn_1",
      position: { entry: 100, item: 2 },
      version: 1,
      text: "baseline",
    },
  ];
  // Snapshot.length is the transcript-byte boundary, not a notification counter.
  baseline.snapshot = { incarnation: identity.incarnation, length: 1_376 };
  owner.acceptRead(baseline, { lifetime: 1, readId: "read-provenance", boundary: "read-provenance-boundary" });
  await owner.settled();

  expect(
    owner.accept(
      {
        method: "history/updated",
        params: {
          ref,
          threadId,
          bootGeneration: "1",
          epoch: 0,
          snapshot: { incarnation: identity.incarnation, length: 9_586 },
          items: [
            {
              type: "agentMessage",
              id: "sparse-append",
              transcriptKey: "sparse-append",
              turnId: "turn_1",
              position: { entry: 700, item: 4 },
              version: 3,
              text: "sparse append",
            },
          ],
        },
      } as AnyNotification,
      { lifetime: 1 },
    ),
  ).toBe(true);
  await owner.settled();
  await expect(fixture.cache.getCoverage(historyKey, "suffix:read-provenance")).resolves.toMatchObject({
    representedSnapshot: 9_586,
    start: { entry: 100, item: 2 },
    end: { entry: 700, item: 4 },
  });
  const firstSuffix = await fixture.cache.getCoverage(historyKey, "suffix:read-provenance");

  expect(
    owner.accept(
      {
        method: "history/updated",
        params: {
          ref,
          threadId,
          bootGeneration: "1",
          epoch: 0,
          snapshot: { incarnation: identity.incarnation, length: 9_586 },
          turns: [{ id: "turn_1", status: "completed", version: 4, itemsView: "full" }],
        },
      } as AnyNotification,
      { lifetime: 1 },
    ),
  ).toBe(true);
  await owner.settled();
  const sameLengthSuffix = await fixture.cache.getCoverage(historyKey, "suffix:read-provenance");
  expect(sameLengthSuffix).toMatchObject({
    representedSnapshot: 9_586,
    start: { entry: 100, item: 2 },
    end: { entry: 700, item: 4 },
  });
  expect(sameLengthSuffix?.boundary).not.toBe(firstSuffix?.boundary);
  owner.dispose();
});

it("requires monotonic producer byte boundaries before extending a suffix", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const owner = repository(fixture.cache);
  const baseline = read("baseline");
  baseline.snapshot = { incarnation: identity.incarnation, length: 4_096 };
  owner.acceptRead(baseline, { lifetime: 1, readId: "read-decreasing", boundary: "read-decreasing-boundary" });
  await owner.settled();

  owner.accept(
    {
      method: "history/updated",
      params: {
        ref,
        threadId,
        bootGeneration: "1",
        epoch: 0,
        snapshot: { incarnation: identity.incarnation, length: 3_900 },
        items: [
          {
            type: "agentMessage",
            id: "baseline-update",
            transcriptKey: "item-baseline",
            turnId: "turn_1",
            position: { entry: 1, item: 0 },
            version: 2,
            text: "updated under a stale byte boundary",
          },
        ],
      },
    } as AnyNotification,
    { lifetime: 1 },
  );
  await owner.settled();

  await expect(fixture.cache.getCoverage(historyKey, "suffix:read-decreasing")).resolves.toBeUndefined();
  const reader = owner.attachReader();
  expect((await reader.openLocal(40)).status).toBe("recovery-needed");
  reader.dispose();
  owner.dispose();
});

it("keeps live overlay in memory without storing its payload beside settled recorded rows", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const owner = repository(fixture.cache);
  owner.acceptRead(read("recorded"), { lifetime: 1, readId: "read-overlay", boundary: "read-overlay-boundary" });
  await owner.settled();
  const live: AnyNotification = {
    method: "overlay/upserted",
    params: {
      ref,
      threadId,
      item: {
        key: "stream:round-live/1:agentMessage",
        kind: "stream",
        turnId: "turn_1",
        roundId: "round-live",
        streamId: "round-live/1",
        item: {
          type: "agentMessage",
          id: "live-only",
          turnId: "turn_1",
          roundId: "round-live",
          text: "not recorded yet",
          status: "inProgress",
        },
      },
    },
  };
  expect(owner.accept(live, { lifetime: 1 })).toBe(true);
  await owner.settled();
  expect(Object.keys(owner.getSnapshot()?.overlay ?? {})).toContain("stream:round-live/1:agentMessage");
  expect(
    owner
      .getSnapshot()
      ?.turns.flatMap((turn) => turn.items)
      .some((item) => item.text === "recorded"),
  ).toBe(false);
  await expect(fixture.cache.getLatest(historyKey, 40)).resolves.toMatchObject([
    { transcriptKey: "item-recorded", item: { text: "recorded" } },
  ]);
  expect(JSON.stringify(await fixture.cache.getLatest(historyKey, 40))).not.toContain("not recorded yet");
  owner.dispose();
});
