// @vitest-environment node
// Pins the in-page wait mechanism the sessioncacheguard's five waits now run on.
//
// The waits used to poll from Node: every tick re-issued one Runtime.evaluate
// per context, so a wait that never settles burned ~250 CDP round trips on its
// 5s deadline. They now poll the SAME predicate at the SAME 20ms cadence on the
// SAME 5s deadline inside the page, as one awaited Runtime.evaluate per context.
//
// These cases drive the REAL pollInPage expression - the string the guard sends
// to Chrome - against a Runtime.evaluate that answers it here, so the loop's
// accept / early-return / deadline behavior is pinned without a browser.

import { afterEach, describe, expect, test } from "vitest";

import { POLL_MS, pollInPage, WAIT_MS, waitDeadline } from "./inPageWait.mjs";

// Answer Runtime.evaluate the way the CDP wire does: the awaited value of the
// expression, in the response shape evaluate() reads.
const evaluatingSend = (run) => async (method, params) => {
  if (method !== "Runtime.evaluate") throw new Error(`unexpected ${method}`);
  return { result: { result: { value: await run(params.expression) } } };
};

// An indirect evaluation in this process's global scope, the way the page's own
// scope resolves the interpolated read/predicate.
const send = evaluatingSend((expression) => new Function(`return (${expression});`)());

afterEach(() => {
  delete globalThis.__reads;
});

describe("the guard's own cadence and deadline", () => {
  test("polls at 20ms on a 5s deadline", () => {
    expect(POLL_MS).toBe(20);
    expect(WAIT_MS).toBe(5000);
  });

  test("a wait that spent its deadline names the deadline and the last value read", () => {
    expect(
      waitDeadline("clear-epoch suppression: B never suppressed before storage I/O", { sourceId: null }).message,
    ).toBe(
      'clear-epoch suppression: B never suppressed before storage I/O (5000ms deadline); observed {"sourceId":null}',
    );
  });
});

describe("pollInPage", () => {
  test("accepts on the first poll and returns the observed value", async () => {
    globalThis.__reads = 0;
    const result = await pollInPage(send, `(globalThis.__reads += 1)`, `(n) => n >= 1`, {
      waitMs: 500,
      pollMs: 5,
    });
    expect(result).toEqual({ ok: true, observed: 1 });
    expect(globalThis.__reads).toBe(1);
  });

  test("re-polls the same predicate until it holds", async () => {
    globalThis.__reads = 0;
    const result = await pollInPage(send, `(globalThis.__reads += 1)`, `(n) => n >= 3`, {
      waitMs: 500,
      pollMs: 5,
    });
    expect(result).toEqual({ ok: true, observed: 3 });
    expect(globalThis.__reads).toBe(3);
  });

  test("gives up on the deadline with ok false and the last observed value", async () => {
    globalThis.__reads = 0;
    const result = await pollInPage(send, `(globalThis.__reads += 1, { at: globalThis.__reads })`, `() => false`, {
      waitMs: 100,
      pollMs: 10,
    });
    expect(result.ok).toBe(false);
    expect(result.observed).toEqual({ at: globalThis.__reads });
    expect(globalThis.__reads).toBeGreaterThanOrEqual(3);
  });
});
