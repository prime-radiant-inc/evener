// @vitest-environment node
import { readdirSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { expect, test } from "vitest";
import { MOBILE_QUERY } from "../shell/useIsMobile";
import { escapeRegExp } from "./cssBlock";

// The font-size and phone-density preference gates (Settings -> Theme) live in
// tokens.css: prefs.ts mirrors the choices onto <body data-font-size> and
// <body data-phone-density>, and these rules are what finally make the
// persisted preference change what the page renders (before this, the legacy
// set the attributes but nothing in the new design system keyed off them -
// prefs.ts:44-49). jsdom resolves neither custom-property var() nor calc(), so
// a live "the text actually got bigger" assertion is impossible here; this
// reads the stylesheet off disk (same mechanism as token-contract.test.ts) and
// pins the gate's structure so a regression that drops a preset, unhooks the
// ramp from --font-scale, or moves density off the phone media query fails
// loudly.
const STYLES_DIR = dirname(fileURLToPath(import.meta.url));
const TOKENS_CSS = readFileSync(join(STYLES_DIR, "tokens.css"), "utf8");

// Block comments stripped first so a class/token mentioned only in prose can
// never satisfy an assertion (same discipline as token-contract.test.ts).
const CSS = TOKENS_CSS.replace(/\/\*[\s\S]*?\*\//g, " ");

// The scale each data-font-size preset sets on --font-scale, read out of the
// `body[data-font-size="<v>"] { --font-scale: <n>; }` rule.
function fontScaleFor(value: string): string | null {
  const rule = new RegExp(`body\\[data-font-size="${value}"\\]\\s*\\{[^}]*--font-scale:\\s*([0-9.]+)`).exec(CSS);
  return rule ? rule[1]! : null;
}

// The compact/default multiplier has no body[data-phone-density="compact"]
// rule of its own - it's seeded on the bare `body { }` rule inside the
// media query (shared with the --line-height-body declaration next to it).
// Scoped to that one rule specifically, same shape as fontScaleFor above,
// so this can't be satisfied by any OTHER --density-scale declaration in
// the media block (see the test below for why that scoping matters here).
function baseDensityScale(mediaBlockContent: string): string | null {
  const bareBodyRule = /\bbody\s*\{([^}]*)\}/.exec(mediaBlockContent);
  const decl = bareBodyRule ? /--density-scale:\s*([0-9.]+)/.exec(bareBodyRule[1]!) : null;
  return decl ? decl[1]! : null;
}

test("each data-font-size preset maps to its pinned --font-scale multiplier", () => {
  expect(fontScaleFor("s")).toBe("0.9");
  expect(fontScaleFor("m")).toBe("1");
  expect(fontScaleFor("l")).toBe("1.1");
  expect(fontScaleFor("xl")).toBe("1.25");
});

test("the type ramp is declared on <body> and multiplies through var(--font-scale)", () => {
  // Declared on body (not :root): a var(--font-scale) reference resolves
  // against the element the property is declared on, and the attribute lands
  // on <body>. Every ramp step must route through the scale or that step
  // silently ignores the preference.
  for (const token of [
    "--font-size-caption",
    "--font-size-ui",
    "--font-size-body",
    "--font-size-pane-title",
    "--font-size-page-title",
  ]) {
    const declared = new RegExp(`${token}:\\s*calc\\([^;]*var\\(--font-scale\\)`).test(CSS);
    expect(declared, `${token} must scale through var(--font-scale)`).toBe(true);
  }
});

test("the type ramp no longer sits as raw px in :root (single source of truth is the scaled body ramp)", () => {
  const rootBlock = /:root(?:\s*,\s*\[data-theme="dark"\])?\s*\{([\s\S]*?)\n\}/.exec(CSS);
  expect(rootBlock, "tokens.css must have a canonical dark block").not.toBeNull();
  expect(rootBlock![1]).not.toMatch(/--font-size-body:/);
});

test("phone density is gated behind the <=899px phone media query", () => {
  // Desktop must never inherit a density override: the whole gate lives inside
  // the phone media query. Assert the comfortable multiplier is inside a
  // mobile block, not at top level.
  const media = new RegExp(`@media\\s*${escapeRegExp(MOBILE_QUERY)}\\s*\\{([\\s\\S]*?)\\n\\}`).exec(CSS);
  expect(media, "tokens.css must have a mobile media block").not.toBeNull();
  expect(media![1]).toMatch(/body\[data-phone-density="comfortable"\]\s*\{[^}]*--density-scale:\s*1\.25/);
});

test("phone density opens vertical rhythm by scaling line-height through --density-scale", () => {
  const media = new RegExp(`@media\\s*${escapeRegExp(MOBILE_QUERY)}\\s*\\{([\\s\\S]*?)\\n\\}`).exec(CSS);
  expect(media![1]).toMatch(/--line-height-body:\s*calc\([^;]*var\(--density-scale\)/);
});

test("the compact density default leaves the base grid unscaled (multiplier 1)", () => {
  const media = new RegExp(`@media\\s*${escapeRegExp(MOBILE_QUERY)}\\s*\\{([\\s\\S]*?)\\n\\}`).exec(CSS);
  // The base body rule inside the media query seeds --density-scale: 1 so
  // "compact" (and any unset value) holds the base line-height. Reads the
  // bare body rule's own value (baseDensityScale above) rather than
  // pattern-matching "1" anywhere in the media block - a bare
  // /--density-scale:\s*1\b/ also matches the "1" prefix of the
  // comfortable rule's "1.25" a few lines down (\b is satisfied by the
  // "1" -> "." transition, since "." is a non-word character), so it
  // cannot tell "the seed is really 1" from "the seed was deleted and
  // this is just comfortable's 1.25 leaking through".
  expect(baseDensityScale(media![1]!)).toBe("1");
});

// The tap floor reaches every coarse pointer (touch laptops, iPad landscape),
// not only the phone width (Jesse 2026-10-11); the font-size and density
// rules above stay phone-only.
const TAP_FLOOR_QUERY = `${MOBILE_QUERY}, (any-pointer: coarse)`;

// Each top-level @media block's prelude and body, comments stripped.
function mediaBlocks(source: string): { prelude: string; body: string }[] {
  const css = source.replace(/\/\*[\s\S]*?\*\//g, " ");
  const blocks: { prelude: string; body: string }[] = [];
  const at = /@media\s*([^{]*)\{/g;
  for (let match = at.exec(css); match; match = at.exec(css)) {
    let depth = 1;
    let end = at.lastIndex;
    while (depth > 0 && end < css.length) {
      if (css[end] === "{") depth++;
      else if (css[end] === "}") depth--;
      end++;
    }
    blocks.push({ prelude: match[1]!.trim(), body: css.slice(at.lastIndex, end - 1) });
    at.lastIndex = end;
  }
  return blocks;
}

// tokens.css parsed once: every media-block test below shares this walk.
const TOKENS_BLOCKS = mediaBlocks(CSS);

test("the 44px touch-target floor is declared for phones and coarse pointers (2026-07-30-mobile-session-layout-design.md, decision 4)", () => {
  const blocks = TOKENS_BLOCKS;
  const tapFloor = blocks.find((block) => block.prelude === TAP_FLOOR_QUERY);
  expect(tapFloor, `tokens.css must have a ${TAP_FLOOR_QUERY} media block`).toBeDefined();
  expect(tapFloor!.body).toMatch(/--tap-min:\s*44px/);
  for (const block of blocks.filter((b) => b.prelude !== TAP_FLOOR_QUERY)) {
    expect(block.body, `--tap-min declared under ${block.prelude}`).not.toMatch(/--tap-min:/);
  }
});

test("every widget tap floor applies on phones and on coarse pointers", () => {
  const widgetsDir = join(dirname(STYLES_DIR), "widgets");
  const consumers = walkCss(widgetsDir).filter((file) => /var\(--tap-min/.test(readFileSync(file, "utf8")));
  expect(consumers.length).toBeGreaterThan(0);
  for (const file of consumers) {
    const source = readFileSync(file, "utf8");
    const total = source.replace(/\/\*[\s\S]*?\*\//g, " ").match(/var\(--tap-min/g)!.length;
    const gated = mediaBlocks(source)
      .filter((block) => block.prelude === TAP_FLOOR_QUERY)
      .reduce((sum, block) => sum + (block.body.match(/var\(--tap-min/g)?.length ?? 0), 0);
    expect(gated, `${file.slice(widgetsDir.length + 1)}: every var(--tap-min) sits in @media ${TAP_FLOOR_QUERY}`).toBe(
      total,
    );
  }
});

// --- editable controls are 16px on phones and on any coarse pointer -------
//
// index.html no longer locks zoom (viewport-pin.test.ts), so the guarantee
// that iOS Safari has nothing to auto-zoom into moves here: every rule that
// styles an editable control - an input/select/textarea element selector, or
// one of the classes the widgets and panes put on their own editable
// controls, including the composer's contenteditable .editor - sizes its
// text from --font-size-control (the ui step on fine-pointer widths, floored
// at 16px on phones and on any coarse pointer at any width) or
// --font-size-body. roborev on PR #947 caught the inputs and selects that
// still took --font-size-ui after the lock was removed.
const CONTROL_SELECTOR_RE =
  /(^|[\s,>+~(])(input|select|textarea)(\b|[:.[])|\.(input|select|textarea|textInput|effortSelect|editor)(\b|[:.[])/;
// var(--font-size-control), or the textarea's max(control, body) - never the
// body size alone, which the S preference scales under 16px on phones.
const CONTROL_SIZE_RE =
  /font-size:\s*(var\(--font-size-control\)|max\(var\(--font-size-control\), var\(--font-size-body\)\))/;

function walkCss(dir: string): string[] {
  const found: string[] = [];
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name);
    if (entry.isDirectory()) found.push(...walkCss(full));
    else if (entry.isFile() && entry.name.endsWith(".css")) found.push(full);
  }
  return found;
}

test("--font-size-control is the ui step on fine pointers and floors at 16px on phones and coarse pointers", () => {
  expect(CSS).toMatch(/\n\s*--font-size-control: var\(--font-size-ui\);/);
  // The floor shares --tap-min's reach (TAP_FLOOR_QUERY above): phones, and
  // any coarse pointer at any width. The iPhone Duo's inner display is 951pt
  // in landscape - past the 899px phone boundary - while still running iOS
  // Safari, which auto-zooms into any focused field under 16px, so a
  // phone-only floor leaves it zooming. max(16px, body, ui) is a floor, not an
  // alias: the S preference scales the phone body to 14.4px and the ui step
  // to 11.7px, and iOS zooms into a 14.4px field just the same (roborev on
  // PR #947).
  const blocks = TOKENS_BLOCKS;
  const floor = blocks.find((block) => block.prelude === TAP_FLOOR_QUERY);
  expect(floor, `tokens.css must have a ${TAP_FLOOR_QUERY} media block`).toBeDefined();
  expect(floor!.body).toMatch(/--font-size-control: max\(16px, var\(--font-size-body\), var\(--font-size-ui\)\);/);
  for (const block of blocks.filter((b) => b.prelude !== TAP_FLOOR_QUERY)) {
    expect(block.body, `--font-size-control declared under ${block.prelude}`).not.toMatch(/--font-size-control:/);
  }
  // The phone body the floor lifts from stays 16px, so fields match the body
  // text on phones.
  const phone = blocks.find((block) => block.prelude === MOBILE_QUERY);
  expect(phone, "tokens.css must have a phone media block").toBeDefined();
  expect(phone!.body).toMatch(/--font-size-body: calc\(16px \* var\(--font-scale\)\);/);
});

test("every rule that sizes an editable control uses --font-size-control or --font-size-body", () => {
  const srcRoot = dirname(dirname(fileURLToPath(import.meta.url)));
  for (const file of walkCss(srcRoot)) {
    const css = readFileSync(file, "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
    for (const block of css.matchAll(/([^{}]+)\{([^{}]*)\}/g)) {
      // The whole captured selector, not its last line: a control can sit on
      // any line of a multi-line selector list.
      const selector = block[1]!.trim();
      const body = block[2]!;
      if (!CONTROL_SELECTOR_RE.test(selector)) continue;
      if (!/font-size:/.test(body)) continue;
      expect(body, `${file.slice(srcRoot.length + 1)}: ${selector}`).toMatch(CONTROL_SIZE_RE);
    }
  }
});

test("tokens.css is the only stylesheet that declares --font-size-control", () => {
  // The floor's reach is the touch block's media query: a redeclaration in a
  // pane or widget stylesheet under a narrower gate would silently drop the
  // 16px floor on wide touch screens (the Duo's 951pt landscape rides the
  // coarse arm of the query, not the width arm), which is exactly what the
  // web UI design system doc says this file catches.
  const srcRoot = dirname(dirname(fileURLToPath(import.meta.url)));
  for (const file of walkCss(srcRoot)) {
    if (file.endsWith("tokens.css")) continue;
    const css = readFileSync(file, "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
    expect(css, `${file.slice(srcRoot.length + 1)} redeclares --font-size-control`).not.toMatch(
      /--font-size-control\s*:/,
    );
  }
});

// The folded tool-run row is an interactive control (a <summary> the reader
// taps to open the calls), so it takes the same phone tap floor
// widgets/disclosure's own summary does (roborev on PR #947).
test("the folded-run summary meets the phone tap floor", () => {
  const srcRoot = dirname(dirname(fileURLToPath(import.meta.url)));
  const css = readFileSync(join(srcRoot, "panes/session/transcript/toolrungroup.module.css"), "utf8");
  const phone = new RegExp(`@media\\s*${escapeRegExp(MOBILE_QUERY)}\\s*\\{([\\s\\S]*?)\\n\\}`).exec(css);
  expect(phone, "toolrungroup.module.css must have a mobile block").not.toBeNull();
  expect(phone![1]).toMatch(/\.summary\s*\{[^}]*min-height:\s*var\(--tap-min\)/);
});

// A phone mounts no status bar: the session's activity counts sit on their
// own line above the composer card, and
// the writing row carries only its controls. The line stays a flex strip at
// the phone tap floor and drops the category names rather than scrolling
// sideways: four named chips do not fit a card line, so each chip keeps its
// glyph and count and its accessible name carries the label. CountButton
// owns the collapse and the floor now; this pins where they live so a
// statusbar rule cannot shadow the widget's.
test("the phone's activity line keeps every chip a tappable glyph", () => {
  const srcRoot = dirname(dirname(fileURLToPath(import.meta.url)));
  const css = readFileSync(join(srcRoot, "shell/statusbar/statusbar.module.css"), "utf8");
  const blocks = mediaBlocks(css);
  const phone = blocks.find((block) => block.prelude === MOBILE_QUERY);
  expect(phone, "statusbar.module.css must have a mobile block").toBeDefined();
  // The narrow-pane grid below (two max-content columns) belongs to the
  // desktop strip; the phone's own line stays a flex row. mediaBlocks strips
  // comments, so this reads rules only.
  expect(phone!.body).toMatch(/\.chips\s*\{[^}]*display:\s*flex/);
  const widgetCss = readFileSync(join(srcRoot, "widgets/countbutton/countbutton.module.css"), "utf8").replace(
    /\/\*[\s\S]*?\*\//g,
    " ",
  );
  const widgetBlocks = mediaBlocks(widgetCss);
  const widgetFloor = widgetBlocks.find((block) => block.prelude === TAP_FLOOR_QUERY);
  expect(widgetFloor, `countbutton.module.css must have a ${TAP_FLOOR_QUERY} block`).toBeDefined();
  expect(widgetFloor!.body).toMatch(/\.root\s*\{[^}]*min-height:\s*var\(--tap-min\)[^}]*min-width:\s*var\(--tap-min\)/);
  // compact drops the visible name; the accessible name keeps it.
  expect(widgetCss).toMatch(/\.root\[data-compact="true"\]\s+\.label\s*\{[^}]*display:\s*none/);
  // Only the statusbar's narrow container drops the glyph; the phone keeps
  // it. The name matters: a phone composer is an inline-size container too,
  // so an unnamed query would resolve there and hide the phone's glyphs.
  const narrow = /@container\s+statusbar\s*\(max-width:\s*320px\)\s*\{([\s\S]*?)\n\}/.exec(widgetCss);
  expect(narrow, "countbutton.module.css must have its named 320px statusbar container rule").not.toBeNull();
  expect(widgetCss).toMatch(/@container\s+statusbar\s*\(max-width:\s*600px\)/);
  expect(narrow![1]).toMatch(/\.glyph\s*\{[^}]*display:\s*none/);
  expect(
    widgetBlocks
      .filter((block) => block.prelude === MOBILE_QUERY)
      .every((block) => !/\.glyph\s*\{[^}]*display:\s*none/.test(block.body)),
    "the phone block must not hide the glyph",
  ).toBe(true);
  const composerCss = readFileSync(join(srcRoot, "panes/session/composer/composer.module.css"), "utf8").replace(
    /\/\*[\s\S]*?\*\//g,
    " ",
  );
  // The removal is the contract: no counts row may return over the composer.
  expect(composerCss).not.toMatch(/activityRow/);
  const chromeCss = readFileSync(join(srcRoot, "panes/session/chrome/sessionchrome.module.css"), "utf8").replace(
    /\/\*[\s\S]*?\*\//g,
    " ",
  );
  // The pending-question mount stands alone in the composer's vertical
  // column, so the shared .inline rule must keep an automatic basis: a fixed
  // width-oriented rung there would supply a height basis and inflate the
  // chrome out of the transcript.
  const inline = /\.inline\s*\{[^}]*flex:\s*1 1 auto[^}]*min-width:\s*0[^}]*\}/.exec(chromeCss);
  const rung = /\.inlineRung\s*\{[^}]*flex-basis:\s*200px/.exec(chromeCss);
  expect(
    inline,
    "sessionchrome.module.css must keep the standalone composer chrome on an automatic basis",
  ).not.toBeNull();
  expect(
    rung,
    "sessionchrome.module.css must give the composer chrome its compact-rung flex basis so the row wraps instead of starving it",
  ).not.toBeNull();
});

// No .leading rule may reserve a fixed intrinsic inline size: doing so starves
// the writing field behind a dead gap whenever the actual chrome is narrower
// than the reservation (both on phone compact rows and on desktop folded rows).
test("no leading rule reserves a fixed intrinsic width", () => {
  const srcRoot = dirname(dirname(fileURLToPath(import.meta.url)));
  const css = readFileSync(join(srcRoot, "panes/session/composer/composer.module.css"), "utf8").replace(
    /\/\*[\s\S]*?\*\//g,
    " ",
  );
  const reserving: string[] = [];
  for (const block of css.matchAll(/([^{}@]+)\{([^{}]*)\}/g)) {
    const selector = block[1]!.trim().split("\n").pop()!.trim();
    if (!selector.startsWith(".leading")) continue;
    const declared = /contain-intrinsic-inline-size:\s*([^;]+)/.exec(block[2]!);
    if (declared === null || declared[1]!.trim() === "none") continue;
    reserving.push(selector);
  }
  expect(reserving).toEqual([]);
});
