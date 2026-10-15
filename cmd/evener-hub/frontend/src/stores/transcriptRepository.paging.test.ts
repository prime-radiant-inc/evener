// Task 2 repository-level paging integration (fix round): the baseline identity
// the cache contract's `authority` needs, page acceptance carrying the captured
// request cursor/authority and honoring the shared reducer's disposition, local
// older-page serving where coverage permits, uncertainty routed to the bounded
// reconciliation (never an ordinary cursor request), stale-async protection and
// held-reader survival through disk eviction.
import "fake-indexeddb/auto";
import type {
  AnyNotification,
  OverlayItem,
  ThreadItem,
  ThreadModel,
  ThreadReadResponse,
  ThreadTurnsListResponse,
} from "@evener/appwire-client";
import { decodeTranscriptItem } from "@evener/appwire-client";
import { IDBFactory, IDBKeyRange } from "fake-indexeddb";
import { afterEach, expect, test, vi } from "vitest";
import {
  entryHistoryKey,
  SESSION_CACHE_LOOKUP_DEADLINE_MS,
  type TranscriptResolvedIdentity,
} from "./sessionCacheSchema";
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
  opts: {
    olderCursor?: string;
    length?: number;
    authoritative?: boolean;
    epoch?: number;
    bootGeneration?: string;
  } = {},
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
    bootGeneration: opts.bootGeneration ?? "1",
    epoch: opts.epoch ?? 0,
    snapshot: { incarnation: identity.incarnation, length },
    overlay: [],
    ...(opts.olderCursor === undefined ? {} : { olderCursor: opts.olderCursor }),
    ...(opts.authoritative === undefined ? {} : { authoritative: opts.authoritative }),
  };
}

function pageOf(
  entries: number[],
  opts: {
    nextCursor?: string;
    length?: number;
    versions?: Record<number, number>;
    incarnation?: string;
    bootGeneration?: string;
    epoch?: number;
  } = {},
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
    bootGeneration: opts.bootGeneration ?? "1",
    epoch: opts.epoch ?? 0,
    snapshot: { incarnation: opts.incarnation ?? identity.incarnation, length },
  };
}

function historyUpdate(entry: number, version: number, text: string, length: number): AnyNotification {
  return {
    method: "history/updated",
    params: {
      ref,
      threadId,
      bootGeneration: "1",
      epoch: 0,
      snapshot: { incarnation: identity.incarnation, length },
      items: [{ ...itemOf(entry, version), text }],
    },
  } as AnyNotification;
}

function repository(cache: SessionEntryCache, lifetime = 1, nowFn: () => number = () => Date.now()) {
  return new TranscriptRepository(cache, {
    ref,
    threadId,
    historyKey,
    identity,
    clearEpoch: 0,
    lifetime,
    now: nowFn,
  });
}

/** Positions in the model's own turn/item order, never re-sorted. */
function orderedPositions(model: ThreadModel | undefined): number[] {
  return (model?.history?.turns ?? [])
    .flatMap((turn) => turn.items)
    .map((item) => item.position?.entry)
    .filter((entry): entry is number => entry !== undefined);
}

function itemVersionAt(model: ThreadModel | undefined, entry: number): number | undefined {
  return (model?.history?.turns ?? []).flatMap((turn) => turn.items).find((item) => item.position?.entry === entry)
    ?.version;
}

function holdsPosition(model: ThreadModel | undefined, entry: number): boolean {
  return orderedPositions(model).includes(entry);
}

/** Holds the real cache's older-window lookup until released. */
function holdOlderWindow() {
  const original = SessionEntryCache.prototype.getOlderWindow;
  let seen: Awaited<ReturnType<typeof original>> | undefined;
  let releaseGate!: () => void;
  const gate = new Promise<void>((resolve) => {
    releaseGate = resolve;
  });
  let markCalled!: () => void;
  const called = new Promise<void>((resolve) => {
    markCalled = resolve;
  });
  const spy = vi.spyOn(SessionEntryCache.prototype, "getOlderWindow").mockImplementation(function (
    this: SessionEntryCache,
    ...args: Parameters<typeof original>
  ) {
    markCalled();
    return gate.then(async () => {
      const result = await original.apply(this, args);
      seen = result;
      return result;
    });
  } as typeof original);
  return {
    whenCalled: called,
    lastWindow: () => seen,
    release: () => {
      releaseGate();
      spy.mockRestore();
    },
  };
}

/**
 * Runs the real cache older-window lookup to completion, then holds before
 * returning it. This exposes the window between "the cache read finished" and
 * "the repository merged it", so a live update that arrives after the read is
 * genuinely in memory rather than queued to disk before the query starts.
 */
function holdOlderWindowAfterRead() {
  const original = SessionEntryCache.prototype.getOlderWindow;
  let seen: Awaited<ReturnType<typeof original>> | undefined;
  let releaseGate!: () => void;
  const gate = new Promise<void>((resolve) => {
    releaseGate = resolve;
  });
  let markRead!: () => void;
  const read = new Promise<void>((resolve) => {
    markRead = resolve;
  });
  const spy = vi.spyOn(SessionEntryCache.prototype, "getOlderWindow").mockImplementation(function (
    this: SessionEntryCache,
    ...args: Parameters<typeof original>
  ) {
    return (async () => {
      const result = await original.apply(this, args);
      seen = result;
      markRead();
      await gate;
      return result;
    })();
  } as typeof original);
  return {
    whenRead: read,
    lastWindow: () => seen,
    release: () => {
      releaseGate();
      spy.mockRestore();
    },
  };
}

