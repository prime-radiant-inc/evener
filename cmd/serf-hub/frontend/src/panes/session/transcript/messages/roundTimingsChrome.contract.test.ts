// Contract test for the tiered-density spec (ratification item 5) and its
// follow-up: a once-per-round notice is topic 07's quiet one-liner - no
// hairline-bordered scaffold box (the original item-5 ruling), and no
// disclosure chrome of its own either (Jesse's review call: the round
// timings line is one flat line, full breakdown on its hover title).
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { expect, test } from "vitest";

const css = readFileSync(join(dirname(fileURLToPath(import.meta.url)), "systemnoticeitem.module.css"), "utf8");

test("round timings have no rule of their own - the plain shared line renders them", () => {
  // Any .timings* selector means the disclosure (or some new round-timings
  // chrome) is back. The flat line needs no rule beyond the shared .line.
  expect(css).not.toMatch(/\.timings\w*\s*\{/);
});
