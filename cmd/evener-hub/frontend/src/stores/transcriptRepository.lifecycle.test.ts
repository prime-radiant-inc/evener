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
  suppressed?: () => boolean;
  retryScheduler?: (attempt: number, retry: () => void) => () => void;
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
    suppressed: options.suppressed,
    retryScheduler: options.retryScheduler,
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

test.each([undefined, ""])("opening during a held update preserves omitted payload, text=%j", async (text) => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const repo = repository(fixture.cache);
  expect(repo.acceptRead(readOf([8]), { lifetime: 1, readId: "payload", boundary: "payload" })).toBe(true);
  await repo.settled();
  const held = holdNextEntryWrite(fixture.cache, "item-8");
  const update = historyUpdate(8, 9, "", 9);
  if (update.method !== "history/updated") throw new Error("wrong fixture");
  update.params.items = [{ ...itemOf(8, 9), text }];
  expect(repo.accept(update, { lifetime: 1 })).toBe(true);
  await held.reached;
  const reader = repo.attachReader();
  try {
    const opening = await reader.openLocal(40);
    expect(
      opening.model?.turns.flatMap((turn) => turn.items).find((item) => item.transcriptKey === "item-8"),
    ).toMatchObject({ version: 9, text: text ?? "row-8" });
    held.release();
    await repo.settled();
    const reopened = await reader.openLocal(40);
    expect(
      reopened.model?.turns.flatMap((turn) => turn.items).find((item) => item.transcriptKey === "item-8"),
    ).toMatchObject({ version: 9, text: text ?? "row-8" });
  } finally {
    held.release();
    await repo.settled();
    reader.dispose();
    repo.dispose();
  }
});

test("suppression pauses previously queued background writes before cache dispatch", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  let suppressed = false;
  const repo = repository(fixture.cache, { suppressed: () => suppressed });
  repo.acceptRead(readOf([8]), { lifetime: 1, readId: "suppression-read", boundary: "suppression-boundary" });
  await repo.settled();
  const held = holdNextEntryWrite(fixture.cache, "item-9");
  repo.accept(historyUpdate(9, 9, "held-nine", 9), { lifetime: 1 });
  await held.reached;
  repo.accept(historyUpdate(10, 10, "queued-ten", 10), { lifetime: 1 });
  expect(repo.retainedInventoryForTesting().backgroundAcceptBodies.length).toBeGreaterThan(0);
  suppressed = true;
  held.release();
  const dispatched = vi.spyOn(fixture.cache, "commitBatch");
  try {
    // Observe after the first batch's terminal continuation has released the
    // shared slot, while suppression remains true, not after Clear completes.
    await vi.waitFor(() => expect(repo.getAdmissionSnapshotForTesting().backgroundInFlight).toBe(false));
    expect(dispatched).not.toHaveBeenCalled();
    expect((await fixture.cache.getLatest(historyKey, 20)).map((row) => row.transcriptKey)).not.toContain("item-10");
    await expect(repo.settled()).rejects.toThrow(/outstanding/);
    // The owner signals abort explicitly, waking the same accepted work without
    // requiring new input or another reader acquisition.
    suppressed = false;
    repo.resumePersistence();
    await repo.settled();
    expect(dispatched).toHaveBeenCalledTimes(1);
    expect((await fixture.cache.getLatest(historyKey, 20)).map((row) => row.transcriptKey)).toContain("item-10");
  } finally {
    dispatched.mockRestore();
    repo.dispose();
  }
});

