import { cleanup } from "@testing-library/react";
import * as React from "react";
import { afterAll, afterEach, beforeAll } from "vitest";
import { reactActScopeGuardPorts, waitOutLeakedActScope } from "./testActScopeGuard";
import { guardConsoleOutput } from "./testConsoleGuard";

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

// Guarded here at module scope, before the test file loads, so a file that
// captures console.error at its own module scope captures the guarded method.
// This file's hooks register first, so they run after the test file's own
// afterEach and afterAll: output from its cleanup (unmounts) counts too.
const consoleGuard = guardConsoleOutput(console);

// What the act-scope guard below found for the test that just ran. The check
// after it reports these instead of the guard throwing them: vitest runs a
// file's afterEach hooks in one loop, so a throw from one hook skips the rest,
// and the unmount and the check must still run for that test.
const actScopeFailures: unknown[] = [];

// Registered first, so it runs last of all: it fails the test on what it left
// behind, which is an act() scope it left open, errors from work that ran in
// that scope, and console output.
function failOnTestLeftovers() {
  const failures = actScopeFailures.splice(0);
  const unexpectedOutput = consoleGuard.takeUnexpectedOutput();
  if (unexpectedOutput !== undefined) failures.push(new Error(unexpectedOutput));
  if (failures.length === 1) throw failures[0];
  // Vitest reports each error an AggregateError holds as a failure of its own.
  if (failures.length > 1) throw new AggregateError(failures);
}

afterEach(failOnTestLeftovers);
afterAll(failOnTestLeftovers);

// Testing Library registers its automatic unmount only when vitest exposes a
// global afterEach, which it does not here, so a test's rendered trees and
// hooks would otherwise stay mounted into the next test. Registered after the
// leftovers check so it runs before it: output from the unmount counts.
afterEach(cleanup);

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
// next test would run inside it (testActScopeGuard.ts). Registered last, so it
// runs first of this file's hooks: whatever the scope's work does as it closes
// happens before the leftovers check, which reports it on this test along with
// what the guard found. The timers and clock are captured here, before any
// test can fake them. A scope still open after the bound is reported once, and
// later tests are not blamed for it until it has closed. React keeps the open
// scope's queue on its internals; if an upgrade drops it, this throws rather
// than guarding nothing.
if (!("actQueue" in reactInternals)) {
  throw new Error("React no longer exposes actQueue; update or remove the act-scope guard in testSetup.ts.");
}
const actInternals = reactInternals as { actQueue: unknown };
const actScopeGuard = reactActScopeGuardPorts(actInternals, React.act, (error) => actScopeFailures.push(error));
let stuckActScopeReported = false;
afterEach(async () => {
  if (stuckActScopeReported && actScopeGuard.actScopeOpen()) return;
  stuckActScopeReported = false;
  const leak = await waitOutLeakedActScope(actScopeGuard, 20_000);
  if (leak === undefined) return;
  actScopeFailures.push(new Error(leak));
  stuckActScopeReported = actScopeGuard.actScopeOpen();
});
