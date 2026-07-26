import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { readSeenWatermark, seenWatermarkKey, writeSeenWatermark } from "./seenWatermark";

// See draft.test.ts's identical comment: Node 26 shadows jsdom's real
// window.localStorage with its own (non-functional under vitest) global.
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

beforeAll(() => {
  // @ts-expect-error see MemoryStorage's own comment for why this is needed
  globalThis.localStorage = new MemoryStorage();
});

beforeEach(() => {
  localStorage.clear();
});

afterEach(() => {
  vi.restoreAllMocks();
});

test("readSeenWatermark returns null when nothing is stored for this ref", () => {
  expect(readSeenWatermark("local:01AAA")).toBeNull();
});

test("writeSeenWatermark then readSeenWatermark round-trips the same ref's turn id", () => {
  writeSeenWatermark("local:01AAA", "turn-42");
  expect(readSeenWatermark("local:01AAA")).toBe("turn-42");
});

test("writing a new watermark overwrites the previous one for the same ref", () => {
  writeSeenWatermark("local:01AAA", "turn-1");
  writeSeenWatermark("local:01AAA", "turn-2");
  expect(readSeenWatermark("local:01AAA")).toBe("turn-2");
});

test("each ref's watermark is isolated: writing one ref never touches another's", () => {
  writeSeenWatermark("local:01AAA", "turn-a");
  writeSeenWatermark("local:01BBB", "turn-b");
  expect(readSeenWatermark("local:01AAA")).toBe("turn-a");
  expect(readSeenWatermark("local:01BBB")).toBe("turn-b");
});

test("readSeenWatermark degrades to null when localStorage throws (private mode / disabled storage)", () => {
  vi.spyOn(localStorage, "getItem").mockImplementation(() => {
    throw new Error("storage disabled");
  });
  expect(readSeenWatermark("local:01AAA")).toBeNull();
});

test("writeSeenWatermark never throws when localStorage throws (quota exceeded etc.)", () => {
  vi.spyOn(localStorage, "setItem").mockImplementation(() => {
    throw new Error("quota exceeded");
  });
  expect(() => writeSeenWatermark("local:01AAA", "turn-1")).not.toThrow();
});

test("seenWatermarkKey namespaces by ref under the app's serf.* convention", () => {
  expect(seenWatermarkKey("local:01AAA")).toBe("serf.transcript.seen.v1.local:01AAA");
});
