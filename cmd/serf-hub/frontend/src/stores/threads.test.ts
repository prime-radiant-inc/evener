import { act, cleanup, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import {
  resetSubagentModuleStoreForTests,
  turnScopeKey,
  updateSubagentRowIfExists,
  upsertSubagentRow,
  useSubagentRows,
} from "../panes/session/transcript/tools/subagentModuleStore";
import type { ConnectionState } from "../protocol/client";
import { ConnectionClosedError, WireError } from "../protocol/errors";
import { FakeClient } from "../protocol/testing/fakeClient";
import type {
  ModelListResponse,
  Thread,
  ThreadCapabilities,
  ThreadReadResponse,
  ThreadTurnsListResponse,
} from "../protocol/types.gen";
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
    });

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
    await threadsStore
      .getState()
      .send("ref_a", "hi")
      .catch(() => {}); // no turn/start handler scripted; rejection irrelevant here

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
    });

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
            items: [
              { type: "agentMessage", id: "item_b1", turnId: "turn_1", text: "B's own answer", status: "completed" },
            ],
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
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        turnId: "turn_1",
        item: { type: "agentMessage", id: "item_a1", turnId: "turn_1", status: "inProgress" },
      },
    });
    fake.emitNotification({
      method: "item/completed",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        turnId: "turn_1",
        item: { type: "agentMessage", id: "item_a1", turnId: "turn_1", text: "A's answer", status: "completed" },
      },
    });

    fake.emitNotification({
      method: "turn/completed",
      params: { turnId: "turn_1", turn: { id: "turn_1", status: "completed", itemsView: "" } },
    });

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
      () =>
        threadsStore.getState().threads.get("ref_a")?.turns.length === 0 &&
        threadsStore.getState().threads.get("ref_b")?.turns.length === 0,
    );

    const readCallsAfterReconnect = fake.calls.filter((c) => c.method === "thread/read").slice(2);
    expect(readCallsAfterReconnect).toHaveLength(2); // every tracked ref re-subscribed, nothing else

    const forA = readCallsAfterReconnect.find((c) => (c.params as { ref: string }).ref === "ref_a");
    const forB = readCallsAfterReconnect.find((c) => (c.params as { ref: string }).ref === "ref_b");
    const expectedParams = (ref: string) => ({
      ref,
      includeTurns: true,
      itemsView: "full",
      subscribe: true,
      replaceSubscription: false,
      turnLimit: 40,
    });
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
    a.on("thread/read", () =>
      readResponse("ref_a", { turns: [{ id: "turn_1", status: "completed", itemsView: "full", items: [] }] }),
    );
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
    });
    expect(threadsStore.getState().threads.get("ref_a")?.status).toEqual({ type: "active" });

    // ...while A's handlers were detached at the swap: the same
    // notification shape, injected via the now-dead client, must NOT be
    // delivered - proof of no lingering double-subscription, not just
    // that B independently works.
    a.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_ref_a", ref: "ref_a", status: { type: "idle" } },
    });
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
  test("calls turn/start with text and a base64 image attachment (wire InputItem.data/mediaType/name - appwire/types.go:561-570)", async () => {
    const fake = connectFakeClient();
    fake.on("turn/start", () => ({ turn: { id: "turn_1", status: "inProgress", itemsView: "" } }));

    await threadsStore
      .getState()
      .send("ref_a", "hello", [{ mediaType: "image/png", data: "aGVsbG8=", name: "pic.png" }]);

    const call = fake.calls.find((c) => c.method === "turn/start");
    expect(call?.params).toEqual({
      ref: "ref_a",
      input: [
        { type: "text", text: "hello" },
        { type: "image", mediaType: "image/png", data: "aGVsbG8=", name: "pic.png" },
      ],
    });
  });

  test("send with no attachments sends text-only input", async () => {
    const fake = connectFakeClient();
    fake.on("turn/start", () => ({ turn: { id: "turn_1", status: "inProgress", itemsView: "" } }));

    await threadsStore.getState().send("ref_a", "hello");

    const call = fake.calls.find((c) => c.method === "turn/start");
    expect(call?.params).toEqual({ ref: "ref_a", input: [{ type: "text", text: "hello" }] });
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
    expect(call?.params).toEqual({
      ref: "ref_a",
      expectedTurnId: "turn_1",
      input: [{ type: "text", text: "steer text" }],
    });
  });

  test("steer includes a base64 image attachment when provided", async () => {
    const fake = connectFakeClient();
    await ensureActiveTurn(fake, "ref_a");
    fake.on("turn/steer", () => ({}));

    await threadsStore.getState().steer("ref_a", "steer text", [{ mediaType: "image/png", data: "aGVsbG8=" }]);

    const call = fake.calls.find((c) => c.method === "turn/steer");
    expect(call?.params).toEqual({
      ref: "ref_a",
      expectedTurnId: "turn_1",
      input: [
        { type: "text", text: "steer text" },
        { type: "image", mediaType: "image/png", data: "aGVsbG8=" },
      ],
    });
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

  test("queue includes a base64 image attachment when provided", async () => {
    const fake = connectFakeClient();
    fake.on("turn/queue", () => ({}));

    await threadsStore.getState().queue("ref_a", "", [{ mediaType: "image/png", data: "aGVsbG8=", name: "x.png" }]);

    const call = fake.calls.find((c) => c.method === "turn/queue");
    // queueText allows empty text when attachments are present (parity
    // finding §B: "image-only queue entries are valid") - buildInput's
    // text.trim() guard means an empty string contributes no text item.
    expect(call?.params).toEqual({
      ref: "ref_a",
      input: [{ type: "image", mediaType: "image/png", data: "aGVsbG8=", name: "x.png" }],
    });
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

// drainAsSteer (kata 0bq1 Path B): the plan's terse locked-interfaces block
// shows `drainAsSteer(ref)`, but the wire method it calls
// (TurnDrainAsSteerParams, appwire/types.go:769-776) carries an optional
// Input the daemon appends before draining ("Input lets clients atomically
// append the current composer payload before the drain"), and the parity
// floor's Path B row requires exactly that ("anything + non-empty queue ...
// turn/drainAsSteer carrying the textarea text/items so the daemon
// appends-then-drains atomically" - parity-m5-composer.md §A). Shipping a
// bare `drainAsSteer(ref)` would silently drop the composer's pending
// text/attachments on every Path-B drain, contradicting both the parity
// floor and the "optimistic pending applies uniformly to
// send/steer/queue/drain" binding constraint (which needs to know WHAT was
// submitted to render a pending chip). This store therefore ships
// `drainAsSteer(ref, text, attachments?)`, mirroring send/steer/queue's own
// shape exactly - flagged in the T1 report as an interpretation, not a
// silent deviation.
describe("useThreadsStore.drainAsSteer", () => {
  test("sends turn/drainAsSteer with the composer's text and attachments as input", async () => {
    const fake = connectFakeClient();
    fake.on("turn/drainAsSteer", () => ({}));

    await threadsStore.getState().drainAsSteer("ref_a", "drain text", [{ mediaType: "image/png", data: "aGVsbG8=" }]);

    const call = fake.calls.find((c) => c.method === "turn/drainAsSteer");
    expect(call?.params).toEqual({
      ref: "ref_a",
      input: [
        { type: "text", text: "drain text" },
        { type: "image", mediaType: "image/png", data: "aGVsbG8=" },
      ],
    });
  });

  test("sends an empty input array when the composer was empty (draining the queue alone)", async () => {
    const fake = connectFakeClient();
    fake.on("turn/drainAsSteer", () => ({}));

    await threadsStore.getState().drainAsSteer("ref_a", "");

    const call = fake.calls.find((c) => c.method === "turn/drainAsSteer");
    expect(call?.params).toEqual({ ref: "ref_a", input: [] });
  });

  test("maps a Conflict rejection to ConflictError", async () => {
    const fake = connectFakeClient();
    fake.on("turn/drainAsSteer", () => {
      throw new WireError("turn is not active", -32013, { serfErrorInfo: "conflict" });
    });

    await expect(threadsStore.getState().drainAsSteer("ref_a", "x")).rejects.toBeInstanceOf(ConflictError);
  });

  test("does not map a queuedDrainPartial rejection to ConflictError (parity §A: distinct 'drained after queueing' outcome)", async () => {
    const fake = connectFakeClient();
    fake.on("turn/drainAsSteer", () => {
      throw new WireError("queue drained partially", -32013, { serfErrorInfo: "queuedDrainPartial" });
    });

    const rejection = threadsStore.getState().drainAsSteer("ref_a", "x");
    await expect(rejection).rejects.not.toBeInstanceOf(ConflictError);
    await expect(rejection).rejects.toBeInstanceOf(WireError);
  });
});

describe("useThreadsStore.promoteQueuedAsSteer / cancelQueued", () => {
  test("promoteQueuedAsSteer sends turn/promoteQueuedAsSteer with {ref, index, expectedEntryId}", async () => {
    const fake = connectFakeClient();
    fake.on("turn/promoteQueuedAsSteer", () => ({}));

    await threadsStore.getState().promoteQueuedAsSteer("ref_a", 1, "entry_2");

    const call = fake.calls.find((c) => c.method === "turn/promoteQueuedAsSteer");
    expect(call?.params).toEqual({ ref: "ref_a", index: 1, expectedEntryId: "entry_2" });
  });

  test("promoteQueuedAsSteer maps a Conflict rejection (queue shifted under the snapshot) to ConflictError", async () => {
    const fake = connectFakeClient();
    fake.on("turn/promoteQueuedAsSteer", () => {
      throw new WireError("entry id mismatch", -32013, { serfErrorInfo: "conflict" });
    });

    await expect(threadsStore.getState().promoteQueuedAsSteer("ref_a", 0, "entry_1")).rejects.toBeInstanceOf(
      ConflictError,
    );
  });

  test("cancelQueued sends turn/cancelQueued with {ref, index, expectedEntryId} and returns {removedText, removedImages}", async () => {
    const fake = connectFakeClient();
    fake.on("turn/cancelQueued", () => ({ removedText: "queued message", removedImages: 2 }));

    const result = await threadsStore.getState().cancelQueued("ref_a", 0, "entry_1");

    const call = fake.calls.find((c) => c.method === "turn/cancelQueued");
    expect(call?.params).toEqual({ ref: "ref_a", index: 0, expectedEntryId: "entry_1" });
    expect(result).toEqual({ removedText: "queued message", removedImages: 2 });
  });

  test("cancelQueued maps a Conflict rejection (already consumed) to ConflictError", async () => {
    const fake = connectFakeClient();
    fake.on("turn/cancelQueued", () => {
      throw new WireError("entry id mismatch", -32013, { serfErrorInfo: "conflict" });
    });

    await expect(threadsStore.getState().cancelQueued("ref_a", 0, "entry_1")).rejects.toBeInstanceOf(ConflictError);
  });
});

describe("useThreadsStore session actions (setModel/setReasoningEffort/setGoal/rename/compact/shutdown/clearThread/forkFromTurn)", () => {
  test("setModel sends thread/model/set with {ref, modelProvider, model}", async () => {
    const fake = connectFakeClient();
    fake.on("thread/model/set", () => ({}));

    await threadsStore.getState().setModel("ref_a", "anthropic", "claude-opus-4-1");

    const call = fake.calls.find((c) => c.method === "thread/model/set");
    expect(call?.params).toEqual({ ref: "ref_a", modelProvider: "anthropic", model: "claude-opus-4-1" });
  });

  test("setReasoningEffort sends thread/reasoning-effort/set with {ref, reasoningEffort: level}", async () => {
    const fake = connectFakeClient();
    fake.on("thread/reasoning-effort/set", () => ({}));

    await threadsStore.getState().setReasoningEffort("ref_a", "high");

    const call = fake.calls.find((c) => c.method === "thread/reasoning-effort/set");
    expect(call?.params).toEqual({ ref: "ref_a", reasoningEffort: "high" });
  });

  test("setGoal sends goal/set with {ref, objective} and returns {started}", async () => {
    const fake = connectFakeClient();
    fake.on("goal/set", () => ({ started: true }));

    const result = await threadsStore.getState().setGoal("ref_a", "ship wave 5");

    const call = fake.calls.find((c) => c.method === "goal/set");
    expect(call?.params).toEqual({ ref: "ref_a", objective: "ship wave 5" });
    expect(result).toEqual({ started: true });
  });

  test("rename sends serf/thread/name/set with {ref, name}", async () => {
    const fake = connectFakeClient();
    fake.on("serf/thread/name/set", () => ({}));

    await threadsStore.getState().rename("ref_a", "New title");

    const call = fake.calls.find((c) => c.method === "serf/thread/name/set");
    expect(call?.params).toEqual({ ref: "ref_a", name: "New title" });
  });

  test("compact sends thread/compact/start with {ref}", async () => {
    const fake = connectFakeClient();
    fake.on("thread/compact/start", () => ({}));

    await threadsStore.getState().compact("ref_a");

    const call = fake.calls.find((c) => c.method === "thread/compact/start");
    expect(call?.params).toEqual({ ref: "ref_a" });
  });

  test("shutdown sends thread/shutdown with {ref}", async () => {
    const fake = connectFakeClient();
    fake.on("thread/shutdown", () => ({}));

    await threadsStore.getState().shutdown("ref_a");

    const call = fake.calls.find((c) => c.method === "thread/shutdown");
    expect(call?.params).toEqual({ ref: "ref_a" });
  });

  test("forkFromTurn sends thread/fork with {ref, ...opts} and returns the response verbatim", async () => {
    const fake = connectFakeClient();
    fake.on("thread/fork", () => ({ thread: testThread("ref_child"), originalInput: undefined }));

    const result = await threadsStore
      .getState()
      .forkFromTurn("ref_a", { sourceTurnId: "turn_1", editedInput: "edited text" });

    const call = fake.calls.find((c) => c.method === "thread/fork");
    expect(call?.params).toEqual({ ref: "ref_a", sourceTurnId: "turn_1", editedInput: "edited text" });
    expect(result.thread.serf.ref).toBe("ref_child");
  });

  test("forkFromTurn supports the aside mode's mutually-exclusive param set", async () => {
    const fake = connectFakeClient();
    fake.on("thread/fork", () => ({ thread: testThread("ref_aside") }));

    await threadsStore.getState().forkFromTurn("ref_a", { aside: true });

    const call = fake.calls.find((c) => c.method === "thread/fork");
    // sourceTurnId has no `omitempty` on the wire (appwire/types.go:694) -
    // it is required JSON even when meaningless (aside is mutually
    // exclusive with it), so the store defaults it to "" rather than
    // omitting the field.
    expect(call?.params).toEqual({ ref: "ref_a", aside: true, sourceTurnId: "" });
  });

  // clearThread has no corresponding live notification (appwire/protocol.go's
  // Notifications catalog carries no "thread cleared" entry - verified), so
  // the response's fresh Thread snapshot is the ONLY signal the transcript
  // is now empty; this store applies it directly rather than leaving the
  // tracked model stale until some unrelated future notification/reconnect.
  test("clearThread sends thread/clear with {ref} and replaces the tracked model from the response snapshot", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () =>
      readResponse("ref_a", { turns: [{ id: "turn_1", status: "completed", itemsView: "full", items: [] }] }),
    );
    await threadsStore.getState().ensureThread("ref_a");
    expect(threadsStore.getState().threads.get("ref_a")?.turns).toHaveLength(1);

    fake.on("thread/clear", () => ({ thread: testThread("ref_a", { turns: [] }), ref: "ref_a" }));
    await threadsStore.getState().clearThread("ref_a");

    const call = fake.calls.find((c) => c.method === "thread/clear");
    expect(call?.params).toEqual({ ref: "ref_a" });
    expect(threadsStore.getState().threads.get("ref_a")?.turns).toEqual([]);
  });

  test("clearThread updates a watched model tracking the same ref too", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () =>
      readResponse("ref_a", { turns: [{ id: "turn_1", status: "completed", itemsView: "full", items: [] }] }),
    );
    await threadsStore.getState().ensureThread("ref_a");
    await threadsStore.getState().watchThread("ref_a");

    fake.on("thread/clear", () => ({ thread: testThread("ref_a", { turns: [] }), ref: "ref_a" }));
    await threadsStore.getState().clearThread("ref_a");

    expect(threadsStore.getState().threads.get("ref_a")?.turns).toEqual([]);
    expect(threadsStore.getState().watchedThreads.get("ref_a")?.turns).toEqual([]);
  });

  test("clearThread propagates a rejection and leaves the tracked model untouched", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));
    await threadsStore.getState().ensureThread("ref_a");
    fake.on("thread/clear", () => {
      throw new Error("turn in progress");
    });

    const before = threadsStore.getState().threads.get("ref_a");
    await expect(threadsStore.getState().clearThread("ref_a")).rejects.toThrow("turn in progress");
    expect(threadsStore.getState().threads.get("ref_a")).toBe(before);
  });

  // One representative Conflict-mapping test standing in for every
  // thread-level action above - each wraps its client.request in the exact
  // same mapConflict try/catch as send/steer/queue/interrupt (proven
  // exhaustively above); repeating it per method would test the identical
  // wrapper code path over and over rather than add real coverage.
  test("session actions also map a Conflict rejection to ConflictError (setModel as the representative case)", async () => {
    const fake = connectFakeClient();
    fake.on("thread/model/set", () => {
      throw new WireError("model unavailable", -32013, { serfErrorInfo: "conflict" });
    });

    await expect(threadsStore.getState().setModel("ref_a", "openai", "gpt-5.5")).rejects.toBeInstanceOf(ConflictError);
  });
});

