// Task 4: identity, replacement, Clear and deletion.
//
// Every case drives the REAL repository and the REAL SessionEntryCache over
// fake-indexeddb; no reducer, cache or store behaviour is mocked. The wrong
// behaviour each assertion is meant to catch was reached through these real
// APIs before the fix (see the report).
import "fake-indexeddb/auto";
import type {
  AnyNotification,
  ThreadItem,
  ThreadModel,
  ThreadReadResponse,
  ThreadTurnsListResponse,
} from "@evener/appwire-client";
import { IDBFactory, IDBKeyRange, IDBObjectStore } from "fake-indexeddb";
import { afterEach, expect, test, vi } from "vitest";
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

function itemOf(entry: number, version = entry, text = `row-${entry}`): ThreadItem {
  return {
    type: "agentMessage",
    id: `item-${entry}`,
    transcriptKey: `item-${entry}`,
    turnId: "turn_1",
    position: { entry, item: 0 },
    version,
    text,
  };
}

function readOf(
  entries: number[],
  opts: { olderCursor?: string; length?: number; authoritative?: boolean; requestGeneration?: number } = {},
) {
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
    ...(opts.requestGeneration === undefined ? {} : { requestGeneration: opts.requestGeneration }),
  } as unknown as ThreadReadResponse;
}

function pageOf(entries: number[], opts: { nextCursor?: string; length?: number } = {}): ThreadTurnsListResponse {
  const length = opts.length ?? Math.max(...entries, 0);
  return {
    data: [
      {
        id: "turn_1",
        itemsView: "full",
        status: "completed",
        version: length,
        items: entries.map((entry) => itemOf(entry)),
      },
    ],
    ...(opts.nextCursor === undefined ? {} : { nextCursor: opts.nextCursor }),
    bootGeneration: "1",
    epoch: 0,
    snapshot: { incarnation: identity.incarnation, length },
  } as unknown as ThreadTurnsListResponse;
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

interface RepoOptions {
  lifetime?: number;
  clearEpoch?: number;
  historyGeneration?: number;
  sessionNonce?: string;
  seedModel?: ThreadModel;
  omitSeedAuthority?: boolean;
  isPreferred?: boolean;
}

function repository(cache: SessionEntryCache, options: RepoOptions = {}) {
  const seedModel = options.seedModel;
  return new TranscriptRepository(cache, {
    ref,
    threadId,
    historyKey,
    identity,
    clearEpoch: options.clearEpoch ?? 0,
    historyGeneration: options.historyGeneration,
    sessionNonce: options.sessionNonce,
    lifetime: options.lifetime ?? 1,
    isPreferred: options.isPreferred ? () => true : undefined,
    seedModel,
    seedAuthority:
      seedModel?.history && !options.omitSeedAuthority
        ? {
            trustedCurrentSource: true,
            bootGeneration: seedModel.history.bootGeneration,
            epoch: seedModel.history.epoch,
            incarnation: seedModel.history.incarnation ?? "",
            snapshotLength: seedModel.history.length,
          }
        : undefined,
    now: () => Date.now(),
  });
}

function orderedPositions(model: ThreadModel | undefined): number[] {
  return (model?.history?.turns ?? [])
    .flatMap((turn) => turn.items)
    .map((item) => item.position?.entry)
    .filter((entry): entry is number => entry !== undefined);
}

// Holds the real batch write for one transcript key until released.
function holdNextEntryWrite(cache: SessionEntryCache, transcriptKey: string) {
  const commit = cache.commitBatch.bind(cache);
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
      release();
      spy.mockRestore();
    },
  };
}

// Starts the real cache's older-window query immediately and holds its RESULT
// delivery until released, so a Clear can land while the query is genuinely in
// flight (unlike holding before the call, which would start the query after).
function holdOlderWindowLookup() {
  const original = SessionEntryCache.prototype.getOlderWindow;
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
    const result = original.apply(this, args);
    markCalled();
    return gate.then(() => result);
  } as typeof original);
  return {
    whenCalled: called,
    release() {
      releaseGate();
      spy.mockRestore();
    },
  };
}

afterEach(async () => {
  vi.restoreAllMocks();
  for (const fixture of fixtures.splice(0)) await fixture.dispose();
});

// ---------------------------------------------------------------------------
// Behaviour 1: replacement — old completions cannot enter the replacement.
// ---------------------------------------------------------------------------

