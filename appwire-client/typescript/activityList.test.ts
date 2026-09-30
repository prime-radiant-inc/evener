import { activityNodeID } from "./activityData";
// @vitest-environment node

import { afterEach, expect, test, vi } from "vitest";
import type { ActivityTree } from "./activityData";
import { ACTIVITY_REFRESH_MIN_INTERVAL_MS, type ActivityClient, ActivityList } from "./activityList";
import { WireError } from "./errors";

const tree = (continuation?: string, revision = 1) => ({
  revision,
  root: {
    kind: "session" as const,
    sessionId: "session",
    ref: "local:session",
    label: "session",
    aggregate: "ended",
    counts: { active: 0, failed: 0, completed: 1, complete: !continuation },
    entries: [
      {
        kind: "shell" as const,
        job: {
          jobId: "job",
          ownerSessionId: "session",
          ownerRef: "local:session",
          type: "shell",
          status: "completed",
          terminal: true,
          background: false,
          hasOutput: false,
          description: "job",
          startedAt: "2026-01-01T00:00:00Z",
          outputBytes: 0,
        },
      },
    ],
    branch: continuation ? { continuation, truncated: true } : {},
  },
});

test("delegate updated notification refreshes only the matching list", async () => {
  vi.useFakeTimers();
  let notify: ((n: { method: string; params: { ref: string; threadId: string } }) => void) | undefined;
  const requests: unknown[] = [];
  let release!: () => void;
  const client = {
    onNotification(callback: typeof notify) {
      notify = callback;
      return () => undefined;
    },
    request(_method: string, params: unknown) {
      requests.push(params);
      if (requests.length === 1) return new Promise((resolve) => (release = () => resolve({ data: tree("next") })));
      return Promise.resolve({ data: tree() });
    },
  } as unknown as ActivityClient;
  const list = new ActivityList(client, "local:session", "session");
  list.start();
  await Promise.resolve();
  const initialRequests = requests.length;
  notify?.({ method: "evener/delegate/updated", params: { ref: "local:session", threadId: "session" } });
  release();
  const refreshed = list.refresh();
  await vi.advanceTimersByTimeAsync(ACTIVITY_REFRESH_MIN_INTERVAL_MS);
  await refreshed;
  expect(requests).toHaveLength(initialRequests + 1);
  expect(list.getSnapshot().tree?.root.branch).toEqual({});
});

test("loadMore requested during refresh is queued and awaited", async () => {
  const first = new ActivityList(
    {
      onNotification: () => () => undefined,
      request: async () => ({ data: tree("next") }),
    } as unknown as ActivityClient,
    "local:session",
    "session",
  );
  await first.refresh();
  const branch = first.branches()[0];
  if (!branch) throw new Error("missing continuation");
  let release!: (value: { data: unknown }) => void;
  let calls = 0;
  const client = {
    onNotification: () => () => undefined,
    request: () => {
      calls++;
      if (calls === 1) return new Promise<{ data: unknown }>((resolve) => (release = resolve));
      return Promise.resolve({ data: tree() });
    },
  } as unknown as ActivityClient;
  const list = new ActivityList(client, "local:session", "session", first.getSnapshot().tree);
  const refresh = list.refresh();
  const more = list.loadMore(branch.id, "next");
  release({ data: tree("next") });
  await refresh;
  await more;
  expect(calls).toBe(2);
});

test("ignores invalidation notifications for another ref or session", async () => {
  const requests: unknown[] = [];
  let notify!: (n: { method: string; params: { ref: string; threadId: string } }) => void;
  const client = {
    request: async (_method: string, params: unknown) => {
      requests.push(params);
      return { data: tree() };
    },
    onNotification: (handler: typeof notify) => {
      notify = handler;
      return () => undefined;
    },
  } as unknown as ActivityClient;
  const list = new ActivityList(client, "local:session", "session");
  list.start();
  notify({ method: "evener/delegate/updated", params: { ref: "other", threadId: "session" } });
  notify({ method: "evener/delegate/updated", params: { ref: "local:session", threadId: "other" } });
  await Promise.resolve();
  expect(requests).toHaveLength(1);
});

