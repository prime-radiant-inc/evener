import { expect, test } from "vitest";
import { readModuleCss, topRuleBlock } from "../styles/cssBlock";

test("the comparison page owns vertical scrolling when the document is locked", () => {
  const page = topRuleBlock(readModuleCss(import.meta.url, "composer-responsive.module.css"), ".page").replace(
    /\s+/g,
    " ",
  );
  expect(page).toContain("box-sizing: border-box");
  expect(page).toContain("height: 100dvh");
  expect(page).toContain("overflow-y: auto");
});

test("previews preserve their labeled viewport widths and own horizontal overflow", () => {
  const css = readModuleCss(import.meta.url, "composer-responsive.module.css");
  expect(css).toMatch(/\.preview\s*\{\s*gap: var\(--space-3\);\s*overflow-x: auto;/);
  expect(topRuleBlock(css, ".frame")).toContain("max-width: none");
});