/** A page certificate committed straight to the cache (ambiguous frontier setup). */
async function commitPageCertificate(
  cache: SessionEntryCache,
  authority: string,
  requestCursor: string,
  serverCursor: string,
  readId: string,
  boundary: string,
) {
  const outcome = await cache.commitBatch({
    historyKey,
    clearEpoch: 0,
    items: [],
    coverage: {
      kind: "page",
      readId,
      before: { entry: 6, item: 0 },
      start: { entry: 3, item: 0 },
      end: { entry: 5, item: 0 },
      representedSnapshot: 8,
      serverCursor,
      requestCursor,
      authority,
      boundary,
    },
  });
  expect(outcome.outcome).toBe("committed");
}

/** A single certified older page committed straight to the cache. */
async function commitLocalPage(cache: SessionEntryCache, authority: string) {
  const outcome = await cache.commitBatch({
    historyKey,
    clearEpoch: 0,
    items: [3, 4, 5].map((entry) => ({
      historyKey,
      transcriptKey: `item-${entry}`,
      position: { entry, item: 0 },
      version: entry,
      item: itemOf(entry),
    })),
    coverage: {
      kind: "page",
      readId: "seed",
      before: { entry: 6, item: 0 },
      start: { entry: 3, item: 0 },
      end: { entry: 5, item: 0 },
      representedSnapshot: 8,
      serverCursor: "C2",
      requestCursor: "C1",
      authority,
      boundary: "seed-page",
    },
  });
  expect(outcome.outcome).toBe("committed");
}

afterEach(async () => {
  await Promise.all(fixtures.splice(0).map((fixture) => fixture.dispose()));
});

test("baseline identity is stable across same-connection reads and mints a new one on authoritative reconciliation", () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const repo = repository(fixture.cache);

  expect(repo.acceptRead(readOf([6, 7, 8], { olderCursor: "C1" }), { lifetime: 1, readId: "r1", boundary: "b1" })).toBe(
    true,
  );
  const first = repo.getBaselineIdentity();

  repo.acceptRead(readOf([6, 7, 8], { olderCursor: "C1" }), { lifetime: 1, readId: "r2", boundary: "b2" });
  expect(repo.getBaselineIdentity()).toBe(first);

  repo.acceptRead(readOf([5, 6, 7, 8], { olderCursor: "C1", authoritative: true }), {
    lifetime: 1,
    readId: "r3",
    boundary: "b3",
  });
  expect(repo.getBaselineIdentity()).not.toBe(first);
});

test("beginFreshBaseline retires the outgoing authority so old certificates cannot certify the new interval", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const repo = repository(fixture.cache);
  repo.acceptRead(readOf([6, 7, 8], { olderCursor: "C1", length: 8 }), { lifetime: 1, readId: "r1", boundary: "b1" });
  const outgoing = repo.getBaselineIdentity();
  expect(
    repo.acceptPage(pageOf([3, 4, 5], { nextCursor: "C2", length: 8 }), {
      lifetime: 1,
      readId: "r1",
      boundary: "p1",
      authority: outgoing,
      requestCursor: "C1",
    }),
  ).toBe(true);
  await repo.settled();

  repo.beginFreshBaseline();
  const fresh = repo.getBaselineIdentity();
  expect(fresh).not.toBe(outgoing);

  const window = await fixture.cache.getOlderWindow(historyKey, { entry: 6, item: 0 }, 40, fresh);
  expect(window.stale).toBe(true);
  expect(window.coverage).toEqual([]);
  const served = await repo.loadOlderPage(40, 1);
  expect(served.kind).toBe("server");

  // A page answered for the outgoing baseline cannot be certified into the new one.
  expect(
    repo.acceptPage(pageOf([1, 2], { nextCursor: "C3", length: 8 }), {
      lifetime: 1,
      readId: "r2",
      boundary: "p2",
      authority: outgoing,
      requestCursor: "C2",
    }),
  ).toBe(false);
  expect(await fixture.cache.getCoverage(historyKey, "page:r2:p2")).toBeUndefined();
});

test("acceptPage records the captured request cursor, authority and boundary under rangeId page:<readId>:<boundary>", async () => {
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
      authority: baseline,
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

test("a page whose captured authority is no longer current is rejected without a certificate", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const repo = repository(fixture.cache);
  repo.acceptRead(readOf([6, 7, 8], { olderCursor: "C1", length: 8 }), { lifetime: 1, readId: "r1", boundary: "b1" });
  const stale = repo.getBaselineIdentity();
  repo.acceptRead(readOf([6, 7, 8], { olderCursor: "C1", authoritative: true, length: 8 }), {
    lifetime: 1,
    readId: "r2",
    boundary: "b2",
  });

  expect(
    repo.acceptPage(pageOf([3, 4, 5], { nextCursor: "C2", length: 8 }), {
      lifetime: 1,
      readId: "r3",
      boundary: "p1",
      authority: stale,
      requestCursor: "C1",
    }),
  ).toBe(false);
  expect(await fixture.cache.getCoverage(historyKey, "page:r3:p1")).toBeUndefined();
});

test("discarded, stale-snapshot and foreign-identity pages create no certificate", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const repo = repository(fixture.cache);
  repo.acceptRead(readOf([6, 7, 8], { olderCursor: "C1", length: 8 }), { lifetime: 1, readId: "r1", boundary: "b1" });
  const authority = repo.getBaselineIdentity();

  // Stale snapshot: the page's length is below the held watermark, so the shared
  // reducer discards it.
  expect(
    repo.acceptPage(pageOf([3, 4, 5], { nextCursor: "C2", length: 5 }), {
      lifetime: 1,
      readId: "r1",
      boundary: "stale",
      authority,
      requestCursor: "C1",
    }),
  ).toBe(false);
  // Older boot generation: the reducer ignores the page.
  expect(
    repo.acceptPage(pageOf([3, 4, 5], { nextCursor: "C2", length: 8, bootGeneration: "0" }), {
      lifetime: 1,
      readId: "r1",
      boundary: "oldgen",
      authority,
      requestCursor: "C1",
    }),
  ).toBe(false);
  // A page not of this binding's identity is rejected before any certification.
  expect(
    repo.acceptPage(pageOf([3, 4, 5], { nextCursor: "C2", length: 8, incarnation: "inc-other" }), {
      lifetime: 1,
      readId: "r1",
      boundary: "foreign",
      authority,
      requestCursor: "C1",
    }),
  ).toBe(false);

  for (const boundary of ["stale", "oldgen", "foreign"]) {
    expect(await fixture.cache.getCoverage(historyKey, `page:r1:${boundary}`)).toBeUndefined();
  }
});

