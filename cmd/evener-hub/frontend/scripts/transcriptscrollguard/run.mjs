#!/usr/bin/env node
// transcriptscrollguard - drives the REAL Session transcript in headless
// Chrome and verifies the jump-to-latest pill end to end: a dynamic,
// variable-height transcript overflows its scroll container; a real scroll
// away from the bottom reveals the pill via the NATIVE scroll event; large
// turns appended while away leave the virtualizer holding only estimates for
// them; clicking the pill scrolls to the TRUE bottom and the landing's own
// native scroll event clears the pill - held through the virtualizer's
// post-jump measurement corrections.
//
// This is the browser guard for the jump-to-bottom fix (PR #851): the jsdom
// suite covers the hook with stubbed geometry and manually dispatched scroll
// events, which cannot exercise real VirtualList scrolling, dynamic
// measurement, native scroll-event delivery, or post-jump corrections - the
// exact browser failure mode the fix targets (jsdom computes no layout -
// kata tzqz). Deterministic: no hub, no credentials, no shared dev server.
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

// A desktop window. The scripted transcript (42 turns, many of them tall,
// plus 3 appended turns of ~a viewport each) overflows it several times
// over - the exact condition the bug needed.
const VIEWPORT = { width: 1400, height: 900 };

// Bottom-gap tolerance: scrollTop/scrollHeight are subject to sub-pixel
// rounding, so "at the true bottom" is pinned within 1px, mirroring the
// tolerance the fix's own jsdom suite uses for the same assertion.
const BOTTOM_TOLERANCE_PX = 1;

async function main() {
  let guard;
  try {
    guard = await startBrowserGuard({
      frontend: FRONTEND,
      profilePrefix: "transcriptscrollguard-chrome-",
    });
  } catch (error) {
    throw new Error(describeBrowserStartupFailure({ error, subsystem: "launch" }));
  }
  const { vitePort, cleanup } = guard;
  let cdpEndpoint;

  try {
    try {
      await waitForHttp(`http://127.0.0.1:${vitePort}/transcriptscrollguard.html`, "vite dev server", guard.getViteLaunchError);
    } catch (error) {
      throw new Error(
        describeBrowserStartupFailure({ error, subsystem: "vite", viteStderr: guard.getViteError() }),
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
          error,
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

    const page = await connectPage(cdpEndpoint);
    const { send } = page;
    const failures = [];
    let initial = null;
    let landed = null;
    try {
      await applyViewport(send, VIEWPORT);
      await navigateTo(page, `http://127.0.0.1:${vitePort}/transcriptscrollguard.html`);
      initial = JSON.parse(await evaluate(send, "(async () => JSON.stringify(await window.settledTranscript))()"));
      await waitForFonts(send);

      // Harness sanity FIRST (docs/developing-evener/testing.md's
      // unfalsifiable-fixture trap): the transcript really overflowed by a
      // large margin before anything about the pill is asserted, and no page
      // error ever fired.
      if (initial.errors.length > 0) failures.push(`page errors: ${initial.errors.join("; ")}`);
      if (initial.clientHeight <= 0) {
        failures.push(`scroll container has no height (clientHeight ${initial.clientHeight}) - the harness did not render`);
      } else if (initial.scrollHeight <= initial.clientHeight * 4) {
        failures.push(
          `transcript did not overflow several times over (scrollHeight ${initial.scrollHeight}, clientHeight ${initial.clientHeight})`,
        );
      }
      if (initial.turns !== 42) failures.push(`expected 42 scripted turns in the model, found ${initial.turns}`);
      if (initial.pill) failures.push(`pill is visible at mount (text: ${JSON.stringify(initial.pillText)}) - the session should open at the bottom`);

      if (failures.length === 0) {
        // A REAL scroll away (scrollTop assignment through CDP - the browser
        // dispatches the native scroll event). The pill must appear from
        // that event, not from a forced dispatch.
        const scrolled = JSON.parse(await evaluate(send, "(async () => JSON.stringify(await window.scrollAwayAndWaitForPill()))()"));
        if (scrolled.errors.length > 0) failures.push(`page errors after scrolling away: ${scrolled.errors.join("; ")}`);
        if (!scrolled.pill) failures.push("pill did not appear after a real scroll away from the bottom");
        if (scrolled.bottomGap <= 4) failures.push(`scroll-away did not leave the bottom (bottomGap ${scrolled.bottomGap})`);

        // New large turns arrive while the reader is away (the production
        // shape: estimate-only rows below the fold).
        const appended = JSON.parse(await evaluate(send, "(async () => JSON.stringify(await window.appendLargeTurns()))()"));
        if (appended.errors.length > 0) failures.push(`page errors after appending turns: ${appended.errors.join("; ")}`);
        if (appended.turns !== 45) failures.push(`expected 45 turns after the append, found ${appended.turns}`);
        if (!appended.pill) failures.push("pill disappeared when new turns arrived while scrolled away");

        // The behavior under test: click the pill, wait out native scroll
        // events and the virtualizer's post-jump corrections, and the
        // landing must hold at the TRUE bottom with the pill cleared.
        landed = JSON.parse(await evaluate(send, "(async () => JSON.stringify(await window.clickPillAndSettle()))()"));
        if (landed.errors.length > 0) failures.push(`page errors after the jump: ${landed.errors.join("; ")}`);
        if (!landed.settled) {
          const last = landed.tail[landed.tail.length - 1] ?? landed;
          failures.push(
            `jump-to-latest never settled: pill=${last.pill} bottomGap=${last.bottomGap} ` +
              `scrollTop=${last.scrollTop} scrollHeight=${last.scrollHeight} clientHeight=${last.clientHeight} (10s deadline)`,
          );
        } else {
          if (landed.pill) failures.push("pill is still visible after the jump landed at the bottom");
          if (Math.abs(landed.bottomGap) > BOTTOM_TOLERANCE_PX) {
            failures.push(
              `settled ${landed.bottomGap}px above the true bottom (scrollTop ${landed.scrollTop}, ` +
                `scrollHeight ${landed.scrollHeight}, clientHeight ${landed.clientHeight}) - ` +
                `the estimate-derived landing was never corrected to the true bottom`,
            );
          }
          // The settle must HOLD: every frame in the settled tail is at the
          // bottom, so no post-jump measurement correction drifted it short.
          const worst = landed.tail.reduce((max, sample) => Math.max(max, Math.abs(sample.bottomGap)), 0);
          if (worst > BOTTOM_TOLERANCE_PX) {
            failures.push(`the landing drifted off the bottom during the settled tail (worst bottomGap ${worst}px)`);
          }
        }
      }
    } finally {
      await clearViewportOverride(send);
      page.close();
    }

    if (failures.length === 0) {
      console.log(
        `transcriptscrollguard ok: transcript ${initial.scrollHeight}px in a ${initial.clientHeight}px scroll port ` +
          `(${initial.turns} turns); pill appeared on a native scroll away; jump settled at the true bottom ` +
          `(bottomGap ${landed.bottomGap}px, pill gone, held ${landed.tail.length} frames)`,
      );
    } else {
      for (const failure of failures) console.error(`transcriptscrollguard FAIL: ${failure}`);
      process.exitCode = 1;
    }
  } finally {
    await cleanup();
  }
}

main().catch((error) => {
  console.error(error instanceof Error ? error.message : String(error));
  process.exitCode = 1;
});
