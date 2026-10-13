// @vitest-environment node
// Pins the in-page wait mechanism the sessioncacheguard's five waits now run on.
//
// The waits used to poll from Node: every tick re-issued one Runtime.evaluate
// per context, so a wait that never settles burned ~250 CDP round trips on its
// 5s deadline. They now poll the SAME predicate at the SAME 20ms cadence on the
// SAME 5s deadline inside the page, as one awaited Runtime.evaluate per context.
//
// These cases drive the REAL pollInPage expression - the string the guard sends
// to Chrome - against a Runtime.evaluate that answers it here, on a fake clock,
// so the loop's accept / early-return / deadline behavior is deterministic and
// pinned without a browser. The deadline MESSAGE is copy a person reads: it is
// reviewed in the diff, never asserted here (docs/developing-evener/testing.md,
// "Copy Is Not a Test Oracle").

import { afterEach, describe, expect, test, vi } from "vitest";

import { POLL_MS, pollInPage, WAIT_MS } from "./inPageWait.mjs";
import { SOURCE_DISCOVERED, SUPPRESSED } from "./predicates.mjs";

// Answer Runtime.evaluate the way the CDP wire does: the awaited value of the
// expression, in the response shape evaluate() reads.
const evaluatingSend = (run) => async (method, params) => {
  if (method !== "Runtime.evaluate") throw new Error(`unexpected ${method}`);
  return { result: { result: { value: await run(params.expression) } } };
};

// An indirect evaluation in this process's global scope, the way the page's own
// scope resolves the interpolated read/predicate. The page's timers and clock
// are this process's, so the fake clock below drives the real loop.
const send = evaluatingSend((expression) => new Function(`return (${expression});`)());

// Evaluate one of the guard's own predicates against a snapshot shape, the way
// the page would - so a predicate that stops meaning what the guard needs fails
// here, not silently in a browser run.
const evaluatePredicate = (predicate, state) => new Function(`return (${predicate});`)()(state);

const advance = (ms) => vi.advanceTimersByTimeAsync(ms);

afterEach(() => {
  vi.useRealTimers();
  delete globalThis.__reads;
});

describe("the guard's own cadence and deadline", () => {
  test("polls at 20ms on a 5s deadline", () => {
    expect(POLL_MS).toBe(20);
    expect(WAIT_MS).toBe(5000);
  });
});

describe("the guard's own predicates", () => {
  test("SUPPRESSED accepts a suppressed lifetime and nothing else", () => {
    expect(evaluatePredicate(SUPPRESSED, { lifetime: { suppressed: true } })).toBe(true);
    expect(evaluatePredicate(SUPPRESSED, { lifetime: { suppressed: false } })).toBe(false);
    expect(evaluatePredicate(SUPPRESSED, { lifetime: null })).toBe(false);
  });

  test("SOURCE_DISCOVERED needs a non-empty sourceId and a suppressed sibling ref", () => {
    expect(evaluatePredicate(SOURCE_DISCOVERED, { sourceId: "a-tab", lifetime: { suppressed: true } })).toBe(true);
    expect(evaluatePredicate(SOURCE_DISCOVERED, { sourceId: "", lifetime: { suppressed: true } })).toBe(false);
    expect(evaluatePredicate(SOURCE_DISCOVERED, { sourceId: null, lifetime: { suppressed: true } })).toBe(false);
    expect(evaluatePredicate(SOURCE_DISCOVERED, { sourceId: "a-tab", lifetime: null })).toBe(false);
    expect(evaluatePredicate(SOURCE_DISCOVERED, { sourceId: "a-tab", lifetime: { suppressed: false } })).toBe(false);
  });
});

describe("pollInPage", () => {
  test("accepts on the first poll and returns the observed value", async () => {
    vi.useFakeTimers();
    globalThis.__reads = 0;
    const result = await pollInPage(send, `(globalThis.__reads += 1)`, `(n) => n >= 1`, {
      waitMs: 500,
      pollMs: 5,
    });
    expect(result).toEqual({ ok: true, observed: 1 });
    expect(globalThis.__reads).toBe(1);
  });

  test("re-polls the same predicate until it holds", async () => {
    vi.useFakeTimers();
    globalThis.__reads = 0;
    const pending = pollInPage(send, `(globalThis.__reads += 1)`, `(n) => n >= 3`, {
      waitMs: 500,
      pollMs: 5,
    });
    await advance(500);
    await expect(pending).resolves.toEqual({ ok: true, observed: 3 });
    expect(globalThis.__reads).toBe(3);
  });

  test("gives up on the deadline with ok false and the last observed value", async () => {
    vi.useFakeTimers();
    globalThis.__reads = 0;
    const pending = pollInPage(send, `(globalThis.__reads += 1, { at: globalThis.__reads })`, `() => false`, {
      waitMs: 100,
      pollMs: 10,
    });
    await advance(100);
    const result = await pending;
    expect(result.ok).toBe(false);
    expect(result.observed).toEqual({ at: globalThis.__reads });
    expect(globalThis.__reads).toBeGreaterThanOrEqual(2);
  });

  test("reads after the deadline check, so a poll delayed past the deadline still observes the condition", async () => {
    vi.useFakeTimers();
    globalThis.__reads = 0;
    // One poll, delayed to 40ms against a 10ms deadline: the condition holds by
    // the time the delayed poll lands, so the wait must succeed. Checking the
    // deadline before the read would report a false timeout here.
    const pending = pollInPage(send, `(globalThis.__reads += 1, globalThis.__reads)`, `(n) => n >= 2`, {
      waitMs: 10,
      pollMs: 40,
    });
    await advance(200);
    await expect(pending).resolves.toEqual({ ok: true, observed: 2 });
  });
});
