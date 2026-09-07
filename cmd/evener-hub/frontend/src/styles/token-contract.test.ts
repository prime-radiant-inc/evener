import { readdirSync, readFileSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";
import { expect, test } from "vitest";

// Every stylesheet under src, keyed by a path relative to src/ itself, read
// straight off disk with node:fs (types: src/styles/node-fs-shim.d.ts).
// Vite's own `?raw` import can't do this reliably: under vitest's default
// `test.css: false`, a .css?raw import resolves to an empty string (the
// css-disable transform short-circuits before raw-query handling runs -
// https://github.com/vitest-dev/vitest/issues/10788), and the documented
// fix (`test.css: true`) means editing vite.config.ts, which this task may
// not touch. Reading the files directly sidesteps the whole transform
// pipeline.
const SRC_ROOT = dirname(dirname(fileURLToPath(import.meta.url))); // src/styles/.. = src

function walkCssFiles(dir: string): string[] {
  const found: string[] = [];
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name);
    if (entry.isDirectory()) found.push(...walkCssFiles(full));
    else if (entry.isFile() && entry.name.endsWith(".css")) found.push(full);
  }
  return found;
}

const STYLESHEETS: Record<string, string> = {};
for (const absPath of walkCssFiles(SRC_ROOT)) {
  STYLESHEETS[relative(SRC_ROOT, absPath)] = readFileSync(absPath, "utf8");
}

const tokensPath = Object.keys(STYLESHEETS).find((path) => path.endsWith("tokens.css"));
if (!tokensPath) throw new Error("token-contract test: could not locate tokens.css under src");
const TOKENS_CSS = STYLESHEETS[tokensPath]!;

// Every stylesheet except tokens.css itself: component CSS Modules plus the
// single base global.css. tokens.css is where literals are SUPPOSED to
// live, so it's exempt from the "no literal" and "no bare semantic var"
// checks below and is instead the subject of its own dark/light check.
const OTHER_STYLESHEETS = Object.entries(STYLESHEETS).filter(([path]) => path !== tokensPath);

function basenameOf(path: string): string {
  return path.slice(path.lastIndexOf("/") + 1);
}

// Guards the file-naming half of the widget convention (one dir per widget:
// index.tsx + <name>.module.css + <name>.test.tsx) that the rest of this
// contract - and every stream after this one - assumes holds.
//
// shell/dockview-theme.css is the one deliberate exception: it restyles a
// third-party library (dockview) via a plain, UNSCOPED class selector
// (.dockview-theme-evener, passed to DockviewReact's own className prop -
// see DockHost.tsx), which a `.module.css` file cannot do (CSS Modules
// hash every class name, and dockview needs to see the literal class it's
// told to apply). The wave-3 plan's Global Constraints name this file
// explicitly as being "on the token-contract allowlist for referencing
// --surface/--edge/--ink vars only" - every OTHER mechanism in this
// contract (no chromatic literal, the attention/alive/danger allowlist)
// still applies to it unchanged, exactly like every other stylesheet; only
// the naming rule gets a named exception, here.
//
// Keyed by the EXACT path (not the basename): a same-named decoy file
// anywhere else in the tree (e.g. widgets/dockview-theme.css) must NOT
// ride along on this one file's exception - see the poison test below.
const NAMING_EXCEPTIONS = new Set(["shell/dockview-theme.css"]);

function isNamingViolation(path: string): boolean {
  const base = basenameOf(path);
  return base !== "global.css" && !base.endsWith(".module.css") && !NAMING_EXCEPTIONS.has(path);
}

test("every non-token stylesheet under src is named global.css, <name>.module.css, or a named exception", () => {
  const offenders = OTHER_STYLESHEETS.map(([path]) => path).filter(isNamingViolation);
  expect(offenders).toEqual([]);
});

test("the dockview-theme.css naming exception is scoped to its exact path, not just its basename", () => {
  expect(isNamingViolation("shell/dockview-theme.css")).toBe(false);
  // A same-named decoy anywhere else still violates the naming rule - the
  // exception must not become "any file called dockview-theme.css".
  expect(isNamingViolation("widgets/dockview-theme.css")).toBe(true);
  expect(isNamingViolation("dev/dockview-theme.css")).toBe(true);
  expect(isNamingViolation("dockview-theme.css")).toBe(true);
});

// --- (a) no chromatic literal outside tokens.css -----------------------
//
// Two independent mechanisms, both feeding chromaticLiteralViolations():
//
// 1. COLOR_LITERAL_RE - hex, rgb()/rgba(), hsl()/hsla(), oklch(), oklab(),
//    lab(), lch(): every literal-color FUNCTION/hex syntax CSS has. Scanned
//    across the ORIGINAL, comment-intact file text, because these forms
//    are distinctive enough not to false-positive on a selector or class
//    name - so a hex code or color function mentioned only in a comment
//    still trips this (accepted, deliberate: not a parser). color-mix() is
//    deliberately NOT in this list - mixing var(--token) values (e.g.
//    `color-mix(in oklab, var(--accent) 40%, transparent)`) is normal
//    token composition, not a new color; any raw hex/rgb/... smuggled
//    into a color-mix() argument is still caught since this regex scans
//    the whole file text, nesting notwithstanding. The hex branch excludes
//    a leading "issue #"/"Issue #" - house style for citing a GitHub issue
//    in a comment (pervasive outside src/styles too) - so a 3-hex-digit
//    issue number (e.g. #196) doesn't false-positive as a hex color; a hex
//    code left in a comment with no such prefix still trips it.
// 2. NAMED_COLOR_RE - the CSS named-color keywords (red, white, black, ...;
//    NOT transparent/currentColor, which aren't chromatic, and NOT CSS-wide
//    keywords like inherit/initial/unset/revert/none/auto). Unlike (1),
//    named colors are ordinary English words that legitimately appear in
//    class names, comments, and font-family lists ("Helvetica" is not a
//    color), so this one is scanned ONLY inside extracted declaration
//    VALUES (property: value pairs), and ONLY after COMMENT_RE strips
//    every /* ... */ block first - a selector like `.red { color:
//    var(--danger); }` is not a violation, and neither is a comment that
//    happens to mention a color name (`/* was red */`). Stripping matters
//    for more than comment text itself: a comment sitting between two
//    declarations (`color: var(--x); /* note */ background: red;`) would
//    otherwise break DECLARATION_VALUE_RE's adjacency requirement and
//    silently hide the declaration after it - see that regex's own
//    comment for why.
const COLOR_LITERAL_RE = /(?<!issue )(?<!Issue )#[0-9a-fA-F]{3,8}\b|\b(?:rgba?|hsla?|oklch|oklab|lab|lch)\(/g;

// CSS Color Module Level 4 named colors (the full "extended color keywords"
// list, including rebeccapurple) - https://www.w3.org/TR/css-color-4/#named-colors,
// cross-checked against MDN's named-color page. transparent is excluded:
// zero alpha, not a hue.
const CSS_NAMED_COLORS = [
  "aliceblue",
  "antiquewhite",
  "aqua",
  "aquamarine",
  "azure",
  "beige",
  "bisque",
  "black",
  "blanchedalmond",
  "blue",
  "blueviolet",
  "brown",
  "burlywood",
  "cadetblue",
  "chartreuse",
  "chocolate",
  "coral",
  "cornflowerblue",
  "cornsilk",
  "crimson",
  "cyan",
  "darkblue",
  "darkcyan",
  "darkgoldenrod",
  "darkgray",
  "darkgreen",
  "darkgrey",
  "darkkhaki",
  "darkmagenta",
  "darkolivegreen",
  "darkorange",
  "darkorchid",
  "darkred",
  "darksalmon",
  "darkseagreen",
  "darkslateblue",
  "darkslategray",
  "darkslategrey",
  "darkturquoise",
  "darkviolet",
  "deeppink",
  "deepskyblue",
  "dimgray",
  "dimgrey",
  "dodgerblue",
  "firebrick",
  "floralwhite",
  "forestgreen",
  "fuchsia",
  "gainsboro",
  "ghostwhite",
  "gold",
  "goldenrod",
  "gray",
  "green",
  "greenyellow",
  "grey",
  "honeydew",
  "hotpink",
  "indianred",
  "indigo",
  "ivory",
  "khaki",
  "lavender",
  "lavenderblush",
  "lawngreen",
  "lemonchiffon",
  "lightblue",
  "lightcoral",
  "lightcyan",
  "lightgoldenrodyellow",
  "lightgray",
  "lightgreen",
  "lightgrey",
  "lightpink",
  "lightsalmon",
  "lightseagreen",
  "lightskyblue",
  "lightslategray",
  "lightslategrey",
  "lightsteelblue",
  "lightyellow",
  "lime",
  "limegreen",
  "linen",
  "magenta",
  "maroon",
  "mediumaquamarine",
  "mediumblue",
  "mediumorchid",
  "mediumpurple",
  "mediumseagreen",
  "mediumslateblue",
  "mediumspringgreen",
  "mediumturquoise",
  "mediumvioletred",
  "midnightblue",
  "mintcream",
  "mistyrose",
  "moccasin",
  "navajowhite",
  "navy",
  "oldlace",
  "olive",
  "olivedrab",
  "orange",
  "orangered",
  "orchid",
  "palegoldenrod",
  "palegreen",
  "paleturquoise",
  "palevioletred",
  "papayawhip",
  "peachpuff",
  "peru",
  "pink",
  "plum",
  "powderblue",
  "purple",
  "rebeccapurple",
  "red",
  "rosybrown",
  "royalblue",
  "saddlebrown",
  "salmon",
  "sandybrown",
  "seagreen",
  "seashell",
  "sienna",
  "silver",
  "skyblue",
  "slateblue",
  "slategray",
  "slategrey",
  "snow",
  "springgreen",
  "steelblue",
  "tan",
  "teal",
  "thistle",
  "tomato",
  "turquoise",
  "violet",
  "wheat",
  "white",
  "whitesmoke",
  "yellow",
  "yellowgreen",
];

