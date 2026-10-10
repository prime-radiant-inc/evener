// Task 2 repository-level paging integration: the baseline identity the cache
// contract's `authority` needs, page acceptance carrying the captured request
// cursor, local older-page serving where coverage permits, the stale frontier
// falling through to the server (never exhaustion), newer-version races and
// held-reader survival through disk eviction.
import "fake-indexeddb/auto";
import type { AnyNotification, ThreadItem, ThreadReadResponse, ThreadTurnsListResponse } from "@evener/appwire-client";
import { IDBFactory, IDBKeyRange } from "fake-indexeddb";
import { afterEach, expect, test } from "vitest";
import { entryHistoryKey, type TranscriptResolvedIdentity } from "./sessionCacheSchema";
import { SessionEntryCache } from "./sessionEntryCache";
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

function itemOf(entry: number, version = entry): ThreadItem {
  return {
    type: "agentMessage",
    id: `item-${entry}`,
    transcriptKey: `item-${entry}`,
    turnId: "turn_1",
    position: { entry, item: 0 },
    version,
    text: `row-${entry}`,
  };
}

function readOf(
  entries: number[],
  opts: { olderCursor?: string; length?: number; authoritative?: boolean } = {},
): ThreadReadResponse {
  const length = opts.length ?? Math.max(...entries, 0);
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
          items: entries.map((entry) => itemOf(entry)),
        },
      ],
      evener: { ref, capabilities: CAPABILITIES, queue: { revision: 0 } },
    },
    bootGeneration: "1",
    epoch: 0,
    snapshot: { incarnation: identity.incarnation, length },
    overlay: [],
    ...(opts.olderCursor === undefined ? {} : { olderCursor: opts.olderCursor }),
    ...(opts.authoritative === undefined ? {} : { authoritative: opts.authoritative }),
  };
}

function pageOf(
  entries: number[],
  opts: { nextCursor?: string; length?: number; versions?: Record<number, number> } = {},
): ThreadTurnsListResponse {
  const length = opts.length ?? Math.max(...entries, 0);
  return {
    data: [
      {
        id: "turn_1",
        itemsView: "full",
        status: "completed",
        version: length,
        items: entries.map((entry) => itemOf(entry, opts.versions?.[entry] ?? entry)),
      },
    ],
    ...(opts.nextCursor === undefined ? {} : { nextCursor: opts.nextCursor }),
    bootGeneration: "1",
    epoch: 0,
    snapshot: { incarnation: identity.incarnation, length },
  };
}

function historyUpdate(entry: number, version: number, text: string): AnyNotification {
  return {
    method: "history/updated",
    params: {
      ref,
      threadId,
      bootGeneration: "1",
      epoch: 0,
      snapshot: { incarnation: identity.incarnation, length: Math.max(version, entry) },
      items: [{ ...itemOf(entry, version), text }],
    },
  } as AnyNotification;
}

function repository(cache: SessionEntryCache, lifetime = 1) {
  return new TranscriptRepository(cache, {
    ref,
    threadId,
    historyKey,
    identity,
    clearEpoch: 0,
    lifetime,
    now: () => Date.now(),
  });
}

function positions(model: import("@evener/appwire-client").ThreadModel | undefined): number[] {
  return (model?.history?.turns.flatMap((turn) => turn.items) ?? [])
    .map((item) => item.position?.entry)
    .filter((entry): entry is number => entry !== undefined)
    .sort((left, right) => left - right);
}

function textAt(model: import("@evener/appwire-client").ThreadModel | undefined, entry: number): string | undefined {
  return (model?.history?.turns.flatMap((turn) => turn.items) ?? []).find((item) => item.position?.entry === entry)
    ?.text;
}

function versionAt(model: import("@evener/appwire-client").ThreadModel | undefined, entry: number): number | undefined {
  return (model?.history?.turns.flatMap((turn) => turn.items) ?? []).find((item) => item.position?.entry === entry)
    ?.version;
}

afterEach(async () => {
  await Promise.all(fixtures.splice(0).map((fixture) => fixture.dispose()));
});

