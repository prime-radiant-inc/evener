// @vitest-environment node

// Contract test for the shared floating-layer surface
// (floatingSurface.module.css): the raised fill, pane corner and overlay
// shadow are declared once there, and every floating widget composes them
// rather than restating the box. jsdom evaluates no cascade, so these are
// declaration-level assertions over the stylesheet sources, comments
// stripped, the same shape as styles/surface.contract.test.ts.
import { expect, test } from "vitest";
import { mediaBlock, readModuleCss, topRuleBlock } from "../../styles/cssBlock";

const shared = readModuleCss(import.meta.url, "../../styles/floatingSurface.module.css");

// The shared class keeps its name; only the specificity wrapper is load
// bearing. A bare `.panel` rule ties every widget's own single-class
// override, and the shared rule can be emitted into a stylesheet that loads
// after the override - any stylesheet that composes it carries its own copy
// (787ca4427f's frame clip landed after the composer's override that way).
// :where() scores (0,0,0), so the widget's plain class always wins.
const PANEL_SELECTOR = ":where(.panel)";

// The three declarations that make the floating surface. A consumer that
// restates one of them next to its composes line has forked the recipe.
const SURFACE_DECLARATIONS = [
  /background:\s*var\(--surface-2\)/,
  /border-radius:\s*var\(--radius-pane\)/,
  /box-shadow:\s*var\(--shadow-overlay\)/,
];

test("the floating surface is one rule: raised fill, pane corner, overlay ring and drop", () => {
  const panel = topRuleBlock(shared, PANEL_SELECTOR);
  for (const declaration of SURFACE_DECLARATIONS) expect(panel).toMatch(declaration);
  // The overlay shadow embeds the 1px ring; a second border would double it.
  expect(panel).not.toMatch(/(^|\s)border:/);
});

test("the shared surface scores zero specificity so widget overrides always win", () => {
  expect(shared).toMatch(/^\s*:where\(\.panel\)\s*\{/m);
  expect(shared).not.toMatch(/^\s*\.panel\s*\{/m);
  expect(shared).not.toMatch(/!important/);
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
    expect(body).toMatch(/composes:\s*panel from "\.\.\/\.\.\/styles\/floatingSurface\.module\.css"/);
    for (const declaration of SURFACE_DECLARATIONS) expect(body).not.toMatch(declaration);
    if (localFill !== null) expect(body).toMatch(new RegExp(`background:\\s*var\\(${localFill}\\)`));
  },
);

test("sheet edge variants keep their square corners and side shadows over the shared defaults", () => {
  const sheet = readModuleCss(import.meta.url, "../sheet/sheet.module.css");
  // The mobile sessions drawer is side="left": square corners, the shadow
  // redirected off its one visible edge. The two vertical edges share one
  // plain single-class geometry, so it outranks the shared :where(.panel)
  // defaults and each edge adds only its anchor, border and shadow.
  const vertical = topRuleBlock(sheet, ".edgeVertical");
  expect(vertical).toMatch(/border-radius:\s*0/);

  const left = topRuleBlock(sheet, ".left");
  expect(left).toMatch(/composes:\s*edgeVertical/);
  expect(left).toMatch(/box-shadow:\s*16px 0 40px var\(--shadow-color\)/);

  const right = topRuleBlock(sheet, ".right");
  expect(right).toMatch(/composes:\s*edgeVertical/);
  expect(right).toMatch(/box-shadow:\s*-16px 0 40px var\(--shadow-color\)/);

  const bottom = topRuleBlock(sheet, ".bottom");
  expect(bottom).toMatch(/border-radius:\s*var\(--radius-panel\) var\(--radius-panel\) 0 0/);
  expect(bottom).toMatch(/box-shadow:\s*0 -16px 40px var\(--shadow-color\)/);

  expect(sheet).not.toMatch(/^\s*:where\(\.(left|right|bottom)\)/m);
});

test("the full-bleed phone directory picker keeps its square panel corners", () => {
  const picker = readModuleCss(import.meta.url, "../directorypicker/directorypicker.module.css");
  const phone = mediaBlock(picker, "max-width: 899px");
  expect(phone).toMatch(/\.panel\s*\{[^}]*border-radius:\s*0/);
});
