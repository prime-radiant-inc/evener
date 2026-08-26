// Static gate: no file under src/ may import from the retired stores/tree
// module, reference the retired /api/tree REST endpoint, or subscribe to the
// retired evener/tree/changed notification. This test reads source files at
// test time and asserts none of the forbidden patterns are present.
//
// The gate is self-excluding: it does not scan itself, and it allows test
// files to mention these patterns in comments only (not in code).

import { readdirSync, readFileSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, test } from "vitest";

const SRC_DIR = join(dirname(fileURLToPath(import.meta.url)), "../..");

function collectSourceFiles(dir: string): string[] {
  const files: string[] = [];
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name);
    if (entry.isDirectory()) {
      files.push(...collectSourceFiles(full));
    } else if (entry.name.endsWith(".ts") || entry.name.endsWith(".tsx")) {
      files.push(full);
    }
  }
  return files;
}

const isTestFile = (path: string): boolean => path.endsWith(".test.ts") || path.endsWith(".test.tsx");
const isThisFile = (path: string): boolean => path.endsWith("noLegacyImports.test.ts");
const isGenerated = (path: string): boolean => path.endsWith("types.gen.ts");

const FORBIDDEN_PATTERNS: { pattern: RegExp; label: string; commentsOK: boolean }[] = [
  // Import from stores/tree — forbidden in any file.
  { pattern: /from\s+["'].*stores\/tree["']/, label: "import from stores/tree", commentsOK: false },
  // Reference to /api/tree in a fetch or URL — forbidden in production files.
  { pattern: /["'`]\/api\/tree["'`]/, label: "reference to /api/tree", commentsOK: true },
  // evener/tree/changed notification subscription — forbidden everywhere.
  { pattern: /evener\/tree\/changed/, label: "evener/tree/changed subscription", commentsOK: true },
  // REFRESH_NOTIFICATIONS reference — forbidden everywhere.
  { pattern: /REFRESH_NOTIFICATIONS/, label: "REFRESH_NOTIFICATIONS reference", commentsOK: true },
];

describe("no legacy tree imports", () => {
  const files = collectSourceFiles(SRC_DIR).filter((f) => !isThisFile(f) && !isGenerated(f));

  for (const file of files) {
    const rel = relative(SRC_DIR, file);
    const content = readFileSync(file, "utf8");
    const lines = content.split("\n");
    const testFile = isTestFile(file);

    test(`${rel} has no legacy tree references`, () => {
      for (const { pattern, label, commentsOK } of FORBIDDEN_PATTERNS) {
        for (let i = 0; i < lines.length; i++) {
          const line = lines[i];
          if (line === undefined) continue;
          if (!pattern.test(line)) continue;
          // In test files, allow the pattern inside comments only.
          if (testFile && commentsOK) {
            const trimmed = line.trim();
            if (trimmed.startsWith("//") || trimmed.startsWith("*") || trimmed.startsWith("/*")) continue;
            // Also allow inline trailing comments: `code // mentions /api/tree`
            const commentIndex = line.indexOf("//");
            if (commentIndex >= 0 && pattern.test(line.slice(commentIndex))) continue;
          }
          expect.fail(`${rel}:${i + 1} forbidden ${label}: ${line.trim()}`);
        }
      }
    });
  }
});