test.each([
  ["unsupported", new WireError("unsupported", -32000, { evenerErrorInfo: "actionUnavailable" })],
  ["ended", new WireError("thread not found: session", -32000, { evenerErrorInfo: "sessionUnavailable" })],
])("maps %s activity rejection", async (kind, error) => {
  const client = {
    request: async () => {
      throw error;
    },
    onNotification: () => () => undefined,
  } as unknown as ActivityClient;
  const list = new ActivityList(client, "local:session", "session");
  await list.refresh();
  expect(list.getSnapshot()[kind === "unsupported" ? "unsupported" : "ended"]).toBe(true);
});

test("rejects wrong identity and stale revision responses without replacing the tree", async () => {
  let response: unknown = { data: tree() };
  const client = {
    request: async () => response,
    onNotification: () => () => undefined,
  } as unknown as ActivityClient;
  const list = new ActivityList(client, "local:session", "session");
  await list.refresh();
  response = { data: { ...tree(undefined, 2), root: { ...tree(undefined, 2).root, ref: "local:other" } } };
  await list.refresh();
  expect(list.getSnapshot().error).toContain("another session");
  response = { data: tree(undefined, 0) };
  await list.refresh();
  expect(list.getSnapshot().tree?.revision).toBe(1);
  expect(list.getSnapshot().error).toContain("older");
});

test("a continuation page from a different revision is discarded for a fresh root", async () => {
  const requests: unknown[] = [];
  const client = {
    onNotification: () => () => undefined,
    request: async (_method: string, params: unknown) => {
      requests.push(params);
      // The continuation answers at a newer revision than the retained tree.
      if (requests.length === 1) return { data: tree("next", 2) };
      return { data: tree() };
    },
  } as unknown as ActivityClient;
  const list = new ActivityList(client, "local:session", "session", tree("next"));
  const branch = list.branches()[0];
  if (!branch) throw new Error("missing continuation");
  await list.loadMore(branch.id, "next");
  // The mismatched page is not grafted; the list re-fetches the root instead.
  expect(requests).toEqual([{ ref: "local:session", continuation: "next" }, { ref: "local:session" }]);
  expect(list.getSnapshot().tree?.revision).toBe(1);
  expect(list.getSnapshot().error).toBeNull();
});

test("maps a continuation branch error while retaining its continuation", async () => {
  let response: unknown = { data: tree("cursor") };
  const client = {
    request: async () => response,
    onNotification: () => () => undefined,
  } as unknown as ActivityClient;
  const list = new ActivityList(client, "local:session", "session");
  await list.refresh();
  const branch = list.branches()[0];
  if (!branch) throw new Error("missing continuation");
  response = Promise.reject(
    new WireError("page unavailable", -32000, { evenerErrorInfo: "transcriptItemCursorStale" }),
  );
  await list.loadMore(branch.id, "cursor");
  expect(list.getSnapshot().error).toContain("Could not load activity");
  expect(list.branches()[0]?.continuation).toBe("cursor");
});

type Deferred<T> = {
  promise: Promise<T>;
  resolve: (value: T) => void;
};

function deferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((complete) => {
    resolve = complete;
  });
  return { promise, resolve };
}

function delegateEntry(delegateId: string, continuation?: string, projectionRevision = 1) {
  return {
    kind: "delegate" as const,
    delegate: {
      delegateId,
      childSessionId: `${delegateId}-child`,
      childRef: `local:${delegateId}-child`,
      description: delegateId,
      projectionRevision,
      status: projectionRevision > 1 ? "completed" : "running",
      terminal: projectionRevision > 1,
      branch: continuation ? { continuation, truncated: true } : {},
    },
  };
}

function activityTree(entries: ReturnType<typeof delegateEntry>[], revision = 1): ActivityTree {
  return {
    revision,
    root: {
      kind: "session",
      sessionId: "session",
      ref: "local:session",
      label: "session",
      aggregate: "working",
      counts: { active: 1, failed: 0, completed: 0, complete: false },
      entries,
      branch: {},
    },
  };
}

