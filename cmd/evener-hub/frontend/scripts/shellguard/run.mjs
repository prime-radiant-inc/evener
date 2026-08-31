#!/usr/bin/env node
// shellguard - checks the real desktop AppShell (rail + workspace) at a
// desktop viewport and asserts the PAGE never grows taller than the viewport
// when the sidebar tree does: the rail's own .body is the scroll container,
// the document is not.
//
// This is intentionally a browser guard rather than a CSS/source assertion:
// it renders the production AppShell against a FakeClient with a scripted
// tall tree (src/dev/shellguard-entry.tsx) and measures real geometry, which
// is the only place a flex/overflow height chain actually exists - jsdom
// computes no cascade (kata tzqz). Deterministic: no hub, no credentials, no
// shared dev server.
import path from "node:path";
import { fileURLToPath } from "node:url";
import {
  applyViewport,
  clearViewportOverride,
  connectPage,
  evaluate,
  createStartupDeadline,
  devtoolsHttpURL,
  navigateTo,
  waitForFonts,
  waitForHttp,
} from "../browserGuardCdp.mjs";
import { describeBrowserStartupFailure, startBrowserGuard } from "../browserGuardProcess.mjs";

const FRONTEND = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..", "..");

// A desktop window, tall enough to be a real screen and short enough that
// the scripted tree (12 projects x 10 sessions) overflows it several times
// over - the exact condition the bug needed.
const VIEWPORT = { width: 1400, height: 900 };
const MOBILE_VIEWPORT = { width: 390, height: 844 };

async function measure(cdpEndpoint, vitePort) {
  const page = await connectPage(cdpEndpoint);
  const { send } = page;
  try {
    await applyViewport(send, VIEWPORT);
    await navigateTo(page, `http://127.0.0.1:${vitePort}/shellguard.html`);
    await evaluate(send, "window.settledShell");
    await waitForFonts(send);
    return JSON.parse(await evaluate(send, "JSON.stringify(window.measureShell())"));
  } finally {
    await clearViewportOverride(send);
    page.close();
  }
}

async function measureMobileSidebar(cdpEndpoint, vitePort) {
  const page = await connectPage(cdpEndpoint);
  const { send } = page;
  try {
    await applyViewport(send, MOBILE_VIEWPORT);
    await navigateTo(page, `http://127.0.0.1:${vitePort}/shellguard.html`);
    await evaluate(send, "window.settledShell");
    await waitForFonts(send);
    return JSON.parse(await evaluate(send, "JSON.stringify(window.measureMobileSidebar())"));
  } finally {
    await clearViewportOverride(send);
    page.close();
  }
}

function assertResult(result) {
  const failures = [];
  if (result.errors.length > 0) failures.push(`page errors: ${result.errors.join("; ")}`);
  if (result.viewport.width !== VIEWPORT.width || result.viewport.height !== VIEWPORT.height) {
    failures.push(
      `viewport is ${result.viewport.width}x${result.viewport.height}, expected ${VIEWPORT.width}x${VIEWPORT.height}`,
    );
  }

  // Harness sanity: the tall tree really rendered, so "no page overflow" is a
  // measurement of a loaded shell, not of an empty one that never drew the
  // rail (docs/developing-evener/testing.md's unfalsifiable-fixture trap).
  if (result.treeRows < 120) failures.push(`expected the tall tree in the page, found ${result.treeRows} rows`);

  // The property the bug broke: with a tree taller than the viewport, the
  // RAIL's own body must be the box that scrolls...
  if (result.railBody === null) {
    failures.push("the rail's scroll body is not in the measured tree");
  } else if (result.railBody.scrollHeight <= result.railBody.clientHeight + 1) {
    failures.push(
      `the rail body is not scrolling its content (scrollHeight ${result.railBody.scrollHeight}, clientHeight ${result.railBody.clientHeight})`,
    );
  }

  // ...and the DOCUMENT must not. The whole page scrolling is the bug: dead
  // space below the shell exactly the rail tree's overflow tall.
  if (result.document.scrollHeight > result.viewport.height + 1) {
    failures.push(
      `the document is ${result.document.scrollHeight}px tall in a ${result.viewport.height}px viewport - the sidebar's height is setting the page's height`,
    );
  }
  if (result.leaks.length > 0) {
    failures.push(
      `elements escape the viewport's bottom edge: ${result.leaks.map((l) => `${l.selector} bottom=${l.bottom.toFixed(1)}`).join("; ")}`,
    );
  }
  return failures;
}

