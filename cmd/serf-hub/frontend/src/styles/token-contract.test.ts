import { test, expect } from "vitest";
import { readFileSync, readdirSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";

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
// (.dockview-theme-serf, passed to DockviewReact's own className prop -
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
//    the whole file text, nesting notwithstanding.
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
const COLOR_LITERAL_RE = /#[0-9a-fA-F]{3,8}\b|\b(?:rgba?|hsla?|oklch|oklab|lab|lch)\(/g;

// CSS Color Module Level 4 named colors (the full "extended color keywords"
// list, including rebeccapurple) - https://www.w3.org/TR/css-color-4/#named-colors,
// cross-checked against MDN's named-color page. transparent is excluded:
// zero alpha, not a hue.
const CSS_NAMED_COLORS = [
  "aliceblue", "antiquewhite", "aqua", "aquamarine", "azure", "beige", "bisque",
  "black", "blanchedalmond", "blue", "blueviolet", "brown", "burlywood",
  "cadetblue", "chartreuse", "chocolate", "coral", "cornflowerblue", "cornsilk",
  "crimson", "cyan", "darkblue", "darkcyan", "darkgoldenrod", "darkgray",
  "darkgreen", "darkgrey", "darkkhaki", "darkmagenta", "darkolivegreen",
  "darkorange", "darkorchid", "darkred", "darksalmon", "darkseagreen",
  "darkslateblue", "darkslategray", "darkslategrey", "darkturquoise",
  "darkviolet", "deeppink", "deepskyblue", "dimgray", "dimgrey", "dodgerblue",
  "firebrick", "floralwhite", "forestgreen", "fuchsia", "gainsboro",
  "ghostwhite", "gold", "goldenrod", "gray", "green", "greenyellow", "grey",
  "honeydew", "hotpink", "indianred", "indigo", "ivory", "khaki", "lavender",
  "lavenderblush", "lawngreen", "lemonchiffon", "lightblue", "lightcoral",
  "lightcyan", "lightgoldenrodyellow", "lightgray", "lightgreen", "lightgrey",
  "lightpink", "lightsalmon", "lightseagreen", "lightskyblue",
  "lightslategray", "lightslategrey", "lightsteelblue", "lightyellow", "lime",
  "limegreen", "linen", "magenta", "maroon", "mediumaquamarine", "mediumblue",
  "mediumorchid", "mediumpurple", "mediumseagreen", "mediumslateblue",
  "mediumspringgreen", "mediumturquoise", "mediumvioletred", "midnightblue",
  "mintcream", "mistyrose", "moccasin", "navajowhite", "navy", "oldlace",
  "olive", "olivedrab", "orange", "orangered", "orchid", "palegoldenrod",
  "palegreen", "paleturquoise", "palevioletred", "papayawhip", "peachpuff",
  "peru", "pink", "plum", "powderblue", "purple", "rebeccapurple", "red",
  "rosybrown", "royalblue", "saddlebrown", "salmon", "sandybrown", "seagreen",
  "seashell", "sienna", "silver", "skyblue", "slateblue", "slategray",
  "slategrey", "snow", "springgreen", "steelblue", "tan", "teal", "thistle",
  "tomato", "turquoise", "violet", "wheat", "white", "whitesmoke", "yellow",
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
  expect(chromaticLiteralViolations(".foo { color: var(--ink-hi); /* comment */ background: red; }")).toEqual([
    "red",
  ]);
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
  "diffblock", // add/del line tints via alive/danger -bg companions
  "toast", // tone prop
  "dialog", // danger footer
];

const SEMANTIC_VAR_RE = /var\(\s*--(?:attention|alive|danger)\b/;

// The allowlist is a widget concept: only src/widgets/<name>/<name>.module.css
// is eligible, keyed off the directory (not just the basename) so a
// same-named stylesheet elsewhere (e.g. a dev-tooling file that happens to
// be called button.module.css) can't ride along on a real widget's entry.
const WIDGET_STYLESHEET_RE = /^widgets\/([a-z0-9-]+)\/\1\.module\.css$/;

for (const [path, text] of OTHER_STYLESHEETS) {
  test(`${path} only reaches for --attention/--alive/--danger if allowlisted`, () => {
    if (!SEMANTIC_VAR_RE.test(text)) return;
    const widgetMatch = WIDGET_STYLESHEET_RE.exec(path);
    expect(widgetMatch).not.toBeNull();
    expect(SEMANTIC_USE_ALLOWLIST).toContain(widgetMatch![1]);
  });
}

// --- (c) dark and light blocks declare the same color tokens -----------
//
// tokens.css defines one unqualified `:root { }` block (base tokens, dark
// by default) and one `[data-theme="light"] { }` block (light overrides).
// A color token declared in only one of the two silently breaks the other
// theme - it either falls back to the wrong hue or resolves to nothing.
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

test("tokens.css dark and light blocks declare the same color token names", () => {
  const darkBlock = extractBlock(TOKENS_CSS, /(?:^|\n):root\s*\{/);
  const lightBlock = extractBlock(TOKENS_CSS, /\[data-theme="light"\][^{]*\{/);
  const darkNames = colorTokenNames(darkBlock);
  const lightNames = colorTokenNames(lightBlock);

  const missingFromLight = [...darkNames].filter((name) => !lightNames.has(name)).sort();
  const missingFromDark = [...lightNames].filter((name) => !darkNames.has(name)).sort();
  expect({ missingFromLight, missingFromDark }).toEqual({ missingFromLight: [], missingFromDark: [] });
});
