import type {
  AnyNotification,
  ThreadItem,
  ThreadModel,
  ThreadReadResponse,
  ThreadTurnsListResponse,
} from "@evener/appwire-client";
import { afterEach, expect, it, vi } from "vitest";
import { EntryWriteAdmission } from "./entryWriteAdmission";
import { entryHistoryKey, type TranscriptResolvedIdentity } from "./sessionCacheSchema";
import type { EntryWriteOutcome } from "./sessionEntryCache";
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

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

async function keysInCache(cache: ReturnType<typeof createEntryCacheFixture>["cache"]): Promise<string[]> {
  return (await cache.getLatest(historyKey, 40)).map((row) => row.transcriptKey);
}

afterEach(async () => {
  for (const fixture of fixtures.splice(0)) await fixture.dispose();
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
  ).toEqual(["item-before", "item-dropped"]);
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
  ).toEqual(["item-base", "item-alpha", "item-beta"]);
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
  ).toEqual(["item-base", "item-oversize"]);
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

  // B, chained after A, fails through the ordinary path with no reader demand and
  // is never admitted to the queue. This records "failed".
  owner.accept(historyNotification("beta", 3, 3), { lifetime: 1 });
  await vi.waitFor(() => expect(commits).toHaveBeenCalledTimes(2));
  // Event-loop barrier: B's failed-outcome handler has run by now.
  await new Promise((resolve) => setTimeout(resolve, 0));

  // Release A. Its real commit extends R through A only (snapshot 2).
  gate.resolve();
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

// Task 3b behaviour 1: completed entry work counts against one aggregate, and
// the bound is observed as real retained body references, not only a counter.
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

// Task 3b behaviours 1 and 2: the repository admits queued/in-writing work
// against the shared aggregate, and an overflow drops the affected cache work,
// breaks readiness honestly, schedules one paced reconciliation, and leaves the
// reader usable.
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
  const heldKeys = owner.retainedWorkForTesting();
  expect(heldKeys.length).toBeGreaterThan(0);
  const retainedBefore = admission.snapshotForTesting().retainedBytes;
  expect(retainedBefore).toBeLessThanOrEqual(3_000);
  expect(owner.getAdmissionSnapshotForTesting().exemptIdentities).toBe(0);

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
  // Real references: the held bodies survive by object identity, and the
  // dropped batch retained none of its own.
  const heldAfter = owner.retainedWorkForTesting();
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

// Task 3b behaviour 2 (the permitted exception): a single response above the
// queue capacity is the sole immediate bounded batch, accounted separately and
// never staged for retry.
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
  expect(admission.snapshotForTesting().exemptIdentities).toBeGreaterThan(0);
  gate.resolve();
  await owner.settled();
  expect(await keysInCache(fixture.cache)).toContain("item-huge");
  // ...and never staged as a retry.
  expect(scheduled).toHaveLength(0);
  heldWrite.mockRestore();
  owner.dispose();
});