test("a newer item version and the page's other rows both survive a merge-eligible page", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const repo = repository(fixture.cache);
  repo.acceptRead(readOf([6, 7, 8], { olderCursor: "C1", length: 8 }), { lifetime: 1, readId: "r1", boundary: "b1" });
  const authority = repo.getBaselineIdentity();
  // Item 4 is already newer; the update keeps the held snapshot length at 8 so
  // the page stays merge-eligible.
  expect(repo.accept(historyUpdate(4, 9, "newer", 8), { lifetime: 1 })).toBe(true);
  expect(
    repo.acceptPage(pageOf([3, 4, 5], { nextCursor: "C2", length: 8 }), {
      lifetime: 1,
      readId: "r1",
      boundary: "p1",
      authority,
      requestCursor: "C1",
    }),
  ).toBe(true);
  await repo.settled();
  expect(itemVersionAt(repo.getSnapshot(), 4)).toBe(9);
  expect(orderedPositions(repo.getSnapshot())).toEqual([3, 4, 5, 6, 7, 8]);
});

test.each(["read", "page"])("a versionless recorded header survives %s ingestion and local reopen", async (source) => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const repo = repository(fixture.cache);
  const reader = repo.attachReader();
  // The daemon's immutable header has a stable key/position but version zero,
  // omitted by Go's JSON encoder (TestHeaderPreludeItemsKeyAtTheHeader).
  const header: ThreadItem = {
    id: "item_system_prompt",
    type: "systemMessage",
    transcriptKey: "apptranscript-item-v2:prelude:header:0",
    position: { entry: 0, item: 0 },
    turnId: "turn_system",
    text: "Recorded system header",
  };
  const headerTurn = { id: "turn_system", itemsView: "full" as const, status: "completed" as const, items: [header] };
  const response = readOf([1, 2], { length: 2, ...(source === "page" ? { olderCursor: "C1" } : {}) });
  if (source === "read") response.thread.turns?.unshift(headerTurn);
  repo.acceptRead(response, { lifetime: 1, readId: "r1", boundary: "b1" });
  if (source === "page") {
    const page = pageOf([], { length: 2 });
    page.data = [headerTurn];
    expect(
      repo.acceptPage(page, {
        lifetime: 1,
        readId: "r1",
        boundary: "p1",
        authority: repo.getBaselineIdentity(),
        requestCursor: "C1",
      }),
    ).toBe(true);
  }
  await repo.settled();
  expect(reader.getSnapshot()?.turns.flatMap((turn) => turn.items.map((item) => item.text))).toContain(header.text);
  expect(await fixture.cache.getLatest(historyKey, 40)).toEqual(
    expect.arrayContaining([
      expect.objectContaining({
        transcriptKey: header.transcriptKey,
        position: header.position,
        version: 0,
        item: expect.objectContaining({ text: header.text }),
      }),
    ]),
  );
  reader.dispose();
  const reopened = repo.attachReader();
  try {
    const opened = await reopened.openLocal(40);
    expect(opened.status).toBe("current");
    expect(opened.model?.turns.flatMap((turn) => turn.items.map((item) => item.text))).toEqual([
      header.text,
      "row-1",
      "row-2",
    ]);
    // Ordinary older demand at the restored start of history must not ask the
    // store to reconcile a header the accepted read/page already supplied.
    // Both the no-cursor read frontier and the terminal page are exhausted.
    expect((await repo.loadOlderPage(40, 1)).kind).toBe("exhausted");
    expect(repo.getSnapshot()?.olderCursor).toBeUndefined();
  } finally {
    reopened.dispose();
    repo.dispose();
  }
});

test("several older pages survive close/reopen, serve locally in order and hand the server cursor across the join", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const first = repository(fixture.cache);
  first.acceptRead(readOf([6, 7, 8], { olderCursor: "C1", length: 8 }), { lifetime: 1, readId: "r1", boundary: "b1" });
  const authority = first.getBaselineIdentity();
  first.acceptPage(pageOf([3, 4, 5], { nextCursor: "C2", length: 8 }), {
    lifetime: 1,
    readId: "r1",
    boundary: "p1",
    authority,
    requestCursor: "C1",
  });
  first.acceptPage(pageOf([1, 2], { nextCursor: "C3", length: 8 }), {
    lifetime: 1,
    readId: "r1",
    boundary: "p2",
    authority,
    requestCursor: "C2",
  });
  await first.settled();
  first.dispose();

  // A retained reader on a fresh repository for the same connection/lifetime
  // opens locally, then pages through the certified pages without a network read.
  const reopened = repository(fixture.cache);
  const reader = reopened.attachReader();
  reopened.acceptRead(readOf([6, 7, 8], { olderCursor: "C1", length: 8 }), {
    lifetime: 1,
    readId: "r2",
    boundary: "b2",
  });
  const opened = await reader.openLocal(40);
  expect(orderedPositions(opened.model)).toEqual([6, 7, 8]);

  const served = await reopened.loadOlderPage(40, 1);
  expect(served.kind).toBe("local");
  if (served.kind !== "local") return;
  // Unsorted assertion: the model's own order is the display order.
  expect(orderedPositions(served.model)).toEqual([1, 2, 3, 4, 5, 6, 7, 8]);

  // Rows below the oldest certified page are a server continuation with the
  // frontier cursor, not a manufactured end.
  const again = await reopened.loadOlderPage(40, 1);
  expect(again.kind).toBe("server");
  const frontier = await fixture.cache.getOlderWindow(
    historyKey,
    { entry: 1, item: 0 },
    40,
    reopened.getBaselineIdentity(),
  );
  expect(frontier.complete).toBe(true);
  expect(frontier.serverCursor).toBe("C3");
  reader.dispose();
});

