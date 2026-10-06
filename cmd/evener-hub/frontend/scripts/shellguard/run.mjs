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
import { writeFile } from "node:fs/promises";
import {
  applyViewport,
  clearViewportOverride,
  closePage,
  connectPage,
  createStartupDeadline,
  evaluate,
  navigateTo,
  openPage,
  waitForFonts,
  waitForHttp,
} from "../browserGuardCdp.mjs";
import { describeBrowserStartupFailure, startBrowserGuard, waitForBrowserReady } from "../browserGuardProcess.mjs";
import { Driver } from "../skillguard/run.mjs";
import { checkSessionHoverCards } from "./sessionHoverCard.mjs";

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

const OVERVIEW_VIEWPORTS = [VIEWPORT, MOBILE_VIEWPORT, { width: 320, height: 844, mobile: true, touch: true }];
const OVERVIEW_THEMES = ["light", "dark"];
const OVERVIEW = '[data-testid="activity-sidebar"][aria-label="Overview"]';
const RAIL_ACTION = '[data-session-actions-ref="local:p0-s0"]:not([data-pane-id])';
const LONG_VALUES = [
  `anthropic/${"modelidentifier".repeat(12)}`,
  "sessionidentifier".repeat(12),
  `feature/${"branchidentifier".repeat(12)}`,
  `/work/${"directorysegment".repeat(12)}/session`,
];

async function waitForDom(send, expression, label) {
  return evaluate(send, `(async () => {
    const deadline = performance.now() + 15000;
    while (!(${expression})) {
      if (performance.now() > deadline) throw new Error(${JSON.stringify(label)} + ': ' + document.body.innerText.slice(-1500));
      await new Promise(resolve => requestAnimationFrame(resolve));
    }
    return true;
  })()`);
}

async function settleOverview(send) {
  await waitForFonts(send);
  await evaluate(send, `(async () => {
    const deadline = performance.now() + 15000;
    for (;;) {
      await new Promise(resolve => requestAnimationFrame(resolve));
      const animations = document.getAnimations().filter(a =>
        a.effect.getTiming().iterations !== Infinity && a.playState !== 'finished');
      if (!animations.length) return;
      const results = await Promise.allSettled(animations.map(a => a.finished));
      for (const result of results) {
        if (result.status === 'rejected' && result.reason?.name !== 'AbortError') throw result.reason;
      }
      if (performance.now() > deadline) throw new Error('finite animations did not settle');
    }
  })()`);
  await waitForDom(send, `(() => {
    const aside = document.querySelector(${JSON.stringify(OVERVIEW)});
    if (!aside) return true;
    const box = aside.getBoundingClientRect();
    return box.right <= innerWidth + 1 && box.left >= -1;
  })()`, "Overview finishes its entrance");
}

async function clickControl(send, selector) {
  await waitForDom(send, `document.querySelector(${JSON.stringify(selector)})`, `mounted control ${selector}`);
  const point = await evaluate(send, `(async () => {
    const marker = document.querySelector(${JSON.stringify(selector)});
    const element = marker?.closest('button') ?? marker;
    if (!element) throw new Error('missing control: ' + ${JSON.stringify(selector)} + ' ' + JSON.stringify({
      state: window.overviewGuardState(),
      markers: Array.from(document.querySelectorAll('[data-session-actions-ref]'), e => e.outerHTML)
    }));
    element.scrollIntoView({ block: 'nearest', inline: 'nearest' });
    await new Promise(resolve => requestAnimationFrame(resolve));
    const box = element.getBoundingClientRect();
    if (!box.width || !box.height) throw new Error('hidden control: ' + ${JSON.stringify(selector)});
    return { x: box.left + box.width / 2, y: box.top + box.height / 2 };
  })()`);
  await send("Input.dispatchMouseEvent", { type: "mouseMoved", ...point });
  try {
    await waitForDom(send, `(() => {
      const marker = document.querySelector(${JSON.stringify(selector)});
      const element = marker?.closest('button') ?? marker;
      return element && getComputedStyle(element).visibility === 'visible' && element.contains(document.elementFromPoint(${point.x}, ${point.y}));
    })()`, `hit-testable control ${selector}`);
  } catch (error) {
    const witness = await evaluate(send, `(() => {
      const marker = document.querySelector(${JSON.stringify(selector)});
      const element = marker?.closest('button') ?? marker;
      const box = element?.getBoundingClientRect();
      return { point: ${JSON.stringify(point)}, box: box?.toJSON(),
        hit: document.elementFromPoint(${point.x}, ${point.y})?.outerHTML.slice(0, 500),
        currentHit: box && document.elementFromPoint(box.left + box.width / 2, box.top + box.height / 2)?.outerHTML.slice(0, 500),
        visibility: element && getComputedStyle(element).visibility, state: window.overviewGuardState() };
    })()`);
    throw new Error(error.message + ' witness: ' + JSON.stringify(witness));
  }
  await send("Input.dispatchMouseEvent", { type: "mousePressed", ...point, button: "left", clickCount: 1 });
  await send("Input.dispatchMouseEvent", { type: "mouseReleased", ...point, button: "left", clickCount: 1 });
}

