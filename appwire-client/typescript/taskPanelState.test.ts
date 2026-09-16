import { expect, test, vi } from "vitest";
import { WireError } from "./errors";
import {
  applyTasksFetchResult,
  classifyTasksRejection,
  classifyTasksResponse,
  createTasksPanelStore,
  EMPTY_TASKS_PANEL_ENTRY,
  type TasksPanelNotifications,
} from "./taskPanelState";
import type { AnyNotification } from "./types.gen";

const row = (id: number, status = "open") => ({
  id,
  status,
  type: "task",
  description: `task-${id}`,
  prompt: "prompt",
  notes: ["update"],
});

// A tasks/list read whose answer the test swaps between calls, plus a
// notification feed the test pushes through - the two ports the store takes.
function boundary() {
  const requests: string[] = [];
  const io = { read: async (): Promise<unknown> => [row(1)] };
  const handlers = new Set<(n: AnyNotification) => void>();
  const notifications: TasksPanelNotifications = {
    onNotification(handler) {
      handlers.add(handler);
      return () => {
        handlers.delete(handler);
      };
    },
  };
  const store = createTasksPanelStore((ref) => {
    requests.push(ref);
    return io.read();
  });
  const notify = (ref = "local:test", threadId = "thread") => {
    for (const handler of handlers)
      handler({ method: "evener/task/updated", params: { ref, threadId, total: 1, done: 0 } });
  };
  const entry = () => store.getState().entries.get("local:test");
  return { store, requests, io, notify, notifications, handlers, entry };
}

test("two stores share nothing: a fetch in one never shows in the other", () => {
  const a = createTasksPanelStore(async () => []);
  const b = createTasksPanelStore(async () => []);
  const fetchID = a.getState().beginFetch("ref_a");
  expect(b.getState().entries.size).toBe(0);
  a.getState().publishFetch("ref_a", fetchID, { kind: "rows", rows: [] });
  b.getState().setRows("ref_a", [row(9) as never]);
  a.getState().evict("ref_a");
  expect(a.getState().entries.has("ref_a")).toBe(false);
  expect(b.getState().entries.get("ref_a")?.rows).toHaveLength(1);
  // Fetch ids are per store too: a fresh store restarts at the same first id.
  expect(b.getState().beginFetch("ref_b")).toBe(fetchID);
});

test("refresh reads through the port and retains the list across a failure until a retry succeeds", async () => {
  const { store, io, requests, entry } = boundary();
  const hasAggregate = () => true;
  expect(await store.refresh("local:test", hasAggregate)).toMatchObject({ kind: "rows" });
  expect(requests).toEqual(["local:test"]);
  expect(entry()?.rows?.[0]).toMatchObject({ id: 1, notes: ["update"] });
  io.read = async () => {
    throw new Error("Connection lost");
  };
  const failed = await store.refresh("local:test", hasAggregate);
  expect(failed?.kind).toBe("failure");
  expect(entry()?.rows).toHaveLength(1);
  expect(entry()?.failure?.sentence).toContain("Connection lost");
  expect(entry()?.failure?.sentence).toContain("Couldn't load tasks");
  io.read = async () => [];
  await store.refresh("local:test", hasAggregate);
  expect(entry()).toMatchObject({ rows: [], failure: null, loading: false });
});

test("watch loads once, coalesces matching invalidations into one follow-up read and stops cleanly", async () => {
  const { store, io, requests, notify, notifications, handlers, entry } = boundary();
  let complete!: (value: unknown) => void;
  io.read = () =>
    new Promise((resolve) => {
      complete = resolve;
    });
  const stop = store.watch(notifications, "local:test", "thread", () => true);
  expect(requests).toHaveLength(1);
  expect(entry()?.loading).toBe(true);
  const publishedRows: Array<number[] | null> = [];
  store.subscribe((s) => publishedRows.push(s.entries.get("local:test")?.rows?.map((item) => item.id) ?? null));
  notify("other:test");
  notify("local:test", "other-thread");
  notify();
  notify();
  io.read = async () => [row(2, "in_progress")];
  complete([row(1)]);
  // A refresh folded into the in-flight run resolves once that run has
  // settled, with null: the run's own caller is the one that gets the result.
  expect(await store.refresh("local:test", () => true)).toBeNull();
  expect(requests).toHaveLength(2);
  expect(entry()?.rows?.map((item) => item.id)).toEqual([2]);
  expect(entry()?.loading).toBe(false);
  // The answer to the read the notifications made stale was never shown.
  expect(publishedRows).not.toContainEqual([1]);
  stop();
  expect(handlers.size).toBe(0);
  notify();
  expect(requests).toHaveLength(2);
});

