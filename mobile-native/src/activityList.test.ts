import { expect, it } from "vitest";
import { parseActivityTree } from "../../appwire-client/typescript/activityData";
import { ActivityList } from "../../appwire-client/typescript/activityList";
import type { AnyNotification } from "../../appwire-client/typescript/types.gen";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";

function tree(revision = 1, ids = ["a"], continuation?: string) {
  return {
    revision,
    root: {
      kind: "session",
      sessionId: "thread",
      ref: "local:test",
      label: "Test",
      aggregate: "running",
      counts: { active: 1, completed: 0, failed: 0, complete: !continuation },
      branch: continuation ? { truncated: true, continuation } : {},
      entries: ids.map((jobId) => ({
        kind: "shell",
        job: {
          jobId,
          ownerSessionId: "thread",
          ownerRef: "local:test",
          type: "shell",
          status: "running",
          terminal: false,
          background: true,
          hasOutput: true,
          description: jobId,
          startedAt: "2026-09-05T12:00:00Z",
          outputBytes: 1,
        },
      })),
    },
  };
}
function boundary() {
  const requests: unknown[] = [];
  const handlers = new Set<(n: AnyNotification) => void>();
  const io = {
    read: async (): Promise<{ data: unknown }> => ({ data: tree() }),
  };
  const client = {
    request: async (method, params) => {
      requests.push({ method, params });
      return io.read();
    },
    onNotification: (handler) => {
      handlers.add(handler);
      return () => {
        handlers.delete(handler);
      };
    },
  } as ConversationClientLike;
  const list = new ActivityList(client, "local:test", "thread");
  const notify = (ref = "local:test", threadId = "thread") => {
    for (const handler of handlers)
      handler({
        method: "evener/jobs/treeUpdated",
        params: { ref, threadId, revision: 2 },
      });
  };
  return { list, io, requests, handlers, notify };
}

it("refuses a retained tree owned by another session before publishing it", () => {
  const { io } = boundary();
  const client = {
    request: io.read,
    onNotification: () => () => {},
  } as ConversationClientLike;
  const retained = parseActivityTree(tree());
  expect(retained).not.toBeNull();
  expect(
    () => new ActivityList(client, "other-ref", "thread", retained),
  ).toThrow();
  expect(
    () => new ActivityList(client, "local:test", "other-thread", retained),
  ).toThrow();
  const list = new ActivityList(client, "local:test", "thread", retained);
  expect(list.getSnapshot().tree).toBe(retained);
  list.dispose();
});

it("refuses an empty activity owner without sending a request", () => {
  const { io } = boundary();
  const client = {
    request: io.read,
    onNotification: () => () => {},
  } as ConversationClientLike;
  expect(() => new ActivityList(client, "", "thread")).toThrow();
  expect(() => new ActivityList(client, "local:test", " ")).toThrow();
});

it("retains activity through failed refresh and rejects a regressing root revision", async () => {
  const { list, io, requests } = boundary();
  await list.refresh();
  expect(requests).toEqual([
    { method: "evener/jobs/list", params: { ref: "local:test" } },
  ]);
  io.read = async () => {
    throw new Error("offline");
  };
  await list.refresh();
  expect(list.getSnapshot().tree?.root.entries).toHaveLength(1);
  expect(list.getSnapshot().error).toBeTruthy();
  io.read = async () => ({ data: tree(0, []) });
  await list.refresh();
  expect(list.getSnapshot().tree?.revision).toBe(1);
  io.read = async () => ({ data: tree(2, []) });
  await list.refresh();
  expect(list.getSnapshot()).toMatchObject({
    tree: { revision: 2, root: { entries: [] } },
    error: null,
    loading: false,
  });
});

it("loads only a currently advertised continuation and summarizes the accumulated jobs", async () => {
  const { list, io, requests } = boundary();
  io.read = async () => ({ data: tree(1, ["a"], "cursor") });
  await list.refresh();
  const branch = list.branches()[0];
  expect(branch).toMatchObject({ continuation: "cursor" });
  if (!branch) throw new Error("missing branch");
  io.read = async () => ({ data: tree(1, ["b"]) });
  await list.loadMore(branch.id, "stale-cursor");
  expect(requests).toHaveLength(1);
  await list.loadMore(branch.id, "cursor");
  expect(requests[1]).toEqual({
    method: "evener/jobs/list",
    params: { ref: "local:test", continuation: "cursor" },
  });
  expect(list.getSnapshot().tree?.root.entries).toHaveLength(2);
  expect(
    list
      .getSnapshot()
      .tree?.root.entries.map((entry) =>
        entry.kind === "shell" ? entry.job.jobId : entry.delegate.delegateId,
      ),
  ).toEqual(["a", "b"]);
  expect(list.getSnapshot().tree?.root.counts).toEqual({
    active: 2,
    completed: 0,
    failed: 0,
    complete: true,
  });
  expect(list.branches()).toHaveLength(0);
});

it("coalesces matching invalidations and discards disposed responses", async () => {
  const { list, io, requests, handlers, notify } = boundary();
  let complete!: (value: { data: unknown }) => void;
  io.read = () =>
    new Promise((resolve) => {
      complete = resolve;
    });
  list.start();
  notify("other");
  notify("local:test", "other");
  expect(requests).toHaveLength(1);
  notify();
  notify();
  io.read = async () => ({ data: tree(2, ["b"]) });
  complete({ data: tree() });
  await list.refresh();
  expect(requests).toHaveLength(2);
  expect(list.getSnapshot().tree?.revision).toBe(2);
  io.read = () =>
    new Promise((resolve) => {
      complete = resolve;
    });
  const pending = list.refresh();
  list.dispose();
  const prior = list.getSnapshot();
  complete({ data: tree(3) });
  await pending;
  expect(list.getSnapshot()).toBe(prior);
  expect(handlers.size).toBe(0);
});

it("keeps a failed continuation retryable and supersedes it with live root updates", async () => {
  const { list, io, notify } = boundary();
  io.read = async () => ({ data: tree(1, ["a"], "cursor") });
  list.start();
  await list.refresh();
  const branch = list.branches()[0];
  if (!branch) throw new Error("missing branch");
  io.read = async () => {
    throw new Error("offline");
  };
  await list.loadMore(branch.id, "cursor");
  expect(list.getSnapshot().error).toBeTruthy();
  expect(list.branches()[0]?.continuation).toBe("cursor");
  let complete!: (value: { data: unknown }) => void;
  io.read = () =>
    new Promise((resolve) => {
      complete = resolve;
    });
  const pending = list.loadMore(branch.id, "cursor");
  notify();
  io.read = async () => ({ data: tree(3, ["c"]) });
  complete({ data: tree(2, ["a", "b"]) });
  await pending;
  expect(list.getSnapshot().tree?.revision).toBe(3);
  expect(list.getSnapshot().tree?.root.entries).toHaveLength(1);
});
