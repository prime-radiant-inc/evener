// The buffered half of the same ordering. A row no loaded page can take yet
// holds its frames; both hold edges arrive at the same revision, so the ladder
// would settle the buffered pair on the earlier edge and the admitted row would
// wear a wait that has already ended. The source sequence orders the pair: each
// buffered frame keeps the sequence it was received with, so settling does not
// re-stamp the older edge as newer.
test("a buffered pre-hold frame cannot outlive the edge that clears it", async () => {
  const client = pushClient();
  const admit = deferred<void>();
  const readStarted = deferred<void>();
  const context = pushContext();
  let served = 0;
  client.on("evener/thread/delegates/list", async ({ scope }) => {
    served += 1;
    if (served > 1) {
      readStarted.resolve();
      await admit.promise;
    }
    return {
      context,
      scope: scope ?? "session",
      page: { complete: true, issues: [] },
      // The row the buffer's identity names is admitted only by the second
      // response, so both edges below buffer behind the first.
      delegates: served > 1 ? [delegateRow({ projectionRevision: 4 })] : [],
    };
  });
  const store = owner(client);
  store.observe("delegates");
  await activityState(store, () => store.getSnapshot().delegates.complete);

  // Both edges for a row the loaded set does not carry yet, with the sequences
  // the source assigns: the release is sampled after the hold it ends.
  const held = frameInfo({ projectionRevision: 4, holdSequence: 5, drainHeldByJobs: true, runningJobCount: 1 });
  client.emitNotification(pushedFrame(held));
  client.emitNotification(pushedFrame(frameInfo({ projectionRevision: 4, holdSequence: 6 })));

  // The read that admits the row arrives after both edges.
  activityChanged(client, ["delegates"]);
  await readStarted.promise;
  admit.resolve();
  await activityState(store, () => store.getSnapshot().delegates.rows.length === 1);
  const row = store.getSnapshot().delegates.rows[0];
  expect(row?.drainHeldByJobs ?? false).toBe(false);
  expect(row?.holdSequence ?? 0).toBe(6);
  expect(row?.runningJobCount ?? 0).toBe(0);
});

// The inverse arrival order, buffered. A frame sampled before the hold and
// delivered after the release must not reinstate the wait: the source sequence
// is what orders the pair, so the older sample loses however late it arrives.
test("a buffered hold older than the release does not reinstate the wait", async () => {
  const client = pushClient();
  const admit = deferred<void>();
  const readStarted = deferred<void>();
  const context = pushContext();
  let served = 0;
  client.on("evener/thread/delegates/list", async ({ scope }) => {
    served += 1;
    if (served > 1) {
      readStarted.resolve();
      await admit.promise;
    }
    return {
      context,
      scope: scope ?? "session",
      page: { complete: true, issues: [] },
      delegates: served > 1 ? [delegateRow({ projectionRevision: 4 })] : [],
    };
  });
  const store = owner(client);
  store.observe("delegates");
  await activityState(store, () => store.getSnapshot().delegates.complete);

  // The release is sampled after the hold, but the hold is delivered last.
  client.emitNotification(pushedFrame(frameInfo({ projectionRevision: 4, holdSequence: 6 })));
  client.emitNotification(
    pushedFrame(
      frameInfo({
        projectionRevision: 4,
        holdSequence: 5,
        drainHeldByJobs: true,
        runningJobCount: 1,
        oldestRunningJobStartedAt: "2026-11-18T10:00:00Z",
      }),
    ),
  );

  activityChanged(client, ["delegates"]);
  await readStarted.promise;
  admit.resolve();
  await activityState(store, () => store.getSnapshot().delegates.rows.length === 1);
  const row = store.getSnapshot().delegates.rows[0];
  expect(row?.drainHeldByJobs ?? false).toBe(false);
  expect(row?.holdSequence ?? 0).toBe(6);
  expect(row?.runningJobCount ?? 0).toBe(0);
});

// @vitest-environment node
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { WireError } from "./errors";
import { MAX_INTENT_CODE_POINTS } from "./sessionActivity";
import { type SessionActivitySnapshot, SessionActivityStore } from "./sessionActivityStore";
import {
  activityChanged,
  activityClient,
  activityContext,
  activityRef,
  activityState,
  delegateFixture,
  jobFixture,
  jobsFixture,
  summaryFixture,
} from "./sessionActivityTestUtils";
import { deferred } from "./testing/deferred";
import { callsTo } from "./testing/fakeClient";
import {
  subagentOutcomesDelegatesResponse,
  subagentResumedDelegatesResponse,
  subagentResumedFinishedNotification,
  subagentResumedOpenDelegatesResponse,
  subagentResumedOpenNotification,
} from "./testing/subagentWireFixtures";
import type {
  AnyNotification,
  EvenerDelegateInfo,
  SessionActivityContext,
  SessionActivitySummary,
  SessionDelegate,
  SessionDelegatesResponse,
  SessionJobsResponse,
} from "./types.gen";

const owners: SessionActivityStore[] = [];

const owner = (
  client = activityClient(),
  scope: "session" | "subtree" = "session",
  retained?: SessionActivitySnapshot,
) => {
  const store = new SessionActivityStore(client, activityRef, {
    scope,
    retained,
    clock: {
      setTimeout: (callback, ms) => setTimeout(callback, ms),
      clearTimeout: (handle) => clearTimeout(handle as ReturnType<typeof setTimeout>),
    },
  });
  owners.push(store);
  return store;
};

beforeEach(() => vi.useFakeTimers());

afterEach(() => {
  for (const store of owners.splice(0)) store.dispose();
  vi.useRealTimers();
});

// A page in flight when a collection's last observer leaves is dropped, not
// merged: the observer that asked for it is gone, and a store kept alive by
// another holder (a summary count) must not take in a closed view's late page.
test("a page in flight when the last observer leaves does not land", async () => {
  const client = activityClient(),
    late = deferred<SessionJobsResponse>(),
    entered = deferred<void>();
  client.on("evener/thread/jobs/list", ({ cursor }) => {
    if (!cursor) return jobsFixture([jobFixture("shell-1")], "page-2");
    entered.resolve();
    return late.promise;
  });
  const store = owner(client);
  store.start();
  await activityState(store, () => store.getSnapshot().summary !== null);
  const leave = store.observe("jobs");
  await activityState(store, () => store.getSnapshot().jobs.hasMore);
  const more = store.loadMore("jobs");
  await entered.promise;
  leave();
  late.resolve(jobsFixture([jobFixture("late")]));
  await more;
  expect(store.getSnapshot().jobs.rows.map(({ jobId }) => jobId)).not.toContain("late");
});

// Demand queued behind that page by an explicit loadMore is dropped with it,
// and the collection is left settled, not pending a read nothing will make.
test("demand queued behind a page when the last observer leaves does not land either", async () => {
  const client = activityClient(),
    late = deferred<SessionJobsResponse>(),
    entered = deferred<void>();
  client.on("evener/thread/jobs/list", ({ cursor }) => {
    if (!cursor) return jobsFixture([jobFixture("shell-1")], "page-2");
    if (callsTo(client, "evener/thread/jobs/list") === 2) {
      entered.resolve();
      return late.promise;
    }
    return jobsFixture([jobFixture("late-queued")]);
  });
  const store = owner(client);
  store.start();
  await activityState(store, () => store.getSnapshot().summary !== null);
  const leave = store.observe("jobs");
  await activityState(store, () => store.getSnapshot().jobs.hasMore);
  const first = store.loadMore("jobs");
  await entered.promise;
  const second = store.loadMore("jobs");
  leave();
  late.resolve(jobsFixture([jobFixture("late")]));
  await Promise.all([first, second]);
  expect(store.getSnapshot().jobs.rows.map(({ jobId }) => jobId)).toEqual(["shell-1"]);
  expect(callsTo(client, "evener/thread/jobs/list")).toBe(2);
  expect(store.getSnapshot().jobs).toMatchObject({ loading: false, pending: false });
});

// A view that leaves while a page it asked for is being published (a listener
// releasing the last observer as the page lands, on either of its publishes)
// is gone for the rest of that read: an explicit loadMore queues no next page
// for it.
test.each([1, 2])("a release during a loaded page's publish %i queues no next page", async (publish) => {
  const client = activityClient();
  client.on("evener/thread/jobs/list", ({ cursor }) => {
    if (!cursor) return jobsFixture([jobFixture("shell-1")], "page-2");
    if (cursor === "page-2") return jobsFixture([], "page-3");
    return jobsFixture([jobFixture("late")]);
  });
  const store = owner(client);
  store.start();
  await activityState(store, () => store.getSnapshot().summary !== null);
  const leave = store.observe("jobs");
  await activityState(store, () => store.getSnapshot().jobs.hasMore);
  let publishes = 0;
  const stop = store.subscribe(() => {
    if (callsTo(client, "evener/thread/jobs/list") === 2 && ++publishes === publish) leave();
  });
  await store.loadMore("jobs");
  stop();
  expect(callsTo(client, "evener/thread/jobs/list")).toBe(2);
  expect(store.getSnapshot().jobs.rows.map(({ jobId }) => jobId)).not.toContain("late");
});

// An explicit load admitted before the connection is ready is the leaving
// observer's too: once the last observer leaves, neither readiness nor a
// change notification starts a read for the closed collection.
test("an explicit load waiting for the connection is dropped when the last observer leaves", async () => {
  const client = activityClient("connecting"),
    store = owner(client);
  store.start();
  const leave = store.observe("jobs");
  void store.load("jobs");
  leave();
  client.emitReady();
  await activityState(store, () => store.getSnapshot().summary !== null);
  activityChanged(client, ["jobs"]);
  await store.refresh("summary");
  expect(callsTo(client, "evener/thread/jobs/list")).toBe(0);
});

// After the last observer leaves and its late page is dropped, the
// collection still works: a new observer's loadMore issues a fresh read
// whose rows land, and the collection settles.
test("a collection whose late page was dropped recovers for its next observer", async () => {
  const client = activityClient(),
    late = deferred<SessionJobsResponse>(),
    entered = deferred<void>();
  client.on("evener/thread/jobs/list", ({ cursor }) => {
    if (!cursor) return jobsFixture([jobFixture("shell-1")], "page-2");
    if (callsTo(client, "evener/thread/jobs/list") === 2) {
      entered.resolve();
      return late.promise;
    }
    return jobsFixture([jobFixture("fresh")]);
  });
  const store = owner(client);
  store.start();
  await activityState(store, () => store.getSnapshot().summary !== null);
  const leave = store.observe("jobs");
  await activityState(store, () => store.getSnapshot().jobs.hasMore);
  const dropped = store.loadMore("jobs");
  await entered.promise;
  leave();
  late.resolve(jobsFixture([jobFixture("late")]));
  await dropped;
  store.observe("jobs");
  await activityState(store, () => store.getSnapshot().jobs.hasMore && !store.getSnapshot().jobs.loading);
  await store.loadMore("jobs");
  const rows = store.getSnapshot().jobs.rows.map(({ jobId }) => jobId);
  expect(rows).toContain("fresh");
  expect(rows).not.toContain("late");
  expect(store.getSnapshot().jobs).toMatchObject({ loading: false, pending: false });
});

// A view that leaves while the session is replaced (a listener releasing the
// last observer as the replacement resets the store) takes nothing from the
// replacement: the collection stays reset, and no read follows for it.
test("a release during a session replacement leaves the collection reset", async () => {
  const client = activityClient();
  // Once the hub has replaced the session, every read answers for the
  // replacement, the summary the replacement queues included.
  let replaced = false;
  const replacement = { ...activityContext(), sessionId: "replacement" };
  client.on("evener/thread/jobs/list", ({ cursor }) => {
    if (cursor) return jobsFixture([jobFixture("late")]);
    replaced = true;
    return { ...jobsFixture([jobFixture("replaced")], "page-2"), context: replacement };
  });
  client.on("evener/thread/activity/read", ({ scope }) =>
    replaced ? { ...summaryFixture(scope), context: replacement } : summaryFixture(scope),
  );
  const store = owner(client);
  store.start();
  await activityState(store, () => store.getSnapshot().summary !== null);
  const leave = store.observe("jobs");
  const stop = store.subscribe(() => {
    if (store.getSnapshot().context?.sessionId === "replacement") leave();
  });
  await activityState(
    store,
    () =>
      store.getSnapshot().summary?.context.sessionId === "replacement" &&
      !store.getSnapshot().jobs.loading &&
      !store.getSnapshot().summaryState.loading,
  );
  stop();
  expect(store.getSnapshot().jobs.rows).toEqual([]);
  expect(callsTo(client, "evener/thread/jobs/list")).toBe(1);
});

// A view that leaves resets its collection's failure count: the next
// observer's first failed read backs off from the start (1s), not from the
// count a closed view's failures left behind.
test("the last observer leaving resets the collection's backoff", async () => {
  const client = activityClient();
  client.on("evener/thread/jobs/list", () => {
    throw new Error("unreachable");
  });
  const store = owner(client);
  const failedReads = (count: number) =>
    activityState(
      store,
      () => callsTo(client, "evener/thread/jobs/list") === count && !store.getSnapshot().jobs.loading,
    );
  store.start();
  await activityState(store, () => store.getSnapshot().summary !== null);
  const leave = store.observe("jobs");
  await failedReads(1);
  await vi.advanceTimersByTimeAsync(1000);
  await failedReads(2);
  leave();
  store.observe("jobs");
  await failedReads(3);
  await vi.advanceTimersByTimeAsync(999);
  expect(callsTo(client, "evener/thread/jobs/list")).toBe(3);
  await vi.advanceTimersByTimeAsync(1);
  await failedReads(4);
});

// Serving a collection read can warm the hub's count for it. A view that
// leaves right after the fetch drops only the page: the store still refreshes
// the summary, so a count the read warmed does not stay unknown.
test("a read dropped because its view left still refreshes an unknown count", async () => {
  const client = activityClient(),
    page = deferred<SessionDelegatesResponse>(),
    entered = deferred<void>();
  client.on("evener/thread/delegates/list", () => {
    entered.resolve();
    return page.promise;
  });
  const store = owner(client);
  store.start();
  await activityState(store, () => store.getSnapshot().summary !== null);
  expect(store.getSnapshot().summary?.delegates.known).toBe(false);
  const leave = store.observe("delegates");
  await entered.promise;
  leave();
  page.resolve({
    context: activityContext(),
    scope: "session",
    page: { complete: true, issues: [] },
    delegates: [delegateFixture()],
  });
  await activityState(store, () => !store.getSnapshot().delegates.loading);
  await vi.advanceTimersByTimeAsync(100);
  expect(callsTo(client, "evener/thread/activity/read")).toBe(2);
  expect(store.getSnapshot().delegates.rows).toEqual([]);
});

// The same holds when the view leaves while the page publishes (a listener
// releasing the last observer on either of its publishes): the page's read
// ends, and the count it may have warmed is still refreshed.
test.each([1, 2])("a read dropped during its page's publish %i still refreshes an unknown count", async (publish) => {
  const client = activityClient();
  const store = owner(client);
  store.start();
  await activityState(store, () => store.getSnapshot().summary !== null);
  expect(store.getSnapshot().summary?.delegates.known).toBe(false);
  const leave = store.observe("delegates");
  let publishes = 0;
  const stop = store.subscribe(() => {
    if (callsTo(client, "evener/thread/delegates/list") === 1 && ++publishes === publish) leave();
  });
  await activityState(
    store,
    () => callsTo(client, "evener/thread/delegates/list") === 1 && !store.getSnapshot().delegates.loading,
  );
  stop();
  await vi.advanceTimersByTimeAsync(100);
  expect(callsTo(client, "evener/thread/activity/read")).toBe(2);
});

// --- Pushed delegate merge (spec: session-activity push updates) ---
//
// The no-read merge is enabled only by a live context that advertises
// `reportPreview`; the shared `activityContext()` fixture omits it on purpose,
// so the default client keeps the read path (the negative test below pins it).

const pushContext = (overrides: Partial<SessionActivityContext> = {}): SessionActivityContext => ({
  ...activityContext(),
  reportPreview: true,
  ...overrides,
});

const delegateRow = (overrides: Partial<SessionDelegate> = {}): SessionDelegate => ({
  ...delegateFixture(),
  projectionRevision: 1,
  ...overrides,
});

const frameInfo = (overrides: Partial<EvenerDelegateInfo> = {}): EvenerDelegateInfo => ({
  runGeneration: 1,
  delegateId: "delegate-1",
  ownerSessionId: "session",
  logicalOwnerSessionId: "session",
  rootSessionId: "session",
  childSessionId: "child",
  childRef: "remote:delegate-1",
  transcriptRef: "remote:child",
  type: "delegate",
  lifecycle: "running",
  phase: "running",
  status: "running",
  terminal: false,
  resumable: true,
  needsAttention: false,
  projectionRevision: 2,
  ...overrides,
});

const pushedFrame = (delegate: EvenerDelegateInfo): AnyNotification => ({
  method: "evener/delegate/updated",
  params: { threadId: "session", ref: activityRef, delegate },
});

