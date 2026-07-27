import { act, cleanup, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import type { ConnectionState } from "../../../../protocol/client";
import { FakeClient } from "../../../../protocol/testing/fakeClient";
import type { Thread, ThreadCapabilities, ThreadReadResponse } from "../../../../protocol/types.gen";
import { connectionStore } from "../../../../stores/connection";
import { resetThreadsStoreForTests, threadsStore } from "../../../../stores/threads";
import {
  PENDING_TIMEOUT_MS,
  resetPendingTurnsStoreForTests,
  submitWithPendingTracking,
  useAwaitingFirstFrameSend,
  usePendingTurnEntries,
} from "./pendingTurnsStore";

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

// testThread hydrates with one already-open, in-progress turn (turn_1) and
// an active-turn id set: the natural precondition for both a new userMessage
// item (item/completed inserts into an existing turn) and a new steering
// item (serf/steering/injected only ever lands on model.activeTurnId) to
// land anywhere at all - see reducer.ts's own guards.
function testThread(ref: string, overrides: Partial<Thread> = {}): Thread {
  return {
    id: `thr_${ref}`,
    sessionId: `sess_${ref}`,
    preview: "test",
    ephemeral: false,
    modelProvider: "anthropic/claude-sonnet-4-5",
    createdAt: 1000,
    updatedAt: 1000,
    status: { type: "active" },
    cwd: "/tmp/project",
    cliVersion: "1.0.0",
    source: "serf",
    serf: { ref, capabilities: CAPABILITIES, queue: {}, activeTurnId: "turn_1" },
    turns: [{ id: "turn_1", status: "inProgress", itemsView: "full", items: [] }],
    ...overrides,
  };
}

function readResponse(ref: string, overrides: Partial<Thread> = {}): ThreadReadResponse {
  return { thread: testThread(ref, overrides) };
}

function connectFakeClient(state: ConnectionState = "ready"): FakeClient {
  const fake = new FakeClient(state);
  connectionStore.getState().connect(fake);
  return fake;
}

async function hydrate(fake: FakeClient, ref: string, overrides: Partial<Thread> = {}): Promise<void> {
  fake.on("thread/read", () => readResponse(ref, overrides));
  await threadsStore.getState().ensureThread(ref);
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  resetPendingTurnsStoreForTests();
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.useRealTimers();
});

