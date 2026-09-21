import { expect, test } from "vitest";
import { mediaBlock, topRuleBlock } from "./cssBlock";

test("topRuleBlock extracts one column-0 rule and nothing after it", () => {
  expect(topRuleBlock(".a { color: red; }\n.b { color: blue; }", ".a")).toBe(" color: red; ");
});

test("topRuleBlock does not reach into a rule nested inside a media block", () => {
  const css = "@media (hover: none) {\n  .a { display: none; }\n}\n.a { color: red; }";
  expect(topRuleBlock(css, ".a")).toBe(" color: red; ");
});

test("topRuleBlock throws on a selector that is not there", () => {
  expect(() => topRuleBlock(".a { color: red; }", ".missing")).toThrow(/no top-level rule for \.missing/);
});

test("mediaBlock returns the nested rules the condition owns", () => {
  expect(mediaBlock("@media (hover: none) {\n  .a { display: none; }\n}", "hover: none")).toBe(
    "\n  .a { display: none; }\n",
  );
});

test("mediaBlock throws on a condition that is not there", () => {
  expect(() => mediaBlock("@media (hover: none) { .a { display: none; } }", "prefers-reduced-motion: reduce")).toThrow(
    /no @media \(prefers-reduced-motion: reduce\) block/,
  );
});
