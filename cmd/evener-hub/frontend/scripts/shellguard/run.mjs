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
  createStartupDeadline,
  evaluate,
  navigateTo,
  waitForFonts,
  waitForHttp,
} from "../browserGuardCdp.mjs";
import { describeBrowserStartupFailure, startBrowserGuard, waitForBrowserReady } from "../browserGuardProcess.mjs";
import { Driver } from "../skillguard/run.mjs";

const FRONTEND = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..", "..");

// A desktop window, tall enough to be a real screen and short enough that
// the scripted tree (12 projects x 10 sessions) overflows it several times
// over - the exact condition the bug needed.
const VIEWPORT = { width: 1400, height: 900 };
// An iPhone-sized window WITH mobile + touch emulation - without mobile the
// page is a fine-pointer desktop window; without touch, (pointer: coarse)
// never matches and the tap-target floors this guard exists to measure are
// styled out of the page.
const MOBILE_VIEWPORT = { width: 390, height: 844, mobile: true, touch: true };

// The unbooted-page seam (see navigateTo in browserGuardCdp.mjs): a network
// change can kill the dev-server module burst mid-boot while the page still
// fires its load event. shellguard.html boots through one entry module,
// src/dev/shellguard-entry.tsx, which assigns window.settledShell at module
// scope - a page whose load event fired without that global never booted.
const BOOT = {
  bootExpression: "typeof window.settledShell !== 'undefined'",
  bootLabel: "the shellguard entry global window.settledShell",
};

// One page load, one measurement: opens a fresh page at `viewport`, waits for
// the harness to settle, and returns the parsed result of `expression`. Every
// measurement below is one call to this - the per-measure differences are the
// viewport and the expression or page action, nothing else.
async function measureOnPage(cdpEndpoint, vitePort, viewport, expression) {
  const page = await connectPage(cdpEndpoint);
  const { send } = page;
  try {
    await applyViewport(send, viewport);
    await navigateTo(page, `http://127.0.0.1:${vitePort}/shellguard.html`, BOOT);
    await evaluate(send, "window.settledShell");
    await waitForFonts(send);
    await evaluate(
      send,
      "Promise.all(document.getAnimations().filter(a => a.effect.getTiming().iterations !== Infinity).map(a => a.finished))",
    );
    const header = JSON.parse(
      await evaluate(
        send,
        `JSON.stringify((() => {
      const brand = document.querySelector('[data-testid="rail-brand"]');
      const settings = document.querySelector('[data-testid="rail-settings"]');
      const search = document.querySelector('[data-testid="rail-search"]');
      const hide = brand?.querySelector('[aria-label="Hide sidebar"]');
      const identity = [...(brand?.querySelectorAll('span') ?? [])].find(el => el.textContent === 'fake-evener-hub');
      return [identity, settings, search, ...(window.innerWidth > 899 ? [hide] : [])].map(el => {
        if (!el || !brand?.contains(el)) return null;
        const r = el.getBoundingClientRect();
        return { left: r.left, right: r.right, top: r.top, bottom: r.bottom, width: r.width, height: r.height };
      });
    })())`,
      ),
    );
    for (let i = 0; i < header.length; i++) {
      const box = header[i];
      if (!box || box.width <= 0 || box.height <= 0) {
        throw new Error(
          `rail header at ${viewport.width}px: missing visible ${["identity", "Settings", "Search", "Hide sidebar"][i]}`,
        );
      }
      if (box.left < 0 || box.right > viewport.width)
        throw new Error(`rail header escapes ${viewport.width}px viewport`);
      if (i > 0) {
        const previous = header[i - 1];
        if (
          previous.right > box.left ||
          Math.abs((previous.top + previous.bottom) / 2 - (box.top + box.bottom) / 2) > 1
        ) {
          throw new Error(
            `rail header at ${viewport.width}px: identity, Settings, Search, Hide sidebar must align on one row in order`,
          );
        }
      }
    }
    return typeof expression === "function" ? await expression(page) : JSON.parse(await evaluate(send, expression));
  } finally {
    await clearViewportOverride(send);
    page.close();
  }
}