// An eligible client: both the summary and delegates reads answer a live
// context advertising the bounded preview, and the collection serves one row.
function pushClient(): ReturnType<typeof activityClient> {
  const client = activityClient();
  client.on("evener/thread/activity/read", ({ scope }) => ({ ...summaryFixture(scope), context: pushContext() }));
  client.on("evener/thread/delegates/list", ({ scope }) => ({
    context: pushContext(),
    scope: scope ?? "session",
    page: { complete: true, issues: [] },
    delegates: [delegateRow()],
  }));
  return client;
}

async function pushOwner(
  client = pushClient(),
): Promise<{ client: ReturnType<typeof activityClient>; store: SessionActivityStore }> {
  const store = owner(client);
  store.observe("delegates");
  await activityState(store, () => store.getSnapshot().delegates.complete);
  return { client, store };
}

test("a delegate field update patches a loaded row with zero delegates reads", async () => {
  const { client, store } = await pushOwner();
  const before = callsTo(client, "evener/thread/delegates/list");
  client.emitNotification(
    pushedFrame(
      frameInfo({ projectionRevision: 2, status: "failed", reportPreview: "done", reportPreviewTruncated: false }),
    ),
  );
  expect(callsTo(client, "evener/thread/delegates/list")).toBe(before);
  expect(store.getSnapshot().delegates.rows[0]).toMatchObject({ status: "failed", reportPreview: "done" });
});

test("a repeated delegate frame publishes nothing", async () => {
  const { client, store } = await pushOwner();
  const frame = frameInfo({ projectionRevision: 2, status: "failed" });
  client.emitNotification(pushedFrame(frame));
  let publishes = 0;
  const stop = store.subscribe(() => {
    publishes += 1;
  });
  client.emitNotification(pushedFrame(frame));
  stop();
  expect(publishes).toBe(0);
});

test("a stale projectionRevision cannot regress a row", async () => {
  const client = pushClient();
  const context = pushContext();
  client.on("evener/thread/delegates/list", ({ scope }) => ({
    context,
    scope: scope ?? "session",
    page: { complete: true, issues: [] },
    delegates: [delegateRow({ projectionRevision: 5, status: "completed" })],
  }));
  const { store } = await pushOwner(client);
  client.emitNotification(pushedFrame(frameInfo({ projectionRevision: 3, status: "running" })));
  expect(store.getSnapshot().delegates.rows[0]?.status).toBe("completed");
});

// The drain hold is its own facet, outside the revision ladder. Entering the
// drain appends no durable delegate event, so both edges publish at the
// revision the loaded row already carries; under the ladder that row would
// never learn the wait and never learn it ended.
test("a hold at an unchanged revision still reaches a loaded row", async () => {
  const client = pushClient();
  const context = pushContext();
  client.on("evener/thread/delegates/list", ({ scope }) => ({
    context,
    scope: scope ?? "session",
    page: { complete: true, issues: [] },
    delegates: [delegateRow({ projectionRevision: 4 })],
  }));
  const { store } = await pushOwner(client);
  const loaded = store.getSnapshot().delegates.rows[0];
  expect(loaded?.drainHeldByJobs ?? false).toBe(false);

  // Same revision the row already holds: every other field must hold its
  // place, and only the hold and its phrasing arrive.
  client.emitNotification(
    pushedFrame(
      frameInfo({
        projectionRevision: 4,
        status: "running",
        drainHeldByJobs: true,
        holdSequence: 5,
        runningJobCount: 2,
        oldestRunningJobStartedAt: "2026-11-18T10:00:00Z",
      }),
    ),
  );
  expect(store.getSnapshot().delegates.rows[0]).toMatchObject({
    drainHeldByJobs: true,
    holdSequence: 5,
    runningJobCount: 2,
    oldestRunningJobStartedAt: "2026-11-18T10:00:00Z",
  });
});

// And the release edge, at the same revision again: the row must stop
// claiming the wait rather than keeping the last hold forever.
test("clearing the hold at an unchanged revision empties the wait", async () => {
  const client = pushClient();
  const context = pushContext();
  client.on("evener/thread/delegates/list", ({ scope }) => ({
    context,
    scope: scope ?? "session",
    page: { complete: true, issues: [] },
    delegates: [
      delegateRow({
        projectionRevision: 4,
        drainHeldByJobs: true,
        holdSequence: 4,
        runningJobCount: 1,
        oldestRunningJobStartedAt: "2026-11-18T10:00:00Z",
      }),
    ],
  }));
  const { store } = await pushOwner(client);
  client.emitNotification(pushedFrame(frameInfo({ projectionRevision: 4, holdSequence: 6 })));
  const row = store.getSnapshot().delegates.rows[0];
  expect(row?.drainHeldByJobs ?? false).toBe(false);
  expect(row?.holdSequence ?? 0).toBe(6);
  expect(row?.runningJobCount ?? 0).toBe(0);
  expect(row?.oldestRunningJobStartedAt ?? "").toBe("");
});

// A higher projection revision does not mean a newer hold sample, and the
// store must not let the durable winner supply the hold. A frame that samples
// the wait at a new revision and pauses can arrive after a heartbeat that
// sampled a reduced count: the durable fields must take the frame's revision
// while the hold keeps the later sample.
test("a higher-revision frame does not roll the hold back", async () => {
  const client = pushClient();
  const context = pushContext();
  client.on("evener/thread/delegates/list", ({ scope }) => ({
    context,
    scope: scope ?? "session",
    page: { complete: true, issues: [] },
    delegates: [
      delegateRow({
        projectionRevision: 4,
        drainHeldByJobs: true,
        holdSequence: 12,
        runningJobCount: 2,
        oldestRunningJobStartedAt: "2026-11-18T10:00:00Z",
      }),
    ],
  }));
  const { store } = await pushOwner(client);

  // One shell finished: higher revision, but its hold was sampled before the
  // row's.
  client.emitNotification(
    pushedFrame(
      frameInfo({
        projectionRevision: 5,
        status: "running",
        drainHeldByJobs: true,
        holdSequence: 10,
        runningJobCount: 1,
        oldestRunningJobStartedAt: "2026-11-18T10:00:00Z",
      }),
    ),
  );
  const row = store.getSnapshot().delegates.rows[0];
  expect(row?.projectionRevision).toBe(5);
  expect(row?.holdSequence).toBe(12);
  expect(row?.runningJobCount).toBe(2);
});

// The inverse arrival order: a delegates refresh captures a running row just
// before the drain starts, and its reply lands after the hold frame. At the
// same revision the read used to clear the hold, and the row then missed the
// wait for the rest of its duration. The hold carries its own freshness, so
// the newer frame stands against the older snapshot while every durable field
// keeps the revision ordering.
test("a read that predates the hold cannot clear it", async () => {
  const client = pushClient();
  const refresh = deferred<void>();
  const started = deferred<void>();
  const context = pushContext();
  let served = 0;
  client.on("evener/thread/delegates/list", async ({ scope }) => {
    served += 1;
    // The first page loads the row; the second is the refresh captured before
    // the hold, held open until after the frame has landed.
    if (served > 1) {
      started.resolve();
      await refresh.promise;
    }
    return {
      context,
      scope: scope ?? "session",
      page: { complete: true, issues: [] },
      // The refresh's page carries a durable change, so the test can await the
      // landing it is judging: a read that never arrived would prove nothing.
      // Its hold sample is the one the read took before the drain started, so
      // the row must reject it even though it arrives last.
      delegates: [delegateRow({ projectionRevision: 4, latestIntent: served > 1 ? "Refreshed." : undefined })],
    };
  });
  const store = owner(client);
  store.observe("delegates");
  await activityState(store, () => store.getSnapshot().delegates.complete);

  // Ask for the refresh, then deliver the hold before the refresh resolves.
  activityChanged(client, ["delegates"]);
  await started.promise;
  client.emitNotification(
    pushedFrame(
      frameInfo({
        projectionRevision: 4,
        holdSequence: 5,
        drainHeldByJobs: true,
        runningJobCount: 1,
        oldestRunningJobStartedAt: "2026-11-18T10:00:00Z",
      }),
    ),
  );
  // The frame must have landed before the older snapshot resolves, or the test
  // is not exercising the ordering it names.
  await activityState(store, () => store.getSnapshot().delegates.rows[0]?.drainHeldByJobs === true);
  refresh.resolve();
  // Wait for the read itself to land. Its durable field is what proves it
  // arrived; the hold is the fact this test is about.
  await activityState(store, () => store.getSnapshot().delegates.rows[0]?.latestIntent === "Refreshed.");
  const row = store.getSnapshot().delegates.rows[0];
  expect(row?.drainHeldByJobs).toBe(true);
  expect(row?.holdSequence).toBe(5);
  expect(row?.runningJobCount).toBe(1);
});

test("a resume clears the prior report", async () => {
  const client = pushClient();
  client.on("evener/thread/delegates/list", ({ scope }) => ({
    context: pushContext(),
    scope: scope ?? "session",
    page: { complete: true, issues: [] },
    delegates: [delegateRow({ runGeneration: 1, projectionRevision: 5, reportPreview: "old", status: "completed" })],
  }));
  const { store } = await pushOwner(client);
  client.emitNotification(pushedFrame(frameInfo({ runGeneration: 2, projectionRevision: 6, status: "running" })));
  const row = store.getSnapshot().delegates.rows[0];
  expect(row?.runGeneration).toBe(2);
  expect(row?.reportPreview).toBeUndefined();
});

test("recorded resumed-run frames match fresh reads without rereading a loaded row", async () => {
  const initial = subagentOutcomesDelegatesResponse();
  const freshOpen = subagentResumedOpenDelegatesResponse();
  const freshFinished = subagentResumedDelegatesResponse();
  const client = activityClient();
  client.on("evener/thread/delegates/list", () => initial);
  const store = new SessionActivityStore(client, initial.context.ref, {
    scope: "subtree",
    clock: {
      setTimeout: (callback, ms) => setTimeout(callback, ms),
      clearTimeout: (handle) => clearTimeout(handle as ReturnType<typeof setTimeout>),
    },
  });
  owners.push(store);
  store.observe("delegates");
  await activityState(store, () => store.getSnapshot().delegates.complete);
  const row = () => store.getSnapshot().delegates.rows.find(({ delegateId }) => delegateId === "dlg_reported");
  const fresh = (response: SessionDelegatesResponse) =>
    response.delegates.find(({ delegateId }) => delegateId === "dlg_reported");
  expect(row()).toMatchObject({ runGeneration: 1, outcome: "completed", terminal: true });

  const reads = callsTo(client, "evener/thread/delegates/list");
  client.emitNotification(subagentResumedOpenNotification());
  const open = row();
  const openRead = fresh(freshOpen);
  expect(callsTo(client, "evener/thread/delegates/list")).toBe(reads);
  expect(open).toMatchObject({
    runGeneration: openRead?.runGeneration,
    projectionRevision: openRead?.projectionRevision,
    lifecycle: "running",
    phase: "running",
    status: "running",
    terminal: false,
  });
  expect([open?.outcome, open?.reason, open?.error, open?.runEndedAt, open?.reportPreview]).toEqual([
    undefined,
    undefined,
    undefined,
    undefined,
    undefined,
  ]);

  client.emitNotification(subagentResumedFinishedNotification());
  const finished = row();
  const finishedRead = fresh(freshFinished);
  expect(callsTo(client, "evener/thread/delegates/list")).toBe(reads);
  expect(finished).toMatchObject({
    runGeneration: finishedRead?.runGeneration,
    projectionRevision: finishedRead?.projectionRevision,
    outcome: finishedRead?.outcome,
    terminal: finishedRead?.terminal,
    runEndedAt: finishedRead?.runEndedAt,
    reportPreview: finishedRead?.reportPreview,
  });
});

test("a frame sets a row's latest intent and a later frame clears it", async () => {
  const { client, store } = await pushOwner();
  client.emitNotification(pushedFrame(frameInfo({ projectionRevision: 2, latestIntent: "Reading the board." })));
  expect(store.getSnapshot().delegates.rows[0]?.latestIntent).toBe("Reading the board.");
  // A winning frame supplies the fields it omits as empty, so the run's
  // settlement clears the line exactly as it clears a report preview.
  client.emitNotification(pushedFrame(frameInfo({ projectionRevision: 3, status: "failed" })));
  expect(store.getSnapshot().delegates.rows[0]?.latestIntent).toBeUndefined();
});

test("a run's second intent frame wins by revision", async () => {
  const { client, store } = await pushOwner();
  client.emitNotification(pushedFrame(frameInfo({ projectionRevision: 2, latestIntent: "First words." })));
  expect(store.getSnapshot().delegates.rows[0]?.latestIntent).toBe("First words.");
  client.emitNotification(pushedFrame(frameInfo({ projectionRevision: 3, latestIntent: "Second words." })));
  expect(store.getSnapshot().delegates.rows[0]?.latestIntent).toBe("Second words.");
});

test("an intent frame stays behind the reportPreview gate", async () => {
  const client = activityClient();
  client.on("evener/thread/delegates/list", ({ scope }) => ({
    context: activityContext(),
    scope: scope ?? "session",
    page: { complete: true, issues: [] },
    delegates: [delegateRow({ latestIntent: "From the read." })],
  }));
  const { store } = await pushOwner(client);
  expect(store.getSnapshot().delegates.rows[0]?.latestIntent).toBe("From the read.");
  client.emitNotification(pushedFrame(frameInfo({ projectionRevision: 2, latestIntent: "From a frame." })));
  const row = store.getSnapshot().delegates.rows[0];
  expect(row?.latestIntent).toBe("From the read.");
  expect(row?.status).toBe("completed");
});

test("an over-bound or invalid frame intent applies as absent without dropping the row", async () => {
  const { client, store } = await pushOwner();
  client.emitNotification(
    pushedFrame(frameInfo({ projectionRevision: 2, latestIntent: "x".repeat(MAX_INTENT_CODE_POINTS + 1) })),
  );
  let row = store.getSnapshot().delegates.rows[0];
  expect(row?.latestIntent).toBeUndefined();
  expect(row?.status).toBe("running");
  // A value the wire type does not admit arrives only through a misbehaving
  // source: it reads as omitted, and the row keeps the frame's other fields.
  const invalid = { ...frameInfo({ projectionRevision: 3 }), latestIntent: 42 } as unknown as EvenerDelegateInfo;
  client.emitNotification(pushedFrame(invalid));
  row = store.getSnapshot().delegates.rows[0];
  expect(row?.latestIntent).toBeUndefined();
  expect(row?.status).toBe("running");
});

test("a read row's over-bound intent applies as absent without dropping the row", async () => {
  const client = pushClient();
  client.on("evener/thread/delegates/list", ({ scope }) => ({
    context: pushContext(),
    scope: scope ?? "session",
    page: { complete: true, issues: [] },
    delegates: [delegateRow({ latestIntent: "x".repeat(MAX_INTENT_CODE_POINTS + 1) })],
  }));
  const { store } = await pushOwner(client);
  const row = store.getSnapshot().delegates.rows[0];
  expect(row?.delegateId).toBe("delegate-1");
  expect(row?.latestIntent).toBeUndefined();
});

test("an older run's intent frame loses to a newer row's revision", async () => {
  // The store fences by the wire's per-delegate revision monotonicity, not by
  // generation: the daemon never publishes an old generation's frame with a
  // revision newer than the row it belongs to, so the strictly-greater revision
  // join keeps the older run's line off the newer run's row.
  const client = pushClient();
  client.on("evener/thread/delegates/list", ({ scope }) => ({
    context: pushContext(),
    scope: scope ?? "session",
    page: { complete: true, issues: [] },
    delegates: [
      delegateRow({
        runGeneration: 2,
        projectionRevision: 6,
        status: "running",
        phase: "running",
        lifecycle: "running",
        terminal: false,
        latestIntent: "New run.",
      }),
    ],
  }));
  const { store } = await pushOwner(client);
  client.emitNotification(
    pushedFrame(frameInfo({ runGeneration: 1, projectionRevision: 5, latestIntent: "Old run." })),
  );
  const row = store.getSnapshot().delegates.rows[0];
  expect(row?.runGeneration).toBe(2);
  expect(row?.latestIntent).toBe("New run.");
});

test("a session store ignores another owner's delegate per the logical owner", async () => {
  const { client, store } = await pushOwner();
  const before = callsTo(client, "evener/thread/delegates/list");
  client.emitNotification(
    pushedFrame(
      frameInfo({ delegateId: "other-delegate", logicalOwnerSessionId: "other-owner", projectionRevision: 9 }),
    ),
  );
  client.emitNotification(
    pushedFrame(frameInfo({ logicalOwnerSessionId: "other-owner", projectionRevision: 9, status: "failed" })),
  );
  expect(store.getSnapshot().delegates.rows).toHaveLength(1);
  expect(store.getSnapshot().delegates.rows[0]?.status).toBe("completed");
  expect(callsTo(client, "evener/thread/delegates/list")).toBe(before);
});

