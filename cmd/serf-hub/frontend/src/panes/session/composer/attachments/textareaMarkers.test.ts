import { expect, test } from "vitest";
import { insertAtCursor, markerText, stripMarker } from "./textareaMarkers";

function textareaWithCursor(value: string, cursor: number): HTMLTextAreaElement {
  const el = document.createElement("textarea");
  el.value = value;
  el.selectionStart = cursor;
  el.selectionEnd = cursor;
  return el;
}

test("markerText renders the literal [image N] placeholder", () => {
  expect(markerText(1)).toBe("[image 1]");
  expect(markerText(42)).toBe("[image 42]");
});

// --- insertAtCursor: parity-m5-composer.md §G / test-composer-image-markers.js ---

test("inserts text at the cursor position, splitting the existing value", () => {
  const el = textareaWithCursor("hello world", 5);
  insertAtCursor(el, markerText(1));
  expect(el.value).toBe("hello[image 1] world");
});

test("moves the cursor to just after the inserted text", () => {
  const el = textareaWithCursor("hello world", 5);
  insertAtCursor(el, markerText(1));
  expect(el.selectionStart).toBe(5 + "[image 1]".length);
  expect(el.selectionEnd).toBe(5 + "[image 1]".length);
});

test("inserting at the end of the value appends", () => {
  const el = textareaWithCursor("look ", 5);
  insertAtCursor(el, markerText(1));
  expect(el.value).toBe("look [image 1]");
});

test("replaces a selected range rather than inserting inside it", () => {
  const el = document.createElement("textarea");
  el.value = "hello world";
  el.selectionStart = 0;
  el.selectionEnd = 5; // "hello" selected
  insertAtCursor(el, markerText(1));
  expect(el.value).toBe("[image 1] world");
});

test("two sequential inserts land side by side in cursor order", () => {
  const el = textareaWithCursor("ab", 1);
  insertAtCursor(el, markerText(1));
  expect(el.value).toBe("a[image 1]b");
  el.selectionStart = el.value.length;
  el.selectionEnd = el.value.length;
  insertAtCursor(el, markerText(2));
  expect(el.value).toBe("a[image 1]b[image 2]");
});

// --- stripMarker: removes only the first literal occurrence ------------

test("strips the first occurrence of the given marker only", () => {
  const el = textareaWithCursor("[image 1][image 2]", 0);
  stripMarker(el, 1);
  expect(el.value).toBe("[image 2]");
});

test("leaves sibling markers untouched", () => {
  const el = textareaWithCursor("[image 1][image 2][image 3]", 0);
  stripMarker(el, 2);
  expect(el.value).toBe("[image 1][image 3]");
});

test("shifts the cursor back when it sat past the removed marker", () => {
  const marker = markerText(1);
  const el = textareaWithCursor(`${marker} tail`, marker.length + 5); // cursor inside "tail"
  stripMarker(el, 1);
  expect(el.value).toBe(" tail");
  expect(el.selectionStart).toBe(5); // shifted back by marker.length
});

test("does not move the cursor when it sat before the removed marker", () => {
  const marker = markerText(2);
  const el = textareaWithCursor(`head ${marker}`, 2);
  stripMarker(el, 2);
  expect(el.value).toBe("head ");
  expect(el.selectionStart).toBe(2);
});

test("no-ops when the marker is not present in the value", () => {
  const el = textareaWithCursor("no markers here", 3);
  stripMarker(el, 99);
  expect(el.value).toBe("no markers here");
  expect(el.selectionStart).toBe(3);
});

test("does not throw when the element is null (no textarea wired)", () => {
  expect(() => stripMarker(null, 1)).not.toThrow();
});