async function measureDockResize(page) {
  const driver = new Driver({});
  driver.page = page;
  await driver.waitPage(
    `(() => {
      const shell = document.querySelector('.dv-shell');
      const host = shell?.querySelector('.dv-floating-overlay-host');
      return host && host.children.length === 0 && shell.clientWidth > 0 &&
        Math.abs(parseFloat(host.style.width) - shell.clientWidth) <= 1;
    })()`,
    { label: "real Dockview empty floating host ready" },
  );
  await evaluate(
    driver.send,
    `(() => {
      const measure = () => ({
        viewport: { width: innerWidth, height: innerHeight },
        document: { width: document.documentElement.scrollWidth, height: document.documentElement.scrollHeight },
        scrollX,
      });
      window.shellguardDockResize = new Promise(resolve => {
        window.addEventListener('resize', () => {
          const host = document.querySelector('.dv-floating-overlay-host');
          const before = measure();
          const cachedHost = { width: host.style.width, height: host.style.height, children: host.children.length };
          resolve({ before, cachedHost });
        }, { once: true });
      });
    })()`,
  );
  await applyViewport(driver.send, { width: 1000, height: 700 });
  return evaluate(driver.send, "window.shellguardDockResize");
}

function assertDockResize(result) {
  const failures = [];
  const { viewport, document } = result.before;
  if (viewport.width !== 1000 || viewport.height !== 700) failures.push("Dockview native resize did not occur");
  if (result.cachedHost.children !== 0) failures.push("Dockview resize fixture has a populated floating host");
  if (document.width > viewport.width + 1 || document.height > viewport.height + 1) {
    failures.push(`Dockview native resize leaks cached dimensions into the document: ${JSON.stringify(result)}`);
  }
  return failures;
}