test("a non-root session store accepts its own children and ignores a deeper descendant", async () => {
  const client = pushClient();
  const context = pushContext({ sessionId: "parent-session" });
  client.on("evener/thread/activity/read", ({ scope }) => ({ ...summaryFixture(scope), context }));
  client.on("evener/thread/delegates/list", ({ scope }) => ({
    context,
    scope: scope ?? "session",
    page: { complete: true, issues: [] },
    delegates: [delegateRow({ status: "running" })],
  }));
  const { store } = await pushOwner(client);
  client.emitNotification(
    pushedFrame(
      frameInfo({
        ownerSessionId: "parent-session",
        logicalOwnerSessionId: "parent-session",
        projectionRevision: 2,
        status: "completed",
      }),
    ),
  );
  expect(store.getSnapshot().delegates.rows[0]?.status).toBe("completed");
  client.emitNotification(
    pushedFrame(
      frameInfo({
        ownerSessionId: "parent-session",
        logicalOwnerSessionId: "grandchild-session",
        projectionRevision: 3,
        status: "failed",
      }),
    ),
  );
  expect(store.getSnapshot().delegates.rows[0]?.status).toBe("completed");
});

test("a root session store ignores a descendant-owned delegate frame without a read", async () => {
  const { client, store } = await pushOwner();
  const before = callsTo(client, "evener/thread/delegates/list");
  client.emitNotification(
    pushedFrame(
      frameInfo({ delegateId: "descendant-delegate", logicalOwnerSessionId: "descendant", ownerSessionId: "session" }),
    ),
  );
  await store.refresh("summary");
  expect(callsTo(client, "evener/thread/delegates/list")).toBe(before);
  expect(store.getSnapshot().delegates.rows).toHaveLength(1);
});

test("the served projectionRevision seeds the merge order", async () => {
  const client = pushClient();
  client.on("evener/thread/delegates/list", ({ scope }) => ({
    context: pushContext(),
    scope: scope ?? "session",
    page: { complete: true, issues: [] },
    delegates: [delegateRow({ projectionRevision: 8, status: "completed" })],
  }));
  const { store } = await pushOwner(client);
  client.emitNotification(pushedFrame(frameInfo({ projectionRevision: 7, status: "running" })));
  expect(store.getSnapshot().delegates.rows[0]?.status).toBe("completed");
  client.emitNotification(pushedFrame(frameInfo({ projectionRevision: 9, status: "failed" })));
  expect(store.getSnapshot().delegates.rows[0]?.status).toBe("failed");
});

test("a lower-revision later-activity frame advances only the timestamp", async () => {
  const client = pushClient();
  client.on("evener/thread/delegates/list", ({ scope }) => ({
    context: pushContext(),
    scope: scope ?? "session",
    page: { complete: true, issues: [] },
    delegates: [delegateRow({ projectionRevision: 5, status: "completed", latestActivityAt: "2026-01-01T00:00:00Z" })],
  }));
  const { store } = await pushOwner(client);
  client.emitNotification(
    pushedFrame(frameInfo({ projectionRevision: 3, status: "running", latestActivityAt: "2026-01-02T00:00:00Z" })),
  );
  const row = store.getSnapshot().delegates.rows[0];
  expect(row?.status).toBe("completed");
  expect(row?.latestActivityAt).toBe("2026-01-02T00:00:00Z");
});

test("a higher-revision frame does not move latestActivityAt backward", async () => {
  const client = pushClient();
  client.on("evener/thread/delegates/list", ({ scope }) => ({
    context: pushContext(),
    scope: scope ?? "session",
    page: { complete: true, issues: [] },
    delegates: [delegateRow({ projectionRevision: 5, status: "running", latestActivityAt: "2026-01-02T00:00:00Z" })],
  }));
  const { store } = await pushOwner(client);
  client.emitNotification(
    pushedFrame(frameInfo({ projectionRevision: 6, status: "failed", latestActivityAt: "2026-01-01T00:00:00Z" })),
  );
  const row = store.getSnapshot().delegates.rows[0];
  expect(row?.status).toBe("failed");
  expect(row?.latestActivityAt).toBe("2026-01-02T00:00:00Z");
});

test("a read landing after a newer frame neither clobbers it nor reopens a skipped revision", async () => {
  const client = pushClient();
  client.on("evener/thread/delegates/list", ({ scope }) => ({
    context: pushContext(),
    scope: scope ?? "session",
    page: { complete: true, issues: [] },
    delegates: [delegateRow({ projectionRevision: 6, status: "running", latestActivityAt: "2026-01-01T00:00:00Z" })],
  }));
  const { store } = await pushOwner(client);
  client.emitNotification(
    pushedFrame(frameInfo({ projectionRevision: 10, status: "completed", latestActivityAt: "2026-01-02T00:00:00Z" })),
  );
  expect(store.getSnapshot().delegates.rows[0]).toMatchObject({
    status: "completed",
    projectionRevision: 10,
    latestActivityAt: "2026-01-02T00:00:00Z",
  });
  await store.refresh("delegates");
  expect(store.getSnapshot().delegates.rows[0]).toMatchObject({
    status: "completed",
    projectionRevision: 10,
    latestActivityAt: "2026-01-02T00:00:00Z",
  });
  // A frame in the gap (7, above the read's 6 and below the applied 10) must
  // still be rejected, while its later activity advances independently.
  client.emitNotification(
    pushedFrame(frameInfo({ projectionRevision: 7, status: "failed", latestActivityAt: "2026-01-03T00:00:00Z" })),
  );
  const row = store.getSnapshot().delegates.rows[0];
  expect(row?.status).toBe("completed");
  expect(row?.latestActivityAt).toBe("2026-01-03T00:00:00Z");
});

test("an epoch change wins over an applied row with the same id at a lower revision", async () => {
  const { client, store } = await pushOwner();
  client.emitNotification(
    pushedFrame(
      frameInfo({ projectionRevision: 5, status: "completed", reportPreview: "frame", reportPreviewTruncated: false }),
    ),
  );
  expect(store.getSnapshot().delegates.rows[0]?.reportPreview).toBe("frame");
  client.on("evener/thread/delegates/list", ({ scope }) => ({
    context: pushContext({ epoch: "epoch-2" }),
    scope: scope ?? "session",
    page: { complete: true, issues: [] },
    delegates: [delegateRow({ projectionRevision: 1, status: "running", reportPreview: "replacement" })],
  }));
  await store.refresh("delegates");
  const row = store.getSnapshot().delegates.rows[0];
  expect(row?.projectionRevision).toBe(1);
  expect(row?.reportPreview).toBe("replacement");
  expect(row?.status).toBe("running");
});

// A replacement daemon numbers its hold samples from its own start, so a
// sequence from the retired epoch must not judge a frame from the new one: a
// larger retired number would either suppress a real wait or keep a finished
// one visible. This is the "within the source epoch" half of the ordering rule.
test("a replacement epoch's frame is not judged by the retired hold sequence", async () => {
  const { client, store } = await pushOwner();
  // The old daemon's last sample carries a high sequence while held.
  client.emitNotification(
    pushedFrame(
      frameInfo({
        projectionRevision: 5,
        drainHeldByJobs: true,
        holdSequence: 900,
        runningJobCount: 2,
        oldestRunningJobStartedAt: "2026-11-18T10:00:00Z",
      }),
    ),
  );
  expect(store.getSnapshot().delegates.rows[0]?.holdSequence).toBe(900);

  // The replacement announces its epoch through a summary read, as it does in
  // production, and then frames the row with its own low sequence and no hold.
  client.on("evener/thread/activity/read", ({ scope }) => ({
    ...summaryFixture(scope),
    context: pushContext({ epoch: "epoch-2" }),
  }));
  await store.refresh("summary");
  client.emitNotification(pushedFrame(frameInfo({ projectionRevision: 1, holdSequence: 1 })));
  const row = store.getSnapshot().delegates.rows[0];
  expect(row?.drainHeldByJobs ?? false).toBe(false);
  expect(row?.holdSequence).toBe(1);
  expect(row?.runningJobCount ?? 0).toBe(0);
});

test("extending the epoch comparison to root reads neither loops nor misses the change", async () => {
  const { client, store } = await pushOwner();
  expect(callsTo(client, "evener/thread/delegates/list")).toBe(1);
  client.on("evener/thread/delegates/list", ({ scope }) => ({
    context: pushContext({ epoch: "epoch-2" }),
    scope: scope ?? "session",
    page: { complete: true, issues: [] },
    delegates: [delegateRow({ projectionRevision: 1, status: "failed" })],
  }));
  await store.refresh("delegates");
  // Nothing announced the replacement, so the read that revealed it queues one
  // reconciliation read under the epoch it just adopted. That read sees the
  // same epoch and stops: the demand is bounded by the change, not a loop.
  expect(callsTo(client, "evener/thread/delegates/list")).toBe(3);
  expect(store.getSnapshot().delegates.context?.epoch).toBe("epoch-2");
  expect(store.getSnapshot().delegates.rows[0]?.status).toBe("failed");
});

test("an epoch change retires a buffered unknown-id frame", async () => {
  const client = pushClient();
  const { store } = await pushOwner(client);
  const before = callsTo(client, "evener/thread/delegates/list");
  client.emitNotification(
    pushedFrame(
      frameInfo({ delegateId: "delegate-2", projectionRevision: 10, status: "completed", reportPreview: "buffered" }),
    ),
  );
  await activityState(
    store,
    () => callsTo(client, "evener/thread/delegates/list") === before + 1 && !store.getSnapshot().delegates.loading,
  );
  client.on("evener/thread/delegates/list", ({ scope }) => ({
    context: pushContext({ epoch: "epoch-2" }),
    scope: scope ?? "session",
    page: { complete: true, issues: [] },
    delegates: [
      delegateRow(),
      delegateRow({ delegateId: "delegate-2", projectionRevision: 1, status: "running", reportPreview: "replacement" }),
    ],
  }));
  await store.refresh("delegates");
  const row = store.getSnapshot().delegates.rows.find((candidate) => candidate.delegateId === "delegate-2");
  expect(row?.projectionRevision).toBe(1);
  expect(row?.reportPreview).toBe("replacement");
  expect(row?.status).toBe("running");
});

test("an ineligible context never retires the delegate ordering for another epoch", async () => {
  const client = activityClient();
  client.on("evener/thread/activity/read", ({ scope }) => ({
    ...summaryFixture(scope),
    context: pushContext({ availability: "retained" }),
  }));
  client.on("evener/thread/delegates/list", ({ scope }) => ({
    context: pushContext({ availability: "retained" }),
    scope: scope ?? "session",
    page: { complete: true, issues: [] },
    delegates: [delegateRow()],
  }));
  const store = owner(client);
  store.start();
  store.observe("delegates");
  await activityState(store, () => store.getSnapshot().summary !== null && store.getSnapshot().delegates.complete);
  expect(store.getSnapshot().context?.availability).toBe("retained");
  const before = callsTo(client, "evener/thread/delegates/list");
  // The summary reports a different epoch on a retained context. With the
  // no-read merge off, nothing here merges into the delegate ordering, so the
  // store must not spend a read re-establishing it.
  client.on("evener/thread/activity/read", ({ scope }) => ({
    ...summaryFixture(scope),
    context: pushContext({ availability: "retained", epoch: "epoch-2" }),
  }));
  activityChanged(client, ["summary"]);
  await activityState(store, () => store.getSnapshot().context?.epoch === "epoch-2");
  // A read the summary could have started reaches the wire a turn later, so
  // settle before asserting that none was taken.
  await vi.advanceTimersByTimeAsync(0);
  await activityState(store, () => !store.getSnapshot().delegates.loading);
  expect(callsTo(client, "evener/thread/delegates/list")).toBe(before);
});

test("an epoch change observed only by the summary retires the delegate merge ordering", async () => {
  const client = pushClient();
  const store = owner(client);
  store.start();
  store.observe("delegates");
  await activityState(store, () => store.getSnapshot().summary !== null && store.getSnapshot().delegates.complete);
  client.emitNotification(
    pushedFrame(
      frameInfo({ projectionRevision: 5, status: "completed", reportPreview: "frame", reportPreviewTruncated: false }),
    ),
  );
  expect(store.getSnapshot().delegates.rows[0]?.projectionRevision).toBe(5);
  client.on("evener/thread/activity/read", ({ scope }) => ({
    ...summaryFixture(scope),
    context: pushContext({ epoch: "epoch-2" }),
  }));
  client.on("evener/thread/delegates/list", ({ scope }) => ({
    context: pushContext({ epoch: "epoch-2" }),
    scope: scope ?? "session",
    page: { complete: true, issues: [] },
    delegates: [delegateRow({ projectionRevision: 1, status: "replacement", reportPreview: "replacement" })],
  }));
  activityChanged(client, ["summary"]);
  await activityState(
    store,
    () => store.getSnapshot().delegates.context?.epoch === "epoch-2" && !store.getSnapshot().delegates.loading,
  );
  const row = store.getSnapshot().delegates.rows[0];
  expect(row?.projectionRevision).toBe(1);
  expect(row?.reportPreview).toBe("replacement");
});

test("an epoch replacement after loading multiple pages never seeds the new order with retained rows", async () => {
  const client = pushClient();
  let epoch = "epoch-1";
  // The replacement journal rebuilds projectionRevision from 1, so a row the
  // walk has not re-served yet still carries the retired epoch's revision and
  // its retired report.
  const rowAtCurrentEpoch = (overrides: Partial<SessionDelegate>): SessionDelegate => ({
    ...delegateRow(overrides),
    projectionRevision: epoch === "epoch-1" ? 10 : 1,
    reportPreview: epoch === "epoch-1" ? "retired" : "replacement",
  });
  client.on("evener/thread/delegates/list", ({ cursor, scope }) => ({
    context: pushContext({ epoch }),
    scope: scope ?? "session",
    page: cursor ? { complete: true, issues: [] } : { complete: false, issues: [], nextCursor: "next" },
    delegates: cursor
      ? [rowAtCurrentEpoch({ delegateId: "delegate-2", childRef: "remote:second" })]
      : [rowAtCurrentEpoch({})],
  }));
  const store = owner(client);
  store.observe("delegates");
  await activityState(store, () => store.getSnapshot().delegates.rows.length === 1);
  await store.loadMore("delegates");
  expect(store.getSnapshot().delegates.rows.map((row) => row.reportPreview)).toEqual(["retired", "retired"]);
  epoch = "epoch-2";
  const refreshed = store.refresh("delegates");
  await activityState(store, () => store.getSnapshot().delegates.context?.epoch === "epoch-2");
  // The walk's continuation runs on the paced timer, and the response that
  // revealed the replacement queued one reconciliation read: it re-walks the
  // collection, so it steps on the paced timer too.
  await vi.advanceTimersByTimeAsync(100);
  await vi.advanceTimersByTimeAsync(100);
  await refreshed;
  expect(
    store.getSnapshot().delegates.rows.map((row) => [row.delegateId, row.projectionRevision, row.reportPreview]),
  ).toEqual([
    ["delegate-1", 1, "replacement"],
    ["delegate-2", 1, "replacement"],
  ]);
});

test("a sibling collection catching up to the epoch keeps the current delegate ordering", async () => {
  const client = pushClient();
  client.on("evener/thread/jobs/list", ({ scope }) => ({
    context: pushContext(),
    scope: scope ?? "session",
    page: { complete: true, issues: [] },
    jobs: [jobFixture()],
  }));
  const store = owner(client);
  store.observe("delegates");
  store.observe("jobs");
  await activityState(store, () => store.getSnapshot().delegates.complete && store.getSnapshot().jobs.complete);
  client.on("evener/thread/delegates/list", ({ scope }) => ({
    context: pushContext({ epoch: "epoch-2" }),
    scope: scope ?? "session",
    page: { complete: true, issues: [] },
    delegates: [delegateRow({ projectionRevision: 1, status: "replacement" })],
  }));
  await store.refresh("delegates");
  expect(store.getSnapshot().delegates.context?.epoch).toBe("epoch-2");
  client.emitNotification(pushedFrame(frameInfo({ projectionRevision: 3, status: "running" })));
  expect(store.getSnapshot().delegates.rows[0]).toMatchObject({ projectionRevision: 3, status: "running" });
  client.on("evener/thread/jobs/list", ({ scope }) => ({
    context: pushContext({ epoch: "epoch-2" }),
    scope: scope ?? "session",
    page: { complete: true, issues: [] },
    jobs: [jobFixture()],
  }));
  activityChanged(client, ["jobs"]);
  await activityState(store, () => store.getSnapshot().jobs.context?.epoch === "epoch-2");
  await store.refresh("delegates");
  expect(store.getSnapshot().delegates.rows[0]).toMatchObject({ projectionRevision: 3, status: "running" });
});

test("a calendar-invalid timestamp never wins the activity move", async () => {
  const client = pushClient();
  client.on("evener/thread/delegates/list", ({ scope }) => ({
    context: pushContext(),
    scope: scope ?? "session",
    page: { complete: true, issues: [] },
    delegates: [delegateRow({ projectionRevision: 5, latestActivityAt: "2026-01-01T00:00:00Z" })],
  }));
  const { store } = await pushOwner(client);
  // Date normalizes 31 February into March; the projector's parser rejects it.
  client.emitNotification(
    pushedFrame(frameInfo({ projectionRevision: 3, status: "running", latestActivityAt: "2026-02-31T00:00:00Z" })),
  );
  expect(store.getSnapshot().delegates.rows[0]?.latestActivityAt).toBe("2026-01-01T00:00:00Z");
});