function boundaryClient(autoResponses: Array<ActivityTree | undefined> = []) {
  const requests: Array<{ params: Record<string, string>; response: Deferred<{ data: ActivityTree }> }> = [];
  const started = new Map<number, Deferred<void>>();
  let notification: Parameters<ActivityClient["onNotification"]>[0] | undefined;
  const client: ActivityClient = {
    onNotification(callback) {
      notification = callback;
      return () => undefined;
    },
    request(_method, params) {
      const index = requests.length;
      const response = deferred<{ data: ActivityTree }>();
      requests.push({ params: params as Record<string, string>, response });
      started.get(index)?.resolve();
      const autoResponse = autoResponses[index];
      if (autoResponse) response.resolve({ data: autoResponse });
      return response.promise;
    },
  };
  return {
    client,
    requests,
    notify: () =>
      notification?.({
        method: "evener/delegate/updated",
        params: {
          ref: "local:session",
          threadId: "session",
          delegate: {
            runGeneration: 1,
            delegateId: "delegate",
            ownerSessionId: "session",
            rootSessionId: "session",
            childSessionId: "delegate-child",
            transcriptRef: "local:delegate-child",
            type: "delegate",
            lifecycle: "terminal",
            phase: "completed",
            status: "completed",
            terminal: true,
            resumable: false,
            needsAttention: false,
            projectionRevision: 2,
          },
        },
      }),
    resolve(index: number, data: ActivityTree) {
      const request = requests[index];
      if (!request) throw new Error(`No activity request ${index} has started`);
      request.response.resolve({ data });
    },
    resolveFirst(data: ActivityTree) {
      this.resolve(0, data);
    },
    waitForRequest(index: number) {
      const existing = requests[index];
      if (existing) return Promise.resolve();
      const wait = deferred<void>();
      started.set(index, wait);
      return wait.promise;
    },
  };
}

test("refresh notification runs before a queued pagination request and retains the fresh delegate state", async () => {
  vi.useFakeTimers();
  const current = activityTree([delegateEntry("delegate", "old-page", 1)]);
  const fresh = activityTree([delegateEntry("delegate", "new-page", 2)], 2);
  const page = activityTree([delegateEntry("delegate", undefined, 2)], 2);
  const boundary = boundaryClient([undefined, fresh, page]);
  const list = new ActivityList(boundary.client, "local:session", "session", current);
  list.start();
  await boundary.waitForRequest(0);

  boundary.notify();
  const more = list.loadMore(activityNodeID(delegateEntry("delegate")), "old-page");
  boundary.resolveFirst(current);
  await vi.advanceTimersByTimeAsync(ACTIVITY_REFRESH_MIN_INTERVAL_MS);
  await more;
  expect(boundary.requests.map(({ params }) => params)).toEqual([
    { ref: "local:session" },
    { ref: "local:session" },
    { ref: "local:session", continuation: "new-page" },
  ]);
  const entry = list.getSnapshot().tree?.root.entries[0];
  if (entry?.kind !== "delegate") throw new Error("missing delegate");
  expect(entry.delegate.projectionRevision).toBe(2);
});

test("retries a page after notification invalidates its in-flight continuation", async () => {
  vi.useFakeTimers();
  const current = activityTree([delegateEntry("delegate", "old-page")]);
  const fresh = activityTree([delegateEntry("delegate", "new-page", 2)], 2);
  const page = activityTree([delegateEntry("delegate", undefined, 2)], 2);
  const boundary = boundaryClient([undefined, current, undefined, fresh, page]);
  const list = new ActivityList(boundary.client, "local:session", "session", current);
  list.start();
  const initial = list.refresh();
  await boundary.waitForRequest(0);
  boundary.resolve(0, current);
  await vi.advanceTimersByTimeAsync(ACTIVITY_REFRESH_MIN_INTERVAL_MS);
  await initial;

  const more = list.loadMore(activityNodeID(delegateEntry("delegate")), "old-page");
  await boundary.waitForRequest(2);
  boundary.notify();
  boundary.resolve(2, page);
  await vi.advanceTimersByTimeAsync(ACTIVITY_REFRESH_MIN_INTERVAL_MS);
  await more;

  expect(boundary.requests.map(({ params }) => params)).toEqual([
    { ref: "local:session" },
    { ref: "local:session" },
    { ref: "local:session", continuation: "old-page" },
    { ref: "local:session" },
    { ref: "local:session", continuation: "new-page" },
  ]);
  expect(list.getSnapshot().tree?.revision).toBe(2);
});