test("a replacement lifetime rejects a held read, page and notification captured for the old binding", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  // The outgoing repository (lifetime 1) holds a read and a page.
  const outgoing = repository(fixture.cache, { lifetime: 1 });
  outgoing.acceptRead(readOf([6, 7, 8], { olderCursor: "C1", length: 8 }), {
    lifetime: 1,
    readId: "r1",
    boundary: "b1",
  });
  const outgoingAuthority = outgoing.getBaselineIdentity();
  outgoing.acceptPage(pageOf([3, 4, 5], { nextCursor: "C2", length: 8 }), {
    lifetime: 1,
    readId: "r1",
    boundary: "p1",
    authority: outgoingAuthority,
    requestCursor: "C1",
  });
  await outgoing.settled();
  outgoing.dispose();

  // The replacement binding (lifetime 2) is the live owner now.
  const replacement = repository(fixture.cache, { lifetime: 2 });
  replacement.acceptRead(readOf([6, 7, 8], { length: 8 }), { lifetime: 2, readId: "r2", boundary: "b2" });

  // A completion still stamped with the old lifetime is refused, not merged
  // into the replacement.
  expect(replacement.accept(historyUpdate(4, 9, "old-lifetime", 8), { lifetime: 1 })).toBe(false);
  expect(
    replacement.acceptPage(pageOf([1, 2], { length: 8 }), {
      lifetime: 1,
      readId: "r1",
      boundary: "p-old",
      authority: outgoingAuthority,
      requestCursor: "C2",
    }),
  ).toBe(false);
  expect(
    replacement.acceptRead(readOf([1, 2, 3], { length: 8 }), { lifetime: 1, readId: "r-old", boundary: "b-old" }),
  ).toBe(false);
  expect(orderedPositions(replacement.getSnapshot())).toEqual([6, 7, 8]);
  replacement.dispose();
});

test("an untrusted seed's running status and capabilities cannot authorize a current read", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  // A committed model becomes a stale seed on reopen. It advertises an active
  // status and full capabilities, but its caller supplied no trusted source
  // authority, so it must not authorize use.
  const committed = repository(fixture.cache, { lifetime: 1 });
  committed.acceptRead(readOf([6, 7, 8], { length: 8 }), { lifetime: 1, readId: "r1", boundary: "b1" });
  await committed.settled();
  const seed = committed.getSnapshot();
  committed.dispose();
  if (!seed) throw new Error("expected a committed model to seed from");

  const reopened = repository(fixture.cache, { lifetime: 1, seedModel: seed, omitSeedAuthority: true });
  const reader = reopened.attachReader();
  const opened = await reader.openLocal(40);
  // Content is usable (readable) but the route is not allowed to claim
  // currentness from an unauthorized seed.
  expect(opened.model).toBeDefined();
  expect(opened.status).toBe("recovery-needed");
  reader.dispose();
  reopened.dispose();
});

test("an authoritative read after a fresh baseline restores current use and usable paging", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const repo = repository(fixture.cache, { lifetime: 1 });
  repo.acceptRead(readOf([6, 7, 8], { olderCursor: "C1", length: 8 }), { lifetime: 1, readId: "r1", boundary: "b1" });
  await repo.settled();
  // Reconnect/replacement retires the held chain.
  repo.beginFreshBaseline();
  repo.acceptRead(readOf([6, 7, 8], { olderCursor: "C2", length: 8, authoritative: true, requestGeneration: 1 }), {
    lifetime: 1,
    readId: "r2",
    boundary: "b2",
  });
  await repo.settled();
  const reader = repo.attachReader();
  const opened = await reader.openLocal(40);
  expect(opened.status).toBe("current");
  expect(orderedPositions(opened.model)).toEqual([6, 7, 8]);
  reader.dispose();
  repo.dispose();
});

// ---------------------------------------------------------------------------
// Behaviour 2: two cache connections.
// ---------------------------------------------------------------------------

