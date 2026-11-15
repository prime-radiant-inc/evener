// @vitest-environment node

// Contract test for the shared surface recipes (styles/surface.module.css):
// the grouped surface, its divided rows, the tool card and the field seat are
// each declared once there, and every consumer composes them rather than
// restating the box. jsdom evaluates no cascade, so these are
// declaration-level assertions over the stylesheet sources, comments
// stripped.
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { expect, test } from "vitest";

const SRC = dirname(dirname(fileURLToPath(import.meta.url)));
const read = (rel: string) => readFileSync(join(SRC, rel), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");

const surface = read("styles/surface.module.css");

function ruleBody(css: string, selector: string): string {
  const escaped = selector.replace(/[.[\]>:()~*+="]/g, (c) => `\\${c}`);
  const match = new RegExp(`(?:^|\\n)${escaped}\\s*\\{([^}]*)\\}`).exec(css);
  if (!match) throw new Error(`no rule ${selector}`);
  return match[1] ?? "";
}

// The four declarations that make the grouped surface. A consumer that
// restates one of them next to its composes line has forked the recipe.
const SURFACE_DECLARATIONS = [
  /border:\s*1px solid var\(--edge\)/,
  /border-radius:\s*var\(--radius-pane\)/,
  /background:\s*var\(--surface-1\)/,
  /box-shadow:\s*var\(--shadow-card\)/,
];

test("the grouped surface is one rule: hairline edge, pane corner, content surface, card lift", () => {
  const grouped = ruleBody(surface, ".grouped");
  for (const declaration of SURFACE_DECLARATIONS) expect(grouped).toMatch(declaration);
});

test("grouped rows draw a hairline between rows that render something, and none around them", () => {
  expect(ruleBody(surface, ".groupedRows > :not(:empty) ~ :not(:empty)")).toMatch(
    /border-top:\s*1px solid var\(--edge\)/,
  );
});

test.each([
  ["panes/spawn/spawn.module.css", ".directoryButton", ["grouped"]],
  ["panes/spawn/MobileSettingRows.module.css", ".config", ["grouped", "groupedRows"]],
  ["panes/settings/settings.module.css", ".clusterRowsGrouped", ["grouped", "groupedRows"]],
  ["panes/session/session.module.css", ".intentGroupRows", ["grouped", "groupedRows"]],
  ["panes/session/transcript/toolrungroup.module.css", ".body", ["groupedRows"]],
])("%s %s composes the shared surface and restates none of it", (file, selector, classes) => {
  const body = ruleBody(read(file), selector);
  for (const name of classes) {
    expect(body).toMatch(new RegExp(`composes:\\s*${name} from "[./]*styles/surface\\.module\\.css"`));
  }
  if (classes.includes("grouped")) {
    for (const declaration of SURFACE_DECLARATIONS) expect(body).not.toMatch(declaration);
  }
});

test("the tool card's insets are declared once and every row of a card reads them", () => {
  const insets = ruleBody(surface, ".toolRowInsets");
  expect(insets).toMatch(/--tool-card-inset:\s*var\(--space-3\)/);
  expect(insets).toMatch(/--tool-card-row-pad:\s*var\(--rhythm-item\)/);
  const declarations = (css: string) => css.match(/--tool-card-(?:inset|row-pad):/g) ?? [];
  expect(declarations(surface)).toHaveLength(2);
  for (const [file, selector] of [
    ["panes/session/transcript/toolcallitem.module.css", ".call"],
    ["panes/session/transcript/toolrungroup.module.css", ".summary"],
  ] as const) {
    const css = read(file);
    expect(ruleBody(css, selector)).toMatch(/composes:\s*toolRowInsets from "[./]*styles\/surface\.module\.css"/);
    expect(declarations(css)).toHaveLength(0);
  }
});

test("a tool card drawn across sibling rows is the same surface, sliced by position", () => {
  // Every slice carries the side edges, the surface and a top hairline, so a
  // row divides from the one above it without a rule of its own.
  const slice = ruleBody(surface, ".toolRunSegment > *");
  expect(slice).toMatch(/border-inline:\s*1px solid var\(--edge\)/);
  expect(slice).toMatch(/border-top:\s*1px solid var\(--edge\)/);
  expect(slice).toMatch(/background:\s*var\(--surface-1\)/);
  // The outer corners belong to the card's first and last rows. They are
  // custom properties on the wrapper, which a row root inside a details can
  // still read; the slice draws them.
  expect(slice).toMatch(/border-radius:[^;]*var\(--tool-card-radius-top\)[^;]*var\(--tool-card-radius-bottom\)/);
  expect(surface).toMatch(
    /\.toolRunSegment\[data-tool-card="first"\],\s*\.toolRunSegment\[data-tool-card="only"\]\s*\{[^}]*--tool-card-radius-top:\s*var\(--radius-pane\)/,
  );
  expect(surface).toMatch(
    /\.toolRunSegment\[data-tool-card="last"\],\s*\.toolRunSegment\[data-tool-card="only"\]\s*\{[^}]*--tool-card-radius-bottom:\s*var\(--radius-pane\)/,
  );
  expect(surface).toMatch(
    /\.toolRunSegment\[data-tool-card="last"\] > \*,\s*\.toolRunSegment\[data-tool-card="only"\] > \*\s*\{[^}]*box-shadow:\s*var\(--shadow-card\)/,
  );
});

test("the field seat is one ground at two named sizes", () => {
  expect(surface).toMatch(
    /\.fieldSeat,\s*\.fieldSeatBehind::before\s*\{[^}]*border-radius:\s*var\(--radius-control\)[^}]*background:\s*var\(--field\)/,
  );
  expect(ruleBody(surface, ".fieldSeat")).toMatch(/width:\s*var\(--field-seat-size\)/);
  const behind = ruleBody(surface, ".fieldSeatBehind::before");
  expect(behind).toMatch(/width:\s*var\(--field-seat-size-row\)/);
  // Placed with insets: a transform on the slot's ground would make every
  // tool row a stacking context.
  expect(behind).toMatch(/top:\s*calc\(50% - var\(--field-seat-size-row\) \/ 2\)/);
  expect(behind).not.toMatch(/transform/);
  expect(ruleBody(read("panes/spawn/spawn.module.css"), ".tileSeat")).toMatch(
    /composes:\s*fieldSeat from "[./]*styles\/surface\.module\.css"/,
  );
  expect(ruleBody(read("panes/session/transcript/toolcallitem.module.css"), ".rowIconSlot")).toMatch(
    /composes:\s*fieldSeatBehind from "[./]*styles\/surface\.module\.css"/,
  );
});
