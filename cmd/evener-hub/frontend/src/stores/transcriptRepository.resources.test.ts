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
            } as ThreadItem,
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
  const owner = repository(fixture.cache);
  owner.acceptRead(read("before"), { lifetime: 1, readId: "read-before", boundary: "boundary-before" });
  await owner.settled();
  // A retained reader keeps the accepted bodies (a purely background model is
  // pruned after its writes settle), so the display half is observable.
  const reader = owner.attachReader();

  // A single transient failure: the real adapter's transaction aborts.
  const failure = vi
    .spyOn(fixture.cache, "commitBatch")
    .mockResolvedValueOnce({ outcome: "failed", error: new Error("transaction aborted") });
  expect(
    owner.acceptRead(read("dropped", 2), { lifetime: 1, readId: "read-dropped", boundary: "boundary-dropped" }),
  ).toBe(true);
  await owner.settled();
  failure.mockRestore();

  // The failure must not erase accepted content.
  expect(
    reader
      .getSnapshot()
      ?.turns.flatMap((turn) => turn.items)
      .map((item) => item.transcriptKey),
  ).toEqual(["item-before", "item-dropped"]);

  // Demand continues: a later accepted read commits successfully.
  owner.acceptRead(read("recovered", 3), { lifetime: 1, readId: "read-recovered", boundary: "boundary-recovered" });
  await owner.settled();

  const opened = await reader.openLocal(40);
  expect(opened.model?.turns.flatMap((turn) => turn.items).map((item) => item.text)).toEqual([
    "before",
    "dropped",
    "recovered",
  ]);
  expect(opened.status).toBe("current");
  reader.dispose();
  owner.dispose();
});

// Behaviour 4 (retry): a transient write fault is retried automatically on a
// bounded backoff while demand exists. A successful probe is not enough: the
// retried batch must really commit and the route must recover.
it("retries a transient write failure on bounded backoff while a reader holds demand", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const scheduled: Array<{ attempt: number; fire: () => void }> = [];
  const owner = repository(fixture.cache, 1, undefined, (attempt, retry) => {
    scheduled.push({ attempt, fire: retry });
    return () => {};
  });
  owner.acceptRead(read("before"), { lifetime: 1, readId: "read-before", boundary: "boundary-before" });
  await owner.settled();
  const reader = owner.attachReader();

  const failure = vi
    .spyOn(fixture.cache, "commitBatch")
    .mockResolvedValueOnce({ outcome: "failed", error: new Error("transaction aborted") });
  owner.acceptRead(read("retried", 2), { lifetime: 1, readId: "read-retried", boundary: "boundary-retried" });
  await owner.settled();
  failure.mockRestore();

  // The failure armed exactly one bounded-backoff retry.
  expect(scheduled).toHaveLength(1);
  expect(scheduled[0]!.attempt).toBeGreaterThanOrEqual(1);
  // Display keeps the accepted content while the write is unsettled.
  expect(
    reader
      .getSnapshot()
      ?.turns.flatMap((turn) => turn.items)
      .map((item) => item.transcriptKey),
  ).toEqual(["item-before", "item-retried"]);

  // Drive the armed retry; it re-commits the same batch for real.
  scheduled[0]!.fire();
  await owner.settled();

  const opened = await reader.openLocal(40);
  expect(opened.status).toBe("current");
  expect(opened.model?.turns.flatMap((turn) => turn.items).map((item) => item.text)).toEqual(["before", "retried"]);
  // A successful commit resets the streak and leaves no further retry armed.
  expect(scheduled).toHaveLength(1);
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
  const scheduled: Array<{ attempt: number; fire: () => void }> = [];
  const owner = repository(fixture.cache, 1, undefined, (attempt, retry) => {
    scheduled.push({ attempt, fire: retry });
    return () => {};
  });
  owner.acceptRead(read("base"), { lifetime: 1, readId: "read-base", boundary: "boundary-base" });
  await owner.settled();
  const reader = owner.attachReader();

  const refused = vi
    .spyOn(fixture.cache, "commitBatch")
    .mockResolvedValueOnce({ outcome: "refused", reason: "capacity" });
  owner.acceptRead(read("oversize", 2), { lifetime: 1, readId: "read-oversize", boundary: "boundary-oversize" });
  await owner.settled();
  refused.mockRestore();

  // No transient retry for a capacity refusal.
  expect(scheduled).toHaveLength(0);
  // Display is preserved for the refused content.
  expect(
    reader
      .getSnapshot()
      ?.turns.flatMap((turn) => turn.items)
      .map((item) => item.transcriptKey),
  ).toEqual(["item-base", "item-oversize"]);
  // Durable readiness is honestly withheld rather than reported current.
  expect((await reader.openLocal(40)).status).toBe("recovery-needed");

  // Changed eligible content is reconsidered: a newer accepted read retries a
  // real commit instead of being suppressed by the refusal.
  const attempts = vi.spyOn(fixture.cache, "commitBatch");
  owner.acceptRead(read("changed", 3), { lifetime: 1, readId: "read-changed", boundary: "boundary-changed" });
  await owner.settled();
  expect(attempts).toHaveBeenCalled();
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
