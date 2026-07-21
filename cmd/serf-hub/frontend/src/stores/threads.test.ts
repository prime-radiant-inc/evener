import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { act, cleanup, renderHook } from "@testing-library/react";
import { FakeClient } from "../protocol/testing/fakeClient";
import { ConnectionClosedError, WireError } from "../protocol/errors";
import type { ConnectionState } from "../protocol/client";
import type { AnyNotification, Thread, ThreadCapabilities, ThreadReadResponse, ThreadTurnsListResponse } from "../protocol/types.gen";
import { connectionStore, useConnectionStore } from "./connection";
import {
  appendFrameTime,
  ConflictError,
  FRAME_TIMES_MAX_ENTRIES,
  FRAME_TIMES_WINDOW_MS,
  resetThreadsStoreForTests,
  threadsStore,
  useThreadsStore,
} from "./threads";

// flushUntil drains microtask turns until `done()` reports true (or a bounded
// number of turns elapse, so a genuine hang fails fast instead of silently).
// Same contract/name as protocol/client.test.ts's helper; duplicated here
// because the two test files share no test-utils module.
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

function readResponse(ref: string, overrides: Partial<Thread> = {}): ThreadReadResponse {
  return { thread: testThread(ref, overrides) };
}

// connectFakeClient wires a fresh FakeClient through useConnectionStore's
// locked connect(client) entry point — the same path threads.ts's
// requireClient() rides to reach the client (see connection.ts).
function connectFakeClient(state: ConnectionState = "ready"): FakeClient {
  const fake = new FakeClient(state);
  connectionStore.getState().connect(fake);
  return fake;
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("FakeClient", () => {
  test("request() rejects while not ready, mirroring AppwireClient's own ready-gate, without recording the call", async () => {
    const fake = new FakeClient("connecting");
    fake.on("thread/read", () => readResponse("ref_a"));

    await expect(fake.request("thread/read", { ref: "ref_a", includeTurns: true })).rejects.toThrow(
      /cannot call "thread\/read" while state is "connecting"/,
    );
    expect(fake.calls).toHaveLength(0); // never "sent" — the real client never reaches socket.send() in this case either
  });
});

describe("useConnectionStore", () => {
  test("connect captures the client's current state immediately", () => {
    const fake = new FakeClient("connecting");
    connectionStore.getState().connect(fake);
    expect(connectionStore.getState().state).toBe("connecting");
  });

  test("connect mirrors every subsequent client state change", () => {
    const fake = connectFakeClient("idle");
    fake.emitStateChange("connecting");
    expect(connectionStore.getState().state).toBe("connecting");
    fake.emitStateChange("ready");
    expect(connectionStore.getState().state).toBe("ready");
    fake.emitStateChange("reconnecting");
    expect(connectionStore.getState().state).toBe("reconnecting");
  });

  test("connect is idempotent: a second call with the same client does not double-subscribe", () => {
    const fake = connectFakeClient("idle");
    connectionStore.getState().connect(fake); // second call, same client instance

    const setStateSpy = vi.spyOn(connectionStore, "setState");
    fake.emitStateChange("connecting");
    expect(setStateSpy).toHaveBeenCalledTimes(1); // one onStateChange listener, not two
  });

  // AppwireClientLike DOES expose connect() (it resolves with the
  // InitializeResponse - see protocol/testing/fakeClient.ts) - this store's
  // own connect(client) just never calls it: it only mirrors
  // ConnectionState, so it stays safe to call before any handshake has even
  // started. AppShell.tsx is the caller that actually drives client.connect()
  // and sets serverInfo directly from its resolved value.
  test("serverInfo stays undefined: connect(client) only mirrors ConnectionState, it never calls the client's own connect()", () => {
    connectFakeClient();
    expect(connectionStore.getState().serverInfo).toBeUndefined();
  });

  test("hook reflects store state and updates on change", () => {
    const fake = connectFakeClient("idle");
    const { result } = renderHook(() => useConnectionStore((s) => s.state));
    expect(result.current).toBe("idle");

    act(() => {
      fake.emitStateChange("ready");
    });
    expect(result.current).toBe("ready");
  });
});

describe("useThreadsStore.ensureThread", () => {
  test("hydrates via thread/read and routes a subsequent matching notification through the reducer", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", (params) => {
      expect(params).toEqual({
        ref: "ref_a",
        includeTurns: true,
        itemsView: "full",
        subscribe: true,
        replaceSubscription: false,
        turnLimit: 40,
      });
      return readResponse("ref_a");
    });

    await threadsStore.getState().ensureThread("ref_a");

    const model = threadsStore.getState().threads.get("ref_a");
    expect(model?.threadId).toBe("thr_ref_a");

    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_ref_a", ref: "ref_a", status: { type: "active" } },
    } as AnyNotification);

    expect(threadsStore.getState().threads.get("ref_a")?.status).toEqual({ type: "active" });
  });

  test("a second ensureThread(ref) does not re-read", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));

    await threadsStore.getState().ensureThread("ref_a");
    await threadsStore.getState().ensureThread("ref_a");

    expect(fake.calls.filter((c) => c.method === "thread/read")).toHaveLength(1);
  });

  test("concurrent ensureThread(ref) calls share one thread/read", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));

    const p1 = threadsStore.getState().ensureThread("ref_a");
    const p2 = threadsStore.getState().ensureThread("ref_a");
    await Promise.all([p1, p2]);

    expect(fake.calls.filter((c) => c.method === "thread/read")).toHaveLength(1);
    expect(threadsStore.getState().threads.has("ref_a")).toBe(true);
  });

  test("throws when no client has been connected yet", async () => {
    await expect(threadsStore.getState().ensureThread("ref_a")).rejects.toThrow(/no client connected/i);
  });

  test("propagates a thread/read failure and leaves the ref untracked", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => {
      throw new Error("boom");
    });

    await expect(threadsStore.getState().ensureThread("ref_a")).rejects.toThrow("boom");
    expect(threadsStore.getState().threads.has("ref_a")).toBe(false);
  });

  test("a failed ensureThread rolls back its own refcount: retry-success + a single release fully untracks the ref", async () => {
    const fake = connectFakeClient();
    let shouldFail = true;
    let readCount = 0;
    fake.on("thread/read", () => {
      readCount += 1;
      if (shouldFail) throw new Error("boom");
      return readResponse("ref_a");
    });

    // Attempt 1 fails. Without rolling back, its refcount increment would
    // strand the ref permanently once attempt 2 succeeds and the caller
    // releases only once (the normal mount/retry/unmount lifecycle: one
    // logical pane, two ensureThread attempts, one eventual release).
    await expect(threadsStore.getState().ensureThread("ref_a")).rejects.toThrow("boom");
    expect(threadsStore.getState().threads.has("ref_a")).toBe(false);

    // Attempt 2 (the retry) succeeds.
    shouldFail = false;
    await threadsStore.getState().ensureThread("ref_a");
    expect(threadsStore.getState().threads.has("ref_a")).toBe(true);
    expect(readCount).toBe(2); // one failed read, one successful read — no stale inflight sharing across attempts

    // The single natural release must fully untrack it. releaseThread()
    // only removes the ref from `threads` on the branch where its refcount
    // was exactly 1 going in — so this passing is itself proof the failed
    // attempt's claim was rolled back rather than silently surviving
    // alongside the successful retry's claim.
    threadsStore.getState().releaseThread("ref_a");
    expect(threadsStore.getState().threads.has("ref_a")).toBe(false);

    // Further evidence refCounts (module-private, unreachable directly from
    // this test) reads back at true zero rather than negative or stale: a
    // brand new cycle behaves exactly as if the ref had never been touched
    // before — one fresh read, one release fully untracks it again.
    await threadsStore.getState().ensureThread("ref_a");
    expect(readCount).toBe(3);
    threadsStore.getState().releaseThread("ref_a");
    expect(threadsStore.getState().threads.has("ref_a")).toBe(false);
  });

  test("a ref released before its in-flight hydrate resolves is not resurrected", async () => {
    const fake = connectFakeClient();
    // A plain `let` reassigned only inside the executor closure below gets
    // narrowed to `never` at the use site by TS's control-flow analysis
    // (a variable mutated solely inside a nested function isn't tracked as
    // "possibly non-null" outside it); a boxed field sidesteps that.
    const box: { resolveRead: ((resp: ThreadReadResponse) => void) | null } = { resolveRead: null };
    fake.on("thread/read", () => new Promise<ThreadReadResponse>((resolve) => (box.resolveRead = resolve)));

    const ensuring = threadsStore.getState().ensureThread("ref_a");
    // request()'s handler invocation (which captures the resolver) is
    // deferred a microtask behind the synchronous call above; wait for it
    // before racing releaseThread() against the still-pending hydrate.
    await flushUntil(() => box.resolveRead !== null);
    threadsStore.getState().releaseThread("ref_a");
    box.resolveRead?.(readResponse("ref_a"));
    await ensuring;

    expect(threadsStore.getState().threads.has("ref_a")).toBe(false);
  });

  // Pre-Task-5, wiring was lazy (attached inside requireClient(), the first
  // time some store action ran) - so this test used to connect the client
  // FIRST, attach spies second, and prove idempotency only across
  // subsequent action calls. Wiring is now reactive to connectionStore's
  // own client reference (see rewireClient/connectionStore.subscribe in
  // threads.ts) precisely so an already-open pane keeps receiving deltas
  // through a manual-retry client swap with no action call required at
  // all - so the spies must be attached BEFORE connect() to observe that,
  // and the test asserts wiring happens exactly once right there, staying
  // at exactly once through every action call that follows.
  test("wires onNotification/onReady on the client exactly once, at connect time - not per store action", async () => {
    const fake = new FakeClient();
    const onNotificationSpy = vi.spyOn(fake, "onNotification");
    const onReadySpy = vi.spyOn(fake, "onReady");
    fake.on("thread/read", (params) => readResponse((params as { ref: string }).ref));

    connectionStore.getState().connect(fake);
    expect(onNotificationSpy).toHaveBeenCalledTimes(1);
    expect(onReadySpy).toHaveBeenCalledTimes(1);

    await threadsStore.getState().ensureThread("ref_a");
    await threadsStore.getState().ensureThread("ref_b");
    await threadsStore.getState().send("ref_a", "hi").catch(() => {}); // no turn/start handler scripted; rejection irrelevant here

    expect(onNotificationSpy).toHaveBeenCalledTimes(1);
    expect(onReadySpy).toHaveBeenCalledTimes(1);
  });
});