// Longest names first: with the trailing lookahead below this ordering
// isn't strictly required (backtracking would find "greenyellow" even if
// "green" is tried first and its lookahead fails), but sorting removes any
// doubt rather than relying on that.
const NAMED_COLOR_ALTERNATION = [...CSS_NAMED_COLORS].sort((a, b) => b.length - a.length).join("|");
// Bounded on both sides so a match is a whole CSS token, not a substring:
// "grayscale(1)" must not match "gray". The end boundary also accepts
// end-of-string ($) because the captured declaration value never includes
// its own terminating `;`/`}` - a value that's *only* "red" has nothing
// after it to satisfy a lookahead that didn't also accept $.
const NAMED_COLOR_RE = new RegExp(`(?:^|[:\\s,(])(${NAMED_COLOR_ALTERNATION})(?=[;\\s,)!]|$)`, "gi");

// Extracts `<value>` from every `<property>: <value>;` (or `<value>}` for a
// last declaration with no trailing semicolon) declaration in a stylesheet.
// Anchored on a `{` or `;` immediately before the property name (mod
// whitespace) so it can only start where a declaration can legally start -
// not, say, on "hover" inside the selector text ".button:hover". The
// terminator is a lookahead, `(?=[;}])`, rather than consumed: an earlier
// version consumed it, which meant that `;` was gone by the time matchAll
// looked for the NEXT declaration's leading anchor, so only the first
// declaration in any block was ever visible - `.foo { a: 1; b: red; }`
// silently dropped `b` even with no comment in sight. The lookahead
// leaves the `;` in the string for the next match to consume as ITS
// leading anchor, so every declaration in a block is found, not just the
// first. Run against comment-stripped text (see chromaticLiteralViolations)
// so a comment between two declarations can't reintroduce the same gap by
// breaking the "immediately preceding, mod whitespace" requirement.
const DECLARATION_VALUE_RE = /[{;]\s*[a-zA-Z-]+\s*:\s*([^;{}]+)(?=[;}])/g;

// Block comments only - CSS has no line comments. Declarations are
// extracted from the stripped text (mechanism 2) so a comment can't hide
// an adjacent declaration or have its own contents mistaken for one;
// mechanism 1 above deliberately keeps scanning the untouched original.
const COMMENT_RE = /\/\*[\s\S]*?\*\//g;

function chromaticLiteralViolations(cssText: string): string[] {
  const violations: string[] = [];
  for (const match of cssText.matchAll(COLOR_LITERAL_RE)) violations.push(match[0]);
  const withoutComments = cssText.replace(COMMENT_RE, " ");
  for (const declaration of withoutComments.matchAll(DECLARATION_VALUE_RE)) {
    const value = declaration[1]!;
    for (const named of value.matchAll(NAMED_COLOR_RE)) violations.push(named[1]!);
  }
  return violations;
}

for (const [path, text] of OTHER_STYLESHEETS) {
  test(`${path} has no chromatic literal outside tokens.css`, () => {
    expect(chromaticLiteralViolations(text)).toEqual([]);
  });
}

// Poison tests: exercise chromaticLiteralViolations() directly against
// hand-written snippets (not real widget files, which shouldn't carry
// intentionally-bad CSS) to prove both what it catches and what it must
// not flag.
test("catches a bare named color in a declaration value", () => {
  expect(chromaticLiteralViolations(".foo { color: red; }")).toEqual(["red"]);
  expect(chromaticLiteralViolations(".foo { background: white; }")).toEqual(["white"]);
  expect(chromaticLiteralViolations(".foo { border-color: black; }")).toEqual(["black"]);
});

test("does not flag transparent or currentColor", () => {
  expect(chromaticLiteralViolations(".foo { outline-color: transparent; color: currentColor; }")).toEqual([]);
});

test("a class literally named .red is not a false positive", () => {
  expect(chromaticLiteralViolations(".red { color: var(--danger); }")).toEqual([]);
});

test("a color name as a substring of a longer token is not a false positive", () => {
  expect(chromaticLiteralViolations(".foo { filter: grayscale(1); }")).toEqual([]);
});

test("a color name mentioned only in a comment is not scanned", () => {
  expect(chromaticLiteralViolations("/* was red before */ .foo { color: var(--danger); }")).toEqual([]);
});

// A prior version of DECLARATION_VALUE_RE consumed its own terminator,
// which meant only the FIRST declaration in any block was ever visible to
// the named-color scan - not a comment-specific bug, a structural one. A
// comment between declarations was one way to notice it (see the case
// below), but a plain second declaration with no comment anywhere
// exhibited the identical gap, so that's asserted directly too.
test("a violation in a non-first declaration is caught, no comment involved", () => {
  expect(chromaticLiteralViolations(".foo { color: var(--ink-hi); background: red; }")).toEqual(["red"]);
  expect(chromaticLiteralViolations(".foo { a: var(--x); b: var(--y); color: white; }")).toEqual(["white"]);
});

test("a comment between two declarations does not hide the one after it", () => {
  expect(chromaticLiteralViolations(".foo { color: var(--ink-hi); /* comment */ background: red; }")).toEqual(["red"]);
});

test("a comment containing a named color does not false-positive once stripped, with otherwise-clean declarations", () => {
  expect(
    chromaticLiteralViolations(".foo { /* was red */ color: var(--danger); background: var(--surface-0); }"),
  ).toEqual([]);
});

test("still catches hex/rgb/hsl/oklch literals alongside named colors", () => {
  expect(chromaticLiteralViolations(".foo { color: #ff0000; }")).toEqual(["#ff0000"]);
  expect(chromaticLiteralViolations(".foo { color: rgb(255, 0, 0); }")).toEqual(["rgb("]);
});

// An "issue #NNN"/"Issue #NNN" reference is house style for citing a GitHub
// issue in a comment (grep any of agent/, cmd/, or this file's own siblings
// for "issue #" - it's pervasive, predating this test). A 3-hex-digit issue
// number (e.g. #196: 1, 9, 6 are all valid hex digits) is otherwise
// indistinguishable from a 3-digit hex color to COLOR_LITERAL_RE, which
// scans comment-intact text on purpose (see that regex's own comment) - so
// without this exception, citing an issue whose number happens to look like
// hex would trip a design-token contract that has nothing to do with the
// citation. The exception is narrow (only the literal "issue #"/"Issue #"
// prefix), so a hex code genuinely left in a comment - the case this
// mechanism exists to catch - still trips it.
test("an issue-number reference that looks like a hex literal is not a false positive", () => {
  expect(chromaticLiteralViolations("/* a real flex item (issue #196) now */")).toEqual([]);
  expect(chromaticLiteralViolations("/* Issue #196: see .actions above */")).toEqual([]);
  // Not issue-prefixed: still a violation, proving the exception didn't
  // broaden into "any #-prefixed hex-looking digits are fine".
  expect(chromaticLiteralViolations("/* #196 alone, no issue prefix */")).toEqual(["#196"]);
});

