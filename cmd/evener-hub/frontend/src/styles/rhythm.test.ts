// @vitest-environment node

// The vertical-rhythm and heading contract (docs/web-ui/typography-spacing-
// critique-2026-09-06.md R3, R4), pinned off disk the way token-contract
// does: jsdom evaluates no cascade, so the contract is on the declarations.
import { readdirSync, readFileSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";
import { expect, test } from "vitest";

const SRC = dirname(dirname(fileURLToPath(import.meta.url)));
const read = (path: string): string => readFileSync(join(SRC, path), "utf8");

function walkCss(dir: string): string[] {
  const found: string[] = [];
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name);
    if (entry.isDirectory()) found.push(...walkCss(full));
    else if (entry.isFile() && entry.name.endsWith(".css")) found.push(relative(SRC, full));
  }
  return found;
}

const rule = (css: string, selector: string): string => {
  const escaped = selector.replace(/[.[\]>]/g, (c) => `\\${c}`);
  const match = new RegExp(`(?:^|\\n)${escaped}\\s*\\{([^}]*)\\}`).exec(css);
  if (!match) throw new Error(`no rule ${selector}`);
  return match[1]!;
};

test("pane titles are sentence-case headings, not micro-labels", () => {
  const title = rule(read("widgets/panescaffold/panescaffold.module.css"), ".title");
  expect(title).not.toMatch(/text-transform/);
  expect(title).toMatch(/font-size: var\(--font-size-pane-title\)/);
  expect(title).toMatch(/font-weight: var\(--font-weight-semibold\)/);
  expect(title).toMatch(/color: var\(--ink-hi\)/);
});

test("every uppercase rule is a complete caption-size eyebrow in --ink-mid or darker", () => {
  for (const path of walkCss(SRC)) {
    const css = read(path).replace(/\/\*[\s\S]*?\*\//g, "");
    for (const block of css.matchAll(/\{([^{}]*)\}/g)) {
      const body = block[1]!;
      if (!/text-transform:\s*uppercase/.test(body)) continue;
      expect(body, `${path}: uppercase rule lacks caption size`).toMatch(/font-size: var\(--font-size-caption\)/);
      expect(body, `${path}: uppercase rule lacks eyebrow tracking`).toMatch(
        /letter-spacing: var\(--tracking-eyebrow\)/,
      );
      expect(body, `${path}: uppercase rule sits in --ink-low`).not.toMatch(/color: var\(--ink-low\)/);
    }
  }
});

test("exchange boundaries, runs and pane bodies use the rhythm and space tokens", () => {
  const user = read("panes/session/transcript/messages/usermessageitem.module.css");
  expect(rule(user, ".message")).toMatch(/margin-top: var\(--rhythm-exchange\)/);
  const tool = read("panes/session/transcript/toolcallitem.module.css");
  expect(rule(tool, ".call")).toMatch(/padding: var\(--rhythm-item\) 0/);
  const scaffold = read("widgets/panescaffold/panescaffold.module.css");
  expect(rule(scaffold, ".body")).toMatch(/padding: var\(--space-5\)/);
  const separator = read("panes/session/transcript/messages/turnseparator.module.css");
  expect(rule(separator, ".row")).toMatch(/padding: var\(--rhythm-group\) 0 var\(--rhythm-line\)/);
});

test("the most-read quiet text sits in --ink-mid, not --ink-low", () => {
  const think = read("panes/session/transcript/messages/thinkblock.module.css");
  expect(rule(think, ".summary")).toMatch(/color: var\(--ink-mid\)/);
  expect(rule(think, ".label")).toMatch(/color: var\(--ink-mid\)/);
  const liveness = read("panes/session/transcript/flow/livenessline.module.css");
  expect(rule(liveness, ".line")).toMatch(/color: var\(--ink-mid\)/);
  const agent = read("panes/session/transcript/messages/agentmessageitem.module.css");
  expect(rule(agent, ".meta")).toMatch(/color: var\(--ink-mid\)/);
});
