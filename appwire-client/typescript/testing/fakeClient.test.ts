// @vitest-environment node

// FakeClient's own contract tests. The behavior under test here is the one
// that keeps the rest of the suite honest: a scripted or requested method
// name, and an injected notification's method, must exist in the hub's real
// catalogs (METHOD_NAMES / NOTIFICATION_NAMES in ../types.gen.ts), so a wire
// rename can no longer leave a test green against a production build that
// calls — or listens for — a name the hub stopped serving.
import { describe, expect, test, vi } from "vitest";
import type { AnyNotification, InitializeResponse, MethodName } from "../types.gen";
import { FakeClient, gateSettlements, type Settlement } from "./fakeClient";
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
