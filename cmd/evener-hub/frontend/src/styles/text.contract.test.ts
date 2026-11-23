// @vitest-environment node

// The shared secondary-text and verbatim recipes are pinned HERE, once, so
// each consumer stylesheet can assert that its class composes the recipe
// instead of repeating the declarations the recipe owns. Same off-disk
// contract style as rhythm.test.ts.
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { expect, test } from "vitest";
import { escapeRegExp } from "./cssBlock";

const here = dirname(fileURLToPath(import.meta.url));
const css = readFileSync(join(here, "text.module.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");

const rule = (selector: string): string => {
  const match = new RegExp(`(?:^|\\n)${escapeRegExp(selector)}\\s*\\{([^}]*)\\}`).exec(css);
  if (!match) throw new Error(`no rule ${selector}`);
  return match[1]!;
};

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
