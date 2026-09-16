import { afterEach, expect, test, vi } from "vitest";
import { createClientIdentity } from "./records";

function fakeStorage(
  overrides: { getItem?: (key: string) => string | null; setItem?: (key: string, value: string) => void } = {},
) {
  const backing = new Map<string, string>();
  return {
    getItem: overrides.getItem ?? ((key: string) => backing.get(key) ?? null),
    setItem: overrides.setItem ?? ((key: string, value: string) => void backing.set(key, value)),
  };
}

afterEach(() => {
  vi.unstubAllGlobals();
});

test("two instances over two storages keep separate identities", () => {
  const a = createClientIdentity(fakeStorage());
  const b = createClientIdentity(fakeStorage());
  expect(a.ownClientId()).not.toBe(b.ownClientId());
});

test("an instance's identity is memoized across repeated calls", () => {
  const identity = createClientIdentity(fakeStorage());
  expect(identity.ownClientId()).toBe(identity.ownClientId());
});

test("a storage whose getItem throws falls back to a generated identity", () => {
  const identity = createClientIdentity({
    getItem: () => {
      throw new Error("denied");
    },
    setItem: () => undefined,
  });
  expect(() => identity.ownClientId()).not.toThrow();
  expect(identity.ownClientId()).toMatch(/^mutation-client-/);
});

test("a storage whose setItem throws falls back to a generated identity", () => {
  const identity = createClientIdentity({
    getItem: () => null,
    setItem: () => {
      throw new Error("denied");
    },
  });
  expect(() => identity.ownClientId()).not.toThrow();
  expect(identity.ownClientId()).toMatch(/^mutation-client-/);
});

test("with randomUUID absent the generated identity is UUID-shaped", () => {
  // crypto's methods live on its prototype, so a spread copy carries none of
  // them; getRandomValues is bound explicitly to keep it real, which
  // createSecureUUID's fallback needs.
  vi.stubGlobal("crypto", {
    getRandomValues: globalThis.crypto.getRandomValues.bind(globalThis.crypto),
    randomUUID: undefined,
  });
  const identity = createClientIdentity(undefined);
  expect(identity.ownClientId()).toMatch(
    /^mutation-client-[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/,
  );
});

test("isOwnMutationRecord claims an unattributed record and this instance's own", () => {
  const identity = createClientIdentity(fakeStorage());
  expect(identity.isOwnMutationRecord({})).toBe(true);
  expect(identity.isOwnMutationRecord({ originClientId: identity.ownClientId() })).toBe(true);
});

test("isOwnMutationRecord refuses a record naming another client", () => {
  const identity = createClientIdentity(fakeStorage());
  expect(identity.isOwnMutationRecord({ originClientId: "someone-else" })).toBe(false);
});
