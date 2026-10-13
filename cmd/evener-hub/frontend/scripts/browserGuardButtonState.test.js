// @vitest-environment jsdom

// The guards' click helpers test REFUSED_BUTTON_SELECTOR in the page before
// clicking. This matches the same selector against real DOM elements, one per
// way a button can refuse a click.

import { expect, test } from "vitest";
import { REFUSED_BUTTON_SELECTOR } from "./browserGuardCdp.mjs";

function refuses(markup) {
  document.body.innerHTML = markup;
  return document.querySelector("#target").matches(REFUSED_BUTTON_SELECTOR);
}

test("an enabled button accepts the click", () => {
  expect(refuses('<button id="target">Go</button>')).toBe(false);
  expect(refuses('<button id="target" aria-disabled="false">Go</button>')).toBe(false);
});

test("a natively disabled button refuses it", () => {
  expect(refuses('<button id="target" disabled>Go</button>')).toBe(true);
});

test("a button inside a disabled fieldset refuses it", () => {
  expect(refuses('<fieldset disabled><button id="target">Go</button></fieldset>')).toBe(true);
});

test("a button the Button widget refuses with aria-disabled refuses it", () => {
  expect(refuses('<button id="target" aria-disabled="true">Go</button>')).toBe(true);
});