test("queues separate valid pagination requests instead of overwriting the first branch", async () => {
  const current = activityTree([delegateEntry("first", "first-page"), delegateEntry("second", "second-page")]);
  const boundary = boundaryClient([undefined, current, current]);
  const list = new ActivityList(boundary.client, "local:session", "session", current);
  const refresh = list.refresh();
  await boundary.waitForRequest(0);

  const first = list.loadMore(activityNodeID(delegateEntry("first")), "first-page");
  const second = list.loadMore(activityNodeID(delegateEntry("second")), "second-page");
  boundary.resolveFirst(current);
  await Promise.all([refresh, first, second]);

  expect(boundary.requests.map(({ params }) => params)).toEqual([
    { ref: "local:session" },
    { ref: "local:session", continuation: "first-page" },
    { ref: "local:session", continuation: "second-page" },
  ]);
});

test("does not issue a queued continuation after refresh removes that branch", async () => {
  const current = activityTree([delegateEntry("delegate", "stale-page")]);
  const fresh = activityTree([], 2);
  const boundary = boundaryClient([undefined, fresh]);
  const list = new ActivityList(boundary.client, "local:session", "session", current);
  const refresh = list.refresh();
  await boundary.waitForRequest(0);

  const more = list.loadMore(activityNodeID(delegateEntry("delegate")), "stale-page");
  boundary.resolveFirst(fresh);
  await Promise.all([refresh, more]);

  expect(boundary.requests.map(({ params }) => params)).toEqual([{ ref: "local:session" }]);
});

test.each([
  ["unsupported", new WireError("unsupported", -32000, { evenerErrorInfo: "actionUnavailable" })],
  ["ended", new WireError("thread not found: session", -32000, { evenerErrorInfo: "sessionUnavailable" })],
  ["transient", new Error("activity server unavailable")],
])("failed refresh does not drain queued pagination (%s)", async (kind, failure) => {
  const current = activityTree([delegateEntry("delegate", "old-page")]);
  let requests = 0;
  let rejectRefresh!: (reason?: unknown) => void;
  const client: ActivityClient = {
    onNotification: () => () => undefined,
    request: () => {
      requests++;
      if (requests === 1)
        return new Promise<{ data: ActivityTree }>((_resolve, reject) => {
          rejectRefresh = reject;
        });
      return Promise.resolve({ data: current });
    },
  };
  const list = new ActivityList(client, "local:session", "session", current);
  const refresh = list.refresh();
  const more = list.loadMore(activityNodeID(delegateEntry("delegate")), "old-page");
  rejectRefresh(failure);
  await refresh;
  await more;

  expect(requests).toBe(1);
  expect(list.getSnapshot().tree).toBe(current);
  expect(list.getSnapshot().error).toBe(
    kind === "transient" ? "Could not load activity: activity server unavailable" : null,
  );
  expect(list.getSnapshot().unsupported).toBe(kind === "unsupported");
  expect(list.getSnapshot().ended).toBe(kind === "ended");

  await list.loadMore(activityNodeID(delegateEntry("delegate")), "old-page");
  expect(requests).toBe(kind === "transient" ? 2 : 1);
});

test("a queued page that succeeds clears the error left by an earlier failed page", async () => {
  const current = activityTree([delegateEntry("first", "page-1", 1), delegateEntry("second", "page-2", 1)]);
  const page = activityTree([delegateEntry("second", undefined, 2)]);
  const boundary = boundaryClient();
  const list = new ActivityList(boundary.client, "local:session", "session", current);
  list.start();
  await boundary.waitForRequest(0);

  // Both pages are requested while the opening refresh is in flight, so they
  // run back to back inside one load: the first fails, the second succeeds.
  const first = list.loadMore(activityNodeID(delegateEntry("first")), "page-1");
  const second = list.loadMore(activityNodeID(delegateEntry("second")), "page-2");
  boundary.resolveFirst(current);
  await boundary.waitForRequest(1);
  boundary.resolve(1, {} as unknown as ActivityTree);
  await boundary.waitForRequest(2);
  boundary.resolve(2, page);
  await Promise.all([first, second]);

  expect(list.getSnapshot().error).toBeNull();
});

