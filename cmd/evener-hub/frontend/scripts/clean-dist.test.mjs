// Pins clean-dist.mjs's contract: the script npm run build invokes before its
// fallible steps, so a failed typecheck cannot leave the previous build's
// dist behind for go:embed to silently pick up (the stale-SPA failure mode
// that previously required a manual `rm -rf dist`).
//
// The cases run the REAL script against a fixture frontend tree laid out
// like the real one (scripts/ next to dist/), because the failure this pins
// is about what actually lands on disk — a fake cleaner would only restate
// the mock.

import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { test } from "node:test";

import { fileURLToPath } from "node:url";

const scriptsDir = path.dirname(fileURLToPath(import.meta.url));
const scriptPath = path.join(scriptsDir, "clean-dist.mjs");

// newFixtureFrontend builds a throwaway tree shaped like the real frontend
// (scripts/clean-dist.mjs with dist/ one level up), so the script under test
// cleans ITS tree, never the real checkout's dist.
function newFixtureFrontend() {
  const root = mkdtempSync(path.join(tmpdir(), "evener-clean-dist-"));
  const fixtureScripts = path.join(root, "scripts");
  mkdirSync(fixtureScripts);
  const realScript = readFileSync(scriptPath, "utf8");
  writeFileSync(path.join(fixtureScripts, "clean-dist.mjs"), realScript);
  return { root, script: path.join(fixtureScripts, "clean-dist.mjs") };
}

test("removes a previous build's dist down to the tracked PLACEHOLDER", () => {
  const { root, script } = newFixtureFrontend();
  try {
    const dist = path.join(root, "dist");
    mkdirSync(path.join(dist, "webassets"), { recursive: true });
    writeFileSync(path.join(dist, "webassets", "index-STALE.js"), "stale bundle");
    writeFileSync(path.join(dist, "webassets", "nested"), "nested dir entry");
    mkdirSync(path.join(dist, "webassets", "deep"), { recursive: true });
    writeFileSync(path.join(dist, "webassets", "deep", "chunk.js"), "stale chunk");
    writeFileSync(path.join(dist, "index.html"), "stale shell");

    execFileSync(process.execPath, [script], { cwd: root });

    assert.equal(existsSync(path.join(dist, "webassets")), false, "stale webassets survived");
    assert.equal(existsSync(path.join(dist, "index.html")), false, "stale index.html survived");
    assert.equal(
      readFileSync(path.join(dist, "PLACEHOLDER"), "utf8"),
      "run make build-web\n",
      "PLACEHOLDER content must stay byte-identical to the tracked file",
    );
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("creates the tracked PLACEHOLDER when dist is absent", () => {
  const { root, script } = newFixtureFrontend();
  try {
    execFileSync(process.execPath, [script], { cwd: root });

    assert.equal(
      readFileSync(path.join(root, "dist", "PLACEHOLDER"), "utf8"),
      "run make build-web\n",
      "a missing dist must end with only the PLACEHOLDER so go:embed compiles",
    );
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("cleans only its own tree, ignoring the working directory", () => {
  const { root, script } = newFixtureFrontend();
  try {
    const otherDist = path.join(root, "elsewhere", "dist");
    mkdirSync(otherDist, { recursive: true });
    writeFileSync(path.join(otherDist, "index.html"), "unrelated");

    execFileSync(process.execPath, [script], { cwd: tmpdir() });

    assert.equal(
      readFileSync(path.join(otherDist, "index.html"), "utf8"),
      "unrelated",
      "cleaned a dist resolved from the working directory instead of the script location",
    );
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});
