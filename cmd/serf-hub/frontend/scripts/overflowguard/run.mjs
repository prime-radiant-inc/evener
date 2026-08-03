#!/usr/bin/env node
// overflowguard - asserts the transcript pane never scrolls sideways, at any
// pane width, with the real React tree rendered in a real browser.
//
// WHY THIS EXISTS, AND WHY IT IS NOT A layoutguard CASE
//
// layoutguard (../layoutguard) measures HAND-AUTHORED markup against the real
// tokens.css and component stylesheets. That is the right shape for "does this
// CSS rule still hold its box", and it is cheap - static files, no build.
//
// It is the WRONG shape for this bug. The transcript's sideways scroll came
// from a chevron whose painted box grew when rotated: a `▸` text glyph sat in
// a 6x18 line box, and `transform: rotate(90deg)` painted it 18px wide, 6px
// outside its own layout box on each side. A hand-authored harness would have
// hard-coded whichever markup was current when the case was written, so
// swapping the glyph back would leave the guard green while the app broke.
// The guard has to see what the app actually renders.
//
// So this boots the app's own Vite dev server, renders the REAL Session pane
// through the REAL reducer (src/dev/overflowharness-entry.tsx), and asserts a
// property no markup change can smuggle past: no scroll container inside the
// pane has content wider than itself.
//
// The property matters because of a CSS detail that is easy to miss.
// PaneScaffold's `.body` and VirtualList's `.root` both declare `overflow-y:
// auto` and nothing for overflow-x. Per spec, when one axis is not `visible`
// the other computes to `auto` rather than staying `visible` - so both are
// silently horizontal scroll containers too, and a few px of escape anywhere
// inside becomes a scrollbar across the whole pane that clips the first
// character of every line above it.
//
// USAGE:
//   npm run overflowguard              # the default width sweep
//   node scripts/overflowguard/run.mjs 390 1400
//
// STATUS: a local pre-merge check and part of `make test-web-browser` in CI,
// not wired into `make lint`, because it costs a Vite boot and a Chrome
// launch (~10s).
import path from "node:path";
import { fileURLToPath } from "node:url";
import { startBrowserGuard } from "../browserGuardProcess.mjs";

const FRONTEND = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..", "..");

// 390 is a phone; 1400 is a wide desktop pane, where the turn hits its 76rem
// reading measure and STOPS growing - which is exactly where a few px of
// escape at the right edge shows up. A width sweep that skipped the wide end
// would have missed the original bug entirely.
const DEFAULT_WIDTHS = [390, 700, 1024, 1400];

async function waitForHttp(url, label) {
  for (let i = 0; i < 300; i++) {
    try {
      if ((await fetch(url)).ok) return;
    } catch {
      // not listening yet
    }
    await new Promise((r) => setTimeout(r, 100));
  }
  throw new Error(`${label} never came up at ${url}`);
}

function connect(wsUrl) {
  const ws = new WebSocket(wsUrl);
  let id = 0;
  const pending = new Map();
  ws.addEventListener("message", (ev) => {
    const msg = JSON.parse(ev.data);
    if (msg.id !== undefined && pending.has(msg.id)) {
      pending.get(msg.id)(msg);
      pending.delete(msg.id);
    }
  });
  const ready = new Promise((resolve, reject) => {
    ws.addEventListener("open", resolve, { once: true });
    ws.addEventListener("error", reject, { once: true });
  });
  const send = (method, params = {}) =>
    new Promise((resolve) => {
      const thisId = ++id;
      pending.set(thisId, resolve);
      ws.send(JSON.stringify({ id: thisId, method, params }));
    });
  return { ws, ready, send };
}

