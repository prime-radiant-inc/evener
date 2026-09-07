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
import {
  applyViewport,
  clearViewportOverride,
  connectPage,
  createStartupDeadline,
  devtoolsHttpURL,
  evaluate,
  navigateTo,
  waitForFonts,
  waitForHttp,
} from "../browserGuardCdp.mjs";
import { describeBrowserStartupFailure, startBrowserGuard } from "../browserGuardProcess.mjs";

const FRONTEND = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..", "..");
const WIDTHS = [320, 390, 899, 900, 1440];
// The staging cap (attachments/limits.ts MAX_ATTACHMENTS), so the row is
// measured at the widest the product allows it to get.
const STAGED_ATTACHMENTS = 8;
const TILE_PX = 80;
// tokens.css's --tap-min, the platform touch floor the widgets' own phone
// blocks apply (2026-07-30-mobile-session-layout-design.md, decision 4).
const TAP_MIN_PX = 44;

// Sub-pixel layout rounding, not a fudge factor - the same 1px slack every
// other geometric comparison in this file already allows.
function contains(parent, child) {
  return (
    child.left >= parent.left - 1 &&
    child.top >= parent.top - 1 &&
    child.right <= parent.right + 1 &&
    child.bottom <= parent.bottom + 1
  );
}

function describeBox(box) {
  if (box === null) return "missing";
  return `${box.left.toFixed(1)},${box.top.toFixed(1)} ${box.width.toFixed(1)}x${box.height.toFixed(1)}`;
}

