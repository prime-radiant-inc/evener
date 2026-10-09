import {
  type AnyNotification,
  encodeTranscriptItem,
  hydrateThread,
  invalidateHistory,
  type ThreadItem,
  type ThreadModel,
  type ThreadReadResponse,
} from "@evener/appwire-client";
import { IDBDatabase, IDBObjectStore } from "fake-indexeddb";
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

function historyUpdate(label: string, entry: number): AnyNotification {
  return {
    method: "history/updated",
    params: {
      ref,
      threadId,
      bootGeneration: "1",
      epoch: 0,
      snapshot: { incarnation: identity.incarnation, length: entry },
      items: [
        {
          type: "agentMessage",
          id: `item-${label}`,
          transcriptKey: `item-${label}`,
          turnId: "turn_1",
          position: { entry, item: 0 },
          version: entry,
          text: label,
        },
      ],
    },
  } as AnyNotification;
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
    seedAuthority: seedModel?.history
      ? {
          trustedCurrentSource: true,
          bootGeneration: seedModel.history.bootGeneration,
          epoch: seedModel.history.epoch,
          incarnation: seedModel.history.incarnation ?? "",
          snapshotLength: seedModel.history.length,
        }
      : undefined,
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

function gateEntryCommit(cache: ReturnType<typeof createEntryCacheFixture>["cache"], transcriptKey: string) {
  const commit = cache.commitBatch.bind(cache);
  let released = false;
  let release!: () => void;
  let markReached!: () => void;
  const gate = new Promise<void>((resolve) => {
    release = resolve;
  });
  const reached = new Promise<void>((resolve) => {
    markReached = resolve;
  });
  let previous = Promise.resolve();
  const spy = vi.spyOn(cache, "commitBatch").mockImplementation((batch) => {
    const predecessor = previous;
    let finish!: () => void;
    previous = new Promise<void>((resolve) => {
      finish = resolve;
    });
    return (async () => {
      await predecessor;
      try {
        if (batch.items.some((row) => row.transcriptKey === transcriptKey)) {
          markReached();
          await gate;
        }
        return await commit(batch);
      } finally {
        finish();
      }
    })();
  });
  return {
    reached,
    release() {
      released = true;
      release();
      spy.mockRestore();
    },
    isReleased: () => released,
  };
}

const coveredOverlayKeys = [
  "stream:covered-round/1:agentMessage",
  "stream:covered-round/2:reasoning",
  "preview:covered-call",
  "tool:covered-tool",
];
const uncoveredOverlayKeys = [
  "stream:uncovered-round/1:agentMessage",
  "stream:uncovered-round/2:reasoning",
  "preview:uncovered-call",
  "tool:uncovered-tool",
];

function mixedHandoffOverlays(): AnyNotification[] {
  const slots = [
    {
      key: coveredOverlayKeys[0],
      kind: "stream",
      turnId: "turn_1",
      roundId: "covered-round",
      streamId: "covered-round/1",
      item: {
        type: "agentMessage",
        id: "covered-text-live",
        turnId: "turn_1",
        roundId: "covered-round",
        text: "covered live text",
        status: "inProgress",
      },
    },
    {
      key: coveredOverlayKeys[1],
      kind: "stream",
      turnId: "turn_1",
      roundId: "covered-round",
      streamId: "covered-round/2",
      item: {
        type: "reasoning",
        id: "covered-reasoning-live",
        turnId: "turn_1",
        roundId: "covered-round",
        text: "covered live reasoning",
        status: "inProgress",
      },
    },
    {
      key: coveredOverlayKeys[2],
      kind: "preview",
      turnId: "turn_1",
      roundId: "covered-round",
      callId: "covered-call",
      item: {
        type: "agentMessage",
        id: "covered-preview-live",
        turnId: "turn_1",
        callId: "covered-call",
        text: "covered preview",
      },
    },
    {
      key: coveredOverlayKeys[3],
      kind: "tool",
      turnId: "turn_1",
      roundId: "covered-round",
      callId: "covered-tool-call",
      historyKey: "covered-tool",
      item: {
        type: "commandExecution",
        id: "covered-tool-live",
        turnId: "turn_1",
        callId: "covered-tool-call",
        output: "covered live tool",
        status: "inProgress",
      },
    },
    {
      key: uncoveredOverlayKeys[0],
      kind: "stream",
      turnId: "turn_1",
      roundId: "uncovered-round",
      streamId: "uncovered-round/1",
      item: {
        type: "agentMessage",
        id: "uncovered-text-live",
        turnId: "turn_1",
        roundId: "uncovered-round",
        text: "uncovered live text",
        status: "inProgress",
      },
    },
    {
      key: uncoveredOverlayKeys[1],
      kind: "stream",
      turnId: "turn_1",
      roundId: "uncovered-round",
      streamId: "uncovered-round/2",
      item: {
        type: "reasoning",
        id: "uncovered-reasoning-live",
        turnId: "turn_1",
        roundId: "uncovered-round",
        text: "uncovered live reasoning",
        status: "inProgress",
      },
    },
    {
      key: uncoveredOverlayKeys[2],
      kind: "preview",
      turnId: "turn_1",
      roundId: "uncovered-round",
      callId: "uncovered-call",
      item: {
        type: "agentMessage",
        id: "uncovered-preview-live",
        turnId: "turn_1",
        callId: "uncovered-call",
        text: "uncovered preview",
      },
    },
    {
      key: uncoveredOverlayKeys[3],
      kind: "tool",
      turnId: "turn_1",
      roundId: "uncovered-round",
      callId: "uncovered-tool-call",
      historyKey: "uncovered-tool",
      item: {
        type: "commandExecution",
        id: "uncovered-tool-live",
        turnId: "turn_1",
        callId: "uncovered-tool-call",
        output: "uncovered live tool",
        status: "inProgress",
      },
    },
  ];
  return slots.map(
    (item) => ({ method: "overlay/upserted", params: { ref, threadId, item } }) as unknown as AnyNotification,
  );
}

function mixedHandoffHistory(): AnyNotification {
  return {
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
          id: "covered-text",
          transcriptKey: "covered-text",
          turnId: "turn_1",
          position: { entry: 2, item: 0 },
          version: 2,
          roundId: "covered-round",
          text: "recorded covered text",
        },
        {
          type: "reasoning",
          id: "covered-reasoning",
          transcriptKey: "covered-reasoning",
          turnId: "turn_1",
          position: { entry: 3, item: 0 },
          version: 2,
          roundId: "covered-round",
          text: "recorded covered reasoning",
        },
        {
          type: "agentMessage",
          id: "covered-preview",
          transcriptKey: "covered-preview",
          turnId: "turn_1",
          position: { entry: 4, item: 0 },
          version: 2,
          callId: "covered-call",
          text: "recorded covered preview",
        },
        {
          type: "commandExecution",
          id: "covered-tool",
          transcriptKey: "covered-tool",
          turnId: "turn_1",
          position: { entry: 5, item: 0 },
          version: 2,
          callId: "covered-tool-call",
          output: "recorded covered tool",
          status: "completed",
          completedAtEntry: 5,
        },
      ],
    },
  } as unknown as AnyNotification;
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

