import type {
  AnyNotification,
  ThreadItem,
  ThreadModel,
  ThreadReadResponse,
  ThreadTurnsListResponse,
} from "@evener/appwire-client";
import { encodeTranscriptItem } from "@evener/appwire-client";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { IDBFactory, IDBKeyRange, IDBObjectStore } from "fake-indexeddb";
import { afterEach, expect, it, vi } from "vitest";
import { EntryWriteAdmission } from "./entryWriteAdmission";
import {
  ENTRY_ITEM_STORE,
  ENTRY_TURN_STORE,
  entryHistoryKey,
  type TranscriptResolvedIdentity,
} from "./sessionCacheSchema";
import { type EntryWriteOutcome, SessionEntryCache } from "./sessionEntryCache";
import { createEntryCacheFixture } from "./testing/entryCacheFixture";
import { holdNextTransaction, settleRealTasks } from "./testing/stalledIndexedDB";
import { TranscriptRepository } from "./transcriptRepository";

const fixtures: ReturnType<typeof createEntryCacheFixture>[] = [];
const rawCaches: SessionEntryCache[] = [];
const rawDatabases: Array<{ factory: IDBFactory; name: string }> = [];
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

function item(label: string, entry: number, version = entry): ThreadItem {
  return {
    type: "agentMessage",
    id: `item-${label}`,
    transcriptKey: `item-${label}`,
    turnId: "turn_1",
    position: { entry, item: 0 },
    version,
    text: label,
  } as ThreadItem;
}

function read(label: string, length = 1): ThreadReadResponse {
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
      turns: [{ id: "turn_1", itemsView: "full", status: "completed", version: length, items: [item(label, length)] }],
      evener: { ref, capabilities: CAPABILITIES, queue: { revision: 0 } },
    },
    bootGeneration: "1",
    epoch: 0,
    snapshot: { incarnation: identity.incarnation, length },
    overlay: [],
  };
}

function readItems(label: string, snapshotLength: number, items: ThreadItem[]): ThreadReadResponse {
  const response = read(label, snapshotLength);
  response.thread.turns = [{ id: "turn_1", itemsView: "full", status: "completed", version: snapshotLength, items }];
  response.snapshot = { incarnation: identity.incarnation, length: snapshotLength };
  return response;
}

function historyNotification(label: string, entry: number, snapshotLength: number): AnyNotification {
  return {
    method: "history/updated",
    params: {
      ref,
      threadId,
      bootGeneration: "1",
      epoch: 0,
      snapshot: { incarnation: identity.incarnation, length: snapshotLength },
      items: [item(label, entry)],
    },
  } as AnyNotification;
}

function repository(
  cache: ReturnType<typeof createEntryCacheFixture>["cache"],
  lifetime = 1,
  seedModel?: ThreadModel,
  retryScheduler?: (attempt: number, retry: () => void) => () => void,
  admission?: EntryWriteAdmission,
  reconcile?: () => void,
  isPreferred?: () => boolean,
) {
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
    now: () => Date.now(),
    ...(retryScheduler ? { retryScheduler } : {}),
    ...(admission ? { admission } : {}),
    ...(reconcile ? { reconcile } : {}),
    ...(isPreferred ? { isPreferred } : {}),
  });
}

/** An item whose transcript key stays short while its retained body is large. */
function bigItem(label: string, entry: number, size: number): ThreadItem {
  return { ...item(label, entry), text: `${label}${"x".repeat(size)}` } as ThreadItem;
}

function capturingScheduler() {
  const scheduled: Array<{ attempt: number; fire: () => void }> = [];
  let cancelCount = 0;
  const scheduler = (attempt: number, retry: () => void): (() => void) => {
    scheduled.push({ attempt, fire: retry });
    return () => {
      cancelCount += 1;
    };
  };
  return { scheduled, scheduler, cancelledCount: () => cancelCount };
}

async function keysInCache(cache: ReturnType<typeof createEntryCacheFixture>["cache"]): Promise<string[]> {
  return (await cache.getLatest(historyKey, 40)).map((row) => row.transcriptKey);
}

afterEach(async () => {
  for (const fixture of fixtures.splice(0)) await fixture.dispose();
  for (const cache of rawCaches.splice(0)) cache.close();
  for (const { factory, name } of rawDatabases.splice(0)) {
    await new Promise<void>((resolve, reject) => {
      const request = factory.deleteDatabase(name);
      request.addEventListener("success", () => resolve(), { once: true });
      request.addEventListener("error", () => reject(request.error), { once: true });
      request.addEventListener("blocked", () => reject(new Error(`${name} deletion blocked`)), { once: true });
    });
  }
});

// Behaviour 4 (storage recovery): a transient disk fault must not permanently
// condemn the entry route. Display keeps the accepted content (local storage
// never gates display), and once a later demand-driven commit succeeds the
// repository must report durable readiness again rather than stay
// recovery-needed forever.
it("restores durable local readiness after a transient write failure once a later commit succeeds", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const { scheduled, scheduler } = capturingScheduler();
  const owner = repository(fixture.cache, 1, undefined, scheduler);
  owner.acceptRead(read("before"), { lifetime: 1, readId: "read-before", boundary: "boundary-before" });
  await owner.settled();
  const reader = owner.attachReader();

  // Simulated transient failure (no real transaction runs): the adapter is
  // stubbed to report a failed outcome.
  const failure = vi
    .spyOn(fixture.cache, "commitBatch")
    .mockResolvedValueOnce({ outcome: "failed", error: new Error("simulated abort") });
  expect(
    owner.acceptRead(read("dropped", 2), { lifetime: 1, readId: "read-dropped", boundary: "boundary-dropped" }),
  ).toBe(true);
  // Truthful settlement: the failure is outstanding recovery, not silent success.
  await expect(owner.settled()).rejects.toThrow(/outstanding/);
  failure.mockRestore();

  // The failure must not erase accepted content.
  expect(
    reader
      .getSnapshot()
      ?.turns.flatMap((turn) => turn.items)
      .map((item) => item.transcriptKey),
  ).toEqual(["item-dropped"]);
  // Round 22 supersedes the identity-only baseline key in DISPLAY: retain
  // its exact key/position in history, and show only the accepted bodies.
  expect(
    reader
      .getSnapshot()
      ?.history?.turns.flatMap((turn) => turn.items)
      .map(encodeTranscriptItem)
      .filter((item) => item.identityOnly)
      .map((item) => ({ key: item.transcriptKey, position: item.position, text: item.text })),
  ).toEqual([{ key: "item-before", position: { entry: 1, item: 0 }, text: "" }]);
  expect(reader.getSnapshot()?.turns.flatMap((turn) => turn.items.map((item) => item.text))).toEqual(["dropped"]);
  // The failed row was never durably written: only the baseline is on disk.
  expect(await keysInCache(fixture.cache)).toEqual(["item-before"]);

  // The retained batch is retried; its real commit restores readiness and
  // durability, with no republishing of already-displayed content.
  expect(scheduled).toHaveLength(1);
  scheduled[0]!.fire();
  await owner.settled();

  const opened = await reader.openLocal(40);
  expect(opened.model?.turns.flatMap((turn) => turn.items).map((item) => item.text)).toEqual(["before", "dropped"]);
  expect(opened.status).toBe("current");
  expect(await keysInCache(fixture.cache)).toEqual(["item-before", "item-dropped"]);
  reader.dispose();
  owner.dispose();
});

// R1 (P1): a second distinct failed batch must be deferred, not discarded. The
// server and display keep it, but its cache work must still be recovered with
// no new transcript input.
it("defers and eventually commits every failed batch, including a second one", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const { scheduled, scheduler } = capturingScheduler();
  const owner = repository(fixture.cache, 1, undefined, scheduler);
  owner.acceptRead(read("base"), { lifetime: 1, readId: "read-base", boundary: "boundary-base" });
  await owner.settled();
  const reader = owner.attachReader();

  // Two distinct batches both fail while one paced callback is armed.
  const failure = vi
    .spyOn(fixture.cache, "commitBatch")
    .mockResolvedValue({ outcome: "failed", error: new Error("simulated abort") });
  owner.acceptRead(read("alpha", 2), { lifetime: 1, readId: "read-alpha", boundary: "boundary-alpha" });
  owner.acceptRead(read("beta", 3), { lifetime: 1, readId: "read-beta", boundary: "boundary-beta" });
  await expect(owner.settled()).rejects.toThrow(/outstanding/);
  await vi.waitFor(() => expect(failure).toHaveBeenCalledTimes(2));
  // Exactly one paced callback is armed; the second batch waits behind it.
  expect(scheduled).toHaveLength(1);

  // Display keeps both accepted bodies; neither reached disk.
  expect(
    reader
      .getSnapshot()
      ?.turns.flatMap((turn) => turn.items)
      .map((item) => item.transcriptKey),
  ).toEqual(["item-alpha", "item-beta"]);
  // Round 22 supersedes the identity-only baseline key in DISPLAY: retain
  // its exact key/position in history, and show only the accepted bodies.
  expect(
    reader
      .getSnapshot()
      ?.history?.turns.flatMap((turn) => turn.items)
      .map(encodeTranscriptItem)
      .filter((item) => item.identityOnly)
      .map((item) => ({ key: item.transcriptKey, position: item.position, text: item.text })),
  ).toEqual([{ key: "item-base", position: { entry: 1, item: 0 }, text: "" }]);
  expect(reader.getSnapshot()?.turns.flatMap((turn) => turn.items.map((item) => item.text))).toEqual(["alpha", "beta"]);
  expect(await keysInCache(fixture.cache)).toEqual(["item-base"]);

  // Restore real commits; drive only the paced callbacks (no new input).
  failure.mockRestore();
  scheduled[0]!.fire();
  await vi.waitFor(() => expect(scheduled.length).toBe(2));
  scheduled[1]!.fire();
  await owner.settled();
  expect((await keysInCache(fixture.cache)).sort()).toEqual(["item-alpha", "item-base", "item-beta"]);

  // A fresh reader opened after recovery is not backed by an abandoned batch.
  const fresh = owner.attachReader();
  const opened = await fresh.openLocal(40);
  expect(opened.status).toBe("current");
  expect(
    opened.model?.turns
      .flatMap((turn) => turn.items)
      .map((item) => item.transcriptKey)
      .sort(),
  ).toEqual(["item-alpha", "item-base", "item-beta"]);
  fresh.dispose();
  reader.dispose();
  owner.dispose();
});

// R2 (P1): a boundary retires an armed retry's persistence claim. Its obsolete
// completion must not overwrite the fresh baseline's committed-coverage
// bookkeeping or certify the old range.
it("drops an obsolete retry after a fresh baseline instead of overwriting its coverage", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const { scheduled, scheduler } = capturingScheduler();
  const owner = repository(fixture.cache, 1, undefined, scheduler);
  owner.acceptRead(read("base"), { lifetime: 1, readId: "read-base", boundary: "boundary-base" });
  await owner.settled();
  const reader = owner.attachReader();

  // Old read A carries an unmistakable old-only row; its write fails and its
  // callback is held.
  const oldRead = readItems("old", 2, [item("base", 1), item("old-only", 2)]);
  oldRead.olderCursor = "cursor_old";
  const failure = vi
    .spyOn(fixture.cache, "commitBatch")
    .mockResolvedValueOnce({ outcome: "failed", error: new Error("simulated abort") });
  owner.acceptRead(oldRead, { lifetime: 1, readId: "read-old", boundary: "boundary-old" });
  await vi.waitFor(() => expect(scheduled).toHaveLength(1));
  failure.mockRestore();

  // A real reconnect/reconciliation boundary, then a fresh same-incarnation
  // read B with disjoint latest rows and a distinct fresh cursor.
  owner.beginFreshBaseline();
  const freshRead = readItems("fresh", 5, [item("base", 1), item("fresh", 5)]);
  freshRead.olderCursor = "cursor_fresh";
  // A read issued after the invalidation must carry a request generation above
  // the invalidation point, or the shared reducer discards it.
  freshRead.requestGeneration = 100;
  owner.acceptRead(freshRead, { lifetime: 1, readId: "read-fresh", boundary: "boundary-fresh" });
  await vi.waitFor(async () => expect(await keysInCache(fixture.cache)).toContain("item-fresh"));

  const boundarySpy = vi.spyOn(fixture.cache, "getLatestWindow");

  // Fire the obsolete callback; the boundary retires it.
  scheduled[0]!.fire();
  await owner.settled();
  expect(scheduled).toHaveLength(1); // no new retry armed for the retired batch
  expect(await keysInCache(fixture.cache)).not.toContain("item-old-only");

  // A new reader opening after the obsolete completion selects the fresh
  // baseline's committed boundary, not the old read's.
  reader.dispose();
  const fresh = owner.attachReader();
  const opened = await fresh.openLocal(40);
  expect(opened.model?.turns.flatMap((turn) => turn.items).map((item) => item.transcriptKey)).not.toContain(
    "item-old-only",
  );
  expect(opened.olderCursor).toBe("cursor_fresh");
  const requestedBoundary = boundarySpy.mock.calls.at(-1)?.[2];
  expect(requestedBoundary).toBe("boundary-fresh");
  boundarySpy.mockRestore();
  fresh.dispose();
  owner.dispose();
});

// R3 (P2): the settlement boundary must not report success while recovery is
// outstanding, and must remain honest under cancellation and repeated failure.
it("reports outstanding recovery from the settlement boundary without false success", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const { scheduled, scheduler } = capturingScheduler();
  const owner = repository(fixture.cache, 1, undefined, scheduler);
  owner.acceptRead(read("base"), { lifetime: 1, readId: "read-base", boundary: "boundary-base" });
  await owner.settled();
  const reader = owner.attachReader();

  const failure = vi
    .spyOn(fixture.cache, "commitBatch")
    .mockResolvedValue({ outcome: "failed", error: new Error("simulated abort") });
  owner.acceptRead(read("alpha", 2), { lifetime: 1, readId: "read-alpha", boundary: "boundary-alpha" });
  // Outstanding recovery is reported, not swallowed.
  await expect(owner.settled()).rejects.toThrow(/outstanding/);

  // A failed retry re-arms and is still reported as outstanding.
  failure.mockRestore();
  const failAgain = vi
    .spyOn(fixture.cache, "commitBatch")
    .mockResolvedValueOnce({ outcome: "failed", error: new Error("simulated abort 2") });
  scheduled[0]!.fire();
  await vi.waitFor(() => expect(scheduled.length).toBe(2));
  await expect(owner.settled()).rejects.toThrow(/outstanding/);
  failAgain.mockRestore();

  // A committed retry resolves truthfully.
  scheduled[1]!.fire();
  await owner.settled();

  reader.dispose();
  owner.dispose();
});

// Behaviour 5 (capacity recovery): a capacity refusal is not a transient
// fault, so it must not arm the bounded retry that would become a repair loop.
// The transcript still displays; the refusal is honest about durable readiness;
// and genuinely changed eligible content is reconsidered.
it("does not arm a transient retry for a capacity refusal and reconsiders changed content", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const { scheduled, scheduler } = capturingScheduler();
  const owner = repository(fixture.cache, 1, undefined, scheduler);
  owner.acceptRead(read("base"), { lifetime: 1, readId: "read-base", boundary: "boundary-base" });
  await owner.settled();
  const reader = owner.attachReader();

  vi.spyOn(fixture.cache, "commitBatch").mockResolvedValueOnce({ outcome: "refused", reason: "capacity" });
  owner.acceptRead(read("oversize", 2), { lifetime: 1, readId: "read-oversize", boundary: "boundary-oversize" });
  await owner.settled();

  // No transient retry for a capacity refusal; display preserved.
  expect(scheduled).toHaveLength(0);
  expect(
    reader
      .getSnapshot()
      ?.turns.flatMap((turn) => turn.items)
      .map((item) => item.transcriptKey),
  ).toEqual(["item-oversize"]);
  // Round 22 supersedes the identity-only baseline key in DISPLAY: retain
  // its exact key/position in history, and show only the accepted bodies.
  expect(
    reader
      .getSnapshot()
      ?.history?.turns.flatMap((turn) => turn.items)
      .map(encodeTranscriptItem)
      .filter((item) => item.identityOnly)
      .map((item) => ({ key: item.transcriptKey, position: item.position, text: item.text })),
  ).toEqual([{ key: "item-base", position: { entry: 1, item: 0 }, text: "" }]);
  expect(reader.getSnapshot()?.turns.flatMap((turn) => turn.items.map((item) => item.text))).toEqual(["oversize"]);
  expect(await keysInCache(fixture.cache)).toEqual(["item-base"]);
  expect((await reader.openLocal(40)).status).toBe("recovery-needed");

  // Changed eligible content is reconsidered: a newer accepted read re-attempts
  // a real commit rather than being suppressed by the refusal.
  const attempts = vi.spyOn(fixture.cache, "commitBatch");
  owner.acceptRead(read("changed", 3), { lifetime: 1, readId: "read-changed", boundary: "boundary-changed" });
  await owner.settled();
  expect(attempts).toHaveBeenCalled();
  expect(await keysInCache(fixture.cache)).toContain("item-changed");
  attempts.mockRestore();
  reader.dispose();
  owner.dispose();
});

it.each([1, 2])("ordinary capacity refusal survives a narrower commit at position %i", async (kept) => {
  const name = `ordinary-gap-${crypto.randomUUID()}`;
  const factory = new IDBFactory();
  rawDatabases.push({ factory, name });
  const cache = new SessionEntryCache({
    indexedDB: factory,
    keyRange: IDBKeyRange,
    databaseName: name,
    maxBytes: 5_000,
  });
  rawCaches.push(cache);
  const admission = new EntryWriteAdmission(100_000);
  const { scheduled, scheduler } = capturingScheduler();
  const owner = repository(cache, 1, undefined, scheduler, admission);
  const commits = vi.spyOn(cache, "commitBatch");
  const submit = (items: ThreadItem[], generation: number) =>
    owner.acceptRead(
      { ...readItems("range", 2, items), requestGeneration: generation },
      { lifetime: 1, readId: `read-${generation}`, boundary: `boundary-${generation}` },
    );
  try {
    submit([bigItem("left", 1, 10_000), bigItem("right", 2, 10_000)], 1);
    expect(admission.snapshotForTesting().exemptBytes).toBe(0);
    await owner.settled();
    expect(await commits.mock.results[0]!.value).toEqual({ outcome: "refused", reason: "capacity" });
    expect(owner.getRecoveryStateForTesting().cacheContinuityLost).toBe(true);
    const small = [item("left", 1), item("right", 2)];
    submit([small[kept - 1]!], 2);
    await owner.settled();
    expect(await commits.mock.results[1]!.value).toMatchObject({ outcome: "committed", coverage: "extended" });
    expect(await keysInCache(cache)).toEqual([kept === 1 ? "item-left" : "item-right"]);
    expect(owner.getRecoveryStateForTesting().cacheContinuityLost).toBe(true);
    const reader = owner.attachReader();
    try {
      expect((await reader.openLocal(40)).status).toBe("recovery-needed");
      submit(small, 3);
      await owner.settled();
      expect(owner.getRecoveryStateForTesting().cacheContinuityLost).toBe(false);
      expect((await reader.openLocal(40)).status).toBe("current");
      expect(await keysInCache(cache)).toEqual(["item-left", "item-right"]);
      expect(scheduled).toHaveLength(0);
    } finally {
      reader.dispose();
    }
  } finally {
    owner.dispose();
  }
});

it.each([1, 2])("authoritative replacement retires only the refused extent it replaces: %i", async (kept) => {
  const name = `replaced-refusal-${crypto.randomUUID()}`;
  const factory = new IDBFactory();
  rawDatabases.push({ factory, name });
  const cache = new SessionEntryCache({
    indexedDB: factory,
    keyRange: IDBKeyRange,
    databaseName: name,
    maxBytes: 5_000,
  });
  rawCaches.push(cache);
  const admission = new EntryWriteAdmission(100_000);
  const { scheduler } = capturingScheduler();
  const owner = repository(cache, 1, undefined, scheduler, admission);
  const commits = vi.spyOn(cache, "commitBatch");
  try {
    owner.acceptRead(readItems("range", 2, [bigItem("left", 1, 10_000), bigItem("right", 2, 10_000)]), {
      lifetime: 1,
      readId: "refused",
      boundary: "refused",
    });
    await owner.settled();
    expect(await commits.mock.results[0]!.value).toEqual({ outcome: "refused", reason: "capacity" });
    expect(owner.getRecoveryStateForTesting().cacheContinuityLost).toBe(true);
    const small = item(kept === 1 ? "left" : "right", kept);
    expect(
      owner.acceptRead(
        { ...readItems("range", 2, [small]), requestGeneration: 2, authoritative: true },
        { lifetime: 1, readId: "replacement", boundary: "replacement" },
      ),
    ).toBe(true);
    await owner.settled();
    expect(await commits.mock.results[1]!.value).toMatchObject({ outcome: "committed", coverage: "extended" });
    expect(await keysInCache(cache)).toEqual([small.transcriptKey]);
    expect(
      owner.getSnapshot()?.history?.turns.flatMap((turn) => turn.items.map((item) => item.position?.entry)),
    ).toEqual(kept === 1 ? [1] : [1, 2]);
    const reader = owner.attachReader();
    try {
      expect((await reader.openLocal(40)).status).toBe(kept === 1 ? "current" : "recovery-needed");
      expect(owner.getRecoveryStateForTesting().cacheContinuityLost).toBe(kept !== 1);
    } finally {
      reader.dispose();
    }
  } finally {
    owner.dispose();
  }
});

