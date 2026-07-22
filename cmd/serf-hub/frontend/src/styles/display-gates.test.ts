import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { expect, test } from "vitest";

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
  const rootBlock = /:root\s*\{([\s\S]*?)\n\}/.exec(CSS);
  expect(rootBlock, "tokens.css must have a :root block").not.toBeNull();
  expect(rootBlock![1]).not.toMatch(/--font-size-body:/);
});

test("phone density is gated behind the <=900px phone media query", () => {
  // Desktop must never inherit a density override: the whole gate lives inside
  // the phone media query. Assert the comfortable multiplier is inside a
  // max-width:900px block, not at top level.
  const media = /@media\s*\(max-width:\s*900px\)\s*\{([\s\S]*?)\n\}/.exec(CSS);
  expect(media, "tokens.css must have a max-width:900px media block").not.toBeNull();
  expect(media![1]).toMatch(/body\[data-phone-density="comfortable"\]\s*\{[^}]*--density-scale:\s*1\.25/);
});

test("phone density opens vertical rhythm by scaling line-height through --density-scale", () => {
  const media = /@media\s*\(max-width:\s*900px\)\s*\{([\s\S]*?)\n\}/.exec(CSS);
  expect(media![1]).toMatch(/--line-height-body:\s*calc\([^;]*var\(--density-scale\)/);
});

test("the compact density default leaves the base grid unscaled (multiplier 1)", () => {
  const media = /@media\s*\(max-width:\s*900px\)\s*\{([\s\S]*?)\n\}/.exec(CSS);
  // The base body rule inside the media query seeds --density-scale: 1 so
  // "compact" (and any unset value) holds the base line-height.
  expect(media![1]).toMatch(/--density-scale:\s*1\b/);
});