it("persists recorded tool fields from a complete read, not its live display overlay", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const owner = repository(fixture.cache);
  const response = read("tool read");
  response.thread.turns![0]!.items = [
    {
      type: "commandExecution",
      id: "tool-display-id",
      transcriptKey: "tool-recorded-key",
      turnId: "turn_1",
      position: { entry: 1, item: 0 },
      version: 1,
      callId: "call-1",
      status: "inProgress",
      output: "recorded-output",
      outputImages: [{ source: "recorded", name: "recorded.png" }],
    },
  ];
  response.overlay = [
    {
      key: "tool:tool-recorded-key",
      kind: "tool",
      turnId: "turn_1",
      roundId: "round-1",
      callId: "call-1",
      historyKey: "tool-recorded-key",
      item: {
        type: "commandExecution",
        id: "tool-display-id",
        turnId: "turn_1",
        roundId: "round-1",
        callId: "call-1",
        status: "inProgress",
        output: "LIVE_ONLY",
        outputImages: [{ source: "live", name: "live.png" }],
      },
    },
  ];

  owner.acceptRead(response, { lifetime: 1, readId: "read-tool-overlay", boundary: "read-tool-overlay-boundary" });
  await owner.settled();

  await expect(fixture.cache.getLatest(historyKey, 40)).resolves.toMatchObject([
    {
      transcriptKey: "tool-recorded-key",
      item: {
        status: "inProgress",
        output: "recorded-output",
        outputImages: [{ source: "recorded", name: "recorded.png" }],
      },
    },
  ]);
  expect(owner.getSnapshot()?.turns.flatMap((turn) => turn.items)).toMatchObject([
    { transcriptKey: "tool-recorded-key", output: "LIVE_ONLY", outputImages: [{ name: "live.png" }] },
  ]);
  owner.dispose();
});

it("persists the accepted recorded tool update while an unfinished tool overlay remains live", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const owner = repository(fixture.cache);
  const response = read("tool read");
  response.thread.turns![0]!.items = [
    {
      type: "commandExecution",
      id: "tool-display-id",
      transcriptKey: "tool-recorded-key",
      turnId: "turn_1",
      position: { entry: 1, item: 0 },
      version: 1,
      callId: "call-1",
      status: "inProgress",
      output: "initial-recorded-output",
    },
  ];
  owner.acceptRead(response, { lifetime: 1, readId: "read-tool-update", boundary: "read-tool-update-boundary" });
  await owner.settled();
  owner.accept(
    {
      method: "overlay/upserted",
      params: {
        ref,
        threadId,
        item: {
          key: "tool:tool-recorded-key",
          kind: "tool",
          turnId: "turn_1",
          roundId: "round-1",
          callId: "call-1",
          historyKey: "tool-recorded-key",
          item: {
            type: "commandExecution",
            id: "tool-display-id",
            turnId: "turn_1",
            roundId: "round-1",
            callId: "call-1",
            status: "inProgress",
            output: "LIVE_ONLY",
            outputImages: [{ source: "live", name: "live.png" }],
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
            type: "commandExecution",
            id: "tool-display-id",
            transcriptKey: "tool-recorded-key",
            turnId: "turn_1",
            position: { entry: 1, item: 0 },
            version: 2,
            callId: "call-1",
            status: "inProgress",
            output: "updated-recorded-output",
            outputImages: [{ source: "recorded", name: "updated.png" }],
          },
        ],
      },
    } as AnyNotification,
    { lifetime: 1 },
  );
  await owner.settled();

  await expect(fixture.cache.getLatest(historyKey, 40)).resolves.toMatchObject([
    {
      transcriptKey: "tool-recorded-key",
      version: 2,
      item: {
        status: "inProgress",
        output: "updated-recorded-output",
        outputImages: [{ source: "recorded", name: "updated.png" }],
      },
    },
  ]);
  expect(owner.getSnapshot()?.turns.flatMap((turn) => turn.items)).toMatchObject([
    { transcriptKey: "tool-recorded-key", output: "LIVE_ONLY", outputImages: [{ name: "live.png" }] },
  ]);
  owner.dispose();
});

