// @vitest-environment node

import { capability, manifest } from "@evener/appwire-client/testing/navigation";
import { expect, test } from "vitest";
import { buildShutdownConvergence } from "./shutdownConvergence";
import { navigationStore, resetNavigationStoreForTests } from "./store";

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
  expect(convergence.matchesSession({ targets: [{ kind: "all_loaded_projects" }] } as never)).toBe(true);
  expect(convergence.matchesSession({ targets: [{ kind: "pin_section", sectionId: "pins" }] } as never)).toBe(true);
  expect(convergence.matchesSession({ targets: [{ kind: "pin_section", sectionId: "other" }] } as never)).toBe(false);
});

test("shutdown factory omits absent pin and project scopes", () => {
  const convergence = buildShutdownConvergence("local:s1", {});
  expect(convergence.targets).toEqual([
    { kind: "section", section: "live" },
    { kind: "section", section: "needs_you" },
  ]);
});

test("the shutdown predicate follows the shared target rule for every scope", () => {
  const unscoped = buildShutdownConvergence("local:s1", {});
  // The wildcard names every loaded project, so it converges a shutdown only
  // when that shutdown has a project scope to reach.
  expect(unscoped.matchesSession({ targets: [{ kind: "all_loaded_projects" }] } as never)).toBe(false);
  // A section outside the live/needs_you tiers is not a shutdown target.
  expect(unscoped.matchesSession({ targets: [{ kind: "section", section: "archived" }] } as never)).toBe(false);
  expect(unscoped.matchesSession({ targets: [{ kind: "section", section: "needs_you" }] } as never)).toBe(true);
  // Catalogs and the manifest name no scope this shutdown covers.
  expect(unscoped.matchesSession({ targets: [{ kind: "manifest" }] } as never)).toBe(false);
  expect(unscoped.matchesSession({ targets: [{ kind: "pin_catalog" }] } as never)).toBe(false);
  expect(unscoped.matchesSession({ targets: [{ kind: "catalog", catalog: "projects" }] } as never)).toBe(false);
});

test("shutdown settled is true when the ref is absent from live rows", () => {
  resetNavigationStoreForTests();
  navigationStore.setState({
    mode: "v3",
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

test("a waiter armed before initialization converges once navigation is v2", async () => {
  resetNavigationStoreForTests();
  // Navigation uninitialized (no revalidator): arming rejects.
  const convergence = buildShutdownConvergence("local:s1", {});
  const waiter = convergence.arm();
  await expect(waiter.promise).rejects.toThrow("navigation is not initialized");
  // Initialization lands before convergence begins: mode is v2 now, but the
  // armed waiter is still the rejected one. Converging must treat it as a
  // successful no-op, not surface a shutdown failure.
  navigationStore.setState({ mode: "v3", clientGenerationID: "generation_test" });
  await expect(convergence.converge(waiter)).resolves.toBeUndefined();
});
