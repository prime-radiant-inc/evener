import { expect, test } from "vitest";
import { type ActivityClient, ActivityList } from "./activityList";
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
  await list.refresh();
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
