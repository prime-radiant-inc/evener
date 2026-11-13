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
  // Only a message that OPENS an exchange takes the exchange step: mid-turn
  // user steering renders through the same view with opensExchange={false}
  // and must not claim the boundary (roborev, PR #947).
  expect(rule(user, ".message")).not.toMatch(/margin-top/);
  expect(rule(user, '.message[data-opens-exchange="true"]')).toMatch(/margin-top: var\(--rhythm-exchange\)/);
  const tool = read("panes/session/transcript/toolcallitem.module.css");
  // A tool row is a row of a card: one item step above and below its lines.
  expect(rule(tool, ".call")).toMatch(/--tool-card-row-pad: var\(--space-2\)/);
  expect(rule(tool, ".call")).toMatch(/padding: var\(--tool-card-row-pad\) var\(--tool-card-inset\)/);
  const scaffold = read("widgets/panescaffold/panescaffold.module.css");
  expect(rule(scaffold, ".body")).toMatch(/padding: var\(--space-5\)/);
  const separator = read("panes/session/transcript/messages/turnseparator.module.css");
  expect(rule(separator, ".row")).toMatch(/padding: var\(--rhythm-group\) 0 var\(--rhythm-line\)/);
});

// Every row of a tool card takes the same step, whatever it shows: the kind
// icon's seat is taller than one line of text, so a one-line row needs the
// full step to keep the seat clear of the card's dividers, and a folded run's
// summary lines up with the rows it stands for.
test("every tool card row takes one padding step, open or collapsed", () => {
  const tool = read("panes/session/transcript/toolcallitem.module.css");
  expect(tool).not.toMatch(/\.call\[data-intent-only/);
  const run = read("panes/session/transcript/toolrungroup.module.css");
  expect(rule(run, ".summary")).toMatch(/padding: var\(--space-2\) var\(--space-3\)/);
  expect(run).not.toMatch(/\.group\[open\] > \.summary/);
});

test("the most-read quiet text sits in --ink-mid, not --ink-low", () => {
  const think = read("panes/session/transcript/messages/thinkblock.module.css");
  expect(rule(think, ".label")).toMatch(/color: var\(--ink-mid\)/);
  const liveness = read("panes/session/transcript/flow/livenessline.module.css");
  expect(rule(liveness, ".line")).toMatch(/color: var\(--ink-mid\)/);
  const agent = read("panes/session/transcript/messages/agentmessageitem.module.css");
  expect(rule(agent, ".meta")).toMatch(/color: var\(--ink-mid\)/);
});
