// @vitest-environment node
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { WireError } from "./errors";
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
import type { SessionJobsResponse } from "./types.gen";

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
