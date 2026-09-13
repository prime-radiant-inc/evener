import { beforeEach, expect, test, vi } from "vitest";

const UUID = "11111111-2222-4333-8444-555555555555";

// ownClientId holds a module-level fallback, so each case gets a fresh module
// and an empty identity slot rather than the previous case's page identity.
beforeEach(() => {
  vi.unstubAllGlobals();
  globalThis.sessionStorage?.clear();
  vi.resetModules();
});

test("the generated identity uses crypto.randomUUID when available", async () => {
  vi.stubGlobal("crypto", { ...globalThis.crypto, randomUUID: () => UUID });
  const { ownClientId } = await import("./mutationClientIdentity");

  expect(ownClientId()).toBe(`mutation-client-${UUID}`);
});

test("the stored identity is cached so a later storage failure keeps one identity", async () => {
  globalThis.sessionStorage?.setItem("evener-hub.mutation-client-identity", "stored-identity");
  const { ownClientId } = await import("./mutationClientIdentity");
  expect(ownClientId()).toBe("stored-identity");

  // Storage becomes unusable (private mode, a policy change): the page must
  // keep the identity it already read rather than generating a new one that
  // would make its own in-flight records look foreign.
  vi.stubGlobal("sessionStorage", {
    getItem: () => {
      throw new Error("storage denied");
    },
    setItem: () => {},
  });
  expect(ownClientId()).toBe("stored-identity");
});

test("falls back to the random and timestamp identity without crypto.randomUUID", async () => {
  vi.stubGlobal("crypto", { ...globalThis.crypto, randomUUID: undefined });
  const { ownClientId } = await import("./mutationClientIdentity");

  expect(ownClientId()).toMatch(/^mutation-client-[0-9a-z]+-[0-9a-z]+$/);
});

test("a fallback identity created before storage was available is never replaced", async () => {
  // Storage-availability seam: the toggle below stands in for the described
  // trigger (storage denied on the first call, reachable later). In production
  // this module is the key's only writer, so no stored value can appear that
  // this tab did not write - see the test's own report note - but the seam is
  // what makes the "late value becomes visible" step observable.
  const backing = new Map<string, string>();
  let available = false;
  vi.stubGlobal("sessionStorage", {
    getItem: (key: string) => {
      if (!available) throw new Error("storage denied");
      return backing.get(key) ?? null;
    },
    setItem: (key: string, value: string) => {
      if (!available) throw new Error("storage denied");
      backing.set(key, value);
    },
    removeItem: (key: string) => {
      if (!available) throw new Error("storage denied");
      backing.delete(key);
    },
    clear: () => backing.clear(),
  });
  const { ownClientId } = await import("./mutationClientIdentity");

  // First call with storage unavailable: the page falls back to a generated
  // identity held in module state.
  const first = ownClientId();
  expect(first).toMatch(/^mutation-client-/);

  // Storage becomes reachable later holding an identity this page never
  // handed out. One page keeps one identity either way: switching now would
  // classify records stamped with the fallback as belonging to a different
  // client.
  available = true;
  backing.set("evener-hub.mutation-client-identity", "stored-identity");
  expect(ownClientId()).toBe(first);
});