// listModels/listTasks (T1 addendum, sanctioned NEEDS_CONTEXT gap for the
// chrome stream): both are plain read-only wire calls with no turn-CAS
// concept - verified against every server-side handler
// (cmd/serf-hub/app_rpc.go's registerMiscHandlers, cmd/serf-hub/
// internal/appsource/{local_daemon,codex_source}.go's ListTasks,
// server/appwire_runtime.go's handleAppTasksList/handleAppModelList): none
// of them ever construct appwire.Conflict(). Neither action maps errors -
// a WireError (even one shaped like a Conflict, which cannot actually occur
// here) passes through unchanged, same as resolveEscalation above.
describe("useThreadsStore.listModels", () => {
  function modelListResponse(): ModelListResponse {
    return {
      data: [
        { provider: "anthropic", model: "claude-sonnet-4-5" },
        { provider: "openai", model: "gpt-5.5" },
      ],
      diagnostics: [{ provider: "ollama", message: "ollama: connection refused" }],
      recent: [{ provider: "anthropic", model: "claude-sonnet-4-5" }],
    };
  }

  test("sends model/list with no params and returns the response verbatim", async () => {
    const fake = connectFakeClient();
    fake.on("model/list", () => modelListResponse());

    const result = await threadsStore.getState().listModels();

    const call = fake.calls.find((c) => c.method === "model/list");
    expect(call?.params).toEqual({});
    expect(result).toEqual(modelListResponse());
  });

  test("caches across calls within the session - a second call does not re-request", async () => {
    const fake = connectFakeClient();
    fake.on("model/list", () => modelListResponse());

    await threadsStore.getState().listModels();
    await threadsStore.getState().listModels();

    expect(fake.calls.filter((c) => c.method === "model/list")).toHaveLength(1);
  });

  test("concurrent calls before the first resolves share one request", async () => {
    const fake = connectFakeClient();
    fake.on("model/list", () => modelListResponse());

    const [a, b] = await Promise.all([threadsStore.getState().listModels(), threadsStore.getState().listModels()]);

    expect(fake.calls.filter((c) => c.method === "model/list")).toHaveLength(1);
    expect(a).toEqual(modelListResponse());
    expect(b).toEqual(modelListResponse());
  });

  test("refresh:true bypasses the cache and issues a fresh request", async () => {
    const fake = connectFakeClient();
    let call = 0;
    fake.on("model/list", () => {
      call += 1;
      return { data: [{ provider: "anthropic", model: `model-${call}` }] };
    });

    const first = await threadsStore.getState().listModels();
    const second = await threadsStore.getState().listModels(true);

    expect(fake.calls.filter((c) => c.method === "model/list")).toHaveLength(2);
    expect(first.data[0]?.model).toBe("model-1");
    expect(second.data[0]?.model).toBe("model-2");
  });

  test("a failed call does not cache a rejected promise - the next call retries rather than repeating the same rejection", async () => {
    const fake = connectFakeClient();
    let shouldFail = true;
    fake.on("model/list", () => {
      if (shouldFail) throw new Error("boom");
      return modelListResponse();
    });

    await expect(threadsStore.getState().listModels()).rejects.toThrow("boom");

    shouldFail = false;
    const result = await threadsStore.getState().listModels();

    expect(fake.calls.filter((c) => c.method === "model/list")).toHaveLength(2);
    expect(result).toEqual(modelListResponse());
  });

  test("propagates a rejection unchanged - not mapped to ConflictError even when it is Conflict-shaped (model/list can never actually return one)", async () => {
    const fake = connectFakeClient();
    fake.on("model/list", () => {
      throw new WireError("shouldn't happen", -32013, { serfErrorInfo: "conflict" });
    });

    const rejection = threadsStore.getState().listModels();
    await expect(rejection).rejects.toBeInstanceOf(WireError);
    await expect(rejection).rejects.not.toBeInstanceOf(ConflictError);
  });

  test("throws when no client has been connected yet", async () => {
    await expect(threadsStore.getState().listModels()).rejects.toThrow(/no client connected/i);
  });

  test("resetThreadsStoreForTests clears the models cache, same as every other module-private cache", async () => {
    const fake = connectFakeClient();
    fake.on("model/list", () => modelListResponse());
    await threadsStore.getState().listModels();
    expect(fake.calls.filter((c) => c.method === "model/list")).toHaveLength(1);

    resetThreadsStoreForTests();

    const fake2 = connectFakeClient();
    fake2.on("model/list", () => modelListResponse());
    await threadsStore.getState().listModels();
    expect(fake2.calls.filter((c) => c.method === "model/list")).toHaveLength(1); // fresh fetch, not a stale cache hit
  });
});

