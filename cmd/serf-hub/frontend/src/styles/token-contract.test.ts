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
test("every non-token stylesheet under src is named global.css or <name>.module.css", () => {
  const offenders = OTHER_STYLESHEETS.map(([path]) => path).filter((path) => {
    const base = basenameOf(path);
    return base !== "global.css" && !base.endsWith(".module.css");
  });
  expect(offenders).toEqual([]);
});

// --- (a) no chromatic literal outside tokens.css -----------------------
//
// Two independent mechanisms, both feeding chromaticLiteralViolations():
//
// 1. COLOR_LITERAL_RE - hex, rgb()/rgba(), hsl()/hsla(), oklch(), oklab(),
//    lab(), lch(): every literal-color FUNCTION/hex syntax CSS has. Scanned
//    across the whole file text (comments included) because these forms
//    are distinctive enough not to false-positive on a selector or class
//    name. color-mix() is deliberately NOT in this list - mixing
//    var(--token) values (e.g. `color-mix(in oklab, var(--accent) 40%,
//    transparent)`) is normal token composition, not a new color; any raw
//    hex/rgb/... smuggled into a color-mix() argument is still caught
//    because this regex scans the whole file text, nesting notwithstanding.
// 2. NAMED_COLOR_RE - the CSS named-color keywords (red, white, black, ...;
//    NOT transparent/currentColor, which aren't chromatic, and NOT CSS-wide
//    keywords like inherit/initial/unset/revert/none/auto). Unlike (1),
//    named colors are ordinary English words that legitimately appear in
//    class names, comments, and font-family lists ("Helvetica" is not a
//    color), so this one is scanned ONLY inside extracted declaration
//    VALUES (property: value pairs), never the raw file text - a selector
//    like `.red { color: var(--danger); }` is not a violation.
//
// Neither mechanism strips comments before scanning (1) or before
// extracting declarations for (2) - a hex code or color name mentioned in
// a comment can still trip this. Accepted trade-off, not a parser.
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
// not, say, on "hover" inside the selector text ".button:hover".
const DECLARATION_VALUE_RE = /[{;]\s*[a-zA-Z-]+\s*:\s*([^;{}]+)[;}]/g;

function chromaticLiteralViolations(cssText: string): string[] {
  const violations: string[] = [];
  for (const match of cssText.matchAll(COLOR_LITERAL_RE)) violations.push(match[0]);
  for (const declaration of cssText.matchAll(DECLARATION_VALUE_RE)) {
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

test("still catches hex/rgb/hsl/oklch literals alongside named colors", () => {
  expect(chromaticLiteralViolations(".foo { color: #ff0000; }")).toEqual(["#ff0000"]);
  expect(chromaticLiteralViolations(".foo { color: rgb(255, 0, 0); }")).toEqual(["rgb("]);
});

// --- (b) the four attention-family vars stay on the allowlist ----------
//
// --attention/--alive/--danger/--accent exist so exactly one meaning maps
// to each hue across the whole app (a human is needed / agent is working /
// something failed / focus-selection-links). A widget earns a place on
// this list only when it has a state that genuinely needs one of those
// hues - a status color, a destructive action, the cadence signature -
// never for decoration. This list is seeded with every widget named in the
// wave-2 plan's locked API (not just the two this task builds) so later
// streams, which never edit this file, land pre-cleared.
const SEMANTIC_USE_ALLOWLIST = [
  "cadence", // signature: state dot + trailing-edge tick tint
  "button", // danger variant
  "chip", // tone prop
  "badge", // tone prop
  "statusdot", // state color
  "meter", // danger/attention fill tone
  "toast", // tone prop
  "dialog", // danger footer
];

const SEMANTIC_VAR_RE = /var\(\s*--(?:attention|alive|danger|accent)\b/;

// The allowlist is a widget concept: only src/widgets/<name>/<name>.module.css
// is eligible, keyed off the directory (not just the basename) so a
// same-named stylesheet elsewhere (e.g. a dev-tooling file that happens to
// be called button.module.css) can't ride along on a real widget's entry.
const WIDGET_STYLESHEET_RE = /^widgets\/([a-z0-9-]+)\/\1\.module\.css$/;

for (const [path, text] of OTHER_STYLESHEETS) {
  test(`${path} only reaches for --attention/--alive/--danger/--accent if allowlisted`, () => {
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