it("uses a supplied transcript key before a colliding display ID when extracting a notification row", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const owner = repository(fixture.cache);
  const response = read("identity seed");
  response.thread.turns![0]!.items = [
    {
      type: "agentMessage",
      id: "colliding-display-id",
      transcriptKey: "other-record",
      turnId: "turn_1",
      position: { entry: 1, item: 0 },
      version: 1,
      text: "other record",
    },
    {
      type: "agentMessage",
      id: "target-display-id",
      transcriptKey: "target-record",
      turnId: "turn_1",
      position: { entry: 2, item: 0 },
      version: 1,
      text: "target record",
    },
  ];
  owner.acceptRead(response, { lifetime: 1, readId: "read-identity", boundary: "read-identity-boundary" });
  await owner.settled();

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
            id: "colliding-display-id",
            transcriptKey: "target-record",
            turnId: "turn_1",
            position: { entry: 2, item: 0 },
            version: 2,
            text: "target update",
          },
        ],
      },
    } as AnyNotification,
    { lifetime: 1 },
  );
  await owner.settled();

  const rows = await fixture.cache.getLatest(historyKey, 40);
  expect(rows.find((row) => row.transcriptKey === "target-record")).toMatchObject({
    transcriptKey: "target-record",
    item: { text: "target update" },
  });
  expect(rows.find((row) => row.transcriptKey === "other-record")).toMatchObject({
    transcriptKey: "other-record",
    item: { text: "other record" },
  });
  owner.dispose();
});