test("a sub-millisecond later activity still advances the move", async () => {
  const client = pushClient();
  client.on("evener/thread/delegates/list", ({ scope }) => ({
    context: pushContext(),
    scope: scope ?? "session",
    page: { complete: true, issues: [] },
    delegates: [delegateRow({ projectionRevision: 5, latestActivityAt: "2026-01-01T00:00:00.000000001Z" })],
  }));
  const { store } = await pushOwner(client);
  // Equal to the millisecond, later by one nanosecond.
  client.emitNotification(
    pushedFrame(
      frameInfo({ projectionRevision: 3, status: "running", latestActivityAt: "2026-01-01T00:00:00.000000002Z" }),
    ),
  );
  expect(store.getSnapshot().delegates.rows[0]?.latestActivityAt).toBe("2026-01-01T00:00:00.000000002Z");
});

test("a timestamp only Date.parse accepts never wins the activity move", async () => {
  const client = pushClient();
  client.on("evener/thread/delegates/list", ({ scope }) => ({
    context: pushContext(),
    scope: scope ?? "session",
    page: { complete: true, issues: [] },
    delegates: [delegateRow({ projectionRevision: 5, latestActivityAt: "2026-01-01T00:00:00Z" })],
  }));
  const { store } = await pushOwner(client);
  // JS parses this one and the projector's RFC3339Nano does not, so it must not
  // advance the stored timestamp.
  client.emitNotification(
    pushedFrame(frameInfo({ projectionRevision: 3, status: "running", latestActivityAt: "2026-01-02 00:00:00" })),
  );
  expect(store.getSnapshot().delegates.rows[0]?.latestActivityAt).toBe("2026-01-01T00:00:00Z");
});

test("a gate change reconciles delegates parked by a permanent read refusal", async () => {
  const client = activityClient();
  client.on("evener/thread/activity/read", ({ scope }) => ({
    ...summaryFixture(scope),
    context: pushContext(),
  }));
  client.on("evener/thread/delegates/list", ({ scope }) => ({
    context: pushContext(),
    scope: scope ?? "session",
    page: { complete: true, issues: [] },
    delegates: [delegateRow({ status: "running" })],
  }));
  const store = owner(client);
  store.start();
  store.observe("delegates");
  await activityState(store, () => store.getSnapshot().summary !== null && store.getSnapshot().delegates.complete);
  client.on("evener/thread/delegates/list", () => {
    throw new WireError("refused", -32602, { evenerErrorInfo: "actionUnavailable" });
  });
  await store.refresh("delegates");
  await activityState(store, () => store.getSnapshot().delegates.permanent);
  client.on("evener/thread/activity/read", ({ scope }) => ({
    ...summaryFixture(scope),
    context: pushContext({ availability: "retained" }),
  }));
  client.on("evener/thread/delegates/list", ({ scope }) => ({
    context: pushContext({ availability: "retained" }),
    scope: scope ?? "session",
    page: { complete: true, issues: [] },
    delegates: [delegateRow({ status: "failed" })],
  }));
  const before = callsTo(client, "evener/thread/delegates/list");
  activityChanged(client, ["summary"]);
  // The gate turning ineligible is a new condition, so the parked resource is
  // reconciled rather than skipped for the rest of the store's life.
  await activityState(
    store,
    () => store.getSnapshot().delegates.rows[0]?.status === "failed" && !store.getSnapshot().delegates.loading,
  );
  expect(callsTo(client, "evener/thread/delegates/list")).toBeGreaterThan(before);
});

test("an unparseable activity never displaces a parseable one", async () => {
  const client = pushClient();
  client.on("evener/thread/delegates/list", ({ scope }) => ({
    context: pushContext(),
    scope: scope ?? "session",
    page: { complete: true, issues: [] },
    delegates: [delegateRow({ latestActivityAt: "2026-01-01T00:00:00Z" })],
  }));
  const { store } = await pushOwner(client);
  client.emitNotification(
    pushedFrame(frameInfo({ projectionRevision: 1, status: "completed", latestActivityAt: "not-a-date" })),
  );
  expect(store.getSnapshot().delegates.rows[0]?.latestActivityAt).toBe("2026-01-01T00:00:00Z");
});

test("a parseable activity displaces an unparseable one at a higher revision", async () => {
  const client = pushClient();
  client.on("evener/thread/delegates/list", ({ scope }) => ({
    context: pushContext(),
    scope: scope ?? "session",
    page: { complete: true, issues: [] },
    delegates: [delegateRow({ latestActivityAt: "not-a-date" })],
  }));
  const { store } = await pushOwner(client);
  client.emitNotification(
    pushedFrame(frameInfo({ projectionRevision: 2, status: "completed", latestActivityAt: "2026-02-02T00:00:00Z" })),
  );
  expect(store.getSnapshot().delegates.rows[0]?.latestActivityAt).toBe("2026-02-02T00:00:00Z");
});

test("a read that does not serve a buffered frame's identity leaves it waiting without further reads", async () => {
  const client = pushClient();
  let admit = false;
  client.on("evener/thread/delegates/list", ({ scope }) => ({
    context: pushContext(),
    scope: scope ?? "session",
    page: { complete: true, issues: [] },
    delegates: admit
      ? [
          delegateRow({ delegateId: "delegate-1" }),
          delegateRow({ delegateId: "delegate-2", childRef: "local:first", status: "running" }),
          delegateRow({ delegateId: "delegate-2", childRef: "local:second", status: "running" }),
        ]
      : [delegateRow({ delegateId: "delegate-1" })],
  }));
  const { store } = await pushOwner(client);
  const before = callsTo(client, "evener/thread/delegates/list");
  client.emitNotification(
    pushedFrame(
      frameInfo({ delegateId: "delegate-2", childRef: "local:third", projectionRevision: 9, status: "failed" }),
    ),
  );
  await activityState(store, () => callsTo(client, "evener/thread/delegates/list") === before + 1);
  admit = true;
  await store.refresh("delegates");
  const twins = () =>
    store
      .getSnapshot()
      .delegates.rows.filter((row) => row.delegateId === "delegate-2")
      .map((row) => row.status);
  // The read served two rows, and neither carries the identity the frame named:
  // it stays buffered rather than being written into a child it cannot name.
  expect(twins()).toEqual(["running", "running"]);
  const settled = callsTo(client, "evener/thread/delegates/list");
  // The frame asked once for its identity (#4258): a repeat of the same frame
  // and a strictly newer one ask for no further read, because the read that
  // already ran did not serve it and no id-based ladder can re-address it.
  client.emitNotification(
    pushedFrame(
      frameInfo({ delegateId: "delegate-2", childRef: "local:third", projectionRevision: 9, status: "failed" }),
    ),
  );
  client.emitNotification(
    pushedFrame(
      frameInfo({ delegateId: "delegate-2", childRef: "local:third", projectionRevision: 10, status: "failed" }),
    ),
  );
  await vi.advanceTimersByTimeAsync(0);
  expect(callsTo(client, "evener/thread/delegates/list")).toBe(settled);
  expect(twins()).toEqual(["running", "running"]);
});

test("a frame whose identity matches neither of two rows under a shared id patches neither and asks once", async () => {
  const client = pushClient();
  client.on("evener/thread/delegates/list", ({ scope }) => ({
    context: pushContext(),
    scope: scope ?? "session",
    page: { complete: true, issues: [] },
    delegates: [
      delegateRow({ delegateId: "delegate-1", childRef: "local:first", status: "running" }),
      delegateRow({ delegateId: "delegate-1", childRef: "local:second", status: "running" }),
    ],
  }));
  const { store } = await pushOwner(client);
  expect(store.getSnapshot().delegates.rows).toHaveLength(2);
  const before = callsTo(client, "evener/thread/delegates/list");
  client.emitNotification(
    pushedFrame(
      frameInfo({ delegateId: "delegate-1", childRef: "local:third", projectionRevision: 9, status: "failed" }),
    ),
  );
  expect(store.getSnapshot().delegates.rows.map((row) => row.status)).toEqual(["running", "running"]);
  // The frame's identity matches neither loaded row, so it patches neither and
  // asks for a read instead.
  await activityState(store, () => callsTo(client, "evener/thread/delegates/list") === before + 1);
  // A later frame naming a loaded row's identity patches exactly that row with
  // no further read: the shared delegate id alone decides nothing.
  client.emitNotification(
    pushedFrame(
      frameInfo({ delegateId: "delegate-1", childRef: "local:second", projectionRevision: 9, status: "failed" }),
    ),
  );
  await vi.advanceTimersByTimeAsync(0);
  expect(store.getSnapshot().delegates.rows.map((row) => row.status)).toEqual(["running", "failed"]);
  expect(callsTo(client, "evener/thread/delegates/list")).toBe(before + 1);
});

// #4258 item 5: the frame carries the same child ref the read row's childRef
// keys on, so it names its row identity directly and the id-ambiguity cases
// retire in favor of identity matching.

test("a frame carrying its child ref patches the row its identity names under a shared delegate id", async () => {
  const client = pushClient();
  client.on("evener/thread/delegates/list", ({ scope }) => ({
    context: pushContext(),
    scope: scope ?? "session",
    page: { complete: true, issues: [] },
    delegates: [
      delegateRow({ childRef: "local:first", status: "running" }),
      delegateRow({ childRef: "local:second", status: "running" }),
    ],
  }));
  const { store } = await pushOwner(client);
  expect(store.getSnapshot().delegates.rows).toHaveLength(2);
  const before = callsTo(client, "evener/thread/delegates/list");
  client.emitNotification(
    pushedFrame(frameInfo({ childRef: "local:second", projectionRevision: 9, status: "failed" })),
  );
  expect(store.getSnapshot().delegates.rows.map((row) => row.status)).toEqual(["running", "failed"]);
  // The frame named its row, so no read was needed to place it.
  expect(callsTo(client, "evener/thread/delegates/list")).toBe(before);
});

test("two children sharing an unknown delegate id each place their own frame after one read", async () => {
  const client = pushClient();
  let admit = false;
  client.on("evener/thread/delegates/list", ({ scope }) => ({
    context: pushContext(),
    scope: scope ?? "session",
    page: { complete: true, issues: [] },
    delegates: admit
      ? [
          delegateRow(),
          delegateRow({ delegateId: "delegate-2", childRef: "local:first", status: "running" }),
          delegateRow({ delegateId: "delegate-2", childRef: "local:second", status: "running" }),
        ]
      : [delegateRow()],
  }));
  const { store } = await pushOwner(client);
  const before = callsTo(client, "evener/thread/delegates/list");
  client.emitNotification(
    pushedFrame(
      frameInfo({ delegateId: "delegate-2", childRef: "local:first", projectionRevision: 9, status: "failed" }),
    ),
  );
  client.emitNotification(
    pushedFrame(
      frameInfo({ delegateId: "delegate-2", childRef: "local:second", projectionRevision: 9, status: "failed" }),
    ),
  );
  // The two frames name two identities, so their reads coalesce into one.
  await activityState(store, () => callsTo(client, "evener/thread/delegates/list") === before + 1);
  admit = true;
  await store.refresh("delegates");
  const twins = () =>
    store
      .getSnapshot()
      .delegates.rows.filter((row) => row.delegateId === "delegate-2")
      .map((row) => row.status);
  expect(twins()).toEqual(["failed", "failed"]);
});

test("a frame matching a loaded identity patches immediately while the collection read continues", async () => {
  const client = pushClient();
  let first = true;
  const second = deferred<SessionDelegatesResponse>();
  client.on("evener/thread/delegates/list", ({ cursor, scope }) =>
    first
      ? {
          context: pushContext(),
          scope: scope ?? "session",
          page: { complete: true, issues: [] },
          delegates: [delegateRow()],
        }
      : cursor
        ? second.promise
        : {
            context: pushContext(),
            scope: scope ?? "session",
            page: { complete: false, issues: [], nextCursor: "next" },
            // The walk's first page serves a new row, not the displayed
            // boundary: the row set is not final while the walk continues.
            delegates: [delegateRow({ delegateId: "delegate-2", childRef: "local:other", status: "running" })],
          },
  );
  const store = owner(client);
  store.observe("delegates");
  await activityState(store, () => store.getSnapshot().delegates.complete);
  first = false;
  activityChanged(client, ["delegates"]);
  await activityState(
    store,
    () =>
      store.getSnapshot().delegates.loading &&
      store.getSnapshot().delegates.hasMore &&
      callsTo(client, "evener/thread/delegates/list") === 2,
  );
  const midWalk = callsTo(client, "evener/thread/delegates/list");
  client.emitNotification(pushedFrame(frameInfo({ projectionRevision: 9, status: "failed" })));
  // The frame named the loaded row's identity, so it is the row's own frame
  // already: no read-finality proof can reassign it to another child.
  const target = () => store.getSnapshot().delegates.rows.find((row) => row.delegateId === "delegate-1")?.status;
  expect(target()).toBe("failed");
  expect(callsTo(client, "evener/thread/delegates/list")).toBe(midWalk);
  second.resolve({
    context: pushContext(),
    scope: "session",
    page: { complete: true, issues: [] },
    delegates: [delegateRow({ status: "running" })],
  });
  // The walk's continuation waits on its 100 ms pace: step the clock so the
  // page lands and the walk reconciles.
  await vi.advanceTimersByTimeAsync(200);
  await activityState(store, () => !store.getSnapshot().delegates.loading);
  expect(target()).toBe("failed");
});

test("a frame without a child ref patches nothing and asks one read per newer revision per id", async () => {
  const client = pushClient();
  // The read's answer ages with the producer: the first read still serves the
  // pre-frame snapshot, and the read a strictly newer frame asks for serves the
  // settlement that frame carried.
  let settledGeneration = false;
  client.on("evener/thread/delegates/list", ({ scope }) => ({
    context: pushContext(),
    scope: scope ?? "session",
    page: { complete: true, issues: [] },
    delegates: [
      settledGeneration
        ? delegateRow({ status: "failed", projectionRevision: 10, reportPreview: "settled" })
        : delegateRow({ status: "completed" }),
    ],
  }));
  const { store } = await pushOwner(client);
  expect(store.getSnapshot().delegates.rows[0]?.status).toBe("completed");
  const before = callsTo(client, "evener/thread/delegates/list");
  // A running frame from an older producer cannot name its row: the read it
  // asks for is the only place its change can land, and the collection still
  // serves the pre-frame snapshot.
  client.emitNotification(
    pushedFrame({ ...frameInfo({ projectionRevision: 9, status: "running" }), childRef: undefined }),
  );
  expect(store.getSnapshot().delegates.rows[0]?.status).toBe("completed");
  await activityState(store, () => callsTo(client, "evener/thread/delegates/list") === before + 1);
  expect(store.getSnapshot().delegates.rows[0]?.status).toBe("completed");
  const asked = callsTo(client, "evener/thread/delegates/list");
  // A repeat at the same revision is the same news: it asks for nothing.
  client.emitNotification(
    pushedFrame({ ...frameInfo({ projectionRevision: 9, status: "running" }), childRef: undefined }),
  );
  await vi.advanceTimersByTimeAsync(0);
  expect(callsTo(client, "evener/thread/delegates/list")).toBe(asked);
  // The terminal frame is strictly newer news than the read the running frame
  // asked for, and the producer cannot name the row itself: one more read is
  // the only way the row's status and report stop being stale.
  settledGeneration = true;
  client.emitNotification(
    pushedFrame({
      ...frameInfo({ projectionRevision: 10, status: "failed", reportPreview: "settled" }),
      childRef: undefined,
    }),
  );
  await store.settleForTests("delegates");
  expect(store.getSnapshot().delegates.rows[0]?.status).toBe("failed");
  expect(store.getSnapshot().delegates.rows[0]?.reportPreview).toBe("settled");
  expect(callsTo(client, "evener/thread/delegates/list")).toBe(asked + 1);
});

test("a legacy frame's advancing activity lands on its one row without a newer revision", async () => {
  const client = pushClient();
  // The producer's activity publications carry no durable revision change:
  // ReportActivityPhase advances latestActivityAt alone, and the read's
  // answer here stays at the pre-activity snapshot the way a page captured
  // before the report would.
  client.on("evener/thread/delegates/list", ({ scope }) => ({
    context: pushContext(),
    scope: scope ?? "session",
    page: { complete: true, issues: [] },
    delegates: [delegateRow({ status: "running", projectionRevision: 1, latestActivityAt: "2026-09-30T12:00:00Z" })],
  }));
  const { store } = await pushOwner(client);
  expect(store.getSnapshot().delegates.rows[0]?.latestActivityAt).toBe("2026-09-30T12:00:00Z");
  // The first frame asks for the one read its id is owed, and its heartbeat
  // lands while the read is still the pre-activity snapshot.
  client.emitNotification(
    pushedFrame({
      ...frameInfo({ projectionRevision: 1, latestActivityAt: "2026-09-30T12:00:01Z" }),
      childRef: undefined,
    }),
  );
  await store.settleForTests("delegates");
  expect(store.getSnapshot().delegates.rows[0]?.latestActivityAt).toBe("2026-09-30T12:00:01Z");
  const reads = callsTo(client, "evener/thread/delegates/list");
  // A streaming legacy delegate keeps reporting activity at the same revision:
  // the heartbeat must land without a read the equal-revision ladder would
  // never ask for, or the row would read as quiet while its child works.
  client.emitNotification(
    pushedFrame({
      ...frameInfo({ projectionRevision: 1, latestActivityAt: "2026-09-30T12:00:02Z" }),
      childRef: undefined,
    }),
  );
  await vi.advanceTimersByTimeAsync(0);
  expect(store.getSnapshot().delegates.rows[0]?.latestActivityAt).toBe("2026-09-30T12:00:02Z");
  expect(callsTo(client, "evener/thread/delegates/list")).toBe(reads);
});

