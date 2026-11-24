// @vitest-environment node

// The shared secondary-text, verbatim and truncation recipes are pinned HERE,
// once, so each consumer stylesheet can assert that its class composes the
// recipe instead of repeating the declarations the recipe owns. Same off-disk
// contract style as rhythm.test.ts.
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { expect, test } from "vitest";
import { escapeRegExp } from "./cssBlock";

const here = dirname(fileURLToPath(import.meta.url));
const css = readFileSync(join(here, "text.module.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");

const ruleIn = (cssText: string, selector: string): string => {
  const match = new RegExp(`(?:^|\\n)${escapeRegExp(selector)}\\s*\\{([^}]*)\\}`).exec(cssText);
  if (!match) throw new Error(`no rule ${selector}`);
  return match[1]!;
};
const rule = (selector: string): string => ruleIn(css, selector);
const consumer = (rel: string): string =>
  readFileSync(join(dirname(here), rel), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");

test("textSecondary carries the shared secondary-text face, size and ink", () => {
  const secondary = rule(".textSecondary");
  expect(secondary).toMatch(/font-family: var\(--font-sans\)/);
  expect(secondary).toMatch(/font-size: var\(--font-size-ui\)/);
  expect(secondary).toMatch(/color: var\(--ink-mid\)/);
});

test("verbatim carries the machine-text recipe; verbatimWrap adds word breaking", () => {
  const verbatim = rule(".verbatim");
  expect(verbatim).toMatch(/font-family: var\(--font-mono\)/);
  expect(verbatim).toMatch(/font-size: var\(--font-size-caption\)/);
  expect(verbatim).toMatch(/color: var\(--ink-mid\)/);
  expect(verbatim).toMatch(/white-space: pre-wrap/);
  expect(rule(".verbatimWrap")).toMatch(/word-break: break-word/);
});

test("truncate carries the single-line truncation recipe", () => {
  const truncate = rule(".truncate");
  expect(truncate).toMatch(/overflow: hidden/);
  expect(truncate).toMatch(/text-overflow: ellipsis/);
  expect(truncate).toMatch(/white-space: nowrap/);
});

test("paneTitle carries the shared pane-title face, size, weight, tracking and leading", () => {
  const paneTitle = rule(".paneTitle");
  expect(paneTitle).toMatch(/color: var\(--ink-hi\)/);
  expect(paneTitle).toMatch(/font-family: var\(--font-prose\)/);
  expect(paneTitle).toMatch(/font-size: var\(--font-size-pane-title\)/);
  expect(paneTitle).toMatch(/font-weight: var\(--font-weight-semibold\)/);
  expect(paneTitle).toMatch(/letter-spacing: var\(--tracking-display\)/);
  expect(paneTitle).toMatch(/line-height: var\(--line-height-title\)/);
});

test.each([
  ["widgets/panescaffold/panescaffold.module.css", ".title"],
  ["shell/mobile/StackHost.module.css", ".stackTitle"],
])("%s %s composes the shared pane title", (file, selector) => {
  expect(ruleIn(consumer(file), selector)).toMatch(
    /composes:[^;]*\bpaneTitle\b[^;]*from "[./]*styles\/text\.module\.css"/,
  );
});