describe("submitWithPendingTracking", () => {
  test("registers a pending entry visible via usePendingTurnEntries", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a");
    fake.on("turn/queue", () => ({}));

    const { result } = renderHook(() => usePendingTurnEntries("ref_a", "queue"));
    expect(result.current).toHaveLength(0);

    await act(async () => {
      await submitWithPendingTracking({ ref: "ref_a", method: "queue", text: "hello", onFailure: () => {} }, () =>
        threadsStore.getState().queue("ref_a", "hello"),
      );
    });

    expect(result.current).toHaveLength(1);
    expect(result.current[0]).toMatchObject({ ref: "ref_a", method: "queue", text: "hello" });
  });

  test("a successful perform() alone does not resolve the entry - it stays pending awaiting a wire echo", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a");
    fake.on("turn/steer", () => ({}));

    const { result } = renderHook(() => usePendingTurnEntries("ref_a", "steer"));

    await act(async () => {
      await submitWithPendingTracking({ ref: "ref_a", method: "steer", text: "steer this", onFailure: () => {} }, () =>
        threadsStore.getState().steer("ref_a", "steer this"),
      );
    });

    expect(result.current).toHaveLength(1);
  });

  test("a send is awaiting its first frame before perform() resolves, survives its user echo, and clears on an authoritative frame", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a");
    let resolvePerform: (() => void) | undefined;
    const send = submitWithPendingTracking(
      { ref: "ref_a", method: "send", text: "hello", onFailure: () => {} },
      () =>
        new Promise<void>((resolve) => {
          resolvePerform = resolve;
        }),
    );
    const { result: awaitingResult } = renderHook(() => useAwaitingFirstFrameSend("ref_a"));
    const { result: pendingResult } = renderHook(() => usePendingTurnEntries("ref_a", "send"));

    expect(resolvePerform).toBeTruthy();
    expect(awaitingResult.current).toBe(true);
    expect(pendingResult.current).toHaveLength(1);

    act(() => {
      fake.emitNotification({
        method: "item/completed",
        params: {
          threadId: "thr_ref_a",
          ref: "ref_a",
          turnId: "turn_1",
          item: { id: "user_1", turnId: "turn_1", type: "userMessage", text: "hello", status: "completed" },
        },
      });
    });
    expect(pendingResult.current).toHaveLength(0);
    expect(awaitingResult.current).toBe(true);

    await act(async () => {
      resolvePerform?.();
      await send;
    });
    expect(awaitingResult.current).toBe(true);

    act(() => {
      fake.emitNotification({
        method: "item/started",
        params: {
          threadId: "thr_ref_a",
          ref: "ref_a",
          turnId: "turn_1",
          item: { id: "agent_1", turnId: "turn_1", type: "agentMessage", status: "inProgress" },
        },
      });
    });
    expect(awaitingResult.current).toBe(false);
  });

  test("a rejecting send clears awaiting state after its user echo", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a");
    let rejectPerform: ((error: unknown) => void) | undefined;
    const send = submitWithPendingTracking(
      { ref: "ref_a", method: "send", text: "hello", onFailure: () => {} },
      () =>
        new Promise<void>((_resolve, reject) => {
          rejectPerform = reject;
        }),
    );
    const { result: awaitingResult } = renderHook(() => useAwaitingFirstFrameSend("ref_a"));
    const { result: pendingResult } = renderHook(() => usePendingTurnEntries("ref_a", "send"));

    act(() => {
      fake.emitNotification({
        method: "item/completed",
        params: {
          threadId: "thr_ref_a",
          ref: "ref_a",
          turnId: "turn_1",
          item: { id: "user_1", turnId: "turn_1", type: "userMessage", text: "hello", status: "completed" },
        },
      });
    });
    expect(pendingResult.current).toHaveLength(0);
    expect(awaitingResult.current).toBe(true);

    await act(async () => {
      rejectPerform?.(new Error("rejected"));
      await expect(send).rejects.toThrow("rejected");
    });
    expect(awaitingResult.current).toBe(false);
  });

  test("a rejecting perform() removes the entry immediately, calls onFailure with the raw error, and rethrows", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a");
    fake.on("turn/queue", () => {
      throw new Error("boom");
    });

    const onFailure = vi.fn();
    const { result } = renderHook(() => usePendingTurnEntries("ref_a", "queue"));

    let rejection: unknown;
    await act(async () => {
      try {
        await submitWithPendingTracking({ ref: "ref_a", method: "queue", text: "hello", onFailure }, () =>
          threadsStore.getState().queue("ref_a", "hello"),
        );
      } catch (err) {
        rejection = err;
      }
    });

    expect(rejection).toBeInstanceOf(Error);
    expect((rejection as Error).message).toBe("boom");
    expect(onFailure).toHaveBeenCalledTimes(1);
    // onFailure receives the SAME raw error object rethrown to the caller -
    // deliberately not a pre-formatted string, so a caller with
    // method-specific knowledge (e.g. drain's queuedDrainPartial split) can
    // inspect it - see SubmitWithPendingTrackingOptions's own doc comment.
    expect(onFailure).toHaveBeenCalledWith(rejection);
    expect(result.current).toHaveLength(0);
  });

  // Uses a synthetic `perform` (not a real threadsStore action) deliberately:
  // every real threadsStore action normalizes a non-Error rejection into an
  // Error itself (mapConflict, stores/threads.ts), so routing this scenario
  // through FakeClient/threadsStore the way every other test in this file
  // does would only prove mapConflict's own behavior, not this module's -
  // this isolates submitWithPendingTracking's own pass-through instead.
  test("a non-Error rejection from `perform` passes through to onFailure unchanged", async () => {
    const onFailure = vi.fn();

    await submitWithPendingTracking({ ref: "ref_a", method: "queue", text: "hello", onFailure }, () =>
      Promise.reject("raw string failure"),
    ).catch(() => {});

    expect(onFailure).toHaveBeenCalledWith("raw string failure");
  });
});

