#!/usr/bin/env node
// spawnguard - checks the real Spawn React tree at the mobile breakpoint and
// scans the rendered page for horizontal overflow.
//
// This is intentionally a browser guard rather than a CSS/source assertion:
// it uses the production Spawn component and actual viewport metrics at 390px,
// 899px, and 900px. It is deterministic because the harness uses FakeClient,
// and it has no dependency on provider credentials or the shared dev server.
import path from "node:path";
import { fileURLToPath } from "node:url";
import { applyViewport, clearViewportOverride, connectPage, evaluate, navigateTo, waitForFonts, waitForHttp } from "../browserGuardCdp.mjs";
import { describeBrowserStartupFailure, startBrowserGuard } from "../browserGuardProcess.mjs";

const FRONTEND = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..", "..");
const WIDTHS = [390, 899, 900];
// The staging cap (attachments/limits.ts MAX_ATTACHMENTS), so the row is
// measured at the widest the product allows it to get.
const STAGED_ATTACHMENTS = 8;
const TILE_PX = 80;

async function measureAt(cdpPort, vitePort, width) {
  const page = await connectPage(cdpPort);
  const { send } = page;
  try {
    await applyViewport(send, { width, height: 900 });
    await navigateTo(page, `http://127.0.0.1:${vitePort}/spawnguard.html`);
    await evaluate(send, "window.settledSpawn");
    // Stage before measuring, at every width: the page is navigated fresh per
    // width, and the staged-attachment row exists only once something is in
    // it. A staging failure has to name itself here rather than surfacing
    // later as an empty row that reads like a layout regression.
    try {
      await evaluate(send, `window.stageSpawnAttachments(${STAGED_ATTACHMENTS})`);
    } catch (error) {
      throw new Error(`staging attachments at ${width}px failed: ${error.message}`);
    }
    // AFTER staging, immediately before measuring. document.fonts.ready is a
    // snapshot, not a standing guarantee: it re-arms whenever a new face starts
    // loading, and a face only loads once something on the page uses it. The
    // staged tiles are the first thing here to use the mono face, so awaiting
    // before staging settles the fonts of a page that has not asked for them
    // yet and measureSpawn still runs mid-swap.
    await waitForFonts(send);
    return JSON.parse(await evaluate(send, "JSON.stringify(window.measureSpawn())"));
  } finally {
    await clearViewportOverride(send);
    page.close();
  }
}