describe("useThreadsStore.releaseThread", () => {
  test("refcounts panes; stops tracking only when the last pane releases", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));
    await threadsStore.getState().ensureThread("ref_a"); // pane 1
    await threadsStore.getState().ensureThread("ref_a"); // pane 2 (refcount 2, no re-read)

    threadsStore.getState().releaseThread("ref_a"); // pane 1 leaves
    expect(threadsStore.getState().threads.has("ref_a")).toBe(true); // pane 2 still open

    threadsStore.getState().releaseThread("ref_a"); // pane 2 leaves
    expect(threadsStore.getState().threads.has("ref_a")).toBe(false);
  });

  test("releasing an untracked ref is a harmless no-op", () => {
    expect(() => threadsStore.getState().releaseThread("never_tracked")).not.toThrow();
  });
});

describe("notification routing", () => {
  test("notifications for a never-tracked ref are dropped (same map reference preserved)", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_tracked"));
    await threadsStore.getState().ensureThread("ref_tracked");

    const before = threadsStore.getState().threads;
    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_untracked", ref: "ref_untracked", status: { type: "active" } },
    } as AnyNotification);

    expect(threadsStore.getState().threads).toBe(before);
  });

  test("turn/completed is delivered only to the model whose activeTurnId matches (sibling immunity)", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", (params) => {
      const ref = (params as { ref: string }).ref;
      if (ref === "ref_a") {
        return readResponse("ref_a", {
          turns: [{ id: "turn_1", status: "inProgress", itemsView: "" }],
          serf: { ref: "ref_a", capabilities: CAPABILITIES, queue: {}, activeTurnId: "turn_1" },
        });
      }
      return readResponse("ref_b", {
        turns: [
          {
            id: "turn_1",
            status: "completed",
            itemsView: "full",
            items: [{ type: "agentMessage", id: "item_b1", turnId: "turn_1", text: "B's own answer", status: "completed" }],
          },
        ],
      });
    });

    await threadsStore.getState().ensureThread("ref_a");
    await threadsStore.getState().ensureThread("ref_b");
    expect(threadsStore.getState().threads.get("ref_a")?.activeTurnId).toBe("turn_1");
    expect(threadsStore.getState().threads.get("ref_b")?.activeTurnId).toBeUndefined();

    const beforeB = threadsStore.getState().threads.get("ref_b");

    // Stream A's own item BEFORE settling — wire-true: the real
    // turn/completed is a bare status/timing stamp with no items (every live
    // settle site in internal/appprojector/appwire_projection.go emits
    // Turn{ID,Status[,Error]} with Items nil, ItemsView "" — see
    // protocol/reducer.ts's "turn/completed" case), so A's item must already
    // be in the model via item/started + item/completed, not smuggled in
    // through the settle payload.
    fake.emitNotification({
      method: "item/started",
      params: { threadId: "thr_ref_a", ref: "ref_a", turnId: "turn_1", item: { type: "agentMessage", id: "item_a1", turnId: "turn_1", status: "inProgress" } },
    } as AnyNotification);
    fake.emitNotification({
      method: "item/completed",
      params: { threadId: "thr_ref_a", ref: "ref_a", turnId: "turn_1", item: { type: "agentMessage", id: "item_a1", turnId: "turn_1", text: "A's answer", status: "completed" } },
    } as AnyNotification);

    fake.emitNotification({
      method: "turn/completed",
      params: { turnId: "turn_1", turn: { id: "turn_1", status: "completed", itemsView: "" } },
    } as AnyNotification);

    // The rightful owner (A, whose activeTurnId matched) settles...
    const modelA = threadsStore.getState().threads.get("ref_a");
    expect(modelA?.activeTurnId).toBeUndefined();
    expect(modelA?.turns[0]?.items[0]?.text).toBe("A's answer");

    // ...while B, sharing the same numbered turn_1 but never active, is a
    // same-reference no-op: the collision never crosses threads.
    expect(threadsStore.getState().threads.get("ref_b")).toBe(beforeB);
  });
});

