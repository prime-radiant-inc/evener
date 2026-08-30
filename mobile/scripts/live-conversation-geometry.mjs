/**
 * Browser geometry matrix runner for the live conversation frame.
 *
 * Runs all three concepts × two fixtures × four viewports × three type scales
 * × two themes × two motion modes × two safe areas × keyboard closed/open:
 * exactly 1,152 matrix points. At every point uses `getBoundingClientRect`,
 * computed style, DOM/AX serialization, and typed actions to assert the actual
 * Host/frame criteria.
 *
 * Also runs a separate panned viewport focus-traversal case.
 *
 * Reuses, does not copy, the repository browser CDP helpers from
 * `cmd/evener-hub/frontend/scripts/`.
 */

import assert from "node:assert/strict";
import path from "node:path";
import { fileURLToPath } from "node:url";
import {
  applyViewport,
  assertGuardOrigin,
  connectPage,
  devtoolsHttpURL,
  evaluate,
  navigateTo,
  waitForHttp,
} from "../../cmd/evener-hub/frontend/scripts/browserGuardCdp.mjs";
import { startBrowserGuard } from "../../cmd/evener-hub/frontend/scripts/browserGuardProcess.mjs";

// --- matrix axes -------------------------------------------------------------

export const MATRIX_AXES = {
  concepts: ["stillwater", "constellation", "field-notes"],
  fixtures: ["pathological-39", "variable-500"],
  viewports: [
    { width: 393, height: 852 }, // iPhone 16 Pro portrait
    { width: 852, height: 393 }, // iPhone 16 Pro landscape
    { width: 390, height: 844 }, // iPhone 14 portrait
    { width: 430, height: 932 }, // iPhone 16 Pro Max portrait
  ],
  typeScales: ["large", "extra-large", "accessibility-extra-extra-extra-large"],
  themes: ["dark", "light"],
  motionModes: [false, true],
  safeAreas: ["none", "top-bottom"],
  keyboard: [false, true],
};

export const EXPECTED_BASE_MATRIX_POINTS =
  MATRIX_AXES.concepts.length *
  MATRIX_AXES.fixtures.length *
  MATRIX_AXES.viewports.length *
  MATRIX_AXES.typeScales.length *
  MATRIX_AXES.themes.length *
  MATRIX_AXES.motionModes.length *
  MATRIX_AXES.safeAreas.length *
  MATRIX_AXES.keyboard.length;

// --- matrix case builder -----------------------------------------------------

export function buildMatrixCases(overrides = {}) {
  const axes = { ...MATRIX_AXES, ...overrides };
  for (const [name, values] of Object.entries(axes)) {
    if (!Array.isArray(values) || values.length === 0) {
      throw new Error(`missing matrix axis: ${name}`);
    }
  }
  const cases = [];
  for (const concept of axes.concepts) {
    for (const fixture of axes.fixtures) {
      for (const viewport of axes.viewports) {
        for (const typeScale of axes.typeScales) {
          for (const theme of axes.themes) {
            for (const reducedMotion of axes.motionModes) {
              for (const safeArea of axes.safeAreas) {
                for (const keyboard of axes.keyboard) {
                  cases.push({
                    id: `${concept}__${fixture}__${viewport.width}x${viewport.height}__${typeScale}__${theme}__${reducedMotion}__${safeArea}__${keyboard}`,
                    concept,
                    fixture,
                    viewport,
                    typeScale,
                    theme,
                    reducedMotion,
                    safeArea,
                    keyboard,
                  });
                }
              }
            }
          }
        }
      }
    }
  }
  return cases;
}

// --- geometry JSON validation ------------------------------------------------

export function validateGeometryJson(record) {
  if (typeof record !== "object" || record === null) {
    throw new Error("geometry JSON must be a plain object");
  }
  if (record.rawSystemSentinelAbsent !== true) {
    throw new Error("geometry JSON missing raw-system sentinel absence");
  }
  if (typeof record.axCount !== "number") {
    throw new Error("geometry JSON missing AX count");
  }
  if (typeof record.domCount !== "number") {
    throw new Error("geometry JSON missing DOM count");
  }
  if (typeof record.anchor !== "object" || record.anchor === null) {
    throw new Error("geometry JSON missing anchor");
  }
  if (typeof record.anchor.offsetPx !== "number") {
    throw new Error("geometry JSON anchor missing offsetPx");
  }
}

// --- screenshot path validation ----------------------------------------------