test("a newer shared-item version wins and a shorter same-identity window cannot erase committed coverage", async () => {
  const name = `entry-lifecycle-tabs-${crypto.randomUUID()}`;
  const factory = new IDBFactory();
  const tabA = new SessionEntryCache({ indexedDB: factory, keyRange: IDBKeyRange, databaseName: name });
  const tabB = new SessionEntryCache({ indexedDB: factory, keyRange: IDBKeyRange, databaseName: name });
  try {
    // Tab A commits the long window first.
    await tabA.commitBatch({
      historyKey,
      clearEpoch: 0,
      items: [1, 2, 3, 4].map((entry) => ({
        historyKey,
        transcriptKey: `item-${entry}`,
        position: { entry, item: 0 },
        version: entry,
        item: { id: `item-${entry}`, turnId: "turn_1", type: "agentMessage", text: `row-${entry}` },
      })),
      coverage: {
        kind: "read",
        readId: "read-long",
        start: { entry: 1, item: 0 },
        end: { entry: 4, item: 0 },
        representedSnapshot: 4,
        boundary: "long",
      },
    });
    const longCoverage = await tabA.getCoverage(historyKey, "read:read-long");
    expect(longCoverage?.representedSnapshot).toBe(4);

    // Tab B commits a shorter window for the same history identity but a
    // different read id, carrying a NEWER version of the shared item. It must
    // not erase A's longer coverage, and the newer version must win.
    await expect(
      tabB.commitBatch({
        historyKey,
        clearEpoch: 0,
        items: [
          {
            historyKey,
            transcriptKey: "item-1",
            position: { entry: 1, item: 0 },
            version: 5,
            item: { id: "item-1", turnId: "turn_1", type: "agentMessage", text: "row-1-newer" },
          },
        ],
        coverage: {
          kind: "read",
          readId: "read-short",
          start: { entry: 1, item: 0 },
          end: { entry: 1, item: 0 },
          representedSnapshot: 1,
          boundary: "short",
        },
      }),
    ).resolves.toMatchObject({ outcome: "committed" });
    expect((await tabA.getCoverage(historyKey, "read:read-long"))?.representedSnapshot).toBe(4);
    await expect(tabB.getCoverage(historyKey, "read:read-short")).resolves.toMatchObject({ representedSnapshot: 1 });
    const merged = await tabB.getLatest(historyKey, 40);
    expect(merged).toHaveLength(4);
    expect(merged.find((row) => row.transcriptKey === "item-1")?.version).toBe(5);
    await expect(tabB.getLatestWindow(historyKey, 40, "long")).resolves.toMatchObject({ complete: true });

    // An equal-version recommit of the same item is a no-op: the shared merge
    // keeps the committed version rather than rewriting identical content.
    await expect(
      tabB.commitBatch({
        historyKey,
        clearEpoch: 0,
        items: [
          {
            historyKey,
            transcriptKey: "item-1",
            position: { entry: 1, item: 0 },
            version: 5,
            item: { id: "item-1", turnId: "turn_1", type: "agentMessage", text: "row-1-newer" },
          },
        ],
      }),
    ).resolves.toMatchObject({ outcome: "committed" });
    expect((await tabB.getLatest(historyKey, 40)).find((row) => row.transcriptKey === "item-1")?.version).toBe(5);
  } finally {
    tabA.close();
    tabB.close();
    await new Promise<void>((resolve) => {
      const request = factory.deleteDatabase(name);
      request.onsuccess = () => resolve();
      request.onerror = () => resolve();
    });
  }
});

test("one tab's successful write does not restore another tab's lost continuity", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  // Two tabs share one cache. Tab B loses continuity after a real abort.
  const tabB = repository(fixture.cache, { lifetime: 1 });
  tabB.acceptRead(readOf([6, 7, 8], { length: 8 }), { lifetime: 1, readId: "rb", boundary: "bb" });
  await tabB.settled();

  const put = IDBObjectStore.prototype.put;
  const spy = vi.spyOn(IDBObjectStore.prototype, "put").mockImplementation(function (
    this: IDBObjectStore,
    ...args: Parameters<IDBObjectStore["put"]>
  ) {
    const value = args[0] as { transcriptKey?: string };
    const request = put.apply(this, args);
    if (this.name === "transcriptItems" && value.transcriptKey === "item-9") this.transaction.abort();
    return request;
  });
  tabB.accept(historyUpdate(9, 9, "aborted", 9), { lifetime: 1 });
  await expect(tabB.settled()).rejects.toThrow(/outstanding|failed|recovery/);
  spy.mockRestore();

  // Tab A commits successfully; its write advances the durable chain but must
  // not clear B's own continuity loss.
  const tabA = repository(fixture.cache, { lifetime: 2 });
  tabA.acceptRead(readOf([6, 7, 8], { length: 8 }), { lifetime: 2, readId: "ra", boundary: "ba" });
  await tabA.settled();
  await expect(fixture.cache.getLatest(historyKey, 40)).resolves.toBeDefined();

  const reader = tabB.attachReader();
  const opened = await reader.openLocal(40);
  expect(opened.status).toBe("recovery-needed");
  reader.dispose();
  tabA.dispose();
  tabB.dispose();
});

