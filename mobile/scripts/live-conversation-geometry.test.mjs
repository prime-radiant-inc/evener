/**
 * RED contract test for the browser geometry matrix runner.
 *
 * Written in Step 4 before `live-conversation-geometry.mjs` exists. The
 * initial `node --test scripts/live-conversation-geometry.test.mjs` fails with
 * `ERR_MODULE_NOT_FOUND` for `./live-conversation-geometry.mjs` — the
 * intentional RED. Step 7 implements the CDP matrix runner and turns this
 * GREEN.
 *
 * The contract: the geometry runner rejects missing matrix axes, duplicate
 * case IDs, geometry JSON without raw-system absence/AX counts/DOM count/
 * anchor values, screenshots outside its output root, a missing
 * representative-HTML renderer, and any origin other than its private loopback
 * Vite port. It must run exactly 1,152 base matrix points plus panned
 * composer → Back → Work → concept-switch traversal rows.
 */
import assert from "node:assert/strict";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

import {
  buildMatrixCases,
  EXPECTED_BASE_MATRIX_POINTS,
  MATRIX_AXES,
  validateGeometryJson,
  validateGuardOrigin,
  validateScreenshotPath,
} from "./live-conversation-geometry.mjs";

const scriptDirectory = path.dirname(fileURLToPath(import.meta.url));

test("MATRIX_AXES declares all eight axes with exact cardinalities", () => {
  assert.ok(MATRIX_AXES, "MATRIX_AXES must be exported");
  assert.strictEqual(MATRIX_AXES.concepts.length, 3, "three concepts");
  assert.strictEqual(MATRIX_AXES.fixtures.length, 2, "two fixtures");
  assert.strictEqual(MATRIX_AXES.viewports.length, 4, "four viewports");
  assert.strictEqual(MATRIX_AXES.typeScales.length, 3, "three type scales");
  assert.strictEqual(MATRIX_AXES.themes.length, 2, "two themes");
  assert.strictEqual(MATRIX_AXES.motionModes.length, 2, "two motion modes");
  assert.strictEqual(MATRIX_AXES.safeAreas.length, 2, "two safe areas");
  assert.strictEqual(MATRIX_AXES.keyboard.length, 2, "keyboard closed/open");
});

test("EXPECTED_BASE_MATRIX_POINTS is exactly 1152", () => {
  assert.strictEqual(
    EXPECTED_BASE_MATRIX_POINTS,
    3 * 2 * 4 * 3 * 2 * 2 * 2 * 2,
    "3×2×4×3×2×2×2×2 = 1152",
  );
  assert.strictEqual(EXPECTED_BASE_MATRIX_POINTS, 1152);
});

test("buildMatrixCases produces exactly 1152 unique case IDs", () => {
  const cases = buildMatrixCases();
  assert.strictEqual(cases.length, EXPECTED_BASE_MATRIX_POINTS);
  const ids = cases.map((c) => c.id);
  const unique = new Set(ids);
  assert.strictEqual(unique.size, ids.length, "duplicate case IDs detected");
});

test("buildMatrixCases rejects when an axis is empty", () => {
  assert.throws(
    () => buildMatrixCases({ concepts: [] }),
    /missing matrix axis/,
  );
  assert.throws(
    () => buildMatrixCases({ fixtures: [] }),
    /missing matrix axis/,
  );
});

test("validateGeometryJson rejects missing raw-system sentinel absence field", () => {
  assert.throws(
    () =>
      validateGeometryJson({
        axCount: 1,
        domCount: 1,
        anchor: { offsetPx: 0 },
      }),
    /raw-system sentinel absence/,
  );
});

test("validateGeometryJson rejects missing AX count", () => {
  assert.throws(
    () =>
      validateGeometryJson({
        rawSystemSentinelAbsent: true,
        domCount: 1,
        anchor: { offsetPx: 0 },
      }),
    /AX count/,
  );
});

test("validateGeometryJson rejects missing DOM count", () => {
  assert.throws(
    () =>
      validateGeometryJson({
        rawSystemSentinelAbsent: true,
        axCount: 1,
        anchor: { offsetPx: 0 },
      }),
    /DOM count/,
  );
});

test("validateGeometryJson rejects missing anchor values", () => {
  assert.throws(
    () =>
      validateGeometryJson({
        rawSystemSentinelAbsent: true,
        axCount: 1,
        domCount: 1,
      }),
    /anchor/,
  );
});

test("validateGeometryJson accepts a complete geometry record", () => {
  assert.doesNotThrow(() =>
    validateGeometryJson({
      rawSystemSentinelAbsent: true,
      axCount: 1,
      domCount: 1,
      anchor: { offsetPx: 0, following: true, threadKey: "k", itemKey: "i" },
    }),
  );
});

test("validateScreenshotPath rejects paths outside the output root", () => {
  const outputRoot = path.join(scriptDirectory, "test-output-root");
  assert.throws(
    () => validateScreenshotPath("/tmp/elsewhere/screenshot.png", outputRoot),
    /output root/,
  );
});

test("validateScreenshotPath accepts paths inside the output root", () => {
  const outputRoot = path.join(scriptDirectory, "test-output-root");
  assert.doesNotThrow(() =>
    validateScreenshotPath(
      path.join(outputRoot, "concept-a", "screenshot.png"),
      outputRoot,
    ),
  );
});

test("validateGuardOrigin rejects any origin other than the private loopback Vite port", () => {
  assert.throws(
    () => validateGuardOrigin("https://localhost:5173"),
    /private loopback Vite port/,
  );
  assert.throws(
    () => validateGuardOrigin("http://0.0.0.0:5173"),
    /private loopback Vite port/,
  );
});

test("validateGuardOrigin accepts the private loopback Vite port origin", () => {
  assert.doesNotThrow(() => validateGuardOrigin("http://127.0.0.1:43117"));
});