async function pressKey(send, key, code, keyCode, modifiers = 0) {
  for (const type of ["keyDown", "keyUp"]) {
    await send("Input.dispatchKeyEvent", { type, key, code, windowsVirtualKeyCode: keyCode,
      nativeVirtualKeyCode: keyCode, modifiers,
      ...(type === "keyDown" && key === "Enter" && modifiers === 0 ? { text: "\r" } : {}) });
  }
}

async function chooseOverviewMenu(send, selector) {
  await clickControl(send, selector);
  await waitForDom(send, `Array.from(document.querySelectorAll('[role="menuitem"]')).some(e => e.textContent.includes('Overview'))`, "Overview menu item");
  await evaluate(send, `(() => {
    document.querySelector('[data-overview-guard-item]')?.removeAttribute('data-overview-guard-item');
    const item = Array.from(document.querySelectorAll('[role="menuitem"]')).find(e => e.textContent.includes('Overview'));
    item.dataset.overviewGuardItem = '';
  })()`);
  await clickControl(send, '[data-overview-guard-item]');
  await waitForDom(send, `document.querySelector(${JSON.stringify(OVERVIEW)})`, "opened Overview");
  await settleOverview(send);
}

async function openRailOverview(send) {
  const visible = await evaluate(send, `(() => {
    const marker = document.querySelector(${JSON.stringify(RAIL_ACTION)});
    const button = marker?.closest('button');
    return { found: !!marker, width: button?.getBoundingClientRect().width,
      visibility: button && getComputedStyle(button).visibility,
      refs: Array.from(document.querySelectorAll('[data-session-actions-ref]')).slice(0, 3).map(e => e.outerHTML) };
  })()`);
  console.log(`Overview rail trigger: ${JSON.stringify(visible)}`);
  if (!visible.width) await clickControl(send, 'button[aria-label="Sessions"]');
  await settleOverview(send);
  await chooseOverviewMenu(send, RAIL_ACTION);
  await clickControl(send, `${OVERVIEW} [role="radio"][aria-label="About"]`);
  await waitForDom(send, `window.overviewGuardState().overview.tab === 'about' && document.querySelector(${JSON.stringify(OVERVIEW)})?.textContent.includes(${JSON.stringify(LONG_VALUES[1])})`, "hydrated About");
  await waitForDom(send, `document.querySelector('[data-session-actions-ref="local:p0-s0"][data-pane-id]')`, "hydrated receiving Session");
  await settleOverview(send);
}

