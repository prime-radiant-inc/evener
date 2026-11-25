#!/usr/bin/env node
import { writeFile } from "node:fs/promises";
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
  connectBrowser,
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
import { checkMovingControls } from "./click-regression.mjs";
import {
  DOCKVIEW_READY_LABEL,
  DOCKVIEW_READY_WAIT_EXPR,
  FLOATING_PANEL_LABEL,
  FLOATING_PANEL_WAIT_EXPR,
  waitForFloatingState,
} from "./floatingFixture.mjs";
import { checkSessionHoverCards } from "./sessionHoverCard.mjs";
import { checkSettingsGeometry } from "./settingsGeometry.mjs";
import { measureDelayedFloatingDock } from "./startup.mjs";

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

// Inside the 768-899px tablet band: StackHost keeps mobile/touch emulation
// while splitting the board column beside the conversation.
const TABLET_VIEWPORT = { width: 800, height: 1025, mobile: true, touch: true };

// A touch device the desktop layout still applies to (a tablet or a touch
// laptop). The rail renders docked and resizable here, so its width floor is
// the one that has to hold 44px tap targets.
const TOUCH_DESKTOP_VIEWPORT = { width: 1180, height: 820, mobile: false, touch: true };

// The unbooted-page seam (see navigateTo in browserGuardCdp.mjs): a network
// change can kill the dev-server module burst mid-boot while the page still
// fires its load event. shellguard.html boots through one entry module,
// src/dev/shellguard-entry.tsx, which assigns window.settledShell at module
// scope - a page whose load event fired without that global never booted.
const BOOT = {
  bootExpression: "typeof window.settledShell !== 'undefined'",
  bootLabel: "the shellguard entry global window.settledShell",
};