test("a paged collection's advancing heartbeats share one paced read", async () => {
  const client = pushClient();
  // A producer that holds only part of the roster pages its answers: the row
  // is loaded but the collection never reaches its end, so a heartbeat cannot
  // be attributed to the row directly -- only a read's join can move its
  // activity -- and the answer carries the activity the hub holds now.
  let served = "2026-09-30T12:00:00Z";
  client.on("evener/thread/delegates/list", ({ scope }) => ({
    context: pushContext(),
    scope: scope ?? "session",
    page: { complete: false, issues: [], nextCursor: "next" },
    delegates: [delegateRow({ status: "running", projectionRevision: 1, latestActivityAt: served })],
  }));
  const store = owner(client);
  store.observe("delegates");
  await activityState(store, () => store.getSnapshot().delegates.rows.length === 1);
  expect(store.getSnapshot().delegates.complete).toBe(false);
  // The first frame spends the id's one ladder read; its answer predates the
  // activity the hub carries by the time the stream reports again.
  client.emitNotification(
    pushedFrame({
      ...frameInfo({ projectionRevision: 1, latestActivityAt: "2026-09-30T12:00:01Z" }),
      childRef: undefined,
    }),
  );
  await store.settleForTests("delegates");
  const activity = () => store.getSnapshot().delegates.rows[0]?.latestActivityAt;
  expect(activity()).toBe("2026-09-30T12:00:00Z");
  const reads = callsTo(client, "evener/thread/delegates/list");
  // A streaming legacy delegate reports activity twice at the same revision
  // while the hub's answer moves with it: both heartbeats are demand for one
  // coalesced read, and the read lands the activity the hub holds now -- the
  // row must not read as quiet while its child works.
  served = "2026-09-30T12:00:02Z";
  client.emitNotification(
    pushedFrame({
      ...frameInfo({ projectionRevision: 1, latestActivityAt: "2026-09-30T12:00:02Z" }),
      childRef: undefined,
    }),
  );
  served = "2026-09-30T12:00:03Z";
  client.emitNotification(
    pushedFrame({
      ...frameInfo({ projectionRevision: 1, latestActivityAt: "2026-09-30T12:00:03Z" }),
      childRef: undefined,
    }),
  );
  await vi.advanceTimersByTimeAsync(2000);
  expect(activity()).toBe("2026-09-30T12:00:03Z");
  expect(callsTo(client, "evener/thread/delegates/list")).toBe(reads + 1);
  // A repeat that moves nothing is no demand: the read it would ask for has
  // nothing new to serve.
  client.emitNotification(
    pushedFrame({
      ...frameInfo({ projectionRevision: 1, latestActivityAt: "2026-09-30T12:00:03Z" }),
      childRef: undefined,
    }),
  );
  await vi.advanceTimersByTimeAsync(2000);
  expect(activity()).toBe("2026-09-30T12:00:03Z");
  expect(callsTo(client, "evener/thread/delegates/list")).toBe(reads + 1);
});

test("a malformed legacy heartbeat cannot poison a blank row's activity", async () => {
  const { client, store } = await pushOwner();
  const activity = () => store.getSnapshot().delegates.rows[0]?.latestActivityAt;
  expect(activity()).toBeUndefined();
  // A malformed timestamp is nonblank, so the blank-current rule alone would
  // take it -- and once it stuck, no later heartbeat could compare as later
  // against a value that cannot parse, and no equal-revision frame asks a
  // read. The modern frame path admits a timestamp only when the projector
  // can parse it; the legacy heartbeat must leave the row alone the same way.
  client.emitNotification(
    pushedFrame({
      ...frameInfo({ projectionRevision: 1, latestActivityAt: "not a timestamp" }),
      childRef: undefined,
    }),
  );
  await store.settleForTests("delegates");
  expect(activity()).toBeUndefined();
  // The valid heartbeat that follows at the same revision still lands.
  client.emitNotification(
    pushedFrame({
      ...frameInfo({ projectionRevision: 1, latestActivityAt: "2026-09-30T12:00:01Z" }),
      childRef: undefined,
    }),
  );
  expect(activity()).toBe("2026-09-30T12:00:01Z");
});

test("a legacy heartbeat from an unloaded twin does not advance the visible child", async () => {
  const client = pushClient();
  // The collection holds two rows under one delegate id; only the first page
  // is loaded, so the loaded set cannot prove which child a legacy frame's
  // heartbeat belongs to.
  client.on("evener/thread/delegates/list", ({ cursor, scope }) => ({
    context: pushContext(),
    scope: scope ?? "session",
    page: cursor ? { complete: true, issues: [] } : { complete: false, issues: [], nextCursor: "next" },
    delegates: cursor
      ? [delegateRow({ childRef: "local:second", latestActivityAt: "2026-09-30T12:00:00Z" })]
      : [delegateRow({ childRef: "local:first", latestActivityAt: "2026-09-30T12:00:00Z" })],
  }));
  const store = owner(client);
  store.observe("delegates");
  await activityState(store, () => store.getSnapshot().delegates.rows.length === 1);
  const visible = () =>
    store.getSnapshot().delegates.rows.find((row) => row.childRef === "local:first")?.latestActivityAt;
  expect(visible()).toBe("2026-09-30T12:00:00Z");
  const reads = callsTo(client, "evener/thread/delegates/list");
  // The heartbeat names an id the loaded set cannot attribute to one child:
  // the second child is unloaded, and its frame could be the twin's.
  client.emitNotification(
    pushedFrame({
      ...frameInfo({ delegateId: "delegate-1", projectionRevision: 1, latestActivityAt: "2026-09-30T12:00:09Z" }),
      childRef: undefined,
    }),
  );
  await store.settleForTests("delegates");
  expect(visible()).toBe("2026-09-30T12:00:00Z");
  // The id's one ladder read is still owed and still asked for; the paged
  // answer keeps the visible child's heartbeat where the hub left it.
  expect(callsTo(client, "evener/thread/delegates/list")).toBe(reads + 1);
  expect(store.getSnapshot().delegates.rows).toHaveLength(1);
});

test("a frame without a child ref arriving during a walk asks for its read once the walk settles", async () => {
  const client = pushClient();
  const store = owner(client);
  store.start();
  store.observe("delegates");
  await activityState(store, () => store.getSnapshot().summary !== null && store.getSnapshot().delegates.complete);
  const initial = callsTo(client, "evener/thread/delegates/list");
  // A user refresh walks the collection: page one serves a row the display
  // has not reached, and the continuation page is held in flight.
  const continuation = deferred<SessionDelegatesResponse>();
  client.on("evener/thread/delegates/list", ({ cursor, scope }) =>
    cursor
      ? continuation.promise
      : {
          context: pushContext(),
          scope: scope ?? "session",
          page: { complete: false, issues: [], nextCursor: "next" },
          delegates: [delegateRow({ delegateId: "delegate-2", status: "running" })],
        },
  );
  const refreshed = store.refresh("delegates");
  await vi.advanceTimersByTimeAsync(200);
  expect(callsTo(client, "evener/thread/delegates/list")).toBe(initial + 2);
  expect(store.getSnapshot().delegates.rows.length).toBe(2);
  // An older producer's frame cannot be buffered for a walk to settle, so the
  // walk in flight must not swallow the one read its change can ride on.
  client.emitNotification(
    pushedFrame({ ...frameInfo({ projectionRevision: 9, status: "failed" }), childRef: undefined }),
  );
  // The walk's pages predate the frame, so they carry the pre-frame snapshot;
  // the read the frame asked for re-serves the collection with its change.
  client.on("evener/thread/delegates/list", ({ scope }) => ({
    context: pushContext(),
    scope: scope ?? "session",
    page: { complete: true, issues: [] },
    delegates: [
      delegateRow({ delegateId: "delegate-2", status: "running" }),
      delegateRow({ status: "failed", projectionRevision: 9 }),
    ],
  }));
  continuation.resolve({
    context: pushContext(),
    scope: "session",
    page: { complete: true, issues: [] },
    delegates: [delegateRow({ delegateId: "delegate-2", status: "running" }), delegateRow({ status: "completed" })],
  });
  await refreshed;
  await vi.advanceTimersByTimeAsync(200);
  expect(callsTo(client, "evener/thread/delegates/list")).toBe(initial + 3);
  const row = store.getSnapshot().delegates.rows.find((candidate) => candidate.delegateId === "delegate-1");
  expect(row?.status).toBe("failed");
});

test("a frame for a delegate created after a walk's membership froze is read once the walk finishes", async () => {
  const client = pushClient();
  const store = owner(client);
  store.observe("delegates");
  await activityState(store, () => store.getSnapshot().delegates.complete);
  // A user refresh walks the collection: its root page is served at once and
  // the continuation page is held in flight. The answers age with the
  // collection: the walk's pages predate the new delegate, and only the read
  // that follows the walk serves it.
  const continuation = deferred<SessionDelegatesResponse>();
  let admitted = false;
  client.on("evener/thread/delegates/list", ({ cursor, scope }) => {
    if (cursor) return continuation.promise;
    return admitted
      ? {
          context: pushContext(),
          scope: scope ?? "session",
          page: { complete: true, issues: [] },
          delegates: [
            delegateRow({ delegateId: "delegate-2", childRef: "remote:delegate-2", status: "running" }),
            delegateRow({ status: "running" }),
          ],
        }
      : {
          context: pushContext(),
          scope: scope ?? "session",
          page: { complete: false, issues: [], nextCursor: "next" },
          // The walk's first page serves a row the display has not reached,
          // so the walk keeps going and stays in flight.
          delegates: [delegateRow({ delegateId: "delegate-3", childRef: "remote:delegate-3", status: "running" })],
        };
  });
  const refreshed = store.refresh("delegates");
  await vi.advanceTimersByTimeAsync(0);
  expect(store.getSnapshot().delegates.loading).toBe(true);
  // The walk froze its membership before this delegate existed (the hub's
  // capture holds one highwater per walk), so no page it serves can carry the
  // row: the frame it pushes is buffered, and the read that can admit the row
  // must not restart the walk it arrives in.
  client.emitNotification(
    pushedFrame(
      frameInfo({
        delegateId: "delegate-2",
        childRef: "remote:delegate-2",
        projectionRevision: 9,
        status: "failed",
        reportPreview: "settled",
      }),
    ),
  );
  const midWalk = callsTo(client, "evener/thread/delegates/list");
  expect(store.getSnapshot().delegates.loading).toBe(true);
  // The collection admits the new delegate; the walk's own pages predate it,
  // so they settle without it.
  admitted = true;
  continuation.resolve({
    context: pushContext(),
    scope: "session",
    page: { complete: true, issues: [] },
    // The continuation page reaches the displayed boundary, ending the walk.
    delegates: [delegateRow({ status: "running" })],
  });
  await vi.advanceTimersByTimeAsync(200);
  await refreshed;
  // The walk finished without the row, so the one coalesced read the frame
  // was owed runs now and admits it.
  expect(callsTo(client, "evener/thread/delegates/list")).toBe(midWalk + 2);
  const row = store.getSnapshot().delegates.rows.find((candidate) => candidate.delegateId === "delegate-2");
  expect(row?.status).toBe("failed");
  expect(row?.reportPreview).toBe("settled");
});

// A view that releases the collection while the walk a buffered frame waits
// on is settling (a listener unsubscribing the last observer as the walk's
// final page publishes) takes the owed follow-up read with it: the frames
// stay buffered for whoever observes next, and an unobserved collection
// neither issues that read nor publishes its page.
test("a release during the walk's settling publish takes the follow-up read with it", async () => {
  const client = pushClient();
  const store = owner(client);
  const leave = store.observe("delegates");
  await activityState(store, () => store.getSnapshot().delegates.complete);
  const continuation = deferred<SessionDelegatesResponse>();
  let admitted = false;
  client.on("evener/thread/delegates/list", ({ cursor, scope }) => {
    if (cursor) return continuation.promise;
    return admitted
      ? {
          context: pushContext(),
          scope: scope ?? "session",
          page: { complete: true, issues: [] },
          delegates: [
            delegateRow({ delegateId: "delegate-2", childRef: "remote:delegate-2", status: "running" }),
            delegateRow({ status: "running" }),
          ],
        }
      : {
          context: pushContext(),
          scope: scope ?? "session",
          page: { complete: false, issues: [], nextCursor: "next" },
          delegates: [delegateRow({ delegateId: "delegate-3", childRef: "remote:delegate-3", status: "running" })],
        };
  });
  const refreshed = store.refresh("delegates");
  await vi.advanceTimersByTimeAsync(0);
  expect(store.getSnapshot().delegates.loading).toBe(true);
  // The walk froze its membership before this delegate existed, so its pages
  // cannot serve the row: the frame is buffered, and one read is owed once
  // the walk finishes.
  client.emitNotification(
    pushedFrame(
      frameInfo({
        delegateId: "delegate-2",
        childRef: "remote:delegate-2",
        projectionRevision: 9,
        status: "failed",
        reportPreview: "settled",
      }),
    ),
  );
  admitted = true;
  const settling = store.getSnapshot().delegates;
  let released = 0;
  const stop = store.subscribe(() => {
    if (store.getSnapshot().delegates !== settling) {
      stop();
      released = callsTo(client, "evener/thread/delegates/list");
      leave();
    }
  });
  continuation.resolve({
    context: pushContext(),
    scope: "session",
    page: { complete: true, issues: [] },
    delegates: [delegateRow({ status: "running" })],
  });
  await vi.advanceTimersByTimeAsync(200);
  await refreshed;
  stop();
  expect(callsTo(client, "evener/thread/delegates/list")).toBe(released);
  expect(store.getSnapshot().delegates.rows.find((row) => row.delegateId === "delegate-2")).toBeUndefined();
});

test("repeated frames for an identity no walk can admit defer one follow-up read in total", async () => {
  const client = pushClient();
  let hold: 0 | 1 | 2 = 0;
  const held = deferred<SessionDelegatesResponse>();
  const heldTwo = deferred<SessionDelegatesResponse>();
  client.on("evener/thread/delegates/list", ({ cursor, scope }) => {
    if (!cursor && hold === 1) return held.promise;
    if (!cursor && hold === 2) return heldTwo.promise;
    return {
      context: pushContext(),
      scope: scope ?? "session",
      page: cursor ? { complete: true, issues: [] } : { complete: false, issues: [], nextCursor: "older" },
      // The delegate the frames name sorts beyond the displayed boundary, so
      // no root walk to that boundary can ever serve its row.
      delegates: cursor ? [] : [delegateRow({ status: "running" })],
    };
  });
  const store = owner(client);
  store.observe("delegates");
  await activityState(store, () => store.getSnapshot().delegates.rows.length === 1);
  const initial = callsTo(client, "evener/thread/delegates/list");
  // Walk one is held in flight while a frame for the beyond-boundary identity
  // arrives; its identity's read is deferred to the walk's end.
  hold = 1;
  const refreshedOne = store.refresh("delegates");
  await vi.advanceTimersByTimeAsync(0);
  client.emitNotification(
    pushedFrame(
      frameInfo({ delegateId: "delegate-9", childRef: "remote:delegate-9", projectionRevision: 9, status: "running" }),
    ),
  );
  hold = 0;
  held.resolve({
    context: pushContext(),
    scope: "session",
    page: { complete: false, issues: [], nextCursor: "older" },
    delegates: [delegateRow({ status: "running" })],
  });
  await refreshedOne;
  // The deferred read ran once: walk one's page plus its follow-up.
  expect(callsTo(client, "evener/thread/delegates/list")).toBe(initial + 2);
  // Walk two is held the same way, and the same identity frames again at a
  // newer revision: its one deferred read is spent, so no walk after it arms
  // another on its behalf.
  hold = 2;
  const refreshedTwo = store.refresh("delegates");
  await vi.advanceTimersByTimeAsync(0);
  client.emitNotification(
    pushedFrame(
      frameInfo({ delegateId: "delegate-9", childRef: "remote:delegate-9", projectionRevision: 10, status: "running" }),
    ),
  );
  hold = 0;
  heldTwo.resolve({
    context: pushContext(),
    scope: "session",
    page: { complete: false, issues: [], nextCursor: "older" },
    delegates: [delegateRow({ status: "running" })],
  });
  await refreshedTwo;
  expect(callsTo(client, "evener/thread/delegates/list")).toBe(initial + 3);
  // The identity stays beyond the boundary: nothing was loaded for it.
  expect(store.getSnapshot().delegates.rows).toHaveLength(1);
});

