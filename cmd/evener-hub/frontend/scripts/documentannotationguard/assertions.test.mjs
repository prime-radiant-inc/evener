// Regression for the guard's independent containment oracle. It exercises the
// geometry rules directly and independently of the browser: a revealed note
// stack inside the larger PANE but below the smaller SCROLL BODY must be
// REJECTED, an ordinary contained stack accepted, empty rectangles rejected, and
// the approved 1 CSS pixel tolerance honored without broadening it.
//
// Runs under both the package's Vitest runner (nested scripts suites) and
// `node --test`. The runner is selected from the ambient environment so the same
// file is collected by `vitest run` and executed by `node --test`.
import assert from "node:assert/strict";
import { test as vitestTest } from "vitest";
import { noteStackContained, rectHasArea, rectInside } from "./assertions.mjs";

const test = process.env.VITEST ? vitestTest : (await import(["node", "test"].join(":"))).test;

// A pane that includes a legal footer below its smaller scrolling body.
const paneRect = { left: 0, top: 0, right: 100, bottom: 100 };
const scrollRect = { left: 0, top: 0, right: 100, bottom: 80 };

test("rejects a note stack inside the pane but below the scroll body", () => {
  const stack = { left: 10, top: 70, right: 50, bottom: 90 }; // inside pane, past body bottom
  // The OLD pane-root oracle would accept it; the corrected one must not.
  assert.equal(rectInside(paneRect, stack), true);
  assert.equal(noteStackContained({ paneRect, scrollRect }, stack), false);
});

test("accepts an ordinary contained note stack", () => {
  const stack = { left: 10, top: 10, right: 50, bottom: 30 };
  assert.equal(noteStackContained({ paneRect, scrollRect }, stack), true);
});

test("rejects a rectangle with no positive area", () => {
  assert.equal(rectHasArea({ left: 10, top: 10, right: 10, bottom: 30 }), false);
  assert.equal(noteStackContained({ paneRect, scrollRect }, { left: 10, top: 10, right: 10, bottom: 30 }), false);
  assert.equal(noteStackContained({ paneRect, scrollRect }, { left: 10, top: 10, right: 50, bottom: 10 }), false);
});

test("honors the approved 1 CSS pixel tolerance without broadening it", () => {
  const onePixelPast = { left: 10, top: 10, right: 50, bottom: 81 };
  assert.equal(noteStackContained({ paneRect, scrollRect }, onePixelPast), true);
  const threePixelsPast = { left: 10, top: 10, right: 50, bottom: 83 };
  assert.equal(noteStackContained({ paneRect, scrollRect }, threePixelsPast), false);
});