// Every text rect is compared with every inline clipping ancestor. Selection
// alone can include invisible text and document overflow alone misses ellipsis.
function fullTextMeasurement(element, expected) {
  if (!element) return { missing: true, expected };
  element.scrollIntoView({ block: "nearest", inline: "nearest" });
  const range = document.createRange();
  range.selectNodeContents(element);
  const textRects = Array.from(range.getClientRects(), r => ({ left: r.left, right: r.right, top: r.top, bottom: r.bottom }));
  const clips = [];
  const scrollContainers = [];
  const unselectable = [];
  for (let node = element; node; node = node.parentElement) {
    const style = getComputedStyle(node);
    const box = node.getBoundingClientRect();
    if (style.userSelect === "none") unselectable.push(node.tagName);
    if (["hidden", "clip"].includes(style.overflowX) &&
        textRects.some(r => r.left < box.left - 1 || r.right > box.right + 1)) {
      clips.push({ tag: node.tagName, class: node.className, left: box.left, right: box.right });
    }
    if (["auto", "scroll"].includes(style.overflowX) && node.scrollWidth > node.clientWidth + 1) {
      const previous = node.scrollLeft;
      node.scrollLeft = node.scrollWidth;
      scrollContainers.push({ clientWidth: node.clientWidth, scrollWidth: node.scrollWidth, reachable: node.scrollLeft });
      node.scrollLeft = previous;
    }
  }
  const selection = window.getSelection();
  selection.removeAllRanges();
  selection.addRange(range);
  const selectable = selection.toString() === expected;
  selection.removeAllRanges();
  const surface = element.closest('[data-testid="activity-sidebar"]').getBoundingClientRect();
  const inlineOverflow = textRects.some(r => r.left < surface.left - 1 || r.right > surface.right + 1);
  return { fullText: element.textContent === expected, selectable, clips, scrollContainers, unselectable, textRects, inlineOverflow };
}

async function measureOverview(send) {
  return evaluate(send, `(() => {
    const aside = document.querySelector(${JSON.stringify(OVERVIEW)});
    if (!aside) throw new Error('Overview never rendered');
    const measure = ${fullTextMeasurement.toString()};
    const box = element => {
      const r = element.getBoundingClientRect();
      return { left: r.left, right: r.right, top: r.top, bottom: r.bottom, width: r.width, height: r.height };
    };
    const categories = Array.from(aside.querySelectorAll('[role="radio"]'), element => ({
      text: element.textContent, checked: element.getAttribute('aria-checked'), box: box(element),
      label: measure(element.querySelector('span'), element.textContent)
    }));
    const values = ${JSON.stringify(LONG_VALUES)}.map(expected => {
      const element = Array.from(aside.querySelectorAll('*')).find(e => e.textContent === expected &&
        !Array.from(e.children).some(child => child.textContent === expected));
      return measure(element, expected);
    });
    const close = aside.querySelector('button[aria-label="Close Overview"]');
    return {
      theme: document.documentElement.dataset.theme, font: document.body.dataset.fontSize,
      sidebar: box(aside), categories, values, close: close && box(close),
      tapMin: parseFloat(getComputedStyle(aside).getPropertyValue('--tap-min')),
      footerTabs: Array.from(document.querySelectorAll('[data-testid="statusbar"]'))
        .filter(e => !e.closest('[data-pane-footer-fixture]'))
        .map(e => Array.from(e.querySelectorAll('[data-activity-tab]'), button => button.dataset.activityTab)),
      document: { width: document.documentElement.scrollWidth, height: document.documentElement.scrollHeight },
      errors: window.__shellGuardErrors || [], state: window.overviewGuardState()
    };
  })()`);
}

