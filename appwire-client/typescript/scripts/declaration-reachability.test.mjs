// reachableModules decides whether `make test-api-package` accepts a shipped
// module as reachable from a published specifier. The shapes worth pinning are
// the ones a text search of the entry's declarations cannot see: a re-export
// that resolves relative to a nested barrel, and a barrel behind a barrel.
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import { expect, test } from "vitest";
import { reachableModules } from "./declaration-reachability.mjs";

// Runs `check` against a temporary dist tree holding `files`, then removes it.
function withDistTree(files, check) {
  const dist = mkdtempSync(path.join(os.tmpdir(), "evener-declaration-reachability-"));
  try {
    for (const [relative, contents] of Object.entries(files)) {
      const full = path.join(dist, relative);
      mkdirSync(path.dirname(full), { recursive: true });
      writeFileSync(full, contents);
    }
    check(dist);
  } finally {
    rmSync(dist, { recursive: true, force: true });
  }
}

const NESTED_BARREL = {
  "index.d.ts": [
    'export { alpha } from "./alpha";',
    'export type * from "./types.gen";',
    'export * from "./state/navigation";',
    'import type { Hidden } from "./hidden";',
    "export declare const shown: Hidden;",
    "",
  ].join("\n"),
  "alpha.d.ts": "export declare function alpha(): void;\n",
  "types.gen.d.ts": "export interface Thread {\n  id: string;\n}\n",
  "hidden.d.ts": "export type Hidden = number;\n",
  "state/navigation/index.d.ts": ['export * from "./codec";', 'export { applyDelta } from "./merge";', ""].join("\n"),
  "state/navigation/codec.d.ts": "export declare function decode(): void;\n",
  "state/navigation/merge.d.ts": "export declare function applyDelta(): void;\n",
  "orphan.d.ts": "export declare const orphan: 1;\n",
};

test("a re-export is resolved relative to the declaration that carries it, through nested barrels", () => {
  withDistTree(NESTED_BARREL, (dist) => {
    // The root barrel names "./state/navigation", a directory whose index
    // re-exports "./codec" and "./merge": both spellings are relative to the
    // barrel that carries them, and neither appears in the root's own text.
    // hidden is only imported for its types and orphan is named by nobody, so
    // neither is reachable.
    expect([...reachableModules([path.join(dist, "index.d.ts")], dist)].sort()).toEqual(
      [
        "alpha",
        "index",
        "state/navigation/codec",
        "state/navigation/index",
        "state/navigation/merge",
        "types.gen",
      ].sort(),
    );
  });
});

test("a published subpath's own entry is reachable even when no other entry re-exports it", () => {
  withDistTree({ ...NESTED_BARREL, "index.d.ts": 'export { alpha } from "./alpha";\n' }, (dist) => {
    const entries = [path.join(dist, "index.d.ts"), path.join(dist, "state/navigation/index.d.ts")];
    expect([...reachableModules(entries, dist)].sort()).toEqual(
      ["alpha", "index", "state/navigation/codec", "state/navigation/index", "state/navigation/merge"].sort(),
    );
  });
});

test("dropping a barrel's re-export drops the module it reached", () => {
  withDistTree({ ...NESTED_BARREL, "state/navigation/index.d.ts": 'export * from "./codec";\n' }, (dist) => {
    const reached = reachableModules([path.join(dist, "index.d.ts")], dist);
    expect(reached.has("state/navigation/codec")).toBe(true);
    expect(reached.has("state/navigation/merge")).toBe(false);
  });
});
