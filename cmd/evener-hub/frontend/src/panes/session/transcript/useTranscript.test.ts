import type { Thread, ThreadCapabilities, ThreadTurnsListResponse } from "@evener/appwire-client";
import { hydrateThread, WireError } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, render, renderHook } from "@testing-library/react";
import { createElement, type PropsWithChildren, StrictMode, Suspense, useEffect } from "react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { connectionStore } from "../../../stores/connection";
import { resetThreadsStoreForTests, threadsStore } from "../../../stores/threads";
import { useTranscriptScroll } from "./flow/useTranscriptScroll";
import { resetTranscriptPagingForTests, useTranscript } from "./useTranscript";

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
  changeVisionModel: true,
  queue: true,
  goal: true,
  sharedNotes: true,
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
    source: "evener",
    evener: { ref, capabilities: CAPABILITIES, queue: { revision: 0 } },
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
  resetTranscriptPagingForTests();
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

// Two callers in the SAME tick, which is the shape automatic paging actually
// produces: LoadOlderRow's geometry fill and useTranscriptScroll's near-top
// scroll trigger both fire for one scroll. A guard reading loadingOlder from a
// state closure lets both through (observed
// live: the same cursor requested twice); the ref-based guard does not.
test("two loadOlder() calls in the same tick issue exactly one request", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => ({ thread: testThread("ref_a"), olderCursor: "cursor_1" }));
  await act(async () => {
    await threadsStore.getState().ensureThread("ref_a");
  });

  const box: { resolve: ((r: ThreadTurnsListResponse) => void) | null } = { resolve: null };
  fake.on("thread/turns/list", () => new Promise<ThreadTurnsListResponse>((resolve) => (box.resolve = resolve)));

  const { result } = renderHook(() => useTranscript("ref_a"));

  let both!: Promise<unknown>;
  act(() => {
    // No await between them - the same synchronous tick.
    both = Promise.all([result.current.loadOlder(), result.current.loadOlder()]);
  });
  await flushUntil(() => box.resolve !== null);

  await act(async () => {
    box.resolve?.({ data: [], nextCursor: undefined });
    await both;
  });

  expect(fake.calls.filter((c) => c.method === "thread/turns/list")).toHaveLength(1);
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

// --- loadOlderReportingError / olderError --------------------------------
// The fire-and-forget form both transcript surfaces' paging affordance calls.
// A failure has to land SOMEWHERE the reader can see: automatic paging means
// nobody pressed anything, so there is no call site to reject back to.

test("olderError starts null", () => {
  const { result } = renderHook(() => useTranscript("ref_a"));
  expect(result.current.olderError).toBeNull();
});

test("loadOlderReportingError records a failed fetch's message instead of rejecting", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => ({ thread: testThread("ref_a"), olderCursor: "cursor_1" }));
  await act(async () => {
    await threadsStore.getState().ensureThread("ref_a");
  });
  fake.on("thread/turns/list", () => Promise.reject(new Error("network error")));

  const { result } = renderHook(() => useTranscript("ref_a"));
  await act(async () => {
    result.current.loadOlderReportingError();
    await flushUntil(() => result.current.olderError !== null);
  });

  expect(result.current.olderError).toBe("Couldn't load older turns: network error");
  expect(result.current.loadingOlder).toBe(false);
});

// Paging routes through the hub's transparent resume like every other
// session call, so a cold session's dead spawner can be what rejects here.
// The page never arrived, so the resume takes the whole sentence: a reader
// told the transcript failed would go hunting in the transcript.
test("a failed auto-resume names the resume, not the page fetch", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => ({ thread: testThread("ref_a"), olderCursor: "cursor_1" }));
  await act(async () => {
    await threadsStore.getState().ensureThread("ref_a");
  });
  fake.on("thread/turns/list", () =>
    Promise.reject(new WireError("evener launch-check timed out", -32014, { evenerErrorInfo: "hubLaunch" })),
  );

  const { result } = renderHook(() => useTranscript("ref_a"));
  await act(async () => {
    result.current.loadOlderReportingError();
    await flushUntil(() => result.current.olderError !== null);
  });

  expect(result.current.olderError).toBe("Couldn't start this session: evener launch-check timed out");
});