test("suppression parks failed-write retries and armed callbacks until the owner resumes", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  let suppressed = false;
  const callbacks: Array<() => void> = [];
  const repo = repository(fixture.cache, {
    suppressed: () => suppressed,
    retryScheduler: (_attempt, retry) => {
      callbacks.push(retry);
      return () => {};
    },
  });
  const reader = repo.attachReader();
  repo.acceptRead(readOf([8]), { lifetime: 1, readId: "suppression-read", boundary: "suppression-boundary" });
  await repo.settled();
  const held = holdNextEntryWrite(fixture.cache, "item-9");
  repo.accept(historyUpdate(9, 9, "retry-nine", 9), { lifetime: 1 });
  await held.reached;
  const put = IDBObjectStore.prototype.put;
  const fault = vi.spyOn(IDBObjectStore.prototype, "put").mockImplementation(function (
    this: IDBObjectStore,
    ...args: Parameters<IDBObjectStore["put"]>
  ) {
    const request = put.apply(this, args);
    if (this.name === "transcriptItems" && args[0].transcriptKey === "item-9") this.transaction.abort();
    return request;
  });
  suppressed = true;
  held.release();
  const dispatched = vi.spyOn(fixture.cache, "commitBatch");
  try {
    await expect(repo.settled()).rejects.toThrow(/outstanding/);
    fault.mockRestore();
    expect(callbacks).toHaveLength(0);
    expect(repo.retainedInventoryForTesting().retryBodies.length).toBeGreaterThan(0);
    suppressed = false;
    repo.resumePersistence();
    expect(callbacks).toHaveLength(1);
    // A timer armed before Clear can still fire inside the suppression window.
    suppressed = true;
    callbacks[0]!();
    expect(dispatched).not.toHaveBeenCalled();
    await expect(repo.settled()).rejects.toThrow(/outstanding/);
    suppressed = false;
    repo.resumePersistence();
    expect(callbacks).toHaveLength(2);
    callbacks[1]!();
    await repo.settled();
    expect(dispatched).toHaveBeenCalledTimes(1);
    expect((await fixture.cache.getLatest(historyKey, 20)).map((row) => row.transcriptKey)).toContain("item-9");
  } finally {
    fault.mockRestore();
    dispatched.mockRestore();
    reader.dispose();
    repo.dispose();
  }
});

function orderedPositions(model: ThreadModel | undefined): number[] {
  return (model?.history?.turns ?? [])
    .flatMap((turn) => turn.items)
    .map((item) => item.position?.entry)
    .filter((entry): entry is number => entry !== undefined);
}