it.each([1, 2])("authoritative replacement retires only exempt abandonment it replaces: %i", async (kept) => {
  const name = `replaced-refusal-${crypto.randomUUID()}`;
  const factory = new IDBFactory();
  rawDatabases.push({ factory, name });
  const cache = new SessionEntryCache({
    indexedDB: factory,
    keyRange: IDBKeyRange,
    databaseName: name,
    maxBytes: 5_000,
  });
  rawCaches.push(cache);
  const admission = new EntryWriteAdmission(1_500);
  const { scheduler } = capturingScheduler();
  const owner = repository(cache, 1, undefined, scheduler, admission);
  const commits = vi.spyOn(cache, "commitBatch");
  try {
    owner.acceptRead(readItems("range", 2, [bigItem("left", 1, 10_000), bigItem("right", 2, 10_000)]), {
      lifetime: 1,
      readId: "refused",
      boundary: "refused",
    });
    await expect(owner.settled()).rejects.toBeInstanceOf(Error);
    expect(await commits.mock.results[0]!.value).toEqual({ outcome: "refused", reason: "capacity" });
    expect(owner.getRecoveryStateForTesting().cacheContinuityLost).toBe(true);
    const small = item(kept === 1 ? "left" : "right", kept);
    expect(
      owner.acceptRead(
        { ...readItems("range", 2, [small]), requestGeneration: 2, authoritative: true },
        { lifetime: 1, readId: "replacement", boundary: "replacement" },
      ),
    ).toBe(true);
    if (kept === 1) await owner.settled();
    else await expect(owner.settled()).rejects.toBeInstanceOf(Error);
    expect(await commits.mock.results[1]!.value).toMatchObject({ outcome: "committed", coverage: "extended" });
    expect(await keysInCache(cache)).toEqual([small.transcriptKey]);
    expect(
      owner.getSnapshot()?.history?.turns.flatMap((turn) => turn.items.map((item) => item.position?.entry)),
    ).toEqual(kept === 1 ? [1] : [1, 2]);
    const reader = owner.attachReader();
    try {
      expect((await reader.openLocal(40)).status).toBe(kept === 1 ? "current" : "recovery-needed");
      expect(owner.getRecoveryStateForTesting().cacheContinuityLost).toBe(kept !== 1);
    } finally {
      reader.dispose();
    }
  } finally {
    owner.dispose();
  }
});

// D3: an unchanged intrinsic capacity refusal (the recorded batch's own bytes
// exceed the disk limit) is memoized independently of memory-queue admission,
// so repeated reads of the same item/version/window are not re-encoded and
// re-written. Changed eligible content is reconsidered.
it.each([1_500, 100_000])("memoizes ordinary rereads above disk capacity with admission limit %i", async (limit) => {
  const name = `entry-capacity-${crypto.randomUUID()}`;
  const factory = new IDBFactory();
  rawDatabases.push({ factory, name });
  const cache = new SessionEntryCache({
    indexedDB: factory,
    keyRange: IDBKeyRange,
    databaseName: name,
    maxBytes: 2_000,
  });
  rawCaches.push(cache);
  const admission = new EntryWriteAdmission(limit);
  const owner = repository(cache, 1, undefined, capturingScheduler().scheduler, admission);
  const reader = owner.attachReader();
  const oversize = (): ThreadItem => ({ ...item("oversize", 1), text: "z".repeat(10_000) }) as ThreadItem;
  const attempts = vi.spyOn(cache, "commitBatch");

  // Three submissions of the same item/version/window with ordinary read churn.
  expect(
    owner.acceptRead(readItems("oversize", 1, [oversize()]), {
      lifetime: 1,
      readId: "read-gen-1",
      boundary: "boundary-gen-1",
    }),
  ).toBe(true);
  if (limit === 1_500) expect(admission.snapshotForTesting().exemptBytes).toBeGreaterThan(0);
  else expect(admission.snapshotForTesting().exemptBytes).toBe(0);
  // The indivisible batch alone exceeds the disk budget: refused, and reported.
  if (limit === 1_500) await expect(owner.settled()).rejects.toThrow(/failed/);
  else await owner.settled();
  for (const generation of [2, 3]) {
    expect(
      owner.acceptRead(
        { ...readItems("oversize", 1, [oversize()]), requestGeneration: generation },
        {
          lifetime: 1,
          readId: `read-gen-${generation}`,
          boundary: `boundary-gen-${generation}`,
        },
      ),
    ).toBe(true);
    await owner.settled().catch(() => {});
  }
  // Only the first attempt reaches the database; the unchanged record is
  // refused from the memo rather than re-encoded and re-written.
  expect(attempts).toHaveBeenCalledTimes(1);
  expect(await cache.getLatest(historyKey, 40)).toEqual([]);
  expect((await reader.openLocal(40)).status).toBe("recovery-needed");

  // Changed eligible content resumes caching.
  owner.acceptRead(
    { ...readItems("small", 2, [item("small", 2)]), requestGeneration: 4 },
    {
      lifetime: 1,
      readId: "read-small",
      boundary: "boundary-small",
    },
  );
  await vi.waitFor(async () =>
    expect(await cache.getLatest(historyKey, 40)).toContainEqual(
      expect.objectContaining({ transcriptKey: "item-small" }),
    ),
  );
  expect(attempts).toHaveBeenCalledTimes(2);
  attempts.mockRestore();
  reader.dispose();
  owner.dispose();
});

function capacityFixture(limit = 1_500) {
  const factory = new IDBFactory();
  const name = `entry-capacity-lifetime-${crypto.randomUUID()}`;
  rawDatabases.push({ factory, name });
  const makeCache = (maxBytes = 2_000) => {
    const cache = new SessionEntryCache({ indexedDB: factory, keyRange: IDBKeyRange, databaseName: name, maxBytes });
    rawCaches.push(cache);
    return cache;
  };
  const cache = makeCache();
  const owner = repository(cache, 1, undefined, capturingScheduler().scheduler, new EntryWriteAdmission(limit));
  const offer = async (target = owner, generation = 1, row = bigItem("oversize", 1, 10_000)) => {
    expect(
      target.acceptRead(
        { ...readItems("oversize", 1, [row]), requestGeneration: generation },
        {
          lifetime: 1,
          readId: `read-${generation}`,
          boundary: `boundary-${generation}`,
        },
      ),
    ).toBe(true);
    await target.settled().catch(() => {});
  };
  return { cache, owner, makeCache, offer };
}

it("a stalled retry capacity preflight releases the shared slot for another session", async () => {
  const { owner: unused, cache, offer } = capacityFixture(100_000);
  unused.dispose();
  const { scheduled, scheduler } = capturingScheduler();
  const admission = new EntryWriteAdmission(100_000);
  const owner = repository(cache, 1, undefined, scheduler, admission);
  const reader = owner.attachReader();
  const otherIdentity = { ...identity, sessionId: "other" };
  const otherKey = entryHistoryKey(otherIdentity);
  const other = new TranscriptRepository(cache, {
    ref: "other-ref",
    threadId: "other-thread",
    identity: otherIdentity,
    historyKey: otherKey,
    clearEpoch: 0,
    lifetime: 1,
    admission,
  });
  const commits = vi.spyOn(cache, "commitBatch");
  let hold: ReturnType<typeof holdNextTransaction> | undefined;
  try {
    await offer(owner);
    commits.mockResolvedValueOnce({ outcome: "failed", error: new Error("transient") });
    owner.acceptRead(
      { ...read("small", 2), requestGeneration: 2 },
      { lifetime: 1, readId: "retry", boundary: "retry" },
    );
    await expect(owner.settled()).rejects.toBeInstanceOf(Error);
    expect(owner.retainedInventoryForTesting().retryBodies.length).toBeGreaterThan(0);
    expect(owner.getRecoveryStateForTesting().cacheContinuityLost).toBe(true);
    expect(scheduled).toHaveLength(1);
    hold = holdNextTransaction([ENTRY_ITEM_STORE, ENTRY_TURN_STORE], "readonly");
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    scheduled[0]!.fire();
    await hold.reached;
    const response = read("other", 1);
    response.thread.id = "other-thread";
    response.thread.sessionId = "other";
    response.thread.evener.ref = "other-ref";
    expect(other.acceptRead(response, { lifetime: 1, readId: "other", boundary: "other" })).toBe(true);
    let settled = false;
    const settling = owner.settled().then(() => {
      settled = true;
    });
    void settling.catch(() => {});
    expect(admission.snapshotForTesting().retainedBytes).toBeGreaterThan(0);
    await vi.advanceTimersByTimeAsync(9_999);
    expect(settled).toBe(false);
    expect(commits).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(1);
    await settleRealTasks(() => settled);
    expect(settled).toBe(true);
    await settling;
    await other.settled();
    expect(admission.snapshotForTesting().retainedBytes).toBe(0);
    expect(await cache.getLatest(otherKey, 40)).toHaveLength(1);
    expect(commits).toHaveBeenCalledTimes(4);
    hold.release();
    await cache.getClearEpoch();
    expect(commits).toHaveBeenCalledTimes(4);
  } finally {
    hold?.release();
    vi.useRealTimers();
    await owner.settled().catch(() => {});
    await other.settled().catch(() => {});
    reader.dispose();
    owner.dispose();
    other.dispose();
  }
});

it.each(["dispose", "invalidate"] as const)(
  "does not commit a retry after %s during capacity planning",
  async (retire) => {
    const { owner: unused, cache, offer } = capacityFixture(100_000);
    unused.dispose();
    const { scheduled, scheduler } = capturingScheduler();
    const admission = new EntryWriteAdmission(100_000);
    const owner = repository(cache, 1, undefined, scheduler, admission);
    const reader = owner.attachReader();
    const gate = deferred<void>();
    const reached = deferred<void>();
    const commits = vi.spyOn(cache, "commitBatch");
    const realPlan = cache.planCapacity.bind(cache);
    try {
      await offer(owner);
      expect(await commits.mock.results[0]!.value).toEqual({ outcome: "refused", reason: "capacity" });
      commits.mockResolvedValueOnce({ outcome: "failed", error: new Error("transient") });
      expect(
        owner.acceptRead(
          { ...read("small", 2), requestGeneration: 2 },
          {
            lifetime: 1,
            readId: "retry",
            boundary: "retry",
          },
        ),
      ).toBe(true);
      await expect(owner.settled()).rejects.toBeInstanceOf(Error);
      expect(scheduled).toHaveLength(1);
      vi.spyOn(cache, "planCapacity").mockImplementationOnce(async (batch) => {
        const plan = await realPlan(batch);
        reached.resolve();
        await gate.promise;
        return plan;
      });
      scheduled[0]!.fire();
      await reached.promise;
      expect(commits).toHaveBeenCalledTimes(2);
      expect(admission.snapshotForTesting().retainedBytes).toBeGreaterThan(0);
      owner[retire]();
      gate.resolve();
      await owner.settled().catch(() => {});
      expect(commits).toHaveBeenCalledTimes(2);
      expect(await keysInCache(cache)).toEqual([]);
      expect(admission.snapshotForTesting().retainedBytes).toBe(0);
    } finally {
      gate.resolve();
      reader.dispose();
      owner.dispose();
    }
  },
);

it("retires a retry when a replacement lands during its capacity lookup", async () => {
  const { owner: unused, cache, offer } = capacityFixture(100_000);
  unused.dispose();
  const { scheduled, scheduler } = capturingScheduler();
  const admission = new EntryWriteAdmission(100_000);
  const owner = repository(cache, 1, undefined, scheduler, admission);
  const reader = owner.attachReader();
  const gate = deferred<void>();
  const reached = deferred<void>();
  const commits = vi.spyOn(cache, "commitBatch");
  const realPlan = cache.planCapacity.bind(cache);
  try {
    await offer(owner);
    expect(await commits.mock.results[0]!.value).toEqual({ outcome: "refused", reason: "capacity" });
    commits.mockResolvedValueOnce({ outcome: "failed", error: new Error("transient") });
    expect(
      owner.acceptRead(
        { ...read("old", 2), requestGeneration: 2 },
        {
          lifetime: 1,
          readId: "old",
          boundary: "old",
        },
      ),
    ).toBe(true);
    await expect(owner.settled()).rejects.toBeInstanceOf(Error);
    expect(owner.retainedInventoryForTesting().retryBodies.length).toBeGreaterThan(0);
    expect(owner.getRecoveryStateForTesting().cacheContinuityLost).toBe(true);
    expect(scheduled).toHaveLength(1);
    const plans = vi.spyOn(cache, "planCapacity").mockImplementationOnce(async (batch) => {
      const plan = await realPlan(batch);
      reached.resolve();
      await gate.promise;
      return plan;
    });
    scheduled[0]!.fire();
    await reached.promise;
    expect(
      owner.acceptRead(
        { ...read("new", 3), authoritative: true, requestGeneration: 3 },
        {
          lifetime: 1,
          readId: "new",
          boundary: "new",
        },
      ),
    ).toBe(true);
    await vi.waitFor(() => expect(commits).toHaveBeenCalledTimes(3));
    expect(await commits.mock.results[2]!.value).toMatchObject({ outcome: "committed" });
    gate.resolve();
    await owner.settled();
    expect(plans).toHaveBeenCalled();
    expect(commits).toHaveBeenCalledTimes(3);
    expect(await keysInCache(cache)).toEqual(["item-new"]);
    expect(admission.snapshotForTesting().retainedBytes).toBe(0);
  } finally {
    gate.resolve();
    reader.dispose();
    owner.dispose();
  }
});

it("keeps a dependent suffix behind its baseline capacity preflight", async () => {
  const { owner: unused, makeCache, offer } = capacityFixture(100_000);
  unused.dispose();
  const cache = makeCache(6_500);
  const { scheduled, scheduler } = capturingScheduler();
  const owner = repository(cache, 1, undefined, scheduler, new EntryWriteAdmission(100_000));
  const reader = owner.attachReader();
  const gate = deferred<void>();
  const reached = deferred<void>();
  try {
    await offer(owner);
    const commits = vi.spyOn(cache, "commitBatch");
    const realPlan = cache.planCapacity.bind(cache);
    vi.spyOn(cache, "planCapacity").mockImplementationOnce(async (batch) => {
      const plan = await realPlan(batch);
      reached.resolve();
      await gate.promise;
      return plan;
    });
    expect(
      owner.acceptRead(
        { ...read("base", 1), requestGeneration: 2 },
        {
          lifetime: 1,
          readId: "baseline",
          boundary: "baseline",
        },
      ),
    ).toBe(true);
    await reached.promise;
    expect(owner.accept(historyNotification("suffix", 2, 2), { lifetime: 1 })).toBe(true);
    // Let the suffix's real preflight finish while its baseline stays held.
    await cache.getClearEpoch();
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(commits).not.toHaveBeenCalled();
    gate.resolve();
    await owner.settled();
    expect(commits.mock.calls.map(([batch]) => batch.coverage?.kind)).toEqual(["read", "suffix"]);
    for (const result of commits.mock.results) {
      expect(await result.value).toMatchObject({ outcome: "committed", coverage: "extended" });
    }
    expect(await keysInCache(cache)).toEqual(["item-base", "item-suffix"]);
    expect(owner.getRecoveryStateForTesting().cacheContinuityLost).toBe(false);
    expect(scheduled).toHaveLength(0);
  } finally {
    gate.resolve();
    await owner.settled().catch(() => {});
    reader.dispose();
    owner.dispose();
  }
});

it("drops a held capacity preflight after a newer authoritative baseline commits", async () => {
  const { owner: unused, makeCache, offer } = capacityFixture(100_000);
  unused.dispose();
  const cache = makeCache(6_500);
  const owner = repository(cache, 1, undefined, capturingScheduler().scheduler, new EntryWriteAdmission(100_000));
  const reader = owner.attachReader();
  await offer(owner);
  const commits = vi.spyOn(cache, "commitBatch");
  const gate = deferred<void>();
  const realPlan = cache.planCapacity.bind(cache);
  vi.spyOn(cache, "planCapacity").mockImplementationOnce(async (batch) => {
    const plan = await realPlan(batch);
    await gate.promise;
    return plan;
  });
  const accept = (label: string, generation: number) =>
    owner.acceptRead(
      { ...read(label, generation), authoritative: true, requestGeneration: generation, olderCursor: label },
      { lifetime: 1, readId: label, boundary: label },
    );
  try {
    expect(accept("old", 2)).toBe(true);
    expect(accept("new", 3)).toBe(true);
    await vi.waitFor(() => expect(commits).toHaveBeenCalledTimes(1));
    expect(await commits.mock.results[0]!.value).toMatchObject({ outcome: "committed" });
    await vi.waitFor(async () =>
      expect((await cache.getSessionMetadata(historyKey))?.cachedHeader?.history.length).toBe(3),
    );
    gate.resolve();
    await owner.settled().catch(() => {});
    expect(commits).toHaveBeenCalledTimes(1);
    expect((await cache.getSessionMetadata(historyKey))?.cachedHeader?.history.length).toBe(3);
    expect((await cache.getLatestWindow(historyKey, 40)).coverage.at(-1)?.readId).toBe("new");
  } finally {
    gate.resolve();
    reader.dispose();
    owner.dispose();
  }
});

it.each(["read", "suffix"] as const)(
  "preserves an omitted accepted update from a delayed %s preflight",
  async (kind) => {
    const { owner: unused, makeCache, offer } = capacityFixture(100_000);
    unused.dispose();
    const cache = makeCache(6_500);
    const owner = repository(cache, 1, undefined, capturingScheduler().scheduler, new EntryWriteAdmission(100_000));
    const reader = owner.attachReader();
    const gate = deferred<void>();
    try {
      await offer(owner);
      expect(
        owner.acceptRead(
          { ...readItems("base", 2, [item("oversize", 1, 2), item("target", 2, 1)]), requestGeneration: 2 },
          { lifetime: 1, readId: "base", boundary: "base" },
        ),
      ).toBe(true);
      await owner.settled();
      const updated = { ...item("target", 2, 2), text: "accepted update omitted by newer response" } as ThreadItem;
      const realPlan = cache.planCapacity.bind(cache);
      vi.spyOn(cache, "planCapacity").mockImplementationOnce(async (batch) => {
        expect(batch.coverage?.kind).toBe(kind);
        const plan = await realPlan(batch);
        await gate.promise;
        return plan;
      });
      if (kind === "read") {
        expect(
          owner.acceptRead(
            { ...readItems("update", 3, [updated]), requestGeneration: 3 },
            { lifetime: 1, readId: "update", boundary: "update" },
          ),
        ).toBe(true);
      } else {
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
                items: [updated],
              },
            } as AnyNotification,
            { lifetime: 1 },
          ),
        ).toBe(true);
      }
      const commits = vi.spyOn(cache, "commitBatch");
      expect(
        owner.acceptRead(
          { ...readItems("new", 4, [item("oversize", 1, 4), item("new", 4)]), requestGeneration: 4 },
          { lifetime: 1, readId: "new", boundary: "new" },
        ),
      ).toBe(true);
      expect(
        reader
          .getSnapshot()
          ?.history?.turns.flatMap((turn) => turn.items)
          .find((item) => item.transcriptKey === "item-target"),
      ).toMatchObject({ version: 2, text: updated.text });
      await vi.waitFor(() => expect(commits).toHaveBeenCalledTimes(1));
      expect(await commits.mock.results[0]!.value).toMatchObject({ outcome: "committed" });
      gate.resolve();
      await owner.settled();
      reader.dispose();
      expect(owner.getSnapshot()?.turns.flatMap((turn) => turn.items)).toEqual([]);
      const reopened = owner.attachReader();
      try {
        const opened = await reopened.openLocal(40);
        const disk = (await cache.getLatest(historyKey, 40)).find((row) => row.transcriptKey === "item-target");
        expect(disk).toMatchObject({ version: 2, item: { text: updated.text } });
        expect(
          opened.model?.turns.flatMap((turn) => turn.items).find((item) => item.transcriptKey === "item-target"),
        ).toMatchObject({ version: 2, text: updated.text });
        expect(opened.status).toBe("current");
        expect((await cache.getSessionMetadata(historyKey))?.cachedHeader?.history.length).toBe(4);
        expect((await cache.getLatestWindow(historyKey, 40)).coverage.at(-1)?.readId).toBe("new");
      } finally {
        reopened.dispose();
      }
    } finally {
      gate.resolve();
      reader.dispose();
      owner.dispose();
    }
  },
);

it("preserves replacement deletions inherited from a delayed read preflight", async () => {
  const { owner: unused, makeCache, offer } = capacityFixture(100_000);
  unused.dispose();
  const cache = makeCache(6_500);
  const owner = repository(cache, 1, undefined, capturingScheduler().scheduler, new EntryWriteAdmission(100_000));
  const reader = owner.attachReader();
  const gate = deferred<void>();
  try {
    await offer(owner);
    expect(
      owner.acceptRead(
        { ...readItems("base", 3, [item("oversize", 1, 2), item("obsolete", 2)]), requestGeneration: 2 },
        { lifetime: 1, readId: "base", boundary: "base" },
      ),
    ).toBe(true);
    await owner.settled();
    expect((await cache.getLatest(historyKey, 40)).map((row) => row.transcriptKey)).toContain("item-obsolete");
    const realPlan = cache.planCapacity.bind(cache);
    vi.spyOn(cache, "planCapacity").mockImplementationOnce(async (batch) => {
      expect(batch.replacement).toEqual({ from: { entry: 1, item: 0 } });
      const plan = await realPlan(batch);
      await gate.promise;
      return plan;
    });
    expect(
      owner.acceptRead(
        {
          ...readItems("replacement", 4, [item("oversize", 1, 3), item("survivor", 3)]),
          authoritative: true,
          requestGeneration: 3,
        },
        { lifetime: 1, readId: "replacement", boundary: "replacement" },
      ),
    ).toBe(true);
    const commits = vi.spyOn(cache, "commitBatch");
    expect(
      owner.acceptRead(
        { ...readItems("new", 5, [item("oversize", 1, 4), item("new", 4)]), requestGeneration: 4 },
        { lifetime: 1, readId: "new", boundary: "new" },
      ),
    ).toBe(true);
    await vi.waitFor(() => expect(commits).toHaveBeenCalledTimes(1));
    expect(await commits.mock.results[0]!.value).toMatchObject({ outcome: "committed" });
    gate.resolve();
    await owner.settled();
    reader.dispose();
    expect(owner.getSnapshot()?.turns.flatMap((turn) => turn.items)).toEqual([]);
    const reopened = owner.attachReader();
    try {
      const opened = await reopened.openLocal(40);
      expect((await cache.getLatest(historyKey, 40)).map((row) => row.transcriptKey).sort()).toEqual([
        "item-new",
        "item-oversize",
        "item-survivor",
      ]);
      expect(opened.model?.turns.flatMap((turn) => turn.items.map((item) => item.transcriptKey)).sort()).toEqual([
        "item-new",
        "item-oversize",
        "item-survivor",
      ]);
      expect(opened.status).toBe("current");
      expect((await cache.getLatestWindow(historyKey, 40)).coverage.map((coverage) => coverage.readId)).toEqual([
        "new",
      ]);
    } finally {
      reopened.dispose();
    }
  } finally {
    gate.resolve();
    reader.dispose();
    owner.dispose();
  }
});

