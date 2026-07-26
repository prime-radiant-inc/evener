import { afterEach, beforeAll, beforeEach, describe, expect, test } from "vitest";
import { EXPANSION_LIMIT, EXPANSION_STORAGE_KEY, loadExpansion, saveExpansion } from "./railExpansion";

// See stores/prefs.test.ts's identical comment: Node 26 shadows jsdom's real
// window.localStorage with its own (non-functional under vitest) global, so
// every test file that touches localStorage needs this same small in-memory
// stand-in. Scoped to this file only.
class MemoryStorage {
  private store = new Map<string, string>();
  getItem(key: string): string | null {
    return this.store.has(key) ? (this.store.get(key) ?? null) : null;
  }
  setItem(key: string, value: string): void {
    this.store.set(key, String(value));
  }
  removeItem(key: string): void {
    this.store.delete(key);
  }
  clear(): void {
    this.store.clear();
  }
}

// A storage that rejects every access, the way a browser in a blocked-cookies
// or over-quota state does. Installed only by the tests that want it.
class ThrowingStorage {
  getItem(): string | null {
    throw new Error("SecurityError");
  }
  setItem(): void {
    throw new Error("QuotaExceededError");
  }
  removeItem(): void {
    throw new Error("SecurityError");
  }
  clear(): void {}
}

function useStorage(storage: unknown): void {
  // @ts-expect-error see MemoryStorage's own comment for why this is needed
  globalThis.localStorage = storage;
}

beforeAll(() => useStorage(new MemoryStorage()));
beforeEach(() => useStorage(new MemoryStorage()));
afterEach(() => useStorage(new MemoryStorage()));

describe("loadExpansion", () => {
  test("reads back exactly what saveExpansion wrote", () => {
    saveExpansion(
      new Map([
        ["projectnode:p1", true],
        ["inactive:s1", false],
      ]),
    );
    expect([...loadExpansion()]).toEqual([
      ["projectnode:p1", true],
      ["inactive:s1", false],
    ]);
  });

  test("an unset key reads as an empty map, not a crash", () => {
    expect(loadExpansion().size).toBe(0);
  });

  test("corrupt JSON reads as an empty map", () => {
    localStorage.setItem(EXPANSION_STORAGE_KEY, "{not json");
    expect(loadExpansion().size).toBe(0);
  });

  test("a JSON value of the wrong shape reads as an empty map", () => {
    localStorage.setItem(EXPANSION_STORAGE_KEY, JSON.stringify(["not", "an", "object"]));
    expect(loadExpansion().size).toBe(0);
  });

  // A hand-edited or half-migrated value must not put non-booleans into the
  // map: the whole point of this store is feeding IsExpanded, and a string
  // there would read as truthy and silently wedge a row open.
  test("drops entries whose value is not a boolean, keeping the rest", () => {
    localStorage.setItem(EXPANSION_STORAGE_KEY, JSON.stringify({ good: true, bad: "yes", alsoBad: 1 }));
    expect([...loadExpansion()]).toEqual([["good", true]]);
  });

  test("a browser that throws on read degrades to an empty map", () => {
    useStorage(new ThrowingStorage());
    expect(loadExpansion().size).toBe(0);
  });
});

describe("saveExpansion", () => {
  test("a browser that throws on write is not fatal", () => {
    useStorage(new ThrowingStorage());
    expect(() => saveExpansion(new Map([["k", true]]))).not.toThrow();
  });

  // Every toggle of every row adds a key, and rows outlive the sessions they
  // named. Without a bound, a long-lived hub accumulates them forever.
  test("keeps only the most recently inserted entries once past the limit", () => {
    const oversized = new Map<string, boolean>();
    for (let i = 0; i < EXPANSION_LIMIT + 10; i++) oversized.set(`k${i}`, true);
    saveExpansion(oversized);

    const loaded = loadExpansion();
    expect(loaded.size).toBe(EXPANSION_LIMIT);
    expect(loaded.has("k0")).toBe(false); // oldest inserted, evicted
    expect(loaded.has("k9")).toBe(false);
    expect(loaded.has(`k${EXPANSION_LIMIT + 9}`)).toBe(true); // newest, kept
  });

  test("a map at exactly the limit is stored whole", () => {
    const exact = new Map<string, boolean>();
    for (let i = 0; i < EXPANSION_LIMIT; i++) exact.set(`k${i}`, true);
    saveExpansion(exact);
    expect(loadExpansion().size).toBe(EXPANSION_LIMIT);
  });
});