// ---------------------------------------------------------------------------
// Real Live/demand preference wiring into the eviction policy.
// ---------------------------------------------------------------------------

test("a live owner's registered demand prefers its history for disk retention", async () => {
  const name = `entry-lifecycle-pref-${crypto.randomUUID()}`;
  const factory = new IDBFactory();
  const identityFor = (sessionId: string): TranscriptResolvedIdentity => ({
    clientId: "c",
    hostId: "h",
    sessionId,
    incarnation: "i",
  });
  const keyFor = (sessionId: string) => entryHistoryKey(identityFor(sessionId));
  const metadataFor = (sessionId: string, stamp: number) => ({
    historyKey: keyFor(sessionId),
    requestedRefs: [{ clientId: "c", ref: `local:${sessionId}` }],
    resolvedIdentity: identityFor(sessionId),
    savedAt: stamp,
    accessedAt: stamp,
    formatVersion: 3,
  });
  const rowsFor = (historyKeyValue: string) =>
    [1, 2].map((entry) => ({
      historyKey: historyKeyValue,
      transcriptKey: `row-${entry}`,
      position: { entry, item: 0 },
      version: entry,
      item: { id: `row-${entry}`, turnId: "turn-1", type: "agentMessage", text: `body-${entry}` },
    }));

  const keyA = keyFor("a");
  const keyB = keyFor("b");
  const keyC = keyFor("c");
  // B is the OLDEST identity (smallest access stamp), so plain LRU would evict
  // B first: only a real registered preference can keep B and take A.
  const metaA = metadataFor("a", 2_000);
  const metaB = metadataFor("b", 1_000);
  const metaC = metadataFor("c", 2_000);
  const measure = new SessionEntryCache({
    indexedDB: factory,
    keyRange: IDBKeyRange,
    databaseName: name,
    ttlMs: Number.MAX_SAFE_INTEGER,
    now: () => 1_000,
  });
  try {
    await measure.commitBatch({ historyKey: keyA, clearEpoch: 0, items: rowsFor(keyA), sessionMetadata: metaA });
    await measure.commitBatch({ historyKey: keyB, clearEpoch: 0, items: rowsFor(keyB), sessionMetadata: metaB });
    const bytesA = (await measure.getByteTotals(keyA)).historyBytes;
    const bytesB = (await measure.getByteTotals(keyB)).historyBytes;
    measure.close();

    const cache = new SessionEntryCache({
      indexedDB: factory,
      keyRange: IDBKeyRange,
      databaseName: name,
      maxBytes: bytesA + bytesB,
      ttlMs: Number.MAX_SAFE_INTEGER,
      now: () => 1_000,
    });
    // A live owner of B registers its real demand through the repository: the
    // forwarding must reach the cache's eviction policy, not only the option.
    const ownerB = new TranscriptRepository(cache, {
      ref: "ref-b",
      threadId: "thr-b",
      historyKey: keyB,
      identity: identityFor("b"),
      clearEpoch: 0,
      lifetime: 1,
      isPreferred: () => true,
    });
    await cache.commitBatch({ historyKey: keyA, clearEpoch: 0, items: rowsFor(keyA), sessionMetadata: metaA });
    await cache.commitBatch({ historyKey: keyB, clearEpoch: 0, items: rowsFor(keyB), sessionMetadata: metaB });
    // A third identity forces exactly one eviction.
    await cache.commitBatch({ historyKey: keyC, clearEpoch: 0, items: rowsFor(keyC), sessionMetadata: metaC });

    await expect(cache.getLatest(keyB, 10)).resolves.toMatchObject([
      { transcriptKey: "row-1" },
      { transcriptKey: "row-2" },
    ]);
    await expect(cache.getLatest(keyA, 10)).resolves.toEqual([]);
    ownerB.dispose();
    cache.close();
  } finally {
    await new Promise<void>((resolve) => {
      const request = factory.deleteDatabase(name);
      request.onsuccess = () => resolve();
      request.onerror = () => resolve();
    });
  }
});

