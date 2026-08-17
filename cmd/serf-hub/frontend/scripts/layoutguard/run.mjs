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
// SERVED, NOT file://: the cases run over the guard's own Vite dev server
// (browserGuardProcess.mjs's startBrowserGuard - the same lifecycle
// overflowguard and spawnguard use, one Vite + one Chrome for the whole
// run). The original file:// design copied each case's CSS into a temp dir,
// where global.css's @font-face src ("../../node_modules/...") dangled, so
// every case silently measured host FALLBACK fonts while fixtures calibrated
// against them drifted per machine (the compact-session-footer 1.25px
// boundary failures). Served from the frontend root, that same relative URL
// resolves to /node_modules/... and the real bundled fonts load - which is
// also what production does. Wire plumbing lives in ../browserGuardCdp.mjs,
// shared with the sibling guards.
//
// USAGE:
//   node scripts/layoutguard/run.mjs            # run every case
//   node scripts/layoutguard/run.mjs p6g8-formrow-overlap   # run one case
//
// ADDING A CASE: make a directory under cases/<name>/ with:
//   - case.json    { "cssFiles": [...paths relative to frontend/src] }, plus
//                  an optional "viewport": { "width": 390, "height": 844,
//                  "deviceScaleFactor": 2, "mobile": true } and/or
//                  optional "forcePseudoStates":
//                  [{ "selector": ".x", "pseudoClasses": ["hover"] }] for a
//                  state no page script can reach (see browserGuardCdp.mjs's
//                  forcePseudoStates doc)
//   - harness.html a hand-authored DOM fragment reproducing the real
//                  component's markup/classnames (link tags for tokens.css
//                  and resolved.css - both are generated fresh at run time,
//                  see build() below), defining window.measure() returning a
//                  JSON-serializable measurement.
//   - assert.mjs   default export (measurement) => { pass, reason }
//
// STATUS: this is a local pre-merge check and part of
// `make test-web-browser` in CI; it is not wired into `make lint`. It only
// covers the one case it has been proven against (p6g8) - it is
// not a general guarantee about the other 17 files that share the same
// min-width:0 dependency. Wiring more of those in is the same recipe as
// p6g8-formrow-overlap; deliberately not done here (scope is "prove the
// mechanism", not "cover 18 files" - see kata tzqz).
import { existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import {
  applyViewport,
  assertGuardOrigin,
  clearViewportOverride,
  connectPage,
  evaluate,
  forcePseudoStates,
  navigateTo,
  realizedViewport,
  waitForFonts,
  waitForHttp,
} from "../browserGuardCdp.mjs";
import { describeBrowserStartupFailure, startBrowserGuard } from "../browserGuardProcess.mjs";
import { resolveComposes } from "./resolve-composes.mjs";
import { diagnoseRealizedViewport, normalizeViewportSpec } from "./viewport.mjs";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const CASES_DIR = path.join(__dirname, "cases");
const FRONTEND = path.resolve(__dirname, "..", "..");
const SRC_DIR = path.join(FRONTEND, "src");

// Served per run from beneath the frontend root, at exactly directory depth
// two: a stylesheet at /<run>/<case>/resolved.css resolves global.css's
// "../../node_modules/..." @font-face src to /node_modules/..., which the
// dev server serves statically. Deeper nesting would strand the fonts the
// same way the old temp-dir copy did. The run dir is removed in finally;
// PID-scoped so concurrent runs never collide on a shared checkout.
const GENERATED_ROOT = path.join(FRONTEND, `layoutguard-generated-${process.pid}`);

// kata eevs (docs/testing.md: "a guard that has never failed is a
// decoration"): every case here was mutation-tested once by hand - its
// governing CSS declaration reverted in a scratch copy, confirmed to fail
// the case, then restored. That evidence is recorded in the case's own
// case.json under "mutation" ({ declaration, verified, expect }) rather than
// re-run automatically (a real mutation test needs a human to judge whether
// the resulting FAIL reason is the right one, not just that something
// failed). This just checks the evidence field exists, so a new case can't
// be added silently without it.
function mutationWarning(name, caseJson) {
  const m = caseJson.mutation;
  if (!m || typeof m !== "object")
    return `${name}: no "mutation" evidence in case.json - was this case ever mutation-tested? see docs/testing.md`;
  const missing = ["declaration", "verified", "expect"].filter((k) => !m[k]);
  if (missing.length > 0) return `${name}: "mutation" evidence is missing ${missing.join(", ")}`;
  return null;
}

// Copy the REAL CSS fresh from src/ every run - never a baked-in snapshot,
// so this can't silently go stale against the component it claims to guard.
function buildCaseFiles(name, caseJson) {
  const outDir = path.join(GENERATED_ROOT, name);
  mkdirSync(outDir, { recursive: true });

  writeFileSync(path.join(outDir, "tokens.css"), readFileSync(path.join(SRC_DIR, "styles/tokens.css"), "utf8"));

  const moduleCssFiles = caseJson.cssFiles.filter((p) => p !== "styles/tokens.css");
  const sources = moduleCssFiles.map((rel) => readFileSync(path.join(SRC_DIR, rel), "utf8"));
  writeFileSync(path.join(outDir, "resolved.css"), resolveComposes(sources));

  writeFileSync(path.join(outDir, "harness.html"), readFileSync(path.join(CASES_DIR, name, "harness.html"), "utf8"));
  return outDir;
}

async function runCase(page, vitePort, caseDir, emulation) {
  const name = path.basename(caseDir);
  const caseJson = JSON.parse(readFileSync(path.join(caseDir, "case.json"), "utf8"));
  const viewport = normalizeViewportSpec(caseJson.viewport ?? null, name);
  const { default: assert } = await import(path.join(caseDir, "assert.mjs"));

  buildCaseFiles(name, caseJson);
  const url = `http://127.0.0.1:${vitePort}/${path.basename(GENERATED_ROOT)}/${name}/harness.html`;

  // Clear a PREVIOUS case's viewport override before applying the next one.
  // A clear immediately followed by a set makes the renderer schedule a
  // reload of the next committed page (observed: Page.frameScheduledNavigation
  // reason "reload" right after loadEventFired, which races the case's
  // evaluate and loses with "Inspected target navigated or closed"), so the
  // clear only happens when there is actually an override to clear - a fresh
  // or never-overridden target just applies its override straight over
  // about:blank, the sequence the per-case-Chrome design always used.
  if (emulation.viewportApplied) {
    await navigateTo(page, "about:blank");
    await clearViewportOverride(page.send);
    emulation.viewportApplied = false;
  }
  if (viewport) {
    await applyViewport(page.send, viewport);
    emulation.viewportApplied = true;
  }

  await navigateTo(page, url);
  await assertGuardOrigin(page.send, `127.0.0.1:${vitePort}`);
  await waitForFonts(page.send);

  if (viewport) {
    const realized = await realizedViewport(page.send);
    const diagnostic = diagnoseRealizedViewport(viewport, realized);
    if (diagnostic) throw new Error(diagnostic);
  }

  await forcePseudoStates(page.send, caseJson.forcePseudoStates ?? []);
  const measurement = await evaluate(page.send, "window.measure()");
  const result = await assert(measurement);
  return { name, description: caseJson.description, measurement, ...result };
}

async function main() {
  const requested = process.argv[2];
  if (!existsSync(CASES_DIR)) {
    console.error(`no cases directory at ${CASES_DIR}`);
    return 2;
  }
  const { readdirSync } = await import("node:fs");
  const caseNames = readdirSync(CASES_DIR, { withFileTypes: true })
    .filter((d) => d.isDirectory())
    .map((d) => d.name)
    .filter((n) => !requested || n === requested);

  if (caseNames.length === 0) {
    console.error(requested ? `no case named ${requested} under ${CASES_DIR}` : `no cases found under ${CASES_DIR}`);
    return 2;
  }

  // One Vite dev server + one headless Chrome for the whole run (the old
  // file:// design launched a fresh Chrome PER CASE). Startup failures are
  // environment problems, not test case failures - say so, with the Vite
  // stderr attached, before any case runs.
  const guard = await startBrowserGuard({
    frontend: FRONTEND,
    profilePrefix: "layoutguard-chrome-",
  });
  const { vitePort, cdpPort } = guard;

  let failed = 0;
  const warnings = [];
  try {
    try {
      await waitForHttp(`http://127.0.0.1:${vitePort}/`, "vite dev server");
      await waitForHttp(`http://127.0.0.1:${cdpPort}/json/version`, "chrome devtools endpoint");
    } catch (err) {
      throw new Error(
        describeBrowserStartupFailure({
          error: err,
          chromeBinary: guard.chromeBinary,
          chromeArgv: guard.getChromeArgv(),
          chromeStderr: guard.getChromeError(),
          viteStderr: guard.getViteError(),
        }),
      );
    }

    const page = await connectPage(cdpPort);
    const emulation = { viewportApplied: false };
    if (process.env.LAYOUTGUARD_DEBUG) {
      page.ws.addEventListener("message", (event) => {
        const msg = JSON.parse(event.data);
        if (msg.method) console.error(`[cdp event] ${msg.method} ${JSON.stringify(msg.params ?? {}).slice(0, 200)}`);
      });
    }
    try {
      for (const name of caseNames) {
        const caseDir = path.join(CASES_DIR, name);
        const caseJson = JSON.parse(readFileSync(path.join(caseDir, "case.json"), "utf8"));
        const warning = mutationWarning(name, caseJson);
        if (warning) warnings.push(warning);

        process.stdout.write(`${name} ... `);
        try {
          const result = await runCase(page, vitePort, caseDir, emulation);
          if (result.pass) {
            console.log(`PASS - ${result.reason}`);
          } else {
            failed++;
            console.log(`FAIL - ${result.reason}`);
          }
        } catch (err) {
          failed++;
          console.log(`ERROR - ${err.message}`);
          if (process.env.LAYOUTGUARD_DEBUG) console.error(err.stack);
        }
      }
    } finally {
      page.close();
    }
  } finally {
    await guard.cleanup();
    rmSync(GENERATED_ROOT, { recursive: true, force: true });
  }

  if (warnings.length > 0) {
    console.warn("");
    console.warn("WARN: mutation evidence missing (does not fail the run - see docs/testing.md):");
    for (const w of warnings) console.warn(`  - ${w}`);
  }

  return failed > 0 ? 1 : 0;
}

main().then(
  (status) => {
    if (process.exitCode === undefined) process.exitCode = status;
  },
  (error) => {
    console.error(error.message);
    if (process.exitCode === undefined) process.exitCode = 2;
  },
);
