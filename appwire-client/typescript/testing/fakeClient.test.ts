// @vitest-environment node

// FakeClient's own contract tests. The behavior under test here is the one
// that keeps the rest of the suite honest: a scripted or requested method
// name, and an injected notification's method, must exist in the hub's real
// catalogs (METHOD_NAMES / NOTIFICATION_NAMES in ../types.gen.ts), so a wire
// rename can no longer leave a test green against a production build that
// calls — or listens for — a name the hub stopped serving.
import { describe, expect, test, vi } from "vitest";
import type { ConnectionState } from "../client";
import { ConnectionClosedError } from "../errors";
import type { AnyNotification, InitializeResponse, MethodName } from "../types.gen";
import { deferRequest, FakeClient, gateSettlements, type Settlement } from "./fakeClient";
import { FAKE_INITIALIZE_RESULT } from "./fakeSocket";

// A name the hub has never served. Cast because MethodName correctly refuses
// it at compile time — this suite exercises the runtime guard that catches
// the same mistake when the type check has been bypassed (a cast, an `any`,
// a string built at runtime), which is exactly how the evener/dirs/complete
// rename slipped through.
const UNKNOWN = "evener/dirs/complete" as MethodName;

describe("FakeClient method-name validation", () => {
  test("on() rejects a method the hub does not serve", () => {
    const fake = new FakeClient();
    expect(() => fake.on(UNKNOWN, () => undefined as never)).toThrow(
      /FakeClient: unknown method "evener\/dirs\/complete"/,
    );
  });

  test("on() names the generated catalog in its error, so the fix is obvious", () => {
    const fake = new FakeClient();
    expect(() => fake.on(UNKNOWN, () => undefined as never)).toThrow(/METHOD_NAMES/);
  });

  test("on() accepts every method in the generated catalog", () => {
    const fake = new FakeClient();
    expect(() => fake.on("evener/paths/complete", () => ({ data: [] }))).not.toThrow();
    expect(() => fake.on("thread/read", () => undefined as never)).not.toThrow();
  });

  test("request() rejects a method the hub does not serve, without recording the call", async () => {
    const fake = new FakeClient();
    await expect(fake.request(UNKNOWN, {} as never)).rejects.toThrow(
      /FakeClient: unknown method "evener\/dirs\/complete"/,
    );
    expect(fake.calls).toHaveLength(0);
  });

  // The unknown-method guard must run before the ready-gate: a bad method
  // name is a bug in the test or the caller either way, and reporting
  // "not ready" for it would mask the real problem behind a plausible one.
  test("request() reports an unknown method rather than the ready-gate when both apply", async () => {
    const fake = new FakeClient("connecting");
    await expect(fake.request(UNKNOWN, {} as never)).rejects.toThrow(/unknown method/);
  });

  // A known method with nothing scripted is a different, legitimate failure
  // (the test forgot to script it) and must stay distinguishable from a
  // method that does not exist at all.
  test("request() still reports a known-but-unscripted method distinctly", async () => {
    const fake = new FakeClient();
    await expect(fake.request("thread/read", { ref: "ref_a", includeTurns: false })).rejects.toThrow(
      /no handler scripted for "thread\/read"/,
    );
  });
});

// A notification whose method the hub never sends, cast the way the suite's
// 178 `as AnyNotification` sites all cast — which is precisely why the
// compile-time union is no guard here at all.
const RENAMED = { method: "thread/renamed", params: {} } as unknown as AnyNotification;

