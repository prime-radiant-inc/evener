import type { AnyNotification, ThreadItem, ThreadModel, ThreadReadResponse } from "@evener/appwire-client";
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

function repository(
  cache: ReturnType<typeof createEntryCacheFixture>["cache"],
  lifetime = 1,
  seedModel?: ThreadModel,
  retryScheduler?: (attempt: number, retry: () => void) => () => void,
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
  });
}

function capturingScheduler() {
  const scheduled: Array<{ attempt: number; fire: () => void }> = [];
  const captures: Array<[number, () => void]> = [];
  const scheduler = (attempt: number, retry: () => void): (() => void) => {
    captures.push([attempt, retry]);
    scheduled.push({ attempt, fire: retry });
    return () => {};
  };
  return { scheduled, captures, scheduler };
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
  timeoutSpy.mockRestore();
  owner.dispose();
});
