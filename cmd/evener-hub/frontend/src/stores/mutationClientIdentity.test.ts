import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { stubThrowingGetter } from "./throwingGetterTestUtils";

const UUID = "11111111-2222-4333-8444-555555555555";
const originalSessionStorage = Object.getOwnPropertyDescriptor(globalThis, "sessionStorage");

// ownClientId holds a module-level fallback, so each case gets a fresh module
// and an empty identity slot rather than the previous case's page identity.
beforeEach(() => {
  vi.unstubAllGlobals();
  globalThis.sessionStorage?.clear();
  vi.resetModules();
});

afterEach(() => {
  if (originalSessionStorage) {
    Object.defineProperty(globalThis, "sessionStorage", originalSessionStorage);
  } else {
    delete (globalThis as { sessionStorage?: unknown }).sessionStorage;
  }
  vi.unstubAllGlobals();
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

test("falls back to a secure UUID-shaped identity without crypto.randomUUID", async () => {
  // crypto's methods live on its prototype, so a spread copy carries none of
  // them; getRandomValues is bound explicitly to keep it real, which is what
  // createSecureUUID's fallback needs.
  vi.stubGlobal("crypto", {
    getRandomValues: globalThis.crypto.getRandomValues.bind(globalThis.crypto),
    randomUUID: undefined,
  });
  const { ownClientId } = await import("./mutationClientIdentity");

  expect(ownClientId()).toMatch(
    /^mutation-client-[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/,
  );
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

// The property access itself, not just a method call: some sandboxed pages
// throw on touching window.sessionStorage at all. globalThis.sessionStorage
// is read lazily inside the shim's adapter methods, one call frame inside the
// package's own try/catch, so this must not surface as an uncaught throw.
// The file's afterEach restores the descriptor, so the two tests below don't
// need the helper's own restore function.

test("a sessionStorage getter that throws on access falls back to a generated identity", async () => {
  stubThrowingGetter(globalThis, "sessionStorage");
  const { ownClientId } = await import("./mutationClientIdentity");

  expect(() => ownClientId()).not.toThrow();
  expect(ownClientId()).toMatch(/^mutation-client-/);
});

test("a memoized identity never re-touches a throwing sessionStorage getter", async () => {
  const { ownClientId } = await import("./mutationClientIdentity");
  const first = ownClientId();

  stubThrowingGetter(globalThis, "sessionStorage");
  expect(ownClientId()).toBe(first);
});
