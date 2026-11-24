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

// A phone mounts no status bar, and the composer has carried no activity
// line since the counts folded into the header's Overview sheet (§12): the
// engaged composer is chips-free, and nothing may regrow a counts row over
// it. The chip vocabulary that remains is the statusbar's own strip, whose
// chips keep the coarse-pointer tap floor and drop the category names
// rather than scrolling sideways: each chip keeps its glyph and count, and
// its accessible name carries the label. CountButton owns the collapse and
// the floor; this pins where they live so a statusbar rule cannot shadow
// the widget's.
test("chips keep the coarse-pointer tap floor and the phone composer carries no activity line", () => {
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

// The folded row reserves the chrome's intrinsic width so a content-sized
// leading still reports the pickers it will hold. That reservation is sized
// for the full folded row (the status rung plus the menu); the phone's
// compact resting row holds the menu alone. While it applied there, WebKit
// sized the control row from the 356px reservation instead of the compact
// basis and the writing field collapsed to a quarter of the card, with a dead
// gap where the pickers would go. The reservation must therefore never apply
// to a compact row.
test("no leading rule reserves width on the phone's compact resting row", () => {
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
  expect(reserving.length, "the folded row still needs its intrinsic reservation").toBeGreaterThan(0);
  for (const selector of reserving) {
    // Every occurrence of the compact attribute must sit inside a :not(),
    // i.e. the selector subtracts compact rows rather than including them.
    const compactMentions = selector.match(/\[data-compact="true"\]/g) ?? [];
    const compactExclusions = selector.match(/:not\(\[data-compact="true"\]\)/g) ?? [];
    expect(compactMentions.length, `${selector} reserves width on a compact row`).toBe(compactExclusions.length);
    expect(compactExclusions.length, `${selector} must exclude the compact resting row`).toBeGreaterThan(0);
  }
});

// The radius vocabulary (Jesse 2026-11-23, from the mock/real comparison:
// "we have too many border radii"): the pill is control geometry - switch
// tracks, badges, avatars - never a SURFACE shape. A pill surface beside the
// square controls inside it is the collision the comparison caught (the
// composer's Send). Fluency surfaces carry ladder values: controls take
// --radius-control, small floating chrome takes --radius-pane, and card
// surfaces take --radius-panel - the same value the desktop prompt card
// already carries, so the phone composer is concentric with its own buttons
// again (16 - 8 inset = 8).
test("the phone fluency surfaces carry ladder radii, never a pill surface", () => {
  const srcRoot = dirname(dirname(fileURLToPath(import.meta.url)));
  const reads = (relative: string) => readFileSync(join(srcRoot, relative), "utf8").replace(/\/\*[\s\S]*?\*\//g, " ");
  const pillFree = (relative: string) =>
    expect(reads(relative), `${relative} still shapes a surface with the pill`).not.toContain("--radius-pill");
  pillFree("widgets/promptcard/promptcard.module.css");
  pillFree("shell/mobile/StackHost.module.css");
  pillFree("panes/session/composer/askdock/askdock.module.css");
  pillFree("panes/session/transcript/flow/loadolderrow.module.css");
  pillFree("panes/session/transcript/messages/usermessageitem.module.css");
  pillFree("shell/rail/RailResizeHandle.module.css");

  // The phone's floating chrome (back/menu capsules, the board toggle) sits
  // one ladder step above the buttons inside it: pane around control.
  const stackHost = reads("shell/mobile/StackHost.module.css");
  expect(stackHost.match(/\.boardHandle \{[^}]*\}/)?.[0]).toContain("var(--radius-pane)");
  expect(stackHost.match(/\.topBarCollapsed \.trailing \{[^}]*\}/)?.[0]).toContain("var(--radius-pane)");
  // The ask dock's answer tabs are controls, not capsules.
  expect(reads("panes/session/composer/askdock/askdock.module.css").match(/\.tab \{[^}]*\}/)?.[0]).toContain(
    "var(--radius-control)",
  );
  expect(reads("panes/session/transcript/flow/loadolderrow.module.css").match(/\.row \{[^}]*\}/)?.[0]).toContain(
    "var(--radius-pane)",
  );
  // The 2px accent lines round at any radius past their own width; keeping
  // them on the control token costs nothing visually and keeps the pill out
  // of the fluency vocabulary.
  expect(reads("shell/rail/RailResizeHandle.module.css").match(/\.resizeHandle::after \{[^}]*\}/)?.[0]).toContain(
    "var(--radius-control)",
  );
  expect(
    reads("panes/session/transcript/messages/usermessageitem.module.css").match(
      /\.provisional \.railmark \{[^}]*\}/,
    )?.[0],
  ).toContain("var(--radius-control)");
});

