import { expect, test } from "vitest";
import { insertMarker, markerText, stripMarker } from "./textareaMarkers";

test("markerText renders the literal [image N] placeholder", () => {
  expect(markerText(1)).toBe("[image 1]");
  expect(markerText(42)).toBe("[image 42]");
});

// --- insertMarker: parity-m5-composer.md §G / test-composer-image-markers.js ---
// Pure string splicing - the caller (useAttachments/Composer.tsx) applies
// the result through React's own controlled-value state, never a direct
// DOM mutation (see this module's own header comment for why).

test("inserts text at the cursor position, splitting the existing value", () => {
  const result = insertMarker("hello world", 5, 5, markerText(1));
  expect(result.value).toBe("hello[image 1] world");
});

test("returns the cursor position just after the inserted text", () => {
  const result = insertMarker("hello world", 5, 5, markerText(1));
  expect(result.cursor).toBe(5 + "[image 1]".length);
});

test("inserting at the end of the value appends", () => {
  const result = insertMarker("look ", 5, 5, markerText(1));
  expect(result.value).toBe("look [image 1]");
});

test("replaces a selected range rather than inserting inside it", () => {
  const result = insertMarker("hello world", 0, 5, markerText(1)); // "hello" selected
  expect(result.value).toBe("[image 1] world");
});

test("two sequential inserts land side by side when the second starts at the first's returned cursor", () => {
  const first = insertMarker("ab", 1, 1, markerText(1));
  expect(first.value).toBe("a[image 1]b");
  const second = insertMarker(first.value, first.value.length, first.value.length, markerText(2));
  expect(second.value).toBe("a[image 1]b[image 2]");
});

// --- stripMarker: removes only the first literal occurrence ------------

test("strips the first occurrence of the given marker only", () => {
  const result = stripMarker("[image 1][image 2]", 0, 1);
  expect(result.value).toBe("[image 2]");
});

test("leaves sibling markers untouched", () => {
  const result = stripMarker("[image 1][image 2][image 3]", 0, 2);
  expect(result.value).toBe("[image 1][image 3]");
});

test("shifts the cursor back when it sat past the removed marker", () => {
  const marker = markerText(1);
  const value = `${marker} tail`;
  const result = stripMarker(value, marker.length + 5, 1); // cursor inside "tail"
  expect(result.value).toBe(" tail");
  expect(result.cursor).toBe(5); // shifted back by marker.length
});

test("does not move the cursor when it sat before the removed marker", () => {
  const marker = markerText(2);
  const value = `head ${marker}`;
  const result = stripMarker(value, 2, 2);
  expect(result.value).toBe("head ");
  expect(result.cursor).toBe(2);
});

test("no-ops (returns the original value/cursor) when the marker is not present", () => {
  const result = stripMarker("no markers here", 3, 99);
  expect(result.value).toBe("no markers here");
  expect(result.cursor).toBe(3);
});

test("returns an undefined cursor unchanged when the caller's cursor is unknown", () => {
  const result = stripMarker("[image 1]", undefined, 1);
  expect(result.value).toBe("");
  expect(result.cursor).toBeUndefined();
});
