// Regression net for the PWA manifest's brand colors (floor
// parity-m8-periphery.md §4.4: background_color/theme_color are "kept in
// sync by hand across two files, not derived from one source... needs to
// be re-synced together, not independently, when the palette changes").
// Reads both files straight off disk with node:fs, the same approach
// token-contract.test.ts uses for tokens.css itself (see that file's own
// header comment for why: vitest's test.css:false makes a `.css?raw`
// import resolve empty, and editing vite.config.ts is out of scope here).
//
// cmd/serf-hub/assets/manifest.webmanifest is a Go-embedded asset outside
// the frontend/ tree (web.go's manifestFS/assetsRoot) - there is no
// manifest.webmanifest anywhere under frontend/src, so this reaches out to
// it by a fixed relative path from this file's own location.
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { expect, test } from "vitest";

const STYLES_DIR = dirname(fileURLToPath(import.meta.url)); // frontend/src/styles
const FRONTEND_ROOT = dirname(dirname(STYLES_DIR)); // .. /.. = frontend
const SERF_HUB_ROOT = dirname(FRONTEND_ROOT); // frontend/.. = cmd/serf-hub

const TOKENS_CSS = readFileSync(join(STYLES_DIR, "tokens.css"), "utf8");
const MANIFEST_RAW = readFileSync(join(SERF_HUB_ROOT, "assets", "manifest.webmanifest"), "utf8");
const INDEX_HTML = readFileSync(join(FRONTEND_ROOT, "index.html"), "utf8");

function darkSurface0(): string {
  // The bare `:root { ... }` block (dark, the default theme) is declared
  // before the `[data-theme="light"]` override further down the file, so
  // the FIRST --surface-0 match is the dark value - the one the manifest
  // and index.html's theme-color meta are meant to mirror.
  const match = /--surface-0:\s*(#[0-9a-fA-F]{6})/.exec(TOKENS_CSS);
  if (!match) throw new Error("pwa-manifest-colors test: could not locate --surface-0 in tokens.css");
  return match[1]!.toLowerCase();
}

test("the PWA manifest's background_color/theme_color match tokens.css's dark --surface-0", () => {
  const brandBackground = darkSurface0();
  const manifest = JSON.parse(MANIFEST_RAW) as { background_color: string; theme_color: string };
  expect(manifest.background_color.toLowerCase()).toBe(brandBackground);
  expect(manifest.theme_color.toLowerCase()).toBe(brandBackground);
});

test("index.html's theme-color meta matches tokens.css's dark --surface-0", () => {
  const metaMatch = /<meta name="theme-color" content="(#[0-9a-fA-F]{6})"/.exec(INDEX_HTML);
  if (!metaMatch) throw new Error("pwa-manifest-colors test: could not locate the theme-color meta in index.html");
  expect(metaMatch[1]!.toLowerCase()).toBe(darkSurface0());
});
