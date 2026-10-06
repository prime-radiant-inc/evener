// @vitest-environment jsdom

// The guards' click helpers run buttonRefusesClick in the page, as source text
// interpolated into an evaluated expression. This evaluates that same source
// against real DOM elements, one per way a button can refuse a click.

import { expect, test } from "vitest";
import { buttonRefusesClick } from "./browserGuardCdp.mjs";

function refuses(markup) {
  document.body.innerHTML = markup;
  // biome-ignore lint/security/noGlobalEval: the helper is page-side source text by design
  return (0, eval)(buttonRefusesClick)(document.querySelector("#target"));
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
