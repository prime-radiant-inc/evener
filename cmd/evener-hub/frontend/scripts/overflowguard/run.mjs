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
import {
  applyViewport,
  clearViewportOverride,
  connectPage,
  createStartupDeadline,
  devtoolsHttpURL,
  evaluate,
  navigateTo,
  realizedViewport,
  waitForFonts,
  waitForHttp,
} from "../browserGuardCdp.mjs";
import { describeBrowserStartupFailure, startBrowserGuard } from "../browserGuardProcess.mjs";

const FRONTEND = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..", "..");

// 390 is a phone; 1400 is a wide desktop pane, where the turn hits its 76rem
// reading measure and STOPS growing - which is exactly where a few px of
// escape at the right edge shows up. A width sweep that skipped the wide end
// would have missed the original bug entirely.
const DEFAULT_WIDTHS = [320, 390, 700, 899, 900, 1024, 1400];
const GEOMETRY_TOLERANCE = 0.5;

async function measureAt(cdpEndpoint, url, width) {
  const page = await connectPage(cdpEndpoint);
  const { send } = page;
  try {
    await applyViewport(send, { width, height: 900, mobile: width < 900 });
    await send(
      "Emulation.setTouchEmulationEnabled",
      width < 900 ? { enabled: true, maxTouchPoints: 1 } : { enabled: false },
    );
    await navigateTo(page, url);

    const host = await evaluate(send, "location.host");
    if (String(host).includes("9180")) throw new Error("refusing: this eval landed on the shared evener-hub port");
    const viewport = await realizedViewport(send);
    const realizedLayout = await evaluate(
      send,
      "JSON.stringify({ mobile: matchMedia('(max-width: 899px)').matches, viewportMeta: document.querySelector('meta[name=viewport]')?.content ?? null })",
    ).then((json) => JSON.parse(json));
    const expectedMobile = width < 900;
    const realizedWidths = [
      ["window.innerWidth", viewport.windowInnerWidth],
      ["document.documentElement.clientWidth", viewport.documentClientWidth],
      ["window.visualViewport.width", viewport.visualViewportWidth],
    ];
    const wrongWidth = realizedWidths.find(([, actual]) => actual !== width);
    if (wrongWidth) {
      throw new Error(
        `${width}px realized viewport mismatch: ${wrongWidth[0]}=${wrongWidth[1]}, expected ${width}; ` +
          `meta=${JSON.stringify(realizedLayout.viewportMeta)}`,
      );
    }
    if (realizedLayout.mobile !== expectedMobile) {
      throw new Error(
        `${width}px realized layout mode mismatch: mobile=${realizedLayout.mobile}, expected ${expectedMobile}; ` +
          `innerWidth=${viewport.windowInnerWidth}, meta=${JSON.stringify(realizedLayout.viewportMeta)}`,
      );
    }

    // Delegate cards update from layout effects and the virtualizer measures
    // rows post-mount, so await the real tree settling before measurement.
    await evaluate(send, "window.settled");
    // After the origin refusal above (it must precede every other eval) and
    // after the tree settles, because document.fonts.ready re-arms for each new
    // face and only a mounted tree has asked for the ones being measured.
    await waitForFonts(send);

    const exceptionSafety = await evaluate(
      send,
      `(() => {
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
    );

    let measurementViewport = viewport;
    if (!realizedLayout.mobile) {
      await applyViewport(send, { width, height: 360, mobile: false });
      await waitForDynamicViewport(send);
      measurementViewport = await realizedViewport(send);
    }
    let detail = null;
    if (!url.includes("settings=1")) {
      try {
        detail = await evaluate(send, "window.inspectDetail()\n");
      } catch (error) {
        // Request the new named measurements before allowing an unchanged
        // harness fixture failure to obscure the intended RED boundary.
        const legacyMeasurement = JSON.parse(await evaluate(send, "JSON.stringify(window.measure())"));
        if (!Array.isArray(legacyMeasurement.editors)) {
          return { ...legacyMeasurement, exceptionSafety, detail: null, viewport: { ...measurementViewport, mobile: realizedLayout.mobile } };
        }
        throw error;
      }
    }
    const focus = detail ? await measureTrustedFocus(send) : null;
    const finalMeasurement = JSON.parse(await evaluate(send, "JSON.stringify(window.measure())"));
    return {
      ...finalMeasurement,
      exceptionSafety,
      detail,
      focus,
      viewport: { ...measurementViewport, mobile: realizedLayout.mobile },
    };
  } finally {
    await send("Emulation.setTouchEmulationEnabled", { enabled: false }).catch(() => {});
    await clearViewportOverride(send);
    page.close();
  }
}

async function waitForDynamicViewport(send) {
  await evaluate(
    send,
    `new Promise((resolve, reject) => {
      const probe = document.createElement('div');
      probe.style.cssText = 'position:fixed;visibility:hidden;pointer-events:none;top:0;left:0;height:100dvh;width:1px';
      document.body.append(probe);
      let previous = null;
      let stable = 0;
      let frames = 0;
      const check = () => {
        const current = probe.getBoundingClientRect().height;
        if (current === window.innerHeight && current === previous) stable++;
        else stable = 0;
        if (stable >= 2) {
          probe.remove();
          resolve(current);
          return;
        }
        if (++frames >= 120) {
          probe.remove();
          reject(new Error('dynamic viewport height did not settle: probe=' + current + ', innerHeight=' + window.innerHeight));
          return;
        }
        previous = current;
        requestAnimationFrame(check);
      };
      requestAnimationFrame(check);
    })`,
  );
}

async function dispatchTrustedKey(send, key, code, windowsVirtualKeyCode) {
  await send("Input.dispatchKeyEvent", { type: "keyDown", key, code, windowsVirtualKeyCode });
  await send("Input.dispatchKeyEvent", { type: "keyUp", key, code, windowsVirtualKeyCode });
  await evaluate(send, "new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)))");
}

async function measureTrustedFocus(send) {
  await evaluate(
    send,
    `(() => {
      const owner = document.querySelector('[data-testid="transcript-detail-control"]');
      const editor = owner?.querySelector('section[aria-label="Transcript detail editor"]');
      const segments = editor ? [...editor.querySelectorAll('[role="radio"]')] : [];
      segments[0]?.focus();
      if (!editor || segments.length !== 6) throw new Error('live Verbosity focus fixture is incomplete');
    })()`,
  );
  const readState = async () =>
    evaluate(
      send,
      `(() => {
        const owner = document.querySelector('[data-testid="transcript-detail-control"]');
        const editor = owner?.querySelector('section[aria-label="Transcript detail editor"]');
        const segments = editor ? [...editor.querySelectorAll('[role="radio"]')] : [];
        const track = editor?.querySelector('[role="radiogroup"]');
        const trackBox = track?.getBoundingClientRect();
        const labels = segments.map((segment) => segment.querySelector('span')?.textContent?.trim() ?? segment.getAttribute('aria-label') ?? '');
        const geometry = segments.map((segment) => {
          const box = segment.getBoundingClientRect();
          return { left: box.left, right: box.right, top: box.top, bottom: box.bottom, width: box.width, height: box.height, checked: segment.getAttribute('aria-checked') === 'true' };
        });
        const active = document.activeElement;
        const activeIndex = segments.indexOf(active);
        const focused = activeIndex >= 0 ? segments[activeIndex] : null;
        const box = focused?.getBoundingClientRect();
        const style = focused ? getComputedStyle(focused) : null;
        const outlineWidth = style ? Number.parseFloat(style.outlineWidth) || 0 : 0;
        const outlineOffset = style ? Number.parseFloat(style.outlineOffset) || 0 : 0;
        const inset = outlineWidth + outlineOffset;
        const painted = box ? { left: box.left - inset, right: box.right + inset, top: box.top - inset, bottom: box.bottom + inset } : null;
        let clipped = painted === null || painted.left < 0 || painted.right > window.innerWidth || painted.top < 0 || painted.bottom > window.innerHeight;
        const clippingAncestors = [];
        for (let ancestor = focused?.parentElement; ancestor && ancestor !== document.body; ancestor = ancestor.parentElement) {
          const ancestorStyle = getComputedStyle(ancestor);
          const clipsX = ancestorStyle.overflowX !== 'visible';
          const clipsY = ancestorStyle.overflowY !== 'visible';
          if (clipsX || clipsY) {
            const ancestorBox = ancestor.getBoundingClientRect();
            const clipLeft = ancestorBox.left + ancestor.clientLeft;
            const clipTop = ancestorBox.top + ancestor.clientTop;
            const clipRight = clipLeft + ancestor.clientWidth;
            const clipBottom = clipTop + ancestor.clientHeight;
            const clippedX = clipsX && (painted.left < clipLeft || painted.right > clipRight);
            const clippedY = clipsY && (painted.top < clipTop || painted.bottom > clipBottom);
            clippingAncestors.push({
              tag: ancestor.tagName.toLowerCase(),
              testId: ancestor.dataset.testid ?? null,
              overflowX: ancestorStyle.overflowX,
              overflowY: ancestorStyle.overflowY,
              clip: { left: clipLeft, right: clipRight, top: clipTop, bottom: clipBottom },
              clippedX,
              clippedY,
            });
            clipped = clipped || clippedX || clippedY;
          }
        }
        return {
          ownerTestId: owner?.dataset.testid ?? null,
          labels,
          activeLabel: activeIndex >= 0 ? labels[activeIndex] : null,
          activeElementIsSegment: activeIndex >= 0,
          focusVisible: focused?.matches(':focus-visible') ?? false,
          outlineStyle: style?.outlineStyle ?? "none",
          outlineWidth,
          outlineOffset,
          painted,
          clipped,
          clippingAncestors,
          checkedLabels: segments.filter((segment) => segment.getAttribute('aria-checked') === 'true').map((segment) => segment.querySelector('span')?.textContent?.trim() ?? ''),
          track: trackBox ? { left: trackBox.left, right: trackBox.right, width: trackBox.width, clientWidth: track.clientWidth, scrollWidth: track.scrollWidth } : null,
          geometry,
        };
      })()`,
    );
  await dispatchTrustedKey(send, "Home", "Home", 36);
  const baseline = await readState();
  const first = await readState();
  for (let index = 0; index < 3; index++) await dispatchTrustedKey(send, "ArrowRight", "ArrowRight", 39);
  const middle = await readState();
  await dispatchTrustedKey(send, "End", "End", 35);
  const last = await readState();
  return { baseline, states: [first, middle, last] };
}

// Unified session menu (2026-08-05-unified-session-context-menu): the chrome
// no longer has inline Details/Tasks/Activity triggers or a narrow-collapse -
// the shared SessionMenu ("Session actions") lists the panes at EVERY width
// with a check adornment for open ones. This fixture therefore drives the
// menu directly: open Tasks, wait for the dock split to squeeze the main
// composer's inline session chrome below 640px, then re-open the menu and
// confirm "Tasks ✓".
async function verifyPanelCollapse(cdpEndpoint, url) {
  const page = await connectPage(cdpEndpoint);
  const { send } = page;
  try {
    await applyViewport(send, { width: 1024, height: 900 });
    await navigateTo(page, url);
    await waitForFonts(page.send);
    const runtimeState = await send("Runtime.evaluate", {
      expression: `({ body: document.body.innerText, html: document.body.innerHTML.slice(0, 1000), errors: window.__panelGuardErrors ?? [] })`,
      returnByValue: true,
    });
    const out = await send("Runtime.evaluate", {
      expression: `(async () => {
        const until = async (read, label) => {
          for (let i = 0; i < 180; i++) {
            const value = read();
            if (value) return value;
            await new Promise((resolve) => requestAnimationFrame(resolve));
          }
          throw new Error('panel collapse fixture did not settle: ' + label + '; body=' + document.body.innerText.slice(0, 500));
        };
        const actionsTrigger = () =>
          [...document.querySelectorAll('button')].find((button) => button.textContent?.includes('Session actions'));
        const actions = await until(actionsTrigger, 'session actions trigger');
        actions.click();
        const tasksItem = await until(
          () => [...document.querySelectorAll('[role="menuitem"]')].find((item) => item.textContent === 'Tasks'),
          'tasks menu item',
        );
        tasksItem.click();
        const panel = await until(() => document.querySelector('[data-pane-scaffold="session-panel:tasks:overflowharness"]'), 'tasks pane');
        const chrome = await until(
          () => document.querySelector('[data-testid="session-chrome-inline"]'),
          'inline session chrome',
        );
        await until(() => chrome.clientWidth > 0 && chrome.clientWidth < 640, 'narrowed chrome');
        // The dock-collapse probe uses the same production Advanced editor so
        // its named container-width/fieldset-column invariant is exercised at
        // the narrow main-pane width as well.
        const detail = await window.inspectDetail(true);
        const verbosityDialog = document.querySelector('[role="dialog"][aria-modal="true"]');
        const closeVerbosity = verbosityDialog?.querySelector('button[aria-label="Close"]');
        if (!closeVerbosity) throw new Error('Verbosity close button missing after dock-collapse measurement');
        closeVerbosity.click();
        await until(() => !document.querySelector('[role="dialog"][aria-modal="true"]'), 'Verbosity dialog close');
        const actionsAgain = actionsTrigger();
        if (!actionsAgain) throw new Error('session actions trigger missing');
        actionsAgain.click();
        const checked = await until(() => [...document.querySelectorAll('[role="menuitem"]')].find((item) => item.textContent?.includes('Tasks ✓')), 'checked menu item');
        const pane = document.getElementById('oh-pane');
        const horizontallyOverflowing = [...pane.querySelectorAll('*')].filter((element) => {
          const style = getComputedStyle(element);
          return element.clientWidth > 1 && element.scrollWidth > element.clientWidth + 1 &&
            (style.overflowX === 'auto' || style.overflowX === 'scroll');
        });
        return {
          mainWidth: chrome.clientWidth,
          panelVisible: panel.getBoundingClientRect().width > 0,
          checkedText: checked.textContent,
          horizontalOverflowCount: horizontallyOverflowing.length,
          detail,
        };
      })()`,
      awaitPromise: true,
      returnByValue: true,
    });
    if (out.result.exceptionDetails) {
      throw new Error(
        `panel-collapse eval threw: ${JSON.stringify(out.result.exceptionDetails)} runtime=${JSON.stringify(runtimeState.result.result.value)}`,
      );
    }
    return out.result.result.value;
  } finally {
    await clearViewportOverride(send);
    page.close();
  }
}

async function verifyShortSessionMenu(cdpEndpoint, url) {
  const page = await connectPage(cdpEndpoint);
  const { send } = page;
  try {
    await applyViewport(send, { width: 844, height: 390, mobile: true });
    await send("Emulation.setTouchEmulationEnabled", { enabled: true, maxTouchPoints: 1 });
    await navigateTo(page, url);
    await evaluate(send, "window.settled");
    await waitForFonts(send);
    await waitForDynamicViewport(send);
    const initial = await evaluate(
      send,
      `(async () => {
        const trigger = [...document.querySelectorAll('button')].find((button) =>
          button.textContent?.includes('Session actions'),
        );
        if (!trigger) throw new Error('short-height Session actions trigger is missing');
        trigger.scrollIntoView({ block: 'nearest', inline: 'nearest' });
        await new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)));
        trigger.click();
        await new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)));
        const animations = document.getAnimations().filter(
          (animation) => animation.effect?.getTiming().iterations !== Number.POSITIVE_INFINITY,
        );
        await Promise.all(animations.map((animation) => animation.finished.catch(() => undefined)));
        const menu = [...document.querySelectorAll('[role="menu"]')].find(
          (candidate) => candidate.getAttribute('aria-labelledby') === trigger.id,
        );
        if (!menu) throw new Error('short-height Session menu is missing');
        const box = menu.getBoundingClientRect();
        return {
          itemCount: menu.querySelectorAll('[role="menuitem"]').length,
          top: box.top,
          bottom: box.bottom,
          clientHeight: menu.clientHeight,
          scrollHeight: menu.scrollHeight,
          overflowY: getComputedStyle(menu).overflowY,
          viewportWidth: window.innerWidth,
          viewportHeight: window.innerHeight,
        };
      })()`,
    );
    await dispatchTrustedKey(send, "End", "End", 35);
    const focused = await evaluate(
      send,
      `(() => {
        const trigger = [...document.querySelectorAll('button')].find((button) =>
          button.textContent?.includes('Session actions'),
        );
        const menu = [...document.querySelectorAll('[role="menu"]')].find(
          (candidate) => candidate.getAttribute('aria-labelledby') === trigger?.id,
        );
        if (!menu) throw new Error('short-height Session menu disappeared');
        const items = [...menu.querySelectorAll('[role="menuitem"]')].filter(
          (item) => item.getAttribute('aria-disabled') !== 'true',
        );
        const last = items.at(-1);
        if (!last) throw new Error('short-height Session menu has no enabled item');
        const menuBox = menu.getBoundingClientRect();
        const lastBox = last.getBoundingClientRect();
        const center = { x: (lastBox.left + lastBox.right) / 2, y: (lastBox.top + lastBox.bottom) / 2 };
        const hit = document.elementFromPoint(center.x, center.y);
        return {
          activeIsLast: document.activeElement === last,
          lastVisible: lastBox.top >= menuBox.top - 0.5 && lastBox.bottom <= menuBox.bottom + 0.5,
          lastHitTestable: hit === last || (hit instanceof Node && last.contains(hit)),
          scrollTop: menu.scrollTop,
        };
      })()`,
    );
    return { ...initial, ...focused };
  } finally {
    await send("Emulation.setTouchEmulationEnabled", { enabled: false }).catch(() => {});
    await clearViewportOverride(send);
    page.close();
  }
}

async function verifyChatFocus(cdpEndpoint, url) {
  const page = await connectPage(cdpEndpoint);
  const { send } = page;
  try {
    await applyViewport(send, { width: 1024, height: 900, mobile: false });
    await navigateTo(page, url);
    await evaluate(send, "window.settled");
    await waitForFonts(send);
    return await evaluate(send, "window.inspectChatFocus()");
  } finally {
    await clearViewportOverride(send);
    page.close();
  }
}

function assertFieldsets(detail, label) {
  const failures = [];
  if (!detail || !Number.isFinite(detail.rootRemPx) || detail.rootRemPx <= 0) {
    return [`${label} fieldset root rem measurement is missing`];
  }
  if (detail.fieldsetsFound !== 3) {
    return [`${label} Advanced rendered ${detail.fieldsetsFound ?? "unknown"} fieldsets, expected 3`];
  }
  const expectedColumns = detail.editorContainerWidth <= 34 * detail.rootRemPx ? 1 : 3;
  if (detail.fieldsetColumns !== expectedColumns) {
    failures.push(
      `${label} editor container is ${detail.editorContainerWidth}px (${detail.rootRemPx}px root rem), ` +
        `so fieldsets must use ${expectedColumns} column(s), found ${detail.fieldsetColumns ?? "unknown"}`,
    );
  }
  if (!detail.fieldsetsNonOverlapping) failures.push(`${label} fieldsets overlap`);
  if (expectedColumns === 1 && !detail.fieldsetStacked) failures.push(`${label} one-column fieldsets are not vertically stacked`);
  return failures;
}

function assertDetail(result, width) {
  const failures = [];
  failures.push(...assertEditors(result, width, "live"));
  const expectedFocusLabels = ["Chat", "Activity", "Custom"];
  const expectedSegmentLabels = ["Chat", "Intent", "Tools", "Activity", "Full", "Custom"];
  const focusStates = result.focus?.states;
  if (!result.focus?.baseline || !Array.isArray(focusStates) || focusStates.length !== expectedFocusLabels.length) {
    failures.push(`live editor trusted focus measurements found ${focusStates?.length ?? "none"}, expected first/middle/last`);
  } else {
    const baseline = result.focus.baseline;
    for (const [index, focus] of focusStates.entries()) {
      if (focus.ownerTestId !== "transcript-detail-control") {
        failures.push(`trusted focus owner is ${JSON.stringify(focus.ownerTestId)}, expected transcript-detail-control`);
      }
      if (JSON.stringify(focus.labels) !== JSON.stringify(expectedSegmentLabels)) {
        failures.push(`trusted focus labels are ${JSON.stringify(focus.labels)}, expected ${JSON.stringify(expectedSegmentLabels)}`);
      }
      if (focus.activeLabel !== expectedFocusLabels[index] || !focus.activeElementIsSegment) {
        failures.push(`trusted keyboard focus reached ${JSON.stringify(focus.activeLabel)}, expected ${expectedFocusLabels[index]}`);
      }
      if (!focus.focusVisible || focus.outlineWidth <= 0) {
        failures.push(`trusted focus for ${expectedFocusLabels[index]} has no positive :focus-visible outline: ${JSON.stringify(focus)}`);
      }
      if (focus.outlineStyle === "none" || focus.outlineOffset <= 0) {
        failures.push(`trusted focus for ${expectedFocusLabels[index]} has no painted outline style/offset: ${JSON.stringify(focus)}`);
      }
      const bounds = focus.painted;
      if (focus.clipped || !bounds || bounds.left < -GEOMETRY_TOLERANCE || bounds.right > (result.viewport?.windowInnerWidth ?? 0) + GEOMETRY_TOLERANCE) {
        failures.push(`painted focus outline for ${JSON.stringify(expectedFocusLabels[index])} is clipped: ${JSON.stringify(focus)}`);
      }
      if (JSON.stringify(focus.checkedLabels) !== JSON.stringify([expectedFocusLabels[index]])) {
        failures.push(`trusted selection is ${JSON.stringify(focus.checkedLabels)}, expected ${expectedFocusLabels[index]}`);
      }
      if (JSON.stringify(focus.labels) !== JSON.stringify(baseline.labels)) failures.push("segment labels changed during keyboard focus transitions");
      if (!focus.track || !baseline.track || !nearlyEqual(focus.track.left, baseline.track.left) || !nearlyEqual(focus.track.right, baseline.track.right) || !nearlyEqual(focus.track.width, baseline.track.width) || focus.track.clientWidth !== baseline.track.clientWidth || focus.track.scrollWidth !== baseline.track.scrollWidth) {
        failures.push(
          `track geometry changed during trusted focus transition to ${expectedFocusLabels[index]}: ` +
            `baseline=${JSON.stringify(baseline.track)}, current=${JSON.stringify(focus.track)}`,
        );
      }
      if (focus.geometry.length !== baseline.geometry.length || focus.geometry.some((segment, segmentIndex) => {
        const before = baseline.geometry[segmentIndex];
        return !before || !nearlyEqual(segment.left, before.left) || !nearlyEqual(segment.right, before.right) || !nearlyEqual(segment.top, before.top) || !nearlyEqual(segment.bottom, before.bottom) || !nearlyEqual(segment.width, before.width) || !nearlyEqual(segment.height, before.height);
      })) {
        failures.push(
          `segment geometry changed during trusted focus transition to ${expectedFocusLabels[index]}: ` +
            `baseline=${JSON.stringify(baseline.geometry)}, current=${JSON.stringify(focus.geometry)}`,
        );
      }
    }
  }
  if (!Array.isArray(result.scrollContainers) || result.scrollContainers.length === 0) {
    failures.push("live Verbosity scroll-container ancestor measurements are missing");
  }
  const overflowingAncestors = (result.scrollContainers ?? []).filter(
    (container) => container.scrollWidth > container.clientWidth + GEOMETRY_TOLERANCE,
  );
  if (overflowingAncestors.length > 0) {
    failures.push(`actual scroll container(s) overflow horizontally: ${JSON.stringify(overflowingAncestors)}`);
  }
  const detail = result.detail;
  if (!detail?.found || !detail.triggerReachable || !detail.triggerHitTestable)
    failures.push("Session actions trigger is not reachable or is occluded at its center hit point");
  if (!detail?.open) failures.push("Verbosity did not open its production Dialog/Sheet");
  if (!detail?.overlayContained) failures.push(`Verbosity Dialog/Sheet escapes the viewport: ${JSON.stringify(detail?.panel)}`);
  if ((detail?.horizontalOverflowCount ?? 1) !== 0) {
    failures.push(
      `Verbosity has ${detail?.horizontalOverflowCount ?? "unknown"} horizontal overflow element(s): ${JSON.stringify(detail?.overflowElements)}`,
    );
  }
  const mobile = result.viewport?.mobile === true;
  if (detail?.mobile !== mobile) failures.push(`Verbosity layout mode is ${detail?.mobile}, realized viewport mode is ${mobile}`);
  failures.push(...assertFieldsets(detail, `${width}px Verbosity`));
  const expectedTargets = 31;
  if ((detail?.targets?.length ?? 0) !== expectedTargets) {
    failures.push(`Verbosity mounted ${detail?.targets?.length ?? "unknown"} interactive targets, expected ${expectedTargets}`);
  }
  if (!(detail?.targets ?? []).some((target) => target.kind === "menuitem" && target.label === "Verbosity…")) {
    failures.push("Verbosity menu item target measurement is missing");
  }
  if (!(detail?.targets ?? []).some((target) => target.kind === "summary" && target.label.startsWith("Customize & advanced"))) {
    failures.push("Verbosity Advanced summary target measurement is missing");
  }
  const switchLabels = detail?.targets?.filter((target) => target.kind === "switch-label") ?? [];
  if (switchLabels.length !== 9) failures.push(`Verbosity mounted ${switchLabels.length} Switch label targets, expected 9`);
  if (!detail?.dialogCentered && !mobile) {
    failures.push(
      `Desktop Verbosity Dialog is not centered in the layout viewport: ${JSON.stringify({ panel: detail?.panel, viewport: result.viewport })}`,
    );
  }
  if (!detail?.sheetBottomAnchored && mobile) failures.push("Mobile Verbosity Sheet is not bottom-anchored");
  if (!result.trigger) failures.push("Session actions trigger geometry is missing");
  const overlayScroll = detail?.overlayScroll;
  if (
    (!overlayScroll?.connected ||
      !overlayScroll.contained ||
      !overlayScroll.scrollable ||
      overlayScroll.scrollHeight <= overlayScroll.clientHeight ||
      overlayScroll.afterTop <= overlayScroll.beforeTop)
  ) {
    failures.push(`Verbosity Dialog/Sheet failed internal-scroll containment: ${JSON.stringify(overlayScroll)}`);
  }
  if (mobile) {
    const undersized = (detail?.targets ?? []).filter((target) => target.height < 44 - 0.5);
    const undersizedEffective = (detail?.effectiveTargets ?? []).filter((target) => target.height < 44 - 0.5);
    if (undersized.length > 0) {
      failures.push(
        `Mobile Verbosity target(s) are below 44px: ${undersized.map((target) => `${target.kind}:${JSON.stringify(target.label)}=${target.height}`).join(", ")}`,
      );
    }
    if (undersizedEffective.length > 0) {
      failures.push(
        `Mobile Verbosity effective target(s) are below 44px: ${undersizedEffective.map((target) => `${target.kind}:${JSON.stringify(target.label)}=${target.height}`).join(", ")}`,
      );
    }
  }
  return failures;
}

function nearlyEqual(actual, expected, tolerance = GEOMETRY_TOLERANCE) {
  return Math.abs(actual - expected) <= tolerance;
}

function assertEditors(result, width, surface) {
  const failures = [];
  const editors = result.editors;
  let expectedOwners;
  let expectedLayouts;
  if (surface === "settings") {
    expectedOwners = new Set(["transcript-display-card-desktop", "transcript-display-card-mobile"]);
    expectedLayouts = new Map([
      ["transcript-display-card-desktop", "desktop"],
      ["transcript-display-card-mobile", "mobile"],
    ]);
  } else {
    expectedOwners = new Set(["transcript-detail-control"]);
    expectedLayouts = new Map([["transcript-detail-control", width < 900 ? "mobile" : "desktop"]]);
  }
  if (!Array.isArray(editors)) return [`${surface} editor measurements are missing`];
  if (editors.length !== expectedOwners.size) {
    failures.push(`${surface} editor measurement count is ${editors.length}, expected ${expectedOwners.size}`);
  }
  if (new Set(editors.map((editor) => editor.ownerTestId)).size !== expectedOwners.size) {
    failures.push(`${surface} editor owners are not unique: ${JSON.stringify(editors.map((editor) => editor.ownerTestId))}`);
  }
  for (const editor of editors) {
    if (!expectedOwners.has(editor.ownerTestId)) {
      failures.push(`${surface} editor has unexpected owner ${JSON.stringify(editor.ownerTestId)}`);
    }
    if (editor.surface !== surface) failures.push(`${surface} editor ${editor.ownerTestId} reports surface ${editor.surface}`);
    if (editor.layout !== expectedLayouts.get(editor.ownerTestId)) {
      failures.push(`${surface} editor ${editor.ownerTestId} reports layout ${editor.layout}, expected ${expectedLayouts.get(editor.ownerTestId)}`);
    }
    const track = editor.track;
    if (!track || !nearlyEqual(track.width, track.right - track.left)) {
      failures.push(`${surface} editor ${editor.ownerTestId} has inconsistent track geometry`);
      continue;
    }
    if (track.scrollWidth > track.clientWidth + GEOMETRY_TOLERANCE) {
      failures.push(
        `${surface} editor ${editor.ownerTestId} track scrolls horizontally: ${track.scrollWidth}px in ${track.clientWidth}px`,
      );
    }
    const expectedLabels = ["Chat", "Intent", "Tools", "Activity", "Full", "Custom"];
    if (!Array.isArray(editor.segments) || editor.segments.length !== expectedLabels.length) {
      failures.push(`${surface} editor ${editor.ownerTestId} has ${editor.segments?.length ?? "unknown"} segments, expected 6`);
      continue;
    }
    const checked = editor.segments.filter((segment) => segment.checked);
    if (checked.length !== 1) failures.push(`${surface} editor ${editor.ownerTestId} has ${checked.length} selected segments, expected 1`);
    const segmentWidth = (track.width - 16) / expectedLabels.length;
    const rowTop = editor.segments[0]?.top;
    const rowBottom = editor.segments[0]?.bottom;
    let firstGap;
    for (const [index, segment] of editor.segments.entries()) {
      if (segment.label !== expectedLabels[index]) {
        failures.push(`${surface} editor ${editor.ownerTestId} segment ${index} is ${JSON.stringify(segment.label)}`);
      }
      if (index > 0) {
        const previous = editor.segments[index - 1];
        const gap = previous ? segment.localLeft - previous.localRight : Number.NaN;
        if (firstGap === undefined) firstGap = gap;
        if (
          !previous ||
          segment.localLeft < previous.localRight - GEOMETRY_TOLERANCE ||
          Math.abs(gap - firstGap) > GEOMETRY_TOLERANCE
        ) {
          failures.push(`${surface} editor ${editor.ownerTestId} segments are not monotonic with stable gaps`);
        }
      }
      if (!Number.isFinite(segment.localLeft) || !Number.isFinite(segment.localRight)) {
        failures.push(`${surface} editor ${editor.ownerTestId} segment ${JSON.stringify(segment.label)} has no local geometry`);
      }
      if (!nearlyEqual(segment.width, segment.right - segment.left) || !nearlyEqual(segment.width, segmentWidth)) {
        failures.push(`${surface} editor ${editor.ownerTestId} segment ${JSON.stringify(segment.label)} has unstable width ${segment.width}px`);
      }
      if (!nearlyEqual(segment.top, rowTop) || !nearlyEqual(segment.bottom, rowBottom)) {
        failures.push(`${surface} editor ${editor.ownerTestId} segments do not share one row: ${segment.label}`);
      }
      if (
        segment.left < track.left - GEOMETRY_TOLERANCE ||
        segment.right > track.right + GEOMETRY_TOLERANCE ||
        segment.localLeft < -GEOMETRY_TOLERANCE ||
        segment.localRight > track.width + GEOMETRY_TOLERANCE
      ) {
        failures.push(`${surface} editor ${editor.ownerTestId} segment ${JSON.stringify(segment.label)} escapes its track`);
      }
      if (index === editor.segments.length - 1 && segment.localRight > track.width + GEOMETRY_TOLERANCE) {
        failures.push(
          `${surface} editor ${editor.ownerTestId} rightmost segment local edge ${segment.localRight.toFixed(1)}px exceeds ${track.width.toFixed(1)}px track`,
        );
      }
      const requiresTouchTarget = surface === "settings" ? width < 900 : editor.layout === "mobile";
      if (requiresTouchTarget && segment.height < 44 - GEOMETRY_TOLERANCE) {
        failures.push(`${surface} editor ${editor.ownerTestId} segment ${JSON.stringify(segment.label)} is below 44px: ${segment.height}px`);
      }
    }
  }
  if (surface === "settings" && (width === 320 || width === 390)) {
    const expectedTrack = width === 320 ? 256 : 326;
    const expectedSegment = width === 320 ? 40 : 51.667;
    for (const editor of editors) {
      if (!nearlyEqual(editor.track?.width ?? Number.NaN, expectedTrack)) {
        failures.push(`${editor.ownerTestId} track is ${editor.track?.width ?? "unknown"}px, expected ${expectedTrack}px at ${width}px`);
      }
      for (const segment of editor.segments ?? []) {
        if (!nearlyEqual(segment.width, expectedSegment)) {
          failures.push(`${editor.ownerTestId} ${segment.label} segment is ${segment.width}px, expected ${expectedSegment}px at ${width}px`);
        }
      }
    }
  }
  return failures;
}

function assertCanvases(result, width) {
  const failures = [];
  const expectedCanvasLayouts = new Map([
    ["transcript-display-preview-canvas-desktop", "desktop"],
    ["transcript-display-preview-canvas-mobile", "mobile"],
  ]);
  if (!Array.isArray(result.canvases) || result.canvases.length !== expectedCanvasLayouts.size) {
    return [`Settings canvas measurements found ${result.canvases?.length ?? "none"}, expected 2`];
  }
  if (new Set(result.canvases.map((canvas) => canvas.testId)).size !== expectedCanvasLayouts.size) {
    failures.push(`Settings canvas owners are not unique: ${JSON.stringify(result.canvases.map((canvas) => canvas.testId))}`);
  }
  const mobile = result.canvases.find((canvas) => canvas.testId === "transcript-display-preview-canvas-mobile");
  if (!mobile) failures.push("mobile preview canvas measurement is missing");
  for (const canvas of result.canvases) {
    if (canvas.layout !== expectedCanvasLayouts.get(canvas.testId)) {
      failures.push(`${canvas.testId} reports layout ${canvas.layout}, expected ${expectedCanvasLayouts.get(canvas.testId)}`);
    }
    if (canvas.width < 0 || canvas.availableWidth < 0) failures.push(`${canvas.testId} has negative available geometry`);
    if (canvas.scrollWidth > canvas.clientWidth + GEOMETRY_TOLERANCE || canvas.scrollHeight > canvas.clientHeight + GEOMETRY_TOLERANCE) {
      failures.push(`${canvas.testId} has inner scroll: ${canvas.scrollWidth}/${canvas.clientWidth}x${canvas.scrollHeight}/${canvas.clientHeight}`);
    }
  }
  if (mobile) {
    const expected = Math.min(390, mobile.availableWidth);
    if (!nearlyEqual(mobile.width, expected)) {
      failures.push(`${mobile.testId} is ${mobile.width}px, expected min(390px, ${mobile.availableWidth}px) at ${width}px`);
    }
    if (width === 320 && !nearlyEqual(mobile.width, 256)) failures.push(`320px mobile preview is ${mobile.width}px, expected 256px`);
    if (width === 390 && !nearlyEqual(mobile.width, 326)) failures.push(`390px mobile preview is ${mobile.width}px, expected 326px`);
  }
  return failures;
}

function assertSettings(result, width) {
  const failures = [];
  if (result.cardsFound !== 2) failures.push(`expected two production Settings cards, found ${result.cardsFound}`);
  if (result.previewsFound !== 2) failures.push(`expected two production Settings previews, found ${result.previewsFound}`);
  if (!result.cardsStacked) failures.push("Settings Desktop/Mobile cards are not stacked");
  if (result.cardOverflowCount !== 0) failures.push(`Settings card overflow count is ${result.cardOverflowCount}`);
  if (result.previewOverflowCount !== 0) failures.push(`Settings preview overflow count is ${result.previewOverflowCount}`);
  if (result.previewInnerScrollCount !== 0) {
    failures.push(`Settings previews contain ${result.previewInnerScrollCount} inner scroll element(s)`);
  }
  if (!Array.isArray(result.scrollContainers) || result.scrollContainers.length === 0) {
    failures.push("Settings scroll-container ancestor measurements are missing");
  }
  const overflowingAncestors = (result.scrollContainers ?? []).filter(
    (container) => container.scrollWidth > container.clientWidth + GEOMETRY_TOLERANCE,
  );
  if (overflowingAncestors.length > 0) {
    failures.push(`Settings actual scroll container(s) overflow horizontally: ${JSON.stringify(overflowingAncestors)}`);
  }
  failures.push(...assertEditors(result, width, "settings"));
  failures.push(...assertCanvases(result, width));
  return failures;
}

async function main() {
  const widths = process.argv.slice(2).map(Number).filter(Boolean);
  const sweep = widths.length > 0 ? widths : DEFAULT_WIDTHS;

  let guard;
  try {
    guard = await startBrowserGuard({
      frontend: FRONTEND,
      profilePrefix: "overflowguard-chrome-",
      chromeArgs: ["--window-size=1800,1000"],
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
      await waitForHttp(
        `http://127.0.0.1:${vitePort}/overflowharness.html`,
        "vite dev server",
        guard.getViteLaunchError,
      );
    } catch (err) {
      throw new Error(
        describeBrowserStartupFailure({ error: err, subsystem: "vite", viteStderr: guard.getViteError() }),
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
    } catch (err) {
      throw new Error(
        describeBrowserStartupFailure({
          error: err,
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

    const shortMenu = await verifyShortSessionMenu(
      cdpEndpoint,
      `http://127.0.0.1:${vitePort}/overflowharness.html?w=844`,
    );
    const shortMenuFailures = [];
    if (shortMenu.viewportWidth !== 844 || shortMenu.viewportHeight !== 390) {
      shortMenuFailures.push(`realized viewport=${shortMenu.viewportWidth}x${shortMenu.viewportHeight}`);
    }
    if (shortMenu.itemCount !== 9) shortMenuFailures.push(`items=${shortMenu.itemCount}, expected 9`);
    if (shortMenu.top < 8 - GEOMETRY_TOLERANCE || shortMenu.bottom > 390 - 8 + GEOMETRY_TOLERANCE) {
      shortMenuFailures.push(`bounds=${shortMenu.top}-${shortMenu.bottom}, expected within 8-382`);
    }
    if (
      shortMenu.scrollHeight <= shortMenu.clientHeight ||
      (shortMenu.overflowY !== "auto" && shortMenu.overflowY !== "scroll")
    ) {
      shortMenuFailures.push(
        `vertical scroll=${shortMenu.scrollHeight}/${shortMenu.clientHeight}, overflow-y=${shortMenu.overflowY}`,
      );
    }
    if (!shortMenu.activeIsLast || !shortMenu.lastVisible || !shortMenu.lastHitTestable || shortMenu.scrollTop <= 0) {
      shortMenuFailures.push(
        `End reachability=${JSON.stringify({
          activeIsLast: shortMenu.activeIsLast,
          lastVisible: shortMenu.lastVisible,
          lastHitTestable: shortMenu.lastHitTestable,
          scrollTop: shortMenu.scrollTop,
        })}`,
      );
    }
    if (shortMenuFailures.length > 0) {
      failed++;
      console.log(`short mobile menu ... FAIL - ${shortMenuFailures.join("; ")}`);
    } else {
      console.log("short mobile menu ... PASS - popup contained, scrollable, and last action keyboard/touch reachable");
    }

    const chatFocus = await verifyChatFocus(
      cdpEndpoint,
      `http://127.0.0.1:${vitePort}/overflowharness.html?w=1024`,
    );
    if (
      !chatFocus.toolsRowFocused ||
      !chatFocus.groupFound ||
      chatFocus.groupOpen ||
      !chatFocus.summaryIsActive ||
      chatFocus.rationaleIsActive ||
      !chatFocus.summaryVisible
    ) {
      failed++;
      console.log(`Chat focus transition ... FAIL - ${JSON.stringify(chatFocus)}`);
    } else {
      console.log("Chat focus transition ... PASS - closed action summary visibly owns focus");
    }

    const panelCollapse = await verifyPanelCollapse(
      cdpEndpoint,
      `http://127.0.0.1:${vitePort}/overflowharness.html?w=1024&panels=1`,
    );
    const panelFieldsetFailures = assertFieldsets(panelCollapse.detail, "1024 narrow dock");
    if (
      panelCollapse.mainWidth >= 640 ||
      !panelCollapse.panelVisible ||
      panelCollapse.checkedText !== "Tasks ✓" ||
      panelCollapse.horizontalOverflowCount !== 0 ||
      !panelCollapse.detail?.triggerReachable ||
      !panelCollapse.detail?.triggerHitTestable ||
      !panelCollapse.detail?.open ||
      panelCollapse.detail.horizontalOverflowCount !== 0 ||
      !panelCollapse.detail.overlayContained ||
      panelFieldsetFailures.length > 0
    ) {
      failed++;
      console.log(`panel collapse ... FAIL - ${JSON.stringify(panelCollapse)}`);
    } else {
      console.log(
        `panel collapse ... PASS - ${panelCollapse.mainWidth}px main pane, checked Tasks adornment visible, ` +
          `reachable Verbosity Dialog, no horizontal overflow`,
      );
    }

    for (const width of sweep) {
      const result = await measureAt(
        cdpEndpoint,
        `http://127.0.0.1:${vitePort}/overflowharness.html?w=${width}`,
        width,
      );
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
      // The footer checks below are only worth what the predicate behind them
      // is worth, so the predicate is exercised against its own fixture first
      // (kata bsq9). A fact under a display:none ancestor must read as missing;
      // an intentionally visually-hidden one must not.
      // One expectation per clause of the shared predicate
      // (src/dev/guardVisibility.ts). spawnguard uses the same function and has
      // no fixture of its own, so this is the only place either guard proves
      // what "visible" means.
      const probe = result.visibility;
      const expected = {
        rendered: true,
        ancestorHidden: false,
        visuallyHidden: true,
        visibilityHiddenAncestor: false,
        zeroArea: false,
      };
      const wrong = Object.entries(expected).filter(([name, want]) => probe[name] !== want);
      if (wrong.length > 0) {
        widthFailed = true;
        console.log(
          `${width}px ... FAIL - the shared visible() predicate behind the footer checks is broken: ` +
            wrong.map(([name, want]) => `${name}=${probe[name]} (expected ${want})`).join(", "),
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
        console.log(
          `${width}px ... FAIL - pressured footer queue label is ${JSON.stringify(result.footer.queueLabel)}`,
        );
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
        console.log(
          `${width}px ... FAIL - pressured footer model has zero visible width: ` +
            JSON.stringify(result.footer.geometry),
        );
      }
      if (
        width === 390 &&
        (!result.subagentCard.found ||
          !result.subagentCard.contained ||
          !result.subagentCard.quoteWrapped ||
          !result.subagentCard.statsContained)
      ) {
        widthFailed = true;
        console.log(`${width}px ... FAIL - narrow long-token subagent card: ${JSON.stringify(result.subagentCard)}`);
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

      const settings = await measureAt(
        cdpEndpoint,
        `http://127.0.0.1:${vitePort}/overflowharness.html?w=${width}&settings=1`,
        width,
      );
      const settingsFailures = assertSettings(settings, width);
      if (settingsFailures.length > 0) {
        failed++;
        console.log(`${width}px Settings ... FAIL - ${settingsFailures.join("; ")}`);
      } else {
        console.log(`${width}px Settings ... PASS - cards stack and previews have no inner scroll`);
      }

      const detailFailures = assertDetail(result, width);
      if (detailFailures.length > 0) {
        failed++;
        console.log(`${width}px Verbosity ... FAIL - ${detailFailures.join("; ")}`);
      } else {
        console.log(
          `${width}px Verbosity ... PASS - Session actions reachable, ${result.detail.mobile ? "Sheet" : "Dialog"} contained, ` +
            `no horizontal scroll${result.detail.mobile ? ", 44px targets" : ""}; ` +
            `final panel=${JSON.stringify(result.detail.panel)}, model=${result.footer.modelClientWidth}px` +
            `, root rem=${result.detail.rootRemPx}px, editor=${result.detail.editorContainerWidth}px/${result.detail.fieldsetColumns} fieldset columns` +
            `${result.detail.mobile ? "" : `, internal scroll=${result.detail.overlayScroll.afterTop}/${result.detail.overlayScroll.scrollHeight} in ${result.detail.overlayScroll.clientHeight}px`}`,
        );
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
  (err) => {
    console.error(err.message);
    if (process.exitCode === undefined) process.exitCode = 2;
  },
);
