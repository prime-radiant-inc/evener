import { expect, test } from "vitest";
import {
  AT_BOTTOM_THRESHOLD_PX,
  isAtBottom,
  isNearTop,
  NEAR_TOP_THRESHOLD_PX,
  readScrollMetrics,
} from "./scrollMetrics";

// isAtBottom: "at the bottom" means the reader is at the TRUE end of the
// scrollable content, not merely near it. The threshold is a few pixels
// (rounding-safe, far smaller than one line of text ~20px) so a reader who
// has scrolled back even one line is NOT treated as at the bottom - the
// legacy 50px value let autoscroll engage while scrolled comfortably back.
test("isAtBottom: true when scrollTop+clientHeight exactly equals scrollHeight (pixel-perfect bottom)", () => {
  expect(isAtBottom({ scrollTop: 950, scrollHeight: 1000, clientHeight: 50 })).toBe(true);
});

test("isAtBottom: true within the 4px threshold (rounding tolerance)", () => {
  expect(isAtBottom({ scrollTop: 946, scrollHeight: 1000, clientHeight: 50 })).toBe(true); // gap = 4
});

test("isAtBottom: false just past the 4px threshold (one line of text is ~20px, far larger)", () => {
  expect(isAtBottom({ scrollTop: 945, scrollHeight: 1000, clientHeight: 50 })).toBe(false); // gap = 5
});

test("isAtBottom: true for a short thread that doesn't scroll at all (scrollHeight <= clientHeight)", () => {
  expect(isAtBottom({ scrollTop: 0, scrollHeight: 100, clientHeight: 200 })).toBe(true);
});

test("isAtBottom: false when scrolled far up", () => {
  expect(isAtBottom({ scrollTop: 0, scrollHeight: 5000, clientHeight: 500 })).toBe(false);
});

test("isAtBottom: accepts a custom threshold, overriding the 4px default", () => {
  expect(isAtBottom({ scrollTop: 800, scrollHeight: 1000, clientHeight: 100 }, 100)).toBe(true); // gap = 100
  expect(isAtBottom({ scrollTop: 800, scrollHeight: 1000, clientHeight: 100 }, 4)).toBe(false);
});

test("AT_BOTTOM_THRESHOLD_PX is the 4px default (rounding-safe, smaller than one line of text)", () => {
  expect(AT_BOTTOM_THRESHOLD_PX).toBe(4);
});

// isNearTop: legacy parity threshold (parity doc §15) - "scrollTop < 200
// triggers older-turn paging".
test("isNearTop: true below the 200px threshold", () => {
  expect(isNearTop(199)).toBe(true);
});

test("isNearTop: false at exactly the 200px threshold (strict less-than, matching legacy)", () => {
  expect(isNearTop(200)).toBe(false);
});

test("isNearTop: true at scrollTop 0", () => {
  expect(isNearTop(0)).toBe(true);
});

test("isNearTop: accepts a custom threshold, overriding the 200px default", () => {
  expect(isNearTop(150, 100)).toBe(false);
  expect(isNearTop(50, 100)).toBe(true);
});

test("NEAR_TOP_THRESHOLD_PX is the legacy-matching 200px default", () => {
  expect(NEAR_TOP_THRESHOLD_PX).toBe(200);
});

// readScrollMetrics: the real-DOM measurement seam. Kept as its own named
// function (rather than reading el.scrollTop/scrollHeight/clientHeight
// inline at every call site) specifically so useTranscriptScroll.ts can
// accept an injected replacement for tests - jsdom performs no real layout
// (every element's scroll* property defaults to 0/0/0, same limitation
// VirtualList's own test suite documents), so a test that wants to drive
// realistic scroll scenarios must be able to substitute this function
// entirely rather than fight jsdom's fixed zeros. This test proves the
// PRODUCTION default reads the three real DOM properties, nothing more.
test("readScrollMetrics reads scrollTop/scrollHeight/clientHeight straight off the element", () => {
  const el = document.createElement("div");
  Object.defineProperty(el, "scrollTop", { configurable: true, value: 120 });
  Object.defineProperty(el, "scrollHeight", { configurable: true, value: 2000 });
  Object.defineProperty(el, "clientHeight", { configurable: true, value: 400 });

  expect(readScrollMetrics(el)).toEqual({ scrollTop: 120, scrollHeight: 2000, clientHeight: 400 });
});
