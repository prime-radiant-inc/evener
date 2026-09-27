import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { expect, test } from "vitest";
import { currentTestMarkerForTests, stopMarkingTests } from "./testEndedBodyGuard";

function markerInAFreshAsyncContext(): Promise<unknown> {
  return new Promise((resolve) => setTimeout(() => resolve(currentTestMarkerForTests()), 0));
}

// Stopping reaches no further than this test: the next test's run marks it
// afresh.
test("once the marking stops, the guard's storage carries no marker, even into a fresh async context", async () => {
  expect(
    await markerInAFreshAsyncContext(),
    "the setup marks the test, and the marker reaches its timers",
  ).toBeDefined();
  stopMarkingTests();
  expect(await markerInAFreshAsyncContext()).toBeUndefined();
});

// What the stop saves shows only in the files a worker runs later, which no
// test in this file can see, so this pins where the setup file makes it.
test("the setup file stops the marking once a file's tests and hooks are done", () => {
  const setup = readFileSync(join(dirname(fileURLToPath(import.meta.url)), "testSetup.ts"), "utf8");
  expect(setup).toMatch(/^afterAll\(stopMarkingTests\);$/m);
});