it("persists a distinct page request after intrinsic capacity refusal", async () => {
  const { owner: unused, makeCache, offer } = capacityFixture(100_000);
  unused.dispose();
  const cache = makeCache(6_500);
  const owner = repository(cache, 1, undefined, capturingScheduler().scheduler, new EntryWriteAdmission(100_000));
  const reader = owner.attachReader();
  try {
    const refusedCommit = vi.spyOn(cache, "commitBatch");
    await offer(owner);
    expect(await refusedCommit.mock.results[0]!.value).toEqual({ outcome: "refused", reason: "capacity" });
    refusedCommit.mockRestore();
    expect(owner.getRecoveryStateForTesting().cacheContinuityLost).toBe(true);
    owner.acceptRead(
      { ...readItems("base", 2, [item("oversize", 1, 2), item("base", 2)]), olderCursor: "older" },
      { lifetime: 1, readId: "baseline", boundary: "baseline" },
    );
    await owner.settled();
    const plans = vi.spyOn(cache, "planCapacity");
    const commits = vi.spyOn(cache, "commitBatch");
    const authority = owner.getBaselineIdentity();
    expect(
      owner.acceptPage(
        {
          data: [{ id: "turn_1", itemsView: "full", status: "completed", version: 2, items: [item("page", 0, 2)] }],
          bootGeneration: "1",
          epoch: 0,
          snapshot: { incarnation: identity.incarnation, length: 2 },
        },
        { lifetime: 1, readId: "distinct-page-request", boundary: "page", authority, requestCursor: "older" },
      ),
    ).toBe(true);
    await owner.settled();
    expect(plans).toHaveBeenCalledWith(
      expect.objectContaining({
        coverage: expect.objectContaining({ kind: "page", readId: "distinct-page-request", authority }),
      }),
    );
    expect(commits).toHaveBeenCalledTimes(1);
    expect(await keysInCache(cache)).toContain("item-page");
    expect(await cache.getCoverage(historyKey, "page:distinct-page-request:page")).toMatchObject({
      kind: "page",
      authority,
      readId: "distinct-page-request",
    });
    reader.dispose();
    expect(owner.getSnapshot()?.turns.flatMap((turn) => turn.items)).toEqual([]);
    const reopened = owner.attachReader();
    try {
      const opened = await reopened.openLocal(40);
      expect(opened.model?.turns.flatMap((turn) => turn.items.map((item) => item.text))).toContain("page");
    } finally {
      reopened.dispose();
    }
  } finally {
    reader.dispose();
    owner.dispose();
  }
});

it("drops a held capacity preflight after a newer ordinary read commits and survives prune/reopen", async () => {
  const { owner: unused, makeCache, offer } = capacityFixture(100_000);
  unused.dispose();
  const cache = makeCache(6_500);
  const owner = repository(cache, 1, undefined, capturingScheduler().scheduler, new EntryWriteAdmission(100_000));
  const reader = owner.attachReader();
  await offer(owner);
  const commits = vi.spyOn(cache, "commitBatch");
  const gate = deferred<void>();
  const realPlan = cache.planCapacity.bind(cache);
  vi.spyOn(cache, "planCapacity").mockImplementationOnce(async (batch) => {
    const plan = await realPlan(batch);
    await gate.promise;
    return plan;
  });
  const accept = (label: string, generation: number) =>
    owner.acceptRead(
      {
        ...readItems(label, generation, [item("oversize", 1, generation), item(label, generation)]),
        requestGeneration: generation,
        olderCursor: label,
      },
      { lifetime: 1, readId: label, boundary: label },
    );
  try {
    expect(accept("old", 2)).toBe(true);
    expect(accept("new", 3)).toBe(true);
    await vi.waitFor(() => expect(commits).toHaveBeenCalledTimes(1));
    expect(await commits.mock.results[0]!.value).toMatchObject({ outcome: "committed" });
    await vi.waitFor(async () =>
      expect((await cache.getSessionMetadata(historyKey))?.cachedHeader?.history.length).toBe(3),
    );
    gate.resolve();
    await owner.settled().catch(() => {});
    reader.dispose();
    expect(owner.getSnapshot()?.turns.flatMap((turn) => turn.items)).toEqual([]);
    const reopened = owner.attachReader();
    try {
      const opened = await reopened.openLocal(40);
      expect(opened.model?.turns.flatMap((turn) => turn.items.map((item) => item.text))).toContain("new");
      expect(opened.status).toBe("current");
    } finally {
      reopened.dispose();
    }
    expect(commits).toHaveBeenCalledTimes(1);
    expect((await cache.getSessionMetadata(historyKey))?.cachedHeader?.history.length).toBe(3);
    expect((await cache.getLatestWindow(historyKey, 40)).coverage.at(-1)?.readId).toBe("new");
  } finally {
    gate.resolve();
    reader.dispose();
    owner.dispose();
  }
});

it("drops a held suffix preflight after a newer ordinary read commits", async () => {
  const { owner: unused, makeCache, offer } = capacityFixture(100_000);
  unused.dispose();
  const cache = makeCache(6_500);
  const owner = repository(cache, 1, undefined, capturingScheduler().scheduler, new EntryWriteAdmission(100_000));
  const reader = owner.attachReader();
  await offer(owner);
  owner.acceptRead(
    { ...readItems("old", 2, [item("oversize", 1, 2), item("old", 2)]), requestGeneration: 2 },
    { lifetime: 1, readId: "old", boundary: "old" },
  );
  await owner.settled();
  const commits = vi.spyOn(cache, "commitBatch");
  const gate = deferred<void>();
  const realPlan = cache.planCapacity.bind(cache);
  vi.spyOn(cache, "planCapacity").mockImplementationOnce(async (batch) => {
    const plan = await realPlan(batch);
    await gate.promise;
    return plan;
  });
  const accept = (label: string, generation: number) =>
    owner.acceptRead(
      {
        ...readItems(label, generation, [item("oversize", 1, generation), item(label, generation)]),
        requestGeneration: generation,
        olderCursor: label,
      },
      { lifetime: 1, readId: label, boundary: label },
    );
  try {
    expect(owner.accept(historyNotification("suffix", 3, 3), { lifetime: 1 })).toBe(true);
    expect(accept("new", 4)).toBe(true);
    await vi.waitFor(() => expect(commits).toHaveBeenCalledTimes(1));
    expect(await commits.mock.results[0]!.value).toMatchObject({ outcome: "committed" });
    await vi.waitFor(async () =>
      expect((await cache.getSessionMetadata(historyKey))?.cachedHeader?.history.length).toBe(4),
    );
    gate.resolve();
    await owner.settled().catch(() => {});
    reader.dispose();
    expect(owner.getSnapshot()?.turns.flatMap((turn) => turn.items)).toEqual([]);
    const reopened = owner.attachReader();
    try {
      const opened = await reopened.openLocal(40);
      expect(opened.model?.turns.flatMap((turn) => turn.items.map((item) => item.text))).toContain("new");
      expect(opened.status).toBe("current");
    } finally {
      reopened.dispose();
    }
    expect(commits).toHaveBeenCalledTimes(1);
    expect((await cache.getSessionMetadata(historyKey))?.cachedHeader?.history.length).toBe(4);
    expect((await cache.getLatestWindow(historyKey, 40)).coverage.at(-1)?.readId).toBe("new");
  } finally {
    gate.resolve();
    reader.dispose();
    owner.dispose();
  }
});

it.each([1, 2])(
  "recovers from an intrinsic refusal through the sole oversize write for changed disk-eligible content at version %i",
  async (version) => {
    const { owner: unused, makeCache } = capacityFixture();
    unused.dispose();
    const cache = makeCache(6_500);
    const admission = new EntryWriteAdmission(1_500);
    const owner = repository(cache, 1, undefined, capturingScheduler().scheduler, admission);
    const reader = owner.attachReader();
    const commits = vi.spyOn(cache, "commitBatch");
    const plans = vi.spyOn(cache, "planCapacity");
    try {
      owner.acceptRead(readItems("oversize", 1, [bigItem("oversize", 1, 10_000)]), {
        lifetime: 1,
        readId: "refused",
        boundary: "refused",
      });
      await expect(owner.settled()).rejects.toBeInstanceOf(Error);
      expect(owner.getRecoveryStateForTesting().cacheContinuityLost).toBe(true);
      expect(owner.retainedInventoryForTesting()).toMatchObject({
        retryBodies: [],
        backgroundAcceptBodies: [],
        inFlightBodies: [],
      });
      expect(await commits.mock.results[0]!.value).toEqual({ outcome: "refused", reason: "capacity" });
      const changed = { ...bigItem("oversize", 1, 3_500), version };
      owner.acceptRead(
        { ...readItems("oversize", 1, [changed]), requestGeneration: 2, authoritative: true },
        {
          lifetime: 1,
          readId: "eligible",
          boundary: "eligible",
        },
      );
      expect(plans).not.toHaveBeenCalled();
      expect(admission.snapshotForTesting().retainedBytes).toBe(0);
      expect(admission.snapshotForTesting().exemptBytes).toBeGreaterThan(1_500);
      await owner.settled();
      expect(commits).toHaveBeenCalledTimes(2);
      expect(await commits.mock.results[1]!.value).toMatchObject({ outcome: "committed", coverage: "extended" });
      expect(await cache.getLatest(historyKey, 40)).toContainEqual(
        expect.objectContaining({ item: expect.objectContaining({ text: changed.text }) }),
      );
      expect(admission.snapshotForTesting().exemptBytes).toBe(0);
    } finally {
      reader.dispose();
      owner.dispose();
    }
  },
);

it.each(["newer", "older"])("preserves a declined %s extent after a narrower commit", async (direction) => {
  const { owner: unused, makeCache } = capacityFixture();
  unused.dispose();
  const cache = makeCache(6_500);
  const admission = new EntryWriteAdmission(1_500);
  const { scheduled, scheduler } = capturingScheduler();
  const reconcile = vi.fn();
  const owner = repository(cache, 1, undefined, scheduler, admission, reconcile);
  const reader = owner.attachReader();
  const commits = vi.spyOn(cache, "commitBatch");
  const plans = vi.spyOn(cache, "planCapacity");
  const submit = (items: ThreadItem[], length: number, generation: number) => {
    expect(
      owner.acceptRead(
        { ...readItems("base", length, items), requestGeneration: generation },
        { lifetime: 1, readId: `read-${generation}`, boundary: `boundary-${generation}` },
      ),
    ).toBe(true);
  };
  try {
    const basePosition = direction === "older" ? 3 : 1;
    const betaPosition = direction === "older" ? 1 : 2;
    const finalLength = direction === "older" ? 3 : 2;
    submit([bigItem("base", basePosition, 10_000)], basePosition, 1);
    await expect(owner.settled()).rejects.toBeInstanceOf(Error);
    expect(owner.getRecoveryStateForTesting().cacheContinuityLost).toBe(true);
    expect(owner.retainedInventoryForTesting()).toMatchObject({
      retryBodies: [],
      backgroundAcceptBodies: [],
      inFlightBodies: [],
    });
    expect(await commits.mock.results[0]!.value).toEqual({ outcome: "refused", reason: "capacity" });

    // The changed candidate widens the refused extent. Its oversize write is
    // declined only because another writer owns the sole immediate slot.
    const competingWriter = {};
    expect(admission.retainExempt([], competingWriter)).toBe(true);
    const beta = bigItem("beta", betaPosition, 3_500);
    submit([item("base", basePosition, 4), beta], finalLength, 2);
    admission.releaseExempt(competingWriter);
    await expect(owner.settled()).rejects.toBeInstanceOf(Error);
    expect(owner.getRecoveryStateForTesting().cacheContinuityLost).toBe(true);
    expect(owner.retainedInventoryForTesting()).toMatchObject({
      retryBodies: [],
      backgroundAcceptBodies: [],
      inFlightBodies: [],
    });
    expect(plans).not.toHaveBeenCalled();
    expect(commits).toHaveBeenCalledTimes(1);
    expect(await keysInCache(cache)).toEqual([]);
    expect(reader.getSnapshot()?.turns.flatMap((turn) => turn.items)).toContainEqual(beta);
    expect(admission.snapshotForTesting().retainedBytes).toBe(0);
    expect(admission.snapshotForTesting().exemptBytes).toBe(0);
    expect(owner.retainedInventoryForTesting().inFlightBodies).toHaveLength(0);

    // This real commit covers the original refusal, not the dropped beta end
    // (newer) or start (older). Neither boundary of the abandoned hull may shrink.
    submit([item("base", basePosition, 4)], finalLength, 3);
    await owner.settled().catch(() => {});
    expect(commits).toHaveBeenCalledTimes(2);
    expect(await commits.mock.results[1]!.value).toMatchObject({ outcome: "committed", coverage: "extended" });
    expect(await keysInCache(cache)).toEqual(["item-base"]);
    expect.soft(owner.getRecoveryStateForTesting().cacheContinuityLost).toBe(true);
    await expect.soft(owner.settled()).rejects.toBeInstanceOf(Error);
    expect.soft(owner.getRecoveryStateForTesting().cacheContinuityLost).toBe(true);
    expect
      .soft(owner.retainedInventoryForTesting())
      .toMatchObject({ retryBodies: [], backgroundAcceptBodies: [], inFlightBodies: [] });
    expect((await reader.openLocal(40)).status).toBe("recovery-needed");
    expect(scheduled).toHaveLength(0);
    expect(reconcile).not.toHaveBeenCalled();
  } finally {
    reader.dispose();
    owner.dispose();
  }
});

it.each([1_500, 100_000])("bounds concurrent unchanged memo preflights with admission limit %i", async (limit) => {
  const { cache, owner: unused } = capacityFixture(limit);
  unused.dispose();
  const admission = new EntryWriteAdmission(limit);
  const { scheduled, scheduler } = capturingScheduler();
  const reconcile = vi.fn();
  const owner = repository(cache, 1, undefined, scheduler, admission, reconcile);
  const attempts = vi.spyOn(cache, "commitBatch");
  const submit = (generation: number) =>
    owner.acceptRead(
      { ...readItems("oversize", 1, [bigItem("oversize", 1, 10_000)]), requestGeneration: generation },
      { lifetime: 1, readId: `bounded-${generation}`, boundary: `bounded-${generation}` },
    );
  try {
    expect(submit(1)).toBe(true);
    await owner.settled().catch(() => {});
    expect(attempts).toHaveBeenCalledTimes(1);
    const plans = vi.spyOn(cache, "planCapacity");
    for (let generation = 2; generation < 130; generation++) expect(submit(generation)).toBe(true);

    // Real IndexedDB cannot finish these lookups during the synchronous burst.
    // Every submitted ASCII item contains at least 10,000 payload bytes, so
    // ordinary admission permits at most floor(limit / 10,000) live preflights.
    expect(plans.mock.calls.length).toBeLessThanOrEqual(Math.floor(limit / 10_000));
    if (limit === 100_000) expect(plans.mock.calls.length).toBeGreaterThan(0);
    const charge = admission.snapshotForTesting();
    expect(charge.retainedBytes).toBeGreaterThanOrEqual(plans.mock.calls.length * 10_000);
    expect(charge.retainedBytes).toBeLessThanOrEqual(limit);
    expect(charge.exemptBytes).toBe(0);
    for (const row of owner.retainedInventoryForTesting().pendingRows) expect(admission.isRetained(row)).toBe(true);
    const inFlight = owner.retainedInventoryForTesting().inFlightBodies;
    expect(inFlight).toHaveLength(
      plans.mock.calls.reduce((sum, [batch]) => sum + batch.items.length + (batch.turns?.length ?? 0), 0),
    );
    for (const body of inFlight) expect(admission.isRetained(body)).toBe(true);

    await owner.settled().catch(() => {});
    expect(admission.snapshotForTesting().retainedBytes).toBe(0);
    expect(owner.retainedInventoryForTesting().pendingRows).toHaveLength(0);
    expect(owner.retainedInventoryForTesting().inFlightBodies).toHaveLength(0);
    expect(attempts).toHaveBeenCalledTimes(1);
    for (const entry of scheduled) entry.fire();
    expect(reconcile).not.toHaveBeenCalled();
    expect(scheduled).toHaveLength(0);
  } finally {
    await owner.settled().catch(() => {});
    owner.dispose();
  }
});

it.each([1_500, 100_000])(
  "overlapping unchanged memo checks do not reconcile with admission limit %i",
  async (limit) => {
    const { cache, owner: unused } = capacityFixture(limit);
    unused.dispose();
    const { scheduled, scheduler } = capturingScheduler();
    const reconcile = vi.fn();
    const owner = repository(cache, 1, undefined, scheduler, new EntryWriteAdmission(limit), reconcile);
    const attempts = vi.spyOn(cache, "commitBatch");
    const offer = (generation: number) =>
      owner.acceptRead(
        { ...readItems("oversize", 1, [bigItem("oversize", 1, 10_000)]), requestGeneration: generation },
        { lifetime: 1, readId: `read-${generation}`, boundary: `boundary-${generation}` },
      );
    try {
      expect(offer(1)).toBe(true);
      await owner.settled().catch(() => {});
      expect(attempts).toHaveBeenCalledTimes(1);
      expect(scheduled).toHaveLength(0);
      expect(offer(2)).toBe(true);
      expect(offer(3)).toBe(true);
      await owner.settled().catch(() => {});
      expect(attempts).toHaveBeenCalledTimes(1);
      for (const entry of scheduled) entry.fire();
      expect(reconcile).not.toHaveBeenCalled();
      expect(scheduled).toHaveLength(0);
    } finally {
      owner.dispose();
    }
  },
);

it("falls back after an aborted memo read without an unhandled rejection", async () => {
  const { cache, owner, offer } = capacityFixture(100_000);
  const commits = vi.spyOn(cache, "commitBatch");
  let reads: ReturnType<typeof vi.spyOn> | undefined;
  try {
    await offer();
    const original = IDBObjectStore.prototype.get;
    let aborted = 0;
    reads = vi.spyOn(IDBObjectStore.prototype, "get").mockImplementation(function (this: IDBObjectStore, key) {
      const request = original.call(this, key);
      if (aborted === 0 && this.name === "transcriptItems" && this.transaction.mode === "readonly") {
        aborted++;
        this.transaction.abort();
      }
      return request;
    });
    await offer(owner, 2);
    // Let the abort/completion events reach Vitest's unhandled-rejection guard.
    await new Promise((resolve) => setTimeout(resolve, 30));
    expect(aborted).toBe(1);
    expect(commits).toHaveBeenCalledTimes(2);
    expect(await commits.mock.results[1]?.value).toEqual({ outcome: "refused", reason: "capacity" });
  } finally {
    reads?.mockRestore();
    owner.dispose();
  }
});

it("reconsiders a stale oversized predecessor after its boundary shrinks", async () => {
  const { cache, owner } = capacityFixture(100_000);
  const small = { ...item("oversize", 1), version: 2 };
  expect(
    (
      await cache.commitBatch({
        historyKey,
        clearEpoch: 0,
        items: [
          { historyKey, transcriptKey: small.transcriptKey!, position: small.position!, version: 2, item: small },
        ],
      })
    ).outcome,
  ).toBe("committed");
  const attempts = vi.spyOn(cache, "commitBatch");
  const offer = async (boundary: string, generation: number) => {
    expect(
      owner.acceptRead(
        { ...readItems("oversize", 1, [bigItem("oversize", 1, 10_000)]), requestGeneration: generation },
        {
          lifetime: 1,
          readId: `read-${generation}`,
          boundary,
        },
      ),
    ).toBe(true);
    await owner.settled();
  };
  try {
    await offer("b".repeat(10_000), 1);
    expect(await attempts.mock.results[0]!.value).toEqual({ outcome: "refused", reason: "capacity" });
    const refused = attempts.mock.calls[0]![0];
    const direct = { ...refused, coverage: { ...refused.coverage!, boundary: "short", readId: "read-2" } };
    await offer("short", 2);
    const ownerAttempts = attempts.mock.calls.length;
    // Independent real writer control, even when the owner suppressed its retry.
    expect((await cache.commitBatch(direct)).outcome).toBe("committed");
    expect(ownerAttempts).toBe(2);
    expect((await attempts.mock.results[1]!.value).outcome).toBe("committed");
    expect(await cache.getLatest(historyKey, 40)).toContainEqual(expect.objectContaining({ version: 2 }));
  } finally {
    owner.dispose();
  }
});

it("reconsiders an authoritative omitted-text reread at the same recorded identity", async () => {
  const { cache, owner, offer } = capacityFixture(100_000);
  const attempts = vi.spyOn(cache, "commitBatch");
  const full = bigItem("oversize", 1, 10_000);
  const { text: _text, ...sparse } = full;
  const control = repository(cache, 1, undefined, capturingScheduler().scheduler, new EntryWriteAdmission(100_000));
  try {
    await offer(owner, 1, full);
    expect(await attempts.mock.results[0]!.value).toEqual({ outcome: "refused", reason: "capacity" });
    await offer(owner, 2, sparse);
    const ownerAttempts = attempts.mock.calls.length;
    const ownerRows = await cache.getLatest(historyKey, 40);
    await offer(control, 3, sparse);
    expect(await cache.getLatest(historyKey, 40)).toContainEqual(
      expect.objectContaining({ transcriptKey: full.transcriptKey }),
    );
    expect(ownerAttempts).toBe(2);
    expect(ownerRows).toContainEqual(expect.objectContaining({ transcriptKey: full.transcriptKey }));
    expect((await attempts.mock.results[1]!.value).outcome).toBe("committed");
  } finally {
    owner.dispose();
    control.dispose();
  }
});

it.each([
  { name: "stable recorded bytes over disk", size: 10_000, boundarySize: 1, intrinsic: true },
  { name: "volatile certificate over disk", size: 500, boundarySize: 10_000, intrinsic: false },
  { name: "smaller certificate", size: 500, boundarySize: 1, intrinsic: false },
])("classifies capacity refusal from $name without side effects", async ({ size, boundarySize, intrinsic }) => {
  const { cache, owner } = capacityFixture();
  try {
    const row = {
      historyKey,
      transcriptKey: "offered",
      position: { entry: 1, item: 0 },
      version: 1,
      item: { id: "offered", turnId: "t", type: "agentMessage", text: "x".repeat(size) },
    };
    const batch = {
      historyKey,
      clearEpoch: 0,
      items: [row],
      coverage: {
        kind: "read" as const,
        readId: "read",
        boundary: "b".repeat(boundarySize),
        start: row.position,
        end: row.position,
        representedSnapshot: 1,
      },
    };
    const attempts = vi.spyOn(cache, "commitBatch");
    const plan = await cache.planCapacity(batch);
    for (let repeat = 0; repeat < 3; repeat += 1) expect(cache.isIntrinsicCapacityRefusal(plan)).toBe(intrinsic);
    expect(attempts).not.toHaveBeenCalled();
    expect(await cache.getByteTotals(historyKey)).toEqual({ historyBytes: 0, aggregateBytes: 0 });
    expect(await cache.getLatest(historyKey, 40)).toEqual([]);
  } finally {
    owner.dispose();
  }
});