describe("FakeClient notification-name validation", () => {
  test("emitNotification rejects a notification the hub does not send", () => {
    const fake = new FakeClient();
    expect(() => fake.emitNotification(RENAMED)).toThrow(/FakeClient: unknown notification "thread\/renamed"/);
  });

  test("emitNotification names the generated catalog in its error, so the fix is obvious", () => {
    const fake = new FakeClient();
    expect(() => fake.emitNotification(RENAMED)).toThrow(/NOTIFICATION_NAMES/);
  });

  test("emitNotification delivers nothing when the name is unknown", () => {
    const fake = new FakeClient();
    const cb = vi.fn();
    fake.onNotification(cb);
    expect(() => fake.emitNotification(RENAMED)).toThrow();
    expect(cb).not.toHaveBeenCalled();
  });

  test("emitNotification still delivers every notification in the generated catalog", () => {
    const fake = new FakeClient();
    const cb = vi.fn();
    fake.onNotification(cb);
    const started = {
      method: "thread/started",
      params: { threadId: "thr_a", ref: "ref_a" },
    } as unknown as AnyNotification;
    fake.emitNotification(started);
    expect(cb).toHaveBeenCalledWith(started);
  });

  // The opt-out is deliberately not symmetrical with emitNotification: it
  // refuses a name the hub DOES send, so it cannot be reached for as a quiet
  // way to silence the guard above after a rename.
  test("emitUnknownNotification delivers a name outside the catalog", () => {
    const fake = new FakeClient();
    const cb = vi.fn();
    fake.onNotification(cb);
    fake.emitUnknownNotification({ method: "totally/unknown", params: { ref: "ref_a" } });
    expect(cb).toHaveBeenCalledWith({ method: "totally/unknown", params: { ref: "ref_a" } });
  });

  test("emitUnknownNotification refuses a name the hub really does send", () => {
    const fake = new FakeClient();
    expect(() => fake.emitUnknownNotification({ method: "thread/started", params: {} })).toThrow(
      /"thread\/started" is a real notification.*emitNotification/s,
    );
  });
});

describe("FakeClient ready handoff", () => {
  test("delivers each exact initialize result to ready handlers", () => {
    const fake = new FakeClient();
    const first: InitializeResponse = {
      ...FAKE_INITIALIZE_RESULT,
      navigation: { version: 1, generationId: "a", sequence: 0 },
    };
    const second: InitializeResponse = {
      ...first,
      navigation: { version: 1, generationId: "b", sequence: 0 },
    };
    const ready = vi.fn();
    fake.onReady(ready);

    fake.emitStateChange("reconnecting");
    fake.emitReady(first);
    fake.emitStateChange("reconnecting");
    fake.emitReady(second);

    expect(ready).toHaveBeenNthCalledWith(1, first);
    expect(ready).toHaveBeenNthCalledWith(2, second);
  });
});

// AppwireClient.resumeThread forces a reconnect and runs beforeRequest only
// after it settles, synchronously before the resume RPC (../client.ts) — which
// is the whole reason a Stop that lands during that reconnect can cancel the
// resume. A fake that ran the guard synchronously inside its own call let a
// test stage exactly that Stop and still pass: a green test about an ordering
// production never has.
describe("FakeClient resume ordering", () => {
  test("resumeThread runs beforeRequest after the transport settles, so a cancellation landing first is observed", async () => {
    const fake = new FakeClient();
    let canceled = false;
    const guard = vi.fn(() => {
      if (canceled) throw new Error("Stop canceled this pending action; send again when ready.");
    });
    const resume = fake.resumeThread("ref_a", { beforeRequest: guard });
    // The window the real client's `await connected` gives its caller: the Stop
    // that lands here is the one the guard has to see.
    canceled = true;
    await expect(resume).rejects.toThrow("Stop canceled this pending action");
    // A guarded-out resume must not send the RPC - the guard's whole point.
    expect(fake.calls.filter((call) => call.method === "thread/resume")).toEqual([]);
  });

  test("resumeThread runs beforeRequest before the resume RPC and after the caller can act", async () => {
    const fake = new FakeClient();
    const order: string[] = [];
    fake.on("thread/resume", () => {
      order.push("resume RPC");
      return undefined as never;
    });
    const resume = fake.resumeThread("ref_a", { beforeRequest: () => order.push("beforeRequest") });
    order.push("caller");
    await resume;
    expect(order).toEqual(["caller", "beforeRequest", "resume RPC"]);
  });

  // A close during the reconnect await rejects with ConnectionClosedError and
  // never runs beforeRequest, exactly like AppwireClient.resumeThread's own
  // `connected` promise (it rejects on the "closed" transition). Falling
  // through instead would run a guard after the connection is gone and then
  // reject with a generic closed-state error production never produces.
  test("resumeThread rejects with ConnectionClosedError before beforeRequest when the client closes during the wait", async () => {
    const fake = new FakeClient();
    const guard = vi.fn();
    const resume = fake.resumeThread("ref_a", { beforeRequest: guard });
    fake.close();
    await expect(resume).rejects.toBeInstanceOf(ConnectionClosedError);
    expect(guard).not.toHaveBeenCalled();
  });

  // A client already closed before the call takes AppwireClient.resumeThread's
  // plain not-ready Error, not ConnectionClosedError: callers classify the
  // latter as hub-unreachable, a branch production never takes for this path.
  test("resumeThread on an already-closed client throws the real client's plain not-ready error", async () => {
    const fake = new FakeClient();
    fake.close();
    const err = await fake.resumeThread("ref_a").catch((e: unknown) => e);
    expect(err).toBeInstanceOf(Error);
    expect(err).not.toBeInstanceOf(ConnectionClosedError);
    expect((err as Error).message).toMatch(/Connect to the hub before resuming this session/);
  });
});