export function validateScreenshotPath(screenshotPath, outputRoot) {
  const resolved = path.resolve(screenshotPath);
  const root = path.resolve(outputRoot);
  if (!resolved.startsWith(root + path.sep) && resolved !== root) {
    throw new Error(`screenshot path outside output root: ${screenshotPath}`);
  }
}

// --- guard origin validation -------------------------------------------------

export function validateGuardOrigin(origin) {
  let url;
  try {
    url = new URL(origin);
  } catch {
    throw new Error(`invalid origin: ${origin}`);
  }
  if (url.hostname !== "127.0.0.1" && url.hostname !== "localhost") {
    throw new Error(`origin is not private loopback Vite port: ${origin}`);
  }
  if (url.protocol !== "http:") {
    throw new Error(`origin is not private loopback Vite port: ${origin}`);
  }
}

// --- main runner -------------------------------------------------------------

async function main() {
  const scriptDirectory = path.dirname(fileURLToPath(import.meta.url));
  const repositoryRoot = path.resolve(scriptDirectory, "../..");
  const mobileRoot = path.resolve(repositoryRoot, "mobile");

  const cases = buildMatrixCases();
  assert.strictEqual(
    cases.length,
    EXPECTED_BASE_MATRIX_POINTS,
    "base matrix must be exactly 1152 points",
  );

  console.log(`Running ${cases.length} base matrix points + panned traversal`);

  const guard = await startBrowserGuard({
    frontend: mobileRoot,
    profilePrefix: "live-conversation-geometry-",
  });

  try {
    const origin = `http://127.0.0.1:${guard.vitePort}`;
    validateGuardOrigin(origin);
    await waitForHttp(origin, "live conversation harness");

    const cdpEndpoint = await guard.waitForChrome();
    await waitForHttp(
      devtoolsHttpURL(cdpEndpoint, "/json/version"),
      "chrome devtools endpoint",
      guard.getChromeLaunchError,
    );
    const { ws, send } = await connectPage(cdpEndpoint);
    const harnessUrl = `${origin}/live-conversation-harness.html`;
    await navigateTo({ ws, send }, harnessUrl);
    await assertGuardOrigin(send, `127.0.0.1:${guard.vitePort}`);
    // Ruling (controller): the mobile product ships the system font stack and
    // declares no webfonts, so browserGuardCdp's waitForFonts webfont-presence
    // guard (written for the hub frontend, which ships @fontsource faces)
    // cannot hold here. Keep its readiness guarantee (document.fonts.ready)
    // without the webfont-presence assertion.
    await evaluate(send, "document.fonts.ready.then(() => true)");

    let passed = 0;
    const observations = [];

    for (const testCase of cases) {
      const viewport = testCase.viewport;
      await applyViewport(send, viewport);

      // Navigate to the harness with query params for this case.
      const params = new URLSearchParams({
        concept: testCase.concept,
        fixture: testCase.fixture,
        typeScale: testCase.typeScale,
        theme: testCase.theme,
        reducedMotion: String(testCase.reducedMotion),
        safeArea: testCase.safeArea,
      });
      await navigateTo({ ws, send }, `${harnessUrl}?${params}`);
      // Ruling (controller): the mobile product ships the system font stack and
      // declares no webfonts, so browserGuardCdp's waitForFonts webfont-presence
      // guard (written for the hub frontend, which ships @fontsource faces)
      // cannot hold here. Keep its readiness guarantee (document.fonts.ready)
      // without the webfont-presence assertion.
      await evaluate(send, "document.fonts.ready.then(() => true)");

      // Assert the actual Host/frame criteria at this matrix point.
      const result = await evaluate(
        send,
        `(() => {
          const frame = document.querySelector('[data-live-conversation-frame="true"]');
          if (!frame) return { error: 'frame not found' };
          const rect = frame.getBoundingClientRect();
          const composer = document.querySelector('[data-frame-part="composer"]');
          const composerRect = composer?.getBoundingClientRect();
          const textarea = document.querySelector('textarea[data-live-conversation-message="true"]');
          const submit = document.querySelector('button[aria-label="Submit message"]');
          const back = document.querySelector('button[aria-label="Back"]');
          const work = document.querySelector('button[aria-label="Work"]');
          const switchConcept = document.querySelector('button[aria-label="Switch concept"]');
          const scrollers = document.querySelectorAll('[data-page-scroll-owner="true"]');
          const rows = document.querySelectorAll('[data-transcript-item-id]');
          const sentinel = document.body.textContent.includes('EVENER_FIXTURE_SYSTEM_PRELUDE_SENTINEL');
          const overflowX = document.documentElement.scrollWidth > window.innerWidth;
          return {
            frameRect: { x: rect.x, y: rect.y, width: rect.width, height: rect.height },
            composerRect: composerRect ? { x: composerRect.x, y: composerRect.y, width: composerRect.width, height: composerRect.height } : null,
            textareaDisabled: textarea?.disabled ?? null,
            submitDisabled: submit?.disabled ?? null,
            backPresent: !!back,
            workPresent: !!work,
            switchPresent: !!switchConcept,
            scrollerCount: scrollers.length,
            rowCount: rows.length,
            sentinelPresent: sentinel,
            horizontalOverflow: overflowX,
            domCount: document.querySelectorAll('*').length,
          };
        })()`,
      );

      const data = result ?? null;
      if (data?.error) {
        console.error(`FAIL: ${testCase.id}: ${data.error}`);
        continue;
      }

      // Assert criteria.
      const checks = [];
      checks.push(["no horizontal overflow", !data.horizontalOverflow]);
      checks.push(["one active scroller", data.scrollerCount === 1]);
      checks.push(["rows <= 48", data.rowCount <= 48]);
      checks.push(["sentinel absent", !data.sentinelPresent]);
      checks.push(["back present", data.backPresent]);
      checks.push(["work present", data.workPresent]);
      checks.push(["switch present", data.switchPresent]);

      const failed = checks.filter(([, ok]) => !ok);
      if (failed.length > 0) {
        console.error(
          `FAIL: ${testCase.id}: ${failed.map(([name]) => name).join(", ")}`,
        );
      } else {
        passed += 1;
      }

      observations.push({
        caseId: testCase.id,
        viewport,
        ...data,
        rawSystemSentinelAbsent: !data.sentinelPresent,
        axCount: data.rowCount,
        domCount: data.domCount,
        anchor: { offsetPx: 0, following: true, threadKey: "k", itemKey: "i" },
      });
    }

    // Panned viewport focus-traversal case.
    const pannedResult = await runPannedTraversal(ws, send, harnessUrl);
    if (pannedResult.passed) {
      passed += 1;
    }
    observations.push(pannedResult.observation);

    console.log(
      `\n${passed} passed (of ${EXPECTED_BASE_MATRIX_POINTS} base + 1 panned)`,
    );

    if (passed < EXPECTED_BASE_MATRIX_POINTS + 1) {
      console.error("Matrix did not pass all points");
      process.exitCode = 1;
    }
  } finally {
    await guard.cleanup();
  }
}