it("memoizes identical under-admission over-disk submissions", async () => {
  const { cache, owner, offer } = capacityFixture(100_000);
  const attempts = vi.spyOn(cache, "commitBatch");
  const classifications = vi.spyOn(cache, "isIntrinsicCapacityRefusal");
  try {
    await offer();
    await offer();
    await offer();
    expect(attempts).toHaveBeenCalledTimes(1);
    expect(classifications).toHaveBeenCalledTimes(1);
    expect(await cache.getLatest(historyKey, 40)).toEqual([]);
  } finally {
    owner.dispose();
  }
});

it.each(["item", "turn"])("classifies %s bytes at the real disk encoding boundary", async (kind) => {
  const { cache, owner, makeCache } = capacityFixture();
  try {
    const batch = {
      historyKey,
      clearEpoch: 0,
      items:
        kind === "item"
          ? [
              {
                historyKey,
                transcriptKey: "encoded",
                position: { entry: 1, item: 0 },
                version: 1,
                item: {
                  id: "encoded",
                  turnId: "t",
                  type: "agentMessage",
                  text: "é",
                  pendingText: new Set(["λ".repeat(200)]),
                },
              },
            ]
          : [],
      turns:
        kind === "turn"
          ? [{ id: "t", version: 1, status: "completed" as const, items: [], error: "λ".repeat(200) }]
          : [],
    };
    // The independent oracle is the real write and stored byte total, not the
    // classifier's helper. Include UTF-8 and iterable pending text encoding.
    const plan = await cache.planCapacity(batch);
    expect((await cache.commitBatch(batch)).outcome).toBe("committed");
    const { historyBytes } = await cache.getByteTotals(historyKey);
    expect(historyBytes).toBeGreaterThan(400);
    const exact = makeCache(historyBytes);
    const below = makeCache(historyBytes - 1);
    expect(exact.isIntrinsicCapacityRefusal(plan)).toBe(false);
    expect(below.isIntrinsicCapacityRefusal(plan)).toBe(true);
    expect(await cache.getByteTotals(historyKey)).toEqual({ historyBytes, aggregateBytes: historyBytes });
  } finally {
    owner.dispose();
  }
});

it("keeps an intrinsic capacity memo across unrelated successful cache writes", async () => {
  const { cache, owner, offer } = capacityFixture();
  const attempts = vi.spyOn(cache, "commitBatch");
  try {
    await offer();
    for (const generation of [2, 3]) {
      const other = `other-${generation}`;
      expect(
        (
          await cache.commitBatch({
            historyKey: other,
            clearEpoch: 0,
            items: [
              {
                historyKey: other,
                transcriptKey: other,
                position: { entry: 1, item: 0 },
                version: 1,
                item: { id: other, turnId: "t", type: "agentMessage", text: "small" },
              },
            ],
          })
        ).outcome,
      ).toBe("committed");
      await offer(owner, generation);
    }
    expect(attempts.mock.calls.filter(([batch]) => batch.historyKey === historyKey)).toHaveLength(1);
    expect(await cache.getLatest(historyKey, 40)).toEqual([]);
  } finally {
    owner.dispose();
  }
});

it.each(["item version", "window"])("reconsiders an intrinsic capacity memo on changed %s", async (change) => {
  const { cache, owner, offer } = capacityFixture();
  const attempts = vi.spyOn(cache, "commitBatch");
  try {
    await offer();
    await offer(owner, 2);
    expect(attempts).toHaveBeenCalledTimes(1);
    const changed = {
      ...item("oversize", 1),
      ...(change === "item version" ? { version: 2 } : { position: { entry: 1, item: 1 } }),
    } as ThreadItem;
    await offer(owner, 3, changed);
    expect(attempts).toHaveBeenCalledTimes(2);
    expect(await cache.getLatest(historyKey, 40)).toContainEqual(
      expect.objectContaining({ transcriptKey: "item-oversize" }),
    );
  } finally {
    owner.dispose();
  }
});

it.each(["disk limit", "Clear", "cache lifetime"])(
  "reconsiders an intrinsic capacity memo on changed %s",
  async (change) => {
    const { cache, owner, makeCache, offer } = capacityFixture();
    const attempts = vi.spyOn(cache, "commitBatch");
    let replacement: TranscriptRepository | undefined;
    try {
      await offer();
      await offer(owner, 2);
      expect(attempts).toHaveBeenCalledTimes(1);
      owner.dispose();
      let epoch = 0;
      if (change === "Clear") {
        const cleared = await cache.clear();
        expect(cleared.committed).toBe(true);
        epoch = cleared.epoch;
      }
      const nextCache = change === "Clear" ? cache : makeCache(change === "disk limit" ? 30_000 : 2_000);
      replacement = new TranscriptRepository(nextCache, {
        ref,
        threadId,
        historyKey,
        identity,
        clearEpoch: epoch,
        lifetime: 1,
        admission: new EntryWriteAdmission(1_500),
        retryScheduler: capturingScheduler().scheduler,
      });
      const nextAttempts = nextCache === cache ? attempts : vi.spyOn(nextCache, "commitBatch");
      const before = nextAttempts.mock.calls.length;
      await offer(replacement, 3);
      expect(nextAttempts).toHaveBeenCalledTimes(before + 1);
      if (change === "disk limit") {
        expect(await nextCache.getLatest(historyKey, 40)).toContainEqual(
          expect.objectContaining({ transcriptKey: "item-oversize" }),
        );
      } else {
        expect(await nextCache.getLatest(historyKey, 40)).toEqual([]);
        await offer(replacement, 4);
        expect(nextAttempts).toHaveBeenCalledTimes(before + 1);
      }
    } finally {
      owner.dispose();
      replacement?.dispose();
    }
  },
);

// Counterexample A: a capacity refusal often means the accumulated history, not
// the offered batch alone, exceeds the budget. Eviction of that history by an
// unrelated write can cure it, so the refusal must not be memoized.
it.each([1_500, 100_000])(
  "does not memoize aggregate refusal after external eviction with admission limit %i",
  async (limit) => {
    const name = `entry-aggregate-${crypto.randomUUID()}`;
    const factory = new IDBFactory();
    rawDatabases.push({ factory, name });
    const cache = new SessionEntryCache({
      indexedDB: factory,
      keyRange: IDBKeyRange,
      databaseName: name,
      maxBytes: 6_500,
    });
    rawCaches.push(cache);
    const owner = repository(cache, 1, undefined, capturingScheduler().scheduler, new EntryWriteAdmission(limit));
    owner.acceptRead(readItems("alpha", 1, [bigItem("alpha", 1, 3_500)]), {
      lifetime: 1,
      readId: "read-alpha",
      boundary: "boundary-alpha",
    });
    await owner.settled();

    const attempts = vi.spyOn(cache, "commitBatch");
    const betaRead = (owner2: TranscriptRepository) =>
      owner2.acceptRead(readItems("beta", 2, [bigItem("beta", 2, 3_500)]), {
        lifetime: 1,
        readId: "read-beta",
        boundary: "boundary-beta",
      });
    // The same history's accumulated bytes exceed the budget: refused (not
    // indivisible — the offered batch alone is only ~3,500 bytes).
    expect(betaRead(owner)).toBe(true);
    await vi.waitFor(() => expect(attempts).toHaveBeenCalledTimes(1));
    if (limit === 1_500) await expect(owner.settled()).rejects.toThrow(/failed/);
    else await owner.settled();
    expect((await cache.getLatest(historyKey, 40)).map((row) => row.transcriptKey)).not.toContain("item-beta");

    // An unrelated history's commit evicts the accumulated subject history.
    const sibling = new SessionEntryCache({
      indexedDB: factory,
      keyRange: IDBKeyRange,
      databaseName: name,
      maxBytes: 6_500,
    });
    rawCaches.push(sibling);
    const other = "other/session/inc";
    const otherOutcome = await sibling.commitBatch({
      historyKey: other,
      clearEpoch: 0,
      items: [
        {
          historyKey: other,
          transcriptKey: "item-other",
          position: { entry: 1, item: 0 },
          version: 1,
          item: { id: "item-other", turnId: "t", type: "agentMessage", text: "o".repeat(5_000) },
        },
      ],
    });
    expect(otherOutcome.outcome).toBe("committed");
    expect(await cache.getLatest(historyKey, 40)).toEqual([]);

    // The body's own bytes fit; the earlier refusal must not suppress the
    // identical batch now that the accumulated history was evicted.
    const beforeOwner = attempts.mock.calls.length;
    betaRead(owner);
    await owner.settled();
    expect(attempts.mock.calls.length).toBeGreaterThan(beforeOwner);
    expect(await cache.getLatest(historyKey, 40)).toContainEqual(
      expect.objectContaining({ transcriptKey: "item-beta" }),
    );
    // Fresh-owner control against the same cache: the identical batch fits.
    const control = repository(cache);
    betaRead(control);
    await control.settled();
    expect(await cache.getLatest(historyKey, 40)).toContainEqual(
      expect.objectContaining({ transcriptKey: "item-beta" }),
    );
    attempts.mockRestore();
    control.dispose();
    owner.dispose();
  },
);

// Counterexample B: the coverage certificate contributes to admission cost, so
// a refusal of one certificate must not suppress the same item/version under a
// smaller certificate that fits.
it.each([1_500, 100_000])(
  "reconsiders a capacity refusal when the coverage certificate changes with admission limit %i",
  async (limit) => {
    const name = `entry-certificate-${crypto.randomUUID()}`;
    const factory = new IDBFactory();
    rawDatabases.push({ factory, name });
    const cache = new SessionEntryCache({
      indexedDB: factory,
      keyRange: IDBKeyRange,
      databaseName: name,
      maxBytes: 4_000,
    });
    rawCaches.push(cache);
    const owner = repository(cache, 1, undefined, capturingScheduler().scheduler, new EntryWriteAdmission(limit));
    const attempts = vi.spyOn(cache, "commitBatch");
    const tinyRead = (owner2: TranscriptRepository, boundary: string) =>
      owner2.acceptRead(readItems("tiny", 1, [bigItem("tiny", 1, 2_000)]), {
        lifetime: 1,
        readId: "read-tiny",
        boundary,
      });

    // A 5,000-character boundary pushes the history over the budget: refused.
    expect(tinyRead(owner, "b".repeat(5_000))).toBe(true);
    await vi.waitFor(() => expect(attempts).toHaveBeenCalledTimes(1));
    if (limit === 1_500) await expect(owner.settled()).rejects.toThrow(/failed/);
    else await owner.settled();
    expect(await cache.getLatest(historyKey, 40)).toEqual([]);

    // A non-indivisible refusal is never memoized: the identical long-certificate
    // batch is attempted again rather than suppressed.
    const beforeIdentical = attempts.mock.calls.length;
    expect(tinyRead(owner, "b".repeat(5_000))).toBe(true);
    if (limit === 1_500) await expect(owner.settled()).rejects.toThrow(/failed/);
    else await owner.settled();
    expect(attempts.mock.calls.length).toBeGreaterThan(beforeIdentical);

    // The same recorded identity with a short boundary must also be attempted:
    // certificate overhead did not establish an intrinsic refusal to memoize.
    const beforeShort = attempts.mock.calls.length;
    expect(tinyRead(owner, "short")).toBe(true);
    await vi.waitFor(async () =>
      expect(await cache.getLatest(historyKey, 40)).toContainEqual(
        expect.objectContaining({ transcriptKey: "item-tiny" }),
      ),
    );
    expect(attempts.mock.calls.length).toBeGreaterThan(beforeShort);
    // Fresh-owner control against the same cache: the short certificate fits.
    const control = repository(cache);
    tinyRead(control, "short");
    await control.settled();
    expect(await cache.getLatest(historyKey, 40)).toContainEqual(
      expect.objectContaining({ transcriptKey: "item-tiny" }),
    );
    attempts.mockRestore();
    control.dispose();
    owner.dispose();
  },
);

// Behaviour 2 (active output above the budget): unfinished overlay output is
// memory-only. It causes no disk write even when its body is far larger than
// any resident budget, and no pressure-only reread. It is retained for display
// until its recorded replacement hands it off.
it("keeps unfinished overlay output in memory with no disk write", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const owner = repository(fixture.cache);
  owner.acceptRead(read("base"), { lifetime: 1, readId: "read-base", boundary: "boundary-base" });
  await owner.settled();

  const commits = vi.spyOn(fixture.cache, "commitBatch");
  const big = "z".repeat(400_000);
  owner.accept(
    {
      method: "overlay/upserted",
      params: {
        ref,
        threadId,
        item: {
          key: "stream:live-round/1:agentMessage",
          kind: "stream",
          turnId: "turn_1",
          roundId: "live-round",
          streamId: "live-round/1",
          item: {
            type: "agentMessage",
            id: "live-stream",
            turnId: "turn_1",
            roundId: "live-round",
            text: big,
            status: "inProgress",
          },
        },
      },
    } as unknown as AnyNotification,
    { lifetime: 1 },
  );
  await owner.settled();

  expect(commits).not.toHaveBeenCalled();
  expect(owner.getSnapshot()?.overlay?.["stream:live-round/1:agentMessage"]?.item?.text).toBe(big);
  commits.mockRestore();
  owner.dispose();
});

// The default timer path (no injected scheduler) must pace on the documented
// bounded exponential backoff and reset after a success. Timers are captured,
// not waited on, so the delays are observed directly.
it("paces the default retry on bounded exponential backoff and resets after success", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const owner = repository(fixture.cache, 1);
  owner.acceptRead(read("base"), { lifetime: 1, readId: "read-base", boundary: "boundary-base" });
  await owner.settled();
  owner.attachReader();

  const captured: Array<{ delay: number; fire: () => void }> = [];
  const realSetTimeout = globalThis.setTimeout;
  const timeoutSpy = vi.spyOn(globalThis, "setTimeout").mockImplementation(((
    handler: TimerHandler,
    timeout?: number,
    ...rest: unknown[]
  ) => {
    if (
      typeof handler === "function" &&
      (timeout === 250 || timeout === 500 || timeout === 1_000 || timeout === 2_000)
    ) {
      captured.push({ delay: timeout, fire: handler as () => void });
      return { ref: () => {}, unref: () => {} } as unknown as ReturnType<typeof setTimeout>;
    }
    return realSetTimeout(handler, timeout, ...rest);
  }) as unknown as typeof setTimeout);

  const failure = vi
    .spyOn(fixture.cache, "commitBatch")
    .mockResolvedValue({ outcome: "failed", error: new Error("simulated abort") });
  owner.acceptRead(read("alpha", 2), { lifetime: 1, readId: "read-alpha", boundary: "boundary-alpha" });
  await vi.waitFor(() => expect(captured).toHaveLength(1));

  // Repeated failures walk the bounded progression.
  captured[0]!.fire();
  await vi.waitFor(() => expect(captured).toHaveLength(2));
  captured[1]!.fire();
  await vi.waitFor(() => expect(captured).toHaveLength(3));
  captured[2]!.fire();
  await vi.waitFor(() => expect(captured).toHaveLength(4));
  captured[3]!.fire();
  await vi.waitFor(() => expect(captured).toHaveLength(5));
  expect(captured.map((entry) => entry.delay)).toEqual([250, 500, 1_000, 2_000, 2_000]);

  // A success resets the streak, so the next failure starts at 250 ms again.
  failure.mockRestore();
  captured[4]!.fire();
  await vi.waitFor(async () => expect(await keysInCache(fixture.cache)).toContain("item-alpha"));
  const nextFailure = vi
    .spyOn(fixture.cache, "commitBatch")
    .mockResolvedValueOnce({ outcome: "failed", error: new Error("simulated abort again") });
  owner.acceptRead(read("next", 4), { lifetime: 1, readId: "read-next", boundary: "boundary-next" });
  await vi.waitFor(() => expect(captured).toHaveLength(6));
  expect(captured[5]!.delay).toBe(250);
  nextFailure.mockRestore();
  timeoutSpy.mockRestore();
  owner.dispose();
});

// R3 remainder: cancelling a held retry must be an explicit disposition, and
// the cancelled callback must perform no write.
it("reports a cancelled settlement and performs no write when a held retry is cancelled", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const { scheduled, scheduler, cancelledCount } = capturingScheduler();
  const owner = repository(fixture.cache, 1, undefined, scheduler);
  owner.acceptRead(read("base"), { lifetime: 1, readId: "read-base", boundary: "boundary-base" });
  await owner.settled();
  const reader = owner.attachReader();

  const failure = vi
    .spyOn(fixture.cache, "commitBatch")
    .mockResolvedValueOnce({ outcome: "failed", error: new Error("simulated abort") });
  owner.acceptRead(read("alpha", 2), { lifetime: 1, readId: "read-alpha", boundary: "boundary-alpha" });
  await expect(owner.settled()).rejects.toThrow(/outstanding/);
  failure.mockRestore();
  expect(scheduled).toHaveLength(1);

  owner.invalidate();
  expect(cancelledCount()).toBeGreaterThanOrEqual(1);
  await expect(owner.settled()).rejects.toThrow(/cancel/);

  // The cancelled callback performs no write.
  const after = vi.spyOn(fixture.cache, "commitBatch");
  scheduled[0]!.fire();
  await Promise.resolve();
  expect(after).not.toHaveBeenCalled();
  after.mockRestore();
  reader.dispose();
  owner.dispose();
});

// R3 remainder: a failed write with no demand is never admitted to the queue
// and must be reported, not settled as void success.
it("reports a failed settlement for a failed write with no demand", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const owner = repository(fixture.cache);
  owner.acceptRead(read("base"), { lifetime: 1, readId: "read-base", boundary: "boundary-base" });
  await owner.settled();

  const failure = vi
    .spyOn(fixture.cache, "commitBatch")
    .mockResolvedValueOnce({ outcome: "failed", error: new Error("simulated abort") });
  owner.acceptRead(read("alpha", 2), { lifetime: 1, readId: "read-alpha", boundary: "boundary-alpha" });
  await expect(owner.settled()).rejects.toThrow(/failed/);
  failure.mockRestore();
  owner.dispose();
});

// D2: a desired Live background session has demand but no attached reader, so a
// failed background write must still schedule bounded-backoff recovery once
// storage recovers.
it("schedules bounded-backoff recovery for a no-reader background failure while demand exists", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const { scheduled, scheduler } = capturingScheduler();
  const owner = repository(fixture.cache, 1, undefined, scheduler, undefined, undefined, () => true);
  owner.acceptRead(read("base"), { lifetime: 1, readId: "read-base", boundary: "boundary-base" });
  await owner.settled();

  // One failed no-reader background write; demand is present but no reader is.
  const failure = vi
    .spyOn(fixture.cache, "commitBatch")
    .mockResolvedValueOnce({ outcome: "failed", error: new Error("simulated abort") });
  owner.acceptRead(read("beta", 2), { lifetime: 1, readId: "read-beta", boundary: "boundary-beta" });
  await vi.waitFor(() => expect(scheduled.length).toBeGreaterThanOrEqual(1));
  failure.mockRestore();

  // Storage is healthy again and nothing else happens: the paced retry recovers.
  scheduled[0]!.fire();
  await vi.waitFor(async () => expect(await keysInCache(fixture.cache)).toContain("item-beta"));
  await owner.settled();
  owner.dispose();
});
it("keeps a new foreground suffix behind a failed predecessor awaiting retry", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const { scheduled, scheduler } = capturingScheduler();
  const owner = repository(fixture.cache, 1, undefined, scheduler);
  const reader = owner.attachReader();
  owner.acceptRead(readItems("base", 1, [item("base", 1)]), {
    lifetime: 1,
    readId: "read-base",
    boundary: "boundary-base",
  });
  await owner.settled();
  const failure = vi
    .spyOn(fixture.cache, "commitBatch")
    .mockResolvedValueOnce({ outcome: "failed", error: new Error("suffix A abort") });
  try {
    owner.accept(historyNotification("alpha", 2, 2), { lifetime: 1 });
    await expect(owner.settled()).rejects.toThrow(/outstanding/);
    failure.mockRestore();
    expect(scheduled).toHaveLength(1);

    owner.accept(historyNotification("beta", 3, 3), { lifetime: 1 });
    expect(
      reader
        .getSnapshot()
        ?.turns.flatMap((turn) => turn.items)
        .map((item) => item.text),
    ).toEqual(["base", "alpha", "beta"]);
    await expect(owner.settled()).rejects.toThrow(/outstanding/);
    scheduled[0]!.fire();
    await owner.settled();

    expect((await fixture.cache.getCoverage(historyKey, "read:read-base"))?.suffixBlocked).not.toBe(true);
    expect((await fixture.cache.getCoverage(historyKey, "suffix:read-base"))?.representedSnapshot).toBe(3);
    expect((await keysInCache(fixture.cache)).sort()).toEqual(["item-alpha", "item-base", "item-beta"]);
  } finally {
    failure.mockRestore();
    reader.dispose();
    owner.dispose();
  }
});

