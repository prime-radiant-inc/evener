import assert from "node:assert/strict";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import { assertAllowedModule } from "./check-boundary.mjs";

const repositoryRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const frontendSourceRoot = path.join(repositoryRoot, "cmd/evener-hub/frontend/src");
const allowlist = new Set(["cmd/evener-hub/frontend/src/protocol/types.gen.ts"]);

function frontendModule(relativePath) {
  return path.join(frontendSourceRoot, relativePath);
}

test("permits an allowed protocol module", () => {
  assert.doesNotThrow(() => {
    assertAllowedModule(frontendModule("protocol/types.gen.ts"), repositoryRoot, allowlist);
  });
});

test("rejects a forbidden web shell module with its resolved path", () => {
  const forbidden = frontendModule("shell/AppShell.tsx");

  assert.throws(
    () => assertAllowedModule(forbidden, repositoryRoot, allowlist),
    new RegExp(forbidden.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")),
  );
});

test("rejects a forbidden dynamically loaded web stylesheet with its resolved path", () => {
  const forbidden = frontendModule("panes/session.css");

  assert.throws(
    () => assertAllowedModule(forbidden, repositoryRoot, allowlist),
    new RegExp(forbidden.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")),
  );
});