// ---------------------------------------------------------------------------
// Behaviour 3: hold lookup/write work across Clear.
// ---------------------------------------------------------------------------

test("a write captured before a real Clear is refused, and a fresh explicit epoch commits", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const stale = repository(fixture.cache, { lifetime: 1, clearEpoch: 0 });
  stale.acceptRead(readOf([6, 7, 8], { length: 8 }), { lifetime: 1, readId: "r1", boundary: "b1" });
  await stale.settled();
  expect(await fixture.cache.getLatest(historyKey, 10)).toHaveLength(3);

  const cleared = await fixture.cache.clear();
  expect(cleared).toEqual({ committed: true, epoch: 1 });

  // A late accepted update under the old capture is refused: neither the write
  // nor a later probe resurrects the cleared content.
  stale.accept(historyUpdate(9, 9, "after-clear", 9), { lifetime: 1 });
  await stale.settled();
  await expect(fixture.cache.getLatest(historyKey, 10)).resolves.toEqual([]);

  // A fresh explicit foreground acquisition that reads the new epoch may cache.
  const fresh = repository(fixture.cache, { lifetime: 2, clearEpoch: cleared.epoch });
  fresh.acceptRead(readOf([9], { length: 9 }), { lifetime: 2, readId: "r2", boundary: "b2" });
  await fresh.settled();
  await expect(fixture.cache.getLatest(historyKey, 10)).resolves.toMatchObject([{ transcriptKey: "item-9" }]);
  fresh.dispose();
  stale.dispose();
});

test("a local open rechecks the Clear epoch and reports recovery-needed for a stale capture", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const repo = repository(fixture.cache, { lifetime: 1, clearEpoch: 0 });
  repo.acceptRead(readOf([6, 7, 8], { length: 8 }), { lifetime: 1, readId: "r1", boundary: "b1" });
  await repo.settled();
  const reader = repo.attachReader();
  await fixture.cache.clear();
  const opened = await reader.openLocal(40);
  expect(opened.status).toBe("recovery-needed");
  reader.dispose();
  repo.dispose();
});

// ---------------------------------------------------------------------------
// Behaviour 4: Clear suppression survives discovery, reconnect and probes.
// ---------------------------------------------------------------------------

test("Clear suppression survives a reconnect and a newer-read probe; only a fresh epoch caches", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const stale = repository(fixture.cache, { lifetime: 1, clearEpoch: 0 });
  stale.acceptRead(readOf([6, 7, 8], { length: 8 }), { lifetime: 1, readId: "r1", boundary: "b1" });
  await stale.settled();
  const cleared = await fixture.cache.clear();
  expect(cleared.committed).toBe(true);

  // Reconnect/rediscovery mints a fresh baseline, but the durable Clear
  // suppression is a separate authority: the old epoch still refuses.
  stale.beginFreshBaseline();
  stale.acceptRead(readOf([6, 7, 8, 9], { length: 9 }), {
    lifetime: 1,
    readId: "r2",
    boundary: "b2",
    authority: stale.getBaselineIdentity(),
  } as unknown as { lifetime: number; readId: string; boundary: string });
  await stale.settled();
  await expect(fixture.cache.getLatest(historyKey, 10)).resolves.toEqual([]);

  // An aborted Clear leaves the durable epoch unchanged, so an epoch-0
  // capture downstream still caches (independent suppression is not undone by
  // a failed clear).
  stale.dispose();
  const after = repository(fixture.cache, { lifetime: 2, clearEpoch: cleared.epoch });
  after.acceptRead(readOf([1], { length: 1 }), { lifetime: 2, readId: "r3", boundary: "b3" });
  await after.settled();
  await expect(fixture.cache.getLatest(historyKey, 10)).resolves.toMatchObject([{ transcriptKey: "item-1" }]);
  after.dispose();
});

// ---------------------------------------------------------------------------
// Behaviour 5: delete while work is queued or in flight.
// ---------------------------------------------------------------------------