it.each([false, true])(
  "a later read replaces the suffix predecessor, later baseline fails=%s",
  async (laterBaselineFails) => {
    const fixture = createEntryCacheFixture();
    fixtures.push(fixture);
    const { scheduled, scheduler } = capturingScheduler();
    const owner = repository(fixture.cache, 1, undefined, scheduler);
    const reader = owner.attachReader();
    owner.acceptRead(readItems("base", 1, [item("base", 1)]), {
      lifetime: 1,
      readId: "read-base",
      boundary: "boundary-base",
    });
    await owner.settled();
    const realCommit = fixture.cache.commitBatch.bind(fixture.cache);
    let failOldSuffix = true;
    let failLaterBaseline = laterBaselineFails;
    const commits = vi.spyOn(fixture.cache, "commitBatch").mockImplementation((batch) => {
      if (failOldSuffix && batch.coverage?.kind === "suffix" && batch.coverage.readId === "read-base") {
        failOldSuffix = false;
        return Promise.resolve({ outcome: "failed", error: new Error("old suffix abort") });
      }
      if (failLaterBaseline && batch.coverage?.kind === "read" && batch.coverage.readId === "read-later") {
        failLaterBaseline = false;
        return Promise.resolve({ outcome: "failed", error: new Error("later baseline abort") });
      }
      return realCommit(batch);
    });
    try {
      owner.accept(historyNotification("alpha", 2, 2), { lifetime: 1 });
      await expect(owner.settled()).rejects.toThrow(/outstanding/);
      const failedBoundary = commits.mock.calls[0]![0].coverage!.boundary;
      expect(scheduled).toHaveLength(1);

      owner.acceptRead(readItems("later", 3, [item("base", 1), item("alpha", 2), item("beta", 3)]), {
        lifetime: 1,
        readId: "read-later",
        boundary: "boundary-later",
      });
      await expect(owner.settled()).rejects.toThrow(/outstanding/);
      // The independent read dispatches before the old suffix's retry fires.
      expect(commits.mock.calls[1]![0].coverage).toMatchObject({ kind: "read", readId: "read-later" });
      expect((await fixture.cache.getCoverage(historyKey, "read:read-later")) !== undefined).toBe(!laterBaselineFails);

      owner.accept(historyNotification("gamma", 4, 4), { lifetime: 1 });
      expect(
        reader
          .getSnapshot()
          ?.turns.flatMap((turn) => turn.items)
          .map((item) => item.text),
      ).toEqual(["base", "alpha", "beta", "gamma"]);
      await expect(owner.settled()).rejects.toThrow(/outstanding/);
      scheduled[0]!.fire();
      if (laterBaselineFails) {
        await vi.waitFor(() => expect(scheduled).toHaveLength(2));
        scheduled[1]!.fire();
      }
      await owner.settled();

      const suffix = commits.mock.calls
        .map(([batch]) => batch.coverage)
        .find((coverage) => coverage?.kind === "suffix" && coverage.readId === "read-later");
      expect(suffix).toMatchObject({ readId: "read-later", previousBoundary: "boundary-later" });
      expect(suffix?.kind === "suffix" && suffix.previousBoundary).not.toBe(failedBoundary);
      expect((await fixture.cache.getCoverage(historyKey, "read:read-later"))?.suffixBlocked).not.toBe(true);
      expect((await fixture.cache.getCoverage(historyKey, "suffix:read-later"))?.representedSnapshot).toBe(4);
    } finally {
      commits.mockRestore();
      reader.dispose();
      owner.dispose();
    }
  },
);

// N1 (P1): a repeatedly failed predecessor must not be moved behind its
// dependent suffix. Consecutive suffix notifications recovered in order
// certify the chain; the reverse order blocks it forever.
it("recovers consecutive chained suffixes in order without permanently blocking the chain", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const { scheduled, scheduler } = capturingScheduler();
  const owner = repository(fixture.cache, 1, undefined, scheduler);
  owner.acceptRead(readItems("base", 1, [item("base", 1)]), {
    lifetime: 1,
    readId: "read-base",
    boundary: "boundary-base",
  });
  await owner.settled();
  const reader = owner.attachReader();

  // Two consecutive suffixes chained R -> A -> B; both initial writes fail.
  const failure = vi
    .spyOn(fixture.cache, "commitBatch")
    .mockResolvedValue({ outcome: "failed", error: new Error("simulated abort") });
  owner.accept(historyNotification("alpha", 2, 2), { lifetime: 1 });
  owner.accept(historyNotification("beta", 3, 3), { lifetime: 1 });
  await expect(owner.settled()).rejects.toThrow(/outstanding/);
  await vi.waitFor(() => expect(failure).toHaveBeenCalledTimes(2));
  expect(scheduled).toHaveLength(1);

  // A's first retry fails again: it must keep the front, ahead of B.
  scheduled[0]!.fire();
  await vi.waitFor(() => expect(scheduled).toHaveLength(2));

  // Storage recovers; drain with no new input.
  failure.mockRestore();
  scheduled[1]!.fire();
  await vi.waitFor(() => expect(scheduled).toHaveLength(3));
  scheduled[2]!.fire();
  await owner.settled();

  // The certified suffix reaches B and a fresh reader is honestly current.
  const coverage = await fixture.cache.getCoverage(historyKey, "suffix:read-base");
  expect(coverage?.representedSnapshot).toBe(3);
  expect((await keysInCache(fixture.cache)).sort()).toEqual(["item-alpha", "item-base", "item-beta"]);
  const fresh = owner.attachReader();
  const opened = await fresh.openLocal(40);
  expect(opened.status).toBe("current");
  fresh.dispose();
  reader.dispose();
  owner.dispose();
});

// Task 3b (R3 remainder): a late predecessor commit must not erase a
// cancellation recorded for a different, unrelated dropped successor. A's retry
// is in flight when invalidate() records "cancelled" and drops queued B; A's
// real completion is an A-only extended suffix, but it repairs only its own
// chain, so B's abandonment must stand and settled() must still refuse success.
it("does not clear a cancelled disposition when a late predecessor commit completes", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const { scheduled, scheduler } = capturingScheduler();
  const owner = repository(fixture.cache, 1, undefined, scheduler);
  owner.acceptRead(readItems("base", 1, [item("base", 1)]), {
    lifetime: 1,
    readId: "read-base",
    boundary: "boundary-base",
  });
  await owner.settled();
  const reader = owner.attachReader();

  // Two consecutive suffixes A -> B; both initial writes fail and queue for retry.
  const failure = vi
    .spyOn(fixture.cache, "commitBatch")
    .mockResolvedValue({ outcome: "failed", error: new Error("simulated abort") });
  owner.accept(historyNotification("alpha", 2, 2), { lifetime: 1 });
  owner.accept(historyNotification("beta", 3, 3), { lifetime: 1 });
  await expect(owner.settled()).rejects.toThrow(/outstanding/);
  await vi.waitFor(() => expect(failure).toHaveBeenCalledTimes(2));
  expect(scheduled).toHaveLength(1);
  failure.mockRestore();

  // Fire A's retry but hold its real commit before it reaches storage. Releasing
  // the gate runs the real method, an A-only extended suffix over the predecessor.
  const realCommit = fixture.cache.commitBatch.bind(fixture.cache);
  const gate = deferred<void>();
  const heldRetry = vi.spyOn(fixture.cache, "commitBatch").mockImplementationOnce(async (batch) => {
    await gate.promise;
    return realCommit(batch);
  });
  scheduled[0]!.fire();
  // A has left the queue with its write in flight; B remains queued.
  expect(scheduled).toHaveLength(1);

  // Cancel: records "cancelled" and drops the queued B, which never commits.
  owner.invalidate();

  // Release A. Its real commit extends coverage over A's own suffix only.
  gate.resolve();
  await vi.waitFor(async () => expect(await keysInCache(fixture.cache)).toContain("item-alpha"));

  // B was abandoned and never committed, so the cancellation stands.
  await expect(owner.settled()).rejects.toThrow(/cancel/);
  expect(await keysInCache(fixture.cache)).not.toContain("item-beta");
  heldRetry.mockRestore();
  reader.dispose();
  owner.dispose();
});

// Task 3c: a *live* repository can also lose an unadmitted failed suffix's
// disposition when a predecessor finishes. The disposition records *that*
// something was abandoned, not *what*, so clearing it because the repository is
// live cannot know whether this commit repaired the abandoned work. A's held
// commit extends only its own suffix (snapshot 2); B was abandoned at snapshot
// 3, so A must not clear it and settled() must still report the failure.
it("does not clear a failed disposition from a live repository when a predecessor commit completes", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const { scheduled, scheduler } = capturingScheduler();
  const owner = repository(fixture.cache, 1, undefined, scheduler);
  owner.acceptRead(readItems("base", 1, [item("base", 1)]), {
    lifetime: 1,
    readId: "read-base",
    boundary: "boundary-base",
  });
  await owner.settled();
  const reader = owner.attachReader();

  // Suffix A's initial write fails and enters the retry queue.
  const failure = vi
    .spyOn(fixture.cache, "commitBatch")
    .mockResolvedValueOnce({ outcome: "failed", error: new Error("simulated abort A") });
  owner.accept(historyNotification("alpha", 2, 2), { lifetime: 1 });
  await expect(owner.settled()).rejects.toThrow(/outstanding/);
  await vi.waitFor(() => expect(scheduled).toHaveLength(1));
  failure.mockRestore();

  // Fire A's retry under a gate that later runs the real commit, so A leaves the
  // queue, sets #retryInFlight, and stays pending in the write set.
  const realCommit = fixture.cache.commitBatch.bind(fixture.cache);
  const gate = deferred<void>();
  const commits = vi
    .spyOn(fixture.cache, "commitBatch")
    .mockImplementationOnce(async (batch) => {
      await gate.promise;
      return realCommit(batch);
    })
    .mockResolvedValueOnce({ outcome: "failed", error: new Error("simulated abort B") });
  scheduled[0]!.fire();

  // Dispose the reader only; the repository stays live (#invalidated/#disposed
  // remain false).
  reader.dispose();

  // B, chained after A, is queued behind A's in-flight retry: one repository must
  // not overlap its own background batches. It issues once A settles, fails
  // through the ordinary path with no reader demand, and is never admitted to the
  // retry queue; that records "failed".
  owner.accept(historyNotification("beta", 3, 3), { lifetime: 1 });
  expect(commits).toHaveBeenCalledTimes(1);

  // Release A. Its real commit extends R through A only (snapshot 2); B then
  // issues and fails for real.
  gate.resolve();
  await vi.waitFor(() => expect(commits).toHaveBeenCalledTimes(2));
  // Event-loop barrier: B's failed-outcome handler has run by now.
  await new Promise((resolve) => setTimeout(resolve, 0));
  await vi.waitFor(async () => expect(await keysInCache(fixture.cache)).toContain("item-alpha"));

  // B was abandoned and never committed, so settled() must still report it.
  await expect(owner.settled()).rejects.toThrow(/failed/);
  expect(await keysInCache(fixture.cache)).toContain("item-alpha");
  expect(await keysInCache(fixture.cache)).not.toContain("item-beta");
  commits.mockRestore();
  owner.dispose();
});

// Task 3c positive control: the new condition must not make every live
// abandonment permanent. A live repository that re-covers the abandoned suffix's
// window with a real extended commit clears the disposition and settles cleanly.
it("clears a failed disposition when a live repository re-covers the abandoned work", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const { scheduler } = capturingScheduler();
  const owner = repository(fixture.cache, 1, undefined, scheduler);
  owner.acceptRead(readItems("base", 1, [item("base", 1)]), {
    lifetime: 1,
    readId: "read-base",
    boundary: "boundary-base",
  });
  await owner.settled();

  // An unadmitted failed suffix with no reader demand records "failed".
  const failure = vi
    .spyOn(fixture.cache, "commitBatch")
    .mockResolvedValueOnce({ outcome: "failed", error: new Error("simulated abort") });
  owner.accept(historyNotification("beta", 2, 2), { lifetime: 1 });
  await expect(owner.settled()).rejects.toThrow(/failed/);
  failure.mockRestore();

  // A fresh live read re-covers snapshot 3 including beta, and its real commit
  // extends read coverage past the abandoned suffix's reach.
  owner.acceptRead(readItems("repair", 3, [item("base", 1), item("beta", 2), item("repair", 3)]), {
    lifetime: 1,
    readId: "read-repair",
    boundary: "boundary-repair",
  });
  await owner.settled();
  expect((await keysInCache(fixture.cache)).sort()).toEqual(["item-base", "item-beta", "item-repair"]);
  owner.dispose();
});

// Task 3e case A: an accepted certificate is window-specific, so a different
// window at the same snapshot count is not a repair. An older-page certificate
// covering only a position strictly below the held oldest range does not carry
// the abandoned latest suffix forward, even at the same snapshot length.
it("does not clear a failed disposition when a different window commits at the same snapshot", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const { scheduler } = capturingScheduler();
  const owner = repository(fixture.cache, 1, undefined, scheduler);
  owner.acceptRead(readItems("base", 1, [item("base", 1)]), {
    lifetime: 1,
    readId: "read-base",
    boundary: "boundary-base",
  });
  await owner.settled();

  // An unadmitted failed latest suffix at snapshot 2 records "failed".
  const failure = vi
    .spyOn(fixture.cache, "commitBatch")
    .mockResolvedValueOnce({ outcome: "failed", error: new Error("simulated abort") });
  owner.accept(historyNotification("beta", 2, 2), { lifetime: 1 });
  await expect(owner.settled()).rejects.toThrow(/failed/);
  failure.mockRestore();

  // A valid older page at the same snapshot 2, covering only a position strictly
  // before beta's range and not beta itself.
  const page: ThreadTurnsListResponse = {
    data: [{ id: "turn_1", itemsView: "full", status: "completed", version: 2, items: [item("alpha", 0, 3)] }],
    bootGeneration: "1",
    epoch: 0,
    snapshot: { incarnation: identity.incarnation, length: 2 },
  };
  expect(
    owner.acceptPage(page, {
      lifetime: 1,
      readId: "read-page",
      boundary: "boundary-page",
      authority: owner.getBaselineIdentity(),
    }),
  ).toBe(true);
  await vi.waitFor(async () => expect(await keysInCache(fixture.cache)).toContain("item-alpha"));

  // The abandoned suffix beta remains unresolved.
  await expect(owner.settled()).rejects.toThrow(/(cancelled|failed)/);
  expect(await keysInCache(fixture.cache)).toContain("item-alpha");
  expect(await keysInCache(fixture.cache)).not.toContain("item-beta");
  owner.dispose();
});

// D1: a later, narrower successful read must not certify a partial transcript
// as current. Gamma extends its own read coverage but does not carry the
// abandoned Beta suffix forward, so local opening must stay recovery-needed.
it("keeps local opening recovery-needed when a later narrower window does not carry an abandoned write", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const owner = repository(fixture.cache);
  owner.acceptRead(readItems("base", 1, [item("base", 1)]), {
    lifetime: 1,
    readId: "read-base",
    boundary: "boundary-base",
  });
  await owner.settled();

  // Beta's background write (no reader demand) fails and records a failed
  // abandonment of its suffix extent.
  const failure = vi
    .spyOn(fixture.cache, "commitBatch")
    .mockResolvedValueOnce({ outcome: "failed", error: new Error("simulated abort") });
  owner.accept(historyNotification("beta", 2, 2), { lifetime: 1 });
  await expect(owner.settled()).rejects.toThrow(/failed/);
  failure.mockRestore();

  // A latest Gamma window with an older cursor whose coverage starts above
  // beta. Its own read certificate still extends.
  const gamma = readItems("gamma", 4, [item("gamma", 3)]);
  gamma.olderCursor = "cursor-older";
  owner.acceptRead(gamma, { lifetime: 1, readId: "read-gamma", boundary: "boundary-gamma" });
  await expect(owner.settled()).rejects.toThrow(/failed/);

  // Disk holds base and gamma, but the abandoned beta body is missing.
  expect(await keysInCache(fixture.cache)).toContain("item-base");
  expect(await keysInCache(fixture.cache)).toContain("item-gamma");
  expect(await keysInCache(fixture.cache)).not.toContain("item-beta");

  const reader = owner.attachReader();
  const opened = await reader.openLocal(40);
  // The abandoned extent was not carried forward, so opening must not report
  // current while settlement still reports failed recovery.
  expect(opened.status).toBe("recovery-needed");
  expect(owner.getRecoveryStateForTesting().cacheContinuityLost).toBe(true);
  reader.dispose();
  owner.dispose();
});

// Task 3e case B: a later smaller abandonment must not lower an earlier larger
// obligation. A's dropped retry at reach 2 must not replace B's failed reach 3,
// so C's real commit covering only reach 2 cannot clear the disposition.
it("does not lower an earlier larger abandonment when a later smaller one is recorded", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const { scheduled, scheduler } = capturingScheduler();
  const owner = repository(fixture.cache, 1, undefined, scheduler);
  owner.acceptRead(readItems("base", 1, [item("base", 1)]), {
    lifetime: 1,
    readId: "read-base",
    boundary: "boundary-base",
  });
  await owner.settled();
  const reader = owner.attachReader();

  // A (reach 2) fails and its retry timer is held.
  const failureA = vi
    .spyOn(fixture.cache, "commitBatch")
    .mockResolvedValueOnce({ outcome: "failed", error: new Error("simulated abort A") });
  owner.accept(historyNotification("alpha", 2, 2), { lifetime: 1 });
  await vi.waitFor(() => expect(scheduled).toHaveLength(1));
  failureA.mockRestore();

  // C is an ordinary read commit at reach 2, held before storage.
  const realCommit = fixture.cache.commitBatch.bind(fixture.cache);
  const gate = deferred<void>();
  const commits = vi
    .spyOn(fixture.cache, "commitBatch")
    .mockImplementationOnce(async (batch) => {
      await gate.promise;
      return realCommit(batch);
    })
    .mockResolvedValueOnce({ outcome: "failed", error: new Error("simulated abort B") });
  expect(
    owner.acceptRead(readItems("c", 2, [item("base", 1, 2), item("alpha", 2, 2)]), {
      lifetime: 1,
      readId: "read-c",
      boundary: "boundary-c",
    }),
  ).toBe(true);

  // Release the last reader; B (reach 3) fails with no demand and is unadmitted.
  reader.dispose();
  owner.accept(historyNotification("beta", 3, 3), { lifetime: 1 });
  await vi.waitFor(() => expect(commits).toHaveBeenCalledTimes(2));
  await new Promise((resolve) => setTimeout(resolve, 0));

  // Fire A's held timer: #drainRetry drops the queued A while no reader remains.
  scheduled[0]!.fire();

  // Release C: a real extended commit covering only reach 2.
  gate.resolve();
  await vi.waitFor(async () => expect(await keysInCache(fixture.cache)).toContain("item-alpha"));

  // The larger abandonment stands; B's higher-reach row remains absent.
  await expect(owner.settled()).rejects.toThrow(/(cancelled|failed)/);
  expect(await keysInCache(fixture.cache)).not.toContain("item-beta");
  commits.mockRestore();
  owner.dispose();
});

// N2 (P2): the drain is serial. While a retried batch is in flight, a normal
// settled write must not arm the next retry.
it("does not issue the next retry while a retried batch is in flight", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const { scheduled, scheduler } = capturingScheduler();
  const owner = repository(fixture.cache, 1, undefined, scheduler);
  owner.acceptRead(read("base"), { lifetime: 1, readId: "read-base", boundary: "boundary-base" });
  await owner.settled();
  const reader = owner.attachReader();

  const failure = vi
    .spyOn(fixture.cache, "commitBatch")
    .mockResolvedValue({ outcome: "failed", error: new Error("simulated abort") });
  owner.acceptRead(read("alpha", 2), { lifetime: 1, readId: "read-alpha", boundary: "boundary-alpha" });
  owner.acceptRead(read("beta", 3), { lifetime: 1, readId: "read-beta", boundary: "boundary-beta" });
  await expect(owner.settled()).rejects.toThrow(/outstanding/);
  await vi.waitFor(() => expect(failure).toHaveBeenCalledTimes(2));
  expect(scheduled).toHaveLength(1);
  failure.mockRestore();

  // Hold A's retried commit; an ordinary C then commits for real.
  const held = deferred<EntryWriteOutcome>();
  const heldRetry = vi.spyOn(fixture.cache, "commitBatch").mockImplementationOnce(() => held.promise);
  scheduled[0]!.fire();
  owner.acceptRead(read("gamma", 4), { lifetime: 1, readId: "read-gamma", boundary: "boundary-gamma" });
  await vi.waitFor(async () => expect(await keysInCache(fixture.cache)).toContain("item-gamma"));

  // A is still in flight: no next retry was armed and B did not issue.
  expect(scheduled).toHaveLength(1);
  expect(await keysInCache(fixture.cache)).not.toContain("item-beta");

  // Release A as failed; A keeps the front and the next attempt is armed.
  held.resolve({ outcome: "failed", error: new Error("simulated abort") });
  await vi.waitFor(() => expect(scheduled).toHaveLength(2));
  heldRetry.mockRestore();
  scheduled[1]!.fire();
  await vi.waitFor(() => expect(scheduled).toHaveLength(3));
  scheduled[2]!.fire();
  await owner.settled();
  expect((await keysInCache(fixture.cache)).sort()).toEqual(["item-alpha", "item-base", "item-beta", "item-gamma"]);
  reader.dispose();
  owner.dispose();
});

// Task 3b R1 (round 2): the over-capacity exception is the *sole* immediate
// bounded batch. A competing over-capacity response is refused for caching, and
// its bytes are accounted separately from the aggregate.
it("admits only one over-capacity batch at a time and accounts its bytes separately", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const admission = new EntryWriteAdmission(500);
  const owner = repository(fixture.cache, 1, undefined, undefined, admission);
  owner.acceptRead(read("base"), { lifetime: 1, readId: "read-base", boundary: "boundary-base" });
  await owner.settled();

  const gate = deferred<void>();
  const realCommit = fixture.cache.commitBatch.bind(fixture.cache);
  const issued: string[] = [];
  const spy = vi.spyOn(fixture.cache, "commitBatch").mockImplementation(async (batch) => {
    issued.push(batch.items.map((row) => row.transcriptKey).join(","));
    if (batch.items.some((row) => row.transcriptKey === "item-hugeA")) await gate.promise;
    return realCommit(batch);
  });

  expect(
    owner.acceptRead(readItems("hugeA", 2, [item("base", 1), bigItem("hugeA", 2, 2_000)]), {
      lifetime: 1,
      readId: "read-hugeA",
      boundary: "boundary-hugeA",
    }),
  ).toBe(true);
  await vi.waitFor(() => expect(admission.snapshotForTesting().exemptActive).toBe(true));
  const exemptBytes = admission.snapshotForTesting().exemptBytes;
  expect(exemptBytes).toBeGreaterThan(2_000);

  // The competing over-capacity response is refused, not issued and not staged.
  expect(
    owner.acceptRead(readItems("hugeB", 3, [item("base", 1), bigItem("hugeB", 3, 2_000)]), {
      lifetime: 1,
      readId: "read-hugeB",
      boundary: "boundary-hugeB",
    }),
  ).toBe(true);
  await Promise.resolve();
  expect(issued.some((keys) => keys.includes("item-hugeB"))).toBe(false);
  expect(await keysInCache(fixture.cache)).not.toContain("item-hugeB");
  expect(admission.snapshotForTesting().retainedBytes).toBeLessThanOrEqual(500);
  // Round-12 amendment 1 supersedes retained background display: refused
  // bodies are released, and reconciliation restores content on demand.
  expect(
    owner
      .getSnapshot()
      ?.turns.flatMap((turn) => turn.items)
      .map((entry) => entry.transcriptKey),
  ).not.toContain("item-hugeB");

  gate.resolve();
  await vi.waitFor(async () => expect(await keysInCache(fixture.cache)).toContain("item-hugeA"));
  await vi.waitFor(() => expect(admission.snapshotForTesting().exemptActive).toBe(false));
  spy.mockRestore();
  owner.dispose();
});