// One page load, one measurement: loads the harness as a new document in the
// shared tab, with empty localStorage, at `viewport`, waits for it to settle,
// and returns the parsed result of `expression`. Every measurement below is
// one call to this - the per-measure differences are the viewport and the
// expression or page action, nothing else.
async function measureOnPage(cdpEndpoint, sharedTabId, vitePort, viewport, expression) {
  const page = await connectPage(cdpEndpoint, sharedTabId);
  const { send } = page;
  try {
    // Every measurement shares this tab, and the outgoing document can still
    // save its layout until the navigation replaces it, so clear storage as each
    // new document starts. The script ends with this CDP session.
    await send("Page.addScriptToEvaluateOnNewDocument", { source: "try { localStorage.clear(); } catch {}" });
    await applyViewport(send, viewport);
    await navigateTo(page, `http://127.0.0.1:${vitePort}/shellguard.html`, BOOT);
    await evaluate(send, "window.settledShell");
    await waitForFonts(send);
    await settleFiniteAnimations(send);
    const header = JSON.parse(
      await evaluate(
        send,
        `JSON.stringify((() => {
      const box = (el, container) => {
        if (!el || !container?.contains(el)) return null;
        const r = el.getBoundingClientRect();
        return { left: r.left, right: r.right, top: r.top, bottom: r.bottom, width: r.width, height: r.height };
      };
      const brand = document.querySelector('[data-testid="rail-brand"]');
      if (window.innerWidth < 768) {
        const dialog = document.querySelector('[role="dialog"]');
        const sheetHeader = dialog?.querySelector("header");
        const titleId = dialog?.getAttribute("aria-labelledby");
        const title = titleId ? document.getElementById(titleId) : sheetHeader?.querySelector("h2");
        const settings = sheetHeader?.querySelector('[data-testid="rail-settings"]');
        const search = sheetHeader?.querySelector('[data-testid="rail-search"]');
        const close = sheetHeader?.querySelector('[aria-label="Close"]');
        return {
          mode: "sheet",
          hasBrand: brand !== null,
          containerRight: dialog ? dialog.getBoundingClientRect().right : null,
          items: [title, settings, search, close].map((el) => box(el, sheetHeader)),
        };
      }
      const settings = document.querySelector('[data-testid="rail-settings"]');
      const search = document.querySelector('[data-testid="rail-search"]');
      const hide = brand?.querySelector('[aria-label="Hide sidebar"]');
      const wordmark = document.querySelector('[data-testid="rail-wordmark"]');
      return {
        mode: "brand",
        hasBrand: brand !== null,
        containerRight: null,
        items: [wordmark, settings, search, ...(window.innerWidth > 899 ? [hide] : [])].map((el) => box(el, brand)),
      };
    })())`,
      ),
    );
    // The rail's own right edge, and whether the wordmark is intact at the
    // floor width: the header is a flex row, so content wider than the rail
    // overflows it rather than widening it, and the brand box keeps the rail's
    // width while its children spill past the edge. Comparing against the
    // viewport (not the rail) is what hid a coarse-pointer overflow in #6153.
    const railBox = JSON.parse(
      await evaluate(
        send,
        `JSON.stringify((() => {
      const rail = document.querySelector('[data-testid="rail"]');
      const wordmark = document.querySelector('[data-testid="rail-wordmark"]');
      const clamp = document.querySelector('[data-testid="rail-resize-handle"]');
      if (!rail) return null;
      const r = rail.getBoundingClientRect();
      return {
        left: r.left,
        right: r.right,
        width: r.width,
        wordmarkClipped: wordmark ? wordmark.scrollWidth > wordmark.clientWidth + 1 : false,
        ariaValueMin: clamp ? Number(clamp.getAttribute("aria-valuemin")) : null,
      };
    })())`,
      ),
    );
    if (header.mode === "sheet" && header.hasBrand) {
      throw new Error(`rail header at ${viewport.width}px: unexpected rail-brand inside phone drawer`);
    }
    const labels =
      header.mode === "sheet" ? ["title", "Settings", "Search", "Close"] : ["wordmark", "Settings", "Search", "Hide sidebar"];
    const containerRight = header.mode === "sheet" ? header.containerRight : railBox?.right ?? null;
    for (let i = 0; i < header.items.length; i++) {
      const box = header.items[i];
      if (!box || box.width <= 0 || box.height <= 0) {
        throw new Error(
          `rail header at ${viewport.width}px: missing visible ${labels[i]}`,
        );
      }
      if (box.left < 0 || box.right > viewport.width)
        throw new Error(`rail header escapes ${viewport.width}px viewport`);
      // Per-element, not just the viewport: an item pushed past the rail's edge
      // paints over the workspace beside it.
      if (containerRight !== null && box.right > containerRight + 0.5) {
        throw new Error(
          `rail header at ${viewport.width}px (rail ${railBox?.width.toFixed(1) ?? "?"}px, min ${railBox?.ariaValueMin ?? null}): ` +
            `${labels[i]} ends at ${box.right.toFixed(1)}px, ` +
            `past the ${header.mode === "sheet" ? "drawer" : "rail"}'s ${containerRight.toFixed(1)}px edge`,
        );
      }
      if (i > 0) {
        const previous = header.items[i - 1];
        if (
          previous.right > box.left ||
          Math.abs((previous.top + previous.bottom) / 2 - (box.top + box.bottom) / 2) > 1
        ) {
          throw new Error(
            `rail header at ${viewport.width}px: ${labels.slice(0, header.items.length).join(", ")} must align on one row in order`,
          );
        }
      }
    }
    // At the width floor the header must fit exactly: the floor is derived from
    // this content, so a clipped wordmark there means the two have drifted.
    if (railBox && railBox.ariaValueMin !== null && Math.abs(railBox.width - railBox.ariaValueMin) <= 0.5) {
      if (railBox.wordmarkClipped) {
        throw new Error(
          `rail header at its ${railBox.ariaValueMin}px minimum clips the wordmark (rail ${railBox.width.toFixed(1)}px)`,
        );
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

async function measureFloatingDock(page, stage = "first measurement") {
  const driver = new Driver({});
  driver.page = page;
  await waitForFloatingState(
    driver,
    // The install rides INSIDE the wait, on every poll: the witness a failed
    // wait attaches reads the store and api through this global, so a wait that
    // times out with the module still importable must not report them as
    // unreadable just because nothing had published them yet - and a transport
    // hiccup still retries the import rather than failing the guard outright.
    `(async () => {
      const { workspaceStore, getDockviewApi } = await import('/src/shell/workspace.ts');
      window.shellguardFloat = { workspaceStore, getDockviewApi };
      return getDockviewApi() != null;
    })()`,
    { label: "real floating fixture's restored workspace", stage },
  );
  // DockHost is a lazy chunk and settledShell waits only for the rail, so the
  // dock can still be booting here. A pane opened before DockHost's onReady is
  // re-minted under a new id when onReady restores the layout an earlier page
  // saved, and the id openPane returned then never gets a panel. DockHost
  // registers the api at the top of its synchronous onReady, so a non-null api
  // means that restore has finished.
  await waitForFloatingState(driver, DOCKVIEW_READY_WAIT_EXPR, {
    label: DOCKVIEW_READY_LABEL,
    stage,
  });
  const openPaneStart = Date.now();
  await evaluate(
    driver.send,
    "window.shellguardFloat.paneId = window.shellguardFloat.workspaceStore.getState().openPane('settings')",
  );
  // The wait covers the openPane -> addPanel hop, measured at single-digit
  // milliseconds on a green run, so a timeout here means the panel really never
  // appeared - or the page stopped answering, which waitPage swallows. Either
  // way the bare label says nothing about the state, so the wait attaches it
  // before rethrowing (#3747).
  await waitForFloatingState(driver, FLOATING_PANEL_WAIT_EXPR, {
    label: FLOATING_PANEL_LABEL,
    stage,
    openPaneMs: Date.now() - openPaneStart,
  });
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
    type: "mousePressed",
    x: handle.x,
    y: handle.y,
    button: "left",
    clickCount: 1,
  });
  // The first movement establishes Dockview's grab offset. The second moves
  // the actual window 240px left, exposing its titlebar over the rail.
  await driver.send("Input.dispatchMouseEvent", {
    type: "mouseMoved",
    x: handle.x + 10,
    y: handle.y,
    button: "left",
    buttons: 1,
  });
  await driver.send("Input.dispatchMouseEvent", {
    type: "mouseMoved",
    x: handle.x - 230,
    y: handle.y,
    button: "left",
    buttons: 1,
  });
  await driver.send("Input.dispatchMouseEvent", {
    type: "mouseReleased",
    x: handle.x - 230,
    y: handle.y,
    button: "left",
    clickCount: 1,
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
  if (
    Math.abs(initial.overlay.left - initial.host.left - 40) > 1 ||
    Math.abs(initial.overlay.top - initial.host.top - 140) > 1 ||
    Math.abs(initial.overlay.width - 480) > 1 ||
    Math.abs(initial.overlay.height - 300) > 1
  ) {
    failures.push(`floating Dockview initial placement changed: ${JSON.stringify(initial)}`);
  }
  for (const [label, state] of Object.entries({ overhang, clicked, resized })) {
    if (
      state.children !== 1 ||
      !state.overlay ||
      state.overlay.left >= state.host.left - 100 ||
      Math.abs(state.overlay.width - 480) > 1 ||
      Math.abs(state.overlay.height - 300) > 1
    ) {
      failures.push(`floating Dockview ${label} lost its actual overhang or dimensions: ${JSON.stringify(state)}`);
    }
  }
  for (const state of [clicked, resized]) {
    if (state.focused !== state.paneId) failures.push("native overhanging titlebar click did not focus its pane");
  }
  if (hits.filter((hit) => hit.trusted && hit.x < initial.host.left - 100).length !== 2) {
    failures.push(`native floating titlebar input outside the workspace was clipped: ${JSON.stringify(hits)}`);
  }
  return failures;
}

// The rail's width floor is derived from its header content (stores/prefs.ts),
// and on a coarse pointer every icon button takes the 44px tap floor instead of
// its 32px square. This pins the consequence: at that floor the row still fits
// inside the rail and the wordmark is intact. #6153 shipped a floor that held
// for a mouse and overflowed by 20px on touch, which the viewport-based header
// check could not see.
function assertTouchRailMinimum(result) {
  const failures = [];
  if (!result?.applied) {
    failures.push("the rail's minimum width was not readable from the resize handle's aria-valuemin");
    return failures;
  }
  if (!result.coarse) {
    failures.push("the touch viewport did not report a coarse pointer, so the tap floor was never applied");
  }
  if (result.lastButtonRight === null) {
    failures.push("the rail header's action buttons did not render at the minimum width");
  } else if (result.lastButtonRight > result.railRight + 0.5) {
    failures.push(
      `at the ${result.min}px touch minimum the header's last action ends at ${result.lastButtonRight.toFixed(1)}px, ` +
        `past the rail's ${result.railRight.toFixed(1)}px edge`,
    );
  }
  if (result.wordmarkClipped) {
    failures.push(`the wordmark is clipped at the ${result.min}px touch minimum`);
  }
  return failures;
}

// The row's menu trigger, marked by scrollRailToEnd: the runner names the
// journey's nodes, the fixture owns how to find them.
const FOCUS_SCROLL_TRIGGER = "document.querySelector('[data-focus-scroll-trigger]')?.closest('button')";

// The rail row menu dismisses itself when a live insertion above its trigger
// pushes the trigger out of the rail body's client area, and the dismissal
// returns focus to that trigger. With preventScroll the rail keeps the offset
// it had; without it the browser scrolls the rail to reveal a trigger the
// reader could not see. That second half is what makes this a guard rather
// than a snapshot of today's call site (#6838).
async function measureFocusScroll(page) {
  const { send } = page;
  const prepared = await evaluate(send, "window.focusScrollFixture.scrollRailToEnd()");
  const requireFixture = (ok, message) => {
    if (!ok) throw new Error(message);
  };
  requireFixture(
    prepared.nearMax,
    `focus-scroll fixture: the rail did not reach its maximum (scrollTop ${prepared.scrollTop} of ` +
      `${prepared.maxScrollTop}, scrollHeight ${prepared.scrollHeight} in clientHeight ${prepared.clientHeight})`,
  );
  requireFixture(
    prepared.actionsRevealed,
    `focus-scroll fixture: rail row ${prepared.triggerRef}'s actions wrapper was not found to hold its reveal`,
  );
  requireFixture(
    prepared.triggerVisible,
    `focus-scroll fixture: rail row ${prepared.triggerRef}'s menu trigger is outside the rail body's client area at maximum scroll`,
  );
  // The fixture holds this row's actions revealed (the state the row's own
  // focus or hover puts them in), so the click reaches the trigger.
  await clickControl(send, "[data-focus-scroll-trigger]");
  await waitForDom(send, `${FOCUS_SCROLL_TRIGGER}?.getAttribute('aria-expanded') === 'true'`, "rail row menu opened");
  // The insertion IS the dismissal: the menu's own layout observer names the
  // now-hidden trigger and closes through its no-scroll path.
  const pushed = await evaluate(send, "window.focusScrollFixture.pushTriggerOut()");
  await waitForDom(
    send,
    `${FOCUS_SCROLL_TRIGGER}?.getAttribute('aria-expanded') === 'false'`,
    "layout dismissal of the rail row menu",
  );
  await settleFiniteAnimations(send);
  const after = await evaluate(send, "window.focusScrollFixture.readAfter()");
  return { triggerRef: prepared.triggerRef, pushed, after };
}

function assertFocusScroll(result) {
  const failures = [];
  const beforeScrollTop = result.pushed.scrollTopBefore;
  if (!result.pushed.triggerHidden) {
    failures.push(
      `focus scroll: the live insertion did not push rail row ${result.triggerRef}'s trigger out of the rail body's client area`,
    );
  }
  if (Math.abs(result.pushed.scrollTopAfterInsertion - beforeScrollTop) > 1) {
    failures.push(
      `focus scroll: the insertion itself moved the rail body's scrollTop from ${beforeScrollTop} ` +
        `to ${result.pushed.scrollTopAfterInsertion}, before any focus changed`,
    );
  }
  if (!result.after.triggerFocused) {
    failures.push(
      `focus scroll: after the layout dismissal document.activeElement is ${result.after.activeElement}, ` +
        `not rail row ${result.triggerRef}'s menu trigger ` +
        `(trigger connected ${result.after.triggerConnected}, visibility ${result.after.triggerVisibility})`,
    );
  }
  const moved = result.after.scrollTop - beforeScrollTop;
  if (Math.abs(moved) > 1) {
    failures.push(
      `focus scroll: the rail body's scrollTop moved from ${beforeScrollTop} to ${result.after.scrollTop} ` +
        `(${moved > 0 ? "+" : ""}${moved.toFixed(1)}px, the fixture inserted ${result.pushed.insertedHeight}px ` +
        `above the trigger) when the dismissal returned focus to the trigger`,
    );
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
  if (!result.railNewSession?.present) failures.push("the rail's New session control did not render");
  if (result.railNewSession?.primary) {
    failures.push("the rail's New session control is a filled primary and outranks the current task");
  }
  if (!result.railNewSession?.secondary) {
    failures.push("the rail's New session control lost its secondary treatment");
  }
  if (result.primaryButtons > 1) {
    failures.push(`the shell route shows ${result.primaryButtons} filled primaries, expected at most one`);
  }

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

async function measureTabletSplit(page) {
  const { send } = page;
  await waitForDom(
    send,
    "Boolean(document.querySelector('[data-testid=\"board-column\"]') && document.querySelector('[data-testid=\"topbar-title\"]')?.textContent?.trim())",
    "tablet split initial topbar title settled",
  );
  await settleFiniteAnimations(send);
  const snapState = `(() => {
    const box = (el) => {
      if (!el) return null;
      const r = el.getBoundingClientRect();
      return { left: r.left, top: r.top, right: r.right, bottom: r.bottom, width: r.width, height: r.height };
    };
    const hit = (el) => {
      if (!el) return false;
      const r = el.getBoundingClientRect();
      if (r.width <= 0 || r.height <= 0) return false;
      const target = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
      return target !== null && el.contains(target);
    };
    const split = document.querySelector('[data-testid="board-split"]');
    const board = document.querySelector('[data-testid="board-column"]');
    const pane = document.querySelector('[data-testid="pane-column"]');
    const topbar = document.querySelector('[data-testid="topbar"]');
    const title = document.querySelector('[data-testid="topbar-title"]');
    const findInput = pane?.querySelector('[data-find-bar] input[type="search"]');
    const findStyle = findInput && getComputedStyle(findInput);
    const findRectWidth = findInput ? findInput.getBoundingClientRect().width : 0;
    const findText = findInput && document.createElement('canvas').getContext('2d');
    if (findText) findText.font = findStyle.font;
    const overview = document.querySelector('[data-session-overview-ref]')?.closest('button') ?? null;
    const brand = document.querySelector('[data-testid="rail-brand"]');
    const wordmark = document.querySelector('[data-testid="rail-wordmark"]');
    const settings = document.querySelector('[data-testid="rail-settings"]');
    const search = document.querySelector('[data-testid="rail-search"]');
    const toggles = [...document.querySelectorAll('[data-testid="board-toggle"]')];
    const toggle = toggles[0] ?? null;
    const glyph = toggle?.querySelector('span span') ?? null;
    const handle = board?.querySelector('[role="separator"]');
    return {
      viewport: { width: innerWidth, height: innerHeight },
      document: { scrollWidth: document.documentElement.scrollWidth, scrollHeight: document.documentElement.scrollHeight },
      dataBoard: split?.getAttribute('data-board') ?? null,
      board: box(board),
      pane: box(pane),
      topbar: box(topbar),
      topbarInPane: Boolean(pane && topbar && pane.contains(topbar)),
      title: box(title),
      overview: box(overview),
      overviewHit: hit(overview),
      find: findInput ? {
        width: findRectWidth,
        editableWidth:
          findRectWidth -
          parseFloat(findStyle.borderLeftWidth) -
          parseFloat(findStyle.borderRightWidth) -
          parseFloat(findStyle.paddingLeft) -
          parseFloat(findStyle.paddingRight),
        queryWidth: findText.measureText(findInput.value).width,
        clearAllowance: parseFloat(findStyle.fontSize),
        focused: document.activeElement === findInput,
        hit: hit(findInput),
        value: findInput.value,
      } : null,
      brand: box(brand),
      wordmark: box(wordmark),
      settings: box(settings),
      settingsHit: hit(settings),
      search: box(search),
      searchHit: hit(search),
      toggleCount: toggles.length,
      toggle: box(toggle),
      toggleHit: hit(toggle),
      toggleInBoard: Boolean(board && toggle && board.contains(toggle)),
      toggleInBrand: Boolean(brand && toggle && brand.contains(toggle)),
      toggleInTopbar: Boolean(topbar && toggle && topbar.contains(toggle)),
      glyphTransform: glyph ? getComputedStyle(glyph).transform : null,
      resizeHandle: box(handle),
      resizeHandleHit: hit(handle),
      range: handle ? {
        min: Number(handle.getAttribute('aria-valuemin')), max: Number(handle.getAttribute('aria-valuemax')),
        now: Number(handle.getAttribute('aria-valuenow')),
      } : null,
    };
  })()`;
  const open = await evaluate(send, snapState);
  const scrolledBoard = await evaluate(
    send,
    `(() => {
    const board = document.querySelector('[data-testid="board-column"]');
    if (!board) return null;
    const scroller = [board, ...board.querySelectorAll('*')].find(
      el => el.scrollHeight > el.clientHeight + 1 && ['auto', 'scroll'].includes(getComputedStyle(el).overflowY)
    );
    if (!scroller) return { scrolled: false };
    scroller.scrollTop = 200;
    const brand = document.querySelector('[data-testid="rail-brand"]');
    const toggle = document.querySelector('[data-testid="board-toggle"]');
    const handle = board.querySelector('[role="separator"]');
    const br = brand?.getBoundingClientRect();
    const tr = toggle?.getBoundingClientRect();
    const hr = handle?.getBoundingClientRect();
    const target = tr ? document.elementFromPoint(tr.left + tr.width / 2, tr.top + tr.height / 2) : null;
    return {
      scrolled: scroller.scrollTop > 0,
      scrollTop: scroller.scrollTop,
      brandTop: br?.top ?? null,
      toggleTop: tr?.top ?? null,
      toggleHit: Boolean(toggle && target && toggle.contains(target)),
      handleTop: hr?.top ?? null,
    };
  })()`,
  );
  const rowFocusSample = `(() => {
    const header = document.querySelector('[data-testid="rail-new-session"]')?.parentElement;
    const row = document.activeElement?.closest('[role="treeitem"]');
    const rows = row?.closest('[role="tree"]')?.querySelectorAll('[role="treeitem"]');
    const rect = row?.getBoundingClientRect();
    const target = rect && document.elementFromPoint(rect.x + rect.width / 2, rect.y + rect.height / 2);
    return { headerBottom: header?.getBoundingClientRect().bottom, rowTop: rect?.top, rowBottom: rect?.bottom,
      index: rows ? [...rows].indexOf(row) : -1,
      hit: Boolean(row && target && row.contains(target)), coveredByHeader: Boolean(target && header?.contains(target)) };
  })()`;
  const startedAtVisibleRow = await evaluate(send, `(() => {
    const header = document.querySelector('[data-testid="rail-new-session"]')?.parentElement;
    if (!header) return false;
    const bottom = header.getBoundingClientRect().bottom;
    for (const tree of document.querySelectorAll('[data-testid="board-column"] [role="tree"]')) {
      const rows = [...tree.querySelectorAll('[role="treeitem"]')];
      const index = rows.findIndex(row => row.getBoundingClientRect().top >= bottom);
      if (index <= 0 || rows[index].getBoundingClientRect().bottom >= innerHeight) continue;
      rows[index].focus({ preventScroll: true });
      return document.activeElement === rows[index];
    }
    return false;
  })()`);
  const beforeArrowUp = await evaluate(send, rowFocusSample);
  const arrowUpRows = [];
  for (let step = 0; startedAtVisibleRow && step < Math.min(5, beforeArrowUp.index); step++) {
    await pressKey(send, "ArrowUp", "ArrowUp", 38);
    arrowUpRows.push(await evaluate(send, rowFocusSample));
  }
  await evaluate(
    send,
    `(() => {
    const board = document.querySelector('[data-testid="board-column"]');
    const scroller = board && [board, ...board.querySelectorAll('*')].find(
      el => el.scrollHeight > el.clientHeight + 1 && ['auto', 'scroll'].includes(getComputedStyle(el).overflowY)
    );
    if (scroller) scroller.scrollTop = 0;
  })()`,
  );
  async function dragDivider(handle, x) {
    const cx = handle.left + handle.width / 2;
    const cy = handle.top + handle.height / 2;
    await send("Input.dispatchMouseEvent", { type: "mouseMoved", x: cx, y: cy });
    await send("Input.dispatchMouseEvent", { type: "mousePressed", x: cx, y: cy, button: "left", clickCount: 1 });
    await send("Input.dispatchMouseEvent", { type: "mouseMoved", x, y: cy, button: "left", buttons: 1 });
    await waitForDom(send, `(() => {
      const board = document.querySelector('[data-testid="board-column"]');
      return board?.getBoundingClientRect().width === Number(board?.querySelector('[role="separator"]')?.getAttribute('aria-valuenow'));
    })()`, "tablet drag width published before pointerup");
    await send("Input.dispatchMouseEvent", { type: "mouseReleased", x, y: cy, button: "left", clickCount: 1 });
    return evaluate(send, snapState);
  }
  let resized = null;
  if (open.resizeHandle && open.resizeHandle.width > 0 && open.resizeHandle.height > 0) {
    resized = await dragDivider(open.resizeHandle, open.resizeHandle.left + open.resizeHandle.width / 2 + 60);
    const resetHandle = resized.resizeHandle ?? open.resizeHandle;
    const rx = resetHandle.left + resetHandle.width / 2;
    const ry = resetHandle.top + resetHandle.height / 2;
    await send("Input.dispatchMouseEvent", { type: "mousePressed", x: rx, y: ry, button: "left", clickCount: 2 });
    await send("Input.dispatchMouseEvent", { type: "mouseReleased", x: rx, y: ry, button: "left", clickCount: 2 });
  }
  await evaluate(send, "window.overviewPane('other')");
  await waitForDom(
    send,
    `(() => {
      const scaffold = document.querySelector('[data-pane-scaffold="session:local:p0-s1"]');
      if (!scaffold || !document.querySelector('[data-session-overview-ref="local:p0-s1"][data-pane-id]')) return false;
      const scroller = [scaffold, ...scaffold.querySelectorAll('*')].find(
        el => el.scrollHeight > el.clientHeight + 80 && ['auto', 'scroll'].includes(getComputedStyle(el).overflowY)
      );
      return Boolean(scroller);
    })()`,
    "tablet split session pane mounted with scrollable transcript",
  );
  const paneScrollCapture = await evaluate(
    send,
    `(() => {
      const scaffold = document.querySelector('[data-pane-scaffold="session:local:p0-s1"]');
      const scroller = scaffold && [scaffold, ...scaffold.querySelectorAll('*')].find(
        el => el.scrollHeight > el.clientHeight + 80 && ['auto', 'scroll'].includes(getComputedStyle(el).overflowY)
      );
      if (!scroller) return null;
      const target = Math.min(180, Math.floor((scroller.scrollHeight - scroller.clientHeight) / 2));
      scroller.scrollTop = target;
      scroller.dispatchEvent(new Event('scroll'));
      window.__tabletPaneScroller = scroller;
      return { scrollTop: scroller.scrollTop, maxScroll: scroller.scrollHeight - scroller.clientHeight };
    })()`,
  );
  const readPaneScroll = `(() => {
    const scaffold = document.querySelector('[data-pane-scaffold="session:local:p0-s1"]');
    const scroller = scaffold && [scaffold, ...scaffold.querySelectorAll('*')].find(
      el => el.scrollHeight > el.clientHeight + 80 && ['auto', 'scroll'].includes(getComputedStyle(el).overflowY)
    );
    const saved = window.__tabletPaneScroller ?? null;
    return {
      sameNode: Boolean(saved && scroller && scroller === saved && saved.isConnected),
      scrollTop: scroller?.scrollTop ?? null,
      maxScroll: scroller ? scroller.scrollHeight - scroller.clientHeight : null,
    };
  })()`;
  const sessionOpen = await evaluate(send, snapState);
  await evaluate(send, `document.querySelector('[data-testid="board-toggle"]').focus({ preventScroll: true })`);
  await pressKey(send, "Enter", "Enter", 13);
  await waitForDom(send, "document.querySelector('[data-testid=\"board-split\"]')?.dataset.board === 'collapsed'", "tablet board collapsed");
  await settleFiniteAnimations(send);
  const collapsed = await evaluate(send, snapState);
  const paneScrollCollapsed = await evaluate(send, readPaneScroll);
  const focusedToggle = `document.activeElement === document.querySelector('[data-testid="board-toggle"]')`;
  if (!(await evaluate(send, focusedToggle))) throw new Error("collapsing the tablet board lost keyboard focus");
  await pressKey(send, "Enter", "Enter", 13);
  await waitForDom(send, "document.querySelector('[data-testid=\"board-split\"]')?.dataset.board === 'visible'", "tablet board re-expanded");
  await settleFiniteAnimations(send);
  const reopened = await evaluate(send, snapState);
  const paneScrollReopened = await evaluate(send, readPaneScroll);
  if (!(await evaluate(send, focusedToggle))) throw new Error("expanding the tablet board lost keyboard focus");
  await send("Emulation.setTouchEmulationEnabled", { enabled: false });
  await applyViewport(send, { width: 768, height: TABLET_VIEWPORT.height });
  await waitForDom(send, "innerWidth === 768", "768px tablet viewport");
  const pointer = await evaluate(send, `({ fine: matchMedia('(pointer: fine)').matches, coarse: matchMedia('(any-pointer: coarse)').matches })`);
  const narrowest = await dragDivider(sessionOpen.resizeHandle, 1);
  const wideDrag = await dragDivider(narrowest.resizeHandle, 700);
  await evaluate(send, `document.querySelector('[data-testid="board-column"] [role="separator"]').focus({ preventScroll: true })`);
  await pressKey(send, "Home", "Home", 36);
  const home = await evaluate(send, snapState);
  await pressKey(send, "End", "End", 35);
  await clickControl(send, '[data-testid="topbar"] button[aria-label="Find in session"]');
  await waitForDom(send, `document.activeElement?.matches('[data-find-bar] input[type="search"]')`, "Find query focus at maximum board width");
  const findQuery = "session";
  await send("Input.insertText", { text: findQuery });
  await waitForDom(send, `document.querySelector('[data-find-bar]:not([data-find-status="idle"])')`, "Find query result at maximum board width");
  const widest = await evaluate(send, snapState);
  const largerText = [];
  for (const font of ["l", "xl"]) {
    await evaluate(send, `window.configureOverview('light', '${font}')`);
    await waitForFonts(send);
    largerText.push([font, await evaluate(send, snapState)]);
  }
  await evaluate(send, `window.configureOverview('light', 'm')`);
  await pressKey(send, "Escape", "Escape", 27);
  await applyViewport(send, { ...MOBILE_VIEWPORT, width: 400 });
  await waitForDom(send, `innerWidth === 400 && !document.querySelector('[data-testid="board-split"]')`,
    "400px uses the phone stack without the tablet board");
  await settleFiniteAnimations(send);
  const paneScrollPhone = await evaluate(send, readPaneScroll);
  const desktopDivider = '[data-testid="rail"] [data-testid="rail-resize-handle"]';
  const desktopReady = `innerWidth === 1024 && !document.querySelector('[data-stacked-pane-type]') && document.querySelector('${desktopDivider}')`;
  const desktopViewport = { ...TOUCH_DESKTOP_VIEWPORT, width: 1024, height: 768 };
  const desktopMeasure = `(() => {
    const divider = document.querySelector('${desktopDivider}');
    return { width: divider.closest('[data-testid="rail"]').getBoundingClientRect().width,
      max: Number(divider.getAttribute('aria-valuemax')) };
  })()`;
  await applyViewport(send, desktopViewport);
  await waitForDom(send, desktopReady, "1024px uses the docked desktop rail without the tablet board");
  await evaluate(send, `document.querySelector('${desktopDivider}').focus({ preventScroll: true })`);
  await pressKey(send, "End", "End", 35);
  const desktopBefore = await evaluate(send, desktopMeasure);
  await applyViewport(send, { ...TABLET_VIEWPORT, width: 768 });
  await waitForDom(send, `innerWidth === 768 && document.querySelector('[data-testid="board-column"]')`, "narrow tablet restores the board");
  const narrowRestored = await evaluate(send, snapState);
  const roomyViewport = { ...TABLET_VIEWPORT, width: 899 };
  await applyViewport(send, roomyViewport);
  await waitForDom(send, `innerWidth === 899`, "wider tablet viewport");
  await settleFiniteAnimations(send);
  const roomy = await evaluate(send, snapState);
  await applyViewport(send, desktopViewport);
  await waitForDom(send, desktopReady, "desktop restores the docked sidebar");
  const desktopAfter = await evaluate(send, desktopMeasure);
  await evaluate(send, `document.querySelector('${desktopDivider}').focus({ preventScroll: true })`);
  for (let step = 0; step < 4; step++) await pressKey(send, "ArrowLeft", "ArrowLeft", 37);
  const desktopFits = await evaluate(send, desktopMeasure);
  await applyViewport(send, roomyViewport);
  await waitForDom(send, `innerWidth === 899 && document.querySelector('[data-testid="board-column"]')`, "wide tablet restores a fitting desktop width");
  const roomyFits = await evaluate(send, snapState);
  return { open, scrolledBoard, resized, collapsed, reopened, sessionOpen,
    paneScrollCapture, paneScrollCollapsed, paneScrollReopened, paneScrollPhone,
    beforeArrowUp, arrowUpRows, pointer, narrowest, home, wideDrag, widest, findQuery, largerText,
    desktopBefore, narrowRestored, roomy, desktopAfter, desktopFits, roomyFits };
}

function assertTabletSplit(result) {
  const failures = [];
  const { open, scrolledBoard, resized, collapsed, reopened } = result;
  const centerY = (box) => (box.top + box.bottom) / 2;
  if (!open.board || !open.topbar || !open.title || !open.toggle) {
    failures.push(`tablet split open missing required nodes: ${JSON.stringify(open)}`);
    return failures;
  }
  if (Math.abs(open.board.top) > 1 || Math.abs(open.board.bottom - TABLET_VIEWPORT.height) > 1) {
    failures.push(`tablet board does not span full height from y=0: ${JSON.stringify(open.board)}`);
  }
  if (!open.topbarInPane || open.topbar.left < open.board.right - 1 || Math.abs(open.topbar.top - open.board.top) > 1) {
    failures.push(`tablet topbar is not beside board at y=0: board=${JSON.stringify(open.board)} topbar=${JSON.stringify(open.topbar)}`);
  }
  if (open.title.left < open.board.right - 1 || open.title.right > open.pane.right + 1 || open.title.width <= 0) {
    failures.push(`tablet title is not beside board: board=${JSON.stringify(open.board)} title=${JSON.stringify(open.title)}`);
  }
  if (open.toggleCount !== 1 || !open.toggleInBoard || !open.toggleInBrand || open.toggleInTopbar) {
    failures.push(`tablet open toggle must sit inside rail brand only: ${JSON.stringify({ count: open.toggleCount, inBoard: open.toggleInBoard, inBrand: open.toggleInBrand, inTopbar: open.toggleInTopbar })}`);
  }
  if (open.search.right > open.toggle.left + 0.5 || open.toggle.right > open.board.right + 0.5 || Math.abs(centerY(open.search) - centerY(open.toggle)) > 1.5) {
    failures.push(`tablet open toggle misaligned or overlapping board header: search=${JSON.stringify(open.search)} toggle=${JSON.stringify(open.toggle)} board=${JSON.stringify(open.board)}`);
  }
  if (!open.settingsHit || !open.searchHit || !open.toggleHit) {
    failures.push(`tablet open header control failed hit-test: ${JSON.stringify({ settings: open.settingsHit, search: open.searchHit, toggle: open.toggleHit })}`);
  }
  if (open.toggle.width < 44 || open.toggle.height < 44) {
    failures.push(`tablet toggle below 44px touch floor: ${JSON.stringify(open.toggle)}`);
  }
  if (!open.resizeHandle || Math.abs(open.resizeHandle.right - open.board.right) > 2 || open.resizeHandle.left < open.board.right - 20 || !open.resizeHandleHit) {
    failures.push(`tablet divider resize handle not hit-testable at board seam: board=${JSON.stringify(open.board)} handle=${JSON.stringify(open.resizeHandle)} hit=${open.resizeHandleHit}`);
  }
  if (!scrolledBoard?.scrolled || scrolledBoard.brandTop === null || Math.abs(scrolledBoard.brandTop - open.brand.top) > 1 || !scrolledBoard.toggleHit) {
    failures.push(`tablet board header did not stay sticky on scroll: openBrand=${JSON.stringify(open.brand)} scrolled=${JSON.stringify(scrolledBoard)}`);
  }
  if (scrolledBoard?.handleTop !== null && Math.abs(scrolledBoard.handleTop - open.resizeHandle.top) > 1) {
    failures.push(`tablet resize handle scrolled off seam top: open=${open.resizeHandle.top} scrolled=${scrolledBoard.handleTop}`);
  }
  if (!resized?.board || resized.board.width <= open.board.width + 20 || !resized.toggleInBrand || !resized.toggleHit) {
    failures.push(`tablet divider drag did not widen board cleanly: before=${open.board.width} after=${resized?.board?.width}`);
  }
  if (collapsed.board !== null || collapsed.dataBoard !== "collapsed" || collapsed.toggleCount !== 1 || !collapsed.toggleInTopbar) {
    failures.push(`tablet collapsed toggle must move into topbar leading edge: ${JSON.stringify(collapsed)}`);
  }
  if (!collapsed.toggle || collapsed.toggle.left < collapsed.pane.left - 1 || collapsed.toggle.left > collapsed.pane.left + 20 || collapsed.toggle.right > collapsed.title.left + 0.5 || !collapsed.toggleHit || collapsed.title.height <= 0) {
    failures.push(`tablet collapsed toggle overlaps or escapes leading conversation edge: ${JSON.stringify({ toggle: collapsed.toggle, title: collapsed.title, toggleHit: collapsed.toggleHit })}`);
  }
  if (collapsed.glyphTransform !== "matrix(-1, 0, 0, -1, 0, 0)") {
    failures.push(`tablet collapsed glyph did not rotate 180deg: ${collapsed.glyphTransform}`);
  }
  if (!reopened.board || reopened.dataBoard !== "visible" || !reopened.toggleInBrand || !reopened.toggleHit) {
    failures.push(`tablet re-expanded board did not return toggle to board header: ${JSON.stringify(reopened)}`);
  } else if (Math.abs(reopened.board.width - open.board.width) > 1) {
    failures.push(`tablet divider reset did not restore the initial width: ${reopened.board.width} vs ${open.board.width}`);
  }
  if (!result.sessionOpen?.topbarInPane || !result.sessionOpen.overviewHit || result.sessionOpen.topbar.left < result.sessionOpen.board.right - 1) {
    failures.push(`tablet session pane topbar/overview geometry failed: ${JSON.stringify(result.sessionOpen)}`);
  }
  const { paneScrollCapture, paneScrollCollapsed, paneScrollReopened, paneScrollPhone } = result;
  if (!paneScrollCapture || paneScrollCapture.scrollTop <= 0) {
    failures.push(`tablet session pane did not scroll before layout changes: ${JSON.stringify(paneScrollCapture)}`);
  }
  for (const [phase, sample] of [
    ["collapsed", paneScrollCollapsed],
    ["reopened", paneScrollReopened],
    ["phone", paneScrollPhone],
  ]) {
    if (!sample?.sameNode || sample.scrollTop === null || sample.scrollTop <= 0) {
      failures.push(`tablet ${phase} remounted the active session scroller or lost its reading position: ${JSON.stringify({ capture: paneScrollCapture, sample })}`);
    }
  }
  const { beforeArrowUp, arrowUpRows, narrowest, widest, pointer, findQuery } = result;
  if (!beforeArrowUp.hit || !arrowUpRows.length || arrowUpRows.some((row, i) => row.index !== beforeArrowUp.index - i - 1 || !row.hit || row.coveredByHeader)) {
    failures.push(`tablet ArrowUp focus is not visible around the board header: ${JSON.stringify({ beforeArrowUp, arrowUpRows })}`);
  }
  for (const [label, sample, edge] of [
    ["minimum drag", narrowest, "min"], ["Home", result.home, "min"],
    ["maximum drag", result.wideDrag, "max"], ["End", widest, "max"],
  ]) {
    if (sample.board.width !== sample.range?.[edge] || sample.range.now !== sample.range[edge]) {
      failures.push(`tablet divider ${label} did not reach the advertised limit: ${JSON.stringify({ range: sample.range, width: sample.board.width })}`);
    }
  }
  if (pointer.coarse || !narrowest.settingsHit || !narrowest.searchHit || !narrowest.toggleHit || narrowest.toggle?.right > narrowest.board.right) {
    failures.push(`tablet minimum divider hides a header control: ${JSON.stringify({ pointer, board: narrowest.board, toggle: narrowest.toggle, toggleHit: narrowest.toggleHit })}`);
  }
  for (const [font, sample] of [["m", widest], ...result.largerText]) {
    const { board, pane, range, find } = sample;
    if (board.width !== range?.max || !find?.focused || !find.hit || find.value !== findQuery ||
        find.editableWidth < find.queryWidth + find.clearAllowance) {
      failures.push(`tablet maximum divider starves the focused Find query at font ${font}: ${JSON.stringify({ board, pane, range, find })}`);
    }
  }
  const { desktopBefore, narrowRestored, roomy, desktopAfter, desktopFits, roomyFits } = result;
  if (desktopBefore.width !== desktopBefore.max || narrowRestored.board.width !== widest.board.width ||
      roomy.pane.width !== narrowRestored.pane.width || desktopAfter.width !== desktopBefore.width ||
      desktopFits.width >= roomy.range.max || roomyFits.board.width !== desktopFits.width) {
    failures.push(`tablet did not show the usable part of the saved desktop width: ${JSON.stringify({ desktopBefore,
      narrow: narrowRestored.board.width, roomy: roomy.board.width, desktopAfter, desktopFits, roomyFits: roomyFits.board.width })}`);
  }
  for (const [label, sample] of [["open", open], ["collapsed", collapsed]]) {
    if (sample.document.scrollWidth > TABLET_VIEWPORT.width + 1 || sample.document.scrollHeight > TABLET_VIEWPORT.height + 1) {
      failures.push(`tablet ${label} document overflow: ${JSON.stringify(sample.document)}`);
    }
  }
  return failures;
}

const OVERVIEW_VIEWPORTS = [VIEWPORT, MOBILE_VIEWPORT, { width: 320, height: 844, mobile: true, touch: true }];
const OVERVIEW_THEMES = ["light", "dark"];
const OVERVIEW = '[data-testid="activity-sidebar"][aria-label="Overview"]';
const OVERVIEW_SELECTED_TAB = '[role="tab"][aria-selected="true"]';
const overviewTabSelector = (labelFilter) => `${OVERVIEW} [role="tab"]${labelFilter}`;
// The Overview sweep's tall task list renders this many live rows (40-row
// fixture, every fourth settled into the collapsed fold). The wait gate and
// the fixture-sanity assert below share it, so resizing the fixture cannot
// desync them into an unexplained wait timeout.
const TALL_TASK_ROWS = 30;
const RAIL_ACTION = '[data-session-actions-ref="local:p0-s0"]:not([data-pane-id])';
const LONG_VALUES = [
  `anthropic/${"modelidentifier".repeat(12)}`,
  "sessionidentifier".repeat(12),
  `feature/${"branchidentifier".repeat(12)}`,
  `/work/${"directorysegment".repeat(12)}/session`,
  `https://artifacts.example.test/p/${"linksegment".repeat(8)}/final-suffix`,
];

async function waitForDom(send, expression, label) {
  // Presence predicates can return DOM nodes, which must not cross CDP by value.
  return waitForValue(send, `Boolean(${expression})`, label);
}

async function waitForValue(send, expression, label) {
  return evaluate(
    send,
    `(async () => {
    const deadline = performance.now() + 15000;
    let value;
    while (!(value = (${expression}))) {
      if (performance.now() > deadline) throw new Error(${JSON.stringify(label)} + ': ' + document.body.innerText.slice(-1500));
      await new Promise(resolve => requestAnimationFrame(resolve));
    }
    return value;
  })()`,
  );
}

// The guard's settle signal for animated surfaces (the menu popup fades out
// through usePresence): the browser's own animation-finished promises, never a
// fixed sleep.
async function settleFiniteAnimations(send) {
  await evaluate(
    send,
    `(async () => {
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
  })()`,
  );
}

async function settleOverview(send) {
  await waitForFonts(send);
  await settleFiniteAnimations(send);
  await waitForDom(
    send,
    `(() => {
    const aside = document.querySelector(${JSON.stringify(OVERVIEW)});
    if (!aside) return true;
    const box = aside.getBoundingClientRect();
    // A sliding Overview that overflows the page gives Linux Chrome classic
    // scrollbars, which can outlive the slide and later vanish under a press.
    const page = document.documentElement;
    if (page.scrollWidth > innerWidth + 1 || page.scrollHeight > innerHeight + 1) {
      throw new Error('Overview entrance overflows the page: ' + JSON.stringify({
        scrollWidth: page.scrollWidth, scrollHeight: page.scrollHeight, aside: box.toJSON() }));
    }
    return box.right <= innerWidth + 1 && box.left >= -1;
  })()`,
    "Overview finishes its entrance",
  );
}

export async function clickControl(send, selector) {
  await waitForDom(send, `document.querySelector(${JSON.stringify(selector)})`, `mounted control ${selector}`);
  let point = await evaluate(
    send,
    `(async () => {
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
  })()`,
  );
  await send("Input.dispatchMouseEvent", { type: "mouseMoved", ...point });
  try {
    point = await waitForValue(
      send,
      `(() => {
      const marker = document.querySelector(${JSON.stringify(selector)});
      const element = marker?.closest('button') ?? marker;
      if (!element || getComputedStyle(element).visibility !== 'visible') return false;
      const box = element.getBoundingClientRect();
      const current = { x: box.left + box.width / 2, y: box.top + box.height / 2 };
      return box.width && box.height && element.contains(document.elementFromPoint(current.x, current.y)) && current;
    })()`,
      `hit-testable control ${selector}`,
    );
  } catch (error) {
    const witness = await evaluate(
      send,
      `(() => {
      const marker = document.querySelector(${JSON.stringify(selector)});
      const element = marker?.closest('button') ?? marker;
      const box = element?.getBoundingClientRect();
      return { point: ${JSON.stringify(point)}, box: box?.toJSON(),
        hit: document.elementFromPoint(${point.x}, ${point.y})?.outerHTML.slice(0, 500),
        currentHit: box && document.elementFromPoint(box.left + box.width / 2, box.top + box.height / 2)?.outerHTML.slice(0, 500),
        visibility: element && getComputedStyle(element).visibility, state: window.overviewGuardState() };
    })()`,
    );
    throw new Error(`${error.message} witness: ${JSON.stringify(witness)}`);
  }
  await send("Input.dispatchMouseEvent", { type: "mousePressed", ...point, button: "left", clickCount: 1 });
  await send("Input.dispatchMouseEvent", { type: "mouseReleased", ...point, button: "left", clickCount: 1 });
}

async function pressKey(send, key, code, keyCode, modifiers = 0) {
  for (const type of ["keyDown", "keyUp"]) {
    await send("Input.dispatchKeyEvent", {
      type,
      key,
      code,
      windowsVirtualKeyCode: keyCode,
      modifiers,
      ...(type === "keyDown" && key === "Enter" && modifiers === 0 ? { text: "\r" } : {}),
    });
  }
}

async function chooseOverviewMenu(send, selector) {
  await clickControl(send, selector);
  await waitForDom(
    send,
    `Array.from(document.querySelectorAll('[role="menuitem"]')).some(e => e.textContent.includes('Overview'))`,
    "Overview menu item",
  );
  await evaluate(
    send,
    `(() => {
    document.querySelector('[data-overview-guard-item]')?.removeAttribute('data-overview-guard-item');
    const item = Array.from(document.querySelectorAll('[role="menuitem"]')).find(e => e.textContent.includes('Overview'));
    item.dataset.overviewGuardItem = '';
  })()`,
  );
  await clickControl(send, "[data-overview-guard-item]");
  await waitForDom(send, `document.querySelector(${JSON.stringify(OVERVIEW)})`, "opened Overview");
  await settleOverview(send);
}

async function choosePaneOverview(send, selector, mobile = false, measure = false) {
  let headerFind;
  if (mobile && measure) {
    await waitForDom(send, `document.querySelector(${JSON.stringify(selector)})`, "mounted phone Overview trigger");
    headerFind = await evaluate(send, `(() => {
      const find = document.querySelector('[data-testid="topbar"] button[aria-label="Find in session"]');
      const overview = document.querySelector(${JSON.stringify(selector)})?.closest('button');
      if (!find || !overview) return { present: false };
      const tapMin = parseFloat(getComputedStyle(overview).getPropertyValue('--tap-min'));
      const findBox = find.getBoundingClientRect();
      const overviewBox = overview.getBoundingClientRect();
      const hit = (box, el) => el.contains(document.elementFromPoint(box.left + box.width / 2, box.top + box.height / 2));
      const fits = (box, el) =>
        box.width >= tapMin - 1 && box.height >= tapMin - 1 && box.left >= -1 && box.right <= innerWidth + 1 && hit(box, el);
      return {
        present: true,
        tapMin,
        findFits: fits(findBox, find),
        overviewFits: fits(overviewBox, overview),
        findBox: findBox.toJSON(),
        overviewBox: overviewBox.toJSON(),
      };
    })()`);
    if (!headerFind.present || !headerFind.findFits || !headerFind.overviewFits) {
      throw new Error(`phone session header misses Find/Overview tap geometry: ${JSON.stringify(headerFind)}`);
    }
    await evaluate(send, `document.querySelector(${JSON.stringify(selector)}).closest('button').focus({ preventScroll: true })`);
    await pressKey(send, "Enter", "Enter", 13);
  } else {
    await clickControl(send, selector);
  }
  await waitForDom(send, `document.querySelector(${JSON.stringify(OVERVIEW)})`, "opened pane Overview");
  await settleOverview(send);
  let inspection;
  if (mobile && measure) {
    inspection = await evaluate(send, `(() => {
      const aside = document.querySelector(${JSON.stringify(OVERVIEW)});
      const rect = aside.getBoundingClientRect();
      const active = document.activeElement;
      const focus = active.getBoundingClientRect();
      const hit = document.elementFromPoint(focus.left + focus.width / 2, focus.top + focus.height / 2);
      const initialFocus = {
        visible: aside.contains(active) && active.matches(':focus-visible') && active.contains(hit),
        box: focus.toJSON(),
        aside: rect.toJSON(),
      };
      const tapMin = parseFloat(getComputedStyle(aside).getPropertyValue('--tap-min'));
      const tabs = [...aside.querySelectorAll('[role="tablist"] [role="tab"]')].map(element => {
        const box = element.getBoundingClientRect();
        return {
          label: element.firstElementChild?.textContent?.trim() ?? element.textContent?.trim() ?? '',
          selected: element.getAttribute('aria-selected') === 'true',
          fits: box.height >= tapMin - 1 && box.width >= tapMin - 1,
        };
      });
      return {
        headerFind: ${JSON.stringify(headerFind)},
        initialFocus,
        tabs,
        fits: rect.left >= -1 && rect.right <= innerWidth + 1 && rect.top >= -1 && rect.bottom <= innerHeight + 1,
      };
    })()`);
    if (!inspection.initialFocus.visible) {
      throw new Error(`phone Overview starts with clipped keyboard focus: ${JSON.stringify(inspection.initialFocus)}`);
    }
    if (
      !inspection.fits ||
      JSON.stringify(inspection.tabs.map((tab) => tab.label)) !==
        JSON.stringify(["About", "Agents", "Jobs", "Watches", "Tasks"]) ||
      inspection.tabs.some((tab) => !tab.fits) ||
      !inspection.tabs[0]?.selected
    ) {
      throw new Error(`phone Overview misses its underline tablist geometry: ${JSON.stringify(inspection)}`);
    }
  }
  return inspection;
}

async function openRailOverview(send) {
  const visible = await evaluate(
    send,
    `(() => {
    const marker = document.querySelector(${JSON.stringify(RAIL_ACTION)});
    const button = marker?.closest('button');
    return { found: !!marker, width: button?.getBoundingClientRect().width,
      visibility: button && getComputedStyle(button).visibility,
      refs: Array.from(document.querySelectorAll('[data-session-actions-ref]')).slice(0, 3).map(e => e.outerHTML) };
  })()`,
  );
  console.log(`Overview rail trigger: ${JSON.stringify(visible)}`);
  if (!visible.width) await clickControl(send, 'button[aria-label="Sessions"]');
  await settleOverview(send);
  await chooseOverviewMenu(send, RAIL_ACTION);
  await waitForDom(
    send,
    `document.querySelector('[data-session-actions-ref="local:p0-s0"][data-pane-id]')`,
    "hydrated receiving Session",
  );
  await settleOverview(send);
  await clickControl(send, overviewTabSelector('[aria-label="About"]'));
  await waitForDom(
    send,
    `window.overviewGuardState().overview.tab === 'about' && document.querySelector(${JSON.stringify(OVERVIEW)})?.textContent.includes(${JSON.stringify(LONG_VALUES[1])})`,
    "hydrated About",
  );
  await settleOverview(send);
}

// Every text rect is compared with every inline clipping ancestor. Selection
// alone can include invisible text and document overflow alone misses ellipsis.
function fullTextMeasurement(element, expected) {
  if (!element) return { missing: true, expected };
  element.scrollIntoView({ block: "nearest", inline: "nearest" });
  const range = document.createRange();
  range.selectNodeContents(element);
  const textRects = Array.from(range.getClientRects(), (r) => ({
    left: r.left,
    right: r.right,
    top: r.top,
    bottom: r.bottom,
  }));
  const clips = [];
  const scrollContainers = [];
  const unselectable = [];
  for (let node = element; node; node = node.parentElement) {
    const style = getComputedStyle(node);
    const box = node.getBoundingClientRect();
    if (style.userSelect === "none") unselectable.push(node.tagName);
    if (
      ["hidden", "clip"].includes(style.overflowX) &&
      textRects.some((r) => r.left < box.left - 1 || r.right > box.right + 1)
    ) {
      clips.push({ tag: node.tagName, class: node.className, left: box.left, right: box.right });
    }
    if (["auto", "scroll"].includes(style.overflowX) && node.scrollWidth > node.clientWidth + 1) {
      const previous = node.scrollLeft;
      node.scrollLeft = node.scrollWidth;
      scrollContainers.push({
        clientWidth: node.clientWidth,
        scrollWidth: node.scrollWidth,
        reachable: node.scrollLeft,
      });
      node.scrollLeft = previous;
    }
  }
  const selection = window.getSelection();
  selection.removeAllRanges();
  selection.addRange(range);
  const selectable = selection.toString() === expected;
  selection.removeAllRanges();
  const surface = element.closest('[data-testid="activity-sidebar"]').getBoundingClientRect();
  const inlineOverflow = textRects.some((r) => r.left < surface.left - 1 || r.right > surface.right + 1);
  return {
    fullText: element.textContent === expected,
    selectable,
    clips,
    scrollContainers,
    unselectable,
    textRects,
    inlineOverflow,
  };
}

async function measureOverview(send) {
  return evaluate(
    send,
    `(async () => {
    const aside = document.querySelector(${JSON.stringify(OVERVIEW)});
    if (!aside) throw new Error('Overview never rendered');
    const measure = ${fullTextMeasurement.toString()};
    const box = element => {
      const r = element.getBoundingClientRect();
      return { left: r.left, right: r.right, top: r.top, bottom: r.bottom, width: r.width, height: r.height };
    };
    const mobile = matchMedia('(max-width: 899px)').matches;
    const tablist = aside.querySelector('[role="tablist"]');
    const tablistScrollsX = Boolean(tablist && tablist.scrollWidth > tablist.clientWidth + 1);
    const tabElements = Array.from(aside.querySelectorAll('[role="tab"]'));
    // Capture resting boxes before fullTextMeasurement calls scrollIntoView.
    const initialTabBoxes = tabElements.map(box);
    // A category is a label plus optional detail in the shared underline
    // tablist; each piece must show its whole text.
    const categories = tabElements.map((element, index) => {
      const lines = Array.from(element.children);
      return {
        text: lines.map(line => line.textContent).join('\\n'),
        checked: element.getAttribute('aria-selected'),
        box: initialTabBoxes[index],
        label: measure(lines[0], lines[0]?.textContent),
        detail: lines[1] ? measure(lines[1], lines[1].textContent) : null,
      };
    });
    const values = ${JSON.stringify(LONG_VALUES)}.map(expected => {
      const element = Array.from(aside.querySelectorAll('*')).find(e => e.textContent === expected &&
        !Array.from(e.children).some(child => child.textContent === expected));
      return measure(element, expected);
    });
    const close = aside.querySelector('button[aria-label="Close Overview"]');
    const surface = mobile ? null : Array.from(document.querySelectorAll('[data-testid="statusbar"]'))
      .find(e => !e.closest('[data-pane-footer-fixture]')) ?? null;
    const chips = surface ? Array.from(surface.querySelectorAll('[data-activity-tab]')) : [];
    return {
      theme: document.documentElement.dataset.theme, font: document.body.dataset.fontSize,
      sidebar: box(aside), tablistScrollsX, categories, values, close: close && box(close),
      tapMin: parseFloat(getComputedStyle(aside).getPropertyValue('--tap-min')),
      footerTabs: surface ? [chips.map(button => button.dataset.activityTab)] : [],
      document: { width: document.documentElement.scrollWidth, height: document.documentElement.scrollHeight },
      errors: window.__shellGuardErrors || [], state: window.overviewGuardState()
    };
  })()`,
  );
}

// Shared into the page evals below via .toString(), like fullTextMeasurement:
// every scroll container under the Overview, in document order. The viewport
// finders below want ActivityViewport's own body div - the first DIV in the
// list; a non-div scroller can sit earlier (the breadcrumb strip's
// overflow-x hidden pairs its overflow-y to computed auto), so they must keep
// the tagName filter, while the pre-screenshot reset walks the whole list.
function overviewScrollContainers(root) {
  return Array.from(root.querySelectorAll("*")).filter((el) =>
    ["auto", "scroll"].includes(getComputedStyle(el).overflowY),
  );
}

// The Tasks tab over a list taller than the viewport (the harness's
// evener/tasks/list fixture answers with 40 rows): the same page-height
// contract the rail scenario asserts, extended to the Overview's own tab
// bodies. The tab's scroll container must own the tall list, and the
// document must not grow: a tall task list scrolling the whole page frame
// is the bug this step exists to catch.
async function measureOverviewTasks(send) {
  await clickControl(send, overviewTabSelector('[aria-label^="Tasks"]'));
  await waitForDom(
    send,
    `document.querySelectorAll('[data-testid="task-row"]').length >= ${TALL_TASK_ROWS}`,
    "the Overview's tall task list rendered",
  );
  await settleOverview(send);
  return evaluate(
    send,
    `(() => {
    const overviewScrollContainers = ${overviewScrollContainers.toString()};
    const de = document.documentElement;
    const aside = document.querySelector(${JSON.stringify(OVERVIEW)});
    const scroller = overviewScrollContainers(aside).find((el) => el.tagName === "DIV");
    const rows = document.querySelectorAll('[data-testid="task-row"]').length;
    return {
      rows,
      scroller: scroller && {
        clientHeight: scroller.clientHeight,
        scrollHeight: scroller.scrollHeight,
        scrollTop: scroller.scrollTop,
      },
      document: { height: de.scrollHeight, width: de.scrollWidth },
    };
  })()`,
  );
}

function assertOverviewTasks(result, viewport) {
  const failures = [];
  // Fixture sanity: the tall list really rendered, so "no page overflow" is
  // a measurement of the loaded tab, not of an empty one that never fetched.
  if (result.rows < TALL_TASK_ROWS) {
    failures.push(`expected a tall task list in the Overview, found ${result.rows} rows`);
  }
  // The list must actually overflow the tab, so the document check below is
  // a real measurement: a list shorter than the viewport would make it vacuous.
  if (!result.scroller || result.scroller.scrollHeight <= result.scroller.clientHeight + 1) {
    failures.push(`the Overview tab body is not scrolling its task list: ${JSON.stringify(result.scroller)}`);
  }
  if (result.document.height > viewport.height + 1) {
    failures.push(
      `the task list set the page's height: document ${result.document.height}px in a ${viewport.height}px viewport - ` +
        "the Overview's tab scroller, not the page frame, must own the tall list",
    );
  }
  if (result.document.width > viewport.width + 1) {
    failures.push(
      `the task list set the page's width: document ${result.document.width}px in a ${viewport.width}px viewport`,
    );
  }
  return failures;
}

function assertOverview(result, viewport, theme) {
  const failures = [];
  const labels = ["About", "Agents\n100/100", "Jobs\n100/100", "Watches\n100/100", "Tasks\n100/100"];
  if (result.theme !== theme || result.font !== "xl") failures.push("XL/theme preferences did not apply");
  if (!viewport.mobile && Math.abs(result.sidebar.width - 320) > 1)
    failures.push(`desktop sidebar width ${result.sidebar.width}`);
  if (result.sidebar.left < -1 || result.sidebar.right > viewport.width + 1) failures.push("sidebar escapes viewport");
  if (
    !result.close ||
    result.close.left < -1 ||
    result.close.right > viewport.width + 1 ||
    result.close.top < -1 ||
    result.close.bottom > viewport.height + 1
  )
    failures.push(`close control escapes viewport: ${JSON.stringify(result.close)}`);
  // Desktop activity retains its own pane footer. The trusted phone path
  // measures those rows at their replacement owner, the inspection sheet.
  if (
    !viewport.mobile && (result.footerTabs.length !== 1 ||
      JSON.stringify(result.footerTabs[0]) !== JSON.stringify(["agents", "jobs", "watches", "tasks"]))
  )
    failures.push(`actual pane footer identities ${JSON.stringify(result.footerTabs)}`);
  if (JSON.stringify(result.categories.map((c) => c.text)) !== JSON.stringify(labels))
    failures.push(`categories/counts ${JSON.stringify(result.categories.map((c) => c.text))}`);
  if (
    result.categories
      .filter((c) => c.checked === "true")
      .map((c) => c.text)
      .join() !== "About"
  )
    failures.push("About is not exclusively selected");
  if (!viewport.mobile && result.tablistScrollsX)
    failures.push("desktop Overview tablist overflows horizontally instead of keeping every tab visible");
  for (const category of result.categories) {
    if (
      !viewport.mobile &&
      (category.box.left < result.sidebar.left - 1 || category.box.right > result.sidebar.right + 1)
    )
      failures.push(
        `desktop category ${category.text} sits outside the sidebar: ${JSON.stringify(category.box)} vs ${JSON.stringify(result.sidebar)}`,
      );
    for (const line of [category.label, category.detail]) {
      if (line === null) continue;
      if (
        !line.fullText ||
        line.clips.length ||
        line.inlineOverflow ||
        !line.textRects.length ||
        category.box.width > result.sidebar.width + 1
      )
        failures.push(`clipped category ${category.text}: ${JSON.stringify(line)}`);
    }
  }
  for (let i = 0; i < result.values.length; i++) {
    const value = result.values[i];
    if (
      !value.fullText ||
      !value.selectable ||
      value.inlineOverflow ||
      value.clips?.length ||
      value.unselectable?.length ||
      value.scrollContainers?.length ||
      !value.textRects?.length
    )
      failures.push(`unreadable ${["model", "session ID", "branch", "path", "saved link URL"][i]}: ${JSON.stringify(value)}`);
  }
  if (viewport.mobile) {
    if (!(result.tapMin >= 44)) failures.push(`missing phone tap floor: ${result.tapMin}`);
    for (const control of [...result.categories.map((c) => c.box), result.close]) {
      if (!control || control.width < result.tapMin - 1 || control.height < result.tapMin - 1)
        failures.push(`sub-floor Overview target: ${JSON.stringify(control)}`);
    }
  }
  if (result.document.width > viewport.width + 1 || result.document.height > viewport.height + 1)
    failures.push(`page overflow ${JSON.stringify(result.document)}`);
  return failures;
}

async function trustedOverviewFlow(send, viewport, childGesture) {
  const pane = await evaluate(
    send,
    "window.overviewGuardState().panes.find(p => p.type === 'session' && p.params.ref === 'local:p0-s0').id",
  );
  const trigger = (id) => `[data-session-actions-ref="local:p0-s0"][data-pane-id="${id}"]`;
  const overviewTrigger = (id, ref = "local:p0-s0") => `[data-session-overview-ref="${ref}"][data-pane-id="${id}"]`;
  const expectedTabRole = "tab";
  const expectedSelectedAttr = "aria-selected";
  await evaluate(
    send,
    `document.querySelector(${JSON.stringify(OVERVIEW)}).querySelector(${JSON.stringify(OVERVIEW_SELECTED_TAB)}).focus()`,
  );
  for (const [key, code, keyCode, tab] of [
    ["End", "End", 35, "tasks"],
    ["Home", "Home", 36, "about"],
    ["ArrowRight", "ArrowRight", 39, "agents"],
    ["ArrowLeft", "ArrowLeft", 37, "about"],
  ]) {
    await pressKey(send, key, code, keyCode);
    await waitForDom(
      send,
      `window.overviewGuardState().overview.tab === ${JSON.stringify(tab)}`,
      `${key} selects ${tab}`,
    );
    const focus = await evaluate(
      send,
      `(() => {
      const active = document.activeElement;
      const style = getComputedStyle(active);
      return { checked: active.getAttribute(${JSON.stringify(expectedSelectedAttr)}), role: active.getAttribute('role'),
        visible: active.matches(':focus-visible'), outline: style.outlineStyle,
        width: parseFloat(style.outlineWidth), color: style.outlineColor };
    })()`,
    );
    if (
      focus.role !== expectedTabRole ||
      focus.checked !== "true" ||
      !focus.visible ||
      focus.outline === "none" ||
      focus.width < 1 ||
      focus.color === "rgba(0, 0, 0, 0)"
    )
      throw new Error(`${key} has no visible selected-${expectedTabRole} focus: ${JSON.stringify(focus)}`);
  }
  await waitForDom(
    send,
    `document.querySelector(${JSON.stringify(OVERVIEW)})?.textContent.includes(${JSON.stringify(LONG_VALUES[1])})`,
    "About restored after keyboard selection",
  );
  await settleOverview(send);
  const tabStops = await evaluate(
    send,
    `(() => {
    const aside = document.querySelector(${JSON.stringify(OVERVIEW)});
    const controls = Array.from(aside.querySelectorAll('button, a[href], input, textarea, [tabindex]'))
      .filter(e => e.tabIndex >= 0 && !e.matches(':disabled') && e.getBoundingClientRect().width > 0 && getComputedStyle(e).visibility === 'visible');
    if (controls.length < 2) throw new Error('Overview has fewer than two tab stops');
    window.__overviewEnds = [controls[0], controls[controls.length - 1]];
    controls[0].focus();
    return controls.length;
  })()`,
  );
  if (viewport.mobile) {
    await pressKey(send, "Tab", "Tab", 9, 8);
    await waitForDom(
      send,
      "document.activeElement === window.__overviewEnds[1]",
      "phone Shift+Tab wraps first to last",
    );
    await pressKey(send, "Tab", "Tab", 9);
    await waitForDom(send, "document.activeElement === window.__overviewEnds[0]", "phone Tab wraps last to first");
    for (const modifiers of [0, 8]) {
      for (let i = 0; i < tabStops + 1; i++) {
        await pressKey(send, "Tab", "Tab", 9, modifiers);
        if (
          !(await evaluate(
            send,
            `document.querySelector(${JSON.stringify(OVERVIEW)}).contains(document.activeElement)`,
          ))
        )
          throw new Error(`phone ${modifiers ? "Shift+Tab" : "Tab"} escaped Overview at step ${i}`);
      }
    }
  } else {
    await evaluate(send, "window.__overviewEnds[1].focus()");
    let left = false;
    for (let i = 0; i < tabStops + 2; i++) {
      await pressKey(send, "Tab", "Tab", 9);
      if (
        !(await evaluate(send, `document.querySelector(${JSON.stringify(OVERVIEW)}).contains(document.activeElement)`))
      ) {
        left = true;
        break;
      }
    }
    if (!left) throw new Error("desktop Overview trapped Tab");
  }

  const dismiss = async (gesture, expectedPane, expectedButton = null) => {
    if (gesture === "close") await clickControl(send, `${OVERVIEW} button[aria-label="Close Overview"]`);
    else {
      await evaluate(
        send,
        `document.querySelector(${JSON.stringify(OVERVIEW)}).querySelector(${JSON.stringify(OVERVIEW_SELECTED_TAB)}).focus()`,
      );
      await pressKey(send, "Escape", "Escape", 27);
    }
    await waitForDom(
      send,
      `!document.querySelector(${JSON.stringify(OVERVIEW)})`,
      `${gesture} completes Overview exit`,
    );
    await waitForDom(
      send,
      `(() => {
      const active = document.activeElement;
      const marker = active.querySelector?.('[data-session-actions-ref="local:p0-s0"]');
      const style = getComputedStyle(active), box = active.getBoundingClientRect();
      return active.isConnected && box.width > 0 && box.height > 0 && style.visibility === 'visible' &&
        box.left >= -1 && box.right <= innerWidth + 1 && box.top >= -1 && box.bottom <= innerHeight + 1 &&
        (!${viewport.mobile} || marker?.dataset.paneId === ${JSON.stringify(expectedPane)} ||
          (${!!expectedButton} && active === window.__overviewExpectedButton)) &&
        (${expectedButton ? `active === window.__overviewExpectedButton` : "true"});
    })()`,
      `${gesture} returns to rendered original session control`,
    );
  };
  // The rail opener is removed when a phone's Sessions drawer closes.
  await dismiss("close", pane);
  const inspection = await choosePaneOverview(send, overviewTrigger(pane), viewport.mobile, true);
  await clickControl(send, overviewTabSelector('[aria-label="About"]'));
  await evaluate(
    send,
    `void (window.__overviewExpectedButton = document.querySelector(${JSON.stringify(overviewTrigger(pane))}).closest('button'))`,
  );
  await dismiss("escape", pane, true);

  if (viewport.width === MOBILE_VIEWPORT.width && childGesture === "close") {
    await evaluate(send, `(() => {
      const button = window.__overviewExpectedButton;
      button.blur();
      // Keep the real click, but suppress Chromium's default mousedown focus.
      button.addEventListener('mousedown', event => event.preventDefault(), { once: true });
      button.addEventListener('click', event => {
        window.__overviewUnfocusedClick = event.isTrusted && document.activeElement !== button;
      }, { once: true });
    })()`);
    await clickControl(send, overviewTrigger(pane));
    await waitForDom(send, `document.querySelector(${JSON.stringify(OVERVIEW)})`, "pointer opens phone Overview");
    await settleOverview(send);
    if (!(await evaluate(send, "window.__overviewUnfocusedClick")))
      throw new Error("phone Overview pointer fixture did not activate without default focus");
    await pressKey(send, "Escape", "Escape", 27);
    await waitForDom(send, `!document.querySelector(${JSON.stringify(OVERVIEW)})`, "Escape dismisses phone Overview");
    await waitForDom(
      send,
      "document.activeElement === window.__overviewExpectedButton",
      "phone Overview returns focus to its header after an unfocused pointer activation",
    );
  }

  const submitStatus = async (wrongFocusedPane = null) => {
    const editor =
      '[data-pane-scaffold="session:local:p0-s0"] ~ [data-testid="pane-footer"] [role="textbox"][aria-label="Message"]';
    await clickControl(send, editor);
    await evaluate(send, `void (window.__overviewExpectedButton = document.querySelector(${JSON.stringify(editor)}))`);
    if (wrongFocusedPane) {
      await evaluate(send, `window.overviewPane('focus', ${JSON.stringify(wrongFocusedPane)})`);
      await waitForDom(
        send,
        `window.overviewGuardState().focusedPaneId === ${JSON.stringify(wrongFocusedPane)} && document.activeElement === document.querySelector(${JSON.stringify(editor)})`,
        "other workspace pane focused with original composer active",
      );
    }
    await send("Input.insertText", { text: "/status" });
    await waitForDom(
      send,
      `document.querySelector(${JSON.stringify(editor)}).textContent.includes('/status')`,
      "real composer received status",
    );
    await pressKey(send, "Enter", "Enter", 13);
    await pressKey(send, "Enter", "Enter", 13, 2);
    await waitForDom(
      send,
      `window.overviewGuardState().overview.open && window.overviewGuardState().overview.tab === 'about' && window.overviewGuardState().overview.ref === 'local:p0-s0' && document.querySelector(${JSON.stringify(OVERVIEW)})?.textContent.includes(${JSON.stringify(LONG_VALUES[1])})`,
      "trusted status opens hydrated About for its own session",
    );
    await settleOverview(send);
    const state = await evaluate(send, "window.overviewGuardState()");
    if (
      state.panes.some((p) => p.type === "sessionDetails") ||
      state.calls.some((c) => c.method === "turn/start" || c.method === "thread/resume")
    )
      throw new Error("status opened Details or requested a provider turn");
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
      await choosePaneOverview(send, overviewTrigger(origin));
      await clickControl(send, overviewTabSelector('[aria-label="About"]'));
      await evaluate(
        send,
        `void (window.__overviewExpectedButton = document.querySelector(${JSON.stringify(overviewTrigger(origin))}))`,
      );
      await dismiss("escape", origin, true);
    }
    await choosePaneOverview(send, overviewTrigger(duplicate));
    await clickControl(send, overviewTabSelector('[aria-label="About"]'));
    await evaluate(send, `window.overviewPane('close', ${JSON.stringify(duplicate)})`);
    await settleOverview(send);
    await evaluate(
      send,
      `void (window.__overviewExpectedButton = document.querySelector(${JSON.stringify(trigger(pane))}).closest('button'))`,
    );
    await dismiss("close", pane, true);
  }
  if (viewport.mobile) {
    if (!(await evaluate(send, "window.overviewGuardState().overview.open")))
      await choosePaneOverview(send, overviewTrigger(pane), true);
    await clickControl(send, overviewTabSelector('[aria-label^="Agents"]'));
    await waitForDom(
      send,
      `Array.from(document.querySelectorAll(${JSON.stringify(`${OVERVIEW} button`)})).some(e => e.textContent.includes('Open Overview child'))`,
      "real child delegate row",
    );
    await evaluate(
      send,
      `(() => {
      const row = Array.from(document.querySelectorAll(${JSON.stringify(`${OVERVIEW} button`)})).find(e => e.textContent.includes('Open Overview child'));
      row.dataset.overviewGuardChild = '';
    })()`,
    );
    await clickControl(send, "[data-overview-guard-child]");
    try {
      await waitForDom(
        send,
        `window.overviewGuardState().overview.ref === 'local:overview-child' && !document.querySelector(${JSON.stringify(trigger(pane))})`,
        "child conversation takes phone focus",
      );
    } catch (error) {
      const witness = await evaluate(
        send,
        `(() => {
        const state = window.overviewGuardState();
        return { ...state, calls: state.calls.slice(-12), errors: window.__shellGuardErrors || [] };
      })()`,
      );
      throw new Error(`${error.message} child-open witness: ${JSON.stringify(witness)}`);
    }
    await settleOverview(send);
    await clickControl(send, overviewTabSelector('[aria-label="About"]'));
    await waitForDom(
      send,
      `document.querySelector(${JSON.stringify(OVERVIEW)})?.textContent.includes(${JSON.stringify(LONG_VALUES[1])})`,
      "child About hydrated",
    );
    // The read-only pane's ancestor breadcrumb is gone: the revealed child
    // conversation owns the return focus through its own actions control.
    const childPaneId = await evaluate(send, "window.overviewGuardState().focusedPaneId");
    const childActions = overviewTrigger(childPaneId, "local:overview-child");
    await evaluate(
      send,
      `void (window.__overviewExpectedButton = document.querySelector(${JSON.stringify(childActions)}).closest('button'))`,
    );
    await waitForDom(
      send,
      `window.__overviewExpectedButton?.isConnected === true && !window.__overviewExpectedButton.closest(${JSON.stringify(OVERVIEW)})`,
      "revealed child conversation owns its actions control",
    );
    const before = await evaluate(send, "window.overviewGuardState()");
    await dismiss(childGesture, pane, true);
    const after = await evaluate(send, "window.overviewGuardState()");
    if (after.focusedPaneId !== before.focusedPaneId || JSON.stringify(after.panes) !== JSON.stringify(before.panes))
      throw new Error("child dismissal navigated or changed panes");
    await clickControl(send, `button[aria-label="Back"]`);
    await waitForDom(
      send,
      `window.overviewGuardState().focusedPaneId === ${JSON.stringify(pane)} && document.querySelector(${JSON.stringify(trigger(pane))})`,
      "Back returns to the parent conversation from the revealed child",
    );
    await settleOverview(send);
  }
  return { tabStops, status: true, phoneTrap: !!viewport.mobile, childReturn: childGesture, originalPane: pane, inspection };
}

async function measureActivityRowPadding(send, viewport) {
  const outer = await evaluate(
    send,
    `(() => {
    const overviewScrollContainers = ${overviewScrollContainers.toString()};
    const aside = document.querySelector(${JSON.stringify(OVERVIEW)});
    const body = overviewScrollContainers(aside).find((el) => el.tagName === "DIV");
    if (!body) throw new Error('Overview scroll viewport missing');
    const style = getComputedStyle(body);
    return [style.paddingTop, style.paddingRight, style.paddingBottom, style.paddingLeft];
  })()`,
  );
  await evaluate(send, "window.activityRowFixture(true)");
  try {
    await waitForDom(
      send,
      `[...document.querySelectorAll('[data-row-case]')].length === 6 && !!document.querySelector('[data-row-case="task"] [data-testid="task-check"]')`,
      "production activity-row fixtures hydrated",
    );
    await waitForFonts(send);
    const rows = await evaluate(
      send,
      `Array.from(document.querySelectorAll('[data-row-case]')).map(container => {
      const kind = container.dataset.rowCase;
      const row = kind === 'watch' || kind === 'task' ? container.querySelector('summary') : container.firstElementChild;
      // The watch row's own box is the ListRow inside its disclosure summary;
      // the summary contributes no inset, but the glyph inset is measured from
      // the summary so it counts both boxes.
      const rowBox = kind === 'watch' ? row.firstElementChild : row;
      let glyph = row.firstElementChild;
      let nested = null;
      if (kind === 'task') {
        glyph = row.querySelector('[data-testid="task-check"]');
      } else if (kind === 'watch') {
        glyph = row.querySelector('[data-testid^="sidebar-watch-"]').parentElement;
        // The row box now lives on the ListRow inside the summary; the summary
        // itself must stay unpadded so the effective inset is the row's alone.
        nested = getComputedStyle(row);
      }
      const style = getComputedStyle(rowBox), box = row.getBoundingClientRect();
      const time = row.querySelector('time[data-activity-time]');
      const title = kind === 'task'
        ? time?.parentElement?.querySelector('[data-struck], [class*="rowName"], [class*="description"]')
        : row.querySelector('[title]');
      const timeBox = time?.getBoundingClientRect();
      const titleBox = title?.getBoundingClientRect();
      const titleClip = title && getComputedStyle(title).textOverflow === 'ellipsis' ? title : title?.parentElement;
      const titleClipBox = titleClip?.getBoundingClientRect();
      if (!time || !title || !titleClipBox) throw new Error(kind + ' row lacks a measurable title line or timestamp');
      return { kind, inset: glyph.getBoundingClientRect().left - box.left,
        padding: [style.paddingTop, style.paddingRight, style.paddingBottom, style.paddingLeft],
        nestedPadding: nested ? [nested.paddingTop, nested.paddingRight, nested.paddingBottom, nested.paddingLeft] : null,
        width: box.width, height: box.height,
        time: {
          dateTime: time.dateTime,
          meaning: time.dataset.meaning,
          top: timeBox.top,
          bottom: timeBox.bottom,
          left: timeBox.left,
          right: timeBox.right,
          titleTop: titleBox.top,
          titleBottom: titleBox.bottom,
          titleClipLeft: titleClipBox.left,
          titleClipRight: titleClipBox.right,
          titleClipWidth: titleClipBox.width,
          titleFullWidth: titleBox.width,
          rowLeft: box.left,
          rowRight: box.right,
        } };
    })`,
    );
    const failures = [];
    if (JSON.stringify(outer) !== JSON.stringify(["8px", "12px", "12px", "12px"]))
      failures.push(`Overview outer padding changed: ${JSON.stringify(outer)}`);
    const rhythm = await evaluate(
      send,
      `(() => {
        const stack = document.querySelector('[data-activity-row-fixture] > div');
        const tasks = document.querySelector('[data-row-case="task"] ul');
        if (!stack || !tasks) throw new Error('Activity/task list rhythm fixture missing');
        return { activity: getComputedStyle(stack).rowGap, tasks: getComputedStyle(tasks).rowGap };
      })()`,
    );
    if (rhythm.activity !== "4px" || rhythm.tasks !== rhythm.activity)
      failures.push(`Activity and task list gaps differ: ${JSON.stringify(rhythm)}`);
    for (const row of rows) {
      if (
        Math.abs(row.inset - 8) > 0.1 ||
        JSON.stringify(row.padding) !== JSON.stringify(["4px", "8px", "4px", "8px"])
      ) {
        failures.push(`${row.kind} row must have 8px glyph inset and 4px/8px padding: ${JSON.stringify(row)}`);
      }
      if (row.nestedPadding?.some((padding) => padding !== "0px"))
        failures.push(`watch summary adds padding: ${JSON.stringify(row.nestedPadding)}`);
      const { time } = row;
      const centerDelta = Math.abs((time.top + time.bottom) / 2 - (time.titleTop + time.titleBottom) / 2);
      if (centerDelta > 2.5) failures.push(`${row.kind} timestamp is not on the title line: ${JSON.stringify(row)}`);
      if (time.left < time.titleClipRight - 0.5 || time.left < time.rowLeft || time.right > time.rowRight)
        failures.push(`${row.kind} timestamp overlaps its title or escapes the row: ${JSON.stringify(row)}`);
      if (time.titleClipWidth < 24 || time.titleClipWidth <= time.right - time.left)
        failures.push(`${row.kind} timestamp leaves too little visible title: ${JSON.stringify(row)}`);
      if (time.titleFullWidth < 12) failures.push(`${row.kind} title has no readable text: ${JSON.stringify(row)}`);
      if (viewport.mobile && !row.kind.endsWith("passive") && (row.width < 44 || row.height < 44))
        failures.push(`${row.kind} touch target below 44px: ${JSON.stringify(row)}`);
    }
    await clickControl(send, '[data-row-case="agent-clickable"] button');
    await waitForDom(
      send,
      `document.querySelector('[data-activity-row-fixture]').dataset.activated === 'agent'`,
      "agent drill callback",
    );
    await evaluate(send, `document.querySelector('[data-row-case="job-clickable"] button').focus()`);
    await pressKey(send, "Enter", "Enter", 13);
    await waitForDom(
      send,
      `document.querySelector('[data-activity-row-fixture]').dataset.activated === 'job'`,
      "job keyboard activation",
    );
    for (const kind of ["watch", "task"]) {
      await clickControl(send, `[data-row-case="${kind}"] summary`);
      await waitForDom(
        send,
        `!!document.querySelector('[data-row-case="${kind}"] details[open]')`,
        `${kind} disclosure opens`,
      );
    }
    return { result: { outer, rhythm, rows }, failures };
  } finally {
    await evaluate(send, "window.activityRowFixture(false)");
  }
}

// Browser regressions may prepare real page input without replacing the flow.
export async function overviewOnPage(cdpEndpoint, vitePort, viewport, theme, childGesture, preparePage) {
  const browser = await connectBrowser(cdpEndpoint);
  let browserContextId;
  let page;
  try {
    // Clearing a shared origin races with outgoing fixtures' pagehide saves.
    // Each journey needs a genuinely fresh reader, not a saved closed child.
    ({ browserContextId } = (await browser.send("Target.createBrowserContext", { disposeOnDetach: true })).result);
    const { targetId } = (await browser.send("Target.createTarget", { url: "about:blank", browserContextId })).result;
    page = await connectPage(cdpEndpoint, targetId);
    const { send } = page;
    await applyViewport(send, viewport);
    await navigateTo(page, `http://127.0.0.1:${vitePort}/shellguard.html`, BOOT);
    await evaluate(send, "window.settledShell");
    await evaluate(send, `window.configureOverview(${JSON.stringify(theme)})`);
    await settleOverview(send);
    await openRailOverview(send);
    const fixture = preparePage && (await preparePage(send));
    const result = await measureOverview(send);
    if (process.env.EVENER_SCRATCH_DIR) {
      await evaluate(
        send,
        `(() => {
        const overviewScrollContainers = ${overviewScrollContainers.toString()};
        const aside = document.querySelector(${JSON.stringify(OVERVIEW)});
        for (const element of overviewScrollContainers(aside)) element.scrollTop = 0;
        const tablist = aside.querySelector('[role="tablist"]');
        if (tablist) tablist.scrollLeft = 0;
      })()`,
      );
      const screenshot = await send("Page.captureScreenshot", { format: "png" });
      await writeFile(
        path.join(process.env.EVENER_SCRATCH_DIR, `overview-${viewport.width}-${theme}.png`),
        Buffer.from(screenshot.result.data, "base64"),
      );
    }
    const failures = assertOverview(result, viewport, theme);
    result.tasksTab = await measureOverviewTasks(send);
    failures.push(...assertOverviewTasks(result.tasksTab, viewport));
    const padding = await measureActivityRowPadding(send, viewport);
    result.rowPadding = padding.result;
    failures.push(...padding.failures);
    try {
      result.keyboard = await trustedOverviewFlow(send, viewport, childGesture);
    } catch (error) {
      const witness = await evaluate(
        send,
        `(() => {
        const state = window.overviewGuardState();
        return { ...state, calls: state.calls.slice(-12), errors: window.__shellGuardErrors || [],
          mountedOverview: !!document.querySelector(${JSON.stringify(OVERVIEW)}) };
      })()`,
      );
      failures.push(`trusted interaction: ${error.message}; child witness: ${JSON.stringify(witness)}`);
    }
    await fixture?.afterInteraction;
    // The initial geometry snapshot predates trusted input. Read this same
    // page again even when an interaction failed, and report each event once.
    result.errors = await evaluate(send, "window.__shellGuardErrors || []");
    failures.push(...result.errors.map((error) => `page error: ${error}`));
    return { result, failures };
  } finally {
    try {
      if (page) {
        try {
          await clearViewportOverride(page.send);
        } finally {
          page.close();
        }
      }
    } finally {
      try {
        if (browserContextId) await browser.send("Target.disposeBrowserContext", { browserContextId });
      } finally {
        browser.close();
      }
    }
  }
}

async function main() {
  let guard;
  try {
    guard = await startBrowserGuard({
      frontend: FRONTEND,
      profilePrefix: "shellguard-chrome-",
      viteConfigFile: "scripts/shellguard/settingsGeometry.vite.config.mjs",
      // Headless hosts may have no pointer hardware. These Chromium settings
      // supply a hover-capable mouse, touch emulation still owns phone cases.
      chromeArgs: [
        "--blink-settings=availableHoverTypes=2,primaryHoverType=2,availablePointerTypes=4,primaryPointerType=4",
      ],
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
    // The measurements share this tab, named by id: Chrome keeps listing a tab
    // for a moment after /json/close, so "the first listed page" can be the
    // click-geometry tab just closed, whose socket fails or never answers (#3895).
    const sharedTab = await openPage(cdpEndpoint, "about:blank");
    console.log(`shellguard click geometry: ${JSON.stringify(await checkMovingControls(cdpEndpoint, clickControl))}`);
    const result = await measureOnPage(
      cdpEndpoint,
      sharedTab.id,
      vitePort,
      VIEWPORT,
      "(async () => { await window.applyShellNavigationDelta(); const renders = window.measureRailRenderCounts(); return JSON.stringify({ ...window.measureShell(), paneFooters: window.measurePaneFooters(), counts: renders.counts, changedRowID: renders.changedRowID, visibleRowIDs: renders.visibleRowIDs }); })()",
    );
    const dockResize = await measureOnPage(cdpEndpoint, sharedTab.id, vitePort, VIEWPORT, measureDockResize);
    const floatingDock = await measureOnPage(cdpEndpoint, sharedTab.id, vitePort, VIEWPORT, measureFloatingDock);
    // Both mobile measurements come from ONE page load of the emulated phone:
    // the sidebar geometry and the tap-floor audit need the same context.
    const mobile = await measureOnPage(
      cdpEndpoint,
      sharedTab.id,
      vitePort,
      MOBILE_VIEWPORT,
      "JSON.stringify({ sidebar: window.measureMobileSidebar(), tap: window.measureTapTargets() })",
    );
    // The header at the rail's own width floor on a coarse pointer. measureOnPage
    // already fails the whole case when the row overflows the rail, so this
    // measurement's job is to put the rail AT that floor and report what the
    // row and the wordmark did there.
    const touchMin = await measureOnPage(
      cdpEndpoint,
      sharedTab.id,
      vitePort,
      TOUCH_DESKTOP_VIEWPORT,
      `JSON.stringify((() => {
        const clamp = document.querySelector('[data-testid="rail-resize-handle"]');
        const rail = document.querySelector('[data-testid="rail"]');
        const wordmark = document.querySelector('[data-testid="rail-wordmark"]');
        const min = clamp ? Number(clamp.getAttribute("aria-valuemin")) : null;
        if (min === null || !rail) return { min, applied: false };
        rail.style.setProperty("--rail-width", min + "px");
        const rb = rail.getBoundingClientRect();
        const buttons = [...document.querySelectorAll('[data-testid="rail-brand"] button')];
        return {
          min,
          applied: true,
          coarse: matchMedia("(any-pointer: coarse)").matches,
          railWidth: rb.width,
          railRight: rb.right,
          lastButtonRight: buttons.length ? buttons[buttons.length - 1].getBoundingClientRect().right : null,
          wordmarkClipped: wordmark ? wordmark.scrollWidth > wordmark.clientWidth + 1 : null,
        };
      })())`,
    );
    // The focus-scroll guard (#6838): its own page load, because the fixture
    // scrolls the rail and inserts a block above the trigger - neither belongs
    // in the page the page-height measurement above reads.
    const focusScroll = await measureOnPage(cdpEndpoint, sharedTab.id, vitePort, VIEWPORT, measureFocusScroll);
    const focusScrollFailures = assertFocusScroll(focusScroll);
    const tabletSplit = await measureOnPage(cdpEndpoint, sharedTab.id, vitePort, TABLET_VIEWPORT, measureTabletSplit);
    const tabletSplitFailures = assertTabletSplit(tabletSplit);
    const { pointer, narrowest, widest, beforeArrowUp, arrowUpRows, largerText, roomy, desktopBefore, desktopAfter, roomyFits, paneScrollCapture, paneScrollCollapsed, paneScrollReopened, paneScrollPhone } = tabletSplit;
    console.log(`shellguard tablet usability: ${JSON.stringify({ pointer, min: narrowest.board.width, minToggleHit: narrowest.toggleHit,
      max: widest.board.width, pane: widest.pane.width, find: widest.find, beforeArrowUp, arrowUpRows,
      paneScroll: { capture: paneScrollCapture, collapsed: paneScrollCollapsed, reopened: paneScrollReopened, phone: paneScrollPhone },
      largerText: largerText.map(([font, { find, pane }]) => ({ font, find, pane: pane.width })),
      savedWidth: { desktopBefore, roomy: roomy.board.width, desktopAfter, fittingTablet: roomyFits.board.width } })}`);
    if (focusScrollFailures.length === 0) {
      console.log(
        `shellguard focus scroll: PASS - dismissed to rail row ${focusScroll.triggerRef}'s menu trigger ` +
          `with the row's actions reveal held by the fixture and the rail body's scrollTop unchanged at ` +
          `${focusScroll.pushed.scrollTopBefore}`,
      );
    }
    if (tabletSplitFailures.length === 0) {
      console.log(
        `shellguard tablet split: PASS - board y=${tabletSplit.open.board.top}..${tabletSplit.open.board.bottom} ` +
          `w=${tabletSplit.open.board.width}px (resized=${tabletSplit.resized?.board?.width}px), ` +
          `topbar x=${tabletSplit.open.topbar.left}, handle x=${tabletSplit.open.resizeHandle.left}..${tabletSplit.open.resizeHandle.right}, ` +
          `open toggle in rail-brand x=${tabletSplit.open.toggle.left}, collapsed toggle in topbar x=${tabletSplit.collapsed.toggle.left} ` +
          `glyph=${tabletSplit.collapsed.glyphTransform}`,
      );
    }
    const failures = [
      ...assertResult(result),
      ...assertDeltaRenders(result),
      ...assertPaneFooters(result.paneFooters),
      ...assertDockResize(dockResize),
      ...assertFloatingDock(floatingDock),
      ...assertMobileResult(mobile.sidebar),
      ...assertTapTargets(mobile.tap),
      ...assertTouchRailMinimum(touchMin),
      ...focusScrollFailures,
      ...tabletSplitFailures,
    ];
    for (const viewport of OVERVIEW_VIEWPORTS) {
      for (const theme of OVERVIEW_THEMES) {
        for (const childGesture of viewport.mobile ? ["close", "escape"] : [null]) {
          const label = `Overview ${viewport.width}px ${theme} XL${childGesture ? ` child ${childGesture}` : ""}`;
          try {
            const overview = await overviewOnPage(cdpEndpoint, vitePort, viewport, theme, childGesture);
            failures.push(...overview.failures.map((failure) => `${label}: ${failure}`));
            console.log(`${label}: ${JSON.stringify(overview.result)}`);
          } catch (error) {
            failures.push(`${label}: ${error.message}`);
          }
        }
      }
    }
    const delayedFloatingDock = await measureDelayedFloatingDock(
      cdpEndpoint,
      `http://127.0.0.1:${vitePort}/shellguard.html`,
      BOOT,
      VIEWPORT,
      measureFloatingDock,
    );
    failures.push(...assertFloatingDock(delayedFloatingDock));
    console.log(`shellguard delayed workspace: ${JSON.stringify(delayedFloatingDock)}`);
    failures.push(...(await checkSessionHoverCards(cdpEndpoint, vitePort)));
    failures.push(...(await checkSettingsGeometry(cdpEndpoint, vitePort)));
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
      console.log(
        `shellguard Dockview resize: ${JSON.stringify(dockResize)}; floating: ${JSON.stringify(floatingDock)}`,
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
