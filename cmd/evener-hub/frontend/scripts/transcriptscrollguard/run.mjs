#!/usr/bin/env node
// transcriptscrollguard - drives the REAL Session transcript in headless
// Chrome and verifies the jump-to-latest pill end to end: a dynamic,
// variable-height transcript overflows its scroll container; a real scroll
// away from the bottom reveals the pill via the NATIVE scroll event; large
// turns appended while away leave the virtualizer holding only estimates for
// them; clicking the pill scrolls to the TRUE bottom and the landing's own
// native scroll event clears the pill - held through the virtualizer's
// post-jump measurement corrections. Settled at the bottom, the transcript
// must then re-anchor to the true bottom when its content grows (#917) and
// when its scroll port shrinks under it (the pane header growing).
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
  evaluate,
  navigateTo,
  waitForFonts,
  waitForHttp,
} from "../browserGuardCdp.mjs";
import { describeBrowserStartupFailure, startBrowserGuard, waitForBrowserReady } from "../browserGuardProcess.mjs";

const FRONTEND = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..", "..");

// A desktop window. The scripted transcript (42 turns, many of them tall,
// plus 3 appended turns of ~a viewport each) overflows it several times
// over - the exact condition the bug needed.
const VIEWPORT = { width: 1400, height: 900 };

// Bottom-gap tolerance: scrollTop/scrollHeight are subject to sub-pixel
// rounding, so "at the true bottom" is pinned within 1px, mirroring the
// tolerance the fix's own jsdom suite uses for the same assertion.
const BOTTOM_TOLERANCE_PX = 1;

// The unbooted-page seam (see navigateTo in browserGuardCdp.mjs): a network
// change can kill the dev-server module burst mid-boot while the page still
// fires its load event. transcriptscrollguard.html boots through one entry
// module, src/dev/transcriptscrollguard-entry.tsx, which assigns
// window.waitForTranscriptSettled at module scope - a page whose load event
// fired without that global never booted.
const BOOT = {
  bootExpression: "typeof window.waitForTranscriptSettled !== 'undefined'",
  bootLabel: "the transcriptscrollguard entry global window.waitForTranscriptSettled",
};