test("a delete fences captured work and a fresh-generation acquisition may cache again", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const owner = repository(fixture.cache, { lifetime: 1, historyGeneration: 0 });
  owner.acceptRead(readOf([6, 7, 8], { length: 8 }), { lifetime: 1, readId: "r1", boundary: "b1" });
  await owner.settled();
  expect(await fixture.cache.getLatest(historyKey, 10)).toHaveLength(3);
  const reader = owner.attachReader();

  // Delete the session while the owner still holds queued/live work.
  expect(await fixture.cache.deleteHistory(historyKey)).toBe(true);
  expect(await fixture.cache.getHistoryGeneration(historyKey)).toBe(1);
  await expect(fixture.cache.getLatest(historyKey, 10)).resolves.toEqual([]);
  await expect(fixture.cache.getCoverage(historyKey, "read:r1")).resolves.toBeUndefined();

  // A late page and a late write from the pre-delete capture must not
  // resurrect the deleted rows.
  owner.acceptPage(pageOf([1, 2, 3], { nextCursor: "C2", length: 8 }), {
    lifetime: 1,
    readId: "r1",
    boundary: "p1",
    authority: owner.getBaselineIdentity(),
    requestCursor: "C1",
  });
  owner.accept(historyUpdate(9, 9, "late", 9), { lifetime: 1 });
  await owner.settled();
  await expect(fixture.cache.getLatest(historyKey, 10)).resolves.toEqual([]);
  reader.dispose();
  owner.dispose();

  // A fresh explicit foreground acquisition reads the new generation and may
  // cache: the delete fence admits a genuinely post-delete writer.
  const fresh = repository(fixture.cache, {
    lifetime: 2,
    historyGeneration: await fixture.cache.getHistoryGeneration(historyKey),
  });
  fresh.acceptRead(readOf([1], { length: 1 }), { lifetime: 2, readId: "r2", boundary: "b2" });
  await fresh.settled();
  await expect(fixture.cache.getLatest(historyKey, 10)).resolves.toMatchObject([{ transcriptKey: "item-1" }]);
  fresh.dispose();
});

test("client teardown retires only its own claims", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const first = repository(fixture.cache, { lifetime: 1, historyGeneration: 0 });
  first.acceptRead(readOf([6, 7, 8], { length: 8 }), { lifetime: 1, readId: "r1", boundary: "b1" });
  await first.settled();
  const second = repository(fixture.cache, { lifetime: 2, historyGeneration: 0 });
  second.acceptRead(readOf([6, 7, 8], { length: 8 }), { lifetime: 2, readId: "r2", boundary: "b2" });
  await second.settled();

  first.dispose();
  // The survivor still accepts and caches its own work.
  second.accept(historyUpdate(9, 9, "survivor", 9), { lifetime: 2 });
  await second.settled();
  await expect(fixture.cache.getLatest(historyKey, 40)).resolves.toEqual(
    expect.arrayContaining([
      expect.objectContaining({ transcriptKey: "item-9", item: expect.objectContaining({ text: "survivor" }) }),
    ]),
  );
  second.dispose();
});

// ---------------------------------------------------------------------------
// Behaviour 6: reopen after a simulated crash.
// ---------------------------------------------------------------------------

test("no page continuity certificate survives the browser lifetime", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  // A previous browser lifetime commits a latest read and two older pages.
  const previous = repository(fixture.cache, { lifetime: 1, sessionNonce: "browser-lifetime-A" });
  previous.acceptRead(readOf([6, 7, 8], { olderCursor: "C1", length: 8 }), {
    lifetime: 1,
    readId: "r1",
    boundary: "b1",
  });
  const authority = previous.getBaselineIdentity();
  previous.acceptPage(pageOf([3, 4, 5], { nextCursor: "C2", length: 8 }), {
    lifetime: 1,
    readId: "r1",
    boundary: "p1",
    authority,
    requestCursor: "C1",
  });
  await previous.settled();
  previous.dispose();

  // A crash/reload begins a NEW browser lifetime: a fresh repository for the
  // same history must not continue the previous lifetime's page chain, even
  // though the binding's per-tab lifetime counter starts at the same value.
  const reopened = repository(fixture.cache, { lifetime: 1, sessionNonce: "browser-lifetime-B" });
  reopened.acceptRead(readOf([6, 7, 8], { length: 8 }), { lifetime: 1, readId: "r2", boundary: "b2" });
  await reopened.settled();

  const frontier = await fixture.cache.getOlderWindow(
    historyKey,
    { entry: 6, item: 0 },
    40,
    reopened.getBaselineIdentity(),
  );
  expect(frontier.rows).toEqual([]);
  expect(frontier.stale).toBe(true);
  reopened.dispose();
});

