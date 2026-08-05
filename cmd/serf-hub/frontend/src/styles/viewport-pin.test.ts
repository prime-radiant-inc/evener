// @vitest-environment node

// Regression net for the mobile "focus zooms the page" defect: iOS Safari
// auto-zooms when an editable field under 16px (the composer textarea is
// 13px via --font-size-ui) gains focus, because the viewport meta permitted
// scaling. The fix pins the viewport instead of restyling the composer, so
// the web UI behaves like an installed app. Reads index.html straight off
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

test("the viewport meta pins zoom so focusing a field cannot scale the page", () => {
  const content = viewportContent();
  expect(content).toContain("width=device-width");
  expect(content).toContain("initial-scale=1");
  expect(content).toContain("maximum-scale=1");
  expect(content).toContain("user-scalable=no");
});

test("the viewport meta uses viewport-fit=cover so safe-area insets are nonzero", () => {
  // StackHost.module.css and the spawn panes already pad with
  // env(safe-area-inset-bottom); without viewport-fit=cover those env()
  // values resolve to 0 and the insets are dead code.
  expect(viewportContent()).toContain("viewport-fit=cover");
});
