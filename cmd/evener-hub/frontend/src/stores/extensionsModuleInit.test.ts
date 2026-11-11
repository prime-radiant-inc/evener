// @vitest-environment node

import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { afterEach, beforeAll, expect, test, vi } from "vitest";

// The module graph's first transform is a setup cost, not the scenario: under
// host load it can exceed the test's 5s deadline even though the import is
// awaited (#5870). Warm it in a hook whose 60s ceiling is a hang tripwire,
// then reset the module registry so the test still evaluates both modules
// fresh against an already-ready client.
beforeAll(async () => {
  await import("./connection");
  await import("./extensions");
  vi.resetModules();
}, 60_000);

afterEach(() => {
  vi.resetModules();
});

// The module is lazily loaded, so it can initialize with a client that is
// already ready: its initial connection pass then runs while the module body
// is still executing.
test("importing the module with a ready client already connected does not throw", async () => {
  vi.resetModules();
  const { connectionStore } = await import("./connection");
  const fake = new FakeClient("ready");
  fake.on("evener/plugin/list", () => ({ plugins: [] }));
  fake.on("evener/marketplace/list", () => ({ marketplaces: [] }));
  connectionStore.getState().connect(fake);

  const extensions = await import("./extensions");
  expect(extensions.extensionsStore.getState().plugins).toBeNull();
});