// The positions a reader actually displays with a body. Pruning keeps history
// identities with their positions but drops bodies from the display turns, so a
// truncated restore is invisible in `orderedPositions` and only shows here.
function displayedPositions(model: ThreadModel | undefined): number[] {
  return (model?.turns ?? [])
    .flatMap((turn) => turn.items)
    .filter((item) => (item.text ?? "") !== "")
    .map((item) => item.position?.entry)
    .filter((entry): entry is number => entry !== undefined)
    .sort((left, right) => left - right);
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

// Runs the real cache's older-window query to completion, then holds its
// RESULT delivery until released: a Clear can land after the query genuinely
// returned rows but before the repository merges them. `lastResult` exposes the
// completed query so a test can prove it really carried rows.
function holdOlderWindowLookup() {
  const original = SessionEntryCache.prototype.getOlderWindow;
  let releaseGate!: () => void;
  const gate = new Promise<void>((resolve) => {
    releaseGate = resolve;
  });
  let markReady!: () => void;
  const ready = new Promise<void>((resolve) => {
    markReady = resolve;
  });
  let lastResult: Awaited<ReturnType<typeof original>> | undefined;
  const spy = vi.spyOn(SessionEntryCache.prototype, "getOlderWindow").mockImplementation(function (
    this: SessionEntryCache,
    ...args: Parameters<typeof original>
  ) {
    const resultPromise = original.apply(this, args);
    return (async () => {
      const result = await resultPromise;
      lastResult = result;
      markReady();
      await gate;
      return result;
    })();
  } as typeof original);
  return {
    whenReady: ready,
    lastResult: () => lastResult,
    release() {
      releaseGate();
      spy.mockRestore();
    },
  };
}

// Runs the real cache's latest-window query to completion, then holds its RESULT
// delivery until released.
function holdLatestWindowLookup() {
  const original = SessionEntryCache.prototype.getLatestWindow;
  let releaseGate!: () => void;
  const gate = new Promise<void>((resolve) => {
    releaseGate = resolve;
  });
  let markReady!: () => void;
  const ready = new Promise<void>((resolve) => {
    markReady = resolve;
  });
  const spy = vi.spyOn(SessionEntryCache.prototype, "getLatestWindow").mockImplementation(function (
    this: SessionEntryCache,
    ...args: Parameters<typeof original>
  ) {
    const resultPromise = original.apply(this, args);
    return (async () => {
      const result = await resultPromise;
      markReady();
      await gate;
      return result;
    })();
  } as typeof original);
  return {
    whenReady: ready,
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

test("a reopened opening restores the durable older rows a retired page chain no longer certifies", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const repo = repository(fixture.cache, { lifetime: 1 });
  const reader = repo.attachReader();
  // A latest window and the three older pages the pane loaded below it.
  repo.acceptRead(readOf([30, 31, 32, 33, 34, 35, 36, 37, 38, 39, 40], { olderCursor: "C1", length: 60 }), {
    lifetime: 1,
    readId: "r1",
    boundary: "b1",
  });
  const authority = repo.getBaselineIdentity();
  repo.acceptPage(pageOf([19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29], { nextCursor: "C2", length: 60 }), {
    lifetime: 1,
    readId: "r1",
    boundary: "p1",
    authority,
    requestCursor: "C1",
  });
  repo.acceptPage(pageOf([8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18], { nextCursor: "C3", length: 60 }), {
    lifetime: 1,
    readId: "r1",
    boundary: "p2",
    authority,
    requestCursor: "C2",
  });
  repo.acceptPage(pageOf([1, 2, 3, 4, 5, 6, 7], { length: 60 }), {
    lifetime: 1,
    readId: "r1",
    boundary: "p3",
    authority,
    requestCursor: "C3",
  });
  await repo.settled();
  expect(displayedPositions(repo.getSnapshot())).toEqual(Array.from({ length: 40 }, (_, index) => index + 1));
  reader.dispose();
  // A reconciliation boundary (the store's beginFreshBaseline on a reconnect or
  // an unverified frontier) retires this baseline's page chain. The rows it
  // produced remain durable recorded history in the same cache.
  repo.beginFreshBaseline();
  await repo.settled();
  const reopened = repo.attachReader();
  const opened = await reopened.openLocal(200);
  // The reopened opening restores the whole window the pane had loaded, not
  // only the latest durable read, and keeps a usable older cursor.
  expect(displayedPositions(opened.model)).toEqual(Array.from({ length: 40 }, (_, index) => index + 1));
  expect(opened.olderCursor).toBe("C1");
  reopened.dispose();
  repo.dispose();
});

test("a local opening restores the durable bodies through an interior gap in the certified older set", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const repo = repository(fixture.cache, { lifetime: 1 });
  // The latest read's certified range starts above the oldest loaded rows.
  repo.acceptRead(readOf([144, 145, 146, 147, 148], { length: 300, olderCursor: "C1" }), {
    lifetime: 1,
    readId: "r1",
    boundary: "b1",
  });
  await repo.settled();
  const authority = repo.getBaselineIdentity();
  // A deep page whose provenance joins the latest read, covering [135,139]: the
  // anchored older component's deepest certified range.
  repo.acceptPage(pageOf([135, 138, 139], { length: 300 }), {
    lifetime: 1,
    readId: "r1",
    boundary: "p1",
    authority,
    before: { entry: 144, item: 0 },
  });
  await repo.settled();
  // A middle page covering [140,143], same authority but disconnected from that
  // component (no range overlap, its before is outside the deep range, no
  // matching cursor). The anchored certified set is therefore gapped: it has a
  // row at the opening cut's oldest (135) but a hole above it (140, 143).
  repo.acceptPage(pageOf([140, 143], { length: 300 }), {
    lifetime: 1,
    readId: "r1",
    boundary: "p2",
    authority,
    before: { entry: 141, item: 0 },
  });
  await repo.settled();
  // Background-idle pruning leaves the model holding the recorded identities
  // with their bodies omitted; the bodies are still durable in the cache.
  expect(displayedPositions(repo.getSnapshot())).toEqual([]);

  const reader = repo.attachReader();
  const opened = await reader.openLocal(200);
  // Every recorded row the opening cut had loaded is restored with its body,
  // including the interior gap the certified older set leaves uncovered.
  expect(displayedPositions(opened.model)).toEqual([135, 138, 139, 140, 143, 144, 145, 146, 147, 148]);
  reader.dispose();
  repo.dispose();
});

test("a local opening restores the durable bodies when the durable-authority lookup cannot be established", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const repo = repository(fixture.cache, { lifetime: 1 });
  repo.acceptRead(readOf([135, 138, 139, 140, 143, 144, 145, 146, 147, 148], { length: 300 }), {
    lifetime: 1,
    readId: "r1",
    boundary: "b1",
  });
  await repo.settled();
  // Background-idle pruning leaves the identities with their bodies omitted.
  expect(displayedPositions(repo.getSnapshot())).toEqual([]);
  // The durable Clear-epoch lookup cannot be established within its deadline
  // (a slow or unreadable storage read), so its result is unverified.
  const clearEpoch = vi.spyOn(fixture.cache, "getClearEpoch").mockImplementation(() => new Promise(() => {}));
  try {
    const reader = repo.attachReader();
    const opened = await reader.openLocal(200);
    // The bodies are still on disk and must be restored; only the status may
    // not claim currentness.
    expect(displayedPositions(opened.model)).toEqual([135, 138, 139, 140, 143, 144, 145, 146, 147, 148]);
    expect(opened.status).toBe("recovery-needed");
    reader.dispose();
  } finally {
    clearEpoch.mockRestore();
    repo.dispose();
  }
});

