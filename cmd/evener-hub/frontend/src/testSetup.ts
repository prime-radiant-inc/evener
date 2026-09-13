import { afterAll, beforeAll } from "vitest";

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
