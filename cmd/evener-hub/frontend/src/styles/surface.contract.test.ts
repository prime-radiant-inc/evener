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
  ["widgets/pathfield/pathfield.module.css", ".prominentTrigger", ["grouped", "summaryTile"]],
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

test("the summary tiles share one row, text column, headline, and caption", () => {
  expect(ruleBody(surface, ".summaryTile")).toMatch(/display:\s*flex/);
  expect(ruleBody(surface, ".summaryText")).toMatch(/flex-direction:\s*column/);
  expect(ruleBody(surface, ".summaryHeadline")).toMatch(/font-weight:\s*var\(--font-weight-medium\)/);
  expect(ruleBody(surface, ".summaryCaption")).toMatch(/font-size:\s*var\(--font-size-caption\)/);
  for (const [file, selector, shared] of [
    ["widgets/pathfield/pathfield.module.css", ".prominentText", "summaryText"],
    ["widgets/pathfield/pathfield.module.css", ".prominentHeadline", "summaryHeadline"],
    ["widgets/pathfield/pathfield.module.css", ".prominentCaption", "summaryCaption"],
  ] as const) {
    expect(ruleBody(read(file), selector)).toMatch(
      new RegExp(`composes:\\s*${shared} from "[./]*styles/surface\\.module\\.css"`),
    );
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

test("the field seat is one ground for a tile's leading glyph", () => {
  const seat = ruleBody(surface, ".fieldSeat");
  expect(seat).toMatch(/border-radius:\s*var\(--radius-control\)/);
  expect(seat).toMatch(/background:\s*var\(--field\)/);
  expect(seat).toMatch(/width:\s*var\(--field-seat-size\)/);
});
