import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { installLocalStorage, MemoryStorage } from "../../../../storageTestUtils";
import { readSeenWatermark, seenWatermarkKey, writeSeenWatermark } from "./seenWatermark";

beforeAll(() => {
  installLocalStorage(new MemoryStorage());
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

test("seenWatermarkKey namespaces by ref under the app's evener.* convention", () => {
  expect(seenWatermarkKey("local:01AAA")).toBe("evener.transcript.seen.v1.local:01AAA");
});
