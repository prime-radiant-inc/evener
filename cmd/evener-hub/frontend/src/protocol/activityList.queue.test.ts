import { expect, test } from "vitest";
import type { ActivityTree } from "./activityData";
import { type ActivityClient, ActivityList } from "./activityList";
import { WireError } from "./errors";

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
  const current = activityTree([delegateEntry("delegate", "old-page", 1)]);
  const fresh = activityTree([delegateEntry("delegate", "new-page", 2)], 2);
  const page = activityTree([delegateEntry("delegate", undefined, 2)], 2);
  const boundary = boundaryClient([undefined, fresh, page]);
  const list = new ActivityList(boundary.client, "local:session", "session", current);
  list.start();
  await boundary.waitForRequest(0);

  boundary.notify();
  const more = list.loadMore("delegate:delegate", "old-page");
  boundary.resolveFirst(current);
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
  const current = activityTree([delegateEntry("delegate", "old-page")]);
  const fresh = activityTree([delegateEntry("delegate", "new-page", 2)], 2);
  const page = activityTree([delegateEntry("delegate", undefined, 2)], 2);
  const boundary = boundaryClient([undefined, current, undefined, fresh, page]);
  const list = new ActivityList(boundary.client, "local:session", "session", current);
  list.start();
  const initial = list.refresh();
  await boundary.waitForRequest(0);
  boundary.resolve(0, current);
  await initial;

  const more = list.loadMore("delegate:delegate", "old-page");
  await boundary.waitForRequest(2);
  boundary.notify();
  boundary.resolve(2, page);
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

  const first = list.loadMore("delegate:first", "first-page");
  const second = list.loadMore("delegate:second", "second-page");
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

  const more = list.loadMore("delegate:delegate", "stale-page");
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
  const more = list.loadMore("delegate:delegate", "old-page");
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

  await list.loadMore("delegate:delegate", "old-page");
  expect(requests).toBe(kind === "transient" ? 2 : 1);
});