function assertResult(result, expectedWidth) {
  const failures = [];
  const mobile = expectedWidth <= 899;
  // The harness decides what "visible" means, in the page, where the geometry
  // is (spawnguard-entry.tsx's readVisibility). This used to re-derive the
  // verdict from the reported display/visibility and dropped the box-size
  // clauses on the way, so an element under a display:none ANCESTOR - own
  // display intact, box collapsed to zero - read as visible (kata bsq9).
  const visible = (value) => !("error" in value) && value.visible === true;
  // The title spans are a breakpoint SWAP, and the swap is all this can
  // honestly claim about them here. On mobile, PaneScaffold's entire .header
  // row is display:none - the pane title moves into StackHost's top bar, which
  // this harness does not render - so NEITHER span has a box at 390 or 899px.
  // The old ancestor-blind check read the mobile span's own `inline` and called
  // it visible, which is precisely the false green bsq9 is about: it had been
  // asserting a title was on screen inside a header that is switched off.
  // Which span the media query turns on is a real contract; read exactly that.
  const displayed = (value) => !("error" in value) && value.display !== "none";
  if (result.viewport.width !== expectedWidth)
    failures.push(`viewport is ${result.viewport.width}px, expected ${expectedWidth}px`);
  if (visible(result.mobileConfig) !== mobile) failures.push(`mobile config visibility is wrong at ${expectedWidth}px`);
  if (visible(result.desktopConfig) === mobile)
    failures.push(`desktop config visibility is wrong at ${expectedWidth}px`);
  if (displayed(result.mobileTitle) !== mobile)
    failures.push(`mobile title is not the span the ${expectedWidth}px breakpoint selects`);
  if (displayed(result.desktopTitle) === mobile)
    failures.push(`desktop title is not the span the ${expectedWidth}px breakpoint selects`);
  if (visible(result.mobileIntro) !== mobile)
    failures.push(`prompt orientation visibility is wrong at ${expectedWidth}px`);

  if (mobile) {
    const action = result.actionBand;
    if (action.position !== "fixed") failures.push(`action band position is ${action.position}, expected fixed`);
    if (Math.abs(action.left) > 1 || Math.abs(action.bottom) > 1 || Math.abs(action.right - expectedWidth) > 1) {
      failures.push(`action band is not viewport-pinned: ${JSON.stringify(action)}`);
    }
    if (action.width < expectedWidth - 1 || Number.parseFloat(action.minHeight) < 76 || action.height < 76) {
      failures.push(`action band geometry is too small: ${JSON.stringify(action)}`);
    }
    if (result.rows.length !== 6) failures.push(`expected 6 mobile setting rows, found ${result.rows.length}`);
    for (const row of result.rows) {
      if (row.minHeight !== "48px" || row.height < 48)
        failures.push(`row ${row.label} is below 48px: ${JSON.stringify(row)}`);
    }
    const prompt = result.accessiblePrompt;
    if (
      prompt.headingTag !== "h3" ||
      prompt.headingText !== "What should the agent do?" ||
      !prompt.headingVisible ||
      prompt.subtitleTag !== "p" ||
      prompt.subtitleText !== "Leave blank to start a dormant session." ||
      !prompt.subtitleVisible ||
      prompt.headingHiddenFromAT ||
      prompt.subtitleHiddenFromAT
    ) {
      failures.push(`prompt orientation is not persistently accessible: ${JSON.stringify(prompt)}`);
    }
  } else {
    if (result.actionBand.position === "fixed") failures.push("desktop action band unexpectedly remains fixed");
  }

  // Staged attachments (kata 289v). The harness stages them through the
  // pane's own file picker before this runs, so a zero count here means the
  // row never entered the measured tree - the whole point of the case.
  const staged = result.attachments;
  if (staged.tiles.length !== STAGED_ATTACHMENTS) {
    failures.push(`expected ${STAGED_ATTACHMENTS} staged attachment tiles in the tree, found ${staged.tiles.length}`);
  }
  if (staged.row === null) {
    failures.push("staged-attachment row is not in the measured tree");
  } else if (staged.row.right > expectedWidth + 1 || staged.row.left < -1) {
    failures.push(`staged-attachment row escapes the viewport: ${JSON.stringify(staged.row)}`);
  }
  for (const [index, tile] of staged.tiles.entries()) {
    if (Math.abs(tile.width - TILE_PX) > 0.5 || Math.abs(tile.height - TILE_PX) > 0.5) {
      failures.push(`attachment tile ${index} is ${tile.width}x${tile.height}, expected ${TILE_PX}x${TILE_PX}`);
    }
    if (tile.right > expectedWidth + 1 || tile.left < -1) {
      failures.push(`attachment tile ${index} escapes the viewport: ${JSON.stringify(tile)}`);
    }
    if (staged.row !== null && (tile.right > staged.row.right + 1 || tile.left < staged.row.left - 1)) {
      failures.push(
        `attachment tile ${index} escapes its own row: ${JSON.stringify(tile)} vs ${JSON.stringify(staged.row)}`,
      );
    }
    // Redundant with the staging wait, which already blocks on this exact
    // condition - it cannot fail while that wait is in place, and it is here
    // for the harness change that drops the wait. Not independent coverage.
    if (!tile.decoded) failures.push(`attachment tile ${index} never decoded its thumbnail`);
  }
  // Fixed-size boxes in a flex-wrap row: where the row is too narrow to hold
  // every tile side by side (ignoring gaps, which only make it narrower), a
  // single line means the row is overflowing rather than wrapping. Read off
  // the MEASURED row width rather than the viewport, so this says nothing at
  // a width where one line is the right answer.
  const tooNarrowForOneLine = staged.row !== null && staged.row.width < staged.tiles.length * TILE_PX;
  if (tooNarrowForOneLine && staged.rowCount < 2) {
    failures.push(
      `${staged.tiles.length} tiles sit on one line inside a ${staged.row.width}px row instead of wrapping`,
    );
  }

  if (result.overflow.length > 0) failures.push(`horizontal overflow: ${result.overflow.join("; ")}`);
  return failures;
}

async function main() {
  let guard;
  try {
    guard = await startBrowserGuard({
      frontend: FRONTEND,
      profilePrefix: "spawnguard-chrome-",
    });
  } catch (error) {
    // findChrome() throws from the first statement of startBrowserGuard,
    // before any of its state exists -- 'no Chrome installed' is the
    // commonest environment failure there is and it reached here unframed.
    throw new Error(describeBrowserStartupFailure({ error, subsystem: "launch" }));
  }
  const { vitePort, cdpPort, cleanup } = guard;

  let failed = 0;
  try {
    try {
      await waitForHttp(`http://127.0.0.1:${vitePort}/spawnguard.html`, "vite dev server", guard.getViteLaunchError);
    } catch (error) {
      throw new Error(
        describeBrowserStartupFailure({ error: error, subsystem: "vite", viteStderr: guard.getViteError() }),
      );
    }
    try {
      await waitForHttp(
        `http://127.0.0.1:${cdpPort}/json/version`,
        "chrome devtools endpoint",
        guard.getChromeLaunchError,
      );
    } catch (error) {
      throw new Error(
        describeBrowserStartupFailure({
          error: error,
          subsystem: "chrome",
          chromeBinary: guard.chromeBinary,
          chromeArgv: guard.getChromeArgv(),
          chromeStderr: guard.getChromeError(),
          viteStderr: guard.getViteError(),
        }),
      );
    }
    for (const width of WIDTHS) {
      const result = await measureAt(cdpPort, vitePort, width);
      const failures = assertResult(result, width);
      if (failures.length === 0) {
        console.log(
          `${width}px ... PASS - Spawn breakpoint, action band, rows, accessibility, ${STAGED_ATTACHMENTS} staged attachment tiles, and overflow`,
        );
      } else {
        failed++;
        console.log(`${width}px ... FAIL`);
        for (const failure of failures) console.log(`    ${failure}`);
      }
    }
  } finally {
    // A rejecting teardown (escaped Chrome helper) must not turn a run with
    // zero failing cases into a red exit — warn and let the verdict stand.
    await cleanup().catch((cleanupError) => {
      console.error(`warning: browser cleanup error: ${cleanupError.message}`);
    });
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