it("commits interleaved read coverage through the greatest transcript position", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const owner = repository(fixture.cache);
  const response = read("interleaved", 20);
  response.thread.turns = [
    {
      id: "turn-a",
      itemsView: "full",
      status: "completed",
      version: 10,
      items: [
        {
          type: "agentMessage",
          id: "a-1",
          transcriptKey: "a-1",
          turnId: "turn-a",
          position: { entry: 1, item: 0 },
          version: 1,
          text: "a1",
        },
        {
          type: "agentMessage",
          id: "a-10",
          transcriptKey: "a-10",
          turnId: "turn-a",
          position: { entry: 10, item: 0 },
          version: 10,
          text: "a10",
        },
      ],
    },
    {
      id: "turn-b",
      itemsView: "full",
      status: "completed",
      version: 3,
      items: [
        {
          type: "agentMessage",
          id: "b-2",
          transcriptKey: "b-2",
          turnId: "turn-b",
          position: { entry: 2, item: 0 },
          version: 2,
          text: "b2",
        },
        {
          type: "agentMessage",
          id: "b-3",
          transcriptKey: "b-3",
          turnId: "turn-b",
          position: { entry: 3, item: 0 },
          version: 3,
          text: "b3",
        },
      ],
    },
  ];
  owner.acceptRead(response, { lifetime: 1, readId: "read-interleaved", boundary: "read-interleaved-boundary" });
  await owner.settled();

  await expect(fixture.cache.getCoverage(historyKey, "read:read-interleaved")).resolves.toMatchObject({
    start: { entry: 1 },
    end: { entry: 10 },
  });
  await expect(fixture.cache.getLatest(historyKey, 40)).resolves.toMatchObject([
    { transcriptKey: "a-1" },
    { transcriptKey: "b-2" },
    { transcriptKey: "b-3" },
    { transcriptKey: "a-10" },
  ]);
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

it("opens the latest window after repeated committed live appends pass the initial window", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const owner = repository(fixture.cache);
  const baseline = readMany(45);
  baseline.snapshot = { incarnation: identity.incarnation, length: 8_004 };
  owner.acceptRead(baseline, {
    lifetime: 1,
    readId: "read-repeated-suffix",
    boundary: "read-repeated-suffix-boundary",
  });
  await owner.settled();

  for (let batch = 0; batch < 5; batch += 1) {
    const firstEntry = 46 + batch * 9;
    const items = Array.from({ length: 9 }, (_, index) => {
      const entry = firstEntry + index;
      return {
        type: "agentMessage",
        id: `live-${entry}`,
        transcriptKey: `live-${entry}`,
        turnId: "turn_1",
        position: { entry, item: 0 },
        version: entry,
        text: `live row ${entry}`,
      };
    });
    expect(
      owner.accept(
        {
          method: "history/updated",
          params: {
            ref,
            threadId,
            bootGeneration: "1",
            epoch: 0,
            snapshot: { incarnation: identity.incarnation, length: 8_004 + (batch + 1) * 100 },
            items,
          },
        } as AnyNotification,
        { lifetime: 1 },
      ),
    ).toBe(true);
    await owner.settled();
  }

  const durable = await fixture.cache.getLatest(historyKey, 100);
  expect(durable).toHaveLength(90);
  expect(durable.slice(-45).map((row) => row.transcriptKey)).toEqual(
    Array.from({ length: 45 }, (_, index) => `live-${index + 46}`),
  );
  const reader = owner.attachReader();
  const opened = await reader.openLocal(40);
  expect(opened.status).toBe("current");
  expect(
    opened.model?.turns
      .flatMap((turn) => turn.items)
      .map((item) => item.text)
      .filter(Boolean),
  ).toEqual(Array.from({ length: 40 }, (_, index) => `live row ${index + 51}`));
  expect(opened.before).toEqual({ entry: 51, item: 0 });
  await expect(fixture.cache.getCoverage(historyKey, "suffix:read-repeated-suffix")).resolves.toMatchObject({
    representedSnapshot: 8_504,
    end: { entry: 90 },
  });
  reader.dispose();
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

it("keeps local opening recovery-needed after a reducer resync invalidation", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const owner = repository(fixture.cache);
  owner.acceptRead(read("baseline"), { lifetime: 1, readId: "read-resync", boundary: "read-resync-boundary" });
  await owner.settled();
  const reader = owner.attachReader();

  owner.accept(
    {
      method: "evener/thread/resync",
      params: { ref, threadId, bootGeneration: "1", epoch: 1 },
    } as AnyNotification,
    { lifetime: 1 },
  );

  expect((await reader.openLocal(40)).status).toBe("recovery-needed");
  reader.dispose();
  owner.dispose();
});

it("does not let an old-generation read erase a newer history authority signal", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const owner = repository(fixture.cache);
  owner.acceptRead(read("baseline"), {
    lifetime: 1,
    readId: "read-new-authority",
    boundary: "read-new-authority-boundary",
  });
  await owner.settled();
  const reader = owner.attachReader();

  owner.accept(
    {
      method: "history/updated",
      params: {
        ref,
        threadId,
        bootGeneration: "2",
        epoch: 0,
        snapshot: { incarnation: identity.incarnation, length: 2 },
        items: [],
      },
    } as AnyNotification,
    { lifetime: 1 },
  );
  const oldRead = read("old-generation", 2);
  oldRead.requestGeneration = 0;

  expect(owner.acceptRead(oldRead, { lifetime: 1, readId: "old-read", boundary: "old-read-boundary" })).toBe(false);
  expect((await reader.openLocal(40)).status).toBe("recovery-needed");
  reader.dispose();
  owner.dispose();
});

it("accepts a matching recorded append into a seeded model's current source authority", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const seed = hydrateThread(read("seed", 1), ref, 100);
  const owner = repository(fixture.cache, 2, seed);

  const accepted = owner.accept(
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
            id: "item-seed-append",
            transcriptKey: "item-seed-append",
            turnId: "turn_1",
            position: { entry: 2, item: 0 },
            version: 2,
            text: "accepted from seed authority",
          },
        ],
      },
    } as AnyNotification,
    { lifetime: 2 },
  );

  expect(accepted).toBe(true);
  expect(
    owner
      .getSnapshot()
      ?.turns.flatMap((turn) => turn.items)
      .map((item) => item.text),
  ).toContain("accepted from seed authority");
  await owner.settled();
  const cached = await fixture.cache.getLatestWindow(historyKey, 40);
  expect(cached.coverage).toEqual([]);
  expect(cached.complete).toBe(false);
  owner.dispose();
});

it("does not certify stale cached coverage as current for a newer invalidated seed", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const writer = repository(fixture.cache);
  writer.acceptRead(read("old cache", 1), {
    lifetime: 1,
    readId: "read-old-cache",
    boundary: "read-old-cache-boundary",
  });
  await writer.settled();
  writer.dispose();

  const invalidSeed = invalidateHistory(hydrateThread(read("new invalid seed", 3), ref, 100));
  const owner = repository(fixture.cache, 2, invalidSeed);
  const reader = owner.attachReader();

  expect((await reader.openLocal(40)).status).toBe("recovery-needed");
  reader.dispose();
  owner.dispose();
});