function assertOverview(result, viewport, theme) {
  const failures = [];
  const labels = ["Agents\n100/100", "Jobs\n100/100", "Watches\n100/100", "Tasks\n100/100", "About"];
  if (result.theme !== theme || result.font !== "xl") failures.push("XL/theme preferences did not apply");
  if (!viewport.mobile && Math.abs(result.sidebar.width - 320) > 1) failures.push(`desktop sidebar width ${result.sidebar.width}`);
  if (result.sidebar.left < -1 || result.sidebar.right > viewport.width + 1) failures.push("sidebar escapes viewport");
  if (!result.close || result.close.left < -1 || result.close.right > viewport.width + 1 || result.close.top < -1 || result.close.bottom > viewport.height + 1) failures.push(`close control escapes viewport: ${JSON.stringify(result.close)}`);
  if (!viewport.mobile && (result.footerTabs.length !== 1 || JSON.stringify(result.footerTabs[0]) !== JSON.stringify(["agents", "jobs", "watches", "tasks"]))) failures.push(`actual pane footer identities ${JSON.stringify(result.footerTabs)}`);
  if (viewport.mobile && result.footerTabs.length) failures.push("phone unexpectedly rendered activity footers");
  if (JSON.stringify(result.categories.map(c => c.text)) !== JSON.stringify(labels)) failures.push(`categories/counts ${JSON.stringify(result.categories.map(c => c.text))}`);
  if (result.categories.filter(c => c.checked === "true").map(c => c.text).join() !== "About") failures.push("About is not exclusively selected");
  for (const category of result.categories) {
    if (!category.label.fullText || category.label.clips.length || category.label.inlineOverflow || !category.label.textRects.length || category.box.width > result.sidebar.width + 1) failures.push(`clipped category ${category.text}: ${JSON.stringify(category.label)}`);
  }
  for (let i = 0; i < result.values.length; i++) {
    const value = result.values[i];
    if (!value.fullText || !value.selectable || value.inlineOverflow || value.clips?.length || value.unselectable?.length || value.scrollContainers?.length || !value.textRects?.length) failures.push(`unreadable ${["model", "session ID", "branch", "path"][i]}: ${JSON.stringify(value)}`);
  }
  if (viewport.mobile) {
    if (!(result.tapMin >= 44)) failures.push(`missing phone tap floor: ${result.tapMin}`);
    for (const control of [...result.categories.map(c => c.box), result.close]) {
      if (!control || control.width < result.tapMin - 1 || control.height < result.tapMin - 1) failures.push(`sub-floor Overview target: ${JSON.stringify(control)}`);
    }
  }
  if (result.document.width > viewport.width + 1 || result.document.height > viewport.height + 1) failures.push(`page overflow ${JSON.stringify(result.document)}`);
  return failures;
}