function assertMobileResult(result) {
  const failures = [];
  if (result.errors.length > 0) failures.push(`page errors: ${result.errors.join("; ")}`);
  if (result.viewport.width !== MOBILE_VIEWPORT.width || result.viewport.height !== MOBILE_VIEWPORT.height) {
    failures.push(
      `mobile viewport is ${result.viewport.width}x${result.viewport.height}, expected ${MOBILE_VIEWPORT.width}x${MOBILE_VIEWPORT.height}`,
    );
  }
  if (result.panel === null) {
    failures.push("mobile Sheet panel is not rendered");
  } else if (result.panel.overflowY !== "hidden") {
    failures.push(`mobile Sheet panel overflow-y is ${result.panel.overflowY}, expected hidden`);
  }
  if (result.panelBody === null) {
    failures.push("mobile Sheet body is not rendered");
  } else {
    if (result.panelBody.overflowY !== "auto") {
      failures.push(`mobile Sheet body overflow-y is ${result.panelBody.overflowY}, expected auto`);
    }
    if (result.panelBody.scrollHeight <= result.panelBody.clientHeight + 1) {
      failures.push(
        `mobile Sheet body is not scrolling its content (scrollHeight ${result.panelBody.scrollHeight}, clientHeight ${result.panelBody.clientHeight})`,
      );
    }
  }
  if (result.panel !== null && result.panel.scrollHeight > result.panel.clientHeight + 1) {
    failures.push(
      `mobile Sheet panel itself scrolls (scrollHeight ${result.panel.scrollHeight}, clientHeight ${result.panel.clientHeight})`,
    );
  }
  if (result.rail === null) failures.push("mobile rail is not rendered inside the Sheet");
  else if (result.rail.overflowY !== "visible") failures.push(`mobile rail overflow-y is ${result.rail.overflowY}, expected visible`);
  if (result.railBody === null) failures.push("mobile rail body is not rendered");
  else if (result.railBody.overflowY !== "visible") {
    failures.push(`mobile rail body overflow-y is ${result.railBody.overflowY}, expected visible`);
  }
  if (result.document.scrollHeight > MOBILE_VIEWPORT.height + 1) {
    failures.push(
      `mobile document is ${result.document.scrollHeight}px tall in a ${MOBILE_VIEWPORT.height}px viewport`,
    );
  }
  if (result.searchBox) failures.push("mobile inline search box is still rendered");
  if (result.resume) failures.push("mobile Jump back in action is still rendered");
  if (result.hints) failures.push("mobile key-binding hints are still rendered");
  if (!result.orientation) failures.push("mobile orientation text is missing");
  return failures;
}

async function main() {
  let guard;
  try {
    guard = await startBrowserGuard({
      frontend: FRONTEND,
      profilePrefix: "shellguard-chrome-",
    });
  } catch (error) {
    throw new Error(describeBrowserStartupFailure({ error, subsystem: "launch" }));
  }
  const { vitePort, cleanup } = guard;
  let cdpEndpoint;

  try {
    try {
      await waitForHttp(`http://127.0.0.1:${vitePort}/shellguard.html`, "vite dev server", guard.getViteLaunchError);
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
    const result = await measure(cdpEndpoint, vitePort);
    const mobileResult = await measureMobileSidebar(cdpEndpoint, vitePort);
    const failures = [...assertResult(result), ...assertMobileResult(mobileResult)];
    if (failures.length === 0) {
      console.log(
        `shellguard ok: document ${result.document.scrollHeight}px in a ${result.viewport.height}px viewport, ` +
          `rail body scrolls (${result.railBody.scrollHeight}px in ${result.railBody.clientHeight}px), ` +
          `${result.treeRows} tree rows; mobile Sheet body scrolls (${mobileResult.panelBody.scrollHeight}px in ${mobileResult.panelBody.clientHeight}px)`,
      );
    } else {
      for (const failure of failures) console.error(`shellguard FAIL: ${failure}`);
      // The rail's ancestor chain is the evidence a height fix is aimed at:
      // print it on failure so the broken link is named, not inferred.
      console.error("rail ancestor chain (innermost first):");
      for (const link of result.chain ?? []) {
        console.error(`  ${link.selector} height=${link.height.toFixed(1)} ${JSON.stringify(link.computed)}`);
      }
      console.error(`railBody: ${JSON.stringify(result.railBody)}`);
      console.error(`mobile sidebar: ${JSON.stringify(mobileResult)}`);
      console.error(`experiments: ${JSON.stringify(result.experiments)}`);
      console.error("positioned elements under the rail:");
      for (const el of result.positioned ?? []) {
        console.error(`  ${el.selector} ${el.position} offsetParent=${el.offsetParent} ${JSON.stringify(el.box)}`);
      }
      console.error(
        `scrollingElement=${result.scrollingElement} html overflowY=${result.htmlOverflowY} ` +
          `body scrollHeight=${result.body.scrollHeight} box=${JSON.stringify(result.body.box)} overflowY=${result.body.overflowY}`,
      );
      for (const child of result.body.children) {
        console.error(`  body child ${child.selector} position=${child.position} ${JSON.stringify(child.box)}`);
      }
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