// The gate's own contract: it holds each call to `method` until the test
// settles it, either way, one handle per call. The suites that need to answer
// or fail a request out of order (storeLifecycle's gatedWrite, launchLayer's
// gated read) turn on exactly this, so it is pinned here rather than only
// through them.
describe("FakeClient request gates", () => {
  function gateAt(gates: Settlement[], index: number): Settlement {
    const gate = gates[index];
    if (!gate) throw new Error(`expected a gate at index ${index}`);
    return gate;
  }

  test("resolves the request in flight with the value it is handed", async () => {
    const fake = new FakeClient();
    const gates = gateSettlements(fake, "thread/read");
    const reply = { ref: "ref_a" } as never;
    const inflight = fake.request("thread/read", { ref: "ref_a", includeTurns: false });
    await Promise.resolve();

    gateAt(gates, 0).resolve(reply);
    await expect(inflight).resolves.toBe(reply);
  });

  test("rejects the request in flight with the error it is handed", async () => {
    const fake = new FakeClient();
    const gates = gateSettlements(fake, "thread/read");
    const boom = new Error("boom");
    const inflight = fake.request("thread/read", { ref: "ref_a", includeTurns: false });
    await Promise.resolve();

    gateAt(gates, 0).reject(boom);
    await expect(inflight).rejects.toBe(boom);
  });

  test("hands back one gate per call, settleable out of order", async () => {
    const fake = new FakeClient();
    const gates = gateSettlements(fake, "thread/read");
    const first = fake.request("thread/read", { ref: "ref_a", includeTurns: false });
    const second = fake.request("thread/read", { ref: "ref_b", includeTurns: false });
    await Promise.resolve();

    gateAt(gates, 1).resolve("second" as never);
    gateAt(gates, 0).resolve("first" as never);
    await expect(first).resolves.toBe("first");
    await expect(second).resolves.toBe("second");
  });
});

