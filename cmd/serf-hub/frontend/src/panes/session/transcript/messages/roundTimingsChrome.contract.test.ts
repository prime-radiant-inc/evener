// Contract test for the tiered-density spec (ratification item 5): a
// once-per-round notice belongs to topic 07's quiet one-liner rule, not
// the hairline-bordered scaffold box the system prompt uses.
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { expect, test } from "vitest";

const css = readFileSync(join(dirname(fileURLToPath(import.meta.url)), "systemnoticeitem.module.css"), "utf8");

// Extract each named rule block and inspect its declarations.
function ruleBlock(name: string): string {
  const start = css.indexOf(`.${name} {`);
  if (start === -1) throw new Error(`.${name} rule not found`);
  const end = css.indexOf("}", start);
  return css.slice(start, end);
}

test("round timings render without box chrome", () => {
  expect(ruleBlock("timings")).not.toContain("border");
  expect(ruleBlock("timingsBody")).not.toContain("border-top");
});
