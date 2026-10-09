import {
  type AnyNotification,
  encodeTranscriptItem,
  hydrateThread,
  type ThreadItem,
  type ThreadModel,
  type ThreadReadResponse,
} from "@evener/appwire-client";
import { afterEach, expect, it, vi } from "vitest";
import { entryHistoryKey, type TranscriptResolvedIdentity } from "./sessionCacheSchema";
import { createEntryCacheFixture } from "./testing/entryCacheFixture";
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
  summary.thread.turns = [];
  const seed = hydrateThread(summary, ref, 101);
  const localOnly = repository(fixture.cache, 2, seed);
  const reader = localOnly.attachReader();
  const opened = await reader.openLocal(40);
  expect(opened.status).toBe("current");
  expect(opened.model?.turns.flatMap((turn) => turn.items).map((item) => item.text)).toEqual(["persisted"]);
  expect(opened.model?.runningTurnId).toBe(seed.runningTurnId);
  expect(localOnly.getSnapshot()?.turns.flatMap((turn) => turn.items)).toHaveLength(1);
  reader.dispose();
  expect(localOnly.getSnapshot()?.turns.flatMap((turn) => turn.items)).toHaveLength(0);
  localOnly.dispose();
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
  const oldWindow = await fixture.cache.getLatestWindow(historyKey, 40, "read-boundary-1");
  let releaseQuery!: (value: typeof oldWindow) => void;
  const pendingQuery = new Promise<typeof oldWindow>((resolve) => {
    releaseQuery = resolve;
  });
  vi.spyOn(fixture.cache, "getLatestWindow").mockReturnValue(pendingQuery);
  const reader = owner.attachReader();
  const opening = reader.openLocal(40);
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
  releaseQuery(oldWindow);
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

it("publishes a recorded item and covered stream retirement together while storage is pending", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const owner = repository(fixture.cache);
  const snapshots: Array<{ text: string[]; overlay: string[] }> = [];
  owner.acceptRead(read("base"), { lifetime: 1, readId: "read-1", boundary: "read-boundary-1" });
  await owner.settled();
  let releaseWrite!: (value: { outcome: "committed"; coverage: "unchanged"; encodedBytes: number }) => void;
  const writeHold = new Promise<{ outcome: "committed"; coverage: "unchanged"; encodedBytes: number }>((resolve) => {
    releaseWrite = resolve;
  });
  const commit = vi.spyOn(fixture.cache, "commitBatch").mockReturnValue(writeHold);
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
  expect(commit).not.toHaveBeenCalled();
  owner.accept(
    {
      method: "overlay/delta",
      params: { ref, threadId, key: "stream:round-1/1:agentMessage", field: "text", delta: " chunk" },
    } as AnyNotification,
    { lifetime: 1 },
  );
  expect(commit).not.toHaveBeenCalled();
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
      ],
    },
  };
  expect(owner.accept(replacement, { lifetime: 1 })).toBe(true);
  expect(snapshots).toHaveLength(1);
  expect(snapshots[0]).toMatchObject({ text: expect.arrayContaining(["recorded"]), overlay: [] });
  expect(commit).toHaveBeenCalledTimes(1);
  expect(commit.mock.calls[0]?.[0]).toMatchObject({
    items: [{ transcriptKey: "recorded-item" }],
    turns: [{ id: "turn_1", items: [] }],
    coverage: { kind: "suffix", representedSnapshot: 2 },
  });
  releaseWrite({ outcome: "committed", coverage: "unchanged", encodedBytes: 0 });
  await owner.settled();
  await expect(fixture.cache.getLatest(historyKey, 40)).resolves.toMatchObject([{ transcriptKey: "item-base" }]);
  owner.dispose();
});

it("does not extend coverage across a mixed isolated-old and distant-new notification", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const owner = repository(fixture.cache);
  owner.acceptRead(readMany(50), { lifetime: 1, readId: "read-gap", boundary: "read-gap-boundary" });
  await owner.settled();
  const notification: AnyNotification = {
    method: "history/updated",
    params: {
      ref,
      threadId,
      bootGeneration: "1",
      epoch: 0,
      snapshot: { incarnation: identity.incarnation, length: 51 },
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
  await expect(fixture.cache.getCoverage(historyKey, "suffix:read-gap")).resolves.toBeUndefined();
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
          snapshot: { incarnation: identity.incarnation, length: 3 },
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
    representedSnapshot: 3,
    start: { entry: 100, item: 2 },
    end: { entry: 700, item: 4 },
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
          snapshot: { incarnation: identity.incarnation, length: 4 },
          turns: [{ id: "turn_1", status: "completed", version: 4, itemsView: "full" }],
        },
      } as AnyNotification,
      { lifetime: 1 },
    ),
  ).toBe(true);
  await owner.settled();
  await expect(fixture.cache.getCoverage(historyKey, "suffix:read-provenance")).resolves.toMatchObject({
    representedSnapshot: 4,
    start: { entry: 100, item: 2 },
    end: { entry: 700, item: 4 },
  });
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
