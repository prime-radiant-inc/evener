// The motion import boundary: the wrapper module (src/motion/index.tsx) is the
// only file allowed to import the motion library - under EITHER specifier
// ("motion/..." or the legacy "framer-motion/...", which the motion package
// re-exports and which would silently load a second copy of the library) - so
// the library, its configuration, and the reduced-motion contract each live in
// exactly one place. Modeled on src/styles/token-contract.test.ts's walker,
// over the same hand-declared node:fs surface (src/styles/node-fs-shim.d.ts).
import { readdirSync, readFileSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, test } from "vitest";

const SRC = dirname(dirname(fileURLToPath(import.meta.url))); // src/motion/.. = src
// Exact-path exemptions: the wrapper (the one legal importer) and this test
// file, whose pattern self-test samples contain the specifiers as strings.
// A same-named file anywhere else gets neither (the token contract's decoy
// precedent).
const EXEMPT = new Set([join("motion", "index.tsx"), join("motion", "import-boundary.test.ts")]);

// Both specifiers, either quote, any subpath, in every form that binds the
// library: static or re-export ("from"), bare side-effect import, dynamic
// import(), and require(). "import.meta" is none of those (no quote follows).
const MOTION_IMPORT_RE = /\b(?:from|import|require)\s*\(?\s*["'](?:motion|framer-motion)(?:\/[^"']*)?["']/;

function walk(dir: string): string[] {
  const found: string[] = [];
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name);
    if (entry.isDirectory()) found.push(...walk(full));
    else if (entry.isFile() && /\.(ts|tsx)$/.test(entry.name)) found.push(full);
  }
  return found;
}

describe("motion import boundary", () => {
  test("only src/motion/index.tsx imports the motion library", () => {
    const offenders: string[] = [];
    for (const absPath of walk(SRC)) {
      const rel = relative(SRC, absPath);
      if (EXEMPT.has(rel)) continue;
      const text = readFileSync(absPath, "utf8");
      if (MOTION_IMPORT_RE.test(text)) offenders.push(rel);
    }
    expect(offenders).toEqual([]);
  });

  test("the pattern catches both specifiers, subpaths, quote styles, and every binding form", () => {
    const caught = [
      `import { m } from "motion/react"`,
      `import { m } from 'motion'`,
      `import { m } from "framer-motion"`,
      `import { m } from 'framer-motion/m'`,
      `} from "motion/react";`,
      // A re-export binds the library just the same as an import.
      `export { m } from "motion/react";`,
      // Bare side-effect import.
      `import "motion";`,
      // Dynamic import and require bind a second copy of the library too.
      `const m = await import("motion/react");`,
      `const m = await import( 'framer-motion' );`,
      `const { m } = require("motion/react");`,
    ];
    for (const sample of caught) expect(MOTION_IMPORT_RE.test(sample)).toBe(true);
    expect(MOTION_IMPORT_RE.test(`import { x } from "motion-sickness"`)).toBe(false);
    expect(MOTION_IMPORT_RE.test(`import { x } from "./motion"`)).toBe(false);
    // import.meta is not an import of anything.
    expect(MOTION_IMPORT_RE.test(`const here = import.meta.url;`)).toBe(false);
  });
});
