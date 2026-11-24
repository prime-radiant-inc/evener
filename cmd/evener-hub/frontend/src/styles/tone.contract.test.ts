// @vitest-environment node

// The four semantic tone families are pinned HERE, once, so each consumer
// stylesheet can compose the family instead of naming that family's
// -edge/-bg/-ink companions again. Same off-disk contract style as
// text.contract.test.ts.
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { expect, test } from "vitest";
import { escapeRegExp } from "./cssBlock";

const here = dirname(fileURLToPath(import.meta.url));
const stripComments = (text: string): string => text.replace(/\/\*[\s\S]*?\*\//g, "");

const toneCss = stripComments(readFileSync(join(here, "tone.module.css"), "utf8"));
const widgetCss = (path: string): string => stripComments(readFileSync(join(here, "..", path), "utf8"));

const rule = (css: string, selector: string): string => {
  const match = new RegExp(`(?:^|\\n)${escapeRegExp(selector)}\\s*\\{([^}]*)\\}`).exec(css);
  if (!match) throw new Error(`no rule ${selector}`);
  return match[1]!;
};

const FAMILIES = { toneAlive: "alive", toneDanger: "danger", toneAttention: "attention", toneAccent: "accent" };
const COMPOSES = /composes:\s*(\w+) from "\.\.\/\.\.\/styles\/tone\.module\.css"/;

test("the shared tone recipe names each family's edge, wash and ink", () => {
  for (const [className, family] of Object.entries(FAMILIES)) {
    const body = rule(toneCss, `.${className}`);
    for (const companion of ["edge", "bg", "ink"]) {
      expect(body).toContain(`var(--${family}-${companion})`);
    }
  }
});

const CONSUMERS = [
  {
    path: "widgets/toast/toast.module.css",
    paints: [".toast", ".text"],
    tones: { ".success": "toneAlive", ".error": "toneDanger", ".warning": "toneAttention", ".info": "toneAccent" },
  },
  {
    path: "widgets/banner/banner.module.css",
    paints: [".banner"],
    tones: { ".attention": "toneAttention", ".danger": "toneDanger" },
  },
  {
    path: "widgets/inlinenotice/inlinenotice.module.css",
    paints: [".danger"],
    tones: { ".danger": "toneDanger" },
  },
] as const;

for (const consumer of CONSUMERS) {
  const css = widgetCss(consumer.path);

  test(`${consumer.path} composes the shared tone family for each of its tones`, () => {
    for (const [selector, family] of Object.entries(consumer.tones)) {
      const composed = COMPOSES.exec(rule(css, selector))?.[1];
      expect(composed, `${selector} in ${consumer.path}`).toBe(family);
    }
  });

  test(`${consumer.path} paints from the tone properties and names no family itself`, () => {
    for (const selector of consumer.paints) {
      expect(rule(css, selector)).toMatch(/var\(--tone-(edge|bg|ink)\)/);
    }
    expect(css).not.toMatch(/var\(--(?:alive|danger|attention)-/);
  });
}