describe("reconnect resubscribe", () => {
  test("onReady refire re-subscribes every tracked ref additively and replaces each model wholesale", async () => {
    const fake = connectFakeClient();
    let readCount = 0;
    fake.on("thread/read", (params) => {
      readCount += 1;
      const ref = (params as { ref: string }).ref;
      // The first pass (ensureThread) hydrates one turn each; the
      // post-reconnect pass returns an empty turns list. If the store merged
      // instead of replacing, the old turn would still be there.
      const turns = readCount <= 2 ? [{ id: "turn_1", status: "completed", itemsView: "full", items: [] }] : [];
      return readResponse(ref, { turns });
    });

    await threadsStore.getState().ensureThread("ref_a");
    await threadsStore.getState().ensureThread("ref_b");
    expect(threadsStore.getState().threads.get("ref_a")?.turns).toHaveLength(1);
    expect(threadsStore.getState().threads.get("ref_b")?.turns).toHaveLength(1);

    // FakeClient defaults to "ready"; emitReady() alone would no-op (same
    // early-return AppwireClient.setState has for a same-state transition),
    // so a genuine reconnect needs the intermediate drop first.
    fake.emitStateChange("reconnecting");
    fake.emitReady(); // simulated reconnect

    await flushUntil(
      () => threadsStore.getState().threads.get("ref_a")?.turns.length === 0 && threadsStore.getState().threads.get("ref_b")?.turns.length === 0,
    );

    const readCallsAfterReconnect = fake.calls.filter((c) => c.method === "thread/read").slice(2);
    expect(readCallsAfterReconnect).toHaveLength(2); // every tracked ref re-subscribed, nothing else

    const forA = readCallsAfterReconnect.find((c) => (c.params as { ref: string }).ref === "ref_a");
    const forB = readCallsAfterReconnect.find((c) => (c.params as { ref: string }).ref === "ref_b");
    const expectedParams = (ref: string) => ({ ref, includeTurns: true, itemsView: "full", subscribe: true, replaceSubscription: false, turnLimit: 40 });
    expect(forA?.params).toEqual(expectedParams("ref_a"));
    expect(forB?.params).toEqual(expectedParams("ref_b"));
  });

  test("a released ref is not re-subscribed on the next onReady", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", (params) => readResponse((params as { ref: string }).ref));
    await threadsStore.getState().ensureThread("ref_a");
    await threadsStore.getState().ensureThread("ref_b");
    threadsStore.getState().releaseThread("ref_a"); // only ref_b remains tracked

    fake.emitStateChange("reconnecting");
    fake.emitReady(); // simulated reconnect — must still re-subscribe ref_b for this test to mean anything
    await flushUntil(() => fake.calls.filter((c) => c.method === "thread/read").length > 2);

    const refsReRead = fake.calls
      .filter((c) => c.method === "thread/read")
      .slice(2)
      .map((c) => (c.params as { ref: string }).ref);
    expect(refsReRead).toEqual(["ref_b"]); // ref_a was released before the reconnect; only ref_b re-subscribes
  });
});