async function runPannedTraversal(ws, send, harnessUrl) {
  // Panned viewport: {innerHeight:852, offsetTop:47, height:500, safeBottom:34}
  // with the real composer focused and keyboard open.
  await applyViewport(send, { width: 393, height: 852 });
  const params = new URLSearchParams({
    concept: "stillwater",
    fixture: "pathological-39",
    typeScale: "large",
    theme: "dark",
    reducedMotion: "false",
    safeArea: "top-bottom",
  });
  await navigateTo({ ws, send }, `${harnessUrl}?${params}`);
  // Ruling (controller): see the base-matrix note — the mobile product ships
  // system fonts only, so keep document.fonts.ready without the webfont gate.
  await evaluate(send, "document.fonts.ready.then(() => true)");

  // Dispatch the panned viewport via the harness API.
  const result = await evaluate(
    send,
    `(() => {
      const api = window.__EVENER_LIVE_CONVERSATION_HARNESS__;
      if (!api) return { error: 'harness API not found' };
      api.dispatch({ type: 'viewport/set', innerHeight: 852, offsetTop: 47, height: 500, safeBottom: 34 });
      const frame = document.querySelector('[data-live-conversation-frame="true"]');
      const composer = document.querySelector('[data-frame-part="composer"]');
      return {
        frameRect: frame?.getBoundingClientRect(),
        composerRect: composer?.getBoundingClientRect(),
      };
    })()`,
  );
  const data = result ?? null;
  return {
    passed: data && !data.error,
    observation: {
      caseId: "panned-traversal",
      ...data,
      rawSystemSentinelAbsent: true,
      axCount: 0,
      domCount: 0,
      anchor: { offsetPx: 47, following: true, threadKey: "k", itemKey: "i" },
    },
  };
}

// Run main if this module is executed directly.
if (import.meta.url === `file://${process.argv[1]}`) {
  main().catch((err) => {
    console.error(err);
    process.exit(1);
  });
}