// The subscriber surface a teardown/unmount test reads: a count that falls
// when a returned unsubscribe runs, and a close() that drives the client to
// "closed". Without a count, a leak assertion can only say a callback did NOT
// fire - which a listener that was never registered also satisfies; without a
// close(), a fake cannot model the teardown a hook performs on its connection.
describe("FakeClient listener lifecycle", () => {
  test("listenerCount counts every registered listener and drops on unsubscribe", () => {
    const fake = new FakeClient();
    expect(fake.listenerCount).toBe(0);
    const offState = fake.onStateChange(vi.fn());
    const offReady = fake.onReady(vi.fn());
    const offNotification = fake.onNotification(vi.fn());
    expect(fake.listenerCount).toBe(3);
    offState();
    offReady();
    offNotification();
    expect(fake.listenerCount).toBe(0);
  });

  test("listenerCount falls when one listener unsubscribes while others stay", () => {
    const fake = new FakeClient();
    const off = fake.onStateChange(vi.fn());
    fake.onStateChange(vi.fn());
    expect(fake.listenerCount).toBe(2);
    off();
    expect(fake.listenerCount).toBe(1);
  });

  test("close() moves the client to closed and notifies state-change listeners", () => {
    const fake = new FakeClient();
    const seen: ConnectionState[] = [];
    fake.onStateChange((s) => seen.push(s));
    fake.close();
    expect(fake.state).toBe("closed");
    expect(seen).toEqual(["closed"]);
  });

  test("close() is idempotent: a second close fires no further transition", () => {
    const fake = new FakeClient();
    const cb = vi.fn();
    fake.onStateChange(cb);
    fake.close();
    fake.close();
    expect(fake.state).toBe("closed");
    expect(cb).toHaveBeenCalledTimes(1);
  });

  // close() tears down the transport, not the subscription list: a real
  // close() leaves listeners attached until their own unsubscribers run, so a
  // fake that dropped them would let a leaked-listener assertion pass for the
  // wrong reason.
  test("close() leaves listeners registered", () => {
    const fake = new FakeClient();
    fake.onStateChange(vi.fn());
    fake.close();
    expect(fake.listenerCount).toBe(1);
  });

  // Fidelity with AppwireClient.request's ready-gate: a NEW request issued
  // after close() rejects with the same plain "cannot call ... while state is
  // closed" Error production produces. ConnectionClosedError is reserved for
  // the close()-driven failures (an in-flight request/connect, or connect() on
  // a closed client), exactly as in the real client.
  test("close() gates a new request through the ready-gate, not ConnectionClosedError", async () => {
    const fake = new FakeClient();
    fake.close();
    await expect(fake.request("thread/read", { ref: "ref_a", includeTurns: false })).rejects.toThrow(
      /state is "closed"/,
    );
  });

  // AppwireClient's closed state is terminal - its private setState is only
  // reached through isClosed()-guarded paths - so an injection after close
  // must not resurrect a client production would keep unusable.
  test("closed is terminal: a later injection cannot resurrect the client", async () => {
    const fake = new FakeClient();
    fake.close();
    fake.emitStateChange("ready");
    fake.emitReady();
    expect(fake.state).toBe("closed");
    await expect(fake.request("thread/read", { ref: "ref_a", includeTurns: false })).rejects.toThrow(
      /state is "closed"/,
    );
  });

  // A getter on `then` (or a `then` method) can throw; the scheduled callback
  // has to turn that into a rejection rather than leave the caller's promise
  // pending forever.
  test("a request result whose then getter throws rejects instead of hanging", async () => {
    const fake = new FakeClient();
    // Not an object literal with a `then` property (biome's noThenProperty
    // rules that out): a Proxy whose get trap throws models the same hostile
    // thenable - any read of `then` raises.
    const hostile = new Proxy(
      {},
      {
        get() {
          throw new Error("bad then");
        },
      },
    );
    fake.on("thread/read", () => hostile as never);
    await expect(fake.request("thread/read", { ref: "ref_a", includeTurns: false })).rejects.toThrow(/bad then/);
  });

  test("a connect result whose then getter throws rejects instead of hanging", async () => {
    const fake = new FakeClient();
    const hostile = new Proxy(
      {},
      {
        get() {
          throw new Error("bad then");
        },
      },
    );
    fake.scriptConnect(() => hostile as never);
    await expect(fake.connect()).rejects.toThrow(/bad then/);
  });
});