it("reclaims background bodies while admitted writes are held and queued", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const admission = new EntryWriteAdmission(3_000);
  const owner = repository(fixture.cache, 1, undefined, undefined, admission);
  owner.acceptRead(read("base"), { lifetime: 1, readId: "base", boundary: "base" });
  await owner.settled();
  owner.accept(
    {
      method: "overlay/upserted",
      params: {
        ref,
        threadId,
        item: {
          key: "stream:live/1:agentMessage",
          kind: "stream",
          turnId: "turn_1",
          roundId: "live",
          streamId: "live/1",
          item: {
            type: "agentMessage",
            id: "live",
            turnId: "turn_1",
            roundId: "live",
            text: "active overlay",
            status: "inProgress",
          },
        },
      },
    } as unknown as AnyNotification,
    { lifetime: 1 },
  );
  const gate = deferred<void>();
  const realCommit = fixture.cache.commitBatch.bind(fixture.cache);
  const spy = vi.spyOn(fixture.cache, "commitBatch").mockImplementation(async (batch) => {
    if (batch.items.some((row) => row.transcriptKey === "item-held")) await gate.promise;
    return realCommit(batch);
  });
  const send = (label: string, entry: number) => {
    const notification = historyNotification(label, entry, entry);
    if (notification.method !== "history/updated") throw new Error("wrong fixture");
    notification.params.items = [bigItem(label, entry, 1_000)];
    expect(owner.accept(notification, { lifetime: 1 })).toBe(true);
  };
  send("held", 2);
  await vi.waitFor(() => expect(spy).toHaveBeenCalledTimes(1));
  send("queued", 3);
  expect(spy).toHaveBeenCalledTimes(1);
  expect(admission.retainedBytes).toBeGreaterThan(2_000);
  expect(
    owner
      .getSnapshot()
      ?.history?.turns.flatMap((turn) => turn.items)
      .map((entry) => entry.text),
  ).toEqual(["", `held${"x".repeat(1_000)}`, `queued${"x".repeat(1_000)}`]);
  expect(
    owner
      .getSnapshot()
      ?.turns.flatMap((turn) => turn.items)
      .map((entry) => entry.text),
  ).toEqual(expect.arrayContaining([`held${"x".repeat(1_000)}`, `queued${"x".repeat(1_000)}`, "active overlay"]));
  for (let entry = 4; entry < 10; entry++) send(`refused-${entry}`, entry);
  expect(admission.retainedBytes).toBeLessThanOrEqual(3_000);
  try {
    const model = owner.getSnapshot();
    for (const turns of [model?.history?.turns, model?.turns]) {
      const bodies = turns?.flatMap((turn) => turn.items).map((entry) => entry.text);
      expect(bodies).toEqual(expect.arrayContaining([`held${"x".repeat(1_000)}`, `queued${"x".repeat(1_000)}`]));
      expect(bodies?.some((text) => text?.startsWith("refused-"))).toBe(false);
    }
    expect(model?.turns.flatMap((turn) => turn.items).map((entry) => entry.text)).toContain("active overlay");
    expect(model?.overlay?.["stream:live/1:agentMessage"]?.item.text).toBe("active overlay");
    expect(spy.mock.calls[0]?.[0].items[0]?.item).toMatchObject({ text: `held${"x".repeat(1_000)}` });
  } finally {
    gate.resolve();
  }
  await vi.waitFor(async () => expect(await keysInCache(fixture.cache)).toContain("item-queued"));
  const rows = await fixture.cache.getLatest(historyKey, 40);
  expect(rows.find((row) => row.transcriptKey === "item-held")?.item).toMatchObject({
    text: `held${"x".repeat(1_000)}`,
  });
  expect(rows.find((row) => row.transcriptKey === "item-queued")?.item).toMatchObject({
    text: `queued${"x".repeat(1_000)}`,
  });
  expect(rows.some((row) => row.transcriptKey.startsWith("item-refused"))).toBe(false);
  spy.mockRestore();
  owner.dispose();
});

it("reclaims versionless refused bodies while admitted writes are held and queued", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const admission = new EntryWriteAdmission(3_000);
  const owner = repository(fixture.cache, 1, undefined, undefined, admission);
  owner.acceptRead(read("base"), { lifetime: 1, readId: "base", boundary: "base" });
  await owner.settled();
  owner.accept(
    {
      method: "overlay/upserted",
      params: {
        ref,
        threadId,
        item: {
          key: "stream:live/1:agentMessage",
          kind: "stream",
          turnId: "turn_1",
          roundId: "live",
          streamId: "live/1",
          item: {
            type: "agentMessage",
            id: "live",
            turnId: "turn_1",
            roundId: "live",
            text: "active overlay",
            status: "inProgress",
          },
        },
      },
    } as unknown as AnyNotification,
    { lifetime: 1 },
  );
  const gate = deferred<void>();
  const realCommit = fixture.cache.commitBatch.bind(fixture.cache);
  const spy = vi.spyOn(fixture.cache, "commitBatch").mockImplementation(async (batch) => {
    if (batch.items.some((row) => row.transcriptKey === "item-held")) await gate.promise;
    return realCommit(batch);
  });
  const send = (label: string, entry: number) => {
    const notification = historyNotification(label, entry, entry);
    if (notification.method !== "history/updated") throw new Error("wrong fixture");
    notification.params.items = [{ ...bigItem(label, entry, 1_000), version: undefined }];
    expect(owner.accept(notification, { lifetime: 1 })).toBe(true);
  };
  send("held", 2);
  await vi.waitFor(() => expect(spy).toHaveBeenCalledTimes(1));
  send("queued", 3);
  expect(spy).toHaveBeenCalledTimes(1);
  expect(admission.retainedBytes).toBeGreaterThan(2_000);
  expect(
    owner
      .getSnapshot()
      ?.history?.turns.flatMap((turn) => turn.items)
      .map((entry) => entry.text),
  ).toEqual(["", `held${"x".repeat(1_000)}`, `queued${"x".repeat(1_000)}`]);
  expect(
    owner
      .getSnapshot()
      ?.turns.flatMap((turn) => turn.items)
      .map((entry) => entry.text),
  ).toEqual(expect.arrayContaining([`held${"x".repeat(1_000)}`, `queued${"x".repeat(1_000)}`, "active overlay"]));
  for (let entry = 4; entry < 10; entry++) send(`refused-${entry}`, entry);
  expect(admission.retainedBytes).toBeLessThanOrEqual(3_000);
  try {
    const model = owner.getSnapshot();
    for (const turns of [model?.history?.turns, model?.turns]) {
      const bodies = turns?.flatMap((turn) => turn.items).map((entry) => entry.text);
      expect(bodies).toEqual(expect.arrayContaining([`held${"x".repeat(1_000)}`, `queued${"x".repeat(1_000)}`]));
      expect(bodies?.some((text) => text?.startsWith("refused-"))).toBe(false);
    }
    expect(model?.turns.flatMap((turn) => turn.items).map((entry) => entry.text)).toContain("active overlay");
    expect(model?.overlay?.["stream:live/1:agentMessage"]?.item.text).toBe("active overlay");
    expect(spy.mock.calls[0]?.[0].items[0]?.item).toMatchObject({ text: `held${"x".repeat(1_000)}` });
  } finally {
    gate.resolve();
  }
  await vi.waitFor(async () => expect(await keysInCache(fixture.cache)).toContain("item-queued"));
  const rows = await fixture.cache.getLatest(historyKey, 40);
  expect(rows.find((row) => row.transcriptKey === "item-held")?.item).toMatchObject({
    text: `held${"x".repeat(1_000)}`,
  });
  expect(rows.find((row) => row.transcriptKey === "item-queued")?.item).toMatchObject({
    text: `queued${"x".repeat(1_000)}`,
  });
  expect(rows.some((row) => row.transcriptKey.startsWith("item-refused"))).toBe(false);
  spy.mockRestore();
  owner.dispose();
});

// Task 3b R2 (round 2): retirement must keep an issued write's charge and real
// body references until its actual terminal outcome, not free them early.
it("keeps an issued write's charge and captured bodies across dispose until it settles", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const admission = new EntryWriteAdmission(3_000);
  const owner = repository(fixture.cache, 1, undefined, undefined, admission);
  owner.acceptRead(read("base"), { lifetime: 1, readId: "read-base", boundary: "boundary-base" });
  await owner.settled();

  const gate = deferred<void>();
  const realCommit = fixture.cache.commitBatch.bind(fixture.cache);
  const spy = vi.spyOn(fixture.cache, "commitBatch").mockImplementationOnce(async (batch) => {
    await gate.promise;
    return realCommit(batch);
  });
  owner.acceptRead(readItems("held", 2, [item("base", 1), bigItem("held", 2, 1_000)]), {
    lifetime: 1,
    readId: "read-held",
    boundary: "boundary-held",
  });
  await vi.waitFor(() => expect(owner.retainedInventoryForTesting().inFlightBodies.length).toBeGreaterThan(0));
  const captured = owner.retainedInventoryForTesting().inFlightBodies;
  const charged = admission.retainedBytes;
  expect(charged).toBeGreaterThan(0);
  expect(admission.isRetained(captured[0]!)).toBe(true);

  owner.dispose();
  // Neither the counter nor the captured references were freed early.
  expect(admission.retainedBytes).toBe(charged);
  expect(admission.isRetained(captured[0]!)).toBe(true);

  gate.resolve();
  await vi.waitFor(() => expect(admission.retainedBytes).toBe(0));
  expect(admission.isRetained(captured[0]!)).toBe(false);
  spy.mockRestore();
});

// Task 3b R3 (round 2): at most one background database batch is in flight
// across repositories. Ordinary no-reader accepted work shares the slot; a
// stalled background batch does not block foreground work.
it("runs one background database batch at a time across repositories", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const admission = new EntryWriteAdmission(100_000);
  const ownerA = repository(fixture.cache, 1, undefined, undefined, admission);
  const ownerB = repository(fixture.cache, 2, undefined, undefined, admission);
  ownerA.acceptRead(read("baseA"), { lifetime: 1, readId: "read-baseA", boundary: "boundary-baseA" });
  await ownerA.settled();
  ownerB.acceptRead(read("baseB"), { lifetime: 2, readId: "read-baseB", boundary: "boundary-baseB" });
  await ownerB.settled();

  const gate = deferred<void>();
  const realCommit = fixture.cache.commitBatch.bind(fixture.cache);
  const issued: string[] = [];
  const spy = vi.spyOn(fixture.cache, "commitBatch").mockImplementation(async (batch) => {
    issued.push(batch.items.map((row) => row.transcriptKey).join(","));
    if (batch.items.some((row) => row.transcriptKey === "item-a1")) await gate.promise;
    return realCommit(batch);
  });

  // No reader: A's accepted write is issued and stalls there.
  ownerA.acceptRead(read("a1", 2), { lifetime: 1, readId: "read-a1", boundary: "boundary-a1" });
  await vi.waitFor(() => expect(issued.some((keys) => keys.includes("item-a1"))).toBe(true));
  // B also has no reader: its accepted write must wait for the slot.
  ownerB.acceptRead(read("b1", 2), { lifetime: 2, readId: "read-b1", boundary: "boundary-b1" });
  await Promise.resolve();
  expect(issued.some((keys) => keys.includes("item-b1"))).toBe(false);

  // Foreground demand is unaffected: a reader lets B commit immediately.
  const readerB = ownerB.attachReader();
  ownerB.acceptRead(read("bForeground", 3), { lifetime: 2, readId: "read-bfg", boundary: "boundary-bfg" });
  await vi.waitFor(async () => expect(await keysInCache(fixture.cache)).toContain("item-bForeground"));

  // Releasing A hands the slot to B, whose queued write then commits.
  gate.resolve();
  await vi.waitFor(async () => expect(await keysInCache(fixture.cache)).toContain("item-b1"));
  spy.mockRestore();
  readerB.dispose();
  ownerA.dispose();
  ownerB.dispose();
});

// Task 3b R3 (round 2): the slot is cleared before a waiter runs, so a waiter
// that lost its demand and declines cannot strand it; a healthy repository still
// progresses.
it("hands the background slot past a waiter that declines", () => {
  const admission = new EntryWriteAdmission(100);
  const holder = {};
  const declining = {};
  const healthy = {};
  const resumed: string[] = [];
  expect(
    admission.tryAcquireBackground(
      holder,
      () => false,
      () => {},
    ),
  ).toBe(true);
  expect(
    admission.tryAcquireBackground(
      declining,
      () => true,
      () => {},
    ),
  ).toBe(false);
  const acquireHealthy = () => {
    if (admission.tryAcquireBackground(healthy, () => true, acquireHealthy)) resumed.push("healthy");
  };
  expect(admission.tryAcquireBackground(healthy, () => true, acquireHealthy)).toBe(false);
  admission.releaseBackground(holder);
  expect(resumed).toEqual(["healthy"]);
  expect(admission.snapshotForTesting().backgroundInFlight).toBe(true);
  admission.releaseBackground(healthy);
  expect(admission.snapshotForTesting().backgroundInFlight).toBe(false);
});

// Task 3b R4 (round 2): overflow must issue a real reconciliation request, not
// merely re-check an empty retry queue.
it("requests reconciliation through its owner when overflow drops accepted work", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const { scheduled, scheduler } = capturingScheduler();
  const reconciles: number[] = [];
  const admission = new EntryWriteAdmission(3_000);
  const owner = repository(fixture.cache, 1, undefined, scheduler, admission, () => reconciles.push(1));
  owner.acceptRead(read("base"), { lifetime: 1, readId: "read-base", boundary: "boundary-base" });
  await owner.settled();
  const reader = owner.attachReader();

  // Hold a foreground write so the aggregate is occupied.
  const gate = deferred<void>();
  const realCommit = fixture.cache.commitBatch.bind(fixture.cache);
  const spy = vi.spyOn(fixture.cache, "commitBatch").mockImplementationOnce(async (batch) => {
    await gate.promise;
    return realCommit(batch);
  });
  owner.acceptRead(readItems("occupier", 2, [item("base", 1), bigItem("occupier", 2, 2_000)]), {
    lifetime: 1,
    readId: "read-occupier",
    boundary: "boundary-occupier",
  });
  await vi.waitFor(() => expect(admission.retainedBytes).toBeGreaterThan(0));

  // A smaller response cannot fit: it overflows and arms one paced reconciliation.
  owner.acceptRead(readItems("overflow", 3, [item("base", 1), bigItem("overflow", 3, 1_000)]), {
    lifetime: 1,
    readId: "read-overflow",
    boundary: "boundary-overflow",
  });
  await Promise.resolve();
  expect(reconciles).toHaveLength(0);
  expect(scheduled.length).toBeGreaterThanOrEqual(1);
  scheduled.at(-1)!.fire();
  expect(reconciles).toHaveLength(1);

  gate.resolve();
  await vi.waitFor(() => expect(admission.retainedBytes).toBe(0));
  spy.mockRestore();
  reader.dispose();
  owner.dispose();
});

// Task 3b R5 (round 2): completed ended-round overlay bodies preserved for
// handoff are retained in memory and count against the aggregate.
it("counts preserved ended handoff overlay bodies against the aggregate", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const admission = new EntryWriteAdmission(8_000);
  const owner = repository(fixture.cache, 1, undefined, undefined, admission);
  owner.acceptRead(read("base"), { lifetime: 1, readId: "read-base", boundary: "boundary-base" });
  await owner.settled();
  const before = admission.retainedBytes;

  const key = "stream:round-9/1:agentMessage";
  expect(
    owner.accept(
      {
        method: "overlay/upserted",
        params: {
          ref,
          threadId,
          item: {
            key,
            kind: "stream",
            turnId: "turn_1",
            roundId: "round-9",
            streamId: "round-9/1",
            item: {
              type: "agentMessage",
              id: "item-ended",
              turnId: "turn_1",
              roundId: "round-9",
              text: "z".repeat(3_000),
            },
          },
        },
      } as unknown as AnyNotification,
      { lifetime: 1 },
    ),
  ).toBe(true);
  expect(
    owner.accept(
      { method: "overlay/end", params: { ref, threadId, roundId: "round-9" } } as unknown as AnyNotification,
      {
        lifetime: 1,
      },
    ),
  ).toBe(true);

  // The uncovered ended body is preserved for handoff and retained in the budget.
  expect(Object.keys(owner.getSnapshot()?.overlay ?? {})).toContain(key);
  const handoff = owner.retainedInventoryForTesting().handoffOverlayBodies;
  expect(handoff.length).toBeGreaterThan(0);
  expect(admission.isRetained(handoff[0]!)).toBe(true);
  expect(admission.retainedBytes).toBeGreaterThan(before);

  owner.dispose();
  expect(admission.retainedBytes).toBe(0);
});

// Quality: the admission API charges same-call duplicate identities once and
// keeps a shared identity charged until every owner releases.
it("charges same-call duplicate and cross-owner shared identities once", () => {
  const admission = new EntryWriteAdmission(30);
  const ownerA = {};
  const ownerB = {};
  const key = {};
  expect(
    admission.retain(
      [
        { key, bytes: 10 },
        { key, bytes: 10 },
      ],
      ownerA,
      "writing",
    ),
  ).toBe(true);
  expect(admission.snapshotForTesting().retainedBytes).toBe(10);
  expect(admission.snapshotForTesting().retainedIdentities).toBe(1);
  expect(admission.retain([{ key, bytes: 10 }], ownerB, "writing")).toBe(true);
  expect(admission.snapshotForTesting().retainedBytes).toBe(10);
  admission.release([{ key, bytes: 10 }], ownerA);
  expect(admission.snapshotForTesting().retainedBytes).toBe(10);
  expect(admission.isRetained(key)).toBe(true);
  admission.release([{ key, bytes: 10 }], ownerB);
  expect(admission.snapshotForTesting().retainedBytes).toBe(0);
  expect(admission.isRetained(key)).toBe(false);
});

// Quality: the repository inventory exposes its own stores, independently of the
// admission accounting map, and every retained body is really charged.
it("inventories pending, in-flight, retry and ended stores against the counter", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const { scheduler } = capturingScheduler();
  const admission = new EntryWriteAdmission(4_000);
  const owner = repository(fixture.cache, 1, undefined, scheduler, admission);
  const reader = owner.attachReader();
  owner.acceptRead(read("base"), { lifetime: 1, readId: "read-base", boundary: "boundary-base" });
  await owner.settled();

  const failure = vi
    .spyOn(fixture.cache, "commitBatch")
    .mockResolvedValueOnce({ outcome: "failed", error: new Error("simulated abort") });
  owner.acceptRead(readItems("retained", 2, [item("base", 1), bigItem("retained", 2, 1_000)]), {
    lifetime: 1,
    readId: "read-retained",
    boundary: "boundary-retained",
  });
  await vi.waitFor(() => expect(owner.retainedInventoryForTesting().retryBodies.length).toBeGreaterThan(0));
  const inventory = owner.retainedInventoryForTesting();
  const retainedKeys = new Set(admission.retainedKeysFor(owner));
  for (const body of inventory.retryBodies) expect(retainedKeys.has(body)).toBe(true);
  for (const body of inventory.pendingRows) expect(retainedKeys.has(body)).toBe(true);
  expect(admission.snapshotForTesting().retainedBytes).toBeGreaterThan(0);

  failure.mockRestore();
  owner.invalidate();
  expect(owner.retainedInventoryForTesting().retryBodies).toHaveLength(0);
  expect(owner.retainedInventoryForTesting().pendingRows).toHaveLength(0);
  expect(admission.retainedBytes).toBe(0);
  reader.dispose();
});

// The union of every store the repository really retains, for a whole-object
// cross-check against the admission's charge ledger.
function allRetained(owner: TranscriptRepository): object[] {
  const inventory = owner.retainedInventoryForTesting();
  return [
    ...inventory.pendingRows,
    ...inventory.inFlightBodies,
    ...inventory.retryBodies,
    ...inventory.backgroundAcceptBodies,
    ...inventory.handoffOverlayBodies,
  ];
}

// Task 3f N1: a repository must not overlap itself in the background drain. A
// second no-reader accepted write while this repository's own background batch
// is in flight must not issue a second concurrent batch.
it("does not let one repository overlap its own background drain", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const admission = new EntryWriteAdmission(100_000);
  const owner = repository(fixture.cache, 1, undefined, undefined, admission);
  owner.acceptRead(read("base"), { lifetime: 1, readId: "read-base", boundary: "boundary-base" });
  await owner.settled();

  const gate = deferred<void>();
  const realCommit = fixture.cache.commitBatch.bind(fixture.cache);
  const issued: string[] = [];
  const spy = vi.spyOn(fixture.cache, "commitBatch").mockImplementation(async (batch) => {
    issued.push(batch.items.map((row) => row.transcriptKey).join(","));
    if (batch.items.some((row) => row.transcriptKey === "item-first")) await gate.promise;
    return realCommit(batch);
  });

  // No reader: the first accepted write takes the background slot and stalls.
  owner.acceptRead(read("first", 2), { lifetime: 1, readId: "read-first", boundary: "boundary-first" });
  await vi.waitFor(() => expect(issued.some((keys) => keys.includes("item-first"))).toBe(true));

  // A second accepted write on the SAME repository must wait for the first.
  owner.acceptRead(read("second", 3), { lifetime: 1, readId: "read-second", boundary: "boundary-second" });
  await Promise.resolve();
  await Promise.resolve();
  expect(issued.filter((keys) => keys.includes("item-second"))).toHaveLength(0);

  gate.resolve();
  await vi.waitFor(async () => expect(await keysInCache(fixture.cache)).toContain("item-second"));
  expect(issued.filter((keys) => keys.includes("item-second"))).toHaveLength(1);
  spy.mockRestore();
  owner.dispose();
});