test("a delete with queued work cancels the queued ticket and no queued row reaches the cache", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const owner = repository(fixture.cache, { lifetime: 1, historyGeneration: 0 });
  owner.acceptRead(readOf([6, 7, 8], { length: 8 }), { lifetime: 1, readId: "r1", boundary: "b1" });
  await owner.settled();
  await expect(fixture.cache.getByteTotals(historyKey)).resolves.toMatchObject({ historyBytes: expect.any(Number) });

  // Hold a real in-flight batch, queue a second behind it, delete the history,
  // then cancel the route (client teardown, the effect store deletion applies).
  const gate = holdNextEntryWrite(fixture.cache, "item-9");
  owner.accept(historyUpdate(9, 9, "in-flight", 9), { lifetime: 1 });
  await gate.reached;
  owner.accept(historyUpdate(10, 10, "queued", 10), { lifetime: 1 });
  expect(await fixture.cache.deleteHistory(historyKey)).toBe(true);
  owner.dispose();
  gate.release();
  // The queued ticket is an explicit CANCELLED disposition, not silent success
  // and not merely outstanding recovery work.
  await expect(owner.settled()).rejects.toThrow(/cancelled/);
  await expect(fixture.cache.getLatest(historyKey, 10)).resolves.toEqual([]);
  await expect(fixture.cache.getByteTotals(historyKey)).resolves.toEqual({ historyBytes: 0, aggregateBytes: 0 });
  await expect(fixture.cache.getHistoryGeneration(historyKey)).resolves.toBe(1);
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
  // Rows 3-5 exist ONLY in certified cache coverage, not in the displayed model
  // (whose oldest row is 6), so a served page would be a genuine stale publish.
  await fixture.cache.commitBatch({
    historyKey,
    clearEpoch: 0,
    items: [3, 4, 5].map((entry) => ({
      historyKey,
      transcriptKey: `item-${entry}`,
      position: { entry, item: 0 },
      version: entry,
      item: { id: `item-${entry}`, turnId: "turn_1", type: "agentMessage", text: `row-${entry}` },
    })),
    coverage: {
      kind: "page",
      readId: "r1",
      before: { entry: 6, item: 0 },
      start: { entry: 3, item: 0 },
      end: { entry: 5, item: 0 },
      representedSnapshot: 8,
      serverCursor: "C2",
      requestCursor: "C1",
      authority,
      boundary: "p1",
    },
  });
  await repo.settled();
  expect(orderedPositions(repo.getSnapshot())).toEqual([6, 7, 8]);

  const gate = holdOlderWindowLookup();
  const publications: number[] = [];
  const unsubscribe = repo.subscribe(() => publications.push(orderedPositions(repo.getSnapshot()).length));
  const lookup = repo.loadOlderPage(40, 1);
  await gate.whenReady;
  // The completed query really carried the certified rows 3-5.
  expect(gate.lastResult()?.rows.map((row) => row.transcriptKey)).toEqual(["item-3", "item-4", "item-5"]);
  // Clear lands before the repository merges the completed result, with
  // unchanged client and source identity.
  await expect(fixture.cache.clear()).resolves.toEqual({ committed: true, epoch: 1 });
  const publishedBeforeRelease = publications.length;
  const releasedAt = Date.now();
  gate.release();
  const outcome = await lookup;
  // The stale rows are never published: the moved durable epoch routes the
  // lookup to the bounded reconciliation.
  expect(outcome.kind).toBe("reconcile");
  expect(Date.now() - releasedAt).toBeLessThan(2_000);
  expect(publications.length).toBe(publishedBeforeRelease);
  expect(orderedPositions(repo.getSnapshot())).toEqual([6, 7, 8]);
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

test("an unverifiable Clear epoch routes a held older lookup to reconciliation within the bound", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const repo = repository(fixture.cache, { lifetime: 1, clearEpoch: 0 });
  repo.acceptRead(readOf([6, 7, 8], { olderCursor: "C1", length: 8 }), { lifetime: 1, readId: "r1", boundary: "b1" });
  const authority = repo.getBaselineIdentity();
  await fixture.cache.commitBatch({
    historyKey,
    clearEpoch: 0,
    items: [3, 4, 5].map((entry) => ({
      historyKey,
      transcriptKey: `item-${entry}`,
      position: { entry, item: 0 },
      version: entry,
      item: { id: `item-${entry}`, turnId: "turn_1", type: "agentMessage", text: `row-${entry}` },
    })),
    coverage: {
      kind: "page",
      readId: "r1",
      before: { entry: 6, item: 0 },
      start: { entry: 3, item: 0 },
      end: { entry: 5, item: 0 },
      representedSnapshot: 8,
      serverCursor: "C2",
      requestCursor: "C1",
      authority,
      boundary: "p1",
    },
  });
  await repo.settled();

  // A rejected epoch read is not treated as unchanged: the lookup cannot be
  // certified and routes to the bounded reconciliation, publishing nothing.
  const publications: number[] = [];
  const unsubscribe = repo.subscribe(() => publications.push(orderedPositions(repo.getSnapshot()).length));
  const publicationsBefore = publications.length;
  const realEpoch = fixture.cache.getClearEpoch.bind(fixture.cache);
  fixture.cache.getClearEpoch = (() =>
    Promise.reject(new Error("epoch unreadable"))) as typeof fixture.cache.getClearEpoch;
  const rejectedAt = Date.now();
  const rejected = await repo.loadOlderPage(40, 1);
  expect(rejected.kind).toBe("reconcile");
  expect(Date.now() - rejectedAt).toBeLessThan(2_000);
  expect(publications.length).toBe(publicationsBefore);

  // A held epoch read is bounded too.
  fixture.cache.getClearEpoch = (() => new Promise<number>(() => {})) as typeof fixture.cache.getClearEpoch;
  const heldAt = Date.now();
  const held = await repo.loadOlderPage(40, 1);
  expect(held.kind).toBe("reconcile");
  expect(Date.now() - heldAt).toBeLessThan(2_000);
  expect(publications.length).toBe(publicationsBefore);
  expect(orderedPositions(repo.getSnapshot())).toEqual([6, 7, 8]);

  fixture.cache.getClearEpoch = realEpoch;
  unsubscribe();
  repo.dispose();
});

test("a local open held across a real Clear reports recovery-needed without publishing a stale window", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const repo = repository(fixture.cache, { lifetime: 1, clearEpoch: 0 });
  repo.acceptRead(readOf([6, 7, 8], { length: 8 }), { lifetime: 1, readId: "r1", boundary: "b1" });
  await repo.settled();

  const gate = holdLatestWindowLookup();
  const publications: number[] = [];
  const unsubscribe = repo.subscribe(() => publications.push(orderedPositions(repo.getSnapshot()).length));
  const reader = repo.attachReader();
  const opening = reader.openLocal(40);
  await gate.whenReady;
  await expect(fixture.cache.clear()).resolves.toEqual({ committed: true, epoch: 1 });
  const publishedBeforeRelease = publications.length;
  gate.release();
  const opened = await opening;
  // The completed window is not certified as current and is not published; the
  // readable model is retained.
  expect(opened.status).toBe("recovery-needed");
  expect(opened.model).toBeDefined();
  expect(orderedPositions(opened.model)).toEqual([6, 7, 8]);
  expect(publications.length).toBe(publishedBeforeRelease);
  unsubscribe();
  reader.dispose();
  repo.dispose();
});
