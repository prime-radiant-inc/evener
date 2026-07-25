// @vitest-environment node
// fieldTrigger.module.css is the ONE field-shaped picker-trigger recipe. Both
// composing widgets (pathfield, modelCatalog) previously carried their own
// copy of the box, and the copies drifted before they were folded back
// together. These tests keep the recipe single-sourced: a widget that
// re-declares the box locally instead of composing this module fails here.
//
// Every read strips CSS comments FIRST. A contract test in this tree once
// passed with its implementation deleted because a doc comment quoted the
// very declarations it asserted.
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { expect, test } from "vitest";

const WIDGETS_ROOT = join(dirname(fileURLToPath(import.meta.url)), "..");

function declarations(relativePath: string): string {
  return readFileSync(join(WIDGETS_ROOT, relativePath), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
}

const SHARED = declarations("internal/fieldTrigger.module.css");

// The recipe's own box. These are the declarations the composing widgets get
// for free, so the shared module has to actually carry them.
test("the shared recipe declares the control-shaped box", () => {
  const trigger = SHARED.match(/\.trigger\s*\{([^}]*)\}/)?.[1] ?? "";
  expect(trigger).toContain("box-sizing: border-box");
  expect(trigger).toContain("width: 100%");
  expect(trigger).toContain("height: 32px");
  expect(trigger).toContain("padding: 0 var(--space-3)");
  expect(trigger).toContain("border: 1px solid var(--edge)");
  expect(trigger).toContain("border-radius: var(--radius-control)");
});

// A button does NOT inherit the body font, so the face is the one thing each
// composing widget must still state for itself: mono for a path read
// character by character, sans for a model id read as prose. The shared
// recipe deliberately stays face-neutral - baking a face here would silently
// re-face whichever widget disagreed.
test("the shared recipe leaves the type face to the composing widget", () => {
  expect(SHARED).not.toContain("font-family");
});

const COMPOSERS = [
  { widget: "modelCatalog", face: "var(--font-sans)" },
  { widget: "pathfield", face: "var(--font-mono)" },
];

for (const { widget, face } of COMPOSERS) {
  const css = declarations(`${widget}/${widget}.module.css`);

  test(`${widget} composes the shared trigger rather than copying it`, () => {
    for (const part of ["trigger", "value", "default", "chevron", "srOnly"]) {
      expect(css).toContain(`composes: ${part} from "../internal/fieldTrigger.module.css"`);
    }
  });

  test(`${widget} states its own type face on the trigger`, () => {
    const trigger = css.match(/\.trigger\s*\{([^}]*)\}/)?.[1] ?? "";
    expect(trigger).toContain(`font-family: ${face}`);
  });

  // The point of the fold: no local re-declaration of the box. A widget that
  // reintroduces its own height/border/padding on .trigger is back to two
  // copies that can drift.
  test(`${widget} does not re-declare the shared box locally`, () => {
    const trigger = css.match(/\.trigger\s*\{([^}]*)\}/)?.[1] ?? "";
    for (const dropped of ["height:", "padding:", "border:", "border-radius:", "background:", "box-sizing:"]) {
      expect(trigger).not.toContain(dropped);
    }
  });
}
