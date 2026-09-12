import { beforeEach, expect, test, vi } from "vitest";

const UUID = "11111111-2222-4333-8444-555555555555";

// ownClientId holds a module-level fallback, so each case gets a fresh module
// and an empty identity slot rather than the previous case's page identity.
beforeEach(() => {
  vi.resetModules();
  globalThis.sessionStorage?.clear();
  vi.unstubAllGlobals();
});

test("the generated identity uses crypto.randomUUID when available", async () => {
  vi.stubGlobal("crypto", { ...globalThis.crypto, randomUUID: () => UUID });
  const { ownClientId } = await import("./mutationClientIdentity");

  expect(ownClientId()).toBe(`mutation-client-${UUID}`);
});

test("falls back to the random and timestamp identity without crypto.randomUUID", async () => {
  vi.stubGlobal("crypto", { ...globalThis.crypto, randomUUID: undefined });
  const { ownClientId } = await import("./mutationClientIdentity");

  expect(ownClientId()).toMatch(/^mutation-client-[0-9a-z]+-[0-9a-z]+$/);
});
