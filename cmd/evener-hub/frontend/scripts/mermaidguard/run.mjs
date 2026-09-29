#!/usr/bin/env node
// mermaidguard - drives the REAL MermaidDiagram in headless Chrome and proves
// the inline-diagram render pipeline end to end.
//
// WHAT IT ASSERTS, and why only a real browser can: the widget's jsdom suite
// renders mermaid with no layout and never fetches a src'd <img>. This guard
// renders a benign flowchart and a hostile one through the same production
// pipeline and checks:
//
//   1. every known label of the benign diagram survives into the DOM;
//   2. the hostile diagram issues ZERO cross-origin fetch attempts (its
//      <img src> label, authored <a href>, and click directive must all be
//      stripped by security.ts's two sanitize layers);
//   3. no anchor survives anywhere inside a diagram;
//   4. each [data-mermaid-diagram] holds an <svg> with a nonzero client size
//      (a diagram really rendered, not a zero-area or empty stub).
//
// The instrumentation lives in src/dev/mermaidguard-entry.tsx (a
// PerformanceObserver on resource entries plus wrapped Image/fetch). Run with a
// FORBID_TAGS layer removed from widgets/mermaid/security.ts and assertion (2)
// goes red, naming the pixel URL - the local mutation check the task records.
//
// Deterministic: no hub, no credentials, no shared dev server. Follows the
// sibling-guard lifecycle (transcriptscrollguard): startBrowserGuard, wait for
// Vite, connect over CDP, navigate with a boot check, evaluate, clean up.
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

const VIEWPORT = { width: 1200, height: 900 };

// The unbooted-page seam (see navigateTo in browserGuardCdp.mjs): a network
// change can kill the dev-server module burst mid-boot while the page still
// fires its load event. mermaidguard.html boots through one entry module,
// src/dev/mermaidguard-entry.tsx, which assigns window.waitForMermaidGuardSettled
// at module scope - a page whose load event fired without that global never
// booted.
const BOOT = {
  bootExpression: "typeof window.waitForMermaidGuardSettled !== 'undefined'",
  bootLabel: "the mermaidguard entry global window.waitForMermaidGuardSettled",
};

async function main() {
  let guard;
  try {
    guard = await startBrowserGuard({
      frontend: FRONTEND,
      profilePrefix: "mermaidguard-chrome-",
    });
  } catch (error) {
    throw new Error(describeBrowserStartupFailure({ error, subsystem: "launch" }));
  }
  const { vitePort, cleanup } = guard;
  let cdpEndpoint;

  try {
    const viteDeadline = createStartupDeadline();
    try {
      await waitForHttp(`http://127.0.0.1:${vitePort}/mermaidguard.html`, "vite dev server", guard.getViteLaunchError, {
        signal: viteDeadline.signal,
      });
    } catch (error) {
      throw new Error(describeBrowserStartupFailure({ error, subsystem: "vite", viteStderr: guard.getViteError() }));
    } finally {
      viteDeadline.clear();
    }
    cdpEndpoint = await waitForBrowserReady(guard);

    const page = await connectPage(cdpEndpoint);
    const { send } = page;
    const failures = [];
    let payload = null;
    try {
      await applyViewport(send, VIEWPORT);
      await navigateTo(page, `http://127.0.0.1:${vitePort}/mermaidguard.html`, BOOT);
      // Fonts first: the diagram's labels are real text in the mermaid
      // foreignObject layer, and a late webfont would only shift geometry, not
      // the label strings - but the shared wait also proves the document
      // declares the product's fonts (the harness's own web-font check).
      await waitForFonts(send);
      payload = JSON.parse(
        await evaluate(
          send,
          `(async () => {
            const result = await window.waitForMermaidGuardSettled();
            const svgs = [...document.querySelectorAll('[data-mermaid-diagram]')].map((container) => {
              const svg = container.querySelector('svg');
              return svg ? { width: svg.clientWidth, height: svg.clientHeight } : null;
            });
            return JSON.stringify({ result, svgs });
          })()`,
        ),
      );

      const { result, svgs } = payload;
      if (result.missingLabels.length > 0) {
        failures.push(`benign diagram is missing labels: ${result.missingLabels.join(", ")}`);
      }
      if (result.externalAttempts.length > 0) {
        failures.push(
          `hostile diagram issued ${result.externalAttempts.length} external fetch attempt(s): ${result.externalAttempts.join(", ")}`,
        );
      }
      if (result.anchors !== 0) {
        failures.push(`found ${result.anchors} anchor(s) inside a diagram - the sanitizer let a link through`);
      }
      if (svgs.length !== 2) {
        failures.push(`expected 2 [data-mermaid-diagram] containers, found ${svgs.length}`);
      }
      svgs.forEach((svg, index) => {
        if (svg === null) failures.push(`diagram #${index + 1} has no <svg>`);
        else if (svg.width <= 0 || svg.height <= 0) {
          failures.push(`diagram #${index + 1} <svg> has no client size (${svg.width}x${svg.height})`);
        }
      });
    } finally {
      await clearViewportOverride(send);
      page.close();
    }

    if (failures.length === 0) {
      console.log(
        `mermaidguard ok: 2 diagrams rendered (svgs ${payload.svgs.map((svg) => `${svg.width}x${svg.height}`).join(", ")}); ` +
          `${payload.result.labelsPresent.length} benign labels present; 0 anchors; ` +
          `0 external fetch attempts from the hostile diagram`,
      );
    } else {
      for (const failure of failures) console.error(`mermaidguard FAIL: ${failure}`);
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
