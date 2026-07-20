import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { act, cleanup, renderHook } from "@testing-library/react";
import { FakeClient } from "../protocol/testing/fakeClient";
import { ConnectionClosedError, WireError } from "../protocol/errors";
import type { ConnectionState } from "../protocol/client";
import type { AnyNotification, Thread, ThreadCapabilities, ThreadReadResponse } from "../protocol/types.gen";
import { connectionStore, useConnectionStore } from "./connection";
import { ConflictError, resetThreadsStoreForTests, threadsStore, useThreadsStore } from "./threads";

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

  test("serverInfo stays undefined: AppwireClientLike exposes no way to read it back after the handshake", () => {
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

  test("wires onNotification/onReady on the client exactly once across multiple store calls", async () => {
    const fake = connectFakeClient();
    const onNotificationSpy = vi.spyOn(fake, "onNotification");
    const onReadySpy = vi.spyOn(fake, "onReady");
    fake.on("thread/read", (params) => readResponse((params as { ref: string }).ref));

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

    fake.emitNotification({
      method: "turn/completed",
      params: {
        turnId: "turn_1",
        turn: {
          id: "turn_1",
          status: "completed",
          itemsView: "",
          items: [{ type: "agentMessage", id: "item_a1", turnId: "turn_1", text: "A's answer", status: "completed" }],
        },
      },
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
