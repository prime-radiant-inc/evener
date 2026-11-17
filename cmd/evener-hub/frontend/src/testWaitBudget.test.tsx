// testSetup.ts's wait deadline: a Testing Library wait inside a test is bounded
// by that test's own vitest timeout, not by Testing Library's fixed second
// (#5373). Under load an async chain - a store gate, a serial mutation queue, a
// wire round-trip - legitimately sums past a second, and the wait then fails
// while the test still has budget to run.
//
// The waits here run on the test-driven fake clock, so "longer than a second"
// costs no wall time and never depends on how loaded the host is.
import { getConfig, waitFor } from "@testing-library/react";
import { afterAll, afterEach, expect, test, vi } from "vitest";
import { useTestDrivenFakeTimers } from "./testing/fakeTimers";
import { asyncWaitBudgetFor } from "./testWaitBudget";

afterEach(() => {
  vi.useRealTimers();
});

// A condition that only holds after `milliseconds` of the test's own clock.
function arrivesAfter(milliseconds: number): () => boolean {
  let arrived = false;
  setTimeout(() => {
    arrived = true;
  }, milliseconds);
  return () => arrived;
}

// Both tests declare their own budget, so neither reads a value the repo does
// not pin: vite.config.ts sets no testTimeout, and a default that changed
// later must not silently move what these expect.
test("a wait whose chain outlives a second still passes, because the test's own budget bounds it", async () => {
  useTestDrivenFakeTimers();
  expect(getConfig().asyncUtilTimeout).toBe(14_000);
  const arrived = arrivesAfter(12_000);

  await waitFor(() => expect(arrived()).toBe(true));
}, 15_000);

// 9_000 derives 8_000, which is neither the 14_000 the test before it held nor
// the 4_000 a default budget would derive: reading it proves the deadline came
// from this test's own task.
test("the test after it reads its own budget again, not the one before it", () => {
  expect(getConfig().asyncUtilTimeout).toBe(8_000);
}, 9_000);

// afterAll runs outside the aroundEach that installs a test's budget, so this
// reads the value the restore put back - not any test's. Without the restore the
// last test's 8_000 would still be in force, and this fails.
afterAll(() => {
  expect(getConfig().asyncUtilTimeout).toBe(1_000);
});

test("the budget is the test's own timeout less a margin, floored at Testing Library's default", () => {
  // Undefined is a caller with no test task behind it; zero and Infinity are
  // the runner's "no deadline". A default that stands is the rule for all
  // three, so none reads as an enormous budget.
  expect(asyncWaitBudgetFor(undefined)).toBe(1_000);
  expect(asyncWaitBudgetFor(0)).toBe(1_000);
  expect(asyncWaitBudgetFor(Number.POSITIVE_INFINITY)).toBe(1_000);
  // A test that declares less than the default keeps the window it had.
  expect(asyncWaitBudgetFor(1_200)).toBe(1_000);
  expect(asyncWaitBudgetFor(2_000)).toBe(1_000);
  expect(asyncWaitBudgetFor(1_000)).toBe(1_000);
  expect(asyncWaitBudgetFor(5_000)).toBe(4_000);
  expect(asyncWaitBudgetFor(15_000)).toBe(14_000);
  expect(asyncWaitBudgetFor(60_000)).toBe(59_000);
});