describe("client swap (manual retry) rewiring", () => {
  // The trap this covers (see docs/superpowers/plans/
  // 2026-07-20-webui-rewrite-wave3-shell.md, Task 5): before this
  // describe block existed, wiredClient was only ever updated lazily,
  // inside requireClient(), the first time some ACTION (ensureThread/send/
  // steer/queue/interrupt) ran. Swapping connectionStore's client
  // reference alone (shell/ConnectionBanner.tsx's retry, which mints a
  // FRESH AppwireClient rather than reusing the dead one) left
  // onNotification/onReady still attached to the dead client - the banner
  // would report "ready" while every open pane silently stopped receiving
  // deltas, since nothing forces a store action to run just because the
  // connection recovered. This test drives the swap through NOTHING but
  // connectionStore.getState().connect(b) - no ensureThread/send/etc call
  // follows it - so it only passes if the rewiring is reactive to the
  // client reference itself, not piggybacked on some later action.
  test("swapping to a fresh client re-hydrates tracked refs, routes its notifications, and detaches the dead client's handlers (no double delivery)", async () => {
    const a = connectFakeClient();
    a.on("thread/read", () => readResponse("ref_a", { turns: [{ id: "turn_1", status: "completed", itemsView: "full", items: [] }] }));
    await threadsStore.getState().ensureThread("ref_a");
    expect(threadsStore.getState().threads.get("ref_a")?.turns).toHaveLength(1);

    // Kill A for good (a terminal drop, not a same-client reconnect - see
    // the "reconnect resubscribe" tests above for that separate case).
    a.emitStateChange("closed");

    // The manual retry: a FRESH client, already "ready" by the time it's
    // handed to connectionStore.connect() - mirrors the real sequence
    // ConnectionBanner's retry follows (construct, await connect(), THEN
    // wire the store), not an in-flight handshake. Its own thread/read
    // response deliberately differs from A's (empty turns, not one) so a
    // passing assertion below can only mean the re-hydrate actually ran
    // against B, not a stale snapshot left over from A.
    const b = new FakeClient("ready");
    b.on("thread/read", () => readResponse("ref_a", { turns: [] }));
    connectionStore.getState().connect(b);

    // Re-hydration is async (a thread/read round trip against B) with
    // nothing else in this test driving it forward.
    await flushUntil(() => threadsStore.getState().threads.get("ref_a")?.turns.length === 0);
    expect(threadsStore.getState().threads.get("ref_a")?.turns).toHaveLength(0);

    // B's own live notification reaches the tracked model...
    b.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_ref_a", ref: "ref_a", status: { type: "active" } },
    } as AnyNotification);
    expect(threadsStore.getState().threads.get("ref_a")?.status).toEqual({ type: "active" });

    // ...while A's handlers were detached at the swap: the same
    // notification shape, injected via the now-dead client, must NOT be
    // delivered - proof of no lingering double-subscription, not just
    // that B independently works.
    a.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_ref_a", ref: "ref_a", status: { type: "idle" } },
    } as AnyNotification);
    expect(threadsStore.getState().threads.get("ref_a")?.status).toEqual({ type: "active" }); // unchanged - A's emit was a no-op
  });

  test("swapping to a fresh client that is not yet ready waits for its own onReady, same as the initial connection", async () => {
    const a = connectFakeClient();
    a.on("thread/read", () => readResponse("ref_a"));
    await threadsStore.getState().ensureThread("ref_a");

    a.emitStateChange("closed");

    const b = new FakeClient("connecting"); // still mid-handshake when wired in
    b.on("thread/read", () => readResponse("ref_a"));
    connectionStore.getState().connect(b);

    // b isn't ready yet, so no eager hydrate should have fired against it -
    // asserted directly on b's own call log (not on `threads` content,
    // which can't distinguish "not re-hydrated yet" from "re-hydrated to
    // an identical snapshot").
    await Promise.resolve(); // let any (wrongly) eager work settle before asserting it didn't happen
    expect(b.calls.filter((c) => c.method === "thread/read")).toHaveLength(0);

    b.emitReady(); // completes B's own handshake
    await flushUntil(() => b.calls.filter((c) => c.method === "thread/read").length > 0);

    expect(b.calls.filter((c) => c.method === "thread/read")).toHaveLength(1);
  });
});

