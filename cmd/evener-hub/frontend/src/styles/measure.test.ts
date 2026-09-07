// @vitest-environment node

// The type ramp and the reading measure (docs/web-ui/typography-spacing-
// critique-2026-09-06.md R1, R2), pinned off disk the way token-contract
// does: jsdom evaluates no cascade, so the contract is on the declarations.
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { expect, test } from "vitest";

const SRC = dirname(dirname(fileURLToPath(import.meta.url)));
const read = (path: string): string => readFileSync(join(SRC, path), "utf8");

test("the ramp is 12/13/15/18/22/28 with a 16px body on phones", () => {
  const tokens = read("styles/tokens.css");
  const steps = [
    ["caption", 12],
    ["ui", 13],
    ["body", 15],
    ["pane-title", 18],
    ["page-title", 22],
    ["display", 28],
  ] as const;
  for (const [name, px] of steps) {
    expect(tokens, `--font-size-${name}`).toMatch(
      new RegExp(`--font-size-${name}: calc\\(${px}px \\* var\\(--font-scale\\)\\);`),
    );
  }
  const phoneStart = tokens.indexOf("@media (max-width: 899px)");
  expect(phoneStart, "tokens.css phone block uses the 899px boundary").toBeGreaterThan(0);
  const phone = tokens.slice(phoneStart);
  expect(phone).toMatch(/--font-size-body: calc\(16px \* var\(--font-scale\)\);/);
});

test("the reading measure is one body-level token with a wide override", () => {
  const tokens = read("styles/tokens.css");
  expect(tokens).toMatch(/--session-measure: 44rem;/);
  expect(tokens).toMatch(/body\[data-transcript-measure="wide"\]\s*\{\s*--session-measure: 64rem;/);
  // The literal used to live on .turn and be hand-copied into session.module.css.
  expect(read("panes/session/transcript/turnblock.module.css")).not.toMatch(/--session-measure:\s*\d/);
  expect(read("panes/session/session.module.css")).not.toMatch(/76rem/);
});

test("prose is bounded by the column, not a percentage of the pane", () => {
  expect(read("panes/session/transcript/messages/agentmessageitem.module.css")).not.toMatch(/max-width:\s*92%/);
  expect(read("panes/session/transcript/messages/usermessageitem.module.css")).not.toMatch(/max-width:\s*92%/);
});

test("the composer field takes the body size, floored at the control size on phones", () => {
  // max(control, body): body on desktop (15px, where control is the 13px ui
  // step), and never under the 16px control floor on phones - --font-size-body
  // alone drops to 14.4px under the S preference (roborev on PR #947).
  expect(read("widgets/textarea/textarea.module.css")).toMatch(
    /\.textarea\s*\{[^}]*font-size: max\(var\(--font-size-control\), var\(--font-size-body\)\)/,
  );
  // The spawn pane's phone override used to re-set the body size; it now
  // inherits the widget's floor and only keeps the writing-surface height.
  const spawn = read("panes/spawn/spawn.module.css");
  expect(spawn).not.toMatch(/\.form textarea\s*\{[^}]*font-size/);
});