test("baseline identity is stable across same-connection reads and mints a new one on authoritative reconciliation", () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const repo = repository(fixture.cache);

  expect(
    repo.acceptRead(readOf([6, 7, 8], { olderCursor: "C1" }), {
      lifetime: 1,
      readId: "r1",
      boundary: "b1",
    }),
  ).toBe(true);
  const first = repo.getBaselineIdentity();

  // A same-connection non-authoritative read keeps one baseline.
  repo.acceptRead(readOf([6, 7, 8], { olderCursor: "C1" }), { lifetime: 1, readId: "r2", boundary: "b2" });
  expect(repo.getBaselineIdentity()).toBe(first);

  // An authoritative reconciliation that changes the model mints a new one.
  repo.acceptRead(readOf([5, 6, 7, 8], { olderCursor: "C1", authoritative: true }), {
    lifetime: 1,
    readId: "r3",
    boundary: "b3",
  });
  expect(repo.getBaselineIdentity()).not.toBe(first);
});

test("acceptPage records the captured request cursor and the baseline authority under rangeId page:<readId>:<boundary>", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const repo = repository(fixture.cache);

  repo.acceptRead(readOf([6, 7, 8], { olderCursor: "C1" }), { lifetime: 1, readId: "read-1", boundary: "rb-1" });
  const baseline = repo.getBaselineIdentity();
  expect(
    repo.acceptPage(pageOf([3, 4, 5], { nextCursor: "C2", length: 8 }), {
      lifetime: 1,
      readId: "read-1",
      boundary: "page-1",
      requestCursor: "C1",
    }),
  ).toBe(true);
  await repo.settled();

  const cert = await fixture.cache.getCoverage(historyKey, "page:read-1:page-1");
  expect(cert?.kind).toBe("page");
  expect(cert?.requestCursor).toBe("C1");
  expect(cert?.serverCursor).toBe("C2");
  expect(cert?.authority).toBe(baseline);
  expect(cert?.before).toEqual({ entry: 6, item: 0 });
  expect(cert?.start).toEqual({ entry: 3, item: 0 });
  expect(cert?.end).toEqual({ entry: 5, item: 0 });
});

test("several older pages survive close/reopen and are served locally through the same baseline", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const first = repository(fixture.cache);
  first.acceptRead(readOf([6, 7, 8], { olderCursor: "C1", length: 8 }), {
    lifetime: 1,
    readId: "r1",
    boundary: "b1",
  });
  first.acceptPage(pageOf([3, 4, 5], { nextCursor: "C2", length: 8 }), {
    lifetime: 1,
    readId: "r1",
    boundary: "p1",
    requestCursor: "C1",
  });
  first.acceptPage(pageOf([1, 2], { nextCursor: "C3", length: 8 }), {
    lifetime: 1,
    readId: "r1",
    boundary: "p2",
    requestCursor: "C2",
  });
  await first.settled();
  first.dispose();

  // A fresh repository for the same connection/lifetime re-reads the latest
  // window and then continues paging from the certified local pages.
  const reopened = repository(fixture.cache);
  reopened.acceptRead(readOf([6, 7, 8], { olderCursor: "C1", length: 8 }), {
    lifetime: 1,
    readId: "r2",
    boundary: "b2",
  });
  const served = await reopened.loadOlderPage(40, 1);
  expect(served.kind).toBe("local");
  if (served.kind !== "local") return;
  expect(positions(served.model)).toEqual([1, 2, 3, 4, 5, 6, 7, 8]);
  expect(served.model.olderCursor).toBe("C3");
});

