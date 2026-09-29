// The motion import boundary: the wrapper module (src/motion/index.tsx) is the
// only file allowed to import the motion library, so the library, its
// configuration, and the reduced-motion contract each live in exactly one
// place. Modeled on src/styles/token-contract.test.ts's walker, over the same
// hand-declared node:fs surface (src/styles/node-fs-shim.d.ts).
import { readdirSync, readFileSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, test } from "vitest";

const SRC = dirname(dirname(fileURLToPath(import.meta.url))); // src/motion/.. = src
const WRAPPER = join("motion", "index.tsx");

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
      if (rel === WRAPPER) continue;
      const text = readFileSync(absPath, "utf8");
      if (/from\s+["']motion(\/|["'])/.test(text)) offenders.push(rel);
    }
    expect(offenders).toEqual([]);
  });
});
