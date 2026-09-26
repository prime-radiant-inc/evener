// @vitest-environment node

import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { afterEach, expect, test, vi } from "vitest";

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
