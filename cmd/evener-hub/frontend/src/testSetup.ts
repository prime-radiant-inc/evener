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
