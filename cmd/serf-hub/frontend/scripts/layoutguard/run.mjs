#!/usr/bin/env node
// layoutguard - a runnable geometric regression check for CSS-cascade layout
// bugs that jsdom cannot see (jsdom computes no cascade; getBoundingClientRect
// always reports zero there - see kata tzqz).
//
// WHY THIS EXISTS: kata p6g8 (a form row overflowing 77.8px into its
// neighbour column) was fixed with one CSS property (min-width:0 on
// formrow.module.css's .root). Its own regression test survives the mutation:
// delete that property and the whole vitest suite stays green, because no
// jsdom test can observe the layout it governs. 18 files rely on the same
// property for the same reason. This runs a REAL browser (headless Chrome,
// its own throwaway profile+port - never the shared MCP Chrome, see kata
// 8ecz) against the REAL tokens.css + component .module.css files, and
// asserts a geometric relationship (box containment / non-overlap) with
// getBoundingClientRect - not a pixel snapshot, which is brittle across
// fonts/platforms and produces diffs nobody trusts.
//
// USAGE:
//   node scripts/layoutguard/run.mjs            # run every case
//   node scripts/layoutguard/run.mjs p6g8-formrow-overlap   # run one case
//
// ADDING A CASE: make a directory under cases/<name>/ with:
//   - case.json    { "cssFiles": [...paths relative to frontend/src] }
//   - harness.html a hand-authored DOM fragment reproducing the real
//                  component's markup/classnames (link tags for tokens.css
//                  and resolved.css - both are generated fresh at run time,
//                  see build() below), defining window.measure() returning a
//                  JSON-serializable measurement.
//   - assert.mjs   default export (measurement) => { pass, reason }
//
// STATUS: this is a manual pre-merge check, NOT wired into `make lint` or
// CI. It only covers the one case it has been proven against (p6g8) - it is
// not a general guarantee about the other 17 files that share the same
// min-width:0 dependency. Wiring more of those in is the same recipe as
// p6g8-formrow-overlap; deliberately not done here (scope is "prove the
// mechanism", not "cover 18 files" - see kata tzqz).
import { existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { evalInFreshChrome } from "./cdp.mjs";
import { resolveComposes } from "./resolve-composes.mjs";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const CASES_DIR = path.join(__dirname, "cases");
const SRC_DIR = path.join(__dirname, "..", "..", "src");

async function runCase(caseDir) {
  const name = path.basename(caseDir);
  const caseJson = JSON.parse(readFileSync(path.join(caseDir, "case.json"), "utf8"));
  const { default: assert } = await import(path.join(caseDir, "assert.mjs"));

  const workDir = mkdtempSync(path.join(tmpdir(), `layoutguard-${name}-`));
  try {
    // Copy the REAL CSS fresh from src/ every run - never a baked-in
    // snapshot, so this can't silently go stale against the component it
    // claims to guard.
    const tokensPath = path.join(SRC_DIR, "styles/tokens.css");
    writeFileSync(path.join(workDir, "tokens.css"), readFileSync(tokensPath, "utf8"));

    const moduleCssFiles = caseJson.cssFiles.filter((p) => p !== "styles/tokens.css");
    const sources = moduleCssFiles.map((rel) => readFileSync(path.join(SRC_DIR, rel), "utf8"));
    writeFileSync(path.join(workDir, "resolved.css"), resolveComposes(sources));

    const harnessSrc = readFileSync(path.join(caseDir, "harness.html"), "utf8");
    const harnessPath = path.join(workDir, "harness.html");
    writeFileSync(harnessPath, harnessSrc);

    const measurement = await evalInFreshChrome(`file://${harnessPath}`, "window.measure()");
    const result = assert(measurement);
    return { name, description: caseJson.description, measurement, ...result };
  } finally {
    rmSync(workDir, { recursive: true, force: true });
  }
}

async function main() {
  const requested = process.argv[2];
  if (!existsSync(CASES_DIR)) {
    console.error(`no cases directory at ${CASES_DIR}`);
    process.exit(2);
  }
  const { readdirSync } = await import("node:fs");
  const caseNames = readdirSync(CASES_DIR, { withFileTypes: true })
    .filter((d) => d.isDirectory())
    .map((d) => d.name)
    .filter((n) => !requested || n === requested);

  if (caseNames.length === 0) {
    console.error(requested ? `no case named ${requested} under ${CASES_DIR}` : `no cases found under ${CASES_DIR}`);
    process.exit(2);
  }

  let failed = 0;
  for (const name of caseNames) {
    const caseDir = path.join(CASES_DIR, name);
    process.stdout.write(`${name} ... `);
    try {
      const result = await runCase(caseDir);
      if (result.pass) {
        console.log(`PASS - ${result.reason}`);
      } else {
        failed++;
        console.log(`FAIL - ${result.reason}`);
      }
    } catch (err) {
      failed++;
      console.log(`ERROR - ${err.message}`);
    }
  }

  process.exit(failed > 0 ? 1 : 0);
}

main();
