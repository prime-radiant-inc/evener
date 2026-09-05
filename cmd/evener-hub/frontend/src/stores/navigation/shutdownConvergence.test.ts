import { expect, test } from "vitest";
import { buildShutdownConvergence } from "./shutdownConvergence";
import { navigationStore, resetNavigationStoreForTests } from "./store";
import { capability, manifest } from "./testing";

test("shutdown factory builds session-scoped targets and predicate", () => {
  const convergence = buildShutdownConvergence("local:s1", { pinSectionId: "pins", projectKey: "p1" });
  expect(convergence.targets).toEqual([
    { kind: "section", section: "live" },
    { kind: "section", section: "needs_you" },
    { kind: "pin_section", sectionId: "pins" },
    { kind: "project", projectKey: "p1" },
  ]);
  expect(convergence.matchesSession({ targets: [{ kind: "project", projectKey: "p1" }] } as never)).toBe(true);
  expect(convergence.matchesSession({ targets: [{ kind: "project", projectKey: "other" }] } as never)).toBe(false);
  expect(convergence.matchesSession({ targets: [{ kind: "section", section: "live" }] } as never)).toBe(true);
});

test("shutdown factory omits absent pin and project scopes", () => {
  const convergence = buildShutdownConvergence("local:s1", {});
  expect(convergence.targets).toEqual([
    { kind: "section", section: "live" },
    { kind: "section", section: "needs_you" },
  ]);
});

test("shutdown settled is true when the ref is absent from live rows", () => {
  resetNavigationStoreForTests();
  navigationStore.setState({
    mode: "v2",
    capability: capability(),
    clientGenerationID: "generation_test",
    manifest: {
      key: { kind: "manifest" },
      data: manifest(),
      loadedRevision: 1,
      targetRevision: 1,
      forceToken: 0,
      etag: "e",
      loading: false,
      stale: false,
      error: null,
      generationID: "generation_test",
    },
    resources: new Map(),
  });
  const convergence = buildShutdownConvergence("local:gone", {});
  expect(convergence.sessionSettled()).toBe(true);
});