// close() is not just a state flip: AppwireClient.close() aborts an in-flight
// handshake and fails every pending request, and a fake that only changed
// state would let a deferred handler settle after the close - exactly the
// lifecycle bug a teardown test is meant to catch.
describe("FakeClient close lifecycle", () => {
  test("connect() rejects immediately when the client is already closed", async () => {
    const fake = new FakeClient();
    fake.close();
    await expect(fake.connect()).rejects.toBeInstanceOf(ConnectionClosedError);
  });

  test("close() aborts a connect still in flight", async () => {
    const fake = new FakeClient();
    let release!: (value: InitializeResponse) => void;
    fake.scriptConnect(
      () =>
        new Promise<InitializeResponse>((resolve) => {
          release = resolve;
        }),
    );
    const connecting = fake.connect();
    await Promise.resolve();
    fake.close();
    await expect(connecting).rejects.toBeInstanceOf(ConnectionClosedError);
    // A handler that settles after the close must not resurrect the connect.
    release(FAKE_INITIALIZE_RESULT);
    await expect(connecting).rejects.toBeInstanceOf(ConnectionClosedError);
  });

  test("close() fails a request already in flight instead of letting it resolve", async () => {
    const fake = new FakeClient();
    const release = deferRequest<string>(fake, "thread/read");
    const inflight = fake.request("thread/read", { ref: "ref_a", includeTurns: false });
    await Promise.resolve();
    fake.close();
    await expect(inflight).rejects.toBeInstanceOf(ConnectionClosedError);
    // A gate released after the close must not settle the request.
    release("late");
    await expect(inflight).rejects.toBeInstanceOf(ConnectionClosedError);
  });

  // The injection seam is the other public path into the now-terminal
  // "closed" state, and every real path into it fails pending work first. It
  // models a transport/server drop, so it uses the plain Error production's
  // handleSocketLoss does - ConnectionClosedError is close()'s alone. A gate
  // released after the transition must not settle the request, and the
  // terminal guard means a later close() cannot repair the pending set.
  test('emitStateChange("closed") fails in-flight work with a transport error', async () => {
    const fake = new FakeClient();
    const release = deferRequest<string>(fake, "thread/read");
    const inflight = fake.request("thread/read", { ref: "ref_a", includeTurns: false });
    await Promise.resolve();
    fake.emitStateChange("closed");
    // A gate released after the terminal transition must not settle it.
    release("late");
    const err = await inflight.catch((e: unknown) => e);
    expect(err).toBeInstanceOf(Error);
    expect(err).not.toBeInstanceOf(ConnectionClosedError);
    expect((err as Error).message).toMatch(/socket closed/);
  });

  // AppwireClient.forceStop's own isClosed() check rejects with
  // ConnectionClosedError, before it builds a recovery client. A generic
  // closed-state error here would misclassify force-stop on a dead client.
  test("forceStop on a closed client rejects with ConnectionClosedError", async () => {
    const fake = new FakeClient();
    fake.close();
    await expect(fake.forceStop("ref_a")).rejects.toBeInstanceOf(ConnectionClosedError);
  });

  // AppwireClient.setState isolates a throwing subscriber so it cannot abort
  // the transition or the operation that triggered it - close() included.
  test("a throwing state-change listener does not stop later listeners or close()", () => {
    const fake = new FakeClient();
    const later = vi.fn();
    fake.onStateChange(() => {
      throw new Error("bad subscriber");
    });
    fake.onStateChange(later);
    expect(() => fake.close()).not.toThrow();
    expect(fake.state).toBe("closed");
    expect(later).toHaveBeenCalledWith("closed");
  });

  test("a throwing ready listener does not abort the ready transition", () => {
    // Start short of ready: the default fake is already ready, and
    // emitStateChange dedupes a transition to the state it is already in.
    const fake = new FakeClient("connecting");
    const later = vi.fn();
    fake.onReady(() => {
      throw new Error("bad subscriber");
    });
    fake.onReady(later);
    expect(() => fake.emitReady(FAKE_INITIALIZE_RESULT)).not.toThrow();
    expect(later).toHaveBeenCalledWith(FAKE_INITIALIZE_RESULT);
  });

  test("a throwing notification listener does not stop dispatch to the rest", () => {
    const fake = new FakeClient();
    const later = vi.fn();
    fake.onNotification(() => {
      throw new Error("bad subscriber");
    });
    fake.onNotification(later);
    const started = {
      method: "thread/started",
      params: { threadId: "thr_a", ref: "ref_a" },
    } as unknown as AnyNotification;
    expect(() => fake.emitNotification(started)).not.toThrow();
    expect(later).toHaveBeenCalledWith(started);
  });
});