describe("useThreadsStore.listTasks", () => {
  // Wire-true shape: TaskListResponse.Data is `any` on the catalog
  // (appwire/types.go:896-898) - server/server.go's SetTasksFunc doc
  // comment says the registered function "should return a JSON-serializable
  // slice (typically []task.Task)"; agent/task/task_store.go:54-74 is that
  // struct. This fixture mirrors its real JSON field names verbatim.
  const TASKS_DATA = [
    { id: 1, type: "implement", description: "Wire up listModels/listTasks", prompt: "…", status: "done" },
    {
      id: 2,
      type: "verify",
      description: "Confirm tests pass",
      prompt: "…",
      status: "in_progress",
      depends_on: [1],
    },
  ];

  test("sends serf/tasks/list with {ref} and returns the raw data field, not the response wrapper", async () => {
    const fake = connectFakeClient();
    fake.on("serf/tasks/list", () => ({ data: TASKS_DATA }));

    const result = await threadsStore.getState().listTasks("ref_a");

    const call = fake.calls.find((c) => c.method === "serf/tasks/list");
    expect(call?.params).toEqual({ ref: "ref_a" });
    expect(result).toEqual(TASKS_DATA);
  });

  test("propagates a Codex-source rejection (actionUnavailable) unchanged, not mapped to ConflictError", async () => {
    const fake = connectFakeClient();
    // Mirrors CodexSource.ListTasks verbatim (cmd/serf-hub/internal/
    // appsource/codex_source.go:405-407): appwire.Unavailable(...), code
    // -32014, serfErrorInfo "actionUnavailable" - never a Conflict.
    fake.on("serf/tasks/list", () => {
      throw new WireError("codex source does not expose serf tasks", -32014, {
        serfErrorInfo: "actionUnavailable",
      });
    });

    const rejection = threadsStore.getState().listTasks("ref_codex");
    await expect(rejection).rejects.toBeInstanceOf(WireError);
    await expect(rejection).rejects.not.toBeInstanceOf(ConflictError);
    await expect(rejection).rejects.toMatchObject({ serfErrorInfo: "actionUnavailable" });
  });

  test("throws when no client has been connected yet", async () => {
    await expect(threadsStore.getState().listTasks("ref_a")).rejects.toThrow(/no client connected/i);
  });
});

