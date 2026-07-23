// @vitest-environment node
import { readdirSync, readFileSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";
import { expect, test } from "vitest";

// popOutPane (paneActions.ts) is DORMANT: dockview-native popout needs a
// same-origin http(s) blank shell the server serves, and serf-hub serves none
// (the SPA fallback would boot a second full app; an about:blank override is
// rejected by dockview 7.0.2's assertSameOriginPopoutUrl). Adding that shell is
// a Go route reserved for Jesse - see paneActions.ts's popOutPane header for the
// full evidence. So no UI affordance may WIRE popout yet: a "Pop out" button or
// menu item calling popOutPane() would open the broken window this dormancy
// exists to prevent.
//
// This guard reads every application source file straight off disk (bypassing
// vite, the same node:fs technique as requireclass-contract.test.ts) and asserts
// popOutPane has zero call sites outside its own definition and this stream's
// unit tests. It fails loudly the moment anyone renders a popout affordance,
// forcing them back to this decision (serve the shell, or keep it dormant)
// rather than silently shipping a window that boots a second app.
//
// Mutation net: adding `popOutPane("x")` to any non-test file under src/ (e.g. a
// pane header button) makes the call-site list non-empty and this test fail.
// (Verified during the wave-8 fix round by temporarily adding a call site.)
//
// The call form is matched as `popOutPane(` with no space before the paren -
// biome normalizes every real call to exactly that, so this catches all of them
// while skipping prose mentions like workspace.ts's "popOutPane (dockview ...)".
const SRC_ROOT = dirname(dirname(fileURLToPath(import.meta.url))); // src/shell/.. = src

// The one file allowed to name popOutPane in a call-shaped way is its own
// definition; its unit tests legitimately invoke it to prove the dormant
// mechanics. Everything else is application code that must not wire it.
const DEFINITION = join(SRC_ROOT, "shell", "paneActions.ts");

function walkSourceFiles(dir: string, found: string[] = []): string[] {
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name);
    if (entry.isDirectory()) {
      walkSourceFiles(full, found);
    } else if (
      entry.isFile() &&
      /\.tsx?$/.test(entry.name) &&
      !/\.test\.tsx?$/.test(entry.name) &&
      full !== DEFINITION
    ) {
      found.push(full);
    }
  }
  return found;
}

test("no application code wires popOutPane (popout stays dormant until a served shell exists)", () => {
  const callSites = walkSourceFiles(SRC_ROOT)
    .filter((file) => /\bpopOutPane\(/.test(readFileSync(file, "utf8")))
    .map((file) => relative(SRC_ROOT, file));

  expect(callSites).toEqual([]);
});
