import { cleanup } from "@testing-library/react";
import * as React from "react";
import { afterAll, aroundEach, beforeAll, beforeEach } from "vitest";
import { reactActScopeGuardPorts, waitOutLeakedActScope } from "./testActScopeGuard";
import { guardConsoleOutput } from "./testConsoleGuard";
import { guardTestingLibraryAgainstEndedBodies, runAsTheCurrentTest, stopMarkingTests } from "./testEndedBodyGuard";
import { unmountEveryTree } from "./testUnmount";

// Every test file must get its own VM context: stores, pane registrations and
// module mocks are module-scoped. Vitest's vmThreads pool gives each file one
// unless it has a single worker, in which case it batches every file into ONE
// shared context and the suite fails with baffling cross-file leaks. This
// setup file runs once per test file, so finding our own marker means another
// file already ran in this context.
const contextMarker = globalThis as typeof globalThis & { __evenerTestContextInUse?: true };
if (contextMarker.__evenerTestContextInUse) {
  throw new Error(
    "Another test file already ran in this VM context. Run vitest with at least two workers (--maxWorkers=2): with one, the vmThreads pool shares a single context across files.",
  );
}
contextMarker.__evenerTestContextInUse = true;

// jsdom never implemented Element.scrollIntoView; production code calls it on
// real browsers (the rail's reveal scrolls the revealed row to center). The
// scroll is presentation, never load-bearing, so a no-op keeps tests honest
// without masking behavior.
if (typeof Element !== "undefined" && !Element.prototype.scrollIntoView) {
  Element.prototype.scrollIntoView = () => {};
}

// Guarded here at module scope, before the test file loads, so a file that
// captures console.error at its own module scope captures the guarded method.
// Each test's teardown below runs after all of that test's afterEach hooks, and
// this file's afterAll hooks register first, so they run after the test file's
// own: output from their cleanup (unmounts) counts too.
const consoleGuard = guardConsoleOutput(console);

// What the act-scope wait and the unmount in a test's teardown found. The check
// at the end of the teardown reports these instead of either step throwing
// them, so the steps after it still run for that test.
const teardownFailures: unknown[] = [];

// Fails the test on what it left behind: an act() scope it left open, errors
// from work that ran in that scope, a tree whose unmount threw, and console
// output. It is the last step of each test's teardown. As an afterAll it is
// registered first, so it runs after every other afterAll.
function failOnTestLeftovers() {
  const failures = teardownFailures.splice(0);
  const unexpectedOutput = consoleGuard.takeUnexpectedOutput();
  if (unexpectedOutput !== undefined) failures.push(new Error(unexpectedOutput));
  if (failures.length === 1) throw failures[0];
  // Vitest reports each error an AggregateError holds as a failure of its own.
  if (failures.length > 1) throw new AggregateError(failures);
}

afterAll(failOnTestLeftovers);

const reactEnvironment = globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT?: boolean };
let previousActEnvironment: boolean | undefined;

// Testing Library cannot register its suite-wide act setup without global
// beforeAll/afterAll hooks. Our tests import their Vitest hooks explicitly.
beforeAll(() => {
  previousActEnvironment = reactEnvironment.IS_REACT_ACT_ENVIRONMENT;
  reactEnvironment.IS_REACT_ACT_ENVIRONMENT = true;
});

afterAll(() => {
  if (previousActEnvironment === undefined) {
    delete reactEnvironment.IS_REACT_ACT_ENVIRONMENT;
  } else {
    reactEnvironment.IS_REACT_ACT_ENVIRONMENT = previousActEnvironment;
  }
});

// A body that outlives its test (vitest cannot stop one that timed out) is
// stopped at its Testing Library waits, and its events are dropped, instead of
// acting on the next test (testEndedBodyGuard.ts). The marking that tells the
// guard which test is running stops once the file is done, so the file's
// storage does not stay live in its worker. A wait a body stopped in had turned
// the act environment off and never turns it back on, so every test starts
// with it on.
guardTestingLibraryAgainstEndedBodies();
aroundEach((runTest, context) => runAsTheCurrentTest(runTest, context.signal));
afterAll(stopMarkingTests);
beforeEach(() => {
  reactEnvironment.IS_REACT_ACT_ENVIRONMENT = true;
});

// React's development build captures an owner stack for every JSX element it
// creates - an Error() plus a console.createTask - up to 10,000 a second. That
// was 7% of the suite's CPU, and it buys only richer component stacks in React
// warnings, which no test reads. React has no switch for it, so the counter
// that caps it is pinned past the cap: every element takes the shared "unknown
// owner" stack instead. If a React upgrade drops the counter, this throws
// rather than silently losing the saving; update or delete this block then.
const reactInternals = (React as unknown as Record<string, Record<string, unknown> | undefined>)
  .__CLIENT_INTERNALS_DO_NOT_USE_OR_WARN_USERS_THEY_CANNOT_UPGRADE;
if (reactInternals === undefined || !("recentlyCreatedOwnerStacks" in reactInternals)) {
  throw new Error(
    "React no longer exposes recentlyCreatedOwnerStacks; update or remove the owner-stack cap in testSetup.ts.",
  );
}
Object.defineProperty(reactInternals, "recentlyCreatedOwnerStacks", {
  get: () => Number.POSITIVE_INFINITY,
  set: () => {},
  configurable: true,
});

// A test that timed out inside act() leaves React's act scope open, and the
// next test would run inside it (testActScopeGuard.ts). The teardown waits it
// out first: whatever the scope's work does as it closes happens before the
// unmount and the leftovers check, which reports it on this test along with
// what the guard found. The timers and clock are captured here, before any
// test can fake them. A scope still open after the bound is reported once, and
// later tests are not blamed for it until it has closed. React keeps the open
// scope's queue on its internals; if an upgrade drops it, this throws rather
// than guarding nothing.
if (!("actQueue" in reactInternals)) {
  throw new Error("React no longer exposes actQueue; update or remove the act-scope guard in testSetup.ts.");
}
const actInternals = reactInternals as { actQueue: unknown };
const actScopeGuard = reactActScopeGuardPorts(actInternals, React.act, (error) => teardownFailures.push(error));
let stuckActScopeReported = false;
async function waitOutAnActScopeLeftOpen(): Promise<void> {
  if (stuckActScopeReported && actScopeGuard.actScopeOpen()) return;
  stuckActScopeReported = false;
  const leak = await waitOutLeakedActScope(actScopeGuard, 20_000);
  if (leak === undefined) return;
  teardownFailures.push(new Error(leak));
  stuckActScopeReported = actScopeGuard.actScopeOpen();
}

// Each test's teardown runs as an onTestFinished, not an afterEach. Vitest runs
// a file's afterEach hooks in one loop, so a throw from any of them skips the
// rest, and as an afterEach the teardown would run after the test file's own.
// Vitest runs finish hooks after every afterEach, even one that threw, and each
// in a try of its own. This file's beforeEach runs before any of the test
// file's, so the teardown is registered before anything else in the test can
// fail.
//
// Testing Library registers its automatic unmount only when vitest exposes a
// global afterEach, which it does not here, so a test's rendered trees and
// hooks would otherwise stay mounted into the next test. The teardown unmounts
// them before the leftovers check, so output from the unmount counts.
beforeEach(({ onTestFinished }) => {
  onTestFinished(async () => {
    await waitOutAnActScopeLeftOpen();
    unmountEveryTree(cleanup, (error) => teardownFailures.push(error));
    failOnTestLeftovers();
  });
});