async function measureAt(cdpEndpoint, vitePort, width) {
  const page = await connectPage(cdpEndpoint);
  const { send } = page;
  try {
    await applyViewport(send, { width, height: 900 });
    // Focus handlers require a focused document even in a background headless tab.
    await send("Emulation.setFocusEmulationEnabled", { enabled: true });
    await navigateTo(page, `http://127.0.0.1:${vitePort}/spawnguard.html`);
    await evaluate(send, "window.settledSpawn");
    const fieldFailures = await evaluate(send, "window.exerciseDirectoryField()");
    if (fieldFailures.length) throw new Error(`Shared directory field at ${width}px: ${fieldFailures.join("; ")}`);
    const directoryFailures = await evaluate(send, "window.exerciseDirectoryPicker()");
    if (directoryFailures.length) throw new Error(`Directory picker at ${width}px: ${directoryFailures.join("; ")}`);
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
    // Pick the harness's long-id model through the real picker before
    // measuring: the card assertions below verify the trigger ellipsizes it
    // inside the row instead of pushing effort/Start out.
    try {
      await evaluate(send, "window.selectLongSpawnModel()");
    } catch (error) {
      throw new Error(`selecting the long model at ${width}px failed: ${error.message}`);
    }
    await evaluate(send, "window.openSpawnPlugins(); new Promise((resolve) => requestAnimationFrame(resolve))");
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
  // The prompt heading and subtitle show at EVERY width now (the desktop
  // pane used to hide them behind a 12px uppercase title - critique R7), so
  // the intro must be visible whether or not the layout is the phone's.
  if (!visible(result.promptIntro)) failures.push(`prompt intro is not visible at ${expectedWidth}px`);
  // Issue #198: the prompt card is the composer, so its control row holds the
  // composer's controls in the composer's place - at EVERY width, which is why
  // this block is outside the mobile branch. The pane used to pass PromptCard a
  // class that spawn.module.css turned into `position: fixed; bottom: 0` inside
  // its 899px block: the row left the card and became a page-level band with the
  // attach button stranded at the foot of the screen, nowhere near the prompt.
  const card = result.promptCard;
  if (card.card === null || card.controls === null) {
    failures.push(`the prompt card or its control row is missing: ${JSON.stringify(card)}`);
  } else {
    if (card.controls.position === "fixed") {
      failures.push(`the control row is position: fixed at ${expectedWidth}px - a viewport band, not the card's row`);
    }
    for (const [name, box] of [
      ["control row", card.controls],
      ["attach button", card.attach],
      ["Start button", card.submit],
    ]) {
      if (box === null) {
        failures.push(`the ${name} is not in the measured tree`);
      } else if (!contains(card.card, box)) {
        failures.push(`the ${name} (${describeBox(box)}) is outside the prompt card (${describeBox(card.card)})`);
      }
    }
    // The attach button belongs in the row BENEATH the writing surface, not
    // overlaid on its corner where typed text runs under it - the placement
    // PR #242 tried and this pane rejected.
    if (card.attach !== null && card.field !== null && card.attach.top < card.field.bottom - 1) {
      failures.push(
        `the attach button (${describeBox(card.attach)}) overlaps the prompt field (${describeBox(card.field)})`,
      );
    }
    // The card's model trigger and effort control are the setting surface at
    // EVERY width now (composer unification): no breakpoint switches them, so
    // the slot stays visible wherever the card is.
    if (!visible(card.modelSlot)) {
      failures.push(`the card's model slot is not visible at ${expectedWidth}px`);
    }
    if (card.modelTrigger === null) {
      failures.push(`the card's model trigger is not in the measured tree at ${expectedWidth}px`);
    } else if (!contains(card.card, card.modelTrigger)) {
      failures.push(
        `the card's model trigger (${describeBox(card.modelTrigger)}) is outside the prompt card (${describeBox(card.card)})`,
      );
    }
    // Long-model case (selectLongSpawnModel above): the ~100-char qualified
    // id must stay inside the card at every width. Where the card itself is
    // narrower than the id (the 320/390 panes - at 899 the form goes full
    // width so the id genuinely fits), the value must ellipsize
    // (scrollWidth past clientWidth) rather than push effort/Start out.
    if (card.modelValue === null) {
      failures.push(`the card's model value is not in the measured tree at ${expectedWidth}px`);
    } else {
      if (!contains(card.card, card.modelValue)) {
        failures.push(
          `the card's model value (${describeBox(card.modelValue)}) is outside the prompt card (${describeBox(card.card)})`,
        );
      }
      if (expectedWidth <= 390 && card.modelValue.scrollWidth <= card.modelValue.clientWidth + 1) {
        failures.push(
          `the long model id is not ellipsizing at ${expectedWidth}px (scroll ${card.modelValue.scrollWidth}px vs client ${card.modelValue.clientWidth}px) - the fixture may not have applied`,
        );
      }
    }
    if (card.effort === null) {
      failures.push(`the card's effort control is not in the measured tree at ${expectedWidth}px`);
    } else if (!contains(card.card, card.effort)) {
      failures.push(
        `the card's effort control (${describeBox(card.effort)}) is outside the prompt card (${describeBox(card.card)})`,
      );
    }
  }

  if (mobile) {
    // The tap floor the deleted action band used to hard-code now comes from
    // the widgets' own phone blocks (button.module.css / iconbutton.module.css,
    // both keyed to --tap-min). Measured, not assumed: that is the whole reason
    // deleting the band was safe.
    for (const [name, box] of [
      ["attach button", card.attach],
      ["Start button", card.submit],
      ["effort control", card.effort],
    ]) {
      if (box !== null && box.height < TAP_MIN_PX - 0.5) {
        failures.push(`the ${name} is ${box.height}px tall, below the ${TAP_MIN_PX}px touch floor`);
      }
    }
    if (card.attach !== null && card.attach.width < TAP_MIN_PX - 0.5) {
      failures.push(`the attach button is ${card.attach.width}px wide, below the ${TAP_MIN_PX}px touch floor`);
    }
    // Model AND effort live in the prompt card (composer unification);
    // Plugins is the fifth row.
    if (result.rows.length !== 5) failures.push(`expected 5 mobile setting rows, found ${result.rows.length}`);
    if (result.rows.some((row) => row.label === "Model" || row.label === "Reasoning effort")) {
      failures.push(
        "the mobile setting rows still carry a Model/Reasoning effort row - the prompt card owns those now",
      );
    }
    for (const row of result.rows) {
      if (row.minHeight !== "48px" || row.height < 48)
        failures.push(`row ${row.label} is below 48px: ${JSON.stringify(row)}`);
    }
  }

  // Staged attachments (kata 289v). The harness stages them through the
  // pane's own file picker before this runs, so a zero count here means the
  // row never entered the measured tree - the whole point of the case.
  const staged = result.attachments;
  if (staged.tiles.length !== STAGED_ATTACHMENTS) {
    failures.push(`expected ${STAGED_ATTACHMENTS} staged attachment tiles in the tree, found ${staged.tiles.length}`);
  }
  // Persistently accessible at every width, not only the phone's: the heading
  // is the page's own (an h2 under the pane title), never aria-hidden.
  const prompt = result.accessiblePrompt;
  if (
    prompt.headingTag !== "h2" ||
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

  const pluginSurface = mobile ? result.plugins.row : result.plugins.summary;
  if (pluginSurface === null || pluginSurface.width <= 1 || pluginSurface.height <= 1) {
    failures.push(`plugin ${mobile ? "row" : "summary"} is not visible at ${expectedWidth}px`);
  }
  if (mobile && result.plugins.row !== null && result.plugins.row.height < TAP_MIN_PX - 0.5) {
    failures.push(`plugin row is ${result.plugins.row.height}px tall, below the ${TAP_MIN_PX}px touch floor`);
  }
  if (mobile && result.plugins.sheet === null) {
    failures.push("plugin sheet did not open on the phone surface");
  } else if (mobile && result.plugins.sheet !== null) {
    if (result.plugins.sheet.width > expectedWidth + 1 || result.plugins.sheet.left < -1) {
      failures.push(`plugin sheet escapes the viewport: ${JSON.stringify(result.plugins.sheet)}`);
    }
    if (result.plugins.sheet.height < 120) failures.push(`plugin sheet is too short to be usable: ${JSON.stringify(result.plugins.sheet)}`);
  }
  // The panel owns no filter and no scroll container: rows render the source
  // subheading, counts and description under each name, and the list grows to
  // fit them.
  if (result.plugins.metadata === null || result.plugins.metadata.width <= 1 || result.plugins.metadata.height <= 1) {
    failures.push(`plugin row metadata (source/counts/description) is not measurable at ${expectedWidth}px`);
  }
  if (result.plugins.listOverflowY !== "visible") {
    failures.push(
      `plugin list is a scroll container (overflow-y: ${result.plugins.listOverflowY}) at ${expectedWidth}px - it should expand to fit`,
    );
  }
  if (result.plugins.switches.length === 0) {
    failures.push(`plugin switches are not measurable at ${expectedWidth}px`);
  } else if (mobile) {
    for (const [index, control] of result.plugins.switches.entries()) {
      if (control.width < TAP_MIN_PX - 0.5 || control.height < TAP_MIN_PX - 0.5) {
        failures.push(`plugin switch ${index} is ${control.width}x${control.height}, below the ${TAP_MIN_PX}px touch floor`);
      }
    }
  } else {
    for (const [index, control] of result.plugins.switches.entries()) {
      if (Math.abs(control.width - 32) > 1 || Math.abs(control.height - 18) > 1) {
        failures.push(`desktop plugin switch ${index} changed dimensions: ${JSON.stringify(control)}`);
      }
    }
  }
  if (pluginSurface !== null && result.plugins.start !== null && pluginSurface.top < result.plugins.start.bottom - 1) {
    failures.push(`plugin surface overlaps the prompt Start action: ${JSON.stringify({ pluginSurface, start: result.plugins.start })}`);
  }
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
  const { vitePort, cleanup } = guard;
  let cdpEndpoint;

  let failed = 0;
  try {
    try {
      await waitForHttp(`http://127.0.0.1:${vitePort}/spawnguard.html`, "vite dev server", guard.getViteLaunchError);
    } catch (error) {
      throw new Error(
        describeBrowserStartupFailure({ error: error, subsystem: "vite", viteStderr: guard.getViteError() }),
      );
    }
    const startupDeadline = createStartupDeadline();
    try {
      cdpEndpoint = await guard.waitForChrome({ signal: startupDeadline.signal });
      await waitForHttp(
        devtoolsHttpURL(cdpEndpoint, "/json/version"),
        "chrome devtools endpoint",
        guard.getChromeLaunchError,
        { signal: startupDeadline.signal, failure: guard.getChromeFailure() },
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
    } finally {
      startupDeadline.clear();
    }
    for (const width of WIDTHS) {
      const result = await measureAt(cdpEndpoint, vitePort, width);
      const failures = assertResult(result, width);
      if (failures.length === 0) {
        console.log(
          `${width}px ... PASS - Spawn directory picker, breakpoint, in-card control row, rows, accessibility, ${STAGED_ATTACHMENTS} staged attachment tiles, and overflow`,
        );
      } else {
        failed++;
        console.log(`${width}px ... FAIL`);
        for (const failure of failures) console.log(`    ${failure}`);
      }
    }
  } finally {
    // A rejecting teardown is a FAILING RUN, not a warning: cleanup only
    // rejects when it has given up on an escaped Chrome helper, which means
    // this run left a live process and its private profile directory behind on
    // the machine. That leak (roughly 1 run in 3) is issue #119; until it is
    // fixed, going red is the signal that keeps it visible, and downgrading it
    // to a warning would only make the guard quietly lossy.
    await cleanup();
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