async function trustedOverviewFlow(send, viewport, childGesture) {
  const pane = await evaluate(send, "window.overviewGuardState().panes.find(p => p.type === 'session' && p.params.ref === 'local:p0-s0').id");
  const trigger = id => `[data-session-actions-ref="local:p0-s0"][data-pane-id="${id}"]`;
  await evaluate(send, `document.querySelector(${JSON.stringify(OVERVIEW)}).querySelector('[role="radio"][aria-checked="true"]').focus()`);
  for (const [key, code, keyCode, tab] of [
    ["Home", "Home", 36, "agents"], ["ArrowRight", "ArrowRight", 39, "jobs"],
    ["ArrowLeft", "ArrowLeft", 37, "agents"], ["End", "End", 35, "about"],
  ]) {
    await pressKey(send, key, code, keyCode);
    await waitForDom(send, `window.overviewGuardState().overview.tab === ${JSON.stringify(tab)}`, `${key} selects ${tab}`);
    const focus = await evaluate(send, `(() => {
      const active = document.activeElement;
      const style = getComputedStyle(active);
      return { checked: active.getAttribute('aria-checked'), role: active.getAttribute('role'),
        visible: active.matches(':focus-visible'), outline: style.outlineStyle,
        width: parseFloat(style.outlineWidth), color: style.outlineColor };
    })()`);
    if (focus.role !== "radio" || focus.checked !== "true" || !focus.visible || focus.outline === "none" || focus.width < 1 || focus.color === "rgba(0, 0, 0, 0)") throw new Error(`${key} has no visible selected-radio focus: ${JSON.stringify(focus)}`);
  }
  await waitForDom(send, `document.querySelector(${JSON.stringify(OVERVIEW)})?.textContent.includes(${JSON.stringify(LONG_VALUES[1])})`, "About restored after keyboard selection");
  await settleOverview(send);
  const tabStops = await evaluate(send, `(() => {
    const aside = document.querySelector(${JSON.stringify(OVERVIEW)});
    const controls = Array.from(aside.querySelectorAll('button, a[href], input, textarea, [tabindex]'))
      .filter(e => e.tabIndex >= 0 && !e.matches(':disabled') && e.getBoundingClientRect().width > 0 && getComputedStyle(e).visibility === 'visible');
    if (controls.length < 2) throw new Error('Overview has fewer than two tab stops');
    window.__overviewEnds = [controls[0], controls[controls.length - 1]];
    controls[0].focus();
    return controls.length;
  })()`);
  if (viewport.mobile) {
    await pressKey(send, "Tab", "Tab", 9, 8);
    await waitForDom(send, "document.activeElement === window.__overviewEnds[1]", "phone Shift+Tab wraps first to last");
    await pressKey(send, "Tab", "Tab", 9);
    await waitForDom(send, "document.activeElement === window.__overviewEnds[0]", "phone Tab wraps last to first");
    for (const modifiers of [0, 8]) {
      for (let i = 0; i < tabStops + 1; i++) {
        await pressKey(send, "Tab", "Tab", 9, modifiers);
        if (!await evaluate(send, `document.querySelector(${JSON.stringify(OVERVIEW)}).contains(document.activeElement)`)) throw new Error(`phone ${modifiers ? 'Shift+Tab' : 'Tab'} escaped Overview at step ${i}`);
      }
    }
  } else {
    await evaluate(send, "window.__overviewEnds[1].focus()");
    let left = false;
    for (let i = 0; i < tabStops + 2; i++) {
      await pressKey(send, "Tab", "Tab", 9);
      if (!await evaluate(send, `document.querySelector(${JSON.stringify(OVERVIEW)}).contains(document.activeElement)`)) {
        left = true;
        break;
      }
    }
    if (!left) throw new Error("desktop Overview trapped Tab");
  }

  const dismiss = async (gesture, expectedPane, expectedButton = null) => {
    if (gesture === "close") await clickControl(send, `${OVERVIEW} button[aria-label="Close Overview"]`);
    else {
      await evaluate(send, `document.querySelector(${JSON.stringify(OVERVIEW)}).querySelector('[role="radio"][aria-checked="true"]').focus()`);
      await pressKey(send, "Escape", "Escape", 27);
    }
    await waitForDom(send, `!document.querySelector(${JSON.stringify(OVERVIEW)})`, `${gesture} completes Overview exit`);
    await waitForDom(send, `(() => {
      const active = document.activeElement;
      const marker = active.querySelector?.('[data-session-actions-ref="local:p0-s0"]');
      const style = getComputedStyle(active), box = active.getBoundingClientRect();
      return active.isConnected && box.width > 0 && box.height > 0 && style.visibility === 'visible' &&
        box.left >= -1 && box.right <= innerWidth + 1 && box.top >= -1 && box.bottom <= innerHeight + 1 &&
        (!${viewport.mobile} || marker?.dataset.paneId === ${JSON.stringify(expectedPane)} ||
          (${!!expectedButton} && active === window.__overviewExpectedButton)) &&
        (${expectedButton ? `active === window.__overviewExpectedButton` : "true"});
    })()`, `${gesture} returns to rendered original session control`);
  };
  // The rail opener is removed when a phone's Sessions drawer closes.
  await dismiss("close", pane);
  await chooseOverviewMenu(send, trigger(pane));
  await clickControl(send, `${OVERVIEW} [role="radio"][aria-label="About"]`);
  await evaluate(send, `void (window.__overviewExpectedButton = document.querySelector(${JSON.stringify(trigger(pane))}).closest('button'))`);
  await dismiss("escape", pane, true);

  const submitStatus = async (wrongFocusedPane = null) => {
    const editor = '[data-pane-scaffold="session:local:p0-s0"] ~ [data-testid="pane-footer"] [role="textbox"][aria-label="Message"]';
    await clickControl(send, editor);
    await evaluate(send, `void (window.__overviewExpectedButton = document.querySelector(${JSON.stringify(editor)}))`);
    if (wrongFocusedPane) {
      await evaluate(send, `window.overviewPane('focus', ${JSON.stringify(wrongFocusedPane)})`);
      await waitForDom(send, `window.overviewGuardState().focusedPaneId === ${JSON.stringify(wrongFocusedPane)} && document.activeElement === document.querySelector(${JSON.stringify(editor)})`, "other workspace pane focused with original composer active");
    }
    await send("Input.insertText", { text: "/status" });
    await waitForDom(send, `document.querySelector(${JSON.stringify(editor)}).textContent.includes('/status')`, "real composer received status");
    await pressKey(send, "Enter", "Enter", 13);
    await pressKey(send, "Enter", "Enter", 13, 2);
    await waitForDom(send, `window.overviewGuardState().overview.open && window.overviewGuardState().overview.tab === 'about' && window.overviewGuardState().overview.ref === 'local:p0-s0' && document.querySelector(${JSON.stringify(OVERVIEW)})?.textContent.includes(${JSON.stringify(LONG_VALUES[1])})`, "trusted status opens hydrated About for its own session");
    await settleOverview(send);
    const state = await evaluate(send, "window.overviewGuardState()");
    if (state.panes.some(p => p.type === "sessionDetails") || state.calls.some(c => c.method === "turn/start" || c.method === "thread/resume")) throw new Error("status opened Details or requested a provider turn");
  };
  await submitStatus();
  await dismiss("close", pane, true);
  if (!viewport.mobile) {
    const other = await evaluate(send, "window.overviewPane('other')");
    await settleOverview(send);
    // Keep the original composer DOM-focused while another real pane owns the
    // workspace focus. The command must capture its own session, not that pane.
    await submitStatus(other);
    await dismiss("close", pane, true);
    await evaluate(send, `window.overviewPane('close', ${JSON.stringify(other)})`);
    const duplicate = await evaluate(send, "window.overviewPane('duplicate')");
    await settleOverview(send);
    for (const origin of [pane, duplicate]) {
      await chooseOverviewMenu(send, trigger(origin));
      await clickControl(send, `${OVERVIEW} [role="radio"][aria-label="About"]`);
      await evaluate(send, `void (window.__overviewExpectedButton = document.querySelector(${JSON.stringify(trigger(origin))}).closest('button'))`);
      await dismiss("escape", origin, true);
    }
    await chooseOverviewMenu(send, trigger(duplicate));
    await clickControl(send, `${OVERVIEW} [role="radio"][aria-label="About"]`);
    await evaluate(send, `window.overviewPane('close', ${JSON.stringify(duplicate)})`);
    await settleOverview(send);
    await evaluate(send, `void (window.__overviewExpectedButton = document.querySelector(${JSON.stringify(trigger(pane))}).closest('button'))`);
    await dismiss("close", pane, true);
  }
  if (viewport.mobile) {
    if (!await evaluate(send, "window.overviewGuardState().overview.open")) await chooseOverviewMenu(send, trigger(pane));
    await clickControl(send, `${OVERVIEW} [role="radio"][aria-label^="Agents"]`);
    await waitForDom(send, `Array.from(document.querySelectorAll(${JSON.stringify(`${OVERVIEW} button`)})).some(e => e.textContent.includes('Open Overview child'))`, "real child delegate row");
    await evaluate(send, `(() => {
      const row = Array.from(document.querySelectorAll(${JSON.stringify(`${OVERVIEW} button`)})).find(e => e.textContent.includes('Open Overview child'));
      row.dataset.overviewGuardChild = '';
    })()`);
    await clickControl(send, '[data-overview-guard-child]');
    try {
      await waitForDom(send, `window.overviewGuardState().overview.ref === 'local:overview-child' && !document.querySelector(${JSON.stringify(trigger(pane))})`, "child transcript replaces parent Session");
    } catch (error) {
      const witness = await evaluate(send, `(() => {
        const state = window.overviewGuardState();
        return { ...state, calls: state.calls.slice(-12), errors: window.__shellGuardErrors || [] };
      })()`);
      throw new Error(error.message + ' child-open witness: ' + JSON.stringify(witness));
    }
    await settleOverview(send);
    await clickControl(send, `${OVERVIEW} [role="radio"][aria-label="About"]`);
    await waitForDom(send, `document.querySelector(${JSON.stringify(OVERVIEW)})?.textContent.includes(${JSON.stringify(LONG_VALUES[1])})`, "child About hydrated");
    await waitForDom(send, `Array.from(document.querySelectorAll('[data-session-navigation-ref="local:p0-s0"]')).some(e => !e.closest(${JSON.stringify(OVERVIEW)}))`, "underlying parent breadcrumb");
    const before = await evaluate(send, "window.overviewGuardState()");
    await evaluate(send, `void (window.__overviewExpectedButton = Array.from(document.querySelectorAll('[data-session-navigation-ref="local:p0-s0"]')).find(e => !e.closest(${JSON.stringify(OVERVIEW)})))`);
    await dismiss(childGesture, pane, true);
    const after = await evaluate(send, "window.overviewGuardState()");
    if (after.focusedPaneId !== before.focusedPaneId || JSON.stringify(after.panes) !== JSON.stringify(before.panes)) throw new Error("child dismissal navigated or changed panes");
    await pressKey(send, "Enter", "Enter", 13);
    await waitForDom(send, `window.overviewGuardState().focusedPaneId === ${JSON.stringify(pane)} && document.querySelector(${JSON.stringify(trigger(pane))})`, "returned parent link remains keyboard-usable");
    await settleOverview(send);
  }
  return { tabStops, status: true, phoneTrap: !!viewport.mobile, childReturn: childGesture, originalPane: pane };
}