test("a fresh baseline cannot anchor the outgoing connection's pages, and a missing certificate is server work, never exhaustion", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const outgoing = repository(fixture.cache, 1);
  outgoing.acceptRead(readOf([6, 7, 8], { olderCursor: "C1", length: 8 }), {
    lifetime: 1,
    readId: "r1",
    boundary: "b1",
  });
  outgoing.acceptPage(pageOf([3, 4, 5], { nextCursor: "C2", length: 8 }), {
    lifetime: 1,
    readId: "r1",
    boundary: "p1",
    requestCursor: "C1",
  });
  await outgoing.settled();

  const replaced = repository(fixture.cache, 2);
  replaced.acceptRead(readOf([6, 7, 8], { olderCursor: "C1", length: 8 }), {
    lifetime: 2,
    readId: "r2",
    boundary: "b2",
  });
  const window = await fixture.cache.getOlderWindow(
    historyKey,
    { entry: 6, item: 0 },
    40,
    replaced.getBaselineIdentity(),
  );
  expect(window.stale).toBe(true);
  const served = await replaced.loadOlderPage(40, 2);
  expect(served.kind).toBe("server");
});

test("a newer item version beats a page's older copy", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const repo = repository(fixture.cache);
  repo.acceptRead(readOf([6, 7, 8], { olderCursor: "C1", length: 8 }), { lifetime: 1, readId: "r1", boundary: "b1" });
  expect(repo.accept(historyUpdate(4, 9, "newer"), { lifetime: 1 })).toBe(true);
  expect(
    repo.acceptPage(pageOf([3, 4, 5], { nextCursor: "C2", length: 8 }), {
      lifetime: 1,
      readId: "r1",
      boundary: "p1",
      requestCursor: "C1",
    }),
  ).toBe(true);
  await repo.settled();
  expect(versionAt(repo.getSnapshot(), 4)).toBe(9);
});

test("a retired repository accepts no page", () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const repo = repository(fixture.cache);
  repo.acceptRead(readOf([6, 7, 8], { olderCursor: "C1" }), { lifetime: 1, readId: "r1", boundary: "b1" });
  repo.invalidate();
  expect(repo.acceptPage(pageOf([3, 4, 5]), { lifetime: 1, readId: "r1", boundary: "p1", requestCursor: "C1" })).toBe(
    false,
  );
});

test("an open reader keeps its loaded pages when disk eviction removes the history", async () => {
  const databaseName = `entry-paging-evict-${crypto.randomUUID()}`;
  const indexedDB = new IDBFactory();
  let now = 1000;
  const cache = new SessionEntryCache({
    indexedDB,
    keyRange: IDBKeyRange,
    databaseName,
    maxBytes: 20_000,
    ttlMs: 14 * 24 * 60 * 60 * 1000,
    now: () => now,
  });
  try {
    const repo = repository(cache);
    repo.acceptRead(readOf([1, 2, 3, 4, 5, 6, 7, 8, 9, 10], { length: 10 }), {
      lifetime: 1,
      readId: "r1",
      boundary: "b1",
    });
    await repo.settled();
    const reader = repo.attachReader();
    const opened = await reader.openLocal(40);
    expect(positions(opened.model ?? repo.getSnapshot())).toEqual([1, 2, 3, 4, 5, 6, 7, 8, 9, 10]);

    const before = await cache.getByteTotals(historyKey);
    // A later, unrelated live history large enough to force this one out first.
    now = 2000;
    const otherHistoryKey = "other-history";
    const huge = "x".repeat(20_000 - before.historyBytes + 500);
    await cache.commitBatch({
      historyKey: otherHistoryKey,
      clearEpoch: 0,
      items: [
        {
          historyKey: otherHistoryKey,
          transcriptKey: "other-item",
          position: { entry: 1, item: 0 },
          version: 1,
          item: {
            id: "other-item",
            transcriptKey: "other-item",
            turnId: "other-turn",
            position: { entry: 1, item: 0 },
            version: 1,
            type: "agentMessage",
            text: huge,
          },
        },
      ],
    });
    const evicted = await cache.getLatest(historyKey, 40);
    expect(evicted).toEqual([]);

    // The retained reader still holds its own loaded pages in memory.
    expect(positions(reader.getSnapshot())).toEqual([1, 2, 3, 4, 5, 6, 7, 8, 9, 10]);
    expect(textAt(reader.getSnapshot(), 5)).toBe("row-5");
    reader.dispose();
    repo.dispose();
  } finally {
    cache.close();
  }
});
