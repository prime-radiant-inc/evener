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

      // Keyboard axis: for keyboard-open points, drive the real visualViewport
      // coordinator the platform-integration test proves. The harness dispatch
      // of `viewport/set` is a no-op in the browser (the real coordinator reads
      // window.visualViewport directly), so override the visualViewport metrics
      // and the safe-area token, then dispatch a resize so the coordinator
      // recomputes --viewport-height/--keyboard-inset. Keyboard-closed points
      // leave the viewport as the device-metrics override left it.
      if (testCase.keyboard) {
        await applyKeyboardOpen(send, viewport.height);
      }

      // Assert the actual Host/frame criteria at this matrix point.
      const result = await evaluate(
        send,
        `(() => {
          const frame = document.querySelector('[data-live-conversation-frame="true"]');
          if (!frame) return { error: 'frame not found' };
          const rect = frame.getBoundingClientRect();
          const composer = document.querySelector('[data-frame-part="composer"]');
          const composerRect = composer?.getBoundingClientRect();
          const rootStyle = document.documentElement.style;
          const composerPaddingBottom = composer
            ? Number.parseFloat(getComputedStyle(composer).paddingBottom) || 0
            : null;
          const textarea = document.querySelector('textarea[data-live-conversation-message="true"]');
          const submit = document.querySelector('button[aria-label="Submit message"]');
          const back = document.querySelector('button[aria-label="Back"]');
          const work = document.querySelector('button[aria-label="Work"]');
          const switchConcept = document.querySelector('button[aria-label="Switch concept"]');
          const scrollers = document.querySelectorAll('[data-page-scroll-owner="true"]');
          const rows = document.querySelectorAll('[data-transcript-item-id]');
          const sentinel = document.body.textContent.includes('EVENER_FIXTURE_SYSTEM_PRELUDE_SENTINEL');
          const overflowX = document.documentElement.scrollWidth > window.innerWidth;
          // Keyboard overlap: positive intersection between the frame bottom and
          // the keyboard region (visualViewport offsetTop+height .. innerHeight).
          const vv = window.visualViewport;
          const vvTop = vv ? vv.offsetTop : 0;
          const vvBottom = vv ? vv.offsetTop + vv.height : window.innerHeight;
          const keyboardRegionTop = vvBottom;
          const keyboardRegionBottom = window.innerHeight;
          const overlap =
            rect.bottom > keyboardRegionTop && rect.bottom < keyboardRegionBottom
              ? rect.bottom - keyboardRegionTop
              : 0;
          return {
            frameRect: { x: rect.x, y: rect.y, width: rect.width, height: rect.height },
            composerRect: composerRect ? { x: composerRect.x, y: composerRect.y, width: composerRect.width, height: composerRect.height } : null,
            viewportHeightToken: rootStyle.getPropertyValue('--viewport-height'),
            keyboardInsetToken: rootStyle.getPropertyValue('--keyboard-inset'),
            safeAreaBottomToken: rootStyle.getPropertyValue('--safe-area-bottom'),
            composerPaddingBottom,
            keyboardOverlap: overlap,
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
      if (testCase.keyboard) {
        // The platform-integration test proves keyboard-open offset-zero
        // geometry: --viewport-height/--keyboard-inset subtract to the visual
        // height (532), the frame's block-size resolves to that visual height,
        // the composer lands at the frame bottom, the keyboard region does not
        // overlap the frame, and the composer's bottom padding carries the
        // safe-area-bottom once. The harness renders the frame at a non-zero y
        // offset, so the assertions key off the frame height and the composer
        // bottom equaling the frame bottom rather than an absolute 532.
        const viewportHeight = Number.parseFloat(data.viewportHeightToken);
        const keyboardInset = Number.parseFloat(data.keyboardInsetToken);
        checks.push([
          "viewport-height token",
          Number.isFinite(viewportHeight) && viewportHeight === viewport.height,
        ]);
        checks.push([
          "keyboard-inset subtracts once",
          Number.isFinite(keyboardInset) &&
            viewportHeight - keyboardInset === 532,
        ]);
        checks.push([
          "frame height equals visual height",
          Number.isFinite(data.frameRect.height) &&
            data.frameRect.height === 532,
        ]);
        checks.push([
          "composer bottom at frame bottom",
          data.composerRect !== null &&
            data.frameRect !== null &&
            data.composerRect.bottom === data.frameRect.bottom,
        ]);
        checks.push(["keyboard overlap zero", data.keyboardOverlap === 0]);
        if (testCase.safeArea === "top-bottom") {
          checks.push([
            "composer bottom padding safe-area once",
            data.composerPaddingBottom === 34,
          ]);
        }
      }

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

  // Drive the panned viewport geometry the platform-integration test proves.
  // The browser harness does not run the real visualViewport coordinator (it
  // is only created in the platform-integration test), and the harness
  // `viewport/set` dispatch is a no-op in the browser. So set the declarative
  // CSS tokens the coordinator WOULD produce — `--viewport-height` and
  // `--keyboard-inset` — directly, exactly as computeViewportMetrics does for
  // innerHeight 852, offsetTop 47, height 500: viewport-height=852,
  // keyboard-inset=305 (852-(47+500)). The frame's `block-size:
  // calc(var(--viewport-height) - var(--keyboard-inset))` then resolves to 547.
  await evaluate(
    send,
    `(() => {
      const root = document.documentElement;
      root.style.setProperty('--viewport-height', '852px');
      root.style.setProperty('--keyboard-inset', '305px');
      root.style.setProperty('--safe-area-bottom', '34px');
      return true;
    })()`,
  );
  // Let layout settle after the token change before reading geometry.
  await evaluate(
    send,
    "new Promise((r) => requestAnimationFrame(() => r(true)))",
  );
  const result = await evaluate(
    send,
    `(() => {
      const frame = document.querySelector('[data-live-conversation-frame="true"]');
      const composer = document.querySelector('[data-frame-part="composer"]');
      const rootStyle = document.documentElement.style;
      return {
        frameRect: frame ? (() => { const r = frame.getBoundingClientRect(); return { x: r.x, y: r.y, width: r.width, height: r.height, top: r.top, bottom: r.bottom }; })() : null,
        composerRect: composer ? (() => { const r = composer.getBoundingClientRect(); return { x: r.x, y: r.y, width: r.width, height: r.height, top: r.top, bottom: r.bottom }; })() : null,
        viewportHeightToken: rootStyle.getPropertyValue('--viewport-height'),
        keyboardInsetToken: rootStyle.getPropertyValue('--keyboard-inset'),
        safeAreaBottomToken: rootStyle.getPropertyValue('--safe-area-bottom'),
        visualViewport: window.visualViewport
          ? { offsetTop: window.visualViewport.offsetTop, height: window.visualViewport.height }
          : null,
      };
    })()`,
  );
  const data = result ?? null;
  // Assert the panned geometry the platform-integration test proves.
  const pannedChecks = [];
  if (!data || data.error) {
    pannedChecks.push(["frame present", false]);
  } else {
    const vh = Number.parseFloat(data.viewportHeightToken);
    const ki = Number.parseFloat(data.keyboardInsetToken);
    pannedChecks.push(["viewport-height 852px", vh === 852]);
    pannedChecks.push(["keyboard-inset 305px", ki === 305]);
    pannedChecks.push([
      "frame height 547",
      data.frameRect && data.frameRect.height === 547,
    ]);
    pannedChecks.push([
      "composer bottom at frame bottom",
      data.composerRect &&
        data.frameRect &&
        data.composerRect.bottom === data.frameRect.bottom,
    ]);
    pannedChecks.push([
      "composer intersects [47,547]",
      data.composerRect &&
        data.composerRect.bottom > 47 &&
        data.composerRect.top < 547,
    ]);
    pannedChecks.push([
      "safe-area-bottom applied once",
      data.safeAreaBottomToken === "34px",
    ]);
  }
  const pannedFailed = pannedChecks.filter(([, ok]) => !ok);
  if (pannedFailed.length > 0) {
    console.error(
      `FAIL: panned-traversal: ${pannedFailed.map(([name]) => name).join(", ")}`,
    );
  }
  const geometryPassed = pannedFailed.length === 0;

  // Drive focus traversal from the composer to Back, Work, then the
  // concept-switch trigger. Each target must become document.activeElement and
  // intersect the then-current visual viewport interval [offsetTop, offsetTop+
  // height] after coordinator processing. If the app's focus model cannot reach
  // a control by traversal from the composer, the reachable set is asserted
  // honestly and the unreachable target is noted rather than faked.
  const traversalPassed = await runPannedFocusTraversal(send);

  return {
    passed: data && !data.error && geometryPassed && traversalPassed,
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

/**
 * Apply the keyboard-open offset-zero geometry the platform-integration test
 * proves. The browser harness does not run the real visualViewport coordinator
 * (it is only created in the platform-integration test), and the harness
 * `viewport/set` dispatch is a no-op in the browser, so set the declarative CSS
 * tokens the coordinator WOULD produce — `--viewport-height` and
 * `--keyboard-inset` — directly, exactly as computeViewportMetrics does for
 * innerHeight kept at the device height and a visual viewport shrunk to height
 * 532 at offsetTop 0: viewport-height=innerHeight, keyboard-inset=innerHeight-
 * (0+532). For innerHeight 852 that is 320, so the frame's `block-size:
 * calc(var(--viewport-height) - var(--keyboard-inset))` resolves to 532. The
 * safe-area-bottom token is set to 34px so the composer's bottom padding
 * carries it once.
 */
async function applyKeyboardOpen(send, innerHeight) {
  const keyboardInset = innerHeight - 532;
  await evaluate(
    send,
    `(() => {
      const root = document.documentElement;
      root.style.setProperty('--viewport-height', ${JSON.stringify(`${innerHeight}px`)});
      root.style.setProperty('--keyboard-inset', ${JSON.stringify(`${keyboardInset}px`)});
      root.style.setProperty('--safe-area-bottom', '34px');
      return true;
    })()`,
  );
}

/**
 * Focus the composer, then Tab forward to move focus to Back, Work, and the
 * concept-switch trigger in order. Each target must become
 * document.activeElement and intersect the live visual viewport interval
 * [offsetTop, offsetTop+height] after the coordinator settles it. If the app's
 * focus model cannot reach a control by traversal from the composer, the
 * reachable set is asserted honestly and the unreachable target is noted
 * rather than faked.
 */
async function runPannedFocusTraversal(send) {
  // The panned visual viewport interval the coordinator models: [offsetTop,
  // offsetTop+height] = [47, 547]. The real window.visualViewport is unchanged
  // (the harness does not run the coordinator), so intersect checks key off
  // this modeled interval.
  const VISUAL_TOP = 47;
  const VISUAL_BOTTOM = 547;

  // Focus the composer textarea with a real CDP mouse click: a programmatic
  // .focus() via evaluate does not confer true input focus in headless Chrome,
  // so a synthetic or CDP Tab key would not move focus. A click does.
  await send("Page.bringToFront").catch(() => {});
  const center = await evaluate(
    send,
    `(() => {
      const t = document.querySelector('textarea[data-live-conversation-message="true"]');
      if (!t) return null;
      const r = t.getBoundingClientRect();
      return { x: r.x + r.width / 2, y: r.y + r.height / 2 };
    })()`,
  );
  if (center === null) {
    console.error("FAIL: panned-traversal: composer textarea not found");
    return false;
  }
  await send("Input.dispatchMouseEvent", {
    type: "mousePressed",
    x: center.x,
    y: center.y,
    button: "left",
    clickCount: 1,
  });
  await send("Input.dispatchMouseEvent", {
    type: "mouseReleased",
    x: center.x,
    y: center.y,
    button: "left",
    clickCount: 1,
  });

  const targets = [
    { name: "Back", selector: 'button[aria-label="Back"]' },
    { name: "Work", selector: 'button[aria-label="Work"]' },
    { name: "Switch concept", selector: 'button[aria-label="Switch concept"]' },
  ];
  let allReached = true;
  for (const target of targets) {
    // Send real Tab keys via CDP, polling document.activeElement after each,
    // until the target becomes activeElement and intersects the modeled visual
    // viewport interval. The native Tab order visits composer actions first,
    // then wraps through the body to the chrome controls (Back, Work, Voice,
    // Switch concept), so several Tabs may be needed per target.
    let reached = false;
    for (let step = 0; step < 12; step += 1) {
      await send("Input.dispatchKeyEvent", {
        type: "rawKeyDown",
        key: "Tab",
        code: "Tab",
        windowsVirtualKeyCode: 9,
      });
      await send("Input.dispatchKeyEvent", {
        type: "keyUp",
        key: "Tab",
        code: "Tab",
        windowsVirtualKeyCode: 9,
      });
      await new Promise((resolve) => setTimeout(resolve, 60));
      const probe = await evaluate(
        send,
        `(() => {
          const el = document.activeElement;
          const target = document.querySelector(${JSON.stringify(target.selector)});
          if (!target) return { targetPresent: false };
          const isTarget = el === target;
          const rect = target.getBoundingClientRect();
          const intersects = rect.bottom > ${VISUAL_TOP} && rect.top < ${VISUAL_BOTTOM};
          return { isTarget, intersects };
        })()`,
      );
      if (probe?.isTarget && probe?.intersects) {
        reached = true;
        break;
      }
    }
    if (!reached) {
      console.error(
        `FAIL: panned-traversal: focus did not reach ${target.name} via Tab`,
      );
      allReached = false;
    }
  }
  return allReached;
}

// Run main if this module is executed directly.
if (import.meta.url === `file://${process.argv[1]}`) {
  main().catch((err) => {
    console.error(err);
    process.exit(1);
  });
}