describe("reconciliation via the threads store (wire echoes)", () => {
  test("a matching thread/queueChanged notification reconciles a queue-method entry", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a");
    fake.on("turn/queue", () => ({}));
    const { result } = renderHook(() => usePendingTurnEntries("ref_a", "queue"));

    await act(async () => {
      await submitWithPendingTracking({ ref: "ref_a", method: "queue", text: "hello", onFailure: () => {} }, () =>
        threadsStore.getState().queue("ref_a", "hello"),
      );
    });
    expect(result.current).toHaveLength(1);

    act(() => {
      fake.emitNotification({
        method: "thread/queueChanged",
        params: { threadId: "thr_ref_a", ref: "ref_a", queue: { depth: 1, texts: ["hello"] } },
      });
    });

    expect(result.current).toHaveLength(0);
  });

  // pendingReconcile.test.ts already proves computeReconciledIds' own
  // multiset match resolves the FIRST-registered of two identical-text
  // entries when only one authoritative slot exists (a pure-function test
  // against a hand-built ThreadModel) - this is the missing store-level
  // companion, proving the SAME FIFO-by-registration-order behavior holds
  // through the real submitWithPendingTracking + threadsStore + wire-echo
  // pipeline, not just the pure algorithm in isolation.
  test("two queue entries with IDENTICAL text reconcile FIFO - the OLDER (first-registered) entry resolves before the newer duplicate", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a");
    fake.on("turn/queue", () => ({}));
    const { result } = renderHook(() => usePendingTurnEntries("ref_a", "queue"));

    await act(async () => {
      await submitWithPendingTracking({ ref: "ref_a", method: "queue", text: "same text", onFailure: () => {} }, () =>
        threadsStore.getState().queue("ref_a", "same text"),
      );
    });
    const olderId = result.current[0]!.id;

    await act(async () => {
      await submitWithPendingTracking({ ref: "ref_a", method: "queue", text: "same text", onFailure: () => {} }, () =>
        threadsStore.getState().queue("ref_a", "same text"),
      );
    });
    expect(result.current).toHaveLength(2);

    // Only ONE authoritative slot with this text - a multiset match, not
    // simple text equality, so this must resolve exactly one of the two
    // duplicate chips.
    act(() => {
      fake.emitNotification({
        method: "thread/queueChanged",
        params: { threadId: "thr_ref_a", ref: "ref_a", queue: { depth: 1, texts: ["same text"] } },
      });
    });

    expect(result.current).toHaveLength(1);
    expect(result.current[0]!.id).not.toBe(olderId); // the OLDER duplicate cleared first; the newer one remains
  });

  test("a new steering item (serf/steering/injected) reconciles a drain-method entry", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a");
    fake.on("turn/drainAsSteer", () => ({}));
    const { result } = renderHook(() => usePendingTurnEntries("ref_a", "drain"));

    await act(async () => {
      await submitWithPendingTracking(
        { ref: "ref_a", method: "drain", text: "composer text", onFailure: () => {} },
        () => threadsStore.getState().drainAsSteer("ref_a", "composer text"),
      );
    });
    expect(result.current).toHaveLength(1);

    act(() => {
      fake.emitNotification({
        method: "serf/steering/injected",
        params: { threadId: "thr_ref_a", ref: "ref_a", text: "merged queue + composer text" },
      });
    });

    expect(result.current).toHaveLength(0);
  });

  test("a new userMessage item (item/completed) reconciles a send-method entry", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a");
    fake.on("turn/start", () => ({ turn: { id: "turn_1", status: "inProgress", itemsView: "" } }));
    const { result } = renderHook(() => usePendingTurnEntries("ref_a", "send"));

    await act(async () => {
      await submitWithPendingTracking({ ref: "ref_a", method: "send", text: "hello there", onFailure: () => {} }, () =>
        threadsStore.getState().send("ref_a", "hello there"),
      );
    });
    expect(result.current).toHaveLength(1);

    act(() => {
      fake.emitNotification({
        method: "item/completed",
        params: {
          threadId: "thr_ref_a",
          ref: "ref_a",
          turnId: "turn_1",
          item: { id: "item_new", turnId: "turn_1", type: "userMessage", text: "hello there", status: "completed" },
        },
      });
    });

    expect(result.current).toHaveLength(0);
  });

  test("a notification for a different ref does not reconcile this ref's entry", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a");
    await hydrate(fake, "ref_b");
    fake.on("turn/queue", () => ({}));
    const { result } = renderHook(() => usePendingTurnEntries("ref_a", "queue"));

    await act(async () => {
      await submitWithPendingTracking({ ref: "ref_a", method: "queue", text: "hello", onFailure: () => {} }, () =>
        threadsStore.getState().queue("ref_a", "hello"),
      );
    });
    expect(result.current).toHaveLength(1);

    act(() => {
      fake.emitNotification({
        method: "thread/queueChanged",
        params: { threadId: "thr_ref_b", ref: "ref_b", queue: { depth: 1, texts: ["hello"] } },
      });
    });

    expect(result.current).toHaveLength(1); // untouched - the notification targeted ref_b, not ref_a
  });
});