test("a frame buffered during the replacement window survives the replacement read", async () => {
  const client = activityClient();
  client.on("evener/thread/activity/read", ({ scope }) => ({
    ...summaryFixture(scope),
    context: pushContext(),
  }));
  client.on("evener/thread/delegates/list", ({ scope }) => ({
    context: pushContext(),
    scope: scope ?? "session",
    page: { complete: true, issues: [] },
    delegates: [delegateRow({ status: "running" })],
  }));
  const store = owner(client);
  store.start();
  store.observe("delegates");
  await activityState(store, () => store.getSnapshot().summary !== null && store.getSnapshot().delegates.complete);
  // The summary reports the replacement and its forced delegates read is left in
  // flight; a frame then arrives for a delegate the read will admit.
  const reconciled = deferred<SessionDelegatesResponse>();
  client.on("evener/thread/activity/read", ({ scope }) => ({
    ...summaryFixture(scope),
    context: pushContext({ epoch: "epoch-2" }),
  }));
  client.on("evener/thread/delegates/list", () => reconciled.promise);
  activityChanged(client, ["summary"]);
  await activityState(store, () => store.getSnapshot().context?.epoch === "epoch-2");
  await vi.advanceTimersByTimeAsync(0);
  expect(store.getSnapshot().delegates.loading).toBe(true);
  client.emitNotification(
    pushedFrame(
      frameInfo({ delegateId: "delegate-2", projectionRevision: 9, status: "completed", reportPreview: "settled" }),
    ),
  );
  reconciled.resolve({
    context: pushContext({ epoch: "epoch-2" }),
    scope: "session",
    page: { complete: true, issues: [] },
    delegates: [delegateRow({ status: "running" }), delegateRow({ delegateId: "delegate-2", status: "running" })],
  });
  await activityState(store, () => !store.getSnapshot().delegates.loading);
  const row = store.getSnapshot().delegates.rows.find((candidate) => candidate.delegateId === "delegate-2");
  expect(row?.status).toBe("completed");
});

test("a gate that turns on recovers a permanently refused delegates read", async () => {
  const client = activityClient();
  client.on("evener/thread/activity/read", ({ scope }) => ({
    ...summaryFixture(scope),
    context: pushContext({ availability: "retained" }),
  }));
  client.on("evener/thread/delegates/list", ({ scope }) => ({
    context: pushContext({ availability: "retained" }),
    scope: scope ?? "session",
    page: { complete: true, issues: [] },
    delegates: [delegateRow({ status: "running" })],
  }));
  const store = owner(client);
  store.start();
  store.observe("delegates");
  await activityState(store, () => store.getSnapshot().summary !== null && store.getSnapshot().delegates.complete);
  // A permanent refusal parks the resource while the source is retained.
  client.on("evener/thread/delegates/list", () => {
    throw new WireError("refused", -32602, { evenerErrorInfo: "actionUnavailable" });
  });
  await store.refresh("delegates");
  await activityState(store, () => store.getSnapshot().delegates.permanent);
  // The gate turns on, so the parked read must be retried rather than left
  // refused for the rest of the store's life.
  client.on("evener/thread/activity/read", ({ scope }) => ({
    ...summaryFixture(scope),
    context: pushContext(),
  }));
  client.on("evener/thread/delegates/list", ({ scope }) => ({
    context: pushContext(),
    scope: scope ?? "session",
    page: { complete: true, issues: [] },
    delegates: [delegateRow({ status: "failed" })],
  }));
  activityChanged(client, ["summary"]);
  await activityState(store, () => store.getSnapshot().delegates.rows[0]?.status === "failed");
});

test("a page that first observes a session replacement restarts instead of adopting its rows", async () => {
  const client = pushClient();
  client.on("evener/thread/delegates/list", ({ cursor, scope }) =>
    cursor
      ? {
          context: { ...pushContext(), sessionId: "replacement", ref: "remote:new" },
          scope: scope ?? "session",
          page: { complete: true, issues: [] },
          delegates: [delegateRow({ delegateId: "stale", status: "failed" })],
        }
      : {
          context: pushContext(),
          scope: scope ?? "session",
          page: { complete: false, issues: [], nextCursor: "next" },
          delegates: [delegateRow({ status: "running" })],
        },
  );
  const store = owner(client);
  store.observe("delegates");
  await activityState(store, () => store.getSnapshot().delegates.rows.length === 1);
  await store.loadMore("delegates");
  // The continuation page belongs to a replacement session, so its row is never
  // adopted as that session's own first page.
  expect(store.getSnapshot().delegates.rows.some((row) => row.delegateId === "stale")).toBe(false);
});

test("a re-served delegate id on a later page keeps both served rows", async () => {
  const client = pushClient();
  client.on("evener/thread/delegates/list", ({ cursor, scope }) => ({
    context: pushContext(),
    scope: scope ?? "session",
    page: { complete: false, issues: [], nextCursor: cursor ? undefined : "next" },
    delegates: cursor
      ? [delegateRow({ delegateId: "delegate-1", childRef: "local:second", status: "failed" })]
      : [delegateRow({ delegateId: "delegate-1", childRef: "local:first", status: "running" })],
  }));
  const store = owner(client);
  store.observe("delegates");
  await activityState(store, () => store.getSnapshot().delegates.rows.length === 1);
  await store.loadMore("delegates");
  expect(store.getSnapshot().delegates.rows.map((row) => row.status)).toEqual(["running", "failed"]);
});

test("an evicted unknown-delegate id can trigger a read again", async () => {
  const { client, store } = await pushOwner();
  // One frame per distinct unknown id: the buffer holds the latest 128, so
  // emitting 129 evicts the first.
  for (let index = 0; index <= 128; index += 1) {
    client.emitNotification(pushedFrame(frameInfo({ delegateId: `unknown-${index}`, projectionRevision: 2 })));
  }
  await activityState(store, () => !store.getSnapshot().delegates.loading && !store.getSnapshot().delegates.pending);
  const before = callsTo(client, "evener/thread/delegates/list");
  client.emitNotification(pushedFrame(frameInfo({ delegateId: "unknown-0", projectionRevision: 3 })));
  await activityState(store, () => callsTo(client, "evener/thread/delegates/list") === before + 1);
});

test("an older unknown-delegate frame arrival cannot replace a newer settlement", async () => {
  const client = pushClient();
  let admit = false;
  client.on("evener/thread/delegates/list", ({ scope }) => ({
    context: pushContext(),
    scope: scope ?? "session",
    page: { complete: true, issues: [] },
    delegates: admit
      ? [delegateRow(), delegateRow({ delegateId: "delegate-2", projectionRevision: 1, status: "running" })]
      : [delegateRow()],
  }));
  const { store } = await pushOwner(client);
  client.emitNotification(
    pushedFrame(
      frameInfo({ delegateId: "delegate-2", projectionRevision: 10, status: "completed", reportPreview: "settled" }),
    ),
  );
  client.emitNotification(
    pushedFrame(
      frameInfo({ delegateId: "delegate-2", projectionRevision: 4, status: "running", reportPreview: "stale" }),
    ),
  );
  await activityState(store, () => !store.getSnapshot().delegates.loading);
  admit = true;
  await store.refresh("delegates");
  const row = store.getSnapshot().delegates.rows.find((candidate) => candidate.delegateId === "delegate-2");
  expect(row?.projectionRevision).toBe(10);
  expect(row?.status).toBe("completed");
  expect(row?.reportPreview).toBe("settled");
});

test("a buffered unknown-id frame is applied once a read admits the row", async () => {
  const client = pushClient();
  let admit = false;
  client.on("evener/thread/delegates/list", ({ scope }) => ({
    context: pushContext(),
    scope: scope ?? "session",
    page: { complete: true, issues: [] },
    delegates: admit
      ? [delegateRow(), delegateRow({ delegateId: "delegate-2", projectionRevision: 1, status: "running" })]
      : [delegateRow()],
  }));
  const { store } = await pushOwner(client);
  client.emitNotification(
    pushedFrame(
      frameInfo({ delegateId: "delegate-2", projectionRevision: 5, status: "completed", reportPreview: "settled" }),
    ),
  );
  await activityState(store, () => !store.getSnapshot().delegates.loading);
  expect(store.getSnapshot().delegates.rows.map((candidate) => candidate.delegateId)).toEqual(["delegate-1"]);
  admit = true;
  await store.refresh("delegates");
  const row = store.getSnapshot().delegates.rows.find((candidate) => candidate.delegateId === "delegate-2");
  expect(row?.status).toBe("completed");
  expect(row?.reportPreview).toBe("settled");
});

test("a frame buffered before a bounded root refresh is applied once the walk reconciles", async () => {
  const client = pushClient();
  let served = false;
  client.on("evener/thread/delegates/list", ({ scope }) => ({
    context: pushContext(),
    scope: scope ?? "session",
    // The refresh stops at the displayed boundary, so its page is not itself
    // complete: the joined row set is what proves the frame's id is admitted.
    page: { complete: false, issues: [] },
    delegates: served
      ? [delegateRow({ delegateId: "delegate-2", status: "running" }), delegateRow({ status: "running" })]
      : [delegateRow({ status: "running" })],
  }));
  const store = owner(client);
  store.observe("delegates");
  await activityState(store, () => store.getSnapshot().delegates.rows.length === 1);
  served = true;
  client.emitNotification(
    pushedFrame(
      frameInfo({ delegateId: "delegate-2", projectionRevision: 5, status: "completed", reportPreview: "settled" }),
    ),
  );
  await activityState(store, () => store.getSnapshot().delegates.rows.some((row) => row.delegateId === "delegate-2"));
  await activityState(store, () => !store.getSnapshot().delegates.loading);
  const row = store.getSnapshot().delegates.rows.find((candidate) => candidate.delegateId === "delegate-2");
  expect(row?.status).toBe("completed");
  expect(row?.reportPreview).toBe("settled");
});

test("a buffered frame places on the identity its walk served, never on the twin a later page serves", async () => {
  const client = pushClient();
  let served = false;
  client.on("evener/thread/delegates/list", ({ cursor, scope }) => ({
    context: pushContext(),
    scope: scope ?? "session",
    page: { complete: false, issues: [], nextCursor: cursor ? undefined : "next" },
    delegates: cursor
      ? [
          delegateRow({ delegateId: "delegate-2", childRef: "local:second", status: "running" }),
          delegateRow({ status: "running" }),
        ]
      : served
        ? [delegateRow({ delegateId: "delegate-2", childRef: "local:first", status: "running" })]
        : [delegateRow({ status: "running" })],
  }));
  const store = owner(client);
  store.observe("delegates");
  await activityState(store, () => store.getSnapshot().delegates.rows.length === 1);
  served = true;
  client.emitNotification(
    pushedFrame(
      frameInfo({
        delegateId: "delegate-2",
        childRef: "local:first",
        projectionRevision: 5,
        status: "completed",
        reportPreview: "settled",
      }),
    ),
  );
  const twins = () =>
    store
      .getSnapshot()
      .delegates.rows.filter((row) => row.delegateId === "delegate-2")
      .map((row) => row.status);
  await activityState(store, () => twins().length === 1);
  // The walk's first page admitted the row the frame's identity names, so the
  // frame takes it at once: the walk's later pages and its reconciliation
  // join against the applied record, so an unsettled page loses nothing by
  // placing early.
  expect(twins()).toEqual(["completed"]);
  await vi.advanceTimersByTimeAsync(100);
  await activityState(store, () => !store.getSnapshot().delegates.loading);
  // The walk ended and served two rows under the id. The frame named its own
  // row's identity, so it takes exactly that row; the twin is not written.
  expect(twins()).toEqual(["completed", "running"]);
});

test("a frame that arrives while a replacement is announced is not overwritten by its read", async () => {
  const client = activityClient();
  // The row starts held: a previous daemon retired this session while it was
  // waiting on a shell, so the retained sequence is the old daemon's and is
  // higher than anything the replacement will assign for a long time.
  client.on("evener/thread/activity/read", ({ scope }) => ({ ...summaryFixture(scope), context: pushContext() }));
  client.on("evener/thread/delegates/list", ({ scope }) => ({
    context: pushContext(),
    scope: scope ?? "session",
    page: { complete: true, issues: [] },
    delegates: [
      delegateRow({
        status: "running",
        drainHeldByJobs: true,
        holdSequence: 900,
        runningJobCount: 2,
        oldestRunningJobStartedAt: "2026-11-18T10:00:00Z",
      }),
    ],
  }));
  const store = owner(client);
  store.start();
  store.observe("delegates");
  await activityState(store, () => store.getSnapshot().summary !== null && store.getSnapshot().delegates.complete);
  // The summary announces the replacement and its forced delegates read is left
  // in flight; a frame for the loaded row then arrives in that window.
  const reconciled = deferred<SessionDelegatesResponse>();
  client.on("evener/thread/activity/read", ({ scope }) => ({
    ...summaryFixture(scope),
    context: pushContext({ epoch: "epoch-2" }),
  }));
  client.on("evener/thread/delegates/list", () => reconciled.promise);
  activityChanged(client, ["summary"]);
  await activityState(store, () => store.getSnapshot().context?.epoch === "epoch-2");
  await vi.advanceTimersByTimeAsync(0);
  expect(store.getSnapshot().delegates.loading).toBe(true);
  client.emitNotification(
    pushedFrame(
      // The resumed delegate is running again and is not waiting on a shell,
      // but the replacement numbers its hold samples from its own start.
      frameInfo({
        delegateId: "delegate-1",
        projectionRevision: 9,
        status: "completed",
        reportPreview: "settled",
        holdSequence: 1,
      }),
    ),
  );
  reconciled.resolve({
    context: pushContext({ epoch: "epoch-2" }),
    scope: "session",
    page: { complete: true, issues: [] },
    delegates: [delegateRow({ status: "running", holdSequence: 1 })],
  });
  await activityState(store, () => !store.getSnapshot().delegates.loading);
  const row = store.getSnapshot().delegates.rows[0];
  expect(row?.status).toBe("completed");
  expect(row?.reportPreview).toBe("settled");
  // The retired daemon's hold cannot survive into the replacement's epoch.
  expect(row?.drainHeldByJobs ?? false).toBe(false);
  expect(row?.holdSequence).toBe(1);
  expect(row?.runningJobCount ?? 0).toBe(0);
});

test("a terminal frame racing the first response of a retained owner waits for the ordering it establishes", async () => {
  const client = pushClient();
  // A previous owner's rows are retained across a client replacement; the
  // replacement's own first delegates response has not established its epoch.
  const first = owner(client);
  first.start();
  const leaveFirst = first.observe("delegates");
  await activityState(first, () => first.getSnapshot().delegates.complete);
  const retained = first.getSnapshot();
  leaveFirst();
  const reconciled = deferred<SessionDelegatesResponse>();
  client.on("evener/thread/delegates/list", () => reconciled.promise);
  const store = owner(client, "session", retained);
  store.start();
  store.observe("delegates");
  // The replacement's own summary response enables the push merge before its
  // delegates response arrives, so a frame in that window is live news with
  // no ordering to join. (The retained snapshot already carries a summary, so
  // the wait is on this owner's read completing, not on the snapshot.)
  await activityState(store, () => {
    const summaryState = store.getSnapshot().summaryState;
    return !summaryState.loading && !summaryState.pending;
  });
  const reads = callsTo(client, "evener/thread/delegates/list");
  expect(store.getSnapshot().delegates.loading).toBe(true);
  client.emitNotification(
    pushedFrame(frameInfo({ projectionRevision: 9, status: "failed", reportPreview: "settled" })),
  );
  // The frame is buffered rather than applied: a merge recorded before any
  // epoch exists would be invisible to the ordering the first response is
  // about to establish, and its older snapshot would overwrite the frame.
  expect(store.getSnapshot().delegates.rows[0]?.status).toBe("completed");
  expect(callsTo(client, "evener/thread/delegates/list")).toBe(reads);
  // The response carries the pre-frame snapshot; the buffered frame reconciles
  // against it by revision once the epoch exists.
  reconciled.resolve({
    context: pushContext(),
    scope: "session",
    page: { complete: true, issues: [] },
    delegates: [delegateRow({ status: "completed", projectionRevision: 1 })],
  });
  await activityState(store, () => !store.getSnapshot().delegates.loading);
  const row = store.getSnapshot().delegates.rows[0];
  expect(row?.status).toBe("failed");
  expect(row?.reportPreview).toBe("settled");
});