describe("useThreadsStore.send", () => {
  test("calls turn/start with text and image input", async () => {
    const fake = connectFakeClient();
    fake.on("turn/start", () => ({ turn: { id: "turn_1", status: "inProgress", itemsView: "" } }));

    await threadsStore.getState().send("ref_a", "hello", ["https://example.com/pic.png"]);

    const call = fake.calls.find((c) => c.method === "turn/start");
    expect(call?.params).toEqual({
      ref: "ref_a",
      input: [
        { type: "text", text: "hello" },
        { type: "image", url: "https://example.com/pic.png" },
      ],
    });
  });

  test("maps a Conflict wire rejection (serfErrorInfo === conflict) to ConflictError", async () => {
    const fake = connectFakeClient();
    // Mirrors appwire.Conflict() (appwire/errors.go): code -32013,
    // data.serfErrorInfo "conflict" — the shape server/appwire_runtime.go's
    // handleAppTurnStart returns when the turn CAS is lost (input buffer full).
    fake.on("turn/start", () => {
      throw new WireError("input buffer full", -32013, { serfErrorInfo: "conflict" });
    });

    const rejection = threadsStore.getState().send("ref_a", "hello");
    await expect(rejection).rejects.toBeInstanceOf(ConflictError);
    await expect(rejection).rejects.toThrow("input buffer full");
  });

  test("does not map a same-code, different-serfErrorInfo WireError to ConflictError", async () => {
    const fake = connectFakeClient();
    // Same wire code (-32013 / CodeConflict) as Conflict(), but a different
    // serfErrorInfo (appwire.QueuedDrainPartial) — the discriminator must be
    // the serfErrorInfo string, not the numeric code alone.
    fake.on("turn/start", () => {
      throw new WireError("queue drained partially", -32013, { serfErrorInfo: "queuedDrainPartial" });
    });

    const rejection = threadsStore.getState().send("ref_a", "hello");
    await expect(rejection).rejects.not.toBeInstanceOf(ConflictError);
    await expect(rejection).rejects.toBeInstanceOf(WireError);
  });

  test("propagates ConnectionClosedError unchanged (terminal, never mapped to ConflictError)", async () => {
    const fake = connectFakeClient();
    fake.on("turn/start", () => {
      throw new ConnectionClosedError("AppwireClient: closed");
    });

    const rejection = threadsStore.getState().send("ref_a", "hello");
    await expect(rejection).rejects.toBeInstanceOf(ConnectionClosedError);
    await expect(rejection).rejects.not.toBeInstanceOf(ConflictError);
  });
});

describe("useThreadsStore.steer / queue / interrupt", () => {
  async function ensureActiveTurn(fake: FakeClient, ref: string): Promise<void> {
    fake.on("thread/read", (params) =>
      readResponse((params as { ref: string }).ref, {
        turns: [{ id: "turn_1", status: "inProgress", itemsView: "" }],
        serf: { ref: (params as { ref: string }).ref, capabilities: CAPABILITIES, queue: {}, activeTurnId: "turn_1" },
      }),
    );
    await threadsStore.getState().ensureThread(ref);
  }

  test("steer sends the tracked model's activeTurnId as expectedTurnId", async () => {
    const fake = connectFakeClient();
    await ensureActiveTurn(fake, "ref_a");
    fake.on("turn/steer", () => ({}));

    await threadsStore.getState().steer("ref_a", "steer text");

    const call = fake.calls.find((c) => c.method === "turn/steer");
    expect(call?.params).toEqual({ ref: "ref_a", expectedTurnId: "turn_1", input: [{ type: "text", text: "steer text" }] });
  });

  test("interrupt sends the tracked model's activeTurnId as expectedTurnId", async () => {
    const fake = connectFakeClient();
    await ensureActiveTurn(fake, "ref_a");
    fake.on("turn/interrupt", () => ({}));

    await threadsStore.getState().interrupt("ref_a");

    const call = fake.calls.find((c) => c.method === "turn/interrupt");
    expect(call?.params).toEqual({ ref: "ref_a", expectedTurnId: "turn_1" });
  });

  test("queue sends turn/queue with text input and no expectedTurnId", async () => {
    const fake = connectFakeClient();
    fake.on("turn/queue", () => ({}));

    await threadsStore.getState().queue("ref_a", "queued text");

    const call = fake.calls.find((c) => c.method === "turn/queue");
    expect(call?.params).toEqual({ ref: "ref_a", input: [{ type: "text", text: "queued text" }] });
  });

  test("steer/queue/interrupt also map a Conflict rejection to ConflictError", async () => {
    const fake = connectFakeClient();
    fake.on("turn/steer", () => {
      throw new WireError("turn is not active", -32013, { serfErrorInfo: "conflict" });
    });
    fake.on("turn/queue", () => {
      throw new WireError("no active turn to queue against", -32013, { serfErrorInfo: "conflict" });
    });
    fake.on("turn/interrupt", () => {
      throw new WireError("turn is not active", -32013, { serfErrorInfo: "conflict" });
    });

    await expect(threadsStore.getState().steer("ref_a", "x")).rejects.toBeInstanceOf(ConflictError);
    await expect(threadsStore.getState().queue("ref_a", "x")).rejects.toBeInstanceOf(ConflictError);
    await expect(threadsStore.getState().interrupt("ref_a")).rejects.toBeInstanceOf(ConflictError);
  });
});

