// @vitest-environment node
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { WireError } from "./errors";
import { projectSessionActivity } from "./sessionActivityPresentation";
import { SessionActivityStore } from "./sessionActivityStore";
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
import type { SessionActivitySummary, SessionDelegatesResponse, SessionJobsResponse, SessionWatch } from "./types.gen";

const owners: SessionActivityStore[] = [];
const owner = (client = activityClient(), scope: "session" | "subtree" = "session") => {
  const store = new SessionActivityStore(client, activityRef, {
    scope,
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
test("pre-ready mount starts summary only and acquires a lean additive subscription", async () => {
  const client = activityClient("connecting"),
    store = owner(client);
  store.start();
  expect(client.calls).toHaveLength(0);
  client.emitReady();
  await activityState(store, () => store.getSnapshot().summary !== null);
  expect(client.calls.map(({ method }) => method)).toEqual(["thread/read", "evener/thread/activity/read"]);
  expect(client.calls[0]?.params).toEqual({
    ref: activityRef,
    includeTurns: false,
    subscribe: true,
    replaceSubscription: false,
  });
  expect(store.getSnapshot().summary?.delegates.known).toBe(false);
  expect(store.getSnapshot().summary?.jobs.total).toBe(201);
  expect(store.getSnapshot().jobs.rows).toEqual([]);
});
test("opening one collection coalesces duplicate demand and releases retry interest", async () => {
  const client = activityClient(),
    store = owner(client);
  store.start();
  await activityState(store, () => store.getSnapshot().summary !== null);
  const leave = store.observe("jobs"),
    leaveAgain = store.observe("jobs");
  await activityState(store, () => store.getSnapshot().jobs.complete);
  expect(callsTo(client, "evener/thread/jobs/list")).toBe(1);
  expect(callsTo(client, "evener/thread/delegates/list")).toBe(0);
  expect(callsTo(client, "evener/thread/watches/list")).toBe(0);
  leave();
  leave();
  activityChanged(client, ["jobs"]);
  await activityState(
    store,
    () => callsTo(client, "evener/thread/jobs/list") === 2 && !store.getSnapshot().jobs.loading,
  );
  leaveAgain();
  activityChanged(client, ["jobs"]);
  await store.refresh("summary");
  expect(callsTo(client, "evener/thread/jobs/list")).toBe(2);
});
test("coalesces invalidations behind one request and refreshes only observed resources", async () => {
  const client = activityClient(),
    first = deferred<SessionJobsResponse>(),
    entered = deferred<void>();
  client.on("evener/thread/jobs/list", () => {
    entered.resolve();
    return callsTo(client, "evener/thread/jobs/list") === 1 ? first.promise : jobsFixture([jobFixture("shell-2")]);
  });
  const store = owner(client);
  store.start();
  await activityState(store, () => store.getSnapshot().summary !== null);
  store.observe("jobs");
  await entered.promise;
  activityChanged(client, ["jobs", "delegates", "watches"]);
  activityChanged(client, ["jobs"]);
  first.resolve(jobsFixture());
  await activityState(
    store,
    () => store.getSnapshot().jobs.rows[0]?.jobId === "shell-2" && !store.getSnapshot().jobs.loading,
  );
  expect(callsTo(client, "evener/thread/jobs/list")).toBe(2);
  expect(callsTo(client, "evener/thread/delegates/list")).toBe(0);
  await store.refresh("summary");
  expect(callsTo(client, "evener/thread/jobs/list")).toBe(2);
});
test("merges keyset pages by identity while counts stay independent of loaded rows", async () => {
  const client = activityClient();
  client.on("evener/thread/jobs/list", ({ cursor }) =>
    cursor
      ? jobsFixture([jobFixture("shell-1", "completed"), jobFixture("shell-2")])
      : jobsFixture([jobFixture()], "page-2"),
  );
  const store = owner(client);
  store.start();
  await activityState(store, () => store.getSnapshot().summary !== null);
  store.observe("jobs");
  await activityState(store, () => store.getSnapshot().jobs.hasMore);
  expect(store.getSnapshot().summary?.jobs.total).toBe(201);
  expect(store.getSnapshot().jobs).toMatchObject({ complete: false, hasMore: true });
  await store.loadMore("jobs");
  expect(store.getSnapshot().jobs.rows.map(({ jobId, status }) => [jobId, status])).toEqual([
    ["shell-1", "completed"],
    ["shell-2", "running"],
  ]);
  expect(store.getSnapshot().jobs).toMatchObject({ complete: true, hasMore: false });
  expect(client.calls.filter(({ method }) => method === "evener/thread/jobs/list").map(({ params }) => params)).toEqual(
    [
      { ref: activityRef, scope: "session" },
      { ref: activityRef, scope: "session", cursor: "page-2" },
    ],
  );
});
test("advances empty incomplete scan pages automatically without authoritative emptiness", async () => {
  const client = activityClient();
  client.on("evener/thread/jobs/list", ({ cursor }) =>
    cursor === "scan-2" ? jobsFixture() : jobsFixture([], "scan-2"),
  );
  const store = owner(client);
  store.observe("jobs");
  await activityState(store, () => store.getSnapshot().jobs.complete);
  expect(store.getSnapshot().jobs.rows).toHaveLength(1);
  expect(callsTo(client, "evener/thread/jobs/list")).toBe(2);
});
test("observed failure retains useful rows and retries through the 30 second cap", async () => {
  const client = activityClient(),
    store = owner(client);
  const leave = store.observe("jobs");
  await activityState(store, () => store.getSnapshot().jobs.complete);
  client.on("evener/thread/jobs/list", () => {
    throw new Error("temporary network failure");
  });
  await store.refresh("jobs");
  expect(store.getSnapshot().jobs.rows[0]?.jobId).toBe("shell-1");
  for (const delay of [1000, 2000, 4000, 8000, 16000, 30000, 30000, 30000]) {
    const before = callsTo(client, "evener/thread/jobs/list");
    await vi.advanceTimersByTimeAsync(delay - 1);
    expect(callsTo(client, "evener/thread/jobs/list")).toBe(before);
    await vi.advanceTimersByTimeAsync(1);
    expect(callsTo(client, "evener/thread/jobs/list")).toBe(before + 1);
  }
  client.on("evener/thread/jobs/list", () => jobsFixture([jobFixture("recovered")]));
  await vi.advanceTimersByTimeAsync(30000);
  expect(store.getSnapshot().jobs).toMatchObject({ error: null, unavailable: false, complete: true });
  expect(store.getSnapshot().jobs.rows[0]?.jobId).toBe("recovered");
  leave();
});
test("one-shot loads do not acquire retry or invalidation demand", async () => {
  const client = activityClient(),
    store = owner(client);
  await store.load("jobs");
  client.on("evener/thread/jobs/list", () => {
    throw new Error("temporary");
  });
  await store.load("jobs");
  activityChanged(client, ["jobs"]);
  await vi.advanceTimersByTimeAsync(120000);
  expect(callsTo(client, "evener/thread/jobs/list")).toBe(2);
  expect(store.getSnapshot().jobs.rows).toHaveLength(1);
  expect(store.getSnapshot().jobs.error).toBeInstanceOf(Error);
});
test("stale cursor restarts only that collection and keeps useful rows until root refresh", async () => {
  const client = activityClient(),
    refreshed = deferred<SessionJobsResponse>(),
    refreshing = deferred<void>();
  let roots = 0;
  client.on("evener/thread/jobs/list", ({ cursor }) => {
    if (cursor) throw new WireError("stale", -32602, { evenerErrorInfo: "sessionActivityCursorStale" });
    if (++roots === 1) return jobsFixture([jobFixture()], "old-page");
    refreshing.resolve();
    return refreshed.promise;
  });
  const store = owner(client);
  store.start();
  await activityState(store, () => store.getSnapshot().summary !== null);
  store.observe("jobs");
  await activityState(store, () => store.getSnapshot().jobs.hasMore);
  const more = store.loadMore("jobs");
  await refreshing.promise;
  expect(store.getSnapshot().jobs.rows[0]?.jobId).toBe("shell-1");
  refreshed.resolve(jobsFixture([jobFixture("new-root")], undefined, "epoch-2"));
  await more;
  expect(store.getSnapshot().jobs.rows.map((row) => row.jobId)).toEqual(["new-root"]);
  expect(callsTo(client, "evener/thread/activity/read")).toBe(1);
});
test("partial issues preserve successful rows and retry while observed", async () => {
  const client = activityClient();
  client.on("evener/thread/jobs/list", () => ({
    ...jobsFixture(),
    page: { complete: false, issues: [{ ref: "remote:child", code: "unavailable" }] },
  }));
  const store = owner(client);
  store.observe("jobs");
  await activityState(store, () => !store.getSnapshot().jobs.loading && store.getSnapshot().jobs.issues.length > 0);
  expect(store.getSnapshot().jobs).toMatchObject({ complete: false, hasMore: false, unavailable: false });
  expect(store.getSnapshot().jobs.rows).toHaveLength(1);
  client.on("evener/thread/jobs/list", () => jobsFixture([jobFixture("recovered")]));
  await vi.advanceTimersByTimeAsync(1000);
  expect(store.getSnapshot().jobs).toMatchObject({ complete: true, issues: [] });
  expect(store.getSnapshot().jobs.rows[0]?.jobId).toBe("recovered");
});
test.each([
  new WireError("bad cursor", -32602, { evenerErrorInfo: "invalidParams" }),
  new WireError("unsupported", -32601, { evenerErrorInfo: "methodNotFound" }),
  new WireError("deleted", -32602, { evenerErrorInfo: "resourceNotFound" }),
  new WireError("deleted target", -32000, { evenerErrorInfo: "sessionUnavailable", mutationOutcome: "targetDeleted" }),
])("permanent errors never spin on reconnect", async (error) => {
  const client = activityClient();
  client.on("evener/thread/jobs/list", () => {
    throw error;
  });
  const store = owner(client);
  store.observe("jobs");
  await activityState(store, () => store.getSnapshot().jobs.permanent);
  await vi.advanceTimersByTimeAsync(120000);
  client.emitStateChange("reconnecting");
  client.emitReady();
  await store.refresh("summary");
  expect(callsTo(client, "evener/thread/jobs/list")).toBe(1);
  expect(store.getSnapshot().jobs.error).toBe(error);
});
test("reconnect and explicit refresh wake pending retries", async () => {
  const client = activityClient();
  client.on("evener/thread/jobs/list", () => {
    throw new Error("offline");
  });
  const store = owner(client);
  store.observe("jobs");
  await activityState(store, () => store.getSnapshot().jobs.error !== null);
  client.on("evener/thread/jobs/list", () => jobsFixture([jobFixture("reconnected")]));
  client.emitStateChange("reconnecting");
  client.emitReady();
  await activityState(store, () => store.getSnapshot().jobs.rows[0]?.jobId === "reconnected");
  expect(callsTo(client, "thread/read")).toBe(2);
  client.on("evener/thread/jobs/list", () => {
    throw new Error("again");
  });
  await store.refresh("jobs");
  client.on("evener/thread/jobs/list", () => jobsFixture([jobFixture("explicit")]));
  await store.refresh("jobs");
  expect(store.getSnapshot().jobs.rows[0]?.jobId).toBe("explicit");
  await vi.advanceTimersByTimeAsync(120000);
  expect(callsTo(client, "evener/thread/jobs/list")).toBe(4);
});
test("session invalidation checks affected owner while subtree accepts routed descendants", async () => {
  const client = activityClient();
  client.on("evener/thread/delegates/list", ({ scope }) => ({
    context: activityContext(),
    scope: scope ?? "session",
    page: { complete: true, issues: [] },
    delegates: [delegateFixture()],
  }));
  const own = owner(client),
    subtree = owner(client, "subtree");
  own.start();
  subtree.start();
  own.observe("delegates");
  subtree.observe("delegates");
  await activityState(own, () => own.getSnapshot().delegates.complete && own.getSnapshot().summary !== null);
  await activityState(
    subtree,
    () => subtree.getSnapshot().delegates.complete && subtree.getSnapshot().summary !== null,
  );
  const before = callsTo(client, "evener/thread/delegates/list");
  activityChanged(client, ["delegates"], "descendant");
  await activityState(
    subtree,
    () => callsTo(client, "evener/thread/delegates/list") === before + 1 && !subtree.getSnapshot().delegates.loading,
  );
  expect(callsTo(client, "evener/thread/delegates/list")).toBe(before + 1);
  activityChanged(client, ["delegates"], "session", "remote:unrelated");
  await own.refresh("summary");
  expect(callsTo(client, "evener/thread/delegates/list")).toBe(before + 1);
});
test("disposal ignores late old-client results when scope changes", async () => {
  const oldClient = activityClient(),
    late = deferred<SessionJobsResponse>(),
    entered = deferred<void>();
  oldClient.on("evener/thread/jobs/list", () => {
    entered.resolve();
    return late.promise;
  });
  const oldStore = owner(oldClient);
  oldStore.observe("jobs");
  await entered.promise;
  oldStore.dispose();
  const oldSnapshot = oldStore.getSnapshot();
  const freshClient = activityClient();
  freshClient.on("evener/thread/jobs/list", () => ({ ...jobsFixture([jobFixture("new-scope")]), scope: "subtree" }));
  const fresh = owner(freshClient, "subtree");
  await fresh.load("jobs");
  late.resolve(jobsFixture([jobFixture("obsolete")]));
  await late.promise;
  await vi.advanceTimersByTimeAsync(120000);
  expect(oldStore.getSnapshot()).toBe(oldSnapshot);
  expect(fresh.getSnapshot().jobs.rows[0]?.jobId).toBe("new-scope");
  expect(callsTo(oldClient, "evener/thread/jobs/list")).toBe(1);
});
test("a page from a different epoch is never grafted into retained rows", async () => {
  const client = activityClient();
  let roots = 0;
  client.on("evener/thread/jobs/list", ({ cursor }) =>
    cursor
      ? jobsFixture([jobFixture("wrong-page")], undefined, "epoch-2")
      : ++roots === 1
        ? jobsFixture([jobFixture()], "page-2")
        : jobsFixture([jobFixture("fresh-root")], undefined, "epoch-2"),
  );
  const store = owner(client);
  store.observe("jobs");
  await activityState(store, () => store.getSnapshot().jobs.hasMore);
  await store.loadMore("jobs");
  expect(store.getSnapshot().jobs.rows.map(({ jobId }) => jobId)).toEqual(["fresh-root"]);
});
test("pending ancestry uses bounded paced progress without error backoff", async () => {
  const client = activityClient();
  client.on("evener/thread/activity/read", () => ({
    ...summaryFixture(),
    context: { ...activityContext(), ancestryKnown: callsTo(client, "evener/thread/activity/read") >= 4 },
  }));
  const store = owner(client);
  store.start();
  await activityState(store, () => store.getSnapshot().summary !== null);
  expect(store.getSnapshot().context?.ancestryKnown).toBe(false);
  expect(store.getSnapshot().summaryState.error).toBeNull();
  await vi.advanceTimersByTimeAsync(300);
  expect(store.getSnapshot().context?.ancestryKnown).toBe(true);
  expect(callsTo(client, "evener/thread/activity/read")).toBe(4);
  await vi.advanceTimersByTimeAsync(30000);
  expect(callsTo(client, "evener/thread/activity/read")).toBe(4);
});

test("releasing pre-ready collection demand prevents a late read", async () => {
  const client = activityClient("connecting"),
    store = owner(client);
  const release = store.observe("jobs");
  expect(store.getSnapshot().jobs.pending).toBe(true);
  release();
  client.emitReady();
  await store.refresh("summary");
  expect(callsTo(client, "evener/thread/jobs/list")).toBe(0);
});

test("transient page failure retries its cursor and preserves loaded membership", async () => {
  const client = activityClient();
  let pageAttempts = 0;
  client.on("evener/thread/jobs/list", ({ cursor }) => {
    if (!cursor) return jobsFixture([jobFixture("page-1")], "page-2");
    if (++pageAttempts === 1) throw new Error("page connection lost");
    return jobsFixture([jobFixture("page-2")]);
  });
  const store = owner(client);
  store.observe("jobs");
  await activityState(store, () => store.getSnapshot().jobs.hasMore);
  await store.loadMore("jobs");
  expect(store.getSnapshot().jobs.rows.map((row) => row.jobId)).toEqual(["page-1"]);
  await vi.advanceTimersByTimeAsync(1000);
  expect(store.getSnapshot().jobs.rows.map((row) => row.jobId)).toEqual(["page-1", "page-2"]);
  expect(client.calls.filter((call) => call.method === "evener/thread/jobs/list").map((call) => call.params)).toEqual([
    { ref: activityRef, scope: "session" },
    { ref: activityRef, scope: "session", cursor: "page-2" },
    { ref: activityRef, scope: "session", cursor: "page-2" },
  ]);
});

test("existing missing-thread subscription refusal is permanent", async () => {
  const client = activityClient();
  client.on("thread/read", () => {
    throw new WireError("thread not found: session", -32012, { evenerErrorInfo: "sessionUnavailable" });
  });
  const store = owner(client);
  store.observe("jobs");
  await activityState(store, () => store.getSnapshot().jobs.error !== null);
  expect(store.getSnapshot().jobs).toMatchObject({ permanent: true, unavailable: true });
  await vi.advanceTimersByTimeAsync(120000);
  expect(callsTo(client, "thread/read")).toBe(1);
});

test("observed unknown counts refresh after bounded collection progress without starting scans", async () => {
  const client = activityClient();
  let reconstructed = false;
  client.on("evener/thread/activity/read", () => ({
    ...summaryFixture(),
    jobs: { known: reconstructed, total: reconstructed ? 321 : 0, active: 0, failed: 0, completed: 0 },
  }));
  client.on("evener/thread/jobs/list", ({ cursor }) => {
    if (!cursor) return jobsFixture([jobFixture("first")], "next");
    reconstructed = true;
    return jobsFixture([jobFixture("last")]);
  });
  const store = owner(client);
  store.start();
  await activityState(store, () => store.getSnapshot().summary !== null);
  expect(store.getSnapshot().context?.ancestryKnown).toBe(true);
  await vi.advanceTimersByTimeAsync(30000);
  expect(callsTo(client, "evener/thread/jobs/list")).toBe(0);
  expect(callsTo(client, "evener/thread/activity/read")).toBe(1);
  store.observe("jobs");
  await activityState(store, () => store.getSnapshot().jobs.hasMore && !store.getSnapshot().jobs.loading);
  await vi.advanceTimersByTimeAsync(100);
  expect(callsTo(client, "evener/thread/activity/read")).toBe(2);
  expect(store.getSnapshot().summary?.jobs.known).toBe(false);
  expect(store.getSnapshot().jobs.complete).toBe(false);
  await vi.advanceTimersByTimeAsync(30000);
  expect(callsTo(client, "evener/thread/activity/read")).toBe(2);
  expect(callsTo(client, "evener/thread/jobs/list")).toBe(1);
  await store.loadMore("jobs");
  await vi.advanceTimersByTimeAsync(100);
  expect(store.getSnapshot().summary?.jobs).toMatchObject({ known: true, total: 321 });
  expect(store.getSnapshot().jobs.rows).toHaveLength(2);
  expect(callsTo(client, "evener/thread/activity/read")).toBe(3);
  await vi.advanceTimersByTimeAsync(30000);
  expect(callsTo(client, "evener/thread/activity/read")).toBe(3);

  const unobserved = owner(client);
  await unobserved.load("jobs");
  await unobserved.loadMore("jobs");
  await vi.advanceTimersByTimeAsync(30000);
  expect(callsTo(client, "evener/thread/activity/read")).toBe(3);
});

test("summary-only warm recovery polls typed demand without collection scans and stops on release", async () => {
  const client = activityClient();
  let recovering = true;
  client.on("evener/thread/activity/read", () => ({
    ...summaryFixture(),
    refreshPending: recovering,
    jobs: { known: !recovering, total: recovering ? 0 : 6, active: recovering ? 0 : 2, failed: 1, completed: 3 },
  }));
  const store = owner(client);
  store.start();
  await activityState(store, () => store.getSnapshot().summary !== null);
  expect(store.getSnapshot().summaryState.pending).toBe(true);
  await vi.advanceTimersByTimeAsync(100);
  expect(callsTo(client, "evener/thread/activity/read")).toBe(2);
  recovering = false;
  await vi.advanceTimersByTimeAsync(100);
  expect(store.getSnapshot().summary?.jobs).toMatchObject({ known: true, active: 2, total: 6 });
  expect(store.getSnapshot().summaryState.pending).toBe(false);
  await vi.advanceTimersByTimeAsync(1000);
  expect(callsTo(client, "evener/thread/activity/read")).toBe(3);
  recovering = true;
  await store.refresh("summary");
  store.dispose();
  await vi.advanceTimersByTimeAsync(1000);
  expect(callsTo(client, "evener/thread/activity/read")).toBe(4);
  expect(callsTo(client, "evener/thread/jobs/list")).toBe(0);
  expect(callsTo(client, "evener/thread/watches/list")).toBe(0);
});

test("automatic warm summary failure revokes count trust but preserves context and collection rows", async () => {
  const client = activityClient();
  const store = owner(client);
  store.start();
  await activityState(store, () => store.getSnapshot().summary !== null);
  await store.load("jobs");
  const rows = store.getSnapshot().jobs.rows;
  const context = store.getSnapshot().context;
  client.on("evener/thread/activity/read", () => {
    throw new WireError("source unavailable", -32003, {
      evenerErrorInfo: "actionUnavailable",
      retryDisposition: "automatic",
    });
  });
  await store.refresh("summary");
  expect(store.getSnapshot().summary).toBeNull();
  expect(store.getSnapshot().context).toEqual(context);
  expect(store.getSnapshot().jobs.rows).toBe(rows);
  expect(store.getSnapshot().summaryState.permanent).toBe(false);
  client.on("evener/thread/activity/read", () => summaryFixture());
  await vi.advanceTimersByTimeAsync(1000);
  expect(store.getSnapshot().summary?.jobs.known).toBe(true);
});

test("warm summary polling pauses offline and resumes through the existing ready owner", async () => {
  const client = activityClient();
  client.on("evener/thread/activity/read", () => ({ ...summaryFixture(), refreshPending: true }));
  const store = owner(client);
  store.start();
  await activityState(store, () => store.getSnapshot().summary !== null);
  client.emitStateChange("reconnecting");
  await vi.advanceTimersByTimeAsync(1000);
  expect(callsTo(client, "evener/thread/activity/read")).toBe(1);
  client.on("evener/thread/activity/read", () => summaryFixture());
  client.emitReady();
  await activityState(
    store,
    () => callsTo(client, "evener/thread/activity/read") === 2 && !store.getSnapshot().summaryState.pending,
  );
  await vi.advanceTimersByTimeAsync(1000);
  expect(callsTo(client, "evener/thread/activity/read")).toBe(2);
  expect(callsTo(client, "evener/thread/jobs/list")).toBe(0);
  expect(callsTo(client, "evener/thread/watches/list")).toBe(0);
});

test("alias resync fences delayed pre-clear summary and collection replies", async () => {
  const client = activityClient();
  const oldSummary = deferred<SessionActivitySummary>(),
    newSummary = deferred<SessionActivitySummary>();
  const oldDelegates = deferred<SessionDelegatesResponse>(),
    newDelegates = deferred<SessionDelegatesResponse>();
  const entered = deferred<void>(),
    summaryEntered = deferred<void>(),
    delegatesEntered = deferred<void>();
  let summaryReads = 0,
    delegateReads = 0;
  client.on("evener/thread/activity/read", () => {
    if (++summaryReads === 1) return oldSummary.promise;
    summaryEntered.resolve();
    return newSummary.promise;
  });
  client.on("evener/thread/delegates/list", () => {
    delegateReads += 1;
    entered.resolve();
    if (delegateReads === 1) return oldDelegates.promise;
    delegatesEntered.resolve();
    return newDelegates.promise;
  });
  const ref = "remote:workspace";
  const store = new SessionActivityStore(client, ref);
  owners.push(store);
  store.start();
  store.observe("delegates");
  await entered.promise;
  client.emitNotification({ method: "evener/thread/resync", params: { ref, threadId: "new-session" } });
  oldSummary.resolve(summaryFixture());
  oldDelegates.resolve({
    context: activityContext(),
    scope: "session",
    delegates: [{ ...delegateFixture(), type: "delegate" }],
    page: { complete: true, issues: [] },
  });
  await Promise.all([summaryEntered.promise, delegatesEntered.promise]);
  expect(summaryReads).toBe(2);
  expect(delegateReads).toBe(2);
  expect(store.getSnapshot().summary).toBeNull();
  expect(store.getSnapshot().delegates.rows).toHaveLength(0);
  expect(store.getSnapshot().summaryState.loading).toBe(true);
  const context = { ...activityContext(), sessionId: "new-session", ref: "remote:new", epoch: "new-epoch" };
  newSummary.resolve({ ...summaryFixture(), context });
  newDelegates.resolve({ context, scope: "session", delegates: [], page: { complete: true, issues: [] } });
  await activityState(
    store,
    () =>
      store.getSnapshot().context?.sessionId === "new-session" &&
      !store.getSnapshot().summaryState.loading &&
      !store.getSnapshot().delegates.loading,
  );
  expect(store.getSnapshot().ref).toBe(ref);
  expect(callsTo(client, "thread/read")).toBe(1);
  expect(callsTo(client, "thread/unsubscribe")).toBe(0);
});

test("replacement identity retires prior rows, cursors and counts atomically before staggered pages", async () => {
  const client = activityClient(),
    store = owner(client);
  client.on("evener/thread/delegates/list", () => ({
    context: activityContext(),
    scope: "session",
    delegates: [{ ...delegateFixture(), type: "delegate" }],
    page: { complete: true, issues: [] },
  }));
  client.on("evener/thread/jobs/list", () => ({
    ...jobsFixture([jobFixture()], "old-cursor"),
    page: { complete: false, nextCursor: "old-cursor", issues: [{ ref: activityRef, code: "sourceUnavailable" }] },
  }));
  store.start();
  store.observe("delegates");
  store.observe("jobs");
  await activityState(
    store,
    () =>
      store.getSnapshot().delegates.complete &&
      store.getSnapshot().jobs.hasMore &&
      store.getSnapshot().summary !== null,
  );
  const context = { ...activityContext(), sessionId: "replacement", ref: "remote:new", epoch: "opaque-new" };
  const pending = deferred<SessionDelegatesResponse>(),
    jobs = deferred<SessionJobsResponse>();
  client.on("evener/thread/activity/read", () => ({ ...summaryFixture(), context }));
  client.on("evener/thread/delegates/list", () => pending.promise);
  client.on("evener/thread/jobs/list", () => jobs.promise);
  client.emitNotification({ method: "evener/thread/resync", params: { ref: activityRef, threadId: "replacement" } });
  await activityState(store, () => store.getSnapshot().context?.sessionId === "replacement");
  expect(store.getSnapshot().delegates).toMatchObject({
    rows: [],
    context: null,
    complete: false,
    hasMore: false,
    issues: [],
  });
  expect(store.getSnapshot().jobs).toMatchObject({
    rows: [],
    context: null,
    complete: false,
    hasMore: false,
    issues: [],
  });
  expect(projectSessionActivity(store.getSnapshot()).tree?.root.entries).toHaveLength(0);
  const readsBeforeMore = callsTo(client, "evener/thread/jobs/list");
  await store.loadMore("jobs");
  expect(callsTo(client, "evener/thread/jobs/list")).toBe(readsBeforeMore);
  pending.resolve({
    context,
    scope: "session",
    delegates: [{ ...delegateFixture("replacement-delegate"), ownerRef: context.ref, type: "delegate" }],
    page: { complete: true, issues: [] },
  });
  jobs.resolve({
    ...jobsFixture(),
    context,
    jobs: [{ ...jobFixture("replacement-job"), ownerSessionId: context.sessionId, ownerRef: context.ref }],
  });
  await activityState(
    store,
    () =>
      store.getSnapshot().delegates.complete && store.getSnapshot().jobs.complete && !store.getSnapshot().jobs.loading,
  );
  expect(store.getSnapshot().jobs.rows.map((row) => row.jobId)).toEqual(["replacement-job"]);
  expect(callsTo(client, "evener/thread/watches/list")).toBe(0);
});

test("a replacement collection retires the former summary before its replacement arrives", async () => {
  const client = activityClient(),
    store = owner(client);
  store.start();
  store.observe("jobs");
  await activityState(store, () => store.getSnapshot().jobs.complete && store.getSnapshot().summary !== null);
  const replacement = { ...activityContext(), sessionId: "replacement", ref: "remote:new" };
  const pending = deferred<SessionActivitySummary>();
  client.on("evener/thread/activity/read", () => pending.promise);
  client.on("evener/thread/jobs/list", () => ({
    ...jobsFixture([{ ...jobFixture("new-job"), ownerSessionId: replacement.sessionId, ownerRef: replacement.ref }]),
    context: replacement,
  }));
  client.emitNotification({
    method: "evener/thread/resync",
    params: { ref: activityRef, threadId: replacement.sessionId },
  });
  await activityState(store, () => store.getSnapshot().jobs.rows[0]?.jobId === "new-job");
  expect(store.getSnapshot().summary).toBeNull();
  expect(store.getSnapshot().summaryState.loading).toBe(true);
  pending.resolve({ ...summaryFixture(), context: replacement });
  await activityState(store, () => store.getSnapshot().summary !== null && !store.getSnapshot().summaryState.loading);
  expect(store.getSnapshot().context?.sessionId).toBe(replacement.sessionId);
});

test("same-session opaque epoch recovery retains other useful rows and its retry owner", async () => {
  const client = activityClient(),
    store = owner(client);
  store.start();
  store.observe("jobs");
  store.observe("delegates");
  await activityState(
    store,
    () =>
      store.getSnapshot().jobs.complete &&
      store.getSnapshot().delegates.complete &&
      store.getSnapshot().summary !== null,
  );
  const before = store.getSnapshot();
  client.on("evener/thread/activity/read", () => ({
    ...summaryFixture(),
    context: activityContext("opaque-cache-reset"),
    jobs: { known: false, total: 0, active: 0, failed: 0, completed: 0 },
  }));
  client.on("evener/thread/jobs/list", () => {
    throw new Error("source temporarily unavailable");
  });
  client.emitNotification({ method: "evener/thread/resync", params: { ref: activityRef, threadId: "session" } });
  await activityState(
    store,
    () => store.getSnapshot().context?.epoch === "opaque-cache-reset" && store.getSnapshot().jobs.error !== null,
  );
  expect(store.getSnapshot().jobs.rows).toEqual(before.jobs.rows);
  expect(store.getSnapshot().delegates.rows).toEqual(before.delegates.rows);
  expect(store.getSnapshot().summary?.jobs.known).toBe(false);
  const reads = callsTo(client, "evener/thread/jobs/list");
  await vi.advanceTimersByTimeAsync(999);
  expect(callsTo(client, "evener/thread/jobs/list")).toBe(reads);
  client.on("evener/thread/jobs/list", () => jobsFixture([jobFixture("recovered")], undefined, "opaque-cache-reset"));
  await vi.advanceTimersByTimeAsync(1);
  expect(store.getSnapshot().jobs.rows[0]?.jobId).toBe("recovered");
  expect(store.getSnapshot().jobs.error).toBeNull();
  expect(callsTo(client, "evener/thread/watches/list")).toBe(0);
});

test("resync during replacement context publication cannot bless the fenced completion", async () => {
  const client = activityClient(),
    store = owner(client);
  store.start();
  await activityState(store, () => store.getSnapshot().summary !== null);
  const context = { ...activityContext(), sessionId: "replacement" };
  let reads = 0;
  client.on("evener/thread/activity/read", () => ({
    ...summaryFixture(),
    context,
    jobs: { known: true, total: ++reads, active: 0, failed: 0, completed: 0 },
  }));
  const published: number[] = [];
  let resynced = false;
  store.subscribe(() => {
    const state = store.getSnapshot();
    if (state.summary?.context.sessionId === context.sessionId) published.push(state.summary.jobs.total);
    if (state.context?.sessionId === context.sessionId && !state.summary && !resynced) {
      resynced = true;
      client.emitNotification({
        method: "evener/thread/resync",
        params: { ref: activityRef, threadId: context.sessionId },
      });
    }
  });
  await store.refresh("summary");
  expect(store.getSnapshot().summary?.jobs.total).toBe(2);
  expect(published).not.toContain(1);
});

test("alias resync rearms demanded reads after the former target became unavailable", async () => {
  const client = activityClient(),
    store = owner(client);
  store.start();
  await activityState(store, () => store.getSnapshot().summary !== null);
  client.on("evener/thread/activity/read", () => {
    throw new WireError("former target is gone", -32601, { evenerErrorInfo: "methodNotFound" });
  });
  await store.refresh("summary");
  expect(store.getSnapshot().summaryState.permanent).toBe(true);
  const context = { ...activityContext(), sessionId: "replacement" };
  client.on("evener/thread/activity/read", () => ({ ...summaryFixture(), context }));
  client.emitNotification({
    method: "evener/thread/resync",
    params: { ref: activityRef, threadId: context.sessionId },
  });
  await activityState(store, () => store.getSnapshot().summary?.context.sessionId === context.sessionId);
  expect(store.getSnapshot().summaryState).toMatchObject({ error: null, permanent: false, unavailable: false });
  expect(callsTo(client, "evener/thread/delegates/list")).toBe(0);
});

test("automatic source failures revoke summary trust and retain rows with paced retry", async () => {
  const client = activityClient(),
    store = owner(client);
  store.start();
  const leave = store.observe("jobs");
  await activityState(store, () => store.getSnapshot().summary !== null && store.getSnapshot().jobs.complete);
  const error = new WireError("session activity metadata unavailable", -32014, {
    evenerErrorInfo: "actionUnavailable",
    retryDisposition: "automatic",
  });
  client.on("evener/thread/activity/read", () => {
    throw error;
  });
  client.on("evener/thread/jobs/list", () => {
    throw error;
  });
  await Promise.all([store.refresh("summary"), store.refresh("jobs")]);
  expect(store.getSnapshot().summaryState).toMatchObject({ permanent: false, unavailable: false, error });
  expect(store.getSnapshot().jobs).toMatchObject({ permanent: false, unavailable: false, error });
  expect(store.getSnapshot().summary).toBeNull();
  expect(store.getSnapshot().jobs.rows[0]?.jobId).toBe("shell-1");
  await vi.advanceTimersByTimeAsync(999);
  expect(callsTo(client, "evener/thread/activity/read")).toBe(2);
  expect(callsTo(client, "evener/thread/jobs/list")).toBe(2);
  client.on("evener/thread/activity/read", () => ({
    ...summaryFixture(),
    jobs: { known: true, total: 7, active: 1, failed: 0, completed: 6 },
  }));
  client.on("evener/thread/jobs/list", () => jobsFixture([jobFixture("healed")]));
  await vi.advanceTimersByTimeAsync(1);
  expect(store.getSnapshot().summary?.jobs.total).toBe(7);
  expect(store.getSnapshot().summaryState).toMatchObject({ error: null, permanent: false, unavailable: false });
  expect(store.getSnapshot().jobs.rows[0]?.jobId).toBe("healed");
  expect(store.getSnapshot().jobs).toMatchObject({ error: null, complete: true });
  await vi.advanceTimersByTimeAsync(120000);
  expect(callsTo(client, "evener/thread/activity/read")).toBe(3);
  expect(callsTo(client, "evener/thread/jobs/list")).toBe(3);
  leave();
});

test("typed activity invalidation heals automatic source failure before its retry deadline", async () => {
  const client = activityClient(),
    store = owner(client);
  store.observe("jobs");
  await activityState(store, () => store.getSnapshot().jobs.complete);
  client.on("evener/thread/jobs/list", () => {
    throw new WireError("source unavailable", -32014, {
      evenerErrorInfo: "actionUnavailable",
      retryDisposition: "automatic",
    });
  });
  await store.refresh("jobs");
  client.on("evener/thread/jobs/list", () => jobsFixture([jobFixture("healed-invalidation")]));
  activityChanged(client, ["jobs"]);
  await activityState(store, () => store.getSnapshot().jobs.rows[0]?.jobId === "healed-invalidation");
  expect(store.getSnapshot().jobs.error).toBeNull();
  await vi.advanceTimersByTimeAsync(120000);
  expect(callsTo(client, "evener/thread/jobs/list")).toBe(3);
});

test("automatic source failure loses retry and invalidation demand when released", async () => {
  const client = activityClient(),
    store = owner(client);
  const error = new WireError("source unavailable", -32014, {
    evenerErrorInfo: "actionUnavailable",
    retryDisposition: "automatic",
  });
  client.on("evener/thread/jobs/list", () => {
    throw error;
  });
  const leave = store.observe("jobs");
  await activityState(store, () => store.getSnapshot().jobs.error !== null && !store.getSnapshot().jobs.loading);
  leave();
  client.on("evener/thread/jobs/list", () => jobsFixture());
  activityChanged(client, ["jobs"]);
  await vi.advanceTimersByTimeAsync(120000);
  expect(callsTo(client, "evener/thread/jobs/list")).toBe(1);
  expect(store.getSnapshot().jobs.rows).toHaveLength(0);
});

test.each([
  { evenerErrorInfo: "actionUnavailable" },
  { evenerErrorInfo: "actionUnavailable", retryDisposition: "blocked" },
  { evenerErrorInfo: "actionUnavailable", retryDisposition: "none" },
  { evenerErrorInfo: "resourceNotFound", retryDisposition: "automatic" },
  { evenerErrorInfo: "methodNotFound", retryDisposition: "automatic" },
  { evenerErrorInfo: "actionUnavailable", mutationOutcome: "targetDeleted", retryDisposition: "automatic" },
])("definitive unavailable activity remains parked with %o", async (data) => {
  const client = activityClient(),
    store = owner(client);
  const error = new WireError("unavailable", -32014, data);
  client.on("evener/thread/jobs/list", () => {
    throw error;
  });
  store.observe("jobs");
  await activityState(store, () => store.getSnapshot().jobs.permanent);
  client.on("evener/thread/jobs/list", () => jobsFixture());
  activityChanged(client, ["jobs"]);
  client.emitStateChange("reconnecting");
  client.emitReady();
  await vi.advanceTimersByTimeAsync(120000);
  expect(callsTo(client, "evener/thread/jobs/list")).toBe(1);
  expect(store.getSnapshot().jobs).toMatchObject({ permanent: true, unavailable: true, error });
});

const retainedWatch = (id: string, state: "armed" | "ended" = "armed"): SessionWatch => ({
  ownerRef: activityRef,
  receiverRef: activityRef,
  state,
  watch: { id, source: "job", createdAt: "2026-09-30T12:00:00Z", active: state === "armed", deliveries: 0 },
});

// One wire boundary drives the three typed collections through the same owner.
function retentionPage(ids: number[], cursor?: string, updated = false, epoch = "epoch-1") {
  return {
    context: activityContext(epoch),
    scope: "session" as const,
    page: { complete: !cursor, issues: [], ...(cursor ? { nextCursor: cursor } : {}) },
    delegates: ids.map((id) => ({
      ...delegateFixture(`item-${id}`),
      terminal: updated,
      status: updated ? "completed" : "running",
      lifecycle: updated ? "idle" : "running",
      phase: updated ? "done" : "running",
    })),
    jobs: ids.map((id) => jobFixture(`item-${id}`, updated ? "completed" : "running")),
    watches: ids.map((id) => retainedWatch(`item-${id}`, updated ? "ended" : "armed")),
  };
}

test.each(["delegates", "jobs", "watches"] as const)(
  "%s invalidation preserves loaded later-page work throughout a bounded fresh walk",
  async (resource) => {
    const client = activityClient(),
      store = owner(client);
    const initialFirst = Array.from({ length: 50 }, (_, index) => index + 1);
    const initialSecond = Array.from({ length: 10 }, (_, index) => index + 51);
    const next = deferred<ReturnType<typeof retentionPage>>(),
      entered = deferred<void>();
    let refreshing = false;
    const cursors: (string | undefined)[] = [];
    const read = ({ cursor }: { cursor?: string }) => {
      cursors.push(cursor);
      if (!refreshing) return retentionPage(cursor ? initialSecond : initialFirst, cursor ? "old-third" : "old-second");
      if (!cursor) return retentionPage(initialFirst, "fresh-second", true, "epoch-2");
      entered.resolve();
      return next.promise;
    };
    client.on("evener/thread/delegates/list", read);
    client.on("evener/thread/jobs/list", read);
    client.on("evener/thread/watches/list", read);
    store.observe(resource);
    await activityState(
      store,
      () => !store.getSnapshot()[resource].loading && store.getSnapshot()[resource].rows.length === 50,
    );
    await store.loadMore(resource);
    expect(store.getSnapshot()[resource].rows).toHaveLength(60);
    const published: number[] = [];
    store.subscribe(() => published.push(store.getSnapshot()[resource].rows.length));
    refreshing = true;
    activityChanged(client, [resource]);
    // Await the actual root completion publication; the former bug stops here.
    await activityState(store, () => store.getSnapshot()[resource].context?.epoch === "epoch-2");
    expect(store.getSnapshot()[resource].rows).toHaveLength(60);
    expect(published.every((count) => count >= 60)).toBe(true);
    await vi.advanceTimersByTimeAsync(100);
    await entered.promise;
    expect(store.getSnapshot()[resource].loading).toBe(true);
    next.resolve(
      retentionPage(
        initialSecond.filter((id) => id !== 58),
        "fresh-third",
        true,
        "epoch-2",
      ),
    );
    await activityState(store, () => !store.getSnapshot()[resource].loading);
    expect(store.getSnapshot()[resource].rows).toHaveLength(59);
    expect(store.getSnapshot()[resource].rows).toEqual(
      retentionPage([...initialFirst, ...initialSecond.filter((id) => id !== 58)], undefined, true, "epoch-2")[
        resource
      ],
    );
    expect(store.getSnapshot()[resource].hasMore).toBe(true);
    expect(cursors).toEqual([undefined, "old-second", undefined, "fresh-second"]);
  },
);

test("failed refresh continuation retains later rows and retries only its fresh cursor", async () => {
  const client = activityClient(),
    store = owner(client);
  let refreshing = false,
    attempts = 0;
  client.on("evener/thread/jobs/list", ({ cursor }) => {
    if (!refreshing) return jobsFixture([jobFixture(cursor ? "older" : "newer")], cursor ? "old-rest" : "old-second");
    if (!cursor) return jobsFixture([jobFixture("newer", "completed")], "fresh-second", "epoch-2");
    if (++attempts === 1) throw new Error("refresh continuation lost");
    return jobsFixture([jobFixture("older", "completed")], "fresh-rest", "epoch-2");
  });
  store.observe("jobs");
  await activityState(store, () => store.getSnapshot().jobs.hasMore && !store.getSnapshot().jobs.loading);
  await store.loadMore("jobs");
  refreshing = true;
  const refresh = store.refresh("jobs");
  await vi.advanceTimersByTimeAsync(100);
  await refresh;
  expect(store.getSnapshot().jobs.rows.map((row) => [row.jobId, row.status])).toEqual([
    ["newer", "completed"],
    ["older", "running"],
  ]);
  expect(store.getSnapshot().jobs.error).toBeInstanceOf(Error);
  await vi.advanceTimersByTimeAsync(1000);
  expect(store.getSnapshot().jobs.rows.map((row) => [row.jobId, row.status])).toEqual([
    ["newer", "completed"],
    ["older", "completed"],
  ]);
  expect(store.getSnapshot().jobs.error).toBeNull();
  expect(client.calls.filter((call) => call.method === "evener/thread/jobs/list").map((call) => call.params)).toEqual([
    { ref: activityRef, scope: "session" },
    { ref: activityRef, scope: "session", cursor: "old-second" },
    { ref: activityRef, scope: "session" },
    { ref: activityRef, scope: "session", cursor: "fresh-second" },
    { ref: activityRef, scope: "session", cursor: "fresh-second" },
  ]);
});

test("same-session epoch reset with partial issues cannot prove old membership absent", async () => {
  const client = activityClient(),
    store = owner(client);
  client.on("evener/thread/jobs/list", ({ cursor }) =>
    jobsFixture([jobFixture(cursor ? "older" : "newer")], cursor ? undefined : "old-second"),
  );
  store.observe("jobs");
  await activityState(store, () => store.getSnapshot().jobs.hasMore && !store.getSnapshot().jobs.loading);
  await store.loadMore("jobs");
  client.on("evener/thread/jobs/list", () => ({
    ...jobsFixture([jobFixture("newer", "completed")], undefined, "epoch-2"),
    page: { complete: true, issues: [{ ref: "remote:child", code: "sourceUnavailable" }] },
  }));
  await store.refresh("jobs");
  expect(store.getSnapshot().jobs.rows.map((row) => row.jobId)).toEqual(["newer", "older"]);
  expect(store.getSnapshot().jobs.complete).toBe(false);
  client.on("evener/thread/jobs/list", () => jobsFixture([jobFixture("newer", "completed")], undefined, "epoch-2"));
  await vi.advanceTimersByTimeAsync(1000);
  expect(store.getSnapshot().jobs.rows.map((row) => row.jobId)).toEqual(["newer"]);
  expect(store.getSnapshot().jobs.complete).toBe(true);
});

test("refresh follows the former last identity past inserted rows instead of stopping at the old count", async () => {
  const client = activityClient(),
    store = owner(client);
  client.on("evener/thread/jobs/list", ({ cursor }) =>
    jobsFixture([jobFixture(cursor ? "older" : "newer")], cursor ? "old-rest" : "old-second"),
  );
  await store.load("jobs");
  await store.loadMore("jobs");
  client.on("evener/thread/jobs/list", ({ cursor }) =>
    !cursor
      ? jobsFixture([jobFixture("inserted"), jobFixture("newer", "completed")], "fresh-second")
      : jobsFixture([jobFixture("older", "completed")], "fresh-rest"),
  );
  const refresh = store.refresh("jobs");
  await vi.advanceTimersByTimeAsync(100);
  await refresh;
  expect(store.getSnapshot().jobs.rows.map((row) => [row.jobId, row.status])).toEqual([
    ["inserted", "running"],
    ["newer", "completed"],
    ["older", "completed"],
  ]);
  expect(callsTo(client, "evener/thread/jobs/list")).toBe(4);
  expect(client.listenerCount).toBe(0);
});

test("a removed boundary remains useful until paced fresh completion proves it absent", async () => {
  const client = activityClient(),
    store = owner(client);
  client.on("evener/thread/jobs/list", ({ cursor }) =>
    jobsFixture([jobFixture(cursor ? "removed-boundary" : "newer")], cursor ? "old-rest" : "old-second"),
  );
  await store.load("jobs");
  await store.loadMore("jobs");
  const last = deferred<SessionJobsResponse>(),
    entered = deferred<void>();
  client.on("evener/thread/jobs/list", ({ cursor }) => {
    if (!cursor) return jobsFixture([jobFixture("newer", "completed")], "fresh-second");
    if (cursor === "fresh-second") return jobsFixture([jobFixture("formerly-unseen")], "fresh-third");
    entered.resolve();
    return last.promise;
  });
  const refresh = store.refresh("jobs");
  await vi.advanceTimersByTimeAsync(200);
  await entered.promise;
  expect(store.getSnapshot().jobs.rows.map((row) => row.jobId)).toEqual([
    "newer",
    "removed-boundary",
    "formerly-unseen",
  ]);
  last.resolve(jobsFixture([jobFixture("last")]));
  await refresh;
  expect(store.getSnapshot().jobs.rows.map((row) => row.jobId)).toEqual(["newer", "formerly-unseen", "last"]);
  expect(store.getSnapshot().jobs.complete).toBe(true);
  expect(callsTo(client, "evener/thread/jobs/list")).toBe(5);
});

test("invalidations coalesce behind the admitted refresh so later rows are not starved", async () => {
  const client = activityClient(),
    store = owner(client);
  client.on("evener/thread/jobs/list", ({ cursor }) =>
    jobsFixture([jobFixture(cursor ? "older" : "newer")], cursor ? "old-rest" : "old-second"),
  );
  const leave = store.observe("jobs");
  await activityState(store, () => store.getSnapshot().jobs.hasMore && !store.getSnapshot().jobs.loading);
  await store.loadMore("jobs");
  const page = deferred<SessionJobsResponse>(),
    entered = deferred<void>();
  let rootReads = 0;
  client.on("evener/thread/jobs/list", ({ cursor }) => {
    if (!cursor) {
      rootReads++;
      return jobsFixture([jobFixture("newer", "completed")], "fresh-second");
    }
    if (rootReads === 1) {
      entered.resolve();
      return page.promise;
    }
    return jobsFixture([jobFixture("older", "completed")], "fresh-rest");
  });
  activityChanged(client, ["jobs"]);
  await vi.advanceTimersByTimeAsync(100);
  await entered.promise;
  activityChanged(client, ["jobs"]);
  activityChanged(client, ["jobs"]);
  expect(rootReads).toBe(1);
  page.resolve(jobsFixture([jobFixture("older", "completed")], "fresh-rest"));
  await vi.advanceTimersByTimeAsync(100);
  await activityState(store, () => !store.getSnapshot().jobs.loading);
  expect(rootReads).toBe(2);
  expect(store.getSnapshot().jobs.rows.map((row) => row.status)).toEqual(["completed", "completed"]);
  leave();
});

test.each(["release", "dispose", "offline"] as const)(
  "%s stops automatic refresh continuation while retaining visible rows",
  async (action) => {
    const client = activityClient(),
      store = owner(client);
    client.on("evener/thread/jobs/list", ({ cursor }) =>
      jobsFixture([jobFixture(cursor ? "older" : "newer")], cursor ? "old-rest" : "old-second"),
    );
    const leave = store.observe("jobs");
    await activityState(store, () => store.getSnapshot().jobs.hasMore && !store.getSnapshot().jobs.loading);
    await store.loadMore("jobs");
    client.on("evener/thread/jobs/list", () =>
      jobsFixture([jobFixture("newer", "completed")], "fresh-second", "epoch-2"),
    );
    activityChanged(client, ["jobs"]);
    await activityState(store, () => store.getSnapshot().jobs.context?.epoch === "epoch-2");
    if (action === "release") leave();
    else if (action === "dispose") store.dispose();
    else client.emitStateChange("reconnecting");
    await vi.advanceTimersByTimeAsync(1000);
    expect(callsTo(client, "evener/thread/jobs/list")).toBe(3);
    expect(store.getSnapshot().jobs.rows.map((row) => row.jobId)).toEqual(["newer", "older"]);
  },
);

test("loadMore admitted during refresh advances from its fresh remainder without mixing walks", async () => {
  const client = activityClient(),
    store = owner(client);
  client.on("evener/thread/jobs/list", ({ cursor }) =>
    jobsFixture([jobFixture(cursor ? "older" : "newer")], cursor ? "old-rest" : "old-second"),
  );
  await store.load("jobs");
  await store.loadMore("jobs");
  const root = deferred<SessionJobsResponse>(),
    entered = deferred<void>();
  client.on("evener/thread/jobs/list", ({ cursor }) => {
    if (!cursor) {
      entered.resolve();
      return root.promise;
    }
    return cursor === "fresh-second"
      ? jobsFixture([jobFixture("older", "completed")], "fresh-rest")
      : jobsFixture([jobFixture("unseen")]);
  });
  const refresh = store.refresh("jobs");
  await entered.promise;
  const more = store.loadMore("jobs");
  root.resolve(jobsFixture([jobFixture("newer", "completed")], "fresh-second"));
  await vi.advanceTimersByTimeAsync(100);
  await Promise.all([refresh, more]);
  expect(store.getSnapshot().jobs.rows.map((row) => row.jobId)).toEqual(["newer", "older", "unseen"]);
  expect(client.calls.filter((call) => call.method === "evener/thread/jobs/list").map((call) => call.params)).toEqual([
    { ref: activityRef, scope: "session" },
    { ref: activityRef, scope: "session", cursor: "old-second" },
    { ref: activityRef, scope: "session" },
    { ref: activityRef, scope: "session", cursor: "fresh-second" },
    { ref: activityRef, scope: "session", cursor: "fresh-rest" },
  ]);
});

test("empty refresh scan pages advance at the paced boundary without dropping displayed rows", async () => {
  const client = activityClient(),
    store = owner(client);
  client.on("evener/thread/jobs/list", ({ cursor }) =>
    jobsFixture([jobFixture(cursor ? "older" : "newer")], cursor ? "old-rest" : "old-second"),
  );
  await store.load("jobs");
  await store.loadMore("jobs");
  client.on("evener/thread/jobs/list", ({ cursor }) =>
    !cursor
      ? jobsFixture([], "scan")
      : cursor === "scan"
        ? jobsFixture([jobFixture("newer", "completed")], "older")
        : jobsFixture([jobFixture("older", "completed")], "rest"),
  );
  const refresh = store.refresh("jobs");
  await vi.advanceTimersByTimeAsync(99);
  expect(callsTo(client, "evener/thread/jobs/list")).toBe(3);
  expect(store.getSnapshot().jobs.rows.map((row) => row.jobId)).toEqual(["newer", "older"]);
  await vi.advanceTimersByTimeAsync(101);
  await refresh;
  expect(callsTo(client, "evener/thread/jobs/list")).toBe(5);
  expect(store.getSnapshot().jobs.rows.map((row) => row.status)).toEqual(["completed", "completed"]);
});

test("a stale cursor during refresh starts a new walk and never reuses its provisional membership", async () => {
  const client = activityClient(),
    store = owner(client);
  client.on("evener/thread/jobs/list", ({ cursor }) =>
    jobsFixture([jobFixture(cursor ? "older" : "newer")], cursor ? "old-rest" : "old-second"),
  );
  await store.load("jobs");
  await store.loadMore("jobs");
  let roots = 0;
  client.on("evener/thread/jobs/list", ({ cursor }) => {
    if (!cursor)
      return ++roots === 1
        ? jobsFixture([jobFixture("provisional"), jobFixture("newer")], "stale", "epoch-2")
        : jobsFixture([jobFixture("newer", "completed")], "fresh", "epoch-3");
    if (cursor === "stale") throw new WireError("stale", -32602, { evenerErrorInfo: "sessionActivityCursorStale" });
    return jobsFixture([jobFixture("older", "completed")], "rest", "epoch-3");
  });
  const refresh = store.refresh("jobs");
  await vi.advanceTimersByTimeAsync(200);
  expect(store.getSnapshot().jobs.rows.map((row) => row.jobId)).toEqual(["newer", "older"]);
  await refresh;
  expect(store.getSnapshot().jobs.context?.epoch).toBe("epoch-3");
  expect(roots).toBe(2);
});

test("reconnect fences an interrupted refresh and rebuilds its displayed extent with a new epoch", async () => {
  const client = activityClient(),
    store = owner(client);
  client.on("evener/thread/jobs/list", ({ cursor }) =>
    jobsFixture([jobFixture(cursor ? "older" : "newer")], cursor ? "old-rest" : "old-second"),
  );
  store.observe("jobs");
  await activityState(store, () => store.getSnapshot().jobs.hasMore && !store.getSnapshot().jobs.loading);
  await store.loadMore("jobs");
  const late = deferred<SessionJobsResponse>(),
    entered = deferred<void>();
  client.on("evener/thread/jobs/list", ({ cursor }) => {
    if (!cursor) return jobsFixture([jobFixture("newer")], "interrupted", "epoch-2");
    entered.resolve();
    return late.promise;
  });
  activityChanged(client, ["jobs"]);
  await vi.advanceTimersByTimeAsync(100);
  await entered.promise;
  client.emitStateChange("reconnecting");
  client.on("evener/thread/jobs/list", ({ cursor }) =>
    jobsFixture(
      [jobFixture(cursor ? "older" : "newer", "completed")],
      cursor ? "fresh-rest" : "fresh-second",
      "epoch-3",
    ),
  );
  client.emitReady();
  late.resolve(jobsFixture([jobFixture("wrong-walk")], undefined, "epoch-2"));
  await vi.advanceTimersByTimeAsync(100);
  await activityState(store, () => !store.getSnapshot().jobs.loading);
  expect(store.getSnapshot().jobs.rows.map((row) => [row.jobId, row.status])).toEqual([
    ["newer", "completed"],
    ["older", "completed"],
  ]);
  expect(store.getSnapshot().jobs.context?.epoch).toBe("epoch-3");
});

test("explicit paging retains unresolved root issues and rearms authoritative reconciliation", async () => {
  const client = activityClient(),
    store = owner(client);
  client.on("evener/thread/jobs/list", () => jobsFixture([jobFixture("deleted"), jobFixture("boundary")], "old-rest"));
  store.observe("jobs");
  await activityState(store, () => !store.getSnapshot().jobs.loading && store.getSnapshot().jobs.hasMore);
  const issue = { ref: "remote:child", code: "sourceUnavailable" };
  client.on("evener/thread/jobs/list", ({ cursor }) =>
    cursor
      ? jobsFixture([jobFixture("later")])
      : {
          ...jobsFixture([jobFixture("boundary")], "fresh-rest"),
          page: { complete: false, nextCursor: "fresh-rest", issues: [issue] },
        },
  );
  await store.refresh("jobs");
  expect(store.getSnapshot().jobs).toMatchObject({ complete: false, pending: true, issues: [issue] });
  await store.loadMore("jobs");
  expect(store.getSnapshot().jobs.rows.map((row) => row.jobId)).toEqual(["deleted", "boundary", "later"]);
  expect(store.getSnapshot().jobs).toMatchObject({ complete: false, pending: true, issues: [issue] });
  const before = callsTo(client, "evener/thread/jobs/list");
  client.on("evener/thread/jobs/list", () =>
    jobsFixture([jobFixture("boundary", "completed"), jobFixture("later", "completed")]),
  );
  await vi.advanceTimersByTimeAsync(999);
  expect(callsTo(client, "evener/thread/jobs/list")).toBe(before);
  await vi.advanceTimersByTimeAsync(1);
  expect(callsTo(client, "evener/thread/jobs/list")).toBe(before + 1);
  expect(store.getSnapshot().jobs.rows.map((row) => row.jobId)).toEqual(["boundary", "later"]);
  expect(store.getSnapshot().jobs).toMatchObject({ complete: true, pending: false, issues: [] });
});

test("fresh accumulator replaces duplicate rows in their original order without clearing unresolved issues early", async () => {
  const client = activityClient(),
    store = owner(client);
  client.on("evener/thread/jobs/list", () =>
    jobsFixture([jobFixture("deleted"), jobFixture("newer"), jobFixture("boundary")], "old-rest"),
  );
  store.observe("jobs");
  await activityState(store, () => !store.getSnapshot().jobs.loading && store.getSnapshot().jobs.hasMore);
  const issue = { ref: "remote:child", code: "sourceUnavailable" };
  client.on("evener/thread/jobs/list", () => ({
    ...jobsFixture([jobFixture("boundary")], "fresh-rest"),
    page: { complete: false, nextCursor: "fresh-rest", issues: [issue] },
  }));
  await store.refresh("jobs");
  const next = deferred<SessionJobsResponse>(),
    entered = deferred<void>();
  client.on("evener/thread/jobs/list", ({ cursor }) => {
    if (!cursor) return jobsFixture([jobFixture("newer")], "fresh-second");
    entered.resolve();
    return next.promise;
  });
  const refresh = store.refresh("jobs");
  await vi.advanceTimersByTimeAsync(100);
  await entered.promise;
  expect(store.getSnapshot().jobs.issues).toEqual([issue]);
  expect(store.getSnapshot().jobs.rows.map((row) => row.jobId)).toEqual(["deleted", "newer", "boundary"]);
  next.resolve(jobsFixture([jobFixture("newer", "completed"), jobFixture("boundary", "completed")], "fresh-rest"));
  await refresh;
  expect(store.getSnapshot().jobs.rows.map((row) => [row.jobId, row.status])).toEqual([
    ["newer", "completed"],
    ["boundary", "completed"],
  ]);
  expect(store.getSnapshot().jobs.issues).toEqual([]);
});

test.each([false, true])(
  "explicit later-page coverage survives root recovery (queued during refresh: %s)",
  async (queued) => {
    const client = activityClient(),
      store = owner(client);
    client.on("evener/thread/jobs/list", () =>
      jobsFixture([jobFixture("deleted"), jobFixture("boundary")], "old-rest"),
    );
    store.observe("jobs");
    await activityState(store, () => !store.getSnapshot().jobs.loading && store.getSnapshot().jobs.hasMore);
    const partial = deferred<SessionJobsResponse>(),
      entered = deferred<void>();
    client.on("evener/thread/jobs/list", ({ cursor }) => {
      if (cursor) return jobsFixture([jobFixture("later")]);
      entered.resolve();
      return partial.promise;
    });
    const refresh = store.refresh("jobs");
    await entered.promise;
    const more = queued ? store.loadMore("jobs") : null;
    partial.resolve({
      ...jobsFixture([jobFixture("boundary")], "fresh-rest"),
      page: { complete: false, nextCursor: "fresh-rest", issues: [{ ref: "remote:child", code: "sourceUnavailable" }] },
    });
    await refresh;
    if (more) await more;
    else await store.loadMore("jobs");
    expect(store.getSnapshot().jobs.rows.map((row) => row.jobId)).toEqual(["deleted", "boundary", "later"]);
    const published: string[][] = [];
    store.subscribe(() => published.push(store.getSnapshot().jobs.rows.map((row) => row.jobId)));
    client.on("evener/thread/jobs/list", ({ cursor }) =>
      jobsFixture([jobFixture(cursor ? "later" : "boundary", "completed")], cursor ? undefined : "clean-rest"),
    );
    const recovery = store.refresh("jobs");
    await vi.advanceTimersByTimeAsync(100);
    expect(store.getSnapshot().jobs.rows.map((row) => row.jobId)).toEqual(["boundary", "later"]);
    await recovery;
    expect(published.every((ids) => ids.includes("later"))).toBe(true);
    expect(store.getSnapshot().jobs).toMatchObject({ complete: true, pending: false, issues: [] });
    expect(
      client.calls
        .filter((call) => call.method === "evener/thread/jobs/list")
        .slice(-2)
        .map((call) => call.params),
    ).toEqual([
      { ref: activityRef, scope: "session" },
      { ref: activityRef, scope: "session", cursor: "clean-rest" },
    ]);
  },
);