test("a retired epoch's late page cannot erase a frame the announcement window applied", async () => {
  const client = pushClient();
  const store = owner(client);
  store.start();
  store.observe("delegates");
  await activityState(store, () => store.getSnapshot().summary !== null && store.getSnapshot().delegates.complete);
  // A refresh walk for the current epoch is left in flight on its root page.
  const page = deferred<SessionDelegatesResponse>();
  client.on("evener/thread/delegates/list", () => page.promise);
  const refreshed = store.refresh("delegates");
  await vi.advanceTimersByTimeAsync(0);
  expect(store.getSnapshot().delegates.loading).toBe(true);
  // The summary announces the replacement, so the forced delegates read queues
  // behind the in-flight walk.
  client.on("evener/thread/activity/read", ({ scope }) => ({
    ...summaryFixture(scope),
    context: pushContext({ epoch: "epoch-2" }),
  }));
  activityChanged(client, ["summary"]);
  await activityState(store, () => store.getSnapshot().context?.epoch === "epoch-2");
  // A frame in the announcement window is the replacement's own: it applies
  // immediately and records under the announced epoch.
  client.emitNotification(
    pushedFrame(frameInfo({ projectionRevision: 2, status: "failed", reportPreview: "settled" })),
  );
  const applied = store.getSnapshot().delegates.rows.find((candidate) => candidate.delegateId === "delegate-1");
  expect(applied?.status).toBe("failed");
  // The walk's page predates the replacement: it names the epoch the ordering
  // left and carries that journal's revision. It must not judge or overwrite
  // the record the window's frame owns, and the replacement's own read must
  // not clobber the frame with a snapshot that predates it.
  client.on("evener/thread/delegates/list", ({ scope }) => ({
    context: pushContext({ epoch: "epoch-2" }),
    scope: scope ?? "session",
    page: { complete: true, issues: [] },
    delegates: [delegateRow({ status: "running", projectionRevision: 1 })],
  }));
  page.resolve({
    context: pushContext(),
    scope: "session",
    page: { complete: true, issues: [] },
    delegates: [delegateRow({ status: "running", projectionRevision: 500 })],
  });
  await refreshed;
  await vi.advanceTimersByTimeAsync(200);
  const row = store.getSnapshot().delegates.rows.find((candidate) => candidate.delegateId === "delegate-1");
  expect(row?.status).toBe("failed");
  expect(row?.projectionRevision).toBe(2);
});

test("a frame admitted by a page the collection continues past places on its identity without waiting for the read to settle", async () => {
  const client = pushClient();
  let first = true;
  client.on("evener/thread/delegates/list", ({ cursor, scope }) => ({
    context: pushContext(),
    scope: scope ?? "session",
    page: first
      ? { complete: true, issues: [] }
      : cursor
        ? { complete: true, issues: [] }
        : { complete: false, issues: [], nextCursor: "next" },
    delegates: first
      ? []
      : cursor
        ? [delegateRow({ delegateId: "delegate-2", childRef: "local:second", status: "running" })]
        : [delegateRow({ delegateId: "delegate-2", childRef: "local:first", status: "running" })],
  }));
  const store = owner(client);
  store.observe("delegates");
  await activityState(
    store,
    () => store.getSnapshot().delegates.complete && store.getSnapshot().delegates.rows.length === 0,
  );
  first = false;
  client.emitNotification(
    pushedFrame(
      frameInfo({
        delegateId: "delegate-2",
        childRef: "local:first",
        projectionRevision: 5,
        status: "completed",
        reportPreview: "settled",
      }),
    ),
  );
  const twins = () =>
    store
      .getSnapshot()
      .delegates.rows.filter((row) => row.delegateId === "delegate-2")
      .map((row) => row.status);
  await activityState(store, () => twins().length === 1);
  // The page that landed the row leaves the collection open, but the frame
  // named its identity exactly: it takes its row at once. Waiting for a
  // settled read would strand it -- nothing but a user's loadMore finishes
  // that read, and a summary-suppressed invalidation reads nothing.
  expect(twins()).toEqual(["completed"]);
  const reads = callsTo(client, "evener/thread/delegates/list");
  await store.loadMore("delegates");
  await activityState(store, () => twins().length === 2);
  // The continuation serves the twin under the same id: the frame still owns
  // exactly its own row, and the twin is not written.
  expect(twins()).toEqual(["completed", "running"]);
  expect(callsTo(client, "evener/thread/delegates/list")).toBe(reads + 1);
});

test("a page that completes the collection places a buffered frame", async () => {
  const client = pushClient();
  let first = true;
  const second = deferred<SessionDelegatesResponse>();
  client.on("evener/thread/delegates/list", ({ cursor, scope }) =>
    first
      ? { context: pushContext(), scope: scope ?? "session", page: { complete: true, issues: [] }, delegates: [] }
      : cursor
        ? second.promise
        : {
            context: pushContext(),
            scope: scope ?? "session",
            page: { complete: false, issues: [], nextCursor: "next" },
            delegates: [],
          },
  );
  const store = owner(client);
  store.observe("delegates");
  await activityState(
    store,
    () => store.getSnapshot().delegates.complete && store.getSnapshot().delegates.rows.length === 0,
  );
  first = false;
  client.emitNotification(
    pushedFrame(
      frameInfo({ delegateId: "delegate-2", projectionRevision: 5, status: "completed", reportPreview: "settled" }),
    ),
  );
  // The collection is read to its end after the frame was buffered: a settled
  // read is the one place a buffered frame can be placed, and the row it serves
  // under the frame's identity takes exactly that frame.
  second.resolve({
    context: pushContext(),
    scope: "session",
    page: { complete: true, issues: [] },
    delegates: [delegateRow({ delegateId: "delegate-2", status: "running" })],
  });
  await activityState(store, () => !store.getSnapshot().delegates.loading);
  const row = store.getSnapshot().delegates.rows.find((candidate) => candidate.delegateId === "delegate-2");
  expect(row?.status).toBe("completed");
  expect(row?.reportPreview).toBe("settled");
});

test("a frame matching a loaded row patches it at once even while the collection continues past it", async () => {
  const client = pushClient();
  let grew = false;
  client.on("evener/thread/delegates/list", ({ cursor, scope }) => ({
    context: pushContext(),
    scope: scope ?? "session",
    page: cursor ? { complete: true, issues: [] } : { complete: false, issues: [], nextCursor: "next" },
    delegates: cursor
      ? [
          delegateRow({ delegateId: "delegate-2", childRef: "remote:first", status: "running" }),
          delegateRow({ delegateId: "delegate-2", childRef: "remote:second", status: "running" }),
        ]
      : grew
        ? [delegateRow({ delegateId: "delegate-3", childRef: "remote:inserted", status: "running" })]
        : [delegateRow({ delegateId: "delegate-2", childRef: "remote:first", status: "running" })],
  }));
  const store = owner(client);
  store.observe("delegates");
  await activityState(store, () => store.getSnapshot().delegates.rows.length === 1);
  const twins = () =>
    store
      .getSnapshot()
      .delegates.rows.filter((row) => row.delegateId === "delegate-2")
      .map((row) => row.status);
  grew = true;
  client.emitNotification(
    pushedFrame(
      frameInfo({
        delegateId: "delegate-2",
        childRef: "remote:first",
        projectionRevision: 5,
        status: "completed",
        reportPreview: "settled",
      }),
    ),
  );
  // The frame names the loaded row's identity, so it is that row's own frame
  // already: a page the collection continues past cannot reassign it to another
  // child, and no read is needed to place it.
  expect(twins()).toEqual(["completed"]);
  await store.loadMore("delegates");
  await activityState(store, () => !store.getSnapshot().delegates.loading && twins().length === 2);
  // The continuation served the row again at a lower revision beside its twin:
  // the frame's patch survives both, and the twin is not written.
  expect(twins()).toEqual(["completed", "running"]);
});

test("a resync re-establishes the push gate, so a delegate settling across it still lands", async () => {
  const client = pushClient();
  const racedDelegates = deferred<SessionDelegatesResponse>();
  const racedSummary = deferred<SessionActivitySummary>();
  let summaryCalls = 0;
  let delegateCalls = 0;
  client.on("evener/thread/activity/read", ({ scope }) => {
    summaryCalls += 1;
    if (summaryCalls === 1) return { ...summaryFixture(scope), context: pushContext() };
    // The resync's own refresh is still out when the delegate settles.
    return racedSummary.promise;
  });
  client.on("evener/thread/delegates/list", ({ scope }) => {
    delegateCalls += 1;
    if (delegateCalls === 1) {
      return {
        context: pushContext(),
        scope: scope ?? "session",
        page: { complete: true, issues: [] },
        delegates: [delegateRow({ status: "running" })],
      };
    }
    if (delegateCalls === 2) return racedDelegates.promise;
    // The invalidation's read runs after the settle, so it serves the report.
    return {
      context: pushContext({ epoch: "epoch-2" }),
      scope: scope ?? "session",
      page: { complete: true, issues: [] },
      delegates: [
        delegateRow({
          status: "completed",
          projectionRevision: 2,
          reportPreview: "settled",
          reportPreviewTruncated: false,
        }),
      ],
    };
  });
  const store = owner(client);
  store.start();
  store.observe("delegates");
  await activityState(store, () => store.getSnapshot().summary !== null && store.getSnapshot().delegates.complete);
  expect(store.getSnapshot().delegates.rows[0]?.status).toBe("running");
  client.emitNotification({
    method: "evener/thread/resync",
    params: { ref: activityRef, threadId: "session" },
  });
  await vi.advanceTimersByTimeAsync(0);
  // The delegate settles while the resync's reads are still out. A merge must not
  // be trusted across the resync boundary: the read it races is answered from a
  // snapshot the settle predates, so the invalidation is what recovers it.
  client.emitNotification(
    pushedFrame(frameInfo({ projectionRevision: 2, status: "completed", reportPreview: "settled" })),
  );
  activityChanged(client, ["delegates"]);
  racedDelegates.resolve({
    context: pushContext({ epoch: "epoch-2" }),
    scope: "session",
    page: { complete: true, issues: [] },
    delegates: [delegateRow({ status: "running" })],
  });
  racedSummary.resolve({ ...summaryFixture("session"), context: pushContext({ epoch: "epoch-2" }) });
  await activityState(store, () => !store.getSnapshot().delegates.loading);
  const row = store.getSnapshot().delegates.rows[0];
  expect(row).toMatchObject({ status: "completed", reportPreview: "settled" });
});

test("a settlement that races the read revealing the replacement epoch is recovered", async () => {
  const client = pushClient();
  const reveals = deferred<SessionDelegatesResponse>();
  let calls = 0;
  client.on("evener/thread/delegates/list", ({ scope }) => {
    calls += 1;
    if (calls === 1) {
      return {
        context: pushContext(),
        scope: scope ?? "session",
        page: { complete: true, issues: [] },
        delegates: [delegateRow({ status: "running" })],
      };
    }
    if (calls === 2) return reveals.promise;
    // The reconciliation read runs after the settle, so it serves the report.
    return {
      context: pushContext({ epoch: "epoch-2" }),
      scope: scope ?? "session",
      page: { complete: true, issues: [] },
      delegates: [
        delegateRow({
          status: "completed",
          projectionRevision: 2,
          reportPreview: "settled",
          reportPreviewTruncated: false,
        }),
      ],
    };
  });
  const store = owner(client);
  store.observe("delegates");
  await activityState(store, () => store.getSnapshot().delegates.complete);
  expect(store.getSnapshot().delegates.rows[0]?.status).toBe("running");
  // The refresh is the first response to see the replacement, and the delegate
  // settles before it returns.
  const refreshed = store.refresh("delegates");
  await vi.advanceTimersByTimeAsync(0);
  client.emitNotification(
    pushedFrame(frameInfo({ projectionRevision: 2, status: "completed", reportPreview: "settled" })),
  );
  reveals.resolve({
    context: pushContext({ epoch: "epoch-2" }),
    scope: "session",
    page: { complete: true, issues: [] },
    delegates: [delegateRow({ status: "running" })],
  });
  await refreshed;
  await activityState(store, () => !store.getSnapshot().delegates.loading);
  const row = store.getSnapshot().delegates.rows[0];
  expect(row).toMatchObject({ status: "completed", reportPreview: "settled" });
});

test("a delegates-only observer keeps reading when its ancestor's runtime is released", async () => {
  const client = pushClient();
  const store = owner(client);
  // The supported API: a view that observes delegates without starting the
  // summary, so its own collection response is what carries the context.
  store.observe("delegates");
  await activityState(store, () => store.getSnapshot().delegates.complete);
  expect(store.getSnapshot().context?.availability).toBe("live");
  expect(store.getSnapshot().delegates.rows[0]?.status).toBe("completed");
  // The ancestor releases its runtime: the invalidation arrives with no frame,
  // and nothing else this store observes would reveal the retained context.
  client.on("evener/thread/delegates/list", ({ scope }) => ({
    context: pushContext({ availability: "retained" }),
    scope: scope ?? "session",
    page: { complete: true, issues: [] },
    delegates: [delegateRow({ status: "failed" })],
  }));
  activityChanged(client, ["delegates"]);
  await vi.advanceTimersByTimeAsync(0);
  // Without the read the invalidation would have skipped, nothing this
  // delegates-only store observes could reveal the retained context.
  expect(callsTo(client, "evener/thread/delegates/list")).toBeGreaterThan(1);
  await activityState(store, () => store.getSnapshot().context?.availability === "retained");
  expect(store.getSnapshot().delegates.rows[0]?.status).toBe("failed");
});

test("a delegates-and-jobs store without a summary still reads on its invalidation", async () => {
  const client = pushClient();
  client.on("evener/thread/jobs/list", ({ scope }) => ({
    context: pushContext(),
    scope: scope ?? "session",
    page: { complete: true, issues: [] },
    jobs: [jobFixture()],
  }));
  const store = owner(client);
  store.observe("delegates");
  store.observe("jobs");
  await activityState(store, () => store.getSnapshot().delegates.complete && store.getSnapshot().jobs.complete);
  const before = callsTo(client, "evener/thread/delegates/list");
  client.on("evener/thread/delegates/list", ({ scope }) => ({
    context: pushContext({ availability: "retained" }),
    scope: scope ?? "session",
    page: { complete: true, issues: [] },
    delegates: [delegateRow({ status: "failed" })],
  }));
  // The producer pairs the summary with every delegate change, but this store
  // never started the summary, so this invalidation refreshes no other resource:
  // without the delegates read nothing here could reveal the retained context.
  activityChanged(client, ["summary", "delegates"]);
  await vi.advanceTimersByTimeAsync(0);
  expect(callsTo(client, "evener/thread/delegates/list")).toBeGreaterThan(before);
  await activityState(store, () => store.getSnapshot().context?.availability === "retained");
  expect(store.getSnapshot().delegates.rows[0]?.status).toBe("failed");
});

test("a late sibling response from a retired epoch cannot reactivate the ordering", async () => {
  const client = pushClient();
  const late = deferred<SessionJobsResponse>();
  client.on("evener/thread/jobs/list", ({ scope }) => ({
    context: pushContext(),
    scope: scope ?? "session",
    page: { complete: true, issues: [] },
    jobs: [jobFixture()],
  }));
  const store = owner(client);
  store.start();
  store.observe("delegates");
  store.observe("jobs");
  await activityState(
    store,
    () =>
      store.getSnapshot().summary !== null &&
      store.getSnapshot().delegates.complete &&
      store.getSnapshot().jobs.complete,
  );
  // A jobs read is served under the old epoch and held while the delegates
  // reconcile the replacement.
  client.on("evener/thread/jobs/list", () => late.promise);
  activityChanged(client, ["jobs"]);
  await vi.advanceTimersByTimeAsync(0);
  client.on("evener/thread/delegates/list", ({ scope }) => ({
    context: pushContext({ epoch: "epoch-2" }),
    scope: scope ?? "session",
    page: { complete: true, issues: [] },
    delegates: [delegateRow({ projectionRevision: 2, status: "completed" })],
  }));
  await store.refresh("delegates");
  expect(store.getSnapshot().delegates.context?.epoch).toBe("epoch-2");
  const settledReads = callsTo(client, "evener/thread/delegates/list");
  const arrived = new Promise<void>((resolve) => {
    const stop = store.subscribe(() => {
      stop();
      resolve();
    });
  });
  late.resolve({
    context: pushContext({ epoch: "epoch-1" }),
    scope: "session",
    page: { complete: true, issues: [] },
    jobs: [jobFixture()],
  });
  await arrived;
  await vi.advanceTimersByTimeAsync(0);
  // The ordering still belongs to the epoch the delegates read served, so its
  // higher revision rejects the stale frame instead of the late announcement
  // judging every record retired.
  client.emitNotification(pushedFrame(frameInfo({ projectionRevision: 1, status: "failed", reportPreview: "stale" })));
  expect(store.getSnapshot().delegates.rows[0]).toMatchObject({
    projectionRevision: 2,
    status: "completed",
  });
  expect(store.getSnapshot().delegates.rows[0]?.reportPreview).not.toBe("stale");
  // And the late response forces no read of its own: its epoch is behind the
  // ordering, so there is nothing to reconcile.
  expect(callsTo(client, "evener/thread/delegates/list")).toBe(settledReads);
});

test("a permanent summary failure reconciles the delegates its read stood in for", async () => {
  const client = pushClient();
  const store = owner(client);
  store.start();
  store.observe("delegates");
  await activityState(store, () => store.getSnapshot().summary !== null && store.getSnapshot().delegates.complete);
  expect(store.getSnapshot().context?.availability).toBe("live");
  const before = callsTo(client, "evener/thread/delegates/list");
  // The invalidation's summary read is the only other read it would make, and it
  // refuses for good, so nothing else would carry the released runtime back.
  client.on("evener/thread/activity/read", () => {
    throw new WireError("thread not found: session", -32012, { evenerErrorInfo: "sessionUnavailable" });
  });
  client.on("evener/thread/delegates/list", ({ scope }) => ({
    context: pushContext({ availability: "retained" }),
    scope: scope ?? "session",
    page: { complete: true, issues: [] },
    delegates: [delegateRow({ status: "failed" })],
  }));
  activityChanged(client, ["summary", "delegates"]);
  await vi.advanceTimersByTimeAsync(0);
  expect(callsTo(client, "evener/thread/delegates/list")).toBeGreaterThan(before);
  await activityState(store, () => store.getSnapshot().delegates.rows[0]?.status === "failed");
  expect(store.getSnapshot().context?.availability).toBe("retained");
});

