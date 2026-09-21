import { expect, test } from "vitest";
import { mediaBlock, readModuleCss, topRuleBlock } from "./cssBlock";

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

test("topRuleBlock treats selector metacharacters literally, so callers pass plain selectors", () => {
  // A caller matching an attribute selector by its plain text, no hand-written
  // regex escapes: the "." in a class and the brackets in an attribute test
  // must not silently widen into any-character or group syntax.
  const css = '.status[data-state="failed"] { color: red; }\n.statusX { color: blue; }';
  expect(topRuleBlock(css, '.status[data-state="failed"]')).toBe(" color: red; ");
  expect(() => topRuleBlock(css, ".statusXy")).toThrow(/no top-level rule/);
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

test("readModuleCss reads a stylesheet next to the calling test file, off disk", () => {
  // tokens.css is the canonical consumer-visible CSS in this directory; reading
  // it through the helper proves the import.meta.url resolution works end to
  // end (the vitest test.css:false constraint is why this exists at all).
  const css = readModuleCss(import.meta.url, "tokens.css");
  expect(css).toContain("--tooltip-bg");
});