describe("useThreadsStore hook", () => {
  test("reflects store state and updates when the tracked threads map changes", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));

    const { result } = renderHook(() => useThreadsStore((s) => s.threads.has("ref_a")));
    expect(result.current).toBe(false);

    await act(async () => {
      await threadsStore.getState().ensureThread("ref_a");
    });

    expect(result.current).toBe(true);
  });
});

// appendFrameTime is the pure ring-buffer step behind the frameTimes
// tracking below: expiry (evict anything older than the 60s trace window
// widgets/cadence's own Cadence renders) and the 64-entry cap are both unit
// tested directly here, with no client/store machinery involved.
describe("appendFrameTime", () => {
  test("appends to an empty ring", () => {
    expect(appendFrameTime([], 1000)).toEqual([1000]);
  });

  test("appends after existing entries, preserving order", () => {
    expect(appendFrameTime([100, 200], 300)).toEqual([100, 200, 300]);
  });

  test("keeps an entry exactly at the 60s window boundary (matches Cadence's own age>WINDOW_MS exclusion)", () => {
    const now = 100_000;
    expect(appendFrameTime([now - FRAME_TIMES_WINDOW_MS], now)).toEqual([now - FRAME_TIMES_WINDOW_MS, now]);
  });

  test("evicts entries older than the 60s window relative to now", () => {
    const now = 100_000;
    const times = [now - FRAME_TIMES_WINDOW_MS - 1, now - 60_000, now - 1000];
    expect(appendFrameTime(times, now)).toEqual([now - 60_000, now - 1000, now]);
  });

  test("caps the ring at 64 entries, dropping the oldest to make room", () => {
    const now = 100_000;
    // 64 entries, all well within the window, oldest first.
    const times = Array.from({ length: FRAME_TIMES_MAX_ENTRIES }, (_, i) => now - (FRAME_TIMES_MAX_ENTRIES - i));
    const result = appendFrameTime(times, now);
    expect(result).toHaveLength(FRAME_TIMES_MAX_ENTRIES);
    expect(result[0]).toBe(times[1]); // the single oldest entry was evicted
    expect(result[result.length - 1]).toBe(now); // the new entry always survives
  });

  test("does not cap below 64 entries", () => {
    expect(appendFrameTime([1, 2, 3], 4)).toEqual([1, 2, 3, 4]);
  });

  test("unsorted input is accepted as-is (no re-sort) - Cadence's own contract already tolerates that", () => {
    expect(appendFrameTime([300, 100, 200], 400)).toEqual([300, 100, 200, 400]);
  });
});

describe("frameTimes tracking (threads store)", () => {
  test("starts with no entry for a freshly-hydrated ref - only live notifications populate it", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));
    await threadsStore.getState().ensureThread("ref_a");

    expect(threadsStore.getState().frameTimes.get("ref_a")).toBeUndefined();
  });

  test("appends the notification's own timestamp on every applied notification - not a fresh Date.now() read", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));
    await threadsStore.getState().ensureThread("ref_a");

    vi.spyOn(Date, "now").mockReturnValue(5_000_000);
    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_ref_a", ref: "ref_a", status: { type: "active" } },
    } as AnyNotification);

    expect(threadsStore.getState().frameTimes.get("ref_a")).toEqual([5_000_000]);
  });

  test("accumulates across multiple applied notifications, in arrival order", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));
    await threadsStore.getState().ensureThread("ref_a");

    const dateNowSpy = vi.spyOn(Date, "now");
    for (const t of [1000, 2000, 3000]) {
      dateNowSpy.mockReturnValue(t);
      fake.emitNotification({
        method: "thread/status/changed",
        params: { threadId: "thr_ref_a", ref: "ref_a", status: { type: "active" } },
      } as AnyNotification);
    }

    expect(threadsStore.getState().frameTimes.get("ref_a")).toEqual([1000, 2000, 3000]);
  });

  test("a notification for an untracked ref creates no frameTimes entry", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_tracked"));
    await threadsStore.getState().ensureThread("ref_tracked");

    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_untracked", ref: "ref_untracked", status: { type: "active" } },
    } as AnyNotification);

    expect(threadsStore.getState().frameTimes.has("ref_untracked")).toBe(false);
  });

  test("a matched notification the reducer treats as a same-reference no-op does not append a frame time", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));
    await threadsStore.getState().ensureThread("ref_a");

    // An unrecognized method still carries a matching ref (passes
    // notificationTargetsThread) but falls through the reducer's `default:`
    // case, which returns the exact same model reference - handleNotification
    // must not treat that as "applied" for frameTimes purposes either.
    fake.emitNotification({ method: "totally/unknown", params: { ref: "ref_a" } } as unknown as AnyNotification);

    expect(threadsStore.getState().frameTimes.get("ref_a")).toBeUndefined();
  });

  test("releaseThread drops the ref's frameTimes entry along with its model", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));
    await threadsStore.getState().ensureThread("ref_a");
    vi.spyOn(Date, "now").mockReturnValue(1000);
    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_ref_a", ref: "ref_a", status: { type: "active" } },
    } as AnyNotification);
    expect(threadsStore.getState().frameTimes.get("ref_a")).toEqual([1000]);

    threadsStore.getState().releaseThread("ref_a");

    expect(threadsStore.getState().frameTimes.has("ref_a")).toBe(false);
  });

  test("a reconnect re-hydrate does not reset or otherwise touch existing frameTimes", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));
    await threadsStore.getState().ensureThread("ref_a");
    vi.spyOn(Date, "now").mockReturnValue(1000);
    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_ref_a", ref: "ref_a", status: { type: "active" } },
    } as AnyNotification);
    expect(threadsStore.getState().frameTimes.get("ref_a")).toEqual([1000]);

    fake.emitStateChange("reconnecting");
    fake.emitReady();
    await flushUntil(() => fake.calls.filter((c) => c.method === "thread/read").length > 1);

    expect(threadsStore.getState().frameTimes.get("ref_a")).toEqual([1000]);
  });
});

