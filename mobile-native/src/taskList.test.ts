import { expect, it } from "vitest";
import { WireError } from "../../appwire-client/typescript/errors";
import type { AnyNotification } from "../../appwire-client/typescript/types.gen";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { TaskList } from "./taskList";

const row = (id: number, status = "open") => ({
  id,
  status,
  type: "task",
  description: `task-${id}`,
  prompt: "prompt",
  notes: ["update"],
});
function boundary(hasAggregate = true) {
  const requests: unknown[] = [];
  const handlers = new Set<(n: AnyNotification) => void>();
  const io = {
    read: async (): Promise<{ data: unknown }> => ({ data: [row(1)] }),
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
  const list = new TaskList(client, "local:test", "thread", () => hasAggregate);
  const notify = (ref = "local:test", threadId = "thread") => {
    for (const handler of handlers)
      handler({
        method: "evener/task/updated",
        params: { ref, threadId, total: 1, done: 0 },
      });
  };
  return { list, requests, io, notify, handlers, client };
}

it("loads current tasks and retains the list across failure until an explicit retry succeeds", async () => {
  const { list, io, requests } = boundary();
  await list.refresh();
  expect(requests).toEqual([
    { method: "evener/tasks/list", params: { ref: "local:test" } },
  ]);
  expect(list.getSnapshot().rows?.[0]).toMatchObject({
    id: 1,
    notes: ["update"],
  });
  io.read = async () => {
    throw new Error("Connection lost");
  };
  await list.refresh();
  expect(list.getSnapshot().rows).toHaveLength(1);
  expect(list.getSnapshot().error).toContain("Connection lost");
  io.read = async () => ({ data: [] });
  await list.refresh();
  expect(list.getSnapshot()).toMatchObject({
    rows: [],
    error: null,
    loading: false,
  });
  list.dispose();
});

it("coalesces current-session invalidations and rejects disposed results", async () => {
  const { list, io, requests, notify, handlers } = boundary();
  let complete!: (value: { data: unknown }) => void;
  io.read = () =>
    new Promise((resolve) => {
      complete = resolve;
    });
  list.start();
  expect(requests).toHaveLength(1);
  notify("other:test");
  notify("local:test", "other-thread");
  notify();
  notify();
  io.read = async () => ({ data: [row(2, "in_progress")] });
  complete({ data: [row(1)] });
  await list.refresh();
  expect(requests).toHaveLength(2);
  expect(list.getSnapshot().rows?.map((item) => item.id)).toEqual([2]);
  io.read = () =>
    new Promise((resolve) => {
      complete = resolve;
    });
  const pending = list.refresh();
  list.dispose();
  const before = list.getSnapshot();
  complete({ data: [row(3)] });
  await pending;
  expect(list.getSnapshot()).toBe(before);
  expect(handlers.size).toBe(0);
});

it("distinguishes unsupported data from an empty task list", async () => {
  const { list, io } = boundary();
  io.read = async () => ({ data: null });
  await list.refresh();
  expect(list.getSnapshot()).toMatchObject({ unsupported: true, rows: null });
  io.read = async () => ({ data: [] });
  await list.refresh();
  expect(list.getSnapshot()).toMatchObject({ unsupported: false, rows: [] });
  list.dispose();
});

it.each([
  {
    info: "actionUnavailable",
    message: "unsupported",
    aggregate: true,
    expected: { unsupported: true, rows: null },
  },
  {
    info: "sessionUnavailable",
    message: "thread not found: thread",
    aggregate: true,
    expected: { daemonGone: true },
  },
  {
    info: "sessionUnavailable",
    message: "thread not found: thread",
    aggregate: false,
    expected: { rows: [], daemonGone: false },
  },
])(
  "classifies $info with aggregate=$aggregate",
  async ({ info, message, aggregate, expected }) => {
    const { list, io } = boundary(aggregate);
    await list.refresh();
    io.read = async () => {
      throw new WireError(message, -32000, { evenerErrorInfo: info });
    };
    await list.refresh();
    expect(list.getSnapshot()).toMatchObject({
      ...expected,
      error: null,
      loading: false,
    });
    list.dispose();
  },
);

it("retains loaded rows when a replacement connection cannot refresh them", async () => {
  const previous = boundary();
  await previous.list.refresh();
  const rows = previous.list.getSnapshot().rows;
  previous.list.dispose();
  const replacement = boundary();
  replacement.io.read = async () => {
    throw new Error("Reconnect failed to load tasks");
  };
  const list = new TaskList(
    replacement.client,
    "local:test",
    "thread",
    () => true,
    rows,
  );
  await list.refresh();
  expect(list.getSnapshot().rows).toEqual(rows);
  expect(list.getSnapshot().error).toBeTruthy();
  list.dispose();
  replacement.list.dispose();
});