// --- (b) the three attention-family vars stay on the allowlist ---------
//
// --attention/--alive/--danger exist so exactly one meaning maps to each
// hue across the whole app (a human is needed / agent is working /
// something failed). A widget earns a place on this list only when it has
// a state that genuinely needs one of those hues - a status color, a
// destructive action, the cadence signature - never for decoration.
//
// --accent is deliberately NOT gated: it is interaction chrome by
// definition (the plan's Global Constraints require an accent
// :focus-visible ring on EVERY interactive widget, and accent also carries
// selection and links), so gating it would grow this list with every
// interactive widget forever while protecting nothing - the design thesis
// guards the three ATTENTION-class hues' meanings, not focus chrome.
const SEMANTIC_USE_ALLOWLIST = [
  "cadence", // signature: state dot + trailing-edge tick tint
  "button", // danger variant
  "chip", // tone prop
  "badge", // tone prop
  "statusdot", // state color
  "meter", // danger/attention fill tone
  "toast", // tone prop
  "dialog", // danger footer
  "formrow", // error-state left border + message text (wave 7)
  "collectioneditor", // inline add-validation error text (wave 7)
  "failureglyph", // the ✗ that marks a failure - the hue IS the content
  "banner", // overlay status strip: tone prop (attention/danger) — a connection banner's whole purpose is to carry a warning hue
];