test("a connected-but-unverified frontier returns reconcile, not server", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const repo = repository(fixture.cache);
  repo.acceptRead(readOf([6, 7, 8], { olderCursor: "C1", length: 8 }), { lifetime: 1, readId: "r1", boundary: "b1" });
  const authority = repo.getBaselineIdentity();
  // Two page certificates both consumed C1 but claim different successors: the
  // frontier is ambiguous, not an ordinary continuation.
  await commitPageCertificate(fixture.cache, authority, "C1", "CA", "r1", "amb-a");
  await commitPageCertificate(fixture.cache, authority, "C1", "CB", "r1", "amb-b");

  const served = await repo.loadOlderPage(40, 1);
  expect(served.kind).toBe("reconcile");
});

test("Clear committed while older-page turn metadata is pending prevents publication", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const repo = repository(fixture.cache);
  const reader = repo.attachReader();
  repo.acceptRead(readOf([6, 7, 8], { olderCursor: "C1", length: 8 }), { lifetime: 1, readId: "r1", boundary: "b1" });
  await repo.settled();
  await commitLocalPage(fixture.cache, repo.getBaselineIdentity());
  let release!: () => void;
  let entered!: () => void;
  const held = new Promise<void>((resolve) => {
    release = resolve;
  });
  const called = new Promise<void>((resolve) => {
    entered = resolve;
  });
  const getTurn = fixture.cache.getTurn.bind(fixture.cache);
  vi.spyOn(fixture.cache, "getTurn").mockImplementation(async (...args) => {
    const metadata = await getTurn(...args);
    entered();
    await held;
    return metadata;
  });
  const published = vi.fn();
  repo.subscribe(published);
  const lookup = repo.loadOlderPage(40, 1);
  await called;
  expect(await fixture.cache.clear()).toEqual({ committed: true, epoch: 1 });
  release();
  expect((await lookup).kind).toBe("reconcile");
  expect(published).not.toHaveBeenCalled();
  expect(orderedPositions(reader.getSnapshot())).toEqual([6, 7, 8]);
  reader.dispose();
  repo.dispose();
});

test("a recorded update during the local lookup is preserved", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const repo = repository(fixture.cache);
  repo.acceptRead(readOf([6, 7, 8], { olderCursor: "C1", length: 8 }), { lifetime: 1, readId: "r1", boundary: "b1" });
  const authority = repo.getBaselineIdentity();
  await commitLocalPage(fixture.cache, authority);

  const gate = holdOlderWindow();
  const lookup = repo.loadOlderPage(40, 1);
  await gate.whenCalled;
  expect(repo.accept(historyUpdate(4, 9, "newer", 8), { lifetime: 1 })).toBe(true);
  gate.release();
  const outcome = await lookup;
  expect(outcome.kind).toBe("local");
  if (outcome.kind !== "local") return;
  expect(itemVersionAt(outcome.model, 4)).toBe(9);
  expect(orderedPositions(outcome.model)).toEqual([3, 4, 5, 6, 7, 8]);
});

test("a recorded update after the cache result was already read is preserved", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const repo = repository(fixture.cache);
  repo.acceptRead(readOf([6, 7, 8], { olderCursor: "C1", length: 8 }), { lifetime: 1, readId: "r1", boundary: "b1" });
  const authority = repo.getBaselineIdentity();
  await commitLocalPage(fixture.cache, authority);

  const gate = holdOlderWindowAfterRead();
  const lookup = repo.loadOlderPage(40, 1);
  await gate.whenRead;
  // The cache window is already in hand (it is held, not delayed). The live
  // update lands only now, so this proves the merge uses the newest in-memory
  // model rather than the one captured before the query started.
  expect(gate.lastWindow()?.rows.length).toBe(3);
  expect(repo.accept(historyUpdate(4, 9, "newer", 8), { lifetime: 1 })).toBe(true);
  gate.release();
  const outcome = await lookup;
  expect(outcome.kind).toBe("local");
  if (outcome.kind !== "local") return;
  expect(itemVersionAt(outcome.model, 4)).toBe(9);
  expect(orderedPositions(outcome.model)).toEqual([3, 4, 5, 6, 7, 8]);
});