test("committed rows reopen as a stale seed with no overlay, and a source read restores currentness", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const committed = repository(fixture.cache, { lifetime: 1, sessionNonce: "browser-lifetime-A" });
  committed.acceptRead(readOf([6, 7, 8], { length: 8 }), { lifetime: 1, readId: "r1", boundary: "b1" });
  await committed.settled();
  const seed = committed.getSnapshot();
  if (!seed) throw new Error("expected a committed seed");
  // Uncommitted live overlay work that the crash will lose.
  committed.accept(
    {
      method: "overlay/upserted",
      params: {
        ref,
        threadId,
        item: {
          key: "crash-overlay",
          kind: "stream",
          turnId: "turn_1",
          roundId: "crash-round",
          streamId: "crash-stream",
          item: {
            type: "agentMessage",
            id: "crash-overlay-item",
            turnId: "turn_1",
            text: "CRASH_OVERLAY_BODY",
            status: "inProgress",
          },
        },
      },
    } as AnyNotification,
    { lifetime: 1 },
  );
  await committed.settled();
  expect(JSON.stringify(committed.getSnapshot() ?? null)).toContain("CRASH_OVERLAY_BODY");
  committed.dispose();

  // The committed rows are a readable stale seed on disk; the uncommitted
  // overlay did not survive memory.
  const seededWindow = await fixture.cache.getLatestWindow(historyKey, 40);
  expect(seededWindow.rows.map((row) => row.transcriptKey)).toEqual(["item-6", "item-7", "item-8"]);
  expect(seededWindow.stale).toBe(false);

  // A crash reopen has the committed rows as a readable stale seed with NO
  // overlay, before any source recovery.
  const reopened = repository(fixture.cache, {
    lifetime: 1,
    sessionNonce: "browser-lifetime-A",
    seedModel: seed,
    omitSeedAuthority: true,
  });
  const preReader = reopened.attachReader();
  const preRead = await preReader.openLocal(40);
  expect(preRead.status).not.toBe("current");
  expect(orderedPositions(preRead.model)).toEqual([6, 7, 8]);
  expect(JSON.stringify(preRead.model ?? null)).not.toContain("CRASH_OVERLAY_BODY");
  preReader.dispose();

  // A source read restores currentness (issued after the invalidation).
  reopened.acceptRead(readOf([6, 7, 8], { length: 8, requestGeneration: 1 }), {
    lifetime: 1,
    readId: "r2",
    boundary: "b2",
  });
  await reopened.settled();
  const reader = reopened.attachReader();
  const restored = await reader.openLocal(40);
  expect(restored.status).toBe("current");
  expect(JSON.stringify(restored.model ?? null)).not.toContain("CRASH_OVERLAY_BODY");
  reader.dispose();
  reopened.dispose();
});

test("a write held in flight across a delete is refused and cannot resurrect the deleted rows", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const owner = repository(fixture.cache, { lifetime: 1, historyGeneration: 0 });
  owner.acceptRead(readOf([6, 7, 8], { length: 8 }), { lifetime: 1, readId: "r1", boundary: "b1" });
  await owner.settled();

  // Hold a real in-flight batch, queue a second behind it, delete the history
  // under both, then release.
  const gate = holdNextEntryWrite(fixture.cache, "item-9");
  owner.accept(historyUpdate(9, 9, "in-flight", 9), { lifetime: 1 });
  await gate.reached;
  owner.accept(historyUpdate(10, 10, "queued", 10), { lifetime: 1 });
  expect(await fixture.cache.deleteHistory(historyKey)).toBe(true);
  gate.release();
  await owner.settled();
  await expect(fixture.cache.getLatest(historyKey, 10)).resolves.toEqual([]);
  await expect(fixture.cache.getHistoryGeneration(historyKey)).resolves.toBe(1);
  owner.dispose();
});

