import { readdirSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { expect, test } from "vitest";

// node:fs, not import.meta.glob: this project has no @types/node, and under
// vitest's default test.css: false a raw-content glob returns "" for CSS
// (see src/styles/token-contract.test.ts's own note) - reading the
// directory listings directly off disk sidesteps both, the same way that
// file does. src/styles/node-fs-shim.d.ts's ambient declarations cover the
// fs/path/url calls here too (ambient .d.ts declarations are program-wide).
const HERE = dirname(fileURLToPath(import.meta.url)); // src/dev
const WIDGETS_DIR = join(HERE, "..", "widgets");
const GALLERY_SECTIONS_DIR = join(HERE, "gallery-sections");

function widgetDirNames(): string[] {
  return readdirSync(WIDGETS_DIR, { withFileTypes: true })
    .filter((entry) => entry.isDirectory() && entry.name !== "internal")
    .map((entry) => entry.name)
    .sort();
}

function gallerySectionNames(): string[] {
  return readdirSync(GALLERY_SECTIONS_DIR, { withFileTypes: true })
    .filter((entry) => entry.isFile() && entry.name.endsWith(".tsx"))
    .map((entry) => entry.name.replace(/\.tsx$/, ""))
    .sort();
}

// This is a completeness guard, not a bugfix - src/widgets/* and
// src/dev/gallery-sections/*.tsx already match 1:1 as of this test's
// writing. Its job is to fail CI the day that stops being true: a future
// widget added without a gallery section, or a section left behind after
// its widget is removed. internal/ is excluded - it's implementation
// machinery (requireClass), not a widget with states to show.
test("every src/widgets/* directory (excluding internal/) has a matching gallery section", () => {
  const widgets = widgetDirNames();
  const sections = gallerySectionNames();

  const missingSections = widgets.filter((name) => !sections.includes(name));
  const orphanSections = sections.filter((name) => !widgets.includes(name));

  expect({ missingSections, orphanSections }).toEqual({ missingSections: [], orphanSections: [] });
});