test("a compatible old-tool completion survives a latest response whose window excludes its position", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const repo = repository(fixture.cache);
  repo.acceptRead(readOf([6, 7, 8], { olderCursor: "C1", length: 8 }), { lifetime: 1, readId: "r1", boundary: "b1" });

  // A compatible completion for an older tool item at position 4, outside the
  // latest window. The isolated update establishes no intervening coverage, but
  // its content must stay in committed rows (or bounded pending work).
  expect(repo.accept(historyUpdate(4, 9, "tool-completed", 8), { lifetime: 1 })).toBe(true);

  // A latest response re-issues the in-window window [6,7,8] and excludes
  // position 4 entirely; item 6 is newer here so the response's version wins.
  const latest = readOf([6, 7, 8], { olderCursor: "C1", length: 8 });
  latest.thread.turns = [
    {
      id: "turn_1",
      itemsView: "full",
      status: "completed",
      version: 8,
      items: [itemOf(6, 9), itemOf(7), itemOf(8)],
    },
  ];
  expect(repo.acceptRead(latest, { lifetime: 1, readId: "r2", boundary: "b2" })).toBe(true);

  const snapshot = repo.getSnapshot();
  expect(holdsPosition(snapshot, 4)).toBe(true);
  expect(itemVersionAt(snapshot, 4)).toBe(9);
  expect(itemVersionAt(snapshot, 6)).toBe(9);
  // The whole completion payload survives, not only its version.
  const item4 = (snapshot?.history?.turns ?? [])
    .flatMap((turn) => turn.items)
    .find((item) => item.position?.entry === 4);
  expect(item4?.text).toBe("tool-completed");

  // The completion's content is preserved in the real cache's committed rows,
  // not merely in memory.
  await repo.settled();
  const rows = await fixture.cache.getLatest(historyKey, 40);
  const row4 = rows.find((row) => row.position.entry === 4);
  expect(row4?.version).toBe(9);
  expect(decodeTranscriptItem(row4?.item as never)?.text).toBe("tool-completed");
});

test("a response cut prunes an overlay delta it already represents, showing the recorded item once", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const repo = repository(fixture.cache);
  repo.acceptRead(readOf([6, 7, 8], { olderCursor: "C1", length: 8 }), { lifetime: 1, readId: "r1", boundary: "b1" });

  // A live preview overlay delta for a communicate call that history has not
  // recorded yet: it is displayed from the overlay alone.
  const preview: OverlayItem = {
    key: "preview:call-6",
    kind: "preview",
    turnId: "turn_1",
    roundId: "r1",
    streamId: "r1/0",
    callId: "call-6",
    item: { type: "agentMessage", id: "preview:call-6", turnId: "turn_1", callId: "call-6", text: "preview" },
  };
  expect(
    repo.accept({ method: "overlay/upserted", params: { ref, threadId, item: preview } } as AnyNotification, {
      lifetime: 1,
    }),
  ).toBe(true);
  expect(Object.keys(repo.getSnapshot()?.overlay ?? {})).toEqual(["preview:call-6"]);

  // A latest response whose cut records the same communicate call and whose
  // overlay still carries the delta. The covered delta must be pruned, not
  // replayed alongside the recorded item.
  const latest = readOf([6, 7, 8], { olderCursor: "C1", length: 8 });
  latest.thread.turns = [
    {
      id: "turn_1",
      itemsView: "full",
      status: "completed",
      version: 8,
      items: [{ ...itemOf(6, 7), callId: "call-6", text: "recorded done" }],
    },
  ];
  latest.overlay = [preview];
  expect(repo.acceptRead(latest, { lifetime: 1, readId: "r2", boundary: "b2" })).toBe(true);

  const snapshot = repo.getSnapshot();
  // No overlay multiplicity: the covered delta is gone from the overlay.
  expect(Object.keys(snapshot?.overlay ?? {})).toEqual([]);
  // Exactly one visible item for the call, carrying the recorded content.
  const visible = (snapshot?.turns ?? [])
    .flatMap((turn) => turn.items)
    .filter((item) => (item as { callId?: string }).callId === "call-6");
  expect(visible).toHaveLength(1);
  expect(visible[0]?.text).toBe("recorded done");
});

test("an invalidating page is refused as a certificate but its invalidation is adopted", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const repo = repository(fixture.cache);
  repo.acceptRead(readOf([6, 7, 8], { olderCursor: "C1", length: 8 }), { lifetime: 1, readId: "r1", boundary: "b1" });
  const authority = repo.getBaselineIdentity();
  const reader = repo.attachReader();

  // A newer epoch in the SAME incarnation makes the reducer invalidate history
  // (not replace it): nothing merges until a later latest-window read.
  expect(
    repo.acceptPage(pageOf([3, 4, 5], { nextCursor: "C2", length: 8, epoch: 1 }), {
      lifetime: 1,
      readId: "r2",
      boundary: "p-inv",
      authority,
      requestCursor: "C1",
    }),
  ).toBe(false);
  expect(await fixture.cache.getCoverage(historyKey, "page:r2:p-inv")).toBeUndefined();

  // Refusing the certificate must not discard the reducer's invalidation: both
  // the repository and its held reader keep the invalid model.
  expect(repo.getSnapshot()?.history?.invalidatedAtGeneration).not.toBeUndefined();
  expect(reader.getSnapshot()?.history?.invalidatedAtGeneration).not.toBeUndefined();
  // The repository's recovery state moved with it: the reader reports it.
  expect((await reader.openLocal(40)).status).toBe("recovery-needed");

  // A frame from the old generation cannot restore the old valid authority.
  expect(repo.accept(historyUpdate(6, 9, "old-gen", 8), { lifetime: 1 })).toBe(false);
  expect(repo.getSnapshot()?.history?.invalidatedAtGeneration).not.toBeUndefined();
  expect(reader.getSnapshot()?.history?.invalidatedAtGeneration).not.toBeUndefined();
  reader.dispose();
});