test("a thread/resync for the watched session refreshes too", async () => {
  const { store, requests, notifications, handlers, entry } = boundary();
  const stop = store.watch(notifications, "local:test", "thread", () => true);
  await vi.waitFor(() => expect(entry()?.loading).toBe(false));
  for (const handler of handlers)
    handler({ method: "evener/thread/resync", params: { ref: "local:test", threadId: "thread" } });
  expect(requests).toHaveLength(2);
  await vi.waitFor(() => expect(entry()?.loading).toBe(false));
  stop();
});

test("the run's caller receives the result and a superseded caller receives null", async () => {
  const { store, io } = boundary();
  let complete!: (value: unknown) => void;
  io.read = () =>
    new Promise((resolve) => {
      complete = resolve;
    });
  const first = store.refresh("local:test", () => true);
  const joined = store.refresh("local:test", () => true);
  io.read = async () => {
    throw new Error("later");
  };
  complete([row(1)]);
  expect(await joined).toBeNull();
  expect(await first).toMatchObject({ kind: "failure" });
});

test("null data is unsupported, an empty array is an empty list", async () => {
  const { store, io, entry } = boundary();
  io.read = async () => null;
  await store.refresh("local:test", () => true);
  expect(entry()).toMatchObject({ unsupported: true, rows: null });
  io.read = async () => [];
  await store.refresh("local:test", () => true);
  expect(entry()).toMatchObject({ unsupported: false, rows: [] });
});

test.each([
  { info: "actionUnavailable", message: "unsupported", aggregate: true, expected: { unsupported: true, rows: null } },
  { info: "sessionUnavailable", message: "thread not found: thread", aggregate: true, expected: { daemonGone: true } },
  {
    info: "sessionUnavailable",
    message: "thread not found: thread",
    aggregate: false,
    expected: { rows: [], daemonGone: false },
  },
])("classifies $info with aggregate=$aggregate", async ({ info, message, aggregate, expected }) => {
  const { store, io, entry } = boundary();
  await store.refresh("local:test", () => aggregate);
  io.read = async () => {
    throw new WireError(message, -32000, { evenerErrorInfo: info });
  };
  await store.refresh("local:test", () => aggregate);
  expect(entry()).toMatchObject({ ...expected, failure: null, loading: false });
});

test("a daemon-gone rejection keeps the rows the panel already holds", async () => {
  const { store, io, entry } = boundary();
  await store.refresh("local:test", () => true);
  io.read = async () => {
    throw new WireError("thread not found: thread", -32000, { evenerErrorInfo: "sessionUnavailable" });
  };
  await store.refresh("local:test", () => true);
  expect(entry()).toMatchObject({ daemonGone: true, rows: [row(1)] });
});

test("the pure classifiers and the entry transition are exposed for a caller that fetches on its own", () => {
  expect(classifyTasksResponse(null)).toEqual({ kind: "unsupported" });
  expect(classifyTasksResponse([])).toEqual({ kind: "rows", rows: [] });
  expect(classifyTasksRejection(new WireError("x", -1, { evenerErrorInfo: "actionUnavailable" }), true)).toEqual({
    kind: "unsupported",
  });
  expect(classifyTasksRejection(new Error("boom"), true)).toMatchObject({
    kind: "failure",
    failure: { headline: "Couldn't load tasks", detail: "boom", sentence: "Couldn't load tasks: boom" },
  });
  const loaded = applyTasksFetchResult(EMPTY_TASKS_PANEL_ENTRY, { kind: "rows", rows: [] });
  expect(applyTasksFetchResult(loaded, { kind: "daemon-gone" })).toMatchObject({ rows: [], daemonGone: true });
  expect(applyTasksFetchResult(loaded, { kind: "unsupported" })).toMatchObject({ rows: null, unsupported: true });
});
