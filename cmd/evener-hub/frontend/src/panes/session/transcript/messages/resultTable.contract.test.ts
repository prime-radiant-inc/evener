// Contract test for the structured-result table's cell layout bounds. jsdom
// cannot see CSS, so these are declaration-level assertions over the
// stylesheet source - the same idiom as notificationStatusIcon.contract.test.ts
// and thinkBlockMotion.test.ts. Comments are stripped first: a stylesheet
// grep that matches its own comment prose asserts nothing.
//
// The row and value bounds leave exactly one overflow axis open unless the
// key cell wraps too: a schema key can be as long as its author likes, and an
// unwrappable key forces the auto-layout table wider than the card.
// `overflow-wrap: anywhere` (not break-word) is the declaration that
// collapses a cell's min-content width, so the table itself never widens.
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { expect, test } from "vitest";

const here = dirname(fileURLToPath(import.meta.url));
const css = readFileSync(join(here, "notificationcard.module.css"), "utf8");
const uncommented = css.replace(/\/\*[\s\S]*?\*\//g, "");

// Matches the simple rule only: the compound first-row selector spells
// `.resultKey,` with a comma, so it cannot satisfy the brace that follows.
const ruleBody = (selector: string) => uncommented.match(new RegExp(`\\.${selector}\\s*\\{([^}]*)\\}`))?.[1] ?? "";

test("a result key wraps anywhere rather than widening the table", () => {
  const key = ruleBody("resultKey");
  expect(key).toContain("overflow-wrap: anywhere");
  expect(key).not.toContain("white-space: nowrap");
});

test("a result value keeps wrapping anywhere", () => {
  expect(ruleBody("resultValue")).toContain("overflow-wrap: anywhere");
});
