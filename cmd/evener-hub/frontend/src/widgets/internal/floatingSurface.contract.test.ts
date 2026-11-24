// @vitest-environment node

// Contract test for the shared floating-layer surface
// (floatingSurface.module.css): the raised fill, pane corner and overlay
// shadow are declared once there, and every floating widget composes them
// rather than restating the box. jsdom evaluates no cascade, so these are
// declaration-level assertions over the stylesheet sources, comments
// stripped, the same shape as styles/surface.contract.test.ts.
import { expect, test } from "vitest";
import { readModuleCss, topRuleBlock } from "../../styles/cssBlock";

const shared = readModuleCss(import.meta.url, "floatingSurface.module.css");

// The three declarations that make the floating surface. A consumer that
// restates one of them next to its composes line has forked the recipe.
const SURFACE_DECLARATIONS = [
  /background:\s*var\(--surface-2\)/,
  /border-radius:\s*var\(--radius-pane\)/,
  /box-shadow:\s*var\(--shadow-overlay\)/,
];

test("the floating surface is one rule: raised fill, pane corner, overlay ring and drop", () => {
  const panel = topRuleBlock(shared, ".panel");
  for (const declaration of SURFACE_DECLARATIONS) expect(panel).toMatch(declaration);
  // The overlay shadow embeds the 1px ring; a second border would double it.
  expect(panel).not.toMatch(/(^|\s)border:/);
});

const WIDGETS: Array<[file: string, selector: string, localFill: string | null]> = [
  ["../menu/menu.module.css", ".popup", null],
  ["../popover/popover.module.css", ".panel", "--surface-1"],
  ["../dialog/dialog.module.css", ".panel", null],
  ["../hovercard/hovercard.module.css", ".bubble", "--surface-1"],
  ["../toast/toast.module.css", ".toast", null],
];

test.each(WIDGETS)(
  "%s %s composes the shared floating surface and restates none of it",
  (file, selector, localFill) => {
    const body = topRuleBlock(readModuleCss(import.meta.url, file), selector);
    expect(body).toMatch(/composes:\s*panel from "\.\.\/internal\/floatingSurface\.module\.css"/);
    for (const declaration of SURFACE_DECLARATIONS) expect(body).not.toMatch(declaration);
    if (localFill !== null) expect(body).toMatch(new RegExp(`background:\\s*var\\(${localFill}\\)`));
  },
);