test("an explicit summary read is no substitute for the delegates read", async () => {
  const client = pushClient();
  const store = owner(client);
  store.observe("delegates");
  await activityState(store, () => store.getSnapshot().delegates.complete);
  const before = callsTo(client, "evener/thread/delegates/list");
  // An explicit read has no retry owner, so the invalidation cannot lean on it
  // to carry the capability back: it is held open here and then fails.
  const pending = deferred<SessionActivitySummary>();
  client.on("evener/thread/activity/read", () => pending.promise);
  const summary = store.refresh("summary");
  await vi.advanceTimersByTimeAsync(0);
  client.on("evener/thread/delegates/list", ({ scope }) => ({
    context: pushContext({ availability: "retained" }),
    scope: scope ?? "session",
    page: { complete: true, issues: [] },
    delegates: [delegateRow({ status: "failed" })],
  }));
  activityChanged(client, ["summary", "delegates"]);
  await vi.advanceTimersByTimeAsync(0);
  expect(callsTo(client, "evener/thread/delegates/list")).toBeGreaterThan(before);
  pending.reject(new WireError("summary hiccup", -32000, { evenerErrorInfo: "internal" }));
  await summary;
  await activityState(store, () => store.getSnapshot().delegates.rows[0]?.status === "failed");
  expect(store.getSnapshot().context?.availability).toBe("retained");
});

test("a superseded announcement cannot be reclaimed by a delayed response", async () => {
  const client = pushClient();
  const jobB = deferred<SessionJobsResponse>(),
    jobC = deferred<SessionJobsResponse>(),
    jobLate = deferred<SessionJobsResponse>();
  const readReplacement = deferred<SessionDelegatesResponse>(),
    readReconcile = deferred<SessionDelegatesResponse>(),
    readLate = deferred<SessionDelegatesResponse>();
  const jobAnswers = [jobB.promise, jobC.promise, jobLate.promise];
  const readAnswers = [readReplacement.promise, readReconcile.promise, readLate.promise];
  let job = 0;
  let read = 0;
  let armed = false;
  client.on("evener/thread/jobs/list", ({ scope }) =>
    armed
      ? (jobAnswers[job++] ?? jobLate.promise)
      : {
          context: pushContext(),
          scope: scope ?? "session",
          page: { complete: true, issues: [] },
          jobs: [jobFixture()],
        },
  );
  client.on("evener/thread/delegates/list", ({ scope }) =>
    armed
      ? (readAnswers[read++] ?? readLate.promise)
      : {
          context: pushContext(),
          scope: scope ?? "session",
          page: { complete: true, issues: [] },
          delegates: [delegateRow()],
        },
  );
  const store = owner(client);
  store.start();
  store.observe("delegates");
  store.observe("jobs");
  await activityState(
    store,
    () =>
      store.getSnapshot().summary !== null &&
      store.getSnapshot().delegates.complete &&
      store.getSnapshot().jobs.complete,
  );
  expect(store.getSnapshot().delegates.context?.epoch).toBe("epoch-1");
  const replacement = (epoch: string): SessionDelegatesResponse => ({
    context: pushContext({ epoch }),
    scope: "session",
    page: { complete: true, issues: [] },
    delegates: [delegateRow({ projectionRevision: 2, status: "completed" })],
  });
  const sibling = (epoch: string): SessionJobsResponse => ({
    context: pushContext({ epoch }),
    scope: "session",
    page: { complete: true, issues: [] },
    jobs: [jobFixture()],
  });
  armed = true;
  // Sibling responses announce B while the delegates still hold A, and then C,
  // while the delegate read that follows each announcement stays in flight.
  activityChanged(client, ["jobs"]);
  await vi.advanceTimersByTimeAsync(0);
  jobB.resolve(sibling("epoch-2"));
  await vi.advanceTimersByTimeAsync(0);
  activityChanged(client, ["jobs"]);
  await vi.advanceTimersByTimeAsync(0);
  jobC.resolve(sibling("epoch-3"));
  await vi.advanceTimersByTimeAsync(0);
  // The delegate read moves from A straight to C, so B is only ever announced.
  readReplacement.resolve(replacement("epoch-3"));
  await vi.advanceTimersByTimeAsync(0);
  readReconcile.resolve(replacement("epoch-3"));
  await activityState(
    store,
    () => store.getSnapshot().delegates.context?.epoch === "epoch-3" && !store.getSnapshot().delegates.loading,
  );
  const settledReads = callsTo(client, "evener/thread/delegates/list");
  // The delayed B response arrives after C settled.
  activityChanged(client, ["jobs"]);
  await vi.advanceTimersByTimeAsync(0);
  jobLate.resolve(sibling("epoch-2"));
  await vi.advanceTimersByTimeAsync(0);
  readLate.resolve(replacement("epoch-3"));
  await vi.advanceTimersByTimeAsync(0);
  // The ordering still belongs to C, so the served revision stands.
  client.emitNotification(pushedFrame(frameInfo({ projectionRevision: 1, status: "failed", reportPreview: "stale" })));
  expect(store.getSnapshot().delegates.rows[0]).toMatchObject({
    projectionRevision: 2,
    status: "completed",
  });
  expect(store.getSnapshot().delegates.rows[0]?.reportPreview).not.toBe("stale");
  // And the delayed response forced no read: its epoch is behind the ordering.
  expect(callsTo(client, "evener/thread/delegates/list")).toBe(settledReads);
});

test("frames arriving during a multi-page refresh do not restart it", async () => {
  const client = pushClient();
  client.on("evener/thread/delegates/list", ({ cursor, scope }) => ({
    context: pushContext(),
    scope: scope ?? "session",
    page: cursor ? { complete: true, issues: [] } : { complete: false, issues: [], nextCursor: "second" },
    delegates: cursor
      ? [delegateRow({ delegateId: "delegate-2", childRef: "remote:second", projectionRevision: 1 })]
      : [delegateRow({ projectionRevision: 1 })],
  }));
  const store = owner(client);
  store.observe("delegates");
  await activityState(store, () => store.getSnapshot().delegates.rows.length === 1);
  await store.loadMore("delegates");
  expect(store.getSnapshot().delegates.rows).toHaveLength(2);
  // A refresh walks the collection in pages, and frames for the loaded row
  // arrive throughout that walk.
  const refreshed = store.refresh("delegates");
  await vi.advanceTimersByTimeAsync(0);
  const duringWalk = callsTo(client, "evener/thread/delegates/list");
  for (let revision = 2; revision <= 7; revision += 1) {
    client.emitNotification(pushedFrame(frameInfo({ projectionRevision: revision, status: "running" })));
  }
  await vi.advanceTimersByTimeAsync(0);
  // The walk in flight will reach the displayed boundary and settle them, so it
  // is not restarted: no read is asked for.
  expect(callsTo(client, "evener/thread/delegates/list")).toBe(duringWalk);
  await vi.advanceTimersByTimeAsync(100);
  await refreshed;
  expect(store.getSnapshot().delegates.loading).toBe(false);
  expect(store.getSnapshot().delegates.rows).toHaveLength(2);
  expect(store.getSnapshot().delegates.rows[0]).toMatchObject({ projectionRevision: 7, status: "running" });
});

test("an evicted frame during a walk is recovered by one read", async () => {
  const client = pushClient();
  let revision = 1;
  const rows = (count: number, first: number) =>
    Array.from({ length: count }, (_, index) => {
      const id = first + index;
      return delegateRow({
        delegateId: `delegate-${id}`,
        childRef: `remote:${id}`,
        projectionRevision: id === 1 ? revision : 1,
        status: id === 1 && revision > 1 ? "completed" : "running",
        ...(id === 1 && revision > 1 ? { reportPreview: "settled" } : {}),
      });
    });
  client.on("evener/thread/delegates/list", ({ cursor, scope }) => ({
    context: pushContext(),
    scope: scope ?? "session",
    page: cursor ? { complete: true, issues: [] } : { complete: false, issues: [], nextCursor: "second" },
    delegates: cursor ? rows(2, 200) : rows(129, 1),
  }));
  const store = owner(client);
  store.observe("delegates");
  await activityState(store, () => store.getSnapshot().delegates.rows.length === 129);
  await store.loadMore("delegates");
  expect(store.getSnapshot().delegates.rows).toHaveLength(131);
  // A refresh walks the collection, and the first loaded row settles before more
  // ids than the buffer holds update, so its frame is evicted unplaced.
  const refreshed = store.refresh("delegates");
  await vi.advanceTimersByTimeAsync(0);
  client.emitNotification(
    pushedFrame(frameInfo({ projectionRevision: 4, status: "completed", reportPreview: "settled" })),
  );
  for (let id = 2; id <= 129; id += 1) {
    client.emitNotification(
      pushedFrame(frameInfo({ delegateId: `delegate-${id}`, projectionRevision: 2, status: "running" })),
    );
  }
  revision = 5;
  await vi.advanceTimersByTimeAsync(100);
  // The recovery read walks the collection too, so its own page steps on the
  // paced timer.
  await vi.advanceTimersByTimeAsync(100);
  await refreshed;
  // The walk could not place the evicted frame, so it is recovered by a read once
  // the walk is over.
  expect(store.getSnapshot().delegates.rows[0]).toMatchObject({
    delegateId: "delegate-1",
    projectionRevision: 5,
    status: "completed",
  });
  // The reads stay the walk's own plus that one recovery.
  expect(callsTo(client, "evener/thread/delegates/list")).toBeLessThanOrEqual(8);
});

test("a sibling walk does not consume the delegates' eviction recovery", async () => {
  const client = pushClient();
  let revision = 1;
  const delegateRows = (count: number, first: number) =>
    Array.from({ length: count }, (_, index) => {
      const id = first + index;
      return delegateRow({
        delegateId: `delegate-${id}`,
        childRef: `remote:${id}`,
        projectionRevision: id === 1 ? revision : 1,
        status: id === 1 && revision > 1 ? "completed" : "running",
        ...(id === 1 && revision > 1 ? { reportPreview: "settled" } : {}),
      });
    });
  client.on("evener/thread/delegates/list", ({ cursor, scope }) => ({
    context: pushContext(),
    scope: scope ?? "session",
    page:
      cursor === "d3"
        ? { complete: true, issues: [] }
        : { complete: false, issues: [], nextCursor: cursor === "d2" ? "d3" : "d2" },
    delegates: cursor ? delegateRows(2, cursor === "d2" ? 200 : 300) : delegateRows(129, 1),
  }));
  client.on("evener/thread/jobs/list", ({ cursor, scope }) => ({
    context: pushContext(),
    scope: scope ?? "session",
    page: cursor ? { complete: true, issues: [] } : { complete: false, issues: [], nextCursor: "j2" },
    jobs: [jobFixture(cursor ? "older" : "newer")],
  }));
  const store = owner(client);
  store.observe("delegates");
  store.observe("jobs");
  await activityState(
    store,
    () => store.getSnapshot().delegates.rows.length === 129 && store.getSnapshot().jobs.rows.length === 1,
  );
  // Both collections carry a displayed boundary that needs the walk's pages.
  await store.loadMore("delegates");
  await store.loadMore("delegates");
  await store.loadMore("jobs");
  const delegatesRefreshed = store.refresh("delegates");
  await vi.advanceTimersByTimeAsync(0);
  client.emitNotification(
    pushedFrame(frameInfo({ projectionRevision: 4, status: "completed", reportPreview: "settled" })),
  );
  for (let id = 2; id <= 129; id += 1) {
    client.emitNotification(
      pushedFrame(frameInfo({ delegateId: `delegate-${id}`, projectionRevision: 2, status: "running" })),
    );
  }
  // The jobs walk finishes one page sooner, and must not clear the delegate
  // buffer's recovery.
  const jobsRefreshed = store.refresh("jobs");
  revision = 5;
  // Both walks step on the timed pacing, and the delegates' recovery read walks
  // the collection again.
  await vi.advanceTimersByTimeAsync(500);
  await delegatesRefreshed;
  await jobsRefreshed;
  expect(store.getSnapshot().delegates.rows[0]).toMatchObject({
    delegateId: "delegate-1",
    projectionRevision: 5,
    status: "completed",
  });
});

test("a comma fraction does not order against the projector's timestamps", async () => {
  const client = pushClient();
  client.on("evener/thread/delegates/list", ({ scope }) => ({
    context: pushContext(),
    scope: scope ?? "session",
    page: { complete: true, issues: [] },
    delegates: [delegateRow({ latestActivityAt: "2026-01-01T00:00:00.500Z" })],
  }));
  const { store } = await pushOwner(client);
  client.emitNotification(
    pushedFrame(
      frameInfo({ projectionRevision: 2, status: "completed", latestActivityAt: "2026-01-01T00:00:01,500Z" }),
    ),
  );
  expect(store.getSnapshot().delegates.rows[0]?.latestActivityAt).toBe("2026-01-01T00:00:00.500Z");
});

test("a live context that becomes retained reconciles the observed delegates", async () => {
  const client = pushClient();
  const store = owner(client);
  store.start();
  store.observe("delegates");
  await activityState(store, () => store.getSnapshot().summary !== null && store.getSnapshot().delegates.complete);
  client.on("evener/thread/activity/read", ({ scope }) => ({
    ...summaryFixture(scope),
    context: pushContext({ availability: "retained" }),
  }));
  client.on("evener/thread/delegates/list", ({ scope }) => ({
    context: pushContext({ availability: "retained" }),
    scope: scope ?? "session",
    page: { complete: true, issues: [] },
    delegates: [delegateRow({ status: "failed" })],
  }));
  const beforeDelegates = callsTo(client, "evener/thread/delegates/list");
  activityChanged(client, ["summary", "delegates"]);
  await activityState(
    store,
    () => store.getSnapshot().delegates.rows[0]?.status === "failed" && !store.getSnapshot().delegates.loading,
  );
  expect(callsTo(client, "evener/thread/delegates/list")).toBe(beforeDelegates + 1);
});

test("a context without the capability keeps reading delegates on the invalidation", async () => {
  const client = activityClient();
  const store = owner(client);
  store.observe("delegates");
  await activityState(store, () => store.getSnapshot().delegates.complete);
  expect(store.getSnapshot().context?.reportPreview).not.toBe(true);
  const before = callsTo(client, "evener/thread/delegates/list");
  client.on("evener/thread/delegates/list", ({ scope }) => ({
    context: activityContext(),
    scope: scope ?? "session",
    page: { complete: true, issues: [] },
    delegates: [delegateRow({ status: "failed" })],
  }));
  activityChanged(client, ["delegates"]);
  await activityState(
    store,
    () => store.getSnapshot().delegates.rows[0]?.status === "failed" && !store.getSnapshot().delegates.loading,
  );
  expect(callsTo(client, "evener/thread/delegates/list")).toBe(before + 1);
});

test("an unknown delegate triggers one read, and a delegates-only store reads on its invalidation", async () => {
  const { client, store } = await pushOwner();
  const before = callsTo(client, "evener/thread/delegates/list");
  client.emitNotification(pushedFrame(frameInfo({ delegateId: "delegate-2", projectionRevision: 2 })));
  client.emitNotification(pushedFrame(frameInfo({ delegateId: "delegate-2", projectionRevision: 3 })));
  client.emitNotification(pushedFrame(frameInfo({ delegateId: "delegate-2", projectionRevision: 4 })));
  await activityState(
    store,
    () => callsTo(client, "evener/thread/delegates/list") === before + 1 && !store.getSnapshot().delegates.loading,
  );
  expect(callsTo(client, "evener/thread/delegates/list")).toBe(before + 1);
  activityChanged(client, ["delegates"]);
  // This store observes no other collection, so no other response could reveal
  // that its ancestor released the runtime: the invalidation has to read.
  await vi.advanceTimersByTimeAsync(0);
  expect(callsTo(client, "evener/thread/delegates/list")).toBe(before + 2);
  await store.refresh("summary");
  expect(callsTo(client, "evener/thread/delegates/list")).toBe(before + 2);
});

test("a count-moving delegate change still refreshes the summary", async () => {
  const client = pushClient();
  const store = owner(client);
  store.start();
  store.observe("delegates");
  await activityState(store, () => store.getSnapshot().summary !== null && store.getSnapshot().delegates.complete);
  const beforeSummary = callsTo(client, "evener/thread/activity/read");
  const beforeDelegates = callsTo(client, "evener/thread/delegates/list");
  client.emitNotification(pushedFrame(frameInfo({ projectionRevision: 2, status: "failed" })));
  activityChanged(client, ["summary", "delegates"]);
  await activityState(
    store,
    () =>
      callsTo(client, "evener/thread/activity/read") === beforeSummary + 1 && !store.getSnapshot().summaryState.loading,
  );
  expect(callsTo(client, "evener/thread/delegates/list")).toBe(beforeDelegates);
});
