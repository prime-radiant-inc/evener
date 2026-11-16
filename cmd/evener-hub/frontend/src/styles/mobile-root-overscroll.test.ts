// @vitest-environment node

import { expect, test } from "vitest";
import { MOBILE_QUERY } from "../shell/useIsMobile";
import { escapeRegExp, mediaBlock, readModuleCss } from "./cssBlock";

// readModuleCss also strips comments, so a declaration quoted in a doc
// comment can never satisfy - or fail - an assertion about a rule.
const GLOBAL_CSS = readModuleCss(import.meta.url, "global.css");

function removeMediaBlock(source: string, mediaQuery: string): string {
  const start = source.indexOf(`@media ${mediaQuery} {`);
  expect(start).toBeGreaterThanOrEqual(0);

  let depth = 0;
  for (let index = start; index < source.length; index += 1) {
    const char = source[index]!;
    if (char === "{") {
      depth += 1;
    } else if (char === "}") {
      depth -= 1;
      if (depth === 0) {
        return source.slice(0, start) + source.slice(index + 1);
      }
    }
  }

  throw new Error("media block was not closed");
}

test("mobile: html suppresses root overscroll while panes retain scroll ownership", () => {
  const mobile = GLOBAL_CSS.match(new RegExp(`@media ${escapeRegExp(MOBILE_QUERY)} \\{([\\s\\S]*?)\\n\\}`));
  expect(mobile).not.toBeNull();

  const htmlRule = mobile![1]!.match(/html \{([^}]*)\}/);
  expect(htmlRule).not.toBeNull();
  expect(htmlRule![1]).toContain("overscroll-behavior: none");

  const documentLock = mobile![1]!.match(/html,\s*\n\s*body \{([^}]*)\}/);
  expect(documentLock).not.toBeNull();
  expect(documentLock![1]).toContain("overflow: hidden");
});

test("desktop: root overscroll containment is not declared outside the mobile query", () => {
  const withoutMobileBlock = removeMediaBlock(GLOBAL_CSS, MOBILE_QUERY);
  expect(withoutMobileBlock).not.toContain("overscroll-behavior");
});

test("touch screens pan but never zoom this app", () => {
  // The 2026-11-15 decision (Jesse): every touch screen pans and never zooms
  // the app, matching the native app, which has no zoom on any display.
  // global.css's coarse-gate comment owns the full rationale.
  const coarse = mediaBlock(GLOBAL_CSS, "any-pointer: coarse");
  expect(coarse).toMatch(/\bbody \{[^}]*touch-action: pan-x pan-y;/);
  // That block alone may declare touch-action: with it removed, no other
  // rule - top-level, the phone gate, or any later gate - owns the property.
  expect(removeMediaBlock(GLOBAL_CSS, "(any-pointer: coarse)")).not.toContain("touch-action");
});