test("a successful fetch leaves olderError null", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => ({
    thread: testThread("ref_a", { turns: [{ id: "turn_2", status: "completed", itemsView: "full", items: [] }] }),
    olderCursor: "cursor_1",
  }));
  fake.on("thread/turns/list", () => ({
    data: [{ id: "turn_1", status: "completed", itemsView: "full", items: [] }],
    nextCursor: undefined,
  }));
  await act(async () => {
    await threadsStore.getState().ensureThread("ref_a");
  });

  const { result } = renderHook(() => useTranscript("ref_a"));
  await act(async () => {
    result.current.loadOlderReportingError();
    await flushUntil(() => result.current.model?.turns.length === 2);
  });

  expect(result.current.olderError).toBeNull();
});

test("a retry after a failure clears the previous error before re-fetching", async () => {
  const fake = connectFakeClient();
  fake.on("thread/read", () => ({
    thread: testThread("ref_a", { turns: [{ id: "turn_2", status: "completed", itemsView: "full", items: [] }] }),
    olderCursor: "cursor_1",
  }));
  await act(async () => {
    await threadsStore.getState().ensureThread("ref_a");
  });

  fake.on("thread/turns/list", () => Promise.reject(new Error("network error")));
  const { result } = renderHook(() => useTranscript("ref_a"));
  await act(async () => {
    result.current.loadOlderReportingError();
    await flushUntil(() => result.current.olderError !== null);
  });
  expect(result.current.olderError).toBe("Couldn't load older turns: network error");

  fake.on("thread/turns/list", () => ({
    data: [{ id: "turn_1", status: "completed", itemsView: "full", items: [] }],
    nextCursor: undefined,
  }));
  await act(async () => {
    result.current.loadOlderReportingError();
    await flushUntil(() => result.current.model?.turns.length === 2);
  });

  expect(result.current.olderError).toBeNull();
  expect(result.current.model?.turns.map((t) => t.id)).toEqual(["turn_1", "turn_2"]);
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

afterEach(() => vi.useRealTimers());

test("retains older-page demand through prolonged failure and heals without another gesture", async () => {
  vi.useFakeTimers();
  const fake = connectFakeClient();
  fake.on("thread/read", () => ({
    thread: testThread("ref_a", { turns: [{ id: "turn_2", status: "completed", itemsView: "full", items: [] }] }),
    olderCursor: "cursor_1",
  }));
  await act(async () => {
    await threadsStore.getState().ensureThread("ref_a");
  });
  let attempts = 0;
  fake.on("thread/turns/list", () => {
    attempts += 1;
    if (attempts <= 12) throw new Error("temporarily unavailable");
    return { data: [{ id: "turn_1", status: "completed", itemsView: "full", items: [] }], nextCursor: undefined };
  });
  const hook = renderHook(() => useTranscript("ref_a"));
  await act(async () => {
    hook.result.current.loadOlderReportingError();
  });
  expect(attempts).toBe(1);
  expect(hook.result.current.model?.turns.map((turn) => turn.id)).toEqual(["turn_2"]);
  await act(async () => {
    await vi.advanceTimersByTimeAsync(999);
  });
  expect(attempts).toBe(1);
  await act(async () => {
    await vi.advanceTimersByTimeAsync(300_000);
  });
  expect(hook.result.current.model?.turns.map((turn) => turn.id)).toEqual(["turn_1", "turn_2"]);
  expect(attempts).toBe(13);
  expect(hook.result.current.olderError).toBeNull();
  hook.unmount();
});

test.each([false, true])("suspends older-page retries and resumes on return (StrictMode: %s)", async (strict) => {
  vi.useFakeTimers();
  const fake = connectFakeClient();
  fake.on("thread/read", () => ({ thread: testThread("ref_a"), olderCursor: "cursor_1" }));
  await act(async () => {
    await threadsStore.getState().ensureThread("ref_a");
  });
  let attempts = 0;
  fake.on("thread/turns/list", () => {
    attempts += 1;
    if (attempts === 1) throw new Error("temporarily unavailable");
    return { data: [{ id: "older", status: "completed", itemsView: "full", items: [] }], nextCursor: undefined };
  });
  const first = renderHook(
    () => useTranscript("ref_a"),
    strict
      ? {
          wrapper: ({ children }: PropsWithChildren) => createElement(StrictMode, null, children),
        }
      : undefined,
  );
  await act(async () => {
    first.result.current.loadOlderReportingError();
  });
  first.unmount();
  await act(async () => {
    await vi.advanceTimersByTimeAsync(60_000);
  });
  expect(attempts).toBe(1);
  const returned = renderHook(() => useTranscript("ref_a"));
  await act(async () => {
    await vi.advanceTimersByTimeAsync(1000);
  });
  expect(returned.result.current.model?.turns.map((turn) => turn.id)).toEqual(["older"]);
  expect(attempts).toBe(2);
  returned.unmount();
});

test("jumping live cancels this pane's recovery without cancelling another pane", async () => {
  vi.useFakeTimers();
  const fake = connectFakeClient();
  fake.on("thread/read", () => ({ thread: testThread("ref_a"), olderCursor: "page" }));
  let attempts = 0;
  fake.on("thread/turns/list", () => {
    attempts += 1;
    throw new Error("temporary");
  });
  await act(async () => {
    await threadsStore.getState().ensureThread("ref_a");
  });
  const first = renderHook(() => {
    const transcript = useTranscript("ref_a", "pane-one");
    const flow = useTranscriptScroll({
      ref: "ref_a",
      model: transcript.model,
      listRef: { current: null },
      loadOlder: transcript.loadOlder,
      cancelOlder: transcript.cancelOlder,
    });
    return { transcript, flow };
  });
  const second = renderHook(() => useTranscript("ref_a", "pane-two"));
  await act(async () => {
    await first.result.current.transcript.loadOlder().catch(() => {});
  });
  await act(async () => {
    await second.result.current.loadOlder();
  });
  act(() => first.result.current.flow.jumpToBottom());
  await act(async () => {
    await vi.advanceTimersByTimeAsync(1000);
  });
  expect(attempts).toBe(2);
  act(() => second.result.current.cancelOlder());
  await act(async () => {
    await vi.advanceTimersByTimeAsync(600_000);
  });
  expect(attempts).toBe(2);
  first.unmount();
  second.unmount();
  vi.useRealTimers();
});

test.each([false, true])(
  "retains failed demand across client replacement (temporary null: %s)",
  async (clearClient) => {
    vi.useFakeTimers();
    const first = connectFakeClient();
    first.on("thread/read", () => ({ thread: testThread("ref_a"), olderCursor: "page" }));
    first.on("thread/turns/list", () => {
      throw new Error("temporary");
    });
    await act(async () => {
      await threadsStore.getState().ensureThread("ref_a");
    });
    const hook = renderHook(() => useTranscript("ref_a"));
    await act(async () => {
      await hook.result.current.loadOlder().catch(() => {});
    });
    if (clearClient) act(() => connectionStore.setState({ client: null, state: "reconnecting" }));
    const replacement = new FakeClient("ready");
    replacement.on("thread/read", () => ({ thread: testThread("ref_a"), olderCursor: "page" }));
    let attempts = 0;
    replacement.on("thread/turns/list", (): ThreadTurnsListResponse => {
      attempts += 1;
      return {
        data: [{ id: "healed-turn", status: "completed", itemsView: "full", items: [] }],
        nextCursor: undefined,
      };
    });
    act(() => connectionStore.getState().connect(replacement));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10_000);
    });
    expect(attempts).toBe(1);
    expect(hook.result.current.model?.turns.map((turn) => turn.id)).toContain("healed-turn");
    hook.unmount();
    vi.useRealTimers();
  },
);

