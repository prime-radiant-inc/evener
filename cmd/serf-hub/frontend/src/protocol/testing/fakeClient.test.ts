// FakeClient's own contract tests. The behavior under test here is the one
// that keeps the rest of the suite honest: a scripted or requested method
// name must exist in the hub's real method catalog (METHOD_NAMES in
// ../types.gen.ts), so a wire rename can no longer leave a test green
// against a production build that calls a method the hub stopped serving.
import { describe, expect, test } from "vitest";
import type { MethodName } from "../types.gen";
import { FakeClient } from "./fakeClient";

// A name the hub has never served. Cast because MethodName correctly refuses
// it at compile time — this suite exercises the runtime guard that catches
// the same mistake when the type check has been bypassed (a cast, an `any`,
// a string built at runtime), which is exactly how the serf/dirs/complete
// rename slipped through.
const UNKNOWN = "serf/dirs/complete" as MethodName;

describe("FakeClient method-name validation", () => {
  test("on() rejects a method the hub does not serve", () => {
    const fake = new FakeClient();
    expect(() => fake.on(UNKNOWN, () => undefined as never)).toThrow(
      /FakeClient: unknown method "serf\/dirs\/complete"/,
    );
  });

  test("on() names the generated catalog in its error, so the fix is obvious", () => {
    const fake = new FakeClient();
    expect(() => fake.on(UNKNOWN, () => undefined as never)).toThrow(/METHOD_NAMES/);
  });

  test("on() accepts every method in the generated catalog", () => {
    const fake = new FakeClient();
    expect(() => fake.on("serf/paths/complete", () => ({ data: [] }))).not.toThrow();
    expect(() => fake.on("thread/read", () => undefined as never)).not.toThrow();
  });

  test("request() rejects a method the hub does not serve, without recording the call", async () => {
    const fake = new FakeClient();
    await expect(fake.request(UNKNOWN, {} as never)).rejects.toThrow(
      /FakeClient: unknown method "serf\/dirs\/complete"/,
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