async function measureActivityRowPadding(send, viewport) {
  const outer = await evaluate(send, `(() => {
    const aside = document.querySelector(${JSON.stringify(OVERVIEW)});
    const body = [...aside.querySelectorAll('div')].find(el => getComputedStyle(el).overflowY === 'auto');
    if (!body) throw new Error('Overview scroll viewport missing');
    const style = getComputedStyle(body);
    return [style.paddingTop, style.paddingRight, style.paddingBottom, style.paddingLeft];
  })()`);
  await evaluate(send, "window.activityRowFixture(true)");
  try {
    await waitForDom(send, `[...document.querySelectorAll('[data-row-case]')].length === 6 && !!document.querySelector('[data-row-case="task"] [data-testid="task-check"]')`, "production activity-row fixtures hydrated");
    await waitForFonts(send);
    const rows = await evaluate(send, `Array.from(document.querySelectorAll('[data-row-case]')).map(container => {
      const kind = container.dataset.rowCase;
      const row = kind === 'watch' || kind === 'task' ? container.querySelector('summary') : container.firstElementChild;
      let glyph = row.firstElementChild;
      let nested = null;
      if (kind === 'task') {
        glyph = row.querySelector('[data-testid="task-check"]');
      } else if (kind === 'watch') {
        glyph = row.querySelector('[data-testid^="sidebar-watch-"]').parentElement;
        nested = getComputedStyle(glyph.parentElement);
      }
      const style = getComputedStyle(row), box = row.getBoundingClientRect();
      return { kind, inset: glyph.getBoundingClientRect().left - box.left,
        padding: [style.paddingTop, style.paddingRight, style.paddingBottom, style.paddingLeft],
        nestedPadding: nested ? [nested.paddingTop, nested.paddingRight, nested.paddingBottom, nested.paddingLeft] : null,
        width: box.width, height: box.height };
    })`);
    const failures = [];
    if (JSON.stringify(outer) !== JSON.stringify(["8px", "12px", "12px", "12px"])) failures.push(`Overview outer padding changed: ${JSON.stringify(outer)}`);
    for (const row of rows) {
      if (Math.abs(row.inset - 8) > 0.1 || JSON.stringify(row.padding) !== JSON.stringify(["4px", "8px", "4px", "8px"])) {
        failures.push(`${row.kind} row must have 8px glyph inset and 4px/8px padding: ${JSON.stringify(row)}`);
      }
      if (row.nestedPadding && row.nestedPadding.some(padding => padding !== "0px")) failures.push(`watch nested row adds padding: ${JSON.stringify(row.nestedPadding)}`);
      if (viewport.mobile && !row.kind.endsWith('passive') && (row.width < 44 || row.height < 44)) failures.push(`${row.kind} touch target below 44px: ${JSON.stringify(row)}`);
    }
    await clickControl(send, '[data-row-case="agent-clickable"] button');
    await waitForDom(send, `document.querySelector('[data-activity-row-fixture]').dataset.activated === 'agent'`, "agent drill callback");
    await evaluate(send, `document.querySelector('[data-row-case="job-clickable"] button').focus()`);
    await pressKey(send, "Enter", "Enter", 13);
    await waitForDom(send, `document.querySelector('[data-activity-row-fixture]').dataset.activated === 'job'`, "job keyboard activation");
    for (const kind of ["watch", "task"]) {
      await clickControl(send, `[data-row-case="${kind}"] summary`);
      await waitForDom(send, `!!document.querySelector('[data-row-case="${kind}"] details[open]')`, `${kind} disclosure opens`);
    }
    return { result: { outer, rows }, failures };
  } finally {
    await evaluate(send, "window.activityRowFixture(false)");
  }
}