// Task 3f N2: multiple accepted background batches queued behind another
// repository's held slot must all drain with no new input.
it("drains every queued accepted background batch without new input", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const admission = new EntryWriteAdmission(100_000);
  const ownerA = repository(fixture.cache, 1, undefined, undefined, admission);
  const ownerB = repository(fixture.cache, 2, undefined, undefined, admission);
  ownerA.acceptRead(read("baseA"), { lifetime: 1, readId: "read-baseA", boundary: "boundary-baseA" });
  await ownerA.settled();
  ownerB.acceptRead(read("baseB"), { lifetime: 2, readId: "read-baseB", boundary: "boundary-baseB" });
  await ownerB.settled();

  const gate = deferred<void>();
  const realCommit = fixture.cache.commitBatch.bind(fixture.cache);
  const issued: string[] = [];
  const spy = vi.spyOn(fixture.cache, "commitBatch").mockImplementation(async (batch) => {
    issued.push(batch.items.map((row) => row.transcriptKey).join(","));
    if (batch.items.some((row) => row.transcriptKey === "item-a1")) await gate.promise;
    return realCommit(batch);
  });

  // A takes the slot and stalls.
  ownerA.acceptRead(read("a1", 2), { lifetime: 1, readId: "read-a1", boundary: "boundary-a1" });
  await vi.waitFor(() => expect(issued.some((keys) => keys.includes("item-a1"))).toBe(true));

  // B queues two accepted writes behind the held slot, with no reader demand.
  ownerB.acceptRead(read("b1", 2), { lifetime: 2, readId: "read-b1", boundary: "boundary-b1" });
  ownerB.acceptRead(read("b2", 3), { lifetime: 2, readId: "read-b2", boundary: "boundary-b2" });
  await Promise.resolve();
  expect(issued.some((keys) => keys.includes("item-b1"))).toBe(false);

  // Release A with no further input: both queued B batches must drain.
  gate.resolve();
  await vi.waitFor(async () => expect(await keysInCache(fixture.cache)).toContain("item-b2"));
  expect((await keysInCache(fixture.cache)).sort()).toEqual([
    "item-a1",
    "item-b1",
    "item-b2",
    "item-baseA",
    "item-baseB",
  ]);
  spy.mockRestore();
  ownerA.dispose();
  ownerB.dispose();
});

// Task 3f N3: a preserved ended handoff body that does not fit the aggregate is
// still retained by the model, so it must be charged and inventoried honestly
// rather than left invisible.
it("charges and inventories an over-budget ended handoff body through the real model", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const admission = new EntryWriteAdmission(1_500);
  const owner = repository(fixture.cache, 1, undefined, undefined, admission);
  owner.acceptRead(read("base"), { lifetime: 1, readId: "read-base", boundary: "boundary-base" });
  await owner.settled();

  const key = "stream:round-9/1:agentMessage";
  expect(
    owner.accept(
      {
        method: "overlay/upserted",
        params: {
          ref,
          threadId,
          item: {
            key,
            kind: "stream",
            turnId: "turn_1",
            roundId: "round-9",
            streamId: "round-9/1",
            item: {
              type: "agentMessage",
              id: "item-ended",
              turnId: "turn_1",
              roundId: "round-9",
              text: "z".repeat(3_000),
            },
          },
        },
      } as unknown as AnyNotification,
      { lifetime: 1 },
    ),
  ).toBe(true);
  expect(
    owner.accept(
      { method: "overlay/end", params: { ref, threadId, roundId: "round-9" } } as unknown as AnyNotification,
      { lifetime: 1 },
    ),
  ).toBe(true);

  // The body is preserved by the model even though it exceeds the aggregate.
  const overlayItem = owner.getSnapshot()?.overlay?.[key];
  expect(overlayItem).toBeDefined();
  const handoff = owner.retainedInventoryForTesting().handoffOverlayBodies;
  expect(handoff).toContain(overlayItem);
  expect(admission.isRetained(overlayItem as object)).toBe(true);
  expect(allRetained(owner)).toContain(overlayItem);
  // The aggregate honestly reports the over-limit retained body.
  expect(admission.retainedBytes).toBeGreaterThan(admission.limit);

  owner.dispose();
  expect(admission.retainedBytes).toBe(0);
});

// Task 3f B1: invalidation keeps the model, whose display turns still carry the
// ended handoff payload, so the charge and the inventory entry must stay with it
// until disposal. Deleting the overlay key alone is not proof of release.
it("keeps the ended handoff charge while the invalidated model retains its payload", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const admission = new EntryWriteAdmission(8_000);
  const owner = repository(fixture.cache, 1, undefined, undefined, admission);
  owner.acceptRead(read("base"), { lifetime: 1, readId: "read-base", boundary: "boundary-base" });
  await owner.settled();

  const key = "stream:round-9/1:agentMessage";
  const endedText = "z".repeat(3_000);
  owner.accept(
    {
      method: "overlay/upserted",
      params: {
        ref,
        threadId,
        item: {
          key,
          kind: "stream",
          turnId: "turn_1",
          roundId: "round-9",
          streamId: "round-9/1",
          item: {
            type: "agentMessage",
            id: "item-ended",
            turnId: "turn_1",
            roundId: "round-9",
            text: endedText,
          },
        },
      },
    } as unknown as AnyNotification,
    { lifetime: 1 },
  );
  owner.accept({ method: "overlay/end", params: { ref, threadId, roundId: "round-9" } } as unknown as AnyNotification, {
    lifetime: 1,
  });
  expect(owner.getSnapshot()?.overlay?.[key]).toBeDefined();
  expect(admission.retainedBytes).toBeGreaterThan(0);
  // The ended payload is retained in the display projection, not only the overlay.
  expect(JSON.stringify(owner.getSnapshot()?.turns)).toContain(endedText);

  owner.invalidate();
  // The invalidated repository keeps the model and its payload, so its charge and
  // inventory entry stay too: nothing is retained uncharged.
  expect(JSON.stringify(owner.getSnapshot()?.turns)).toContain(endedText);
  expect(owner.retainedInventoryForTesting().handoffOverlayBodies).toHaveLength(1);
  expect(admission.retainedBytes).toBeGreaterThan(0);
  expect(admission.isRetained(owner.getSnapshot()?.overlay?.[key] as object)).toBe(true);

  owner.dispose();
  // Disposal drops the model, so the payload and its charge go together.
  expect(owner.getSnapshot()).toBeUndefined();
  expect(owner.retainedInventoryForTesting().handoffOverlayBodies).toHaveLength(0);
  expect(admission.retainedBytes).toBe(0);
});

// Task 3f B2: bytes retained beyond the bound must still constrain ordinary
// admission, and releasing them restores the room exactly, including after
// unrelated charged bodies are released.
it("counts forced over-limit retention against ordinary admission", () => {
  const admission = new EntryWriteAdmission(200);
  const owner = {};
  const charged = {};
  const forced = {};
  const ordinary = {};
  // An unrelated ordinary body takes part of the budget.
  expect(admission.retain([{ key: charged, bytes: 120 }], owner, "writing")).toBe(true);
  // A forced retention that does not fit is retained and reported over the limit.
  expect(admission.retain([{ key: forced, bytes: 100 }], owner, "ended", true)).toBe(true);
  expect(admission.retainedBytes).toBe(220);
  // An otherwise-fitting ordinary body is refused while the forced bytes stand.
  expect(admission.retain([{ key: ordinary, bytes: 50 }], owner, "writing")).toBe(false);
  expect(admission.isRetained(ordinary)).toBe(false);
  // Releasing the unrelated charged body restores ordinary admission exactly.
  admission.release([{ key: charged, bytes: 120 }], owner);
  expect(admission.retainedBytes).toBe(100);
  expect(admission.retain([{ key: ordinary, bytes: 50 }], owner, "writing")).toBe(true);
  expect(admission.retainedBytes).toBe(150);
  admission.release([{ key: forced, bytes: 100 }], owner);
  admission.release([{ key: ordinary, bytes: 50 }], owner);
  expect(admission.retainedBytes).toBe(0);
});

// Task 3f B2 (repository): an over-limit ended body refuses an ordinary write that
// would otherwise fit, and releasing the ended body restores admission.
it("refuses an otherwise-fitting ordinary write while a forced over-limit ended body stands", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const admission = new EntryWriteAdmission(2_500);
  const owner = repository(fixture.cache, 1, undefined, undefined, admission);
  owner.acceptRead(read("base"), { lifetime: 1, readId: "read-base", boundary: "boundary-base" });
  await owner.settled();

  const key = "stream:round-9/1:agentMessage";
  owner.accept(
    {
      method: "overlay/upserted",
      params: {
        ref,
        threadId,
        item: {
          key,
          kind: "stream",
          turnId: "turn_1",
          roundId: "round-9",
          streamId: "round-9/1",
          item: {
            type: "agentMessage",
            id: "item-ended",
            turnId: "turn_1",
            roundId: "round-9",
            text: "z".repeat(3_000),
          },
        },
      },
    } as unknown as AnyNotification,
    { lifetime: 1 },
  );
  owner.accept({ method: "overlay/end", params: { ref, threadId, roundId: "round-9" } } as unknown as AnyNotification, {
    lifetime: 1,
  });
  expect(admission.retainedBytes).toBeGreaterThan(admission.limit);

  const reader = owner.attachReader();
  // An ordinary recorded suffix that fits the configured limit is refused while
  // the forced ended bytes stand. Its notification leaves the ended overlay in
  // place (a read response would replace it).
  owner.accept(historyNotification("ordinary", 2, 2), { lifetime: 1 });
  await Promise.resolve();
  expect(await keysInCache(fixture.cache)).not.toContain("item-ordinary");
  expect((await reader.openLocal(40)).status).toBe("recovery-needed");

  // Releasing the forced ended body (an overlay reset drops it) restores the room.
  owner.accept(
    { method: "overlay/reset", params: { ref, threadId, streamId: "round-9/1" } } as unknown as AnyNotification,
    { lifetime: 1 },
  );
  expect(admission.retainedBytes).toBe(0);
  owner.acceptRead(readItems("recovered", 3, [item("base", 1), bigItem("recovered", 3, 200)]), {
    lifetime: 1,
    readId: "read-recovered",
    boundary: "boundary-recovered",
  });
  await vi.waitFor(async () => expect(await keysInCache(fixture.cache)).toContain("item-recovered"));
  reader.dispose();
  owner.dispose();
});

// Task 3f B3: the late-predecessor invariant stays reachable under the
// controller's foreground ruling. A held ordinary foreground predecessor never
// takes the background slot, so a no-reader successor issues and is abandoned
// while A is held; A's later successful completion must not clear it.
it("does not clear a failed disposition when a held foreground predecessor commit completes", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const { scheduler } = capturingScheduler();
  const owner = repository(fixture.cache, 1, undefined, scheduler);
  owner.acceptRead(readItems("base", 1, [item("base", 1)]), {
    lifetime: 1,
    readId: "read-base",
    boundary: "boundary-base",
  });
  await owner.settled();
  const reader = owner.attachReader();

  // A is an ordinary foreground read held before storage. It covers snapshot 2
  // only, so its completion cannot carry successor B's later reach.
  const gate = deferred<void>();
  const realCommit = fixture.cache.commitBatch.bind(fixture.cache);
  const commits = vi.spyOn(fixture.cache, "commitBatch").mockImplementation(async (batch) => {
    if (batch.items.some((row) => row.transcriptKey === "item-alpha")) await gate.promise;
    if (batch.items.some((row) => row.transcriptKey === "item-beta")) {
      return { outcome: "failed", error: new Error("simulated abort B") };
    }
    return realCommit(batch);
  });
  owner.acceptRead(readItems("alpha", 2, [item("base", 1), item("alpha", 2)]), {
    lifetime: 1,
    readId: "read-alpha",
    boundary: "boundary-alpha",
  });
  await vi.waitFor(() => expect(commits).toHaveBeenCalledTimes(1));

  // Dispose only the reader; the repository stays live. A never took the
  // background slot, so B issues through the background drain while A is held.
  reader.dispose();
  owner.accept(historyNotification("beta", 3, 3), { lifetime: 1 });
  await vi.waitFor(() => expect(commits).toHaveBeenCalledTimes(2));
  // Event-loop barrier: B's failed-outcome handler has recorded its abandonment.
  await new Promise((resolve) => setTimeout(resolve, 0));

  // Release A: a successful predecessor completion whose coverage stops at
  // snapshot 2. It must not clear B's abandoned reach.
  gate.resolve();
  await vi.waitFor(async () => expect(await keysInCache(fixture.cache)).toContain("item-alpha"));

  await expect(owner.settled()).rejects.toThrow(/failed/);
  expect(await keysInCache(fixture.cache)).not.toContain("item-beta");
  commits.mockRestore();
  owner.dispose();
});

// Task 3f R1 remainder: the sole oversize exemption and the single background
// slot are mutually exclusive. An oversize response must not issue while a
// background batch holds the slot.
it("refuses the oversize exemption while a background batch holds the slot", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const admission = new EntryWriteAdmission(1_500);
  const owner = repository(fixture.cache, 1, undefined, undefined, admission);
  owner.acceptRead(read("base"), { lifetime: 1, readId: "read-base", boundary: "boundary-base" });
  await owner.settled();

  const gate = deferred<void>();
  const realCommit = fixture.cache.commitBatch.bind(fixture.cache);
  const issued: string[] = [];
  const spy = vi.spyOn(fixture.cache, "commitBatch").mockImplementation(async (batch) => {
    issued.push(batch.items.map((row) => row.transcriptKey).join(","));
    if (batch.items.some((row) => row.transcriptKey === "item-holder")) await gate.promise;
    return realCommit(batch);
  });

  // A no-reader ordinary batch takes the background slot and stalls.
  owner.acceptRead(readItems("holder", 2, [item("base", 1), bigItem("holder", 2, 100)]), {
    lifetime: 1,
    readId: "read-holder",
    boundary: "boundary-holder",
  });
  await vi.waitFor(() => expect(issued.some((keys) => keys.includes("item-holder"))).toBe(true));
  expect(admission.snapshotForTesting().backgroundInFlight).toBe(true);
  expect(admission.snapshotForTesting().exemptActive).toBe(false);

  // The oversize response must not issue concurrently; it is refused for caching.
  owner.acceptRead(readItems("huge", 3, [item("base", 1), bigItem("huge", 3, 2_000)]), {
    lifetime: 1,
    readId: "read-huge",
    boundary: "boundary-huge",
  });
  await Promise.resolve();
  expect(issued.some((keys) => keys.includes("item-huge"))).toBe(false);
  expect(await keysInCache(fixture.cache)).not.toContain("item-huge");
  // Round-12 amendment 1 and its extension supersede retained background
  // display: release refused bodies, restoring content through reconciliation.
  expect(
    owner
      .getSnapshot()
      ?.turns.flatMap((turn) => turn.items)
      .map((entry) => entry.transcriptKey),
  ).not.toContain("item-huge");

  gate.resolve();
  await vi.waitFor(async () => expect(await keysInCache(fixture.cache)).toContain("item-holder"));
  spy.mockRestore();
  owner.dispose();
});

// Task 3f R1 remainder, other direction: while the sole oversize exemption is in
// flight, an ordinary no-reader background batch must wait for it.
it("holds an ordinary background batch while the sole oversize exemption is in flight", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const admission = new EntryWriteAdmission(1_500);
  const owner = repository(fixture.cache, 1, undefined, undefined, admission);
  owner.acceptRead(read("base"), { lifetime: 1, readId: "read-base", boundary: "boundary-base" });
  await owner.settled();

  const gate = deferred<void>();
  const realCommit = fixture.cache.commitBatch.bind(fixture.cache);
  const issued: string[] = [];
  const spy = vi.spyOn(fixture.cache, "commitBatch").mockImplementation(async (batch) => {
    issued.push(batch.items.map((row) => row.transcriptKey).join(","));
    if (batch.items.some((row) => row.transcriptKey === "item-huge")) await gate.promise;
    return realCommit(batch);
  });

  // The oversize response takes the sole exemption and stalls.
  owner.acceptRead(readItems("huge", 2, [item("base", 1), bigItem("huge", 2, 2_000)]), {
    lifetime: 1,
    readId: "read-huge",
    boundary: "boundary-huge",
  });
  await vi.waitFor(() => expect(issued.some((keys) => keys.includes("item-huge"))).toBe(true));
  expect(admission.snapshotForTesting().exemptActive).toBe(true);

  // An ordinary no-reader batch must wait for the exemption to release.
  owner.acceptRead(readItems("ordinary", 3, [item("base", 1), bigItem("ordinary", 3, 100)]), {
    lifetime: 1,
    readId: "read-ordinary",
    boundary: "boundary-ordinary",
  });
  await Promise.resolve();
  expect(issued.some((keys) => keys.includes("item-ordinary"))).toBe(false);

  gate.resolve();
  await vi.waitFor(async () => expect(await keysInCache(fixture.cache)).toContain("item-ordinary"));
  spy.mockRestore();
  owner.dispose();
});

// Task 3f quality (a): the inventory traverses the real retained batch objects
// and the actual ended overlay, not the admission's descriptor list, so an
// unregistered closure or ended body cannot hide. Exercises in-flight,
// background-accept, retry, ended and pending stores under one aggregate.
it("inventories every retained store from the real batches and the ended overlay", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const { scheduler } = capturingScheduler();
  const admission = new EntryWriteAdmission(50_000);
  const ownerNoReader = repository(fixture.cache, 1, undefined, scheduler, admission);
  const ownerReader = repository(fixture.cache, 2, undefined, undefined, admission);
  const reader = ownerReader.attachReader();
  ownerNoReader.acceptRead(read("nbase"), { lifetime: 1, readId: "read-nbase", boundary: "boundary-nbase" });
  await ownerNoReader.settled();
  ownerReader.acceptRead(read("rbase"), { lifetime: 2, readId: "read-rbase", boundary: "boundary-rbase" });
  await ownerReader.settled();

  const realCommit = fixture.cache.commitBatch.bind(fixture.cache);
  const batches: Array<Parameters<typeof realCommit>[0]> = [];
  const n1Gate = deferred<void>();
  const r1Gate = deferred<void>();
  const spy = vi.spyOn(fixture.cache, "commitBatch").mockImplementation(async (batch) => {
    batches.push(batch);
    if (batch.items.some((row) => row.transcriptKey === "item-n1")) await n1Gate.promise;
    if (batch.items.some((row) => row.transcriptKey === "item-r1")) await r1Gate.promise;
    if (batch.items.some((row) => row.transcriptKey === "item-rfail")) {
      return { outcome: "failed", error: new Error("simulated abort") };
    }
    return realCommit(batch);
  });

  // No-reader owner: one held background batch and one queued behind it.
  ownerNoReader.acceptRead(readItems("n1", 2, [item("nbase", 1), bigItem("n1", 2, 1_000)]), {
    lifetime: 1,
    readId: "read-n1",
    boundary: "boundary-n1",
  });
  await vi.waitFor(() =>
    expect(batches.some((batch) => batch.items.some((row) => row.transcriptKey === "item-n1"))).toBe(true),
  );
  ownerNoReader.acceptRead(readItems("n2", 3, [item("nbase", 1), bigItem("n2", 3, 1_000)]), {
    lifetime: 1,
    readId: "read-n2",
    boundary: "boundary-n2",
  });

  // Reader owner: one held foreground batch and one failed retry.
  ownerReader.acceptRead(readItems("r1", 3, [item("rbase", 1), bigItem("r1", 3, 1_000)]), {
    lifetime: 2,
    readId: "read-r1",
    boundary: "boundary-r1",
  });
  await vi.waitFor(() =>
    expect(batches.some((batch) => batch.items.some((row) => row.transcriptKey === "item-r1"))).toBe(true),
  );
  ownerReader.acceptRead(readItems("rfail", 4, [item("rbase", 1), bigItem("rfail", 4, 1_000)]), {
    lifetime: 2,
    readId: "read-rfail",
    boundary: "boundary-rfail",
  });
  await vi.waitFor(() => expect(ownerReader.retainedInventoryForTesting().retryBodies.length).toBeGreaterThan(0));

  // Ended overlay on the no-reader owner.
  const overlayKey = "stream:round-9/1:agentMessage";
  ownerNoReader.accept(
    {
      method: "overlay/upserted",
      params: {
        ref,
        threadId,
        item: {
          key: overlayKey,
          kind: "stream",
          turnId: "turn_1",
          roundId: "round-9",
          streamId: "round-9/1",
          item: {
            type: "agentMessage",
            id: "item-ended",
            turnId: "turn_1",
            roundId: "round-9",
            text: "z".repeat(300),
          },
        },
      },
    } as unknown as AnyNotification,
    { lifetime: 1 },
  );
  ownerNoReader.accept(
    { method: "overlay/end", params: { ref, threadId, roundId: "round-9" } } as unknown as AnyNotification,
    { lifetime: 1 },
  );

  const batchFor = (key: string) => batches.find((batch) => batch.items.some((row) => row.transcriptKey === key))!;
  const bodiesOf = (batch: Parameters<typeof realCommit>[0]) => [...batch.items, ...(batch.turns ?? [])];

  // In-flight and background-accept are the real batch objects, not descriptors.
  const inventoryNoReader = ownerNoReader.retainedInventoryForTesting();
  expect(new Set(inventoryNoReader.inFlightBodies)).toEqual(new Set(bodiesOf(batchFor("item-n1"))));
  // The queued accepted batch was never issued, so its real bodies come from the
  // repository's own queue rather than the commit spy; they are still charged.
  expect(inventoryNoReader.backgroundAcceptBodies.length).toBeGreaterThan(0);
  for (const body of inventoryNoReader.backgroundAcceptBodies) {
    expect(admission.retainedKeysFor(ownerNoReader)).toContain(body);
  }
  // The ended store is the actual preserved overlay object.
  expect(inventoryNoReader.handoffOverlayBodies).toContain(ownerNoReader.getSnapshot()?.overlay?.[overlayKey]);

  const inventoryReader = ownerReader.retainedInventoryForTesting();
  expect(new Set(inventoryReader.inFlightBodies)).toEqual(new Set(bodiesOf(batchFor("item-r1"))));
  expect(new Set(inventoryReader.retryBodies)).toEqual(new Set(bodiesOf(batchFor("item-rfail"))));

  // Every real retained object is charged for its owner.
  for (const body of allRetained(ownerNoReader)) expect(admission.retainedKeysFor(ownerNoReader)).toContain(body);
  for (const body of allRetained(ownerReader)) expect(admission.retainedKeysFor(ownerReader)).toContain(body);

  n1Gate.resolve();
  r1Gate.resolve();
  spy.mockRestore();
  reader.dispose();
  ownerNoReader.dispose();
  ownerReader.dispose();
});

// Restored Task 3b behaviour 1 (round 1): distinct retained copies are charged
// separately, shared identity is deduped, and an over-budget reservation is
// atomic.
it("counts distinct retained copies separately and dedupes only shared object identity", () => {
  const admission = new EntryWriteAdmission(30);
  const owner = {};
  const shared = { transcriptKey: "a" };
  // The same object retained from two sites is charged once against the queue.
  expect(admission.retain([{ key: shared, bytes: 10 }], owner, "writing")).toBe(true);
  expect(admission.retain([{ key: shared, bytes: 10 }], owner, "retry")).toBe(true);
  expect(admission.snapshotForTesting().retainedBytes).toBe(10);
  expect(admission.snapshotForTesting().retainedIdentities).toBe(1);
  // A second, independently decoded copy of the same transcript key is a
  // different object and is charged separately.
  const copy = { transcriptKey: "a" };
  expect(admission.retain([{ key: copy, bytes: 10 }], owner, "writing")).toBe(true);
  expect(admission.snapshotForTesting().retainedBytes).toBe(20);
  expect(admission.snapshotForTesting().retainedIdentities).toBe(2);
  // An over-budget reservation is atomic: none of its identities are retained.
  const extra = {};
  expect(admission.retain([{ key: extra, bytes: 20 }], owner, "writing")).toBe(false);
  expect(admission.snapshotForTesting().retainedBytes).toBe(20);
  expect(admission.isRetained(extra)).toBe(false);
  expect(admission.isRetained(shared)).toBe(true);
});

