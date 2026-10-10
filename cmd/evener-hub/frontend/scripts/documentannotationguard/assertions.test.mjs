// Vitest regression for the guard's independent containment oracle. It exercises
// the geometry rules directly and independently of the browser: a revealed note
// stack inside the larger PANE but below the smaller SCROLL BODY must be
// REJECTED, an ordinary contained stack accepted, empty rectangles rejected, and
// the approved 1 CSS pixel tolerance honored without broadening it.
//
// This nested scripts suite runs under the package's Vitest runner (like the
// other scripts/*/*.test.mjs suites); the package script's Node phase collects
// only the top-level scripts/*.test.mjs files.
import { expect, test } from "vitest";
import { markerVisible, noteStackContained, rectHasArea, rectInside } from "./assertions.mjs";

// A pane that includes a legal footer below its smaller scrolling body.
const paneRect = { left: 0, top: 0, right: 100, bottom: 100 };
const scrollRect = { left: 0, top: 0, right: 100, bottom: 80 };

test("rejects a note stack inside the pane but below the scroll body", () => {
  const stack = { left: 10, top: 70, right: 50, bottom: 90 }; // inside pane, past body bottom
  // The OLD pane-root oracle would accept it; the corrected one must not.
  expect(rectInside(paneRect, stack)).toBe(true);
  expect(noteStackContained({ paneRect, scrollRect }, stack)).toBe(false);
});

test("accepts an ordinary contained note stack", () => {
  const stack = { left: 10, top: 10, right: 50, bottom: 30 };
  expect(noteStackContained({ paneRect, scrollRect }, stack)).toBe(true);
});

test("rejects a rectangle with no positive area", () => {
  expect(rectHasArea({ left: 10, top: 10, right: 10, bottom: 30 })).toBe(false);
  expect(noteStackContained({ paneRect, scrollRect }, { left: 10, top: 10, right: 10, bottom: 30 })).toBe(false);
  expect(noteStackContained({ paneRect, scrollRect }, { left: 10, top: 10, right: 50, bottom: 10 })).toBe(false);
});

test("honors the approved 1 CSS pixel tolerance without broadening it", () => {
  const onePixelPast = { left: 10, top: 10, right: 50, bottom: 81 };
  expect(noteStackContained({ paneRect, scrollRect }, onePixelPast)).toBe(true);
  const threePixelsPast = { left: 10, top: 10, right: 50, bottom: 83 };
  expect(noteStackContained({ paneRect, scrollRect }, threePixelsPast)).toBe(false);
});

// ---- marker reachability ----

const markerClip = { left: 0, top: 0, right: 100, bottom: 50 };

test("accepts an END observation when a long note's end marker is visible", () => {
  // The note spans far beyond the viewport; only its own end fragment is inside.
  expect(markerVisible(markerClip, [{ left: 10, top: 40, right: 60, bottom: 48 }])).toBe(true);
});

test("rejects an offscreen marker even though its text node exists offscreen", () => {
  expect(markerVisible(markerClip, [{ left: 10, top: 60, right: 60, bottom: 70 }])).toBe(false);
  expect(markerVisible(markerClip, [{ left: 10, top: -30, right: 60, bottom: -20 }])).toBe(false);
  expect(markerVisible(markerClip, [{ left: -60, top: 10, right: -10, bottom: 20 }])).toBe(false);
});

test("rejects a zero-area or absent marker fragment", () => {
  expect(markerVisible(markerClip, [{ left: 10, top: 10, right: 10, bottom: 30 }])).toBe(false);
  expect(markerVisible(markerClip, [])).toBe(false);
});

test("rejects a partly clipped marker fragment", () => {
  expect(markerVisible(markerClip, [{ left: 10, top: 40, right: 60, bottom: 60 }])).toBe(false);
});

test("rejects a wrapped marker with one offscreen fragment", () => {
  expect(
    markerVisible(markerClip, [
      { left: 10, top: 10, right: 60, bottom: 20 },
      { left: 10, top: 60, right: 60, bottom: 70 },
    ]),
  ).toBe(false);
});

test("rejects an empty or inverted visible clip", () => {
  expect(
    markerVisible({ left: 50, top: 50, right: 40, bottom: 40 }, [{ left: 10, top: 10, right: 60, bottom: 60 }]),
  ).toBe(false);
  expect(
    markerVisible({ left: 50, top: 50, right: 50, bottom: 50 }, [{ left: 10, top: 10, right: 60, bottom: 60 }]),
  ).toBe(false);
});