// watchThread is the sanctioned narrow extension the transcript/tools
// stream (subagent-module watched-child rows) added to this store: an
// ADDITIVE, leaner (includeTurns:false) subscription to a child thread,
// refcounted independently of ensureThread's own counter so a real pane
// and a watching subagent row never fight over the same lifecycle - and
// stored in its own watchedThreads/watchedFrameTimes fields so releasing
// a watch can never touch a ref's "real pane" data (or vice versa), even
// when the exact same ref happens to be both ensureThread'd and
// watchThread'd at once.
describe("useThreadsStore.watchThread", () => {
  test("hydrates via thread/read with includeTurns:false and routes a subsequent matching notification", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", (params) => {
      expect(params).toEqual({
        ref: "ref_a",
        includeTurns: false,
        itemsView: "full",
        subscribe: true,
        replaceSubscription: false,
        turnLimit: 40,
      });
      return readResponse("ref_a");
    });

    await threadsStore.getState().watchThread("ref_a");

    const model = threadsStore.getState().watchedThreads.get("ref_a");
    expect(model?.threadId).toBe("thr_ref_a");

    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_ref_a", ref: "ref_a", status: { type: "active" } },
    } as AnyNotification);

    expect(threadsStore.getState().watchedThreads.get("ref_a")?.status).toEqual({ type: "active" });
  });

  test("a second watchThread(ref) does not re-read", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));

    await threadsStore.getState().watchThread("ref_a");
    await threadsStore.getState().watchThread("ref_a");

    expect(fake.calls.filter((c) => c.method === "thread/read")).toHaveLength(1);
  });

  test("throws when no client has been connected yet", async () => {
    await expect(threadsStore.getState().watchThread("ref_a")).rejects.toThrow(/no client connected/i);
  });

  test("propagates a thread/read failure and leaves the ref untracked", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => {
      throw new Error("boom");
    });

    await expect(threadsStore.getState().watchThread("ref_a")).rejects.toThrow("boom");
    expect(threadsStore.getState().watchedThreads.has("ref_a")).toBe(false);
  });

  test("watchThread and ensureThread are refcounted independently - releasing one never affects the other's tracking", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));

    await threadsStore.getState().ensureThread("ref_a");
    await threadsStore.getState().watchThread("ref_a");
    expect(threadsStore.getState().threads.has("ref_a")).toBe(true);
    expect(threadsStore.getState().watchedThreads.has("ref_a")).toBe(true);

    threadsStore.getState().releaseThread("ref_a");
    expect(threadsStore.getState().threads.has("ref_a")).toBe(false);
    expect(threadsStore.getState().watchedThreads.has("ref_a")).toBe(true); // the watch survives

    threadsStore.getState().releaseWatchedThread("ref_a");
    expect(threadsStore.getState().watchedThreads.has("ref_a")).toBe(false);
  });

  test("frameTimes for a watched ref land in watchedFrameTimes, never the real-pane frameTimes map", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));
    await threadsStore.getState().watchThread("ref_a");

    vi.spyOn(Date, "now").mockReturnValue(4_000_000);
    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_ref_a", ref: "ref_a", status: { type: "active" } },
    } as AnyNotification);

    expect(threadsStore.getState().watchedFrameTimes.get("ref_a")).toEqual([4_000_000]);
    expect(threadsStore.getState().frameTimes.get("ref_a")).toBeUndefined();
  });

  test("a notification is delivered to BOTH a real pane and a watch on the same ref, but frameTimes is appended once per map, not doubled into either", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));
    await threadsStore.getState().ensureThread("ref_a");
    await threadsStore.getState().watchThread("ref_a");

    vi.spyOn(Date, "now").mockReturnValue(9_000_000);
    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_ref_a", ref: "ref_a", status: { type: "active" } },
    } as AnyNotification);

    expect(threadsStore.getState().threads.get("ref_a")?.status).toEqual({ type: "active" });
    expect(threadsStore.getState().watchedThreads.get("ref_a")?.status).toEqual({ type: "active" });
    expect(threadsStore.getState().frameTimes.get("ref_a")).toEqual([9_000_000]);
    expect(threadsStore.getState().watchedFrameTimes.get("ref_a")).toEqual([9_000_000]);
  });

  test("releaseWatchedThread refcounts watchers; stops tracking only when the last watcher releases", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));
    await threadsStore.getState().watchThread("ref_a");
    await threadsStore.getState().watchThread("ref_a");

    threadsStore.getState().releaseWatchedThread("ref_a");
    expect(threadsStore.getState().watchedThreads.has("ref_a")).toBe(true);

    threadsStore.getState().releaseWatchedThread("ref_a");
    expect(threadsStore.getState().watchedThreads.has("ref_a")).toBe(false);
  });

  test("releasing an untracked watch is a harmless no-op", () => {
    expect(() => threadsStore.getState().releaseWatchedThread("never_watched")).not.toThrow();
  });

  test("a watched ref is not re-subscribed on reconnect once released, same as a real pane", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", (params) => readResponse((params as { ref: string }).ref));
    await threadsStore.getState().watchThread("ref_a");
    threadsStore.getState().releaseWatchedThread("ref_a");

    fake.emitStateChange("reconnecting");
    fake.emitReady();
    await flushUntil(() => fake.calls.filter((c) => c.method === "thread/read").length > 1);

    const refsReRead = fake.calls
      .filter((c) => c.method === "thread/read")
      .slice(1)
      .map((c) => (c.params as { ref: string }).ref);
    expect(refsReRead).toEqual([]);
  });

  test("onReady re-subscribes every tracked watch additively, replacing its model wholesale, independent of ensureThread's own refs", async () => {
    const fake = connectFakeClient();
    let readCount = 0;
    fake.on("thread/read", (params) => {
      readCount += 1;
      const ref = (params as { ref: string }).ref;
      const turns = readCount <= 1 ? [{ id: "turn_1", status: "completed", itemsView: "full", items: [] }] : [];
      return readResponse(ref, { turns });
    });

    await threadsStore.getState().watchThread("ref_a");
    expect(threadsStore.getState().watchedThreads.get("ref_a")?.turns).toHaveLength(1);

    fake.emitStateChange("reconnecting");
    fake.emitReady();
    await flushUntil(() => threadsStore.getState().watchedThreads.get("ref_a")?.turns.length === 0);

    const readCallsAfterReconnect = fake.calls.filter((c) => c.method === "thread/read").slice(1);
    expect(readCallsAfterReconnect).toHaveLength(1);
    expect(readCallsAfterReconnect[0]?.params).toEqual({
      ref: "ref_a",
      includeTurns: false,
      itemsView: "full",
      subscribe: true,
      replaceSubscription: false,
      turnLimit: 40,
    });
  });

  test("a watch released before its in-flight hydrate resolves is not resurrected", async () => {
    const fake = connectFakeClient();
    const box: { resolveRead: ((resp: ThreadReadResponse) => void) | null } = { resolveRead: null };
    fake.on("thread/read", () => new Promise<ThreadReadResponse>((resolve) => (box.resolveRead = resolve)));

    const watching = threadsStore.getState().watchThread("ref_a");
    await flushUntil(() => box.resolveRead !== null);
    threadsStore.getState().releaseWatchedThread("ref_a");
    box.resolveRead?.(readResponse("ref_a"));
    await watching;

    expect(threadsStore.getState().watchedThreads.has("ref_a")).toBe(false);
  });
});

