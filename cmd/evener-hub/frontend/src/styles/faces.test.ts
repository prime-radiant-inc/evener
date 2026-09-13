// @vitest-environment node

// Principle 7 (docs/web-ui/decisions.md): mono is for machine text only.
// These rules style chrome a person reads constantly - the model chip, the
// status row's figures, rail ages, turn footers - and must stay on the sans
// face with tabular figures, never JetBrains Mono (docs/web-ui/typography-
// spacing-critique-2026-09-06.md finding 5 / R5). Read off disk like
// token-contract.test.ts: jsdom evaluates no cascade.
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { expect, test } from "vitest";

const SRC = dirname(dirname(fileURLToPath(import.meta.url)));

function rule(path: string, selector: string): string {
  const css = readFileSync(join(SRC, path), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  const escaped = selector.replace(/[.[\]]/g, (c) => `\\${c}`);
  const match = new RegExp(`(?:^|\\n)${escaped}\\s*\\{([^}]*)\\}`).exec(css);
  if (!match) throw new Error(`${path}: no rule ${selector}`);
  return match[1]!;
}

test.each([
  ["panes/session/chrome/modelswitch.module.css", ".value"],
  ["panes/session/chrome/statusrow.module.css", ".figure"],
  ["shell/rail/RailRow.module.css", ".time"],
  ["panes/session/transcript/messages/turnseparator.module.css", ".row"],
])("%s %s is sans with tabular figures", (path, selector) => {
  const body = rule(path, selector);
  expect(body).not.toMatch(/--font-mono/);
  expect(body).toMatch(/font-variant-numeric:\s*tabular-nums/);
});

test("the status row no longer offers a mono class for figures", () => {
  const css = readFileSync(join(SRC, "panes/session/chrome/statusrow.module.css"), "utf8");
  expect(css).not.toMatch(/(?:^|\n)\.mono\s*\{/);
});

test("editorial prose has a distinct serif face and body-scaled size", () => {
  const palette = rule("styles/tokens.css", '[data-theme="dark"]');
  expect(palette).toMatch(/--font-prose:\s*"Source Serif 4 Variable", Georgia, "Times New Roman", serif;/);
  expect(palette).toMatch(/--font-mono:\s*"JetBrains Mono Variable"/);
  expect(palette).not.toMatch(/--font-size-prose:/);
  expect(rule("styles/tokens.css", "body")).toMatch(/--font-size-prose:\s*calc\(18px \* var\(--font-scale\)\);/);
});

test("Markdown reads in serif while code retains its machine face", () => {
  const prose = rule("widgets/markdown/markdown.module.css", ".root");
  expect(prose).toMatch(/font-family:\s*var\(--font-prose\);/);
  expect(prose).toMatch(/font-size:\s*var\(--prose-font-size, var\(--font-size-prose\)\);/);
  expect(rule("widgets/markdown/markdown.module.css", ".inlineCode")).toMatch(/font-family:\s*var\(--font-mono\);/);
  expect(rule("widgets/codeblock/codeblock.module.css", ".pre")).toMatch(/font-family:\s*var\(--font-mono\);/);
});