// Task 3b behaviour 3: at most one retried database batch is in flight across
// entry repositories; a stalled repository holds only the background slot, so
// unrelated healthy foreground work is not blocked.
it("serializes the retry drain across repositories without blocking healthy work", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const admission = new EntryWriteAdmission(100_000);
  const schedA = capturingScheduler();
  const schedB = capturingScheduler();
  const ownerA = repository(fixture.cache, 1, undefined, schedA.scheduler, admission);
  const ownerB = repository(fixture.cache, 2, undefined, schedB.scheduler, admission);
  ownerA.acceptRead(read("baseA"), { lifetime: 1, readId: "read-baseA", boundary: "boundary-baseA" });
  await ownerA.settled();
  ownerB.acceptRead(read("baseB"), { lifetime: 2, readId: "read-baseB", boundary: "boundary-baseB" });
  await ownerB.settled();
  const readerA = ownerA.attachReader();
  const readerB = ownerB.attachReader();

  // Both repositories fail one write, each queuing a retry.
  const failure = vi
    .spyOn(fixture.cache, "commitBatch")
    .mockResolvedValue({ outcome: "failed", error: new Error("simulated abort") });
  ownerA.acceptRead(read("aFail", 2), { lifetime: 1, readId: "read-aFail", boundary: "boundary-aFail" });
  ownerB.acceptRead(read("bFail", 2), { lifetime: 2, readId: "read-bFail", boundary: "boundary-bFail" });
  await vi.waitFor(() => expect(schedA.scheduled).toHaveLength(1));
  await vi.waitFor(() => expect(schedB.scheduled).toHaveLength(1));
  failure.mockRestore();

  // A's retry takes the shared slot and stalls there.
  const realCommit = fixture.cache.commitBatch.bind(fixture.cache);
  const held = deferred<EntryWriteOutcome>();
  const gate = vi.spyOn(fixture.cache, "commitBatch").mockImplementation(async (batch) => {
    if (batch.items.some((row) => row.transcriptKey === "item-aFail")) return held.promise;
    return realCommit(batch);
  });
  schedA.scheduled[0]!.fire();
  await vi.waitFor(() => expect(admission.snapshotForTesting().backgroundInFlight).toBe(true));

  // B's retry must wait: still exactly one retried batch in flight.
  schedB.scheduled[0]!.fire();
  await Promise.resolve();
  expect(await keysInCache(fixture.cache)).not.toContain("item-bFail");

  // Ordinary healthy work in B is not blocked by A's stalled retry.
  ownerB.acceptRead(read("bHealthy", 5), { lifetime: 2, readId: "read-bHealthy", boundary: "boundary-bHealthy" });
  await vi.waitFor(async () => expect(await keysInCache(fixture.cache)).toContain("item-bHealthy"));
  expect(await keysInCache(fixture.cache)).not.toContain("item-aFail");

  // Releasing A hands the slot to B, whose retry then commits.
  held.resolve({ outcome: "committed", coverage: "extended", encodedBytes: 0 });
  await vi.waitFor(async () => expect(await keysInCache(fixture.cache)).toContain("item-bFail"));
  gate.mockRestore();
  readerA.dispose();
  readerB.dispose();
  ownerA.dispose();
  ownerB.dispose();
});

// Task 3b behaviour 3 (fairness): among repositories waiting for the single
// background slot, one with live reader demand is served before one without.
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
      () => resumed.push("holder"),
    ),
  ).toBe(true);
  expect(
    admission.tryAcquireBackground(
      background,
      () => false,
      () => resumed.push("background"),
    ),
  ).toBe(false);
  expect(
    admission.tryAcquireBackground(
      foreground,
      () => true,
      () => resumed.push("foreground"),
    ),
  ).toBe(false);
  admission.releaseBackground(holder);
  expect(resumed).toEqual(["foreground"]);
  admission.releaseBackground(foreground);
  expect(resumed).toEqual(["foreground", "background"]);
  expect(
    admission.tryAcquireBackground(
      background,
      () => false,
      () => {},
    ),
  ).toBe(true);
});

// Task 3b behaviour 4: an overflow does not start a repair loop. Repeated opens
// and unchanged live deltas arm nothing further; changed eligible content is
// reconsidered and restores readiness.
it("does not start a repair loop for repeated opens or unchanged live deltas", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const { scheduled, scheduler } = capturingScheduler();
  const admission = new EntryWriteAdmission(3_000);
  const owner = repository(fixture.cache, 1, undefined, scheduler, admission);
  owner.acceptRead(read("base"), { lifetime: 1, readId: "read-base", boundary: "boundary-base" });
  await owner.settled();
  const reader = owner.attachReader();

  // Hold a first large write so its bodies occupy the aggregate; the second
  // read is then ordinary overflow (not the oversize exception).
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

  // Release the held occupier so the recovery read can commit and settle.
  held.resolve({ outcome: "committed", coverage: "extended", encodedBytes: 0 });
  heldWrite.mockRestore();
  await vi.waitFor(() => expect(admission.retainedBytes).toBe(0));

  // Changed eligible content is reconsidered and restores readiness.
  owner.acceptRead(readItems("changed", 3, [item("base", 1), item("changed", 3)]), {
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