describe("useThreadsStore.resolveEscalation", () => {
  function threadWithEscalation(ref: string, escalationId: string): ThreadReadResponse {
    return readResponse(ref, {
      serf: {
        ref,
        capabilities: CAPABILITIES,
        queue: {},
        pendingEscalations: [
          {
            ref,
            threadId: `thr_${ref}`,
            escalationId,
            mode: "workspace-write",
            tool: "shell",
            kind: "shell",
            deniedPath: "/etc/hosts",
          },
        ],
      },
    });
  }

  test("calls serf/sandbox/escalation/resolve with exact params", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => threadWithEscalation("ref_a", "esc_1"));
    await threadsStore.getState().ensureThread("ref_a");
    fake.on("serf/sandbox/escalation/resolve", () => ({}));

    await threadsStore.getState().resolveEscalation("ref_a", "esc_1", true);

    const call = fake.calls.find((c) => c.method === "serf/sandbox/escalation/resolve");
    expect(call?.params).toEqual({ ref: "ref_a", escalationId: "esc_1", approve: true });
  });

  test("a successful resolve removes the escalation from the tracked model", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => threadWithEscalation("ref_a", "esc_1"));
    await threadsStore.getState().ensureThread("ref_a");
    fake.on("serf/sandbox/escalation/resolve", () => ({}));

    expect(threadsStore.getState().threads.get("ref_a")?.pendingEscalations).toHaveLength(1);
    await threadsStore.getState().resolveEscalation("ref_a", "esc_1", false);
    expect(threadsStore.getState().threads.get("ref_a")?.pendingEscalations).toEqual([]);
  });

  test("a rejected resolve propagates and leaves the model untouched", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => threadWithEscalation("ref_a", "esc_1"));
    await threadsStore.getState().ensureThread("ref_a");
    fake.on("serf/sandbox/escalation/resolve", () => {
      throw new Error("sandbox offline");
    });

    const before = threadsStore.getState().threads.get("ref_a");
    await expect(threadsStore.getState().resolveEscalation("ref_a", "esc_1", true)).rejects.toThrow("sandbox offline");
    expect(threadsStore.getState().threads.get("ref_a")).toBe(before); // same reference: untouched
  });

  test("maps a Conflict wire rejection (serfErrorInfo === conflict) to ConflictError", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => threadWithEscalation("ref_a", "esc_1"));
    await threadsStore.getState().ensureThread("ref_a");
    // A stale/double/raced resolve is surfaced as appwire.Conflict() by the
    // daemon (server/appwire_runtime.go's handleAppSandboxEscalationResolve:
    // "Surface it as a conflict so the client can drop the card rather than
    // retry"). resolve must map it to ConflictError like every other mutating
    // action, so the escalation rail treats it as terminal, not retryable.
    fake.on("serf/sandbox/escalation/resolve", () => {
      throw new WireError("escalation is not pending (unknown or already resolved)", -32013, {
        serfErrorInfo: "conflict",
      });
    });

    const rejection = threadsStore.getState().resolveEscalation("ref_a", "esc_1", true);
    await expect(rejection).rejects.toBeInstanceOf(ConflictError);
    await expect(rejection).rejects.toThrow("already resolved");
  });

  test("does not map a same-code, different-serfErrorInfo WireError to ConflictError", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => threadWithEscalation("ref_a", "esc_1"));
    await threadsStore.getState().ensureThread("ref_a");
    // Same wire code (-32013) but a non-conflict serfErrorInfo — the
    // discriminator is the serfErrorInfo string, not the code alone.
    fake.on("serf/sandbox/escalation/resolve", () => {
      throw new WireError("something else", -32013, { serfErrorInfo: "queuedDrainPartial" });
    });

    const rejection = threadsStore.getState().resolveEscalation("ref_a", "esc_1", true);
    await expect(rejection).rejects.not.toBeInstanceOf(ConflictError);
    await expect(rejection).rejects.toBeInstanceOf(WireError);
  });

  test("a resolve for an escalation absent from the model is a same-reference no-op", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => threadWithEscalation("ref_a", "esc_1"));
    await threadsStore.getState().ensureThread("ref_a");
    fake.on("serf/sandbox/escalation/resolve", () => ({}));

    const before = threadsStore.getState().threads;
    await threadsStore.getState().resolveEscalation("ref_a", "esc_never_pending", true);
    expect(threadsStore.getState().threads).toBe(before);
  });

  test("updates BOTH threads and watchedThreads when the same ref is tracked in each", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => threadWithEscalation("ref_a", "esc_1"));
    fake.on("serf/sandbox/escalation/resolve", () => ({}));
    await threadsStore.getState().ensureThread("ref_a");
    await threadsStore.getState().watchThread("ref_a");

    await threadsStore.getState().resolveEscalation("ref_a", "esc_1", true);

    expect(threadsStore.getState().threads.get("ref_a")?.pendingEscalations).toEqual([]);
    expect(threadsStore.getState().watchedThreads.get("ref_a")?.pendingEscalations).toEqual([]);
  });

  test("a serf/sandbox/escalation/resolved notification clears the matching card from both tracked and watched models", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => threadWithEscalation("ref_a", "esc_1"));
    await threadsStore.getState().ensureThread("ref_a");
    await threadsStore.getState().watchThread("ref_a");
    expect(threadsStore.getState().threads.get("ref_a")?.pendingEscalations).toHaveLength(1);

    // The real wire shape (appwire.SandboxEscalationResolved): {threadId, ref,
    // escalationId} — the daemon's broadcast to every OTHER subscribed client
    // when a pending escalation leaves the set (server/appwire_runtime.go's M7
    // fix). Unlike the local resolveEscalation action, this arrives for a
    // resolve some OTHER client made, so a client that only watches the session
    // still drops its now-stale card.
    fake.emitNotification({
      method: "serf/sandbox/escalation/resolved",
      params: { threadId: "thr_ref_a", ref: "ref_a", escalationId: "esc_1" },
    });

    expect(threadsStore.getState().threads.get("ref_a")?.pendingEscalations).toEqual([]);
    expect(threadsStore.getState().watchedThreads.get("ref_a")?.pendingEscalations).toEqual([]);
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
    });

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
      });
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
    });

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
    fake.emitUnknownNotification({ method: "totally/unknown", params: { ref: "ref_a" } });

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
    });
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
    });
    expect(threadsStore.getState().frameTimes.get("ref_a")).toEqual([1000]);

    fake.emitStateChange("reconnecting");
    fake.emitReady();
    await flushUntil(() => fake.calls.filter((c) => c.method === "thread/read").length > 1);

    expect(threadsStore.getState().frameTimes.get("ref_a")).toEqual([1000]);
  });
});