it("requires explicit current source authority and cache coverage through a valid seed boundary", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const writer = repository(fixture.cache);
  writer.acceptRead(read("old cache", 1), {
    lifetime: 1,
    readId: "read-seed-old-coverage",
    boundary: "read-seed-old-coverage-boundary",
  });
  await writer.settled();
  writer.dispose();

  const seed = hydrateThread(read("current seed", 3), ref, 100);
  const owner = new TranscriptRepository(fixture.cache, {
    ref,
    threadId,
    historyKey,
    identity,
    clearEpoch: 0,
    lifetime: 2,
    seedModel: seed,
    seedAuthority: {
      trustedCurrentSource: true,
      bootGeneration: seed.history!.bootGeneration,
      epoch: seed.history!.epoch,
      incarnation: seed.history!.incarnation ?? "",
      snapshotLength: seed.history!.length,
    },
    now: () => 100,
  });
  const reader = owner.attachReader();

  expect((await reader.openLocal(40)).status).toBe("recovery-needed");
  reader.dispose();
  owner.dispose();
});

it("does not treat a valid-looking seed as trusted when its caller omits source authority", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const writer = repository(fixture.cache);
  writer.acceptRead(read("cached", 1), {
    lifetime: 1,
    readId: "read-seed-untrusted",
    boundary: "seed-untrusted-boundary",
  });
  await writer.settled();
  writer.dispose();

  const seed = hydrateThread(read("untrusted seed", 1), ref, 100);
  const owner = new TranscriptRepository(fixture.cache, {
    ref,
    threadId,
    historyKey,
    identity,
    clearEpoch: 0,
    lifetime: 2,
    seedModel: seed,
    now: () => 100,
  });
  const reader = owner.attachReader();

  expect((await reader.openLocal(40)).status).toBe("recovery-needed");
  reader.dispose();
  owner.dispose();
});

it("opens the fixed A/B cut after a stale A query while later C and D stay live", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const owner = repository(fixture.cache);
  owner.acceptRead(read("A", 1), { lifetime: 1, readId: "read-cut-A", boundary: "read-cut-A-boundary" });
  await owner.settled();

  const writeGate = gateEntryCommit(fixture.cache, "item-B");
  owner.acceptRead(read("B", 2), { lifetime: 1, readId: "read-cut-B", boundary: "read-cut-B-boundary" });
  await writeGate.reached;
  const getLatestWindow = fixture.cache.getLatestWindow.bind(fixture.cache);
  const observedWindows: Awaited<ReturnType<typeof fixture.cache.getLatestWindow>>[] = [];
  vi.spyOn(fixture.cache, "getLatestWindow").mockImplementation(async (...args) => {
    const result = await getLatestWindow(...args);
    observedWindows.push(result);
    return result;
  });
  const queryHold = holdNextTransaction([ENTRY_ITEM_STORE, ENTRY_COVERAGE_STORE], "readonly");
  const reader = owner.attachReader();
  const opening = reader.openLocal(40);
  await queryHold.reached;

  owner.acceptRead(read("C", 3), { lifetime: 1, readId: "read-cut-C", boundary: "read-cut-C-boundary" });
  writeGate.release();
  await owner.settled();
  queryHold.release();
  const opened = await opening;

  expect(observedWindows[0]?.boundary).toBe("read-cut-A-boundary");
  expect(observedWindows[0]?.rows.map((row) => row.transcriptKey)).toEqual(["item-A"]);
  expect(opened.status).toBe("current");
  expect(opened.model?.turns.flatMap((turn) => turn.items).map((item) => item.text)).toEqual(["A", "B", "C"]);
  owner.acceptRead(read("D", 4), { lifetime: 1, readId: "read-cut-D", boundary: "read-cut-D-boundary" });
  expect(
    reader
      .getSnapshot()
      ?.turns.flatMap((turn) => turn.items)
      .map((item) => item.text),
  ).toEqual(["A", "B", "C", "D"]);
  reader.dispose();
  owner.dispose();
});

it("opens a retained A/B boundary while B's real cache write is still pending", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const owner = repository(fixture.cache);
  owner.acceptRead(read("A", 1), { lifetime: 1, readId: "read-pending-A", boundary: "read-pending-A-boundary" });
  await owner.settled();

  const writeGate = gateEntryCommit(fixture.cache, "item-B");
  owner.acceptRead(read("B", 2), { lifetime: 1, readId: "read-pending-B", boundary: "read-pending-B-boundary" });
  await writeGate.reached;
  const getLatestWindow = fixture.cache.getLatestWindow.bind(fixture.cache);
  let observedWindow: Awaited<ReturnType<typeof fixture.cache.getLatestWindow>> | undefined;
  vi.spyOn(fixture.cache, "getLatestWindow").mockImplementation(async (...args) => {
    observedWindow = await getLatestWindow(...args);
    return observedWindow;
  });
  const reader = owner.attachReader();

  const opened = await reader.openLocal(40);
  expect(observedWindow?.boundary).toBe("read-pending-A-boundary");
  expect(observedWindow?.rows.map((row) => row.transcriptKey)).toEqual(["item-A"]);
  expect(opened.status).toBe("current");
  expect(opened.model?.turns.flatMap((turn) => turn.items).map((item) => item.text)).toEqual(["A", "B"]);

  writeGate.release();
  await owner.settled();
  reader.dispose();
  owner.dispose();
});