test("an authoritative reconciliation during the local lookup retires the old window", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const repo = repository(fixture.cache);
  repo.acceptRead(readOf([6, 7, 8], { olderCursor: "C1", length: 8 }), { lifetime: 1, readId: "r1", boundary: "b1" });
  const authority = repo.getBaselineIdentity();
  await commitLocalPage(fixture.cache, authority);

  const gate = holdOlderWindow();
  const lookup = repo.loadOlderPage(40, 1);
  await gate.whenCalled;
  repo.acceptRead(readOf([5, 6, 7, 8], { olderCursor: "C9", authoritative: true, length: 9 }), {
    lifetime: 1,
    readId: "r-auth",
    boundary: "b-auth",
  });
  gate.release();
  const outcome = await lookup;
  expect(outcome.kind).toBe("reconcile");
  // The obsolete window's rows never entered the replacement model.
  expect(orderedPositions(repo.getSnapshot())).toEqual([5, 6, 7, 8]);
  expect(holdsPosition(repo.getSnapshot(), 3)).toBe(false);
});

test("a disposal during an outstanding lookup publishes nothing", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const repo = repository(fixture.cache);
  repo.acceptRead(readOf([6, 7, 8], { olderCursor: "C1", length: 8 }), { lifetime: 1, readId: "r1", boundary: "b1" });
  const authority = repo.getBaselineIdentity();
  await commitLocalPage(fixture.cache, authority);

  let publishes = 0;
  const unsubscribe = repo.subscribe(() => {
    publishes += 1;
  });
  const gate = holdOlderWindow();
  const lookup = repo.loadOlderPage(40, 1);
  await gate.whenCalled;
  repo.dispose();
  gate.release();
  const outcome = await lookup;
  expect(outcome.kind).not.toBe("local");
  expect(publishes).toBe(0);
  unsubscribe();
  expect(repo.getSnapshot()).toBeUndefined();
});

test("a page completion raced with a newer item version and reader disposal keeps the newer content and leaves the repository alive", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const repo = repository(fixture.cache);
  repo.acceptRead(readOf([6, 7, 8], { olderCursor: "C1", length: 8 }), { lifetime: 1, readId: "r1", boundary: "b1" });
  const authority = repo.getBaselineIdentity();
  await commitLocalPage(fixture.cache, authority);
  const reader = repo.attachReader();
  // openLocal restores the latest read window; the older page below it is
  // served by the page completion under test.
  expect(orderedPositions((await reader.openLocal(40)).model)).toEqual([6, 7, 8]);

  // Hold the real older-window lookup after the cache result was read, so both
  // a newer item version and the reader's disposal land mid-completion.
  const gate = holdOlderWindowAfterRead();
  const lookup = repo.loadOlderPage(40, 1);
  await gate.whenRead;
  expect(repo.accept(historyUpdate(4, 9, "newer", 8), { lifetime: 1 })).toBe(true);
  reader.dispose();
  gate.release();
  const outcome = await lookup;
  expect(outcome.kind).toBe("local");

  // The newer content is kept; the retired reader holds nothing to publish.
  expect(itemVersionAt(repo.getSnapshot(), 4)).toBe(9);
  expect(orderedPositions(repo.getSnapshot())).toEqual([3, 4, 5, 6, 7, 8]);
  expect(reader.getSnapshot()).toBeUndefined();

  // The disposal did not take the repository down: a fresh reader attaches and
  // opens against the same live repository.
  const later = repo.attachReader();
  expect(orderedPositions((await later.openLocal(40)).model)).toEqual([3, 4, 5, 6, 7, 8]);
  later.dispose();
  repo.dispose();
});

test("a disposed reader's in-flight openLocal publishes nothing and the repository keeps its newer content", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const repo = repository(fixture.cache);
  repo.acceptRead(readOf([6, 7, 8], { olderCursor: "C1", length: 8 }), { lifetime: 1, readId: "r1", boundary: "b1" });
  const authority = repo.getBaselineIdentity();
  await commitLocalPage(fixture.cache, authority);
  const reader = repo.attachReader();

  // Hold the real latest-window read openLocal uses, completing it first and
  // then holding before it returns, so the disposal races a finished read.
  const realGetLatestWindow = fixture.cache.getLatestWindow.bind(fixture.cache);
  let releaseGate!: () => void;
  const gate = new Promise<void>((resolve) => {
    releaseGate = resolve;
  });
  let markRead!: () => void;
  const read = new Promise<void>((resolve) => {
    markRead = resolve;
  });
  fixture.cache.getLatestWindow = (async (historyKeyArg: string, limit: number, boundary?: string) => {
    const window = await realGetLatestWindow(historyKeyArg, limit, boundary);
    markRead();
    await gate;
    return window;
  }) as typeof fixture.cache.getLatestWindow;
  try {
    let publishes = 0;
    const unsubscribe = repo.subscribe(() => {
      publishes += 1;
    });
    const restore = reader.openLocal(40);
    await read;
    expect(repo.accept(historyUpdate(4, 9, "newer", 8), { lifetime: 1 })).toBe(true);
    reader.dispose();
    const before = publishes;
    releaseGate();
    const result = await restore;
    // The retired reader's restore published nothing after its disposal.
    expect(result.status).toBe("recovery-needed");
    expect(publishes).toBe(before);
    expect(reader.getSnapshot()).toBeUndefined();
    expect(itemVersionAt(repo.getSnapshot(), 4)).toBe(9);
    unsubscribe();
  } finally {
    repo.dispose();
  }
});