describe("useThreadsStore.loadOlderTurns", () => {
  test("fetches the older page via thread/turns/list using the model's olderCursor, prepends it, and advances the cursor", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => ({
      thread: testThread("ref_a", { turns: [{ id: "turn_2", status: "completed", itemsView: "full", items: [] }] }),
      olderCursor: "cursor_1",
    }));
    fake.on("thread/turns/list", (params) => {
      expect(params).toEqual({ ref: "ref_a", cursor: "cursor_1", itemsView: "full", limit: 30 });
      return {
        data: [{ id: "turn_1", status: "completed", itemsView: "full", items: [] }],
        nextCursor: "cursor_0",
      };
    });
    await threadsStore.getState().ensureThread("ref_a");

    await threadsStore.getState().loadOlderTurns("ref_a");

    const model = threadsStore.getState().threads.get("ref_a");
    expect(model?.turns.map((t) => t.id)).toEqual(["turn_1", "turn_2"]);
    expect(model?.olderCursor).toBe("cursor_0");
  });

  test("is a no-op when the tracked model has no olderCursor (nothing more to load)", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a")); // no olderCursor override -> undefined
    await threadsStore.getState().ensureThread("ref_a");

    await threadsStore.getState().loadOlderTurns("ref_a");

    expect(fake.calls.filter((c) => c.method === "thread/turns/list")).toHaveLength(0);
  });

  test("is a no-op when the ref is not tracked at all", async () => {
    const fake = connectFakeClient();

    await threadsStore.getState().loadOlderTurns("ref_never_tracked");

    expect(fake.calls.filter((c) => c.method === "thread/turns/list")).toHaveLength(0);
  });

  test("throws when no client has been connected yet, same as every other action", async () => {
    await expect(threadsStore.getState().loadOlderTurns("ref_a")).rejects.toThrow(/no client connected/i);
  });

  test("a ref released while the older-turns request is in flight is not resurrected", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => ({ thread: testThread("ref_a"), olderCursor: "cursor_1" }));
    await threadsStore.getState().ensureThread("ref_a");

    const box: { resolve: ((r: ThreadTurnsListResponse) => void) | null } = { resolve: null };
    fake.on("thread/turns/list", () => new Promise<ThreadTurnsListResponse>((resolve) => (box.resolve = resolve)));

    const loading = threadsStore.getState().loadOlderTurns("ref_a");
    await flushUntil(() => box.resolve !== null);
    threadsStore.getState().releaseThread("ref_a");
    box.resolve?.({ data: [], nextCursor: undefined });
    await loading;

    expect(threadsStore.getState().threads.has("ref_a")).toBe(false);
  });
});