// Browser regressions may prepare real page input without replacing the flow.
export async function overviewOnPage(cdpEndpoint, vitePort, viewport, theme, childGesture, preparePage) {
  const target = await openPage(cdpEndpoint, "about:blank");
  const page = await connectPage(cdpEndpoint, target.id);
  const { send } = page;
  try {
    await send("Storage.clearDataForOrigin", { origin: `http://127.0.0.1:${vitePort}`, storageTypes: "local_storage" });
    await applyViewport(send, viewport);
    await navigateTo(page, `http://127.0.0.1:${vitePort}/shellguard.html`, BOOT);
    await evaluate(send, "window.settledShell");
    await evaluate(send, `window.configureOverview(${JSON.stringify(theme)})`);
    await settleOverview(send);
    await openRailOverview(send);
    const fixture = preparePage && await preparePage(send);
    const result = await measureOverview(send);
    if (process.env.EVENER_SCRATCH_DIR) {
      await evaluate(send, `(() => {
        const aside = document.querySelector(${JSON.stringify(OVERVIEW)});
        for (const element of aside.querySelectorAll('*')) {
          if (['auto', 'scroll'].includes(getComputedStyle(element).overflowY)) element.scrollTop = 0;
        }
      })()`);
      const screenshot = await send("Page.captureScreenshot", { format: "png" });
      await writeFile(path.join(process.env.EVENER_SCRATCH_DIR, `overview-${viewport.width}-${theme}.png`), Buffer.from(screenshot.result.data, "base64"));
    }
    const failures = assertOverview(result, viewport, theme);
    const padding = await measureActivityRowPadding(send, viewport);
    result.rowPadding = padding.result;
    failures.push(...padding.failures);
    try {
      result.keyboard = await trustedOverviewFlow(send, viewport, childGesture);
    } catch (error) {
      const witness = await evaluate(send, `(() => {
        const state = window.overviewGuardState();
        return { ...state, calls: state.calls.slice(-12), errors: window.__shellGuardErrors || [],
          mountedOverview: !!document.querySelector(${JSON.stringify(OVERVIEW)}) };
      })()`);
      failures.push(`trusted interaction: ${error.message}; child witness: ${JSON.stringify(witness)}`);
    }
    await fixture?.afterInteraction;
    // The initial geometry snapshot predates trusted input. Read this same
    // page again even when an interaction failed, and report each event once.
    result.errors = await evaluate(send, "window.__shellGuardErrors || []");
    failures.push(...result.errors.map(error => `page error: ${error}`));
    return { result, failures };
  } finally {
    await clearViewportOverride(send);
    page.close();
    await closePage(cdpEndpoint, target.id);
  }
}