const SEMANTIC_VAR_RE = /var\(\s*--(?:attention|alive|danger)\b/;

// The allowlist is a widget concept: only src/widgets/<name>/<name>.module.css
// is eligible, keyed off the directory (not just the basename) so a
// same-named stylesheet elsewhere (e.g. a dev-tooling file that happens to
// be called button.module.css) can't ride along on a real widget's entry.
const WIDGET_STYLESHEET_RE = /^widgets\/([a-z0-9-]+)\/\1\.module\.css$/;

// kata zq7g: shell/rail/RailRow.module.css tints a signal row's gloss text
// with its own state family (working/needs-you/failed - see RailRow.tsx's
// ACTIVITY_FAMILY_CLASS) so the rail's "waiting on you" signal is no longer
// carried by a 6px dot alone. RailRow.module.css is a shell stylesheet, not a
// widget (it lives under shell/rail/, not widgets/<name>/), so it can never
// match WIDGET_STYLESHEET_RE no matter how SEMANTIC_USE_ALLOWLIST is
// extended - this is a deliberate, exact-path exception, the same shape as
// the dockview-theme.css naming exception above, scoped to this one file so
// a same-named stylesheet elsewhere can't ride along on it. (The rail's
// signal hues live with the row content in RailRow.module.css; Rail.module.
// css itself carries none.)
//
// kata 3h80: panes/session/transcript/tools/subagentmodule.module.css earns
// the same exception for the same structural reason - it lives under
// panes/session/transcript/tools/, not widgets/<name>/, so it can never
// match WIDGET_STYLESHEET_RE either, no matter what SEMANTIC_USE_ALLOWLIST
// contains. Its own failure edge (`.module[data-has-failure="true"]`) is the
// module-level "a child failed" signal parity §12 calls for - the failure
// colour has to come from somewhere, and this is the same deliberate,
// exact-path route RailRow.module.css already established, not a new one.
//
// kata crcf: panes/session/composer/askDock/askdock.module.css earns the
// same exception for the same structural reason - it lives under
// panes/session/composer/askDock/, not widgets/<name>/, so it can never
// match WIDGET_STYLESHEET_RE either. Its pending-ask batch is the app's
// clearest "a human is needed right now" moment (docs/web-ui/decisions.md
// topic 16); before this it was drawn in neutral ink alone, same-color as
// everything else. The amber container is --attention-bg/-edge on the
// batch envelope - the primary Send action inside it stays --accent (blue),
// which is not gated at all, per the amber-owns-the-container/blue-owns-
// the-action split the mockup settled on.
//
// tasklist-checklist-card: panes/session/transcript/tools/taskcheck.module.css
// earns the same exception for the same structural reason - it lives under
// panes/session/transcript/tools/, not widgets/<name>/, so it can never match
// WIDGET_STYLESHEET_RE either. Its one semantic reach (--alive on the done
// glyph) is the user-approved exception to the transcript card's neutral-
// color house rule (docs/superpowers/specs/2026-07-31-tasklist-renderer-
// design.md): the glyph alone carries subtle semantic colour so a row's
// state reads at a glance; every piece of TEXT on the card stays on the
// ink scale.
//
// activity-redesign task 7: panes/session/chrome/activitypanel.module.css
// earns the same exception for the same structural reason - it lives under
// panes/session/chrome/, not widgets/<name>/, so it can never match
// WIDGET_STYLESHEET_RE either. Its one semantic reach is --danger on the
// dense tree's failure text (.denseFailed: the terminal-row "failed" meta
// suffix and the fold row's "· M failed" count), which the 2026-08-05
// activity-view redesign spec assigns to the danger hue exactly the way
// RailRow.module.css's failure signals already established.
//
// task-list-ui task 8: panes/session/chrome/taskspanel.module.css earns the
// same exception for the same structural reason - it lives under
// panes/session/chrome/, not widgets/<name>/, so it can never match
// WIDGET_STYLESHEET_RE either. Its one semantic reach is --alive on the
// latest-update dot of the tasks panel's notes rail (docs/superpowers/
// specs/2026-08-09-task-list-ui-design.md): position already marks the
// latest note, the hue is a glyph-level accent, and all panel text stays
// on the ink scale.
//
// reviewed-ux-fixes fix 1: panes/session/transcript/tools/sandboxescalation.
// module.css earns the same exception for the same structural reason - it
// lives under panes/session/transcript/tools/, not widgets/<name>/, so it
// can never match WIDGET_STYLESHEET_RE either. A sandbox escalation card is
// a BLOCKING approval (the tool-exec goroutine is parked waiting on
// evener/sandbox/escalation/resolve) - the app's SECOND "a human is needed
// right now" moment after askDock's ask batch (kata crcf, above), same
// structural reason, so it earns the same --attention-bg/--attention-edge
// container tint askDock's .batch uses instead of a neutral Card.
// shell/rail/railDialog.module.css earns the same exception for the same
// structural reason - it lives under shell/rail/, not widgets/<name>/, so it
// can never match WIDGET_STYLESHEET_RE. Its one semantic reach is --danger
// on .pickerError, the rail dialogs' inline failure text - error text is the
// danger hue's canonical, ungateable job.
// focus-sentence: panes/session/composer/currentwork.module.css earns the same
// exact-path exception because it is pane content, not a widget stylesheet.
// Its --alive ring marks an actually in-progress task, which is precisely the
// semantic "agent working" meaning the token contract reserves for that hue.
//
// delegate-status: panes/session/transcript/tools/delegateStatus.module.css
// earns the same exception for the same structural reason - it lives under
// panes/session/transcript/tools/, not widgets/<name>/, so it can never match
// WIDGET_STYLESHEET_RE either. Its one semantic reach is --danger-ink on
// .dangerText — failure text for a delegate's not-resumable reason and last
// failed outcome reason. Error text is the danger hue's canonical, ungateable
// job, the same as railDialog.module.css's .pickerError above.
//
// webui-keybindings-p4 task 6: panes/settings/sections/keybindings.module.css
// earns the same exception for the same structural reason - it lives under
// panes/settings/sections/, not widgets/<name>/, so it can never match
// WIDGET_STYLESHEET_RE either. Its one semantic reach is --danger-ink on
// .rowError and .captureError, the keybindings editor's inline validation and
// hub-error text. Error text is the danger hue's canonical, ungateable job,
// the same as railDialog.module.css's .pickerError and
// delegateStatus.module.css's .dangerText above.
const SEMANTIC_PATH_EXCEPTIONS = new Set([
  "shell/rail/RailRow.module.css",
  "shell/rail/railDialog.module.css",
  "panes/session/transcript/tools/subagentmodule.module.css",
  "panes/session/composer/askDock/askdock.module.css",
  "panes/session/composer/currentwork.module.css",
  "panes/session/transcript/tools/taskcheck.module.css",
  "panes/session/chrome/activitypanel.module.css",
  "panes/session/chrome/taskspanel.module.css",
  "panes/session/transcript/tools/sandboxescalation.module.css",
  "panes/session/transcript/tools/delegateStatus.module.css",
  "panes/settings/sections/keybindings.module.css",
]);

for (const [path, text] of OTHER_STYLESHEETS) {
  test(`${path} only reaches for --attention/--alive/--danger if allowlisted`, () => {
    if (!SEMANTIC_VAR_RE.test(text)) return;
    if (SEMANTIC_PATH_EXCEPTIONS.has(path)) return;
    const widgetMatch = WIDGET_STYLESHEET_RE.exec(path);
    expect(widgetMatch).not.toBeNull();
    expect(SEMANTIC_USE_ALLOWLIST).toContain(widgetMatch![1]);
  });
}

test("the RailRow.module.css semantic-var exception is scoped to its exact path, not just its basename", () => {
  expect(SEMANTIC_PATH_EXCEPTIONS.has("shell/rail/RailRow.module.css")).toBe(true);
  // A same-named decoy anywhere else must still go through the normal
  // widget-allowlist check, exactly like the dockview-theme.css precedent.
  expect(SEMANTIC_PATH_EXCEPTIONS.has("widgets/RailRow.module.css")).toBe(false);
  expect(SEMANTIC_PATH_EXCEPTIONS.has("dev/RailRow.module.css")).toBe(false);
});

test("the askdock.module.css semantic-var exception is scoped to its exact path, not just its basename", () => {
  expect(SEMANTIC_PATH_EXCEPTIONS.has("panes/session/composer/askDock/askdock.module.css")).toBe(true);
  // A same-named decoy anywhere else must still go through the normal
  // widget-allowlist check, exactly like the dockview-theme.css precedent.
  expect(SEMANTIC_PATH_EXCEPTIONS.has("widgets/askdock.module.css")).toBe(false);
  expect(SEMANTIC_PATH_EXCEPTIONS.has("askdock.module.css")).toBe(false);
});

test("the currentwork.module.css semantic-var exception is scoped to its exact path", () => {
  expect(SEMANTIC_PATH_EXCEPTIONS.has("panes/session/composer/currentwork.module.css")).toBe(true);
  expect(SEMANTIC_PATH_EXCEPTIONS.has("widgets/currentwork/currentwork.module.css")).toBe(false);
  expect(SEMANTIC_PATH_EXCEPTIONS.has("currentwork.module.css")).toBe(false);
});

test("the taskspanel.module.css semantic-var exception is scoped to its exact path, not just its basename", () => {
  expect(SEMANTIC_PATH_EXCEPTIONS.has("panes/session/chrome/taskspanel.module.css")).toBe(true);
  expect(SEMANTIC_PATH_EXCEPTIONS.has("widgets/taskspanel.module.css")).toBe(false);
});

test("the sandboxescalation.module.css semantic-var exception is scoped to its exact path, not just its basename", () => {
  expect(SEMANTIC_PATH_EXCEPTIONS.has("panes/session/transcript/tools/sandboxescalation.module.css")).toBe(true);
  // A same-named decoy anywhere else must still go through the normal
  // widget-allowlist check, exactly like the dockview-theme.css precedent.
  expect(SEMANTIC_PATH_EXCEPTIONS.has("widgets/sandboxescalation.module.css")).toBe(false);
  expect(SEMANTIC_PATH_EXCEPTIONS.has("sandboxescalation.module.css")).toBe(false);
});

// --- (c) dark and light blocks declare the same color tokens -----------
//
// tokens.css defines one canonical dark block for `:root` plus nested
// `[data-theme="dark"]` wrappers, and one `[data-theme="light"]` block
// (light overrides). A color token declared in only one of the two silently
// breaks the other theme - it either falls back to the wrong hue or resolves
// to nothing.
// This does a bracket-depth extraction rather than pulling in a CSS
// parser dependency; it works because tokens.css (authored alongside this
// test) never nests braces inside either block.
function extractBlock(css: string, startPattern: RegExp): string {
  const start = css.search(startPattern);
  if (start === -1) {
    throw new Error(`token-contract test: could not find a block matching ${startPattern}`);
  }
  const braceStart = css.indexOf("{", start);
  let depth = 0;
  for (let i = braceStart; i < css.length; i++) {
    if (css[i] === "{") depth++;
    else if (css[i] === "}") {
      depth--;
      if (depth === 0) return css.slice(braceStart + 1, i);
    }
  }
  throw new Error("token-contract test: unbalanced braces while extracting a block");
}

const DECLARATION_RE = /(--[a-z0-9-]+)\s*:\s*([^;]+);/gi;
// A declared token counts as a "color token" (subject to dark/light parity)
// when its value is a literal color or a color-mix() of tokens. Every
// non-color token (space/type/radius/motion) is theme-invariant and is
// declared once, only in the dark block, by design.
const COLOR_VALUE_RE = /^(#|rgba?\(|hsla?\(|oklch\(|oklab\(|color-mix\()/i;

function colorTokenNames(block: string): Set<string> {
  const names = new Set<string>();
  for (const match of block.matchAll(DECLARATION_RE)) {
    const name = match[1]!;
    const value = match[2]!.trim();
    if (COLOR_VALUE_RE.test(value)) names.add(name);
  }
  return names;
}

function declaredToken(block: string, name: string): string | undefined {
  const escapedName = name.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  return new RegExp(`${escapedName}\\s*:\\s*([^;]+);`).exec(block)?.[1]?.trim();
}

function declarationInRule(css: string, selector: string, property: string): string | undefined {
  const withoutComments = css.replace(COMMENT_RE, " ");
  const rule = new RegExp(`\\.${selector}\\s*\\{([^{}]*)\\}`).exec(withoutComments)?.[1];
  if (!rule) return undefined;
  return new RegExp(`(?:^|;)\\s*${property}\\s*:\\s*([^;]+)`).exec(rule)?.[1]?.trim();
}

type RGB = [number, number, number];

function parseHexColor(value: string): RGB {
  const match = /^#([0-9a-f]{6})$/i.exec(value);
  if (!match) throw new Error(`token-contract test: expected a six-digit hex color, got ${value}`);
  return [0, 2, 4].map((offset) => Number.parseInt(match[1]!.slice(offset, offset + 2), 16)) as RGB;
}

function channelLuminance(channel: number): number {
  const normalized = channel / 255;
  return normalized <= 0.03928 ? normalized / 12.92 : ((normalized + 0.055) / 1.055) ** 2.4;
}

function relativeLuminance([red, green, blue]: RGB): number {
  return 0.2126 * channelLuminance(red) + 0.7152 * channelLuminance(green) + 0.0722 * channelLuminance(blue);
}

function contrastRatio(foreground: RGB, background: RGB): number {
  const foregroundLuminance = relativeLuminance(foreground);
  const backgroundLuminance = relativeLuminance(background);
  const lighter = Math.max(foregroundLuminance, backgroundLuminance);
  const darker = Math.min(foregroundLuminance, backgroundLuminance);
  return (lighter + 0.05) / (darker + 0.05);
}

const DIFF_TOKEN_NAMES = ["--diff-add-bg", "--diff-del-bg"];
const DIFFBLOCK_STYLESHEET = STYLESHEETS["widgets/diffblock/diffblock.module.css"];

test("DiffBlock uses exactly the dedicated non-semantic background pair", () => {
  expect(DIFFBLOCK_STYLESHEET).toBeDefined();
  if (!DIFFBLOCK_STYLESHEET) return;

  expect(DIFFBLOCK_STYLESHEET.match(SEMANTIC_VAR_RE) ?? []).toEqual([]);
  expect(declarationInRule(DIFFBLOCK_STYLESHEET, "add", "background")).toBe("var(--diff-add-bg)");
  expect(declarationInRule(DIFFBLOCK_STYLESHEET, "del", "background")).toBe("var(--diff-del-bg)");

  const diffTokenNames = [...TOKENS_CSS.matchAll(/(--diff-[a-z0-9-]+)\s*:/gi)].map((match) => match[1]!);
  expect([...new Set(diffTokenNames)].sort()).toEqual([...DIFF_TOKEN_NAMES].sort());
});

test("dedicated diff backgrounds preserve quiet contrast and grayscale separation in both themes", () => {
  const themes = [
    {
      name: "dark",
      block: extractBlock(TOKENS_CSS, /(?:^|\n):root(?:\s*,\s*\[data-theme="dark"\])?\s*\{/),
    },
    {
      name: "light",
      block: extractBlock(TOKENS_CSS, /\[data-theme="light"\][^{]*\{/),
    },
  ];

  for (const theme of themes) {
    const surface = declaredToken(theme.block, "--surface-0");
    const content = declaredToken(theme.block, "--ink-hi");
    const marker = declaredToken(theme.block, "--ink-low");
    const add = declaredToken(theme.block, "--diff-add-bg");
    const del = declaredToken(theme.block, "--diff-del-bg");
    expect(
      [surface, content, marker, add, del],
      `${theme.name} theme declares all DiffBlock contrast tokens`,
    ).not.toContain(undefined);
    if (!surface || !content || !marker || !add || !del) continue;

    const surfaceRgb = parseHexColor(surface);
    const contentRgb = parseHexColor(content);
    const markerRgb = parseHexColor(marker);
    const addRgb = parseHexColor(add);
    const delRgb = parseHexColor(del);

    for (const [kind, background] of [
      ["add", addRgb],
      ["del", delRgb],
    ] as const) {
      const contentRatio = contrastRatio(contentRgb, background);
      const markerRatio = contrastRatio(markerRgb, background);
      const surfaceRatio = contrastRatio(background, surfaceRgb);
      expect(contentRatio, `${theme.name} ${kind} content contrast`).toBeGreaterThanOrEqual(4.5);
      expect(markerRatio, `${theme.name} ${kind} marker contrast`).toBeGreaterThanOrEqual(3);
      expect(surfaceRatio, `${theme.name} ${kind} background contrast`).toBeGreaterThanOrEqual(1.05);
      expect(surfaceRatio, `${theme.name} ${kind} background quietness`).toBeLessThanOrEqual(1.2);
    }

    expect(relativeLuminance(addRgb), `${theme.name} additions are lighter in grayscale`).toBeGreaterThan(
      relativeLuminance(delRgb),
    );
    expect(addRgb[1], `${theme.name} additions retain a green channel`).toBeGreaterThan(addRgb[0]);
    expect(delRgb[0], `${theme.name} deletions retain a red channel`).toBeGreaterThan(delRgb[1]);
  }
});

test("tokens.css dark and light blocks declare the same color token names", () => {
  const darkBlock = extractBlock(TOKENS_CSS, /(?:^|\n):root(?:\s*,\s*\[data-theme="dark"\])?\s*\{/);
  const lightBlock = extractBlock(TOKENS_CSS, /\[data-theme="light"\][^{]*\{/);
  const darkNames = colorTokenNames(darkBlock);
  const lightNames = colorTokenNames(lightBlock);

  const missingFromLight = [...darkNames].filter((name) => !lightNames.has(name)).sort();
  const missingFromDark = [...lightNames].filter((name) => !darkNames.has(name)).sort();
  expect({ missingFromLight, missingFromDark }).toEqual({ missingFromLight: [], missingFromDark: [] });
});

test("the canonical dark token block directly scopes nested dark wrappers", () => {
  const darkBlock = extractBlock(TOKENS_CSS, /(?:^|\n):root\s*,\s*\[data-theme="dark"\]\s*\{/);
  expect(darkBlock).toContain("color-scheme: dark;");
  expect(darkBlock).toContain("--surface-1: #232427;");
});

// --- (f) the -ink text companions clear AA in both themes ---------------
//
// The bare attention-family hues are glyph/border/wash colors; any use as
// TEXT goes through the -ink companion, which exists precisely because the
// light theme's bare hues measure 2.8–3.9:1. Same computation as the
// DiffBlock contrast test above, with one addition: chips, badges, and
// toasts set -ink text on the hue's own -bg tint (`color-mix(in oklab,
// hue 15%, --surface-1)`), so that tint is a real text ground and is
// checked too — which requires reproducing the oklab mix here. The mix
// implementation below follows the OKLab reference (Björn Ottosson's
// published matrices), and is only ever asked to reproduce a 15% mix of
// two in-gamut sRGB colors, so gamut clipping stays a plain clamp.

type OklabTriple = [number, number, number];

function srgbChannelToLinear(channel: number): number {
  const c = channel / 255;
  return c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
}

function linearChannelToSrgb(linear: number): number {
  const c = linear <= 0.0031308 ? 12.92 * linear : 1.055 * linear ** (1 / 2.4) - 0.055;
  return Math.max(0, Math.min(255, Math.round(c * 255)));
}

function rgbToOklab([red, green, blue]: RGB): OklabTriple {
  const r = srgbChannelToLinear(red);
  const g = srgbChannelToLinear(green);
  const b = srgbChannelToLinear(blue);
  const l = Math.cbrt(0.4122214708 * r + 0.5363325363 * g + 0.0514459929 * b);
  const m = Math.cbrt(0.2119034982 * r + 0.6806995451 * g + 0.1073969566 * b);
  const s = Math.cbrt(0.0883024619 * r + 0.2817188376 * g + 0.6299787005 * b);
  return [
    0.2104542553 * l + 0.793617785 * m - 0.0040720468 * s,
    1.9779984951 * l - 2.428592205 * m + 0.4505937099 * s,
    0.0259040371 * l + 0.7827717662 * m - 0.808675766 * s,
  ];
}

function oklabToRgb([L, a, b]: OklabTriple): RGB {
  const l = (L + 0.3963377774 * a + 0.2158037573 * b) ** 3;
  const m = (L - 0.1055613458 * a - 0.0638541728 * b) ** 3;
  const s = (L - 0.0894841775 * a - 1.291485548 * b) ** 3;
  return [
    linearChannelToSrgb(4.0767416621 * l - 3.3077115913 * m + 0.2309699292 * s),
    linearChannelToSrgb(-1.2684380046 * l + 2.6097574011 * m - 0.3413193965 * s),
    linearChannelToSrgb(-0.0041960863 * l - 0.7034186147 * m + 1.707614701 * s),
  ] as RGB;
}

function oklabMix(colorA: RGB, colorB: RGB, fractionOfA: number): RGB {
  const a = rgbToOklab(colorA);
  const b = rgbToOklab(colorB);
  return oklabToRgb([
    a[0] * fractionOfA + b[0] * (1 - fractionOfA),
    a[1] * fractionOfA + b[1] * (1 - fractionOfA),
    a[2] * fractionOfA + b[2] * (1 - fractionOfA),
  ]);
}

test("the four -ink companions clear 4.5:1 on their theme's lightest text grounds", () => {
  const themes = [
    {
      name: "dark",
      block: extractBlock(TOKENS_CSS, /(?:^|\n):root(?:\s*,\s*\[data-theme="dark"\])?\s*\{/),
      grounds: ["--surface-2", "--hover-1"],
    },
    {
      name: "light",
      block: extractBlock(TOKENS_CSS, /\[data-theme="light"\][^{]*\{/),
      grounds: ["--surface-0", "--surface-1"],
    },
  ];
  for (const theme of themes) {
    const surface1 = declaredToken(theme.block, "--surface-1");
    expect(surface1, `${theme.name} declares --surface-1`).toBeDefined();
    for (const family of ["attention", "alive", "danger", "accent"]) {
      const ink = declaredToken(theme.block, `--${family}-ink`);
      expect(ink, `${theme.name} declares --${family}-ink`).toBeDefined();
      const bareHue = declaredToken(theme.block, `--${family}`);
      expect(bareHue, `${theme.name} declares --${family}`).toBeDefined();
      if (!ink || !bareHue || !surface1) continue;
      const grounds: [string, RGB][] = theme.grounds.map((groundName) => {
        const ground = declaredToken(theme.block, groundName);
        expect(ground, `${theme.name} declares ${groundName}`).toBeDefined();
        return [groundName, parseHexColor(ground!)];
      });
      // The hue's own -bg tint (15% oklab mix into --surface-1) is the
      // ground -ink text most often sits on: chip/badge/toast fills.
      grounds.push([`--${family}-bg`, oklabMix(parseHexColor(bareHue), parseHexColor(surface1), 0.15)]);
      for (const [groundName, ground] of grounds) {
        const ratio = contrastRatio(parseHexColor(ink), ground);
        expect(ratio, `${theme.name} --${family}-ink on ${groundName}`).toBeGreaterThanOrEqual(4.5);
      }
    }
  }
});

// --ink-low is documented as placeholder/disabled/timestamp ink, but 96
// text rules set it as `color` (docs/web-ui/typography-spacing-critique-
// 2026-09-06.md finding 7). It therefore has to clear AA on the two grounds
// text actually sits on in each theme, the same bar the -ink companions meet.
test("--ink-low clears 4.5:1 on the page and pane surfaces in both themes", () => {
  const themes = [
    { name: "dark", block: extractBlock(TOKENS_CSS, /(?:^|\n):root(?:\s*,\s*\[data-theme="dark"\])?\s*\{/) },
    { name: "light", block: extractBlock(TOKENS_CSS, /\[data-theme="light"\][^{]*\{/) },
  ];
  for (const theme of themes) {
    const inkLow = declaredToken(theme.block, "--ink-low");
    expect(inkLow, `${theme.name} declares --ink-low`).toBeDefined();
    for (const groundName of ["--surface-0", "--surface-1"]) {
      const ground = declaredToken(theme.block, groundName);
      expect(ground, `${theme.name} declares ${groundName}`).toBeDefined();
      if (!inkLow || !ground) continue;
      const ratio = contrastRatio(parseHexColor(inkLow), parseHexColor(ground));
      expect(ratio, `${theme.name} --ink-low on ${groundName}`).toBeGreaterThanOrEqual(4.5);
    }
  }
});

// --- (d) z-index values must use the token ladder ----------------------
//
// Every z-index in the app comes from the token ladder (--z-raised, --z-sticky-bar,
// --z-dialog, --z-menu, --z-tooltip, --z-toast) to keep stacking context
// predictable and conflict-free. Raw integers are banned except for 0 (reset)
// and auto (default).

const Z_INDEX_RE = /z-index\s*:\s*([^;}]+)/gi;
const Z_INDEX_VALUE_RE = /^(var\(--z-[a-z0-9-]+\)|0|auto)$/;

function zIndexViolations(cssText: string): string[] {
  const violations: string[] = [];
  const withoutComments = cssText.replace(COMMENT_RE, " ");
  for (const match of withoutComments.matchAll(Z_INDEX_RE)) {
    const value = match[1]!.trim();
    if (!Z_INDEX_VALUE_RE.test(value)) {
      violations.push(value);
    }
  }
  return violations;
}

for (const [path, text] of OTHER_STYLESHEETS) {
  test(`${path} uses only tokenized z-index values`, () => {
    expect(zIndexViolations(text)).toEqual([]);
  });
}

test("catches a bare z-index integer", () => {
  expect(zIndexViolations(".a { z-index: 1020; }")).toEqual(["1020"]);
  expect(zIndexViolations(".a { z-index: 999; }")).toEqual(["999"]);
});

test("allows var(--z-*) token references", () => {
  expect(zIndexViolations(".a { z-index: var(--z-menu); }")).toEqual([]);
  expect(zIndexViolations(".a { z-index: var(--z-tooltip); }")).toEqual([]);
});

test("allows 0 and auto for z-index", () => {
  expect(zIndexViolations(".a { z-index: 0; }")).toEqual([]);
  expect(zIndexViolations(".a { z-index: auto; }")).toEqual([]);
});

test("does not flag z-index mentioned only in a comment", () => {
  expect(zIndexViolations("/* z-index: 999; */ .a { position: relative; }")).toEqual([]);
});

// --- (e) focus ring must use token, not hand-rolled geometry -----------
//
// THE focus treatment: every :focus-visible ring is `outline: var(--focus-ring)`
// or `var(--focus-ring-danger)` for destructive controls. Hand-rolled rings
// are banned in every form they've been written: outline shorthands carrying
// their own length + line style, the outline longhands, and the retired
// inset-ring box-shadow hack (an inset spread-only shadow in any color, or a
// shadow colored with bare --accent/--danger).
//
// TWO exact-path exceptions, each scoped to its exact path so a same-named
// stylesheet elsewhere cannot ride along on it:
// - shell/palette/commandpalette.module.css keeps a deliberate quiet 2px
//   var(--accent-edge) ring on `.input:focus` — a full-strength --focus-ring
//   on a text input you are actively typing in is louder than the palette
//   wants, and --accent-edge (40% accent) is the sanctioned muted mix.
// - widgets/dropzone/dropzone.module.css draws a 2px DASHED var(--accent)
//   outline as its drag-target affordance — that outline is drop-here
//   signage, not a focus ring, and dashed is what distinguishes it from one.
//   The checker can't tell signage from rings, so the deviation is recorded
//   here where it's visible instead of loosened out of the regex.

const FOCUS_RING_EXCEPTIONS = new Set([
  "shell/palette/commandpalette.module.css",
  "widgets/dropzone/dropzone.module.css",
]);

function focusRingViolations(cssText: string): string[] {
  const violations: string[] = [];
  const withoutComments = cssText.replace(COMMENT_RE, " ");

  // Hand-rolled ring geometry in an outline shorthand: any explicit length
  // plus any line style, in either order ("2px solid", "solid 2px", "2px
  // dashed", "0.125rem solid" — two independent tests so ordering and unit
  // can't dodge the check). var(--focus-ring*) and none carry neither a
  // length nor a style keyword, so they pass untouched.
  const outlineRe = /outline\s*:\s*([^;}]+)/gi;
  const OUTLINE_LENGTH_RE = /\b[\d.]+(px|rem|em)\b/i;
  const OUTLINE_STYLE_RE = /\b(solid|dashed|dotted|double)\b/i;
  for (const match of withoutComments.matchAll(outlineRe)) {
    const value = match[1]!.trim();
    if (OUTLINE_LENGTH_RE.test(value) && OUTLINE_STYLE_RE.test(value)) {
      violations.push(`outline: ${value}`);
    }
  }

  // The outline longhands are how a hand-rolled ring evades the shorthand
  // scan piecemeal; there is no sanctioned use of any of them.
  const outlineLonghandRe = /outline-(width|style|color)\s*:\s*([^;}]+)/gi;
  for (const match of withoutComments.matchAll(outlineLonghandRe)) {
    violations.push(`outline-${match[1]}: ${match[2]!.trim()}`);
  }

  // The retired inset-ring hack, both ways it was ever written: an inset
  // spread-only shadow (any color — `inset 0 0 0 2px <anything>` is a ring
  // by construction), or a shadow colored with the bare interaction hues.
  // The hue test requires the exact token — var(--accent-bg)/-edge mixes in
  // a shadow are legitimate tinting, not a ring.
  const boxShadowRe = /box-shadow\s*:\s*([^;}]+)/gi;
  const INSET_RING_RE = /\binset\s+0\s+0\s+0\s+[\d.]/i;
  const BARE_HUE_RE = /var\(\s*--(accent|danger)\s*\)/i;
  for (const match of withoutComments.matchAll(boxShadowRe)) {
    const value = match[1]!.trim();
    if (INSET_RING_RE.test(value) || BARE_HUE_RE.test(value)) {
      violations.push(`box-shadow: ${value}`);
    }
  }

  return violations;
}

for (const [path, text] of OTHER_STYLESHEETS) {
  test(`${path} uses token focus rings, not hand-rolled geometry`, () => {
    if (FOCUS_RING_EXCEPTIONS.has(path)) return;
    expect(focusRingViolations(text)).toEqual([]);
  });
}

test("the focus-ring exceptions are scoped to their exact paths", () => {
  expect(FOCUS_RING_EXCEPTIONS.has("shell/palette/commandpalette.module.css")).toBe(true);
  expect(FOCUS_RING_EXCEPTIONS.has("widgets/dropzone/dropzone.module.css")).toBe(true);
  // A same-named decoy elsewhere must still go through the normal check.
  expect(FOCUS_RING_EXCEPTIONS.has("widgets/commandpalette.module.css")).toBe(false);
  expect(FOCUS_RING_EXCEPTIONS.has("shell/commandpalette.module.css")).toBe(false);
  expect(FOCUS_RING_EXCEPTIONS.has("panes/spawn/dropzone.module.css")).toBe(false);
  expect(FOCUS_RING_EXCEPTIONS.has("dropzone.module.css")).toBe(false);
});

test("catches a hand-rolled outline ring with px + solid", () => {
  const violations1 = focusRingViolations(".a:focus-visible { outline: 2px solid var(--accent); }");
  expect(violations1.length).toBeGreaterThan(0);
  expect(violations1[0]).toMatch(/^outline:/);
  const violations2 = focusRingViolations(".a:focus-visible { outline: 3px solid red; }");
  expect(violations2.length).toBeGreaterThan(0);
  expect(violations2[0]).toMatch(/^outline:/);
});

test("allows var(--focus-ring) outline", () => {
  expect(focusRingViolations(".a:focus-visible { outline: var(--focus-ring); }")).toEqual([]);
  expect(focusRingViolations(".a:focus-visible { outline: var(--focus-ring-danger); }")).toEqual([]);
});

test("allows outline: none", () => {
  expect(focusRingViolations(".a { outline: none; }")).toEqual([]);
});

test("catches ring geometry regardless of value order, unit, or line style", () => {
  expect(focusRingViolations(".a:focus-visible { outline: solid 2px var(--accent); }").length).toBe(1);
  expect(focusRingViolations(".a:focus-visible { outline: 2px dashed var(--accent); }").length).toBe(1);
  expect(focusRingViolations(".a:focus-visible { outline: 0.125rem solid var(--accent); }").length).toBe(1);
});

test("catches outline longhands outright", () => {
  expect(focusRingViolations(".a { outline-width: 2px; }")).toEqual(["outline-width: 2px"]);
  expect(focusRingViolations(".a { outline-style: solid; }")).toEqual(["outline-style: solid"]);
  expect(focusRingViolations(".a { outline-color: var(--accent); }")).toEqual(["outline-color: var(--accent)"]);
});

test("catches an inset spread-only ring in any color", () => {
  expect(focusRingViolations(".a { box-shadow: inset 0 0 0 2px var(--ink-hi); }").length).toBe(1);
  expect(focusRingViolations(".a { box-shadow: inset 0 0 0 1px var(--edge); }").length).toBe(1);
});

test("allows -bg/-edge hue mixes in a box-shadow (tinting, not a ring)", () => {
  expect(focusRingViolations(".a { box-shadow: 0 4px 16px var(--accent-bg); }")).toEqual([]);
  expect(focusRingViolations(".a { box-shadow: 0 4px 16px var(--danger-edge); }")).toEqual([]);
});

test("catches box-shadow with var(--accent) or var(--danger)", () => {
  const violations1 = focusRingViolations(".a { box-shadow: inset 0 0 0 2px var(--accent); }");
  expect(violations1.length).toBeGreaterThan(0);
  expect(violations1[0]).toMatch(/^box-shadow:/);
  const violations2 = focusRingViolations(".a { box-shadow: inset 0 0 0 2px var(--danger); }");
  expect(violations2.length).toBeGreaterThan(0);
  expect(violations2[0]).toMatch(/^box-shadow:/);
});

test("allows box-shadow with token shadow values", () => {
  expect(focusRingViolations(".a { box-shadow: var(--shadow-overlay); }")).toEqual([]);
  expect(focusRingViolations(".a { box-shadow: var(--shadow-modal); }")).toEqual([]);
});

test("does not flag outline or box-shadow mentioned only in comments", () => {
  expect(focusRingViolations("/* outline: 2px solid red; */ .a { position: relative; }")).toEqual([]);
  expect(focusRingViolations("/* box-shadow: inset 0 0 0 2px var(--accent); */ .a { color: red; }")).toEqual([]);
});

// --- (g) every bare var(--token) resolves to a declared property --------
//
// `var(--radius-sm)` shipped in two rules - composer.module.css:65 and
// currentwork.module.css:81 (docs/web-ui/typography-spacing-critique-
// 2026-09-06.md, "Bugs found on the way"): --radius-sm was never a token,
// so the attachment tile rendered square and nothing said a word. CSS
// resolves an undeclared var() to the property's initial value and moves
// on - no error, no warning, and nothing in a screenshot that looks
// obviously wrong.
//
// Only the FALLBACK-LESS form is a violation: `var(--x, 4px)` is a
// deliberate, self-documenting default (streamingtext.module.css uses it
// for the prose hooks), so the scan only matches a var() whose token name
// is followed straight by `)`.
//
// The declared set is the union over EVERY stylesheet, not tokens.css
// alone - modules legitimately declare their own local custom properties
// (--textarea-min-lines in textarea, --sheet-inline-size in sheet) and set
// them on a nested selector or inside a media query. For the same reason
// this is the one mechanism here that also scans tokens.css, which both
// declares and references (--focus-ring composes var(--accent)): a
// dangling reference there breaks a rule exactly the same way.
const CUSTOM_PROPERTY_DECLARATION_RE = /(--[a-zA-Z0-9-]+)\s*:/g;
const BARE_VAR_RE = /var\(\s*(--[a-zA-Z0-9-]+)\s*\)/g;

// Names no stylesheet is required to declare. Three groups, all listed
// because the check cannot tell "nothing declares this" from "nothing
// declares this yet":
//   - JS writes it, as an inline style or a setProperty call, so no
//     stylesheet ever declares it: --keyboard-inset (shell/
//     useKeyboardInset.ts), --rail-width (shell/rail/RailResizeHandle.tsx),
//     --fill (Meter and RecommendationCard paint their fill width).
//   - A theming hook a container sets to retint the content it owns:
//     --markdown-ink, --prose-ink, --prose-font-size.
//   - Declared today only under a media query or an attribute selector
//     (--tap-min, --density-scale, --font-scale). The declaration scan
//     picks those up anyway; they are named here so this check never
//     silently depends on where that one declaration happens to live.
const UNDECLARED_BY_DESIGN = new Set([
  "--keyboard-inset",
  "--rail-width",
  "--fill",
  "--markdown-ink",
  "--prose-ink",
  "--prose-font-size",
  "--tap-min",
  "--density-scale",
  "--font-scale",
]);

const DECLARED_CUSTOM_PROPERTIES = new Set<string>();
for (const text of Object.values(STYLESHEETS)) {
  for (const match of text.replace(COMMENT_RE, " ").matchAll(CUSTOM_PROPERTY_DECLARATION_RE)) {
    DECLARED_CUSTOM_PROPERTIES.add(match[1]!);
  }
}

function undefinedTokenViolations(
  cssText: string,
  declared: ReadonlySet<string> = DECLARED_CUSTOM_PROPERTIES,
): string[] {
  const violations: string[] = [];
  const withoutComments = cssText.replace(COMMENT_RE, " ");
  for (const match of withoutComments.matchAll(BARE_VAR_RE)) {
    const name = match[1]!;
    // dockview declares its own --dv-* theme properties inside the library;
    // shell/dockview-theme.css only ever overrides and composes them.
    if (name.startsWith("--dv-")) continue;
    if (UNDECLARED_BY_DESIGN.has(name)) continue;
    if (!declared.has(name)) violations.push(name);
  }
  return violations;
}

for (const [path, text] of Object.entries(STYLESHEETS)) {
  test(`${path} references only declared custom properties`, () => {
    expect(undefinedTokenViolations(text)).toEqual([]);
  });
}

test("the declared set spans module-local custom properties, not just tokens.css", () => {
  expect(DECLARED_CUSTOM_PROPERTIES.has("--space-4")).toBe(true);
  expect(DECLARED_CUSTOM_PROPERTIES.has("--textarea-min-lines")).toBe(true);
  expect(DECLARED_CUSTOM_PROPERTIES.has("--sheet-inline-size")).toBe(true);
  // The shipped bug's name: referenced twice, declared nowhere, ever.
  expect(DECLARED_CUSTOM_PROPERTIES.has("--radius-sm")).toBe(false);
});

test("catches a fallback-less var() naming an undeclared token", () => {
  const declared = new Set(["--radius-chip"]);
  expect(undefinedTokenViolations(".tile { border-radius: var(--radius-sm); }", declared)).toEqual(["--radius-sm"]);
  expect(undefinedTokenViolations(".a { color: var(--edge-hi); }", declared)).toEqual(["--edge-hi"]);
});

test("allows a var() that carries its own fallback", () => {
  expect(undefinedTokenViolations(".tile { border-radius: var(--radius-sm, 4px); }", new Set())).toEqual([]);
  // A token used as another var()'s fallback is itself fallback-less, so it
  // still has to resolve - that nested reference is the one that renders
  // when the outer name is unset.
  const declared = new Set(["--font-size-body"]);
  expect(undefinedTokenViolations(".a { font-size: var(--prose-size, var(--font-size-body)); }", declared)).toEqual([]);
  expect(undefinedTokenViolations(".a { font-size: var(--prose-size, var(--font-size-gone)); }", declared)).toEqual([
    "--font-size-gone",
  ]);
});

test("allows dockview's own --dv-* properties and the runtime-set names", () => {
  expect(undefinedTokenViolations(".dockview-theme-evener { --dv-tab-bg: var(--dv-foo); }", new Set())).toEqual([]);
  expect(undefinedTokenViolations(".fill { width: var(--fill); }", new Set())).toEqual([]);
  expect(undefinedTokenViolations(".shell { padding-bottom: var(--keyboard-inset); }", new Set())).toEqual([]);
});

test("does not flag a var() named only in a comment", () => {
  const declared = new Set(["--radius-chip"]);
  expect(
    undefinedTokenViolations("/* was var(--radius-sm) */ .tile { border-radius: var(--radius-chip); }", declared),
  ).toEqual([]);
});

// --- (h) type sizes come from the ramp, never from a px literal ---------
//
// The critique counted 18 literal px font-sizes outside tokens.css - 9px,
// 10.5px, 11px x8, 11.5px x2, 12px x2, 13px x2, 16px x2 (docs/web-ui/
// typography-spacing-critique-2026-09-06.md, "Literal (off-ramp) sizes").
// An off-ramp size ignores --font-scale, so the Settings text-size control
// silently does not move that text, and it puts a size on screen the ramp
// never agreed to.
//
// Relative units are not literals in this sense and stay legal: `0.86em`
// on inline code sizes it against whatever line it sits in and rides the
// ramp with that line, as do `%` and `inherit`. Only a px number in the
// value fails - including one hidden inside a calc(), which is how a
// literal would otherwise slip past while still looking tokenized.
const FONT_SIZE_RE = /font-size\s*:\s*([^;}]+)/gi;
const PX_LITERAL_RE = /\b[\d.]+px\b/i;

function literalFontSizeViolations(cssText: string): string[] {
  const violations: string[] = [];
  const withoutComments = cssText.replace(COMMENT_RE, " ");
  for (const match of withoutComments.matchAll(FONT_SIZE_RE)) {
    const value = match[1]!.trim();
    if (PX_LITERAL_RE.test(value)) violations.push(value);
  }
  return violations;
}

for (const [path, text] of OTHER_STYLESHEETS) {
  test(`${path} sizes type from the ramp, not a px literal`, () => {
    expect(literalFontSizeViolations(text)).toEqual([]);
  });
}

test("catches a literal px font-size", () => {
  expect(literalFontSizeViolations(".a { font-size: 11px; }")).toEqual(["11px"]);
  expect(literalFontSizeViolations(".a { font-size: 10.5px; }")).toEqual(["10.5px"]);
});

test("catches a px literal hidden inside a calc()", () => {
  expect(literalFontSizeViolations(".a { font-size: calc(12px * var(--font-scale)); }")).toEqual([
    "calc(12px * var(--font-scale))",
  ]);
});

test("allows ramp tokens, inherit, and relative units", () => {
  expect(literalFontSizeViolations(".a { font-size: var(--font-size-ui); }")).toEqual([]);
  expect(literalFontSizeViolations(".a { font-size: var(--font-size-caption); }")).toEqual([]);
  expect(literalFontSizeViolations(".a { font-size: 0.86em; }")).toEqual([]);
  expect(literalFontSizeViolations(".a { font-size: 1em; }")).toEqual([]);
  expect(literalFontSizeViolations(".a { font-size: 90%; }")).toEqual([]);
  expect(literalFontSizeViolations(".a { font-size: inherit; }")).toEqual([]);
});

test("does not flag a px font-size mentioned only in a comment", () => {
  expect(literalFontSizeViolations("/* was font-size: 11px */ .a { font-size: var(--font-size-caption); }")).toEqual(
    [],
  );
});

// --- (i) tracking comes from one of the two tracking tokens -------------
//
// The critique found five hand-written tracking values in use at once
// (-0.02em, 0.02em x4, 0.04em x5, 0.05em x2, 0.08em x7) doing two jobs
// between them. The ramp settles it at two tokens, one per job:
// --tracking-display (-0.02em) tightens semibold titles, --tracking-eyebrow
// (0.06em) opens THE uppercase eyebrow. `inherit` and `normal` are the two
// ways to say "no tracking of my own", and stay legal.
const LETTER_SPACING_RE = /letter-spacing\s*:\s*([^;}]+)/gi;
const TRACKING_VALUE_RE = /^(var\(--tracking-(?:display|eyebrow)\)|inherit|normal)$/;

function trackingViolations(cssText: string): string[] {
  const violations: string[] = [];
  const withoutComments = cssText.replace(COMMENT_RE, " ");
  for (const match of withoutComments.matchAll(LETTER_SPACING_RE)) {
    const value = match[1]!.trim();
    if (!TRACKING_VALUE_RE.test(value)) violations.push(value);
  }
  return violations;
}

for (const [path, text] of OTHER_STYLESHEETS) {
  test(`${path} tracks type with a tracking token`, () => {
    expect(trackingViolations(text)).toEqual([]);
  });
}

test("catches a hand-written tracking value", () => {
  expect(trackingViolations(".a { letter-spacing: 0.04em; }")).toEqual(["0.04em"]);
  expect(trackingViolations(".a { letter-spacing: 0.05em; }")).toEqual(["0.05em"]);
  expect(trackingViolations(".a { letter-spacing: -0.02em; }")).toEqual(["-0.02em"]);
});

test("allows the two tracking tokens and the two opt-outs", () => {
  expect(trackingViolations(".a { letter-spacing: var(--tracking-display); }")).toEqual([]);
  expect(trackingViolations(".a { letter-spacing: var(--tracking-eyebrow); }")).toEqual([]);
  expect(trackingViolations(".a { letter-spacing: inherit; }")).toEqual([]);
  expect(trackingViolations(".a { letter-spacing: normal; }")).toEqual([]);
});

test("does not flag letter-spacing mentioned only in a comment", () => {
  expect(trackingViolations("/* letter-spacing: 0.04em; */ .a { color: var(--ink-mid); }")).toEqual([]);
});

// --- (j) uppercase means the whole eyebrow recipe -----------------------
//
// Uppercase is the app's ONE eyebrow: caption size, --tracking-eyebrow, at
// most two words, never a sentence. The critique found 21 uppercase rules
// disagreeing about all three of size, tracking and ink (docs/web-ui/
// typography-spacing-critique-2026-09-06.md finding 4). Uppercase at body
// size with default tracking is just shouting; uppercase in --ink-low is
// shouting quietly, which is the worst of both.
//
// This supersedes the interim copy of the check that lived in
// rhythm.test.ts while the uppercase sites were being fixed.
//
// The block regex matches innermost brace pairs, so a rule nested inside a
// media query is checked as itself and the wrapper is skipped - the recipe
// is a property of the rule, not of what surrounds it.
const RULE_BLOCK_RE = /\{([^{}]*)\}/g;
const UPPERCASE_RE = /text-transform\s*:\s*uppercase/;
const CAPTION_SIZE_RE = /font-size\s*:\s*var\(--font-size-caption\)/;
const EYEBROW_TRACKING_RE = /letter-spacing\s*:\s*var\(--tracking-eyebrow\)/;
const INK_LOW_TEXT_RE = /color\s*:\s*var\(--ink-low\)/;

function eyebrowRecipeViolations(cssText: string): string[] {
  const violations: string[] = [];
  const withoutComments = cssText.replace(COMMENT_RE, " ");
  for (const block of withoutComments.matchAll(RULE_BLOCK_RE)) {
    const body = block[1]!;
    if (!UPPERCASE_RE.test(body)) continue;
    if (!CAPTION_SIZE_RE.test(body)) violations.push("uppercase without caption size");
    if (!EYEBROW_TRACKING_RE.test(body)) violations.push("uppercase without eyebrow tracking");
    if (INK_LOW_TEXT_RE.test(body)) violations.push("uppercase in --ink-low");
  }
  return violations;
}

for (const [path, text] of OTHER_STYLESHEETS) {
  test(`${path} writes every uppercase rule as a complete eyebrow`, () => {
    expect(eyebrowRecipeViolations(text)).toEqual([]);
  });
}

test("catches an uppercase rule missing a piece of the recipe", () => {
  expect(eyebrowRecipeViolations(".a { text-transform: uppercase; letter-spacing: var(--tracking-eyebrow); }")).toEqual(
    ["uppercase without caption size"],
  );
  expect(eyebrowRecipeViolations(".a { font-size: var(--font-size-caption); text-transform: uppercase; }")).toEqual([
    "uppercase without eyebrow tracking",
  ]);
});

test("catches an uppercase rule set in --ink-low", () => {
  expect(
    eyebrowRecipeViolations(
      ".a { font-size: var(--font-size-caption); text-transform: uppercase; letter-spacing: var(--tracking-eyebrow); color: var(--ink-low); }",
    ),
  ).toEqual(["uppercase in --ink-low"]);
});

test("allows a complete eyebrow", () => {
  expect(
    eyebrowRecipeViolations(
      ".a { font-size: var(--font-size-caption); text-transform: uppercase; letter-spacing: var(--tracking-eyebrow); color: var(--ink-mid); }",
    ),
  ).toEqual([]);
});

test("ignores a rule that is not uppercase at all", () => {
  expect(eyebrowRecipeViolations(".a { text-transform: none; }")).toEqual([]);
  expect(eyebrowRecipeViolations(".a { text-transform: capitalize; font-size: var(--font-size-body); }")).toEqual([]);
  expect(eyebrowRecipeViolations(".a { color: var(--ink-low); font-size: var(--font-size-body); }")).toEqual([]);
});

test("does not flag an uppercase rule quoted only in a comment", () => {
  expect(eyebrowRecipeViolations("/* .old { text-transform: uppercase; } */ .a { color: var(--ink-mid); }")).toEqual(
    [],
  );
});
