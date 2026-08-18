// @vitest-environment node

import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { expect, test } from "vitest";

// vitest leaves CSS unprocessed (test.css defaults to false), so the chrome-
// strip's effect can't be asserted through a rendered DOM. Read the stylesheet
// straight off disk and assert its rules instead - the same disk-read tactic
// token-contract.test.ts uses for the same reason. What actually matters here
// is verifiable statically: the rules are keyed off [data-single-pane] (so they
// are inert in normal multi-pane mode) and singlePane.ts loads the sheet (so
// they reach the bundle at all).
const HERE = dirname(fileURLToPath(import.meta.url)); // src/shell/singlePane
const GLOBAL_CSS = readFileSync(join(HERE, "global.css"), "utf8");
const SINGLEPANE_TS = readFileSync(join(dirname(HERE), "singlePane.ts"), "utf8");

const COMMENT_RE = /\/\*[\s\S]*?\*\//g;

// Each rule's selector = the prelude text before its `{`, comments stripped.
function ruleSelectors(css: string): string[] {
  const stripped = css.replace(COMMENT_RE, " ");
  return [...stripped.matchAll(/([^{}]+)\{/g)].map((m) => m[1]!.trim()).filter((s) => s.length > 0);
}

test("ruleSelectors extracts each rule's selector, ignoring comments", () => {
  const css = "/* c */ .a { display: none; }\n.b .c { color: var(--x); }";
  expect(ruleSelectors(css)).toEqual([".a", ".b .c"]);
});

test("every single-pane rule is scoped under [data-single-pane] so it is inert in normal mode", () => {
  const selectors = ruleSelectors(GLOBAL_CSS);
  expect(selectors.length).toBeGreaterThan(0);
  // A rule that didn't start at the marker would leak into the regular
  // multi-pane workspace - the whole point of keying off the marker.
  expect(selectors.filter((s) => !s.startsWith("[data-single-pane]"))).toEqual([]);
});

test("hides dockview's own tab strip in single-pane mode", () => {
  expect(GLOBAL_CSS).toMatch(/\[data-single-pane\][^{]*\.dv-tabs-and-actions-container\s*\{[^}]*display:\s*none/);
});

test("hides the mobile tree-drawer trigger (the sidebar/search/settings entry point) in single-pane mode", () => {
  expect(GLOBAL_CSS).toMatch(/\[data-single-pane\][^{]*button\[aria-label="Sessions"\]\s*\{[^}]*display:\s*none/);
});

test("singlePane.ts loads the chrome-strip stylesheet so its rules reach the eager bundle", () => {
  expect(SINGLEPANE_TS).toMatch(/import\s+["']\.\/singlePane\/global\.css["']/);
});
