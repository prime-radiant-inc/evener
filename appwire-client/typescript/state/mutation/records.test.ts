// @vitest-environment node

import { expect, test } from "vitest";
import type { MutationRecord } from "./records";
import { acceptedRecord, carriesOptimisticInput, createClientIdentity } from "./records";

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

// A source whose randomUUID hands back one fixed id, for cases that assert
// on identity rather than on uniqueness: deterministic instead of resting on
// two real UUIDs never colliding.
function scriptedRandomUUIDSource(id: string) {
  return { randomUUID: () => id };
}

// A settled source record with every field the accepted whitelist names, plus
// the optional outbox-only fields a real source may still carry.
function settledSource(overrides: Record<string, unknown> = {}): MutationRecord {
  return {
    version: 1,
    clientMutationId: "cm-1",
    originClientId: "client-a",
    targetRef: "local:1",
    threadId: "thread-1",
    instanceId: "instance-1",
    method: "turn/queue",
    payload: { ref: "local:1" },
    attachments: [],
    optimisticDisplay: { input: [{ type: "text", text: "hi" }] },
    composerText: "hi",
    intentSequence: 3,
    createdAt: 42,
    ...overrides,
  };
}

test("acceptedRecord carries the accepted whitelist and drops outbox/recovery-only fields", () => {
  const accepted = acceptedRecord(
    settledSource({ state: "submitting", attempted: true, recoveryKind: "rejected", recoveryReason: "nope" }),
  );
  expect(accepted).toEqual({
    version: 1,
    clientMutationId: "cm-1",
    originClientId: "client-a",
    intentSequence: 3,
    createdAt: 42,
    targetRef: "local:1",
    threadId: "thread-1",
    instanceId: "instance-1",
    method: "turn/queue",
    payload: { ref: "local:1" },
    attachments: [],
    optimisticDisplay: { input: [{ type: "text", text: "hi" }] },
    state: "accepted",
  });
  expect(accepted).not.toHaveProperty("attempted");
  expect(accepted).not.toHaveProperty("recoveryKind");
  expect(accepted).not.toHaveProperty("recoveryReason");
  expect(accepted).not.toHaveProperty("composerText");
});

test("carriesOptimisticInput reports an input array and nothing else", () => {
  expect(carriesOptimisticInput(settledSource())).toBe(true);
  expect(carriesOptimisticInput(settledSource({ optimisticDisplay: { input: [] } }))).toBe(true);
  expect(carriesOptimisticInput(settledSource({ optimisticDisplay: { text: "x" } }))).toBe(false);
  expect(carriesOptimisticInput(settledSource({ optimisticDisplay: { input: "not-array" } }))).toBe(false);
  expect(carriesOptimisticInput(settledSource({ optimisticDisplay: null }))).toBe(false);
  expect(carriesOptimisticInput(settledSource({ optimisticDisplay: undefined }))).toBe(false);
  expect(carriesOptimisticInput(settledSource({ optimisticDisplay: {} }))).toBe(false);
});

test("two instances over two storages keep separate identities", () => {
  const a = createClientIdentity(fakeStorage(), scriptedRandomUUIDSource("11111111-1111-4111-8111-111111111111"));
  const b = createClientIdentity(fakeStorage(), scriptedRandomUUIDSource("22222222-2222-4222-8222-222222222222"));
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

test("undefined storage gives a stable per-process identity", () => {
  const identity = createClientIdentity(undefined, getRandomValuesSource());
  expect(() => identity.ownClientId()).not.toThrow();
  expect(identity.ownClientId()).toBe(identity.ownClientId());
});
