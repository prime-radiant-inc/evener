// @vitest-environment node
import { expect, test } from "vitest";
import { readModuleCss, topRuleBlock } from "./cssBlock";

// iOS Safari's text autosizing grows text in a block whose measured width it
// judges too narrow, so a rail row's truncated caption grew to ~16.8px while
// the 13px title above it and the 12px timestamp beside it did not (iPhone Duo
// cover display). Nothing in the app opted out. The engine's heuristic is
// invisible to jsdom and to Playwright's WebKit (no isMobile), so this pins
// the stylesheet structure instead.
const GLOBAL_CSS = readModuleCss(import.meta.url, "global.css");

test("the body rule pins text-size-adjust so iOS stops boosting caption text", () => {
  const body = topRuleBlock(GLOBAL_CSS, "body");
  // Both spellings are explicit because the dev server serves this file
  // unminified; the production build adds -moz- on its own.
  expect(body).toMatch(/-webkit-text-size-adjust:\s*100%/);
  expect(body).toMatch(/^\s*text-size-adjust:\s*100%/m);
});

test("no other rule reopens autosizing", () => {
  // Inheriting 100% does not stop a later rule setting `auto` on a subtree,
  // which would put the engine's guess back in front of the ramp. Same
  // exclusivity idiom as mobile-root-overscroll.test.ts: with the body rule
  // taken out, nothing else may declare the property.
  const body = topRuleBlock(GLOBAL_CSS, "body");
  expect(body).toMatch(/text-size-adjust\s*:/);
  expect(GLOBAL_CSS.replace(body, "")).not.toMatch(/text-size-adjust\s*:/);
});