// Restored Task 3b behaviours 1 and 2 (round 1): the repository admits
// queued/in-writing work against the shared aggregate, and an overflow drops the
// affected cache work, keeps the row displayed, breaks readiness honestly and
// schedules a paced reconciliation.
it("drops over-aggregate entry work while keeping the referenced bodies and the reader honest", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const { scheduled, scheduler } = capturingScheduler();
  const admission = new EntryWriteAdmission(3_000);
  const owner = repository(fixture.cache, 1, undefined, scheduler, admission);
  owner.acceptRead(read("base"), { lifetime: 1, readId: "read-base", boundary: "boundary-base" });
  await owner.settled();
  expect(admission.retainedBytes).toBe(0);

  const reader = owner.attachReader();
  // Hold the first large write in flight so its bodies are genuinely retained,
  // and keep each batch below the limit so the second is ordinary overflow, not
  // the oversize exception.
  const realCommit = fixture.cache.commitBatch.bind(fixture.cache);
  const gate = deferred<void>();
  const heldWrite = vi.spyOn(fixture.cache, "commitBatch").mockImplementationOnce(async (batch) => {
    await gate.promise;
    return realCommit(batch);
  });
  owner.acceptRead(readItems("first", 2, [item("base", 1), bigItem("first", 2, 2_000)]), {
    lifetime: 1,
    readId: "read-first",
    boundary: "boundary-first",
  });
  await vi.waitFor(() => expect(admission.retainedBytes).toBeGreaterThan(0));
  const heldKeys = admission.retainedKeysFor(owner);
  expect(heldKeys.length).toBeGreaterThan(0);
  const retainedBefore = admission.snapshotForTesting().retainedBytes;
  expect(retainedBefore).toBeLessThanOrEqual(3_000);
  expect(admission.snapshotForTesting().exemptBytes).toBe(0);

  // A second read cannot fit the remaining aggregate.
  expect(
    owner.acceptRead(readItems("second", 3, [item("base", 1), bigItem("second", 3, 1_000)]), {
      lifetime: 1,
      readId: "read-second",
      boundary: "boundary-second",
    }),
  ).toBe(true);
  await Promise.resolve();

  // Counter bound: the aggregate never exceeds its limit.
  expect(admission.retainedBytes).toBeLessThanOrEqual(3_000);
  // Real references: the held bodies survive by object identity, and the dropped
  // batch retained none of its own.
  const heldAfter = admission.retainedKeysFor(owner);
  for (const key of heldKeys) expect(heldAfter).toContain(key);
  expect(heldAfter.length).toBe(heldKeys.length);
  // The dropped work is not durably cached, but it is still displayed.
  expect(await keysInCache(fixture.cache)).not.toContain("item-second");
  expect(
    reader
      .getSnapshot()
      ?.turns.flatMap((turn) => turn.items)
      .map((entry) => entry.transcriptKey),
  ).toContain("item-second");
  // Readiness is honestly broken and one paced reconciliation is scheduled.
  expect((await reader.openLocal(40)).status).toBe("recovery-needed");
  expect(scheduled.length).toBeGreaterThanOrEqual(1);

  // The held predecessor still commits normally.
  gate.resolve();
  await vi.waitFor(() => expect(admission.retainedBytes).toBe(0));
  expect(await keysInCache(fixture.cache)).toContain("item-first");
  heldWrite.mockRestore();
  reader.dispose();
  owner.dispose();
});

// Restored Task 3b behaviour 2 (round 1): a single response above the queue
// capacity is the sole immediate bounded batch, accounted separately and never
// staged for retry.
it("admits one immediate bounded batch above the queue capacity without staging it", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const { scheduled, scheduler } = capturingScheduler();
  const admission = new EntryWriteAdmission(500);
  const owner = repository(fixture.cache, 1, undefined, scheduler, admission);
  owner.acceptRead(read("base"), { lifetime: 1, readId: "read-base", boundary: "boundary-base" });
  await owner.settled();
  expect(admission.retainedBytes).toBe(0);

  const realCommit = fixture.cache.commitBatch.bind(fixture.cache);
  const gate = deferred<void>();
  const heldWrite = vi.spyOn(fixture.cache, "commitBatch").mockImplementationOnce(async (batch) => {
    await gate.promise;
    return realCommit(batch);
  });
  owner.acceptRead(readItems("huge", 2, [item("base", 1), bigItem("huge", 2, 2_000)]), {
    lifetime: 1,
    readId: "read-huge",
    boundary: "boundary-huge",
  });
  // The oversize response was committed immediately, not dropped...
  expect(heldWrite).toHaveBeenCalled();
  // ...accounted separately from the 4 MiB-class aggregate...
  expect(admission.retainedBytes).toBe(0);
  expect(admission.snapshotForTesting().exemptBytes).toBeGreaterThan(0);
  expect(admission.snapshotForTesting().exemptActive).toBe(true);
  gate.resolve();
  await owner.settled();
  expect(await keysInCache(fixture.cache)).toContain("item-huge");
  // ...and never staged as a retry.
  expect(scheduled).toHaveLength(0);
  heldWrite.mockRestore();
  owner.dispose();
});

// Restored Task 3b behaviour 3 (round 1, fairness): among repositories waiting
// for the single background slot, one with live reader demand is served before
// one without.
it("serves a foreground waiter before a background waiter", () => {
  const admission = new EntryWriteAdmission(100);
  const holder = {};
  const foreground = {};
  const background = {};
  const resumed: string[] = [];
  expect(
    admission.tryAcquireBackground(
      holder,
      () => false,
      () => {},
    ),
  ).toBe(true);
  const acquireBackground = () => {
    if (admission.tryAcquireBackground(background, () => false, acquireBackground)) resumed.push("background");
  };
  const acquireForeground = () => {
    if (admission.tryAcquireBackground(foreground, () => true, acquireForeground)) resumed.push("foreground");
  };
  expect(admission.tryAcquireBackground(background, () => false, acquireBackground)).toBe(false);
  expect(admission.tryAcquireBackground(foreground, () => true, acquireForeground)).toBe(false);
  admission.releaseBackground(holder);
  expect(resumed).toEqual(["foreground"]);
  admission.releaseBackground(foreground);
  expect(resumed).toEqual(["foreground", "background"]);
  expect(admission.snapshotForTesting().backgroundInFlight).toBe(true);
});

// Restored and strengthened Task 3b behaviour 4 (round 1): an overflow does not
// start a repair loop. The paced callback is actually fired, repeated opens and
// unchanged deltas arm nothing further, and a later overflow re-arms exactly one
// more reconciliation.
it("does not start a repair loop for repeated opens, unchanged deltas or a re-overflow", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const { scheduled, scheduler } = capturingScheduler();
  let reconciles = 0;
  const admission = new EntryWriteAdmission(3_000);
  const owner = repository(fixture.cache, 1, undefined, scheduler, admission, () => (reconciles += 1));
  owner.acceptRead(read("base"), { lifetime: 1, readId: "read-base", boundary: "boundary-base" });
  await owner.settled();
  const reader = owner.attachReader();

  // Hold a first large write so its bodies occupy the aggregate; the second read
  // is then ordinary overflow (not the oversize exception).
  const held = deferred<EntryWriteOutcome>();
  const heldWrite = vi.spyOn(fixture.cache, "commitBatch").mockImplementationOnce(() => held.promise);
  owner.acceptRead(readItems("occupier", 2, [item("base", 1), bigItem("occupier", 2, 2_000)]), {
    lifetime: 1,
    readId: "read-occupier",
    boundary: "boundary-occupier",
  });
  await vi.waitFor(() => expect(admission.retainedBytes).toBeGreaterThan(0));
  owner.acceptRead(readItems("overflow", 3, [item("base", 1), bigItem("overflow", 3, 1_000)]), {
    lifetime: 1,
    readId: "read-overflow",
    boundary: "boundary-overflow",
  });
  await Promise.resolve();
  const armedAfterOverflow = scheduled.length;
  expect(armedAfterOverflow).toBeGreaterThanOrEqual(1);

  // Firing the paced callback makes one real reconciliation request and does not
  // re-arm itself.
  scheduled.at(-1)!.fire();
  expect(reconciles).toBe(1);
  expect(scheduled.length).toBe(armedAfterOverflow);

  // Repeated opens must not arm further reconciliation.
  await reader.openLocal(40);
  await reader.openLocal(40);
  await Promise.resolve();
  expect(scheduled.length).toBe(armedAfterOverflow);

  // An unchanged live notification with no recorded input arms nothing.
  owner.accept(
    {
      method: "history/updated",
      params: {
        ref,
        threadId,
        bootGeneration: "1",
        epoch: 0,
        snapshot: { incarnation: identity.incarnation, length: 2 },
        items: [],
      },
    } as unknown as AnyNotification,
    { lifetime: 1 },
  );
  await Promise.resolve();
  expect(scheduled.length).toBe(armedAfterOverflow);

  // Feedback: a recovery read that overflows again arms exactly one more
  // reconciliation, not a spiral.
  owner.acceptRead(readItems("overflow2", 4, [item("base", 1), bigItem("overflow2", 4, 1_000)]), {
    lifetime: 1,
    readId: "read-overflow2",
    boundary: "boundary-overflow2",
  });
  await Promise.resolve();
  expect(scheduled.length).toBe(armedAfterOverflow + 1);

  // Fire the feedback callback, then feed the SAME rejected authoritative content
  // back under unchanged capacity. A reconciliation already requested for this
  // reach is not requested again, so repeated callback/read feedback does not
  // keep scheduling recovery.
  scheduled.at(-1)!.fire();
  expect(reconciles).toBe(2);
  const armedAfterFeedback = scheduled.length;
  owner.acceptRead(readItems("overflow2", 4, [item("base", 1), bigItem("overflow2", 4, 1_000)]), {
    lifetime: 1,
    readId: "read-overflow2-again",
    boundary: "boundary-overflow2-again",
  });
  await Promise.resolve();
  owner.acceptRead(readItems("overflow2", 4, [item("base", 1), bigItem("overflow2", 4, 1_000)]), {
    lifetime: 1,
    readId: "read-overflow2-again2",
    boundary: "boundary-overflow2-again2",
  });
  await Promise.resolve();
  expect(scheduled.length).toBe(armedAfterFeedback);

  // Release the held occupier so the recovery read can commit and settle.
  held.resolve({ outcome: "committed", coverage: "extended", encodedBytes: 0 });
  heldWrite.mockRestore();
  await vi.waitFor(() => expect(admission.retainedBytes).toBe(0));

  // Changed eligible content is reconsidered and restores readiness.
  owner.acceptRead(readItems("changed", 5, [item("base", 1), item("changed", 5)]), {
    lifetime: 1,
    readId: "read-changed",
    boundary: "boundary-changed",
  });
  await owner.settled();
  expect(await keysInCache(fixture.cache)).toContain("item-changed");
  const opened = await reader.openLocal(40);
  expect(opened.status).toBe("current");
  reader.dispose();
  owner.dispose();
});

// Task 3f follow-up: a position identifies a slot, not a version. An older
// snapshot committed at the same position must not clear a newer abandoned
// version's disposition, and the durable row must stay the newer version absent.
it("does not clear a failed disposition when an older version commits at the same position", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const { scheduler } = capturingScheduler();
  const owner = repository(fixture.cache, 1, undefined, scheduler);
  const v1 = { ...item("beta", 1, 1), text: "beta-v1" } as ThreadItem;
  const v3 = { ...item("beta", 1, 3), text: "beta-v3" } as ThreadItem;
  const v2 = { ...item("beta", 1, 2), text: "beta-v2" } as ThreadItem;

  // Version 1 is durably committed at position P.
  owner.acceptRead(readItems("beta", 1, [v1]), { lifetime: 1, readId: "read-v1", boundary: "boundary-v1" });
  await owner.settled();
  expect(
    (await fixture.cache.getLatest(historyKey, 40)).find((row) => row.transcriptKey === "item-beta")?.version,
  ).toBe(1);

  // A recorded snapshot-3 version-3 update at the same position fails with no
  // reader demand, so it is abandoned.
  const failure = vi
    .spyOn(fixture.cache, "commitBatch")
    .mockResolvedValueOnce({ outcome: "failed", error: new Error("simulated abort") });
  owner.accept(
    {
      method: "history/updated",
      params: {
        ref,
        threadId,
        bootGeneration: "1",
        epoch: 0,
        snapshot: { incarnation: identity.incarnation, length: 3 },
        items: [v3],
      },
    } as unknown as AnyNotification,
    { lifetime: 1 },
  );
  await expect(owner.settled()).rejects.toThrow(/failed/);
  failure.mockRestore();
  expect(
    owner
      .getSnapshot()
      ?.history?.turns.flatMap((turn) => turn.items)
      .find((entry) => entry.transcriptKey === "item-beta")?.version,
  ).toBe(3);

  // An ordinary snapshot-2 read writes version 2 at the same position and
  // commits extended. It is older than the abandoned version at the same slot.
  const older = readItems("beta", 2, [v2]);
  older.authoritative = true;
  expect(owner.acceptRead(older, { lifetime: 1, readId: "read-v2", boundary: "boundary-v2" })).toBe(true);
  await vi.waitFor(async () =>
    expect((await fixture.cache.getLatest(historyKey, 40)).some((row) => row.transcriptKey === "item-beta")).toBe(true),
  );

  // The newer version's disposition stands: the older commit did not repair it,
  // and the durable observation is the row's version and payload, not key presence.
  await expect(owner.settled()).rejects.toThrow(/failed/);
  const durable = (await fixture.cache.getLatest(historyKey, 40)).find((row) => row.transcriptKey === "item-beta");
  expect(durable?.version).toBe(2);
  expect(JSON.stringify(durable?.item)).toContain("beta-v2");
  expect(JSON.stringify(durable?.item)).not.toContain("beta-v3");
  owner.dispose();
});

// Task 3h: reachWithin must compare the snapshot too. A newer abandoned
// obligation at the same positions is not answered by an earlier reconciliation
// request, so it arms exactly one fresh request; identical repeats are still
// suppressed.
it("arms a fresh reconciliation for a newer snapshot at the same positions and suppresses identical repeats", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const { scheduled, scheduler } = capturingScheduler();
  let reconciles = 0;
  const admission = new EntryWriteAdmission(3_000);
  const owner = repository(fixture.cache, 1, undefined, scheduler, admission, () => (reconciles += 1));
  owner.acceptRead(read("base"), { lifetime: 1, readId: "read-base", boundary: "boundary-base" });
  await owner.settled();
  const reader = owner.attachReader();

  // Occupy the aggregate with an unrelated held write so the reads below overflow.
  const held = deferred<EntryWriteOutcome>();
  const heldWrite = vi.spyOn(fixture.cache, "commitBatch").mockImplementationOnce(() => held.promise);
  owner.acceptRead(readItems("occupier", 2, [item("base", 1), bigItem("occupier", 2, 2_000)]), {
    lifetime: 1,
    readId: "read-occupier",
    boundary: "boundary-occupier",
  });
  await vi.waitFor(() => expect(admission.retainedBytes).toBeGreaterThan(0));

  // Version 2 at position P, snapshot 2: overflows and arms a reconciliation.
  const betaV2 = { ...bigItem("beta", 2, 1_000), version: 2 } as ThreadItem;
  owner.acceptRead(readItems("beta", 2, [betaV2]), { lifetime: 1, readId: "read-v2", boundary: "boundary-v2" });
  await Promise.resolve();
  const armedAfterV2 = scheduled.length;
  expect(armedAfterV2).toBeGreaterThanOrEqual(1);

  // Fire it: the request now remembers position P at snapshot 2.
  scheduled.at(-1)!.fire();
  expect(reconciles).toBe(1);

  // Version 3 at the SAME position P, snapshot 3: overflows again and raises the
  // abandonment snapshot, so it must arm exactly one fresh request.
  const betaV3 = { ...bigItem("beta", 2, 1_000), version: 3 } as ThreadItem;
  owner.acceptRead(readItems("beta", 3, [betaV3]), { lifetime: 1, readId: "read-v3", boundary: "boundary-v3" });
  await Promise.resolve();
  expect(scheduled.length).toBe(armedAfterV2 + 1);

  // Fire the fresh request, then re-feed the identical v3 content twice: an
  // identical repeat is suppressed and arms nothing further.
  scheduled.at(-1)!.fire();
  expect(reconciles).toBe(2);
  const armedAfterV3 = scheduled.length;
  owner.acceptRead(readItems("beta", 3, [betaV3]), {
    lifetime: 1,
    readId: "read-v3-again",
    boundary: "boundary-v3-again",
  });
  await Promise.resolve();
  owner.acceptRead(readItems("beta", 3, [betaV3]), {
    lifetime: 1,
    readId: "read-v3-again2",
    boundary: "boundary-v3-again2",
  });
  await Promise.resolve();
  expect(scheduled.length).toBe(armedAfterV3);

  held.resolve({ outcome: "committed", coverage: "extended", encodedBytes: 0 });
  heldWrite.mockRestore();
  reader.dispose();
  owner.dispose();
});

it("recovers durable readiness after a foreground predecessor abort blocks its committed successor", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const { scheduled, scheduler } = capturingScheduler();
  const reconcile = vi.fn(() => {
    owner.acceptRead(readItems("repaired", 3, [item("base", 1), item("alpha", 2), item("beta", 3)]), {
      lifetime: 1,
      readId: "read-repaired",
      boundary: "boundary-repaired",
    });
  });
  const owner = repository(fixture.cache, 1, undefined, scheduler, undefined, reconcile);
  const reader = owner.attachReader();
  owner.acceptRead(read("base"), { lifetime: 1, readId: "read-base", boundary: "boundary-base" });
  await owner.settled();
  const held = deferred<EntryWriteOutcome>();
  const realCommit = fixture.cache.commitBatch.bind(fixture.cache);
  const successorCommitted = deferred<void>();
  const commit = vi
    .spyOn(fixture.cache, "commitBatch")
    .mockImplementation(async (batch) => {
      const outcome = await realCommit(batch);
      if (batch.items.some((row) => row.transcriptKey === "item-beta")) successorCommitted.resolve();
      return outcome;
    })
    .mockImplementationOnce(() => held.promise);
  try {
    owner.accept(historyNotification("alpha", 2, 2), { lifetime: 1 });
    owner.accept(historyNotification("beta", 3, 3), { lifetime: 1 });
    await successorCommitted.promise;
    expect((await fixture.cache.getCoverage(historyKey, "read:read-base"))?.suffixBlocked).toBe(true);
    held.resolve({ outcome: "failed", error: new Error("foreground A abort") });
    await owner.settled().catch(() => undefined);
    expect((await reader.openLocal(40)).status).toBe("recovery-needed");
    // Exercise all existing paced work, including the owner's authoritative
    // reconciliation response. No fresh input is supplied outside that callback.
    let fired = 0;
    while (fired < scheduled.length && fired < 5) {
      scheduled[fired++]!.fire();
      await owner.settled().catch(() => undefined);
    }
    expect((await fixture.cache.getCoverage(historyKey, "read:read-repaired"))?.representedSnapshot).toBe(3);
    expect(owner.getRecoveryStateForTesting()).toMatchObject({ recoveryNeeded: false, cacheContinuityLost: false });
    expect((await reader.openLocal(40)).status).toBe("current");
    expect(
      reader
        .getSnapshot()
        ?.turns.flatMap((turn) => turn.items)
        .map((item) => item.text),
    ).toEqual(["base", "alpha", "beta"]);
    expect((await keysInCache(fixture.cache)).sort()).toEqual(["item-alpha", "item-base", "item-beta"]);
    expect(reconcile).toHaveBeenCalledTimes(1);
    await owner.settled();
  } finally {
    held.resolve({ outcome: "failed", error: new Error("cleanup") });
    commit.mockRestore();
    reader.dispose();
    owner.dispose();
  }
});

it("requests bounded reconciliation for committed not-extended suffix coverage", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const { scheduled, scheduler } = capturingScheduler();
  const reconcile = vi.fn();
  const owner = repository(fixture.cache, 1, undefined, scheduler, undefined, reconcile);
  const reader = owner.attachReader();
  owner.acceptRead(read("base"), { lifetime: 1, readId: "read-base", boundary: "boundary-base" });
  await owner.settled();
  const realCommit = fixture.cache.commitBatch.bind(fixture.cache);
  // Exercise the real cache's mismatch/block outcome, not a fabricated success.
  const commit = vi.spyOn(fixture.cache, "commitBatch").mockImplementationOnce((batch) =>
    realCommit({
      ...batch,
      coverage:
        batch.coverage?.kind === "suffix"
          ? { ...batch.coverage, previousBoundary: "missing-predecessor" }
          : batch.coverage,
    }),
  );
  try {
    owner.accept(historyNotification("alpha", 2, 2), { lifetime: 1 });
    await owner.settled().catch(() => undefined);
    expect((await fixture.cache.getCoverage(historyKey, "read:read-base"))?.suffixBlocked).toBe(true);
    expect(
      reader
        .getSnapshot()
        ?.turns.flatMap((turn) => turn.items)
        .map((item) => item.text),
    ).toEqual(["base", "alpha"]);
    expect(scheduled).toHaveLength(1);
    expect((await reader.openLocal(40)).status).toBe("recovery-needed");
    scheduled[0]!.fire();
    expect(reconcile).toHaveBeenCalledTimes(1);
    await expect(owner.settled()).rejects.toThrow();
    expect(scheduled).toHaveLength(1);
    owner.acceptRead(readItems("repaired", 2, [item("base", 1), item("alpha", 2)]), {
      lifetime: 1,
      readId: "read-repaired",
      boundary: "boundary-repaired",
    });
    await owner.settled();
    expect((await fixture.cache.getCoverage(historyKey, "read:read-repaired"))?.representedSnapshot).toBe(2);
    expect((await reader.openLocal(40)).status).toBe("current");
    expect(scheduled).toHaveLength(1);
  } finally {
    commit.mockRestore();
    reader.dispose();
    owner.dispose();
  }
});