async function measureAt(cdpPort, url) {
  const targets = await (await fetch(`http://127.0.0.1:${cdpPort}/json/list`)).json();
  const target = targets.find((t) => t.type === "page");
  if (!target) throw new Error("chrome exposed no page target");
  const { ws, ready, send } = connect(target.webSocketDebuggerUrl);
  await ready;
  try {
    await send("Page.enable");
    const loaded = new Promise((resolve) => {
      const handler = (ev) => {
        if (JSON.parse(ev.data).method === "Page.loadEventFired") {
          ws.removeEventListener("message", handler);
          resolve();
        }
      };
      ws.addEventListener("message", handler);
    });
    await send("Page.navigate", { url });
    await loaded;

    const host = (await send("Runtime.evaluate", { expression: "location.host", returnByValue: true })).result.result
      .value;
    if (String(host).includes("9180")) throw new Error("refusing: this eval landed on the shared serf-hub port");

    // The delegate module claims its turn's leadership in a layout effect and
    // the virtualizer measures rows post-mount, so the tree settles a frame or
    // two after load. Await that settling rather than measuring a tree that is
    // still assembling itself.
    await send("Runtime.evaluate", {
      expression: "window.settled",
      awaitPromise: true,
      returnByValue: true,
    });

    const exceptionSafety = await send("Runtime.evaluate", {
      expression: `(() => {
        const systemPrompt = [...document.querySelectorAll('[data-testid="system-notice-scaffold"]')].find((details) =>
          details.querySelector(':scope > summary')?.textContent?.startsWith('System prompt'),
        );
        const rawNotification = document.querySelector('[data-testid="notification-raw-disclosure"]');
        const details = [systemPrompt, rawNotification].filter(Boolean);
        const originalOpen = details.map((details) => details.open);
        const originalGetBoundingClientRect = HTMLElement.prototype.getBoundingClientRect;
        let threw = false;
        HTMLElement.prototype.getBoundingClientRect = function () {
          if (this.closest('[data-testid="system-notice-scaffold"], [data-testid="notification-raw-disclosure"]')) {
            throw new Error("forced disclosure geometry failure");
          }
          return originalGetBoundingClientRect.call(this);
        };
        try {
          window.measure();
        } catch {
          threw = true;
        } finally {
          HTMLElement.prototype.getBoundingClientRect = originalGetBoundingClientRect;
        }
        return { found: details.length, threw, originalOpen, restoredOpen: details.map((details) => details.open) };
      })()`,
      returnByValue: true,
    });
    if (exceptionSafety.result.exceptionDetails) {
      throw new Error(`exception-safety eval threw: ${JSON.stringify(exceptionSafety.result.exceptionDetails)}`);
    }

    const out = await send("Runtime.evaluate", {
      expression: "JSON.stringify(window.measure())",
      returnByValue: true,
      awaitPromise: true,
    });
    if (out.result.exceptionDetails) throw new Error(`page eval threw: ${JSON.stringify(out.result.exceptionDetails)}`);
    return { ...JSON.parse(out.result.result.value), exceptionSafety: exceptionSafety.result.result.value };
  } finally {
    ws.close();
  }
}

