// @vitest-environment node
import { expect, test } from "vitest";
import { blockBody, readModuleCss } from "./cssBlock";

// iOS Safari's text autosizing ("font boosting") grows text in a block it
// judges too narrow for its container, and it exempts text inside a form
// control. The rail's session title lives in a <button> (RailRow's .label) and
// its why line in a plain <span> (.why), so the engine grew only the smaller
// line: the caption rendered ~16.8px against a 13px title on the iPhone Duo's
// cover display, both authored 12/13. Nothing in the app opted out, so
// text-size-adjust is pinned on <body> and every descendant inherits it.
// The engine's heuristic is invisible to jsdom and to Playwright's WebKit
// (no isMobile), so the stylesheet structure is what this test can pin.
const GLOBAL_CSS = readModuleCss(import.meta.url, "global.css");

function bodyRule(): string {
  const open = GLOBAL_CSS.indexOf("body {");
  expect(open, "global.css must have a body rule").toBeGreaterThanOrEqual(0);
  return blockBody(GLOBAL_CSS, GLOBAL_CSS.indexOf("{", open));
}

test("the body rule pins text-size-adjust so iOS stops boosting caption text", () => {
  const body = bodyRule();
  // The prefixed form is the one iOS Safari honours; the unprefixed one is
  // what the spec names, and the build has no autoprefixer to add it.
  expect(body).toMatch(/-webkit-text-size-adjust:\s*100%/);
  expect(body).toMatch(/(?<!-webkit-)text-size-adjust:\s*100%/);
});
