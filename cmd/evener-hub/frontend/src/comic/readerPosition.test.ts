import { afterEach, expect, test, vi } from "vitest";
import type { ComicRow } from "./layout";
import { findComicAnchorRow, readComicPosition, writeComicPosition } from "./readerPosition";

afterEach(() => {
  sessionStorage.clear();
  vi.restoreAllMocks();
});
test("finds the same source offset after continuation boundaries change", () => {
  const rows: ComicRow[] = [
    { id: "m:0", panels: [{ slice: { entryId: "m", start: 0, end: 5 }, composition: "wide" }] },
    { id: "m:5", panels: [{ slice: { entryId: "m", start: 5, end: 12 }, composition: "wide" }] },
  ];
  expect(
    findComicAnchorRow(rows, {
      mode: "away",
      sessionRef: "local:root",
      path: [],
      entryId: "m",
      offset: 7,
      viewportOffset: 24,
    }),
  ).toBe(1);
  expect(
    findComicAnchorRow(rows, {
      mode: "away",
      sessionRef: "local:root",
      path: [],
      entryId: "m",
      offset: 5,
      viewportOffset: 24,
    }),
  ).toBe(1);
});
test("stores only a versioned source anchor under the complete selected ref", () => {
  const position = {
    mode: "away" as const,
    sessionRef: "remote:child",
    path: ["local:root/delegate"],
    entryId: "m",
    offset: 7,
    viewportOffset: -12,
  };
  writeComicPosition("local:root", position);
  expect(readComicPosition("local:root")).toEqual(position);
  expect(readComicPosition("remote:child")).toBeUndefined();
  const key = sessionStorage.key(0)!;
  expect(JSON.parse(sessionStorage.getItem(key)!)).toEqual({ version: 1, position });
  for (const value of [
    "bad json",
    "null",
    '{"version":99}',
    JSON.stringify({ version: 1, position: { ...position, offset: -1 } }),
    JSON.stringify({ version: 1, position: { ...position, viewportOffset: null } }),
  ]) {
    sessionStorage.setItem(key, value);
    expect(readComicPosition("local:root")).toBeUndefined();
  }
});
test("storage denial never prevents reading or saving", () => {
  vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
    throw new Error("denied");
  });
  vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
    throw new Error("quota");
  });
  expect(readComicPosition("local:root")).toBeUndefined();
  expect(() => writeComicPosition("local:root", { mode: "end", sessionRef: "local:root", path: [] })).not.toThrow();
});