it.each(["server cursor present", "no server cursor"] as const)(
  "returns a before position separately from the server cursor with %s",
  async (cursorCase) => {
    const fixture = createEntryCacheFixture();
    fixtures.push(fixture);
    const owner = repository(fixture.cache);
    const response = readMany(45);
    if (cursorCase === "server cursor present") response.olderCursor = "opaque-server-cursor-before-row-1";
    const openingBoundary = `boundary-${cursorCase}`;
    owner.acceptRead(response, { lifetime: 1, readId: `read-cursor-${cursorCase}`, boundary: openingBoundary });
    await owner.settled();
    const reader = owner.attachReader();

    const opened = await reader.openLocal(40);

    const localWindow = await fixture.cache.getLatestWindow(historyKey, 40, openingBoundary);
    expect(localWindow.rows.map((row) => row.transcriptKey)).toEqual(
      Array.from({ length: 40 }, (_, index) => `item-${index + 6}`),
    );
    expect(opened.before).toEqual({ entry: 6, item: 0 });
    expect(opened.olderCursor).toBe(
      cursorCase === "server cursor present" ? "opaque-server-cursor-before-row-1" : undefined,
    );
    reader.dispose();
    owner.dispose();
  },
);

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

it.each(["open after C commits", "query held before transaction"] as const)(
  "opens the fixed cut after a later same-continuity suffix replaces its certificate, %s",
  async (holdPoint) => {
    const fixture = createEntryCacheFixture();
    fixtures.push(fixture);
    const owner = repository(fixture.cache);
    owner.acceptRead(read("A", 1), { lifetime: 1, readId: "suffix-read", boundary: "suffix-A" });
    await owner.settled();
    owner.accept(historyUpdate("B", 2), { lifetime: 1 });
    await owner.settled();

    const reader = owner.attachReader();
    const realQuery = fixture.cache.getLatestWindow.bind(fixture.cache);
    let releaseQuery!: () => void;
    let queryReached!: () => void;
    const gate = new Promise<void>((resolve) => (releaseQuery = resolve));
    const reached = new Promise<void>((resolve) => (queryReached = resolve));
    const querySpy =
      holdPoint === "query held before transaction"
        ? vi.spyOn(fixture.cache, "getLatestWindow").mockImplementation(async (...args) => {
            queryReached();
            await gate;
            return realQuery(...args);
          })
        : undefined;
    if (holdPoint === "open after C commits") {
      owner.accept(historyUpdate("C", 3), { lifetime: 1 });
      await owner.settled();
    }
    const opening = reader.openLocal(40);
    if (holdPoint === "query held before transaction") {
      await reached;
      owner.accept(historyUpdate("C", 3), { lifetime: 1 });
      await owner.settled();
      releaseQuery();
    }
    querySpy?.mockRestore();

    const opened = await opening;
    expect(opened.status).toBe("current");
    expect(opened.model?.turns.flatMap((turn) => turn.items).map((item) => item.text)).toEqual(["A", "B", "C"]);
    reader.dispose();
    owner.dispose();
  },
);

it.each(["same generation", "new boot after invalidation"] as const)(
  "does not republish a held opening after authoritative %s replacement",
  async (replacement) => {
    const fixture = createEntryCacheFixture();
    fixtures.push(fixture);
    const owner = repository(fixture.cache);
    owner.acceptRead(read("retired A", 1), { lifetime: 1, readId: "retired-A", boundary: "retired-A-boundary" });
    await owner.settled();
    const queryHold = holdNextTransaction([ENTRY_ITEM_STORE, ENTRY_COVERAGE_STORE], "readonly");
    const reader = owner.attachReader();
    const opening = reader.openLocal(40);
    await queryHold.reached;
    if (replacement === "new boot after invalidation") {
      owner.accept(
        {
          method: "history/updated",
          params: {
            ref,
            threadId,
            bootGeneration: "2",
            epoch: 1,
            snapshot: { incarnation: identity.incarnation, length: 2 },
            items: [],
          },
        } as AnyNotification,
        { lifetime: 1 },
      );
      expect(owner.getSnapshot()?.history?.invalidatedAtGeneration).toBeDefined();
    }
    const newAuthority = read("replacement B", 2, "item-B");
    newAuthority.bootGeneration = replacement === "same generation" ? "1" : "2";
    newAuthority.epoch = 1;
    newAuthority.requestGeneration = 1;
    expect(
      owner.acceptRead(newAuthority, {
        lifetime: 1,
        readId: `replacement-${replacement}`,
        boundary: `replacement-${replacement}`,
      }),
    ).toBe(true);
    queryHold.release();
    const opened = await opening;

    expect(opened.status).toBe("recovery-needed");
    expect(opened.model?.turns.flatMap((turn) => turn.items).map((item) => item.text)).not.toContain("retired A");
    expect(
      reader
        .getSnapshot()
        ?.turns.flatMap((turn) => turn.items)
        .map((item) => item.text),
    ).toEqual(["replacement B"]);
    reader.dispose();
    owner.dispose();
  },
);

