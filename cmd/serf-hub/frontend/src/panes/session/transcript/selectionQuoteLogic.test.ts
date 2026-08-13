import { expect, test } from "vitest";
import { clampToBounds, formatQuoteBlock, messageContentElement } from "./selectionQuoteLogic";

// --- formatQuoteBlock ----------------------------------------------------

test("formatQuoteBlock: a single line becomes one '> ' line plus a trailing blank line", () => {
  expect(formatQuoteBlock("hello world")).toBe("> hello world\n\n");
});

test("formatQuoteBlock: each line of a multi-line selection gets its own '> ' prefix", () => {
  expect(formatQuoteBlock("first line\nsecond line\nthird line")).toBe("> first line\n> second line\n> third line\n\n");
});

test("formatQuoteBlock: trims leading/trailing whitespace-only lines before quoting", () => {
  expect(formatQuoteBlock("\n\n  middle  \n\n")).toBe("> middle\n\n");
});

test("formatQuoteBlock: normalizes CRLF line endings to a single '> ' prefix per line", () => {
  expect(formatQuoteBlock("a\r\nb")).toBe("> a\n> b\n\n");
});

test("formatQuoteBlock: preserves a blank line in the middle of the selection as an empty '>' line", () => {
  expect(formatQuoteBlock("first\n\nsecond")).toBe("> first\n> \n> second\n\n");
});

test("formatQuoteBlock: an empty or whitespace-only selection formats to an empty string", () => {
  expect(formatQuoteBlock("")).toBe("");
  expect(formatQuoteBlock("   \n  ")).toBe("");
});

// --- messageContentElement ------------------------------------------------

function el(tag: string, attrs: Record<string, string> = {}): HTMLElement {
  const node = document.createElement(tag);
  for (const [key, value] of Object.entries(attrs)) node.setAttribute(key, value);
  return node;
}

test("messageContentElement: finds the nearest ancestor marked data-view-anchor-message=true", () => {
  const container = el("div");
  const message = el("div", { "data-view-anchor-message": "true" });
  const paragraph = el("p");
  const text = document.createTextNode("hello");
  paragraph.appendChild(text);
  message.appendChild(paragraph);
  container.appendChild(message);

  expect(messageContentElement(text, container)).toBe(message);
});

test("messageContentElement: returns null when no ancestor up to the container is marked", () => {
  const container = el("div");
  const chrome = el("div", { "data-view-anchor-message": "false" });
  const text = document.createTextNode("chrome text");
  chrome.appendChild(text);
  container.appendChild(chrome);

  expect(messageContentElement(text, container)).toBeNull();
});

test("messageContentElement: returns null for a node outside the container entirely", () => {
  const container = el("div");
  const outside = el("div", { "data-view-anchor-message": "true" });
  const text = document.createTextNode("outside text");
  outside.appendChild(text);
  // outside is never appended to container

  expect(messageContentElement(text, container)).toBeNull();
});

test("messageContentElement: returns null for a null node", () => {
  const container = el("div");
  expect(messageContentElement(null, container)).toBeNull();
});

test("messageContentElement: the container itself never counts, even if marked", () => {
  const container = el("div", { "data-view-anchor-message": "true" });
  expect(messageContentElement(container, container)).toBeNull();
});

// --- clampToBounds ---------------------------------------------------------

test("clampToBounds: leaves a position untouched when fully inside the bounds", () => {
  expect(clampToBounds({ x: 50, y: 50 }, { width: 100, height: 30 }, { width: 400, height: 300 })).toEqual({
    x: 50,
    y: 50,
  });
});

test("clampToBounds: pulls a negative position back to the padding floor", () => {
  expect(clampToBounds({ x: -20, y: -20 }, { width: 100, height: 30 }, { width: 400, height: 300 })).toEqual({
    x: 8,
    y: 8,
  });
});

test("clampToBounds: pulls an over-the-edge position back so the element's far edge stays inside bounds", () => {
  // width=100, bounds.width=400, padding=8 -> max x = 400 - 100 - 8 = 292
  expect(clampToBounds({ x: 500, y: 500 }, { width: 100, height: 30 }, { width: 400, height: 300 })).toEqual({
    x: 292,
    y: 262,
  });
});

test("clampToBounds: accepts a custom padding", () => {
  expect(clampToBounds({ x: -100, y: -100 }, { width: 100, height: 30 }, { width: 400, height: 300 }, 0)).toEqual({
    x: 0,
    y: 0,
  });
});

test("clampToBounds: when the element is larger than the bounds, still returns the padding floor rather than a negative", () => {
  expect(clampToBounds({ x: 10, y: 10 }, { width: 500, height: 500 }, { width: 400, height: 300 })).toEqual({
    x: 8,
    y: 8,
  });
});