// The rail's New-session action is the session list's floating action: a
// trailing row inside the scroll body that pins to the scroll owner's
// bottom-right corner while rows scroll under it. Sticky - not fixed, not JS
// scroll listeners - is the contract because the rail's two scroll modes
// have different scrollers: rail-owned scroll scrolls .body itself, while
// parent-owned scroll (the phone drawer's sheet, the tablet band's column)
// scrolls a host ancestor, and a sticky trailing row pins against whichever
// of the two owns the scrolling - one rule, every surface the rail renders
// in. The z token is the one the tablet band's floating chrome uses, so
// tree rows pass under the pinned action - and the strip is transparent to
// pointers except where the button opts back in: a full-width pinned row
// that ate pointer events would block the whole row beneath it, not just
// the patch under the button (the session overlay dock's pass-through
// idiom).
test("the rail's New-session action is a sticky bottom-right floating row above the tree rows", () => {
  const srcRoot = dirname(dirname(fileURLToPath(import.meta.url)));
  const css = readFileSync(join(srcRoot, "shell/rail/Rail.module.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, " ");
  const rule = css.match(/\.newSessionRow\s*\{([^}]*)\}/)?.[1];
  expect(rule, "Rail.module.css must carry the floating action's sticky row").toBeDefined();
  expect(rule).toMatch(/position:\s*sticky/);
  expect(rule).toMatch(/bottom:\s*calc\(var\(--space-2\) \+ var\(--dock-clearance\)\)/);
  expect(rule).toMatch(/display:\s*flex/);
  expect(rule).toMatch(/justify-content:\s*flex-end/);
  expect(rule).toMatch(/z-index:\s*var\(--z-raised\)/);
  expect(rule).toMatch(/pointer-events:\s*none/);
  const optIn = css.match(/\.newSessionRow\s*>\s*\*\s*\{([^}]*)\}/)?.[1];
  expect(optIn, "the floating row's children must opt back into pointer events").toBeDefined();
  expect(optIn).toMatch(/pointer-events:\s*auto/);
});

test("the rail's New-session action rests at the bottom edge even when the list is short", () => {
  const srcRoot = dirname(dirname(fileURLToPath(import.meta.url)));
  const css = readFileSync(join(srcRoot, "shell/rail/Rail.module.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, " ");
  // The glue needs the scroll body to be a column with free space to
  // distribute and the row to claim it; otherwise the row parks after the
  // last row instead of the rail's bottom.
  const body = css.match(/\.body\s*\{([^}]*)\}/)?.[1];
  expect(body, "Rail.module.css must carry the scroll body rule").toBeDefined();
  expect(body).toMatch(/display:\s*flex/);
  expect(body).toMatch(/flex-direction:\s*column/);
  const row = css.match(/\.newSessionRow\s*\{([^}]*)\}/)?.[1];
  expect(row).toMatch(/margin-top:\s*auto/);
  // Parent-owns-scroll hosts (the phone drawer's sheet body, the tablet
  // band's column) must stretch the rail to their own height so the body
  // has the free space the glue consumes.
  const hostRail = css.match(/\.parentScrollRail\s*\{([^}]*)\}/)?.[1];
  expect(hostRail, "Rail.module.css must carry the parent-scroll rail rule").toBeDefined();
  expect(hostRail).toMatch(/min-height:\s*100%/);
  // In the phone drawer the sheet body element is the scroll owner, and the
  // shared dialog chrome leaves it content-sized; unless it stretches to the
  // panel's bottom the rail has no box to fill and the glue has no space.
  const panelCss = readFileSync(join(srcRoot, "shell/mobile/MobilePanel.module.css"), "utf8").replace(
    /\/\*[\s\S]*?\*\//g,
    " ",
  );
  const flushBody = panelCss.match(/\.flushBody\s*\{([^}]*)\}/)?.[1];
  expect(flushBody, "MobilePanel.module.css must carry the drawer's flush body rule").toBeDefined();
  expect(flushBody).toMatch(/flex:\s*1 1 auto/);
  expect(flushBody).toMatch(/min-height:\s*0/);
});
