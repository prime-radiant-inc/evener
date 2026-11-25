// @vitest-environment node

// Contract test for the shared surface recipes (styles/surface.module.css):
// the grouped surface, its divided rows, the tool row insets and the field seat are
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

test("single calls and folded runs share their row insets", () => {
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

test("the field seat is one ground for a tile's leading glyph", () => {
  const seat = ruleBody(surface, ".fieldSeat");
  expect(seat).toMatch(/border-radius:\s*var\(--radius-control\)/);
  expect(seat).toMatch(/background:\s*var\(--field\)/);
  expect(seat).toMatch(/width:\s*var\(--field-seat-size\)/);
});

test("the pill is geometry only, so a widget's own face override decides the font", () => {
  // The face override has to live in the same sheet as the class it beats:
  // this sheet's chunk can load after a widget's, and the chip's mono face
  // would otherwise lose to the pill's sans copy.
  expect(ruleBody(surface, ".pill")).not.toMatch(/font-family/);
  for (const [file, selector] of [
    ["widgets/badge/badge.module.css", ".badge"],
    ["widgets/chip/chip.module.css", ".chip"],
  ] as const) {
    expect(ruleBody(read(file), selector)).toMatch(/font-family:\s*var\(--font-sans\)/);
  }
});

test("the left-rail body is one recipe and both daemon notices compose it", () => {
  const recipe = ruleBody(surface, ".railBody");
  expect(recipe).toMatch(/padding:\s*var\(--space-2\)/);
  expect(recipe).toMatch(/border-left:\s*2px solid var\(--edge\)/);
  expect(recipe).toMatch(/font-family:\s*var\(--font-sans\)/);
  expect(recipe).toMatch(/font-size:\s*var\(--font-size-body\)/);
  expect(recipe).toMatch(/color:\s*var\(--ink-mid\)/);
  for (const [file, selector] of [
    ["panes/session/transcript/messages/steeringitem.module.css", ".body"],
    ["panes/session/transcript/messages/memorycontextitem.module.css", ".body"],
  ] as const) {
    const consumer = ruleBody(read(file), selector);
    expect(consumer).toMatch(/composes:\s*railBody from "[./]*styles\/surface\.module\.css"/);
    expect(consumer).not.toMatch(/border-left:/);
    expect(consumer).not.toMatch(/padding:\s*var\(--space-2\)/);
  }
});

test("the prompt card's leading cluster and attachment strip are one recipe each", () => {
  const leading = ruleBody(surface, ".cardLeading");
  expect(leading).toMatch(/display:\s*flex/);
  expect(leading).toMatch(/flex:\s*1 1 auto/);
  expect(leading).toMatch(/min-width:\s*0/);
  expect(leading).toMatch(/align-items:\s*center/);
  expect(leading).toMatch(/gap:\s*var\(--space-1\)/);
  const strip = ruleBody(surface, ".attachmentStrip");
  expect(strip).toMatch(/display:\s*flex/);
  expect(strip).toMatch(/flex-wrap:\s*wrap/);
  expect(strip).toMatch(/gap:\s*var\(--space-2\)/);
  // The session composer and the spawn form render the same PromptCard row
  // and the same staged-tile strip; each keeps only its own local additions.
  for (const [file, selector, recipe] of [
    ["panes/session/composer/composer.module.css", ".leading", "cardLeading"],
    ["panes/spawn/spawn.module.css", ".leading", "cardLeading"],
    ["panes/session/composer/composer.module.css", ".attachments", "attachmentStrip"],
    ["panes/spawn/spawn.module.css", ".attachments", "attachmentStrip"],
  ] as const) {
    const body = ruleBody(read(file), selector);
    expect(body, `${file} ${selector}`).toMatch(
      new RegExp(`composes:\\s*${recipe} from "[./]*styles/surface\\.module\\.css"`),
    );
    expect(body, `${file} ${selector}`).not.toMatch(/display:\s*flex/);
  }
});

test("the reading measure is one centered column recipe", () => {
  const measure = ruleBody(surface, ".measureColumn");
  expect(measure).toMatch(/width:\s*100%/);
  expect(measure).toMatch(/max-width:\s*var\(--session-measure\)/);
  expect(measure).toMatch(/margin-inline:\s*auto/);
  // These surfaces render outside .turn (virtual-list rows, a docked bar, the
  // spawn form, the cold-start block), so each must read the measure itself.
  // Settings' .content is bounded but not centered and is deliberately absent.
  for (const [file, selector] of [
    ["panes/session/transcript/turnblock.module.css", ".turn"],
    ["panes/session/transcript/messages/pendingrowstack.module.css", ".stack"],
    ["panes/session/find/findbar.module.css", ".dock"],
    ["panes/session/composer/askDock/askdock.module.css", ".dock"],
    ["panes/session/session.module.css", ".intentGroup"],
    ["panes/session/session.module.css", ".coldStart"],
    ["panes/session/session.module.css", ".loadingSkeleton"],
    ["panes/session/session.module.css", ".measure"],
    ["panes/spawn/spawn.module.css", ".form"],
  ] as const) {
    const body = ruleBody(read(file), selector);
    expect(body, `${file} ${selector}`).toMatch(/composes:\s*measureColumn from "[./]*styles\/surface\.module\.css"/);
    expect(body, `${file} ${selector}`).not.toMatch(/max-width:\s*var\(--session-measure\)/);
  }
  // The cold-start skeleton used to cap at a hard-coded 42rem instead of the
  // shared measure.
  expect(read("panes/session/session.module.css")).not.toMatch(/42rem/);
});
