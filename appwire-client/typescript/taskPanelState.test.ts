// @vitest-environment node

import { expect, test, vi } from "vitest";
import { WireError } from "./errors";
import {
  applyTasksFetchResult,
  classifyTasksRejection,
  classifyTasksResponse,
  createTasksPanelStore,
  EMPTY_TASKS_PANEL_ENTRY,
  panelLoadFailure,
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
  const notify = (
    ref = "local:test",
    threadId = "thread",
    method: "evener/task/updated" | "evener/thread/resync" = "evener/task/updated",
  ) => {
    const notification: AnyNotification =
      method === "evener/thread/resync"
        ? { method, params: { ref, threadId } }
        : { method, params: { ref, threadId, total: 1, done: 0 } };
    for (const handler of handlers) handler(notification);
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
  // settled; as the run's latest caller it is the one that gets the result.
  expect(await store.refresh("local:test", () => true)).toMatchObject({ kind: "rows" });
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
  const { store, requests, notifications, notify, entry } = boundary();
  const stop = store.watch(notifications, "local:test", "thread", () => true);
  await vi.waitFor(() => expect(entry()?.loading).toBe(false));
  notify("local:test", "thread", "evener/thread/resync");
  expect(requests).toHaveLength(2);
  await vi.waitFor(() => expect(entry()?.loading).toBe(false));
  stop();
});

test("the run's latest caller receives the result and every earlier caller receives null", async () => {
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
  expect(await first).toBeNull();
  expect(await joined).toMatchObject({ kind: "failure" });
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
  expect(panelLoadFailure("Couldn't load activity", new Error(""))).toEqual({
    headline: "Couldn't load activity",
    sentence: "Couldn't load activity",
  });
  const loaded = applyTasksFetchResult(EMPTY_TASKS_PANEL_ENTRY, { kind: "rows", rows: [] });
  expect(applyTasksFetchResult(loaded, { kind: "daemon-gone" })).toMatchObject({ rows: [], daemonGone: true });
  expect(applyTasksFetchResult(loaded, { kind: "unsupported" })).toMatchObject({ rows: null, unsupported: true });
});

test("a read that throws synchronously still settles the caller and clears the run", async () => {
  const store = createTasksPanelStore(() => {
    throw new Error("no client yet");
  });
  const result = await store.refresh("local:test", () => true);
  expect(result).toMatchObject({ kind: "failure" });
  expect(store.getState().entries.get("local:test")).toMatchObject({ loading: false, rows: null });
  expect(store.getState().entries.get("local:test")?.failure?.sentence).toContain("no client yet");
  // The run is gone: the next refresh starts a fresh one rather than joining a ghost.
  expect(await store.refresh("local:test", () => true)).toMatchObject({ kind: "failure" });
});

test("a hasAggregate that throws does not strand the run", async () => {
  const store = createTasksPanelStore(async () => {
    throw new WireError("thread not found: t", -32000, { evenerErrorInfo: "sessionUnavailable" });
  });
  await expect(
    store.refresh("local:test", () => {
      throw new Error("model gone");
    }),
  ).rejects.toThrow("model gone");
  expect(await store.refresh("local:test", () => false)).toEqual({ kind: "empty" });
});

test("resetForTests settles the waiters of a run in flight and stops it looping into the reset store", async () => {
  const { store, io, requests, entry } = boundary();
  let complete!: (value: unknown) => void;
  io.read = () =>
    new Promise((resolve) => {
      complete = resolve;
    });
  const first = store.refresh("local:test", () => true);
  const joined = store.refresh("local:test", () => true);
  store.getState().resetForTests();
  expect(await first).toBeNull();
  expect(await joined).toBeNull();
  io.read = async () => [row(2)];
  complete([row(1)]);
  await Promise.resolve();
  await Promise.resolve();
  // The abandoned run neither re-read nor published into the reset store.
  expect(requests).toHaveLength(1);
  expect(entry()).toBeUndefined();
  expect(await store.refresh("local:test", () => true)).toMatchObject({ kind: "rows" });
  expect(entry()?.rows?.map((item) => item.id)).toEqual([2]);
});

test("a thrown hasAggregate settles the entry as a failure, not a spinner", async () => {
  const store = createTasksPanelStore(async () => {
    throw new WireError("thread not found: t", -32000, { evenerErrorInfo: "sessionUnavailable" });
  });
  await expect(
    store.refresh("local:test", () => {
      throw new Error("model gone");
    }),
  ).rejects.toThrow("model gone");
  const entry = store.getState().entries.get("local:test");
  expect(entry?.loading).toBe(false);
  expect(entry?.failure?.sentence).toContain("model gone");
});

test("a thrown hasAggregate rejects only the run's owner; earlier callers still resolve null", async () => {
  let complete!: (value: unknown) => void;
  const store = createTasksPanelStore(
    () =>
      new Promise((resolve) => {
        complete = resolve;
      }),
  );
  const throwing = () => {
    throw new Error("model gone");
  };
  const first = store.refresh("local:test", throwing);
  const owner = store.refresh("local:test", throwing);
  // Only a rejection classifies through hasAggregate; make the read reject.
  const failing = new WireError("thread not found: t", -32000, { evenerErrorInfo: "sessionUnavailable" });
  complete(Promise.reject(failing));
  await expect(owner).rejects.toThrow("model gone");
  expect(await first).toBeNull();
});

test("a push-driven refresh whose port throws leaves no unhandled rejection behind", async () => {
  const { store, io, notifications, notify, entry } = boundary();
  io.read = async () => {
    throw new WireError("thread not found: t", -32000, { evenerErrorInfo: "sessionUnavailable" });
  };
  const stop = store.watch(notifications, "local:test", "thread", () => {
    throw new Error("model gone");
  });
  await vi.waitFor(() => expect(entry()?.loading).toBe(false));
  notify();
  await vi.waitFor(() => expect(entry()?.failure?.sentence).toContain("model gone"));
  await new Promise((resolve) => setTimeout(resolve, 0));
  stop();
});