// serf/job/started|finished (dr7e): the "Signal merging" step from
// docs/superpowers/specs/2026-06-25-subagent-run-rendering-design.md.
// handleNotification routes these into subagentModuleStore independent of
// ThreadModel entirely (see applySubagentJobSignal's own comment in
// threads.ts) - these tests exercise that wiring end to end through the
// SAME client.emitNotification path the ThreadModel-routing tests above use,
// then read the result back out of subagentModuleStore's own useSubagentRows
// hook (not out of ThreadModel, which these notifications never touch beyond
// lastFrameAt).
describe("serf/job/started|finished routing into subagentModuleStore (dr7e)", () => {
  afterEach(resetSubagentModuleStoreForTests);

  test("serf/job/finished patches an existing row's liveKind/liveReason/resumable/exhaustion", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));
    await threadsStore.getState().ensureThread("ref_a");
    const scope = turnScopeKey("ref_a", "turn_1");
    upsertSubagentRow(scope, { rowKey: "dlg:dlg_1", kind: "running", task: "t", resultPreview: "" });

    fake.emitNotification({
      method: "serf/job/finished",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        job: {
          jobId: "job_1",
          jobType: "delegate",
          status: "exhausted",
          reason: "ran out of turns",
          resumable: true,
          exhaustionBudget: "30m",
          exhaustionLimit: 60,
          outputBytes: 0,
          delegateId: "dlg_1",
          originTurnId: "turn_1",
        },
      },
    });

    const { result } = renderHook(() => useSubagentRows(scope));
    expect(result.current[0]).toMatchObject({
      liveKind: "failed",
      liveReason: "ran out of turns",
      resumable: true,
      exhaustionBudget: "30m",
      exhaustionLimit: 60,
    });
  });

  test("serf/job/started resets an existing row to running", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));
    await threadsStore.getState().ensureThread("ref_a");
    const scope = turnScopeKey("ref_a", "turn_1");
    upsertSubagentRow(scope, { rowKey: "dlg:dlg_1", kind: "done", task: "t", resultPreview: "" });
    updateSubagentRowIfExists(scope, "dlg:dlg_1", { liveKind: "failed", liveReason: "boom" });

    fake.emitNotification({
      method: "serf/job/started",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        job: {
          jobId: "job_2",
          jobType: "delegate",
          status: "running",
          outputBytes: 0,
          delegateId: "dlg_1",
          originTurnId: "turn_1",
        },
      },
    });

    const { result } = renderHook(() => useSubagentRows(scope));
    expect(result.current[0]).toMatchObject({ liveKind: "running", liveReason: undefined });
  });

  test("a job with no originTurnId (not run via delegate) is silently ignored, no row touched", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));
    await threadsStore.getState().ensureThread("ref_a");
    const scope = turnScopeKey("ref_a", "turn_1");
    upsertSubagentRow(scope, { rowKey: "job:job_3", kind: "running", task: "t", resultPreview: "" });

    fake.emitNotification({
      method: "serf/job/finished",
      params: {
        threadId: "thr_ref_a",
        ref: "ref_a",
        job: { jobId: "job_3", jobType: "shell", status: "completed", outputBytes: 0 },
      },
    });

    const { result } = renderHook(() => useSubagentRows(scope));
    expect(result.current[0]?.liveKind).toBeUndefined();
  });

  // kata 8525: a notification for a DIFFERENT session must never patch a row
  // planted under the same bare turnId in another session - this is exactly
  // the collision the fix closes (turn ids restart at 0 per session).
  test("a serf/job/finished notification for a different session's ref never touches this session's row", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));
    await threadsStore.getState().ensureThread("ref_a");
    const scope = turnScopeKey("ref_a", "turn_1");
    upsertSubagentRow(scope, { rowKey: "dlg:dlg_1", kind: "running", task: "t", resultPreview: "" });

    fake.emitNotification({
      method: "serf/job/finished",
      params: {
        threadId: "thr_ref_b",
        ref: "ref_b",
        job: {
          jobId: "job_1",
          jobType: "delegate",
          status: "completed",
          outputBytes: 0,
          delegateId: "dlg_1",
          originTurnId: "turn_1",
        },
      },
    });

    const { result } = renderHook(() => useSubagentRows(scope));
    expect(result.current[0]?.liveKind).toBeUndefined();
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
    });

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
    });

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
    });

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

  // yd16 §4.2: the expanded subagent card watches with { includeTurns: true }
  // so the Activity feed has the child's turn history. A read scripted to
  // return a turn only when includeTurns is true lets these tests prove turns
  // actually crossed the wire, not just that a flag was threaded through.
  function turnsAwareRead(fake: FakeClient): void {
    fake.on("thread/read", (params) => {
      const includeTurns = (params as { includeTurns: boolean }).includeTurns;
      return readResponse("ref_a", {
        turns: includeTurns ? [{ id: "turn_1", status: "completed", itemsView: "full", items: [] }] : [],
      });
    });
  }

  test("watchThread(ref, { includeTurns: true }) hydrates with turns populated", async () => {
    const fake = connectFakeClient();
    turnsAwareRead(fake);

    await threadsStore.getState().watchThread("ref_a", { includeTurns: true });

    const call = fake.calls.find((c) => c.method === "thread/read");
    expect(call?.params).toMatchObject({ includeTurns: true });
    expect(threadsStore.getState().watchedThreads.get("ref_a")?.turns).toHaveLength(1);
  });

  test("a lean watch followed by a { includeTurns: true } call upgrades: turns become populated despite the .has(ref) short-circuit", async () => {
    const fake = connectFakeClient();
    turnsAwareRead(fake);

    await threadsStore.getState().watchThread("ref_a"); // lean first: no turns
    expect(threadsStore.getState().watchedThreads.get("ref_a")?.turns).toHaveLength(0);

    await threadsStore.getState().watchThread("ref_a", { includeTurns: true }); // upgrade re-read
    expect(threadsStore.getState().watchedThreads.get("ref_a")?.turns).toHaveLength(1);
    // The upgrade bypasses the .has(ref)/inflight-dedup short-circuits (those
    // are for concurrent first-mounts), so a genuine second read fired.
    expect(fake.calls.filter((c) => c.method === "thread/read")).toHaveLength(2);
  });

  test("a concurrent rich watch does not share an in-flight lean hydrate or lose its turns", async () => {
    const fake = connectFakeClient();
    const pending: Array<{
      includeTurns: boolean;
      resolve: (response: ThreadReadResponse) => void;
    }> = [];
    fake.on("thread/read", (params) => {
      return new Promise<ThreadReadResponse>((resolve) => {
        pending.push({ includeTurns: (params as { includeTurns: boolean }).includeTurns, resolve });
      });
    });

    const lean = threadsStore.getState().watchThread("ref_a");
    await flushUntil(() => pending.length === 1);
    const rich = threadsStore.getState().watchThread("ref_a", { includeTurns: true });
    await flushUntil(() => pending.length === 2);

    expect(pending.map((request) => request.includeTurns)).toEqual([false, true]);

    pending[1]!.resolve(
      readResponse("ref_a", {
        turns: [{ id: "turn_rich", status: "completed", itemsView: "full", items: [] }],
      }),
    );
    pending[0]!.resolve(readResponse("ref_a", { turns: [] }));
    await Promise.all([lean, rich]);

    expect(threadsStore.getState().watchedThreads.get("ref_a")?.turns).toHaveLength(1);
    threadsStore.getState().releaseWatchedThread("ref_a");
    expect(threadsStore.getState().watchedThreads.has("ref_a")).toBe(true);
    threadsStore.getState().releaseWatchedThread("ref_a");
    expect(threadsStore.getState().watchedThreads.has("ref_a")).toBe(false);
  });

  test("a rejected shared hydrate leaves the mounted watcher claim until cleanup", async () => {
    const fake = connectFakeClient();
    const pending: Array<{
      resolve: (response: ThreadReadResponse) => void;
      reject: (error: Error) => void;
    }> = [];
    fake.on(
      "thread/read",
      () =>
        new Promise<ThreadReadResponse>((resolve, reject) => {
          pending.push({ resolve, reject });
        }),
    );

    const first = threadsStore.getState().watchThread("ref_a");
    await flushUntil(() => pending.length === 1);
    const second = threadsStore.getState().watchThread("ref_a");
    expect(pending).toHaveLength(1);

    // The first watcher has already unmounted; the second watcher still owns
    // its claim when the shared request fails.
    threadsStore.getState().releaseWatchedThread("ref_a");
    pending[0]!.reject(new Error("hydrate failed"));
    await expect(first).rejects.toThrow("hydrate failed");
    await expect(second).rejects.toThrow("hydrate failed");

    // A fresh watch provides a model so the remaining original claim can be
    // observed through the normal last-release behavior.
    const probe = threadsStore.getState().watchThread("ref_a");
    await flushUntil(() => pending.length === 2);
    pending[1]!.resolve(readResponse("ref_a"));
    await probe;

    // Releasing the probe must leave the still-mounted original watcher
    // tracked; only its later cleanup may clear the ref.
    threadsStore.getState().releaseWatchedThread("ref_a");
    expect(threadsStore.getState().watchedThreads.has("ref_a")).toBe(true);
    threadsStore.getState().releaseWatchedThread("ref_a");
    expect(threadsStore.getState().watchedThreads.has("ref_a")).toBe(false);
  });

  test("a rich upgrade wins over a slower lean reconnect hydrate", async () => {
    const fake = connectFakeClient();
    let readCount = 0;
    const pending: Array<{
      includeTurns: boolean;
      resolve: (response: ThreadReadResponse) => void;
    }> = [];
    fake.on("thread/read", (params) => {
      readCount += 1;
      if (readCount === 1) return readResponse("ref_a", { turns: [] });
      return new Promise<ThreadReadResponse>((resolve) => {
        pending.push({ includeTurns: (params as { includeTurns: boolean }).includeTurns, resolve });
      });
    });

    await threadsStore.getState().watchThread("ref_a");
    fake.emitStateChange("reconnecting");
    fake.emitReady();
    await flushUntil(() => pending.length === 1);

    const rich = threadsStore.getState().watchThread("ref_a", { includeTurns: true });
    await flushUntil(() => pending.length === 2);
    expect(pending.map((request) => request.includeTurns)).toEqual([false, true]);
    expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(3);

    pending[1]!.resolve(
      readResponse("ref_a", {
        turns: [{ id: "turn_reconnect_rich", status: "completed", itemsView: "full", items: [] }],
      }),
    );
    pending[0]!.resolve(readResponse("ref_a", { turns: [] }));
    await rich;

    expect(threadsStore.getState().watchedThreads.get("ref_a")?.turns).toHaveLength(1);
    expect(threadsStore.getState().watchedThreads.get("ref_a")?.turns[0]?.id).toBe("turn_reconnect_rich");

    threadsStore.getState().releaseWatchedThread("ref_a");
    threadsStore.getState().releaseWatchedThread("ref_a");
    expect(threadsStore.getState().watchedThreads.has("ref_a")).toBe(false);
  });

  test("a new watcher starts a fresh hydrate after the previous lifecycle is released", async () => {
    const fake = connectFakeClient();
    const pending: Array<(response: ThreadReadResponse) => void> = [];
    fake.on("thread/read", () => new Promise<ThreadReadResponse>((resolve) => pending.push(resolve)));

    const first = threadsStore.getState().watchThread("ref_a");
    await flushUntil(() => pending.length === 1);
    threadsStore.getState().releaseWatchedThread("ref_a");

    const second = threadsStore.getState().watchThread("ref_a");
    await flushUntil(() => pending.length === 2);
    expect(fake.calls.filter((call) => call.method === "thread/read")).toHaveLength(2);

    pending[1]!(
      readResponse("ref_a", { turns: [{ id: "turn_new", status: "completed", itemsView: "full", items: [] }] }),
    );
    await second;
    expect(threadsStore.getState().watchedThreads.get("ref_a")?.turns[0]?.id).toBe("turn_new");

    pending[0]!(
      readResponse("ref_a", { turns: [{ id: "turn_old", status: "completed", itemsView: "full", items: [] }] }),
    );
    await first;
    expect(threadsStore.getState().watchedThreads.get("ref_a")?.turns[0]?.id).toBe("turn_new");

    threadsStore.getState().releaseWatchedThread("ref_a");
    expect(threadsStore.getState().watchedThreads.has("ref_a")).toBe(false);
  });

  test("monotonic: once turns are loaded, a later lean watch does not downgrade them away", async () => {
    const fake = connectFakeClient();
    turnsAwareRead(fake);

    await threadsStore.getState().watchThread("ref_a", { includeTurns: true });
    expect(threadsStore.getState().watchedThreads.get("ref_a")?.turns).toHaveLength(1);

    await threadsStore.getState().watchThread("ref_a"); // a lean watcher joins
    expect(threadsStore.getState().watchedThreads.get("ref_a")?.turns).toHaveLength(1); // still there
    expect(fake.calls.filter((c) => c.method === "thread/read")).toHaveLength(1); // no re-read: already rich
  });

  test("releasing the last watcher clears the per-ref includeTurns flag so a fresh watch starts lean again", async () => {
    const fake = connectFakeClient();
    turnsAwareRead(fake);

    await threadsStore.getState().watchThread("ref_a", { includeTurns: true });
    threadsStore.getState().releaseWatchedThread("ref_a");
    expect(threadsStore.getState().watchedThreads.has("ref_a")).toBe(false);

    await threadsStore.getState().watchThread("ref_a"); // fresh lean watch
    expect(threadsStore.getState().watchedThreads.get("ref_a")?.turns).toHaveLength(0);
  });
});