test("does not transfer old demand into a different conversation binding at the same ref", async () => {
  vi.useFakeTimers();
  const fake = connectFakeClient();
  fake.on("thread/read", () => ({ thread: testThread("ref_a"), olderCursor: "page" }));
  let attempts = 0;
  fake.on("thread/turns/list", () => {
    attempts += 1;
    throw new Error("temporary");
  });
  await act(async () => {
    await threadsStore.getState().ensureThread("ref_a");
  });
  const hook = renderHook(() => useTranscript("ref_a"));
  await act(async () => {
    await hook.result.current.loadOlder().catch(() => {});
  });
  act(() =>
    threadsStore.setState((state) => {
      const threads = new Map(state.threads);
      const model = threads.get("ref_a");
      if (model) threads.set("ref_a", { ...model, instanceId: "different-binding" });
      return { threads };
    }),
  );
  await act(async () => {
    await vi.advanceTimersByTimeAsync(600_000);
  });
  expect(attempts).toBe(1);
  hook.unmount();
  vi.useRealTimers();
});

test("abandoned first-binding renders cannot retire retained pre-hydration demand", async () => {
  vi.useFakeTimers();
  const fake = connectFakeClient();
  const waiting = renderHook(() => useTranscript("ref_a"));
  await act(async () => {
    await waiting.result.current.loadOlder();
  });
  waiting.unmount();
  const putModel = (id: string) =>
    threadsStore.setState({
      threads: new Map([
        ["ref_a", hydrateThread({ thread: testThread("ref_a", { id }), olderCursor: "page" }, "ref_a", 1000)],
      ]),
    });
  putModel("uncommitted-thread");
  const never = new Promise<never>(() => {});
  function AbandonedPane(): never {
    useTranscript("ref_a");
    throw never;
  }
  const abandoned = render(createElement(Suspense, { fallback: null }, createElement(AbandonedPane)));
  abandoned.unmount();
  putModel("committed-thread");
  fake.on("thread/turns/list", () => ({ data: [], nextCursor: undefined }));
  const returned = renderHook(() => useTranscript("ref_a"));
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0);
  });
  expect(fake.calls.filter((call) => call.method === "thread/turns/list")).toHaveLength(1);
  expect(returned.result.current.olderError).toBeNull();
  returned.unmount();
});