// The open-with-history contract every transcript surface holds when a read
// answers with a page plus an olderCursor: the paging row mounted, the page
// actually overflowing its port (a collapsed pane would otherwise settle
// trivially at a 0px bottom gap and read as a pass), no older page auto-fetched
// on open, no jump pill at open, and the landing held at the true bottom across
// the settle. `paneFooter` is the surface's own DOM marker - the live Session
// renders its pane-footer/SessionChrome slot, the read-only pane renders none -
// so a pass that meant to exercise one surface while the other rendered is
// caught here instead of passing tautologically.
function assertPagedOpenContract(failures, label, m, { paneFooter = true } = {}) {
  if (m.errors.length > 0) failures.push(`page errors on ${label} paged open: ${m.errors.join("; ")}`);
  if (m.clientHeight <= 0) {
    failures.push(
      `${label} opened with no scroll-port height (clientHeight ${m.clientHeight}) - the pane did not render`,
    );
  } else if (m.scrollHeight <= m.overflowRequired) {
    failures.push(
      `${label} opened without overflowing its port (scrollHeight ${m.scrollHeight}, clientHeight ${m.clientHeight}, ` +
        `needs more than ${m.overflowRequired}) - the open contract was not exercised`,
    );
  }
  if (!m.pagingRow) failures.push(`${label} opened without the paging row mounted (no olderCursor?)`);
  if (m.listCalls !== 0) {
    failures.push(
      `opening ${label} with older history auto-loaded ${m.listCalls} older page(s); the automatic paging trigger ` +
        "must wait until the reader approaches the top of history",
    );
  }
  if (m.pill) failures.push(`${label} shows the jump pill right after opening a session with older history`);
  if (m.paneFooter !== paneFooter) {
    failures.push(
      `${label} rendered the wrong transcript surface (pane footer present: ${m.paneFooter}, expected ${paneFooter}) ` +
        "- the pass is not exercising the pane it names",
    );
  }
  if (Math.abs(m.bottomGap) > BOTTOM_TOLERANCE_PX) {
    failures.push(
      `${label} opened ${m.bottomGap}px off the true bottom after opening a session with older history ` +
        `(scrollTop ${m.scrollTop}, scrollHeight ${m.scrollHeight}, clientHeight ${m.clientHeight}) - the landing did ` +
        "not hold across the settle",
    );
  }
}

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
    const viteDeadline = createStartupDeadline();
    try {
      await waitForHttp(
        `http://127.0.0.1:${vitePort}/transcriptscrollguard.html`,
        "vite dev server",
        guard.getViteLaunchError,
        { signal: viteDeadline.signal },
      );
    } catch (error) {
      throw new Error(describeBrowserStartupFailure({ error, subsystem: "vite", viteStderr: guard.getViteError() }));
    } finally {
      viteDeadline.clear();
    }
    cdpEndpoint = await waitForBrowserReady(guard);

    const page = await connectPage(cdpEndpoint);
    const { send } = page;
    const failures = [];
    let initial = null;
    let landed = null;
    try {
      await applyViewport(send, VIEWPORT);
      await navigateTo(page, `http://127.0.0.1:${vitePort}/transcriptscrollguard.html?paged=1&deferRead=1`, BOOT);
      // Fonts FIRST, then settle: a late-arriving webfont changes row
      // geometry, so the settle loop must measure post-font geometry -
      // otherwise a font-driven shift could surface the pill before the
      // scroll-away phase and the pill assertions would pass for the wrong
      // reason.
      await waitForFonts(send);
      // An asynchronous cold-cache/read admission can outlive page boot and
      // fonts. Invoke the actual paged wait while hydration is held, prove it
      // remains pending through ordinary frames, then release the read.
      const deferredOpen = JSON.parse(await evaluate(send, `(async () => {
        let finished = false;
        let failure;
        let failed = false;
        const pending = window.waitForPagedOpenSettled().then(
          result => { finished = true; return result; },
          error => { finished = true; failed = true; failure = error; }
        );
        await new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)));
        if (finished) throw failure ?? new Error("paged wait finished before hydration admission");
        if (document.querySelector('[data-testid="transcript-virtual-list"] > div'))
          throw new Error("deferred fixture mounted before its read was released");
        window.releaseTranscriptRead();
        const result = await pending;
        if (failed) throw failure;
        return JSON.stringify(result);
      })()`));
      assertPagedOpenContract(failures, "the deferred cold-read session", deferredOpen);

      await navigateTo(page, `http://127.0.0.1:${vitePort}/transcriptscrollguard.html`, BOOT);
      await waitForFonts(send);
      initial = JSON.parse(
        await evaluate(send, "(async () => JSON.stringify(await window.waitForTranscriptSettled()))()"),
      );

      // Harness sanity FIRST (docs/developing-evener/testing.md's
      // unfalsifiable-fixture trap): the transcript really overflowed by a
      // large margin before anything about the pill is asserted, and no page
      // error ever fired.
      if (initial.errors.length > 0) failures.push(`page errors: ${initial.errors.join("; ")}`);
      if (initial.clientHeight <= 0) {
        failures.push(
          `scroll container has no height (clientHeight ${initial.clientHeight}) - the harness did not render`,
        );
      } else if (initial.scrollHeight <= initial.overflowRequired) {
        failures.push(
          `transcript did not overflow several times over (scrollHeight ${initial.scrollHeight}, ` +
            `clientHeight ${initial.clientHeight}, needs more than ${initial.overflowRequired})`,
        );
      }
      if (initial.turns !== 42) failures.push(`expected 42 scripted turns in the model, found ${initial.turns}`);
      if (initial.pill)
        failures.push(
          `pill is visible at mount (text: ${JSON.stringify(initial.pillText)}) - the session should open at the bottom`,
        );

      if (failures.length === 0) {
        // A REAL scroll away (scrollTop assignment through CDP - the browser
        // dispatches the native scroll event). The pill must appear from
        // that event, not from a forced dispatch.
        const scrolled = JSON.parse(
          await evaluate(send, "(async () => JSON.stringify(await window.scrollAwayAndWaitForPill()))()"),
        );
        if (scrolled.errors.length > 0)
          failures.push(`page errors after scrolling away: ${scrolled.errors.join("; ")}`);
        if (!scrolled.pill) failures.push("pill did not appear after a real scroll away from the bottom");
        if (scrolled.bottomGap <= 4)
          failures.push(`scroll-away did not leave the bottom (bottomGap ${scrolled.bottomGap})`);

        // New large turns arrive while the reader is away (the production
        // shape: estimate-only rows below the fold).
        const appended = JSON.parse(
          await evaluate(send, "(async () => JSON.stringify(await window.appendLargeTurns()))()"),
        );
        if (appended.errors.length > 0)
          failures.push(`page errors after appending turns: ${appended.errors.join("; ")}`);
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

        // #917: content grows after the transcript has settled at the bottom,
        // with no item append and no scroll event (the late-webfont-swap shape
        // - scrollTop pinned while scrollHeight grows). The transcript must
        // re-anchor to the TRUE bottom and stay there, with no pill: a few
        // pixels short is exactly the stranding the issue measured.
        if (failures.length === 0) {
          const grew = JSON.parse(
            await evaluate(send, "(async () => JSON.stringify(await window.growContentAndSettle()))()"),
          );
          if (grew.errors.length > 0)
            failures.push(`page errors after post-mount content growth: ${grew.errors.join("; ")}`);
          if (grew.scrollHeight <= grew.beforeScrollHeight) {
            failures.push(
              `post-mount content growth never grew the transcript (scrollHeight ${grew.scrollHeight} vs ` +
                `${grew.beforeScrollHeight}) - the fixture did not reproduce a late content change`,
            );
          } else if (!grew.settled) {
            failures.push(
              `transcript never re-anchored to the true bottom after post-mount content growth: ` +
                `bottomGap=${grew.bottomGap} scrollTop=${grew.scrollTop} scrollHeight=${grew.scrollHeight} (8s deadline)`,
            );
          } else {
            if (grew.pill) failures.push("pill appeared after content grew while the reader was at the bottom");
            if (Math.abs(grew.bottomGap) > BOTTOM_TOLERANCE_PX) {
              failures.push(
                `stayed ${grew.bottomGap}px off the true bottom after post-mount content growth ` +
                  `(scrollTop ${grew.scrollTop}, scrollHeight ${grew.scrollHeight}, clientHeight ${grew.clientHeight})`,
              );
            }
            if (grew.scrollTop <= grew.beforeScrollTop) {
              failures.push("scrollTop never advanced to follow the grown content");
            }
          }
        }

        // The scroll port shrinks under a reader at the bottom (the pane
        // header's cadence trace appearing grows the header 4px), with no
        // scroll event and no content resize. The transcript must re-anchor
        // to the TRUE bottom: 4px short is inside isAtBottom's rounding
        // tolerance, so nothing else - not the pill, not the end-anchor -
        // would ever recover it.
        if (failures.length === 0) {
          const shrank = JSON.parse(
            await evaluate(send, "(async () => JSON.stringify(await window.shrinkPortAndSettle()))()"),
          );
          if (shrank.errors.length > 0)
            failures.push(`page errors after the scroll port shrank: ${shrank.errors.join("; ")}`);
          if (shrank.clientHeight >= shrank.beforeClientHeight) {
            failures.push(
              `the header growth never shrank the scroll port (clientHeight ${shrank.clientHeight} vs ` +
                `${shrank.beforeClientHeight}) - the fixture did not reproduce a port shrink`,
            );
          } else if (!shrank.settled) {
            failures.push(
              `transcript never re-anchored to the true bottom after its scroll port shrank: ` +
                `pill=${shrank.pill} bottomGap=${shrank.bottomGap} scrollTop=${shrank.scrollTop} ` +
                `scrollHeight=${shrank.scrollHeight} clientHeight=${shrank.clientHeight} (8s deadline)`,
            );
          }
        }
      }

      // The open-with-history shape: a page plus an olderCursor, so the paging
      // row is mounted and can auto-fill. This pass runs whatever the passes
      // above did - a failure there must not hide this regression coverage.
      // Before the fix the row's old sentinel fired on mount, and the prepend
      // stranded the reader a page short of the bottom.
      await navigateTo(page, `http://127.0.0.1:${vitePort}/transcriptscrollguard.html?paged=1`, BOOT);
      await waitForFonts(send);
      const opened = JSON.parse(
        await evaluate(send, "(async () => JSON.stringify(await window.waitForPagedOpenSettled()))()"),
      );
      assertPagedOpenContract(failures, "the live session", opened);
      // Paging must still work: a real scroll to the top drives the near-top
      // trigger and fetches a page. Runs whatever the open assertions found, so
      // a failure there cannot hide this coverage. (One request per tick is
      // useTranscript.test.ts's to prove; a real scroll can legitimately fetch
      // more than one page here, since the helper keeps re-zeroing the port.)
      const scrolled = JSON.parse(
        await evaluate(send, "(async () => JSON.stringify(await window.scrollAwayAndWaitForPill()))()"),
      );
      if (scrolled.errors.length > 0) failures.push(`page errors while paging: ${scrolled.errors.join("; ")}`);
      if (scrolled.listCalls < 1) failures.push("scrolling to the top of a paged session fetched no older page");

      // The READ-ONLY transcript pane runs the SAME scroll coordinator (#2963:
      // it lands on the latest row on open, follows a prepend without stranding
      // the reader, and never auto-loads an older page just because it opened),
      // but the guard only ever rendered the live Session. This pass renders
      // the read-only Transcript and holds it to the same open contract: the
      // paging row mounted, no older page fetched on open, and the landing
      // held at the true bottom across the settle.
      await navigateTo(page, `http://127.0.0.1:${vitePort}/transcriptscrollguard.html?paged=1&readonly=1`, BOOT);
      await waitForFonts(send);
      const readOnlyOpened = JSON.parse(
        await evaluate(send, "(async () => JSON.stringify(await window.waitForPagedOpenSettled()))()"),
      );
      assertPagedOpenContract(failures, "the read-only transcript pane", readOnlyOpened, { paneFooter: false });
      await navigateTo(page, `http://127.0.0.1:${vitePort}/transcriptscrollguard.html?dock=1`, BOOT);
      await waitForFonts(send);
      const dock = JSON.parse(await evaluate(send, `(async () => {
        await window.waitForTranscriptSettled();
        const ports = () => [...document.querySelectorAll('[data-testid="transcript-virtual-list"] > div')];
        const root = ports()[0];
        const click = name => [...document.querySelectorAll('button')].find(button => button.textContent === name).click();
        const geometry = port => ({ top: port.scrollTop, height: port.clientHeight, total: port.scrollHeight });
        const visible = port => {
          const viewport = port.getBoundingClientRect();
          return [...port.querySelectorAll('[data-index]')].some(row => {
            const rect = row.getBoundingClientRect();
            return rect.bottom > viewport.top && rect.top < viewport.bottom;
          });
        };
        root.scrollTop = Math.max(0, root.scrollHeight - root.clientHeight - 700);
        await window.waitForTranscriptSettled();
        click('Open observer'); await window.waitForTranscriptSettled();
        click('Open grandchild'); await window.waitForTranscriptSettled();
        const secondary = ports().find(port => port !== root);
        secondary.scrollTop = Math.max(0, secondary.scrollHeight - secondary.clientHeight - 200);
        await window.waitForTranscriptSettled();
        const before = { root: geometry(root), secondary: geometry(secondary), visible: visible(root) && visible(secondary) };
        click('Focus root'); await window.waitForTranscriptSettled();
        const after = { root: geometry(root), secondary: geometry(secondary), visible: visible(root) && visible(secondary), connected: root.isConnected && secondary.isConnected };
        click('Focus observer'); await window.waitForTranscriptSettled();
        const observerSelected = [...document.querySelectorAll('.dv-tab.dv-active-tab')].some(tab => tab.textContent.includes('local:observer'));
        return JSON.stringify({ before, after, observerSelected, switchedRoot: geometry(root), switchedRootVisible: visible(root) });
      })()`));
      console.log("dock focus geometry", dock);
      if (!dock.before.visible || !dock.after.visible || !dock.after.connected) failures.push("Dock group focus stranded mounted transcript content");
      for (const pane of ["root", "secondary"]) {
        if (Math.abs(dock.before[pane].top - dock.after[pane].top) > 1 || dock.before[pane].height !== dock.after[pane].height) failures.push(`Dock group focus changed ${pane} viewport position`);
      }
      if (!dock.observerSelected) failures.push("Dock focus did not select the other retained transcript tab");
      if (!dock.switchedRootVisible || Math.abs(dock.switchedRoot.top - dock.before.root.top) > 1) failures.push("Selecting another secondary tab changed the retained root viewport");

    } finally {
      await clearViewportOverride(send);
      page.close();
    }

    if (failures.length === 0) {
      console.log(
        `transcriptscrollguard ok: transcript ${initial.scrollHeight}px in a ${initial.clientHeight}px scroll port ` +
          `(${initial.turns} turns); pill appeared on a native scroll away; jump settled at the true bottom ` +
          `(bottomGap ${landed.bottomGap}px, pill gone, held ${landed.tail.length} frames); ` +
          `post-mount content growth and a scroll-port shrink both re-anchored to the true bottom; ` +
          `a session opened with older history stayed at the bottom without auto-loading a page; ` +
          `the read-only transcript pane did the same; deferred hydration waited for the mounted paged viewport`,
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