test.each(["empty", "short"])(
  "a %s raw page after eviction cannot erase an unrestored local frontier",
  async (page) => {
    const cache = new SessionEntryCache({
      indexedDB: new IDBFactory(),
      keyRange: IDBKeyRange,
      databaseName: `entry-local-frontier-${crypto.randomUUID()}`,
      maxBytes: 20_000,
      now: () => 1000,
    });
    const repo = repository(cache, 1, () => 1000);
    try {
      repo.acceptRead(readOf([1, 2, 3, 4, 5, 6, 7, 8, 9, 10]), { lifetime: 1, readId: "r1", boundary: "b1" });
      await repo.settled();
      const reader = repo.attachReader();
      const opened = await reader.openLocal(3);
      // Round 22 supersedes seven empty DISPLAY strings before row-8..10:
      // identities belong in history as eviction/frontier evidence, not rows.
      expect(
        opened.model?.history?.turns
          .flatMap((turn) => turn.items)
          .filter((item) => item.position && item.position.entry < 8)
          .map((item) => ({ key: item.transcriptKey, position: item.position, text: item.text })),
      ).toEqual(
        Array.from({ length: 7 }, (_, index) => ({
          key: `item-${index + 1}`,
          position: { entry: index + 1, item: 0 },
          text: "",
        })),
      );
      expect(opened.model?.turns.flatMap((turn) => turn.items.map((item) => item.text))).toEqual([
        "row-8",
        "row-9",
        "row-10",
      ]);
      expect(repo.localOlderPosition()).toEqual({ entry: 8, item: 0 });
      expect(repo.getSnapshot()?.olderCursor).toBeUndefined();
      const before = await cache.getByteTotals(historyKey);
      expect(
        (
          await cache.commitBatch({
            historyKey: "other-history",
            clearEpoch: 0,
            items: [
              {
                historyKey: "other-history",
                transcriptKey: "other",
                position: { entry: 1, item: 0 },
                version: 1,
                item: { ...itemOf(1), text: "x".repeat(20_000 - before.historyBytes + 500) },
              },
            ],
          })
        ).outcome,
      ).toBe("committed");
      expect(await cache.getLatest(historyKey, 40)).toEqual([]);
      if (page === "short") {
        expect(
          (
            await cache.commitBatch({
              historyKey,
              clearEpoch: 0,
              items: [
                { historyKey, transcriptKey: "item-1", position: { entry: 1, item: 0 }, version: 1, item: itemOf(1) },
              ],
            })
          ).outcome,
        ).toBe("committed");
      }
      expect(await cache.getClearEpoch()).toBe(0);
      expect((await repo.loadOlderPage(40, 1)).kind).toBe("reconcile");
      expect(repo.localOlderPosition()).toEqual({ entry: 8, item: 0 });
      // Same supersession after eviction: seven identities remain in history,
      // rather than the old seven-empty-display-row expectation.
      expect(
        reader
          .getSnapshot()
          ?.history?.turns.flatMap((turn) => turn.items)
          .filter((item) => item.position && item.position.entry < 8)
          .map((item) => ({ key: item.transcriptKey, position: item.position, text: item.text })),
      ).toEqual(
        Array.from({ length: 7 }, (_, index) => ({
          key: `item-${index + 1}`,
          position: { entry: index + 1, item: 0 },
          text: "",
        })),
      );
      expect(reader.getSnapshot()?.turns.flatMap((turn) => turn.items.map((item) => item.text))).toEqual([
        "row-8",
        "row-9",
        "row-10",
      ]);
      reader.dispose();
    } finally {
      repo.dispose();
      cache.close();
    }
  },
);

test("raw paging preserves missing-metadata rows evicted between payload reads", async () => {
  const cache = new SessionEntryCache({
    indexedDB: new IDBFactory(),
    keyRange: IDBKeyRange,
    databaseName: `entry-metadata-race-${crypto.randomUUID()}`,
    maxBytes: 20_000,
    now: () => 1000,
  });
  const repo = repository(cache, 1, () => 1000);
  const response = readOf([1, 2, 3, 4, 5, 6, 7]);
  response.thread.turns!.push({
    ...response.thread.turns![0]!,
    id: "turn_2",
    items: [8, 9, 10].map((entry) => ({ ...itemOf(entry), turnId: "turn_2" })),
  });
  response.snapshot = { incarnation: identity.incarnation, length: 10 };
  try {
    repo.acceptRead(response, { lifetime: 1, readId: "r1", boundary: "b1" });
    await repo.settled();
    const reader = repo.attachReader();
    try {
      const opened = await reader.openLocal(3);
      expect(opened.model?.turns.map((turn) => turn.id)).toEqual(["turn_2"]);
      expect(opened.model?.olderCursor).toBeUndefined();
      expect(repo.localOlderPosition()).toEqual({ entry: 8, item: 0 });
      const savedRows = await cache.getLatest(historyKey, 40);
      const savedTurns = reader.getSnapshot()!.history!.turns;
      const getBefore = cache.getBefore.bind(cache);
      vi.spyOn(cache, "getBefore").mockImplementationOnce(async (...args) => {
        const rows = await getBefore(...args);
        expect(rows).toHaveLength(7);
        const before = await cache.getByteTotals(historyKey);
        expect(
          (
            await cache.commitBatch({
              historyKey: "other-history",
              clearEpoch: 0,
              items: [
                {
                  historyKey: "other-history",
                  transcriptKey: "other",
                  position: { entry: 1, item: 0 },
                  version: 1,
                  item: { ...itemOf(1), text: "x".repeat(20_000 - before.historyBytes + 500) },
                },
              ],
            })
          ).outcome,
        ).toBe("committed");
        expect(await cache.getTurn(historyKey, "turn_1")).toBeUndefined();
        return rows;
      });
      expect((await repo.loadOlderPage(40, 1)).kind).toBe("reconcile");
      expect(repo.localOlderPosition()).toEqual({ entry: 8, item: 0 });
      expect(reader.getSnapshot()?.olderCursor).toBeUndefined();
      // Once metadata is available again, the retained raw frontier can serve
      // the bodies without a server cursor or a new read certificate.
      expect(
        (await cache.commitBatch({ historyKey, clearEpoch: 0, items: savedRows, turns: savedTurns })).outcome,
      ).toBe("committed");
      expect((await repo.loadOlderPage(40, 1)).kind).toBe("local");
      expect(reader.getSnapshot()?.turns.flatMap((turn) => turn.items.map((item) => item.text))).toEqual(
        Array.from({ length: 10 }, (_, index) => `row-${index + 1}`),
      );
      expect(repo.localOlderPosition()).toBeUndefined();
    } finally {
      reader.dispose();
    }
  } finally {
    repo.dispose();
    cache.close();
  }
});

