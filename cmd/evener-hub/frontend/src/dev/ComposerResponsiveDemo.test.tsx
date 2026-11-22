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