// scrollPositions (wave 4 T4): per-ref scroll offset persisted across a
// PaneHost unmount/remount (dockview unmounts an inactive pane's whole tree
// - see Session.tsx's own comment). Deliberately NOT wired into the
// ensureThread/releaseThread refcount lifecycle the way `threads`/
// `frameTimes` are: those two are cheaply re-derived from a fresh
// thread/read on the next mount, but a scroll offset has no server-side
// source of truth to re-derive - it must outlive a ref's refcount dropping
// to zero, which is exactly the "survives remount" case this field exists
// for. setScrollPosition is a plain, synchronous, no-network write (unlike
// every other action on this store) - flow/'s hook calls it directly off a
// real scroll event, not through requireClient().
describe("scrollPositions (threads store)", () => {
  test("starts with no entry for a ref that has never had a position recorded", () => {
    expect(threadsStore.getState().scrollPositions.get("ref_a")).toBeUndefined();
  });

  test("setScrollPosition records the position for a ref", () => {
    threadsStore.getState().setScrollPosition("ref_a", 240);

    expect(threadsStore.getState().scrollPositions.get("ref_a")).toBe(240);
  });

  test("setScrollPosition overwrites a previous position for the same ref, leaving other refs untouched", () => {
    threadsStore.getState().setScrollPosition("ref_a", 100);
    threadsStore.getState().setScrollPosition("ref_b", 999);

    threadsStore.getState().setScrollPosition("ref_a", 300);

    expect(threadsStore.getState().scrollPositions.get("ref_a")).toBe(300);
    expect(threadsStore.getState().scrollPositions.get("ref_b")).toBe(999);
  });

  test("does not require the ref to be a tracked/hydrated thread - a pure client-side write", () => {
    expect(threadsStore.getState().threads.has("ref_never_hydrated")).toBe(false);

    threadsStore.getState().setScrollPosition("ref_never_hydrated", 50);

    expect(threadsStore.getState().scrollPositions.get("ref_never_hydrated")).toBe(50);
  });

  test("releaseThread does NOT drop the ref's scroll position - it must survive a pane unmount, unlike frameTimes", async () => {
    const fake = connectFakeClient();
    fake.on("thread/read", () => readResponse("ref_a"));
    await threadsStore.getState().ensureThread("ref_a");
    threadsStore.getState().setScrollPosition("ref_a", 1234);

    threadsStore.getState().releaseThread("ref_a");

    expect(threadsStore.getState().threads.has("ref_a")).toBe(false); // the model itself IS released
    expect(threadsStore.getState().scrollPositions.get("ref_a")).toBe(1234); // the scroll position is not
  });

  test("resetThreadsStoreForTests clears scrollPositions, same as every other store field", () => {
    threadsStore.getState().setScrollPosition("ref_a", 240);

    resetThreadsStoreForTests();

    expect(threadsStore.getState().scrollPositions.get("ref_a")).toBeUndefined();
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
