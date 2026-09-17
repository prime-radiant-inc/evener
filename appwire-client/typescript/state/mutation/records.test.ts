import { expect, test } from "vitest";
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

// A random source with real getRandomValues (Node's own, this module names
// no globalThis of its own to reach it from), so a generated identity is
// UUID-shaped without going through crypto.randomUUID.
function getRandomValuesSource() {
  return { getRandomValues: globalThis.crypto.getRandomValues.bind(globalThis.crypto) };
}

test("two instances over two storages keep separate identities", () => {
  const a = createClientIdentity(fakeStorage(), getRandomValuesSource());
  const b = createClientIdentity(fakeStorage(), getRandomValuesSource());
  expect(a.ownClientId()).not.toBe(b.ownClientId());
});

test("an instance's identity is memoized across repeated calls", () => {
  const identity = createClientIdentity(fakeStorage(), getRandomValuesSource());
  expect(identity.ownClientId()).toBe(identity.ownClientId());
});

test("a storage whose getItem throws falls back to a generated identity", () => {
  const identity = createClientIdentity(
    {
      getItem: () => {
        throw new Error("denied");
      },
      setItem: () => undefined,
    },
    getRandomValuesSource(),
  );
  expect(() => identity.ownClientId()).not.toThrow();
  expect(identity.ownClientId()).toMatch(/^mutation-client-/);
});

test("a storage whose setItem throws falls back to a generated identity", () => {
  const identity = createClientIdentity(
    {
      getItem: () => null,
      setItem: () => {
        throw new Error("denied");
      },
    },
    getRandomValuesSource(),
  );
  expect(() => identity.ownClientId()).not.toThrow();
  expect(identity.ownClientId()).toMatch(/^mutation-client-/);
});

test("with randomUUID absent but getRandomValues present the generated identity is UUID-shaped", () => {
  const identity = createClientIdentity(fakeStorage(), getRandomValuesSource());
  expect(identity.ownClientId()).toMatch(
    /^mutation-client-[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/,
  );
});

test("a random source with neither method still yields a stable, non-throwing identity", () => {
  const identity = createClientIdentity(fakeStorage(), {});
  expect(() => identity.ownClientId()).not.toThrow();
  expect(identity.ownClientId()).toBe(identity.ownClientId());
});

test("isOwnMutationRecord claims an unattributed record and this instance's own", () => {
  const identity = createClientIdentity(fakeStorage(), getRandomValuesSource());
  expect(identity.isOwnMutationRecord({})).toBe(true);
  expect(identity.isOwnMutationRecord({ originClientId: identity.ownClientId() })).toBe(true);
});

test("isOwnMutationRecord refuses a record naming another client", () => {
  const identity = createClientIdentity(fakeStorage(), getRandomValuesSource());
  expect(identity.isOwnMutationRecord({ originClientId: "someone-else" })).toBe(false);
});
