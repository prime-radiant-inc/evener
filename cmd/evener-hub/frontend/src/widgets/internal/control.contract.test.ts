// @vitest-environment node

// Contract test for the shared form-control chrome
// (widgets/internal/control.module.css): Input, Select, Textarea, the picker
// trigger (fieldTrigger) and PathField's browse field all draw the same box,
// so it is declared once there and each composes `.field`, keeping its own
// metrics and its own disabled treatment. jsdom evaluates no cascade, so
// these are declaration-level assertions over the stylesheet sources,
// comments stripped.
import { expect, test } from "vitest";
import { escapeRegExp, readModuleCss, topRuleBlock } from "../../styles/cssBlock";

const read = (relativePath: string): string => readModuleCss(import.meta.url, relativePath);

const control = read("./control.module.css");

// The box chrome every consumer gets from `.field`.
const FIELD_DECLARATIONS = [
  /border-radius:\s*var\(--radius-control\)/,
  /border:\s*1px solid var\(--edge\)/,
  /background:\s*var\(--field\)/,
  /box-shadow:\s*var\(--shadow-inset-field\)/,
  /color:\s*var\(--ink-hi\)/,
];

test("the shared field declares the control box once", () => {
  const field = topRuleBlock(control, ".field");
  for (const declaration of FIELD_DECLARATIONS) expect(field).toMatch(declaration);
  expect(field).toMatch(/transition:[\s\S]*background-color var\(--motion-duration-hover\)/);
  expect(field).toMatch(/transition:[\s\S]*border-color var\(--motion-duration-hover\)/);
  expect(field).toMatch(/transition:[\s\S]*box-shadow var\(--motion-duration-hover\)/);
});

test("the field's edge strengthens on hover and focus-within, and it carries the focus ring", () => {
  expect(control).toMatch(
    /\.field:hover:not\(:disabled\),\s*\n\.field:focus-within\s*\{[^}]*border-color:\s*var\(--edge-strong\)/,
  );
  expect(control).toMatch(/\.field:focus-visible\s*\{[^}]*outline:\s*var\(--focus-ring\)/);
  expect(control).toMatch(/\.field:focus-visible\s*\{[^}]*outline-offset:\s*2px/);
});

// The picker trigger is a button, which does not inherit the body face, and
// PathField's field is monospace: a face baked into the shared box would
// silently re-face whichever composing widget disagreed.
test("the shared field stays face-neutral", () => {
  expect(topRuleBlock(control, ".field")).not.toContain("font-family");
});

const CONSUMERS: [file: string, selector: string, source: string][] = [
  ["../input/input.module.css", ".input", "../internal/control.module.css"],
  ["../select/select.module.css", ".select", "../internal/control.module.css"],
  ["../textarea/textarea.module.css", ".textarea", "../internal/control.module.css"],
  ["./fieldTrigger.module.css", ".trigger", "./control.module.css"],
  ["../pathfield/pathfield.module.css", ".input", "../internal/control.module.css"],
];

for (const [file, selector, source] of CONSUMERS) {
  test(`${file} ${selector} composes the shared field and restates none of its box`, () => {
    const css = read(file);
    const body = topRuleBlock(css, selector);
    expect(body).toContain(`composes: field from "${source}"`);
    for (const declaration of FIELD_DECLARATIONS) expect(body).not.toMatch(declaration);
    expect(body).not.toMatch(/\btransition:/);
    expect(css).not.toMatch(new RegExp(`${escapeRegExp(selector)}:(?:hover|focus-visible)`));
  });
}
