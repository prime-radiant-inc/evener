// The connect-provider dialog must stay a dynamic-only import: it is a lazy
// chunk (panes/spawn/connectDialogChunk.ts) whose below-the-fold loading, stale
// -chunk detection and cache-busted retry all depend on Rollup emitting it as
// its own file. One static import back into the module graph is enough to close
// a cycle that collapses it into another module's chunk - CredentialsSection
// and ConnectProviderDialog importing each other did exactly that - after which
// the chunk path/stylesheet regexes match nothing real and recovery silently
// stops working. The component tests cannot see it: they replace the loader and
// assert recovery against a synthetic ConnectProviderDialog-*.js URL.
import assert from "node:assert/strict";
import { readdirSync, readFileSync } from "node:fs";
import path from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";

const SRC = path.join(path.dirname(fileURLToPath(import.meta.url)), "..", "src");
// A static import of the connector's own module (not the chunk loader, not the
// boundary): `import ... from "…/ConnectProviderDialog"`.
const STATIC_IMPORT = /^\s*import\b[^;]*?from\s+["'][^"']*\/ConnectProviderDialog["']/m;

function sourceFiles(dir) {
  const out = [];
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) out.push(...sourceFiles(full));
    else if (/\.(ts|tsx)$/.test(entry.name) && !/\.test\./.test(entry.name)) out.push(full);
  }
  return out;
}

test("the connect-provider dialog is only ever imported dynamically", () => {
  const offenders = sourceFiles(SRC)
    .filter((file) => STATIC_IMPORT.test(readFileSync(file, "utf8")))
    .map((file) => path.relative(SRC, file));
  assert.deepEqual(
    offenders,
    [],
    `a static ConnectProviderDialog import collapses its lazy chunk (and the stale-chunk recovery with it): ${offenders.join(", ")}`,
  );
});
