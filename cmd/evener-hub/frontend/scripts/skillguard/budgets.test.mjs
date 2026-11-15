// Pins the skillguard driver's load-aware wait budgets.
//
// The driver's waits are hang tripwires, not the pacing mechanism: each one is
// released by the app's own next event, and the budget only decides how long a
// silent page is a wedged page. A FIXED budget trips a correct-but-slow
// reaction on a loaded machine (CI run 35486854632 tripped the 30s
// continuation wait against a correct daemon), so the budget scales with the
// slowest reaction the run has already observed -- the daemon-retirement
// watchdog fix (#2255) applied to this driver.
//
// The proof below drives the REAL waitPage loop under vitest's fake clock. The
// page is scripted to hold the awaited condition for a fixed reaction time, so
// a slow-but-correct step and a wait that must absorb it are both deterministic
// and neither needs a browser.

import { afterEach, describe, expect, test, vi } from "vitest";

import { ReactionBudget, scaledDeadlineMs } from "./budgets.mjs";
import { Driver, EVALUATE_FAILURE_FAIL_FAST_MS } from "./run.mjs";

afterEach(() => {
  vi.useRealTimers();
});

describe("scaledDeadlineMs", () => {
  test("keeps the wait's own budget while nothing slow has been observed", () => {
    expect(scaledDeadlineMs(30_000, 0)).toBe(30_000);
    expect(scaledDeadlineMs(15_000, 0)).toBe(15_000);
  });

  test("widens with the slowest observed reaction", () => {
    expect(scaledDeadlineMs(30_000, 5_000)).toBe(50_000);
    expect(scaledDeadlineMs(15_000, 1_000)).toBe(15_000);
  });

  test("never exceeds the ceiling, whatever the observation", () => {
    expect(scaledDeadlineMs(30_000, 12_000)).toBe(120_000);
    expect(scaledDeadlineMs(30_000, 600_000)).toBe(120_000);
    expect(scaledDeadlineMs(15_000, 9_999_999)).toBe(60_000);
  });
});

describe("ReactionBudget", () => {
  test("remembers the slowest reaction and ignores a faster one after it", () => {
    const budget = new ReactionBudget();
    budget.observe(4_000);
    budget.observe(9_000);
    budget.observe(1_000);
    expect(budget.slowestMs).toBe(9_000);
    expect(budget.deadline(30_000)).toBe(90_000);
  });

  test("ignores observations that are not finite positive durations", () => {
    const budget = new ReactionBudget();
    budget.observe(Number.NaN);
    budget.observe(-5);
    budget.observe(undefined);
    expect(budget.slowestMs).toBe(0);
    expect(budget.deadline(30_000)).toBe(30_000);
  });
});

// A driver whose page holds the awaited condition for `reactAt` ms from now.
// `send` answers Runtime.evaluate the way the CDP wire does, so the REAL
// waitPage poll loop runs against the fake clock.
function scriptedDriver() {
  const driver = new Driver({ url: "http://127.0.0.1/", artifactDir: "", controlPath: "", milestonePath: "" });
  let readyAt = Number.POSITIVE_INFINITY;
  driver.page = {
    send: async () => ({ result: { result: { value: Date.now() >= readyAt ? true : null } } }),
  };
  return {
    driver,
    reactAt(ms) {
      readyAt = Date.now() + ms;
    },
  };
}

describe("waitPage budgets under an injected slow reaction", () => {
  // Drive one slow-but-correct step to completion so the driver has an
  // observation to size the next wait's budget from.
  async function establishSlowStep({ driver, reactAt }) {
    reactAt(80);
    const earlier = driver.waitPage("true", { timeoutMs: 200, label: "earlier slow-but-correct step" });
    await vi.advanceTimersByTimeAsync(500);
    await expect(earlier).resolves.toBe(true);
  }

  test("absorbs a slow-but-correct reaction once a slower step has been observed", async () => {
    vi.useFakeTimers();
    const { driver, reactAt } = scriptedDriver();
    await establishSlowStep({ driver, reactAt });
    // The machine owes us this reaction; with a fixed 200ms budget the wait
    // would trip on correct work.
    reactAt(400);
    const later = driver.waitPage("true", { timeoutMs: 200, label: "later reaction" });
    const resolved = expect(later).resolves.toBe(true);
    await vi.advanceTimersByTimeAsync(1_500);
    await resolved;
  });

  test("still trips when the reaction outruns the ceiling, so a wedged page is not waited on forever", async () => {
    vi.useFakeTimers();
    const { driver, reactAt } = scriptedDriver();
    await establishSlowStep({ driver, reactAt });
    reactAt(60_000);
    const wedged = driver.waitPage("true", { timeoutMs: 200, label: "wedged page" });
    const rejected = expect(wedged).rejects.toThrow(/timed out after 800ms/);
    await vi.advanceTimersByTimeAsync(2_000);
    await rejected;
  });
});

describe("waitPage rejected-poll handling", () => {
  test("a page that rejects every poll fails on the rejection, before the missing-condition budget", async () => {
    vi.useFakeTimers();
    const { driver } = scriptedDriver();
    const transportSentinel = new Error("Runtime.evaluate transport sentinel");
    driver.page.send = async () => {
      throw transportSentinel;
    };
    const stopped = driver.waitPage("true", { timeoutMs: 15_000, label: "the panel" });
    const settled = vi.fn();
    void stopped.then(settled, settled);
    // Just inside the fail-fast bound the page is still merely unobserved.
    await vi.advanceTimersByTimeAsync(EVALUATE_FAILURE_FAIL_FAST_MS - 200);
    expect(settled).not.toHaveBeenCalled();
    // The real wait rejects on the rejection itself, long before 15s.
    await vi.advanceTimersByTimeAsync(400);
    expect(settled).toHaveBeenCalledTimes(1);
    await expect(stopped).rejects.toThrow(/Runtime\.evaluate transport sentinel/);
  });

  test("an isolated rejected poll is unobserved time, not a stopped page", async () => {
    vi.useFakeTimers();
    const { driver } = scriptedDriver();
    let polls = 0;
    driver.page.send = async () => {
      polls += 1;
      if (polls === 2) throw new Error("one CDP hiccup");
      return { result: { result: { value: polls >= 3 ? true : null } } };
    };
    const waited = driver.waitPage("true", { timeoutMs: 15_000, label: "recovers after one hiccup" });
    await vi.advanceTimersByTimeAsync(500);
    await expect(waited).resolves.toBe(true);
  });

  test("a missing condition still times out, and names the failed polls it saw", async () => {
    vi.useFakeTimers();
    const { driver } = scriptedDriver();
    const transportSentinel = new Error("Runtime.evaluate transport sentinel");
    let polls = 0;
    driver.page.send = async () => {
      polls += 1;
      if (polls % 2 === 0) throw transportSentinel;
      return { result: { result: { value: null } } };
    };
    const missing = driver.waitPage("true", { timeoutMs: 300, label: "the absent panel" });
    const settled = vi.fn();
    void missing.then(settled, settled);
    await vi.advanceTimersByTimeAsync(1_000);
    expect(settled).toHaveBeenCalledTimes(1);
    await expect(missing).rejects.toThrow(/Runtime\.evaluate transport sentinel/);
  });
});