async function main() {
  let guard;
  try {
    guard = await startBrowserGuard({
      frontend: FRONTEND,
      profilePrefix: "shellguard-chrome-",
      // Headless hosts may have no pointer hardware. These Chromium settings
      // supply a hover-capable mouse, touch emulation still owns phone cases.
      chromeArgs: ["--blink-settings=availableHoverTypes=2,primaryHoverType=2,availablePointerTypes=4,primaryPointerType=4"],
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
    for (const viewport of OVERVIEW_VIEWPORTS) {
      for (const theme of OVERVIEW_THEMES) {
        for (const childGesture of viewport.mobile ? ["close", "escape"] : [null]) {
          const label = `Overview ${viewport.width}px ${theme} XL${childGesture ? ` child ${childGesture}` : ""}`;
          try {
            const overview = await overviewOnPage(cdpEndpoint, vitePort, viewport, theme, childGesture);
            failures.push(...overview.failures.map(failure => `${label}: ${failure}`));
            console.log(`${label}: ${JSON.stringify(overview.result)}`);
          } catch (error) {
            failures.push(`${label}: ${error.message}`);
          }
        }
      }
    }
    failures.push(...await checkSessionHoverCards(cdpEndpoint, vitePort));
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

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main().catch((error) => {
    console.error(error instanceof Error ? error.message : String(error));
    process.exitCode = 1;
  });
}