async function measureFloatingDock(page) {
  const driver = new Driver({});
  driver.page = page;
  await evaluate(
    driver.send,
    `(async () => {
      const { workspaceStore, getDockviewApi } = await import('/src/shell/workspace.ts');
      const paneId = workspaceStore.getState().openPane('settings');
      window.shellguardFloat = { paneId, workspaceStore, getDockviewApi };
    })()`,
  );
  await driver.waitPage(
    "window.shellguardFloat.getDockviewApi()?.getPanel(window.shellguardFloat.paneId) != null",
    { label: "real floating fixture's workspace panel" },
  );
  const initial = await evaluate(
    driver.send,
    `(() => {
      const { paneId, workspaceStore, getDockviewApi } = window.shellguardFloat;
      const api = getDockviewApi();
      api.addFloatingGroup(api.getPanel(paneId), { x: 40, y: 140, width: 480, height: 300 });
      window.measureShellguardFloat = () => {
        const host = document.querySelector('.dv-floating-overlay-host');
        const overlay = host?.querySelector('.dv-resize-container');
        const box = el => {
          const r = el?.getBoundingClientRect();
          return r ? { left: r.left, top: r.top, width: r.width, height: r.height } : null;
        };
        return { host: box(host), overlay: box(overlay), children: host?.children.length,
          focused: workspaceStore.getState().focusedPaneId, paneId };
      };
      window.shellguardFloatHits = [];
      document.addEventListener('pointerdown', event => {
        if (event.target.closest('.dv-floating-titlebar')) {
          window.shellguardFloatHits.push({ trusted: event.isTrusted, x: event.clientX });
        }
      }, true);
      return window.measureShellguardFloat();
    })()`,
  );
  if (!initial.overlay || initial.overlay.width <= 0 || initial.overlay.height <= 0) {
    throw new Error(`floating Dockview initial placement is not visible: ${JSON.stringify(initial)}`);
  }
  const handle = await driver.elementBox(".dv-floating-titlebar");
  if (!handle) throw new Error("floating Dockview drag handle missing");
  await driver.send("Input.dispatchMouseEvent", { type: "mouseMoved", x: handle.x, y: handle.y });
  await driver.send("Input.dispatchMouseEvent", {
    type: "mousePressed", x: handle.x, y: handle.y, button: "left", clickCount: 1,
  });
  // The first movement establishes Dockview's grab offset. The second moves
  // the actual window 240px left, exposing its titlebar over the rail.
  await driver.send("Input.dispatchMouseEvent", {
    type: "mouseMoved", x: handle.x + 10, y: handle.y, button: "left", buttons: 1,
  });
  await driver.send("Input.dispatchMouseEvent", {
    type: "mouseMoved", x: handle.x - 230, y: handle.y, button: "left", buttons: 1,
  });
  await driver.send("Input.dispatchMouseEvent", {
    type: "mouseReleased", x: handle.x - 230, y: handle.y, button: "left", clickCount: 1,
  });
  const overhang = await evaluate(driver.send, "window.measureShellguardFloat()");
  const focusFromOverhang = async () => {
    const point = await evaluate(
      driver.send,
      `(() => {
        const { workspaceStore } = window.shellguardFloat;
        workspaceStore.getState().focusPane(workspaceStore.getState().mainPane().id);
        const r = document.querySelector('.dv-floating-titlebar').getBoundingClientRect();
        return { x: r.left + 40, y: r.top + r.height / 2 };
      })()`,
    );
    await driver.clickAt(point.x, point.y);
    return evaluate(driver.send, "window.measureShellguardFloat()");
  };
  const clicked = await focusFromOverhang();
  await applyViewport(driver.send, { width: 1000, height: 700 });
  await driver.waitPage(
    `(() => {
      const host = document.querySelector('.dv-floating-overlay-host');
      const shell = document.querySelector('.dv-shell');
      return Math.abs(host.getBoundingClientRect().width - shell.clientWidth) <= 1 && shell.clientHeight === 700;
    })()`,
    { label: "real populated Dockview layout after resize" },
  );
  const resized = await focusFromOverhang();
  const hits = await evaluate(driver.send, "window.shellguardFloatHits");
  return { initial, overhang, clicked, resized, hits };
}