async function main() {
  const widths = process.argv.slice(2).map(Number).filter(Boolean);
  const sweep = widths.length > 0 ? widths : DEFAULT_WIDTHS;

  const guard = await startBrowserGuard({
    frontend: FRONTEND,
    profilePrefix: "overflowguard-chrome-",
    chromeArgs: ["--window-size=1800,1000"],
  });
  const { vitePort, cdpPort, cleanup } = guard;

  let failed = 0;
  try {
    try {
      await waitForHttp(`http://127.0.0.1:${vitePort}/overflowharness.html`, "vite dev server");
    } catch (err) {
      throw new Error(`${err.message}\nvite stderr:\n${guard.getViteError()}`);
    }
    await waitForHttp(`http://127.0.0.1:${cdpPort}/json/version`, "chrome devtools endpoint");

    for (const width of sweep) {
      const result = await measureAt(cdpPort, `http://127.0.0.1:${vitePort}/overflowharness.html?w=${width}`);
      let widthFailed = false;
      if (
        result.exceptionSafety.found !== 2 ||
        !result.exceptionSafety.threw ||
        JSON.stringify(result.exceptionSafety.originalOpen) !== JSON.stringify(result.exceptionSafety.restoredOpen)
      ) {
        widthFailed = true;
        console.log(
          `${width}px ... FAIL - disclosure exception safety: ` +
            `found=${result.exceptionSafety.found}, threw=${result.exceptionSafety.threw}, ` +
            `original=${JSON.stringify(result.exceptionSafety.originalOpen)}, ` +
            `restored=${JSON.stringify(result.exceptionSafety.restoredOpen)}`,
        );
      }
      if (result.disclosures.length !== 2) {
        widthFailed = true;
        console.log(
          `${width}px ... FAIL - disclosure browser contract found ${result.disclosures.length} of 2 fixtures`,
        );
      }
      if (!result.footer.effortVisible || !result.footer.contextVisible || !result.footer.queueVisible) {
        widthFailed = true;
        console.log(
          `${width}px ... FAIL - pressured footer facts missing: ` +
            `effort=${result.footer.effortVisible}, context=${result.footer.contextVisible}, queue=${result.footer.queueVisible}`,
        );
      }
      if (result.footer.queueLabel !== "12 queued") {
        widthFailed = true;
        console.log(`${width}px ... FAIL - pressured footer queue label is ${JSON.stringify(result.footer.queueLabel)}`);
      }
      if (result.footer.statusScrollWidth > result.footer.statusClientWidth + 1) {
        widthFailed = true;
        console.log(
          `${width}px ... FAIL - footer status facts are internally clipped: ` +
            `${result.footer.statusScrollWidth}px in ${result.footer.statusClientWidth}px`,
        );
      }
      if (result.footer.modelClientWidth <= 0) {
        widthFailed = true;
        console.log(`${width}px ... FAIL - pressured footer model has zero visible width`);
      }
      for (const disclosure of result.disclosures) {
        if (!disclosure.openDuringOverflowScan) {
          widthFailed = true;
          console.log(`${width}px ... FAIL - ${disclosure.kind} body was closed during horizontal-overflow scan`);
        }
        if (disclosure.restoredOpen !== disclosure.originalOpen) {
          widthFailed = true;
          console.log(`${width}px ... FAIL - ${disclosure.kind} disclosure state was not restored after scan`);
        }
        if (disclosure.kind === "raw-notification" && disclosure.bodyTextLength < 12000) {
          widthFailed = true;
          console.log(
            `${width}px ... FAIL - raw-notification overflow fixture body is only ${disclosure.bodyTextLength} characters`,
          );
        }
        const fullWidth =
          disclosure.summaryWidth >= disclosure.expectedWidth - 1 &&
          disclosure.bodyWidth >= disclosure.expectedWidth - 1;
        const stacked = disclosure.bodyTop >= disclosure.summaryBottom - 1;
        const aligned = Math.abs(disclosure.summaryLeft - disclosure.bodyLeft) <= 1;
        if (
          disclosure.summaryDisplay !== "list-item" ||
          disclosure.markerDisplay === "none" ||
          !fullWidth ||
          !stacked ||
          !aligned
        ) {
          widthFailed = true;
          console.log(
            `${width}px ... FAIL - ${disclosure.kind} disclosure affordance/layout: ` +
              `summary=${disclosure.summaryDisplay}, marker=${disclosure.markerDisplay}, ` +
              `summary/body=${disclosure.summaryWidth.toFixed(1)}/${disclosure.bodyWidth.toFixed(1)}px, ` +
              `expected=${disclosure.expectedWidth.toFixed(1)}px, stacked=${stacked}, aligned=${aligned}`,
          );
        }
      }
      // Never silent about what was excluded: a 1px-wide box is a
      // visually-hidden clip container (the standard screen-reader recipe),
      // not a pane anyone can scroll - but it is reported, not dropped.
      if (result.ignored.length > 0) {
        console.log(
          `${width}px ... ignored ${result.ignored.length} visually-hidden clip box(es) (clientWidth <= 1px)`,
        );
      }
      if (result.scrollers.length === 0) {
        if (!widthFailed)
          console.log(`${width}px ... PASS - disclosures stay native/stacked and nothing scrolls horizontally`);
      } else {
        widthFailed = true;
        console.log(`${width}px ... FAIL - ${result.scrollers.length} horizontal scroll container(s):`);
        for (const s of result.scrollers) {
          console.log(
            `    ${s.tag}.${s.cls}  content ${s.scrollWidth}px in a ${s.clientWidth}px box (+${s.overflowPx}px)`,
          );
          // Deepest first: the innermost escapee is the element actually too
          // wide; its ancestors are only carrying that width upward.
          for (const e of s.escapees) {
            console.log(`      escapes by ${e.overflowPx.toFixed(1)}px: ${e.tag}.${e.cls}`);
          }
        }
      }
      if (widthFailed) failed++;
    }
  } finally {
    cleanup();
  }
  process.exit(failed > 0 ? 1 : 0);
}

main().catch((err) => {
  console.error(err.message);
  process.exit(2);
});
