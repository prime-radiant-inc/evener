// @vitest-environment node

// The viewport meta used to pin zoom (maximum-scale=1, user-scalable=no) to
// stop iOS Safari auto-zooming into the 13px composer field. That disabled
// pinch-zoom for the whole app (WCAG 1.4.4 resize text). Every editable
// control is now at least 16px on phones and on any coarse pointer -
// tokens.css's touch block floors --font-size-control beside --tap-min, and
// every input, select and textarea rule takes it (display-gates.test.ts scans
// for it) - and touch screens spend touch-action: pan-x pan-y (global.css)
// plus pinchVeto's gesturestart veto under the same coarse query, so
// double-tap and pinch zoom are
// deliberately gone there (Jesse 2026-11-15). The meta lock must not come
// back either way: the coarse query and the veto already own touch zoom,
// and iOS ignores maximum-scale for pinch besides. Reads index.html
// straight off
// disk with node:fs, the same approach pwa-manifest-colors.test.ts uses.
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { expect, test } from "vitest";

const STYLES_DIR = dirname(fileURLToPath(import.meta.url)); // frontend/src/styles
const FRONTEND_ROOT = dirname(dirname(STYLES_DIR)); // .. /.. = frontend

const INDEX_HTML = readFileSync(join(FRONTEND_ROOT, "index.html"), "utf8");

function viewportContent(): string {
  const match = /<meta name="viewport" content="([^"]*)"/.exec(INDEX_HTML);
  if (!match) throw new Error("viewport-pin test: could not locate the viewport meta in index.html");
  return match[1]!;
}

test("the viewport meta never disables zoom", () => {
  const content = viewportContent();
  expect(content).toContain("width=device-width");
  expect(content).toContain("initial-scale=1");
  expect(content).not.toContain("maximum-scale");
  expect(content).not.toContain("user-scalable");
});

test("the viewport meta uses viewport-fit=cover so safe-area insets are nonzero", () => {
  // StackHost.module.css and the spawn panes already pad with
  // env(safe-area-inset-bottom); without viewport-fit=cover those env()
  // values resolve to 0 and the insets are dead code.
  expect(viewportContent()).toContain("viewport-fit=cover");
});

test("the viewport meta resizes content around the on-screen keyboard", () => {
  // Chromium/Android's half of the keyboard fix; Safari ignores the unknown
  // key and is covered by useKeyboardInset (see its header).
  expect(viewportContent()).toContain("interactive-widget=resizes-content");
});