test("unavailable history stays pending while a hydrated model with no cursor confirms the end", async () => {
  vi.useFakeTimers();
  const fake = connectFakeClient();
  fake.on("thread/read", () => ({ thread: testThread("ref_a") }));
  const hook = renderHook(() => useTranscript("ref_a"));
  await act(async () => {
    await hook.result.current.loadOlder();
  });
  expect(hook.result.current.model).toBeUndefined();
  await act(async () => {
    await threadsStore.getState().ensureThread("ref_a");
    await vi.advanceTimersByTimeAsync(0);
  });
  expect(hook.result.current.model?.olderCursor).toBeUndefined();
  expect(hook.result.current.loadingOlder).toBe(false);
  expect(hook.result.current.olderError).toBeNull();
  expect(fake.calls.filter((call) => call.method === "thread/turns/list")).toHaveLength(0);
  hook.unmount();
});

test("a committed ref change routes automatic demand to that ref with another reader still active", async () => {
  vi.useFakeTimers();
  const fake = connectFakeClient();
  fake.on("thread/read", (params) => {
    if (params.ref === undefined) throw new Error("Expected a ref-bound transcript read.");
    return { thread: testThread(params.ref), olderCursor: "page-1" };
  });
  fake.on("thread/turns/list", () => ({ data: [], nextCursor: "page-2" }));
  await act(async () => {
    await threadsStore.getState().ensureThread("ref_a");
    await threadsStore.getState().ensureThread("ref_b");
  });
  const other = renderHook(() => useTranscript("ref_a", "other-pane"));
  const requestedRefs: string[] = [];
  const reader = renderHook(
    ({ ref }) => {
      const transcript = useTranscript(ref, "switching-pane");
      useEffect(() => {
        requestedRefs.push(ref);
        void transcript.loadOlder().catch(() => {});
      }, [ref, transcript.loadOlder]);
      return transcript;
    },
    { initialProps: { ref: "ref_a" } },
  );
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0);
  });
  expect(
    fake.calls.filter(
      (call) => call.method === "thread/turns/list" && (call.params as { ref: string }).ref === "ref_a",
    ),
  ).toHaveLength(1);
  reader.rerender({ ref: "ref_b" });
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0);
  });
  expect(
    fake.calls.filter(
      (call) => call.method === "thread/turns/list" && (call.params as { ref: string }).ref === "ref_a",
    ),
  ).toHaveLength(1);
  expect(
    fake.calls.filter(
      (call) => call.method === "thread/turns/list" && (call.params as { ref: string }).ref === "ref_b",
    ),
  ).toHaveLength(1);
  expect(requestedRefs.at(-1)).toBe("ref_b");
  reader.unmount();
  other.unmount();
});