it.each(["before open", "during lookup", "after lookup"] as const)(
  "releases opening model and pending payload ownership %s and supports a fresh repeated open",
  async (disposalPoint) => {
    const fixture = createEntryCacheFixture();
    fixtures.push(fixture);
    const seed = hydrateThread(readMany(45), ref, 101);
    const owner = new TranscriptRepository(fixture.cache, {
      ref,
      threadId,
      historyKey,
      identity,
      clearEpoch: 0,
      lifetime: 2,
      seedModel: seed,
      seedAuthority: {
        trustedCurrentSource: true,
        bootGeneration: seed.history!.bootGeneration,
        epoch: seed.history!.epoch,
        incarnation: seed.history!.incarnation ?? "",
        snapshotLength: seed.history!.length,
      },
      now: () => 100,
    });
    const writeGate = gateEntryCommit(fixture.cache, "item-pending");
    owner.accept(historyUpdate("pending", 46), { lifetime: 2 });
    await writeGate.reached;
    const reader = owner.attachReader();
    expect(reader.getRetentionSnapshotForTesting()).toEqual({ openingModelRetained: true, pendingPayloadCount: 1 });

    if (disposalPoint === "before open") {
      reader.dispose();
      expect(reader.getRetentionSnapshotForTesting()).toEqual({ openingModelRetained: false, pendingPayloadCount: 0 });
    } else if (disposalPoint === "during lookup") {
      const queryHold = holdNextTransaction([ENTRY_ITEM_STORE, ENTRY_COVERAGE_STORE], "readonly");
      const opening = reader.openLocal(40);
      await queryHold.reached;
      reader.dispose();
      expect(reader.getRetentionSnapshotForTesting()).toEqual({ openingModelRetained: false, pendingPayloadCount: 0 });
      queryHold.release();
      await opening;
    } else {
      const opened = await reader.openLocal(40);
      expect(opened.model).toBeDefined();
      expect(reader.getRetentionSnapshotForTesting()).toEqual({ openingModelRetained: false, pendingPayloadCount: 0 });
      const repeated = await reader.openLocal(40);
      expect(repeated.model?.turns.flatMap((turn) => turn.items).some((item) => item.text === "pending")).toBe(true);
      expect(reader.getRetentionSnapshotForTesting()).toEqual({ openingModelRetained: false, pendingPayloadCount: 0 });
      reader.dispose();
    }
    writeGate.release();
    await owner.settled();
    owner.dispose();
  },
);

it("drops the seed reference from binding ownership after reader disposal and write settlement", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const seed = hydrateThread(readMany(45), ref, 101);
  const owner = new TranscriptRepository(fixture.cache, {
    ref,
    threadId,
    historyKey,
    identity,
    clearEpoch: 0,
    lifetime: 2,
    seedModel: seed,
    seedAuthority: {
      trustedCurrentSource: true,
      bootGeneration: seed.history!.bootGeneration,
      epoch: seed.history!.epoch,
      incarnation: seed.history!.incarnation ?? "",
      snapshotLength: seed.history!.length,
    },
    now: () => 100,
  });
  const reader = owner.attachReader();
  expect(reader.getSnapshot()).toBe(seed);
  reader.dispose();
  await owner.settled();
  owner.dispose();

  expect(owner.getRetentionSnapshotForTesting()).toEqual({ bindingHasSeedModel: false });
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

it("hands off only history-covered text, reasoning, preview, and tool overlays before a real commit", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const owner = repository(fixture.cache);
  owner.acceptRead(read("base"), { lifetime: 1, readId: "read-mixed-commit", boundary: "read-mixed-commit-boundary" });
  await owner.settled();
  for (const overlay of mixedHandoffOverlays()) expect(owner.accept(overlay, { lifetime: 1 })).toBe(true);
  owner.accept({ method: "overlay/end", params: { ref, threadId, roundId: "covered-round" } } as AnyNotification, {
    lifetime: 1,
  });
  expect(Object.keys(owner.getSnapshot()?.overlay ?? {}).sort()).toEqual(
    [...coveredOverlayKeys, ...uncoveredOverlayKeys].sort(),
  );

  const writeHold = holdNextEntryPut("covered-text");
  expect(owner.accept(mixedHandoffHistory(), { lifetime: 1 })).toBe(true);
  try {
    await writeHold.reached;
    expect(Object.keys(owner.getSnapshot()?.overlay ?? {})).toEqual(uncoveredOverlayKeys);
    expect(
      owner
        .getSnapshot()
        ?.turns.flatMap((turn) => turn.items)
        .map((item) => item.text),
    ).toEqual(
      expect.arrayContaining([
        "recorded covered text",
        "recorded covered reasoning",
        "recorded covered preview",
        "uncovered live text",
        "uncovered live reasoning",
        "uncovered preview",
      ]),
    );
  } finally {
    writeHold.commit();
  }
  await owner.settled();
  await expect(fixture.cache.getLatest(historyKey, 40)).resolves.toMatchObject([
    { transcriptKey: "item-base" },
    { transcriptKey: "covered-text", item: { text: "recorded covered text" } },
    { transcriptKey: "covered-reasoning", item: { text: "recorded covered reasoning" } },
    { transcriptKey: "covered-preview", item: { text: "recorded covered preview" } },
    { transcriptKey: "covered-tool", item: { output: "recorded covered tool" } },
  ]);
  expect(Object.keys(owner.getSnapshot()?.overlay ?? {})).toEqual(uncoveredOverlayKeys);
  owner.dispose();
});

