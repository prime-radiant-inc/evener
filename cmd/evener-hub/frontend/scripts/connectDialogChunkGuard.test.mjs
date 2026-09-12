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
// A static value reference to the connector's own module (not the chunk
// loader, not the boundary): a named/default import (`import ... from
// "…/ConnectProviderDialog"`, possibly spanning lines), a side-effect import
// (`import "…/ConnectProviderDialog"`), or a re-export (`export ... from
// "…/ConnectProviderDialog"`). Type-only forms (`import type`, `export type`)
// are erased before the bundler sees them and must not be flagged. A specifier
// list that is inline-type-only (`import { type X } from …`) is still flagged;
// that is rare, and erring toward reporting is the safe direction.
const STATIC_IMPORT =
  /^[ \t]*(?:import\s+(?!type\s)[\s\S]*?\sfrom\s+|import\s+(?!type\s)|export\s+(?!type\s)[\s\S]*?\sfrom\s+)["'][^"']*\/ConnectProviderDialog["']/m;

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

// The real-tree scan above only fires if someone writes the offense; these
// samples pin what the matcher must recognize. A side-effect import or a
// re-export collapses the lazy chunk exactly like a named import, while an
// erased `import type`/`export type` never reaches the bundle (TypeScript's
// own emit strips it), so flagging one would be a false positive that teaches
// readers to distrust this guard.
test("the static-reference matcher covers value references and skips erased types", () => {
  const flagged = (source) => STATIC_IMPORT.test(source);

  assert.equal(flagged('import { ConnectProviderDialog } from "../x/ConnectProviderDialog";'), true);
  assert.equal(flagged('import ConnectProviderDialog from "../x/ConnectProviderDialog";'), true);
  assert.equal(flagged('import {\n  ConnectProviderDialog,\n} from "../x/ConnectProviderDialog";'), true);
  assert.equal(flagged('import "../x/ConnectProviderDialog";'), true);
  assert.equal(flagged('export { ConnectProviderDialog } from "../x/ConnectProviderDialog";'), true);
  assert.equal(flagged('export * from "../x/ConnectProviderDialog";'), true);

  assert.equal(flagged('import type { ConnectProviderDialogProps } from "../x/ConnectProviderDialog";'), false);
  assert.equal(flagged('export type { ConnectProviderDialogProps } from "../x/ConnectProviderDialog";'), false);
  assert.equal(flagged('const load = () => import("../x/ConnectProviderDialog");'), false);
  assert.equal(flagged('import { other } from "../x/somethingElse";'), false);
});