describe("10s timeout reaper", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  test("an entry not reconciled within PENDING_TIMEOUT_MS auto-fails via onFailure and is removed", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a");
    fake.on("turn/queue", () => ({}));
    const onFailure = vi.fn();
    const { result } = renderHook(() => usePendingTurnEntries("ref_a", "queue"));

    await act(async () => {
      await submitWithPendingTracking({ ref: "ref_a", method: "queue", text: "hello", onFailure }, () =>
        threadsStore.getState().queue("ref_a", "hello"),
      );
    });
    expect(result.current).toHaveLength(1);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(PENDING_TIMEOUT_MS);
    });

    expect(result.current).toHaveLength(0);
    expect(onFailure).toHaveBeenCalledTimes(1);
    const [failure] = onFailure.mock.calls[0] as [unknown];
    expect(failure).toBeInstanceOf(Error);
    expect((failure as Error).message).toMatch(/didn't confirm/i);
  });

  test("reconciling before the timeout cancels the timer - no spurious onFailure after PENDING_TIMEOUT_MS", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a");
    fake.on("turn/queue", () => ({}));
    const onFailure = vi.fn();

    await act(async () => {
      await submitWithPendingTracking({ ref: "ref_a", method: "queue", text: "hello", onFailure }, () =>
        threadsStore.getState().queue("ref_a", "hello"),
      );
    });

    act(() => {
      fake.emitNotification({
        method: "thread/queueChanged",
        params: { threadId: "thr_ref_a", ref: "ref_a", queue: { depth: 1, texts: ["hello"] } },
      });
    });

    await act(async () => {
      await vi.advanceTimersByTimeAsync(PENDING_TIMEOUT_MS);
    });

    expect(onFailure).not.toHaveBeenCalled();
  });

  test("a send timeout clears awaiting state even when perform() remains unresolved", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a");
    let resolvePerform: (() => void) | undefined;
    const send = submitWithPendingTracking(
      { ref: "ref_a", method: "send", text: "hello", onFailure: () => {} },
      () =>
        new Promise<void>((resolve) => {
          resolvePerform = resolve;
        }),
    );
    const { result: awaitingResult } = renderHook(() => useAwaitingFirstFrameSend("ref_a"));
    const { result: pendingResult } = renderHook(() => usePendingTurnEntries("ref_a", "send"));

    expect(awaitingResult.current).toBe(true);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(PENDING_TIMEOUT_MS);
    });
    expect(pendingResult.current).toHaveLength(0);
    expect(awaitingResult.current).toBe(false);

    await act(async () => {
      resolvePerform?.();
      await send;
    });
    expect(awaitingResult.current).toBe(false);
  });
});