test("a page requested from the completion notification is loaded", async () => {
  const current = activityTree([delegateEntry("delegate", "next")]);
  const page = activityTree([delegateEntry("delegate", undefined, 2)]);
  const boundary = boundaryClient([current, page]);
  const list = new ActivityList(boundary.client, "local:session", "session");
  let requested: Promise<void> | undefined;
  let reacted = false;
  // A surface that pages from a subscription asks for the next page when the
  // load reports it is done, rather than driving its own reloads.
  list.subscribe(() => {
    const branch = list.branches()[0];
    if (reacted || list.getSnapshot().loading || !branch?.continuation) return;
    reacted = true;
    requested = list.loadMore(branch.id, branch.continuation);
  });

  const refresh = list.refresh();
  await refresh;
  await requested;

  expect(boundary.requests.map(({ params }) => params)).toEqual([
    { ref: "local:session" },
    { ref: "local:session", continuation: "next" },
  ]);
  const entry = list.getSnapshot().tree?.root.entries[0];
  if (entry?.kind !== "delegate") throw new Error("missing delegate");
  expect(entry.delegate.projectionRevision).toBe(2);
  expect(list.branches()).toEqual([]);
});

// Notification-driven refresh behavior. A session with hundreds of delegates
// makes every evener/jobs/list response slow and large, while
// evener/delegate/updated arrives about once a second; these tests pin that the
// notifications no longer keep the list refetching back to back.

afterEach(() => {
  vi.useRealTimers();
});

type Notification = Parameters<Parameters<ActivityClient["onNotification"]>[0]>[0];

function delegateInfo(overrides: Record<string, unknown> = {}) {
  return {
    runGeneration: 1,
    delegateId: "delegate",
    ownerSessionId: "session",
    rootSessionId: "session",
    childSessionId: "delegate-child",
    transcriptRef: "local:delegate-child",
    type: "delegate",
    lifecycle: "running",
    phase: "running",
    status: "running",
    terminal: false,
    resumable: false,
    needsAttention: false,
    projectionRevision: 1,
    ...overrides,
  };
}

// A client whose jobs/list answers after latencyMs of fake time. Each request
// records when it started and when it answered.
function timedClient(latencyMs: number, respond: (call: number, params: Record<string, string>) => ActivityTree) {
  const calls: Array<{ startedAt: number; answeredAt?: number; params: Record<string, string> }> = [];
  let handler: ((n: Notification) => void) | undefined;
  const client: ActivityClient = {
    onNotification(callback) {
      handler = callback as (n: Notification) => void;
      return () => undefined;
    },
    request(_method, params) {
      const call: { startedAt: number; answeredAt?: number; params: Record<string, string> } = {
        startedAt: Date.now(),
        params: params as Record<string, string>,
      };
      calls.push(call);
      const data = respond(calls.length, call.params);
      return new Promise((resolve) =>
        setTimeout(() => {
          call.answeredAt = Date.now();
          resolve({ data });
        }, latencyMs),
      ) as Promise<{ data: ActivityTree }>;
    },
  };
  const params = { ref: "local:session", threadId: "session" };
  return {
    client,
    calls,
    delegateUpdated: (overrides: Record<string, unknown> = {}) =>
      handler?.({
        method: "evener/delegate/updated",
        params: { ...params, delegate: delegateInfo(overrides) },
      } as unknown as Notification),
    treeUpdated: (revision: number) =>
      handler?.({ method: "evener/jobs/treeUpdated", params: { ...params, revision } } as Notification),
  };
}

function heldDelegate(list: ActivityList) {
  const entry = list.getSnapshot().tree?.root.entries[0];
  if (entry?.kind !== "delegate") throw new Error("missing delegate");
  return entry.delegate;
}

async function startedList(t: ReturnType<typeof timedClient>, held: ActivityTree) {
  const list = new ActivityList(t.client, "local:session", "session", held);
  list.start();
  await vi.advanceTimersByTimeAsync(0);
  return list;
}

test("a burst of updates for a delegate already in the tree applies in place without refetching", async () => {
  vi.useFakeTimers();
  const held = activityTree([delegateEntry("delegate")], 5);
  const t = timedClient(0, () => held);
  const list = await startedList(t, held);
  await vi.advanceTimersByTimeAsync(ACTIVITY_REFRESH_MIN_INTERVAL_MS);
  const baseline = t.calls.length;

  for (let revision = 2; revision <= 51; revision++) {
    t.delegateUpdated({
      phase: `step-${revision}`,
      projectionRevision: revision,
      latestActivityAt: `2026-01-01T00:00:${String(revision).padStart(2, "0")}Z`,
    });
  }
  await vi.advanceTimersByTimeAsync(10 * ACTIVITY_REFRESH_MIN_INTERVAL_MS);

  expect(t.calls).toHaveLength(baseline);
  expect(heldDelegate(list).phase).toBe("step-51");
  expect(heldDelegate(list).projectionRevision).toBe(51);
  expect(heldDelegate(list).latestActivityAt).toBe("2026-01-01T00:00:51Z");
  expect(list.getSnapshot().tree?.revision).toBe(5);
});