function assertFloatingDock(result) {
  const failures = [];
  const { initial, overhang, clicked, resized, hits } = result;
  if (Math.abs(initial.overlay.left - initial.host.left - 40) > 1 ||
      Math.abs(initial.overlay.top - initial.host.top - 140) > 1 ||
      Math.abs(initial.overlay.width - 480) > 1 || Math.abs(initial.overlay.height - 300) > 1) {
    failures.push(`floating Dockview initial placement changed: ${JSON.stringify(initial)}`);
  }
  for (const [label, state] of Object.entries({ overhang, clicked, resized })) {
    if (state.children !== 1 || !state.overlay || state.overlay.left >= state.host.left - 100 ||
        Math.abs(state.overlay.width - 480) > 1 || Math.abs(state.overlay.height - 300) > 1) {
      failures.push(`floating Dockview ${label} lost its actual overhang or dimensions: ${JSON.stringify(state)}`);
    }
  }
  for (const state of [clicked, resized]) {
    if (state.focused !== state.paneId) failures.push("native overhanging titlebar click did not focus its pane");
  }
  if (hits.filter(hit => hit.trusted && hit.x < initial.host.left - 100).length !== 2) {
    failures.push(`native floating titlebar input outside the workspace was clipped: ${JSON.stringify(hits)}`);
  }
  return failures;
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

function assertDeltaRenders(result) {
  const failures = [];
  const changed = result.changedRowID;
  const visibleRowIDs = result.visibleRowIDs ?? [];
  if (typeof changed !== "string" || changed.length === 0) failures.push("changed observer row ID was not retained");
  if (visibleRowIDs.length === 0) failures.push("visible observer row IDs were not retained before the delta");
  if (typeof changed === "string" && !visibleRowIDs.includes(changed)) {
    failures.push(`changed observer row ${changed} was not visible before the delta`);
  }
  if ((result.counts?.[changed] ?? 0) < 1) failures.push("changed navigation row did not render");
  for (const [id, count] of Object.entries(result.counts ?? {})) {
    if (id !== changed && count !== 0) failures.push(`unchanged row ${id} rendered ${count} time(s)`);
  }
  if (result.document.scrollHeight > result.viewport.height + 1) failures.push("delta caused document overflow");
  return failures;
}

function assertTapTargets(result) {
  const failures = [];
  // Harness sanity: the full tree really rendered before "no offenders" means
  // anything (docs/developing-evener/testing.md's unfalsifiable-fixture trap).
  if (result.measured < 120) {
    failures.push(`expected the session list's interactive elements in the page, measured only ${result.measured}`);
  }
  if (result.offenders.length > 0) {
    const counts = new Map();
    for (const o of result.offenders) counts.set(o.selector, (counts.get(o.selector) ?? 0) + 1);
    const summary = [...counts.entries()].map(([selector, count]) => `${count}x ${selector}`).join("; ");
    failures.push(
      `${result.offenders.length} interactive elements in the mobile session list are under the ${result.min}px tap floor: ${summary}`,
    );
  }
  return failures;
}

function assertPaneFooters(result) {
  const failures = [];
  if (result.panes.length !== 2) failures.push(`expected two pane fixtures, found ${result.panes.length}`);
  for (const [index, pane] of result.panes.entries()) {
    if (pane.box === null) {
      failures.push(`pane ${index + 1} did not render`);
      continue;
    }
    if (pane.edgeFooter === null) {
      failures.push(`pane ${index + 1} has no edge footer`);
      continue;
    }
    if (!pane.containsStatusbar || pane.statusbar === null) {
      failures.push(`pane ${index + 1} does not contain its status bar`);
      continue;
    }
    if (Math.abs(pane.box.left - pane.edgeFooter.left) > 1 || Math.abs(pane.box.right - pane.edgeFooter.right) > 1) {
      failures.push(`pane ${index + 1} edge footer does not span its own pane`);
    }
    if (
      Math.abs(pane.edgeFooter.left - pane.statusbar.left) > 1 ||
      Math.abs(pane.edgeFooter.right - pane.statusbar.right) > 1
    ) {
      failures.push(`pane ${index + 1} status bar does not span its edge footer`);
    }
    if (Math.abs(pane.box.bottom - pane.edgeFooter.bottom) > 1) {
      failures.push(`pane ${index + 1} edge footer is not docked to its pane bottom`);
    }
    if (pane.controls.length !== result.expectedControlsPerPane) {
      failures.push(
        `pane ${index + 1} rendered ${pane.controls.length} activity controls instead of ${result.expectedControlsPerPane}`,
      );
    }
    for (const control of pane.controls) {
      if (control.left < pane.statusbar.left - 1 || control.right > pane.statusbar.right + 1) {
        failures.push(
          `pane ${index + 1} activity control escapes its status bar ` +
            `(control ${control.left.toFixed(1)}..${control.right.toFixed(1)}, bar ${pane.statusbar.left.toFixed(1)}..${pane.statusbar.right.toFixed(1)})`,
        );
      }
    }
    if (pane.statusbarScrollWidth > pane.statusbarClientWidth + 1) {
      failures.push(
        `pane ${index + 1} status bar overflows horizontally (${pane.statusbarScrollWidth}px in ${pane.statusbarClientWidth}px)`,
      );
    }
  }
  if (result.panes.length === 2) {
    const [first, second] = result.panes;
    if (first.edgeFooter?.right > second.box.left + 1)
      failures.push("the first pane footer crosses into the second pane");
    if (second.edgeFooter?.left < first.box.right - 1)
      failures.push("the second pane footer crosses into the first pane");
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
  else if (result.rail.overflowY !== "visible")
    failures.push(`mobile rail overflow-y is ${result.rail.overflowY}, expected visible`);
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
    const viteDeadline = createStartupDeadline();
    try {
      await waitForHttp(`http://127.0.0.1:${vitePort}/shellguard.html`, "vite dev server", guard.getViteLaunchError, {
        signal: viteDeadline.signal,
      });
    } catch (error) {
      throw new Error(describeBrowserStartupFailure({ error, subsystem: "vite", viteStderr: guard.getViteError() }));
    } finally {
      viteDeadline.clear();
    }
    cdpEndpoint = await waitForBrowserReady(guard);
    const result = await measureOnPage(
      cdpEndpoint,
      vitePort,
      VIEWPORT,
      "(async () => { await window.applyShellNavigationDelta(); const renders = window.measureRailRenderCounts(); return JSON.stringify({ ...window.measureShell(), paneFooters: window.measurePaneFooters(), counts: renders.counts, changedRowID: renders.changedRowID, visibleRowIDs: renders.visibleRowIDs }); })()",
    );
    const dockResize = await measureOnPage(cdpEndpoint, vitePort, VIEWPORT, measureDockResize);
    const floatingDock = await measureOnPage(cdpEndpoint, vitePort, VIEWPORT, measureFloatingDock);
    // Both mobile measurements come from ONE page load of the emulated phone:
    // the sidebar geometry and the tap-floor audit need the same context.
    const mobile = await measureOnPage(
      cdpEndpoint,
      vitePort,
      MOBILE_VIEWPORT,
      "JSON.stringify({ sidebar: window.measureMobileSidebar(), tap: window.measureTapTargets() })",
    );
    const failures = [
      ...assertResult(result),
      ...assertDeltaRenders(result),
      ...assertPaneFooters(result.paneFooters),
      ...assertDockResize(dockResize),
      ...assertFloatingDock(floatingDock),
      ...assertMobileResult(mobile.sidebar),
      ...assertTapTargets(mobile.tap),
    ];
    if (failures.length === 0) {
      console.log(
        `shellguard ok: document ${result.document.scrollHeight}px in a ${result.viewport.height}px viewport, ` +
          `rail body scrolls (${result.railBody.scrollHeight}px in ${result.railBody.clientHeight}px), ` +
          `${result.treeRows} tree rows; mobile Sheet body scrolls (${mobile.sidebar.panelBody.scrollHeight}px in ${mobile.sidebar.panelBody.clientHeight}px); ` +
          `${mobile.tap.measured} mobile tap targets all >= ${mobile.tap.min}px; ` +
          `${result.paneFooters.panes.length} pane-local footers stay within their panes; Dockview native resize keeps the document bounded; ` +
          "floating placement, overhang and native input survive resize",
      );
      console.log(
        `shellguard render isolation: changed=${result.changedRowID} count=${result.counts[result.changedRowID] ?? 0}; ` +
          `visible=${result.visibleRowIDs.length}; counts=${JSON.stringify(result.counts)}; ` +
          `visibleRowIDs=${JSON.stringify(result.visibleRowIDs)}`,
      );
      console.log(`shellguard Dockview resize: ${JSON.stringify(dockResize)}; floating: ${JSON.stringify(floatingDock)}`);
    } else {
      for (const failure of failures) console.error(`shellguard FAIL: ${failure}`);
      // The rail's ancestor chain is the evidence a height fix is aimed at:
      // print it on failure so the broken link is named, not inferred.
      console.error("rail ancestor chain (innermost first):");
      for (const link of result.chain ?? []) {
        console.error(`  ${link.selector} height=${link.height.toFixed(1)} ${JSON.stringify(link.computed)}`);
      }
      console.error(`railBody: ${JSON.stringify(result.railBody)}`);
      console.error(`mobile sidebar: ${JSON.stringify(mobile.sidebar)}`);
      console.error("sub-floor tap targets in the mobile session list:");
      for (const o of mobile.tap.offenders.slice(0, 20)) {
        console.error(`  ${o.selector} ${JSON.stringify(o.box)}`);
      }
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
