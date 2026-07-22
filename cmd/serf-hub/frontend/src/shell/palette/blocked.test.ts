import { expect, test } from "vitest";
import { blocked, blockedMessage, isBlocked } from "./blocked";

// The blocked sentinel keeps the palette OPEN and surfaces an inline error
// instead of closing (parity-m6-surfaces.md §2.7, search.js:198,839-846).

test("blocked builds the {paletteBlocked, message} sentinel", () => {
  expect(blocked("no active turn")).toEqual({ paletteBlocked: true, message: "no active turn" });
});

test("isBlocked recognizes only the sentinel shape", () => {
  expect(isBlocked(blocked("x"))).toBe(true);
  expect(isBlocked({ paletteBlocked: true, message: "x" })).toBe(true);
});

test("isBlocked rejects everything that is not the sentinel", () => {
  expect(isBlocked(undefined)).toBe(false);
  expect(isBlocked(null)).toBe(false);
  expect(isBlocked("blocked")).toBe(false);
  expect(isBlocked({})).toBe(false);
  expect(isBlocked({ paletteBlocked: false, message: "x" })).toBe(false);
  // A resolved wire response object is not a block.
  expect(isBlocked({ ok: true })).toBe(false);
});

test("blockedMessage returns the message for a sentinel and undefined otherwise", () => {
  expect(blockedMessage(blocked("stuck"))).toBe("stuck");
  expect(blockedMessage({ ok: true })).toBeUndefined();
});