test("a write held across a replacement cannot republish into the replacement", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const outgoing = repository(fixture.cache, { lifetime: 1 });
  // A reader keeps the outgoing write on the foreground (immediate) path so the
  // two repositories do not contend for the shared background drain.
  const outgoingReader = outgoing.attachReader();
  outgoing.acceptRead(readOf([6, 7, 8], { length: 8 }), { lifetime: 1, readId: "r1", boundary: "b1" });
  await outgoing.settled();

  // Hold a real outgoing write, then install the replacement binding.
  const gate = holdNextEntryWrite(fixture.cache, "item-9");
  outgoing.accept(historyUpdate(9, 9, "outgoing-held", 9), { lifetime: 1 });
  await gate.reached;
  const replacement = repository(fixture.cache, { lifetime: 2 });
  const replacementReader = replacement.attachReader();
  replacement.acceptRead(readOf([6, 7, 8], { length: 8, authoritative: true, requestGeneration: 1 }), {
    lifetime: 2,
    readId: "r2",
    boundary: "b2",
  });
  // The replacement publishes its model synchronously; its write queues behind
  // the held outgoing batch, so settle only after the gate releases.
  const replacementBefore = JSON.stringify(replacement.getSnapshot() ?? null);

  // The outgoing held write releases after the replacement exists: it must not
  // re-enter or mutate the replacement's model.
  gate.release();
  await outgoing.settled();
  await replacement.settled();
  expect(JSON.stringify(replacement.getSnapshot() ?? null)).toBe(replacementBefore);
  // The replacement still owns its own publication.
  expect(replacement.accept(historyUpdate(10, 10, "replacement", 10), { lifetime: 2 })).toBe(true);
  expect(JSON.stringify(replacement.getSnapshot() ?? null)).toContain("replacement");
  replacementReader.dispose();
  outgoingReader.dispose();
  replacement.dispose();
  outgoing.dispose();
});

test("an older-page lookup held across a real Clear publishes no stale page", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const repo = repository(fixture.cache, { lifetime: 1, clearEpoch: 0 });
  repo.acceptRead(readOf([6, 7, 8], { olderCursor: "C1", length: 8 }), { lifetime: 1, readId: "r1", boundary: "b1" });
  const authority = repo.getBaselineIdentity();
  repo.acceptPage(pageOf([3, 4, 5], { nextCursor: "C2", length: 8 }), {
    lifetime: 1,
    readId: "r1",
    boundary: "p1",
    authority,
    requestCursor: "C1",
  });
  await repo.settled();

  const gate = holdOlderWindowLookup();
  const publications: number[] = [];
  const unsubscribe = repo.subscribe(() => publications.push(orderedPositions(repo.getSnapshot()).length));
  const lookup = repo.loadOlderPage(40, 1);
  await gate.whenCalled;
  // The real older-window query is genuinely in flight now; Clear lands while it
  // is running, with unchanged client and source identity.
  await expect(fixture.cache.clear()).resolves.toEqual({ committed: true, epoch: 1 });
  const publishedBeforeRelease = publications.length;
  const releasedAt = Date.now();
  gate.release();
  const outcome = await lookup;
  // The held lookup publishes no stale page: the durable Clear epoch it read
  // under has moved, so it routes to the bounded reconciliation and the already
  // loaded display rows are untouched.
  expect(outcome.kind).toBe("reconcile");
  expect(Date.now() - releasedAt).toBeLessThan(2_000);
  expect(publications.length).toBe(publishedBeforeRelease);
  expect(orderedPositions(repo.getSnapshot())).toEqual([3, 4, 5, 6, 7, 8]);
  unsubscribe();
  repo.dispose();
});

test("a committed Clear's suppression stands through a real aborted Clear", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const before = repository(fixture.cache, { lifetime: 1, clearEpoch: 0 });
  before.acceptRead(readOf([6, 7, 8], { length: 8 }), { lifetime: 1, readId: "r1", boundary: "b1" });
  await before.settled();
  await expect(fixture.cache.clear()).resolves.toEqual({ committed: true, epoch: 1 });

  // A real aborted Clear at the IDB boundary: the epoch row is untouched.
  const spy = vi.spyOn(IDBObjectStore.prototype, "clear").mockImplementationOnce(() => {
    throw new Error("clear storage fault");
  });
  await expect(fixture.cache.clear()).resolves.toEqual({ committed: false, epoch: 1 });
  spy.mockRestore();

  // The committed suppression stands: the old-epoch write is refused.
  before.accept(historyUpdate(9, 9, "after", 9), { lifetime: 1 });
  await before.settled();
  await expect(fixture.cache.getLatest(historyKey, 10)).resolves.toEqual([]);

  // A current-epoch repository is admitted.
  const after = repository(fixture.cache, { lifetime: 2, clearEpoch: 1 });
  after.acceptRead(readOf([1], { length: 1 }), { lifetime: 2, readId: "r2", boundary: "b2" });
  await after.settled();
  await expect(fixture.cache.getLatest(historyKey, 10)).resolves.toMatchObject([{ transcriptKey: "item-1" }]);
  before.dispose();
  after.dispose();
});