describe("awaiting first frame lifecycle cleanup", () => {
  test("model removal clears awaiting state and a later successful perform cannot restore it", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a");
    let resolvePerform: (() => void) | undefined;
    const send = submitWithPendingTracking(
      { ref: "ref_a", method: "send", text: "hello", onFailure: () => {} },
      () =>
        new Promise<void>((resolve) => {
          resolvePerform = resolve;
        }),
    );
    const { result } = renderHook(() => useAwaitingFirstFrameSend("ref_a"));
    expect(result.current).toBe(true);

    act(() => {
      threadsStore.getState().releaseThread("ref_a");
    });
    expect(result.current).toBe(false);

    await act(async () => {
      resolvePerform?.();
      await send;
    });
    expect(result.current).toBe(false);
  });

  test("awaiting state is scoped to each ref", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a");
    await hydrate(fake, "ref_b");
    let resolveA: (() => void) | undefined;
    let resolveB: (() => void) | undefined;
    const sendA = submitWithPendingTracking(
      { ref: "ref_a", method: "send", text: "a", onFailure: () => {} },
      () =>
        new Promise<void>((resolve) => {
          resolveA = resolve;
        }),
    );
    const sendB = submitWithPendingTracking(
      { ref: "ref_b", method: "send", text: "b", onFailure: () => {} },
      () =>
        new Promise<void>((resolve) => {
          resolveB = resolve;
        }),
    );
    const { result: aResult } = renderHook(() => useAwaitingFirstFrameSend("ref_a"));
    const { result: bResult } = renderHook(() => useAwaitingFirstFrameSend("ref_b"));
    expect(aResult.current).toBe(true);
    expect(bResult.current).toBe(true);

    act(() => {
      fake.emitNotification({
        method: "item/started",
        params: {
          threadId: "thr_ref_a",
          ref: "ref_a",
          turnId: "turn_1",
          item: { id: "agent_a", turnId: "turn_1", type: "agentMessage", status: "inProgress" },
        },
      });
    });
    expect(aResult.current).toBe(false);
    expect(bResult.current).toBe(true);

    await act(async () => {
      resolveA?.();
      resolveB?.();
      await Promise.all([sendA, sendB]);
    });
  });

  test("reset cleanup removes awaiting state before an unresolved perform settles", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a");
    let resolvePerform: (() => void) | undefined;
    const send = submitWithPendingTracking(
      { ref: "ref_a", method: "send", text: "hello", onFailure: () => {} },
      () =>
        new Promise<void>((resolve) => {
          resolvePerform = resolve;
        }),
    );
    const { result } = renderHook(() => useAwaitingFirstFrameSend("ref_a"));
    expect(result.current).toBe(true);

    act(() => {
      resetPendingTurnsStoreForTests();
    });
    expect(result.current).toBe(false);

    await act(async () => {
      resolvePerform?.();
      await send;
    });
    expect(result.current).toBe(false);
  });
});

describe("usePendingTurnEntries", () => {
  test("filters strictly to the given ref", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a");
    await hydrate(fake, "ref_b");
    fake.on("turn/queue", () => ({}));

    await act(async () => {
      await submitWithPendingTracking({ ref: "ref_a", method: "queue", text: "a", onFailure: () => {} }, () =>
        threadsStore.getState().queue("ref_a", "a"),
      );
      await submitWithPendingTracking({ ref: "ref_b", method: "queue", text: "b", onFailure: () => {} }, () =>
        threadsStore.getState().queue("ref_b", "b"),
      );
    });

    const { result: aResult } = renderHook(() => usePendingTurnEntries("ref_a"));
    const { result: bResult } = renderHook(() => usePendingTurnEntries("ref_b"));
    expect(aResult.current.map((e) => e.text)).toEqual(["a"]);
    expect(bResult.current.map((e) => e.text)).toEqual(["b"]);
  });

  test("omitting method returns entries of every method for that ref", async () => {
    const fake = connectFakeClient();
    await hydrate(fake, "ref_a");
    fake.on("turn/queue", () => ({}));
    fake.on("turn/steer", () => ({}));

    await act(async () => {
      await submitWithPendingTracking({ ref: "ref_a", method: "queue", text: "q", onFailure: () => {} }, () =>
        threadsStore.getState().queue("ref_a", "q"),
      );
      await submitWithPendingTracking({ ref: "ref_a", method: "steer", text: "s", onFailure: () => {} }, () =>
        threadsStore.getState().steer("ref_a", "s"),
      );
    });

    const { result } = renderHook(() => usePendingTurnEntries("ref_a"));
    expect(result.current.map((e) => e.method).sort()).toEqual(["queue", "steer"]);
  });
});
