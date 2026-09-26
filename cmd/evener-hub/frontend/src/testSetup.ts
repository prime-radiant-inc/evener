import * as React from "react";
import { afterAll, beforeAll } from "vitest";

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
