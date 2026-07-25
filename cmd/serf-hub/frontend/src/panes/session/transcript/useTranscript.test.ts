import { act, cleanup, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, expect, test } from "vitest";
import { FakeClient } from "../../../protocol/testing/fakeClient";
import type { Thread, ThreadCapabilities, ThreadTurnsListResponse } from "../../../protocol/types.gen";
import { connectionStore } from "../../../stores/connection";
import { resetThreadsStoreForTests, threadsStore } from "../../../stores/threads";
import { useTranscript } from "./useTranscript";

// flushUntil drains microtask turns until `done()` reports true - same
// contract/name as stores/threads.test.ts's own helper (duplicated here:
// the two test files share no test-utils module).
async function flushUntil(done: () => boolean, maxTurns = 20): Promise<void> {
  for (let i = 0; i < maxTurns && !done(); i += 1) await Promise.resolve();
}

const CAPABILITIES: ThreadCapabilities = {
  send: true,
  steer: true,
  interrupt: true,
  compact: true,
  clear: true,
  forkFromTurn: true,
  shutdown: true,
  changeModel: true,
  queue: true,
  goal: true,
  rename: true,
};

function testThread(ref: string, overrides: Partial<Thread> = {}): Thread {
  return {
    id: `thr_${ref}`,
    sessionId: `sess_${ref}`,
    preview: "test",
    ephemeral: false,
    modelProvider: "anthropic/claude-sonnet-4-5",
    createdAt: 1000,
    updatedAt: 1000,
    status: { type: "idle" },
    cwd: "/tmp/project",
    cliVersion: "1.0.0",
    source: "serf",
    serf: { ref, capabilities: CAPABILITIES, queue: {} },
    ...overrides,
  };
}

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
});

afterEach(() => {
  cleanup();
});

test("model is undefined before the ref is tracked", () => {
  const { result } = renderHook(() => useTranscript("ref_untracked"));
  expect(result.current.model).toBeUndefined();
});

test("model reflects the store once ensureThread hydrates it", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => ({ thread: testThread("ref_a") }));
  const { result } = renderHook(() => useTranscript("ref_a"));
  expect(result.current.model).toBeUndefined();

  await act(async () => {
    await threadsStore.getState().ensureThread("ref_a");
  });

  expect(result.current.model?.ref).toBe("ref_a");
});

test("loadingOlder starts false", () => {
  const { result } = renderHook(() => useTranscript("ref_a"));
  expect(result.current.loadingOlder).toBe(false);
});

test("loadOlder() fetches thread/turns/list via the model's olderCursor and prepends the page", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => ({
    thread: testThread("ref_a", { turns: [{ id: "turn_2", status: "completed", itemsView: "full", items: [] }] }),
    olderCursor: "cursor_1",
  }));
  fake.on("thread/turns/list", (params) => {
    expect(params).toMatchObject({ ref: "ref_a", cursor: "cursor_1" });
    return { data: [{ id: "turn_1", status: "completed", itemsView: "full", items: [] }], nextCursor: undefined };
  });
  await act(async () => {
    await threadsStore.getState().ensureThread("ref_a");
  });

  const { result } = renderHook(() => useTranscript("ref_a"));
  await act(async () => {
    await result.current.loadOlder();
  });

  expect(result.current.model?.turns.map((t) => t.id)).toEqual(["turn_1", "turn_2"]);
});

test("loadingOlder is true while the request is in flight and false once it settles", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => ({ thread: testThread("ref_a"), olderCursor: "cursor_1" }));
  await act(async () => {
    await threadsStore.getState().ensureThread("ref_a");
  });

  const box: { resolve: ((r: ThreadTurnsListResponse) => void) | null } = { resolve: null };
  fake.on("thread/turns/list", () => new Promise<ThreadTurnsListResponse>((resolve) => (box.resolve = resolve)));

  const { result } = renderHook(() => useTranscript("ref_a"));
  expect(result.current.loadingOlder).toBe(false);

  let loadPromise!: Promise<void>;
  act(() => {
    loadPromise = result.current.loadOlder();
  });
  await flushUntil(() => box.resolve !== null);
  expect(result.current.loadingOlder).toBe(true);

  await act(async () => {
    box.resolve?.({ data: [], nextCursor: undefined });
    await loadPromise;
  });
  expect(result.current.loadingOlder).toBe(false);
});

test("a second loadOlder() call while one is already in flight does not issue a second request", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => ({ thread: testThread("ref_a"), olderCursor: "cursor_1" }));
  await act(async () => {
    await threadsStore.getState().ensureThread("ref_a");
  });

  const box: { resolve: ((r: ThreadTurnsListResponse) => void) | null } = { resolve: null };
  fake.on("thread/turns/list", () => new Promise<ThreadTurnsListResponse>((resolve) => (box.resolve = resolve)));

  const { result } = renderHook(() => useTranscript("ref_a"));

  let firstLoad!: Promise<void>;
  act(() => {
    firstLoad = result.current.loadOlder();
  });
  await flushUntil(() => box.resolve !== null);

  let secondLoad!: Promise<void>;
  await act(async () => {
    secondLoad = result.current.loadOlder(); // loadingOlder is true; this must no-op
  });

  await act(async () => {
    box.resolve?.({ data: [], nextCursor: undefined });
    await Promise.all([firstLoad, secondLoad]);
  });

  expect(fake.calls.filter((c) => c.method === "thread/turns/list")).toHaveLength(1);
});

test("loadOlder() is a harmless no-op when the model has no olderCursor (nothing more to load)", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => ({ thread: testThread("ref_a") })); // no olderCursor
  await act(async () => {
    await threadsStore.getState().ensureThread("ref_a");
  });

  const { result } = renderHook(() => useTranscript("ref_a"));
  await act(async () => {
    await result.current.loadOlder();
  });

  expect(fake.calls.filter((c) => c.method === "thread/turns/list")).toHaveLength(0);
  expect(result.current.loadingOlder).toBe(false);
});

// Pins the `finally` block's own behavior (useTranscript.ts's loadOlder has
// no catch of its own - a rejection propagates to the caller) rather than
// leaving loadingOlder stuck true forever, and does so without ever
// becoming an unhandled rejection (vitest fails the run on those).
test("a rejected loadOlder() propagates to the caller and still resets loadingOlder to false", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => ({ thread: testThread("ref_a"), olderCursor: "cursor_1" }));
  await act(async () => {
    await threadsStore.getState().ensureThread("ref_a");
  });
  fake.on("thread/turns/list", () => Promise.reject(new Error("network error")));

  const { result } = renderHook(() => useTranscript("ref_a"));
  expect(result.current.loadingOlder).toBe(false);

  await act(async () => {
    await expect(result.current.loadOlder()).rejects.toThrow("network error");
  });

  expect(result.current.loadingOlder).toBe(false);
});