test("an update older than the held delegate only moves its latest activity forward", async () => {
  vi.useFakeTimers();
  const held = activityTree([delegateEntry("delegate", undefined, 3)], 5);
  const heldEntry = held.root.entries[0] as ReturnType<typeof delegateEntry>;
  heldEntry.delegate.status = "running";
  heldEntry.delegate.terminal = false;
  const t = timedClient(0, () => held);
  const list = await startedList(t, held);
  await vi.advanceTimersByTimeAsync(ACTIVITY_REFRESH_MIN_INTERVAL_MS);
  const baseline = t.calls.length;

  t.delegateUpdated({
    phase: "stale",
    projectionRevision: 2,
    terminal: true,
    latestActivityAt: "2026-01-01T00:00:09Z",
  });
  await vi.advanceTimersByTimeAsync(10 * ACTIVITY_REFRESH_MIN_INTERVAL_MS);

  expect(t.calls).toHaveLength(baseline);
  expect(heldDelegate(list).phase).toBeUndefined();
  expect(heldDelegate(list).projectionRevision).toBe(3);
  expect(heldDelegate(list).latestActivityAt).toBe("2026-01-01T00:00:09Z");
});

test("an update that changes what the tree counts or shows of a finished delegate refreshes instead", async () => {
  vi.useFakeTimers();
  const held = activityTree([delegateEntry("delegate")], 5);
  const t = timedClient(0, () => held);
  await startedList(t, held);
  await vi.advanceTimersByTimeAsync(ACTIVITY_REFRESH_MIN_INTERVAL_MS);
  const baseline = t.calls.length;

  t.delegateUpdated({
    terminal: true,
    outcome: "completed",
    status: "completed",
    projectionRevision: 2,
    packetKind: "final",
  });
  await vi.advanceTimersByTimeAsync(ACTIVITY_REFRESH_MIN_INTERVAL_MS);

  expect(t.calls).toHaveLength(baseline + 1);
});

test("an update for a delegate the tree does not hold refreshes once, after the minimum interval", async () => {
  vi.useFakeTimers();
  const held = activityTree([delegateEntry("delegate")], 5);
  const t = timedClient(0, () => held);
  await startedList(t, held);
  const baseline = t.calls.length;

  for (let i = 0; i < 5; i++) t.delegateUpdated({ delegateId: "brand-new", childSessionId: "brand-new-child" });
  await vi.advanceTimersByTimeAsync(ACTIVITY_REFRESH_MIN_INTERVAL_MS - 1);
  expect(t.calls).toHaveLength(baseline);
  await vi.advanceTimersByTimeAsync(1);
  expect(t.calls).toHaveLength(baseline + 1);
  await vi.advanceTimersByTimeAsync(10 * ACTIVITY_REFRESH_MIN_INTERVAL_MS);
  expect(t.calls).toHaveLength(baseline + 1);
});

test("an update before any tree is held refreshes", async () => {
  vi.useFakeTimers();
  const t = timedClient(1000, () => activityTree([delegateEntry("delegate")], 5));
  const list = new ActivityList(t.client, "local:session", "session");
  list.start();
  await vi.advanceTimersByTimeAsync(0);
  t.delegateUpdated();
  await vi.advanceTimersByTimeAsync(1000 + ACTIVITY_REFRESH_MIN_INTERVAL_MS);
  expect(t.calls).toHaveLength(2);
});

test("tree revisions at or below the held one do not refetch; a newer one does", async () => {
  vi.useFakeTimers();
  const held = activityTree([delegateEntry("delegate")], 5);
  const t = timedClient(0, () => held);
  await startedList(t, held);
  await vi.advanceTimersByTimeAsync(ACTIVITY_REFRESH_MIN_INTERVAL_MS);
  const baseline = t.calls.length;

  t.treeUpdated(4);
  t.treeUpdated(5);
  await vi.advanceTimersByTimeAsync(10 * ACTIVITY_REFRESH_MIN_INTERVAL_MS);
  expect(t.calls).toHaveLength(baseline);

  t.treeUpdated(6);
  await vi.advanceTimersByTimeAsync(ACTIVITY_REFRESH_MIN_INTERVAL_MS);
  expect(t.calls).toHaveLength(baseline + 1);
});

