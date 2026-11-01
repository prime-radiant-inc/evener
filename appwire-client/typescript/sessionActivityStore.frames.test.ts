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

test("a read that admits two rows under one id drops a buffered frame and re-reads on a newer revision", async () => {
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
    pushedFrame(frameInfo({ delegateId: "delegate-2", projectionRevision: 9, status: "failed" })),
  );
  await activityState(store, () => callsTo(client, "evener/thread/delegates/list") === before + 1);
  admit = true;
  await store.refresh("delegates");
  const twins = () =>
    store
      .getSnapshot()
      .delegates.rows.filter((row) => row.delegateId === "delegate-2")
      .map((row) => row.status);
  // The read served the id twice, so the frame cannot be attributed: it is
  // dropped rather than written into one of the twins.
  expect(twins()).toEqual(["running", "running"]);
  const settled = callsTo(client, "evener/thread/delegates/list");
  // The id already asked for a read at this revision and this frame carries
  // nothing newer, so the repeat asks for no further read.
  client.emitNotification(
    pushedFrame(frameInfo({ delegateId: "delegate-2", projectionRevision: 9, status: "failed" })),
  );
  await activityState(store, () => !store.getSnapshot().delegates.loading);
  expect(callsTo(client, "evener/thread/delegates/list")).toBe(settled);
  // A strictly newer revision asks for one more read, the only way the rows
  // under an id a frame cannot address can change.
  client.emitNotification(
    pushedFrame(frameInfo({ delegateId: "delegate-2", projectionRevision: 10, status: "failed" })),
  );
  await activityState(store, () => callsTo(client, "evener/thread/delegates/list") === settled + 1);
  await activityState(store, () => !store.getSnapshot().delegates.loading);
  expect(callsTo(client, "evener/thread/delegates/list")).toBe(settled + 1);
  expect(twins()).toEqual(["running", "running"]);
});

test("a frame whose delegate id names two loaded rows patches neither", async () => {
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
    pushedFrame(frameInfo({ delegateId: "delegate-1", projectionRevision: 9, status: "failed" })),
  );
  expect(store.getSnapshot().delegates.rows.map((row) => row.status)).toEqual(["running", "running"]);
  // The id cannot be attributed to a row, so the frame asks for a read instead.
  await activityState(store, () => callsTo(client, "evener/thread/delegates/list") === before + 1);
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

test("a buffered frame waits while the walk can still serve its id twice", async () => {
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
      frameInfo({ delegateId: "delegate-2", projectionRevision: 5, status: "completed", reportPreview: "settled" }),
    ),
  );
  const twins = () =>
    store
      .getSnapshot()
      .delegates.rows.filter((row) => row.delegateId === "delegate-2")
      .map((row) => row.status);
  await activityState(store, () => twins().length === 1);
  // The first page of the walk does not reach the displayed boundary, so a later
  // page can still serve the id again: the frame waits rather than being written
  // into the one row this page happens to hold.
  expect(twins()).toEqual(["running"]);
  await vi.advanceTimersByTimeAsync(100);
  await activityState(store, () => !store.getSnapshot().delegates.loading);
  // The walk ended and served the id twice, so the frame cannot be attributed to
  // either row and is dropped.
  expect(twins()).toEqual(["running", "running"]);
});

test("a frame that arrives while a replacement is announced is not overwritten by its read", async () => {
  const client = activityClient();
  client.on("evener/thread/activity/read", ({ scope }) => ({ ...summaryFixture(scope), context: pushContext() }));
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
      frameInfo({ delegateId: "delegate-1", projectionRevision: 9, status: "completed", reportPreview: "settled" }),
    ),
  );
  reconciled.resolve({
    context: pushContext({ epoch: "epoch-2" }),
    scope: "session",
    page: { complete: true, issues: [] },
    delegates: [delegateRow({ status: "running" })],
  });
  await activityState(store, () => !store.getSnapshot().delegates.loading);
  const row = store.getSnapshot().delegates.rows[0];
  expect(row?.status).toBe("completed");
  expect(row?.reportPreview).toBe("settled");
});

test("a frame admitted by a page the collection continues past is not attributed yet", async () => {
  const client = pushClient();
  let first = true;
  client.on("evener/thread/delegates/list", ({ cursor, scope }) => ({
    context: pushContext(),
    scope: scope ?? "session",
    page: first
      ? { complete: true, issues: [] }
      : { complete: false, issues: [], nextCursor: cursor ? undefined : "next" },
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
      frameInfo({ delegateId: "delegate-2", projectionRevision: 5, status: "completed", reportPreview: "settled" }),
    ),
  );
  const twins = () =>
    store
      .getSnapshot()
      .delegates.rows.filter((row) => row.delegateId === "delegate-2")
      .map((row) => row.status);
  await activityState(store, () => twins().length === 1);
  // The collection continues past this page, so a second row under the id is
  // still possible: the frame waits rather than being written into this one.
  expect(twins()).toEqual(["running"]);
  await store.loadMore("delegates");
  await activityState(store, () => twins().length === 2);
  expect(twins()).toEqual(["running", "running"]);
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
  // The collection is read to its end after the frame was buffered: that page is
  // what proves the id has no second row, so the frame is placed there.
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

test("a frame waits while a page the collection continues past holds its id only once", async () => {
  const client = pushClient();
  let grew = false;
  client.on("evener/thread/delegates/list", ({ cursor, scope }) => ({
    // The first page leaves the collection open, so its one row under the id
    // does not prove the id names one child. A delegate created since moves the
    // row the frame matched onto the page after this one.
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
      frameInfo({ delegateId: "delegate-2", projectionRevision: 5, status: "completed", reportPreview: "settled" }),
    ),
  );
  // The one row this page holds is not yet the frame's own: nothing has read the
  // id twice, so the frame waits for the read that settles the collection.
  expect(twins()).toEqual(["running"]);
  await vi.advanceTimersByTimeAsync(100);
  await activityState(store, () => !store.getSnapshot().delegates.loading && twins().length === 2);
  // The walk reached the collection's end and served the id twice, so the frame
  // belongs to neither row and is dropped rather than written into the first.
  expect(twins()).toEqual(["running", "running"]);
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