test("an open reader keeps its loaded rows when disk eviction removes the history", async () => {
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
    // Matched clocks: repository metadata and the cache policy share one clock.
    const repo = repository(cache, 1, () => now);
    repo.acceptRead(readOf([1, 2, 3, 4, 5, 6, 7, 8, 9, 10], { length: 10 }), {
      lifetime: 1,
      readId: "r1",
      boundary: "b1",
    });
    await repo.settled();
    const reader = repo.attachReader();
    const opened = await reader.openLocal(40);
    expect(orderedPositions(opened.model ?? repo.getSnapshot())).toEqual([1, 2, 3, 4, 5, 6, 7, 8, 9, 10]);

    const before = await cache.getByteTotals(historyKey);
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
    expect(await cache.getLatest(historyKey, 40)).toEqual([]);

    // The retained reader still holds its own loaded rows in memory after the
    // history's disk rows are gone. This proves ten loaded latest-window rows,
    // not ten separately loaded pages.
    expect(orderedPositions(reader.getSnapshot())).toEqual([1, 2, 3, 4, 5, 6, 7, 8, 9, 10]);
    reader.dispose();
    repo.dispose();
  } finally {
    cache.close();
  }
});

// Storage is optional for paging, including after successful hydration.
test.each(["rows", "metadata"] as const)("local paging falls back when %s throws", async (stage) => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const repo = repository(fixture.cache);
  const reader = repo.attachReader();
  expect(
    repo.acceptRead(readOf([6, 7, 8], { olderCursor: "C1", length: 8 }), {
      lifetime: 1,
      readId: "r1",
      boundary: "b1",
    }),
  ).toBe(true);
  await repo.settled();
  await commitLocalPage(fixture.cache, repo.getBaselineIdentity());
  const before = repo.getSnapshot();
  const publish = vi.fn();
  const unsubscribe = repo.subscribe(publish);
  const spy = vi.spyOn(fixture.cache, stage === "rows" ? "getOlderWindow" : "getTurn").mockImplementation(() => {
    throw new Error("IndexedDB unavailable");
  });
  try {
    await expect(repo.loadOlderPage(40, 1)).resolves.toEqual({ kind: "server" });
    expect(spy).toHaveBeenCalled();
    expect(repo.getSnapshot()).toBe(before);
    expect(publish).not.toHaveBeenCalled();
  } finally {
    spy.mockRestore();
    unsubscribe();
    reader.dispose();
  }
});

test.each(["rows", "metadata"] as const)("local paging bounds stalled %s and ignores late results", async (stage) => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const repo = repository(fixture.cache);
  const reader = repo.attachReader();
  expect(
    repo.acceptRead(readOf([6, 7, 8], { olderCursor: "C1", length: 8 }), {
      lifetime: 1,
      readId: "r1",
      boundary: "b1",
    }),
  ).toBe(true);
  await repo.settled();
  await commitLocalPage(fixture.cache, repo.getBaselineIdentity());
  const window = await fixture.cache.getOlderWindow(historyKey, { entry: 6, item: 0 }, 40, repo.getBaselineIdentity());
  const turn = await fixture.cache.getTurn(historyKey, "turn_1");
  expect(window.rows).toHaveLength(3);
  const before = repo.getSnapshot();
  const publish = vi.fn();
  const unsubscribe = repo.subscribe(publish);
  let release!: () => void;
  const held = new Promise<void>((resolve) => {
    release = resolve;
  });
  const rowSpy = vi.spyOn(fixture.cache, "getOlderWindow");
  const turnSpy = vi.spyOn(fixture.cache, "getTurn");
  // Keep durable authority reads real while controlling only the two payload reads.
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  rowSpy.mockImplementation(async () => {
    if (stage === "rows") await held;
    else await new Promise((resolve) => setTimeout(resolve, 100));
    return window;
  });
  let metadataEntered!: () => void;
  const metadataCalled = new Promise<void>((resolve) => {
    metadataEntered = resolve;
  });
  turnSpy.mockImplementation(async () => {
    metadataEntered();
    if (stage === "metadata") await held;
    return turn;
  });
  let result: unknown;
  const lookup = repo.loadOlderPage(40, 1).then((value) => {
    result = value;
  });
  try {
    await vi.advanceTimersByTimeAsync(100);
    if (stage === "metadata") await metadataCalled;
    await vi.advanceTimersByTimeAsync(SESSION_CACHE_LOOKUP_DEADLINE_MS - 101);
    expect(result).toBeUndefined();
    await vi.advanceTimersByTimeAsync(1);
    expect(result).toEqual({ kind: "server" });
    expect(publish).not.toHaveBeenCalled();
    release();
    await lookup;
    // Drain real IndexedDB authority reads too, not just promise microtasks.
    await fixture.cache.getHistoryGeneration(historyKey);
    await fixture.cache.getClearEpoch();
    expect(repo.getSnapshot()).toBe(before);
    expect(publish).not.toHaveBeenCalled();
    expect(vi.getTimerCount()).toBe(0);
  } finally {
    release();
    await lookup;
    rowSpy.mockRestore();
    turnSpy.mockRestore();
    vi.useRealTimers();
    unsubscribe();
    reader.dispose();
  }
});