it("keeps mixed uncovered overlays and accepted history visible after a real abort in history-before-end order", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const owner = repository(fixture.cache);
  owner.acceptRead(read("base"), { lifetime: 1, readId: "read-mixed-abort", boundary: "read-mixed-abort-boundary" });
  await owner.settled();
  const reader = owner.attachReader();
  for (const overlay of mixedHandoffOverlays()) expect(owner.accept(overlay, { lifetime: 1 })).toBe(true);

  const writeHold = holdNextEntryPut("covered-text");
  expect(owner.accept(mixedHandoffHistory(), { lifetime: 1 })).toBe(true);
  try {
    await writeHold.reached;
    expect(Object.keys(owner.getSnapshot()?.overlay ?? {})).toEqual(uncoveredOverlayKeys);
    owner.accept({ method: "overlay/end", params: { ref, threadId, roundId: "covered-round" } } as AnyNotification, {
      lifetime: 1,
    });
    expect(Object.keys(owner.getSnapshot()?.overlay ?? {})).toEqual(uncoveredOverlayKeys);
  } finally {
    writeHold.abort();
  }
  await owner.settled();

  expect(
    reader
      .getSnapshot()
      ?.turns.flatMap((turn) => turn.items)
      .map((item) => item.text),
  ).toEqual(
    expect.arrayContaining([
      "recorded covered text",
      "recorded covered reasoning",
      "recorded covered preview",
      "uncovered live text",
      "uncovered live reasoning",
      "uncovered preview",
    ]),
  );
  expect(Object.keys(reader.getSnapshot()?.overlay ?? {})).toEqual(uncoveredOverlayKeys);
  await expect(fixture.cache.getLatest(historyKey, 40)).resolves.toMatchObject([{ transcriptKey: "item-base" }]);
  await expect(fixture.cache.getCoverage(historyKey, "suffix:read-mixed-abort")).resolves.toBeUndefined();
  reader.dispose();
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

it("rejects isolated old-plus-distant input after shared history authority breaks", async () => {
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
  ).toBe(false);
  await owner.settled();

  await expect(fixture.cache.getCoverage(historyKey, "suffix:read-broken")).resolves.toBeUndefined();
  await expect(fixture.cache.getCoverage(historyKey, "read:read-broken")).resolves.toMatchObject({
    representedSnapshot: 8_004,
    end: { entry: 50 },
  });
  const rows = await fixture.cache.getLatest(historyKey, 100);
  expect(rows).toHaveLength(50);
  expect(rows[0]).toMatchObject({ transcriptKey: "item-1", item: { text: "row-1" } });
  expect(rows.find((row) => row.transcriptKey === "item-2")).toMatchObject({ item: { text: "row-2" } });
  expect(rows.find((row) => row.transcriptKey === "item-51")).toBeUndefined();
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

it("issues no IDB transaction, request, or recorded-history load for overlay-only traffic", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const owner = repository(fixture.cache);
  owner.acceptRead(read("recorded"), {
    lifetime: 1,
    readId: "read-overlay-zero-io",
    boundary: "read-overlay-zero-io-boundary",
  });
  await owner.settled();

  const transactionSpy = vi.spyOn(IDBDatabase.prototype, "transaction");
  const historyLoads = [
    vi.spyOn(fixture.cache, "getLatestWindow"),
    vi.spyOn(fixture.cache, "getLatest"),
    vi.spyOn(fixture.cache, "getCoverage"),
    vi.spyOn(fixture.cache, "commitBatch"),
  ];
  const requestCalls: string[] = [];
  const requests = [
    "get",
    "getAll",
    "getAllKeys",
    "openCursor",
    "openKeyCursor",
    "put",
    "delete",
    "clear",
    "count",
  ] as const;
  const requestSpies = requests.map((method) => {
    const original = IDBObjectStore.prototype[method] as (...args: unknown[]) => IDBRequest;
    return vi.spyOn(IDBObjectStore.prototype, method).mockImplementation(function (
      this: IDBObjectStore,
      ...args: unknown[]
    ) {
      requestCalls.push(method);
      return original.apply(this, args);
    } as never);
  });

  try {
    for (const overlay of mixedHandoffOverlays()) expect(owner.accept(overlay, { lifetime: 1 })).toBe(true);
    expect(
      owner.accept(
        {
          method: "overlay/delta",
          params: { ref, threadId, key: coveredOverlayKeys[0], field: "text", delta: " more" },
        } as AnyNotification,
        { lifetime: 1 },
      ),
    ).toBe(true);
    expect(
      owner.accept({ method: "overlay/end", params: { ref, threadId, roundId: "covered-round" } } as AnyNotification, {
        lifetime: 1,
      }),
    ).toBe(true);
    await owner.settled();

    expect(transactionSpy).not.toHaveBeenCalled();
    expect(requestCalls).toEqual([]);
    for (const load of historyLoads) expect(load).not.toHaveBeenCalled();
  } finally {
    transactionSpy.mockRestore();
    for (const spy of requestSpies) spy.mockRestore();
    for (const load of historyLoads) load.mockRestore();
  }
  owner.dispose();
});
