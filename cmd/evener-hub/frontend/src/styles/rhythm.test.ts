// @vitest-environment node

// The vertical-rhythm and heading contract (docs/web-ui/typography-spacing-
// critique-2026-09-06.md R3, R4), pinned off disk the way token-contract
// does: jsdom evaluates no cascade, so the contract is on the declarations.
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { expect, test } from "vitest";

const SRC = dirname(dirname(fileURLToPath(import.meta.url)));
const read = (path: string): string => readFileSync(join(SRC, path), "utf8");

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