test("continuous notifications during slow loads do not refetch back to back", async () => {
  vi.useFakeTimers();
  const latency = 3000;
  const elapsed = 60_000;
  const t = timedClient(latency, (call) => activityTree([delegateEntry("delegate")], call));
  const list = new ActivityList(t.client, "local:session", "session");
  list.start();
  let sawFreshTree = false;
  for (let at = 0; at < elapsed; at += 500) {
    t.delegateUpdated({ delegateId: "brand-new", childSessionId: "brand-new-child" });
    await vi.advanceTimersByTimeAsync(500);
    if ((list.getSnapshot().tree?.revision ?? 0) >= 2) sawFreshTree = true;
  }

  expect(t.calls.length).toBeLessThanOrEqual(Math.ceil(elapsed / ACTIVITY_REFRESH_MIN_INTERVAL_MS) + 1);
  for (let i = 1; i < t.calls.length; i++) {
    const previousAnswer = t.calls[i - 1]?.answeredAt ?? Number.POSITIVE_INFINITY;
    expect((t.calls[i]?.startedAt ?? 0) - previousAnswer).toBeGreaterThanOrEqual(ACTIVITY_REFRESH_MIN_INTERVAL_MS);
  }
  // Loads that finish while notifications keep arriving still reach the screen.
  expect(sawFreshTree).toBe(true);
});

test("a root answer older than an in-place update does not undo it", async () => {
  vi.useFakeTimers();
  const held = activityTree([delegateEntry("delegate")], 5);
  const t = timedClient(1000, () => held);
  const list = new ActivityList(t.client, "local:session", "session", held);
  list.start();
  // The opening fetch is in flight when the update arrives.
  await vi.advanceTimersByTimeAsync(500);
  t.delegateUpdated({ phase: "newer", projectionRevision: 2, latestActivityAt: "2026-01-01T00:00:09Z" });
  await vi.advanceTimersByTimeAsync(600);

  expect(heldDelegate(list).phase).toBe("newer");
  expect(heldDelegate(list).projectionRevision).toBe(2);
});

test("a trailing refresh does not start inside the interval of a fetch made while it waited", async () => {
  vi.useFakeTimers();
  const held = activityTree([delegateEntry("delegate")], 5);
  const t = timedClient(0, () => held);
  const list = await startedList(t, held);
  const baseline = t.calls.length;

  t.delegateUpdated({ delegateId: "brand-new", childSessionId: "brand-new-child" });
  await vi.advanceTimersByTimeAsync(1500);
  void list.refresh();
  await vi.advanceTimersByTimeAsync(0);
  expect(t.calls).toHaveLength(baseline + 1);
  await vi.advanceTimersByTimeAsync(600);
  expect(t.calls).toHaveLength(baseline + 1);
  await vi.advanceTimersByTimeAsync(ACTIVITY_REFRESH_MIN_INTERVAL_MS);
  expect(t.calls).toHaveLength(baseline + 2);
});

test("a queued page is not starved by notifications that keep invalidating the root", async () => {
  vi.useFakeTimers();
  const held = activityTree([delegateEntry("delegate", "next")], 1);
  // Once the page has been served the daemon's roots no longer truncate.
  let served = false;
  const t = timedClient(1000, (_call, params) => {
    if (params.continuation) served = true;
    return activityTree([delegateEntry("delegate", served ? undefined : "next")], 1);
  });
  const list = new ActivityList(t.client, "local:session", "session", held);
  list.start();
  await vi.advanceTimersByTimeAsync(100);
  const branch = list.branches()[0];
  if (!branch?.continuation) throw new Error("missing continuation");
  void list.loadMore(branch.id, branch.continuation);
  for (let at = 100; at < 20_000; at += 500) {
    t.delegateUpdated({ delegateId: "brand-new", childSessionId: "brand-new-child" });
    await vi.advanceTimersByTimeAsync(500);
  }

  const pageAt = t.calls.findIndex((call) => call.params.continuation);
  expect(pageAt).toBeGreaterThan(-1);
  // At most the load already running and one root fetched after the click come first.
  expect(pageAt).toBeLessThanOrEqual(2);
  expect(list.branches()).toEqual([]);
});
